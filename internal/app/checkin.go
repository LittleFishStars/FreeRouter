package app

// checkin.go —— 定时签到领积分（2026-10-31）。
//
// 参考实现：dsh-providers-hub 的 auto-checkin（每日首次启动自动签到）与
// credits.claimAll（领取骨架）。本文件只做**调度与落库**，领取本身的分发
// 在 sidecar（providers/credits.mjs 复用 hub 的 createProviderCredits 表）。
//
// ## 链条
//
//	CheckinLoop（调度：启动延迟 + 周期 tick + 到期判定）
//	  → checkinBridge.ClaimOne（解密凭据 → sidecar credits.claim → 落库/写回）
//	    → sidecar providers/credits.mjs claimCreditBalance（分发到 hub 各家 claim）
//	  → store UpdateCredentialCheckin / UpdatePlaintextByCredentialID（轮换写回）
//
// ## 从 hub auto-checkin 借来的三条语义
//
//  1. **UTC+8 日界用算术平移**（UTC8DateString）：各渠道每日额度按 UTC+8 结算，
//     用本机时区在时区变化时会漏签/重签（hub 同一条成训）。
//  2. **「能证明今天已处理」才记账**（coversToday 口径）：claimed /
//     already-claimed 才写 checkin_date；失败、活动未开启不写 —— 否则
//     「活动每日 10:00 才刷新」的渠道，上午那轮会被当成今天已处理，
//     当天新额度整天漏领（hub 2026-10-02 定位的真实缺陷）。
//  3. **不建第二份能力名单**：哪些渠道能签到由 sidecar 分发表「调用即判定」；
//     不支持的渠道（cline / raccoon / zcode / qoder…）第一次尝试即被记上
//     当天日期，之后不再重试（hub 同款原则：另抄一份必然漂移）。
//
// ## 与 hub 的差异（服务端化）
//
// hub 是「每日首次启动跑一轮」，宿主一天开关很多次；FreeRouter 是常驻服务，
// 可能连续运行数周 —— 改为**周期 tick + 按凭据到期判定**（date != today 且
// 退避期满才重试）：日界一过全部重新到期，当天中途新贡献的凭据也能被覆盖；
// 失败凭据按退避重试（30min），足以追上「10 点刷新」的活动又不会打爆上游。
//
// 开关默认**打开**（判据：只有显式 false 才算关）——与 hub 的
// 「自动签到默认保持打开」同款用户预期，settings 缺键即视为开。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// —— 常量 ——

const (
	// CheckinStartupDelay 启动后延迟再跑第一轮：一轮签到是「凭据数 × 预检+领取」
	// 次串行公网请求，启动瞬间打它会拖慢装配（hub auto-checkin 同值同理由）。
	CheckinStartupDelay = 30 * time.Second
	// checkinSupportedTTL 是签到能力清单的缓存时长（见 CheckinSupportedProviders）。
	checkinSupportedTTL = 5 * time.Minute
	// CheckinTickInterval 到点检查周期。日界由「date != today」判定而不是推算
	// 下一次时刻，周期只决定「发现到期」的延迟上限；10min 对签到粒度足够。
	CheckinTickInterval = 10 * time.Minute
	// CheckinRetryBackoff 失败退避：checkin_date 未覆盖今天的凭据，两次尝试的
	// 最小间隔。30min 意味着一天最多重试 48 次——足够追上「10 点刷新」的
	// 活动，也不会把失败渠道打成请求洪水。
	CheckinRetryBackoff = 30 * time.Minute
	// CheckinCallTimeout 单次领取上限：预检 + 领取两趟公网请求（还可能带内部
	// 重试），15s 默认值不够；与 04-sidecar.md 5.3 的 credits.claim 行一致。
	CheckinCallTimeout = 60 * time.Second
)

// settings 键（值是 JSON 原文，与 SettingsRepo 的约定一致）。
const (
	settingCheckinEnabled = "checkin_auto_enabled"
	settingCheckinLastRun = "checkin_last_run"
)

