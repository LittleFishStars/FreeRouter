package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// M3 会话面的契约测试（task-11，TestContract ≥10）。
// 断言面：登录/登出/会话恢复的状态码与错误码、Cookie 属性、CSRF、
// setup 向导窗口语义。防枚举时序在 auth 包已测，这里只测 HTTP 面形状。

// ---------------------------------------------------------------------------
// 脚手架：会话面 harness（真 LoginService/SessionManager/LoginGuard +
// 内存假仓储；auth 包的 fakeStore 不导出，这里建轻量同形假体）。
// ---------------------------------------------------------------------------

type sessFakeStore struct {
	accounts map[string]*auth.Account // by username
	byID     map[string]*auth.Account
	sessions map[string]*auth.Session // key: hex(tokenHash)
	nextID   int
}

func newSessFakeStore() *sessFakeStore {
	return &sessFakeStore{
		accounts: map[string]*auth.Account{},
		byID:     map[string]*auth.Account{},
		sessions: map[string]*auth.Session{},
	}
}

func (f *sessFakeStore) AccountByID(_ context.Context, id string) (*auth.Account, error) {
	a, ok := f.byID[id]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *a
	cp.Classes = a.Classes
	return &cp, nil
}

func (f *sessFakeStore) AccountByUsername(_ context.Context, username string) (*auth.Account, error) {
	a, ok := f.accounts[username]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *a
	cp.Classes = a.Classes
	return &cp, nil
}

func (f *sessFakeStore) TouchLastLogin(_ context.Context, id string, at time.Time) error {
	if a, ok := f.byID[id]; ok {
		t := at
		a.LastLoginAt = &t
	}
	return nil
}

func (f *sessFakeStore) UpdatePasswordHash(_ context.Context, id, hash string) error {
	return nil
}

func (f *sessFakeStore) CreateSession(_ context.Context, s *auth.Session) error {
	f.sessions[fmt.Sprintf("%x", s.TokenHash)] = s
	return nil
}

func (f *sessFakeStore) SessionByTokenHash(_ context.Context, hash []byte) (*auth.Session, error) {
	s, ok := f.sessions[fmt.Sprintf("%x", hash)]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *sessFakeStore) DeleteSession(_ context.Context, sessionID string) error {
	for k, s := range f.sessions {
		if s.ID == sessionID {
			delete(f.sessions, k)
		}
	}
	return nil
}

func (f *sessFakeStore) DeleteSessionsByAccount(_ context.Context, accountID, keep string) error {
	for k, s := range f.sessions {
		if s.AccountID == accountID && s.ID != keep {
			delete(f.sessions, k)
		}
	}
	return nil
}

// authStores 适配到 auth.Stores（本假体只提供会话面需要的两个仓储）。
func (f *sessFakeStore) stores() auth.Stores {
	return auth.Stores{Accounts: f, Sessions: f}
}

func (f *sessFakeStore) putAccount(id, username string, status auth.AccountStatus, classes ...auth.Class) {
	a := &auth.Account{
		ID: id, Username: username, Status: status,
		Classes:      auth.NewClassSet(classes...),
		PasswordHash: "", // 登录走 hasher.Verify，测试里用真实 argon2（低参数）
	}
	f.accounts[username] = a
	f.byID[id] = a
}

// m3Harness 是会话面测试链：mwSession + 三个 auth 端点。
type m3Harness struct {
	s       *Server
	store   *sessFakeStore
	login   *auth.LoginService
	sess    *auth.SessionManager
	now     *time.Time
	handler http.Handler
}

const m3Password = "correct-horse-batt"

func newM3Harness(t *testing.T) *m3Harness {
	t.Helper()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	f := newSessFakeStore()
	cfg := config.Default()
	s := New(Deps{Config: cfg, Logger: discardLogger()})
	hasher := auth.NewPasswordHasher(2) // 测试低参数（正式参数下界由 auth 包契约测）
	sess := auth.NewSessionManager(auth.SessionPolicy{TTL: 24 * time.Hour}, f.stores()).WithClock(clk)
	guard := auth.NewLoginGuard(auth.LoginPolicy{}).WithClock(clk)
	login := auth.NewLoginService(f.stores(), hasher, sess, guard, auth.LoginPolicy{})

	h := &m3Harness{s: s, store: f, login: login, sess: sess, now: &now}
	// 登录不需要会话中间件（它建立会话）；session/logout 挂 mwSession。
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin(&AuthDeps{Login: login, Sess: sess}))
	mux.HandleFunc("POST /api/auth/logout", s.mwSession(sess)(s.handleAuthLogout(&AuthDeps{Login: login, Sess: sess})).ServeHTTP)
	mux.HandleFunc("GET /api/auth/session", s.mwSession(sess)(s.handleAuthSession()).ServeHTTP)
	// 外面套 requestID（ClientIPFromContext 的来源链）。
	h.handler = s.mwRequestID(mux)
	return h
}

