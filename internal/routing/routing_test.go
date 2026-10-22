// routing_test.go —— M6 路由契约测试（TestContract 前缀，任务要求 ≥12 条）。
//
// 测试策略：routing 是纯决策层（包注释），全部用表驱动 fake 覆盖——
// 不引 store/upstream/httpapi 的任何运行件。每条测试钉住文档的一句
// 规则（注释里给节号），防止后续实现漂移。
package routing

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// —— fake AbstractModelStore（内存表） ——

type fakeStore struct {
	models map[string]*AbstractModel // by name
	byID   map[int64]*AbstractModel  // by id
	cands  map[int64][]CandidateDecl // by abstract model id
	real   map[int64]*RealModelInfo  // by real model id
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		models: map[string]*AbstractModel{},
		byID:   map[int64]*AbstractModel{},
		cands:  map[int64][]CandidateDecl{},
		real:   map[int64]*RealModelInfo{},
	}
}

func (f *fakeStore) addModel(m *AbstractModel) *AbstractModel {
	f.models[m.Name] = m
	f.byID[m.ID] = m
	return m
}

func (f *fakeStore) GetAbstractModelByName(ctx context.Context, name string) (*AbstractModel, error) {
	return f.models[name], nil
}

func (f *fakeStore) GetAbstractModel(ctx context.Context, id int64) (*AbstractModel, error) {
	return f.byID[id], nil
}

func (f *fakeStore) ListCandidates(ctx context.Context, abstractModelID int64) ([]CandidateDecl, error) {
	return f.cands[abstractModelID], nil
}

func (f *fakeStore) GetRealModel(ctx context.Context, id int64) (*RealModelInfo, error) {
	return f.real[id], nil
}

// mkReal 构造一个正常可用的真实模型行。
func mkReal(id int64, channel, modelID string) *RealModelInfo {
	return &RealModelInfo{
		ID: id, UpstreamID: 100 + id, UpstreamModelID: modelID, UpstreamKind: "relay",
		Channel: channel, Status: "active", UpstreamEnabled: true,
		Modalities: []string{"text"}, Efforts: []string{"off", "low", "high"},
	}
}

// ptrI64 帮手。
func ptrI64(v int64) *int64 { return &v }

func ctx() context.Context { return context.Background() }

// ============ 1. 抽象模型解析 ============

// TestContractResolveActive 命中 active 模型（02-abstract.md §6.2：
// 请求抽象模型名 → 正常路由）。
func TestContractResolveActive(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: 1, Name: "smart", Status: StatusActive})
	st.cands[1] = []CandidateDecl{{RealModelID: 10}}
	st.real[10] = mkReal(10, "deepseek-official", "deepseek-flash")

	r, rerr, err := NewResolver(st).Resolve(ctx(), "smart")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: err=%v rerr=%v", err, rerr)
	}
	if r.Model.Name != "smart" || r.Terminal.Name != "smart" {
		t.Fatalf("Model/Terminal 应同名: %v %v", r.Model.Name, r.Terminal.Name)
	}
	if len(r.Candidates) != 1 || r.Candidates[0].ID != 10 {
		t.Fatalf("候选展开错误: %+v", r.Candidates)
	}
	if r.CycleBroken {
		t.Fatalf("active 模型不应报环")
	}
}

// TestContractResolveUnknownName 未知名 404 语义（02-abstract.md §6.5 表：
// 模型不存在 → 404；不区分「不存在」与「不可见」防泄露池结构）。
func TestContractResolveUnknownName(t *testing.T) {
	st := newFakeStore()
	_, rerr, err := NewResolver(st).Resolve(ctx(), "nope")
	if err != nil {
		t.Fatalf("存储错误不应发生: %v", err)
	}
	if rerr == nil || rerr.Code != "model_not_found" {
		t.Fatalf("未知名应 model_not_found: %+v", rerr)
	}
}

