// task-15 契约测试：组 CRUD/成员/策略、有效策略合成（纯函数直测）、
// 鉴定四态写入。全部 t.TempDir()，不污染仓库。
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
)

// mustGroup 建组（失败即 fatal）。
func mustGroup(t *testing.T, s *Store, name string) string {
	t.Helper()
	id, err := s.Repos().Groups.CreateGroup(context.Background(), name, "", false)
	if err != nil {
		t.Fatalf("建测试组 %q 失败: %v", name, err)
	}
	return id
}

var testAcctSeq int

// addTenantAccount 建 tenant 账户（AddMember 的类校验要求）。
// 用户名带原子序号：同一测试同秒建多个账户时 now() 粒度不够会撞 UNIQUE。
func addTenantAccount(t *testing.T, s *Store) string {
	t.Helper()
	testAcctSeq++
	id, err := s.Repos().Accounts.CreateAccount(context.Background(),
		AccountAuth{Account: Account{DisplayName: "t", Username: fmt.Sprintf("acct-%s-%d-%d", t.Name(), now(), testAcctSeq)},
			PasswordHash: "x"}, []Class{ClassTenant})
	if err != nil {
		t.Fatalf("建 tenant 账户失败: %v", err)
	}
	return id
}

// gSetPolicy 写数字策略（测试便捷封装，值走 JSON 整数）。
func gSetPolicy(t *testing.T, s *Store, gid string, key PolicyKey, v int64) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := s.Repos().Groups.SetPolicy(context.Background(), gid, key, string(raw)); err != nil {
		t.Fatalf("设策略 %s=%d 失败: %v", key, v, err)
	}
}

// gSetBoolPolicy 写布尔策略。
func gSetBoolPolicy(t *testing.T, s *Store, gid string, key PolicyKey, v bool) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := s.Repos().Groups.SetPolicy(context.Background(), gid, key, string(raw)); err != nil {
		t.Fatalf("设布尔策略 %s=%v 失败: %v", key, v, err)
	}
}

// gSetClassList 写类别列表策略。
func gSetClassList(t *testing.T, s *Store, gid string, classes []string) {
	t.Helper()
	raw, _ := json.Marshal(classes)
	if err := s.Repos().Groups.SetPolicy(context.Background(), gid, PolicyModelClass, string(raw)); err != nil {
		t.Fatalf("设类别策略 %v 失败: %v", classes, err)
	}
}

// ---- 1. 组 CRUD 幂等 ----

