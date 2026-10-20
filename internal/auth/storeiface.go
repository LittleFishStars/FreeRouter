package auth

import (
	"context"
	"time"
)

// 本文件定义本层**需要**的持久化能力，而不是持久化层的形状。
//
// 为什么要有这一层（架构边界）：
//   - 本分支不允许 import internal/store（那个包还不存在），
//     而 internal/auth 又不能自己写 SQLite（表结构属于 storage 的领域，
//     两处各写一份 DDL 必然漂移）。
//   - 因此这里只声明**窄接口**：方法名表达语义（"按 username 取账户"），
//     不表达存储（不出现 SQL、事务、表名）。storage 分支用一个适配器实现它。
//   - 测试用内存假体（memory_fake_test.go）实现同一组接口。
//
// 与 storage 对齐时需要确认的几处语义（见交付说明的遗留问题）：
//  1) AccountByUsername 对不存在必须返回 ErrNotFound（而不是 (nil, nil)），
//     否则登录的"哑哈希"分支无法与"存在"分支区分；
//  2) 所有 List 方法必须返回**拷贝**，调用方会修改（例如并集运算）；
//  3) 写方法必须是单事务的（RotateKey 尤其）：轮换不能出现
//     "旧的已吊销、新的没写进去"的中间态；
//  4) 时间一律由调用方注入（本包不调用 time.Now 决定语义），
//     这样锁定/窗口/过期测试不需要 sleep。

// AccountRepository 是账户读取与登录相关写入的窄接口。
//
// 为什么不提供"列出全部账户"：本层不做管理面列表，多一个方法就多一处
// 权限校验可能被绕过的地方。
type AccountRepository interface {
	// AccountByID 按账户 ID 取账户。不存在返回 ErrNotFound。
	AccountByID(ctx context.Context, id string) (*Account, error)

	// AccountByUsername 按登录名取账户（ux_accounts_username）。
	// 不存在返回 ErrNotFound（**不是** nil, nil）。
	AccountByUsername(ctx context.Context, username string) (*Account, error)

	// TouchLastLogin 记录最近登录时刻。失败不应阻断登录：
	// 它是留痕字段，不是安全判定依据。
	TouchLastLogin(ctx context.Context, accountID string, at time.Time) error
}

// SessionRepository 是会话的读写接口。
type SessionRepository interface {
	// CreateSession 写入一条会话；id 与 token_hash 的唯一性由存储保证。
	CreateSession(ctx context.Context, s *Session) error

	// SessionByTokenHash 按 token 的 SHA-256 取会话（走唯一索引，一次 B 树探测）。
	// 不存在返回 ErrNotFound。
	SessionByTokenHash(ctx context.Context, tokenHash []byte) (*Session, error)

	// DeleteSession 删除单个会话（登出）。
	DeleteSession(ctx context.Context, sessionID string) error

	// DeleteSessionsByAccount 删除该账户的全部会话，可选保留某一条。
	//
	// keepSessionID 非空时保留它：自助改密"是否保留当前会话"在
	// 02-authn.md 里仍是待定，接口必须能表达两种选择，默认实现取"一并吊销"
	// （更保守），但调用方可以显式保留。
	DeleteSessionsByAccount(ctx context.Context, accountID, keepSessionID string) error
}

