// unavailability_test.go —— 不可用模型台账 + 指数退避探测的契约测试（2026-11-01）。
//
// 钉住四件事（每件都是用户定案的直接可执行副本）：
//  1. 退避曲线：1m → 2m → 4m → 8m → 16m → 32m → 封顶 1h，**到顶后恒 1h**；
//  2. 失败分类：「哪些失败够格把模型记成不可用」的口径（尤其 429 不算）；
//  3. 路由行为：不可用候选排末尾而非剔除（跳过但保留兜底）；
//  4. 落库往返：MarkUnavailable/Restore 的 attempts 自增与出册。
//
// 全部跑真 SQLite（与 app 包其他契约测试同一口径）。
package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// newUnavailTestApp 起一个只装台账所需数据的 App（无后台循环）。
func newUnavailTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:18089"
	cfg.Store.Path = filepath.Join(dir, "u.db")
	cfg.Store.MasterKeyFile = filepath.Join(dir, "u.key")
	cfg.Sidecar.Enabled = false
	a, err := Setup(context.Background(), Options{Config: cfg, DisableBackgroundLoops: true})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// TestContractBackoffCurve 指数退避曲线（用户定案：超过 1h 后每 1h 检测一次）。
//
// 曲线表是**唯一事实源**（unavailableBackoff），这条测试是它的逐点副本：
//
//	1→1m 2→2m 3→4m 4→8m 5→16m 6→32m 7→1h 8→1h 100→1h
func TestContractBackoffCurve(t *testing.T) {
	cases := []struct {
		attempts int64
		want     time.Duration
	}{
		{1, 1 * time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 32 * time.Minute},
		{7, 1 * time.Hour},    // 64m → 封顶到 1h
		{8, 1 * time.Hour},    // 封顶后恒 1h（不再翻倍）
		{20, 1 * time.Hour},   // 远超封顶点仍是 1h
		{1000, 1 * time.Hour}, // 不溢出、不退化
	}
	for _, c := range cases {
		if got := unavailableBackoff(c.attempts); got != c.want {
			t.Errorf("unavailableBackoff(%d) = %v，期望 %v", c.attempts, got, c.want)
		}
	}
	// 边界：0 与负数按 1 次处理（防御性，不该出现但也不能返回 0 间隔——
	// 0 间隔会让探测循环每 tick 都打上游）。
	if got := unavailableBackoff(0); got != time.Minute {
		t.Errorf("unavailableBackoff(0) = %v，期望 1m（按首次处理）", got)
	}
	if got := unavailableBackoff(-5); got != time.Minute {
		t.Errorf("unavailableBackoff(-5) = %v，期望 1m", got)
	}
}

// TestContractClassifyFailure 失败分类口径：哪些失败够格记「不可用」。
//
// 判定表的三条硬边界（都有明确理由，见 classifyAttemptFailure 注释）：
//   - 凭据坏 / 额度尽 / 上游 5xx / 连接类 → **记**（换请求也救不了）；
//   - 429 限流 → **不记**（几十秒后自己好，冷却账本已覆盖「别立刻再打」）；
//   - 请求侧问题（能力缺失/方言冲突）→ **不记**（换一次请求就好了）。
func TestContractClassifyFailure(t *testing.T) {
	cases := []struct {
		name       string
		fail       *httpapi.AttemptFailure
		wantReason string
		wantUnuse  bool
	}{
		{"成功不归类", nil, "", false},
		{
			name:       "凭据失效算不可用",
			fail:       &httpapi.AttemptFailure{Err: &upstream.Error{Class: upstream.ErrAuth, Status: 401}},
			wantReason: string(upstream.ErrAuth), wantUnuse: true,
		},
		{
			name:       "额度耗尽算不可用",
			fail:       &httpapi.AttemptFailure{Err: &upstream.Error{Class: upstream.ErrQuota}},
			wantReason: string(upstream.ErrQuota), wantUnuse: true,
		},
		{
			name:       "上游5xx算不可用",
			fail:       &httpapi.AttemptFailure{Err: &upstream.Error{Class: upstream.ErrUpstream5xx, Status: 500}},
			wantReason: string(upstream.ErrUpstream5xx), wantUnuse: true,
		},
		{
			name:       "连不上算不可用",
			fail:       &httpapi.AttemptFailure{Err: &upstream.Error{Class: upstream.ErrConnect}},
			wantReason: string(upstream.ErrConnect), wantUnuse: true,
		},
		{
			name:       "429限流不算不可用",
			fail:       &httpapi.AttemptFailure{Err: &upstream.Error{Class: upstream.ErrRateLimited, Status: 429}},
			wantReason: "", wantUnuse: false,
		},
		{
			name:       "请求侧问题不算不可用",
			fail:       &httpapi.AttemptFailure{Kind: httpapi.AttemptFailureRequest, Err: errors.New("能力缺失")},
			wantReason: "", wantUnuse: false,
		},
		{
			name:       "协议不可解析算不可用",
			fail:       &httpapi.AttemptFailure{Kind: httpapi.AttemptFailureProtocol, Err: errors.New("bad json")},
			wantReason: string(upstream.ErrProtocol), wantUnuse: true,
		},
		{
			name:       "认不出类的错误偏向记入",
			fail:       &httpapi.AttemptFailure{Err: errors.New("半截 JSON")},
			wantReason: "unknown", wantUnuse: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, _, unusable := classifyAttemptFailure(c.fail)
			if unusable != c.wantUnuse {
				t.Fatalf("unusable = %v，期望 %v（reason=%q）", unusable, c.wantUnuse, reason)
			}
			if c.wantUnuse && reason != c.wantReason {
				t.Fatalf("reason = %q，期望 %q", reason, c.wantReason)
			}
			if !c.wantUnuse && reason != "" {
				t.Fatalf("不该记台账的失败却给了原因码 %q", reason)
			}
		})
	}
}

