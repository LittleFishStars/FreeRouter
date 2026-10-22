// admin_models.go —— 真实模型组 + 抽象模型组端点（02-admin §7/§8，task-20 M7）。
//
// 真实模型：
//   GET    /api/admin/real-models               列表（status/hidden/source/upstream_id 筛选）
//   GET    /api/admin/real-models/{id}          详情
//   POST   /api/admin/real-models/refresh       触发目录刷新（能力可 nil → 501）
//   PATCH  /api/admin/real-models/{id}         改显示名/分组/能力元数据/隐藏/排序
//   POST   /api/admin/real-models/{id}/exclude  排除/恢复候选资格（body.excluded）
//
// 抽象模型：
//   GET    /api/admin/abstract-models           列表（含候选数）
//   GET    /api/admin/abstract-models/{id}      详情
//   POST   /api/admin/abstract-models           新建
//   PATCH  /api/admin/abstract-models/{id}      改显示名/能力模式/能力声明/排序链/status
//   PUT    /api/admin/abstract-models/{id}/candidates  替换候选集
//   POST   /api/admin/abstract-models/{id}/dry-run     保存前干跑
//   GET    /api/admin/abstract-models/{id}/chain      替代链预览
//   PUT    /api/admin/abstract-models/{id}/deprecation 保存弃用配置（三条硬校验）
//   GET    /api/admin/abstract-models/{id}/references 被哪些组引用
//
// 弃用三条硬校验（§8.4）保存时即时执行，判据与 chain 预览同源一份实现：
//   1. 替代目标存在且 status=active（目标是 deprecated/disabled/不存在 → 拒绝）；
//   2. 替代目标能力逐项 ≥ 原模型（context_window/max_output/tool_calling/vision，
//      §8.4 定四项）；
//   3. 环检测（防御性兜底）——**与 routing/abstract.go resolveChain 同一套
//      拓扑口径**：visited 集合游走、重访即环。运行时命中环降级"无替代"
//      （CycleBroken），保存路径按 §8.4 直接拒绝并回环路径；两者是同一
//      判据的两种处置（配置时拒绝 = 主防线，运行时 visited = 兜底），
//      不是两套环检测。

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/idcode"
)

func (s *Server) mountAdminModels(r chi.Router, deps *AdminDeps) {
	r.Get("/api/admin/real-models", s.handleAdminRealModelsList(deps))
	r.Post("/api/admin/real-models/refresh", s.handleAdminRealModelsRefresh(deps))
	r.Get("/api/admin/real-models/{id}", s.handleAdminRealModelDetail(deps))
	r.Patch("/api/admin/real-models/{id}", s.handleAdminRealModelPatch(deps))
	r.Post("/api/admin/real-models/{id}/exclude", s.handleAdminRealModelExclude(deps))

	r.Get("/api/admin/abstract-models", s.handleAdminAbstractModelsList(deps))
	r.Post("/api/admin/abstract-models", s.handleAdminAbstractModelCreate(deps))
	r.Get("/api/admin/abstract-models/{id}", s.handleAdminAbstractModelDetail(deps))
	r.Patch("/api/admin/abstract-models/{id}", s.handleAdminAbstractModelPatch(deps))
	r.Put("/api/admin/abstract-models/{id}/candidates", s.handleAdminAbstractCandidatesPut(deps))
	r.Post("/api/admin/abstract-models/{id}/dry-run", s.handleAdminAbstractDryRun(deps))
	r.Get("/api/admin/abstract-models/{id}/chain", s.handleAdminAbstractChain(deps))
	r.Put("/api/admin/abstract-models/{id}/deprecation", s.handleAdminAbstractDeprecation(deps))
	r.Get("/api/admin/abstract-models/{id}/references", s.handleAdminAbstractReferences(deps))
}

// realModelIDParam / abstractModelIDParam：idcode 前缀校验（lead 裁决①，
// 跨资源 id 混用在解码层失败）。
func realModelIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "id")
	if _, err := idcode.Decode(idcode.RealModel, raw); err != nil {
		adminWriteParamErr(w, "id", "真实模型 id 形状非法（期望 rm_<数字>）")
		return "", false
	}
	return raw, true
}

func abstractModelIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "id")
	if _, err := idcode.Decode(idcode.AbstractModel, raw); err != nil {
		adminWriteParamErr(w, "id", "抽象模型 id 形状非法（期望 am_<数字>）")
		return "", false
	}
	return raw, true
}

// ---------------------------------------------------------------------------
// 真实模型
// ---------------------------------------------------------------------------

func (s *Server) handleAdminRealModelsList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		var hidden *bool
		if hv := q.Get("hidden"); hv != "" {
			b := hv == "true" || hv == "1"
			hidden = &b
		}
		if st := q.Get("status"); st != "" && !adminRealStatusValid(st) {
			adminWriteParamErr(w, "status", "status 取值须为 active/missing/disabled")
			return
		}
		if src := q.Get("source"); src != "" && src != "auto" && src != "override" {
			adminWriteParamErr(w, "source", "source 取值须为 auto/override")
			return
		}
		var upID string
		if u := q.Get("upstream_id"); u != "" {
			if _, err := idcode.Decode(idcode.Upstream, u); err != nil {
				adminWriteParamErr(w, "upstream_id", "upstream_id 形状非法（期望 up_<数字>）")
				return
			}
			upID = u
		}
		rows, total, err := deps.Models.ListRealModels(r.Context(), q.Get("status"), hidden, q.Get("source"), upID, limit, offset)
		if err != nil {
			mapAdminError(w, err, "真实模型")
			return
		}
		out := make([]AdminRealModelOut, 0, len(rows))
		for i := range rows {
			out = append(out, renderAdminRealModel(&rows[i]))
		}
		adminListEnvelope(w, out, total, limit, offset)
	}
}

func adminRealStatusValid(st string) bool {
	switch st {
	case "active", "missing", "disabled":
		return true
	}
	return false
}

func (s *Server) handleAdminRealModelDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		id, ok := realModelIDParam(w, r)
		if !ok {
			return
		}
		row, err := deps.Models.GetRealModel(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "真实模型")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": renderAdminRealModel(row)})
	}
}

// handleAdminRealModelsRefresh POST /api/admin/real-models/refresh。
//
// 触发 upstream 域的刷新（sidecar models.list → upsert）；结果原样回显
// 桥给的统计（新增/消失/变更计数）。未接刷新器 → 501（不是 404——
// 路由存在、能力未装配，见 ErrAdminNotWired 注释）。
func (s *Server) handleAdminRealModelsRefresh(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "目录刷新器", deps.Refresh != nil) {
			return
		}
		res, err := deps.Refresh.RefreshModels(r.Context())
		if err != nil {
			// 刷新失败是上游侧事实（sidecar 掉了），不是管理请求的错：
			// 503 + upstream_unavailable（入站码在控制台面同表可复用，
			// errors.go 已登记 upstream_unavailable）。
			writeError(w, http.StatusServiceUnavailable, "upstream_error",
				"目录刷新失败："+err.Error(), nil)
			return
		}
		adminWriteOK(w, http.StatusOK, res, "")
	}
}

// realModelPatchKeys 是 PATCH 的真实模型可写白名单（§7.1）。
// 白名单外（qualified_name/status/source/upstream_*）与只读字段同处理：
// DisallowUnknownFields 已拒未知 JSON 键，这里是显式可写集的第二道
// ——status 由 refresh/巡检维护、source 由写行为自动翻 override，
// 它们合法存在于响应体、但绝不能成为写载荷的键。
var realModelPatchKeys = map[string]bool{
	"display_name": true, "group_name": true, "context_window": true,
	"modalities": true, "efforts": true, "hidden": true,
	"sort_order": true, "credit_multiplier": true,
}

func (s *Server) handleAdminRealModelPatch(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := realModelIDParam(w, r)
		if !ok {
			return
		}
		var probe map[string]json.RawMessage
		if _, ok := adminDecodeBoth(w, r, &probe); !ok {
			return
		}
		for k := range probe {
			if !realModelPatchKeys[k] {
				adminWriteParamErr(w, k, "字段 "+k+" 不可写（可写：display_name/group_name/context_window/modalities/efforts/hidden/sort_order/credit_multiplier）")
				return
			}
		}
		if len(probe) == 0 {
			adminWriteParamErr(w, "body", "PATCH 至少要改一个字段")
			return
		}
		audit := adminBaseAudit(p, r, "model.real_update", "real_model:"+id, "ok")
		row, auditID, err := deps.Models.UpdateRealModel(r.Context(), id, probe, audit)
		if err != nil {
			mapAdminError(w, err, "真实模型")
			return
		}
		adminWriteOK(w, http.StatusOK, renderAdminRealModel(row), auditID)
	}
}

