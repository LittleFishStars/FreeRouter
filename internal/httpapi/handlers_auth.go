package httpapi

import (
	"net/http"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// handlers_auth.go：控制台认证端点（POST /api/auth/login、
// POST /api/auth/logout、GET /api/auth/session）。
//
// 契约（docs/ops/03-milestones.md §5 验收 + docs/security/02-authn.md）：
//   - 错误体走 errors.go 控制台码：invalid_credentials / too_many_attempts /
//     session_expired（03-errors §3.2）；
//   - 防枚举：LoginService 已把两条分支收敛为同码同文案同工作量，
//     handler 只做翻译，不新增任何可区分行为；
//   - 登录成功 Set-Cookie（newSessionCookie：HttpOnly+SameSite=Lax，
//     tls.mode=files 加 Secure），**令牌明文不进响应体**——它只存在于
//     Cookie 与内存中的 IssuedSession；
//   - 登出：删会话行 + 清 Cookie（幂等：会话已不存在也 200）。
//
// handler 写成 Server 方法；路由挂载归 Lead（不改 server.go 红线）。

// AuthDeps 是认证端点的依赖包（装配层在 Handler() 外构造一次）。
//
// 为什么不挂在 Deps（server.go）：那是 M0 的公共依赖（不可改），
// 认证服务的组装（stores+hasher+guard+policy）是 authn 域的知识，
// 装配层用 auth.NewLoginService(...) 一行组出来传进来。
type AuthDeps struct {
	Login *auth.LoginService
	Sess  *auth.SessionManager
}

// handleAuthLogin POST /api/auth/login。
//
// 请求体 {"username","password"}；成功 200 {"account_id","display_name",
// "classes","expires_at"}；失败 401 invalid_credentials / 429
// too_many_attempts（带 Retry-After 头）。
func (s *Server) handleAuthLogin(deps *AuthDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Login == nil || deps.Sess == nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"认证服务未装配", nil)
			return
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		ip := ClientIPFromContext(r.Context())
		ua := r.UserAgent()

		is, err := deps.Login.Login(r.Context(), body.Username, body.Password, ip, ua)
		if err != nil {
			var ae *auth.Error
			if asAuthError(err, &ae) {
				switch ae.Code {
				case auth.CodeTooManyAttempts:
					// 锁定/退避面：429 + Retry-After（02-authn §10 的限流响应）。
					s.writeAuthErr(w, err)
					return
				case auth.CodeInvalidCredentials:
					writeError(w, http.StatusUnauthorized, "invalid_credentials",
						"用户名或密码错误", nil)
					return
				}
			}
			writeError(w, http.StatusInternalServerError, "internal_error",
				"登录处理失败", nil)
			return
		}

		// 令牌只进 Cookie（HttpOnly）；响应体给前端要渲染的主体信息。
		// display_name/classes 需要账户读——Login 返回的 IssuedSession 只带
		// Session；这里经 Validate 同一读路径取一次（登录不是热路径，
		// 一次额外读换取响应体与 GET /api/auth/session 同形）。
		http.SetCookie(w, s.newSessionCookie(is.Token, s.sessionTTL()))
		resp := map[string]any{
			"account_id": is.Session.AccountID,
			"expires_at": is.Session.ExpiresAt.UTC(),
		}
		if p, verr := deps.Sess.Validate(r.Context(), is.Token, ""); verr == nil {
			resp["display_name"] = p.DisplayName
			resp["classes"] = p.Classes.Sorted()
			// 首启随机口令/管理员重置后为 true：前端据此直跳改密页，
			// 服务端仍有 mwSession 的强制闸兜底。
			resp["must_change_password"] = p.MustChangePassword
			// 账户状态：pending 账户的前端让路（PendingGate）依赖它。
			resp["status"] = p.Status
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleAuthLogout POST /api/auth/logout。
//
// 幂等：会话已失效/不存在也 200（登出的语义是"此后不可用"，不是
// "必须曾经可用"——把已登出当错误会让前端登出按钮偶发报错）。
// CSRF：自定义头检查由 mwSession 承担（本端点应挂在 mwSession 之下）；
// 无有效会话的登出直接 200+清 Cookie。
func (s *Server) handleAuthLogout(deps *AuthDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, s.clearSessionCookie())
		if deps == nil || deps.Sess == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		c, err := r.Cookie(SessionCookieName)
		if err != nil || c.Value == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		// 取令牌对应的会话 id 再删行；Validate 失败（已过期/已吊销）
		// 不影响登出语义——Cookie 已清，浏览器侧不会再带。
		if p, verr := deps.Sess.Validate(r.Context(), c.Value, ""); verr == nil {
			_ = deps.Sess.Logout(r.Context(), p.SessionID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleAuthSession GET /api/auth/session——当前会话主体（前端恢复
// 登录态/渲染顶栏用）。必须挂在 mwSession 之下。
func (s *Server) handleAuthSession() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := SessionPrincipalFromContext(r.Context())
		if p == nil {
			writeError(w, http.StatusUnauthorized, "session_expired",
				"会话不存在或已过期，请重新登录", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":   p.AccountID,
			"display_name": p.DisplayName,
			"classes":      p.Classes.Sorted(),
			"acting_class": p.ActingClass,
			"session_id":   p.SessionID,
			// 强制改密窗口：前端路由守卫与顶栏提示的输入。
			"must_change_password": p.MustChangePassword,
			// 账户状态（pending 账户只能停在 /pending）。
			"status": p.Status,
		})
	}
}
