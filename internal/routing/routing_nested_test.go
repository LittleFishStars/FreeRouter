package routing

// routing_nested_test.go —— 嵌套候选（抽象模型作为候选）的纯决策层契约。
//
// 用户需求（2026-11-01）：抽象模型可以把其他抽象模型加为自己的候选；运行时
// 按声明序展开它们的候选（分层回退）。循环依赖在**选择时隐藏**——成环的那条
// 候选被跳过，不报错、不连坐同层其它候选（与弃用链的降级哲学一致）。
//
// 这里钉的四条行为，每条都对应一次真实的误用方式：
//  1. 嵌套候选展开 = 目标模型的候选按声明序插入；
//  2. 直接成环（A→B→A）与间接成环（A→B→C→A）都只跳过成环那条；
//  3. 菱形依赖（A→B、A→C，B/C 都含 D）合法，D 去重保留首见位置；
//  4. 嵌套目标停用/不存在 → 该条跳过。

import (
	"testing"
)

// idsOfResolved 取展开结果的 real_model id 序列（断言用）。
func idsOfResolved(res *Resolved) []string {
	out := make([]string, 0, len(res.Candidates))
	for _, rm := range res.Candidates {
		out = append(out, rm.ID)
	}
	return out
}

func assertIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("候选序列 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("候选序列 = %v，期望 %v（第 %d 位 %s ≠ %s）", got, want, i, got[i], want[i])
		}
	}
}

// TestContractNestedCandidatesExpand 基本展开：A 的候选 = 真实模型 ra + 嵌套 B；
// B 的候选 = rb。请求 A 展开结果 = [ra, rb]，顺序按声明序。
func TestContractNestedCandidatesExpand(t *testing.T) {
	st := newFakeStore()
	a := st.addModel(&AbstractModel{ID: "amA", Name: "a", Status: StatusActive})
	b := st.addModel(&AbstractModel{ID: "amB", Name: "b", Status: StatusActive})
	st.real["ra"] = mkReal("ra", "ch1", "m1")
	st.real["rb"] = mkReal("rb", "ch2", "m2")
	st.cands["amA"] = []CandidateDecl{
		{TargetID: "ra", TargetKind: KindReal},
		{TargetID: "amB", TargetKind: KindAbstract},
	}
	st.cands["amB"] = []CandidateDecl{{TargetID: "rb", TargetKind: KindReal}}

	res, rerr, err := NewResolver(st).Resolve(ctx(), "a")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: %v %v", err, rerr)
	}
	assertIDs(t, idsOfResolved(res), "ra", "rb")

	// 约束跟着叶子走：嵌套进来的 rb 的约束必须能被找到（装配层 pathsOf
	// 靠 Resolved.Constraints 取声明约束，取不到=约束静默失效）。
	st.cands["amB"] = []CandidateDecl{{
		TargetID: "rb", TargetKind: KindReal,
		Constraint: Constraint{ChannelMode: "include", Channels: []string{"ch2"}},
	}}
	res, _, _ = NewResolver(st).Resolve(ctx(), "a")
	if got := res.Constraints["rb"]; got.ChannelMode != "include" {
		t.Fatalf("嵌套候选的约束应随叶子收集: %+v", res.Constraints)
	}
	_ = a
	_ = b
}

// TestContractNestedCycleHidden 选择时隐藏：A→B→A 直接环。请求 A 时，那条
// 指向 B 的候选被跳过（B 的其它候选不牵连），请求 B 时同理——环的两侧都
// 仍可路由，只是绕开成环的那条边。
func TestContractNestedCycleHidden(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: "amA", Name: "a", Status: StatusActive})
	st.addModel(&AbstractModel{ID: "amB", Name: "b", Status: StatusActive})
	st.real["ra"] = mkReal("ra", "ch1", "m1")
	st.real["rb"] = mkReal("rb", "ch2", "m2")
	// A: [ra, B(嵌套)]；B: [rb, A(嵌套)]——A→B→A 成环。
	st.cands["amA"] = []CandidateDecl{
		{TargetID: "ra", TargetKind: KindReal},
		{TargetID: "amB", TargetKind: KindAbstract},
	}
	st.cands["amB"] = []CandidateDecl{
		{TargetID: "rb", TargetKind: KindReal},
		{TargetID: "amA", TargetKind: KindAbstract},
	}

	// 请求 A：跳过 A→B 边？不——跳过的是「展开 B 时走回 A 的那条」。
	// A→B 本身不成环（B 展开时遇 A 在路径栈上，只跳 B→A 那条），所以
	// A 的展开 = ra + B 的展开（rb）。这正是「环不连坐」的语义：A→B 是
	// 合法的分层，只有 B→A 这条回头路被隐藏。
	res, rerr, err := NewResolver(st).Resolve(ctx(), "a")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: %v %v", err, rerr)
	}
	assertIDs(t, idsOfResolved(res), "ra", "rb")
}

