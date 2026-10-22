// console_routes.go —— 会话面（登录/登出/会话恢复 + setup 向导）的装配。
//
// 这是 task-11 交付的 handler 与真实 store 之间的最后一跳：
//   - authLoginStores 把 store 的 Account/Session 仓储钉到 auth.Stores
//     （LoginService/SessionManager 只认窄接口——判定逻辑不碰 SQL）；
//   - setupStoreAdapter 把 CountAccounts/CreateFirstAdmin 钉到
//     httpapi.SetupStore（向导判定不碰账户仓储的大面）；
//   - MountConsoleRoutes 按既定挂载规则把四个端点注册进 chi 路由器。
//
// 桥的纪律与 bridges.go 相同：只翻译不判定、字段逐个抄、错误原样透传。

package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// ---------------------------------------------------------------------------
// auth.Stores 桥（登录 + 会话两个仓储）
// ---------------------------------------------------------------------------

// authLoginStores 实现 auth.Stores 的 Accounts/Sessions 两面。
//
// Keys/Policies 留 nil：LoginService/SessionManager 的路径只触达这两个
// 仓储（改密路径触达 UpdatePasswordHash 已含在 Accounts 面）；
// Key/策略桥在 Key 面走 keyAuthBridge，不复用这里的形状——两个面
// 的失败语义不同（401 invalid_api_key vs 401 invalid_credentials），
// 共享一个「大而全」的桥只会让两边的翻译悄悄耦合。
type authLoginStores struct {
	accounts *store.AccountRepo
	sessions *store.SessionRepo
}

func (s authLoginStores) AccountByID(ctx context.Context, id string) (*auth.Account, error) {
	nid, err := parseID(id)
	if err != nil {
		return nil, store.ErrNotFound // 形状不对与不存在同语义（不给探测面）
	}
	a, err := s.accounts.GetAccount(ctx, nid)
	if err != nil {
		return nil, err
	}
	return accountToAuth(a), nil
}

func (s authLoginStores) AccountByUsername(ctx context.Context, username string) (*auth.Account, error) {
	// 登录路径需要 password_hash（Verify 要真验一次）——GetAccountAuth
	// 是唯一带 hash 的读形状（01-accounts：hash 不进通用读模型）。
	a, err := s.accounts.GetAccountAuth(ctx, username)
	if err != nil {
		// 哨兵对齐：store.ErrNotFound（裸 errors.New）翻成 auth.ErrNotFound
		//（*auth.Error）——Login 的哑哈希分支按 CodeOf==CodeNotFound 认，
		// 不翻译会走 500（「登录处理失败」），防枚举路径整个断掉。
		if errors.Is(err, store.ErrNotFound) {
			return nil, auth.ErrNotFound
		}
		return nil, err
	}
	out := accountToAuth(&a.Account)
	out.PasswordHash = a.PasswordHash
	return out, nil
}

func (s authLoginStores) TouchLastLogin(ctx context.Context, accountID string, at time.Time) error {
	nid, err := parseID(accountID)
	if err != nil {
		return err
	}
	return s.accounts.TouchLastLogin(ctx, nid, at.Unix())
}

func (s authLoginStores) UpdatePasswordHash(ctx context.Context, accountID, hash string) error {
	nid, err := parseID(accountID)
	if err != nil {
		return err
	}
	return s.accounts.UpdatePasswordHash(ctx, nid, hash)
}

func (s authLoginStores) CreateSession(ctx context.Context, sess *auth.Session) error {
	nid, err := parseID(sess.AccountID)
	if err != nil {
		return err
	}
	// ip/ua 仅留痕（02-authn §5：不做强绑定）——空串存 NULL。
	var ip, ua *string
	if sess.IP != "" {
		ip = &sess.IP
	}
	if sess.UA != "" {
		ua = &sess.UA
	}
	return s.sessions.CreateSession(ctx, sess.ID, nid, sess.TokenHash,
		sess.ExpiresAt.Unix(), ip, ua)
}

func (s authLoginStores) SessionByTokenHash(ctx context.Context, tokenHash []byte) (*auth.Session, error) {
	// store 版把账户状态一并 join 出来；auth 窄接口只要会话行
	//（账户状态走 AccountByID 实时读——Validate 的纵深防御次序）。
	sess, _, err := s.sessions.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, err
	}
	out := &auth.Session{
		ID:        sess.ID,
		AccountID: fmt.Sprintf("%d", sess.AccountID),
		TokenHash: tokenHash,
		CreatedAt: time.Unix(sess.CreatedAt, 0),
		ExpiresAt: time.Unix(sess.ExpiresAt, 0),
	}
	if sess.IP != nil {
		out.IP = *sess.IP
	}
	if sess.UA != nil {
		out.UA = *sess.UA
	}
	return out, nil
}

func (s authLoginStores) DeleteSession(ctx context.Context, sessionID string) error {
	return s.sessions.DeleteSession(ctx, sessionID)
}

