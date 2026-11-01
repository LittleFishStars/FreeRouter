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
	models map[string]*AbstractModel  // by name
	byID   map[string]*AbstractModel  // by id
	cands  map[string][]CandidateDecl // by abstract model id
	real   map[string]*RealModelInfo  // by real model id
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		models: map[string]*AbstractModel{},
		byID:   map[string]*AbstractModel{},
		cands:  map[string][]CandidateDecl{},
		real:   map[string]*RealModelInfo{},
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

func (f *fakeStore) GetAbstractModel(ctx context.Context, id string) (*AbstractModel, error) {
	return f.byID[id], nil
}

func (f *fakeStore) ListCandidates(ctx context.Context, abstractModelID string) ([]CandidateDecl, error) {
	return f.cands[abstractModelID], nil
}

func (f *fakeStore) GetRealModel(ctx context.Context, id string) (*RealModelInfo, error) {
	return f.real[id], nil
}

// mkReal 构造一个正常可用的真实模型行。
func mkReal(id string, channel, modelID string) *RealModelInfo {
	return &RealModelInfo{
		ID: id, UpstreamID: "up-" + id, UpstreamModelID: modelID, UpstreamKind: "relay",
		Channel: channel, Status: "active", UpstreamEnabled: true,
		Modalities: []string{"text"}, Efforts: []string{"off", "low", "high"},
	}
}

// ptrI64 给上下文窗口等数值列用；id 已是 string，不再需要指针。
func ptrI64(v int64) *int64 { return &v }

// ptrStr 造 *string（ReplacementModelID 是可空 id）。
func ptrStr(v string) *string { return &v }

func ctx() context.Context { return context.Background() }

// ============ 1. 抽象模型解析 ============

// TestContractResolveActive 命中 active 模型（02-abstract.md §6.2：
// 请求抽象模型名 → 正常路由）。
func TestContractResolveActive(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: "am1", Name: "smart", Status: StatusActive})
	st.cands["am1"] = []CandidateDecl{{RealModelID: "rm10"}}
	st.real["rm10"] = mkReal("rm10", "deepseek-official", "deepseek-flash")

	r, rerr, err := NewResolver(st).Resolve(ctx(), "smart")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: err=%v rerr=%v", err, rerr)
	}
	if r.Model.Name != "smart" || r.Terminal.Name != "smart" {
		t.Fatalf("Model/Terminal 应同名: %v %v", r.Model.Name, r.Terminal.Name)
	}
	if len(r.Candidates) != 1 || r.Candidates[0].ID != "rm10" {
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
	st.addModel(&AbstractModel{ID: "am1", Name: "smart", Status: StatusActive})
	st.cands["am1"] = []CandidateDecl{{RealModelID: "rm10"}}
	st.real["rm10"] = mkReal("rm10", "deepseek-official", "deepseek-flash")
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
	st.addModel(&AbstractModel{ID: "am1", Name: "old", Status: StatusDisabled})
	_, rerr, err := NewResolver(st).Resolve(ctx(), "old")
	if err != nil || rerr == nil || rerr.Code != "model_not_found" {
		t.Fatalf("disabled 应 model_not_found: err=%v rerr=%+v", err, rerr)
	}
}

// TestContractDeprecationChain 弃用链走到 active（04-deprecation.md §2.1：
// Alice 旧配置 → A(old) → B(old) → C(active)；头里用 A、路由用 C）。
func TestContractDeprecationChain(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: "am1", Name: "a", Status: StatusDeprecated, ReplacementModelID: ptrStr("am2")})
	st.addModel(&AbstractModel{ID: "am2", Name: "b", Status: StatusDeprecated, ReplacementModelID: ptrStr("am3")})
	st.addModel(&AbstractModel{ID: "am3", Name: "c", Status: StatusActive})
	st.cands["am3"] = []CandidateDecl{{RealModelID: "rm30"}}
	st.real["rm30"] = mkReal("rm30", "cline", "big-model")

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
	if len(r.Candidates) != 1 || r.Candidates[0].ID != "rm30" {
		t.Fatalf("候选应来自终点 c: %+v", r.Candidates)
	}
}

