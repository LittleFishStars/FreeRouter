package auth

import (
	"context"
	"crypto/rand"
	"io"
	"math/big"
	"strings"
	"sync"
	"time"
)

// LoginPolicy 是登录防护的参数集。
//
// 全部字段从配置（security.*）注入；零值回落的默认是 02-authn.md §10
// 「2026-10-20 升级为必须定」的那组数——写死在文档里的东西在代码里也只有一个
// 具名回落点，而不是散落在各判定处的魔法数。
type LoginPolicy struct {
	// Window 是失败计数窗口（默认 15 分钟）。窗口太长会把"偶尔记错密码"
	// 累积成锁定；太短则慢速爆破靠时间分散绕过。
	Window time.Duration

	// BackoffAfter 是开始退避的连续失败次数（默认 3：前 2 次留给正常手误）。
	BackoffAfter int

	// MaxBackoffDelay 是指数退避封顶（默认 60 秒）：让合法用户在记错四五次后
	// 仍能在一分钟内重试，而不是等十分钟。
	MaxBackoffDelay time.Duration

	// AccountLockAfter / IPLockAfter 是两个维度的锁定阈值。
	// **生产路径由 config.security.login_max_failures 注入**（lead 仲裁③：
	// config 表口径 5 次/15m 优先于 02-authn 分册的 10/50；后者已转呈用户
	// 裁决为文档待统一项）。这里的零值兜底与 config.Default() 对齐：
	// 账户 5；IP 阈值 config 无对应键，保留分册的 50——它仍满足
	// "IP 显著高于账户"（共享出口下一个同事输错不能锁掉整栋楼，
	// 账户维度要拦住盯着一个账户硬猜）。
	AccountLockAfter int
	IPLockAfter      int

	// LockDuration 是锁定时长（默认 15 分钟，与计数窗口同量级，
	// 使"锁定期满自动解锁"的语义简单）。
	LockDuration time.Duration

	// KeepCurrentSessionOnSelfChange 决定自助改密后当前会话是否保留。
	// 02-authn.md §4 仍是待定；接口必须能表达两种选择，默认取"一并吊销"
	// （更保守：改密的语义是"之前的凭据不再可信"）。
	KeepCurrentSessionOnSelfChange bool
}

func (p LoginPolicy) withDefaults() LoginPolicy {
	if p.Window <= 0 {
		p.Window = 15 * time.Minute
	}
	if p.BackoffAfter <= 0 {
		p.BackoffAfter = 3
	}
	if p.MaxBackoffDelay <= 0 {
		p.MaxBackoffDelay = 60 * time.Second
	}
	if p.AccountLockAfter <= 0 {
		p.AccountLockAfter = 5 // = config.Default().Security.LoginMaxFailures（仲裁③）
	}
	if p.IPLockAfter <= 0 {
		p.IPLockAfter = 50
	}
	if p.LockDuration <= 0 {
		p.LockDuration = 15 * time.Minute
	}
	return p
}

// BackoffDelay 返回第 failures 次失败之后的建议退避。
//
// 曲线（02-authn.md §10 定案）：前 2 次为 0；第 3 次起 min(2^(n-2) 秒, 封顶)。
// 做成纯函数是为了让测试直接断言曲线，而不是从错误里反推。
func (p LoginPolicy) BackoffDelay(failures int) time.Duration {
	if failures < p.withDefaults().BackoffAfter {
		return 0
	}
	lim := p.withDefaults()
	// 2^(n-2) 秒；n 很大时指数会溢出，所以先比较再乘。
	shift := failures - 2
	if shift >= 30 {
		return lim.MaxBackoffDelay
	}
	d := time.Duration(int64(1)<<shift) * time.Second
	if d > lim.MaxBackoffDelay || d <= 0 {
		return lim.MaxBackoffDelay
	}
	return d
}

// ---------------------------------------------------------------------------
// 失败计数与锁定（内存态）
// ---------------------------------------------------------------------------

// failState 是一个限速键（账户维度或 IP 维度）的当前失败状态。
type failState struct {
	count     int
	firstAt   time.Time // 本轮窗口锚点：首次失败（或窗口重置）的时刻，用于窗口过期判定
	lastAt    time.Time // 最近一次失败时刻，用于退避计时
	lockedOut time.Time // 非零且 > now 表示锁定中
}

