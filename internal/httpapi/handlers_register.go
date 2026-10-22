package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// handlers_register.go —— 注册与待审核（docs/ui/02-public.md §4/§5）。
//
// 端点：
//
//	POST /api/register       注册（未登录；安全 02-authn §3）
//	GET  /api/account/status 待审核页的「刷新状态」（会话面）
//
// 注册语义（02-public 4.2 防枚举定案）：
//   - 用户名占用与成功**对外同形**（都 201 pending）——不确认用户名是否已注册，
//     占用探测面从注册口堵死；真实冲突进服务器日志。
//   - 关闭注册时 403 forbidden（这是功能开关，不是枚举问题）。
//   - 格式/强度错误 400 invalid_request（是本人输入错误，可以反馈）。
//
// 注册出来的账户：status=pending（不能登录）、contributor 容器类
// （01-accounts：零权限容器，保证类集合非空不变式）、进默认组、
// 密码是本人设的（must_change_password=false）。

// RegisterDeps 是注册面的依赖。
type RegisterDeps struct {
	Store   RegisterStore
	Hasher  *auth.PasswordHasher
	Enabled bool
}

// RegisterStore 是注册面的窄接口（装配桥实现）。
type RegisterStore interface {
	// CreatePendingAccount 创建 pending 账户（contributor 容器类 + 默认组）。
	CreatePendingAccount(ctx context.Context, username, passwordHash, displayName string) error
	// StatusOf 读会话主体的状态与权限类。
	StatusOf(ctx context.Context, accountID string) (status string, classes []string, err error)
}

// HandleRegister POST /api/register。
func (s *Server) HandleRegister(deps *RegisterDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Store == nil || deps.Hasher == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "注册服务未装配", nil)
			return
		}
		if !deps.Enabled {
			writeError(w, http.StatusForbidden, "forbidden", "注册已关闭", nil)
			return
		}
		var body struct {
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Password    string `json:"password"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		username := strings.TrimSpace(body.Username)
		if msg := s.validateSetupInput(username, body.Password); msg != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", msg, nil)
			return
		}
		displayName := strings.TrimSpace(body.DisplayName)
		if displayName == "" {
			displayName = username
		}
		hash, err := deps.Hasher.Hash(body.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "密码哈希失败", nil)
			return
		}
		if err := deps.Store.CreatePendingAccount(r.Context(), username, hash, displayName); err != nil {
			var ae *auth.Error
			if asAuthError(err, &ae) && ae.Code == auth.CodeConflict {
				// 防枚举：对外与成功同形。真实冲突只进日志。
				s.log.Warn("register: 用户名冲突（对外按 pending 同形返回）", "username", username)
			} else {
				s.log.Error("register: 创建待审核账户失败", "err", err)
				writeError(w, http.StatusInternalServerError, "internal_error", "注册失败", nil)
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"data": map[string]any{"status": "pending"},
		})
	}
}

// HandleAccountStatus GET /api/account/status（挂 mwSession）。
func (s *Server) HandleAccountStatus(deps *RegisterDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Store == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "账户状态服务未装配", nil)
			return
		}
		p := SessionPrincipalFromContext(r.Context())
		if p == nil {
			writeError(w, http.StatusUnauthorized, "session_expired",
				"会话不存在或已过期，请重新登录", nil)
			return
		}
		status, classes, err := deps.Store.StatusOf(r.Context(), p.AccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取账户状态失败", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{"status": status, "classes": classes},
		})
	}
}
