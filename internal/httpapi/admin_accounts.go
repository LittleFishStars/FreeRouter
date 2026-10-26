// admin_accounts.go —— 账户组端点（02-admin §5，task-20 M7）。
//
// 端点（全部在 /api/admin 路由组内、mwAdmin 之后）：
//   GET    /api/admin/accounts               列表（class/status/group_id 筛选）
//   POST   /api/admin/accounts               单个新建
//   PATCH  /api/admin/accounts/{id}          改类/状态/组/备注/显示名/额度/速率
//   POST   /api/admin/accounts/{id}/password 重置密码（给定或生成）
//   POST   /api/admin/accounts/{id}/sessions/revoke 吊销全部会话
//
// 没有的端点及原因（契约 §5.1）：
//   - DELETE /accounts/{id}：requests/audit_log 引用 account_id，物理删除
//     让历史悬空；"删除"一律是状态迁移（disabled/rejected）。
//   - GET /accounts/pending、approve、reject：**属注册审批流**，02-admin
//     把它列在 §5.1，但注册端点本身（registration_enabled 流程）还没人
//     交付（handlers_setup.go 里是 TODO(审批流)）。审批要选组+配额、
//     与 §5.2 的批准必带组与配额校验同源——本轮把 pending 列表与批准
//     一并落地（不依赖注册流即可工作：任何 status=pending 的账户都在
//     队列里），拒绝暂不落（reject 与 rejected 状态已可经 PATCH 达到）。
//   - POST /accounts/import（CSV）与 merge/merge/rollback：见文件尾 TODO，
//     merge 的 reversible_until 语义属账户域新逻辑（01-accounts §4），
//     本轮 narrow interface 未定型，留给下一轮（回报已列）。
//
// 危险写（§2.3）：PATCH 改类/状态、POST password 都是界面二次确认项，
// API 层不加分支——这里没有 "dangerous" 判定，和 §2.2 一样是定案。

package httpapi

import (
	"encoding/json"
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// mountAdminAccounts 注册账户面路由。
func (s *Server) mountAdminAccounts(r chi.Router, deps *AdminDeps) {
	r.Get("/accounts", s.handleAdminAccountsList(deps))
	r.Post("/accounts", s.handleAdminAccountsCreate(deps))
	// 静态路径先注册：chi 的字面量段优先级高于 {id}，"pending" 不会被
	// /accounts/{id} 吞掉（注册顺序再兜一层可读性）。
	r.Get("/accounts/pending", s.handleAdminAccountsPending(deps))
	r.Post("/accounts/pending/{id}/approve", s.handleAdminAccountApprove(deps))
	r.Post("/accounts/pending/{id}/reject", s.handleAdminAccountReject(deps))
	// CSV 导入：静态路径 /import 必须在 /{id} 之前注册（chi 字面量优先，
	// 注册顺序再兜一层可读性）。
	r.Post("/accounts/import", s.HandleAdminAccountsImport(deps))
	r.Get("/accounts/{id}", s.handleAdminAccountDetail(deps))
	r.Patch("/accounts/{id}", s.handleAdminAccountPatch(deps))
	r.Post("/accounts/{id}/password", s.handleAdminAccountPassword(deps))
	r.Post("/accounts/{id}/sessions/revoke", s.handleAdminAccountRevokeSessions(deps))
	// 合并面（🔒 危险写）：预检是只读、合并与回滚是写。
	r.Get("/accounts/{id}/merge/precheck", s.handleAdminAccountMergePrecheck(deps))
	r.Post("/accounts/{id}/merge", s.handleAdminAccountMerge(deps))
	r.Post("/accounts/{id}/merge/rollback", s.handleAdminAccountMergeRollback(deps))
}

// handleAdminAccountsList GET /api/admin/accounts?class=&status=&group_id=。
//
// 每行只给 key_count/credential_count 数量（§5.2）：计数经独立的
// Keys/Credentials 窄接口补查——列表端点因此不具备"一次拿到某人的
// 全部资源"的能力。计数查询失败不阻断列表（数量列缺 0 + 服务器日志），
// 理由：列表的主价值是账户行，计数是装饰；把装饰故障升级成整页 500
// 会让管理员在从库抖动时连"谁被禁了"都看不到。
func (s *Server) handleAdminAccountsList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		f := AdminAccountFilter{
			Class:   q.Get("class"),
			Status:  q.Get("status"),
			GroupID: q.Get("group_id"),
			Limit:   limit,
			Offset:  offset,
		}
		if f.Class != "" && !adminClassValid(f.Class) {
			adminWriteParamErr(w, "class", "class 取值须为 admin/tenant/contributor")
			return
		}
		if f.Status != "" && !adminFilterStatusValid(f.Status) {
			adminWriteParamErr(w, "status", "status 筛选取值非法（pending/active/disabled/rejected/merged）")
			return
		}
		rows, total, err := deps.Accounts.ListAccounts(r.Context(), f)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		out := make([]AdminAccountOut, 0, len(rows))
		for i := range rows {
			row := rows[i]
			s.fillAdminCounts(deps, r, &row)
			out = append(out, renderAdminAccount(&row))
		}
		adminListEnvelope(w, out, total, limit, offset)
	}
}

