package app

// oauth_codearts.go —— 供应商 OAuth 贡献（14 家）的主进程侧编排。
//
// 链路（docs/contributions/01-flow.md §2 + PLAN §5.5）：
//
//	POST /api/contributions/oauth/{provider}        → 发起授权（redirect 或 session）
//	POST /api/contributions/oauth/{provider}/poll   → 查后台会话（session 形态）
//	GET  /oauth/callback?code=&state=               → 供应商回调（redirect 形态）
//
// 为什么协议在 sidecar：PLAN §5.5 定案「17 家供应商的 OAuth 与静默续期不可能
// 重写」。15 家的实现已按提取计划移植进 sidecar/providers/，主进程只做编排与落库。
//
// 两种形态的差别（由 sidecar 注册表决定，主进程只转发）：
//   - redirect：sidecar 出 authorize_url + state，供应商把 code 回传到这里，
//     再交给 sidecar 换取；state（含 PKCE verifier / DPoP 私钥）放进程内一次性表。
//   - session：设备码/二维码/短信。上游的轮询是**阻塞循环**，会卡住 10 秒超时的
//     RPC 通道，所以轮询在 sidecar 后台跑，主进程只做状态转发。
//
// 三条红线不变：sidecar 不落盘、不读 DSH 状态；凭据由主进程 AES-GCM 密封入库。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// oauthStateTTL 是 redirect 形态授权会话的寿命。
//
// 为什么是 30 分钟而不是 sidecar sessions 的 10 分钟：这条 TTL 罩的是
// 「用户去供应商 portal 登录账号」的慢流程——输账号密码、短信/MFA、
// 找控制台入口，10 分钟经常不够（2026-10-27 用户 CodeArts 实测超时）。
// 安全上可接受：nonce 是一次性 CSRF 令牌（take 即删），放宽窗口只是
// 拉长「未消费令牌」的有效期，不扩大重放面。session 形态（扫码）不受
// 影响：二维码自己几分钟就过期（微信侧返 402），由供应商时限兜底。
const oauthStateTTL = 30 * time.Minute

// oauthStateStore 是 redirect 形态待完成授权的进程内一次性表。
type oauthStateStore struct {
	mu    sync.Mutex
	items map[string]oauthState
	nowFn func() time.Time
}

type oauthState struct {
	provider     string
	sidecarState string // sidecar 回的换取材料（含 PKCE verifier 与 DPoP 私钥）
	createdAt    time.Time
}

func newOAuthStateStore() *oauthStateStore {
	return &oauthStateStore{items: map[string]oauthState{}, nowFn: time.Now}
}

// put 存入一条授权会话，返回浏览器可见的 state nonce。
func (s *oauthStateStore) put(st oauthState) string {
	nonce := randomToken(24)
	st.createdAt = s.nowFn()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
	s.items[nonce] = st
	return nonce
}

// take 取出并删除（一次性）。不存在/过期返回 false。
func (s *oauthStateStore) take(nonce string) (oauthState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
	st, ok := s.items[nonce]
	if !ok {
		return oauthState{}, false
	}
	delete(s.items, nonce)
	return st, true
}

func (s *oauthStateStore) reapLocked() {
	now := s.nowFn()
	for k, v := range s.items {
		if now.Sub(v.createdAt) > oauthStateTTL {
			delete(s.items, k)
		}
	}
}

