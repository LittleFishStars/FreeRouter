package httpapi

// account_keys.go —— 租户自助的 API Key 生命周期（docs/ui/04-tenant.md「我的 API Key」）。
//
// 为什么必须由会话面提供：Key 是"人给自己签的机器凭据"，签发动作需要
// 人的会话（mwSession），而调用模型用签出来的 Key（mwKeyAuth）。两条
// 凭据链在 L1 就分开，不能混用——用 Key 去签 Key 等于让泄露的 Key 能
// 无限自我续命。
//
// 归属与数量上限全部由 auth.KeyManager 判定（tenant 类、每账户上限、
// 黑名单只可能缩小可用集）；本文件只做 HTTP 形状（解码、错误映射、明文只回一次）。
//
// 响应形状对齐前端 web/src/api/types.ts 的 ApiKey/ApiKeyCreated：
//   {id, name, prefix, status, expires_at, denylist: [模型名],
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
	// DenylistOf 读一把 Key 的黑名单（抽象模型名）。nil = 恒空数组。
	DenylistOf func(ctx context.Context, keyID string) ([]string, error)
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
	denylist := []string{}
	if deps != nil && deps.DenylistOf != nil {
		if names, err := deps.DenylistOf(ctx, k.ID); err == nil && names != nil {
			denylist = names
		}
	}
	return map[string]any{
		"id":           k.ID,
		"name":         k.Name,
		"prefix":       k.Prefix,
		"status":       string(k.Status),
		"expires_at":   timePtrRFC3339(k.ExpiresAt),
		"denylist":     denylist,
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
// 请求体 {"name"?, "expires_at"?(RFC3339), "denylist"?: [抽象模型名]}。
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
			Denylist []string `json:"denylist"`
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
		if body.Denylist != nil {
			if err := deps.Manager.SetDenylist(r.Context(), sub, res.Key.ID, body.Denylist); err != nil {
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

// patchString 把 JSON 的「字段缺省」与「显式 null」区分开。
//
// 为什么不用 *string：encoding/json 把缺省字段与 null 都解成 nil，两者在类型上
// 不可分辨；而本端点的语义恰好要求「缺省=不变、null=清空」，用 *string 的结果
// 就是前端发 null 想清空却被当成「没给」，过期时间永远清不掉（审计 P1）。
//
// 依赖 encoding/json 的两条约定：只在字段出现时调用 UnmarshalJSON，且 null 也会
// 被送进来（null 不跳过 Unmarshaler）。于是：
//
//	Set=false        → 字段缺省（不写）
//	Set=true, Value=nil → 显式 null（清空）
//	Set=true, Value=&s   → 字符串（设定）
type patchString struct {
	Set   bool    // 字段是否出现在请求体里
	Value *string // nil = 显式 null
}

func (n *patchString) UnmarshalJSON(b []byte) error {
	n.Set = true
	if string(b) == "null" {
		n.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.Value = &s
	return nil
}

// HandleAccountKeyPatch PATCH /api/account/keys/{id}。
//
// 请求体 {"name"?, "expires_at"?(RFC3339 | null | ""), "denylist"?: [模型名]}。
// 三个字段都可选；**只有出现过的字段才会被写**。响应 {data:key}。
//
// expires_at 的三值语义（审计 P1，2026-10-24 修复）：
//
//	字段缺省                → 不变（今天过期的 Key 明天还是那个时刻）
//	"expires_at": null      → 清空（永不过期）
//	"expires_at": "...Z"    → 设定为该时刻（RFC3339）
//
// 空串 "" 仍按「清空」处理：旧表单/脚本曾用空串表达同一意图，删掉这条兼容
// 只会把行为差异留给调用方去猜。
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
		// 先校验归属再写：expires_at 的写入按 id 直达存储层（deps.SetKeyExpiry
		// 只做 id 翻译），若把归属检查留到响应阶段，别人的 Key id 就能被改掉
		// 过期时间。读/写他人 Key 一律 404（04-authz.md，不泄露资源存在性）。
		if _, ok := s.ownKeyOr404(w, r, deps); !ok {
			return
		}
		var body struct {
			Name      *string     `json:"name"`
			ExpiresAt patchString `json:"expires_at"`
			Denylist *[]string   `json:"denylist"`
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
		if body.ExpiresAt.Set {
			var t *time.Time
			if v := body.ExpiresAt.Value; v != nil && *v != "" {
				parsed, err := time.Parse(time.RFC3339, *v)
				if err != nil {
					writeErrorParam(w, http.StatusBadRequest, "invalid_request", "expires_at", "expires_at 须为 RFC3339 时刻或 null")
					return
				}
				t = &parsed
			}
			// t == nil 在此处是「清空」的确定语义，不是「不写」——「不写」在前面的
			// Set 判断就返回了。存储层的 nil 同样表示清空（写 NULL）。
			if deps.SetKeyExpiry == nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Key 过期时间写入未装配", nil)
				return
			}
			if err := deps.SetKeyExpiry(r.Context(), id, t); err != nil {
				mapKeyErr(w, err)
				return
			}
		}
		if body.Denylist != nil {
			if err := deps.Manager.SetDenylist(r.Context(), sub, id, *body.Denylist); err != nil {
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