// fillAdminCounts 补 key_count/credential_count（可 nil 的依赖降级为 0）。
func (s *Server) fillAdminCounts(deps *AdminDeps, r *http.Request, row *AdminAccount) {
	if deps.Keys != nil {
		if n, err := deps.Keys.CountKeys(r.Context(), row.ID); err == nil {
			row.KeyCount = n
		} else {
			s.log.Warn("admin accounts list: key count failed", "account", row.ID, "err", err)
		}
	}
	if deps.Credentials != nil {
		if n, err := deps.Credentials.CountCredentials(r.Context(), row.ID); err == nil {
			row.CredentialCount = n
		} else {
			s.log.Warn("admin accounts list: credential count failed", "account", row.ID, "err", err)
		}
	}
}

// adminClassValid 是筛选值域（区别于 adminValidClassSet：筛选允许单值、
// 不给是不筛）。
func adminClassValid(c string) bool {
	return c == "admin" || c == "tenant" || c == "contributor"
}

// adminFilterStatusValid 是状态筛选值域（全枚举：列表要看 pending/merged，
// 只有**写入**才收窄到 active/disabled/rejected 子集）。
func adminFilterStatusValid(st string) bool {
	switch st {
	case "pending", "active", "disabled", "rejected", "merged":
		return true
	}
	return false
}

// accountIDParam 解 /accounts/{id} 路径参数：idcode 前缀校验（lead 裁决①）。
func accountIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "账户")
}

// handleAdminAccountDetail GET /api/admin/accounts/{id}：详情只给行字段
// 与两个计数，结构上没有能携带 Key/凭据列表的字段（§5.2）。
func (s *Server) handleAdminAccountDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		id, ok := accountIDParam(w, r)
		if !ok {
			return
		}
		row, err := deps.Accounts.GetAccount(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		s.fillAdminCounts(deps, r, row)
		writeJSON(w, http.StatusOK, map[string]any{"data": renderAdminAccount(row)})
	}
}