// randomToken 生成 n 字节 base64url 随机串。
func randomToken(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		// 熵源失效是不可继续的状态：宁可 panic 也不要发放可预测的 state。
		panic("crypto/rand 不可用: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// oauthBridge 实现 httpapi.ProviderOAuth。
type oauthBridge struct {
	sidecar *dshprovider.Client
	states  *oauthStateStore
	st      *store.Store
	sess    *auth.SessionManager
	contrib *contributionBridge
	// settled 是「已完成落库的 session」缓存：session 形态下 sidecar 对已
	// 完成 session 的每次 poll 都会原样返回 credential（无消费一次即清的
	// 语义），而前端每 3 秒轮询一次——没有这层幂等，同一凭据会被反复
	// settle 落库（MiniMax 实测第二次起撞 (provider, external_id) 唯一
	// 索引报 UNIQUE constraint failed）。key=sessionId，value=落库结果。
	// 与 states 同生命周期（进程内存，重启即清，重新授权走新 session）。
	settledMu sync.Mutex
	settled   map[string]*httpapi.OAuthOutcome
}

var _ httpapi.ProviderOAuth = (*oauthBridge)(nil)

// sidecarProvider 是 oauth.list 的一行。
type sidecarProvider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Flow    string   `json:"flow"`
	Needs   []string `json:"needs"`
	APIBase string   `json:"apiBase"`
	Kind    string   `json:"kind"`
	// Unavailable 是「配置缺失」的原因（如未设 OAuth 客户端 secret）。
	// 非空时该 provider 列出但不可用——界面显示原因而不是一个必败的按钮。
	Unavailable string `json:"unavailable"`
}

// sidecarList 是 oauth.list 的应答。
type sidecarList struct {
	Providers []sidecarProvider `json:"providers"`
	// APIKeyOnly 是旧形状的「只能粘贴 Key」清单。贡献入口 v2 已删掉两家
	// （modelscope / opencode），本进程不再读取它；保留字段是为了能让旧 sidecar
	// 的应答原样解析（多一个空数组不报错），而新 sidecar 恒返回空。
	APIKeyOnly []struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Reason  string   `json:"reason"`
		BaseURL string   `json:"baseUrl"`
		Steps   []string `json:"steps"`
	} `json:"api_key_only"`
}

// list 拉一次 sidecar 的 provider 清单。
func (b *oauthBridge) list(ctx context.Context) (*sidecarList, error) {
	if ok, _ := b.availability(); !ok {
		return nil, fmt.Errorf("sidecar 不可用")
	}
	raw, err := b.sidecar.Call(ctx, "oauth.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out sidecarList
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 oauth.list 应答失败: %w", err)
	}
	return &out, nil
}

func (b *oauthBridge) availability() (bool, string) {
	if b.sidecar == nil {
		return false, "sidecar 未运行：供应商 OAuth 需要 Node sidecar 承载移植后的登录实现"
	}
	st := b.sidecar.Status()
	if !st.Available {
		return false, "sidecar 不可用：" + st.Reason
	}
	return true, ""
}

// Providers 列出**反代供应商**（reverse_proxy 分类的 items）：sidecar 在 → 用它
// 的清单；不在 → 全部标不可用并给原因。
//
// 为什么不可用时仍列出：界面上「这个入口存在但需要 sidecar」比「凭空少 14 个
// 供应商」更可诊断——用户能自己发现是没起 sidecar。
//
// 另两个分类（api / custom）不经过这里：api 组由 internal/apidir 提供（编译进
// 二进制的本地事实数据，不依赖 sidecar），custom 组恒为空（入口是表单）。
func (b *oauthBridge) Providers(ctx context.Context) []httpapi.ProviderChoice {
	avail, reason := b.availability()
	list, err := b.list(ctx)
	if err != nil {
		if avail {
			reason = "读取供应商清单失败：" + err.Error()
		}
		return fallbackProviders(avail, reason)
	}
	out := make([]httpapi.ProviderChoice, 0, len(list.Providers))
	for _, p := range list.Providers {
		c := httpapi.ProviderChoice{
			ID: p.ID, Name: p.Name, Flow: p.Flow, Needs: p.Needs,
			Available: avail,
		}
		switch {
		case !avail:
			c.UnavailableReason = reason
			c.UnavailableKind = httpapi.UnavailableKindConfig
		case p.Unavailable != "":
			// sidecar 在、但这家缺配置（如没设 OAuth 客户端 secret）。
			c.Available = false
			c.UnavailableReason = p.Unavailable
			c.UnavailableKind = httpapi.UnavailableKindConfig
		}
		out = append(out, c)
	}
	// sidecar 的 api_key_only 不再并入：这类需求已由 api 分类（内置目录）承担，
	// 其 flow 也不再是 api_key。字段仍保留在 sidecarList 里只是为了兼容旧 sidecar
	// 的应答形状（那份列表现在是空数组）。
	return out
}