// LoginGuard 是账户/IP 两维的失败计数器与锁定表。
//
// 为什么是内存态而不是落库：02-authn.md §10 待定"重启后是否保留锁定"，
// 当前按不保留实现——重启丢锁定会让爆破者获得免费重试，但换来的是
// 不需要为锁定新增存储面，且锁定窗口只有 15 分钟，攻击者重启一次服务的
// 成本远低于撞开一个 12 字符 argon2id 口令的成本。将来若定案落库，
// 只需要把这份 map 换成一个 Repository，判定语义不动。
//
// 为什么账户与 IP 分开锁定、互不包含（文档定案）：合并锁定会让一次针对
// 单个账户的爆破顺带锁掉整个出口网段——攻击者反而得到一条廉价 DoS。
type LoginGuard struct {
	mu     sync.Mutex
	policy LoginPolicy
	states map[string]*failState
	now    func() time.Time
}

// NewLoginGuard 构造失败计数表。
func NewLoginGuard(policy LoginPolicy) *LoginGuard {
	return &LoginGuard{
		policy: policy.withDefaults(),
		states: make(map[string]*failState),
		now:    time.Now,
	}
}

// WithClock 注入时钟。
func (g *LoginGuard) WithClock(now func() time.Time) *LoginGuard {
	g.now = now
	return g
}

func accountKey(identifier string) string { return "acct:" + strings.ToLower(identifier) }
func ipKey(ip string) string              { return "ip:" + ip }

// stateLocked 取（或建立）某键的状态并把过期的窗口清零。调用方必须持锁。
func (g *LoginGuard) stateLocked(key string) *failState {
	st, ok := g.states[key]
	if !ok {
		st = &failState{}
		g.states[key] = st
	}
	now := g.now()
	// 滑动窗口下界：距上一笔失败超过整个窗口，历史清零——
	// "记错密码"的旧账不该在半小时后还攒着。锁定到期同样清零。
	// 固定窗口：距**首次**失败超过整个窗口，历史清零。
	// 为什么用 firstAt 而不是最近失败时刻：滑动窗口会让持续爆破者永远
	// 碰不到「窗口过期」，锁定阈值反而形同虚设。
	if st.count > 0 && !st.firstAt.IsZero() && now.Sub(st.firstAt) > g.policy.Window {
		st.count, st.firstAt, st.lastAt = 0, time.Time{}, time.Time{}
	}
	// 锁定到期自动解锁，同时清零计数（锁定期满 = 新一轮窗口）。
	if !st.lockedOut.IsZero() && !now.Before(st.lockedOut) {
		st.lockedOut, st.count, st.firstAt, st.lastAt = time.Time{}, 0, time.Time{}, time.Time{}
	}
	return st
}

// Blocked 报告某键当前是否处于锁定或退避期，返回应告知客户端的 Retry-After。
//
// 只报告、不消耗：登录入口先问一次，避免"锁定期内每一次尝试仍然真算一次
// argon2id"——那会让锁定本身成为放大 CPU 消耗的开关（DoS 反向放大）。
func (g *LoginGuard) Blocked(key string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.stateLocked(key)
	now := g.now()
	if !st.lockedOut.IsZero() && now.Before(st.lockedOut) {
		return st.lockedOut.Sub(now), true
	}
	if st.count >= g.policy.BackoffAfter {
		if d := g.policy.BackoffDelay(st.count); d > 0 {
			// 退避期 = 自最后一次失败起等待 BackoffDelay(count)；
			// lastAt 每笔失败都会被刷新，所以这里的等待只针对最近一次。
			waited := now.Sub(st.lastAt)
			if waited < d {
				return d - waited, true
			}
		}
	}
	return 0, false
}

// RecordFailure 记一笔失败，返回该键当前的失败计数。
// 达到该维度阈值时进入锁定。
func (g *LoginGuard) RecordFailure(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.stateLocked(key)
	now := g.now()
	if st.firstAt.IsZero() {
		st.firstAt = now // 新一轮窗口的锚点
	}
	st.count++
	st.lastAt = now
	if st.count >= g.lockThreshold(key) {
		st.lockedOut = now.Add(g.policy.LockDuration)
	}
	return st.count
}

