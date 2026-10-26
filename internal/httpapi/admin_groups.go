// admin_groups.go —— 组组端点（02-admin §6，task-20 M7）。
//
// GET    /api/admin/groups                      列表
// POST   /api/admin/groups                      新建
// GET    /api/admin/groups/{id}                 详情（成员/模型授权/策略）
// PATCH  /api/admin/groups/{id}                 改基本信息（名称/描述/默认位）
// DELETE /api/admin/groups/{id}                 删组（成员退回默认组；默认组不可删→409）
// POST   /api/admin/groups/{id}/duplicate       复制组
// PUT    /api/admin/groups/{id}/models          **替换**模型授权集合
// PUT    /api/admin/groups/{id}/policies        **替换**策略集合
// POST   /api/admin/groups/{id}/members         增成员
// DELETE /api/admin/groups/{id}/members/{acc}   移出成员
//
// 为什么 models/policies 用 PUT 整体替换：§6 定合并语义（放开取并集、
// 限制取最严、deny 优先）对整个集合求值，局部增删会让"当前生效值"依赖
// 一段难以复盘的历史；整体替换让每次变更都是 A→B 的完整记录。
// 替换是**全量**语义：空数组 = 清空，不是"没给"。
//
// 危险写（§2.3）：改组、删组、设默认组都是界面 🔒 项——API 层不加标记。

package httpapi

import (
	"encoding/json"
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) mountAdminGroups(r chi.Router, deps *AdminDeps) {
	r.Get("/groups", s.handleAdminGroupsList(deps))
	r.Post("/groups", s.handleAdminGroupsCreate(deps))
	r.Get("/groups/{id}", s.handleAdminGroupDetail(deps))
	r.Patch("/groups/{id}", s.handleAdminGroupPatch(deps))
	r.Delete("/groups/{id}", s.handleAdminGroupDelete(deps))
	r.Post("/groups/{id}/duplicate", s.handleAdminGroupDuplicate(deps))
	r.Put("/groups/{id}/models", s.handleAdminGroupModelsPut(deps))
	r.Put("/groups/{id}/policies", s.handleAdminGroupPoliciesPut(deps))
	r.Post("/groups/{id}/members", s.handleAdminGroupMembersAdd(deps))
	r.Delete("/groups/{id}/members/{accountID}", s.handleAdminGroupMemberRemove(deps))
}

// groupIDParam 解组路径参数：idcode 前缀校验（lead 裁决①——拿 acc_1 打
// groups 端点必须在解码层失败，而不是去查错表）。
func groupIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "组")
}

func (s *Server) handleAdminGroupsList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		rows, total, err := deps.Groups.ListGroups(r.Context(), limit, offset)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		out := make([]AdminGroupOut, 0, len(rows))
		for i := range rows {
			out = append(out, renderAdminGroup(&rows[i]))
		}
		adminListEnvelope(w, out, total, limit, offset)
	}
}

func (s *Server) handleAdminGroupDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		id, ok := groupIDParam(w, r)
		if !ok {
			return
		}
		row, err := deps.Groups.GetGroup(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		if row.Policies == nil {
			// 契约 §6.1 里 policies 恒为对象：无策略文档渲染成空对象，
			// 让前端不必区分"没配"与"没行"。
			row.Policies = json.RawMessage(`{}`)
		}
		if row.Members == nil {
			row.Members = []string{}
		}
		if row.Models == nil {
			row.Models = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": row})
	}
}

func (s *Server) handleAdminGroupsCreate(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var body struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			IsDefault   bool            `json:"is_default,omitempty"`
			Models      []string        `json:"models,omitempty"`
			Policies    json.RawMessage `json:"policies,omitempty"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if body.Name == "" {
			adminWriteParamErr(w, "name", "组名不能为空")
			return
		}
		g := AdminGroup{Name: body.Name, Description: body.Description, IsDefault: body.IsDefault}
		audit := adminBaseAudit(p, r, "group.create", "", "ok")
		id, auditID, err := deps.Groups.CreateGroup(r.Context(), g, body.Models, body.Policies, audit)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		// 审计 target 用最终 id（桥返回的对外 id）。
		audit.Target = strPtr("group:" + id)
		if row, err := deps.Groups.GetGroup(r.Context(), id); err == nil {
			adminWriteOK(w, http.StatusCreated, row, auditID)
			return
		}
		g.ID = id
		adminWriteOK(w, http.StatusCreated, renderAdminGroup(&g), auditID)
	}
}

func (s *Server) handleAdminGroupPatch(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := groupIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Name        *string `json:"name,omitempty"`
			Description *string `json:"description,omitempty"`
			IsDefault   *bool   `json:"is_default,omitempty"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if body.Name != nil && *body.Name == "" {
			adminWriteParamErr(w, "name", "组名不能为空串")
			return
		}
		audit := adminBaseAudit(p, r, "group.update", "group:"+id, "ok")
		row, auditID, err := deps.Groups.UpdateGroup(r.Context(), id, body.Name, body.Description, body.IsDefault, audit)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		adminWriteOK(w, http.StatusOK, renderAdminGroup(row), auditID)
	}
}

