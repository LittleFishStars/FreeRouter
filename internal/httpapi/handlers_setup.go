package httpapi

import (
	"context"
	"net/http"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// handlers_setup.go：初始化向导端点（GET /api/setup/status 真判定 +
// POST /api/setup/init 创建首个 admin）。
//
// 契约（docs/api/02-admin.md §4 初始化 + PLAN 2.7）：
//   - /api/setup **不在 /api/admin 下**：它必须在"库中没有任何账户"时
//     就可用，而 /api/admin 的 admin 类检查在那一刻没有账户可满足；
//   - init 仅当**0 账户**时可用（不是"0 个 admin"——第一个账户就是
//     站长自举，任何已有账户都让向导关闭，防二次初始化劫持）；
//   - 二次提交 409 conflict；
//   - 首个账户直接给 {admin, tenant}（管理员默认双类，PLAN 2.2——
//     admin 管全站、tenant 让站长自己的 Key 能调模型走两套账）。
//
// handler 写成 Server 方法（handleSetupStatusV2 / handleSetupInit）；
// Lead 挂路由并移除 server.go 的 M0 占位（本文件不碰 server.go）。

// SetupDeps 是初始化向导的依赖（经窄接口，不 import internal/store 形状）。
type SetupDeps struct {
	// SetupStore 是 setup 判定的最小存储面。
	SetupStore SetupStore
	// Hasher 创建首个账户的 argon2id 哈希（密码不落明文）。
	Hasher *auth.PasswordHasher
}

// SetupStore 是 setup 判定的窄接口。
//
// 为什么只有两读一写：向导的整个生命周期就是"查有没有账户 → 创建一个"，
// 更大的面（列账户、改状态）都是 /api/admin 的事——向导持大接口
// 只会让"未初始化状态下的向导"变成一个绕过 admin 检查的后门。
type SetupStore interface {
	// CountAccounts 报告库中账户总数（0 = 未初始化）。
	CountAccounts(ctx context.Context) (int, error)
	// CreateFirstAdmin 创建首个账户（admin+tenant 类）。仅当当前 0 账户
	// 时成功；已存在账户返回 ErrConflict（调用方翻译 409）。
	CreateFirstAdmin(ctx context.Context, username, passwordHash, displayName string) error
}

// handleSetupStatusV2 GET /api/setup/status——真判定（读库）。
//
// 响应形状与 M0 占位**保持一致**（前端 M0 已按这个形状写）：
// setup_required / registration_enabled / requires_approval。
// setup_required = 账户数 == 0。读库失败时**不谎报**：
// 返回 503 not_ready（一个读不到库的进程不该对外宣称任何初始化状态）。
func (s *Server) handleSetupStatusV2(deps *SetupDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.SetupStore == nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"初始化判定未装配", nil)
			return
		}
		n, err := deps.SetupStore.CountAccounts(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready",
				"初始化状态暂不可查："+err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"setup_required":       n == 0,
			"registration_enabled": s.cfg.Security.RegistrationEnabled,
			"requires_approval":    s.cfg.Security.RegistrationRequiresApproval,
		})
	}
}

// handleSetupInit POST /api/setup/init——创建首个 admin。
//
// 请求体 {"username","password","display_name"?}。
// 成功 201（无 Cookie——向导创建后引导用户**正常登录一次**，
// 而不是直接发会话：向导页是未初始化状态的一部分，签发会话等于
// 让"创建账户"与"建立登录态"合并成一步，绕过登录路径上的
// 审计与限速预热）。
// 二次提交（已有账户）409 conflict；密码不达标 400 invalid_request。
func (s *Server) handleSetupInit(deps *SetupDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.SetupStore == nil || deps.Hasher == nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"初始化服务未装配", nil)
			return
		}
		var body struct {
			Username    string `json:"username"`
			Password    string `json:"password"`
			DisplayName string `json:"display_name"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		// 先判窗口再验参数：向外的错误顺序本身是信息——
		// 已初始化的实例上，"用户名格式不对"比"409"多泄露了一次
		// 参数校验的存在；统一先 409 让向导关闭后的行为恒定。
		n, err := deps.SetupStore.CountAccounts(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready",
				"初始化状态暂不可查："+err.Error(), nil)
			return
		}
		if n > 0 {
			writeError(w, http.StatusConflict, "conflict",
				"实例已初始化，向导不可用", nil)
			return
		}
		if msg := s.validateSetupInput(body.Username, body.Password); msg != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", msg, nil)
			return
		}
		if body.DisplayName == "" {
			body.DisplayName = body.Username
		}

		hash, err := deps.Hasher.Hash(body.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error",
				"密码哈希失败", nil)
			return
		}
		if err := deps.SetupStore.CreateFirstAdmin(r.Context(),
			body.Username, hash, body.DisplayName); err != nil {
			var ae *auth.Error
			if asAuthError(err, &ae) && ae.Code == auth.CodeConflict {
				writeError(w, http.StatusConflict, "conflict",
					"实例已初始化，向导不可用", nil)
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error",
				"创建首个账户失败", nil)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"ok":      true,
			"message": "初始化完成，请登录",
		})
	}
}

// validateSetupInput 校验向导输入；返回空串表示通过。
//
// 用户名规则与注册面共用（长度 3-64，可打印 ASCII——复用 02-authn §2
// 的口径）；密码 ≥12（Security.PasswordMinLength，不硬编码）。
func (s *Server) validateSetupInput(username, password string) string {
	if len(username) < 3 || len(username) > 64 {
		return "用户名长度须在 3–64 之间"
	}
	for i := 0; i < len(username); i++ {
		c := username[i]
		if c < 0x21 || c > 0x7e {
			return "用户名仅允许可打印 ASCII 字符"
		}
	}
	minLen := 12
	if s.cfg != nil && s.cfg.Security.PasswordMinLength > 0 {
		minLen = int(s.cfg.Security.PasswordMinLength)
	}
	if len(password) < minLen {
		return "密码至少需要 12 个字符"
	}
	return ""
}
