package httpapi

// handlers_oauth.go —— 供应商 OAuth 贡献（docs/contributions/01-flow.md §2）。
//
// 两个端点，语义完全不同：
//
//	POST /api/contributions/oauth/{provider}   控制台发起：返回 authorize_url（JSON）
//	GET  /oauth/callback?...&code=...&state=...  供应商回调：换取凭据 → 302 回前端结果页
//
// 为什么回调是**裸路径** `/oauth/callback` 而不是 `/api/...`：授权地址里的回调
// 由供应商按「端口 + 固定路径」构造（CodeArts portal 只认 `127.0.0.1:<port>/oauth/callback`），
// 路径不是我们能改的参数。因此它必须挂在根路径上，且不经过 Key 鉴权链
// （/api/* 与 /v1/* 之外本来就走不到 Key 面）。
//
// 回调成功后 302 到前端 `/#/contribute/callback?accepted=1&...`：前端那页只读
// query 渲染结果（web/src/pages/ContributePage.tsx 的 ContributeCallbackPage），
// 它不需要再发请求确认——判定结果已经在这次 302 的参数里了。
//
// 为什么失败也 302（带 accepted=0）：把 JSON 错误页丢给浏览器会让用户停在一个
// 裸 JSON 上；前端结果页有「重新发起 / 改用 API Key」两条出路。

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ProviderOAuth 是供应商 OAuth 的窄接口（装配层实现）。
type ProviderOAuth interface {
	// Providers 列出可用供应商与不可用原因（可用性由 sidecar 与移植状态决定）。
	Providers(ctx context.Context) []ProviderChoice
	// Authorize 生成授权 URL。port 是供应商回调会打到的本机端口。
	Authorize(ctx context.Context, provider string, port int) (authorizeURL string, err error)
	// Callback 用回调参数换取凭据并落库。sessionToken 为空 = 未登录贡献。
	Callback(ctx context.Context, provider, code, state, sessionToken string) (*OAuthOutcome, error)
}

// ProviderChoice 是一个供应商的可用性。
type ProviderChoice struct {
	ID                string
	Name              string
	Available         bool
	UnavailableReason string
}

// OAuthOutcome 是一次 OAuth 贡献的结果。
type OAuthOutcome struct {
	Accepted       bool
	Status         string
	VerifyStatus   string
	AccountCreated bool
	RecoveryCode   string
	Message        string
}

// OAuthDeps 是 OAuth 端点的依赖。
type OAuthDeps struct {
	OAuth ProviderOAuth
	// Port 是供应商回调要打到的本机端口（服务实际监听端口）。
	Port int
	// CallbackURL 是前端结果页路径（默认 /#/contribute/callback）。
	CallbackURL string
}

// HandleContributeOAuthStart POST /api/contributions/oauth/{provider}。
func (s *Server) HandleContributeOAuthStart(deps *OAuthDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.OAuth == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "供应商授权未装配", nil)
			return
		}
		provider := chi.URLParam(r, "provider")
		if provider == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "缺少 provider", nil)
			return
		}
		// 可用性先判：未移植的供应商给 404 not_found 而不是 501——
		// 与「未接线能力不确认存在」同一口径（docs/api/02-admin §2.2）。
		var choice *ProviderChoice
		for i, c := range deps.OAuth.Providers(r.Context()) {
			if c.ID == provider {
				choice = &deps.OAuth.Providers(r.Context())[i]
				break
			}
		}
		if choice == nil {
			writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
			return
		}
		if !choice.Available {
			writeError(w, http.StatusServiceUnavailable, "upstream_unavailable",
				choice.UnavailableReason, nil)
			return
		}
		authorizeURL, err := deps.OAuth.Authorize(r.Context(), provider, deps.Port)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"authorize_url": authorizeURL},
		})
	}
}

// HandleOAuthCallback GET /oauth/callback?code=&state=。
//
// 必须挂在根路径、不挂会话中间件：供应商不会带 Cookie（跨站跳转），
// 而"是哪个贡献者"由 state 里的授权会话决定（未登录则自动建 contributor）。
// 若浏览器恰好带着本站会话 Cookie（同一浏览器发起的授权），这里仍会用它，
// 让贡献落在当前账户下。
func (s *Server) HandleOAuthCallback(deps *OAuthDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.OAuth == nil {
			s.oauthFail(w, r, deps, "供应商授权未装配")
			return
		}
		q := r.URL.Query()
		code := strings.TrimSpace(q.Get("code"))
		state := strings.TrimSpace(q.Get("state"))
		provider := strings.TrimSpace(q.Get("provider"))
		if provider == "" {
			// portal 不带宽 supplier 段：只有 codearts 一家时按它的回调形状兜底。
			provider = "codearts"
		}
		if code == "" {
			// 旧 ticket 流程回退（portal 判定 OAuth 不可用时会带 secret+redirect）：
			// 这条回退路径需要轮询 ticket 端点，本期不接——如实告诉用户重试。
			s.oauthFail(w, r, deps, "供应商没有返回授权码（可能回退到了不支持的旧登录流程）")
			return
		}
		cookieToken := ""
		if c, err := r.Cookie(SessionCookieName); err == nil {
			cookieToken = c.Value
		}
		out, err := deps.OAuth.Callback(r.Context(), provider, code, state, cookieToken)
		if err != nil {
			s.oauthFail(w, r, deps, err.Error())
			return
		}
		target := deps.callbackURL()
		params := url.Values{}
		if out.Accepted {
			params.Set("accepted", "1")
		}
		if out.AccountCreated {
			params.Set("account_created", "1")
		}
		if out.RecoveryCode != "" {
			params.Set("recovery_code", out.RecoveryCode)
		}
		if out.Message != "" {
			params.Set("message", out.Message)
		}
		http.Redirect(w, r, target+withHashQuery(target, params), http.StatusFound)
	}
}

// oauthFail 把失败也送到前端结果页（带 message），而不是裸 JSON。
func (s *Server) oauthFail(w http.ResponseWriter, r *http.Request, deps *OAuthDeps, message string) {
	if deps == nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", message, nil)
		return
	}
	target := deps.callbackURL()
	params := url.Values{}
	params.Set("accepted", "0")
	params.Set("message", message)
	http.Redirect(w, r, target+withHashQuery(target, params), http.StatusFound)
}

// callbackURL 取前端结果页路径（默认 /#/contribute/callback）。
func (d *OAuthDeps) callbackURL() string {
	if d.CallbackURL != "" {
		return d.CallbackURL
	}
	return "/#/contribute/callback"
}

// withHashQuery 把参数拼到 HashRouter 的 query 段（`/#/path?a=1`）。
//
// 为什么不是 `?a=1#/path`：HashRouter 只解析 # 之后的部分，参数必须进 hash 内，
// 否则前端读 useSearchParams 得到空。
func withHashQuery(target string, params url.Values) string {
	if len(params) == 0 {
		return ""
	}
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	return sep + params.Encode()
}