// handleAdminRealModelExclude POST /api/admin/real-models/{id}/exclude。
//
// exclude 端点带 excluded 布尔（默认 true）：它是"排除出候选"的开关，
// 恢复候选资格用 excluded=false——同一条 PUT 式动作端点、两态，
// 免得再造一个 /include 破"POST=创建/触发"的方法语义（§3.1）。
func (s *Server) handleAdminRealModelExclude(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := realModelIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Excluded *bool `json:"excluded,omitempty"`
		}
		if r.ContentLength > 0 {
			if _, ok := adminDecodeBoth(w, r, &body); !ok {
				return
			}
		}
		excluded := true
		if body.Excluded != nil {
			excluded = *body.Excluded
		}
		action := "model.real_exclude"
		if !excluded {
			action = "model.real_include"
		}
		audit := adminBaseAudit(p, r, action, "real_model:"+id, "ok")
		row, auditID, err := deps.Models.SetRealModelExcluded(r.Context(), id, excluded, audit)
		if err != nil {
			mapAdminError(w, err, "真实模型")
			return
		}
		adminWriteOK(w, http.StatusOK, renderAdminRealModel(row), auditID)
	}
}

// ---------------------------------------------------------------------------
// 抽象模型
// ---------------------------------------------------------------------------

func (s *Server) handleAdminAbstractModelsList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		rows, total, err := deps.Models.ListAbstractModels(r.Context(), limit, offset)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		out := make([]AdminAbstractModelOut, 0, len(rows))
		for i := range rows {
			out = append(out, renderAdminAbstractModel(&rows[i]))
		}
		adminListEnvelope(w, out, total, limit, offset)
	}
}

func (s *Server) handleAdminAbstractModelDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		id, ok := abstractModelIDParam(w, r)
		if !ok {
			return
		}
		row, err := deps.Models.GetAbstractModel(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		cands, err := deps.Models.ListCandidates(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "候选集")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data":       renderAdminAbstractModel(row),
			"candidates": cands,
		})
	}
}

// adminStrategyValues 是排序链元素闭集合（§8.1）：round-robin 只允许
// 作末位平局决胜。
var adminStrategyValues = map[string]bool{
	"expiry-first": true, "balance": true, "speed": true, "price": true,
}

// adminValidStrategy 校验排序链（表驱动，新增策略只改这里一处）。
func adminValidStrategy(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil // 没给 = 不动 / 新建用默认
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return fmt.Errorf("strategy 必须是字符串数组")
	}
	if len(arr) == 0 {
		return fmt.Errorf("strategy 不能为空数组（默认 [\"expiry-first\",\"balance\"]）")
	}
	for i, v := range arr {
		if v == "round-robin" {
			if i != len(arr)-1 {
				return fmt.Errorf("round-robin 只能追加在末位作平局决胜")
			}
			continue
		}
		if !adminStrategyValues[v] {
			return fmt.Errorf("strategy 含未知策略 %q（expiry-first/balance/speed/price，round-robin 仅限末位）", v)
		}
	}
	return nil
}

