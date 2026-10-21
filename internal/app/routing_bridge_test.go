package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// routing_bridge.go 的契约测试（≥6）：M6 策略路由的装配面。
//
// 断言的是「装配层翻译是否正确」而不是 routing 的算法本身（后者在
// internal/routing 的 23 条契约里锁死）：store 的 (nil,ErrNotFound) →
// routing 的 (nil,nil)、候选展开的过滤口径、冷却回填、trace 回填、
// 错误语义（404 vs 503）。全部跑真 SQLite。

// newRoutingTestApp 起一个只装路由所需数据的 App。
func newRoutingTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:18081"
	cfg.Store.Path = filepath.Join(dir, "r.db")
	cfg.Store.MasterKeyFile = filepath.Join(dir, "r.key")
	cfg.Sidecar.Enabled = false
	a, err := Setup(context.Background(), Options{Config: cfg})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// seedModel 建一条完整链路：上游 → 真实模型 → 抽象模型 → 候选声明。
//
// 返回抽象模型名与真实模型行 ID（测试用它断言冷却/排序结果）。
func seedModel(t *testing.T, a *App, abstractName, upstreamModel string, ctxWin *int64, constraint string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "test-u-"+abstractName,
		"https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	rmID, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID:      up,
		UpstreamModelID: upstreamModel,
		ContextWindow:   ctxWin,
	})
	if err != nil {
		t.Fatalf("建真实模型失败: %v", err)
	}
	amID, err := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name:           abstractName,
		DisplayName:    abstractName,
		Status:         store.ModelActive,
		CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	var cj *string
	if constraint != "" {
		cj = &constraint
	}
	if err := a.store.Repos().Models.SetCandidates(ctx, amID, []store.Candidate{
		{AbstractModelID: amID, RealModelID: rmID, ConstraintJSON: cj},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}
	return amID, rmID
}

// 1. 不存在的模型名 → ErrModelNotFound（v1 面板据此写 404）。
func TestContractRoutingModelNotFound(t *testing.T) {
	a := newRoutingTestApp(t)
	cs := newRoutingCandidates(a)
	if _, err := cs.Candidates(context.Background(), "no-such-model", ir.Request{}); !errors.Is(err, httpapi.ErrModelNotFound) {
		t.Fatalf("期望 ErrModelNotFound，得到 %v", err)
	}
}

// 2. 真实模型限定名（含 ':'）→ 404（C2：真实模型对外不可见）。
func TestContractRoutingRealModelNameIs404(t *testing.T) {
	a := newRoutingTestApp(t)
	seedModel(t, a, "smart", "deepseek-chat", nil, "")
	cs := newRoutingCandidates(a)
	_, err := cs.Candidates(context.Background(), "dsh:test-u-smart/deepseek-chat", ir.Request{})
	if !errors.Is(err, httpapi.ErrModelNotFound) {
		t.Fatalf("真实模型名应 404，得到 %v", err)
	}
}

// 3. 正常路径：抽象模型 → 候选展开 → 返回候选（含 real_models 行 ID）。
func TestContractRoutingCandidatesByAbstractName(t *testing.T) {
	a := newRoutingTestApp(t)
	win := int64(128000)
	_, rmID := seedModel(t, a, "smart", "deepseek-chat", &win, "")
	cs := newRoutingCandidates(a)
	cands, err := cs.Candidates(context.Background(), "smart", ir.Request{})
	if err != nil {
		t.Fatalf("Candidates 失败: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("期望 1 条候选，得到 %d", len(cands))
	}
	if cands[0].ID != rmID {
		t.Fatalf("候选 ID = %d，期望 %d（冷却回填要靠它）", cands[0].ID, rmID)
	}
	if cands[0].UpstreamModelID != "deepseek-chat" {
		t.Fatalf("UpstreamModelID = %q", cands[0].UpstreamModelID)
	}
}

// 4. 抽象模型存在但候选集为空 → (nil, nil)（v1 面板写 503 no_candidate）。
func TestContractRoutingEmptyCandidatesIsNoCandidate(t *testing.T) {
	a := newRoutingTestApp(t)
	ctx := context.Background()
	if _, err := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "lonely", DisplayName: "lonely", Status: store.ModelActive, CapabilityMode: "strict",
	}); err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	cs := newRoutingCandidates(a)
	cands, err := cs.Candidates(ctx, "lonely", ir.Request{})
	if err != nil {
		t.Fatalf("空候选不应报错（503 语义）：%v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("期望空候选，得到 %d", len(cands))
	}
}

