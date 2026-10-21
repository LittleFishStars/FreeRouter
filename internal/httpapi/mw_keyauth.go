package httpapi

import (
	"context"
	"net/http"

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
