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
func seedModel(t *testing.T, a *App, abstractName, upstreamModel string, ctxWin *int64, constraint string) (string, string) {
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
		t.Fatalf("候选 ID = %s，期望 %s（冷却回填要靠它）", cands[0].ID, rmID)
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
		t.Fatalf("冷却中的候选应排到末尾，仍出现在首位（id=%s）", first.ID)
	}
	if after[len(after)-1].ID != first.ID {
		t.Fatalf("冷却中的候选应在末位，末位是 %s（期望 %s）", after[len(after)-1].ID, first.ID)
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

// 9. 有意为之的 fail-open 契约化（code-review 巡检建议）：
// 目录没有 tools 能力字段时，带工具的请求必须放行到上游而不是全池 503。
//
// 这条测试保护的是一个**有意的非对称**：tools 未知→放行（失败形态是上游
// 400，客户端拿到准确错误）；context 未知→剔除（失败形态是静默截断，
// 不可逆且不可归因）。两者的差别只存在于注释里时，它迟早会被某个
// 「顺手统一一下」的改动抹平——所以钉成契约。
func TestContractRoutingToolsUnknownFailsOpen(t *testing.T) {
	a := newRoutingTestApp(t)
	ctx := context.Background()
	win := int64(128000)
	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "u-tools", "https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	// modalities 不含 image、efforts 未给——目录能给的都给了；tools 字段
	// 在数据模型里根本不存在（探测未落地）。
	rm, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up, UpstreamModelID: "model-tools", ContextWindow: &win,
		Modalities: pstr(`["text"]`),
	})
	if err != nil {
		t.Fatalf("落库失败: %v", err)
	}
	amID, err := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "tooled", DisplayName: "tooled", Status: store.ModelActive, CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	if err := a.store.Repos().Models.SetCandidates(ctx, amID, []store.Candidate{
		{AbstractModelID: amID, RealModelID: rm},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}

	cands, err := newRoutingCandidates(a).Candidates(ctx, "tooled", ir.Request{
		Tools: []ir.ToolDef{{Name: "calc"}},
	})
	if err != nil {
		t.Fatalf("候选查询不该报错: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("tools 能力未知时应 fail-open 放行（交给上游判），得到 %d 条候选", len(cands))
	}
}

// 10. 请求需要图片而候选只有 text → 模态过滤剔除（对照组：与 9 组成一对，
// 证明桥层确实在按画像过滤，而不是恒放行一切）。
func TestContractRoutingImageNeedFiltersOut(t *testing.T) {
	a := newRoutingTestApp(t)
	ctx := context.Background()
	win := int64(128000)
	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "u-img", "https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	rm, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up, UpstreamModelID: "text-only", ContextWindow: &win,
		Modalities: pstr(`["text"]`),
	})
	if err != nil {
		t.Fatalf("落库失败: %v", err)
	}
	amID, _ := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "imgless", DisplayName: "imgless", Status: store.ModelActive, CapabilityMode: "strict",
	})
	if err := a.store.Repos().Models.SetCandidates(ctx, amID, []store.Candidate{
		{AbstractModelID: amID, RealModelID: rm},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}

	req := ir.Request{Messages: []ir.Message{{
		Role:  ir.RoleUser,
		Parts: []ir.Part{ir.TextPart{Text: "看图"}, ir.ImagePart{}},
	}}}
	cands, err := newRoutingCandidates(a).Candidates(ctx, "imgless", req)
	if err != nil {
		t.Fatalf("候选查询不该报错: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("请求带图片而候选只有 text，应被模态过滤剔除，得到 %d 条", len(cands))
	}
}

// pstr 是本文件的字符串指针帮手（JSON 列以 *string 存 NULL 语义）。
func pstr(s string) *string { return &s }

//  11. 渠道的两级来源（01-catalog.md §2.3）：模型级 real_models.channel
//     压过上游行级兜底。
//
// 这条锁的是「权威来源」这件事本身。上游行级渠道是管理员的运营命名
// （upstreams.name / sidecar://provider），而一个 provider 里往往能拉
// 多个厂商的模型——把上游名当权威渠道，会让「按渠道分组」「按渠道约束
// 过滤」全部分错。0002 迁移让模型级渠道落库后，权威就该落在模型行上。
func TestContractRoutingChannelPrefersModelColumn(t *testing.T) {
	a := newRoutingTestApp(t)
	win := int64(128000)
	ctx := context.Background()

	// 上游名叫 "test-u-chan"，若按旧口径渠道就是这个名字；
	// 约束要求的是模型级派生渠道 "deepseek"，两者必然分道扬镳。
	amID, rmID := seedModel(t, a, "chan", "deepseek/deepseek-chat", &win, "")
	if err := a.store.Repos().Models.SetCandidates(ctx, amID, []store.Candidate{
		{AbstractModelID: amID, RealModelID: rmID,
			ConstraintJSON: pstr(`{"channel_scope":{"mode":"include","channels":["deepseek"]}}`)},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}

	// 先确认落库面：channel 已由仓储从限定名首段派生。
	rm, err := a.store.Repos().Models.GetRealModel(ctx, rmID)
	if err != nil {
		t.Fatalf("读真实模型失败: %v", err)
	}
	if rm.Channel == nil || *rm.Channel != "deepseek" {
		t.Fatalf("仓储应派生 channel=deepseek，得到 %v", rm.Channel)
	}

	// 桥层必须把模型级渠道喂给路由，而不是回落到上游名。
	br := newAbstractModelBridge(a)
	info, err := br.GetRealModel(ctx, rmID)
	if err != nil {
		t.Fatalf("桥读失败: %v", err)
	}
	if info.Channel != "deepseek" {
		t.Fatalf("桥应取模型级渠道 deepseek，得到 %q", info.Channel)
	}
	cs := newRoutingCandidates(a)
	cands, err := cs.Candidates(ctx, "chan", ir.Request{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("按模型级渠道 include=deepseek 应命中 1 条候选，得到 %d", len(cands))
	}
}

// 12. aggregate 排除与渠道来源彻底解耦（01-catalog.md §5 + §2.3）。
//
// 剔除判据是 upstream_model_id 首段（aggregate/ 前缀），与渠道无关。
// 这条锁两件事：
//   - 渠道名恰好叫 "aggregate" 的普通模型**不该**被误剔——那只是
//     管理员起的名字，不是模型归类；
//   - 真实前缀是 aggregate/* 的模型，即使渠道列有值也照样被剔。
//
// 两条合起来证明剔除判据不读渠道。code-review 巡检发现的
// 「第二道闸是死代码」正是这个解耦没做到时的产物：旧代码判
// Channel=="aggregate"，而渠道列里根本产不出这个值（它是模型名
// 前缀），于是那道闸恒假；修复后判据与存储层 SQL 同源。
func TestContractRoutingAggregateExcludedRegardlessOfChannel(t *testing.T) {
	a := newRoutingTestApp(t)
	win := int64(128000)
	ctx := context.Background()
	models := a.store.Repos().Models

	// 上游就名叫 "aggregate"；模型用裸 id（派生不出渠道 → channel
	// 留 NULL → 桥层回落到上游名 → 渠道恰好是 "aggregate"）。
	up, err := models.UpsertUpstream(ctx, "selfhosted", "aggregate", "https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	rmPlain, err := models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up, UpstreamModelID: "plain-model", ContextWindow: &win,
	})
	if err != nil {
		t.Fatalf("建真实模型失败: %v", err)
	}
	amPlain, err := models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "named-agg", DisplayName: "named-agg",
		Status: store.ModelActive, CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	if err := models.SetCandidates(ctx, amPlain, []store.Candidate{
		{AbstractModelID: amPlain, RealModelID: rmPlain},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}

	// 前置条件：模型级渠道确实缺失，桥层兜底后渠道 == "aggregate"。
	rm, err := models.GetRealModel(ctx, rmPlain)
	if err != nil {
		t.Fatalf("读真实模型失败: %v", err)
	}
	if rm.Channel != nil {
		t.Fatalf("裸 id 不该派生渠道，得到 %v", *rm.Channel)
	}
	br := newAbstractModelBridge(a)
	info, err := br.GetRealModel(ctx, rmPlain)
	if err != nil {
		t.Fatalf("桥读失败: %v", err)
	}
	if info.Channel != "aggregate" {
		t.Fatalf("渠道缺失时应回落到上游名 aggregate，得到 %q", info.Channel)
	}

	// 渠道叫 aggregate 的普通模型必须活着。
	cs := newRoutingCandidates(a)
	cands, err := cs.Candidates(ctx, "named-agg", ir.Request{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("渠道叫 aggregate 不等于聚合模型，应保留 1 条候选，得到 %d", len(cands))
	}

	// 真实前缀是 aggregate/* 的模型必须被剔（渠道无关）。
	amReal, _ := seedModel(t, a, "real-agg", "aggregate/pool-model", &win, "")
	_ = amReal
	cands, err = cs.Candidates(ctx, "real-agg", ir.Request{})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("聚合模型必须被剔除，得到 %d 条候选", len(cands))
	}
}
