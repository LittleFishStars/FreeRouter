// unavailability.go —— 不可用模型台账 + 指数退避探测（2026-11-01 用户定案）。
//
// # 需求原文
//
//	「仅当前一个不可用时才使用下一个；记录每个不可用的模型，使用指数退避
//	 循环检测（超过 1h 后每 1h 检测一次）是否可用。」
//
// # 三段职责
//
//  1. 记账（本文件 unavailabilityLedger）：流量中判定上游侧不可用 → 落库
//     （原因/诊断/次数/下次检测时刻）；一次真实成功 → 删行（立刻出册）。
//  2. 排位（routing.Order + Cooldowns.Apply）：在册的保持手动声明序在前，
//     不可用的排到末尾——**跳过但不剔除**（全池不可用时仍按序兜底试一次）。
//  3. 探测（unavailabilityProbe）：后台按退避曲线到期扫描，用**真实推理
//     ping**（max_tokens=256，与「检验连通性」同款）打穿候选→凭据→出站→
//     响应全链路；成功即出册，失败则推进排期。
//
// # 退避曲线
//
//	attempts=1 → 1m，2 → 2m，3 → 4m，4 → 8m，5 → 16m，6 → 32m，
//	之后封顶 1h，且**封顶后每 1h 一次**（用户定案的「超过 1h 后每 1h 检测一次」
//	= 到顶后间隔恒为 1h，不再继续翻倍）。
//
// 为什么落库而不是纯进程内：退避排期是小时级，进程重启不该把排期清零重新
// 从 1m 开始——那等于每次重启都白打一轮探测（打上游、花额度）。
//
// 为什么用真实推理 ping 而不是「列模型/HEAD」：轻量检查只能证明网络与凭据
// 通，证明不了**这个模型本身**可服务（模型下线/改名/配额耗尽的典型表现是
// 「网关 200，但这个模型 404/400」）。用户定案就是真实推理 ping。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// 退避曲线参数（用户定案：指数退避，封顶 1h，到顶后每 1h 一次）。
const (
	// UnavailableBaseDelay 首次判定不可用后的探测间隔。
	UnavailableBaseDelay = time.Minute
	// UnavailableMaxDelay 退避封顶（也是到顶后的固定间隔）。
	UnavailableMaxDelay = time.Hour
	// UnavailableProbeTick 扫描周期。**不是**探测间隔——探测间隔由退避曲线
	// 决定，tick 只负责「到点没有」。取 30s 让「1m 的首次探测」最多晚 30s。
	UnavailableProbeTick = 30 * time.Second
	// UnavailableProbeBatch 单轮最多探测几条。探测是真实推理（花额度、占
	// 上游并发），一轮要给可预期的上界；台账行数通常个位数，8 足够。
	UnavailableProbeBatch = 8
	// UnavailableProbeTimeout 单条探测上限。给足慢模型的首字延迟，又不让
	// 单条吃光整轮。
	UnavailableProbeTimeout = 30 * time.Second
	// UnavailableProbeMaxOutput 探测请求的 max_tokens。
	//
	// 为什么是 256 而不是 1：推理型模型（MiniMax-M2.7 实测）会把 1 个 token
	// 全花在思考上、返回零内容块，探测会把「可达且正常服务」误判成不可用。
	// 256 对 ping 提示词的成本可忽略。
	UnavailableProbeMaxOutput = 256
	// UnavailableProbePrompt 探测提示词（最短可用输入）。
	UnavailableProbePrompt = "ping"
)

