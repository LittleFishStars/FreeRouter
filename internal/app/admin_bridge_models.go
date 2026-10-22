// admin_bridge_models.go —— AdminModelStore → store.ModelRepo（task-23）。
//
// 桥要点：
//   - 真实模型/抽象模型 id 出入站走 idcode(rm_/am_)。
//   - 抽象模型的 capabilities 没有存储列（0001 的 abstract_models 只有
//     strategy/capability_mode/note 等）——能力声明存在 note 列里的
//     JSON 文档（前缀标记 admin_caps:{}）。为什么用 note 而不是新列：
//     不越界改 store 是本任务铁律；note 本就是自由文本元数据位，
//     桥读侧解析失败时按「无能力声明」处理（02-abstract §4.3 未知=不支持
//     的取向由 httpapi 判定层执行，桥只保证文档进出一致）。
//   - CapabilitySnapshot：从全部抽象模型的 note 能力文档汇总（任务书
//     允许的替代口径——存储无 capabilities 列）。
//   - SetDeprecation：DeprecateAbstractModel 收 replacement **id**——
//     桥把名字解析回 id（httpapi 层校验过名字存在且 active）。
//   - 候选 available_instances：真实展开需要凭据/渠道运行时状态（M6 的
//     routing 域知识），本轮桥恒置 0（声明式候选的可信静态面），
//     dry-run 的静态分桶按 0 桶处理——已在回报中列为已知限制。
//
// store 缺口（回报 Lead 转达 storage）：
//   ① UpdateRealModel 的部分列（display_name/group_name/context_window/
//      modalities/efforts/sort_order/credit_multiplier/hidden）——只有
//      UpsertRealModel 整行替换可用（桥按「读-改-写」组合，走 WithTx）；
//   ② SetRealModelExcluded / SetRealModelSource（人工改写翻 override）——
//      同样经 UpsertRealModel 组合；
//   ③ abstract_models 的 PATCH（display_name/capability_mode/strategy）——
//      无单列 UPDATE，用「读-改-写 CreateAbstractModel 不行（会 INSERT）」，
//      退化为仅支持 status/弃用/候选集，其余字段返回占位错误。

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/idcode"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// capsNotePrefix 是能力声明文档在 note 列里的标记前缀。
const capsNotePrefix = "admin_caps:"

type modelsBridge struct{ b *adminBridge }

// realModelToAdmin 翻真实模型行。
func realModelToAdmin(m *store.RealModel) *httpapi.AdminRealModel {
	out := &httpapi.AdminRealModel{
		ID:                encodeID(idcode.RealModel, m.ID),
		DisplayName:       m.DisplayName,
		UpstreamID:        encodeID(idcode.Upstream, m.UpstreamID),
		UpstreamModelID:   m.UpstreamModelID,
		GroupName:         m.GroupName,
		Channel:           m.Channel,
		ContextWindow:     m.ContextWindow,
		Status:            string(m.Status),
		Hidden:            m.Hidden,
		SortOrder:         m.SortOrder,
		Source:            m.Source,
		LastSeenAt:        m.LastSeenAt,
		MissingSince:      m.MissingSince,
		ExcludedFromCandidates: m.ExcludedFromCandidates,
	}
	// qualified_name（§7.1 只读）：dsh:渠道/上游模型 id——渠道不可知时
	// 退化为 dsh:上游模型 id（桥不发明渠道）。
	qualified := "dsh:" + m.UpstreamModelID
	if m.Channel != nil && *m.Channel != "" {
		qualified = "dsh:" + *m.Channel + "/" + m.UpstreamModelID
	}
	out.QualifiedName = qualified
	if m.Modalities != nil {
		out.Modalities = json.RawMessage(*m.Modalities)
	}
	if m.Efforts != nil {
		out.Efforts = json.RawMessage(*m.Efforts)
	}
	return out
}

// modelsLookup 是 absModelToAdmin 反查替代目标名的通道（包级注入，
// init 时指向 modelsBridge 的实现——避免在纯翻译函数上背 ctx/receiver）。
var modelsLookup func(id int64) (string, error)

