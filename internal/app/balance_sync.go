// balance_sync.go —— 定时刷新全部凭据余额（2026-11-01 用户要求）。
//
// ## 与既有两套余额机制的分工
//
//	balance.go 的 BalanceLoop   —— 只探 relay 的 provider 级额度（routing
//	                               排序键），不落凭据行、不面向展示。
//	单凭据「刷新余额」端点      —— 一次一条，管理端点按行触发。
//	本文件                      —— **定时遍历全部在册凭据**，逐个调用与单点
//	                               完全相同的实现（balanceRefreshBridge），
//	                               把 balance_text / balance_checked_at 刷成最新。
//
// 为什么复用单点的实现而不是另写一份查询：余额的落库口径（展示串格式、
// 失败文案、续期凭据写回密文）全在 balanceRefreshBridge 里，第二份实现
// 必然漂移——「手动刷新说 1,234，定时刷新说 1234」这种不一致最难查。
//
// ## 取数口径
//
// 只刷**在册且未移除**的凭据（upstream_credentials.removed_at IS NULL）：
// 已移除的凭据密文是墓碑，查也只会得到解密失败，纯浪费上游请求。
// 暂停/停用/失效的凭据照刷——余额是「上游账号还剩多少」，与能不能路由
// 无关（与签到同一口径；管理员要靠它判断停用的账号还值不值得救回来）。
//
// ## 失败与退避
//
// 单条失败不影响整轮（结果记进摘要）；sidecar 未装配时整个循环不启动
// （与 startCheckin 同一条纪律：没有 sidecar 每轮空转只会刷日志）。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// settings 键（值是 JSON 原文，与 SettingsRepo 的约定一致）。
const (
	settingBalanceSyncEnabled  = "balance_sync_enabled"
	settingBalanceSyncInterval = "balance_sync_interval_minutes"
	settingBalanceSyncLastRun  = "balance_sync_last_run"
)

const (
	// BalanceSyncStartupDelay 启动后延迟首轮：与签到错开，避让装配窗口。
	BalanceSyncStartupDelay = 90 * time.Second
	// BalanceSyncTickInterval 检查周期。**不是**刷新周期——刷新周期由
	// balance_sync_interval_minutes 设置项决定（默认 60 分钟），tick 只负责
	// 「到点没有」的判定。tick 取 1 分钟让设置改动（改短周期）最多 1 分钟生效。
	BalanceSyncTickInterval = time.Minute
	// BalanceSyncDefaultIntervalMinutes 默认刷新周期（小时级：余额变动慢，
	// 且每家查询都是一次上游请求，太勤会触发风控）。
	BalanceSyncDefaultIntervalMinutes = 60
	// BalanceSyncCallTimeout 单条查询上限：credits.query 是一趟公网请求
	// （可能带内部重试），与单点刷新的口径一致。
	BalanceSyncCallTimeout = 30 * time.Second
)

// BalanceSyncRecord 是最近一轮刷新的持久化摘要（settings 的 balance_sync_last_run）。
type BalanceSyncRecord struct {
	At          int64  `json:"at"`          // 完成时刻（Unix 秒）
	Attempted   int    `json:"attempted"`   // 本轮尝试条数
	OK          int    `json:"ok"`          // 成功拿到余额
	Failed      int    `json:"failed"`      // 上游拒绝/网络失败
	Unavailable int    `json:"unavailable"` // 上游未报余额（不是错误，是「没有这个数」）
	Summary     string `json:"summary"`     // 中文摘要（前端直接展示）
}

// BalanceSyncResult 是一轮刷新的汇总（手动端点与调度器共用）。
type BalanceSyncResult struct {
	Attempted   int    `json:"attempted"`
	OK          int    `json:"ok"`
	Failed      int    `json:"failed"`
	Unavailable int    `json:"unavailable"`
	Summary     string `json:"summary"`
}

// BalanceSyncState 是状态视图（GET /balance-sync/status）。
type BalanceSyncState struct {
	Enabled bool `json:"enabled"`
	// IntervalMinutes 是当前刷新周期（分钟）。
	IntervalMinutes int    `json:"interval_minutes"`
	LastRunAt       int64  `json:"last_run_at,omitempty"`
	LastRunResult   string `json:"last_run_result,omitempty"`
	// NextRunAt 是下一次计划刷新的时刻（Unix 秒）；0 = 开关关闭、不排期。
	NextRunAt int64 `json:"next_run_at,omitempty"`
	// DueCount 是当前能参与刷新的凭据数（用于「立即刷新」按钮旁的数量提示）。
	DueCount int `json:"due_count"`
}

// balanceSyncBridge 实现 httpapi.AdminBalanceSyncer。
type balanceSyncBridge struct {
	b       *adminBridge
	refresh httpapi.AdminCredentialBalanceRefresher
}

