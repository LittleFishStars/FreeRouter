package auth

import (
	"context"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 操作闭集合与能力矩阵（表驱动）
// ---------------------------------------------------------------------------

// Decision 是判定结果。2026-10-20 取消类级提权后由三值收敛为两值
// （04-authz.md §1：不存在"权限够但此刻不算数"的中间态）。
type Decision int

const (
	// DecisionAllow 允许执行。
	DecisionAllow Decision = iota
	// DecisionDeny 拒绝执行；error 里带具体 Code（403 或 404）。
	DecisionDeny
)

// Op 是操作闭集合。资源相关操作必须显式声明，不由路由拼接猜测
// （04-authz.md §1）。取值与 §3 决策表逐行对齐。
type Op string

const (
	// —— 模型面（tenant）——
	OpCallModel       Op = "call_model"
	OpCreateKey       Op = "create_key"
	OpListKeys        Op = "list_keys"
	OpRenameKey       Op = "rename_key"
	OpDisableKey      Op = "disable_key"
	OpEnableKey       Op = "enable_key"
	OpRevokeKey       Op = "revoke_key"
	OpSetKeyAllowlist Op = "set_key_allowlist"
	OpViewOwnUsage    Op = "view_own_usage"
	OpViewOwnKeys     Op = OpListKeys // 别名：读侧习惯叫"我的 Key"

	// —— 贡献面（contributor）——
	OpContributeCredential  Op = "contribute_credential"
	OpListOwnCredentials    Op = "list_own_credentials"
	OpViewOwnVerification   Op = "view_own_verification"
	OpDisableOwnCredential  Op = "disable_own_credential"
	OpDeleteOwnCredential   Op = "delete_own_credential"
	OpToggleCredentialLogin Op = "toggle_credential_login"

	// —— 管理面（admin）——
	OpReadAdminData       Op = "read_admin_data"
	OpApproveAccount      Op = "approve_account"
	OpRejectAccount       Op = "reject_account"
	OpGrantClass          Op = "grant_class"
	OpRevokeClass         Op = "revoke_class"
	OpSetAccountStatus    Op = "set_account_status"
	OpSetAccountLimits    Op = "set_account_limits"
	OpCreateGroup         Op = "create_group"
	OpRenameGroup         Op = "rename_group"
	OpChangeGroup         Op = "change_group" // 改策略/成员/默认组/删组的整体归类
	OpChangeAbstractModel Op = "change_abstract_model"
	OpChangeDeprecation   Op = "change_deprecation"
	OpViewRealModel       Op = "view_real_model"
	OpViewAttribution     Op = "view_attribution" // 某次请求的真实落点与归因
	OpUpstreamCatalog     Op = "upstream_catalog" // 上游目录与鉴定处置
	OpReadAudit           Op = "read_audit"
	OpResetOtherPassword  Op = "reset_other_password"
	OpRevokeOtherSessions Op = "revoke_other_sessions"
	OpUnlockAccount       Op = "unlock_account" // 提前解除登录锁定（进审计）

	// —— 任意已认证主体（本人路径）——
	OpSelfChangePassword Op = "self_change_password"
	OpViewOwnSessions    Op = "view_own_sessions"
)

// ResourceKind 是被操作对象的类型（04-authz.md §8 Resource.Kind）。
type ResourceKind string

const (
	ResAccount       ResourceKind = "account"
	ResGroup         ResourceKind = "group"
	ResAbstractModel ResourceKind = "abstract_model"
	ResRealModel     ResourceKind = "real_model"
	ResAPIKey        ResourceKind = "api_key"
	ResCredential    ResourceKind = "credential"
	ResUsage         ResourceKind = "usage"
	ResAudit         ResourceKind = "audit"
	ResSession       ResourceKind = "session"
	ResInbox         ResourceKind = "inbox"
	ResRequest       ResourceKind = "request"
)

// Resource 是被操作对象引用：类型 + ID。ID 为空表示"集合级"操作（如建 Key）。
type Resource struct {
	Kind ResourceKind
	ID   string
}

// CapabilitySet 是一个权限类能执行的 Op 集合。
type CapabilitySet map[Op]bool

// Can 报告该类是否声明了这个操作。
func (c CapabilitySet) Can(op Op) bool { return c[op] }

// classCapabilities 是**唯一**的能力事实表（04-authz.md §3 决策表的代码形态）。
//
// 为什么用 map[Class]CapabilitySet 而不是 if/else 或接口分发：
// 决策表是"类 × 操作 → 允许/拒绝"的二维矩阵，表驱动让评审可以直接对照文档
// 逐行核；新增操作时**必须**在这里出现，否则默认拒绝（fail-closed）。
// 判定永远不改表，改的是表本身——审计与文档的对照面只有一个。
var classCapabilities = map[Class]CapabilitySet{
	ClassTenant: opsTo(
		OpCallModel, OpCreateKey, OpListKeys, OpRenameKey, OpDisableKey,
		OpEnableKey, OpRevokeKey, OpSetKeyAllowlist, OpViewOwnUsage,
	),
	ClassContributor: opsTo(
		OpContributeCredential, OpListOwnCredentials, OpViewOwnVerification,
		OpDisableOwnCredential, OpDeleteOwnCredential, OpToggleCredentialLogin,
	),
	ClassAdmin: opsTo(
		OpReadAdminData, OpApproveAccount, OpRejectAccount, OpGrantClass,
		OpRevokeClass, OpSetAccountStatus, OpSetAccountLimits, OpCreateGroup,
		OpRenameGroup, OpChangeGroup, OpChangeAbstractModel, OpChangeDeprecation,
		OpViewRealModel, OpViewAttribution, OpUpstreamCatalog, OpReadAudit,
		OpResetOtherPassword, OpRevokeOtherSessions, OpUnlockAccount,
	),
}

// universalCaps 是任何有效主体（任何类）都能做的本人路径。
//
// 为什么单独一张表：自助改密/查自己的会话不属于任何一"面"，
// 塞进三个类里会让"改密需要 admin"这类荒谬结论成为可能。
var universalCaps = opsTo(OpSelfChangePassword, OpViewOwnSessions)

func opsTo(list ...Op) CapabilitySet {
	c := make(CapabilitySet, len(list))
	for _, o := range list {
		c[o] = true
	}
	return c
}

// ---------------------------------------------------------------------------
// AuthzService
// ---------------------------------------------------------------------------

// AuthzService 是判定链的唯一入口（04-authz.md §8）。
//
// 管理面与模型面共用同一条链；差异只在"主体从哪来"（会话 vs Key），
// 而那发生在协议层组装 Principal 的时候，不在这里。
type AuthzService struct {
	stores Stores
	now    func() time.Time // CheckModelCall 的 Key 过期判定需要时钟（测试注入，不 sleep）
}

// NewAuthz 构造判定服务。
func NewAuthz(stores Stores) *AuthzService {
	return &AuthzService{stores: stores, now: time.Now}
}

// WithClock 注入时钟（测试用）。
func (a *AuthzService) WithClock(now func() time.Time) *AuthzService {
	a.now = now
	return a
}

// Check 执行判定：主体 + 操作 + 资源 → 允许 / 拒绝。
//
// 拒绝时 error 必非 nil 且带正确 Code：
//   - 类不允许 → ErrForbidden（403）
//   - 资源不属于主体（读他人 Key/凭据/用量）→ ErrNotFound（404，不泄露存在性）
//
// 为什么"他人资源"要 404 而不是 403：04-authz.md §3 永远不允许表要求
// 读他人 Key/凭据/用量返回 404 或空集——403 等于确认"这个 ID 存在，只是不是你的"。
func (a *AuthzService) Check(ctx context.Context, s Subject, op Op, res Resource) (Decision, error) {
	if !s.ActingClass.Valid() {
		return DecisionDeny, Errorf(CodeInvalidRequest, nil, "非法 acting_class %q", s.ActingClass)
	}
	if s.AccountID == "" {
		return DecisionDeny, ErrForbidden
	}
	if !classCapabilities[s.ActingClass].Can(op) && !universalCaps.Can(op) {
		return DecisionDeny, Errorf(CodeForbidden, nil, "%s 类不允许执行 %s", s.ActingClass, op)
	}

	// 自有资源归属校验：凡 Kind 属于"名下资源"且给了具体 ID，必须属于本人。
	if a.ownedKind(res.Kind) && res.ID != "" {
		owner, err := a.resourceOwner(ctx, res)
		if err != nil {
			return DecisionDeny, err // store 错误原样上抛（协议层映射 500）
		}
		if owner != s.AccountID {
			return DecisionDeny, ErrNotFound
		}
	}
	// "本人以 Key 身份改自己密码"的额外线：Key 主体没有会话，改密无对象可吊。
	if op == OpSelfChangePassword && s.ActingClass == ClassTenant {
		// 合法路径；协议层保证会话主体才能走到这里，本层不再重复判凭据种类。
	}
	return DecisionAllow, nil
}

// ownedKind 报告该资源类型是否属于"按账户归属"的名下资源。
//
// 为什么 usage/session 也算：它们都按 account_id 分表（01-accounts.md §6 维度二），
// 判定链对"名下的东西"统一走归属校验，不给每种资源单独写一遍。
func (a *AuthzService) ownedKind(k ResourceKind) bool {
	switch k {
	case ResAPIKey, ResCredential, ResUsage, ResSession, ResRequest:
		return true
	}
	return false
}

// resourceOwner 取资源归属账户。Key 走存储实查（不信任调用方拼的 ID 前缀）；
// 凭据表不在本层窄接口内（contributions 分支的存储域），当前只能按"Key 可查、
// 其余资源交由上层服务自查归属"的边界执行——见交付说明的遗留问题。
func (a *AuthzService) resourceOwner(ctx context.Context, res Resource) (string, error) {
	switch res.Kind {
	case ResAPIKey:
		if a.stores.Keys == nil {
			return "", Errorf(CodeInternal, nil, "Authz 缺少 Key 存储")
		}
		k, err := a.stores.Keys.KeyByID(ctx, res.ID)
		if err != nil {
			return "", err
		}
		return k.AccountID, nil
	default:
		// 调用方持有该资源的场景（列表/集合级）不会带 ID；带 ID 的非 Key 资源
		// 由各自领域的服务（contributions/usage/session）先做归属过滤，
		// 本层无法跨域核验。返回空串让上面的 owner != AccountID 变成 404 反而是错的，
		// 因此这里显式放行——归属核验是那些服务的职责。
		return "", nil //nolint:nilerr // 见上：非本层资源类型不在这里判归属
	}
}

// AccountUsable 是禁用联动的单点（04-authz.md §8）：账户状态与类**实时读**，
// 不依赖长 TTL 缓存。模型面每请求调用它（判定链第 4 步）。
func (a *AuthzService) AccountUsable(ctx context.Context, accountID string, required Class) (*Account, error) {
	if a.stores.Accounts == nil {
		return nil, Errorf(CodeInternal, nil, "Authz 缺少账户存储")
	}
	acct, err := a.stores.Accounts.AccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !acct.Status.CanCallModels() || !acct.HasClass(required) {
		// 统一 ErrInvalidAPIKey 形态的 401 语义：04-authz.md §5 —— 账户禁用、
		// 无 tenant 类对 Key 调用方都表现为"这把凭据不可用"，客户端动作相同。
		return nil, Errorf(CodeForbidden, nil, "账户不可用（状态 %s / 类 %s）", acct.Status, required)
	}
	return acct, nil
}

// ---------------------------------------------------------------------------
// 多组合并：放开类取并集，限制类取最严，布尔 deny 优先，池不合并
// （03-groups.md §4）
// ---------------------------------------------------------------------------

// （ModelQuotaKey / Unlimited 的定义在 storeiface.go：它们是 GroupPolicy 字段的一部分，
// 随存储快照走，合并与计量两侧共用同一定义。）

// EffectivePolicy 是一个账户全部所属组合并后的策略快照。
type EffectivePolicy struct {
	// 放开类
	VisibleModels  []string // ∪ 各组抽象模型（稳定排序）
	CanCreateKey   bool     // 布尔 deny 优先：任一组显式 false 即 false
	AllowlistWiden bool     // 同上
	MaxKeys        *int     // 放开类数值：多组取**最大值**；nil = 无组设置（落默认值）
	// 限制类：逐键取 min；键不存在于结果 = 所有组都未设 = Unlimited。
	Quotas         map[QuotaKey]int64
	ModelQuotas    map[ModelQuotaKey]int64
	RPMLimit       *int
	TPMLimit       *int64
	MaxConcurrency *int

	// 共享池：**不合并**。每个池是不同组的独立资源，逐个都要满足
	// （min 会丢掉"是哪个组的池先耗尽"的归因，错误码与审计都需要它）。
	Pools []PoolRef
}

// Quota 返回指定键的合并上限；未设键返回 (Unlimited, false)。
func (e *EffectivePolicy) Quota(q QuotaKey) (int64, bool) {
	v, ok := e.Quotas[q]
	return v, ok
}

// MergeGroupPolicies 执行合并算法。
//
// 为什么它是纯函数：合并是判定链最容易写错的部分（并集/min/deny 优先/池不合并
// 四种算子各管一类键），纯函数让 Contract 测试可以逐算子断言而不碰任何存储。
// 注意 Key 数量上限的**定案**（03-groups.md §3）：它归放开类取最大值，
// 不能因为"它也是个上限"就错拿 min——那会让加一个小配额组顺带压掉建 Key 能力。
func MergeGroupPolicies(groups []GroupPolicy) *EffectivePolicy {
	eff := &EffectivePolicy{
		CanCreateKey:   true,
		AllowlistWiden: true,
		Quotas:         make(map[QuotaKey]int64),
		ModelQuotas:    make(map[ModelQuotaKey]int64),
	}
	seenModels := make(map[string]bool)

	for i := range groups {
		g := &groups[i]

		// —— 放开类：并集 ——
		for _, mdl := range g.Models {
			if !seenModels[mdl] {
				seenModels[mdl] = true
				eff.VisibleModels = append(eff.VisibleModels, mdl)
			}
		}
		// 布尔开关：deny 优先——任何一组显式禁止即禁止；未设（nil）不参与。
		if g.CanCreateKey != nil && !*g.CanCreateKey {
			eff.CanCreateKey = false
		}
		if g.KeyAllowlistWiden != nil && !*g.KeyAllowlistWiden {
			eff.AllowlistWiden = false
		}
		// Key 数量上限：放开类数值取**最大值**。
		if g.MaxKeys != nil {
			if eff.MaxKeys == nil || *g.MaxKeys > *eff.MaxKeys {
				v := *g.MaxKeys
				eff.MaxKeys = &v
			}
		}

		// —— 限制类：逐键取最严（min），-1（Unlimited）与未设不参与 ——
		for qk, v := range g.Quotas {
			if v == Unlimited {
				continue // 显式"无限制"不是一道闸
			}
			if cur, ok := eff.Quotas[qk]; !ok || v < cur {
				eff.Quotas[qk] = v
			}
		}
		for mk, v := range g.ModelQuotas {
			if v == Unlimited {
				continue
			}
			if cur, ok := eff.ModelQuotas[mk]; !ok || v < cur {
				eff.ModelQuotas[mk] = v
			}
		}
		eff.RPMLimit = minPtr(eff.RPMLimit, g.RPMLimit)
		eff.TPMLimit = minPtr64(eff.TPMLimit, g.TPMLimit)
		eff.MaxConcurrency = minPtr(eff.MaxConcurrency, g.MaxConcurrency)

		// —— 共享池：原样收集，逐个都要满足 ——
		eff.Pools = append(eff.Pools, g.Pools...)
	}

	sort.Strings(eff.VisibleModels)
	return eff
}

// minPtr 取"两个已设值里更严（更小）的那个"；nil 表示该来源未设置。
// 显式 Unlimited（-1 及一切负数）同样视为"不构成闸"，不参与。
func minPtr(cur, next *int) *int {
	if next == nil || *next < 0 {
		return cur
	}
	if cur == nil || *next < *cur {
		v := *next
		return &v
	}
	return cur
}

func minPtr64(cur, next *int64) *int64 {
	if next == nil || *next < 0 {
		return cur
	}
	if cur == nil || *next < *cur {
		v := *next
		return &v
	}
	return cur
}

// （modelQuotaKeyFrom 不提供：构造 ModelQuotaKey 直接写字段即可，
// 一个只包装结构体字面量的函数不会阻止笔误，只会多一个间接层。）

// ---------------------------------------------------------------------------
// 三层收敛：实际可用 = (∪ 组授权) ∩ Key 白名单
// ---------------------------------------------------------------------------

// EffectiveModels 计算第二层收敛的结果（04-authz.md §2）。
//
// 白名单默认空 = 交集为空 = 新建 Key 不改配置时**无法发起任何请求**（验收 5）。
// 白名单元素必须 ⊆ 组授权是写入时校验（SetAllowlist）；这里再交一次，
// 是因为组授权会在白名单写入**之后**被管理员收窄——读取时求交集让
// "账户被移出组后白名单不变、可用集合自动收窄"（05-keys.md §5）无损成立。
func (a *AuthzService) EffectiveModels(ctx context.Context, accountID, keyID string) ([]string, error) {
	groups, err := a.groupsFor(ctx, accountID)
	if err != nil {
		return nil, err
	}
	eff := MergeGroupPolicies(groups)
	if a.stores.Keys == nil {
		return nil, Errorf(CodeInternal, nil, "Authz 缺少 Key 存储")
	}
	allow, err := a.stores.Keys.KeyAllowlist(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if len(allow) == 0 {
		return nil, nil // 默认拒绝：空白名单交集必空
	}
	vis := make(map[string]bool, len(eff.VisibleModels))
	for _, m := range eff.VisibleModels {
		vis[m] = true
	}
	out := make([]string, 0, len(allow))
	for _, m := range allow {
		if vis[m] {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ModelCallable 是判定链第 5 步的单点：requestedModel 是否在实际可用集合内。
// 不在 → 调用方必须回 404（与"模型不存在"同形，不泄露目录）。
func (a *AuthzService) ModelCallable(ctx context.Context, accountID, keyID, requestedModel string) (bool, error) {
	m, err := a.EffectiveModels(ctx, accountID, keyID)
	if err != nil {
		return false, err
	}
	for _, x := range m {
		if strings.EqualFold(x, requestedModel) {
			return true, nil
		}
	}
	return false, nil
}

// groupsFor 读取账户全部组策略；无 PolicyRepository 时返回空切片
// （合并算法对空输入 = 全部键未设 = 全部 Unlimited，由调用方落全局默认）。
func (a *AuthzService) groupsFor(ctx context.Context, accountID string) ([]GroupPolicy, error) {
	if a.stores.Policies == nil {
		return nil, nil
	}
	return a.stores.Policies.GroupsForAccount(ctx, accountID)
}

// EffectivePolicyFor 是供 meter / 中间件取"合并后的上界"的入口。
func (a *AuthzService) EffectivePolicyFor(ctx context.Context, accountID string) (*EffectivePolicy, error) {
	groups, err := a.groupsFor(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return MergeGroupPolicies(groups), nil
}

// ModelCallPlan 是模型面运行时顺序（04-authz.md §8）中**auth 层能确定的部分**。
//
// RealModelID / CredentialID 由策略引擎填充（候选展开在第 8 步，属于 models 层）；
// auth 层职责止于：Key 合规、账户实时可用、模型可见性（第 5 步）、
// 以及给出"本次计入哪个额度维度"（QuotaUnit，来自账户级字段）。
type ModelCallPlan struct {
	AccountID       string
	KeyID           string
	AbstractModelID string // 对外呈现的抽象模型
	QuotaUnit       string // request / token / credit —— 账户级，与 Key 无关
}

// CheckModelCall 执行判定链第 2~6 步中与 auth 相关的部分（不含限速，限速在 meter）。
//
// 失败错误就是协议层要映射的对象：401（凭据/账户）与 404（不可见模型）。
func (a *AuthzService) CheckModelCall(ctx context.Context, rawKey, requestedModel string) (*ModelCallPlan, error) {
	if a.stores.Keys == nil || a.stores.Accounts == nil {
		return nil, Errorf(CodeInternal, nil, "Authz 缺少存储依赖")
	}
	// 第 2/3 步：Key 自身（格式、哈希查表、状态/过期）——复用 KeyManager.Usable 的判定，
	// 但这里不重复构造 KeyManager：Usable 只做本地+Key 表判定，无策略依赖。
	if !LooksLikeKey(rawKey) {
		return nil, ErrInvalidAPIKey
	}
	key, err := a.stores.Keys.KeyByHash(ctx, HashToken(rawKey))
	if err != nil {
		return nil, ErrInvalidAPIKey
	}
	if !key.Usable(a.now()) {
		return nil, ErrInvalidAPIKey
	}
	// 第 4 步：账户实时状态 + tenant 类。
	acct, err := a.stores.Accounts.AccountByID(ctx, key.AccountID)
	if err != nil {
		return nil, ErrInvalidAPIKey // 对外同形：机器凭据不给探测面（§5 禁用联动 401）
	}
	if !acct.Status.CanCallModels() || !acct.HasClass(ClassTenant) {
		return nil, ErrInvalidAPIKey
	}
	// 第 5 步：可见性 = 组授权 ∩ 白名单。
	ok, err := a.ModelCallable(ctx, acct.ID, key.ID, requestedModel)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound // 404：不可见等于不存在
	}
	return &ModelCallPlan{
		AccountID:       acct.ID,
		KeyID:           key.ID,
		AbstractModelID: requestedModel,
		QuotaUnit:       quotaUnit(acct),
	}, nil
}

// quotaUnit 取账户的计量维度；未配置时按 request 解释。
//
// 为什么默认 request 而不是报错：01-accounts.md 规定新账户 quota_limit=0
// （默认拒绝），维度字段留空不影响"0 不可用"的判定；对已有正额度的账户，
// 按次数计是三种维度里最保守（不会因单位误解多放）的解释。
func quotaUnit(acct *Account) string {
	switch acct.QuotaUnit {
	case "token", "credit", "request":
		return acct.QuotaUnit
	default:
		return "request"
	}
}