//  5. 上下文窗口未知 → FilterPaths 保守剔除（宁可 503 不可截断），
//     候选集变空但模型本身存在（仍是 503 而非 404）。
func TestContractRoutingUnknownContextWindowFiltered(t *testing.T) {
	a := newRoutingTestApp(t)
	seedModel(t, a, "narrow", "m-unknown", nil, "") // ContextWindow=nil
	cs := newRoutingCandidates(a)
	cands, err := cs.Candidates(context.Background(), "narrow", ir.Request{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("上下文未知的候选应被剔除，得到 %d 条", len(cands))
	}
}

// 6. 冷却回填：失败候选再查询时排到末尾（不剔除——cooldown.go 口径）。
func TestContractRoutingCooldownMovesToEnd(t *testing.T) {
	a := newRoutingTestApp(t)
	ctx := context.Background()
	win := int64(128000)
	// 两条候选：A、B（同抽象模型）。
	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "u-cd", "https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	rmA, _ := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{UpstreamID: up, UpstreamModelID: "model-a", ContextWindow: &win})
	rmB, _ := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{UpstreamID: up, UpstreamModelID: "model-b", ContextWindow: &win})
	amID, err := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "dual", DisplayName: "dual", Status: store.ModelActive, CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	if err := a.store.Repos().Models.SetCandidates(ctx, amID, []store.Candidate{
		{AbstractModelID: amID, RealModelID: rmA},
		{AbstractModelID: amID, RealModelID: rmB},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}

	cs := newRoutingCandidates(a)
	before, err := cs.Candidates(ctx, "dual", ir.Request{})
	if err != nil || len(before) != 2 {
		t.Fatalf("期望 2 条候选（err=%v, got %d）", err, len(before))
	}
	first := before[0]
	cs.NotifyCandidateFailure(first) // 首条失败 → 进冷却

	after, err := cs.Candidates(ctx, "dual", ir.Request{})
	if err != nil || len(after) != 2 {
		t.Fatalf("冷却中候选不该被剔除（err=%v, got %d）", err, len(after))
	}
	if after[0].ID == first.ID {
		t.Fatalf("冷却中的候选应排到末尾，仍出现在首位（id=%d）", first.ID)
	}
	if after[len(after)-1].ID != first.ID {
		t.Fatalf("冷却中的候选应在末位，末位是 %d（期望 %d）", after[len(after)-1].ID, first.ID)
	}
}

// 7. 渠道范围约束（channel_scope=include 不命中）→ 候选被剔除。
func TestContractRoutingChannelScopeExcludes(t *testing.T) {
	a := newRoutingTestApp(t)
	win := int64(128000)
	// 上游名 test-u-scoped → channelOf 用上游 name = "test-u-scoped"。
	// 约束要求渠道 "other-channel"，必然不命中。
	seedModel(t, a, "scoped", "m1", &win, `{"channel_scope":{"mode":"include","channels":["other-channel"]}}`)
	cs := newRoutingCandidates(a)
	cands, err := cs.Candidates(context.Background(), "scoped", ir.Request{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("渠道不匹配的候选应被剔除，得到 %d 条", len(cands))
	}
}

// 8. trace 回填：挂了 sink 的 ctx 拿到诊断（x-fr-trace 的数据面）。
func TestContractRoutingFillsTrace(t *testing.T) {
	a := newRoutingTestApp(t)
	win := int64(128000)
	seedModel(t, a, "traced", "m1", &win, "")
	cs := newRoutingCandidates(a)

	ctx := httpapi.WithV1Trace(context.Background())
	if _, err := cs.Candidates(ctx, "traced", ir.Request{}); err != nil {
		t.Fatalf("Candidates 失败: %v", err)
	}
	// 用一个能拿到 HeaderValue 的路径验证 sink 已填：ParseTraceHeader 往返。
	// 这里只断言「填了东西」——headervalue 的编码契约在 routing 的测试里。
	tr := httpapi.V1TraceFrom(ctx)
	if tr == nil {
		t.Fatal("trace 未回填到 ctx sink")
	}
	if tr.RequestedModel != "traced" {
		t.Fatalf("trace 缺少请求模型名（%+v）", tr)
	}
	if len(tr.Order) != 1 {
		t.Fatalf("trace 的排序结果应含 1 条，得到 %d", len(tr.Order))
	}
	raw, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("trace 不可序列化: %v", err)
	}
	if len(raw) < 10 {
		t.Fatalf("trace 过短：%s", raw)
	}
}