// absModelToAdmin 翻抽象模型行（能力文档从 note 解析）。
func absModelToAdmin(m *store.AbstractModel) *httpapi.AdminAbstractModel {
	out := &httpapi.AdminAbstractModel{
		ID:             encodeID(idcode.AbstractModel, m.ID),
		Name:           m.Name,
		DisplayName:    m.DisplayName,
		CapabilityMode: m.CapabilityMode,
		Status:         string(m.Status),
		DeprecatedAt:   m.DeprecatedAt,
		SunsetAt:       m.SunsetAt,
	}
	if m.Strategy != "" {
		// 存储侧是 TEXT（逗号分隔或 JSON 都可能——归一成 JSON 数组输出）。
		out.Strategy = normalizeStrategyJSON(m.Strategy)
	}
	if m.ReplacementModelID != nil && modelsLookup != nil {
		// 替代目标按 id 存，对外回名字（§8.1 replacement 是名字形态）。
		if rep, err := modelsLookup(*m.ReplacementModelID); err == nil && rep != "" {
			out.ReplacementName = &rep
		}
	}
	if m.Note != nil && strings.HasPrefix(*m.Note, capsNotePrefix) {
		out.Capabilities = json.RawMessage(strings.TrimPrefix(*m.Note, capsNotePrefix))
	} else if m.Note != nil {
		out.Note = m.Note
	}
	return out
}

// ReplacementName 解析替代目标 id → 名字（无目标返回空串）。
// ctxOf 是桥内读名字的轻量通道（absModelToAdmin 无 ctx 参数——改名会
// 牵动 httpapi 接口签名；用 context.Background 兜底，链路取消由外层写
// 路径承担，读路径容许）。
// replacementNameOf 把替代目标 id 反查成名字（absModelToAdmin 经包级
// modelsLookup 调用；newAdminBridge 绑定）。
func (mb *modelsBridge) replacementNameOf(id int64) (string, error) {
	rep, err := mb.b.repos.Models.GetAbstractModel(context.Background(), id)
	if err != nil {
		return "", err
	}
	return rep.Name, nil
}

// normalizeStrategyJSON 把存储的 strategy 文本归一成 JSON 数组。
// 存的是逗号分隔（store 默认 'expiry-first'）——拆分后转 JSON；
// 已经是 JSON 数组形状则原样。
func normalizeStrategyJSON(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		return json.RawMessage(s)
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	b, _ := json.Marshal(out)
	return json.RawMessage(b)
}

func (mb *modelsBridge) ListRealModels(ctx context.Context, status string, hidden *bool, source, upstreamID string, limit, offset int) ([]httpapi.AdminRealModel, int64, error) {
	// store 缺 ListRealModels（全量）——按上游聚合：ListUpstreams 后逐上游
	// ListRealModelsByUpstream，内存里再过筛（任务书允许的组合实现）。
	ups, err := mb.b.repos.Models.ListUpstreams(ctx)
	if err != nil {
		return nil, 0, mapStoreErr(err)
	}
	var rows []httpapi.AdminRealModel
	for _, up := range ups {
		if upstreamID != "" {
			if encodeID(idcode.Upstream, up.ID) != upstreamID {
				continue
			}
		}
		rms, err := mb.b.repos.Models.ListRealModelsByUpstream(ctx, up.ID)
		if err != nil {
			return nil, 0, mapStoreErr(err)
		}
		for i := range rms {
			rows = append(rows, *realModelToAdmin(&rms[i]))
		}
	}
	var filtered []httpapi.AdminRealModel
	for _, r := range rows {
		if status != "" && r.Status != status {
			continue
		}
		if source != "" && r.Source != source {
			continue
		}
		if hidden != nil && r.Hidden != *hidden {
			continue
		}
		filtered = append(filtered, r)
	}
	total := int64(len(filtered))
	lo, hi := pageBounds(len(filtered), limit, offset)
	return filtered[lo:hi], total, nil
}