// unavailableBackoff 返回判定次数为 attempts 时的探测间隔（指数退避 + 1h 封顶）。
//
// attempts 从 1 起（第一次判定不可用）。曲线：1m, 2m, 4m, 8m, 16m, 32m, 1h, 1h…
//
// 为什么到顶后是「恒 1h」而不是继续翻倍：继续翻倍会让一个 6 小时才能恢复的
// 上游被排到「下次检测：明天」——用户定案明确要求「超过 1h 后每 1h 检测一次」，
// 那是可接受的最坏恢复延迟上限。
func unavailableBackoff(attempts int64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := UnavailableBaseDelay
	for i := int64(1); i < attempts; i++ {
		d *= 2
		if d >= UnavailableMaxDelay {
			return UnavailableMaxDelay
		}
	}
	if d > UnavailableMaxDelay {
		return UnavailableMaxDelay
	}
	return d
}

// unavailabilityLedger 是台账的写入面（路由桥与探测循环共用）。
//
// 为什么带内存视图：路由每次请求都要问「这条候选在不在册」，那是热路径上
// 的一次主键读×候选数。维护一份进程内集合（写穿库、读走内存）让热路径
// 零 SQL，而权威仍然是库——重启后由 probe 启动时载入一次。
//
// 代价与取舍：多实例部署时内存视图会各自滞后（A 实例记的不可用，B 实例
// 要等它自己的请求失败或探测才看见）。本项目是单进程 + SQLite（05-storage
// §1 的部署形态），这个代价不存在；若将来多实例，把 Reads 换成每次查库即可。
type unavailabilityLedger struct {
	app *App
	// 内存视图：real_model_id → 台账记录。并发安全由 mu 保证（标准
	// sync.Mutex 而非 channel 版：这里没有「带缓冲的排队」语义，只要互斥）。
	mu   sync.RWMutex
	view map[string]store.Unavailability
}

// newUnavailabilityLedger 建台账视图（启动时载入一次库内记录）。
func newUnavailabilityLedger(app *App) *unavailabilityLedger {
	l := &unavailabilityLedger{app: app, view: map[string]store.Unavailability{}}
	if app == nil || app.store == nil {
		return l
	}
	// 启动载入：失败只影响「重启后第一次请求看不到旧台账」，探测循环会
	// 在首个 tick 重新读库补齐——不阻塞启动。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := app.store.Repos().Unavailability.List(ctx)
	if err != nil {
		if app.log != nil {
			app.log.Warn("app: 载入不可用台账失败（探测循环会重新读库）", "error", err)
		}
		return l
	}
	for _, u := range rows {
		l.view[u.RealModelID] = u
	}
	return l
}

// Unavailable 报告该路径是否在册（热路径用，走内存视图）。
func (l *unavailabilityLedger) Unavailable(realModelID string) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.view[realModelID]
	return ok
}

// Snapshot 返回内存视图的副本（诊断/测试用）。
func (l *unavailabilityLedger) Snapshot() map[string]store.Unavailability {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]store.Unavailability, len(l.view))
	for k, v := range l.view {
		out[k] = v
	}
	return out
}

// MarkUnavailable 记一条不可用（写穿库 + 更新内存视图）。
//
// 落库失败的处理：日志 + **仍然更新内存视图**。为什么内存优先：
// 台账失灵的后果是「路由反复打同一台坏上游」，比「重启后台账少一条记录」
// 严重得多；而重启后的重建由探测循环兜底。
func (l *unavailabilityLedger) MarkUnavailable(realModelID, reason, detail string) {
	if l == nil || l.app == nil || l.app.store == nil || realModelID == "" {
		return
	}
	now := time.Now().UTC().Unix()

	l.mu.Lock()
	prev, existed := l.view[realModelID]
	l.mu.Unlock()

	attempts := int64(1)
	if existed {
		attempts = prev.Attempts + 1
	}
	next := now + int64(unavailableBackoff(attempts)/time.Second)

	l.mu.Lock()
	l.view[realModelID] = store.Unavailability{
		RealModelID: realModelID,
		Reason:      reason,
		Detail:      detail,
		FailedAt:    now,
		Attempts:    attempts,
		NextCheckAt: next,
	}
	l.mu.Unlock()

	// 落库（脱离请求取消：请求失败了，但记账必须完成——否则这条路径
	// 会在下一秒又被优先选中）。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 3*time.Second)
	defer cancel()
	if err := l.app.store.Repos().Unavailability.MarkUnavailable(ctx, store.Unavailability{
		RealModelID: realModelID,
		Reason:      reason,
		Detail:      detail,
		FailedAt:    now,
		Attempts:    attempts, // 库侧会 +1，这里传的是「已知值」，仅首次生效
		NextCheckAt: next,
	}); err != nil && l.app.log != nil {
		l.app.log.Warn("app: 写不可用台账失败（内存已生效，探测循环会补）",
			"real_model_id", realModelID, "error", err)
	}
}

