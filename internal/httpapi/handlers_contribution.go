package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/apidir"
	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
)

// handlers_contribution.go —— 贡献者面（docs/ui/05-contributor.md + 02-public §6）。
//
// 端点：
//
//	GET  /api/contributions/providers        可用贡献方式（公开）
//	POST /api/contributions                  API Key 型贡献（公开，未登录自动建号）
//	POST /api/contributions/recovery         恢复码兑换（公开）
//	GET  /api/contributions                  我的贡献概览（会话）
//	GET  /api/contributions/credentials      我的凭据列表（会话）
//	GET  /api/contributions/credentials/{id} 凭据详情（会话，属主校验）
//	GET  /api/contributions/stats            贡献统计（会话，?range=day|month）
//	POST /api/contributions/credentials/{id}/pause   暂停（会话，属主校验）
//	POST /api/contributions/credentials/{id}/resume  恢复（会话，属主校验）
//	GET  /api/contributions/recovery-code    重发恢复码（会话，覆盖旧码）
//
// 提交链路委托 upstream.ContributionService（L0 → 落库 → 探测 → 判定），
// 本文件只做：可选会话解析、未登录自动建 contributor 账户 + 恢复码、
// HTTP 形状与错误映射。恢复码格式 `frv-<accountID>-<secret>`：兑换时先
// 解析 id 再 Verify(hash)，避免「按码全表扫」。
//
// 三条贡献路径（契约 v2 §1）：
//
//	反代供应商  供应商账号登录，凭据由 sidecar 换出，走 /api/contributions/oauth/*；
//	API 供应商  内置端点（internal/apidir）+ 用户粘贴 Key；
//	自定义      任意 OpenAI 兼容端点 + Key（端点由用户填）。
//
// 暂停/恢复只切 upstreams.enabled：凭据与密钥材料都留着，所以它不是撤销
// （撤销会销毁密文，属管理面的动作）。

// ContributionDeps 是贡献面的依赖。
type ContributionDeps struct {
	Submitter ContributionSubmitter
	Store     ContributionStore
	Hasher    *auth.PasswordHasher
	// Sess 用于「带 Cookie 提交」时识别已登录贡献者；nil = 视为未登录。
	Sess *auth.SessionManager
	// OAuth 是反代供应商清单来源（reverse_proxy 分类的 items）。
	// nil = 该分类为空（前端仍能看到 api / custom 两个入口）。
	OAuth ProviderOAuth
	// Pauser 切换贡献凭据的可调用性（暂停 / 恢复）；nil = 端点报未装配。
	Pauser ContributionPauser
}

// ContributionSubmitter 是贡献提交链路的窄接口（装配桥包装 ContributionService）。
type ContributionSubmitter interface {
	Submit(ctx context.Context, sub ContributionSubmission) (ContributionOutcome, error)
}

// ContributionSubmission 是一次提交的入参（形状与 upstream.Submission 对齐）。
type ContributionSubmission struct {
	OwnerAccountID string
	Provider       string
	BaseURL        string
	APIKey         string
	ClaimedModels  []string
	Label          string
	Kind           string
}

// ContributionOutcome 是一次提交的结果。
type ContributionOutcome struct {
	CredentialID string // UUID
	Status       string
	VerifyStatus string
	InPool       bool
	Reused       bool
}

// ContributionStore 是贡献面的存储窄接口（装配桥实现）。
type ContributionStore interface {
	// AccountIDOf 校验账户确实存在（认证面的 id 与库内主键已是同一串 UUID，
	// 这层不再做类型转换，只保留"这个账户还在吗"的存在性检查）。
	AccountIDOf(ctx context.Context, accountID string) (string, error)
	// CreateContributor 创建 contributor 类账户（active、无密码），返回 id。
	CreateContributor(ctx context.Context, displayName string) (string, error)
	// SetRecoveryCodeHash 写恢复码哈希（覆盖旧码）。
	SetRecoveryCodeHash(ctx context.Context, accountID string, hash string) error
	// RecoveryHashOf 读恢复码哈希（无码返回空串）。
	RecoveryHashOf(ctx context.Context, accountID string) (string, error)
	// RecoveryStateOf 读恢复码是否已生成（生成时间列暂无，恒 null）。
	RecoveryStateOf(ctx context.Context, accountID string) (generated bool, err error)
	// CredentialsOf 列出属主的贡献凭据。
	CredentialsOf(ctx context.Context, owner string) ([]ContributionCredentialRow, error)
	// CredentialOf 读属主的一条凭据（非属主 not_found）。
	CredentialOf(ctx context.Context, owner, id string) (*ContributionCredentialRow, error)
	// ContributionUsage 汇总贡献凭据自 since（Unix 秒，0 = 不限）以来的调用量。
	ContributionUsage(ctx context.Context, owner string, since int64) (calls, inTokens, outTokens int64, err error)
}