func (s authLoginStores) DeleteSessionsByAccount(ctx context.Context, accountID, keepSessionID string) error {
	nid, err := parseID(accountID)
	if err != nil {
		return err
	}
	if keepSessionID == "" {
		return s.sessions.DeleteSessionsByAccount(ctx, nid)
	}
	// 保留语义：先删全部再补建保留行不可接受（凭据窗口期）——
	// 用「逐条删」实现：列出该账户会话，跳过 keep 的逐条删。
	// store 侧暂无 ListSessionsByAccount；改密默认吊销全部（02-authn
	// 待定项未决，当前调用方 LoginService 默认 keep=""），保留分支
	// 返回 not_supported 让调用方显式选择"全吊"而不是静默全删。
	return auth.Errorf(auth.CodeInternal, nil,
		"store 桥暂不支持按保留项吊销（02-authn 待定项；当前路径默认全吊）")
}

// compile-time 接口满足断言：桥形状漂移在编译期炸，不在第一个登录请求上。
var (
	_ auth.AccountRepository = authLoginStores{}
	_ auth.SessionRepository = authLoginStores{}
)

// ---------------------------------------------------------------------------
// setup 向导桥
// ---------------------------------------------------------------------------

// setupStoreAdapter 把账户仓储钉到 httpapi.SetupStore。
type setupStoreAdapter struct {
	accounts *store.AccountRepo
}

func (a setupStoreAdapter) CountAccounts(ctx context.Context) (int, error) {
	return a.accounts.CountAccounts(ctx)
}

// CreateFirstAdmin 创建首个账户：{admin, tenant} 双类（PLAN 2.2——
// admin 管全站、tenant 让站长自己的 Key 调模型走两套账），默认组兜底
// 授权由 storage 的默认组机制处理（新账户自动进默认组，03-groups §2）。
//
// 0 账户窗口的并发竞态由 store 的用户名唯一约束兜底：两个并发的
// init 至多一个 INSERT 成功，另一个撞唯一索引——这里把该错误翻成
// auth.ErrConflict（调用方 handler 已按 CodeConflict → 409 翻译）。
func (a setupStoreAdapter) CreateFirstAdmin(ctx context.Context, username, passwordHash, displayName string) error {
	id, err := a.accounts.CreateAccount(ctx, store.AccountAuth{
		Account: store.Account{
			DisplayName: displayName,
			Username:    username,
			Status:      store.AccountActive,
		},
		PasswordHash: passwordHash,
	}, []store.Class{store.ClassAdmin, store.ClassTenant})
	if err != nil {
		// 用户名冲突 = 已有人抢注（或库已有账户而向导未关）→ 409 语义。
		if isDuplicateError(err) {
			return auth.ErrConflict
		}
		return err
	}
	_ = id
	return nil
}