// TestContractGroupCRUD：建/查/改名/重名拒绝/删组 CASCADE 带走策略与成员关系/
// 默认组拒删。
func TestContractGroupCRUD(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	// 建默认组（首个 is_default=1 合法）。
	defID, err := s.Repos().Groups.CreateGroup(ctx, "default", "默认组", true)
	if err != nil {
		t.Fatalf("建默认组失败: %v", err)
	}
	// 第二个默认组：拒绝。
	if _, err := s.Repos().Groups.CreateGroup(ctx, "def2", "", true); !errors.Is(err, ErrDefaultGroupProtected) {
		t.Errorf("第二个默认组应报 ErrDefaultGroupProtected，得到 %v", err)
	}

	g := mustGroup(t, s, "g1")
	// GetGroupByName 命中。
	got, err := s.Repos().Groups.GetGroupByName(ctx, "g1")
	if err != nil || got.ID != g {
		t.Fatalf("按名查组 = %v, %v", got, err)
	}
	// 改名。
	if err := s.Repos().Groups.UpdateGroup(ctx, g, "g1-renamed", "新描述"); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	got, _ = s.Repos().Groups.GetGroup(ctx, g)
	if got.Name != "g1-renamed" || got.Description != "新描述" {
		t.Errorf("改名后 = %+v", got)
	}
	// 重名：UNIQUE 拒绝。
	mustGroup(t, s, "other")
	if err := s.Repos().Groups.UpdateGroup(ctx, g, "other", ""); err == nil {
		t.Error("改成重名应拒绝（ux_groups_name）")
	}

	// 策略 + 成员后删组：CASCADE 全带走。
	acct := addTenantAccount(t, s)
	if err := s.Repos().Groups.AddMember(ctx, g, acct); err != nil {
		t.Fatalf("加成员失败: %v", err)
	}
	gSetPolicy(t, s, g, PolicyQuotaTokenDay, 100)
	if err := s.Repos().Groups.DeleteGroup(ctx, g); err != nil {
		t.Fatalf("删组失败: %v", err)
	}
	var n int
	_ = s.writeDB.QueryRow(`SELECT COUNT(*) FROM group_policies WHERE group_id = ?`, g).Scan(&n)
	if n != 0 {
		t.Errorf("删组后组策略残留 %d 行", n)
	}
	_ = s.writeDB.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_id = ?`, g).Scan(&n)
	if n != 0 {
		t.Errorf("删组后成员残留 %d 行", n)
	}
	if _, err := s.Repos().Groups.GetGroup(ctx, g); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("删组后查询应 ErrGroupNotFound，得到 %v", err)
	}
	// 删不存在的组：ErrGroupNotFound。
	if err := s.Repos().Groups.DeleteGroup(ctx, g); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("删不存在的组应 ErrGroupNotFound，得到 %v", err)
	}
	// 默认组拒删。
	if err := s.Repos().Groups.DeleteGroup(ctx, defID); !errors.Is(err, ErrDefaultGroupProtected) {
		t.Errorf("删默认组应 ErrDefaultGroupProtected，得到 %v", err)
	}
}

// ---- 2. 成员增删 ----

// TestContractGroupMembership：tenant 可入组幂等；非 tenant 拒绝；
// 移出幂等；ListMembers 升序。
func TestContractGroupMembership(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	g := mustGroup(t, s, "mg")

	a1 := addTenantAccount(t, s)
	a2 := addTenantAccount(t, s)
	// 重复加：幂等无错。
	for i := 0; i < 2; i++ {
		if err := s.Repos().Groups.AddMember(ctx, g, a1); err != nil {
			t.Fatalf("第 %d 次加成员失败: %v", i+1, err)
		}
	}
	if err := s.Repos().Groups.AddMember(ctx, g, a2); err != nil {
		t.Fatalf("加第二个成员失败: %v", err)
	}
	var n int
	_ = s.writeDB.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_id = ?`, g).Scan(&n)
	if n != 2 {
		t.Errorf("重复加成员后应仍 2 行（幂等），实际 %d", n)
	}
	members, err := s.Repos().Groups.ListMembers(ctx, g)
	// 「升序」= account_id 字典序（UUID 主键后不再有 id 序=插入序）。
	if err != nil || len(members) != 2 {
		t.Fatalf("成员列表 = %v, %v，期望 2 个", members, err)
	}
	if (members[0] != a1 || members[1] != a2) && (members[0] != a2 || members[1] != a1) {
		t.Fatalf("成员集合 = %v，期望 {%s, %s}", members, a1, a2)
	}

	// 非 tenant 账户拒绝入组。
	admin, err := s.Repos().Accounts.CreateAccount(ctx,
		AccountAuth{Account: Account{DisplayName: "a", Username: "admin-" + t.Name()}, PasswordHash: "x"},
		[]Class{ClassAdmin})
	if err != nil {
		t.Fatalf("建 admin 失败: %v", err)
	}
	if err := s.Repos().Groups.AddMember(ctx, g, admin); err == nil {
		t.Error("非 tenant 账户入组应拒绝")
	}

	// 移出幂等。
	if err := s.Repos().Groups.RemoveMember(ctx, g, a1); err != nil {
		t.Fatalf("移出失败: %v", err)
	}
	if err := s.Repos().Groups.RemoveMember(ctx, g, a1); err != nil {
		t.Fatalf("重复移出应幂等: %v", err)
	}
	members, _ = s.Repos().Groups.ListMembers(ctx, g)
	if len(members) != 1 || members[0] != a2 {
		t.Errorf("移出后成员 = %v，期望 [%s]", members, a2)
	}
}

// ---- 3. 策略值形状校验 ----

// TestContractPolicyValueValidation：布尔键收布尔、数字键收整数、类别键收
// 字符串数组；坏形状拒绝；未知键拒绝；删除后回到未设。
func TestContractPolicyValueValidation(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	g := mustGroup(t, s, "pv")

	// 正常三形状。
	gSetBoolPolicy(t, s, g, PolicyCanCreateKey, true)
	gSetPolicy(t, s, g, PolicyRPMLimit, 60)
	gSetClassList(t, s, g, []string{"smart", "fast"})

	// 坏形状。
	if err := s.Repos().Groups.SetPolicy(ctx, g, PolicyCanCreateKey, `"true"`); err == nil {
		t.Error("布尔键收字符串应拒绝")
	}
	if err := s.Repos().Groups.SetPolicy(ctx, g, PolicyRPMLimit, `60.5`); err == nil {
		t.Error("数字键收浮点应拒绝")
	}
	if err := s.Repos().Groups.SetPolicy(ctx, g, PolicyModelClass, `"smart"`); err == nil {
		t.Error("类别键收单个字符串应拒绝")
	}
	// 未知键。
	if err := s.Repos().Groups.SetPolicy(ctx, g, PolicyKey("no_such_key"), `1`); err == nil {
		t.Error("未知键应拒绝")
	}
	// 读回。
	v, err := s.Repos().Groups.GetPolicy(ctx, g, PolicyRPMLimit)
	if err != nil || v != "60" {
		t.Fatalf("读回 rpm_limit = %q, %v", v, err)
	}
	// 删除 → 未设。
	if err := s.Repos().Groups.DeletePolicy(ctx, g, PolicyRPMLimit); err != nil {
		t.Fatalf("删策略失败: %v", err)
	}
	v, err = s.Repos().Groups.GetPolicy(ctx, g, PolicyRPMLimit)
	if err != nil || v != "" {
		t.Fatalf("删后应未设（空串 nil），得到 %q, %v", v, err)
	}
	// 重复删：幂等。
	if err := s.Repos().Groups.DeletePolicy(ctx, g, PolicyRPMLimit); err != nil {
		t.Fatalf("重复删策略应幂等: %v", err)
	}
	// -1（显式不限）是合法数字值。
	gSetPolicy(t, s, g, PolicyRPMLimit, -1)
	v, _ = s.Repos().Groups.GetPolicy(ctx, g, PolicyRPMLimit)
	if v != "-1" {
		t.Errorf("-1 应原样存取，得到 %q", v)
	}
}