// ContributionCredentialRow 是一条贡献凭据的展示行。
type ContributionCredentialRow struct {
	ID           string // UUID
	Label        string
	Status       string
	VerifyStatus string
	VerifyDetail string
	CreatedAt    int64
	LastUsedAt   int64
}

// ContributionPauser 切换一条贡献凭据的可调用性（暂停 / 恢复）。
//
// 为什么是独立窄接口而不是塞进 ContributionStore：切换的落点是 upstreams.enabled，
// 那个开关的业务含义归 upstream.ContributionService（与撤销同一个入口），
// 存储仓储只负责写——两个面分开，失败语义才不会被合成一个“可能失败”。
//
// SetPaused 返回操作后的可调用状态（"paused" / "enabled"）；重复调用幂等。
type ContributionPauser interface {
	SetPaused(ctx context.Context, owner, credentialID string, paused bool) (string, error)
}

// 贡献入口的三个分类 id（前端按 id 分 Tab，契约 v2 §1）。
const (
	ContributeCategoryReverseProxy = "reverse_proxy"
	ContributeCategoryAPI          = "api"
	ContributeCategoryCustom       = "custom"
)

// HandleContributeProviders GET /api/contributions/providers（公开）。
//
// 响应是**三个互斥入口**而不是一张平铺清单（契约 v2 §1）：
//
//	reverse_proxy 供应商账号登录，凭据由 sidecar 换出（不接触用户的 Key）
//	api           内置端点 + 用户粘贴 Key（数据来自 internal/apidir）
//	custom        任意 OpenAI 兼容端点（入口是表单，清单恒空）
//
// 为什么不再平铺：平铺把「要不要登录供应商账号」这个决定性差异藏进了 flow 字段，
// 前端只能靠猜来分组；分成三类后，每一类的前端交互是确定的。
//
// reverse_proxy 的不可用项**照列**并带 unavailable_reason：界面上要显示
// 「为什么不能用」而不是凭空少一个入口。字段一律发全（空值给零值），
// 前端的判空逻辑因此只需要一套。
//
// api 组是编译进二进制的本地事实数据：不依赖 sidecar，也不存在「今天不可用」，
// 因此不带 available 字段（前端不必判空）。
func (s *Server) HandleContributeProviders(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reverseProxy := make([]map[string]any, 0, 16)
		if deps != nil && deps.OAuth != nil {
			for _, c := range deps.OAuth.Providers(r.Context()) {
				// flow/needs 决定前端渲染哪种交互（跳转 / 设备码 / 二维码 / 手机号）。
				reverseProxy = append(reverseProxy, map[string]any{
					"id":                 c.ID,
					"name":               c.Name,
					"flow":               c.Flow,
					"needs":              nonNilStrings(c.Needs),
					"available":          c.Available,
					"unavailable_reason": c.UnavailableReason,
					"unavailable_kind":   c.UnavailableKind,
					"suggested_endpoint": c.SuggestedEndpoint,
					"steps":              nonNilStrings(c.Steps),
				})
			}
		}
		categories := []map[string]any{
			{
				"id":          ContributeCategoryReverseProxy,
				"name":        "反代供应商",
				"description": "用供应商账号登录，由 sidecar 换出凭据（不接触你的 Key）",
				"items":       reverseProxy,
			},
			{
				"id":          ContributeCategoryAPI,
				"name":        "API 供应商",
				"description": "内置端点，只需粘贴 Key",
				"items":       apidir.List(),
			},
			{
				"id":          ContributeCategoryCustom,
				"name":        "自定义供应商",
				"description": "任意 OpenAI 兼容端点（自建网关、局域网服务、其它中转站）",
				// 这一类没有清单：入口就是一张表单，空数组让前端只走一种分支。
				"items": []any{},
			},
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"categories": categories},
		})
	}
}