// lockThreshold 按键前缀选择阈值：账户 10 / IP 50（文档定案，两维不同）。
func (g *LoginGuard) lockThreshold(key string) int {
	if strings.HasPrefix(key, "ip:") {
		return g.policy.IPLockAfter
	}
	return g.policy.AccountLockAfter
}

// Reset 在某键成功登录时清零（含锁定：成功登录证明来的是本人）。
func (g *LoginGuard) Reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.states, key)
}

// Unlock 管理员提前解锁某个账户维度（键为登录名）。解锁动作的审计由调用方负责。
func (g *LoginGuard) Unlock(username string) { g.Reset(accountKey(username)) }

// ---------------------------------------------------------------------------
// LoginService
// ---------------------------------------------------------------------------

// LoginService 是密码/恢复码登录、改密与重置的编排层。
//
// 它把三块彼此独立的机制（argon2 校验、会话签发、失败计数）串成
// 02-authn.md 规定的顺序，并且是"防枚举"唯一有机会写对的地方：
// 两条分支的文案、错误码、工作量必须逐项对齐，散在调用方必然漂移。
type LoginService struct {
	stores  Stores
	hasher  *PasswordHasher
	sess    *SessionManager
	guard   *LoginGuard
	policy  LoginPolicy
	randSrc io.Reader
}

// NewLoginService 组装登录服务。hasher/sess/guard 任一为 nil 时对应路径报错，
// 不 panic——装配错误要能在第一个请求上以 500 暴露，而不是在进程里炸。
func NewLoginService(stores Stores, hasher *PasswordHasher, sess *SessionManager, guard *LoginGuard, policy LoginPolicy) *LoginService {
	return &LoginService{
		stores:  stores,
		hasher:  hasher,
		sess:    sess,
		guard:   guard,
		policy:  policy.withDefaults(),
		randSrc: rand.Reader,
	}
}

// Login 校验账密并签发会话。
//
// 顺序与防枚举（02-authn.md §9）：
//  1. 两维 gate 先问（锁定/退避中直接拒，不付哈希成本）；
//  2. 取账户；不存在 → 哑哈希（等价工作量）→ 与"密码错误"同一 ErrInvalidCredentials；
//  3. 状态不可登录 → 仍然真验一次密码再返回同一错误：
//     若直接返回，"存在但被禁用"就比"不存在"快，时间差把账户库刷出来了；
//  4. 密码通过 → 清计数、TouchLastLogin、签发会话。
//
// 锁定/退避命中时返回 ErrTooManyAttempts + Retry-After；这是**限流面**的响应，
// 与凭据面文案不同不违反防枚举——防枚举保护的是"存在性"，而锁定信息
// 对存在与不存在的键同样可得（爆破者拿不到额外事实）。
func (s *LoginService) Login(ctx context.Context, username, password, ip, ua string) (*IssuedSession, error) {
	if s.stores.Accounts == nil || s.hasher == nil || s.sess == nil || s.guard == nil {
		return nil, Errorf(CodeInternal, nil, "LoginService 依赖未装配完整")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, ErrInvalidCredentials // 空用户名也走"凭据错误"同一出口，不给格式差异留探测面
	}

	aKey, iKey := accountKey(username), ipKey(ip)
	for _, k := range []string{aKey, iKey} {
		if wait, blocked := s.guard.Blocked(k); blocked {
			return nil, ErrTooManyAttempts.WithRetryAfter(wait)
		}
	}

	acct, err := s.stores.Accounts.AccountByUsername(ctx, username)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			// 哑哈希：与真实分支等价的一次 argon2id，堵住时间信道。
			s.hasher.DummyVerify(password)
			s.guard.RecordFailure(aKey)
			s.guard.RecordFailure(iKey)
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	ok, needRehash, verr := s.hasher.Verify(acct.PasswordHash, password)
	if verr != nil {
		// decodeArgon 已把格式坏统一成 ok=false；走到这里说明是底层真故障。
		return nil, Errorf(CodeInternal, verr, "密码校验异常")
	}
	if !ok || !acct.Status.CanLogin() {
		// 禁用/待审账户**密码正确也不给会话**，但对外与密码错误同形。
		s.guard.RecordFailure(aKey)
		s.guard.RecordFailure(iKey)
		return nil, ErrInvalidCredentials
	}

	// 校验成功后若参数低于当前档，用新参数重算写回（02-authn.md §4）。
	// 写回失败不阻断登录：那是存量成本优化，不是本请求的安全前提。
	if needRehash {
		if fresh, herr := s.hasher.Hash(password); herr == nil {
			_ = s.stores.Accounts.UpdatePasswordHash(ctx, acct.ID, fresh)
		}
	}

	s.guard.Reset(aKey)
	s.guard.Reset(iKey)
	if err := s.stores.Accounts.TouchLastLogin(ctx, acct.ID, s.sess.now()); err != nil {
		// 留痕字段失败不阻断（接口注释已约定）；错误进日志由上层负责。
		_ = err
	}
	return s.sess.Issue(ctx, acct, ip, ua)
}