// ---- 4-7. 合成（纯函数直测，不开库） ----

// mkSnapshot 构造两组合成的最小快照。
func mkSnapshot(groups []GroupPolicySet, accountRPM int64) PolicySnapshot {
	return PolicySnapshot{
		AccountID:             tid5,
		Classes:               []Class{ClassTenant},
		Groups:                groups,
		AccountRPMLimit:       accountRPM,
		AccountTPMLimit:       -1,
		AccountMaxConcurrency: -1,
	}
}

func boolPtr(b bool) *bool  { return &b }
func intPtr(i int64) *int64 { return &i }

// TestContractComposeUnion：放开类并集——布尔任一 false 即 false、
// 全 true 才 true；MaxKeys 取 max；类别并集排序去重。
func TestContractComposeUnion(t *testing.T) {
	g1 := GroupPolicySet{GroupID: "g1", CanCreateKey: boolPtr(true), KeyAllowlistWiden: boolPtr(true),
		MaxKeys: intPtr(5), ModelClasses: []string{"fast", "smart"}}
	g2 := GroupPolicySet{GroupID: "g2", CanCreateKey: boolPtr(true), KeyAllowlistWiden: boolPtr(false),
		MaxKeys: intPtr(10), ModelClasses: []string{"smart", "vision"}}

	ep := ComposeEffectivePolicy(mkSnapshot([]GroupPolicySet{g1, g2}, -1))
	if !ep.CanCreateKey {
		t.Error("两组都 true，CanCreateKey 应 true")
	}
	if ep.KeyAllowlistWiden {
		t.Error("g2 false，KeyAllowlistWiden 应 false（deny 优先）")
	}
	if ep.MaxKeys != 10 {
		t.Errorf("MaxKeys 应取 max=10，得到 %d", ep.MaxKeys)
	}
	want := []string{"fast", "smart", "vision"}
	if len(ep.ModelClasses) != 3 {
		t.Errorf("类别并集 = %v，期望 %v", ep.ModelClasses, want)
	}
	for i, w := range want {
		if ep.ModelClasses[i] != w {
			t.Errorf("类别并集[%d] = %q ≠ %q", i, ep.ModelClasses[i], w)
		}
	}

	// 任一组 -1（不限）支配 max。
	g2.MaxKeys = intPtr(-1)
	ep = ComposeEffectivePolicy(mkSnapshot([]GroupPolicySet{g1, g2}, -1))
	if ep.MaxKeys != -1 {
		t.Errorf("任一组 -1 → MaxKeys 应 -1，得到 %d", ep.MaxKeys)
	}

	// 未设组（nil 字段）与无组：布尔 fail-closed。
	g3 := GroupPolicySet{GroupID: "g3"} // 全未设
	ep = ComposeEffectivePolicy(mkSnapshot([]GroupPolicySet{g1, g3}, -1))
	if ep.CanCreateKey {
		t.Error("存在未设组 → CanCreateKey 应 false（fail-closed）")
	}
	ep = ComposeEffectivePolicy(mkSnapshot(nil, -1))
	if ep.CanCreateKey || ep.KeyAllowlistWiden {
		t.Error("无组 → 布尔应 false")
	}
	if ep.MaxKeys != -1 {
		t.Errorf("无组 MaxKeys 应 -1，得到 %d", ep.MaxKeys)
	}
}

