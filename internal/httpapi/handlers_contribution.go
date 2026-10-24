package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
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
//	GET  /api/contributions/stats            贡献统计（会话）
//	GET  /api/contributions/recovery-code    重发恢复码（会话，覆盖旧码）
//
// 提交链路委托 upstream.ContributionService（L0 → 落库 → 探测 → 判定），
// 本文件只做：可选会话解析、未登录自动建 contributor 账户 + 恢复码、
// HTTP 形状与错误映射。恢复码格式 `frv-<accountID>-<secret>`：兑换时先
// 解析 id 再 Verify(hash)，避免「按码全表扫」。
//
// OAuth 型贡献（供应商授权）本版未接：providers 只列 API Key 型，
// OAuth 区块由前端按 available=false 折叠并给原因。

// ContributionDeps 是贡献面的依赖。
type ContributionDeps struct {
	Submitter ContributionSubmitter
	Store     ContributionStore
	Hasher    *auth.PasswordHasher
	// Sess 用于「带 Cookie 提交」时识别已登录贡献者；nil = 视为未登录。
	Sess *auth.SessionManager
	// OAuth 是供应商 OAuth 列表来源（Providers）。nil = 只列 API Key 型。
	OAuth ProviderOAuth
}

// ContributionSubmitter 是贡献提交链路的窄接口（装配桥包装 ContributionService）。
type ContributionSubmitter interface {
	Submit(ctx context.Context, sub ContributionSubmission) (ContributionOutcome, error)
}

// ContributionSubmission 是一次提交的入参（形状与 upstream.Submission 对齐）。
type ContributionSubmission struct {
	OwnerAccountID int64
	Provider       string
	BaseURL        string
	APIKey         string
	ClaimedModel   string
	Label          string
	Kind           string
}

// ContributionOutcome 是一次提交的结果。
type ContributionOutcome struct {
	CredentialID int64
	Status       string
	VerifyStatus string
	InPool       bool
	Reused       bool
}

// ContributionStore 是贡献面的存储窄接口（装配桥实现）。
type ContributionStore interface {
	// AccountIDOf 把认证面账户 id（字符串）转成贡献链路的 int64。
	AccountIDOf(ctx context.Context, accountID string) (int64, error)
	// CreateContributor 创建 contributor 类账户（active、无密码），返回 id。
	CreateContributor(ctx context.Context, displayName string) (int64, error)
	// SetRecoveryCodeHash 写恢复码哈希（覆盖旧码）。
	SetRecoveryCodeHash(ctx context.Context, accountID int64, hash string) error
	// RecoveryHashOf 读恢复码哈希（无码返回空串）。
	RecoveryHashOf(ctx context.Context, accountID int64) (string, error)
	// RecoveryStateOf 读恢复码是否已生成（生成时间列暂无，恒 null）。
	RecoveryStateOf(ctx context.Context, accountID int64) (generated bool, err error)
	// CredentialsOf 列出属主的贡献凭据。
	CredentialsOf(ctx context.Context, owner int64) ([]ContributionCredentialRow, error)
	// CredentialOf 读属主的一条凭据（非属主 not_found）。
	CredentialOf(ctx context.Context, owner, id int64) (*ContributionCredentialRow, error)
	// ContributionUsage 汇总贡献凭据的调用量。
	ContributionUsage(ctx context.Context, owner int64) (calls, inTokens, outTokens int64, err error)
}

// ContributionCredentialRow 是一条贡献凭据的展示行。
type ContributionCredentialRow struct {
	ID           int64
	Label        string
	Status       string
	VerifyStatus string
	VerifyDetail string
	CreatedAt    int64
	LastUsedAt   int64
}

