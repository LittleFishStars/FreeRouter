package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// mw_keyauth.go：L1 链第 5 步（鉴权）在 API Key 面的中间件壳。
//
// 壳的职责边界：HTTP 形状（Authorization 头解析、401 错误体、context 注入）
// 全部在本层；鉴权**判定**全部在 auth.KeyAuthenticator——中间件不写任何
// "什么算有效凭据"的分支，否则判定逻辑会在 HTTP 层复制一份并开始漂移。
//
// 装配归 Lead（不改 server.go 的红线）：Handler() 里在 mwBodyLimit 之后
// 挂 s.mwKeyAuth(auth.NewKeyAuthenticator(store))，随后 mwRate、mw_quota。
// 本文件只提供函数，不注册路由。

// ctxKeyKeyPrincipal 是 Key 鉴权主体的 context 键（middleware.go 的
// ctxKey 枚举池里加段：鉴权主体键从 100 起步，与会话/IR 未来的键
// 各占一段；同池不同段，避免两个文件各自 iota 产生撞值）。
const ctxKeyKeyPrincipal ctxKey = 100

// KeyPrincipalFromContext 取 Key 鉴权主体；不在上下文（未过鉴权/会话面）返回 nil。
func KeyPrincipalFromContext(ctx context.Context) *auth.KeyPrincipal {
	v, _ := ctx.Value(ctxKeyKeyPrincipal).(*auth.KeyPrincipal)
	return v
}

// mwKeyAuth 是 Key 鉴权中间件（docs/security/04-authz.md §4 第 2~4 步）。
//
// 失败响应统一 401 invalid_api_key（03-errors §3.1）——包括账户禁用：
// 机器凭据面**不给探测面**（与 keyauth.go 的单出口哨兵同一条纪律，
// 05-keys.md §2 联动表"账户 disabled → 名下全部 Key 立即 401"）。
// 响应体不写 detail（Dimension 差异只进日志）。
func (s *Server) mwKeyAuth(authn *auth.KeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 公共路径豁免：鉴权链在 server.go 是全局 r.Use（装配层注入），
			// 但运维探针（healthz/readyz——LB 探活不能带凭据）、公开信息
			// （/api/version）、控制台鉴权面（/api/auth/*——登录建立会话，
			// 它自身就是凭据入口）与初始化向导（/api/setup/*——0 账户窗口
			// 内没有可鉴权主体，窗口判定在 handler 内）都不是 Key 面。
			// 不豁免会让整个控制台与探针被 Key 面挡死（集成实测 401）。
			if isNonKeyPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			raw := auth.ParseBearer(r.Header.Get("Authorization"))
			p, err := authn.Authenticate(r.Context(), raw)
			if err != nil {
				// 装配错误（CodeInternal，如缺存储依赖）是 500 面，不是凭据面；
				// 其余一律 401 invalid_api_key 单出口（防枚举）。
				// 判据用 Code 而不是错误文本——文本匹配会在文案调整时静默失效。
				var ae *auth.Error
				if asAuthError(err, &ae) && ae.Code == auth.CodeInternal {
					writeError(w, http.StatusInternalServerError, string(auth.CodeInternal), ae.Msg, nil)
					return
				}
				writeError(w, http.StatusUnauthorized, string(auth.CodeInvalidAPIKey), "API Key 无效或缺失", nil)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(
				r.Context(), ctxKeyKeyPrincipal, p)))
		})
	}
}

// nonKeyPaths 是 Key 鉴权链的公共路径豁免表。
//
// 为什么用精确表而不是前缀匹配：豁免面必须**穷举可审计**——每一条
// 都有"为什么它不需要 Key"的理由写在注释里；前缀匹配（如 strings.
// HasPrefix(path, "/api/")）会悄悄把将来的管理端点也豁免掉。
var nonKeyPaths = map[string]bool{
	"/healthz":          true, // LB/K8s 探活，不带凭据（server.go 注释：不进鉴权）
	"/readyz":           true, // 同上
	"/api/version":      true, // 公开探测面（01-inbound §1：供客户端探测）
	"/api/auth/login":   true, // 登录建立会话：凭据入口本身，Key 面无意义
	"/api/auth/logout":  true, // 登出走会话校验（mwSession），非 Key 面
	"/api/auth/session": true, // 同上
	"/api/setup/status": true, // 向导状态：0 账户窗口的判定入口
	"/api/setup/init":   true, // 向导创建：窗口判定在 handler（先 409 后验参）
}

// nonKeyPrefixes 是**整组**走会话面（而非 Key 面）的路径前缀。
//
// 为什么必须有前缀豁免：管理面 45 个端点、账户自助若干端点都用人机会话
// 鉴权（mwSession / mwAdmin），逐个登记进 nonKeyPaths 既不可维护、也会
// 在新增端点时漏登记——而漏登记的表现正是「登录了仍然 401 invalid_api_key」
// （M7 装配实测踩到）。这里只放整组语义明确的会话面，并逐条写明理由；
// Key 面（/v1/*）的端点仍不在豁免范围内。
var nonKeyPrefixes = []string{
	"/api/admin/",   // 管理面：会话 + mwAdmin（45 端点整组）
	"/api/account/", // 账户自助：会话（改密/会话管理/用量）
}

// isNonKeyPath 报告该路径是否豁免 Key 鉴权。
func isNonKeyPath(p string) bool {
	if nonKeyPaths[p] {
		return true
	}
	for _, pre := range nonKeyPrefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	// 非 API/协议路径（控制台静态页与 SPA 资源）不是 Key 面：
	// keyAuth 是全局 r.Use，若不豁免，浏览器打开 "/" 会直接 401，
	// 整个控制台前端无法加载（实测踩到）。
	//
	// 只用两个前缀判定而不是枚举静态文件：前端产物文件名带 hash，
	// 枚举必然漏；而 "/api/*" 与 "/v1/*" 之外没有任何需要 Key 的端点
	// （未知路径最终落到 JSON 404，不会变成“无鉴权的 API”）。
	return !strings.HasPrefix(p, "/api/") &&
		p != "/api" &&
		!strings.HasPrefix(p, "/v1/") &&
		p != "/v1"
}