// TestContractBooleanFailClosedMatrix：布尔放开类（can_create_key /
// key_allowlist_widen）双组三态全组合矩阵。语义（effective_policy.go
// allGroupsAllow）：任一组显式 false → false（deny 优先）；全部组显式
// true → true；混入任一未设组 → false（fail-closed：「没有全员明确
// 允许就是不允许」）。两个布尔键走同一实现，逐一扫防单键特化回归。
func TestContractBooleanFailClosedMatrix(t *testing.T) {
	compose := func(groups []GroupPolicySet) EffectivePolicy {
		return ComposeEffectivePolicy(mkSnapshot(groups, -1))
	}
	states := []struct {
		name string
		v    *bool
	}{
		{"未设", nil},
		{"true", boolPtr(true)},
		{"false", boolPtr(false)},
	}
	selects := []struct {
		name string
		set  func(*GroupPolicySet, *bool)
		read func(EffectivePolicy) bool
	}{
		{"CanCreateKey",
			func(g *GroupPolicySet, v *bool) { g.CanCreateKey = v },
			func(ep EffectivePolicy) bool { return ep.CanCreateKey }},
		{"KeyAllowlistWiden",
			func(g *GroupPolicySet, v *bool) { g.KeyAllowlistWiden = v },
			func(ep EffectivePolicy) bool { return ep.KeyAllowlistWiden }},
	}

	for _, sel := range selects {
		t.Run(sel.name, func(t *testing.T) {
			// 双组 3×3 全组合：仅 (true,true) 放行，其余全部 false。
			for _, a := range states {
				for _, b := range states {
					g1 := GroupPolicySet{GroupID: "g1"}
					g2 := GroupPolicySet{GroupID: "g2"}
					sel.set(&g1, a.v)
					sel.set(&g2, b.v)
					got := sel.read(compose([]GroupPolicySet{g1, g2}))
					want := a.v != nil && b.v != nil && *a.v && *b.v
					if got != want {
						t.Errorf("组(A=%s, B=%s) → %v，期望 %v（deny 优先+fail-closed）",
							a.name, b.name, got, want)
					}
				}
			}
			// 顺序无关：(true,未设) 与 (未设,true) 结论一致——矩阵已双向
			// 覆盖，这里再显式断言一次防实现引入顺序依赖。
			g1, g2 := GroupPolicySet{GroupID: "g1"}, GroupPolicySet{GroupID: "g2"}
			sel.set(&g1, boolPtr(true))
			if sel.read(compose([]GroupPolicySet{g1, g2})) {
				t.Error("(true, 未设) 应 false")
			}
			// 单组：true→true；false→false；未设→false。
			for _, s := range states {
				g := GroupPolicySet{GroupID: "g1"}
				sel.set(&g, s.v)
				got := sel.read(compose([]GroupPolicySet{g}))
				want := s.v != nil && *s.v
				if got != want {
					t.Errorf("单组(%s) → %v，期望 %v", s.name, got, want)
				}
			}
			// 三组混入未设：T+T+未设 → false（矩阵核心边界：多数派允许
			// 不能掩盖单个未设组）。
			g1, g2, g3 := GroupPolicySet{GroupID: "g1"}, GroupPolicySet{GroupID: "g2"}, GroupPolicySet{GroupID: "g3"}
			sel.set(&g1, boolPtr(true))
			sel.set(&g2, boolPtr(true))
			if sel.read(compose([]GroupPolicySet{g1, g2, g3})) {
				t.Error("T+T+未设 → 应 false（fail-closed）")
			}
		})
	}
}

// TestContractComposeMin：限制类逐键 min、-1 不参与、未设键不出现、
// 0 是合法最严值。
func TestContractComposeMin(t *testing.T) {
	g1 := GroupPolicySet{GroupID: "g1", Numbers: map[PolicyKey]int64{
		PolicyQuotaTokenDay:   100,
		PolicyQuotaTokenMonth: 5000,
		PolicyRPMLimit:        60,
		PolicyQuotaRequestDay: 0, // 零额度全拒
	}}
	g2 := GroupPolicySet{GroupID: "g2", Numbers: map[PolicyKey]int64{
		PolicyQuotaTokenDay:  200, // 更宽，不生效
		PolicyRPMLimit:       -1,  // 不限，不参与
		PolicyTPMLimit:       90000,
		PolicyMaxConcurrency: 4,
	}}

	ep := ComposeEffectivePolicy(mkSnapshot([]GroupPolicySet{g1, g2}, 30))
	// token/day: min(100,200)=100
	if v := ep.QuotaLimits[QuotaKey{UnitToken, PeriodDay}]; v != 100 {
		t.Errorf("token/day 应 min=100，得到 %d", v)
	}
	// token/month 只有 g1 设：5000（不是 -1，也不落回账户）
	if v := ep.QuotaLimits[QuotaKey{UnitToken, PeriodMonth}]; v != 5000 {
		t.Errorf("token/month 只有 g1 设应 5000，得到 %d", v)
	}
	// request/day: 0 合法（零额度全拒）
	if v := ep.QuotaLimits[QuotaKey{UnitRequest, PeriodDay}]; v != 0 {
		t.Errorf("request/day 应 0（合法全拒），得到 %d", v)
	}
	// credit/day 两组都没设：键不出现（= 不限，不是 -1 键）
	if _, ok := ep.QuotaLimits[QuotaKey{UnitCredit, PeriodDay}]; ok {
		t.Error("credit/day 未设不应出现在 QuotaLimits")
	}
	// RPM: g2 的 -1 不参与 → min 只有 g1 的 60。
	if ep.RPMLimit != 60 {
		t.Errorf("RPM 应 60（-1 不参与 min），得到 %d", ep.RPMLimit)
	}
	// TPM: 只有 g2 设 → 90000。
	if ep.TPMLimit != 90000 {
		t.Errorf("TPM 应 90000，得到 %d", ep.TPMLimit)
	}
	// 并发：只有 g2 设 → 4。
	if ep.MaxConcurrency != 4 {
		t.Errorf("并发应 4，得到 %d", ep.MaxConcurrency)
	}

	// 全部组未设 RPM → 落回账户列 30。
	ep = ComposeEffectivePolicy(mkSnapshot([]GroupPolicySet{{GroupID: "g9"}}, 30))
	if ep.RPMLimit != 30 {
		t.Errorf("全部未设应落回账户列 30，得到 %d", ep.RPMLimit)
	}
}

