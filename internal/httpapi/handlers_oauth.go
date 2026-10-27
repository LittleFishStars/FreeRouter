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
//
// 三种交互形态由 sidecar 的注册表收敛成两个动作对：
//   - redirect：Authorize 返回 authorize_url + state，供应商把 code 回传到
//     /oauth/callback，主进程再 Callback 换取凭据；
//   - session：Authorize 返回 session_id，sidecar 在**后台**轮询（设备码/二维码/
//     短信——上游的 pollXxx 是阻塞循环，不能占用 RPC 通道），主进程用 Poll 查状态，
//     拿到凭据后落库。
type ProviderOAuth interface {
	// Providers 列出可用供应商与不可用原因（可用性由 sidecar 与移植状态决定）。
	Providers(ctx context.Context) []ProviderChoice
	// Authorize 发起授权。phone 仅 sms 形态需要。
	Authorize(ctx context.Context, in AuthorizeInput) (*AuthorizeResult, error)
	// Callback 用回调参数换取凭据并落库（redirect 形态）。
	// rawQuery 是回调的原始查询串：TRAE 的回调不带标准 code 参数，
	// 而是带 userInfo/userJwt/authCodeInfo 等自有字段——provider 侧
	// 按需解析（标准 OAuth provider 忽略它）。
	Callback(ctx context.Context, provider, code, state, sessionToken, rawQuery string) (*OAuthOutcome, error)
	// Poll 查一次后台会话状态（session 形态）；完成时落库并返回结果。
	Poll(ctx context.Context, in PollInput) (*PollResult, error)
}

// AuthorizeInput 是发起授权的入参。
type AuthorizeInput struct {
	Provider string
	Port     int
	Phone    string
	// CaptchaParam 是阿里云滑块验证码的产物（Raccoon 短信登录必需，
	// 其他 provider 忽略）。从 start 的 query 传入（滑块完成即发起）。
	CaptchaParam string
	// SessionToken 是浏览器可能带的会话 Cookie（贡献归属用；可为空）。
	SessionToken string
}