func (h *m3Harness) do(t *testing.T, method, path, body, cookie, csrfHeader string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if csrfHeader != "" {
		req.Header.Set("X-Requested-With", csrfHeader)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *m3Harness) loginOK(t *testing.T, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, http.MethodPost, "/api/auth/login",
		fmt.Sprintf(`{"username":%q,"password":%q}`, username, password), "", "")
}

// hashPassword 给假体账户装真实 argon2 哈希（登录真验一次）。
func (h *m3Harness) seedPassword(t *testing.T, username, password string) {
	t.Helper()
	hasher := auth.NewPasswordHasher(2)
	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	h.store.accounts[username].PasswordHash = hash
}

// ---------------------------------------------------------------------------
// 用例
// ---------------------------------------------------------------------------

// 1. 登录成功：200 + Set-Cookie 属性（HttpOnly/SameSite=Lax/MaxAge=TTL）
// + 响应体不含令牌明文。
func TestContractLoginSetsSecureCookie(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "admin", auth.StatusActive, auth.ClassAdmin, auth.ClassTenant)
	h.seedPassword(t, "admin", m3Password)

	rec := h.loginOK(t, "admin", m3Password)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应 200，得 %d（%s）", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录响应必须 Set-Cookie")
	}
	c := cookies[0]
	if c.Name != SessionCookieName {
		t.Fatalf("Cookie 名 = %s，期望 fr_session", c.Name)
	}
	if !c.HttpOnly {
		t.Fatal("Cookie 必须 HttpOnly（挡 XSS 读走会话）")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v，期望 Lax（OAuth 回调跳回时 Cookie 仍在）", c.SameSite)
	}
	if c.MaxAge != 24*60*60 {
		t.Fatalf("MaxAge = %d，期望 86400（与 expires_at 同刻）", c.MaxAge)
	}
	if c.Secure {
		t.Fatal("tls.mode=off 时不得加 Secure（浏览器不会通过 HTTP 回传）")
	}
	if strings.Contains(rec.Body.String(), c.Value) {
		t.Fatal("响应体不得含令牌明文（令牌只存在于 Cookie）")
	}
}

// 2. 凭据错误：401 invalid_credentials；不存在的用户同码同文案（防枚举 HTTP 面）。
func TestContractLoginWrongCredentialsAntiEnum(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "admin", auth.StatusActive, auth.ClassAdmin)
	h.seedPassword(t, "admin", m3Password)

	recWrong := h.loginOK(t, "admin", "wrong-password-xxx")
	if recWrong.Code != http.StatusUnauthorized {
		t.Fatalf("错密码应 401，得 %d", recWrong.Code)
	}
	e1 := decodeError(t, recWrong)
	if e1["code"] != "invalid_credentials" {
		t.Fatalf("code 应 invalid_credentials，得 %v", e1["code"])
	}

	recNoUser := h.loginOK(t, "ghost-user", "whatever-password")
	if recNoUser.Code != http.StatusUnauthorized {
		t.Fatalf("不存在用户应 401，得 %d", recNoUser.Code)
	}
	e2 := decodeError(t, recNoUser)
	if e1["code"] != e2["code"] || e1["message"] != e2["message"] {
		t.Fatalf("防枚举破坏：%v vs %v", e1, e2)
	}
}

// 3. 5 次失败锁定：第 5 次仍是凭据错误，第 6 次（正确密码）429
// too_many_attempts + Retry-After（锁定下一次才可见——防枚举时序）。
func TestContractLoginLockoutOnSixthAttempt(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "admin", auth.StatusActive, auth.ClassAdmin, auth.ClassTenant)
	h.seedPassword(t, "admin", m3Password)

	for i := 0; i < 5; i++ {
		*h.now = h.now.Add(61 * time.Second) // 越过退避封顶，让 5 枪都真打
		rec := h.loginOK(t, "admin", "wrong-password-xxx")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败应仍是 401（锁定下一次才可见），得 %d", i+1, rec.Code)
		}
	}
	*h.now = h.now.Add(61 * time.Second)
	rec := h.loginOK(t, "admin", m3Password) // 正确密码也被锁
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定后正确密码应 429，得 %d", rec.Code)
	}
	e := decodeError(t, rec)
	if e["code"] != "too_many_attempts" {
		t.Fatalf("code 应 too_many_attempts，得 %v", e["code"])
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("too_many_attempts 必带 Retry-After")
	}
}