func (s *Server) handleAdminAbstractModelCreate(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var body struct {
			Name           string               `json:"name"`
			DisplayName    string               `json:"display_name"`
			CapabilityMode string               `json:"capability_mode,omitempty"`
			Capabilities   json.RawMessage      `json:"capabilities,omitempty"`
			Strategy       json.RawMessage      `json:"strategy,omitempty"`
			Note           string               `json:"note,omitempty"`
			Candidates     []AdminCandidateItem `json:"candidates,omitempty"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			adminWriteParamErr(w, "name", "name 不能为空（对外模型名，创建后不可改）")
			return
		}
		if body.DisplayName == "" {
			body.DisplayName = body.Name
		}
		if body.CapabilityMode == "" {
			body.CapabilityMode = "strict"
		}
		if body.CapabilityMode != "strict" && body.CapabilityMode != "filter" {
			adminWriteParamErr(w, "capability_mode", "capability_mode 须为 strict/filter")
			return
		}
		if err := adminValidStrategy(body.Strategy); err != nil {
			adminFail(w, "strategy", err)
			return
		}
		if len(body.Strategy) == 0 {
			body.Strategy = json.RawMessage(`["expiry-first","balance"]`)
		}
		m := AdminAbstractModel{
			Name: body.Name, DisplayName: body.DisplayName,
			CapabilityMode: body.CapabilityMode, Capabilities: body.Capabilities,
			Strategy: body.Strategy, Status: "active",
		}
		if body.Note != "" {
			m.Note = strPtr(body.Note)
		}
		audit := adminBaseAudit(p, r, "model.abstract_create", "abstract_model:"+body.Name, "ok")
		id, auditID, err := deps.Models.CreateAbstractModel(r.Context(), m, body.Candidates, audit)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		m.ID = id
		adminWriteOK(w, http.StatusCreated, renderAdminAbstractModel(&m), auditID)
	}
}

// abstractPatchKeys 是 PATCH 的抽象模型可写白名单（§8.1；status 走
// SetAbstractStatus 受控迁移，name 创建后不可改）。
var abstractPatchKeys = map[string]bool{
	"display_name": true, "capability_mode": true, "capabilities": true,
	"strategy": true, "status": true, "note": true, "dry_run": true,
}

func (s *Server) handleAdminAbstractModelPatch(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := abstractModelIDParam(w, r)
		if !ok {
			return
		}
		var probe map[string]json.RawMessage
		if _, ok := adminDecodeBoth(w, r, &probe); !ok {
			return
		}
		for k := range probe {
			if !abstractPatchKeys[k] {
				adminWriteParamErr(w, k, "字段 "+k+" 不可写（name 创建后不可改；候选集与弃用走各自端点）")
				return
			}
		}
		if len(probe) == 0 {
			adminWriteParamErr(w, "body", "PATCH 至少要改一个字段")
			return
		}
		ctx := r.Context()

		// status 迁移单独拦（弃用单向：deprecated→active 拒绝——§8.1
		// "改名/回退的正确手段是弃用+替代链"）。
		if stRaw, has := probe["status"]; has {
			var st string
			if err := json.Unmarshal(stRaw, &st); err != nil {
				adminWriteParamErr(w, "status", "status 必须是字符串")
				return
			}
			if st != "active" && st != "disabled" {
				adminWriteParamErr(w, "status", "PATCH 只能设 active（撤销弃用）/disabled；deprecated 走 /deprecation 端点")
				return
			}
			cur, err := deps.Models.GetAbstractModel(ctx, id)
			if err != nil {
				mapAdminError(w, err, "抽象模型")
				return
			}
			if st == "active" && cur.Status == "deprecated" {
				// deprecated→active 拒绝 = 单向性。disabled→active 允许
				// （禁用是可逆止血，01-accounts 同款语义）。
				adminWriteParamErr(w, "status", "弃用是单向的，不能从 deprecated 回 active（04-deprecation：撤销弃用不在本轮端点表）")
				return
			}
			audit := adminBaseAudit(p, r, "model.abstract_status", "abstract_model:"+id, "ok")
			auditID, err := deps.Models.SetAbstractStatus(ctx, id, st, audit)
			if err != nil {
				mapAdminError(w, err, "抽象模型")
				return
			}
			delete(probe, "status")
			if len(probe) == 0 {
				row, err := deps.Models.GetAbstractModel(ctx, id)
				if err != nil {
					mapAdminError(w, err, "抽象模型")
					return
				}
				adminWriteOK(w, http.StatusOK, renderAdminAbstractModel(row), auditID)
				return
			}
			// probe 还有别的键：继续走下面的字段更新（两次事务、各自审计，
			// 状态迁移与字段修改本就是两个动作）。
		}
		if cm, has := probe["capability_mode"]; has {
			var v string
			_ = json.Unmarshal(cm, &v)
			if v != "strict" && v != "filter" {
				adminWriteParamErr(w, "capability_mode", "capability_mode 须为 strict/filter")
				return
			}
		}
		if strat, has := probe["strategy"]; has {
			if err := adminValidStrategy(strat); err != nil {
				adminFail(w, "strategy", err)
				return
			}
		}
		var freshDryRun json.RawMessage
		if dr, has := probe["dry_run"]; has {
			freshDryRun = dr
			delete(probe, "dry_run")
		}
		audit := adminBaseAudit(p, r, "model.abstract_update", "abstract_model:"+id, "ok")
		row, auditID, err := deps.Models.UpdateAbstractModel(ctx, id, probe, freshDryRun, audit)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		adminWriteOK(w, http.StatusOK, renderAdminAbstractModel(row), auditID)
	}
}

// handleAdminAbstractCandidatesPut PUT /abstract-models/{id}/candidates——
// 整表替换（§6 同一条"整体替换"理由；available_instances 请求里给了忽略）。
func (s *Server) handleAdminAbstractCandidatesPut(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := abstractModelIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Candidates *[]AdminCandidateItem `json:"candidates"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if body.Candidates == nil {
			adminWriteParamErr(w, "candidates", "PUT 必须携带 candidates 数组（清空给 []）")
			return
		}
		for i := range *body.Candidates {
			c := &(*body.Candidates)[i]
			c.AvailableInstances = 0 // 响应字段归零，绝不让请求值穿透到桥
			if c.RealModelID != "" {
				if _, err := idcode.Decode(idcode.RealModel, c.RealModelID); err != nil {
					adminWriteParamErr(w, "candidates", fmt.Sprintf("第 %d 条候选的 real_model_id 形状非法（期望 rm_<数字>）", i+1))
					return
				}
			} else {
				adminWriteParamErr(w, "candidates", fmt.Sprintf("第 %d 条候选缺 real_model_id", i+1))
				return
			}
		}
		audit := adminBaseAudit(p, r, "model.candidates_replace", "abstract_model:"+id, "ok")
		old, auditID, err := deps.Models.ReplaceCandidates(r.Context(), id, *body.Candidates, audit)
		if err != nil {
			mapAdminError(w, err, "候选集")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{
			"abstract_model_id": id,
			"candidates":        *body.Candidates,
			"replaced":          old,
		}, auditID)
	}
}