// RecoverWithCode 用恢复码登录（纯贡献者账户的唯一找回路径，02-authn.md §8）。
//
// 与密码登录共用两维计数与同一防枚举出口：它同样是"标识符 + 秘密"，
// 不纳入防枚举就等于把用户名探测面从后门留回来了。
// 恢复码与密码同档 argon2id（人是抄写者，熵低）。
// 使用后置空（一次性）由存储层在此回调内完成；本层负责传空串语义：
// 传 "" 表示"这条路径没有可再用密码的账户"，与哑哈希分支同形返回。
func (s *LoginService) RecoverWithCode(ctx context.Context, username, code, ip, ua string) (*IssuedSession, error) {
	if s.stores.Accounts == nil || s.hasher == nil || s.sess == nil || s.guard == nil {
		return nil, Errorf(CodeInternal, nil, "LoginService 依赖未装配完整")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, ErrInvalidCredentials
	}
	aKey, iKey := accountKey(username), ipKey(ip)
	for _, k := range []string{aKey, iKey} {
		if wait, blocked := s.guard.Blocked(k); blocked {
			return nil, ErrTooManyAttempts.WithRetryAfter(wait)
		}
	}

	acct, err := s.stores.Accounts.AccountByUsername(ctx, username)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			s.hasher.DummyVerify(code)
			s.guard.RecordFailure(aKey)
			s.guard.RecordFailure(iKey)
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	// 恢复码路径只对"没有密码路径可用"的账户敞开是运营假设，不是安全边界；
	// 判定只看：该账户有恢复码哈希、码匹配、状态可登录。
	if acct.RecoveryCodeHash == "" {
		s.hasher.DummyVerify(code) // 无码账户也付出等价工作量，分支耗时不泄露"有没有设过码"
		s.guard.RecordFailure(aKey)
		s.guard.RecordFailure(iKey)
		return nil, ErrInvalidCredentials
	}
	ok, _, _ := s.hasher.Verify(acct.RecoveryCodeHash, code)
	if !ok || !acct.Status.CanLogin() {
		s.guard.RecordFailure(aKey)
		s.guard.RecordFailure(iKey)
		return nil, ErrInvalidCredentials
	}
	s.guard.Reset(aKey)
	s.guard.Reset(iKey)
	return s.sess.Issue(ctx, acct, ip, ua)
}

// ChangePassword 自助改密。
//
// 联动（02-authn.md §4）：改密吊销会话（当前会话是否保留为待定，走 policy；
// 默认一并吊销，更保守），但**绝不吊销 Key**——Key 与密码是两条独立凭据链，
// 绑在一起会逼出"从不改密码"的习惯（05-keys.md §6）。
// Key 的失效由登录后的"账户改密不触 Key"这条不变得以保证：本方法不调用
// 任何 Key 写路径，这就是实现层面的守点。
func (s *LoginService) ChangePassword(ctx context.Context, p *Principal, oldPw, newPw string) error {
	if s.stores.Accounts == nil || s.hasher == nil || s.sess == nil {
		return Errorf(CodeInternal, nil, "LoginService 依赖未装配完整")
	}
	if p == nil || p.Kind != CredentialSession {
		return Errorf(CodeForbidden, nil, "改密必须由会话身份发起")
	}
	if err := s.hasher.ValidatePassword(newPw); err != nil {
		return err // 长度不足等返回 invalid_request：这是本人路径，不涉防枚举
	}
	// AccountByID 是**不含 password_hash** 的通用读模型（01-accounts 的
	// 刻意最小化）；验证旧口令必须再走 AccountByUsername 这条带 hash 的
	// 认证读路径——否则 Verify 拿到空串，所有人的"旧口令"都判错。
	acct, err := s.stores.Accounts.AccountByID(ctx, p.AccountID)
	if err != nil {
		return err
	}
	withHash, err := s.stores.Accounts.AccountByUsername(ctx, acct.Username)
	if err != nil {
		return err
	}
	ok, _, _ := s.hasher.Verify(withHash.PasswordHash, oldPw)
	if !ok {
		return ErrInvalidCredentials
	}
	fresh, err := s.hasher.Hash(newPw)
	if err != nil {
		return Errorf(CodeInternal, err, "计算新密码哈希")
	}
	if err := s.stores.Accounts.UpdatePasswordHash(ctx, acct.ID, fresh); err != nil {
		return err
	}
	// 改密成功 = 一次性口令已被替换，强制改密要求随之解除。
	// 这一步失败必须上报：否则用户改完密仍被困在「先改密」的循环里。
	if err := s.stores.Accounts.SetMustChangePassword(ctx, acct.ID, false); err != nil {
		return err
	}
	keep := ""
	if s.policy.KeepCurrentSessionOnSelfChange {
		keep = p.SessionID
	}
	return s.sess.LogoutAll(ctx, acct.ID, keep)
}

