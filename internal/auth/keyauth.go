package auth

import (
	"context"
	"time"
)

// Key 鉴权：L1 中间件链第 5 步（鉴权）在**机器凭据**（API Key）上的落点，
// 对应 docs/security/04-authz.md §4 运行时鉴权顺序的第 2~4 步（Key 格式 →
// 哈希查表 → 账户实时状态）。
//
// 为什么独立于 SessionManager.Validate：两条凭据链的失效语义完全不同——
// 会话随改密/登出吊销，Key 随吊销/过期/账户禁用失效；把两者压进一个
// Validate 会让"吊销粒度"在判断处必然出错（02-authn §2 的凭据分形）。
// 本文件只做鉴权（"这把 Key 是谁的"）；可见性判定（组授权 ∩ 白名单）
// 与额度预检不在这里——它们分别是 AuthzService 与 meter 的职责。

// KeyPrincipal 是 Key 鉴权成功后的主体快照。
//
// 为什么 ActingClass 恒为 ClassTenant：Key 只出现在模型调用面，而模型调用
// 能力**只有** tenant 类提供（01-accounts.md §2 推论 1）；不存在"以 admin
// 类持有 Key 调模型"的合法路径，admin/contributor 持有的 Key 在这里就该
// 被拒（HasClass(ClassTenant) 检查），而不是被降级成 tenant。
type KeyPrincipal struct {
	AccountID string
	KeyID     string
	Classes   ClassSet
	// ActingClass 固定填 ClassTenant（构造函数保证），字段保留是为了
	// 与会话侧 Principal 的形状一致——下游（C4 审计、计量归属）只读
	// 这三个字段，不需要分叉两条读取路径。
	ActingClass Class
}

// KeyAuthStore 是 Key 鉴权的最小存储面（窄接口，05-keys.md 口径）。
//
// 为什么不直接复用 storeiface 的 KeyRepository/AccountRepository 大接口：
// 鉴权热路径只需要两个读；让中间件依赖大接口等于宣布"鉴权层可以创建/
// 轮换 Key"——能力面与职责面必须同宽，否则将来某个顺手调用就是一次越权。
// adapter（internal/meter/adapter.go）负责把 store 仓储接到这个形状上。
type KeyAuthStore interface {
	// KeyByHash 按 SHA-256 哈希查 Key。不存在返回 ErrNotFound。
	KeyByHash(ctx context.Context, keyHash []byte) (*APIKey, error)

	// AccountByID 实时读账户（状态 + 权限类）。禁用联动要求**每次**
	// 鉴权都读、不走缓存（04-authz.md §5：缓存必须有失效机制，
	// 而最便宜的失效机制就是不缓存）。
	AccountByID(ctx context.Context, accountID string) (*Account, error)
}

// KeyAuthenticator 校验裸 Key 明文并产出主体。
type KeyAuthenticator struct {
	store KeyAuthStore
	now   func() time.Time
}

// NewKeyAuthenticator 构造。store 为 nil 时 Authenticate 返回 internal_error
// （装配错误要在第一枪就暴露，而不是静默全拒/全放）。
func NewKeyAuthenticator(store KeyAuthStore) *KeyAuthenticator {
	return &KeyAuthenticator{store: store, now: time.Now}
}

// WithClock 注入时钟（测试用；过期判定不靠 sleep）。
func (a *KeyAuthenticator) WithClock(now func() time.Time) *KeyAuthenticator {
	a.now = now
	return a
}

// Authenticate 执行 04-authz.md §4 的第 2~4 步。
//
// 失败**全部**收敛为 ErrInvalidAPIKey（401 invalid_api_key，03-errors §3.1）：
// 格式错、查无此 Key、Key 停用/吊销/过期、账户禁用/被撤 tenant 类——
// 对外**一个出口**是防枚举纪律（02-authn §4 的机器凭据版）：让探测者
// 无法用响应差异区分"Key 存在但停用"与"Key 不存在"。
// 差异只进日志（WithDetail 的 dimension），不进响应体。
func (a *KeyAuthenticator) Authenticate(ctx context.Context, rawKey string) (*KeyPrincipal, error) {
	if a.store == nil {
		return nil, Errorf(CodeInternal, nil, "KeyAuthenticator 缺少存储依赖")
	}

	// 第 2 步：格式。不是本系统格式的凭据在最便宜的阶段拒掉，
	// 不浪费一次索引探测（LooksLikeKey 的设计用途）。
	if !LooksLikeKey(rawKey) {
		return nil, ErrInvalidAPIKey
	}

	// 第 3 步：SHA-256 哈希查表。查表本身即等值判定（B 树索引对
	// 32 字节哈希做全长度比较）；查到后再走一次常量时间比较
	// ConstantTimeEqual(key.KeyHash, hash)，对内存实现是真实比较，
	// 对按哈希查表的存储适配器是"查表已证等值"的形式确认——
	// 保持全项目"凭据比较只有 subtle 一种写法"的纪律（password.go）。
	hash := HashToken(rawKey)
	key, err := a.store.KeyByHash(ctx, hash)
	if err != nil {
		return nil, ErrInvalidAPIKey // 含 ErrNotFound 与存储抖动：不给探测面
	}
	if !ConstantTimeEqual(key.KeyHash, hash) {
		return nil, ErrInvalidAPIKey
	}

	// Key 自身状态：active 才可用；disabled/revoked/expired（推导）同码拒绝。
	now := a.now()
	if !key.Usable(now) {
		return nil, ErrInvalidAPIKey
	}

	// 第 4 步：账户实时状态 + tenant 类。禁用/撤销类联动在**这里**兜住，
	// 不依赖存储层把账户禁用写穿到 Key 表（05-keys.md §2 联动表）。
	acct, err := a.store.AccountByID(ctx, key.AccountID)
	if err != nil {
		return nil, ErrInvalidAPIKey
	}
	if !acct.Status.CanCallModels() || !acct.HasClass(ClassTenant) {
		return nil, ErrInvalidAPIKey
	}

	return &KeyPrincipal{
		AccountID:   acct.ID,
		KeyID:       key.ID,
		Classes:     acct.Classes,
		ActingClass: ClassTenant,
	}, nil
}