// ---------------------------------------------------------------------------
// 干跑与替代链（§8.3 / §8.4）
// ---------------------------------------------------------------------------

// handleAdminAbstractDryRun POST /abstract-models/{id}/dry-run。
//
// 最小可用版（任务书允许 501 或最小实现）：不重放历史流量（那需要
// requests 表的读能力，本轮 AdminModelStore 不含），而是对**当前声明
// 候选集**做静态干跑——每条候选按 available_instances 判断有没有凭据
// 支撑、按替代链是否可达 active 判断路由终局。这恰好覆盖 §8.3 想防的
// 故障："抽象模型策略配错 → 全站不可用"的最主要形态（候选全空 /
// 弃用链指向死路）。sample_size 接受但未使用（保留字段位，真实流量
// 重放属后续增量）。响应形状对齐 §8.3 例子。
func (s *Server) handleAdminAbstractDryRun(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		id, ok := abstractModelIDParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Strategy   json.RawMessage `json:"strategy,omitempty"`
			SampleSize int             `json:"sample_size,omitempty"`
		}
		if r.ContentLength > 0 {
			if _, ok := adminDecodeBoth(w, r, &body); !ok {
				return
			}
		}
		if err := adminValidStrategy(body.Strategy); err != nil {
			adminFail(w, "strategy", err)
			return
		}
		ctx := r.Context()
		m, err := deps.Models.GetAbstractModel(ctx, id)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		cands, err := deps.Models.ListCandidates(ctx, id)
		if err != nil {
			mapAdminError(w, err, "候选集")
			return
		}
		chain, _, _, _, cerr := adminWalkChain(ctx, deps.Models, m.Name)
		// 静态结论：候选条数按"有无可用凭据实例"分桶；链断（终点非
		// active 或成环）整体记 failure——干跑的价值就在把"配了但永远
		// 选不中 / 路由到死路"变成保存前的红灯。
		var reachable, noCand int64
		for _, c := range cands {
			if c.AvailableInstances > 0 {
				reachable++
			} else {
				noCand++
			}
		}
		failures := []map[string]any{}
		if cerr != nil {
			failures = append(failures, map[string]any{"reason": "chain_broken", "count": len(cands)})
			reachable = 0
		} else if noCand > 0 && reachable == 0 {
			failures = append(failures, map[string]any{"reason": "no_candidate", "count": noCand})
		} else if noCand > 0 {
			failures = append(failures, map[string]any{"reason": "no_candidate", "count": noCand})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"model":         m.Name,
			"reachable":     reachable,
			"no_candidate":  noCand,
			"fallback_used": 0,
			"failures":      failures,
			"chain":         chain,
			"note":          "静态干跑：候选按 available_instances 分桶，不重放历史流量（sample_size 未消费）",
		}})
	}
}

