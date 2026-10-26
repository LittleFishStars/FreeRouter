// effective_policy.go —— 有效策略合成（task-15，M7）。
//
// 本文件只放**纯函数**：输入策略快照，输出 EffectivePolicy。不查库、不读
// 时钟（now 也不调）——同一快照两次合成必须逐字节相等（任务红线）。
// 快照的装载在 repo_group.go 的 LoadPolicySnapshot（读路径分离，便于
// 单测合成语义时不需要真的开库）。
//
// 合并语义（docs/security/03-groups.md §3/§4，2026-10-19/20 两次定案）：
//   - 放开类（许可/可见性）取并集：加组只会更宽或不变；
//   - 限制类（额度/速率/并发）取最严（逐键 min）：加组只会更紧或不变——
//     「加组即提权」在额度维度被 min 消灭；
//   - 布尔开关 deny 优先：任一组明确禁止即禁止（否定判断不能被翻掉）；
//   - 未设键 = -1 哨兵 = 无限制，**不参与 min**；全部未设 = 该键不构成闸
//     （速率/并发类此时落回账户列——那是管理员建账时显式确认过的值）；
//   - 共享池不合并成一个数：逐组独立的资源，合成侧只收集（判定时逐个
//     都要满足，见 TryDebitPool）。
package store

import "sort"

// PolicyKey 是组策略键的闭集合（group_policies.policy_key）。
// 新增键必须先归入放开类或限制类（03-groups §9）——kindOf 是唯一分类
// 事实源，SetPolicy 的形状校验与合成算法都查它。
type PolicyKey string

const (
	// 放开类：并集。
	PolicyCanCreateKey      PolicyKey = "can_create_key"      // 布尔
	PolicyMaxKeys           PolicyKey = "max_keys"            // 整数 ≥-1，多组取 max（2026-10-19 定案）
	PolicyKeyAllowlistWiden PolicyKey = "key_allowlist_widen" // 布尔
	PolicyModelClass        PolicyKey = "model_class_allow"   // 字符串数组

	// 限制类：逐键取最严（min）。
	PolicyQuotaRequestDay   PolicyKey = "quota_request_day"
	PolicyQuotaRequestMonth PolicyKey = "quota_request_month"
	PolicyQuotaTokenDay     PolicyKey = "quota_token_day"
	PolicyQuotaTokenMonth   PolicyKey = "quota_token_month"
	PolicyQuotaCreditDay    PolicyKey = "quota_credit_day"
	PolicyQuotaCreditMonth  PolicyKey = "quota_credit_month"
	PolicyRPMLimit          PolicyKey = "rpm_limit"
	PolicyTPMLimit          PolicyKey = "tpm_limit"
	PolicyMaxConcurrency    PolicyKey = "max_concurrency"

	// PolicyVisibleModels（"visible_models"）刻意不在键值表里：模型授权是
	// 关联数据（group_models 表），见 PolicySnapshot.VisibleModels 的注释。
)

// PolicyKind 决定该策略在合并时取并集还是取最严（03-groups §9）。
type PolicyKind string

const (
	PolicyOpen     PolicyKind = "open"     // 放开类：并集
	PolicyRestrict PolicyKind = "restrict" // 限制类：最严
)

// policyKindOf 返回键的分类。第二个返回值 = 是否为已知键。
// 可见性（visible_models）与共享池不进键值表，返回 false 由调用方指路。
func policyKindOf(key PolicyKey) (PolicyKind, bool) {
	switch key {
	case PolicyCanCreateKey, PolicyMaxKeys, PolicyKeyAllowlistWiden, PolicyModelClass:
		return PolicyOpen, true
	case PolicyQuotaRequestDay, PolicyQuotaRequestMonth,
		PolicyQuotaTokenDay, PolicyQuotaTokenMonth,
		PolicyQuotaCreditDay, PolicyQuotaCreditMonth,
		PolicyRPMLimit, PolicyTPMLimit, PolicyMaxConcurrency:
		return PolicyRestrict, true
	}
	return "", false
}