// TestContractUnavailabilityLedgerRoundTrip 台账落库往返：
// 首次记 1 次（排期 1m 后）→ 再记一次 attempts=2（排期 2m 后）→ 出册后查不到。
func TestContractUnavailabilityLedgerRoundTrip(t *testing.T) {
	a := newUnavailTestApp(t)
	ctx := context.Background()

	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "unavail-u",
		"https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	rmID, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up, UpstreamModelID: "m1",
	})
	if err != nil {
		t.Fatalf("建真实模型失败: %v", err)
	}

	led := newUnavailabilityLedger(a)
	if led.Unavailable(rmID) {
		t.Fatal("新模型不该在册")
	}
	led.MarkUnavailable(rmID, string(upstream.ErrAuth), "凭据失效")
	if !led.Unavailable(rmID) {
		t.Fatal("记过之后应在册（内存视图）")
	}

	got, err := a.store.Repos().Unavailability.Get(ctx, rmID)
	if err != nil || got == nil {
		t.Fatalf("库里应有记录: %v %v", got, err)
	}
	if got.Attempts != 1 || got.Reason != string(upstream.ErrAuth) {
		t.Fatalf("首次记录应为 attempts=1 / reason=auth，实得 %+v", got)
	}
	// 首次排期 ≈ 1 分钟后（允许几秒的执行漂移）。
	if d := time.Until(time.Unix(got.NextCheckAt, 0)); d < 40*time.Second || d > 90*time.Second {
		t.Fatalf("首次排期应约 1 分钟后，实得 %v", d)
	}

	// 第二次判定：attempts 自增到 2，排期翻倍到约 2 分钟后。
	led.MarkUnavailable(rmID, string(upstream.ErrUpstream5xx), "上游 5xx")
	got, err = a.store.Repos().Unavailability.Get(ctx, rmID)
	if err != nil || got == nil {
		t.Fatalf("库里应有记录: %v", err)
	}
	if got.Attempts != 2 {
		t.Fatalf("第二次判定应 attempts=2，实得 %d", got.Attempts)
	}
	if d := time.Until(time.Unix(got.NextCheckAt, 0)); d < 90*time.Second || d > 180*time.Second {
		t.Fatalf("第二次排期应约 2 分钟后，实得 %v", d)
	}

	// 出册：内存与库都要清掉。
	led.MarkAvailable(rmID)
	if led.Unavailable(rmID) {
		t.Fatal("出册后不该在内存视图")
	}
	if again, err := a.store.Repos().Unavailability.Get(ctx, rmID); err != nil || again != nil {
		t.Fatalf("出册后库里应查不到: %+v %v", again, err)
	}
}