// fallbackNames 是 sidecar 不可达时的展示清单（与移植目录一一对应）。
var fallbackNames = [][2]string{
	{"codearts", "华为云 CodeArts"}, {"gemini", "Google Gemini"}, {"accio", "Accio"},
	{"minimax", "MiniMax"}, {"cline", "Cline"}, {"qoder", "Qoder"},
	{"loomy", "Loomy"}, {"raccoon", "Raccoon"}, {"lobsterai", "LobsterAI"},
	{"trae", "TRAE"}, {"autoclaw", "AutoClaw"},
	{"officeace", "OfficeAce（华为云）"}, {"zcode", "ZCode"},
	{"buddy", "CodeBuddy（腾讯）"}, {"workbuddy", "WorkBuddy（国际版）"}, {"codely", "Codely"},
}

func fallbackProviders(avail bool, reason string) []httpapi.ProviderChoice {
	out := make([]httpapi.ProviderChoice, 0, len(fallbackNames))
	for _, n := range fallbackNames {
		out = append(out, httpapi.ProviderChoice{
			ID: n[0], Name: n[1], Available: avail, UnavailableReason: reason,
			UnavailableKind: httpapi.UnavailableKindConfig,
		})
	}
	return out
}

// Authorize 发起授权：redirect 形态登记 state，session 形态直接透传 session_id。
func (b *oauthBridge) Authorize(ctx context.Context, in httpapi.AuthorizeInput) (*httpapi.AuthorizeResult, error) {
	if ok, reason := b.availability(); !ok {
		return nil, fmt.Errorf("%s", reason)
	}
	params := map[string]any{"provider": in.Provider, "port": in.Port}
	if in.Phone != "" {
		params["phone"] = in.Phone
	}
	if in.CaptchaParam != "" {
		params["captcha_param"] = in.CaptchaParam
	}
	raw, err := b.sidecar.Call(ctx, "oauth.authorize", params)
	if err != nil {
		return nil, err
	}
	var res struct {
		Mode            string `json:"mode"`
		AuthorizeURL    string `json:"authorize_url"`
		State           string `json:"state"`
		SessionID       string `json:"session_id"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		QRImageURL      string `json:"qr_image_url"`
		QRImageData     string `json:"qr_image_data"`
		QRCode          string `json:"qr_code"`
		Flow            string `json:"flow"`
		Interval        int    `json:"interval"`
		Message         string `json:"message"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("解析授权应答失败: %w", err)
	}
	out := &httpapi.AuthorizeResult{
		Mode: res.Mode, SessionID: res.SessionID, UserCode: res.UserCode,
		VerificationURI: res.VerificationURI, QRImageURL: res.QRImageURL,
		QRImageData: res.QRImageData,
		QRCode:      res.QRCode,
		Flow:        res.Flow, Interval: res.Interval, Message: res.Message,
	}
	if res.Mode == "session" {
		out.AuthorizeURL = res.AuthorizeURL
		return out, nil
	}
	if res.AuthorizeURL == "" {
		return nil, fmt.Errorf("sidecar 未返回 authorize_url")
	}
	nonce := b.states.put(oauthState{provider: in.Provider, sidecarState: res.State})
	sep := "&"
	if !hasQuery(res.AuthorizeURL) {
		sep = "?"
	}
	out.AuthorizeURL = res.AuthorizeURL + sep + "state=" + nonce
	return out, nil
}

// hasQuery 判断 URL 里是否已有查询串。
func hasQuery(u string) bool {
	for i := 0; i < len(u); i++ {
		if u[i] == '?' {
			return true
		}
	}
	return false
}