// TestContractDeprecationCycleRejected 链成环拒绝（04-deprecation.md §2.4/2.5：
// 正常路径不可能（保存时只准指向 active），直接改库可造出环——运行时
// visited 兜底，降级为无替代不报错，CycleBroken 告警）。
func TestContractDeprecationCycleRejected(t *testing.T) {
	st := newFakeStore()
	// 直接改库口径：a→b、b→a 互相指向（状态机正常路径写不出来）。
	st.addModel(&AbstractModel{ID: "am1", Name: "a", Status: StatusDeprecated, ReplacementModelID: ptrStr("am2")})
	st.addModel(&AbstractModel{ID: "am2", Name: "b", Status: StatusDeprecated, ReplacementModelID: ptrStr("am1")})
	st.cands["am1"] = []CandidateDecl{{RealModelID: "rm10"}}
	st.real["rm10"] = mkReal("rm10", "deepseek-official", "deepseek-flash")

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
	st.addModel(&AbstractModel{ID: "am1", Name: "empty", Status: StatusActive})
	st.cands["am1"] = []CandidateDecl{{RealModelID: "rm99"}} // real 行不存在

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
	st.addModel(&AbstractModel{ID: "am1", Name: "smart", Status: StatusActive})
	st.cands["am1"] = []CandidateDecl{{RealModelID: "rm10"}, {RealModelID: "rm11"}, {RealModelID: "rm12"}, {RealModelID: "rm13"}}
	st.real["rm10"] = mkReal("rm10", "aggregate", "agg-model") // 闸①：标志命中
	st.real["rm10"].ExcludedFromCandidates = true
	st.real["rm11"] = mkReal("rm11", "deepseek-official", "flash") // 正常
	st.real["rm12"] = mkReal("rm12", "cline", "aggregate/x")       // 闸②：id 前缀命中、标志被清掉（改库）
	st.real["rm12"].ExcludedFromCandidates = false
	// 近似名不误伤：渠道段必须精确等于 aggregate（与 store 的 SQL 前缀
	// 判定同款口径——aggregate-v2/、my_aggregate/ 都不是聚合渠道）。
	st.real["rm13"] = mkReal("rm13", "aggregate-v2", "aggregate-v2/x")

	r, rerr, err := NewResolver(st).Resolve(ctx(), "smart")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: %v %v", err, rerr)
	}
	if len(r.Candidates) != 2 || r.Candidates[0].ID != "rm11" || r.Candidates[1].ID != "rm13" {
		t.Fatalf("aggregate 应全排除、近似名不误伤（标志+id 前缀两道）: %+v", r.Candidates)
	}
}

// ============ 2. 过滤 ============

// TestContractFilterCapability 能力过滤（03-strategy.md §2.1 F1：
// 本次请求需要的能力候选都必须支持；未知按不支持——02-abstract.md §4.3）。
func TestContractFilterCapability(t *testing.T) {
	cw := int64(131072)
	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1"), Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text"}, SupportsTools: true, Efforts: []string{"off", "low"}}},
		{RealModel: *mkReal("rm11", "b", "m2"), Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text", "image"}, SupportsTools: true, Efforts: []string{"off", "low", "high"}}},
		{RealModel: *mkReal("rm12", "c", "m3"), Caps: Capabilities{ContextWindow: &cw, Modalities: nil}}, // 模态未知
	}
	// 请求：文本+图片 + 工具 + high 档。
	profile := RequestProfile{NeedModalities: []string{"text", "image"}, EstInputTokens: 1000, MaxOutputTokens: 500, NeedTools: true, NeedEffort: "high"}

	kept, outs := FilterPaths(profile, paths)
	if len(kept) != 1 || kept[0].RealModel.ID != "rm11" {
		t.Fatalf("只有 11 满足全部要求: %+v", kept)
	}
	// 10 缺 image（在 modality 被剔）；12 模态未知按不支持剔。
	if len(outs) != 2 {
		t.Fatalf("应剔 2 条: %+v", outs)
	}
}

// TestContractFilterContext 上下文过滤（03-strategy.md §2.1 F2：
// 估算输入+声明最大输出 ≤ 候选上下文；窗口未知时**放行**（2026-10-30 生产实测改策：
// hub 目录只给裸 id，目录来源候选窗口恒未知，「未知即剔」等于整个目录池永远 503；
// 放行的最坏后果是上游报超限，可自愈）。
func TestContractFilterContext(t *testing.T) {
	small, big := int64(4096), int64(131072)
	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1"), Caps: Capabilities{ContextWindow: &small}},
		{RealModel: *mkReal("rm11", "b", "m2"), Caps: Capabilities{ContextWindow: &big}},
		{RealModel: *mkReal("rm12", "c", "m3"), Caps: Capabilities{ContextWindow: nil}}, // 未知 → 放行
	}
	profile := RequestProfile{EstInputTokens: 3000, MaxOutputTokens: 2000} // 需 5000

	kept, outs := FilterPaths(profile, paths)
	if len(kept) != 2 || kept[0].RealModel.ID != "rm11" || kept[1].RealModel.ID != "rm12" {
		t.Fatalf("11 窗口够、12 未知放行: kept=%+v", kept)
	}
	// 只有 10 窗口不够（context）。
	if len(outs) != 1 || outs[0].Reason != "context" || outs[0].PathID != "rm10" {
		t.Fatalf("应只剔 rm10 且原因为 context: %+v", outs)
	}
}