// MarkAvailable 出册（写穿库 + 清内存视图）。
//
// 幂等且廉价：不在册时不做任何库写（热路径上每条成功请求都会调它）。
func (l *unavailabilityLedger) MarkAvailable(realModelID string) {
	if l == nil || realModelID == "" {
		return
	}
	l.mu.Lock()
	_, existed := l.view[realModelID]
	delete(l.view, realModelID)
	l.mu.Unlock()
	if !existed || l.app == nil || l.app.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 3*time.Second)
	defer cancel()
	if err := l.app.store.Repos().Unavailability.MarkAvailable(ctx, realModelID); err != nil && l.app.log != nil {
		l.app.log.Warn("app: 清除不可用台账失败（内存已清，库侧残留会被探测覆盖）",
			"real_model_id", realModelID, "error", err)
	}
}

// Reschedule 只推进排期（探测失败但保留原有次数语义）。
func (l *unavailabilityLedger) Reschedule(realModelID string, nextCheckAt int64) {
	if l == nil || realModelID == "" {
		return
	}
	l.mu.Lock()
	if u, ok := l.view[realModelID]; ok {
		u.NextCheckAt = nextCheckAt
		l.view[realModelID] = u
	}
	l.mu.Unlock()
	if l.app == nil || l.app.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 3*time.Second)
	defer cancel()
	if err := l.app.store.Repos().Unavailability.Reschedule(ctx, realModelID, nextCheckAt); err != nil && l.app.log != nil {
		l.app.log.Warn("app: 推进探测排期失败", "real_model_id", realModelID, "error", err)
	}
}

// classifyAttemptFailure 把一次尝试失败归一成（原因码, 诊断, 是否算不可用）。
//
// 判定口径（这是「哪些失败该记台账」的唯一事实源）：
//
//	算不可用（上游侧问题，换请求也救不了）：
//	  - upstream_auth：凭据坏了/失效（401/403）
//	  - upstream_quota：额度耗尽
//	  - upstream_5xx：上游 5xx
//	  - connect / tls / conn_reset / timeout：连接类（端点挂了或网络不通）
//	  - protocol：响应不可解析（协议层坏了）
//
//	不算不可用（请求侧问题或不该归咎上游）：
//	  - bad_request：请求本身的问题（换请求就好了）
//	  - rate_limited（429）：**刻意不算**——它是「现在太挤」，几十秒后自己好，
//	    把它记成不可用会让一个健康的模型因为一次限流被压到末尾并开始 1h 级
//	    的探测周期。冷却账本（30s 窗口）已经覆盖了「别立刻再打」的需求。
//	  - internal_bug：我们的程序错，不该让上游背锅。
func classifyAttemptFailure(fail *httpapi.AttemptFailure) (reason, detail string, unusable bool) {
	if fail == nil {
		return "", "", false
	}
	// httpapi 侧已判定的原因码优先（它比原始错误更准确）。
	switch fail.Kind {
	case httpapi.AttemptFailureRequest:
		return "", fail.Detail, false
	case httpapi.AttemptFailureProtocol:
		return string(upstream.ErrProtocol), nonEmpty(fail.Detail, "上游响应不可解析"), true
	}
	// 交给上游错误分类（装配层是唯一能 import upstream 的地方）。
	var ue *upstream.Error
	if errors.As(fail.Err, &ue) {
		switch ue.Class {
		case upstream.ErrAuth, upstream.ErrQuota, upstream.ErrUpstream5xx,
			upstream.ErrConnect, upstream.ErrTLS, upstream.ErrConnReset,
			upstream.ErrTimeout, upstream.ErrProtocol:
			return string(ue.Class), nonEmpty(fail.Detail, ue.Message), true
		case upstream.ErrRateLimited, upstream.ErrBadRequest, upstream.ErrInternalBug:
			// 限流与请求问题不进台账（见函数注释）；内部缺陷也不进。
			return "", fail.Detail, false
		}
	}
	// 认不出类的错误：按「不可用」处理但原因码标 unknown。
	//
	// 为什么偏向记入而不是放过：认不出的失败往往是上游以非标准形态坏了
	// （自建网关吐了半截 JSON）。漏记的后果是反复打坏上游；误记的后果只是
	// 多一次后台探测（成功即出册）。
	return "unknown", nonEmpty(fail.Detail, "未分类的上游失败"), true
}