// Callback 处理 redirect 形态的供应商回调。
func (b *oauthBridge) Callback(ctx context.Context, provider, code, nonce, sessionToken, rawQuery string) (*httpapi.OAuthOutcome, error) {
	st, ok := b.states.take(nonce)
	if !ok {
		// 三种触发场景都要点到（用户只能看到这行字）：超时（登录供应商账号
		// 超过 30 分钟）、服务重启（内存表丢失，旧标签页/旧链接作废）、
		// 链接已被用（一次性令牌，刷新结果页会重放回调）。
		return nil, fmt.Errorf("授权会话不存在或已过期：可能是从发起授权到完成登录超过了 30 分钟、" +
			"授权期间服务重启过（旧标签页作废）、或该链接已被使用。请回到贡献入口重新发起一次")
	}
	if st.provider != provider {
		return nil, fmt.Errorf("state 与 provider 不匹配")
	}
	if ok, reason := b.availability(); !ok {
		return nil, fmt.Errorf("%s", reason)
	}
	params := map[string]any{
		"provider": provider,
		"code":     code,
		"state":    st.sidecarState,
	}
	// rawQuery 原样透传：TRAE 的回调 query 不含 code（userInfo/userJwt/…），
	// 由 provider 侧自行解析；标准 OAuth provider 忽略它。
	if rawQuery != "" {
		params["raw_query"] = rawQuery
	}
	raw, err := b.sidecar.Call(ctx, "oauth.exchange", params)
	if err != nil {
		return nil, fmt.Errorf("换取失败：%s", sidecarMessage(err))
	}
	var ex struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(raw, &ex); err != nil {
		return nil, fmt.Errorf("解析换取应答失败: %w", err)
	}
	if ex.Credential == "" {
		return nil, fmt.Errorf("sidecar 未返回凭据")
	}
	return b.settle(ctx, provider, ex.Credential, sessionToken)
}