// TestContractComposeDeterminism：同一快照两次合成结果一致（纯函数红线）。
func TestContractComposeDeterminism(t *testing.T) {
	g1 := GroupPolicySet{GroupID: "g1", CanCreateKey: boolPtr(true), MaxKeys: intPtr(3),
		Numbers: map[PolicyKey]int64{PolicyQuotaTokenDay: 100, PolicyRPMLimit: 60}}
	snap := PolicySnapshot{
		AccountID: tid5, Classes: []Class{ClassTenant},
		VisibleModels:   []string{"5", "3", "3", "9"},
		Groups:          []GroupPolicySet{g1},
		ModelCaps:       []ModelCap{{GroupID: "g1", AbstractModelID: "3", Unit: UnitRequest, Period: PeriodDay, Cap: 50}},
		Pools:           []PoolRef{{GroupID: "g2", AbstractModelID: TotalScope, Unit: UnitToken, Period: PeriodDay, Limit: 1000}},
		AccountRPMLimit: -1, AccountTPMLimit: -1, AccountMaxConcurrency: -1,
	}
	a := ComposeEffectivePolicy(snap)
	b := ComposeEffectivePolicy(snap)
	if fmt.Sprintf("%+v", a) != fmt.Sprintf("%+v", b) {
		t.Fatalf("同一快照两次合成不一致:\n%+v\n%+v", a, b)
	}
	// 输入乱序不影响集合结果（切片显式排序兜底）。
	snap2 := snap
	snap2.VisibleModels = []string{"9", "3", "5", "3"}
	if got := ComposeEffectivePolicy(snap2).VisibleModels; fmt.Sprint(got) != "[3 5 9]" {
		t.Errorf("可见模型应排序去重 [3 5 9]，得到 %v", got)
	}
}

// TestContractComposeModelCapsAndPools：按模型上限逐 (model,unit,period)
// 取 min；无行不限；池原样收集不合并。
func TestContractComposeModelCapsAndPools(t *testing.T) {
	snap := mkSnapshot([]GroupPolicySet{{GroupID: "g1"}}, -1)
	snap.ModelCaps = []ModelCap{
		{GroupID: "g1", AbstractModelID: "3", Unit: UnitRequest, Period: PeriodDay, Cap: 50},
		{GroupID: "g2", AbstractModelID: "3", Unit: UnitRequest, Period: PeriodDay, Cap: 30}, // 同键更小
		{GroupID: "g1", AbstractModelID: "3", Unit: UnitToken, Period: PeriodDay, Cap: 8000},
		{GroupID: "g1", AbstractModelID: "9", Unit: UnitRequest, Period: PeriodDay, Cap: 7},
	}
	snap.Pools = []PoolRef{
		{GroupID: "g1", AbstractModelID: TotalScope, Unit: UnitToken, Period: PeriodDay, Limit: 100000}, // 合计池
		{GroupID: "g1", AbstractModelID: "3", Unit: UnitToken, Period: PeriodDay, Limit: 5000},          // 模型级池
		{GroupID: "g2", AbstractModelID: TotalScope, Unit: UnitCredit, Period: PeriodMonth, Limit: 200},
	}
	ep := ComposeEffectivePolicy(snap)

	if v := ep.ModelQuotaCaps[ModelQuotaKey{"3", UnitRequest, PeriodDay}]; v != 30 {
		t.Errorf("smart(request/day) 应 min(50,30)=30，得到 %d", v)
	}
	if v := ep.ModelQuotaCaps[ModelQuotaKey{"3", UnitToken, PeriodDay}]; v != 8000 {
		t.Errorf("smart(token/day) 单组设应 8000，得到 %d", v)
	}
	if v := ep.ModelQuotaCaps[ModelQuotaKey{"9", UnitRequest, PeriodDay}]; v != 7 {
		t.Errorf("其它模型 cap 应 7，得到 %d", v)
	}
	// (3, credit, day) 无行：不出现（= 不限）。
	if _, ok := ep.ModelQuotaCaps[ModelQuotaKey{"3", UnitCredit, PeriodDay}]; ok {
		t.Error("无行的 (model,unit,period) 不应出现")
	}
	// 池：三个全在，不合并。
	if len(ep.Pools) != 3 {
		t.Fatalf("池应原样收集 3 个，得到 %d", len(ep.Pools))
	}
	if ep.Pools[0].GroupID != "g1" || ep.Pools[0].AbstractModelID != TotalScope {
		t.Errorf("池排序[0] 应 (g1, 合计)，得到 %+v", ep.Pools[0])
	}
}

