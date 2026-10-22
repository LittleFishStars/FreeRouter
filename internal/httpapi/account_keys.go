package httpapi

// account_keys.go —— 租户自助的 API Key 生命周期（docs/ui/04-tenant.md「我的 API Key」）。
//
// 为什么必须由会话面提供：Key 是"人给自己签的机器凭据"，签发动作需要
// 人的会话（mwSession），而调用模型用签出来的 Key（mwKeyAuth）。两条
// 凭据链在 L1 就分开，不能混用——用 Key 去签 Key 等于让泄露的 Key 能
// 无限自我续命。
//
// 归属与数量上限全部由 auth.KeyManager 判定（tenant 类、每账户上限、
// 白名单只能收窄）；本文件只做 HTTP 形状（解码、错误映射、明文只回一次）。
//
// 响应形状对齐前端 web/src/api/types.ts 的 ApiKey/ApiKeyCreated：
//   {id, name, prefix, status, expires_at, allowlist: [模型名],
//    last_used_at, created_at}
// last_used_at/created_at 当前列不存在，恒为 null——前端类型可空，
// 不谎造数据；列落地后这里无需再改前端。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// AccountKeysDeps 是租户 Key 面需要的依赖。
type AccountKeysDeps struct {
	Manager *auth.KeyManager
	// AllowlistOf 读一把 Key 的白名单（抽象模型名）。nil = 恒空数组。
	AllowlistOf func(ctx context.Context, keyID string) ([]string, error)
	// SetKeyExpiry 写/清过期时间（PATCH expires_at 用）。nil = 不支持（PATCH 里忽略）。
	SetKeyExpiry func(ctx context.Context, keyID string, expiresAt *time.Time) error
}

// Subject 从上下文取当前会话主体并转成 auth.Subject。
func subjectFromRequest(r *http.Request) (auth.Subject, bool) {
	p := SessionPrincipalFromContext(r.Context())
	if p == nil {
		return auth.Subject{}, false
	}
	return auth.Subject{AccountID: p.AccountID, ActingClass: auth.ClassTenant}, true
}

// renderKey 把 auth.APIKey 渲染成前端 ApiKey 形状。
func (s *Server) renderKey(ctx context.Context, deps *AccountKeysDeps, k auth.APIKey) map[string]any {
	allowlist := []string{}
	if deps != nil && deps.AllowlistOf != nil {
		if names, err := deps.AllowlistOf(ctx, k.ID); err == nil && names != nil {
			allowlist = names
		}
	}
	return map[string]any{
		"id":           k.ID,
		"name":         k.Name,
		"prefix":       k.Prefix,
		"status":       string(k.Status),
		"expires_at":   timePtrRFC3339(k.ExpiresAt),
		"allowlist":    allowlist,
		"last_used_at": nil,
		"created_at":   nil,
	}
}

// timePtrRFC3339 把 *time.Time 转 RFC3339（nil → null）。
func timePtrRFC3339(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// HandleAccountKeysList GET /api/account/keys。
func (s *Server) HandleAccountKeysList(deps *AccountKeysDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := subjectFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "会话不存在或已过期，请重新登录", nil)
			return
		}
		keys, err := deps.Manager.List(r.Context(), sub)
		if err != nil {
			mapKeyErr(w, err)
			return
		}
		rows := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, s.renderKey(r.Context(), deps, k))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": rows, "next_cursor": nil})
	}
}

// HandleAccountKeyGet GET /api/account/keys/{id}。
func (s *Server) HandleAccountKeyGet(deps *AccountKeysDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := s.ownKeyOr404(w, r, deps)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": s.renderKey(r.Context(), deps, *key)})
	}
}

// ownKeyOr404 取当前主体名下的一把 Key；不存在/非本人一律 404（不给探测面）。
func (s *Server) ownKeyOr404(w http.ResponseWriter, r *http.Request, deps *AccountKeysDeps) (*auth.APIKey, bool) {
	sub, ok := subjectFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "session_expired", "会话不存在或已过期，请重新登录", nil)
		return nil, false
	}
	id := keyIDParam(r)
	if id == "" {
		writeErrorParam(w, http.StatusBadRequest, "invalid_request", "id", "缺少 Key id")
		return nil, false
	}
	keys, err := deps.Manager.List(r.Context(), sub)
	if err != nil {
		mapKeyErr(w, err)
		return nil, false
	}
	for i := range keys {
		if keys[i].ID == id {
			return &keys[i], true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
	return nil, false
}

// HandleAccountKeyCreate POST /api/account/keys。
//
// 请求体 {"name"?, "expires_at"?(RFC3339), "allowlist"?: [抽象模型名]}。
// 明文只在本次响应出现（05-keys §3）；响应 {data:{key, plaintext}}。
func (s *Server) HandleAccountKeyCreate(deps *AccountKeysDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := subjectFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "会话不存在或已过期，请重新登录", nil)
			return
		}
		var body struct {
			Name      string   `json:"name"`
			ExpiresAt string   `json:"expires_at"`
			Allowlist []string `json:"allowlist"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "body", "请求体不是合法 JSON")
			return
		}
		opts := auth.CreateOptions{Name: strings.TrimSpace(body.Name)}
		if body.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, body.ExpiresAt)
			if err != nil {
				writeErrorParam(w, http.StatusBadRequest, "invalid_request", "expires_at", "expires_at 须为 RFC3339 时刻")
				return
			}
			opts.ExpiresAt = &t
		}
		res, err := deps.Manager.CreateWithOptions(r.Context(), sub, opts)
		if err != nil {
			mapKeyErr(w, err)
			return
		}
		if body.Allowlist != nil {
			if err := deps.Manager.SetAllowlist(r.Context(), sub, res.Key.ID, body.Allowlist); err != nil {
				mapKeyErr(w, err)
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"data": map[string]any{
				"key":       s.renderKey(r.Context(), deps, *res.Key),
				"plaintext": res.Plaintext,
			},
			"note": "这把 Key 的明文只显示这一次，请立即保存",
		})
	}
}

// HandleAccountKeyPatch PATCH /api/account/keys/{id}。
//
// {"name"?, "expires_at"?(RFC3339 或 null 清除), "allowlist"?: [模型名]}。
// 三个字段都可选；给了才改。响应 {data:key}。
func (s *Server) HandleAccountKeyPatch(deps *AccountKeysDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := subjectFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "会话不存在或已过期，请重新登录", nil)
			return
		}
		id := keyIDParam(r)
		if id == "" {
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "id", "缺少 Key id")
			return
		}
		var body struct {
			Name      *string   `json:"name"`
			ExpiresAt *string   `json:"expires_at"`
			Allowlist *[]string `json:"allowlist"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "body", "请求体不是合法 JSON")
			return
		}
		if body.Name != nil {
			if err := deps.Manager.Rename(r.Context(), sub, id, strings.TrimSpace(*body.Name)); err != nil {
				mapKeyErr(w, err)
				return
			}
		}
		if body.ExpiresAt != nil && deps.SetKeyExpiry != nil {
			var t *time.Time
			if *body.ExpiresAt != "" {
				parsed, err := time.Parse(time.RFC3339, *body.ExpiresAt)
				if err != nil {
					writeErrorParam(w, http.StatusBadRequest, "invalid_request", "expires_at", "expires_at 须为 RFC3339 时刻")
					return
				}
				t = &parsed
			}
			if err := deps.SetKeyExpiry(r.Context(), id, t); err != nil {
				mapKeyErr(w, err)
				return
			}
		}
		if body.Allowlist != nil {
			if err := deps.Manager.SetAllowlist(r.Context(), sub, id, *body.Allowlist); err != nil {
				mapKeyErr(w, err)
				return
			}
		}
		key, ok := s.ownKeyOr404(w, r, deps)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": s.renderKey(r.Context(), deps, *key)})
	}
}