// CheckinRunRecord 是最近一轮签到的持久化摘要（settings 的 checkin_last_run）。
type CheckinRunRecord struct {
	// Date 是本轮**完成**的 UTC+8 日期。完成 = 轮里至少有一条结果能证明
	// 「今天已处理」（hub shouldMarkToday 口径）；整轮全失败不写——下轮重试。
	Date   string `json:"date"`
	At     int64  `json:"at"`
	Result string `json:"result"`
}

// UTC8DateString 取 UTC+8 的当天日期（YYYY-MM-DD）。
//
// ⚠️ 必须算术平移而不是 time.Local：日界归上游（每日额度按 UTC+8 结算），
// 本机时区在部署迁移/夏令时下会给出错的「今天」。
func UTC8DateString(now time.Time) string {
	return now.UTC().Add(8 * time.Hour).Format("2006-01-02")
}

// checkinDue 判定一条凭据现在该不该签（纯函数，单测直接覆盖）。
//
//	已覆盖今天（date == today）→ 不签；
//	从未尝试（at == 0）→ 签（新贡献的凭据当天就要领）；
//	其余 → 距上次尝试满退避期才签。
func checkinDue(row store.CheckinDueRow, today string, now time.Time) bool {
	if row.CheckinDate == today {
		return false
	}
	if row.CheckinAt <= 0 {
		return true
	}
	return now.Unix()-row.CheckinAt >= int64(CheckinRetryBackoff/time.Second)
}

// —— 调度循环 ——

// CheckinLoop 是定时签到的后台循环（BalanceLoop 的同型：装配层实例化并 Run）。
type CheckinLoop struct {
	exec *checkinBridge
	// enabled 读自动签到开关（装配层注入闭包；nil = 恒开）。
	enabled func(ctx context.Context) bool
	// delay / interval 可注入以便单测零等待；0 时用常量默认。
	delay    time.Duration
	interval time.Duration
	// busy 防重入：上一轮还没跑完时跳过本 tick（一轮最坏 60s × 凭据数，
	// 会跨越多个 tick）。
	busy atomic.Bool
	log  *slog.Logger
}

// NewCheckinLoop 建循环。exec 为 nil 时 Run 直接返回（sidecar 未装配）。
func NewCheckinLoop(exec *checkinBridge, enabled func(ctx context.Context) bool, log *slog.Logger) *CheckinLoop {
	if log == nil {
		log = slog.Default()
	}
	return &CheckinLoop{
		exec:     exec,
		enabled:  enabled,
		delay:    CheckinStartupDelay,
		interval: CheckinTickInterval,
		log:      log,
	}
}

// Run 阻塞运行直到 ctx 取消（go CheckinLoop.Run(bgCtx) 使用）。
func (l *CheckinLoop) Run(ctx context.Context) {
	if l == nil || l.exec == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(l.delay):
	}
	l.runIfDue(ctx)
	tick := time.NewTicker(l.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			l.runIfDue(ctx)
		}
	}
}