// TestContractResolveRealModel404 真实模型名对外 404（红线 C2；
// 限定名 dsh:… 含 ':' 在字符串层面直接判，裸 id 落「未知名」）。
func TestContractResolveRealModel404(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: 1, Name: "smart", Status: StatusActive})
	st.cands[1] = []CandidateDecl{{RealModelID: 10}}
	st.real[10] = mkReal(10, "deepseek-official", "deepseek-flash")
	res := NewResolver(st)

	// 限定名（含 ':'）。
	_, rerr, err := res.Resolve(ctx(), "dsh:deepseek-official/deepseek-flash")
	if err != nil || rerr == nil || rerr.Code != "model_not_found" {
		t.Fatalf("限定名应 404: err=%v rerr=%+v", err, rerr)
	}
	// 裸上游 id（库里没有这个抽象名）。
	_, rerr, err = res.Resolve(ctx(), "deepseek-flash")
	if err != nil || rerr == nil || rerr.Code != "model_not_found" {
		t.Fatalf("裸真实 id 应 404: err=%v rerr=%+v", err, rerr)
	}
}

// TestContractResolveDisabled disabled → 404 语义（04-deprecation.md §1.1：
// 不出现在 /v1/models，请求 404/410；待定项取 404 与不存在同码防探测）。
func TestContractResolveDisabled(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: 1, Name: "old", Status: StatusDisabled})
	_, rerr, err := NewResolver(st).Resolve(ctx(), "old")
	if err != nil || rerr == nil || rerr.Code != "model_not_found" {
		t.Fatalf("disabled 应 model_not_found: err=%v rerr=%+v", err, rerr)
	}
}

// TestContractDeprecationChain 弃用链走到 active（04-deprecation.md §2.1：
// Alice 旧配置 → A(old) → B(old) → C(active)；头里用 A、路由用 C）。
func TestContractDeprecationChain(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: 1, Name: "a", Status: StatusDeprecated, ReplacementModelID: ptrI64(2)})
	st.addModel(&AbstractModel{ID: 2, Name: "b", Status: StatusDeprecated, ReplacementModelID: ptrI64(3)})
	st.addModel(&AbstractModel{ID: 3, Name: "c", Status: StatusActive})
	st.cands[3] = []CandidateDecl{{RealModelID: 30}}
	st.real[30] = mkReal(30, "cline", "big-model")

	r, rerr, err := NewResolver(st).Resolve(ctx(), "a")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: %v %v", err, rerr)
	}
	if r.Model.Name != "a" {
		t.Fatalf("Model 应是用户请求的 a: %v", r.Model.Name)
	}
	if r.Terminal.Name != "c" {
		t.Fatalf("Terminal 应走到 c: %v", r.Terminal.Name)
	}
	if len(r.Chain) != 3 || r.Chain[0] != "a" || r.Chain[2] != "c" {
		t.Fatalf("链记录错误: %v", r.Chain)
	}
	if len(r.Candidates) != 1 || r.Candidates[0].ID != 30 {
		t.Fatalf("候选应来自终点 c: %+v", r.Candidates)
	}
}

// TestContractDeprecationCycleRejected 链成环拒绝（04-deprecation.md §2.4/2.5：
// 正常路径不可能（保存时只准指向 active），直接改库可造出环——运行时
// visited 兜底，降级为无替代不报错，CycleBroken 告警）。
func TestContractDeprecationCycleRejected(t *testing.T) {
	st := newFakeStore()
	// 直接改库口径：a→b、b→a 互相指向（状态机正常路径写不出来）。
	st.addModel(&AbstractModel{ID: 1, Name: "a", Status: StatusDeprecated, ReplacementModelID: ptrI64(2)})
	st.addModel(&AbstractModel{ID: 2, Name: "b", Status: StatusDeprecated, ReplacementModelID: ptrI64(1)})
	st.cands[1] = []CandidateDecl{{RealModelID: 10}}
	st.real[10] = mkReal(10, "deepseek-official", "deepseek-flash")

	r, rerr, err := NewResolver(st).Resolve(ctx(), "a")
	if err != nil || rerr != nil {
		t.Fatalf("环应降级不报错: %v %v", err, rerr)
	}
	if !r.CycleBroken {
		t.Fatalf("环应置 CycleBroken 告警位")
	}
	// 降级为无替代：停在起点 a，用 a 自己的候选。
	if r.Terminal.Name != "a" {
		t.Fatalf("环降级应停在起点: %v", r.Terminal.Name)
	}
	if len(r.Candidates) != 1 {
		t.Fatalf("降级后仍应可路由: %+v", r.Candidates)
	}
}