// handleAdminGroupDelete DELETE /api/admin/groups/{id}。
//
// 默认组不可删 → 409 conflict（ErrAdminDefaultGroupProtected 由桥返回）。
// 删组不删人：成员退回默认组由桥在**同一事务**里做（退回数进审计 Detail）。
func (s *Server) handleAdminGroupDelete(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := groupIDParam(w, r)
		if !ok {
			return
		}
		audit := adminBaseAudit(p, r, "group.delete", "group:"+id, "ok")
		auditID, err := deps.Groups.DeleteGroup(r.Context(), id, audit)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{"id": id, "deleted": true}, auditID)
	}
}

func (s *Server) handleAdminGroupDuplicate(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := groupIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Name string `json:"name,omitempty"`
		}
		// duplicate 允许空 body {}（新名由桥按"<原名> 副本"生成）——
		// adminDecodeBoth 拒空 body，这里给个 {} 兜底读取。
		if r.ContentLength == 0 || r.Header.Get("Content-Length") == "0" {
			_ = p
		} else if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		audit := adminBaseAudit(p, r, "group.duplicate", "group:"+id, "ok")
		row, auditID, err := deps.Groups.DuplicateGroup(r.Context(), id, body.Name, audit)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		adminWriteOK(w, http.StatusCreated, renderAdminGroup(row), auditID)
	}
}

// handleAdminGroupModelsPut PUT /api/admin/groups/{id}/models——整表替换。
func (s *Server) handleAdminGroupModelsPut(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := groupIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Models *[]string `json:"models"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		// 指针必填：整表替换不接受"忘了给"——没给 models 与给空数组是
		// 两件完全不同的事，必须让调用方明确表态。
		if body.Models == nil {
			adminWriteParamErr(w, "models", "PUT 必须携带 models 数组（清空给 []）")
			return
		}
		audit := adminBaseAudit(p, r, "group.models_replace", "group:"+id, "ok")
		old, auditID, err := deps.Groups.ReplaceGroupModels(r.Context(), id, *body.Models, audit)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{
			"group_id": id,
			"models":   *body.Models,
			"replaced": old,
		}, auditID)
	}
}

func (s *Server) handleAdminGroupPoliciesPut(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := groupIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Policies json.RawMessage `json:"policies"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if len(body.Policies) == 0 {
			adminWriteParamErr(w, "policies", "PUT 必须携带 policies 文档（清空给 {}）")
			return
		}
		// 策略文档形状只校验"是 JSON 对象"：键语义属于组域
		// （auth.GroupPolicy 是强类型合并的权威），API 层再验一遍键
		// 就是第二份口径。
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(body.Policies, &probe); err != nil {
			adminWriteParamErr(w, "policies", "policies 必须是 JSON 对象")
			return
		}
		audit := adminBaseAudit(p, r, "group.policies_replace", "group:"+id, "ok")
		old, auditID, err := deps.Groups.ReplaceGroupPolicies(r.Context(), id, body.Policies, audit)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{
			"group_id":      id,
			"policies":      body.Policies,
			"replaced_from": old,
		}, auditID)
	}
}

func (s *Server) handleAdminGroupMembersAdd(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := groupIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			AccountIDs []string `json:"account_ids"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if len(body.AccountIDs) == 0 {
			adminWriteParamErr(w, "account_ids", "account_ids 至少一个")
			return
		}
		// 逐个前缀校验（lead 裁决①）：一个坏 id 整单拒——半途加一半
		// 比全拒更难归因（桥的单事务只防崩溃窗口，不防"手滑"）。
		for _, a := range body.AccountIDs {
			if _, err := idgen.Parse(a); err != nil {
				adminWriteParamErr(w, "account_ids", "账户 id 形状非法："+a)
				return
			}
		}
		audit := adminBaseAudit(p, r, "group.members_add", "group:"+id, "ok")
		auditID, err := deps.Groups.AddGroupMembers(r.Context(), id, body.AccountIDs, audit)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{"group_id": id, "added": body.AccountIDs}, auditID)
	}
}

// handleAdminGroupMemberRemove DELETE /api/admin/groups/{id}/members/{accountID}。
// §3.1：DELETE 只用于可逆移除——移出组随时可再加回，符合。
func (s *Server) handleAdminGroupMemberRemove(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "组存储", deps.Groups != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := groupIDParam(w, r)
		if !ok {
			return
		}
		acc := chi.URLParam(r, "accountID")
		if _, err := idgen.Parse(acc); err != nil {
			adminWriteParamErr(w, "account_id", "账户 id 形状非法（期望 acc_<数字>）")
			return
		}
		audit := adminBaseAudit(p, r, "group.members_remove", "group:"+id, "ok")
		auditID, err := deps.Groups.RemoveGroupMember(r.Context(), id, acc, audit)
		if err != nil {
			mapAdminError(w, err, "组")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{"group_id": id, "removed": acc}, auditID)
	}
}