// HandleContributeProviders GET /api/contributions/providers（公开）。
//
// 列表 = 已接入的供应商 OAuth（可用性由 sidecar 与移植状态决定）+ API Key 型
// （恒可用：它只需要一个 OpenAI 兼容端点）。不可用的供应商**照列**并带
// unavailable_reason：界面上要显示「为什么不能用」而不是凭空少一个入口。
func (s *Server) HandleContributeProviders(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows := make([]map[string]any, 0, 4)
		if deps != nil && deps.OAuth != nil {
			for _, c := range deps.OAuth.Providers(r.Context()) {
				row := map[string]any{
					"id":        c.ID,
					"name":      c.Name,
					"available": c.Available,
				}
				// flow/needs 决定前端渲染哪种交互（跳转 / 设备码 / 二维码 / 手机号）；
				// 不发的话前端只能一律当跳转处理，session 形态就永远出不来。
				if c.Flow != "" {
					row["flow"] = c.Flow
				}
				if len(c.Needs) > 0 {
					row["needs"] = c.Needs
				}
				if !c.Available && c.UnavailableReason != "" {
					row["unavailable_reason"] = c.UnavailableReason
				}
				// 两类不可用对运维的含义完全不同（配了就能用 vs 永远不支持），
				// 前端要据此给不同措辞，所以必须把它发出去。
				if !c.Available && c.UnavailableKind != "" {
					row["unavailable_kind"] = c.UnavailableKind
				}
				rows = append(rows, row)
			}
		}
		// 「API Key 型」不是一个 OAuth 供应商，而是一条**并列的贡献路径**。
		// 这里带上 flow=api_key，前端据此把它渲染成说明而不是跳转链接——
		// 之前它 available=true 又不带 flow，前端会生成 /contribute/oauth/api-key
		// 这个必然 404 的死链（本页已有 API Key 表单，不需要再跳一次）。
		rows = append(rows, map[string]any{
			"id":        "api-key",
			"name":      "OpenAI 兼容端点（API Key）",
			"available": true,
			"flow":      "api_key",
		})
		writeJSON(w, http.StatusOK, map[string]any{"data": rows})
	}
}

// HandleContributeSubmit POST /api/contributions（公开；未登录自动建号）。
func (s *Server) HandleContributeSubmit(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Submitter == nil || deps.Store == nil || deps.Hasher == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "贡献服务未装配", nil)
			return
		}
		var body struct {
			Endpoint      string   `json:"endpoint"`
			APIKey        string   `json:"api_key"`
			Label         string   `json:"label"`
			ClaimedModels []string `json:"claimed_models"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		endpoint := strings.TrimSpace(body.Endpoint)
		apiKey := strings.TrimSpace(body.APIKey)
		if endpoint == "" || apiKey == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "endpoint 与 api_key 必填", nil)
			return
		}
		claimed := ""
		for _, m := range body.ClaimedModels {
			if m = strings.TrimSpace(m); m != "" {
				claimed = m
				break // Submission 只支持单个声称模型；多填取第一个（界面提示）
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
			Provider:       "api-key",
			BaseURL:        endpoint,
			APIKey:         apiKey,
			ClaimedModel:   claimed,
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

// contributeOwner 解析可选会话 → 贡献属主。未登录时创建 contributor 账户。
func (s *Server) contributeOwner(r *http.Request, deps *ContributionDeps, label string) (int64, bool, error) {
	if deps.Sess != nil {
		if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
			if p, verr := deps.Sess.Validate(r.Context(), c.Value, ""); verr == nil {
				id, aerr := deps.Store.AccountIDOf(r.Context(), p.AccountID)
				return id, false, aerr
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
// 格式 `frv-<accountID>-<secret>`；secret 用 argon2id 哈希落库。
func issueRecoveryCode(ctx context.Context, deps *ContributionDeps, accountID int64) (string, error) {
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
	return "frv-" + strconv.FormatInt(accountID, 10) + "-" + secret, nil
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

// parseRecoveryCode 拆 `frv-<id>-<secret>`。
func parseRecoveryCode(code string) (int64, string, bool) {
	parts := strings.SplitN(code, "-", 3)
	if len(parts) != 3 || parts[0] != "frv" {
		return 0, "", false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 || parts[2] == "" {
		return 0, "", false
	}
	return id, parts[2], true
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
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
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

// HandleContributionStats GET /api/contributions/stats（会话）。
func (s *Server) HandleContributionStats(deps *ContributionDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := requireContributor(w, r, deps)
		if !ok {
			return
		}
		rows, err := deps.Store.CredentialsOf(r.Context(), owner)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取贡献统计失败", nil)
			return
		}
		calls, inTok, outTok, err := deps.Store.ContributionUsage(r.Context(), owner)
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
func requireContributor(w http.ResponseWriter, r *http.Request, deps *ContributionDeps) (int64, bool) {
	if deps == nil || deps.Store == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "贡献服务未装配", nil)
		return 0, false
	}
	p := SessionPrincipalFromContext(r.Context())
	if p == nil {
		writeError(w, http.StatusUnauthorized, "session_expired",
			"会话不存在或已过期，请重新登录", nil)
		return 0, false
	}
	id, err := deps.Store.AccountIDOf(r.Context(), p.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "读取账户失败", nil)
		return 0, false
	}
	return id, true
}

// renderContributionCredential 渲染前端 ContributionCredential 行。
// kind 恒 api_key（OAuth 型贡献本版未接）；claimed_model 无列，给空串。
func renderContributionCredential(row ContributionCredentialRow) map[string]any {
	return map[string]any{
		"id":             strconv.FormatInt(row.ID, 10),
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