// isBoolPolicyKey / isNumberPolicyKey / isClassListPolicyKey 判定值形状，
// 供 SetPolicy 的应用层 schema 校验（DDL 注释明说：键值表的值无法由 DB
// 约束校验形状，必须在应用层做——这三个函数就是那道校验的形状分支）。
func isBoolPolicyKey(key PolicyKey) bool {
	return key == PolicyCanCreateKey || key == PolicyKeyAllowlistWiden
}

func isNumberPolicyKey(key PolicyKey) bool {
	switch key {
	case PolicyMaxKeys, PolicyRPMLimit, PolicyTPMLimit, PolicyMaxConcurrency,
		PolicyQuotaRequestDay, PolicyQuotaRequestMonth,
		PolicyQuotaTokenDay, PolicyQuotaTokenMonth,
		PolicyQuotaCreditDay, PolicyQuotaCreditMonth:
		return true
	}
	return false
}

func isClassListPolicyKey(key PolicyKey) bool {
	return key == PolicyModelClass
}

// QuotaKey 是「每人额度上限」的键：计量维度 × 周期（03-groups §3 定案：
// 逐键独立、互不比较——token 下的 100 与 credit 下的 50 无法比谁更严）。
type QuotaKey struct {
	Unit   QuotaUnit
	Period QuotaPeriod
}

// ModelQuotaKey 是「每人按模型额度上限」的键（2026-10-20 新增 3.3.1）：
// 作用域比 QuotaKey 多一维抽象模型。
type ModelQuotaKey struct {
	AbstractModelID string
	Unit            QuotaUnit
	Period          QuotaPeriod
}

// PoolRef 指向一个组级共享池（不合并、不取 min——03-groups §4：池是逐组
// 独立资源，合并成单个数字会丢掉「哪个组的池先耗尽」的归因信息）。
type PoolRef struct {
	GroupID         string
	AbstractModelID string // "" = 合计池（TotalScope）；否则是抽象模型 UUID
	Unit            QuotaUnit
	Period          QuotaPeriod
	Limit           int64
}

// GroupPolicySet 是单个组的键值策略已解码形态（LoadPolicySnapshot 从
// group_policies 的 raw JSON 解出；解析失败的行会让装载报错而不是静默
// 跳过——静默跳过 = 该组在合成时凭空消失 = fail-open）。
type GroupPolicySet struct {
	GroupID           string              // UUID
	CanCreateKey      *bool               // nil = 该组未设
	MaxKeys           *int64              // nil = 未设；-1 = 显式不限
	KeyAllowlistWiden *bool               // nil = 未设
	ModelClasses      []string            // nil = 未设（已去重排序）
	Numbers           map[PolicyKey]int64 // 限制类数字键：只含该组已设且 ≠-1 的键
}

// PolicySnapshot 是合成的全部输入（LoadPolicySnapshot 拼装）。
// 字段刻意做成「已解码」而不是 raw JSON：合成的纯函数性要求输入本身
// 确定，把 JSON 解码留在装载侧，坏值在装载时报错。
type PolicySnapshot struct {
	AccountID string
	Classes   []Class // 账户权限类（透传；类不参与组合并——类是账户属性）
	GroupIDs  []string

	// VisibleModels 是 ∪ 各组 group_models 的抽象模型 id。为什么不在
	// GroupPolicySet 里：模型授权存关联表（group_models）而不是键值表
	// ——它要 FK、要按模型反查（ix_gmqc_model 同理），JSON 塞不下这些。
	VisibleModels []string

	Groups []GroupPolicySet // 每组一行

	// ModelCaps 是全部所属组的每人按模型上限行（group_model_quota_caps）。
	ModelCaps []ModelCap

	// Pools 是全部所属组已配置的共享池（group_quota_pools，含合计与按模型）。
	Pools []PoolRef

	// 账户行上的速率三列（-1 = 不限）。速率/并发全部组未设时落回这里
	// （03-groups §4：「全部未设则落回账户列的 -1/自定义值」——账户列是
	// 管理员建账时显式确认过的，不能因为组没设就悄悄变成无限制）。
	AccountRPMLimit       int64
	AccountTPMLimit       int64
	AccountMaxConcurrency int64
}