// handleAdminAccountsCreate POST /api/admin/accounts。
//
// 载荷是 AdminAccount 的可写子集（username/display_name/classes/status/
// groups/quota/速率 + password 一次性字段）。password 给了就用、不给则
// 生成随机值**一次性回显**（§5.3 同一条理由：不返回就等于账户不可登录）。
// 密码本身经 Hasher 转 argon2id 后才进桥——明文只存在于本次响应。
func (s *Server) handleAdminAccountsCreate(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) ||
			!deps.need(w, "密码哈希器", deps.Hasher != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var body struct {
			Username       string      `json:"username"`
			DisplayName    string      `json:"display_name"`
			Classes        []string    `json:"classes"`
			Status         string      `json:"status,omitempty"`
			Groups         []string    `json:"groups,omitempty"`
			Note           string      `json:"note,omitempty"`
			Password       string      `json:"password,omitempty"`
			Quota          *AdminQuota `json:"quota,omitempty"`
			RPMLimit       *int64      `json:"rpm_limit,omitempty"`
			TPMLimit       *int64      `json:"tpm_limit,omitempty"`
			MaxConcurrency *int64      `json:"max_concurrency,omitempty"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if strings.TrimSpace(body.Username) == "" {
			adminWriteParamErr(w, "username", "username 不能为空")
			return
		}
		if err := adminValidClassSet(body.Classes); err != nil {
			adminFail(w, "classes", err)
			return
		}
		status := body.Status
		if status == "" {
			status = "active" // 管理员建号默认 active（§5.3：导入即 active 同口径）
		}
		if err := adminValidAccountStatus(status); err != nil {
			adminFail(w, "status", err)
			return
		}
		if err := adminValidQuota(body.Quota); err != nil {
			adminFail(w, "quota", err)
			return
		}
		rpm, tpm, conc := int64(-1), int64(-1), int64(-1)
		if body.RPMLimit != nil {
			rpm = *body.RPMLimit
		}
		if body.TPMLimit != nil {
			tpm = *body.TPMLimit
		}
		if body.MaxConcurrency != nil {
			conc = *body.MaxConcurrency
		}
		for _, pair := range []struct {
			f string
			v int64
		}{{"rpm_limit", rpm}, {"tpm_limit", tpm}, {"max_concurrency", conc}} {
			if err := adminValidRateLimit(pair.f, pair.v); err != nil {
				adminFail(w, pair.f, err)
				return
			}
		}
		password := body.Password
		generated := false
		if password == "" {
			var err error
			if password, err = adminGenPassword(); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "生成密码失败", nil)
				return
			}
			generated = true
		}
		if err := deps.Hasher.ValidatePassword(password); err != nil {
			adminFail(w, "password", err)
			return
		}
		hash, err := deps.Hasher.Hash(password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "密码哈希失败", nil)
			return
		}
		row := AdminAccount{
			DisplayName:    body.DisplayName,
			Username:       body.Username,
			Classes:        body.Classes,
			Status:         status,
			Groups:         body.Groups,
			Note:           body.Note,
			Quota:          body.Quota,
			RPMLimit:       rpm,
			TPMLimit:       tpm,
			MaxConcurrency: conc,
		}
		target := "account:" + row.Username
		audit := adminBaseAudit(p, r, "account.create", target, "ok")
		id, auditID, err := deps.Accounts.CreateAccount(r.Context(), row, hash, audit)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		row.ID = id
		out := renderAdminAccount(&row)
		if generated {
			// 一次性密码只出现在这一个响应里（§5.3：CSV 导入是唯一能
			// 看到这批密码的时刻——单体创建同理）。不进审计。
			out.GeneratedPassword = password
		}
		adminWriteOK(w, http.StatusCreated, out, auditID)
	}
}

// handleAdminAccountPatch PATCH /api/admin/accounts/{id}。
//
// 全部字段局部更新（nil 不动）。改类 = 变更能力面（§2.3 账户面危险写），
// 界面负责二次确认；API 层照常执行——差别只在审计 action 归类，
// 这里统一记 account.update，改类/禁用在 Detail 里逐字段旧值→新值
// （桥补；06-audit §3 的"权限类授予/撤销单独成条"由桥在同一事务里
// 拆分多条审计实现，API 入参只给一条基线行）。
func (s *Server) handleAdminAccountPatch(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := accountIDParam(w, r)
		if !ok {
			return
		}
		var in AdminAccountUpdate
		if _, ok := adminDecodeBoth(w, r, &in); !ok {
			return
		}
		if in.Classes != nil {
			if err := adminValidClassSet(*in.Classes); err != nil {
				adminFail(w, "classes", err)
				return
			}
		}
		if in.Status != nil {
			if err := adminValidAccountStatus(*in.Status); err != nil {
				adminFail(w, "status", err)
				return
			}
		}
		if err := adminValidQuota(in.Quota); err != nil {
			adminFail(w, "quota", err)
			return
		}
		for _, pair := range []struct {
			f string
			v *int64
		}{{"rpm_limit", in.RPMLimit}, {"tpm_limit", in.TPMLimit}, {"max_concurrency", in.MaxConcurrency}} {
			if pair.v != nil {
				if err := adminValidRateLimit(pair.f, *pair.v); err != nil {
					adminFail(w, pair.f, err)
					return
				}
			}
		}
		audit := adminBaseAudit(p, r, "account.update", "account:"+id, "ok")
		row, auditID, err := deps.Accounts.UpdateAccount(r.Context(), id, in, audit)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		s.fillAdminCounts(deps, r, row)
		adminWriteOK(w, http.StatusOK, renderAdminAccount(row), auditID)
	}
}

// handleAdminAccountPassword POST /api/admin/accounts/{id}/password。
//
// {"password": "..."} 给定新密码；{"generate": true} 生成一次性密码并
// 在响应里回显一次。revoke_sessions 缺省 true——改密吊销会话是
// 02-authn 的联动定案，保守默认不留在调用方自觉。
func (s *Server) handleAdminAccountPassword(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) ||
			!deps.need(w, "密码哈希器", deps.Hasher != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id := chi.URLParam(r, "id")
		var body struct {
			Password       string `json:"password,omitempty"`
			Generate       bool   `json:"generate,omitempty"`
			RevokeSessions *bool  `json:"revoke_sessions,omitempty"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		// 同给或同不给都是歧义：显式二选一，别猜管理员想要哪个。
		if (body.Password != "") == body.Generate {
			adminWriteParamErr(w, "password", "password 与 generate 必须恰好给一个")
			return
		}
		password := body.Password
		generated := false
		if body.Generate {
			var err error
			if password, err = adminGenPassword(); err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "生成密码失败", nil)
				return
			}
			generated = true
		}
		if err := deps.Hasher.ValidatePassword(password); err != nil {
			adminFail(w, "password", err)
			return
		}
		hash, err := deps.Hasher.Hash(password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "密码哈希失败", nil)
			return
		}
		revoke := true
		if body.RevokeSessions != nil {
			revoke = *body.RevokeSessions
		}
		audit := adminBaseAudit(p, r, "account.password_reset", "account:"+id, "ok")
		revoked, auditID, err := deps.Accounts.ResetPassword(r.Context(), id, hash, revoke, audit)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		data := map[string]any{"account_id": id, "sessions_revoked": revoked}
		if generated {
			data["generated_password"] = password // 一次性回显，不进审计
		}
		adminWriteOK(w, http.StatusOK, data, auditID)
	}
}

