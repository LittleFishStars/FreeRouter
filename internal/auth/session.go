package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strings"
	"time"
)

// SessionPolicy 是会话相关的策略参数，全部来自配置（security.session_ttl），不硬编码。
type SessionPolicy struct {
	// TTL 是会话有效期（默认 24h）。
	// 固定窗口、不做滑动续期：理由见 02-authn.md §5 —— 滑动续期会让长期开着的
	// 标签页变成永久会话，固定窗口才能给出「一次登录的最大暴露时长」这个上界。
	TTL time.Duration
}

func (p SessionPolicy) effectiveTTL() time.Duration {
	if p.TTL <= 0 {
		return 24 * time.Hour // 配置缺失时绝不退化成「永不过期」
	}
	return p.TTL
}

// SessionManager 负责会话的签发、校验与吊销。
//
// 它**不解析 Cookie**：Cookie 属性（HttpOnly / Secure / SameSite）与请求头读取
// 属于协议层（internal/httpapi），本层只管「令牌 ↔ 账户」。这样同一套逻辑
// 既能服务浏览器登录态，也能服务 CLI 令牌。
type SessionManager struct {
	policy SessionPolicy
	stores Stores

	// now / rand 注入：过期判定必须可在测试里确定性推进，不允许 sleep。
	now  func() time.Time
	rand io.Reader
}

// NewSessionManager 构造会话管理器。
func NewSessionManager(policy SessionPolicy, stores Stores) *SessionManager {
	return &SessionManager{
		policy: policy,
		stores: stores,
		now:    time.Now,
		rand:   rand.Reader,
	}
}

// WithClock 注入时钟（测试用）。
func (m *SessionManager) WithClock(now func() time.Time) *SessionManager {
	m.now = now
	return m
}

// IssuedSession 是签发的会话与**一次性明文令牌**。
type IssuedSession struct {
	Session *Session

	// Token 是令牌明文，**只在这次调用里出现**；存储层只拿到它的 SHA-256。
	// 协议层需要它一次来写 Set-Cookie，此后没有任何组件需要再看它。
	Token string
}

// Issue 为账户签发一条会话。
//
// 调用方（LoginService）本已确认账户可登录；这里再查一次是纵深防御——
// 签发会话是唯一「制造通行证」的动作，它的入口条件不能靠调用方自觉。
func (m *SessionManager) Issue(ctx context.Context, account *Account, ip, ua string) (*IssuedSession, error) {
	if m.stores.Sessions == nil {
		return nil, Errorf(CodeInternal, nil, "SessionManager 缺少会话存储")
	}
	if account == nil {
		return nil, Errorf(CodeInternal, nil, "签发会话缺少账户")
	}
	if !account.Status.CanLogin() {
		return nil, Errorf(CodeForbidden, nil, "账户状态 %s 不能建立会话", account.Status)
	}

	token, err := m.generateToken()
	if err != nil {
		return nil, err
	}
	now := m.now()
	s := &Session{
		ID:        newID("sess"),
		AccountID: account.ID,
		TokenHash: HashToken(token),
		CreatedAt: now,
		ExpiresAt: now.Add(m.policy.effectiveTTL()),
		IP:        ip,
		UA:        ua,
	}
	if err := m.stores.Sessions.CreateSession(ctx, s); err != nil {
		return nil, err
	}
	return &IssuedSession{Session: s, Token: token}, nil
}

// generateToken 生成会话令牌明文：`frs-` + 32 字节 base64url。
//
// 为什么前缀与 API Key（fr-）不同：两者走完全不同的判定链与吊销联动
// （Key 只服务模型面、会话只服务管理面）。可区分的 scheme 让误用一眼可见，
// 也让日志脱敏规则能分别命中。
func (m *SessionManager) generateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(m.rand, raw); err != nil {
		return "", Errorf(CodeInternal, err, "生成会话令牌")
	}
	return "frs-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Validate 按令牌明文取回会话主体。