// runIfDue 做一轮「有到期凭据才执行」的检查。开关关闭 / 忙 / 无到期 → 空转返回。
func (l *CheckinLoop) runIfDue(ctx context.Context) {
	if !l.busy.CompareAndSwap(false, true) {
		return // 上一轮还没跑完，本轮跳过（防 tick 重入）
	}
	defer l.busy.Store(false)
	if l.enabled != nil && !l.enabled(ctx) {
		return
	}
	due, err := l.exec.DueTargets(ctx, time.Now())
	if err != nil {
		l.log.WarnContext(ctx, "定时签到取到期清单失败", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}
	res := l.exec.RunRound(ctx, due)
	l.log.InfoContext(ctx, "定时签到一轮完成",
		"attempted", res.Attempted, "claimed", res.Claimed,
		"already", res.AlreadyClaimed, "inactive", res.Inactive,
		"failed", res.Failed, "unsupported", res.Unsupported,
		"summary", res.Summary)
}

// —— 执行体（桥） ——

// checkinBridge 是「解密凭据 → sidecar 领取 → 落库/写回」的执行体，
// 同时实现 httpapi 的两个签到窄接口（手动单签 / 整轮+状态+开关）。
type checkinBridge struct {
	b       *adminBridge
	sidecar *dshprovider.Client
}

// CheckinTarget 是调度器交给执行体的最小输入（来自 ListCheckinCandidates）。
type CheckinTarget struct {
	ID       string
	Provider string
}

// DueTargets 取当前到期的凭据清单（到期判定见 checkinDue）。
func (cb *checkinBridge) DueTargets(ctx context.Context, now time.Time) ([]CheckinTarget, error) {
	rows, err := cb.b.repos.Models.ListCheckinCandidates(ctx)
	if err != nil {
		return nil, err
	}
	today := UTC8DateString(now)
	var out []CheckinTarget
	for _, row := range rows {
		if row.Provider == "" {
			continue // 无 provider 的行无法分发，跳过
		}
		if checkinDue(row, today, now) {
			out = append(out, CheckinTarget{ID: row.ID, Provider: row.Provider})
		}
	}
	return out, nil
}

// RunRound 串行执行一轮（单个失败不中断整体——hub claimAll 的设计语义）。
// 每条凭据的落库发生在 ClaimOne 内部；本函数只聚合摘要并把「完成的一轮」
// 写进 settings（供状态端点展示）。
func (cb *checkinBridge) RunRound(ctx context.Context, targets []CheckinTarget) *httpapi.CheckinRoundResult {
	res := &httpapi.CheckinRoundResult{Attempted: len(targets)}
	gains := map[string]float64{}
	for _, t := range targets {
		r, err := cb.ClaimOne(ctx, t.ID)
		if err != nil {
			res.Failed++
			continue
		}
		switch r.Kind {
		case "claimed":
			res.Claimed++
			if r.Credit != nil {
				unit := r.Unit
				if unit == "" {
					unit = "credit"
				}
				gains[unit] += *r.Credit
			}
		case "already-claimed":
			res.AlreadyClaimed++
		case "inactive":
			res.Inactive++
		default:
			res.Failed++
		}
		if r.Unsupported {
			res.Unsupported++
		}
	}
	res.Summary = describeRound(res, gains)
	// 「完成」的记账口径与 hub shouldMarkToday 一致：claimed/already 大于 0
	// 才写日期（全失败的一轮不写，下轮重试）；摘要与时刻无论如何都记，
	// 状态端点据此展示「上次尝试」。
	rec := CheckinRunRecord{
		At:     time.Now().UTC().Unix(),
		Result: res.Summary,
	}
	if res.Claimed+res.AlreadyClaimed > 0 {
		rec.Date = UTC8DateString(time.Now())
	}
	if err := cb.saveLastRun(ctx, rec); err != nil {
		slog.WarnContext(ctx, "签到轮次摘要写库失败", "err", err)
	}
	return res
}

// describeRound 把一轮结果拼成中文摘要（给日志与状态端点）。
func describeRound(res *httpapi.CheckinRoundResult, gains map[string]float64) string {
	var parts []string
	for _, unit := range []string{"credit", "token"} {
		v := gains[unit]
		if v == 0 {
			continue
		}
		label := "积分"
		if unit == "token" {
			label = "token"
		}
		parts = append(parts, fmt.Sprintf("+%s %s", formatAmount(v), label))
	}
	if res.Claimed > 0 {
		parts = append(parts, fmt.Sprintf("新领 %d", res.Claimed))
	}
	if res.AlreadyClaimed > 0 {
		parts = append(parts, fmt.Sprintf("已签 %d", res.AlreadyClaimed))
	}
	if res.Inactive > 0 {
		parts = append(parts, fmt.Sprintf("活动未开启 %d", res.Inactive))
	}
	if res.Failed > 0 {
		parts = append(parts, fmt.Sprintf("失败 %d", res.Failed))
	}
	if res.Unsupported > 0 {
		parts = append(parts, fmt.Sprintf("不支持 %d", res.Unsupported))
	}
	if len(parts) == 0 {
		return "本轮没有可签到的凭据"
	}
	return strings.Join(parts, "，")
}

// formatAmount 数字转展示串：整数不带小数点，其余保留最多两位有效小数。
func formatAmount(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// ClaimOne 对单条凭据执行签到并落库。返回给 API 的结果**不含凭据明文**：
// 轮换出的新凭据直接写回密文，与余额刷新的静默续期同一条通道。
//
// 上游拒绝/网络失败**不是 error**（可重试的运行时条件）：返回 Kind=failed
// 的结果；只有凭据不存在/sidecar 未装配才是 error（与余额刷新同口径）。
func (cb *checkinBridge) ClaimOne(ctx context.Context, id string) (*httpapi.CheckinClaimResult, error) {
	c, err := cb.b.repos.Models.LookupCredential(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	plain, err := cb.b.repos.Secrets.GetPlaintextByCredentialID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrSecretNotFound) {
			// 密文缺失是结构性问题（来源异常），重试无意义 → 记为
			// unsupported（记上当天日期，不再重试）而不是 failed。
			return cb.record(ctx, c, &httpapi.CheckinClaimResult{
				OK: false, Kind: "failed", Unsupported: true,
				Message: "凭据没有可用的密文（来源异常）",
			}), nil
		}
		return nil, mapStoreErr(err)
	}
	// 凭据明文的双形态与余额刷新一致：API 型（api:<目录id>/custom）是裸 key
	// 字符串；反代型是 JSON 对象。sidecar 侧各走各的分发。
	var credential any
	if strings.HasPrefix(c.Provider, "api:") || c.Provider == "custom" {
		credential = strings.TrimSpace(string(plain))
	} else {
		var obj map[string]any
		if err := json.Unmarshal(plain, &obj); err != nil {
			return cb.record(ctx, c, &httpapi.CheckinClaimResult{
				OK: false, Kind: "failed", Unsupported: true,
				Message: "凭据明文不是 JSON 对象（形状异常）",
			}), nil
		}
		credential = obj
	}
	if cb.sidecar == nil {
		return nil, httpapi.ErrAdminNotWired
	}
	cctx, cancel := context.WithTimeout(ctx, CheckinCallTimeout)
	defer cancel()
	raw, err := cb.sidecar.CreditsClaim(cctx, c.Provider, credential)
	if err != nil {
		// sidecar 通道失败：记 failed（不写日期 → 退避重试），但对外不是 error。
		res := &httpapi.CheckinClaimResult{OK: false, Kind: "failed", Message: sidecarMessage(err)}
		return cb.record(ctx, c, res), nil
	}
	res := &httpapi.CheckinClaimResult{OK: raw.OK, Unsupported: raw.Unsupported}
	if o := raw.Outcome; o != nil {
		res.Kind = o.Kind
		res.Credit = o.Credit
		res.Unit = o.Unit
		res.Message = o.Message
	} else {
		res.Kind = "failed"
		res.Message = "sidecar 应答缺少 outcome"
	}
	// coversToday 记账（hub 口径）：claimed/already-claimed 且渠道没有自标
	// covers_today=false 才算「覆盖今天」；unsupported 也记日期（结构性
	// 不支持，当天不再试）。失败/活动未开启留空 → 退避重试。
	if res.Kind == "claimed" || res.Kind == "already-claimed" || res.Unsupported {
		if o := raw.Outcome; o != nil && o.CoversToday != nil && !*o.CoversToday {
			res.CoversToday = false
		} else {
			res.CoversToday = true
		}
	}
	// 轮换的新凭据写回密文（失败不阻断签到展示——新值已丢，下次领取再轮换）。
	if raw.RefreshedCredential != nil {
		if fresh, err := json.Marshal(raw.RefreshedCredential); err == nil {
			if err := cb.b.repos.Secrets.UpdatePlaintextByCredentialID(ctx, id, fresh); err != nil {
				slog.WarnContext(ctx, "签到轮换凭据写回失败", "credential", id, "err", err)
			}
		}
	}
	return cb.record(ctx, c, res), nil
}

// record 把结果格式化成展示串并落库（checkin_text / checkin_date / checkin_at）。
func (cb *checkinBridge) record(ctx context.Context, c *store.Credential, res *httpapi.CheckinClaimResult) *httpapi.CheckinClaimResult {
	res.Text = describeClaim(res)
	date := ""
	if res.CoversToday {
		date = UTC8DateString(time.Now())
	}
	if err := cb.b.repos.Models.UpdateCredentialCheckin(ctx, c.ID, res.Text, date, time.Now().UTC().Unix()); err != nil {
		slog.WarnContext(ctx, "签到结果落库失败", "credential", c.ID, "err", err)
	}
	return res
}

// describeClaim 把一次领取结果格式化成展示串（徽标/状态行用，尽量短）。
func describeClaim(res *httpapi.CheckinClaimResult) string {
	switch res.Kind {
	case "claimed":
		if res.Credit != nil {
			unit := "积分"
			if res.Unit == "token" {
				unit = "token"
			}
			return "+" + formatAmount(*res.Credit) + " " + unit
		}
		return "签到成功"
	case "already-claimed":
		if res.Message != "" {
			return res.Message // "今天已签到"
		}
		return "今天已签到"
	case "inactive":
		if res.Message != "" {
			return res.Message
		}
		return "签到活动未开启"
	case "failed":
		if res.Message != "" {
			return "签到失败：" + res.Message
		}
		return "签到失败"
	default:
		if res.Message != "" {
			return res.Message
		}
		return "未知结果"
	}
}

// —— settings 读写（自动签到开关 + 轮次摘要） ——

// checkinEnabled 读自动签到开关。缺键 = 开（hub 口径：默认打开，显式 false 才算关）。
func (cb *checkinBridge) checkinEnabled(ctx context.Context) bool {
	v, err := cb.b.repos.Settings.Get(ctx, settingCheckinEnabled)
	if err != nil {
		return true // ErrSettingNotFound / 读失败都按默认开处理
	}
	var enabled bool
	if err := json.Unmarshal([]byte(v), &enabled); err != nil {
		return true
	}
	return enabled
}

// saveLastRun 记录最近一轮摘要（整体替换写；失败只影响展示不影响签到）。
func (cb *checkinBridge) saveLastRun(ctx context.Context, rec CheckinRunRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return cb.b.repos.Settings.SetInTx(ctx, settingCheckinLastRun, string(raw))
}

// State 组装状态端点的视图（开关 + 最近一轮 + 当前到期数）。
func (cb *checkinBridge) State(ctx context.Context) (*httpapi.CheckinState, error) {
	st := &httpapi.CheckinState{Enabled: cb.checkinEnabled(ctx)}
	if v, err := cb.b.repos.Settings.Get(ctx, settingCheckinLastRun); err == nil {
		var rec CheckinRunRecord
		if json.Unmarshal([]byte(v), &rec) == nil {
			st.LastRunDate = rec.Date
			st.LastRunAt = rec.At
			st.LastRunResult = rec.Result
		}
	}
	if rows, err := cb.b.repos.Models.ListCheckinCandidates(ctx); err == nil {
		today, now := UTC8DateString(time.Now()), time.Now()
		for _, row := range rows {
			if checkinDue(row, today, now) {
				st.DueCount++
			}
		}
	}
	return st, nil
}

// SetEnabled 写自动签到开关（业务写与审计写同事务，与设置页同口径）。
func (cb *checkinBridge) SetEnabled(ctx context.Context, enabled bool, audit httpapi.AdminAuditEntry) error {
	raw, _ := json.Marshal(enabled)
	return cb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Settings.SetInTx(ctx, settingCheckinEnabled, string(raw)); err != nil {
			return err
		}
		if audit.Action == "" {
			audit.Action = string(store.ActionCategorySystem) + ".checkin.enabled"
		}
		_, err := appendAudit(ctx, tx.Logs, audit)
		return err
	})
}