// TestContractNoCandidate 空候选 → no_candidate（02-abstract.md §6.5：
// 模型存在但无候选可用 → 503 + 专用错误码，不是 500 也不是 404）。
func TestContractNoCandidate(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: 1, Name: "empty", Status: StatusActive})
	st.cands[1] = []CandidateDecl{{RealModelID: 99}} // real 行不存在

	_, rerr, err := NewResolver(st).Resolve(ctx(), "empty")
	if err != nil {
		t.Fatalf("存储错误不应发生: %v", err)
	}
	if rerr == nil || rerr.Code != "no_candidate" {
		t.Fatalf("空候选应 no_candidate: %+v", rerr)
	}
}

// TestContractAggregateExcluded aggregate/* 排除（01-catalog.md §5：
// 上游自己的策略层与我们重叠后无法归因——两层策略只留我们这层）。
//
// 两道闸各测各的：①excluded_from_candidates 标志（刷新器写）；②
// upstream_model_id 首斜杠段 == aggregate（路由层兜底，2026-10-22 把原
// 「渠道名 == aggregate」改为前缀判——渠道名由 upstream 行推出，永远产不出
// aggregate，那道是恒假的死代码）。
func TestContractAggregateExcluded(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: 1, Name: "smart", Status: StatusActive})
	st.cands[1] = []CandidateDecl{{RealModelID: 10}, {RealModelID: 11}, {RealModelID: 12}, {RealModelID: 13}}
	st.real[10] = mkReal(10, "aggregate", "agg-model") // 闸①：标志命中
	st.real[10].ExcludedFromCandidates = true
	st.real[11] = mkReal(11, "deepseek-official", "flash") // 正常
	st.real[12] = mkReal(12, "cline", "aggregate/x")       // 闸②：id 前缀命中、标志被清掉（改库）
	st.real[12].ExcludedFromCandidates = false
	// 近似名不误伤：渠道段必须精确等于 aggregate（与 store 的 SQL 前缀
	// 判定同款口径——aggregate-v2/、my_aggregate/ 都不是聚合渠道）。
	st.real[13] = mkReal(13, "aggregate-v2", "aggregate-v2/x")

	r, rerr, err := NewResolver(st).Resolve(ctx(), "smart")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: %v %v", err, rerr)
	}
	if len(r.Candidates) != 2 || r.Candidates[0].ID != 11 || r.Candidates[1].ID != 13 {
		t.Fatalf("aggregate 应全排除、近似名不误伤（标志+id 前缀两道）: %+v", r.Candidates)
	}
}

// ============ 2. 过滤 ============

