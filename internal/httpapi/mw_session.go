package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// mw_session.go：控制台面的会话鉴权中间件（docs/security/02-authn.md §5、
// docs/api/02-admin.md §2）。
//
// 与 Key 面（mw_keyauth.go）**分形不分流**：两类凭据的失效语义完全不同
// （会话随登出/改密/24h 吊销；Key 随吊销/过期/账户禁用失效），所以
// 中间件各自独立、各自注入不同的 Principal——但共用 ctxKey 枚举池。
//
// CSRF（lead 任务描述的「同源/自定义头双保险」口径）：
//   - 第一道：SameSite=Lax Cookie（浏览器层挡跨站携带，docs/api/02-admin
//     §2.1 已定案；Lax 而非 Strict 是为 OAuth 回调跳回本站时 Cookie 仍在）；
//   - 第二道：非 GET/HEAD/OPTIONS 请求要求自定义头 `X-Requested-With:
//     XMLHttpRequest`（或等价 `X-CSRF-Token` 非空）——浏览器跨站表单
//     **无法伪造自定义头**（需 CORS 预检，而 CORS 默认关闭），脚本页
//     能发自定义头但拿不到 HttpOnly Cookie。两道独立，绕过任一不足以
//     完成跨站写。
//
// 装配归 Lead：控制台路由组挂 s.mwSession(sessMgr)。

// ctxKeySessionPrincipal 是会话主体的 context 键（Key 主体用 100 段，
// 会话主体用 101 段——同池不同段，见 mw_keyauth.go 的说明）。
const ctxKeySessionPrincipal ctxKey = 101

// SessionPrincipalFromContext 取会话主体；不在上下文（未过会话鉴权/Key 面）返回 nil。
func SessionPrincipalFromContext(ctx context.Context) *auth.Principal {
	v, _ := ctx.Value(ctxKeySessionPrincipal).(*auth.Principal)
	return v
}

// SessionCookieName 是会话 Cookie 名（docs/api/02-admin.md §2.1）。
const SessionCookieName = "fr_session"

// mwSession 是会话鉴权中间件。
//
// 失败响应 401 session_expired（控制台码，03-errors §3.2）：会话不存在/
// 已登出/过期/账户禁用——与会话面同码（不给探测面：控制台不区分
// "会话曾经存在"与"从未存在"）。
// CSRF 拒绝是 403 forbidden（请求本身不合法，不是凭据问题）。
func (s *Server) mwSession(sessMgr *auth.SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// CSRF 第二道：跨站写挡在**读 Cookie 之前**——
			// 不给跨站请求任何触达会话判定的机会。
			if needsCSRFCheck(r.Method) && !hasCustomHeader(r) {
				writeError(w, http.StatusForbidden, "forbidden",
					"缺少自定义请求头（跨站请求不接受）", nil)
				return
			}

			c, err := r.Cookie(SessionCookieName)
			if err != nil || c.Value == "" {
				writeError(w, http.StatusUnauthorized, "session_expired",
					"会话不存在或已过期，请重新登录", nil)
				return
			}
			// actingClass 传空 = Validate 按最低权限优先选取。
			p, verr := sessMgr.Validate(r.Context(), c.Value, "")
			if verr != nil {
				var ae *auth.Error
				if asAuthError(verr, &ae) && ae.Code == auth.CodeInternal {
					writeError(w, http.StatusInternalServerError,
						string(auth.CodeInternal), ae.Msg, nil)
					return
				}
				// 会话过期/账户禁用/存储抖动：同码 session_expired。
				writeError(w, http.StatusUnauthorized, "session_expired",
					"会话不存在或已过期，请重新登录", nil)
				return
			}
			// 强制改密闸：首启/重置的一次性口令在改密前只能触达
			// 「看会话 / 登出 / 改密」三个端点。其余控制台 API（包括
			// 管理面与 Key 面）一律 403——前端守卫可被绕过，
			// 服务端闸才是强制。
			if p.MustChangePassword && !mustChangeAllowedPath(r.URL.Path) {
				writeError(w, http.StatusForbidden, "password_change_required",
					"首次登录必须先修改口令并补全账户信息", nil)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(
				r.Context(), ctxKeySessionPrincipal, p)))
		})
	}
}

// mustChangeAllowedPath 是「强制改密期间仍可访问」的最小路径集。
//
// 为什么只有四个：改密本身（POST /api/account/password）、看当前会话
// （前端渲染改密页要 display_name）、登出（用户想换账号），以及资料更新
// （首次页同时补全显示名，PATCH /api/account/profile）。其余端点
// 一律要求先改密——包括“查看管理面”这种看起来无害的读：能读全站
// 账户的会话没有理由在一串随机口令仍然有效时存在。
func mustChangeAllowedPath(path string) bool {
	switch path {
	case "/api/auth/session",
		"/api/auth/logout",
		"/api/account/password",
		"/api/account/profile":
		return true
	}
	return false
}