// BalanceSyncRound 跑一轮：遍历在册凭据逐个刷新余额，返回汇总。
//
// 串行而不是并发：credits.query 走 sidecar 的单条 stdio 管道（并发会让
// 各家的风控看到突发请求，也会让 Go 侧的超时账难对）。单条 30s 上限、
// 凭据数在几十条量级，一轮最坏几分钟——下一轮 tick 会因 busy 跳过。
func (b *balanceSyncBridge) BalanceSyncRound(ctx context.Context) (*httpapi.BalanceSyncResult, error) {
	if b.refresh == nil {
		return nil, httpapi.ErrAdminNotWired
	}
	targets, err := b.targets(ctx)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	res := &BalanceSyncResult{}
	for _, id := range targets {
		if ctx.Err() != nil {
			break
		}
		mctx, cancel := context.WithTimeout(ctx, BalanceSyncCallTimeout)
		one, rerr := b.refresh.RefreshCredentialBalance(mctx, id)
		cancel()
		res.Attempted++
		switch {
		case rerr != nil:
			res.Failed++
		case one == nil:
			res.Failed++
		case one.OK:
			res.OK++
		default:
			res.Unavailable++
		}
	}
	res.Summary = balanceSyncSummary(res)
	if err := b.saveLastRun(ctx, BalanceSyncRecord{
		At: time.Now().Unix(), Attempted: res.Attempted, OK: res.OK,
		Failed: res.Failed, Unavailable: res.Unavailable, Summary: res.Summary,
	}); err != nil {
		slog.WarnContext(ctx, "余额刷新：写最近一轮摘要失败", "err", err)
	}
	return &httpapi.BalanceSyncResult{
		Attempted: res.Attempted, OK: res.OK, Failed: res.Failed,
		Unavailable: res.Unavailable, Summary: res.Summary,
	}, nil
}

// targets 列出本轮要刷的凭据 id（在册、未移除）。
func (b *balanceSyncBridge) targets(ctx context.Context) ([]string, error) {
	rows, err := b.b.repos.Models.ListBalanceSyncTargets(ctx)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// BalanceSyncState 组装状态视图。
func (b *balanceSyncBridge) BalanceSyncState(ctx context.Context) (*httpapi.BalanceSyncState, error) {
	st := &httpapi.BalanceSyncState{
		Enabled:         b.enabled(ctx),
		IntervalMinutes: b.intervalMinutes(ctx),
	}
	if v, err := b.b.repos.Settings.Get(ctx, settingBalanceSyncLastRun); err == nil {
		var rec BalanceSyncRecord
		if json.Unmarshal([]byte(v), &rec) == nil {
			st.LastRunAt = rec.At
			st.LastRunResult = rec.Summary
		}
	}
	if rows, err := b.b.repos.Models.ListBalanceSyncTargets(ctx); err == nil {
		st.DueCount = len(rows)
	}
	if st.Enabled && st.LastRunAt > 0 {
		st.NextRunAt = st.LastRunAt + int64(st.IntervalMinutes)*60
	}
	return st, nil
}

// SetBalanceSyncEnabled 写开关（业务写与审计同事务）。
func (b *balanceSyncBridge) SetBalanceSyncEnabled(ctx context.Context, enabled bool, audit httpapi.AdminAuditEntry) error {
	raw, _ := json.Marshal(enabled)
	return b.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Settings.SetInTx(ctx, settingBalanceSyncEnabled, string(raw)); err != nil {
			return err
		}
		if audit.Action == "" {
			audit.Action = string(store.ActionCategorySystem) + ".balance_sync.enabled"
		}
		_, err := appendAudit(ctx, tx.Logs, audit)
		return err
	})
}

// SetBalanceSyncInterval 写刷新周期（分钟，下限 5 分钟）。
func (b *balanceSyncBridge) SetBalanceSyncInterval(ctx context.Context, minutes int, audit httpapi.AdminAuditEntry) error {
	if minutes < 5 {
		minutes = 5
	}
	raw, _ := json.Marshal(minutes)
	return b.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Settings.SetInTx(ctx, settingBalanceSyncInterval, string(raw)); err != nil {
			return err
		}
		if audit.Action == "" {
			audit.Action = string(store.ActionCategorySystem) + ".balance_sync.interval"
		}
		_, err := appendAudit(ctx, tx.Logs, audit)
		return err
	})
}

// enabled 读开关。缺键 = 开（与签到同口径：默认打开，显式 false 才算关）。
func (b *balanceSyncBridge) enabled(ctx context.Context) bool {
	v, err := b.b.repos.Settings.Get(ctx, settingBalanceSyncEnabled)
	if err != nil {
		return true
	}
	var on bool
	if json.Unmarshal([]byte(v), &on) != nil {
		return true
	}
	return on
}

// intervalMinutes 读刷新周期（缺键/坏值 → 默认 60；下限 5）。
func (b *balanceSyncBridge) intervalMinutes(ctx context.Context) int {
	v, err := b.b.repos.Settings.Get(ctx, settingBalanceSyncInterval)
	if err != nil {
		return BalanceSyncDefaultIntervalMinutes
	}
	var n int
	if json.Unmarshal([]byte(v), &n) != nil || n <= 0 {
		return BalanceSyncDefaultIntervalMinutes
	}
	if n < 5 {
		return 5
	}
	return n
}