// handleAdminAccountRevokeSessions POST /api/admin/accounts/{id}/sessions/revoke。
func (s *Server) handleAdminAccountRevokeSessions(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := accountIDParam(w, r)
		if !ok {
			return
		}
		audit := adminBaseAudit(p, r, "session.revoke", "account:"+id, "ok")
		n, auditID, err := deps.Accounts.RevokeSessions(r.Context(), id, audit)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{"account_id": id, "sessions_revoked": n}, auditID)
	}
}

// handleAdminAccountsPending GET /api/admin/accounts/pending——审核队列
// （status=pending 的列表，复用列表面）。
func (s *Server) handleAdminAccountsPending(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		rows, total, err := deps.Accounts.ListAccounts(r.Context(), AdminAccountFilter{
			Status: "pending", Limit: limit, Offset: offset,
		})
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		out := make([]AdminAccountOut, 0, len(rows))
		for i := range rows {
			row := rows[i]
			s.fillAdminCounts(deps, r, &row)
			out = append(out, renderAdminAccount(&row))
		}
		adminListEnvelope(w, out, total, limit, offset)
	}
}

// handleAdminAccountApprove POST /api/admin/accounts/pending/{id}/approve。
//
// **必填组与配额**（§5.2/§5.1）：缺任一项 400 invalid_request 且消息
// 指明缺哪项（uxK 行）。批准 = 状态转 active + 落组 + 落账户级额度，
// 全部在桥的同一事务里（批准半套生效 = 一个无组账户能登录，正是
// "缺任一项"要拦的东西）。审计记选中的组与额度（06-audit §3 批准必带）。
func (s *Server) handleAdminAccountApprove(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := accountIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Groups *[]string   `json:"groups,omitempty"`
			Quota  *AdminQuota `json:"quota,omitempty"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		// 缺项判定先于值域判定：报"缺什么"比报"不对"更有行动性（uxK）。
		if body.Groups == nil || len(*body.Groups) == 0 {
			adminWriteParamErr(w, "groups", "批准必须指定组（groups 缺失或为空）")
			return
		}
		if body.Quota == nil {
			adminWriteParamErr(w, "quota", "批准必须指定配额（quota 缺失）")
			return
		}
		if err := adminValidQuota(body.Quota); err != nil {
			adminFail(w, "quota", err)
			return
		}
		groups := *body.Groups
		audit := adminBaseAudit(p, r, "account.approve", "account:"+id, "ok")
		if b, err := json.Marshal(map[string]any{
			"groups": groups,
			"quota":  body.Quota,
		}); err == nil {
			audit.Detail = b
		}
		// 批准 = PATCH{status:active, groups, quota} 的受控版本；
		// 桥（UpdateAccount）对 pending 行放行 active 迁移并同事务落组/额度。
		in := AdminAccountUpdate{Status: strPtr("active"), Groups: &groups, Quota: body.Quota}
		row, auditID, err := deps.Accounts.UpdateAccount(r.Context(), id, in, audit)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		s.fillAdminCounts(deps, r, row)
		adminWriteOK(w, http.StatusOK, renderAdminAccount(row), auditID)
	}
}

// strPtr 小工具（载荷构造处反复用到 *string）。
func strPtr(v string) *string { return &v }

// handleAdminAccountReject POST /api/admin/accounts/pending/{id}/reject。
//
// 拒绝 = 状态转 rejected（01-accounts §5：rejected 是「从未通过审核」，
// 与 disabled 的「曾经可用后被停用」区分开）。理由可选但**必须落审计**：
// 申请人后续申诉、以及「同一个上游账号被反复申请」的识别都靠它。
//
// 与 approve 对称地走 UpdateAccount（受控的 status 迁移），而不是自己写 SQL：
// 两条路径共用同一个桥方法，状态机的关卡（pending→rejected 合法、
// active→rejected 不合法）只有一份实现。
func (s *Server) handleAdminAccountReject(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := accountIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		reason := strings.TrimSpace(body.Reason)
		audit := adminBaseAudit(p, r, "account.reject", "account:"+id, "ok")
		if b, err := json.Marshal(map[string]any{"reason": reason}); err == nil {
			audit.Detail = b
		}
		status := "rejected"
		in := AdminAccountUpdate{Status: &status}
		row, auditID, err := deps.Accounts.UpdateAccount(r.Context(), id, in, audit)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		s.fillAdminCounts(deps, r, row)
		adminWriteOK(w, http.StatusOK, renderAdminAccount(row), auditID)
	}
}

// TODO(M7-下一轮，已在回报里列给 lead)：
//   - POST /accounts/import（CSV 导入）：逐行创建 + 批次汇总审计（admin.
//     import_batch）+ 一次性密码集合回显；行级失败策略（整单拒 vs 部分
//     成功）分册未定案，需要先定。
//   - POST /accounts/{id}/merge 与 /merge/rollback：合并的
//     account_merges 行 + reversible_until + Key/资产迁移是账户域新逻辑
//     （01-accounts §4），窄接口本轮未定形。
//   - POST /accounts/pending/{id}/reject：rejected 已可经 PATCH 达到；
//     reject 单独端点只是 action 归类差异（account.reject），待注册流
//     落地时与 approve 对称补齐。

// ---------------------------------------------------------------------------
// 账户合并 / 回滚 / 预检（01-accounts §4，02-admin §5.1 的 🔒 危险写）
// ---------------------------------------------------------------------------

// handleAdminAccountMergePrecheck GET /api/admin/accounts/{id}/merge/precheck?target=acc_x
//
// 为什么单独给一个只读端点：文档要求「管理员在合并确认页看到预检结果，
// 只有🔴为空时才能点确认」。没有预检端点，确认页就只能盲签——而合并是
// 不可逆方向的操作（虽有 7 天窗口，但窗口内的用量已经归到 target 了）。
func (s *Server) handleAdminAccountMergePrecheck(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		id, ok := accountIDParam(w, r)
		if !ok {
			return
		}
		target := strings.TrimSpace(r.URL.Query().Get("target"))
		if target == "" {
			adminWriteParamErr(w, "target", "预检必须带目标账户（?target=acc_<n>）")
			return
		}
		if _, err := idgen.Parse(target); err != nil {
			adminWriteParamErr(w, "target", "目标账户 id 形状非法（期望 acc_<数字>）")
			return
		}
		plan, err := deps.Accounts.PrecheckMerge(r.Context(), id, target)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": plan})
	}
}

// handleAdminAccountMerge POST /api/admin/accounts/{id}/merge。
//
// 请求体 {"target_account_id":"acc_x"}（前端 mergeAccount 的形状）。
// 阻塞项非空 → 409 conflict 且消息带具体是哪条凭据撞了（01-accounts §4：
// 同 provider+external_id 是身份重叠，静默合并会把两个自然人变成一个）。
func (s *Server) handleAdminAccountMerge(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := accountIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			TargetAccountID string `json:"target_account_id"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		target := strings.TrimSpace(body.TargetAccountID)
		if target == "" {
			adminWriteParamErr(w, "target_account_id", "必须指定合并目标账户")
			return
		}
		if _, err := idgen.Parse(target); err != nil {
			adminWriteParamErr(w, "target_account_id", "目标账户 id 形状非法（期望 acc_<数字>）")
			return
		}
		if target == id {
			adminWriteParamErr(w, "target_account_id", "不能把账户合并到自己")
			return
		}
		audit := adminBaseAudit(p, r, "account.merge", "account:"+id, "ok")
		if b, err := json.Marshal(map[string]any{"target": target}); err == nil {
			audit.Detail = b
		}
		plan, auditID, err := deps.Accounts.MergeAccounts(r.Context(), id, target, audit)
		if err != nil {
			// 409 时把预检明细一并回出去：管理员要知道「先处理哪条凭据」。
			var ae *auth.Error
			if asAuthError(err, &ae) && ae.Code == auth.CodeConflict {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": map[string]any{
						"message": ae.Msg, "type": "conflict_error",
						"param": "target_account_id", "code": "conflict",
						"details": map[string]any{"blocking": planBlocking(plan)},
					},
				})
				return
			}
			mapAdminError(w, err, "账户")
			return
		}
		adminWriteOK(w, http.StatusOK, mergeResponse(plan, true, false), auditID)
	}
}