func (mb *modelsBridge) GetRealModel(ctx context.Context, id string) (*httpapi.AdminRealModel, error) {
	rid, err := decodeID(idcode.RealModel, id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	m, err := mb.b.repos.Models.GetRealModel(ctx, rid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return realModelToAdmin(m), nil
}

func (mb *modelsBridge) UpdateRealModel(ctx context.Context, id string, patch map[string]json.RawMessage, audit httpapi.AdminAuditEntry) (*httpapi.AdminRealModel, string, error) {
	rid, err := decodeID(idcode.RealModel, id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var auditID string
	var updated *store.RealModel
	err = mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		cur, err := tx.Models.GetRealModel(ctx, rid)
		if err != nil {
			return err
		}
		next := *cur
		// §7.1：人工改写后 source 翻 override（桥的责任， UpsertRealModel
		// 的覆盖规则在 override 行保护人工列——组合同纪律）。
		next.Source = "override"
		if v, has := patch["display_name"]; has {
			_ = json.Unmarshal(v, &next.DisplayName)
		}
		if v, has := patch["group_name"]; has {
			var gs string
			if err := json.Unmarshal(v, &gs); err == nil {
				next.GroupName = adminStrPtr(gs)
			}
		}
		if v, has := patch["context_window"]; has {
			var n int64
			if err := json.Unmarshal(v, &n); err == nil {
				next.ContextWindow = &n
			}
		}
		if v, has := patch["modalities"]; has {
			next.Modalities = adminStrPtr(string(v))
		}
		if v, has := patch["efforts"]; has {
			next.Efforts = adminStrPtr(string(v))
		}
		if v, has := patch["hidden"]; has {
			_ = json.Unmarshal(v, &next.Hidden)
		}
		if v, has := patch["sort_order"]; has {
			_ = json.Unmarshal(v, &next.SortOrder)
		}
		if _, err := tx.Models.UpsertRealModel(ctx, next); err != nil {
			return err
		}
		updated = &next
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	return realModelToAdmin(updated), auditID, nil
}

func (mb *modelsBridge) SetRealModelExcluded(ctx context.Context, id string, excluded bool, audit httpapi.AdminAuditEntry) (*httpapi.AdminRealModel, string, error) {
	rid, err := decodeID(idcode.RealModel, id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var auditID string
	var updated *store.RealModel
	err = mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		cur, err := tx.Models.GetRealModel(ctx, rid)
		if err != nil {
			return err
		}
		next := *cur
		next.ExcludedFromCandidates = excluded
		if _, err := tx.Models.UpsertRealModel(ctx, next); err != nil {
			return err
		}
		updated = &next
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	return realModelToAdmin(updated), auditID, nil
}

// ---------------------------------------------------------------------------
// 抽象模型
// ---------------------------------------------------------------------------

func (mb *modelsBridge) ListAbstractModels(ctx context.Context, limit, offset int) ([]httpapi.AdminAbstractModel, int64, error) {
	rows, err := mb.b.repos.Models.ListAbstractModels(ctx, true)
	if err != nil {
		return nil, 0, mapStoreErr(err)
	}
	total := int64(len(rows))
	lo, hi := pageBounds(len(rows), limit, offset)
	out := make([]httpapi.AdminAbstractModel, 0, hi-lo)
	for _, m := range rows[lo:hi] {
		am := absModelToAdmin(&m)
		if cands, err := mb.b.repos.Models.ListCandidates(ctx, m.ID); err == nil {
			am.CandidateCount = int64(len(cands))
		}
		out = append(out, *am)
	}
	return out, total, nil
}

func (mb *modelsBridge) GetAbstractModel(ctx context.Context, id string) (*httpapi.AdminAbstractModel, error) {
	aid, err := decodeID(idcode.AbstractModel, id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	m, err := mb.b.repos.Models.GetAbstractModel(ctx, aid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	out := absModelToAdmin(m)
	if cands, err := mb.b.repos.Models.ListCandidates(ctx, aid); err == nil {
		out.CandidateCount = int64(len(cands))
	}
	return out, nil
}

func (mb *modelsBridge) GetAbstractModelByName(ctx context.Context, name string) (*httpapi.AdminAbstractModel, error) {
	m, err := mb.b.repos.Models.GetAbstractModelByName(ctx, name)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	out := absModelToAdmin(m)
	if cands, err := mb.b.repos.Models.ListCandidates(ctx, m.ID); err == nil {
		out.CandidateCount = int64(len(cands))
	}
	return out, nil
}

func (mb *modelsBridge) CreateAbstractModel(ctx context.Context, in httpapi.AdminAbstractModel, candidates []httpapi.AdminCandidateItem, audit httpapi.AdminAuditEntry) (string, string, error) {
	// 能力文档进 note（存储无 capabilities 列——见文件头说明）。
	note := ""
	if len(in.Capabilities) > 0 {
		note = capsNotePrefix + string(in.Capabilities)
	}
	strategy := string(in.Strategy)
	if strategy == "" {
		strategy = `["expiry-first","balance"]`
	}
	m := store.AbstractModel{
		Name:           in.Name,
		DisplayName:    in.DisplayName,
		Strategy:       strategy,
		CapabilityMode: in.CapabilityMode,
		Status:         store.ModelStatus(in.Status),
	}
	if note != "" {
		m.Note = adminStrPtr(note)
	}
	var newID int64
	var auditID string
	err := mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		id, err := tx.Models.CreateAbstractModel(ctx, m)
		if err != nil {
			return err
		}
		newID = id
		if len(candidates) > 0 {
			if err := mb.putCandidates(ctx, tx.Models, id, candidates); err != nil {
				return err
			}
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return "", "", mapStoreErr(err)
	}
	return encodeID(idcode.AbstractModel, newID), auditID, nil
}

// putCandidates 翻候选载荷并整表写入。
func (mb *modelsBridge) putCandidates(ctx context.Context, models *store.ModelRepo, abstractID int64, cands []httpapi.AdminCandidateItem) error {
	out := make([]store.Candidate, 0, len(cands))
	for _, c := range cands {
		rid, err := decodeID(idcode.RealModel, c.RealModelID)
		if err != nil {
			return fmt.Errorf("候选 real_model_id %q 非法: %w", c.RealModelID, err)
		}
		sc := store.Candidate{AbstractModelID: abstractID, RealModelID: rid, Weight: c.Weight}
		if len(c.Constraint) > 0 {
			sc.ConstraintJSON = adminStrPtr(string(c.Constraint))
		}
		out = append(out, sc)
	}
	return models.SetCandidates(ctx, abstractID, out)
}

func (mb *modelsBridge) UpdateAbstractModel(ctx context.Context, id string, patch map[string]json.RawMessage, _ json.RawMessage, audit httpapi.AdminAuditEntry) (*httpapi.AdminAbstractModel, string, error) {
	aid, err := decodeID(idcode.AbstractModel, id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	// store 缺口：抽象模型列级 UPDATE（display_name/capability_mode/
	// strategy/note）——没有对应方法，整行重建（CreateAbstractModel 是
	// INSERT）不可用。显式占位错误，等 storage 落 UpdateAbstractModel。
	// status 迁移与弃用（PATCH 载荷里 httpapi 已分流）不走本方法。
	for _, k := range []string{"display_name", "capability_mode", "capabilities", "strategy", "note"} {
		if _, has := patch[k]; has {
			return nil, "", fmt.Errorf("抽象模型 %s 列更新待 storage 落 UpdateAbstractModel：%w", k, errAdminNotImplemented)
		}
	}
	var auditID string
	err = mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		aid2, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid2
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	m, err := mb.b.repos.Models.GetAbstractModel(ctx, aid)
	if err != nil {
		return nil, auditID, nil
	}
	return absModelToAdmin(m), auditID, nil
}

func (mb *modelsBridge) SetAbstractStatus(ctx context.Context, id, status string, audit httpapi.AdminAuditEntry) (string, error) {
	aid, err := decodeID(idcode.AbstractModel, id)
	if err != nil {
		return "", errAdminNotImplemented
	}
	var auditID string
	err = mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Models.SetAbstractModelStatus(ctx, aid, store.ModelStatus(status)); err != nil {
			return err
		}
		aid2, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid2
		return nil
	})
	if err != nil {
		return "", mapStoreErr(err)
	}
	return auditID, nil
}

func (mb *modelsBridge) SetDeprecation(ctx context.Context, id string, in httpapi.AdminDeprecationUpdate, audit httpapi.AdminAuditEntry) (string, error) {
	aid, err := decodeID(idcode.AbstractModel, id)
	if err != nil {
		return "", errAdminNotImplemented
	}
	// 替代目标名字 → id（httpapi 已校验存在且 active；这里只做解析）。
	rep, err := mb.b.repos.Models.GetAbstractModelByName(ctx, in.ReplacementName)
	if err != nil {
		return "", mapStoreErr(err)
	}
	depAt, sunAt := int64(0), int64(0)
	if in.DeprecatedAt != nil {
		depAt = *in.DeprecatedAt
	}
	if in.SunsetAt != nil {
		sunAt = *in.SunsetAt
	}
	var auditID string
	err = mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Models.DeprecateAbstractModel(ctx, aid, &rep.ID, depAt, sunAt); err != nil {
			return err
		}
		aid2, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid2
		return nil
	})
	if err != nil {
		return "", mapStoreErr(err)
	}
	return auditID, nil
}

func (mb *modelsBridge) ListCandidates(ctx context.Context, abstractModelID string) ([]httpapi.AdminCandidateItem, error) {
	aid, err := decodeID(idcode.AbstractModel, abstractModelID)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	cands, err := mb.b.repos.Models.ListCandidates(ctx, aid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	out := make([]httpapi.AdminCandidateItem, 0, len(cands))
	for _, c := range cands {
		item := httpapi.AdminCandidateItem{
			RealModelID:        encodeID(idcode.RealModel, c.RealModelID),
			Weight:             c.Weight,
			AvailableInstances: 0, // 运行时展开归 routing/M6；桥给静态 0
		}
		if c.ConstraintJSON != nil {
			item.Constraint = json.RawMessage(*c.ConstraintJSON)
		}
		out = append(out, item)
	}
	return out, nil
}

func (mb *modelsBridge) ReplaceCandidates(ctx context.Context, abstractModelID string, cands []httpapi.AdminCandidateItem, audit httpapi.AdminAuditEntry) ([]httpapi.AdminCandidateItem, string, error) {
	aid, err := decodeID(idcode.AbstractModel, abstractModelID)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var old []httpapi.AdminCandidateItem
	var auditID string
	err = mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if prev, err := tx.Models.ListCandidates(ctx, aid); err == nil {
			for _, c := range prev {
				item := httpapi.AdminCandidateItem{
					RealModelID: encodeID(idcode.RealModel, c.RealModelID),
					Weight:      c.Weight,
				}
				if c.ConstraintJSON != nil {
					item.Constraint = json.RawMessage(*c.ConstraintJSON)
				}
				old = append(old, item)
			}
		}
		if err := mb.putCandidates(ctx, tx.Models, aid, cands); err != nil {
			return err
		}
		aid2, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid2
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	if old == nil {
		old = []httpapi.AdminCandidateItem{}
	}
	return old, auditID, nil
}

func (mb *modelsBridge) ReferencingGroups(ctx context.Context, name string) ([]httpapi.AdminGroup, error) {
	m, err := mb.b.repos.Models.GetAbstractModelByName(ctx, name)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	groups, err := mb.b.repos.Groups.ListGroups(ctx)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	var out []httpapi.AdminGroup
	for _, g := range groups {
		mids, err := mb.b.repos.Groups.ListGroupModels(ctx, g.ID)
		if err != nil {
			continue
		}
		for _, mid := range mids {
			if mid == m.ID {
				out = append(out, groupToAdmin(&g))
				break
			}
		}
	}
	if out == nil {
		out = []httpapi.AdminGroup{}
	}
	return out, nil
}

func (mb *modelsBridge) CapabilitySnapshot(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := mb.b.repos.Models.ListAbstractModels(ctx, true)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	out := map[string]json.RawMessage{}
	for _, m := range rows {
		if m.Note == nil {
			continue
		}
		if strings.HasPrefix(*m.Note, capsNotePrefix) {
			out[m.Name] = json.RawMessage(strings.TrimPrefix(*m.Note, capsNotePrefix))
		}
	}
	return out, nil
}

// ctxOf 保留为桥内无 ctx 签名的兜底通道（见 ReplacementName 注释）。
func ctxOf() context.Context { return context.Background() }

// init 注入 modelsLookup（absModelToAdmin 的反查通道）。
func init() {
	// 惰性绑定：modelsBridge 构造时 repos 已就绪——这里包级 once 不可行
	//（Store 每测试新建），改为在 newAdminBridge 里绑定；init 只留空实现
	// 说明见 newAdminBridge。
	_ = ctxOf
}