// handleAdminAbstractChain GET /api/admin/abstract-models/{id}/chain——
// 替代链预览（§8.4 响应形状：chain/hops/final_active/warning/capability_check）。
func (s *Server) handleAdminAbstractChain(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		id, ok := abstractModelIDParam(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		m, err := deps.Models.GetAbstractModel(ctx, id)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		names, hops, finalActive, cycle, werr := adminWalkChain(ctx, deps.Models, m.Name)
		if werr != nil {
			mapAdminError(w, werr, "替代链")
			return
		}
		caps, err := deps.Models.CapabilitySnapshot(ctx)
		if err != nil {
			mapAdminError(w, err, "能力快照")
			return
		}
		check := adminCapabilityCheck(caps, m.Name, finalActive)
		out := map[string]any{
			"chain":            names,
			"hops":             hops,
			"final_active":     finalActive,
			"capability_check": check,
		}
		if cycle {
			// warning 的唯一用途（§8.4）：运行时兜底命中环的告警位。
			out["warning"] = "替代链存在环：" + strings.Join(names, " → ")
		} else {
			out["warning"] = nil
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out})
	}
}

// adminWalkChain 从 from 名沿 replacement 链走到终局。
//
// visited 集合 = 与 routing/abstract.go resolveChain 同一套拓扑口径
// （lead 确认：不写第二套环检测算法——判据同为"重访即环"；保存路径
// 拒绝、预览路径回 warning，是同一判据的两种处置而非两种判定）。
// 返回 (链上名字含起点, hops=跳数, 终点名, 成环标志, 读存储错误)。
// 终点为 disabled 或链中查不到目标（桥数据不一致）不算环——hops 走到
// 哪儿算哪儿，由调用方按 finalActive 的 status 判定。
func adminWalkChain(ctx context.Context, store AdminModelStore, from string) ([]string, int, string, bool, error) {
	chain := []string{from}
	visited := map[string]bool{from: true}
	cur := from
	for {
		m, err := store.GetAbstractModelByName(ctx, cur)
		if err != nil {
			if err == ErrAdminModelNotFound && cur != from {
				// 链指向了不存在的行（数据被绕过 API 改动）：停在上一跳。
				return chain, len(chain) - 1, cur, false, nil
			}
			return chain, len(chain) - 1, cur, false, err
		}
		if m.ReplacementName == nil || *m.ReplacementName == "" || m.Status != "deprecated" {
			return chain, len(chain) - 1, cur, false, nil
		}
		next := *m.ReplacementName
		if visited[next] {
			chain = append(chain, next) // 回显闭环路径（§8.4 拒绝时"返回环路径"）
			return chain, len(chain) - 1, next, true, nil
		}
		visited[next] = true
		chain = append(chain, next)
		cur = next
	}
}