// ---- 8. 快照装载（真库） ----

// TestContractLoadPolicySnapshot：真库装配快照后合成——多组策略/模型/
// 上限/池全部进入；坏 JSON 报错不静默。
func TestContractLoadPolicySnapshot(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	acct := addTenantAccount(t, s)
	g1, g2 := mustGroup(t, s, "snap1"), mustGroup(t, s, "snap2")

	// g1 全配：布尔 true、token/day 100、模型 [m1]、按模型上限、池。
	m1, err := s.Repos().Models.CreateAbstractModel(ctx, AbstractModel{Name: "fast", DisplayName: "Fast"})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	gSetBoolPolicy(t, s, g1, PolicyCanCreateKey, true)
	gSetPolicy(t, s, g1, PolicyQuotaTokenDay, 100)
	if err := s.Repos().Groups.SetGroupModels(ctx, g1, []string{m1}); err != nil {
		t.Fatalf("设组模型失败: %v", err)
	}
	if err := s.Repos().Quotas.SetGroupModelCap(ctx, ModelCap{GroupID: g1, AbstractModelID: m1,
		Unit: UnitRequest, Period: PeriodDay, Cap: 50}); err != nil {
		t.Fatalf("设按模型上限失败: %v", err)
	}
	if err := s.Repos().Quotas.SetPool(ctx, g1, TotalScope, UnitToken, PeriodDay, 100000); err != nil {
		t.Fatalf("设池失败: %v", err)
	}
	// g2 半配：token/day 200（min 后 100 生效）、布尔未设（fail-closed）。
	gSetPolicy(t, s, g2, PolicyQuotaTokenDay, 200)
	// 账户速率：rpm 30。
	if err := s.Repos().Accounts.SetAccountRateLimits(ctx, acct, 30, -1, -1); err != nil {
		t.Fatalf("设账户速率失败: %v", err)
	}

	// 入组。
	if err := s.Repos().Groups.AddMember(ctx, g1, acct); err != nil {
		t.Fatalf("入 g1 失败: %v", err)
	}
	if err := s.Repos().Groups.AddMember(ctx, g2, acct); err != nil {
		t.Fatalf("入 g2 失败: %v", err)
	}

	snap, err := s.Repos().Groups.LoadPolicySnapshot(ctx, acct)
	if err != nil {
		t.Fatalf("装载快照失败: %v", err)
	}
	ep := ComposeEffectivePolicy(*snap)

	if v := ep.QuotaLimits[QuotaKey{UnitToken, PeriodDay}]; v != 100 {
		t.Errorf("token/day 生效应 min(100,200)=100，得到 %d", v)
	}
	if ep.CanCreateKey {
		t.Error("g2 布尔未设 → CanCreateKey 应 false（fail-closed）")
	}
	if len(ep.VisibleModels) != 1 || ep.VisibleModels[0] != m1 {
		t.Errorf("可见模型应 [m1]，得到 %v", ep.VisibleModels)
	}
	if v := ep.ModelQuotaCaps[ModelQuotaKey{m1, UnitRequest, PeriodDay}]; v != 50 {
		t.Errorf("按模型上限应 50，得到 %d", v)
	}
	if len(ep.Pools) != 1 || ep.Pools[0].GroupID != g1 || ep.Pools[0].Limit != 100000 {
		t.Errorf("池应收集 g1 的合计池，得到 %+v", ep.Pools)
	}
	if ep.RPMLimit != 30 {
		t.Errorf("组未设 RPM 应落回账户列 30，得到 %d", ep.RPMLimit)
	}
	if ep.MaxKeys != -1 {
		t.Errorf("MaxKeys 全未设应 -1，得到 %d", ep.MaxKeys)
	}

	// 坏 JSON：直插一条坏布尔，装载必须报错（不静默跳过）。
	if _, err := s.writeDB.Exec(
		`INSERT INTO group_policies(group_id, policy_key, policy_value) VALUES(?, 'can_create_key', 'not-json')`,
		g2); err != nil {
		t.Fatalf("插坏策略失败: %v", err)
	}
	if _, err := s.Repos().Groups.LoadPolicySnapshot(ctx, acct); err == nil {
		t.Error("坏 JSON 应让装载报错（fail-closed），不能静默跳过")
	}
}

// ---- 9-10. 鉴定四态 ----