// Poll 查一次 session 形态的后台会话；完成时落库。
func (b *oauthBridge) Poll(ctx context.Context, in httpapi.PollInput) (*httpapi.PollResult, error) {
	// 已落库的 session 直接回放缓存结果：后续 poll 不再触碰落库路径。
	if in.SessionID != "" {
		b.settledMu.Lock()
		cached := b.settled[in.SessionID]
		b.settledMu.Unlock()
		if cached != nil {
			return &httpapi.PollResult{Status: "ok", Outcome: cached}, nil
		}
	}
	if ok, reason := b.availability(); !ok {
		return nil, fmt.Errorf("%s", reason)
	}
	params := map[string]any{"sessionId": in.SessionID}
	if in.Provider != "" {
		params["provider"] = in.Provider
	}
	if in.Code != "" {
		params["code"] = in.Code
	}
	raw, err := b.sidecar.Call(ctx, "oauth.poll", params)
	if err != nil {
		return nil, fmt.Errorf("查询授权状态失败：%s", sidecarMessage(err))
	}
	var res struct {
		Status     string `json:"status"`
		Message    string `json:"message"`
		Credential string `json:"credential"`
		Interval   int    `json:"interval"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("解析轮询应答失败: %w", err)
	}
	out := &httpapi.PollResult{Status: res.Status, Message: res.Message, Interval: res.Interval}
	if res.Status != "ok" {
		return out, nil
	}
	if res.Credential == "" {
		return &httpapi.PollResult{Status: "failed", Message: "授权完成但未返回凭据"}, nil
	}
	o, err := b.settle(ctx, in.Provider, res.Credential, in.SessionToken)
	if err != nil {
		return &httpapi.PollResult{Status: "failed", Message: err.Error()}, nil
	}
	if in.SessionID != "" {
		b.settledMu.Lock()
		if b.settled == nil {
			b.settled = map[string]*httpapi.OAuthOutcome{}
		}
		b.settled[in.SessionID] = o
		b.settledMu.Unlock()
	}
	out.Outcome = o
	return out, nil
}

// Cancel 释放 sidecar 的后台会话（幂等）。
func (b *oauthBridge) Cancel(ctx context.Context, sessionID string) error {
	if b.sidecar == nil || !b.sidecar.Available() {
		return nil // sidecar 不在 = 会话早已不存在
	}
	_, err := b.sidecar.Call(ctx, "oauth.cancel", map[string]any{"sessionId": sessionID})
	return err
}

// settle 把拿到的凭据落到指定归属账户（或新建 contributor），并生成恢复码。
func (b *oauthBridge) settle(ctx context.Context, provider, credential, sessionToken string) (*httpapi.OAuthOutcome, error) {
	var owner string
	created := false
	if id, ok := b.sessionAccount(ctx, sessionToken); ok {
		owner = id
	} else {
		id, err := b.contrib.CreateContributor(ctx, "贡献者")
		if err != nil {
			return nil, err
		}
		owner, created = id, true
	}
	if _, err := b.persist(ctx, owner, provider, credential); err != nil {
		return nil, err
	}
	res := &httpapi.OAuthOutcome{
		// OAuth 凭据已拿到但尚未通过目录探测：按「待鉴定」记录，不入池。
		// 刻意的保守：01-flow 5.2「落库与可调用分开」。
		Accepted:       false,
		Status:         string(store.CredPending),
		VerifyStatus:   string(store.VerifyUnverified),
		AccountCreated: created,
		Message:        "OAuth 授权成功，凭据已入库并等待鉴定",
	}
	if created {
		if code, err := issueRecoveryFor(ctx, b.contrib, owner); err == nil {
			res.RecoveryCode = code
		}
	}
	return res, nil
}

// sessionAccount 解析会话 Cookie → 账户 id。无效/过期一律当作未登录。
func (b *oauthBridge) sessionAccount(ctx context.Context, token string) (string, bool) {
	if b.sess == nil || token == "" {
		return "", false
	}
	p, err := b.sess.Validate(ctx, token, "")
	if err != nil {
		return "", false
	}
	id, err := b.contrib.AccountIDOf(ctx, p.AccountID)
	if err != nil {
		return "", false
	}
	return id, true
}

// persist 把 OAuth 凭据写入存储：上游行（enabled=false）+ AES-GCM 密文 + 凭据行。
//
// 上游行的 base_url/kind 来自 sidecar 的 provider 清单（每家推理端点不同）；
// 拿不到时退回 defaultEndpoint。
func (b *oauthBridge) persist(ctx context.Context, owner string, provider, credential string) (string, error) {
	fingerprint := upstream.KeyFingerprint(credential)
	short := fingerprint
	if len(short) > 8 {
		short = short[:8]
	}
	upstreamName := fmt.Sprintf("%s-oauth-%s", provider, short)
	kind, baseURL := b.endpointFor(ctx, provider)

	var credID string
	var uid string
	err := b.st.WithTx(ctx, func(tx *store.Repos) error {
		// 上游行先建且 enabled=false：未通过判定的凭据绝不能可调用。
		var uerr error
		uid, uerr = tx.Models.UpsertUpstream(ctx, kind, upstreamName, baseURL, &owner, false)
		if uerr != nil {
			return uerr
		}
		sid, serr := tx.Secrets.PutSecret(ctx, uid, []byte(credential))
		if serr != nil {
			return serr
		}
		cid, cerr := tx.Models.CreateCredential(ctx, owner, provider, fingerprint,
			provider+" OAuth", fmt.Sprintf("%s", sid))
		if isDuplicateRow(cerr) {
			// 同一外部身份重新授权（供应商可能重发相同 token，MiniMax 实测）：
			// 语义是「换新凭据」而非错误——替换已有行的密文/归属/label，
			// 状态重置回 pending 重新鉴定；废弃的旧密文同步删除。
			var oldRef string
			cid, oldRef, cerr = tx.Models.ReplaceCredential(ctx, owner, provider, fingerprint,
				provider+" OAuth", fmt.Sprintf("%s", sid))
			if cerr == nil && oldRef != "" && oldRef != fmt.Sprintf("%s", sid) {
				_ = tx.Secrets.DeleteSecret(ctx, oldRef)
			}
		}
		if cerr != nil {
			// 建行失败要把刚写的密文收回去，避免留下无主的密钥材料。
			_ = tx.Secrets.DeleteSecret(ctx, sid)
			return cerr
		}
		credID = cid
		return nil
	})
	if err != nil {
		return "", err
	}
	// detail 的形状契约是 JSON（verify_detail 会被鉴定队列当 RawMessage
	// 内联进应答）——纯文本会让整份列表的编码失败（2026-10-28 实测）。
	if err := b.st.Repos().Models.MarkVerified(ctx, credID,
		store.VerifyUnverified,
		`{"stage":"oauth","message":"已获取凭据，等待目录探测"}`); err != nil {
		return credID, err
	}
	// 静态模型目录种子（2026-10-29）：反代型没有 OpenAI 兼容 /models 可探测，
	// 模型卡的数据源是 hub 各家 product 的 fallbackModels（逐字段对齐上游
	// 客户端实测）。失败不阻断凭据落库（目录可自愈，下次刷新/续期再补）。
	b.seedModelCatalog(ctx, provider, uid)
	return credID, nil
}

// seedModelCatalog 把 sidecar models.catalog 的静态目录富写入 real_models
// （DisplayName=展示名、ContextWindow=官方档位最大值——比 SeedCatalog 的
// 纯 id 种子信息量大）。上游行 enabled=false 也不影响：模型行随入池点亮。
func (b *oauthBridge) seedModelCatalog(ctx context.Context, provider, upstreamID string) {
	raw, err := b.sidecar.Call(ctx, "models.catalog", map[string]any{"provider": provider})
	if err != nil {
		return // 目录是可自愈的补充信息，不阻断
	}
	var res struct {
		Models []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"display_name"`
			ContextWindow int64  `json:"context_window"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return
	}
	now := time.Now().UTC().Unix()
	for _, m := range res.Models {
		rm := store.RealModel{
			UpstreamID:      upstreamID,
			UpstreamModelID: m.ID,
			DisplayName:     m.DisplayName,
			Status:          store.RealModelActive,
			Source:          "auto",
			LastSeenAt:      &now,
		}
		if m.ContextWindow > 0 {
			cw := m.ContextWindow
			rm.ContextWindow = &cw
		}
		if _, err := b.st.Repos().Models.UpsertRealModel(ctx, rm); err != nil {
			return // 单条失败即整批停（同一目录连续失败的语义相同）
		}
	}
}

// endpointFor 取该 provider 的（kind, base_url）。
func (b *oauthBridge) endpointFor(ctx context.Context, provider string) (string, string) {
	if list, err := b.list(ctx); err == nil {
		for _, p := range list.Providers {
			if p.ID == provider && p.APIBase != "" {
				kind := p.Kind
				if kind == "" {
					kind = "vendor"
				}
				return kind, p.APIBase
			}
		}
	}
	return defaultEndpoint(provider)
}

// defaultEndpoint 是 sidecar 清单不可用时的兜底。
//
// 只有 codearts 有实测过的端点常量；其余给一个**不可解析**的占位域名——
// 宁缺勿错：写一个看起来对但错的 URL，比写空更难排查。
func defaultEndpoint(provider string) (string, string) {
	if provider == "codearts" {
		return "vendor", "https://snap-access.cn-north-4.myhuaweicloud.com"
	}
	return "vendor", "https://oauth-only.invalid"
}

// sidecarMessage 从 sidecar 错误里取可展示的原文（剥掉错误码前缀）。
func sidecarMessage(err error) string {
	var se *dshprovider.Error
	if errors.As(err, &se) {
		if se.Message != "" {
			return se.Message
		}
	}
	return err.Error()
}

// issueRecoveryFor 生成并落库一次性恢复码（与 API Key 贡献路径同格式）。
func issueRecoveryFor(ctx context.Context, b *contributionBridge, owner string) (string, error) {
	secret := randomToken(18)
	hash, err := auth.NewPasswordHasher(12).Hash(secret)
	if err != nil {
		return "", err
	}
	if err := b.SetRecoveryCodeHash(ctx, owner, hash); err != nil {
		return "", err
	}
	return fmt.Sprintf("frv-%s-%s", owner, secret), nil
}

// newOAuthBridge 构造供应商 OAuth 桥（装配层唯一入口）。
func newOAuthBridge(st *store.Store, sc *dshprovider.Client, sess *auth.SessionManager, contrib *contributionBridge) *oauthBridge {
	return &oauthBridge{
		sidecar: sc,
		states:  newOAuthStateStore(),
		st:      st,
		sess:    sess,
		contrib: contrib,
	}
}

// listenPort 从 server.listen 取端口（供应商回调要打到的端口）。
//
// 为什么要显式传：portal 只认「127.0.0.1:<port>」形态的回调，端口必须等于
// 服务实际监听端口；监听在通配地址时回调仍打到回环（同一台机器）。
func listenPort(listen string) int {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return p
}
