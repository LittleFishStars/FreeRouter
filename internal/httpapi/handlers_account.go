package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// handlers_account.go：账户自助面（M11：首次登录强制改密 + 资料补全）。
//
// 端点（都挂 mwSession，CSRF 自定义头由中间件检查）：
//
//	POST  /api/account/password  改密（可顺带补全显示名）
//	PATCH /api/account/profile   只改显示名
//
// 为什么改密端点要能顺带改显示名：首启随机口令的流程里，用户登录后第一件事
// 就是「换成自己的口令 + 给自己起个称呼」；拆成两个请求会让前端在会话吊销的
// 边界上做两次状态机（第一个请求就吊销了全部会话），把两步并进同一次交互
// 才是这条路径的真实形状。
//
// 成功语义（与 auth.LoginService.ChangePassword 的联动定案一致）：
//   - 改密吊销**全部**会话（含当前）→ 清 Cookie → 前端回登录页；
//   - 显示名是 best-effort：改密成功而显示名写入失败时返回 200 +
//     display_name_updated=false，不把已经生效的口令变更报成失败；
//   - 一次性口令文件（首启）由装配层通过 OnPasswordChanged 回调删除。

// AccountPasswordDeps 是账户自助面的依赖。
type AccountPasswordDeps struct {
	// Login 提供改密编排（校验旧口令、写新哈希、吊销会话）。
	Login *auth.LoginService
	// Profile 可选：给了才能在改密请求里更新显示名。
	Profile AccountProfileStore
	// OnPasswordChanged 可选：改密成功后的清理回调（装配层删首启密码文件）。
	OnPasswordChanged func()
}

// AccountProfileStore 是显示名写面的窄接口（装配层桥到 store）。
type AccountProfileStore interface {
	UpdateDisplayName(ctx context.Context, accountID, displayName string) error
}

// handleAccountPassword POST /api/account/password。
//
// 请求体 {"current_password","new_password","display_name"?}。
// 旧口令错误 401 invalid_credentials；新口令不达标 400 invalid_request；
// 非会话身份 403 forbidden。成功 200 {"ok":true, ...} 并清 Cookie。
func (s *Server) handleAccountPassword(deps *AccountPasswordDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Login == nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"账户服务未装配", nil)
			return
		}
		p := SessionPrincipalFromContext(r.Context())
		if p == nil {
			writeError(w, http.StatusUnauthorized, "session_expired",
				"会话不存在或已过期，请重新登录", nil)
			return
		}
		var body struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
			DisplayName     string `json:"display_name"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		displayName := strings.TrimSpace(body.DisplayName)
		if msg := validateDisplayName(displayName); msg != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", msg, nil)
			return
		}

		if err := deps.Login.ChangePassword(r.Context(), p, body.CurrentPassword, body.NewPassword); err != nil {
			var ae *auth.Error
			if asAuthError(err, &ae) {
				switch ae.Code {
				case auth.CodeInvalidCredentials:
					writeError(w, http.StatusUnauthorized, "invalid_credentials",
						"当前口令错误", nil)
					return
				case auth.CodeInvalidRequest:
					writeError(w, http.StatusBadRequest, "invalid_request", ae.Msg, nil)
					return
				case auth.CodeForbidden:
					writeError(w, http.StatusForbidden, "forbidden", ae.Msg, nil)
					return
				}
				writeError(w, http.StatusInternalServerError, "internal_error", ae.Msg, nil)
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", "改密失败", nil)
			return
		}

		// 口令已生效；会话已全部吊销。以下都是收尾，不能把成功改判成失败。
		displayNameUpdated := false
		if displayName != "" && deps.Profile != nil {
			if err := deps.Profile.UpdateDisplayName(r.Context(), p.AccountID, displayName); err != nil {
				s.log.Warn("account password: 显示名更新失败（口令已更新）",
					"account", p.AccountID, "err", err)
			} else {
				displayNameUpdated = true
			}
		}
		if deps.OnPasswordChanged != nil {
			deps.OnPasswordChanged()
		}
		// 会话已吊销；清 Cookie 让浏览器不再携带失效令牌。
		http.SetCookie(w, s.clearSessionCookie())
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                   true,
			"message":              "口令已更新，请用新口令重新登录",
			"display_name_updated": displayNameUpdated,
		})
	}
}

// handleAccountProfile PATCH /api/account/profile。
//
// 请求体 {"display_name"}。只改显示名，不触碰口令与会话——它同时服务
// 强制改密窗口内的「补全资料」与普通账户的自助改名。
func (s *Server) handleAccountProfile(deps *AccountPasswordDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Profile == nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"账户资料服务未装配", nil)
			return
		}
		p := SessionPrincipalFromContext(r.Context())
		if p == nil {
			writeError(w, http.StatusUnauthorized, "session_expired",
				"会话不存在或已过期，请重新登录", nil)
			return
		}
		var body struct {
			DisplayName string `json:"display_name"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		displayName := strings.TrimSpace(body.DisplayName)
		if msg := validateDisplayName(displayName); msg != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", msg, nil)
			return
		}
		if err := deps.Profile.UpdateDisplayName(r.Context(), p.AccountID, displayName); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"显示名更新失败", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           true,
			"display_name": displayName,
		})
	}
}

// validateDisplayName 校验显示名（1–64 个字符，不含控制字符）。
//
// 空串表示「本次不修改」——由调用方决定是否写入；这里只对非空值做形状校验。
func validateDisplayName(name string) string {
	if name == "" {
		return ""
	}
	n := len([]rune(name))
	if n > 64 {
		return "显示名最长 64 个字符"
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "显示名不能包含控制字符"
		}
	}
	return ""
}