// —— httpapi 窄接口适配（方法名与接口对齐；桥本体方法名保持领域语义） ——

// —— 签到能力清单（2026-11-01） ——
//
// 缓存 5 分钟：名单是「哪些渠道有签到活动」这类极慢变化的事实，而它会在
// 凭据池页每次渲染时被问一次。缓存让前端拿到的是稳定答案，也避免每次渲染
// 都跨一次 stdio 边界。sidecar 装了新渠道最迟 5 分钟反映出来。

var (
	checkinSupportedMu   sync.Mutex
	checkinSupportedList []string
	checkinSupportedAt   time.Time
)

// CheckinSupportedProviders 实现 httpapi.AdminCheckinCapability。
//
// sidecar 未装配/清单装载失败 → 返回**空列表且不报错**：前端少显示一排按钮，
// 比整页报错轻（「不知道」绝不能显示成「全部支持」）。
func (cb *checkinBridge) CheckinSupportedProviders(ctx context.Context) ([]string, error) {
	if cb.sidecar == nil {
		return []string{}, nil
	}
	checkinSupportedMu.Lock()
	defer checkinSupportedMu.Unlock()
	if !checkinSupportedAt.IsZero() && time.Since(checkinSupportedAt) < checkinSupportedTTL {
		out := make([]string, len(checkinSupportedList))
		copy(out, checkinSupportedList)
		return out, nil
	}
	list, err := cb.sidecar.CreditsSupported(ctx)
	if err != nil {
		// 读缓存兜底（非首次失败时），否则空列表。
		if !checkinSupportedAt.IsZero() {
			out := make([]string, len(checkinSupportedList))
			copy(out, checkinSupportedList)
			return out, nil
		}
		return []string{}, nil
	}
	checkinSupportedList = list
	checkinSupportedAt = time.Now()
	out := make([]string, len(list))
	copy(out, list)
	return out, nil
}