// KeyRepository 是 API Key 的读写接口。
type KeyRepository interface {
	// CreateKey 写入一把 Key。调用方负责已做数量与归属校验。
	CreateKey(ctx context.Context, k *APIKey) error

	// KeyByHash 按 SHA-256 取 Key（ux_api_keys_key_hash）。
	// 不存在返回 ErrNotFound。
	KeyByHash(ctx context.Context, keyHash []byte) (*APIKey, error)

	// KeyByID 按 ID 取 Key。不存在返回 ErrNotFound。
	KeyByID(ctx context.Context, keyID string) (*APIKey, error)

	// ListKeysByAccount 返回该账户名下全部 Key（含已吊销的，供数量与审计）。
	ListKeysByAccount(ctx context.Context, accountID string) ([]APIKey, error)

	// SetKeyStatus 改状态。revoked 是单向的：从 revoked 转回 active
	// 必须由本层拒绝，不能依赖调用方自觉。
	SetKeyStatus(ctx context.Context, keyID string, status KeyStatus) error

	// RenameKey 改名（不影响鉴权）。
	RenameKey(ctx context.Context, keyID, name string) error

	// RotateKey 在**一个事务内**吊销 oldKeyID 并写入 newKey。
	//
	// 为什么必须是一个方法而不是"调用方先 Revoke 再 Create"：
	// 两步之间进程崩溃会留下"旧 Key 已废、新 Key 未生效"的状态，
	// 用户被锁在门外且无凭据可用。
	RotateKey(ctx context.Context, oldKeyID string, newKey *APIKey) error
}

// GroupPolicy 是一个组的策略快照（storage 把 group_models / group_policies /
// group_quota_pools 三张表拼成这个结构）。
//
// 为什么用**强类型字段**而不是 map[string]string：
// 合并算法是逐键的（放开取并集 / 限制取最严），策略键一旦是字符串，
// "该键属于放开类还是限制类"就只能在运行期查表，拼错的键会静默失效。
// 强类型让"新增策略键必须同时在合并算法里出现"成为编译期约束。
type GroupPolicy struct {
	Group  Group
	Models []string // group_models：可访问的抽象模型（放开类，并集）

	// 放开类布尔开关：nil = 该组未设置（不参与合并）。
	CanCreateKey      *bool
	KeyAllowlistWiden *bool

	// 放开类数值：nil = 未设置；多组取**最大值**（Key 数量上限的定案）。
	MaxKeys *int

	// 限制类：逐 (unit, period) 键独立取 min，互不比较。
	// 键形如 "token/day"、"request/month"、"credit/day"。
	Quotas map[QuotaKey]int64

	// 限制类速率与并发：nil = 未设置，多组取 min。
	RPMLimit       *int
	TPMLimit       *int64
	MaxConcurrency *int

	// 组级共享池（限制类，但**不合并**：逐个都要满足）。
	Pools []PoolRef
}

// QuotaKey 是"计量维度 × 周期"的键。
//
// 为什么不能用单一数值：token 下的 100 与 credit 下的 50 无法比谁更严，
// 把它们压成一个数会让"取最严"这个算子当场失效（03-groups.md §3）。
type QuotaKey struct {
	Unit   string // request / token / credit
	Period string // day / month
}

// String 返回稳定可比较的键文本（用于日志、错误细节与 map 键的展示）。
func (q QuotaKey) String() string { return q.Unit + "/" + q.Period }

// PolicyRepository 提供"按账户拿到他全部所属组的策略"这一条读取路径。
//
// 为什么不暴露"按 groupID 取策略"：本层只做合并计算，
// 按组取会诱导调用方自己写一遍合并逻辑——而那正是最容易写错的部分。
type PolicyRepository interface {
	// GroupsForAccount 返回该账户所属的全部组（含默认组）。
	// 不变式：至少要有一条（默认组保证"任何账户永远至少有一组策略"）。
	GroupsForAccount(ctx context.Context, accountID string) ([]GroupPolicy, error)
}

// Stores 把四个窄接口打成一个依赖包，便于 Authn 构造时一次性注入。
//
// 为什么允许某个字段为 nil：本层有几块功能彼此独立
// （例如只做会话校验的中间件不需要 KeyRepository），
// 用 nil 表达"这条路径不需要"比强制实现空方法更诚实；
// 真的用到了 nil 字段会返回明确的错误而不是 panic。
type Stores struct {
	Accounts AccountRepository
	Sessions SessionRepository
	Keys     KeyRepository
	Policies PolicyRepository
}