// TestContractVerificationStates：四态写入读回；非法状态拒绝；不存在的
// 凭据报 ErrCredentialNotFound；初始态 unverified；ListUnverified 过滤。
func TestContractVerificationStates(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	acct := addTenantAccount(t, s)

	c1, err := s.Repos().Models.CreateCredential(ctx, acct, "fake_provider", "ext-1", "凭据一", "secretref://1")
	if err != nil {
		t.Fatalf("建凭据失败: %v", err)
	}
	c2, err := s.Repos().Models.CreateCredential(ctx, acct, "fake_provider", "ext-2", "", "secretref://2")
	if err != nil {
		t.Fatalf("建凭据 2 失败: %v", err)
	}
	// 平台凭据：owner 挂管理员账户（凭据表 NOT NULL；「平台自有」语义在
	// upstreams.owner=NULL，本行是它的凭据侧锚点）。
	admin, err := s.Repos().Accounts.CreateAccount(ctx,
		AccountAuth{Account: Account{DisplayName: "a", Username: "admin-" + t.Name()}, PasswordHash: "x"},
		[]Class{ClassAdmin})
	if err != nil {
		t.Fatalf("建 admin 失败: %v", err)
	}
	c3, err := s.Repos().Models.UpsertPlatformCredential(ctx, admin, "fake_provider", "platform-key-1", "平台", "secretref://p1", "active")
	if err != nil {
		t.Fatalf("建平台凭据失败: %v", err)
	}

	// 初始态 unverified + pending。
	got, err := s.Repos().Models.LookupCredential(ctx, c1)
	if err != nil || got.VerifyStatus != VerifyUnverified || got.Status != "pending" {
		t.Fatalf("初始态 = %v, %v", got, err)
	}

	// 四态写入读回（evidence 原样存取）。
	for _, st := range []VerifyStatus{VerifyVerified, VerifySuspect, VerifyTrusted, VerifyUnverified} {
		ev := fmt.Sprintf(`{"state":%q,"score":0.9}`, st)
		if err := s.Repos().Models.MarkVerified(ctx, c1, st, ev); err != nil {
			t.Fatalf("写 %s 失败: %v", st, err)
		}
		got, err := s.Repos().Models.LookupCredential(ctx, c1)
		if err != nil || got.VerifyStatus != st || got.VerifyDetail != ev {
			t.Fatalf("%s 写后读回 = %v, %v", st, got, err)
		}
	}
	// suspect 的判定是调用方的事：MarkVerified 只落库（上面已隐式验证）。

	// 非法状态。
	if err := s.Repos().Models.MarkVerified(ctx, c1, VerifyStatus("bogus"), "{}"); err == nil {
		t.Error("非法状态应拒绝")
	}
	// 不存在的凭据。
	if err := s.Repos().Models.MarkVerified(ctx, tid3, VerifyVerified, "{}"); !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("不存在的凭据应 ErrCredentialNotFound，得到 %v", err)
	}
	if _, err := s.Repos().Models.LookupCredential(ctx, tid3); !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("查不存在的凭据应 ErrCredentialNotFound，得到 %v", err)
	}

	// ListUnverified：c2、c3 在列（c1 已被改成 unverified 也在列——四态循环
	// 最后一轮是 unverified）；按 provider 过滤后空 provider 全在。
	list, err := s.Repos().Models.ListUnverified(ctx, "fake_provider", "")
	if err != nil {
		t.Fatalf("列未鉴定失败: %v", err)
	}
	var ids []string
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	// 期望集合 {c1,c2,c3}（全部 unverified）。不再断言顺序：UUID 主键的
	// 排序是字典序，与"创建先后"无关，而这三条在同一秒内创建。
	sort.Strings(ids)
	want := []string{c1, c2, c3}
	sort.Strings(want)
	if len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Errorf("未鉴定列表 = %v，期望集合 %v（全部 unverified）", ids, want)
	}
	// LIKE 前缀圈定。
	list, err = s.Repos().Models.ListUnverified(ctx, "", "platform-%")
	if err != nil || len(list) != 1 || list[0].ID != c3 {
		t.Errorf("LIKE 圈定 = %v, %v，期望只 [c3]", list, err)
	}
	// 标 verified 后从列表消失。
	if err := s.Repos().Models.MarkVerified(ctx, c2, VerifyVerified, "{}"); err != nil {
		t.Fatalf("标 verified 失败: %v", err)
	}
	list, _ = s.Repos().Models.ListUnverified(ctx, "", "")
	if len(list) != 2 {
		t.Errorf("标 verified 后未鉴定应剩 2（c1 c3），得到 %d", len(list))
	}
	// 按状态取回。
	list, err = s.Repos().Models.ListCredentialsByStatus(ctx, VerifyVerified)
	if err != nil || len(list) != 1 || list[0].ID != c2 {
		t.Errorf("按 verified 列 = %v, %v，期望 [c2]", list, err)
	}
}