// EffectivePolicy 是合成结果（03-groups §9 的文档形状按存储侧类型落地）。
// 任务约定：AllowedCaps 的五个放开字段平铺在顶层（不嵌套结构），
// 装配侧（httpapi/console/meter）按字段直取。
type EffectivePolicy struct {
	AccountID string
	Classes   []Class

	// ---- 放开类（并集）----
	VisibleModels     []string // ∪ 各组 group_models
	CanCreateKey      bool     // ALL(允许)：任一组 false 或全部未设 → false（fail-closed）
	MaxKeys           int64    // 取 max；任一组 -1 → -1；全部未设 → -1
	KeyAllowlistWiden bool     // ALL(允许)，同 CanCreateKey
	ModelClasses      []string // ∪ model_class_allow（排序去重）

	// ---- 限制类（逐键 min）----
	// QuotaLimits 只含至少一个组设置了的 (unit,period) 键；无键 = 不限
	// （-1 哨兵不参与 min，也不落成键——「无行 = 不限」与 account_quotas
	// 的表达一致）。0 是合法值 = 零额度全拒。
	QuotaLimits map[QuotaKey]int64
	// ModelQuotaCaps 逐 (model,unit,period) 取 min；无行 = 不限。
	ModelQuotaCaps map[ModelQuotaKey]int64
	// 速率/并发：组间 min；全部组未设 → 账户列值。0 合法（全拒），
	// -1 = 不限。
	RPMLimit       int64
	TPMLimit       int64
	MaxConcurrency int64

	// ---- 共享池（不合并，只收集）----
	Pools []PoolRef
}

// ComposeEffectivePolicy 是纯合成函数：同一快照必得同一结果。
// 不查库、不取时、不排序输入也输出确定形态（map 按 Go 遍历序不定，
// 但 map 相等性不受遍历序影响；切片输出全部显式排序）。
func ComposeEffectivePolicy(snap PolicySnapshot) EffectivePolicy {
	ep := EffectivePolicy{
		AccountID:      snap.AccountID,
		Classes:        snap.Classes,
		QuotaLimits:    map[QuotaKey]int64{},
		ModelQuotaCaps: map[ModelQuotaKey]int64{},
		Pools:          snap.Pools,
	}

	// ---- 放开类 ----
	ep.VisibleModels = dedupSortID(snap.VisibleModels)
	ep.ModelClasses = dedupSortString(snap.ModelClassesAggregate())

	// 布尔 deny 优先：未设 = false（fail-closed——「没有明确允许就是不
	// 允许」；与 masterkey 权限、审计触发器的姿态一致。管理员想放开，
	// 就在（至少默认）组里显式配 true）。
	ep.CanCreateKey = allGroupsAllow(snap.Groups, func(g GroupPolicySet) *bool { return g.CanCreateKey })
	ep.KeyAllowlistWiden = allGroupsAllow(snap.Groups, func(g GroupPolicySet) *bool { return g.KeyAllowlistWiden })

	// MaxKeys：放开类取 max，但 -1 是「+∞」语义——任一组不限则整体不限。
	ep.MaxKeys = -1
	anySet := false
	for _, g := range snap.Groups {
		if g.MaxKeys == nil {
			continue
		}
		v := *g.MaxKeys
		if v < 0 { // -1（或更负的哨兵）= 不限，支配 max
			ep.MaxKeys = -1
			anySet = true
			break
		}
		if !anySet || v > ep.MaxKeys {
			ep.MaxKeys = v
			anySet = true
		}
	}

	// ---- 限制类：逐键 min，-1 不参与 ----
	for _, g := range snap.Groups {
		for key, v := range g.Numbers {
			if v < 0 { // -1 = 未设语义（装载侧已滤，防御性再滤一次）
				continue
			}
			qk := policyKeyToQuotaKey(key)
			if qk == nil {
				continue
			}
			if old, ok := ep.QuotaLimits[*qk]; !ok || v < old {
				ep.QuotaLimits[*qk] = v
			}
		}
	}

	// 速率/并发：组间 min；全部未设 → 落回账户列（快照已带）。
	ep.RPMLimit = minAcrossGroups(snap.Groups, PolicyRPMLimit, snap.AccountRPMLimit)
	ep.TPMLimit = minAcrossGroups(snap.Groups, PolicyTPMLimit, snap.AccountTPMLimit)
	ep.MaxConcurrency = minAcrossGroups(snap.Groups, PolicyMaxConcurrency, snap.AccountMaxConcurrency)

	// ---- 每人按模型上限：逐 (model,unit,period) min ----
	for _, c := range snap.ModelCaps {
		if c.Cap <= 0 {
			continue // CHECK 已挡，防御
		}
		k := ModelQuotaKey{AbstractModelID: c.AbstractModelID, Unit: c.Unit, Period: c.Period}
		if old, ok := ep.ModelQuotaCaps[k]; !ok || c.Cap < old {
			ep.ModelQuotaCaps[k] = c.Cap
		}
	}

	// 池输出排序：确定性（Pools 内容合成不改，顺序排了便于断言/日志）。
	sort.Slice(ep.Pools, func(i, j int) bool {
		a, b := ep.Pools[i], ep.Pools[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.AbstractModelID != b.AbstractModelID {
			return a.AbstractModelID < b.AbstractModelID
		}
		if a.Unit != b.Unit {
			return a.Unit < b.Unit
		}
		return a.Period < b.Period
	})
	return ep
}

// ModelClassesAggregate 汇总各组的 model_class_allow 并集（快照层聚合，
// 合成函数内不重复展开）。
func (s PolicySnapshot) ModelClassesAggregate() []string {
	if len(s.Groups) == 0 {
		return nil
	}
	var out []string
	for _, g := range s.Groups {
		out = append(out, g.ModelClasses...)
	}
	return out
}

// allGroupsAllow 实现布尔放开类的 deny 优先 + fail-closed：
// 任一组显式 false → false；全部组显式 true → true；存在未设组或无组 → false。
func allGroupsAllow(groups []GroupPolicySet, get func(GroupPolicySet) *bool) bool {
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		v := get(g)
		if v == nil || !*v {
			return false
		}
	}
	return true
}