//
// actingClass 传空 = 由本层按「最低权限优先」选取（见 resolveActingClass）；
// 传具体类 = 本轮以该类行动，但必须是账户真实持有的类（04-authz.md §1）。
//
// 顺序即成本顺序（04-authz.md §6 管理面鉴权 1→2）：先查会话（一次索引探测 +
// 本地过期判定），再实时读账户状态——「会话还在但账户已被禁用」的窗口
// 必须由后者堵住，所以账户状态每次校验都读、不走缓存。
func (m *SessionManager) Validate(ctx context.Context, token, actingClass string) (*Principal, error) {
	if m.stores.Sessions == nil || m.stores.Accounts == nil {
		return nil, Errorf(CodeInternal, nil, "SessionManager 缺少存储依赖")
	}
	if token == "" {
		return nil, ErrSessionExpired
	}
	s, err := m.stores.Sessions.SessionByTokenHash(ctx, HashToken(token))
	if err != nil {
		// 不存在 / 已被登出删除 / 存储抖动，对调用方含义相同：重新登录。
		return nil, ErrSessionExpired
	}
	if s.Expired(m.now()) {
		return nil, ErrSessionExpired
	}
	acct, err := m.stores.Accounts.AccountByID(ctx, s.AccountID)
	if err != nil {
		return nil, err
	}
	if !acct.Status.CanLogin() {
		// 「禁用即吊销全部会话」是存储层的主路径，这里是它的实时兜底
		// （01-accounts.md §5：disabled 不能登录）。
		return nil, Errorf(CodeForbidden, nil, "账户状态 %s 已不可用", acct.Status)
	}
	cls, err := resolveActingClass(acct, actingClass)
	if err != nil {
		return nil, err
	}
	return &Principal{
		AccountID:   acct.ID,
		DisplayName: acct.DisplayName,
		Classes:     acct.Classes, // 会话携带**全部**权限类（2026-10-20 取消类级提权）
		ActingClass: cls,
		Kind:        CredentialSession,
		// 强制改密标志与状态每次校验都从账户实时读取（管理员随时可能改）。
		MustChangePassword: acct.MustChangePassword,
		Status:             acct.Status,
		SessionID:          s.ID,
	}, nil
}

// defaultClassPrecedence 是「未显式指定 acting_class 时」的取类顺序。
//
// 为什么最低权限排在最前：一个同时带 contributor 与 admin 的账户在贡献侧
// 行动时，不应因为「遍历 map 先撞到了 admin」就以 admin 身份记审计。
// 默认值必须是账户持有的权限最小的类；以更高类行动永远是一次显式选择。
var defaultClassPrecedence = []Class{ClassContributor, ClassTenant, ClassAdmin}

// resolveActingClass 确定本轮以哪个类行动。非法/缺失一律拒绝，绝不静默升降级。
func resolveActingClass(acct *Account, actingClass string) (Class, error) {
	if actingClass != "" {
		c := Class(actingClass)
		if !c.Valid() {
			return "", Errorf(CodeInvalidRequest, nil, "非法权限类 %q", actingClass)
		}
		if !acct.Classes.Has(c) {
			return "", Errorf(CodeForbidden, nil, "账户不持有 %q 类", actingClass)
		}
		return c, nil
	}
	for _, c := range defaultClassPrecedence {
		if acct.Classes.Has(c) {
			return c, nil
		}
	}
	return "", Errorf(CodeForbidden, nil, "账户权限类集合为空（不变式违反）")
}

// Logout 登出（删除单条会话）。
//
// 幂等：sessionID 为空或会话已被删除时返回 nil——登出的语义是「此后不再生效」，
// 一条不存在的会话早已满足它；把「找不到」写成错误只会让前端重试时看到含义不明的失败。
func (m *SessionManager) Logout(ctx context.Context, sessionID string) error {
	if m.stores.Sessions == nil {
		return Errorf(CodeInternal, nil, "SessionManager 缺少会话存储")
	}
	if sessionID == "" {
		return nil
	}
	return m.stores.Sessions.DeleteSession(ctx, sessionID)
}

// LogoutAll 吊销该账户全部会话（keepSessionID 非空则保留它）。
//
// 触发点必须穷举（02-authn.md §5）：改密 / 管理员重置 / 撤销 admin 类 /
// 转 disabled / 账户合并。全部收敛到这一个方法是为了让「穷举」发生在一处——
// 调用点越多，漏掉一处的概率越高。
func (m *SessionManager) LogoutAll(ctx context.Context, accountID, keepSessionID string) error {
	if m.stores.Sessions == nil {
		return Errorf(CodeInternal, nil, "SessionManager 缺少会话存储")
	}
	if accountID == "" {
		return Errorf(CodeInvalidRequest, nil, "缺少账户 ID")
	}
	return m.stores.Sessions.DeleteSessionsByAccount(ctx, accountID, keepSessionID)
}

// ParseBearer 从 Authorization 头值里取出凭据明文。
//
// 为什么本层要碰这个字符串：判定链第 2 步要在查库之前分出「这是 Key 还是会话」，
// 这个判断的输入就是头值。协议层负责读头，本层负责语义识别（LooksLikeKey），
// 边界清楚，也避免协议层为鉴权自带一份 scheme 解析。
func ParseBearer(headerValue string) string {
	v := strings.TrimSpace(headerValue)
	if v == "" {
		return ""
	}
	// RFC 7235：scheme 大小写不敏感。
	if len(v) >= 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return v
}