// RunCredentialCheckin 实现 httpapi.AdminCredentialCheckinRunner（手动单签：
// 显式操作不受开关与到期判定约束，永远放行）。
func (cb *checkinBridge) RunCredentialCheckin(ctx context.Context, id string) (*httpapi.CheckinClaimResult, error) {
	return cb.ClaimOne(ctx, id)
}

// RunCheckinRound 实现 httpapi.AdminCheckinScheduler：取到期清单并跑一轮。
func (cb *checkinBridge) RunCheckinRound(ctx context.Context) (*httpapi.CheckinRoundResult, error) {
	targets, err := cb.DueTargets(ctx, time.Now())
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return cb.RunRound(ctx, targets), nil
}

// CheckinState 实现 httpapi.AdminCheckinScheduler。
func (cb *checkinBridge) CheckinState(ctx context.Context) (*httpapi.CheckinState, error) {
	return cb.State(ctx)
}

// SetCheckinEnabled 实现 httpapi.AdminCheckinScheduler。
func (cb *checkinBridge) SetCheckinEnabled(ctx context.Context, enabled bool, audit httpapi.AdminAuditEntry) error {
	return cb.SetEnabled(ctx, enabled, audit)
}

// startCheckin 装配定时签到循环（app.go 在 startBalanceProbe 之后调用）。
//
// sidecar 不在时不启动：签到的分发全在 sidecar，没有它每 10 分钟空转
// 只会刷日志（与 startBalanceProbe 同一条纪律）。sidecar 起来了但 hub
// 不可装载的情形由 ClaimOne 的失败退避自然处理，不需要在这里预判。
func (a *App) startCheckin(ctx context.Context) {
	if a.sidecar == nil {
		return
	}
	loop := NewCheckinLoop(
		&checkinBridge{b: newAdminBridge(a.store), sidecar: a.sidecar},
		func(c context.Context) bool {
			v, err := a.store.Repos().Settings.Get(c, settingCheckinEnabled)
			if err != nil {
				return true // 缺键 = 开（hub 口径：显式 false 才算关）
			}
			var enabled bool
			return json.Unmarshal([]byte(v), &enabled) != nil || enabled
		},
		a.log,
	)
	go loop.Run(ctx)
}