// minAcrossGroups 取某数字限制键在全部组的最小值；-1 不参与；没有任何
// 组设置 → fallback（账户列）。fallback 是调用方给的确定值，无隐藏默认。
func minAcrossGroups(groups []GroupPolicySet, key PolicyKey, fallback int64) int64 {
	min := int64(-1)
	found := false
	for _, g := range groups {
		v, ok := g.Numbers[key]
		if !ok || v < 0 {
			continue // 未设 / -1 哨兵：不参与 min
		}
		if !found || v < min {
			min = v
			found = true
		}
	}
	if !found {
		return fallback
	}
	return min
}

// policyKeyToQuotaKey 把限制类数字键映射回 QuotaKey；速率/并发键返回 nil
// （它们不是额度键，走 minAcrossGroups）。
func policyKeyToQuotaKey(key PolicyKey) *QuotaKey {
	switch key {
	case PolicyQuotaRequestDay:
		return &QuotaKey{Unit: UnitRequest, Period: PeriodDay}
	case PolicyQuotaRequestMonth:
		return &QuotaKey{Unit: UnitRequest, Period: PeriodMonth}
	case PolicyQuotaTokenDay:
		return &QuotaKey{Unit: UnitToken, Period: PeriodDay}
	case PolicyQuotaTokenMonth:
		return &QuotaKey{Unit: UnitToken, Period: PeriodMonth}
	case PolicyQuotaCreditDay:
		return &QuotaKey{Unit: UnitCredit, Period: PeriodDay}
	case PolicyQuotaCreditMonth:
		return &QuotaKey{Unit: UnitCredit, Period: PeriodMonth}
	}
	return nil
}

// dedupSortID 对 id 切片去重并按字典序排序（id 已是 UUID 文本）。
//
// 为什么还排序：这段输出的确定性是合成结果可复现的前提（同一输入两次装载
// 必须给同一顺序，否则权限判定的日志对不上）。UUID 的字典序没有时间语义，
// 但"稳定"只需要它是全序，字典序满足。
func dedupSortID(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := append([]string(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	w := 0
	for i := 1; i < len(out); i++ {
		if out[i] != out[w] {
			w++
			out[w] = out[i]
		}
	}
	return out[:w+1]
}

func dedupSortString(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	w := 0
	for i := 1; i < len(out); i++ {
		if out[i] != out[w] {
			w++
			out[w] = out[i]
		}
	}
	return out[:w+1]
}