// needsCSRFCheck 报告该方法是否需要 CSRF 自定义头检查。
// GET/HEAD/OPTIONS 是只读（幂等），跨站携带 Cookie 读不到响应
// （CORS 关闭），不构成写风险。
func needsCSRFCheck(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// hasCustomHeader 报告请求是否携带跨站表单无法伪造的自定义头。
func hasCustomHeader(r *http.Request) bool {
	if v := r.Header.Get("X-Requested-With"); v != "" {
		return true
	}
	return r.Header.Get("X-CSRF-Token") != ""
}

// newSessionCookie 构造登录成功后的 Set-Cookie（02-authn.md §5 属性表）。
//
// 属性定案：
//   - HttpOnly 恒开（挡 XSS 读走会话）；
//   - Secure 仅在 tls.mode=files 时加——浏览器不通过 HTTP 回传带 Secure
//     的 Cookie，无条件加等于登录直接不可用（PLAN 9.2 的定案原文）；
//   - SameSite=Lax（02-admin §2.1 定案；OAuth 回调跳回本站时 Cookie 仍在）；
//   - MaxAge=TTL：与 expires_at 同刻——Cookie 先于会话过期会让
//     "Cookie 在但会话无"的请求打 401，反序则浏览器留着无效 Cookie。
func (s *Server) newSessionCookie(token string, ttl time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl / time.Second),
	}
	if s.cfg != nil && s.cfg.TLS.Mode == "files" {
		c.Secure = true
	}
	return c
}

// clearSessionCookie 构造登出后的清除 Cookie（MaxAge=-1 让浏览器立刻删除）。
func (s *Server) clearSessionCookie() *http.Cookie {
	c := &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
	if s.cfg != nil && s.cfg.TLS.Mode == "files" {
		c.Secure = true
	}
	return c
}

// isSameOrigin 报告请求的 Origin 头是否与 Host 同源（额外的保险层，
// 当前未强制——SameSite+自定义头已构成双保险；保留给将来 CORS 放开时）。
func isSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // 非浏览器客户端（curl/SDK）不带 Origin，放行
	}
	host := r.Host
	if h, _, err := splitHostPort(origin); err == nil {
		return strings.EqualFold(h, host)
	}
	return false
}

func splitHostPort(s string) (host, port string, err error) {
	i := strings.LastIndex(s, "://")
	if i >= 0 {
		s = s[i+3:]
	}
	j := strings.LastIndex(s, "/")
	if j >= 0 {
		s = s[:j]
	}
	return s, "", nil
}

// sessionTTL 取会话时长（装配层可覆盖；默认 24h，02-authn §5）。
func (s *Server) sessionTTL() time.Duration {
	if s.cfg != nil && time.Duration(s.cfg.Security.SessionTTL) > 0 {
		return time.Duration(s.cfg.Security.SessionTTL)
	}
	return 24 * time.Hour
}

// decodeJSONBody 是控制台面共用的请求体解析（小体积 JSON）。
//
// 为什么限长：mwBodyLimit 已在链上挡大 body；这里再限一次 8KB 是
// 防御纵深（登录表单等永远用不到 8KB），且给 JSON 解码一个明确上界。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"请求体不是合法 JSON："+err.Error(), nil)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// 装配层导出面（internal/app 经 SetConsoleRoutes 回调挂载用）。
//
// 为什么在 mw_session.go 而不是 server.go：server.go 是装配挂载点
// （写域铁律不动）；导出包装与对应私有实现放同一文件，改 handler 时
// 包装不会被落下。模式与 server.go 的 MiddlewareKeyAuth 相同（那是
// Lead 为 Key 面加的导出面）。
// ---------------------------------------------------------------------------

// ConsoleHandlerLogin 导出登录 handler。挂载点：**不挂 mwSession**——
// 登录建立会话，挂在会话判定之下是循环依赖。
func (s *Server) ConsoleHandlerLogin(deps *AuthDeps) http.HandlerFunc {
	return s.handleAuthLogin(deps)
}

// ConsoleHandlerLogout 导出登出 handler（挂 ConsoleMiddlewareSession 下）。
func (s *Server) ConsoleHandlerLogout(deps *AuthDeps) http.HandlerFunc {
	return s.handleAuthLogout(deps)
}

// ConsoleHandlerSession 导出会话主体 handler（挂 ConsoleMiddlewareSession 下）。
func (s *Server) ConsoleHandlerSession() http.HandlerFunc {
	return s.handleAuthSession()
}

// ConsoleMiddlewareSession 导出会话中间件（控制台面路由组用）。
func (s *Server) ConsoleMiddlewareSession(sessMgr *auth.SessionManager) func(http.Handler) http.Handler {
	return s.mwSession(sessMgr)
}

// ConsoleHandlerSetupStatus 导出 setup 状态真判定 handler（替换 M0 占位：
// 装配层注册本 handler 的路由即覆盖 server.go 里 handleSetupStatus 的挂载）。
func (s *Server) ConsoleHandlerSetupStatus(deps *SetupDeps) http.HandlerFunc {
	return s.handleSetupStatusV2(deps)
}

// ConsoleHandlerSetupInit 导出初始化 handler。
func (s *Server) ConsoleHandlerSetupInit(deps *SetupDeps) http.HandlerFunc {
	return s.handleSetupInit(deps)
}

// ConsoleHandlerAccountPassword 导出账户改密 handler（挂会话中间件下）。
func (s *Server) ConsoleHandlerAccountPassword(deps *AccountPasswordDeps) http.HandlerFunc {
	return s.handleAccountPassword(deps)
}

// ConsoleHandlerAccountProfile 导出账户资料 handler（挂会话中间件下）。
func (s *Server) ConsoleHandlerAccountProfile(deps *AccountPasswordDeps) http.HandlerFunc {
	return s.handleAccountProfile(deps)
}