// TestContractNestedIndirectCycle 间接环（A→B→C→A）：从 A 出发，C 里指向 A
// 的那条被隐藏；A 的展开仍含 B、C 的真实候选。
func TestContractNestedIndirectCycle(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: "amA", Name: "a", Status: StatusActive})
	st.addModel(&AbstractModel{ID: "amB", Name: "b", Status: StatusActive})
	st.addModel(&AbstractModel{ID: "amC", Name: "c", Status: StatusActive})
	st.real["ra"] = mkReal("ra", "ch1", "m1")
	st.real["rb"] = mkReal("rb", "ch2", "m2")
	st.real["rc"] = mkReal("rc", "ch3", "m3")
	st.cands["amA"] = []CandidateDecl{{TargetID: "amB", TargetKind: KindAbstract}}
	st.cands["amB"] = []CandidateDecl{{TargetID: "amC", TargetKind: KindAbstract}}
	st.cands["amC"] = []CandidateDecl{
		{TargetID: "rc", TargetKind: KindReal},
		{TargetID: "amA", TargetKind: KindAbstract}, // 回头路：隐藏这条
	}

	res, rerr, err := NewResolver(st).Resolve(ctx(), "a")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: %v %v", err, rerr)
	}
	assertIDs(t, idsOfResolved(res), "rc")
}

// TestContractNestedDiamondDedup 菱形依赖合法：A→B、A→C，B 与 C 都含 rd。
// rd 展开两次由 dedupe 收口（同一条上游路径不被连续尝试两次），保留首见位置。
func TestContractNestedDiamondDedup(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: "amA", Name: "a", Status: StatusActive})
	st.addModel(&AbstractModel{ID: "amB", Name: "b", Status: StatusActive})
	st.addModel(&AbstractModel{ID: "amC", Name: "c", Status: StatusActive})
	st.real["rb"] = mkReal("rb", "ch2", "m2")
	st.real["rc"] = mkReal("rc", "ch3", "m3")
	st.real["rd"] = mkReal("rd", "ch4", "m4")
	st.cands["amA"] = []CandidateDecl{
		{TargetID: "amB", TargetKind: KindAbstract},
		{TargetID: "amC", TargetKind: KindAbstract},
	}
	st.cands["amB"] = []CandidateDecl{
		{TargetID: "rb", TargetKind: KindReal},
		{TargetID: "rd", TargetKind: KindReal},
	}
	st.cands["amC"] = []CandidateDecl{
		{TargetID: "rc", TargetKind: KindReal},
		{TargetID: "rd", TargetKind: KindReal},
	}

	res, rerr, err := NewResolver(st).Resolve(ctx(), "a")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: %v %v", err, rerr)
	}
	assertIDs(t, idsOfResolved(res), "rb", "rd", "rc")
}

// TestContractNestedDeadTargetSkip 嵌套目标停用/不存在/自身为空 → 该条跳过，
// 同层的真实模型候选不受牵连。
func TestContractNestedDeadTargetSkip(t *testing.T) {
	st := newFakeStore()
	st.addModel(&AbstractModel{ID: "amA", Name: "a", Status: StatusActive})
	st.addModel(&AbstractModel{ID: "amDead", Name: "dead", Status: StatusDisabled})
	st.real["ra"] = mkReal("ra", "ch1", "m1")
	st.cands["amA"] = []CandidateDecl{
		{TargetID: "amDead", TargetKind: KindAbstract}, // 停用 → 跳过
		{TargetID: "amNope", TargetKind: KindAbstract}, // 不存在 → 跳过
		{TargetID: "ra", TargetKind: KindReal},
	}

	res, rerr, err := NewResolver(st).Resolve(ctx(), "a")
	if err != nil || rerr != nil {
		t.Fatalf("解析失败: %v %v", err, rerr)
	}
	assertIDs(t, idsOfResolved(res), "ra")

	// 全部候选都指向死目标 → no_candidate（503 语义），不是 500。
	st2 := newFakeStore()
	st2.addModel(&AbstractModel{ID: "amA2", Name: "a2", Status: StatusActive})
	st2.cands["amA2"] = []CandidateDecl{{TargetID: "amNope", TargetKind: KindAbstract}}
	_, rerr2, err2 := NewResolver(st2).Resolve(ctx(), "a2")
	if err2 != nil {
		t.Fatalf("存储错误不该出现: %v", err2)
	}
	if rerr2 == nil || rerr2.Code != "no_candidate" {
		t.Fatalf("应降级 no_candidate: %+v", rerr2)
	}
}