// nonNilStrings 把 nil 切片换成空切片。
//
// JSON 里 null 与 [] 对前端是两种分支，而契约里「没有内容」一律是空数组：
// 把 nil 漏出去会逼前端同时处理两种形态。
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// HandleContributeSubmit POST /api/contributions（公开；未登录自动建号）。
func (s *Server) HandleContributeSubmit(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Submitter == nil || deps.Store == nil || deps.Hasher == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "贡献服务未装配", nil)
			return
		}
		var body struct {
			// kind/provider 是契约 v2 新增的两项：kind=api 时端点由目录解析，
			// kind=custom（或缺失）时端点由请求体提供。
			Kind          string   `json:"kind"`
			Provider      string   `json:"provider"`
			Endpoint      string   `json:"endpoint"`
			APIKey        string   `json:"api_key"`
			Label         string   `json:"label"`
			ClaimedModels []string `json:"claimed_models"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		apiKey := strings.TrimSpace(body.APIKey)
		endpoint, providerID, ok := resolveSubmissionTarget(w, body.Kind, body.Provider, body.Endpoint)
		if !ok {
			return
		}
		if apiKey == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "api_key 必填", nil)
			return
		}
		// 声称模型全量收下（去空；判定层的归一见 judgeProbe）。此前只取第一个是
		// Submission 单模型时代的残留——2026-10-25 起支持多选。
		claimed := make([]string, 0, len(body.ClaimedModels))
		for _, m := range body.ClaimedModels {
			if m = strings.TrimSpace(m); m != "" {
				claimed = append(claimed, m)
			}
		}

		// 可选会话：带 Cookie 且有效 → 用当前账户；否则自动建 contributor 账户。
		owner, accountCreated, err := s.contributeOwner(r, deps, body.Label)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "创建贡献账户失败", nil)
			return
		}

		var recoveryCode string
		if accountCreated {
			recoveryCode, err = issueRecoveryCode(r.Context(), deps, owner)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "生成恢复码失败", nil)
				return
			}
		}

		outcome, err := deps.Submitter.Submit(r.Context(), ContributionSubmission{
			OwnerAccountID: owner,
			Provider:       providerID,
			BaseURL:        endpoint,
			APIKey:         apiKey,
			ClaimedModels:  claimed,
			Label:          strings.TrimSpace(body.Label),
		})
		if err != nil {
			// 提交链路的错误是「可执行原因」（结构性垃圾/端点不合法），
			// 不是内部故障——400 invalid_request 而不是 500。
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}

		message := "凭据已记录，进入鉴定队列"
		switch {
		case outcome.InPool:
			message = "凭据校验通过，已进入候选池"
		case outcome.Reused:
			message = "该凭据此前已提交过（幂等），未重复入库"
		}
		resp := map[string]any{
			"accepted":        outcome.InPool,
			"status":          outcome.Status,
			"verify_status":   outcome.VerifyStatus,
			"reused":          outcome.Reused,
			"account_created": accountCreated,
			"message":         message,
		}
		if recoveryCode != "" {
			resp["recovery_code"] = recoveryCode
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": resp})
	}
}

// resolveSubmissionTarget 把请求体的 (kind, provider, endpoint) 翻成落库用的
// (base_url, provider 列值)。校验失败时直接写出应答并返回 ok=false。
//
// 三种形态（契约 v2 §3）：
//
//	kind=api     endpoint **由后端从目录解析**，请求体里的 endpoint 一律忽略——
//	             让前端能提交端点等于把「目录是权威」变成一个可绕过的建议；
//	kind=custom  endpoint 必填（校验沿用 upstream.ValidateTarget）；provider 列存 "custom";
//	其它取值     400：宁可显式拒绝，也不要按 custom 静默落一条形状不明的凭据。
//
// provider 列的两种取值就是契约 §4 的存储口径（不新增表）：api:<catalogID> / custom。
// 注意这里的 kind 是**贡献类型**，与 upstream.Submission.Kind（vendor/selfhosted 上游
// 类型，由 BaseURL 自动判定）是两回事，不要互相传递。
func resolveSubmissionTarget(w http.ResponseWriter, kind, providerID, endpoint string) (string, string, bool) {
	switch strings.TrimSpace(kind) {
	case "", "custom":
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "endpoint 与 api_key 必填", nil)
			return "", "", false
		}
		return endpoint, "custom", true
	case "api":
		pid := strings.TrimSpace(providerID)
		p, found := apidir.Get(pid)
		if !found {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"未知的 API 供应商："+pid, nil)
			return "", "", false
		}
		// provider 存 "api:<catalogID>"：同一家的 key 共享幂等键空间，而 base_url
		// 仍逐条落库——目录将来改端点不会改变历史行的语义。
		return p.BaseURL, "api:" + pid, true
	default:
		writeError(w, http.StatusBadRequest, "invalid_request",
			"未知的贡献类型："+strings.TrimSpace(kind)+"（仅支持 api / custom）", nil)
		return "", "", false
	}
}

// contributeOwner 解析可选会话 → 贡献属主。未登录时创建 contributor 账户。
func (s *Server) contributeOwner(r *http.Request, deps *ContributionDeps, label string) (string, bool, error) {
	if deps.Sess != nil {
		if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
			if p, verr := deps.Sess.Validate(r.Context(), c.Value, ""); verr == nil {
				id, err := deps.Store.AccountIDOf(r.Context(), p.AccountID)
				return id, false, err
			}
		}
	}
	displayName := strings.TrimSpace(label)
	if displayName == "" {
		displayName = "贡献者"
	}
	id, err := deps.Store.CreateContributor(r.Context(), displayName)
	return id, err == nil, err
}

// issueRecoveryCode 生成并落库一条新恢复码（覆盖旧码）。
// 格式 `frv_<accountID>_<secret>`；secret 用 argon2id 哈希落库。
//
// 为什么分隔符从 `-` 改成 `_`（2026-10-26 UUID 改造）：accountID 现在是 UUID
// 文本，自身含 4 个 `-`，用 `-` 分隔会让 SplitN(code,"-",3) 把 UUID 拆成两截，
// 兑换必然失败。`_` 不在 UUID 字符集里，是唯一安全的分隔符。
func issueRecoveryCode(ctx context.Context, deps *ContributionDeps, accountID string) (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	hash, err := deps.Hasher.Hash(secret)
	if err != nil {
		return "", err
	}
	if err := deps.Store.SetRecoveryCodeHash(ctx, accountID, hash); err != nil {
		return "", err
	}
	return "frv_" + accountID + "_" + secret, nil
}

// HandleContributeRecovery POST /api/contributions/recovery（公开）。
//
// 防枚举：无论码错、账户没了还是哈希对不上，对外都是同一个 accepted:false。
func (s *Server) HandleContributeRecovery(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Store == nil || deps.Hasher == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "贡献服务未装配", nil)
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		if !s.recoveryValid(r.Context(), deps, strings.TrimSpace(body.Code)) {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"accepted": false}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"accepted": true}})
	}
}

// recoveryValid 解析并校验恢复码。
func (s *Server) recoveryValid(ctx context.Context, deps *ContributionDeps, code string) bool {
	id, secret, ok := parseRecoveryCode(code)
	if !ok {
		return false
	}
	hash, err := deps.Store.RecoveryHashOf(ctx, id)
	if err != nil || hash == "" {
		return false
	}
	okVerify, _, _ := deps.Hasher.Verify(hash, secret)
	return okVerify
}

// parseRecoveryCode 拆 `frv_<id>_<secret>`。
//
// 分隔符是 `_` 而不是 `-`：id 是 UUID v4 文本，自带 4 个 `-`（见 issueRecoveryCode
// 的注释）。拆三段的写法（SplitN(...,3)）保证 secret 里即使出现 `_` 也不受影响——
// secret 是 base64url，不含 `_`，但这条保证不依赖那个事实。
//
// 形状非法就返回 ok=false，**不做「看起来像 UUID 才继续」的宽松解析**：
// 恢复码的失败面必须与「码错」同形（防枚举，见 HandleContributeRecovery），
// 多一个分支就多一处可区分的时序。
func parseRecoveryCode(code string) (string, string, bool) {
	parts := strings.SplitN(code, "_", 3)
	if len(parts) != 3 || parts[0] != "frv" {
		return "", "", false
	}
	id, secret := parts[1], parts[2]
	if !idgen.Valid(id) || secret == "" {
		return "", "", false
	}
	return id, secret, true
}

// HandleMyContributions GET /api/contributions（会话）。
// 与 credentials 列表同数据：贡献概览页直接复用行数据。
func (s *Server) HandleMyContributions(deps *ContributionDeps) http.HandlerFunc {
	return s.handleMyCredentials(deps)
}

// handleMyCredentials 渲染属主凭据列表（credentials 与概览共用）。
func (s *Server) handleMyCredentials(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := requireContributor(w, r, deps)
		if !ok {
			return
		}
		rows, err := deps.Store.CredentialsOf(r.Context(), owner)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取贡献凭据失败", nil)
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, renderContributionCredential(row))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nil})
	}
}

// HandleMyCredentials GET /api/contributions/credentials（会话）。
func (s *Server) HandleMyCredentials(deps *ContributionDeps) http.HandlerFunc {
	return s.handleMyCredentials(deps)
}

// HandleMyCredentialDetail GET /api/contributions/credentials/{id}（会话）。
func (s *Server) HandleMyCredentialDetail(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := requireContributor(w, r, deps)
		if !ok {
			return
		}
		id, err := idgen.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
			return
		}
		row, err := deps.Store.CredentialOf(r.Context(), owner, id)
		if err != nil {
			// 非属主/不存在同形 404：不确认他人凭据的存在。
			writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
			return
		}
		data := renderContributionCredential(*row)
		if row.VerifyDetail != "" {
			var detail any
			if json.Unmarshal([]byte(row.VerifyDetail), &detail) == nil {
				data["stage_results"] = detail
			} else {
				// 历史行可能存的是纯文本证据：原样放进 evidence 字段。
				data["evidence_text"] = row.VerifyDetail
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// HandleContributionCredentialPause POST /api/contributions/credentials/{id}/pause（会话）。
//
// 暂停 = 把该凭据对应的 upstream 行 enabled 置 false。它与撤销共用同一个开关，
// 但**不动密钥材料**：暂停是「先别用我的 key」，不是「收回我的 key」——
// 区别在恢复时能不能真的恢复。
func (s *Server) HandleContributionCredentialPause(deps *ContributionDeps) http.HandlerFunc {
	return s.handleCredentialPause(deps, true)
}

// HandleContributionCredentialResume POST .../resume（会话）。
//
// 恢复 = 把 upstream 行 enabled 置回 true。只有通过鉴定的凭据（status=active）
// 才恢复得起来：未通过鉴定的凭据 enabled=false 是**判定结果**，不是暂停造成的，
// 打开它等于把一把没验过的 key 放进候选池——此时应答仍是 200，但 status
// 保持 "paused"（幂等，不报错，也不假装成功）。
func (s *Server) HandleContributionCredentialResume(deps *ContributionDeps) http.HandlerFunc {
	return s.handleCredentialPause(deps, false)
}

// handleCredentialPause 是两个幂等操作的共同实现（重复调用状态不变）。
func (s *Server) handleCredentialPause(deps *ContributionDeps, paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := requireContributor(w, r, deps)
		if !ok {
			return
		}
		if deps.Pauser == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "暂停/恢复服务未装配", nil)
			return
		}
		id, err := idgen.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
			return
		}
		// 属主校验走既有 CredentialOf 口径：非属主与不存在同形 404，
		// 不确认他人凭据的存在（与详情端点一致）。
		if _, err := deps.Store.CredentialOf(r.Context(), owner, id); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
			return
		}
		status, err := deps.Pauser.SetPaused(r.Context(), owner, id, paused)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "切换凭据状态失败", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"ok": true, "status": status,
		}})
	}
}

// HandleContributionStats GET /api/contributions/stats（会话，?range=day|month）。
//
// range 只影响**调用量三项**（call_count / input_tokens / output_tokens）：凭据数与
// 入池数是当前快照，不是区间统计——把它们也按区间算会让「我有几条凭据」变成
// 「我本周期新建了几条」，那是另一个问题（也是另一个端点）。
func (s *Server) HandleContributionStats(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := requireContributor(w, r, deps)
		if !ok {
			return
		}
		since, ok := contributionStatsSince(w, r.URL.Query().Get("range"), time.Now())
		if !ok {
			return
		}
		rows, err := deps.Store.CredentialsOf(r.Context(), owner)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取贡献统计失败", nil)
			return
		}
		calls, inTok, outTok, err := deps.Store.ContributionUsage(r.Context(), owner, since)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取贡献统计失败", nil)
			return
		}
		var inPool, claimed int64
		for _, row := range rows {
			if row.Status == "active" {
				inPool++
			}
			claimed++
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"credential_count":    len(rows),
			"in_pool_count":       inPool,
			"claimed_model_count": claimed,
			"call_count":          calls,
			"input_tokens":        inTok,
			"output_tokens":       outTok,
		}})
	}
}

// contributionStatsSince 把 range 参数翻成「统计起点」（Unix 秒）。
//
// 日界一律 UTC：usage_daily.day 与请求时间戳的日界都是 UTC（05-storage 第 9 节的
// 既定口径），这里换成本地时区会让统计与用量表对不上账（北京时间凌晨 0-8 点
// 属于同一天的两个 UTC 日，两个页面的数字会差一整天）。
//
// 非法值写 400 并把字段名放进 param：前端要能定位到是哪个请求参数错了。
func contributionStatsSince(w http.ResponseWriter, raw string, now time.Time) (int64, bool) {
	utc := now.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	switch strings.TrimSpace(raw) {
	case "", "day":
		return day.Unix(), true
	case "month":
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC).Unix(), true
	default:
		writeErrorParam(w, http.StatusBadRequest, "invalid_request", "range",
			"range 只支持 day / month")
		return 0, false
	}
}

// HandleRecoveryCodeState GET /api/contributions/recovery-code（会话）。
//
// 只返回「是否已生成」的状态，不返回码本身——恢复码是仅展示一次的秘密，
// 状态页重新打开不能重新看到它（否则一次性展示形同虚设）。
func (s *Server) HandleRecoveryCodeState(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := requireContributor(w, r, deps)
		if !ok {
			return
		}
		generated, err := deps.Store.RecoveryStateOf(r.Context(), owner)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取恢复码状态失败", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"generated":    generated,
			"generated_at": nil, // 恢复码时间列暂无；宁缺勿错
			"last_used_at": nil,
		}})
	}
}

// HandleRecoveryCodeRegenerate POST /api/contributions/recovery-code/regenerate（会话）。
//
// 「仅展示一次」的实现：每次调用都生成**新**码并覆盖旧码——旧码立即失效，
// 响应体里的码是当前唯一有效的一次性展示。
func (s *Server) HandleRecoveryCodeRegenerate(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := requireContributor(w, r, deps)
		if !ok {
			return
		}
		code, err := issueRecoveryCode(r.Context(), deps, owner)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "生成恢复码失败", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"code": code}})
	}
}

// requireContributor 是贡献者面的公共前置（会话 + 装配依赖）。
func requireContributor(w http.ResponseWriter, r *http.Request, deps *ContributionDeps) (string, bool) {
	if deps == nil || deps.Store == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "贡献服务未装配", nil)
		return "", false
	}
	p := SessionPrincipalFromContext(r.Context())
	if p == nil {
		writeError(w, http.StatusUnauthorized, "session_expired",
			"会话不存在或已过期，请重新登录", nil)
		return "", false
	}
	id, err := deps.Store.AccountIDOf(r.Context(), p.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "读取账户失败", nil)
		return "", false
	}
	return id, true
}

// renderContributionCredential 渲染前端 ContributionCredential 行。
// kind 恒 api_key（凭据表没有 kind 列，OAuth 与 API Key 贡献的行同形）；
// claimed_model 无列，给空串。改这两个值要同步前端契约。
func renderContributionCredential(row ContributionCredentialRow) map[string]any {
	return map[string]any{
		"id":             row.ID,
		"external_label": row.Label,
		"kind":           "api_key",
		"claimed_model":  "",
		"status":         row.Status,
		"verdict":        row.VerifyStatus,
		"stage":          nil,
		"created_at":     unixRFC3339(row.CreatedAt),
		"last_called_at": unixRFC3339(row.LastUsedAt),
		"in_pool":        row.Status == "active",
	}
}