// TestContractUnavailableGoesToTailButKept 路由行为：不可用候选排末尾但**不剔除**。
//
// 这是「跳过但保留兜底」的端到端副本：两条候选，第一条被记成不可用之后，
// 它应该掉到最后一位；而候选总数必须仍是 2（不是 1）。
func TestContractUnavailableGoesToTailButKept(t *testing.T) {
	a := newUnavailTestApp(t)
	ctx := context.Background()

	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "tail-u",
		"https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	win := int64(128000)
	mk := func(model string) string {
		id, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
			UpstreamID: up, UpstreamModelID: model, ContextWindow: &win,
		})
		if err != nil {
			t.Fatalf("建真实模型失败: %v", err)
		}
		return id
	}
	rmA, rmB := mk("m-a"), mk("m-b")

	amID, err := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "manual", DisplayName: "manual", Status: store.ModelActive, CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	// 声明序：A 在前、B 在后（这就是手动排序的唯一表达）。
	if err := a.store.Repos().Models.SetCandidates(ctx, amID, []store.Candidate{
		{AbstractModelID: amID, TargetID: rmA},
		{AbstractModelID: amID, TargetID: rmB},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}

	cs := newRoutingCandidates(a)
	before, err := cs.Candidates(ctx, "manual", ir.Request{})
	if err != nil || len(before) != 2 {
		t.Fatalf("期望 2 条候选（err=%v got=%d）", err, len(before))
	}
	// 手动序第 0 位必须是 A（用户排列的顺序）。
	if before[0].ID != rmA {
		t.Fatalf("首条应为 A（%s），实得 %s", rmA, before[0].ID)
	}

	// 判 A 不可用（凭据失效——够格入册的那类失败）。
	cs.NotifyCandidateOutcome(before[0], &httpapi.AttemptFailure{
		Err: &upstream.Error{Class: upstream.ErrAuth, Status: 401},
	})

	after, err := cs.Candidates(ctx, "manual", ir.Request{})
	if err != nil {
		t.Fatalf("Candidates 失败: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("不可用候选必须保留（跳过但不剔除），实得 %d 条", len(after))
	}
	if after[0].ID != rmB {
		t.Fatalf("在册的 B 应升到首位，实得 %s", after[0].ID)
	}
	if after[len(after)-1].ID != rmA {
		t.Fatalf("不可用的 A 应掉到末位，实得 %s", after[len(after)-1].ID)
	}

	// 恢复：一次真实成功即出册，A 回到首位（手动序立刻恢复）。
	cs.NotifyCandidateOutcome(httpapi.Candidate{ID: rmA}, nil)
	if a.unavailability.Unavailable(rmA) {
		t.Fatal("成功应出册")
	}
	back, err := cs.Candidates(ctx, "manual", ir.Request{})
	if err != nil || len(back) != 2 {
		t.Fatalf("恢复后仍应 2 条（err=%v got=%d）", err, len(back))
	}
	if back[0].ID != rmA {
		t.Fatalf("恢复后 A 应回到手动序首位，实得 %s", back[0].ID)
	}
}

// TestContractRateLimitDoesNotMarkUnavailable 429 不该让模型进台账。
//
// 这是「分类口径」在路由层的直接后果：一次限流把模型压到末尾并触发 1h 级
// 探测周期，是这套机制最容易造成的误伤。
func TestContractRateLimitDoesNotMarkUnavailable(t *testing.T) {
	a := newUnavailTestApp(t)
	ctx := context.Background()

	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "rl-u",
		"https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	rmID, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up, UpstreamModelID: "m1",
	})
	if err != nil {
		t.Fatalf("建真实模型失败: %v", err)
	}
	cs := newRoutingCandidates(a)
	cs.NotifyCandidateOutcome(httpapi.Candidate{ID: rmID}, &httpapi.AttemptFailure{
		Err: &upstream.Error{Class: upstream.ErrRateLimited, Status: 429},
	})
	if a.unavailability.Unavailable(rmID) {
		t.Fatal("429 不该把模型记成不可用（几十秒后自己好，冷却账本已覆盖）")
	}
	if rows, err := a.store.Repos().Unavailability.List(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("429 不该落库: rows=%v err=%v", rows, err)
	}
}

// TestContractProbeRemovesStaleRows 探测发现行已消失/停用 → 出册（不留僵尸记录）。
//
// 「僵尸记录」是最隐蔽的一种：一条永远探测不到的真实模型会永久占据台账，
// 让管理端看到一个明明已经不存在的模型「仍不可用」。
func TestContractProbeRemovesStaleRows(t *testing.T) {
	a := newUnavailTestApp(t)
	ctx := context.Background()

	led := newUnavailabilityLedger(a)
	// 直接记一条指向不存在行的台账（模拟「模型被删但台账还在」）。
	led.MarkUnavailable("00000000-0000-4000-8000-000000000001", "upstream_5xx", "历史残留")

	p := newUnavailabilityProbe(a, led)
	recovered, _, detail := p.probeOne(ctx, "00000000-0000-4000-8000-000000000001")
	if !recovered {
		t.Fatalf("行不存在时应按「出册」处理，实得 recovered=false（detail=%s）", detail)
	}

	// 停用的上游同理：不是「不可用」而是「不该用」。
	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "disabled-u",
		"https://example.com/v1", nil, false)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	rmID, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up, UpstreamModelID: "m1",
	})
	if err != nil {
		t.Fatalf("建真实模型失败: %v", err)
	}
	led.MarkUnavailable(rmID, "upstream_5xx", "上游 5xx")
	recovered, _, detail = p.probeOne(ctx, rmID)
	if !recovered {
		t.Fatalf("停用上游应出册，实得 recovered=false（detail=%s）", detail)
	}
}