// TestContractPlatformCredentialOwnerNull：平台凭据 owner NULL、幂等
// upsert 同 id、贡献者凭据必须挂账户。
func TestContractPlatformCredentialOwnerNull(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	adm, err := s.Repos().Accounts.CreateAccount(ctx,
		AccountAuth{Account: Account{DisplayName: "a", Username: "adm-" + t.Name()}, PasswordHash: "x"},
		[]Class{ClassAdmin})
	if err != nil {
		t.Fatalf("建 admin 失败: %v", err)
	}
	id1, err := s.Repos().Models.UpsertPlatformCredential(ctx, adm, "fake_provider", "pk-1", "", "secretref://x", "active")
	if err != nil {
		t.Fatalf("建平台凭据失败: %v", err)
	}
	id2, err := s.Repos().Models.UpsertPlatformCredential(ctx, adm, "fake_provider", "pk-1", "改标签", "secretref://y", "pending")
	if err != nil {
		t.Fatalf("重复 upsert 应幂等: %v", err)
	}
	if id1 != id2 {
		t.Errorf("同 (provider, external_id) 应同 id：%s vs %s", id1, id2)
	}
	got, err := s.Repos().Models.LookupCredential(ctx, id1)
	if err != nil || got.OwnerAccountID != adm || got.ExternalLabel != "改标签" || got.Status != "pending" {
		t.Fatalf("平台凭据 = %+v, %v（owner 应挂 admin=%s，标签/状态应更新）", got, err, adm)
	}

	// 贡献者凭据挂账户强制（空串拒绝；owner 列是 UUID NOT NULL）。
	if _, err := s.Repos().Models.CreateCredential(ctx, "", "p", "e", "", "s"); err == nil {
		t.Error("CreateCredential 无 owner 应拒绝")
	}
	// 平台凭据入口同样拒绝空 owner（NOT NULL 列的保护）。
	if _, err := s.Repos().Models.UpsertPlatformCredential(ctx, "", "p2", "e2", "", "s", "active"); err == nil {
		t.Error("UpsertPlatformCredential owner 为空应拒绝")
	}
}

// TestContractSetDefaultMigration：转移默认组后旧组成员整体迁入新默认组、
// 幂等、无成员组也正常转移。
func TestContractSetDefaultMigration(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	// 初始默认组（openTestStore 建库后无组，首个 is_default=1 合法）。
	oldDef, err := s.Repos().Groups.CreateGroup(ctx, "old-def", "", true)
	if err != nil {
		t.Fatalf("建旧默认组失败: %v", err)
	}
	newDef := mustGroup(t, s, "new-def")
	side := mustGroup(t, s, "side") // 第三组：成员不应被动

	a1, a2 := addTenantAccount(t, s), addTenantAccount(t, s)
	if err := s.Repos().Groups.AddMember(ctx, oldDef, a1); err != nil {
		t.Fatalf("a1 入旧默认组失败: %v", err)
	}
	if err := s.Repos().Groups.AddMember(ctx, side, a2); err != nil {
		t.Fatalf("a2 入旁组失败: %v", err)
	}

	if err := s.Repos().Groups.SetDefault(ctx, newDef); err != nil {
		t.Fatalf("转移默认组失败: %v", err)
	}
	// 标记转移。
	var isDef int
	_ = s.writeDB.QueryRow(`SELECT is_default FROM groups WHERE id = ?`, newDef).Scan(&isDef)
	if isDef != 1 {
		t.Fatal("新组应成为默认组")
	}
	_ = s.writeDB.QueryRow(`SELECT is_default FROM groups WHERE id = ?`, oldDef).Scan(&isDef)
	if isDef != 0 {
		t.Error("旧组标记应清零")
	}
	// 旧默认组成员迁入新默认组；旁组成员不动；旧组自己仍保有成员。
	newMembers, _ := s.Repos().Groups.ListMembers(ctx, newDef)
	if len(newMembers) != 1 || newMembers[0] != a1 {
		t.Errorf("新默认组成员 = %v，期望 [a1]（迁移落空即旧 bug）", newMembers)
	}
	sideMembers, _ := s.Repos().Groups.ListMembers(ctx, side)
	if len(sideMembers) != 1 || sideMembers[0] != a2 {
		t.Errorf("旁组成员不应被动：%v", sideMembers)
	}

	// 幂等：再次 SetDefault 同组无错且不变。
	if err := s.Repos().Groups.SetDefault(ctx, newDef); err != nil {
		t.Fatalf("重复转移应幂等: %v", err)
	}
	newMembers, _ = s.Repos().Groups.ListMembers(ctx, newDef)
	if len(newMembers) != 1 {
		t.Errorf("幂等后成员不应重复：%v", newMembers)
	}

	// 不存在的组：ErrGroupNotFound。
	if err := s.Repos().Groups.SetDefault(ctx, tid3); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("转移到不存在的组应 ErrGroupNotFound，得到 %v", err)
	}
}