// TestContractFilterEmptyNoCandidate 过滤后为空 → 调用方走 503 no_candidate
// （03-strategy.md §2.3；FilterOut 带原因——「险些可用」诊断的原料）。
func TestContractFilterEmptyNoCandidate(t *testing.T) {
	cw := int64(4096)
	paths := []Path{{RealModel: *mkReal("rm10", "a", "m1"), Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text"}}}}
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
		{RealModel: *mkReal("rm10", "deepseek-official", "m1"), Constraint: Constraint{ChannelMode: "include", Channels: []string{"cline"}}},
		{RealModel: *mkReal("rm11", "cline", "m2"), Constraint: Constraint{ChannelMode: "include", Channels: []string{"cline"}}},
		{RealModel: *mkReal("rm12", "cline", "m3"), Constraint: Constraint{ChannelMode: "exclude", Channels: []string{"deepseek-official"}}},
	}
	for i := range paths {
		paths[i].Caps = Capabilities{ContextWindow: &cw, Modalities: []string{"text"}}
	}
	profile := RequestProfile{EstInputTokens: 10, MaxOutputTokens: 10}

	kept, outs := FilterPaths(profile, paths)
	// 10 被渠道 include 剔（它是 deepseek-official）；11/12 留。
	if len(kept) != 2 || kept[0].RealModel.ID != "rm11" || kept[1].RealModel.ID != "rm12" {
		t.Fatalf("渠道约束过滤错误: kept=%+v outs=%+v", kept, outs)
	}
}

// ============ 3. 排位（2026-11-01 定案：管理员手动排序） ============

// TestContractManualOrderKeepDeclared 候选的使用序就是声明序，一个字符都不重排。
//
// 这是手动排序的**定义性**契约：入参切片的先后 = 管理員在候选集编辑器里
// 排的先后，Order 不比较任何东西（没有比较器就没有「稳定排序」这回事）。
// 曾存在的四种自动策略（expiry-first/balance/speed/price）已整体退役。
func TestContractManualOrderKeepDeclared(t *testing.T) {
	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1")},
		{RealModel: *mkReal("rm11", "b", "m2")},
		{RealModel: *mkReal("rm12", "c", "m3")},
		{RealModel: *mkReal("rm13", "d", "m4")},
	}
	out := Order(paths)
	want := []string{"rm10", "rm11", "rm12", "rm13"}
	for i := range want {
		if out[i].RealModel.ID != want[i] {
			t.Fatalf("声明序被改动: got[%d]=%s want=%s（全序列：%v）",
				i, out[i].RealModel.ID, want[i], idsOf(out))
		}
	}
}

// TestContractManualOrderUnavailableTail 不可用路径排末尾而非剔除。
//
// 「跳过但保留兜底」是用户定案的原话：不可用的候选不该占用首选位次，但
// 也**不能**消失——一次误判（上游抖动/临时 401）不该让整个池子 503。
func TestContractManualOrderUnavailableTail(t *testing.T) {
	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1"), Unavailable: true},
		{RealModel: *mkReal("rm11", "b", "m2")},
		{RealModel: *mkReal("rm12", "c", "m3"), Unavailable: true},
		{RealModel: *mkReal("rm13", "d", "m4")},
	}
	out := Order(paths)
	// 在册的保持声明序在前；不可用的保持声明序在后（不剔除）。
	want := []string{"rm11", "rm13", "rm10", "rm12"}
	if got := idsOf(out); !equalIDs(got, want) {
		t.Fatalf("不可用排末尾错误: got=%v want=%v", got, want)
	}
}

// TestContractManualOrderAllUnavailableStillRoutes 全池不可用时**仍然返回全部**。
//
// 这是「保留兜底」的极限情形：全部候选都在不可用台账里时，Order 仍要给出
// 完整序列（依次试一遍），而不是返回空切片。返回空 = no_candidate(503)，
// 而「全都不在册」恰恰是最该试一次的场合（台账可能已经过时）。
func TestContractManualOrderAllUnavailableStillRoutes(t *testing.T) {
	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1"), Unavailable: true},
		{RealModel: *mkReal("rm11", "b", "m2"), Unavailable: true},
	}
	out := Order(paths)
	if len(out) != 2 {
		t.Fatalf("全不可用时应保留全部候选做兜底，实得 %d 条", len(out))
	}
	if got := idsOf(out); !equalIDs(got, []string{"rm10", "rm11"}) {
		t.Fatalf("全不可用时仍按声明序: got=%v", got)
	}
}