// nonEmpty 取第一个非空串（诊断文案优先级的小工具）。
func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ============ 探测循环 ============

// unavailabilityProbe 是后台探测循环。
type unavailabilityProbe struct {
	app   *App
	led   *unavailabilityLedger
	tick  time.Duration
	batch int
}

// newUnavailabilityProbe 建循环（装配层在 startUnavailabilityProbe 里调用）。
func newUnavailabilityProbe(app *App, led *unavailabilityLedger) *unavailabilityProbe {
	return &unavailabilityProbe{
		app: app, led: led,
		tick:  UnavailableProbeTick,
		batch: UnavailableProbeBatch,
	}
}

// Run 阻塞运行到 ctx 取消。
//
// 启动即跑一轮：重启后第一条要探测的往往已经过期（排期是持久化的），
// 立刻探测能把「其实早就好了」的模型尽快放回首选位次。
func (p *unavailabilityProbe) Run(ctx context.Context) {
	if p == nil || p.app == nil || p.app.store == nil || p.led == nil {
		return
	}
	p.runDue(ctx)
	t := time.NewTicker(p.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.runDue(ctx)
		}
	}
}

// runDue 探测一批到期的不可用模型。
//
// 串行而不是并发：探测是真实推理调用，并发会让各家上游的風控看到突发，
// 也会与业务流量抢同一批凭据的并发名额。台账行数是个位数，串行足够。
func (p *unavailabilityProbe) runDue(ctx context.Context) {
	now := time.Now().UTC().Unix()
	rows, err := p.app.store.Repos().Unavailability.ListDue(ctx, now, p.batch)
	if err != nil {
		p.app.log.Warn("app: 扫描到期不可用模型失败", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	// 库是权威：把库里多出来的行并入内存视图（多实例/手工改库的情形）。
	p.led.refresh(rows)

	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		if recovered, reason, detail := p.probeOne(ctx, row.RealModelID); recovered {
			p.led.MarkAvailable(row.RealModelID)
			p.app.log.Info("app: 不可用模型已恢复", "real_model_id", row.RealModelID,
				"was_reason", row.Reason, "attempts", row.Attempts)
		} else {
			// 探测失败：推进排期但**不再加 attempts**——已经知道的失败重复
			// 计数会让退避多翻一倍，把「半小时能好」的模型排到两小时后。
			next := time.Now().UTC().Unix() + int64(unavailableBackoff(row.Attempts+1)/time.Second)
			p.led.Reschedule(row.RealModelID, next)
			p.app.log.Info("app: 不可用模型仍未恢复", "real_model_id", row.RealModelID,
				"attempts", row.Attempts, "reason", reason, "detail", detail,
				"next_check_in", time.Until(time.Unix(next, 0)).Round(time.Second))
		}
	}
}

// refresh 把库里的记录并入内存视图（只增不删：删由 MarkAvailable 负责）。
func (l *unavailabilityLedger) refresh(rows []store.Unavailability) {
	if l == nil || len(rows) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.view == nil {
		l.view = map[string]store.Unavailability{}
	}
	for _, u := range rows {
		l.view[u.RealModelID] = u
	}
}

// probeOne 对一条路径做真实推理 ping，返回是否恢复。
//
// 走的是与业务流量**完全相同**的管线（候选源 → 出站 shim → 适配器 → 传输），
// 差别只有「用 max_tokens=256 的 ping 提示词、不写响应、不结算用量」。
func (p *unavailabilityProbe) probeOne(ctx context.Context, realModelID string) (bool, string, string) {
	row, err := p.app.store.Repos().Models.GetRealModel(ctx, realModelID)
	if err != nil || row == nil {
		// 行不存在（被删/被排除出候选）：**不是恢复也不是失败**——直接从
		// 台账里摘掉，否则会留下一条永远探测不到的僵尸记录。
		return true, "", "真实模型行已不存在，移出台账"
	}
	up, err := p.app.store.Repos().Models.GetUpstreamByID(ctx, row.UpstreamID)
	if err != nil || up == nil {
		return true, "", "上游行已不存在，移出台账"
	}
	if !up.Enabled || row.Status != store.RealModelActive || row.ExcludedFromCandidates {
		// 管理员停用了它：不是「不可用」而是「不该用」——出册（复活由管理员
		// 显式启用，不该被探测悄悄放回去）。
		return true, "", "已停用或已排除出候选，移出台账"
	}

	probeCtx, cancel := context.WithTimeout(ctx, UnavailableProbeTimeout)
	defer cancel()

	// 出站面板与业务流量同源（同一个 upstreamShim）：探测打的是真实链路。
	// 不经过候选源（那是「这一单该走谁」的决策，探测已经知道要测哪一行）。
	up0 := p.app.upstreamCaller
	if up0 == nil {
		return false, "routing_unwired", "出站管线未接线，无法探测"
	}

	cand := httpapi.Candidate{
		UpstreamModelID: row.UpstreamModelID,
		Display:         fmt.Sprintf("%s/%s#%s", channelOf(row.Channel, *up), row.UpstreamModelID, row.ID),
		Adapter:         adapterForKind(up.Kind),
		ID:              row.ID,
	}
	if cand.Adapter == nil {
		return false, "unknown_kind", "上游 kind 无适配器"
	}

	call, cerr := up0.Call(probeCtx, cand, probeRequest(row.UpstreamModelID), false)
	if cerr != nil || call == nil {
		return false, "connect", fmt.Sprint("探测调用失败:", cerr)
	}
	if call.Err != nil {
		reason, detail, _ := classifyAttemptFailure(&httpapi.AttemptFailure{Err: call.Err})
		if call.Body != nil {
			_ = call.Body.Close()
		}
		return false, nonEmpty(reason, "unknown"), nonEmpty(detail, call.Err.Error())
	}
	raw, rerr := io.ReadAll(io.LimitReader(call.Body, 1<<20))
	_ = call.Body.Close()
	if rerr != nil {
		return false, "protocol", "读取探测响应失败: " + rerr.Error()
	}
	resp, derr := cand.Adapter.DecodeResponse(raw)
	if derr != nil {
		snippet := string(raw)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return false, string(upstream.ErrProtocol), "响应解析失败: " + derr.Error() + "；body: " + snippet
	}
	_ = resp // 只要能解码出结构就算「这个模型真的在服务」
	return true, "", ""
}

// probeRequest 构造探测请求（真实推理 ping，与「检验连通性」同款参数）。
func probeRequest(upstreamModelID string) ir.Request {
	max := UnavailableProbeMaxOutput
	return ir.Request{
		Model:    upstreamModelID,
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: UnavailableProbePrompt}}}},
		Sampling: ir.Sampling{MaxOutput: &max},
		Stream:   false,
	}
}