// handleAdminAccountMergeRollback POST /api/admin/accounts/{id}/merge/rollback。
//
// 超窗或没有未撤销的合并 → 404（不确认「这里曾有过一次合并」；
// 与「未接线能力不确认存在」同一口径）。
func (s *Server) handleAdminAccountMergeRollback(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := accountIDParam(w, r)
		if !ok {
			return
		}
		audit := adminBaseAudit(p, r, "account.merge_rollback", "account:"+id, "ok")
		plan, auditID, err := deps.Accounts.RollbackMerge(r.Context(), id, audit)
		if err != nil {
			mapAdminError(w, err, "账户")
			return
		}
		adminWriteOK(w, http.StatusOK, mergeResponse(plan, false, true), auditID)
	}
}

// planBlocking 安全取预检的阻塞明细（plan 可能为 nil）。
func planBlocking(plan *AdminMergePlan) []string {
	if plan == nil {
		return []string{}
	}
	return plan.Blocking
}

// mergeResponse 在预检形状上补前端契约声明的 merged / rolled_back 标记。
//
// 为什么用 map 而不是给 AdminMergePlan 加两个恒为 false 的字段：预检响应里
// 出现 "merged": false 会被读成「这次预检的结论是没合并」，而它其实只是
// 「这不是执行结果」。标记只在它所属的那一个响应里存在。
func mergeResponse(plan *AdminMergePlan, merged, rolledBack bool) map[string]any {
	out := map[string]any{
		"source_id": plan.SourceID, "target_id": plan.TargetID,
		"blocking": plan.Blocking, "renamed_keys": plan.RenamedKeys,
		"keys": plan.Keys, "credentials": plan.Credentials,
		"group_relations": plan.GroupRels,
		"source_classes":  plan.SourceClasses, "target_classes": plan.TargetClasses,
	}
	if merged {
		out["merged"] = true
	}
	if rolledBack {
		out["rolled_back"] = true
	}
	return out
}
