package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Contract 测试：锁定 internal/auth 对外的语义契约（不是实现细节）。
// 每条都对应 docs/security/ 的一篇定案；改动这些断言 = 改契约，需要过评审。

// ---------------------------------------------------------------------------
// 公共脚手架
// ---------------------------------------------------------------------------

type harness struct {
	f       *fakeStore
	clock   *time.Time
	advance func(time.Duration)
	nowFn   func() time.Time
	hasher  *PasswordHasher
	sess    *SessionManager
	guard   *LoginGuard
}

func newHarness() *harness {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	h := &harness{
		f:       newFakeStore(),
		clock:   &now,
		nowFn:   clk,
		advance: func(d time.Duration) { now = now.Add(d) },
		hasher:  NewPasswordHasher(12),
	}
	h.sess = NewSessionManager(SessionPolicy{TTL: 24 * time.Hour}, h.f.stores()).WithClock(clk)
	h.guard = NewLoginGuard(LoginPolicy{}).WithClock(clk)
	return h
}

// putUser 建一个带密码的 active 账户并返回。
func (h *harness) putUser(t *testing.T, id, username string, classes ...Class) *Account {
	t.Helper()
	pwHash, err := h.hasher.Hash("correct-horse-battery")
	if err != nil {
		t.Fatalf("hash 密码: %v", err)
	}
	a := &Account{
		ID: id, Username: username, DisplayName: username,
		PasswordHash: pwHash, Classes: NewClassSet(classes...), Status: StatusActive,
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("账户不变式: %v", err)
	}
	h.f.putAccount(a)
	return a
}

func (h *harness) loginService() *LoginService {
	return NewLoginService(h.f.stores(), h.hasher, h.sess, h.guard, LoginPolicy{})
}

func codeOfErr(t *testing.T, err error, want Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误 %s，得到 nil", want)
	}
	if got := CodeOf(err); got != want {
		t.Fatalf("错误码 = %s，期望 %s（%v）", got, want, err)
	}
}

// ---------------------------------------------------------------------------
// 1. Key：归属与组策略三段校验
// ---------------------------------------------------------------------------

func TestContractKeyCreateRequiresTenantClass(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-c", "contrib", ClassContributor)
	km := NewKeyManager(KeyPolicy{DefaultMaxKeysPerAccount: 5}, h.f.stores()).WithClock(h.nowFn)
	_, err := km.Create(context.Background(), Subject{AccountID: "acc-c", ActingClass: ClassContributor}, "n")
	// 05-keys.md §1：没有 tenant 类的账户发得出、用不了 → 创建即拒。
	codeOfErr(t, err, CodeForbidden)
}

func TestContractKeyGroupPolicyDenyPriority(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	// 组 A 允许、组 B 显式禁止 → deny 优先（03-groups.md §4）。
	h.f.groups["acc-t"] = []GroupPolicy{
		{Group: Group{ID: "ga"}, CanCreateKey: boolp(true)},
		{Group: Group{ID: "gb"}, CanCreateKey: boolp(false)},
	}
	km := NewKeyManager(KeyPolicy{DefaultMaxKeysPerAccount: 5}, h.f.stores()).WithClock(h.nowFn)
	_, err := km.Create(context.Background(), Subject{AccountID: "acc-t", ActingClass: ClassTenant}, "n")
	codeOfErr(t, err, CodeForbidden)

	// 全组未设置 → 允许（默认组不变式："没配"不等于"禁止"）。
	h.f.groups["acc-t"] = []GroupPolicy{{Group: Group{ID: "ga"}}}
	if _, err := km.Create(context.Background(), Subject{AccountID: "acc-t", ActingClass: ClassTenant}, "n"); err != nil {
		t.Fatalf("未设 CanCreateKey 应允许建 Key: %v", err)
	}
}