// TestContractFilterCapability 能力过滤（03-strategy.md §2.1 F1：
// 本次请求需要的能力候选都必须支持；未知按不支持——02-abstract.md §4.3）。
func TestContractFilterCapability(t *testing.T) {
	cw := int64(131072)
	paths := []Path{
		{RealModel: *mkReal(10, "a", "m1"), Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text"}, SupportsTools: true, Efforts: []string{"off", "low"}}},
		{RealModel: *mkReal(11, "b", "m2"), Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text", "image"}, SupportsTools: true, Efforts: []string{"off", "low", "high"}}},
		{RealModel: *mkReal(12, "c", "m3"), Caps: Capabilities{ContextWindow: &cw, Modalities: nil}}, // 模态未知
	}
	// 请求：文本+图片 + 工具 + high 档。
	profile := RequestProfile{NeedModalities: []string{"text", "image"}, EstInputTokens: 1000, MaxOutputTokens: 500, NeedTools: true, NeedEffort: "high"}

	kept, outs := FilterPaths(profile, paths)
	if len(kept) != 1 || kept[0].RealModel.ID != 11 {
		t.Fatalf("只有 11 满足全部要求: %+v", kept)
	}
	// 10 缺 image（在 modality 被剔）；12 模态未知按不支持剔。
	if len(outs) != 2 {
		t.Fatalf("应剔 2 条: %+v", outs)
	}
}

// TestContractFilterContext 上下文过滤（03-strategy.md §2.1 F2：
// 估算输入+声明最大输出 ≤ 候选上下文；窗口未知按保守下界剔除）。
func TestContractFilterContext(t *testing.T) {
	small, big := int64(4096), int64(131072)
	paths := []Path{
		{RealModel: *mkReal(10, "a", "m1"), Caps: Capabilities{ContextWindow: &small}},
		{RealModel: *mkReal(11, "b", "m2"), Caps: Capabilities{ContextWindow: &big}},
		{RealModel: *mkReal(12, "c", "m3"), Caps: Capabilities{ContextWindow: nil}}, // 未知
	}
	profile := RequestProfile{EstInputTokens: 3000, MaxOutputTokens: 2000} // 需 5000

	kept, outs := FilterPaths(profile, paths)
	if len(kept) != 1 || kept[0].RealModel.ID != 11 {
		t.Fatalf("只有 11 窗口够: %+v", kept)
	}
	// 10 窗口不够（context）；12 未知（context 保守）。
	for _, o := range outs {
		if o.Reason != "context" {
			t.Fatalf("剔除原因应为 context: %+v", o)
		}
	}
}

// TestContractFilterEmptyNoCandidate 过滤后为空 → 调用方走 503 no_candidate
// （03-strategy.md §2.3；FilterOut 带原因——「险些可用」诊断的原料）。
func TestContractFilterEmptyNoCandidate(t *testing.T) {
	cw := int64(4096)
	paths := []Path{{RealModel: *mkReal(10, "a", "m1"), Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text"}}}}
	profile := RequestProfile{NeedModalities: []string{"image"}, EstInputTokens: 100, MaxOutputTokens: 100}

	kept, outs := FilterPaths(profile, paths)
	if len(kept) != 0 || len(outs) != 1 || outs[0].Reason != "modality" {
		t.Fatalf("过滤后为空应带原因: kept=%v outs=%+v", kept, outs)
	}
}

// TestContractFilterConstraint 渠道范围约束（02-abstract.md §3.1
// channel_scope include/exclude）。
func TestContractFilterConstraint(t *testing.T) {
	cw := int64(65536)
	paths := []Path{
		{RealModel: *mkReal(10, "deepseek-official", "m1"), Constraint: Constraint{ChannelMode: "include", Channels: []string{"cline"}}},
		{RealModel: *mkReal(11, "cline", "m2"), Constraint: Constraint{ChannelMode: "include", Channels: []string{"cline"}}},
		{RealModel: *mkReal(12, "cline", "m3"), Constraint: Constraint{ChannelMode: "exclude", Channels: []string{"deepseek-official"}}},
	}
	for i := range paths {
		paths[i].Caps = Capabilities{ContextWindow: &cw, Modalities: []string{"text"}}
	}
	profile := RequestProfile{EstInputTokens: 10, MaxOutputTokens: 10}

	kept, outs := FilterPaths(profile, paths)
	// 10 被渠道 include 剔（它是 deepseek-official）；11/12 留。
	if len(kept) != 2 || kept[0].RealModel.ID != 11 || kept[1].RealModel.ID != 12 {
		t.Fatalf("渠道约束过滤错误: kept=%+v outs=%+v", kept, outs)
	}
}

// ============ 3. 排序 ============

// TestContractRankDefaultChain 默认链 [expiry-first, balance]（03-strategy.md
// §3.2：先按到期时间升序，同到期再按余额升序——两问合起来破
// 「每次都打同一个账号」的并列）。
func TestContractRankDefaultChain(t *testing.T) {
	paths := []Path{
		{RealModel: *mkReal(10, "a", "m1"), ExpiresAt: 2000, BalanceKnown: true, Balance: 5},
		{RealModel: *mkReal(11, "b", "m2"), ExpiresAt: 1000, BalanceKnown: true, Balance: 50},
		{RealModel: *mkReal(12, "c", "m3"), ExpiresAt: 1000, BalanceKnown: true, Balance: 3},
		{RealModel: *mkReal(13, "d", "m4"), ExpiresAt: 0, BalanceKnown: false}, // 双无键
	}
	out := Rank(paths, nil, nil) // 空链 → 默认链
	got := []int64{out[0].RealModel.ID, out[1].RealModel.ID, out[2].RealModel.ID, out[3].RealModel.ID}
	// 12（最早到期+余额最少）→ 11（同到期、余额大）→ 10（更晚到期）→ 13（无键垫底）。
	want := []int64{12, 11, 10, 13}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("默认链顺序错误: got=%v want=%v", got, want)
		}
	}
}

// TestContractRankRoundRobinTieBreak round-robin 平局决胜（03-strategy.md
// §3.2：仅显式配置时追加链尾；§3.4 游标快照——决策可追溯）。
func TestContractRankRoundRobinTieBreak(t *testing.T) {
	// 三个完全同键的路径（同到期同余额）——默认链下是平局组。
	paths := []Path{
		{RealModel: *mkReal(10, "a", "m1"), ExpiresAt: 1000, BalanceKnown: true, Balance: 5},
		{RealModel: *mkReal(11, "b", "m2"), ExpiresAt: 1000, BalanceKnown: true, Balance: 5},
	}
	rr := &RoundRobinCursor{}
	// 无 round-robin：稳定序（声明序）。
	out1 := Rank(paths, []string{StrategyExpiryFirst, StrategyBalance}, nil)
	if out1[0].RealModel.ID != 10 {
		t.Fatalf("无决胜应稳定序: %v", out1[0].RealModel.ID)
	}
	// 显式配置 round-robin：游标推进应翻转同键对的次序。
	gotFirst := []int64{}
	for i := 0; i < 4; i++ {
		out := Rank(paths, []string{StrategyExpiryFirst, StrategyRoundRobin}, rr)
		gotFirst = append(gotFirst, out[0].RealModel.ID)
	}
	// 交替 10/11 才证明均摊生效（固定值=没轮转）。
	if gotFirst[0] == gotFirst[1] || gotFirst[0] != gotFirst[2] {
		t.Fatalf("round-robin 应轮转: %v", gotFirst)
	}
	if rr.Calls != 4 {
		t.Fatalf("游标应记录推进 4 次: %d", rr.Calls)
	}
}

// TestContractRankDeterminism 确定性（03-strategy.md §3.4：同样输入必须
// 同样输出——干跑可回放、故障可复现的前提）。
func TestContractRankDeterminism(t *testing.T) {
	paths := []Path{
		{RealModel: *mkReal(10, "a", "m1"), ExpiresAt: 3000, BalanceKnown: true, Balance: 1},
		{RealModel: *mkReal(11, "b", "m2"), ExpiresAt: 1000, BalanceKnown: true, Balance: 9},
		{RealModel: *mkReal(12, "c", "m3"), ExpiresAt: 2000, BalanceKnown: true, Balance: 5},
	}
	var first []int64
	for i := 0; i < 5; i++ {
		out := Rank(paths, nil, nil)
		if i == 0 {
			for _, p := range out {
				first = append(first, p.RealModel.ID)
			}
		} else {
			for j, p := range out {
				if p.RealModel.ID != first[j] {
					t.Fatalf("第 %d 次排序漂移: %v vs %v", i, p.RealModel.ID, first[j])
				}
			}
		}
	}
}

// TestContractValidateChain 策略链校验（03-strategy.md §5：重复项拒绝、
// 未知策略拒绝；空链=默认链在 Rank 内展开）。
func TestContractValidateChain(t *testing.T) {
	if err := ValidateChain([]string{StrategyExpiryFirst, StrategyBalance}); err != nil {
		t.Fatalf("默认链应合法: %v", err)
	}
	if err := ValidateChain([]string{StrategyExpiryFirst, StrategyExpiryFirst}); err == nil {
		t.Fatalf("重复项应拒绝")
	}
	if err := ValidateChain([]string{"cheap-first"}); err == nil {
		t.Fatalf("未知策略应拒绝（旧名已被删，见 §3.3.1）")
	}
	if err := ValidateChain(nil); err != nil {
		t.Fatalf("空链=默认链应合法: %v", err)
	}
}

// ============ 4. 冷却 ============

// TestContractCooldownTailNotRemoved 冷却候选排末尾而非剔除（lead 指令④：
// 最后一个候选冷却时剔除会 no_candidate——「冷却」意味着失败过但可能恢复，
// 没有更好选择时它还是要顶上）。
func TestContractCooldownTailNotRemoved(t *testing.T) {
	cd := NewCooldowns().SetClock(func() int64 { return 1000 })
	cd.MarkFailure(11, 5000) // 候选 11 冷却中

	paths := []Path{
		{RealModel: *mkReal(10, "a", "m1")},
		{RealModel: *mkReal(11, "b", "m2")},
		{RealModel: *mkReal(12, "c", "m3")},
	}
	out := cd.Apply(paths)
	if len(out) != 3 {
		t.Fatalf("冷却不应剔除: %d", len(out))
	}
	if out[2].RealModel.ID != 11 || !out[2].CooledDown {
		t.Fatalf("冷却候选应排末尾且带标记: %+v", out)
	}
	// 唯一候选冷却 → 仍在场（不 no_candidate）。
	solo := cd.Apply([]Path{{RealModel: *mkReal(11, "b", "m2")}})
	if len(solo) != 1 || !solo[0].CooledDown {
		t.Fatalf("唯一冷却候选应保留: %+v", solo)
	}
}

// TestContractCooldownExpiry 冷却到期恢复（冷却是短窗运行态，不是熔断）。
func TestContractCooldownExpiry(t *testing.T) {
	now := int64(1000)
	cd := NewCooldowns().SetClock(func() int64 { return now }).
		SetJitter(func() int64 { return 0 }) // 钉死抖动：真实抖动 0..10s 会让窗口断言随机失败
	cd.MarkFailure(10, 1000) // 窗口 1s
	if !cd.Cooling(10) {
		t.Fatalf("窗口内应冷却")
	}
	now = 5000 // 推进时钟越过窗口
	if cd.Cooling(10) {
		t.Fatalf("窗口过后应恢复")
	}
	out := cd.Apply([]Path{{RealModel: *mkReal(10, "a", "m1")}})
	if len(out) != 1 || out[0].CooledDown {
		t.Fatalf("到期后不应再标记冷却: %+v", out)
	}
}

// TestContractCooldownHealthSync 只读镜像 upstream Health（任务要点：
// 只读其状态不自造——cooling/degraded 排末尾，healthy 清失败记录）。
func TestContractCooldownHealthSync(t *testing.T) {
	view := &fakeHealth{states: map[int64]string{10: "cooling", 11: "degraded", 12: "healthy"}}
	cd := NewCooldowns().SetClock(func() int64 { return 1000 })
	cd.MarkFailure(12, 5000) // healthy 前先打失败标记

	cd.SyncFromHealth(view, []int64{10, 11, 12}, 5000)

	if !cd.Cooling(10) || !cd.Cooling(11) {
		t.Fatalf("cooling/degraded 都应排末尾")
	}
	if cd.Cooling(12) {
		t.Fatalf("healthy 应清失败记录")
	}
}

type fakeHealth struct{ states map[int64]string }

func (f *fakeHealth) StateOf(id int64) string { return f.states[id] }

// ============ 5. 切换预算 ============

// TestContractSwitchBudget C7 已吐可见内容不切候选（03-strategy.md §4.1
// 唯一硬约束：判定只读 VisibleWritten 入参——upstream.StreamStats 的字段，
// 本包不自造判定）。
func TestContractSwitchBudget(t *testing.T) {
	b := SwitchBudget{}
	if !b.CanSwitch(false, 0) {
		t.Fatalf("未吐内容且预算未耗应可切换")
	}
	if b.CanSwitch(true, 0) {
		t.Fatalf("已吐可见内容绝不可切换（C7 串话红线）")
	}
	if b.CanSwitch(false, DefaultMaxSwitches) {
		t.Fatalf("切换次数耗尽应不可切换")
	}
	b2 := SwitchBudget{MaxDurationMs: 1000, ElapsedMs: 2000}
	if b2.CanSwitch(false, 0) {
		t.Fatalf("总时长耗尽应不可切换")
	}
}

// ============ 6. trace ============

// TestContractTraceStructure x-fr-trace 输出结构（任务要点⑤：
// 回候选选择过程——过滤掉几个/为什么/排序后顺序/最终选谁）。
func TestContractTraceStructure(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: 1, Name: "smart", Status: StatusActive})
	st.cands[1] = []CandidateDecl{{RealModelID: 10}, {RealModelID: 11}}
	st.real[10] = mkReal(10, "a", "m1")
	st.real[11] = mkReal(11, "b", "m2")

	res, _, _ := NewResolver(st).Resolve(ctx(), "smart")
	cw := int64(65536)
	paths := []Path{
		{RealModel: *st.real[10], Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text"}}, ExpiresAt: 2000, BalanceKnown: true, Balance: 5},
		{RealModel: *st.real[11], Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text"}}, ExpiresAt: 1000, BalanceKnown: true, Balance: 3},
	}
	profile := RequestProfile{EstInputTokens: 10, MaxOutputTokens: 10}
	filtered, outs := FilterPaths(profile, paths)
	ordered := Rank(filtered, nil, nil)

	tr := BuildTrace(res, filtered, outs, ordered, nil)
	if tr.RequestedModel != "smart" {
		t.Fatalf("trace 应记请求模型名: %v", tr.RequestedModel)
	}
	if len(tr.Stages) != 3 {
		t.Fatalf("应有 resolve/filter/order 三阶段: %+v", tr.Stages)
	}
	if len(tr.Order) != 2 || tr.Order[0].RealModelID != 11 || tr.SelectedID != 11 {
		t.Fatalf("排序后最终选择应为 11: %+v sel=%d", tr.Order, tr.SelectedID)
	}
	if len(tr.Strategy) != 2 || tr.Strategy[0] != StrategyExpiryFirst {
		t.Fatalf("空链应展开默认链: %v", tr.Strategy)
	}
	// 头编码可往返。
	hv, err := tr.HeaderValue()
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	back, err := ParseTraceHeader(hv)
	if err != nil || back.SelectedID != 11 {
		t.Fatalf("头值应可解析回: %v %v", err, back)
	}
}

// TestContractTraceNoLeak trace 不泄露凭据与 base_url（02-abstract.md §6.3
// 两条约束——结构体编译期不含这些字段，RedactScan 形状自检兜底）。
func TestContractTraceNoLeak(t *testing.T) {
	tr := &Trace{
		RequestedModel: "smart",
		Stages:         []TraceStage{{Stage: "order", In: 1, Out: 1}},
		Order:          []TraceOrder{{RealModelID: 10, Channel: "deepseek-official", UpstreamModelID: "deepseek-flash"}},
		SelectedID:     10,
		Strategy:       DefaultChain,
	}
	hv, err := tr.HeaderValue()
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if hits := RedactScan(hv); len(hits) != 0 {
		t.Fatalf("trace 不应含敏感形状: %v in %s", hits, hv)
	}
	// 正面用例：渠道名与真实模型 id 是管理端可见范围（§6.3），不算泄露。
	if !strings.Contains(hv, "deepseek-official") {
		t.Fatalf("管理端 trace 应含渠道名: %s", hv)
	}
}

// TestContractPathShape Path 携带的排序键面（03-strategy.md §3.1 四种策略
// 的数据面——全部可测量外部事实，无键路径的排位语义）。
func TestContractPathShape(t *testing.T) {
	// speed 无样本（TTFTMs<0）排最后；price 恒有键（默认乘数 1）。
	paths := []Path{
		{RealModel: *mkReal(10, "a", "m1"), TTFTMs: -1, PriceMultiplier: 1},
		{RealModel: *mkReal(11, "b", "m2"), TTFTMs: 300, PriceMultiplier: 1},
	}
	out := Rank(paths, []string{StrategySpeed}, nil)
	if out[0].RealModel.ID != 11 {
		t.Fatalf("有样本在前: %v", out[0].RealModel.ID)
	}
	out = Rank(paths, []string{StrategyPrice}, nil)
	if len(out) != 2 {
		t.Fatalf("price 应全有键: %+v", out)
	}
}

// 帮手：fmt 引用保活（红actScan 用 strings；本行防 import 被裁）。
var _ = fmt.Sprintf