// saveLastRun 记录最近一轮摘要（失败只影响展示，不影响刷新）。
func (b *balanceSyncBridge) saveLastRun(ctx context.Context, rec BalanceSyncRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return b.b.repos.Settings.SetInTx(ctx, settingBalanceSyncLastRun, string(raw))
}

// balanceSyncSummary 生成中文摘要（前端 toast 直接展示）。
func balanceSyncSummary(r *BalanceSyncResult) string {
	if r.Attempted == 0 {
		return "没有需要刷新的凭据"
	}
	s := fmt.Sprintf("%d 条：%d 条拿到余额", r.Attempted, r.OK)
	if r.Unavailable > 0 {
		s += fmt.Sprintf("，%d 条上游未报余额", r.Unavailable)
	}
	if r.Failed > 0 {
		s += fmt.Sprintf("，%d 条查询失败", r.Failed)
	}
	return s
}

// BalanceSyncLoop 是定时刷新的调度循环。
type BalanceSyncLoop struct {
	exec *balanceSyncBridge
	// enabled / interval 由装配层注入闭包（每轮重读，设置改动即时生效）。
	enabled  func(ctx context.Context) bool
	interval func(ctx context.Context) int
	delay    time.Duration
	tick     time.Duration
	// busy 防重入：一轮最坏 = 凭据数 × 30s，会跨越多个 tick。
	busy atomic.Bool
	log  *slog.Logger
	// lastRun 记录上次真正刷新的时刻（**纳秒**，进程内即可：跨进程的「上次」
	// 在 settings 里，但那只是展示；排期判定用内存里的这个，避免每 tick 读库）。
	//
	// 为什么用纳秒而不是 Unix 秒：秒级精度下「同一秒内的两次判定」会读到
	// 同一个值，把「刚跑完」与「还没跑」混成一种状态（测试实测：伪造
	// 61 分钟前后两次 runIfDue 落在同一秒，导致到点判定失效）。
	lastRun atomic.Int64
}

// NewBalanceSyncLoop 建循环。
func NewBalanceSyncLoop(exec *balanceSyncBridge, enabled func(ctx context.Context) bool,
	interval func(ctx context.Context) int, log *slog.Logger) *BalanceSyncLoop {
	if log == nil {
		log = slog.Default()
	}
	return &BalanceSyncLoop{
		exec: exec, enabled: enabled, interval: interval,
		delay: BalanceSyncStartupDelay, tick: BalanceSyncTickInterval, log: log,
	}
}

// Run 阻塞运行到 ctx 取消。
//
// 排期语义：上次刷新时刻 + interval 到点才刷。lastRun 初值为 0 ⇒ 首轮
// 在启动延迟后执行（重启即刷新一次，合理：重启后余额可能已经变了）。
func (l *BalanceSyncLoop) Run(ctx context.Context) {
	if l == nil || l.exec == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(l.delay):
	}
	l.runIfDue(ctx)
	t := time.NewTicker(l.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.runIfDue(ctx)
		}
	}
}

// runIfDue 到点才跑一轮（关着 / 忙 / 未到点 → 空转）。
func (l *BalanceSyncLoop) runIfDue(ctx context.Context) {
	if !l.enabled(ctx) {
		return
	}
	due := l.lastRun.Load() + int64(l.interval(ctx))*int64(time.Minute)
	if time.Now().UnixNano() < due {
		return
	}
	if !l.busy.CompareAndSwap(false, true) {
		return // 上一轮还没跑完
	}
	defer l.busy.Store(false)
	// 先记时刻再跑：长轮次期间不该被下一个 tick 再排一次；失败也让下一轮
	// 等到完整周期（余额是增强信息，不需要失败后每分钟重试）。
	l.lastRun.Store(time.Now().UnixNano())
	res, err := l.exec.BalanceSyncRound(ctx)
	if err != nil {
		l.log.Warn("余额刷新：本轮失败", "error", err)
		return
	}
	if res.Attempted > 0 {
		l.log.Info("余额刷新完成", "summary", res.Summary)
	}
}

// startBalanceSync 装配定时余额刷新（app.go 在 startCheckin 之后调用）。
//
// sidecar 不在时不启动：credits.query 全在 sidecar，没有它每轮只会得到
// 一堆「sidecar 未运行」（与 startCheckin / startBalanceProbe 同一条纪律）。
func (a *App) startBalanceSync(ctx context.Context) {
	if a.sidecar == nil {
		return
	}
	bridge := a.balanceSyncBridge()
	loop := NewBalanceSyncLoop(
		bridge,
		bridge.enabled,
		bridge.intervalMinutes,
		a.log,
	)
	go loop.Run(ctx)
}

// balanceSyncBridge 构造桥（手动端点与后台循环共用同一实例语义）。
func (a *App) balanceSyncBridge() *balanceSyncBridge {
	return &balanceSyncBridge{
		b:       newAdminBridge(a.store),
		refresh: balanceRefreshBridge{b: newAdminBridge(a.store), sidecar: a.sidecar},
	}
}