// AuthorizeResult 是发起授权的结果。
type AuthorizeResult struct {
	// Mode 是 'redirect' 或 'session'。
	Mode            string `json:"mode"`
	AuthorizeURL    string `json:"authorize_url,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	QRImageURL      string `json:"qr_image_url,omitempty"`
	// QRImageData 是二维码的 data URL（sidecar 已抓取并内联）。
	//
	// 为什么不用 QRImageURL：站点的 CSP 是 `img-src 'self' data:`，微信的
	// 二维码域名不在白名单里，浏览器会**在发请求前**就拦掉（实测 <img>
	// naturalWidth 恒为 0、网络面板里连请求都没有）。所以由 sidecar 抓取、
	// 以 data URL 下发；QRImageURL 保留给"将来放宽 CSP 或做了反代"的场景。
	QRImageData string `json:"qr_image_data,omitempty"`
	Flow        string `json:"flow,omitempty"`
	Interval    int    `json:"interval,omitempty"`
	Message     string `json:"message,omitempty"`
	// QRCode 是二维码承载的**原始文本**（如 Raccoon 的登录页 URL）：
	// sidecar 没有现成图片端点、也没有 QR 编码依赖时，由前端本地画码。
	// 与 QRImageData 互斥使用：有图用图，有文本画码。
	QRCode string `json:"qr_code,omitempty"`
}

// PollInput 是查询后台授权会话的入参。
type PollInput struct {
	Provider     string
	SessionID    string
	Code         string // sms 形态：用户回填的验证码
	SessionToken string
}

// PollResult 是会话状态。
type PollResult struct {
	Status   string        `json:"status"` // pending | ok | failed | expired
	Message  string        `json:"message,omitempty"`
	Outcome  *OAuthOutcome `json:"outcome,omitempty"`
	Interval int           `json:"interval,omitempty"`
}

// ProviderChoice 是一个供应商的可用性。
type ProviderChoice struct {
	ID                string
	Name              string
	Flow              string
	Needs             []string
	Available         bool
	UnavailableReason string
	// UnavailableKind 区分「不可用」的两种性质，前端据此给不同的措辞：
	//
	//	"config"      —— 我们这边缺配置（例如没设 OAuth 客户端 secret），
	//	                 补上配置就能用，属于运维可操作项。
	//	"unsupported" —— 上游没有可脱离 IDE 的 OAuth 登录面（或登录流会自己
	//	                 起 HTTP 服务、与 sidecar「不监听端口」红线冲突），
	//	                 补任何配置都不会变成可用，只能走 API Key 型贡献。
	//
	// 为什么必须分开：两类在界面上都叫"不可用"会让运维去翻配置找那三家
	// 本来就不该有配置的供应商，也会让他们以为缺配置的三家是产品功能缺失。
	UnavailableKind string
	// SuggestedEndpoint / Steps 是「不能用 OAuth，但可以这样贡献」的替代路径：
	// SuggestedEndpoint 是这家的 OpenAI 兼容 base URL（前端一键填进 API Key
	// 表单），Steps 是"去哪儿拿凭据"的操作清单。对可用的供应商留空。
	SuggestedEndpoint string
	Steps             []string
}

// 不可用性质常量（见 ProviderChoice.UnavailableKind）。
const (
	UnavailableKindConfig      = "config"
	UnavailableKindUnsupported = "unsupported"
)

// OAuthOutcome 是一次 OAuth 贡献的结果。
type OAuthOutcome struct {
	Accepted       bool   `json:"accepted"`
	Status         string `json:"status"`
	VerifyStatus   string `json:"verify_status"`
	AccountCreated bool   `json:"account_created"`
	RecoveryCode   string `json:"recovery_code,omitempty"`
	Message        string `json:"message"`
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
		res, err := deps.OAuth.Authorize(r.Context(), AuthorizeInput{
			Provider:     provider,
			Port:         deps.Port,
			Phone:        strings.TrimSpace(r.URL.Query().Get("phone")),
			CaptchaParam: strings.TrimSpace(r.URL.Query().Get("captcha_param")),
			SessionToken: cookieValue(r),
		})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": res})
	}
}

// HandleOAuthCallback GET /oauth/callback?code=&state=。
//
// 必须挂在根路径、不挂会话中间件：供应商不会带 Cookie（跨站跳转），
// 而"是哪个贡献者"由 state 里的授权会话决定（未登录则自动建 contributor）。
// 若浏览器恰好带着本站会话 Cookie（同一浏览器发起的授权），这里仍会用它，
// 让贡献落在当前账户下。
//
// CORS 与 OPTIONS：供应商 portal（https 源）在确认授权前会用 fetch 探测
// 回环回调的可达性——跨源探测先发 CORS 预检 OPTIONS。参照 hub 的
// login.js（OPTIONS → 204 + CORS，GET 应答也带 CORS）：不答预检，portal
// 判定「本地客户端未就绪」，确认按钮点了无反应（2026-10-27 用户实测）。
// Allow-Origin 用 * 安全：这条路由只消费 URL 参数、不读 Cookie 做鉴权，
// 回应体是重定向地址而非敏感数据。
func (s *Server) HandleOAuthCallbackPreflight() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		oauthCallbackCORS(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

// oauthCallbackCORS 写回环回调所需的跨源头（与 hub login.js 的 CORS_HEADERS 同形）。
func oauthCallbackCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	h.Set("Access-Control-Max-Age", "86400")
}

func (s *Server) HandleOAuthCallback(deps *OAuthDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		oauthCallbackCORS(w)
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
		cookieToken := ""
		if c, err := r.Cookie(SessionCookieName); err == nil {
			cookieToken = c.Value
		}
		// TRAE 的回调 query 里没有 code（带的是 userInfo/userJwt 等自有字段），
		// 所以「没有 code」不再直接判失败——交给 provider 侧按原始 query 解析；
		// 两者都空才是真的没参数。
		rawQuery := r.URL.RawQuery
		if code == "" && rawQuery == "" {
			// 旧 ticket 流程回退（portal 判定 OAuth 不可用时会带 secret+redirect）：
			// 这条回退路径需要轮询 ticket 端点，本期不接——如实告诉用户重试。
			s.oauthFail(w, r, deps, "供应商没有返回授权码（可能回退到了不支持的旧登录流程）")
			return
		}
		out, err := deps.OAuth.Callback(r.Context(), provider, code, state, cookieToken, rawQuery)
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
		// 两种消费模式分流（Sec-Fetch-Mode，现代浏览器都发）：
		// - cors（portal 确认弹窗里 fetch 本地回调验证在线）→ 答 200 空体，
		//   对齐 hub login.js 的 writeHead(200).end()。若答 302，fetch 会跟随
		//   到 SPA 页，应答体是整页 HTML，portal 校验不过时确认流程静默失败。
		// - navigate / 无头（老浏览器顶层 302）→ 302 落前端结果页（原行为）。
		switch r.Header.Get("Sec-Fetch-Mode") {
		case "cors", "no-cors":
			w.WriteHeader(http.StatusOK)
			return
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
	// fetch 模式（portal 确认弹窗）答 400 + 错误文本 + CORS，对齐 hub 的兜底
	//（writeHead(400).end('Missing token or secret')）；302 会让 fetch 跟随
	// 到 SPA 页，portal 校验不过时错误被吞、弹窗无反馈。顶层导航仍 302。
	switch r.Header.Get("Sec-Fetch-Mode") {
	case "cors", "no-cors":
		oauthCallbackCORS(w)
		http.Error(w, message, http.StatusBadRequest)
		return
	}
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

// cookieValue 取会话 Cookie 明文（没有则空串）。
func cookieValue(r *http.Request) string {
	if c, err := r.Cookie(SessionCookieName); err == nil {
		return c.Value
	}
	return ""
}

// HandleOAuthPoll POST /api/contributions/oauth/{provider}/poll。
//
// session 形态（设备码/二维码/短信）的进度查询：前端每隔 interval 秒调一次。
// 授权完成时 sidecar 交出凭据，这里立刻落库并把结果回给前端（含未登录贡献时的
// 一次性恢复码）。
func (s *Server) HandleOAuthPoll(deps *OAuthDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.OAuth == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "供应商授权未装配", nil)
			return
		}
		provider := chi.URLParam(r, "provider")
		var body struct {
			SessionID string `json:"session_id"`
			Code      string `json:"code"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		if body.SessionID == "" {
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "session_id", "缺少 session_id")
			return
		}
		res, err := deps.OAuth.Poll(r.Context(), PollInput{
			Provider:     provider,
			SessionID:    body.SessionID,
			Code:         strings.TrimSpace(body.Code),
			SessionToken: cookieValue(r),
		})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": res})
	}
}

// HandleOAuthCancel POST /api/contributions/oauth/{provider}/cancel。
//
// 用户关掉授权页时释放 sidecar 的后台轮询（幂等）。
func (s *Server) HandleOAuthCancel(deps *OAuthDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.OAuth == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "供应商授权未装配", nil)
			return
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		c, ok := deps.OAuth.(interface {
			Cancel(ctx context.Context, sessionID string) error
		})
		if !ok {
			writeError(w, http.StatusInternalServerError, "internal_error", "取消能力未装配", nil)
			return
		}
		if err := c.Cancel(r.Context(), body.SessionID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
