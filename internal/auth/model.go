package auth

import (
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// 权限类
// ---------------------------------------------------------------------------

// Class 是权限类闭集合。唯一事实源：docs/security/01-accounts.md 第 2 节。
//
// 为什么是 string 而不是 int 枚举：权限类要进审计、进组策略、进 error 文案，
// 数值枚举会在每一处边界都需要一次转换，而转换表是漂移的高发点。
type Class string

const (
	ClassAdmin       Class = "admin"       // 全局配置、账户审批、组、审计
	ClassTenant      Class = "tenant"      // 模型调用、建 Key
	ClassContributor Class = "contributor" // 上游凭据贡献
)

// AllClasses 是全部合法取值，供集合校验与"并集"计算使用。
//
// 顺序固定：它决定了 Principal.Classes 的稳定输出顺序，从而让审计与测试
// 的字符串可比较（否则每次遍历 map 都会得到不同的顺序）。
var AllClasses = []Class{ClassAdmin, ClassTenant, ClassContributor}

// Valid 报告该取值是否是闭集合中的一员。
func (c Class) Valid() bool {
	switch c {
	case ClassAdmin, ClassTenant, ClassContributor:
		return true
	}
	return false
}

// ClassSet 是权限类的集合。
//
// 为什么用集合类型而不是 []Class：并集、包含、非空校验都是集合运算，
// 用切片会让每个调用点各写一遍循环，而且极易把"空集合"与"字段缺失"混为一谈。
// 不变式：账户的 ClassSet **非空**（01-accounts.md §1 不变式 1）。
type ClassSet map[Class]bool

// NewClassSet 从若干类构造集合（忽略非法取值的判断交给 Validate）。
func NewClassSet(classes ...Class) ClassSet {
	s := make(ClassSet, len(classes))
	for _, c := range classes {
		s[c] = true
	}
	return s
}

// Add 加入一个类并返回集合本身，便于链式构造。
func (s ClassSet) Add(c Class) ClassSet { s[c] = true; return s }

// Has 报告集合是否包含某个类。
func (s ClassSet) Has(c Class) bool { return s[c] }

// IsEmpty 报告集合是否为空。空集合是账户的不变式违反，不是"无权限"的正常状态。
func (s ClassSet) IsEmpty() bool { return len(s) == 0 }

// Sorted 返回按 AllClasses 顺序排列的切片，保证输出稳定可比较。
func (s ClassSet) Sorted() []Class {
	out := make([]Class, 0, len(s))
	for _, c := range AllClasses {
		if s[c] {
			out = append(out, c)
		}
	}
	return out
}

// Union 返回并集（合并账户、组间合并都用它）。
// 返回**新集合**而不修改接收者：合并语义要求"两边都保留"，
// 原地修改会让调用者手里的集合被悄悄放大或污染。
func (s ClassSet) Union(other ClassSet) ClassSet {
	out := make(ClassSet, len(s)+len(other))
	for c := range s {
		out[c] = true
	}
	for c := range other {
		out[c] = true
	}
	return out
}

// Validate 校验集合的闭集合约束与非空不变式。
func (s ClassSet) Validate(field string) error {
	if s.IsEmpty() {
		return fmt.Errorf("%s: 权限类集合不能为空（01-accounts.md §1 不变式 1）", field)
	}
	for c := range s {
		if !c.Valid() {
			return fmt.Errorf("%s: 非法权限类 %q", field, c)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 账户
// ---------------------------------------------------------------------------

// AccountStatus 是账户状态机取值。唯一事实源：docs/security/01-accounts.md 第 5 节。
type AccountStatus string

const (
	StatusPending  AccountStatus = "pending"  // 注册后默认；未审核不能登录
	StatusActive   AccountStatus = "active"   // 唯一正常使用态
	StatusDisabled AccountStatus = "disabled" // 可逆止血
	StatusRejected AccountStatus = "rejected" // 从未通过审核
	StatusMerged   AccountStatus = "merged"   // 被并入其它账户
)

// CanLogin 报告该状态是否允许建立会话。
//
// 为什么把"能否登录"和"能否调用模型"分成两个方法而不是一个：
// 两者当前取值一致，但语义来源不同——登录看状态机第 1 道关卡，
// 调用还额外要求 tenant 类与 Key 合规（04-authz.md §5 禁用联动）。
// 合并成一个方法会在将来某个状态只关掉一侧时无法表达。
func (s AccountStatus) CanLogin() bool { return s == StatusActive }

// CanCallModels 报告该状态是否允许发起模型调用。
// 注意：状态只是必要条件之一，调用还需要 tenant 类且 Key 合规。
func (s AccountStatus) CanCallModels() bool { return s == StatusActive }

// Account 是账户模型：显示名 + 账户 ID + 权限类集合。
//
// 额度与速率字段在账户级、不在 Key 上（2026-10-20 定案）：
// 同一账户的所有 Key 共享同一份额度，因此"多开 Key 提高额度"这条绕过路径
// 在数据形状上就不存在。
type Account struct {
	ID          string
	DisplayName string // 人读称呼，**不承担唯一性**
	Username    string // 登录名，唯一（ux_accounts_username）

	// PasswordHash 是 argon2id 自描述串；只走 OAuth / 恢复码路径的账户可以为空。
	PasswordHash string
	// RecoveryCodeHash 与密码同一套 argon2id 参数：
	// 恢复码是人可抄写的短串，熵远低于 API Key，不能只做 SHA-256。
	RecoveryCodeHash string

	Classes ClassSet // 不变式：非空
	Status  AccountStatus

	CreatedAt   time.Time
	LastLoginAt *time.Time
	Note        string

	// 账户级速率与并发（一个账户的所有 Key 共享这一份；05-keys.md §4）。
	//
	// 哨兵语义（01-accounts.md 2026-10-20 定案，速率三列统一）：
	//   **-1 = 不限；0 = 全拒**。两者意思完全相反且禁止 NULL——
	//   “没配”必须写成 -1（不启用这道闸），写成 0 就是“一个请求都拒”。
	//   旧注释里的“由全局默认兜底”已废：limits.default_* 只在审批/导入时
	//   写初值，运行期合并不回退（03-groups.md §3.5）。
	//
	// 周期额度不在本 struct：额度已行化到 account_quotas 表
	// ((account_id, abstract_model_id, unit, period) → limit/used/anchor；
	// abstract_model_id=0 合计行、>0 按模型行)。“不限 = 没有这一行，
	// 0 = 零额度全拒”。预检由 meter 对 store 读出的行判定。
	RPMLimit       int   // 请求数/分钟；-1 不限；0 全拒
	TPMLimit       int64 // token/分钟；同上
	MaxConcurrency int   // 在途数；同上

	// MustChangePassword 表示当前口令是一次性口令（系统自举随机生成，
	// 或管理员重置时生成）：登录后必须先改密并补全资料，其它控制台接口
	// 一律 403 password_change_required。它是账户事实，不随会话变化。
	MustChangePassword bool
}

// HasClass 报告账户是否持有某个类。
func (a *Account) HasClass(c Class) bool {
	if a == nil {
		return false
	}
	return a.Classes.Has(c)
}

// Validate 校验账户的不变式。写库前必须调用（由 storage 适配器强制执行）。
func (a *Account) Validate() error {
	if a == nil {
		return fmt.Errorf("账户为空")
	}
	if a.ID == "" {
		return fmt.Errorf("账户 ID 不能为空")
	}
	if a.Username == "" {
		return fmt.Errorf("登录名不能为空")
	}
	if err := a.Classes.Validate("account.classes"); err != nil {
		return err
	}
	// 速率/并发哨兵范围校验（01-accounts.md 2026-10-20）：三列仅允许
	// -1（不限）、0（全拒）或正数；其余负数无语义，写库前拒绝。
	if a.RPMLimit < -1 || a.TPMLimit < -1 || a.MaxConcurrency < -1 {
		return fmt.Errorf("账户速率/并发列非法 %d/%d/%d：仅允许 -1=不限、0=全拒或正数", a.RPMLimit, a.TPMLimit, a.MaxConcurrency)
	}
	switch a.Status {
	case StatusPending, StatusActive, StatusDisabled, StatusRejected, StatusMerged:
	default:
		return fmt.Errorf("非法账户状态 %q", a.Status)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 主体与身份快照
// ---------------------------------------------------------------------------

// Subject 是判定链上的主体快照 = 账户 ID + 本次行为以哪个类进行。
//
// 为什么必须带 acting_class：一个账户可同时有多个类，判定必须知道
// "本轮操作以哪一重身份做的"，否则 admin 类的存在会自动抬高 tenant 侧的能力，
// 隔离的第一维度（能力不互授）当场失效。
type Subject struct {
	AccountID   string
	ActingClass Class
}

// CredentialKind 表示本次身份来自哪条凭据链。
//
// 为什么要显式区分：Key 是机器凭据，只应出现在模型面；会话是人的登录凭据，
// 承载管理面。两者的吊销联动（改密吊销会话但不吊销 Key）完全不同，
// 把它压成一个布尔在判断处必然出错。
type CredentialKind string

const (
	CredentialSession CredentialKind = "session"
	CredentialAPIKey  CredentialKind = "apikey"
)

// Principal 是一次成功鉴权的结果快照。
//
// 关键语义（2026-10-20 取消类级提权）：会话携带账户的**全部**权限类，
// 不存在"低权限会话"。Classes 是账户当时的类集合，ActingClass 是本轮
// 实际使用的那个类——两者都要带上，前者回答"他能做什么"，后者回答
// "这次是谁做的"（审计的 acting_class）。
type Principal struct {
	AccountID   string
	DisplayName string
	Classes     ClassSet
	ActingClass Class
	Kind        CredentialKind
	// MustChangePassword 从账户实时读入：会话不过期，但账户可能被管理员
	// 重置成一次性口令——强制改密要求必须每次校验都重新判定。
	MustChangePassword bool
	// Status 是账户当前状态机取值（pending 账户的界面让路要靠它）。
	Status AccountStatus

	SessionID string // Kind == session 时非空
	KeyID     string // Kind == apikey 时非空
}

// Subject 返回判定链可直接使用的主体。
func (p *Principal) Subject() Subject {
	return Subject{AccountID: p.AccountID, ActingClass: p.ActingClass}
}

// WithClass 切换本轮使用的类，用于同一账户在管理面与模型面之间切换身份。
//
// 为什么这不是"提权"：它只允许在账户**已经持有**的类之间选择，
// 不产生任何新的权限；账户没有的类一律返回错误。
func (p *Principal) WithClass(c Class) (*Principal, error) {
	if !c.Valid() {
		return nil, fmt.Errorf("非法权限类 %q", c)
	}
	if !p.Classes.Has(c) {
		return nil, fmt.Errorf("%w: 账户 %s 不持有 %q 类", ErrForbidden, p.AccountID, c)
	}
	clone := *p
	clone.ActingClass = c
	return &clone, nil
}

// ---------------------------------------------------------------------------
// 组
// ---------------------------------------------------------------------------

// Group 是一个组 = 一组策略 + 一批成员，是管理员的批量控制单元。
//
// 组**不持有** Key、不持有凭据、不提供端点（PLAN 2.6）：
// 一旦组持有 Key，"共享 Key"就等价于团队共享凭据，团队语义会从后门回来。
type Group struct {
	ID          string
	Name        string
	Description string
	IsDefault   bool // 唯一且不可删除
	CreatedAt   time.Time
}

// ---------------------------------------------------------------------------
// API Key
// ---------------------------------------------------------------------------

// KeyStatus 是 API Key 的生命周期状态。
//
// 为什么 disabled 与 revoked 必须分开：前者是运营动作（"先停一下"），
// 后者是安全动作（"这把已泄露"）。只有一种状态时，每一次临时停用都会
// 永久毁掉一把 Key，用户下一次就倾向于不报告异常——反而降低安全水位。
type KeyStatus string

const (
	KeyActive   KeyStatus = "active"
	KeyDisabled KeyStatus = "disabled" // 可恢复
	KeyRevoked  KeyStatus = "revoked"  // 单向，不可恢复
	KeyExpired  KeyStatus = "expired"  // 由 expires_at 推导
)

// APIKey 只有身份、生命周期与白名单三件事。
//
// 刻意**没有**额度字段：额度在账户级，Key 上出现 quota/rpm/tpm 字段
// 就等于"多开 Key 就是额度翻倍"。
type APIKey struct {
	ID        string
	AccountID string // 归属账户必须带 tenant 类
	KeyHash   []byte // SHA-256，不存明文
	Prefix    string // 展示用：fr-xxxx…xxxx
	Name      string
	Status    KeyStatus
	ExpiresAt *time.Time
	CreatedAt time.Time

	LastUsedAt *time.Time
}

// Expired 报告该 Key 是否因 expires_at 到期而失效。
//
// 为什么单独一个方法而不是把状态改成 expired 落库：
// 到期是时间的函数，落库会引入"谁来写"的问题（没人访问就永远不写），
// 推导则是无状态且恒正确的。
func (k *APIKey) Expired(now time.Time) bool {
	return k.ExpiresAt != nil && !now.Before(*k.ExpiresAt)
}

// Usable 报告该 Key 自身是否可用于调用（不含归属账户状态）。
//
// 为什么拆出"不含账户状态"这一层：判定链第 3 步（Key 自身属性）与
// 第 4 步（账户实时状态）是两次不同代价的检查，前者可以本地判定，
// 后者必须读账户表。混在一起会让便宜的检查也付出昂贵代价。
func (k *APIKey) Usable(now time.Time) bool {
	if k == nil {
		return false
	}
	return k.Status == KeyActive && !k.Expired(now)
}

// ---------------------------------------------------------------------------
// 会话
// ---------------------------------------------------------------------------

// Session 是会话记录。只存 token_hash，不存会话令牌明文。
//
// 为什么没有 elevated_until：2026-10-20 取消类级提权，会话不再有
// "低权限/高权限"两态，因此也不需要记录提权到期时刻。
type Session struct {
	ID        string
	AccountID string
	TokenHash []byte // SHA-256(token)，32 字节
	CreatedAt time.Time
	// ExpiresAt = CreatedAt + session_ttl（默认 24h）。
	// 不做滑动续期：滑动续期会把"有效期"变成"最后一次操作后 24 小时"，
	// 长期开着的标签页就成了永久会话；固定窗口才能给出可预测的上界。
	ExpiresAt time.Time
	IP        string // 仅留痕，不做强绑定
	UA        string // 仅留痕
}

// Expired 报告会话是否已过期。
func (s *Session) Expired(now time.Time) bool {
	return !now.Before(s.ExpiresAt)
}

// ---------------------------------------------------------------------------
// 共享池引用
// ---------------------------------------------------------------------------

// PoolRef 指向一个已配置的组级共享池。
//
// 为什么不把池合并成一个数：池是逐组独立的资源，合并会丢掉
// "是哪个组的池先耗尽"这个归因信息，而错误码（pool_exhausted）与审计都需要它。
type PoolRef struct {
	GroupID string
	// AbstractModelID：0 = 全模型合计池；>0 = 按模型的组级共享池
	// （group_quota_pools 的哨兵列，03-groups.md §4.5 硬规则：同一模型上
	// "每人按模型上限"与"模型级池"是两道独立闸，都是交集）。
	// 用 int64 与 storage 主键同形，避免字符串 id 的映射层再翻一次。
	AbstractModelID int64
	Unit            string // request / token / credit
	Period          string // day / month
	Limit           int64  // 严格正整数；不设池由"没有这一行"表达
	Used            int64
}

// PoolExhausted 报告在给定消耗下该池是否会耗尽。
// 判定形式与存储层的单条条件 UPDATE 对齐：used + cost <= limit。
func (p PoolRef) PoolExhausted(cost int64) bool {
	return p.Used+cost > p.Limit
}