// 4. 登出后会话失效：logout 清 Cookie+删行，此后 session 端点 401。
func TestContractLogoutInvalidatesSession(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "admin", auth.StatusActive, auth.ClassAdmin, auth.ClassTenant)
	h.seedPassword(t, "admin", m3Password)

	rec := h.loginOK(t, "admin", m3Password)
	cookie := SessionCookieName + "=" + rec.Result().Cookies()[0].Value

	// 会话可用（GET session 200）。
	recS := h.do(t, http.MethodGet, "/api/auth/session", "", cookie, "")
	if recS.Code != http.StatusOK {
		t.Fatalf("登出前 session 应 200，得 %d", recS.Code)
	}

	// 登出（POST 带 CSRF 头）。
	recOut := h.do(t, http.MethodPost, "/api/auth/logout", "", cookie, "XMLHttpRequest")
	if recOut.Code != http.StatusOK {
		t.Fatalf("登出应 200，得 %d", recOut.Code)
	}
	// 清除 Cookie：MaxAge=-1。
	for _, c := range recOut.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge != -1 {
			t.Fatalf("登出 Cookie MaxAge = %d，期望 -1（立即删除）", c.MaxAge)
		}
	}

	// 旧 Cookie 再访问 → 401 session_expired。
	recAfter := h.do(t, http.MethodGet, "/api/auth/session", "", cookie, "")
	if recAfter.Code != http.StatusUnauthorized {
		t.Fatalf("登出后会话应失效 401，得 %d", recAfter.Code)
	}
	if e := decodeError(t, recAfter); e["code"] != "session_expired" {
		t.Fatalf("code 应 session_expired，得 %v", e["code"])
	}
}

// 5. 会话过期：时钟推进 24h+ → 401 session_expired。
func TestContractSessionExpiresAt24h(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "admin", auth.StatusActive, auth.ClassAdmin, auth.ClassTenant)
	h.seedPassword(t, "admin", m3Password)

	rec := h.loginOK(t, "admin", m3Password)
	cookie := SessionCookieName + "=" + rec.Result().Cookies()[0].Value

	*h.now = h.now.Add(24*time.Hour + time.Minute) // 越过 TTL
	recAfter := h.do(t, http.MethodGet, "/api/auth/session", "", cookie, "")
	if recAfter.Code != http.StatusUnauthorized {
		t.Fatalf("过期会话应 401，得 %d", recAfter.Code)
	}
	if e := decodeError(t, recAfter); e["code"] != "session_expired" {
		t.Fatalf("code 应 session_expired，得 %v", e["code"])
	}
}

// 6. CSRF：非 GET 无自定义头 → 403 forbidden（挡在读 Cookie 之前）。
func TestContractCSRFHeaderRequiredOnWrite(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "admin", auth.StatusActive, auth.ClassAdmin, auth.ClassTenant)
	h.seedPassword(t, "admin", m3Password)
	rec := h.loginOK(t, "admin", m3Password)
	cookie := SessionCookieName + "=" + rec.Result().Cookies()[0].Value

	// POST 不带头（即使带着有效 Cookie）→ 403。
	recNoHdr := h.do(t, http.MethodPost, "/api/auth/logout", "", cookie, "")
	if recNoHdr.Code != http.StatusForbidden {
		t.Fatalf("POST 无自定义头应 403，得 %d", recNoHdr.Code)
	}
	// GET 不需要头。
	recGet := h.do(t, http.MethodGet, "/api/auth/session", "", cookie, "")
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET 不应要求 CSRF 头，得 %d", recGet.Code)
	}
}