// ResetPassword 管理员重置他人密码：生成随机一次性密码、落哈希、吊销全部会话。
// 返回的一次性明文只出现这一次（与 Key/导入同原则）。
// 吊销 Key？不——管理员重置针对的是"人的口令"，Key 联动见 ChangePassword 注释。
func (s *LoginService) ResetPassword(ctx context.Context, actor Subject, accountID string) (string, error) {
	if s.stores.Accounts == nil || s.hasher == nil || s.sess == nil {
		return "", Errorf(CodeInternal, nil, "LoginService 依赖未装配完整")
	}
	// 谁可以重置他人密码：admin 类的判定在 authz.Check 里完成；
	// 本方法只守"actor 必须是 admin 行动主体"这条最低线，防止被误接到自助路径上。
	if actor.ActingClass != ClassAdmin {
		return "", Errorf(CodeForbidden, nil, "重置他人密码必须以 admin 类行动")
	}
	acct, err := s.stores.Accounts.AccountByID(ctx, accountID)
	if err != nil {
		return "", err
	}
	oneTime, err := s.generateOneTimePassword()
	if err != nil {
		return "", err
	}
	fresh, err := s.hasher.Hash(oneTime)
	if err != nil {
		return "", Errorf(CodeInternal, err, "哈希一次性密码")
	}
	if err := s.stores.Accounts.UpdatePasswordHash(ctx, acct.ID, fresh); err != nil {
		return "", err
	}
	// 重置出来的是一次性口令：下次登录必须先换成自己的口令。
	if err := s.stores.Accounts.SetMustChangePassword(ctx, acct.ID, true); err != nil {
		return "", err
	}
	// 管理员重置吊销**全部**会话（无保留分支，文档 §4 表格定案）。
	if err := s.sess.LogoutAll(ctx, acct.ID, ""); err != nil {
		return "", err
	}
	return oneTime, nil
}

// GenerateOneTimePassword 生成一次性口令：20 字符 base62（≈119 位熵）。
//
// 除管理员重置外，实例首次启动的自举随机口令也走这里——两处用同一套
// 字符集与长度，运维看到的口令形态一致。
// 为什么不复用 Key 的 43 字符：一次性密码会被复制粘贴进登录框，
// 长度首先服务于"抄得动"；119 位对在线爆破（有锁定护栏）已是绝对余量。
// 它必须过 ≥12 字符策略——20 > 12，天然满足。
func GenerateOneTimePassword() (string, error) { return generateOneTimePassword(rand.Reader) }

func (s *LoginService) generateOneTimePassword() (string, error) {
	return generateOneTimePassword(s.randSrc)
}

func generateOneTimePassword(src io.Reader) (string, error) {
	const otpLen = 20
	out := make([]byte, 0, otpLen)
	for len(out) < otpLen {
		n, err := rand.Int(src, big.NewInt(int64(len(base62Alphabet))))
		if err != nil {
			return "", Errorf(CodeInternal, err, "生成一次性密码")
		}
		out = append(out, base62Alphabet[n.Int64()])
	}
	return string(out), nil
}
