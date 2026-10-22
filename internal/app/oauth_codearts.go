package app

// oauth_codearts.go —— 供应商 OAuth 贡献（CodeArts）的主进程侧装配。
//
// 链路（docs/contributions/01-flow.md §2 + PLAN §5.5）：
//
//	POST /api/contributions/oauth/{provider}          → 生成授权 URL（sidecar 出 PKCE/DPoP）
//	GET  /oauth/callback?code=&state=                 → 收 code → sidecar 换取 → 凭据入库
//
// 为什么换取必须在 sidecar 里做：PLAN §5.5 定案「17 家供应商的 OAuth 与静默续期
// 不可能重写」，实现已按提取计划移植进 sidecar/providers/codearts/。主进程只做
// 编排与落库，不持有任何供应商协议细节。
//
// 状态（PKCE verifier + DPoP 私钥）必须跨请求保存且**不能落在浏览器可见处**：
// 这里用进程内一次性表（10 分钟 TTL，take 即删）。为什么不进 Cookie：那等于把
// 换取凭据所需的秘密交给客户端；不进库：它是短命的中间态，落库只增加泄漏面。

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

// oauthStateTTL 是一次授权会话的寿命（与 sidecar 侧 assertState 的 10 分钟同源）。
const oauthStateTTL = 10 * time.Minute

// oauthStateStore 是待完成授权的进程内一次性表。
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
}

var _ httpapi.ProviderOAuth = (*oauthBridge)(nil)

// Providers 返回供应商清单：可用性 = sidecar 在跑 + 方法已移植。
//
// 未移植的 provider 不列出来（而不是列一个必然失败的入口）：界面上「一次都
// 点不通的按钮」比「没有这个按钮」更糟。当前只移植了 codearts。
func (b *oauthBridge) Providers(ctx context.Context) []httpapi.ProviderChoice {
	avail, reason := b.availability()
	return []httpapi.ProviderChoice{{
		ID:                "codearts",
		Name:              "华为云 CodeArts",
		Available:         avail,
		UnavailableReason: reason,
	}}
}

func (b *oauthBridge) availability() (bool, string) {
	if b.sidecar == nil {
		return false, "sidecar 未运行：供应商 OAuth 需要 Node sidecar 出 PKCE/DPoP"
	}
	st := b.sidecar.Status()
	if !st.Available {
		return false, "sidecar 不可用：" + st.Reason
	}
	return true, ""
}