// 7. session 端点主体形状：account_id/classes/acting_class/session_id。
func TestContractSessionEndpointShape(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "admin", auth.StatusActive, auth.ClassAdmin, auth.ClassTenant)
	h.seedPassword(t, "admin", m3Password)
	rec := h.loginOK(t, "admin", m3Password)
	cookie := SessionCookieName + "=" + rec.Result().Cookies()[0].Value

	recS := h.do(t, http.MethodGet, "/api/auth/session", "", cookie, "")
	if recS.Code != http.StatusOK {
		t.Fatalf("session 应 200，得 %d", recS.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recS.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["account_id"] != "acc-1" {
		t.Fatalf("account_id = %v", body["account_id"])
	}
	cls, _ := body["classes"].([]any)
	if len(cls) != 2 {
		t.Fatalf("classes 应含 admin+tenant，得 %v", cls)
	}
	if body["acting_class"] != string(auth.ClassAdmin) {
		// admin 账户的最低权限优先：contributor < tenant < admin——
		// 持有 {admin,tenant} 时缺省取 tenant。
		if body["acting_class"] != string(auth.ClassTenant) {
			t.Fatalf("acting_class = %v", body["acting_class"])
		}
	}
}

// ---------------------------------------------------------------------------
// setup 向导用例
// ---------------------------------------------------------------------------

type fakeSetupStore struct {
	count    int
	created  []string
	failWith error
}

func (f *fakeSetupStore) CountAccounts(context.Context) (int, error) {
	return f.count, nil
}

func (f *fakeSetupStore) CreateFirstAdmin(_ context.Context, username, hash, display string) error {
	if f.count > 0 {
		return auth.Errorf(auth.CodeConflict, nil, "已初始化")
	}
	f.count = 1
	f.created = append(f.created, username)
	return nil
}

func newSetupHarness(t *testing.T) (*Server, *fakeSetupStore, http.HandlerFunc, http.HandlerFunc) {
	t.Helper()
	cfg := config.Default()
	s := New(Deps{Config: cfg, Logger: discardLogger()})
	fake := &fakeSetupStore{}
	return s, fake, s.handleSetupStatusV2(&SetupDeps{SetupStore: fake, Hasher: auth.NewPasswordHasher(2)}), s.handleSetupInit(&SetupDeps{SetupStore: fake, Hasher: auth.NewPasswordHasher(2)})
}

func doHandler(t *testing.T, h http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// 8. setup 状态真判定：0 账户 → setup_required=true；有账户 → false。
func TestContractSetupStatusReflectsDatabase(t *testing.T) {
	_, fake, statusH, _ := newSetupHarness(t)

	rec := doHandler(t, statusH, http.MethodGet, "/api/setup/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status 应 200，得 %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["setup_required"] != true {
		t.Fatalf("0 账户应 setup_required=true，得 %v", body["setup_required"])
	}

	fake.count = 1
	rec2 := doHandler(t, statusH, http.MethodGet, "/api/setup/status", "")
	var body2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &body2)
	if body2["setup_required"] != false {
		t.Fatalf("有账户应 setup_required=false，得 %v", body2["setup_required"])
	}
}

// 9. init 仅 0 账户可用；创建成功 201；密码不达标 400。
func TestContractSetupInitCreatesFirstAdmin(t *testing.T) {
	_, fake, _, initH := newSetupHarness(t)

	rec := doHandler(t, initH, http.MethodPost, "/api/setup/init",
		`{"username":"root","password":"correct-horse-battery"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("init 应 201，得 %d（%s）", rec.Code, rec.Body.String())
	}
	if len(fake.created) != 1 || fake.created[0] != "root" {
		t.Fatalf("应创建 root，得 %v", fake.created)
	}

	// 短密码 400。
	_, _, _, initH2 := newSetupHarness(t)
	recBad := doHandler(t, initH2, http.MethodPost, "/api/setup/init",
		`{"username":"root2","password":"short"}`)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("短密码应 400，得 %d", recBad.Code)
	}
}

// 10. 二次 init 409（即使参数不合法也先 409——错误顺序不泄信息）。
func TestContractSetupInitSecondAttempt409(t *testing.T) {
	_, fake, _, initH := newSetupHarness(t)
	fake.count = 1 // 已初始化

	rec := doHandler(t, initH, http.MethodPost, "/api/setup/init",
		`{"username":"attacker","password":"short"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("二次 init 应 409，得 %d", rec.Code)
	}
	if e := decodeError(t, rec); e["code"] != "conflict" {
		t.Fatalf("code 应 conflict，得 %v", e["code"])
	}
}

// 11. TLS files 模式：Cookie 加 Secure；off 不加（02-authn §5 定案）。
func TestContractCookieSecureFollowsTLSMode(t *testing.T) {
	cfg := config.Default()
	cfg.TLS.Mode = "files"
	cfg.Server.Listen = "127.0.0.1:8080"
	s := New(Deps{Config: cfg, Logger: discardLogger()})
	c := s.newSessionCookie("tok", 24*time.Hour)
	if !c.Secure {
		t.Fatal("tls.mode=files 时 Cookie 必须 Secure")
	}
	cfg2 := config.Default()
	cfg2.TLS.Mode = "off"
	s2 := New(Deps{Config: cfg2, Logger: discardLogger()})
	c2 := s2.newSessionCookie("tok", 24*time.Hour)
	if c2.Secure {
		t.Fatal("tls.mode=off 时不得 Secure（HTTP 下浏览器不回传）")
	}
}

// 12. 登录响应的错误体不含任何令牌痕迹：探测者无法从 401 差异判断账户存在性
// （HTTP 面的补充断言；时间信道由 auth 包契约测守）。
func TestContractLoginFailureBodiesIdentical(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "admin", auth.StatusActive, auth.ClassAdmin)
	h.seedPassword(t, "admin", m3Password)

	r1 := h.loginOK(t, "admin", "totally-wrong-pass")
	r2 := h.loginOK(t, "nobody-here", "totally-wrong-pass")
	if r1.Body.String() != r2.Body.String() {
		t.Fatalf("两种失败响应体必须逐字节一致：\n%s\nvs\n%s", r1.Body.String(), r2.Body.String())
	}
}