// isDuplicateError 判 store 错误是否唯一约束冲突。
// SQLite 的 UNIQUE 错误串含 "UNIQUE constraint failed"——不引
// sqlite3 依赖（避免装配层绑死驱动），按错误串判。
func isDuplicateError(err error) bool {
	return err != nil && contains(err.Error(), "UNIQUE constraint")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var _ httpapi.SetupStore = setupStoreAdapter{}

// ---------------------------------------------------------------------------
// 路由挂载
// ---------------------------------------------------------------------------

// consoleAuthDeps 组装会话面全部运行时依赖（buildServer 调用）。
//
// hasher 的登录参数取 PasswordHasher 的包内定案值（64MiB/3/2——
// 02-authn「登录强度三项定值」），minLength 从 config 注入。
// guard 的锁定阈值从 config 注入（仲裁③：config 表 5/15m 优先于分册）。
type consoleAuthDeps struct {
	login *auth.LoginService
	sess  *auth.SessionManager
}

func (a *App) buildConsoleAuth() consoleAuthDeps {
	stores := authLoginStores{
		accounts: a.store.Repos().Accounts,
		sessions: a.store.Repos().Sessions,
	}
	authStores := auth.Stores{Accounts: stores, Sessions: stores}
	hasher := auth.NewPasswordHasher(a.cfg.Security.PasswordMinLength)
	sess := auth.NewSessionManager(auth.SessionPolicy{
		TTL: a.cfg.Security.SessionTTL.Std(),
	}, authStores)
	guard := auth.NewLoginGuard(auth.LoginPolicy{
		AccountLockAfter: a.cfg.Security.LoginMaxFailures,
		IPLockAfter:      50, // config 无 IP 阈值键；分册兜底（IP 显著高于账户）
		LockDuration:     a.cfg.Security.LoginLockout.Std(),
	})
	login := auth.NewLoginService(authStores, hasher, sess, guard, auth.LoginPolicy{})
	return consoleAuthDeps{login: login, sess: sess}
}

// AttachConsole 把 MountConsoleRoutes 注入 Server 的挂载点。
// 这是会话面装配的**唯一入口**：lead 在 buildServer 末尾加一行
// `a.AttachConsole()` 即完成接线（server_build.go 属装配层共享文件，
// 本文件不直接改它——那一行由 lead 落）。
func (a *App) AttachConsole() {
	a.server.SetConsoleRoutes(a.MountConsoleRoutes)
}

// MountConsoleRoutes 把会话面四端点挂进路由器（SetConsoleRoutes 的回调体）。
//
// 挂载规则（task-11 交付口径）：
//   - POST /api/auth/login **不挂 mwSession**（它建立会话，挂在会话判定
//     之下是循环依赖）；CSRF 防线是登录本身的限速与退避——登录是
//     每个会话的起点，攻击面在凭据猜射不在跨站携带；
//   - POST /api/auth/logout 与 GET /api/auth/session 挂 mwSession
//     （CSRF 自定义头 + 会话校验）；
//   - GET /api/setup/status 换 handleSetupStatusV2（替换 M0 占位——
//     server.go 的 handleSetupStatus 已由装配层的这条路由覆盖路由表）；
//   - POST /api/setup/init 不挂任何鉴权（0 账户窗口内没有可鉴权的主体），
//     窗口判定在 handler 内部（先 409 后验参数的顺序定案）。
func (a *App) MountConsoleRoutes(r chiRouter) {
	deps := a.buildConsoleAuth()
	authDeps := &httpapi.AuthDeps{Login: deps.login, Sess: deps.sess}
	setupDeps := &httpapi.SetupDeps{
		SetupStore: setupStoreAdapter{accounts: a.store.Repos().Accounts},
		Hasher:     auth.NewPasswordHasher(a.cfg.Security.PasswordMinLength),
	}

	r.Post("/api/auth/login", a.server.ConsoleHandlerLogin(authDeps))
	// 登出与会话恢复挂会话中间件（CSRF 头 + 会话校验）。
	r.Post("/api/auth/logout", a.server.ConsoleMiddlewareSession(deps.sess)(
		a.server.ConsoleHandlerLogout(authDeps)).(http.HandlerFunc))
	r.Get("/api/auth/session", a.server.ConsoleMiddlewareSession(deps.sess)(
		a.server.ConsoleHandlerSession()).(http.HandlerFunc))
	// setup 向导：status 换真判定（覆盖 M0 占位）；init 零鉴权（0 账户
	// 窗口内没有可鉴权主体），窗口判定在 handler 内（先 409 后验参数）。
	r.Get("/api/setup/status", a.server.ConsoleHandlerSetupStatus(setupDeps))
	r.Post("/api/setup/init", a.server.ConsoleHandlerSetupInit(setupDeps))

	// 管理面（M7）：45 端点整组挂 /api/admin，组级中间件 mwAdmin。
	// 先 AttachAdmin 再 Mount：Server 的 deps 注册表按实例键，挂载时取。
	a.server.AttachAdmin(a.buildAdminDeps())
	a.server.MountAdminRoutes(r)
}

// buildAdminDeps 组装管理面依赖（M7 装配）。
//
// 为什么在装配层组装而不是 httpapi 包内自带：AdminDeps 的每个字段都是
// **窄接口**（Accounts/Groups/Keys/...），实现方是 store 仓储；httpapi 不认识
// store，装配层是唯一同时认识两边的位置。这与 v1/会话面的切法一致。
//
// 缺字段（Refresh/UpstreamTest/SettingsPatch/IPCheck）暂不接：它们的实现
// 依赖刷新器实例与上游连通性测试，属 M10 后续整理；留 nil 时对应端点返回
// 未实现而不是 panic（handler 侧判空）。
func (a *App) buildAdminDeps() *httpapi.AdminDeps {
	b := newAdminBridge(a.store)
	authDeps := a.buildConsoleAuth()
	return &httpapi.AdminDeps{
		Sess:          authDeps.sess,
		Hasher:        auth.NewPasswordHasher(a.cfg.Security.PasswordMinLength),
		Accounts:      &accountsBridge{b: b},
		Groups:        &groupsBridge{b: b},
		Keys:          &keysCountBridge{b: b},
		Credentials:   &credentialsBridge{b: b},
		Models:        &modelsBridge{b: b},
		Upstreams:     &upstreamsBridge{b: b},
		Verifications: &verificationsBridge{b: b},
		Audit:         &auditBridge{b: b},
		Inbox:         &inboxBridge{b: b},

		// M10 补齐的四个触发型依赖：刷新/连通性测试/设置读/设置写与 IP 自检。
		Refresh:      a.buildRefreshBridge(),
		UpstreamTest: &upstreamTesterBridge{store: a.store, sidecar: a.sidecar, cfg: a.cfg, httpc: &http.Client{Timeout: 3 * time.Second}},
		SettingsView: &settingsBridge{b: b, cfg: a.cfg},
		SettingsPatch: &settingsBridge{b: b, cfg: a.cfg},
		IPCheck:      &settingsBridge{b: b, cfg: a.cfg},
	}
}

// chiRouter 是 chi.Router 的本地别名（SetConsoleRoutes 回调签名）。
type chiRouter = chi.Router