// HandleAccountKeyEnable / HandleAccountKeyDisable
// POST /api/account/keys/{id}/enable|disable。
//
// disabled 是可恢复状态（运营动作）；revoked 是单向安全动作，不在这条路径上。
func (s *Server) HandleAccountKeyEnable(deps *AccountKeysDeps) http.HandlerFunc {
	return s.handleKeyEnable(deps, true)
}

func (s *Server) HandleAccountKeyDisable(deps *AccountKeysDeps) http.HandlerFunc {
	return s.handleKeyEnable(deps, false)
}

func (s *Server) handleKeyEnable(deps *AccountKeysDeps, enable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := subjectFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "会话不存在或已过期，请重新登录", nil)
			return
		}
		id := keyIDParam(r)
		if id == "" {
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "id", "缺少 Key id")
			return
		}
		var err error
		if enable {
			err = deps.Manager.Enable(r.Context(), sub, id)
		} else {
			err = deps.Manager.Disable(r.Context(), sub, id)
		}
		if err != nil {
			mapKeyErr(w, err)
			return
		}
		key, ok := s.ownKeyOr404(w, r, deps)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": s.renderKey(r.Context(), deps, *key)})
	}
}

// HandleAccountKeyRevoke POST /api/account/keys/{id}/revoke。
//
// revoked 是单向不可恢复（05-keys §2）：响应 {"data":{"revoked":true}}。
func (s *Server) HandleAccountKeyRevoke(deps *AccountKeysDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := subjectFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "会话不存在或已过期，请重新登录", nil)
			return
		}
		id := keyIDParam(r)
		if id == "" {
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "id", "缺少 Key id")
			return
		}
		if err := deps.Manager.Revoke(r.Context(), sub, id); err != nil {
			mapKeyErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"revoked": true}})
	}
}

// HandleAccountKeyRotate POST /api/account/keys/{id}/rotate。
//
// 与前端契约无关（Key 页目前不提供轮换入口），保留 M3 语义：单事务吊销旧 Key
// 并写入新 Key；响应 {data:{key, plaintext}} 与创建同形。
func (s *Server) HandleAccountKeyRotate(deps *AccountKeysDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := subjectFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "会话不存在或已过期，请重新登录", nil)
			return
		}
		id := keyIDParam(r)
		if id == "" {
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "id", "缺少 Key id")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		res, err := deps.Manager.Rotate(r.Context(), sub, id, strings.TrimSpace(body.Name))
		if err != nil {
			mapKeyErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"data": map[string]any{
				"key":       s.renderKey(r.Context(), deps, *res.Key),
				"plaintext": res.Plaintext,
			},
			"note": "旧 Key 已在同一事务内吊销；新 Key 明文只显示这一次",
		})
	}
}

func keyIDParam(r *http.Request) string {
	return chi.URLParam(r, "id")
}

// mapKeyErr 把 auth.KeyManager 的错误映射到对外错误码。
func mapKeyErr(w http.ResponseWriter, err error) {
	var ae *auth.Error
	if asAuthError(err, &ae) {
		switch ae.Code {
		case auth.CodeConflict:
			writeError(w, http.StatusConflict, "conflict", ae.Msg, nil)
			return
		case auth.CodeForbidden:
			writeError(w, http.StatusForbidden, "forbidden", ae.Msg, nil)
			return
		case auth.CodeNotFound:
			writeError(w, http.StatusNotFound, "not_found", ae.Msg, nil)
			return
		case auth.CodeInvalidRequest:
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "body", ae.Msg)
			return
		case auth.CodeInternal:
			writeError(w, http.StatusInternalServerError, "internal_error", ae.Msg, nil)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Key 操作失败", nil)
}