// TestContractManualOrderDeterminism 确定性：同样输入必须同样输出。
//
// 手动序的确定性是**结构性**的（没有比较器、没有测量输入），这条测试是
// 那个性质的照妖镜：任何人后来往 Order 里塞一个「按延迟微调」的比较器，
// 这里就会因为后台样本变化而漂移。
func TestContractManualOrderDeterminism(t *testing.T) {
	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1")},
		{RealModel: *mkReal("rm11", "b", "m2"), Unavailable: true},
		{RealModel: *mkReal("rm12", "c", "m3")},
	}
	first := idsOf(Order(paths))
	for i := 0; i < 5; i++ {
		if got := idsOf(Order(paths)); !equalIDs(got, first) {
			t.Fatalf("第 %d 次排位漂移: %v vs %v", i, got, first)
		}
	}
}

// TestContractManualOrderNoAliasing 返回副本：改返回值不能污染入参。
//
// 调用方手里的 paths 还会被 trace/诊断复用；若 Order 返回同一段底层数组，
// 「排位前」与「排位后」就变成同一个观察点，日志再也回答不了「它本来在哪」。
func TestContractManualOrderNoAliasing(t *testing.T) {
	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1")},
		{RealModel: *mkReal("rm11", "b", "m2"), Unavailable: true},
	}
	out := Order(paths)
	out[0].RealModel.ID = "mutated"
	if paths[0].RealModel.ID == "mutated" {
		t.Fatal("Order 返回了入参的别名（改返回值污染了调用方数据）")
	}
	if len(paths) != 2 || paths[0].RealModel.ID != "rm10" {
		t.Fatalf("入参被就地改动: %+v", paths)
	}
}

// ============ 4. 冷却 ============

// TestContractCooldownTailNotRemoved 冷却候选排末尾而非剔除（lead 指令④：
// 最后一个候选冷却时剔除会 no_candidate——「冷却」意味着失败过但可能恢复，
// 没有更好选择时它还是要顶上）。
func TestContractCooldownTailNotRemoved(t *testing.T) {
	cd := NewCooldowns().SetClock(func() int64 { return 1000 })
	cd.MarkFailure("rm11", 5000) // 候选 11 冷却中

	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1")},
		{RealModel: *mkReal("rm11", "b", "m2")},
		{RealModel: *mkReal("rm12", "c", "m3")},
	}
	out := cd.Apply(paths)
	if len(out) != 3 {
		t.Fatalf("冷却不应剔除: %d", len(out))
	}
	if out[2].RealModel.ID != "rm11" || !out[2].CooledDown {
		t.Fatalf("冷却候选应排末尾且带标记: %+v", out)
	}
	// 唯一候选冷却 → 仍在场（不 no_candidate）。
	solo := cd.Apply([]Path{{RealModel: *mkReal("rm11", "b", "m2")}})
	if len(solo) != 1 || !solo[0].CooledDown {
		t.Fatalf("唯一冷却候选应保留: %+v", solo)
	}
}

// TestContractCooldownExpiry 冷却到期恢复（冷却是短窗运行态，不是熔断）。
func TestContractCooldownExpiry(t *testing.T) {
	now := int64(1000)
	cd := NewCooldowns().SetClock(func() int64 { return now }).
		SetJitter(func() int64 { return 0 }) // 钉死抖动：真实抖动 0..10s 会让窗口断言随机失败
	cd.MarkFailure("rm10", 1000) // 窗口 1s
	if !cd.Cooling("rm10") {
		t.Fatalf("窗口内应冷却")
	}
	now = 5000 // 推进时钟越过窗口
	if cd.Cooling("rm10") {
		t.Fatalf("窗口过后应恢复")
	}
	out := cd.Apply([]Path{{RealModel: *mkReal("rm10", "a", "m1")}})
	if len(out) != 1 || out[0].CooledDown {
		t.Fatalf("到期后不应再标记冷却: %+v", out)
	}
}

