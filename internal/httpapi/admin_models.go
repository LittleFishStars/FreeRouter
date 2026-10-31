// admin_models.go —— 模型来源 + 真实模型组 + 抽象模型组端点（02-admin §7/§8，task-20 M7）。
//
// 模型来源（候选集编辑器的「供应商 → 真实模型」两栏数据；只读、不写审计，
// 字段口径见 tmp/candidate-sources-contract.md §1.1）：
//   GET    /api/admin/model-sources              上游 × 真实模型聚合（含全池渠道）
//
// 真实模型：
//   GET    /api/admin/real-models               列表（status/hidden/source/upstream_id 筛选）
//   GET    /api/admin/real-models/{id}          详情
//   POST   /api/admin/real-models/refresh       触发目录刷新（能力未接 → 404）
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
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"errors"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"io"
	"time"
)

func (s *Server) mountAdminModels(r chi.Router, deps *AdminDeps) {
	// 模型来源：它是「上游 × 模型」的聚合视图，不属于真实模型或抽象模型任一组，
	// 但服务的仍是候选集编辑（与 /candidates 同一块界面），所以挂在同一个 mount 里。
	r.Get("/model-sources", s.handleAdminModelSources(deps))

	r.Get("/real-models", s.handleAdminRealModelsList(deps))
	r.Post("/real-models", s.handleAdminRealModelCreate(deps))
	r.Post("/real-models/refresh", s.handleAdminRealModelsRefresh(deps))
	r.Get("/real-models/{id}", s.handleAdminRealModelDetail(deps))
	r.Patch("/real-models/{id}", s.handleAdminRealModelPatch(deps))
	r.Post("/real-models/{id}/exclude", s.handleAdminRealModelExclude(deps))

	r.Get("/abstract-models", s.handleAdminAbstractModelsList(deps))
	r.Post("/abstract-models", s.handleAdminAbstractModelCreate(deps))
	r.Get("/abstract-models/{id}", s.handleAdminAbstractModelDetail(deps))
	r.Patch("/abstract-models/{id}", s.handleAdminAbstractModelPatch(deps))
	r.Put("/abstract-models/{id}/candidates", s.handleAdminAbstractCandidatesPut(deps))
	r.Post("/abstract-models/{id}/dry-run", s.handleAdminAbstractDryRun(deps))
	r.Post("/abstract-models/{id}/connectivity", s.handleAdminAbstractConnectivity(deps))
	r.Get("/abstract-models/{id}/chain", s.handleAdminAbstractChain(deps))
	r.Put("/abstract-models/{id}/deprecation", s.handleAdminAbstractDeprecation(deps))
	r.Get("/abstract-models/{id}/references", s.handleAdminAbstractReferences(deps))
}

// realModelIDParam / abstractModelIDParam：id 形状校验（lead 裁决①，
// 跨资源 id 混用在解码层失败）。
func realModelIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "真实模型")
}

func abstractModelIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "抽象模型")
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
			if _, err := idgen.Parse(u); err != nil {
				adminWriteParamErr(w, "upstream_id", "upstream_id 形状非法（期望 UUID）")
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
// 桥给的统计（新增/消失/变更计数）。未接刷新器 → 404 + not_found
// （lead 裁决：未上线能力不确认存在，与真 404 同形）。
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

// handleAdminRealModelCreate POST /api/admin/real-models——手工登记模型
// （凭据池改版 2026-10-26，tmp/pool-merge-contract.md §1.3）。
//
// 用途：目录自动发现（/v1/models）拿不到、但确实存在的模型，管理员手工
// 落一条 real_models 行（source=override，字段级刷新保护由此生效）。
//
// 校验全部在这一层（§1.3 表）：桥只负责落库与审计。幂等语义：同上游下
// upstream_model_id 已存在时返回既有行（created=false，HTTP 200），
// 弹窗重复提交不产生第二行也不报错——「已登记」就是它想要的结果。
func (s *Server) handleAdminRealModelCreate(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型登记", deps.ModelCreate != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var body struct {
			UpstreamID      string `json:"upstream_id"`
			UpstreamModelID string `json:"upstream_model_id"`
			DisplayName     string `json:"display_name"`
			// ContextWindow 用指针区分「没给」与「给了 0」：0 是非法值，
			// 而缺省是合法（未知上下文，能力匹配按不设限处理）。
			ContextWindow *int64 `json:"context_window"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		// 上游 id 是 UUID 文本；形状非法即 400（不再是 up_<数字>）。
		upNum, err := idgen.Parse(body.UpstreamID)
		if err != nil {
			adminWriteParamErr(w, "upstream_id", "upstream_id 形状非法（期望 UUID）")
			return
		}
		mid := strings.TrimSpace(body.UpstreamModelID)
		if mid == "" {
			adminWriteParamErr(w, "upstream_model_id", "upstream_model_id 不得为空")
			return
		}
		if strings.ContainsAny(mid, " \t\n") {
			adminWriteParamErr(w, "upstream_model_id", "upstream_model_id 不得含空白字符")
			return
		}
		// aggregate/* 恒不可选（01-catalog §5）：手工通道也不能绕过它——
		// 绕过等于给「两层策略无法归因」开一个合法入口。
		if ch, _, ok := strings.Cut(mid, "/"); ok && ch == "aggregate" {
			adminWriteParamErr(w, "upstream_model_id", "aggregate/* 渠道的模型不可登记为候选来源")
			return
		}
		if body.ContextWindow != nil && *body.ContextWindow <= 0 {
			adminWriteParamErr(w, "context_window", "context_window 给了就必须大于 0（未知就不传）")
			return
		}
		audit := adminBaseAudit(p, r, "model.manual_add", "upstream:"+body.UpstreamID, "ok")
		row, created, auditID, err := deps.ModelCreate.CreateRealModel(
			r.Context(), upNum, mid, strings.TrimSpace(body.DisplayName), body.ContextWindow, audit)
		if err != nil {
			mapAdminError(w, err, "真实模型")
			return
		}
		// 新建 201、命中既有 200（幂等）；created 同时进响应供前端区分提示。
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		// adminWriteOK 自带 data 信封，这里给信封里的载荷本体。
		adminWriteOK(w, status, map[string]any{
			"model":   renderAdminRealModel(row),
			"created": created,
		}, auditID)
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
				if _, err := idgen.Parse(c.RealModelID); err != nil {
					adminWriteParamErr(w, "candidates", fmt.Sprintf("第 %d 条候选的 real_model_id 形状非法（期望 UUID）", i+1))
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

// handleAdminAbstractConnectivity POST /api/admin/abstract-models/{id}/connectivity。
//
// 「检验连通性」（2026-10-31 用户定案：直接实际请求一次）：对抽象模型发起一次
// **真实的端到端推理**（max_tokens=1 的 ping），打穿「候选解析 → 凭据 → 出站
// 协议 → 响应解析」全链路。静态干跑（dry-run）与上游存活探测都替代不了这个：
// 凭据失效 / 模型名错 / 协议不兼容，只有真发一次才知道。
//
// 与 /v1/chat/completions 的关系：复用同一套管线（v1Candidates 候选源 +
// v1Upstream 出站 shim + ir 适配器 + 冷却账本），差别只有三处——
//  1. 不写 HTTP 响应流，结果收进结构化应答（逐候选尝试记录）；
//  2. 不落用量结算：probe 的 token 消耗真实发生但不进 requests 表——
//     它不是业务流量；应答里如实带 tokens 供参考；
//  3. 失败同样回传冷却账本（CandidateFailureNotifier）——探测到的失败与
//     真实流量同源，冷却口径不打折。
//
// 逐候选尝试到首个成功为止：一次点击最多消耗「候选数 × max_tokens=1」的
// 额度，换来的是每条路径通不通的完整清单。
func (s *Server) handleAdminAbstractConnectivity(deps *AdminDeps) http.HandlerFunc {
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
		cs, up := s.v1Candidates(), s.v1Upstream()
		if cs == nil || up == nil {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"model": m.Name, "ok": false,
				"reason":   "路由管线未接线（部署态：候选源或出站 shim 为空）",
				"attempts": []any{},
			}})
			return
		}

		// ping 请求：max_tokens=1 把探测成本压到最低；非流式便于整体解析。
		one := 1
		probe := ir.Request{
			Model:    m.Name,
			Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "ping"}}}},
			Sampling: ir.Sampling{MaxOutput: &one},
			Stream:   false,
		}

		// 整体预算：候选循环内部各调用有自己的超时，这里只兜总上限。
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()

		// trace sink：no_candidate / 全失败的「为什么」就在剔除原因里。
		ctx = WithV1Trace(ctx)
		cands, cerr := cs.Candidates(ctx, m.Name, probe)

		type probeAttempt struct {
			Display   string `json:"display"`
			OK        bool   `json:"ok"`
			LatencyMs int64  `json:"latency_ms"`
			Tokens    *int   `json:"tokens,omitempty"`
			Reason    string `json:"reason,omitempty"`
		}
		attempts := []probeAttempt{}
		okTotal := false
		var latency int64
		var tokens *int

		switch {
		case errors.Is(cerr, ErrModelNotFound):
			attempts = append(attempts, probeAttempt{Reason: "模型不可见（不存在 / 已停用 / 已弃用且无替代）"})
		case cerr != nil:
			attempts = append(attempts, probeAttempt{Reason: "候选解析失败：" + cerr.Error()})
		case len(cands) == 0:
			attempts = append(attempts, probeAttempt{Reason: "无可用候选（路由诊断里是逐条剔除原因）"})
		}

		if cerr == nil {
			for _, cand := range cands {
				attempt := probeAttempt{Display: cand.Display}
				start := time.Now()
				call, callErr := up.Call(ctx, cand, probe, false)
				switch {
				case callErr != nil:
					attempt.Reason = callErr.Error()
				case call.Err != nil:
					attempt.Reason = call.Err.Error()
				default:
					raw, rerr := io.ReadAll(io.LimitReader(call.Body, 1<<20))
					_ = call.Body.Close()
					switch {
					case rerr != nil:
						attempt.Reason = "读响应失败：" + rerr.Error()
					default:
						resp, derr := cand.Adapter.DecodeResponse(raw)
						if derr != nil {
							// 原文摘要必须带上：不少「OpenAI 兼容」网关把业务错误
							// 以 HTTP 200 + {code,desc} 信封返回（loomy 讯飞网关
							// 2026-10-31 实测：{"code":"100002","desc":"缺少 token"}），
							// 只报「协议不符」等于没诊断。body 是网关侧响应，
							// 不含凭据，摘录安全。
							snippet := string(raw)
							if len(snippet) > 200 {
								snippet = snippet[:200]
							}
							attempt.Reason = "响应解析失败：" + derr.Error() + "；body: " + snippet
						} else {
							attempt.OK = true
							// Usage 是值类型；指针字段为 nil = 上游没报该项。
							if resp.Usage.InputTokens != nil || resp.Usage.OutputTokens != nil {
								t := 0
								if resp.Usage.InputTokens != nil {
									t += *resp.Usage.InputTokens
								}
								if resp.Usage.OutputTokens != nil {
									t += *resp.Usage.OutputTokens
								}
								attempt.Tokens = &t
								tokens = &t
							}
						}
					}
				}
				attempt.LatencyMs = time.Since(start).Milliseconds()
				attempts = append(attempts, attempt)
				if attempt.OK {
					okTotal = true
					latency = attempt.LatencyMs
					break
				}
				// 失败回传冷却账本——与 v1 流量同源同口径。
				if nf, nOk := cs.(CandidateFailureNotifier); nOk {
					nf.NotifyCandidateFailure(cand)
				}
			}
		}

		// trace 头值（候选剔除原因）随应答下发——「为什么没有候选」的答案。
		var trace string
		if t := V1TraceFrom(ctx); t != nil {
			if v, terr := t.HeaderValue(); terr == nil {
				trace = v
			}
		}
		reason := ""
		if !okTotal && len(attempts) > 0 {
			reason = attempts[len(attempts)-1].Reason
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"model": m.Name, "ok": okTotal, "latency_ms": latency,
			"tokens": tokens, "attempts": attempts, "trace": trace, "reason": reason,
		}})
	}
}

// handleAdminAbstractDryRun POST /abstract-models/{id}/dry-run。
//
// 最小可用版：不重放历史流量（那需要
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
		// 跟边**不看 status**（防御性口径）：replacement 边在 active 节点上
		// 是"绕库写"的坏配置——运行时 resolveChain 不消费它，但保存校验与
		// 预览必须看得见它，否则环检测兜底形同虚设（§8.4 环检测留着的理由
		// 就是"配置路径可能被绕过"）。判据本身仍是同一个"重访即环"。
		if m.ReplacementName == nil || *m.ReplacementName == "" {
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
		if idgen.Valid(in.ReplacementName) {
			// §8.4 的字段名叫 replacement_model_id、例子却给名字，两种都收下。
			// 判型方式随 UUID 改造换了：旧做法看 `am_` 前缀，现在前缀体系已
			// 废弃，改成看形状——合法 UUID 就当 id 查行，否则当模型名查。
			// （抽象模型名不可能长得像 UUID：ux_abstract_models_name 唯一且
			// 是给人读的短词。）
			nid, derr := idgen.Parse(in.ReplacementName)
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
		// 校验 3：环检测——新边 cur→target 成环 ⟺ 从 target 沿既有边能走回
		// cur（"重访即环"判据与 adminWalkChain/resolveChain 同一份实现）。
		// 走不动的环在结构上已被校验 1 挡住（目标必须 active），这里兜的是
		// 绕库写出来的边（active 节点上也可能存在 replacement 指向）。
		names, _, _, _, werr := adminWalkChain(ctx, deps.Models, target.Name)
		if werr != nil {
			mapAdminError(w, werr, "替代链")
			return
		}
		reaches := false
		for _, n := range names {
			if n == cur.Name {
				reaches = true
				break
			}
		}
		if reaches {
			adminFail(w, "replacement_model_id", fmt.Errorf("保存该弃用配置将成环：%s → %s（§8.4 环检测拒绝，回显环路径）", cur.Name, strings.Join(names, " → ")))
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

// ---------------------------------------------------------------------------
// 模型来源（候选集编辑器的「供应商 → 真实模型」两栏数据，契约 §1/§1.1）
// ---------------------------------------------------------------------------

// handleAdminModelSources 是 GET /api/admin/model-sources。
//
// 只读、不写审计（03-admin.md §1.1：读操作不产生审计行）。装配缺失按既有惯例
// 走 deps.need → 404 not_found（不确认能力存在）。
//
// 响应是**裸对象** {"sources":[...],"channels":[...]}，不包管理面的 data 信封：
// 契约 §1.1 把这一块直接标成「响应体（200）」，§3 又写明它不是 ListEnvelope
// （前端 getModelSources 走 getEnvelope 不拆封）。与其它管理端点的 {data:...}
// 不同是**有意的**——照契约来，别顺手给它加信封。
func (s *Server) handleAdminModelSources(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "模型来源", deps.Sources != nil) {
			return
		}
		sources, channels, err := deps.Sources.ListModelSources(r.Context())
		if err != nil {
			mapAdminError(w, err, "模型来源")
			return
		}
		// 排序与空切片规范化都在出站这一处做（而不是让桥顺手再排一遍）：它们是
		// **响应形状**的一部分，装配实现换了也不会漂——两处都做必然出现
		// "桥改了顺序但接口没跟着改"的漂移。
		sortAdminModelSources(sources)
		sources = normalizeModelSources(sources)
		// channels 同 sources：接口实现只要给「去重后的全池渠道」，升序在这一层
		// 定型（前端多选控件的展示顺序就是它）。
		sort.Strings(channels)
		if channels == nil {
			channels = []string{}
		}

		// 裸对象，不包 data 信封：见函数头对契约 §1.1/§3 的说明。
		writeJSON(w, http.StatusOK, map[string]any{
			"sources":  sources,
			"channels": channels,
		})
	}
}

// sortAdminModelSources 按契约 §1.1 排序：contributed 降序 → name 升序 → id 升序。
//
// 末位用**对外 id 字符串**（up_<n>）比较而不是数字：id 是响应里可见的那个值，
// 前端不解码也能复现同一顺序；两条来源撞 name（同名不同 kind）时才会用到它，
// 但"稳定"必须是真的——否则同一份数据两次请求可能给出两种顺序。
// 三个键构成全序（id 唯一），用 SliceStable 是把这个结论写进代码。
func sortAdminModelSources(rows []AdminModelSource) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Contributed != b.Contributed {
			return a.Contributed
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}

// normalizeModelSources 把可空切片补齐成空切片并返回。
//
// 契约里 `[]`（空数组）与 `null` 是两种意思，null 只用于 channel /
// available_instances / last_seen_at 这几个"值本身可能不存在"的字段。少给一个
// [] 会让前端拿到 null 后走另一条分支（协议徽标、模态、多选渠道都按数组消费），
// 而且这种缺失只在空数据下才暴露—正是最难在开发机上撞见的那种。
func normalizeModelSources(rows []AdminModelSource) []AdminModelSource {
	if rows == nil {
		// sources 本身也不能是 null：契约给的就是数组（空池时前端渲染空左栏）。
		rows = []AdminModelSource{}
	}
	for i := range rows {
		if rows[i].Protocols == nil {
			rows[i].Protocols = []string{}
		}
		if rows[i].Credentials == nil {
			rows[i].Credentials = []AdminSourceCredential{}
		}
		if rows[i].Models == nil {
			rows[i].Models = []AdminModelSourceModel{}
		}
		for j := range rows[i].Models {
			m := &rows[i].Models[j]
			if m.Modalities == nil {
				m.Modalities = []string{}
			}
			if m.Efforts == nil {
				m.Efforts = []string{}
			}
			if m.Protocols == nil {
				m.Protocols = []string{}
			}
		}
	}
	return rows
}
