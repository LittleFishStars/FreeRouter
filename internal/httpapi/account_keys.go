package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// account_keys.go —— 租户自助的 API Key 生命周期（M3 的「Key 管理」面）。
//
// 为什么必须由会话面提供：Key 是"人给自己签的机器凭据"，签发动作需要
// 人的会话（mwSession），而调用模型用签出来的 Key（mwKeyAuth）。两条
// 凭据链在 L1 就分开，不能混用——用 Key 去签 Key 等于让泄露的 Key 能
// 无限自我续命。
//
// 归属与数量上限全部由 auth.KeyManager 判定（tenant 类、每账户上限、
// 白名单只能收窄）；本文件只做 HTTP 形状（解码、错误映射、明文只回一次）。

// AccountKeysDeps 是租户 Key 面需要的依赖。
type AccountKeysDeps struct {
	Manager *auth.KeyManager
}

// Subject 从上下文取当前会话主体并转成 auth.Subject。
func subjectFromRequest(r *http.Request) (auth.Subject, bool) {
	p := SessionPrincipalFromContext(r.Context())
	if p == nil {
		return auth.Subject{}, false
	}
	return auth.Subject{AccountID: p.AccountID, ActingClass: auth.ClassTenant}, true
}

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
			rows = append(rows, map[string]any{
				"id":      k.ID,
				"prefix":  k.Prefix,
				"name":    k.Name,
				"status":  string(k.Status),
				"expires": k.ExpiresAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": rows})
	}
}

func (s *Server) HandleAccountKeyCreate(deps *AccountKeysDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sub, ok := subjectFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session_expired", "会话不存在或已过期，请重新登录", nil)
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErrorParam(w, http.StatusBadRequest, "invalid_request", "body", "请求体不是合法 JSON")
			return
		}
		res, err := deps.Manager.Create(r.Context(), sub, strings.TrimSpace(body.Name))
		if err != nil {
			mapKeyErr(w, err)
			return
		}
		// 明文只在这里出现一次（05-keys §3）：响应体之外没有任何读回路径。
		writeJSON(w, http.StatusCreated, map[string]any{
			"plaintext": res.Plaintext,
			"id":        res.Key.ID,
			"prefix":    res.Key.Prefix,
			"name":      res.Key.Name,
			"status":    string(res.Key.Status),
			"note":      "这把 Key 的明文只显示这一次，请立即保存",
		})
	}
}

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
		writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	}
}

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
			"plaintext": res.Plaintext,
			"id":        res.Key.ID,
			"prefix":    res.Key.Prefix,
			"note":      "旧 Key 已在同一事务内吊销；新 Key 明文只显示这一次",
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

var _ = context.Background