// TestContractCooldownHealthSync 只读镜像 upstream Health（任务要点：
// 只读其状态不自造——cooling/degraded 排末尾，healthy 清失败记录）。
func TestContractCooldownHealthSync(t *testing.T) {
	view := &fakeHealth{states: map[string]string{"rm10": "cooling", "rm11": "degraded", "rm12": "healthy"}}
	cd := NewCooldowns().SetClock(func() int64 { return 1000 })
	cd.MarkFailure("rm12", 5000) // healthy 前先打失败标记

	cd.SyncFromHealth(view, []string{"rm10", "rm11", "rm12"}, 5000)

	if !cd.Cooling("rm10") || !cd.Cooling("rm11") {
		t.Fatalf("cooling/degraded 都应排末尾")
	}
	if cd.Cooling("rm12") {
		t.Fatalf("healthy 应清失败记录")
	}
}

type fakeHealth struct{ states map[string]string }

func (f *fakeHealth) StateOf(id string) string { return f.states[id] }

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
	st.addModel(&AbstractModel{ID: "am1", Name: "smart", Status: StatusActive})
	st.cands["am1"] = []CandidateDecl{{RealModelID: "rm10"}, {RealModelID: "rm11"}}
	st.real["rm10"] = mkReal("rm10", "a", "m1")
	st.real["rm11"] = mkReal("rm11", "b", "m2")

	res, _, _ := NewResolver(st).Resolve(ctx(), "smart")
	cw := int64(65536)
	paths := []Path{
		{RealModel: *st.real["rm11"], Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text"}}},
		{RealModel: *st.real["rm10"], Caps: Capabilities{ContextWindow: &cw, Modalities: []string{"text"}}},
	}
	profile := RequestProfile{EstInputTokens: 10, MaxOutputTokens: 10}
	filtered, outs := FilterPaths(profile, paths)
	// 手动序：入参切片的先后就是管理员的声明序（这里宣告 rm11 在前）。
	ordered := Order(filtered)

	tr := BuildTrace(res, filtered, outs, ordered)
	if tr.RequestedModel != "smart" {
		t.Fatalf("trace 应记请求模型名: %v", tr.RequestedModel)
	}
	if len(tr.Stages) != 3 {
		t.Fatalf("应有 resolve/filter/order 三阶段: %+v", tr.Stages)
	}
	if len(tr.Order) != 2 || tr.Order[0].RealModelID != "rm11" || tr.SelectedID != "rm11" {
		t.Fatalf("手动序的第一位应为 rm11: %+v sel=%s", tr.Order, tr.SelectedID)
	}
	if tr.OrderMode != OrderModeManual {
		t.Fatalf("排位模式必须是 manual（策略链已退役）: %q", tr.OrderMode)
	}
	// 头编码可往返。
	hv, err := tr.HeaderValue()
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	back, err := ParseTraceHeader(hv)
	if err != nil || back.SelectedID != "rm11" {
		t.Fatalf("头值应可解析回: %v %v", err, back)
	}
}

// TestContractTraceNoLeak trace 不泄露凭据与 base_url（02-abstract.md §6.3
// 两条约束——结构体编译期不含这些字段，RedactScan 形状自检兜底）。
func TestContractTraceNoLeak(t *testing.T) {
	tr := &Trace{
		RequestedModel: "smart",
		Stages:         []TraceStage{{Stage: "order", In: 1, Out: 1}},
		Order:          []TraceOrder{{RealModelID: "rm10", Channel: "deepseek-official", UpstreamModelID: "deepseek-flash"}},
		SelectedID:     "rm10",
		OrderMode:      OrderModeManual,
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

// TestContractPathShape Path 的手动序面（2026-11-01 定案后不再有排序键）。
//
// 这条钉住的是「Path 只有排位标记，没有可测量量」：Unavailable 是唯一
// 影响位次的字段，且它只改变位次、不改变成员（跳过但保留兜底）。
func TestContractPathShape(t *testing.T) {
	paths := []Path{
		{RealModel: *mkReal("rm10", "a", "m1")},
		{RealModel: *mkReal("rm11", "b", "m2"), Unavailable: true},
	}
	out := Order(paths)
	if len(out) != 2 {
		t.Fatalf("不可用不剔除: %d", len(out))
	}
	if out[0].RealModel.ID != "rm10" || !out[1].Unavailable {
		t.Fatalf("在册在前、不可用在后: %+v", out)
	}
	// 空切片安全。
	if got := Order(nil); got != nil {
		t.Fatalf("空入参应返回 nil: %+v", got)
	}
}

// 帮手：fmt 引用保活（红actScan 用 strings；本行防 import 被裁）。
var _ = fmt.Sprintf

// idsOf 取序列的 real_model 行 id（排位断言用）。
func idsOf(paths []Path) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, p.RealModel.ID)
	}
	return out
}

// equalIDs 逐位比较两个 id 序列。
func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