func TestContractKeyCountLimitTakesMaxAcrossGroups(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	// 放开类数值取**最大值**（03-groups.md §3 定案）：10 vs 3 → 10。
	h.f.groups["acc-t"] = []GroupPolicy{
		{Group: Group{ID: "ga"}, MaxKeys: intp(3)},
		{Group: Group{ID: "gb"}, MaxKeys: intp(10)},
	}
	km := NewKeyManager(KeyPolicy{DefaultMaxKeysPerAccount: 1}, h.f.stores()).WithClock(h.nowFn)
	s := Subject{AccountID: "acc-t", ActingClass: ClassTenant}
	for i := 0; i < 10; i++ { // 恰好等于上限：第 10 把能建
		if _, err := km.Create(context.Background(), s, "k"); err != nil {
			t.Fatalf("第 %d 把应可建: %v", i+1, err)
		}
	}
	_, err := km.Create(context.Background(), s, "over") // 超 1 个：拒
	codeOfErr(t, err, CodeConflict)

	// revoked 不占名额、disabled 占名额（05-keys.md §2 单向性推论）。
	keys, _ := km.List(context.Background(), s)
	if err := km.Revoke(context.Background(), s, keys[0].ID); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	if err := km.Disable(context.Background(), s, keys[1].ID); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, err := km.Create(context.Background(), s, "after-revoke"); err != nil {
		t.Fatalf("revoked 应释放名额: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. Key 生命周期：revoked 单向 / 轮换原子 / 过期推导
// ---------------------------------------------------------------------------

func TestContractKeyRevokedIsOneWay(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	km := NewKeyManager(KeyPolicy{}, h.f.stores()).WithClock(h.nowFn)
	s := Subject{AccountID: "acc-t", ActingClass: ClassTenant}
	res, err := km.Create(context.Background(), s, "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := km.Revoke(context.Background(), s, res.Key.ID); err != nil {
		t.Fatal(err)
	}
	// 可恢复的吊销不是吊销（05-keys.md §2）。
	codeOfErr(t, km.Enable(context.Background(), s, res.Key.ID), CodeConflict)
	if err := km.Revoke(context.Background(), s, res.Key.ID); err != nil {
		t.Fatalf("重复吊销应幂等: %v", err)
	}
	_, err = km.Usable(context.Background(), res.Plaintext)
	codeOfErr(t, err, CodeInvalidAPIKey)
}

func TestContractKeyRotateSwapsAtomically(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	km := NewKeyManager(KeyPolicy{}, h.f.stores()).WithClock(h.nowFn)
	s := Subject{AccountID: "acc-t", ActingClass: ClassTenant}
	old, err := km.Create(context.Background(), s, "old")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := km.Rotate(context.Background(), s, old.Key.ID, "rotated")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := km.Usable(context.Background(), fresh.Plaintext); err != nil {
		t.Fatalf("新 Key 立即可用: %v", err)
	}
	if _, err := km.Usable(context.Background(), old.Plaintext); err == nil {
		t.Fatal("旧 Key 轮换后必须失效")
	}
	// 对已 revoked 的再轮换 = 冲突（不是静默新建）。
	_, err = km.Rotate(context.Background(), s, old.Key.ID, "again")
	codeOfErr(t, err, CodeConflict)
}

func TestContractKeyExpiryIsDerived(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	km := NewKeyManager(KeyPolicy{}, h.f.stores()).WithClock(h.nowFn)
	res, err := km.Create(context.Background(), Subject{AccountID: "acc-t", ActingClass: ClassTenant}, "k")
	if err != nil {
		t.Fatal(err)
	}
	// 直接改存储里的 expires_at（模拟用户设置了过期）。
	exp := h.nowFn().Add(2 * time.Hour)
	h.f.mu.Lock()
	h.f.keys[res.Key.ID].ExpiresAt = &exp
	h.f.mu.Unlock()

	if _, err := km.Usable(context.Background(), res.Plaintext); err != nil {
		t.Fatalf("到期前应可用: %v", err)
	}
	h.advance(3 * time.Hour) // 时钟推进，不 sleep
	_, err = km.Usable(context.Background(), res.Plaintext)
	codeOfErr(t, err, CodeInvalidAPIKey)
}

// ---------------------------------------------------------------------------
// 3. 会话：24h 固定窗口 / 禁用即失效 / 全吊销
// ---------------------------------------------------------------------------

func TestContractSessionFixedWindow24h(t *testing.T) {
	h := newHarness()
	a := h.putUser(t, "acc-t", "ten", ClassTenant)
	is, err := h.sess.Issue(context.Background(), a, "10.0.0.1", "curl")
	if err != nil {
		t.Fatal(err)
	}
	if got := is.Session.ExpiresAt.Sub(is.Session.CreatedAt); got != 24*time.Hour {
		t.Fatalf("会话窗口 = %v，期望 24h", got)
	}
	// 23h59m：仍有效——没有滑动续期，有效期只由创建时刻决定。
	h.advance(23*time.Hour + 59*time.Minute)
	if _, err := h.sess.Validate(context.Background(), is.Token, "tenant"); err != nil {
		t.Fatalf("23h59m 应有效: %v", err)
	}
	h.advance(3 * time.Minute)
	_, err = h.sess.Validate(context.Background(), is.Token, "tenant")
	codeOfErr(t, err, CodeSessionExpired)
}

func TestContractSessionDisabledAccountInstantlyInvalid(t *testing.T) {
	h := newHarness()
	a := h.putUser(t, "acc-t", "ten", ClassTenant)
	is, err := h.sess.Issue(context.Background(), a, "", "")
	if err != nil {
		t.Fatal(err)
	}
	h.f.setStatus("acc-t", StatusDisabled)
	// 会话行还在库里，但账户状态实时查（04-authz.md §6 第 2 步）。
	_, err = h.sess.Validate(context.Background(), is.Token, "tenant")
	codeOfErr(t, err, CodeForbidden)
}

func TestContractActingClassDefaultsToLowestHeld(t *testing.T) {
	h := newHarness()
	a := h.putUser(t, "acc-m", "multi", ClassAdmin, ClassTenant, ClassContributor)
	is, err := h.sess.Issue(context.Background(), a, "", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.sess.Validate(context.Background(), is.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.ActingClass != ClassContributor {
		t.Fatalf("缺省 acting_class = %s，期望最低权限 contributor", p.ActingClass)
	}
	if !p.Classes.Has(ClassAdmin) {
		t.Fatal("会话必须携带账户全部类（2026-10-20 取消类级提权）")
	}
	// 请求账户不持有的类 → 拒绝，不静默降级。
	_, err = h.sess.Validate(context.Background(), is.Token, "ghost")
	codeOfErr(t, err, CodeInvalidRequest)

	// WithClass 只能在已持有类之间切换。
	if _, err := p.WithClass(ClassAdmin); err != nil {
		t.Fatalf("已持有类可切换: %v", err)
	}
	other := &Principal{AccountID: "x", Classes: NewClassSet(ClassTenant)}
	if _, err := other.WithClass(ClassAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("不持有的类必须 ErrForbidden: %v", err)
	}
}

func TestContractLogoutAllKeepsCurrent(t *testing.T) {
	h := newHarness()
	a := h.putUser(t, "acc-t", "ten", ClassTenant)
	s1, _ := h.sess.Issue(context.Background(), a, "", "")
	s2, _ := h.sess.Issue(context.Background(), a, "", "")

	// 保留当前会话（policy 可表达的分支）。
	if err := h.sess.LogoutAll(context.Background(), "acc-t", s1.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.sess.Validate(context.Background(), s1.Token, ""); err != nil {
		t.Fatalf("保留的会话应可用: %v", err)
	}
	if _, err := h.sess.Validate(context.Background(), s2.Token, ""); err == nil {
		t.Fatal("其余会话必须失效")
	}
	// 一并吊销（默认语义）：keep 为空时当前也不留。
	if err := h.sess.LogoutAll(context.Background(), "acc-t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.sess.Validate(context.Background(), s1.Token, ""); err == nil {
		t.Fatal("全部吊销后应不可用")
	}
}

// ---------------------------------------------------------------------------
// 4. 登录：退避曲线 / 双维锁定 / 防枚举 / 改密与重置联动
// ---------------------------------------------------------------------------

func TestContractBackoffCurve(t *testing.T) {
	p := LoginPolicy{} // BackoffDelay 内部回落定案参数（零值 = 用文档默认）
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{1, 0}, {2, 0}, // 前 2 次不退避（02-authn.md §10：留给手误）
		{3, 2 * time.Second},  // 2^(3-2)
		{4, 4 * time.Second},  // 2^(4-2)
		{8, 60 * time.Second}, // 2^6=64 → 封顶 60
		{30, 60 * time.Second},
	}
	for _, c := range cases {
		if got := p.BackoffDelay(c.failures); got != c.want {
			t.Errorf("第 %d 次失败退避 = %v，期望 %v", c.failures, got, c.want)
		}
	}
}

func TestContractAccountLockIndependentFromIP(t *testing.T) {
	h := newHarness()
	aKey, iKey := accountKey("victim"), ipKey("203.0.113.9")
	// 阈值 = config.security.login_max_failures 默认 5（lead 仲裁③）。
	for i := 0; i < 4; i++ {
		h.advance(61 * time.Second)
		h.guard.RecordFailure(aKey)
		h.guard.RecordFailure(iKey)
	}
	// 第 5 次：账户维度锁定 15 分钟。
	h.advance(61 * time.Second)
	h.guard.RecordFailure(aKey)
	wait, locked := h.guard.Blocked(aKey)
	if !locked || wait < 14*time.Minute {
		t.Fatalf("账户应锁定 15min，实际 locked=%v wait=%v", locked, wait)
	}
	// IP 维度 5 次 << 50：不可能锁定；Blocked 至多是退避（≤ 60s）。
	// 用等待时长区分两态：锁定会给出分钟级 Retry-After。
	if waitIP, _ := h.guard.Blocked(iKey); waitIP > time.Minute {
		t.Fatalf("IP 维度不得因账户锁定而连带（等待=%v），否则爆破=对整栋 NAT 的 DoS", waitIP)
	}
	// 管理员提前解锁（动作进审计由调用方负责）。
	h.guard.Unlock("victim")
	if _, locked := h.guard.Blocked(aKey); locked {
		t.Fatal("解锁后应可重试")
	}
	// 锁定期满自动解锁。
	for i := 0; i < 5; i++ {
		h.advance(61 * time.Second)
		h.guard.RecordFailure(aKey)
	}
	h.advance(16 * time.Minute)
	if _, locked := h.guard.Blocked(aKey); locked {
		t.Fatal("15min 后应自动解锁")
	}
}

func TestContractLoginAntiEnumeration(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "realuser", ClassTenant)
	svc := h.loginService()
	ctx := context.Background()

	_, errNoUser := svc.Login(ctx, "ghost", "whatever-123456", "1.2.3.4", "ua")
	_, errBadPw := svc.Login(ctx, "realuser", "wrong-password-1", "1.2.3.4", "ua")
	// 两条分支必须同码同哨兵（02-authn.md §9：文案/状态码信道堵死）。
	codeOfErr(t, errNoUser, CodeInvalidCredentials)
	codeOfErr(t, errBadPw, CodeInvalidCredentials)
	if !errors.Is(errNoUser, ErrInvalidCredentials) || !errors.Is(errBadPw, ErrInvalidCredentials) {
		t.Fatal("两条分支必须命中同一哨兵")
	}
	if errNoUser.Error() != errBadPw.Error() {
		t.Fatalf("响应文案必须无差异: %q vs %q", errNoUser, errBadPw)
	}

	// 密码正确但账户 pending：仍与"密码错"同形（时间信道由真验一次密码堵住）。
	h.putUser(t, "acc-p", "pendinguser", ClassTenant)
	h.f.setStatus("acc-p", StatusPending)
	_, errPending := svc.Login(ctx, "pendinguser", "correct-horse-battery", "1.2.3.4", "ua")
	if !errors.Is(errPending, ErrInvalidCredentials) {
		t.Fatalf("pending 账户登录应返回 ErrInvalidCredentials: %v", errPending)
	}
}

func TestContractLoginLockoutThenUnlockThenSuccess(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	svc := h.loginService()
	ctx := context.Background()

	// 前 5 次失败对外都必须是凭据错误（锁定在下一次尝试才可见）；
	// 间隔 61s 越过退避封顶 60s，保证每枪都真打。5 次共 5.1min < 15min 窗口。
	for i := 0; i < 5; i++ {
		h.advance(61 * time.Second)
		if _, err := svc.Login(ctx, "ten", "bad-password-wrong", "5.6.7.8", "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("第 %d 次失败应是凭据错误: %v", i+1, err)
		}
	}
	_, err := svc.Login(ctx, "ten", "correct-horse-battery", "5.6.7.8", "ua")
	// 锁定期内连正确密码也不放行——且返回 too_many_attempts（限流面），
	// 与凭据面同形反而是错的：客户端动作不同（等 Retry-After vs 改密码）。
	codeOfErr(t, err, CodeTooManyAttempts)
	if ra := RetryAfterOf(err); ra < 10*time.Minute {
		t.Fatalf("too_many_attempts 必须带可用 Retry-After，得 %v", ra)
	}
	// 解锁后本人立刻能进。
	h.advance(61 * time.Second) // IP 维度退避（≤60s）自然到期
	h.guard.Unlock("ten")
	is, err := svc.Login(ctx, "ten", "correct-horse-battery", "5.6.7.8", "ua")
	if err != nil {
		t.Fatalf("解锁后登录失败: %v", err)
	}
	if _, err := h.sess.Validate(ctx, is.Token, "tenant"); err != nil {
		t.Fatalf("签发会话应可用: %v", err)
	}
}

func TestContractChangePasswordRevokesSessionsNotKeys(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	svc := h.loginService()
	ctx := context.Background()

	is, err := svc.Login(ctx, "ten", "correct-horse-battery", "", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.sess.Validate(ctx, is.Token, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	km := NewKeyManager(KeyPolicy{}, h.f.stores()).WithClock(h.nowFn)
	created, err := km.Create(ctx, p.Subject(), "keep-me")
	if err != nil {
		t.Fatal(err)
	}

	// 旧密码错 → 拒绝改密。
	if err := svc.ChangePassword(ctx, p, "wrong-old-password", "brand-new-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧密码错应 ErrInvalidCredentials: %v", err)
	}
	// 新密码短于策略 → invalid_request（本人路径不涉防枚举）。
	err = svc.ChangePassword(ctx, p, "correct-horse-battery", "short")
	codeOfErr(t, err, CodeInvalidRequest)

	if err := svc.ChangePassword(ctx, p, "correct-horse-battery", "brand-new-password"); err != nil {
		t.Fatalf("改密失败: %v", err)
	}
	// 默认一并吊销当前会话（待定项的保守分支）。
	_, err = h.sess.Validate(ctx, is.Token, "tenant")
	codeOfErr(t, err, CodeSessionExpired)
	// 但 **Key 不受影响**（05-keys.md §6：两条凭据链独立）。
	if _, err := km.Usable(ctx, created.Plaintext); err != nil {
		t.Fatalf("改密后 Key 必须仍可用: %v", err)
	}
	// 新密码可登录。
	if _, err := svc.Login(ctx, "ten", "brand-new-password", "", ""); err != nil {
		t.Fatalf("新密码应可登录: %v", err)
	}
}

func TestContractAdminResetRevokesAllSessions(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	sess1, _ := h.sess.Issue(context.Background(), h.f.accounts["acc-t"], "", "")
	svc := h.loginService()

	otp, err := svc.ResetPassword(context.Background(), Subject{AccountID: "admin1", ActingClass: ClassAdmin}, "acc-t")
	if err != nil {
		t.Fatal(err)
	}
	if len(otp) != 20 || strings.ContainsAny(otp, " ") {
		t.Fatalf("一次性密码应 20 字符 base62: %q", otp)
	}
	if _, err := h.sess.Validate(context.Background(), sess1.Token, ""); err == nil {
		t.Fatal("管理员重置必须吊销全部会话（02-authn.md §4 表）")
	}
	// 非 admin 主体 → 403，防止被接到自助路径。
	_, err = svc.ResetPassword(context.Background(), Subject{AccountID: "x", ActingClass: ClassTenant}, "acc-t")
	codeOfErr(t, err, CodeForbidden)
}

// ---------------------------------------------------------------------------
// 5. 密码哈希：argon2id 参数下界与常数时间比较面
// ---------------------------------------------------------------------------

func TestContractArgonRejectsSubfloorParams(t *testing.T) {
	h := newHarness()
	// 手工构造一条 m=1,t=1 的记录：低于安全下界的哈希必须**按校验失败处理**，
	// 否则等于把"慢哈希"防线静默关掉（password.go 下界保护的契约面）。
	weak := encodeArgon(argonParams{Memory: 1, Time: 1, Threads: 1, Salt: make([]byte, ArgonSaltLen), KeyLen: ArgonKeyLen}, make([]byte, 32))
	ok, _, err := h.hasher.Verify(weak, "anything")
	if ok || err != nil {
		t.Fatalf("低于下界的记录必须拒为校验失败: ok=%v err=%v", ok, err)
	}
	// 正常记录 round-trip。
	enc, err := h.hasher.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if ok, needRehash, err := h.hasher.Verify(enc, "correct-horse-battery"); !ok || err != nil || needRehash {
		t.Fatalf("round-trip: ok=%v rehash=%v err=%v", ok, needRehash, err)
	}
	if ok, _, _ := h.hasher.Verify(enc, "wrong-horse-battery"); ok {
		t.Fatal("错密码必须 false")
	}
	if err := h.hasher.ValidatePassword("short12"); err == nil {
		t.Fatal("12 字符下界必须生效")
	}
}

// ---------------------------------------------------------------------------
// 6. 判定链：能力矩阵 / 他人资源 404 / 白名单收窄
// ---------------------------------------------------------------------------

func TestContractAuthzCapabilityMatrix(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	h.putUser(t, "acc-a", "adm", ClassAdmin)
	az := NewAuthz(h.f.stores())
	ctx := context.Background()

	cases := []struct {
		name     string
		sub      Subject
		op       Op
		res      Resource
		wantOK   bool
		wantCode Code
	}{
		{"tenant 调模型", Subject{"acc-t", ClassTenant}, OpCallModel, Resource{ResAbstractModel, "fast"}, true, ""},
		{"admin 类调模型被拒（换 tenant 才行）", Subject{"acc-a", ClassAdmin}, OpCallModel, Resource{ResAbstractModel, "fast"}, false, CodeForbidden},
		{"tenant 读审计被拒", Subject{"acc-t", ClassTenant}, OpReadAudit, Resource{ResAudit, ""}, false, CodeForbidden},
		{"admin 读审计放行", Subject{"acc-a", ClassAdmin}, OpReadAudit, Resource{ResAudit, ""}, true, ""},
		{"contributor 建 Key 被拒", Subject{"acc-t", ClassContributor}, OpCreateKey, Resource{}, false, CodeForbidden},
		{"自助改密任何类放行", Subject{"acc-t", ClassTenant}, OpSelfChangePassword, Resource{ResSession, ""}, true, ""},
		{"非法类直接 invalid_request", Subject{"acc-t", Class("boss")}, OpCallModel, Resource{}, false, CodeInvalidRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := az.Check(ctx, c.sub, c.op, c.res)
			if c.wantOK {
				if d != DecisionAllow || err != nil {
					t.Fatalf("应允许: %v", err)
				}
				return
			}
			if d != DecisionDeny {
				t.Fatal("应拒绝")
			}
			codeOfErr(t, err, c.wantCode)
		})
	}
}

func TestContractAuthzOthersKeyIs404(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-a", "alice", ClassTenant)
	h.putUser(t, "acc-b", "bob", ClassTenant)
	km := NewKeyManager(KeyPolicy{}, h.f.stores()).WithClock(h.nowFn)
	res, err := km.Create(context.Background(), Subject{AccountID: "acc-b", ActingClass: ClassTenant}, "bobs")
	if err != nil {
		t.Fatal(err)
	}
	az := NewAuthz(h.f.stores())
	// 读他人 Key → 404 而不是 403（04-authz.md §3：不泄露资源存在性）。
	_, err = az.Check(context.Background(), Subject{AccountID: "acc-a", ActingClass: ClassTenant}, OpRevokeKey, Resource{ResAPIKey, res.Key.ID})
	codeOfErr(t, err, CodeNotFound)
	if errors.Is(err, ErrForbidden) {
		t.Fatal("404 不得同时命中 403 哨兵")
	}
	// 自己的 Key 放行。
	if _, err := az.Check(context.Background(), Subject{AccountID: "acc-b", ActingClass: ClassTenant}, OpRevokeKey, Resource{ResAPIKey, res.Key.ID}); err != nil {
		t.Fatalf("本人 Key 应放行: %v", err)
	}
}

func TestContractSetAllowlistOnlyNarrows(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	h.f.groups["acc-t"] = []GroupPolicy{
		{Group: Group{ID: "ga"}, Models: []string{"fast", "smart"}},
		{Group: Group{ID: "gb"}, Models: []string{"eco"}},
	}
	km := NewKeyManager(KeyPolicy{}, h.f.stores()).WithClock(h.nowFn)
	s := Subject{AccountID: "acc-t", ActingClass: ClassTenant}
	res, err := km.Create(context.Background(), s, "k")
	if err != nil {
		t.Fatal(err)
	}
	// 超出组授权（∪ = fast/smart/eco）→ 拒绝。
	err = km.SetAllowlist(context.Background(), s, res.Key.ID, []string{"fast", "gpt-4o-real"})
	codeOfErr(t, err, CodeForbidden)
	// 合法子集写入。
	if err := km.SetAllowlist(context.Background(), s, res.Key.ID, []string{"fast", "eco"}); err != nil {
		t.Fatalf("子集应可写: %v", err)
	}
	// 空白名单 = 什么都不能调（验收 5：默认拒绝）。
	if err := km.SetAllowlist(context.Background(), s, res.Key.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := km.Usable(context.Background(), res.Plaintext); err != nil {
		t.Fatalf("Key 本身仍可用（白名单为空只影响模型集）: %v", err)
	}
}

func TestContractEffectiveModelsIsIntersection(t *testing.T) {
	h := newHarness()
	h.putUser(t, "acc-t", "ten", ClassTenant)
	h.f.groups["acc-t"] = []GroupPolicy{
		{Group: Group{ID: "ga"}, Models: []string{"fast", "smart"}},
		{Group: Group{ID: "gb"}, Models: []string{"eco"}},
	}
	km := NewKeyManager(KeyPolicy{}, h.f.stores()).WithClock(h.nowFn)
	s := Subject{AccountID: "acc-t", ActingClass: ClassTenant}
	res, _ := km.Create(context.Background(), s, "k")
	if err := km.SetAllowlist(context.Background(), s, res.Key.ID, []string{"smart", "eco"}); err != nil {
		t.Fatal(err)
	}
	az := NewAuthz(h.f.stores())
	m, err := az.EffectiveModels(context.Background(), "acc-t", res.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m, ",") != "eco,smart" {
		t.Fatalf("(∪组)∩白名单 = %v，期望 [eco smart]", m)
	}
	// 管理员把 smart 从组里收走：白名单不变，可用集自动收窄（05-keys.md §5）。
	h.f.groups["acc-t"] = []GroupPolicy{{Group: Group{ID: "ga"}, Models: []string{"fast"}}}
	if ok, _ := az.ModelCallable(context.Background(), "acc-t", res.Key.ID, "smart"); ok {
		t.Fatal("组授权收窄后 smart 必须不可调")
	}
	// eco 随 gb 一起消失；fast 在白名单外——交集应为空（默认拒绝）。
	if ok, _ := az.ModelCallable(context.Background(), "acc-t", res.Key.ID, "eco"); ok {
		t.Fatal("eco 已从组授权中移除，必须不可调")
	}
}

// ---------------------------------------------------------------------------
// 7. 多组合并：四种算子各管一类键
// ---------------------------------------------------------------------------

func TestContractMergeUnionAndStrictest(t *testing.T) {
	unlimited := -1
	gA := GroupPolicy{
		Group:          Group{ID: "ga"},
		Models:         []string{"fast", "smart"},
		CanCreateKey:   boolp(true),
		MaxKeys:        intp(3),
		Quotas:         map[QuotaKey]int64{{Unit: "token", Period: "day"}: 100, {Unit: "credit", Period: "month"}: 60},
		RPMLimit:       intp(60),
		TPMLimit:       func() *int64 { v := int64(unlimited); return &v }(), // -1 = 显式不限
		MaxConcurrency: intp(2),
		Pools:          []PoolRef{{GroupID: "ga", Unit: "token", Period: "day", Limit: 500}, {GroupID: "gb", Unit: "credit", Period: "month", Limit: 80}},
	}
	gB := GroupPolicy{
		Group:          Group{ID: "gb"},
		Models:         []string{"eco", "smart"},
		CanCreateKey:   boolp(false),
		MaxKeys:        intp(10),
		Quotas:         map[QuotaKey]int64{{Unit: "token", Period: "day"}: 200},
		ModelQuotas:    map[ModelQuotaKey]int64{{Model: "smart", Unit: "request", Period: "day"}: 50},
		RPMLimit:       nil, // 未设：不参与
		MaxConcurrency: intp(5),
	}
	eff := MergeGroupPolicies([]GroupPolicy{gA, gB})

	// 放开类并集（去重 + 稳定排序）。
	if strings.Join(eff.VisibleModels, ",") != "eco,fast,smart" {
		t.Fatalf("模型并集 = %v", eff.VisibleModels)
	}
	// 布尔 deny 优先。
	if eff.CanCreateKey {
		t.Fatal("一组禁止必须整体禁止")
	}
	// Key 数量：放开类数值取最大值（定案，不是 min）。
	if eff.MaxKeys == nil || *eff.MaxKeys != 10 {
		t.Fatalf("MaxKeys = %v，期望 10（取最大）", eff.MaxKeys)
	}
	// 限制类逐键 min；不同 (unit, period) 是独立闸门。
	if v := eff.Quotas[QuotaKey{Unit: "token", Period: "day"}]; v != 100 {
		t.Fatalf("token/day = %d，期望 100（min）", v)
	}
	if v := eff.Quotas[QuotaKey{Unit: "credit", Period: "month"}]; v != 60 {
		t.Fatalf("credit/month = %d，期望 60（单组键也在结果里）", v)
	}
	if v := eff.ModelQuotas[ModelQuotaKey{Model: "smart", Unit: "request", Period: "day"}]; v != 50 {
		t.Fatalf("按模型限额 = %d，期望 50", v)
	}
	// RPM：60 vs 未设 → 60；TPM：-1 不参与 → 结果 nil（不构成闸）。
	if eff.RPMLimit == nil || *eff.RPMLimit != 60 {
		t.Fatalf("RPM = %v，期望 60", eff.RPMLimit)
	}
	if eff.TPMLimit != nil {
		t.Fatalf("TPM -1 不得进结果: %v", *eff.TPMLimit)
	}
	// 并发 2 vs 5 → min=2。
	if eff.MaxConcurrency == nil || *eff.MaxConcurrency != 2 {
		t.Fatalf("并发 = %v，期望 2（min）", eff.MaxConcurrency)
	}
	// 池：不合并、逐个都要满足、保留归因。
	if len(eff.Pools) != 2 {
		t.Fatalf("池应原样保留两条: %v", eff.Pools)
	}
	// 空输入：默认拒绝方向由调用方落（结果 = 全未设，布尔全真由 ALL 语义推得）。
	empty := MergeGroupPolicies(nil)
	if !empty.CanCreateKey || empty.MaxKeys != nil || len(empty.Pools) != 0 {
		t.Fatalf("空组合并不应引入限制: %+v", empty)
	}
}

// ---------------------------------------------------------------------------
// 8. 错误契约：按 Code 匹配 / Retry-After 副本语义
// ---------------------------------------------------------------------------

func TestContractErrorsMatchByCodeAndCopy(t *testing.T) {
	// Errorf 造的实例与哨兵按 Code 匹配（Is 的契约：指针不等也算同类）。
	err := Errorf(CodeForbidden, errors.New("底层原因"), "自定义文案")
	if !errors.Is(err, ErrForbidden) {
		t.Fatal("同 Code 应 errors.Is 命中哨兵")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("跨 Code 不得误命中")
	}
	if !errors.Is(err, context.DeadlineExceeded) && errors.Unwrap(err) == nil {
		t.Fatal("Wrapped 必须可 Unwrap（日志需要底层原因）")
	}
	// WithRetryAfter/WithDetail 返回副本，不改共享哨兵。
	e1 := ErrTooManyAttempts.WithRetryAfter(90 * time.Second)
	if e1.RetryAfter != 90*time.Second || ErrTooManyAttempts.RetryAfter != 0 {
		t.Fatal("WithRetryAfter 必须是值拷贝")
	}
	e2 := e1.WithDetail("rpm", "grp-1")
	if e2.Dimension != "rpm" || e2.GroupID != "grp-1" || e1.Dimension != "" {
		t.Fatal("WithDetail 必须是值拷贝")
	}
	// 非本包错误回落 invalid_request 而不是 panic。
	if got := CodeOf(errors.New("raw")); got != CodeInvalidRequest {
		t.Fatalf("CodeOf 回落 = %s", got)
	}
}