// adminCapabilityGE 判断替代目标的声明能力是否逐项 ≥ 原模型。
//
// 四项比较（§8.4：context_window/max_output/tool_calling/vision——
// 04-deprecation 的"这四项逐项比较"定案）。
// **未知取向 = 按不满足处理（fail-closed）**：02-abstract.md §4.3 已把
// "能力未知按不支持处理"定为严格模式取向——原模型声明 vision:true 而
// 替代目标没写 vision（未知），不能当作"它也满足"：让一个未声明视觉
// 的名字接走视觉流量，正是 §8.4 校验要防的事故。数值项同理：目标缺
// context_window 而原模型有 → 未知 = 不满足。两边都缺该字段 = 该维度
// 无要求，视为满足（没有需求就没有比较）。
func adminCapabilityGE(caps map[string]json.RawMessage, from, to string) (ok bool, checks map[string]bool, missingFields []string) {
	checks = map[string]bool{}
	fromDoc := capDocOf(caps, from)
	toDoc := capDocOf(caps, to)
	if fromDoc == nil || toDoc == nil {
		// 任一侧能力文档整体缺失：无法逐项比较 → 拒绝保存（未知=不满足）。
		return false, checks, []string{"capabilities_doc"}
	}
	// 数值项：目标 ≥ 源。
	for _, k := range []string{"context_window", "max_output"} {
		fv, fok := numAt(fromDoc, k)
		tv, tok := numAt(toDoc, k)
		switch {
		case !fok:
			checks[k] = true // 源没声明 = 无要求
		case !tok:
			checks[k] = false
			missingFields = append(missingFields, to+"."+k)
		default:
			checks[k] = tv >= fv
			if !checks[k] {
				missingFields = append(missingFields, fmt.Sprintf("%s.%s(%d<%d)", to, k, tv, fv))
			}
		}
	}
	// 布尔项：源为 true 时目标必须 true。
	for _, k := range []string{"tool_calling", "vision"} {
		fv, fok := boolAt(fromDoc, k)
		tv, tok := boolAt(toDoc, k)
		switch {
		case !fok || !fv:
			checks[k] = true // 源未要求
		case !tok || !tv:
			checks[k] = false // 未知=不支持：目标缺字段与目标 false 同判
			missingFields = append(missingFields, to+"."+k)
		default:
			checks[k] = true
		}
	}
	for _, v := range checks {
		if !v {
			return false, checks, missingFields
		}
	}
	return true, checks, nil
}

// adminCapabilityCheck 组装 chain 预览的 capability_check 对象。
func adminCapabilityCheck(caps map[string]json.RawMessage, from, to string) map[string]any {
	ok, checks, missing := adminCapabilityGE(caps, from, to)
	out := map[string]any{"ok": ok, "checks": checks}
	if len(missing) > 0 {
		out["failing"] = missing
	}
	return out
}