// Authorize 生成授权 URL 并登记 state。
func (b *oauthBridge) Authorize(ctx context.Context, provider string, port int) (string, error) {
	if provider != "codearts" {
		return "", fmt.Errorf("供应商 %q 的 OAuth 未移植", provider)
	}
	if ok, reason := b.availability(); !ok {
		return "", fmt.Errorf("%s", reason)
	}
	raw, err := b.sidecar.Call(ctx, "oauth.authorize", map[string]any{
		"provider": provider,
		"port":     port,
	})
	if err != nil {
		return "", err
	}
	var res struct {
		AuthorizeURL string `json:"authorize_url"`
		State        string `json:"state"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("解析授权应答失败: %w", err)
	}
	if res.AuthorizeURL == "" {
		return "", fmt.Errorf("sidecar 未返回 authorize_url")
	}
	nonce := b.states.put(oauthState{provider: provider, sidecarState: res.State})
	// 把 nonce 拼进 portal 会原样回传的 state 参数。
	sep := "&"
	if !hasQuery(res.AuthorizeURL) {
		sep = "?"
	}
	return res.AuthorizeURL + sep + "state=" + nonce, nil
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

// hasQuery 判断 URL 里是否已有查询串。
func hasQuery(u string) bool {
	return len(u) > 0 && indexByte(u, '?') >= 0
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// Callback 用 code 换取凭据并落库。
//
// 归属账户：浏览器带有效会话 → 当前账户；否则建 contributor 容器账户
// （01-flow 2.2：匿名贡献也先建账户，恢复码是唯一回站路径）。
func (b *oauthBridge) Callback(ctx context.Context, provider, code, nonce, sessionToken string) (*httpapi.OAuthOutcome, error) {
	if provider != "codearts" {
		return nil, fmt.Errorf("供应商 %q 的 OAuth 未移植", provider)
	}
	st, ok := b.states.take(nonce)
	if !ok {
		return nil, fmt.Errorf("授权会话不存在或已过期，请重新发起授权")
	}
	if st.provider != provider {
		return nil, fmt.Errorf("state 与 provider 不匹配")
	}
	if ok, reason := b.availability(); !ok {
		return nil, fmt.Errorf("%s", reason)
	}
	raw, err := b.sidecar.Call(ctx, "oauth.exchange", map[string]any{
		"provider": provider,
		"code":     code,
		"state":    st.sidecarState,
	})
	if err != nil {
		// 换取的失败原因是给用户看的（授权码过期/已被用过/回调地址不匹配），
		// 把 sidecar 错误码那层壳剥掉再上抛，界面才不用解释 "E_OAUTH_EXCHANGE_FAILED"。
		return nil, fmt.Errorf("换取失败：%s", sidecarMessage(err))
	}
	var ex struct {
		Credential string `json:"credential"`
		ExpiresAt  string `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &ex); err != nil {
		return nil, fmt.Errorf("解析换取应答失败: %w", err)
	}
	if ex.Credential == "" {
		return nil, fmt.Errorf("sidecar 未返回凭据")
	}

	var owner int64
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

	credID, err := b.persist(ctx, owner, provider, ex.Credential)
	if err != nil {
		return nil, err
	}
	res := &httpapi.OAuthOutcome{
		// OAuth 凭据已拿到但尚未通过目录探测：按「待鉴定」记录，不入池。
		// 这是刻意的保守：01-flow 5.2「落库与可调用分开」——没验过的凭据不能进候选池。
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
	_ = credID
	return res, nil
}

// sessionAccount 解析会话 Cookie → 账户 id。无效/过期一律当作未登录。
func (b *oauthBridge) sessionAccount(ctx context.Context, token string) (int64, bool) {
	if b.sess == nil || token == "" {
		return 0, false
	}
	p, err := b.sess.Validate(ctx, token, "")
	if err != nil {
		return 0, false
	}
	id, err := b.contrib.AccountIDOf(ctx, p.AccountID)
	if err != nil {
		return 0, false
	}
	return id, true
}

// persist 把 OAuth 凭据写入存储：上游行（enabled=false）+ AES-GCM 密文 + 凭据行。
//
// 为什么上游行先建且关闭：未通过判定的凭据绝不能可调用（01-flow 5.2）；
// 判定通过后由鉴定流水线打开（本期 OAuth 路径先停在 pending）。
func (b *oauthBridge) persist(ctx context.Context, owner int64, provider, credential string) (int64, error) {
	fingerprint := upstream.KeyFingerprint(credential)
	short := fingerprint
	if len(short) > 8 {
		short = short[:8]
	}
	upstreamName := fmt.Sprintf("%s-oauth-%s", provider, short)
	// CodeArts 的推理与目录端点同域（snap-access，01-catalog 的 vendor 形态）；
	// 走 https 公网，不是回环 selfhosted。
	baseURL := "https://snap-access.cn-north-4.myhuaweicloud.com"

	var credID int64
	err := b.st.WithTx(ctx, func(tx *store.Repos) error {
		uid, err := tx.Models.UpsertUpstream(ctx, "vendor", upstreamName, baseURL, &owner, false)
		if err != nil {
			return err
		}
		sid, err := tx.Secrets.PutSecret(ctx, uid, []byte(credential))
		if err != nil {
			return err
		}
		cid, err := tx.Models.CreateCredential(ctx, owner, provider, fingerprint,
			provider+" OAuth", fmt.Sprintf("%d", sid))
		if err != nil {
			// 建行失败要把刚写的密文收回去，避免留下无主的密钥材料。
			_ = tx.Secrets.DeleteSecret(ctx, sid)
			return err
		}
		credID = cid
		return nil
	})
	if err != nil {
		return 0, err
	}
	// 判定证据留痕：拿到凭据但尚未探测（本期 OAuth 路径的真实状态）。
	if err := b.st.Repos().Models.MarkVerified(ctx, credID,
		store.VerifyUnverified, "oauth: 已获取凭据，等待目录探测"); err != nil {
		return credID, err
	}
	return credID, nil
}

// issueRecoveryFor 生成并落库一次性恢复码（与 API Key 贡献路径同格式）。
func issueRecoveryFor(ctx context.Context, b *contributionBridge, owner int64) (string, error) {
	secret := randomToken(18)
	hash, err := auth.NewPasswordHasher(12).Hash(secret)
	if err != nil {
		return "", err
	}
	if err := b.SetRecoveryCodeHash(ctx, owner, hash); err != nil {
		return "", err
	}
	return fmt.Sprintf("frv-%d-%s", owner, secret), nil
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