func capDocOf(caps map[string]json.RawMessage, name string) map[string]any {
	raw, ok := caps[name]
	if !ok || len(raw) == 0 {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return doc
}

func numAt(doc map[string]any, key string) (int64, bool) {
	v, ok := doc[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

func boolAt(doc map[string]any, key string) (bool, bool) {
	v, ok := doc[key]
	if !ok || v == nil {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// handleAdminAbstractDeprecation PUT /api/admin/abstract-models/{id}/deprecation。
//
// 三条硬校验在保存时即时执行（§8.4"配置时即时校验是主防线"）：
//  1. 替代目标存在且 status=active；
//  2. 替代目标能力逐项 ≥ 原模型（失败返回哪一项不合格）；
//  3. 环检测（visited 拓扑口径同 adminWalkChain，回环路径拒绝）。
//
// deprecated→active 不在这里撤销（单向性由 PATCH 端点把关）。
func (s *Server) handleAdminAbstractDeprecation(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := abstractModelIDParam(w, r)
		if !ok {
			return
		}
		var in AdminDeprecationUpdate
		if _, ok := adminDecodeBoth(w, r, &in); !ok {
			return
		}
		ctx := r.Context()
		cur, err := deps.Models.GetAbstractModel(ctx, id)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		if in.Status != "deprecated" {
			adminWriteParamErr(w, "status", "本端点只接受 status=\"deprecated\"（弃用单向；回 active 不在本轮端点表）")
			return
		}
		if in.ReplacementName == "" {
			adminWriteParamErr(w, "replacement_model_id", "弃用必须指定替代目标")
			return
		}
		if strings.HasPrefix(in.ReplacementName, idcode.AbstractModel+"_") {
			// 给了 am_<数字> 形态：解出数字后按 id 查行（§8.4 字段名叫
			// replacement_model_id，例子却给名字——两种都收下，前缀判型）。
			nid, derr := idcode.Decode(idcode.AbstractModel, in.ReplacementName)
			if derr != nil {
				adminWriteParamErr(w, "replacement_model_id", "替代目标 id 形状非法")
				return
			}
			_ = nid
			target, terr := deps.Models.GetAbstractModel(ctx, in.ReplacementName)
			if terr != nil {
				if terr == ErrAdminModelNotFound {
					adminFail(w, "replacement_model_id", fmt.Errorf("替代目标 %s 不存在", in.ReplacementName))
					return
				}
				mapAdminError(w, terr, "抽象模型")
				return
			}
			in.ReplacementName = target.Name
		}
		// 校验 1：目标存在且 active。
		target, err := deps.Models.GetAbstractModelByName(ctx, in.ReplacementName)
		if err == ErrAdminModelNotFound {
			adminFail(w, "replacement_model_id", fmt.Errorf("替代目标 %q 不存在", in.ReplacementName))
			return
		}
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		if target.Status != "active" {
			adminFail(w, "replacement_model_id", fmt.Errorf("替代目标 %q 状态为 %s（必须是 active——目标是 deprecated 会造成死路链、disabled 等于把流量导向 404）", in.ReplacementName, target.Status))
			return
		}
		if in.ReplacementName == cur.Name {
			adminFail(w, "replacement_model_id", fmt.Errorf("替代目标不能是自己（自环）"))
			return
		}
		// 校验 2：能力逐项 ≥（未知=不满足，取向注释见 adminCapabilityGE）。
		caps, err := deps.Models.CapabilitySnapshot(ctx)
		if err != nil {
			mapAdminError(w, err, "能力快照")
			return
		}
		if okGE, _, failing := adminCapabilityGE(caps, cur.Name, target.Name); !okGE {
			adminFail(w, "replacement_model_id", fmt.Errorf("替代目标能力不及原模型，不合格项：%s", strings.Join(failing, ", ")))
			return
		}
		// 校验 3：环检测——从目标继续沿**已有**链走，若回访 cur 即新边
		// cur→target 将成环（visited 口径同 adminWalkChain/resolveChain）。
		_, _, _, cycle, werr := adminWalkChain(ctx, deps.Models, target.Name)
		if werr != nil {
			mapAdminError(w, werr, "替代链")
			return
		}
		if cycle {
			loopPath, _, _, _, _ := adminWalkChain(ctx, deps.Models, cur.Name)
			// cur.Name 的链此时含 cur→target→…→cur；回显闭环。
			adminFail(w, "replacement_model_id", fmt.Errorf("保存该弃用配置将成环：%s（链上必须全部 active 终点，§8.4 环检测拒绝）", strings.Join(loopPath, " → ")))
			return
		}
		if in.DeprecatedAt == nil {
			now := adminEpochNow()
			in.DeprecatedAt = &now
		}
		audit := adminBaseAudit(p, r, "model.abstract_deprecate", "abstract_model:"+id, "ok")
		if b, err := json.Marshal(map[string]any{
			"replacement":   in.ReplacementName,
			"deprecated_at": in.DeprecatedAt,
			"sunset_at":     in.SunsetAt,
		}); err == nil {
			audit.Detail = b
		}
		auditID, err := deps.Models.SetDeprecation(ctx, id, in, audit)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{"abstract_model_id": id, "status": "deprecated",
			"replacement_model_id": in.ReplacementName}, auditID)
	}
}

// handleAdminAbstractReferences GET /api/admin/abstract-models/{id}/references。
func (s *Server) handleAdminAbstractReferences(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型存储", deps.Models != nil) {
			return
		}
		id, ok := abstractModelIDParam(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		m, err := deps.Models.GetAbstractModel(ctx, id)
		if err != nil {
			mapAdminError(w, err, "抽象模型")
			return
		}
		groups, err := deps.Models.ReferencingGroups(ctx, m.Name)
		if err != nil {
			mapAdminError(w, err, "组引用")
			return
		}
		out := make([]AdminGroupOut, 0, len(groups))
		for i := range groups {
			out = append(out, renderAdminGroup(&groups[i]))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out})
	}
}
