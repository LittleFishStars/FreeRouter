// admin_bridge_models.go —— AdminModelStore → store.ModelRepo（task-23）。
//
// 桥要点：
//   - 真实模型/抽象模型 id 出入站走 idgen 形状校验（前缀体系已废弃）。
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
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/apidir"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// capsNotePrefix 是能力声明文档在 note 列里的标记前缀。
const capsNotePrefix = "admin_caps:"

type modelsBridge struct{ b *adminBridge }

// realModelToAdmin 翻真实模型行。
func realModelToAdmin(m *store.RealModel) *httpapi.AdminRealModel {
	out := &httpapi.AdminRealModel{
		ID:                     m.ID,
		DisplayName:            m.DisplayName,
		UpstreamID:             m.UpstreamID,
		UpstreamModelID:        m.UpstreamModelID,
		GroupName:              m.GroupName,
		Channel:                m.Channel,
		ContextWindow:          m.ContextWindow,
		Status:                 string(m.Status),
		Hidden:                 m.Hidden,
		SortOrder:              m.SortOrder,
		Source:                 m.Source,
		LastSeenAt:             m.LastSeenAt,
		MissingSince:           m.MissingSince,
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
var modelsLookup func(id string) (string, error)

// absModelToAdmin 翻抽象模型行（能力文档从 note 解析）。
func absModelToAdmin(m *store.AbstractModel) *httpapi.AdminAbstractModel {
	out := &httpapi.AdminAbstractModel{
		ID:             m.ID,
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
func (mb *modelsBridge) replacementNameOf(id string) (string, error) {
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
			if up.ID != upstreamID {
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
	rid, err := decodeID(id)
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
	rid, err := decodeID(id)
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
	rid, err := decodeID(id)
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
			am.AvailableCandidateCount = mb.availableCandidates(ctx, cands)
		}
		out = append(out, *am)
	}
	return out, total, nil
}

func (mb *modelsBridge) GetAbstractModel(ctx context.Context, id string) (*httpapi.AdminAbstractModel, error) {
	aid, err := decodeID(id)
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
		out.AvailableCandidateCount = mb.availableCandidates(ctx, cands)
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
		out.AvailableCandidateCount = mb.availableCandidates(ctx, cands)
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
	var newID string
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
	return newID, auditID, nil
}

// putCandidates 翻候选载荷并整表写入。
func (mb *modelsBridge) putCandidates(ctx context.Context, models *store.ModelRepo, abstractID string, cands []httpapi.AdminCandidateItem) error {
	out := make([]store.Candidate, 0, len(cands))
	for _, c := range cands {
		rid, err := decodeID(c.RealModelID)
		if err != nil {
			return fmt.Errorf("候选 real_model_id %q 非法: %w", c.RealModelID, err)
		}
		// 声明序由 SetCandidates 按**入参切片顺序**编号，所以这里不能传
		// weight（已退役，恒写 0）也不能自己编 sort_order——入参顺序就是
		// 管理员在界面上排的顺序。
		sc := store.Candidate{AbstractModelID: abstractID, RealModelID: rid}
		if len(c.Constraint) > 0 {
			sc.ConstraintJSON = adminStrPtr(string(c.Constraint))
		}
		out = append(out, sc)
	}
	return models.SetCandidates(ctx, abstractID, out)
}

func (mb *modelsBridge) UpdateAbstractModel(ctx context.Context, id string, patch map[string]json.RawMessage, _ json.RawMessage, audit httpapi.AdminAuditEntry) (*httpapi.AdminAbstractModel, string, error) {
	aid, err := decodeID(id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	// store 已落 SetAbstractModelProfile（列级 UPDATE，nil 列不动）。
	// 这里把 PATCH 载荷翻成列值：strategy 存成 JSON 数组（与创建路径同源），
	// capabilities 仍按 note 前缀编码（存储无 capabilities 列，见文件头）。
	var displayName, strategy, mode, note *string
	if raw, has := patch["display_name"]; has {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, "", fmt.Errorf("display_name 必须是字符串: %w", err)
		}
		displayName = &v
	}
	if raw, has := patch["capability_mode"]; has {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, "", fmt.Errorf("capability_mode 必须是字符串: %w", err)
		}
		mode = &v
	}
	if raw, has := patch["strategy"]; has {
		// 载荷是数组（["expiry-first","balance"]），存成 JSON 文本。
		strategy = new(string)
		var arr []string
		if err := json.Unmarshal(raw, &arr); err == nil {
			b, _ := json.Marshal(arr)
			*strategy = string(b)
		} else {
			var single string
			if err2 := json.Unmarshal(raw, &single); err2 != nil {
				return nil, "", fmt.Errorf("strategy 必须是策略名数组: %w", err)
			}
			b, _ := json.Marshal([]string{single})
			*strategy = string(b)
		}
	}
	if raw, has := patch["capabilities"]; has {
		// 能力文档写进 note（带 capsNotePrefix，与创建路径同一编码）。
		note = new(string)
		*note = capsNotePrefix + string(raw)
	}
	if raw, has := patch["note"]; has {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, "", fmt.Errorf("note 必须是字符串: %w", err)
		}
		note = &v
	}
	var auditID string
	err = mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if displayName != nil || strategy != nil || mode != nil || note != nil {
			if err := tx.Models.SetAbstractModelProfile(ctx, aid, displayName, strategy, mode, note); err != nil {
				return err
			}
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
	m, err := mb.b.repos.Models.GetAbstractModel(ctx, aid)
	if err != nil {
		return nil, auditID, nil
	}
	return absModelToAdmin(m), auditID, nil
}

func (mb *modelsBridge) SetAbstractStatus(ctx context.Context, id, status string, audit httpapi.AdminAuditEntry) (string, error) {
	aid, err := decodeID(id)
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
	aid, err := decodeID(id)
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
	aid, err := decodeID(abstractModelID)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	cands, err := mb.b.repos.Models.ListCandidates(ctx, aid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	// 每条候选的「可用凭据实例数」是**它所属上游**的展开结果——候选声明的是
	// 真实模型，真实模型挂在某个上游下，可展开的实例数由那条上游的凭据决定
	// （凭据↔上游的边是 upstream_secrets，见 store.ListUpstreamCredentials）。
	//
	// 为什么以前给静态 0：那时还没有「按上游聚合凭据」这条读路径，只能占位。
	// 占位的代价是真实的——干跑（§8.3）按 available_instances 分桶，静态 0 会让
	// 每条候选都被判成 no_candidate，于是「保存前干跑」这个防呆机制恒报红灯，
	// 而界面上「可用实例」恒为 0，「配了但永远选不中」的静默故障照旧看不出
	// 来（02-admin.md §8.2 明写这个字段就是为了让预览能看出有没有凭据支撑）。
	//
	// 口径与 model-sources 端点同源（同一份聚合、同一套规则）：停用上游 = 0、
	// relay = 池内（Go 侧不知数量，按 0 记——见下方注释）、其余 = active 凭据数。
	insts, err := mb.candidateInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.AdminCandidateItem, 0, len(cands))
	for _, c := range cands {
		rm, err := mb.b.repos.Models.GetRealModel(ctx, c.RealModelID)
		inst := int64(0)
		if err != nil {
			// 悬空的 real_model_id（外键不在）：给 0 而不是跳过这条候选——跳过会让
			// 候选行数与库里对不上，管理员会以为这条被删了。
			inst = 0
		} else {
			inst = insts[rm.UpstreamID]
		}
		item := httpapi.AdminCandidateItem{
			RealModelID: c.RealModelID,
			// Weight 恒 0（已退役）：字段位留着只为不破坏 DTO 形状。
			Weight:             0,
			AvailableInstances: inst,
		}
		if c.ConstraintJSON != nil {
			item.Constraint = json.RawMessage(*c.ConstraintJSON)
		}
		out = append(out, item)
	}
	return out, nil
}

// candidateInstances 给出「上游 id → 可用凭据实例数」的映射。
//
// 口径（与 model-sources 端点同源，注释见 ListCandidates 的调用处）：
//   - 上游停用 → 0：停用的上游一条也展不开，这是确定结论。
//   - relay → 0：**不是**「没有实例」，而是 Go 侧数不出来——relay 的账号池在
//     sidecar 进程里，Go 这边只有一行 kind=relay 的上游行。记 0 是保守值：
//     干跑按 available_instances>0 分桶，relay 候选因此会被算进 no_candidate，
//     那会让「干跑恒报红灯」在反代候选上重现。所以 relay 记 **1**：一个上游行
//     至少意味着有一个可用的 provider 入口。至于账号池里到底有几把 key，
//     权威答案在 sidecar，Go 侧不给它编数字——但把「不知道」报成 0 等于
//     谎报「没有」，前者是缺数据，后者是错数据。
//   - 其余（vendor / selfhosted）→ 该上游下 status=active 的凭据数。
//
// 为什么不做「凭据是否真能调通」的探测：探测要出网、要花额度，而这是管理面
// 列表的读路径，秒级响应是硬要求。这里给的是「有没有凭据支撑」这一层事实，
// 够回答「配了但永远选不中」的问题。
func (mb *modelsBridge) candidateInstances(ctx context.Context) (map[string]int64, error) {
	ups, err := mb.b.repos.Models.ListUpstreams(ctx)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	creds, err := mb.b.repos.Models.ListUpstreamCredentials(ctx)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	out := make(map[string]int64, len(ups))
	for _, u := range ups {
		switch {
		case !u.Enabled:
			out[u.ID] = 0
		case u.Kind == "relay":
			out[u.ID] = 1
		default:
			out[u.ID] = int64(creds[u.ID].Active)
		}
	}
	return out, nil
}

// availableCandidates 数出「有凭据支撑的候选数」——即声明式候选里当前能被
// 展开成至少一个凭据实例的那些条数。
//
// 为什么不是候选条数减掉「无支撑」的条数，而是直接数有支撑的：两者的差别在
// 悬空的 real_model_id（真实模型行被删了但候选还留着）。那种行既不算有支撑
// 也不该被当成一次失败——它是一条坏配置，直接数有支撑的自然不包含它。
//
// 读不出凭据事实时（仓储报错）返回 0：这是「我不知道」与「没有」的差别，
// 但这个字段在列表页只是给管理员一个量级参考，0 会让列表页的徽标变黄
// （"0 / 4"），而真出错时那次请求本身会失败并被上层看到。
func (mb *modelsBridge) availableCandidates(ctx context.Context, cands []store.Candidate) int64 {
	insts, err := mb.candidateInstances(ctx)
	if err != nil {
		return 0
	}
	var n int64
	for _, c := range cands {
		rm, err := mb.b.repos.Models.GetRealModel(ctx, c.RealModelID)
		if err != nil {
			continue
		}
		if insts[rm.UpstreamID] > 0 {
			n++
		}
	}
	return n
}

func (mb *modelsBridge) ReplaceCandidates(ctx context.Context, abstractModelID string, cands []httpapi.AdminCandidateItem, audit httpapi.AdminAuditEntry) ([]httpapi.AdminCandidateItem, string, error) {
	aid, err := decodeID(abstractModelID)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var old []httpapi.AdminCandidateItem
	var auditID string
	err = mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if prev, err := tx.Models.ListCandidates(ctx, aid); err == nil {
			for _, c := range prev {
				item := httpapi.AdminCandidateItem{
					RealModelID: c.RealModelID,
					// Weight 恒 0（已退役）。
					Weight: 0,
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

// ListModelSources 实现 httpapi.AdminModelSourceStore（契约 §1.1 的两栏数据）。
//
// 装配口径：
//   - 来源 = upstreams 全表（不筛 kind / enabled）：停用的上游必须在界面上
//     看得见并标「已停用」——把它过滤掉，用户会以为这家没了，而它的凭据还在
//     库里；enabled=false 的语义已经由 available_instances=0 表达。
//   - 贡献归因 = ListUpstreamCredentials 的那张 map（键是上游 id），那是全库
//     唯一一条凭据↔上游的边（见该方法注释）。
//   - 模型 = 每条上游一次 ListRealModelsByUpstream：直接复用既有的
//     ORDER BY (sort_order, upstream_model_id)——契约 §1.1 的模型排序就是它。
//     N+1 次查询在这里可接受：本地 SQLite 读、管理面低频访问、上游数是几十的
//     量级；换成「一次查全表再在 Go 里按上游分组」要多一个仓储方法，而那次分组
//     的正确性还得单独测一遍——既有那一个实现已经是契约顺序。
//   - channels：上面这些行已覆盖 real_models 全表，所以在 Go 里顺手去重就够，
//     不必再加一个 DISTINCT 查询（两份实现就是两个漂移源）。
func (mb *modelsBridge) ListModelSources(ctx context.Context) ([]httpapi.AdminModelSource, []string, error) {
	all, err := mb.b.repos.Models.ListUpstreams(ctx)
	if err != nil {
		return nil, nil, mapStoreErr(err)
	}
	// 已移除的上游不进来源聚合（2026-11-01）：管理面「移除供应商」与贡献者
	// 「移除贡献」都会置 removed_at，那之后这条来源在凭据池里不该再出现——
	// 它已经不在池里了（密文是墓碑、模型也退出候选），留着只会诱发
	// 「这条还能不能救」的无效排查。行本身保留给归因外键与审计。
	ups := make([]store.Upstream, 0, len(all))
	for _, u := range all {
		if u.RemovedAt != nil {
			continue
		}
		ups = append(ups, u)
	}
	// 先读齐再装配：channels 是**全池**事实（跨所有上游），必须看完全部行才能去重。
	byUpstream := make(map[string][]store.RealModel, len(ups))
	channelSet := make(map[string]struct{})
	for _, u := range ups {
		models, err := mb.b.repos.Models.ListRealModelsByUpstream(ctx, u.ID)
		if err != nil {
			return nil, nil, mapStoreErr(err)
		}
		byUpstream[u.ID] = models
		for i := range models {
			// 空串渠道不算渠道：列可空，但「派生不出渠道」在库里也可能落成空串，
			// 而契约要的是去重**非空**值（空串会变成界面上一个没有名字的芯片）。
			if ch := models[i].Channel; ch != nil && *ch != "" {
				channelSet[*ch] = struct{}{}
			}
		}
	}
	channels := make([]string, 0, len(channelSet))
	for c := range channelSet {
		channels = append(channels, c)
	}
	sort.Strings(channels)

	// 凭据全行（凭据池右栏用）：一次读全表按上游分组，避免 N 次查询。
	// 与上面 creds 的聚合同一条边，两份读面的「属于哪个上游」答案必须一致。
	credRows, err := mb.b.repos.Models.ListUpstreamCredentialRows(ctx)
	if err != nil {
		return nil, nil, mapStoreErr(err)
	}

	// 来源合并（2026-10-27 用户决定）：同一 provider 的多把 key 会贡献出多条
	// upstream 行（贡献一次 = 一条行），凭据池左栏此前按行列出，同一供应商就
	// 出现多行。左栏要的粒度是「来源」——provider 列值（api:<目录id> / custom /
	// relay:<池名>）正是契约 §4 的来源存储口径，所以按它聚合；没有凭据的上游
	//（平台渠道）拿不到 provider 列，单独成条。
	//
	// 聚合键为什么从凭据行取而不是从上游名正则反捞：上游名是展示形态
	// （contributeUpstreamName 拼的），名里带的指纹后缀会随换 key 变化；
	// 凭据行的 provider 列是结构化的来源事实，两边各推一套必然分叉。
	type sourceGroup struct {
		members []store.Upstream
		creds   map[string][]store.UpstreamCredentialRow // 按上游分组（与全局同形）
		models  map[string]store.RealModel               // 全成员模型并集，按 upstream_model_id 去重
	}
	groups := make(map[string]*sourceGroup)
	solo := make([]*sourceGroup, 0) // 无凭据上游（平台渠道）：按行单独成条
	for _, u := range ups {
		rows := credRows[u.ID]
		if len(rows) == 0 {
			solo = append(solo, &sourceGroup{members: []store.Upstream{u}})
			continue
		}
		key := rows[0].Provider
		g := groups[key]
		if g == nil {
			g = &sourceGroup{models: map[string]store.RealModel{}, creds: map[string][]store.UpstreamCredentialRow{}}
			groups[key] = g
		}
		g.members = append(g.members, u)
		g.creds[u.ID] = rows
		for _, m := range byUpstream[u.ID] {
			// 同 provider 不同 key 的目录高度重叠，按 upstream_model_id 去重：
			// 并集才是"这个供应商提供哪些模型"的完整答案，逐行复制会翻倍计数。
			if _, dup := g.models[m.UpstreamModelID]; !dup {
				g.models[m.UpstreamModelID] = m
			}
		}
	}

	// 装配顺序稳定：先按来源键排序，平台渠道条目排在最后（与旧版
	// 「贡献凭据在前、平台渠道在后」的左栏顺序一致）。
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]httpapi.AdminModelSource, 0, len(groups)+len(solo))
	for _, k := range keys {
		g := groups[k]
		out = append(out, mergedSourceOf(k, g.members, g.creds, g.models))
	}
	for _, g := range solo {
		src := modelSourceOf(g.members[0], store.UpstreamCredentialSummary{}, byUpstream[g.members[0].ID])
		// solo（平台渠道，无凭据）也要带成员清单：形状统一为非空切片，
		// 前端的批量操作不必为"这条来源有没有成员"写第二个分支。
		src.UpstreamIDs = []string{g.members[0].ID}
		out = append(out, src)
	}
	return out, channels, nil
}

// mergedSourceOf 把「同一 provider 的多条上游行 + 全部凭据 + 模型并集」聚合成
// 一条来源。聚合后的字段口径：
//
//   - ID 用主行（成员里凭据最早的那条）：添加模型这类"单目标"操作要一个真实
//     上游行；批量操作（停用/启用整条来源）用 UpstreamIDs 逐个成员调用。
//   - Name 走目录名（apidir 命中时），不带贡献指纹后缀——同一来源的多行只在
//     名字后缀上不同，合并后再展示指纹反而是噪音。
//   - Enabled = 任一成员启用：一个来源停了一半凭据仍能干活，界面上说"停用"
//     会误导；想精确到行的状态看右栏凭据列表。
//   - AvailableInstances = 各成员之和（relay 成员 → nil，口径同单行版）。
//   - Models 用并集（去重后），ModelCount 是去重条数。
func mergedSourceOf(key string, members []store.Upstream, credRows map[string][]store.UpstreamCredentialRow, models map[string]store.RealModel) httpapi.AdminModelSource {
	// 主行 = 来源里**凭据最早创建**的成员所在的上游行（贡献时间最早 = 来源的
	// 起点）。credRows 的每条清单已按 created_at 升序排，所以各组首行里
	// created_at 最小的就是主行凭据。
	primary := members[0]
	var activeSum int64
	var lastSeen int64
	anyEnabled := false
	anyContributed := false
	protos := map[string]struct{}{}
	totalCreds := 0
	// 主行 = 拥有**最早创建的那条凭据**的上游行。凭据清单按 created_at 升序，
	// 所以每个成员的"最早凭据"就是它的首行；取这些首行里 created_at 最小者。
	// 为什么按凭据时间而不是上游行顺序：upstreams 没有 created_at 列，
	// 而"这个来源从哪一把 key 开始"的事实就在凭据的创建时间上。
	primaryCreated := int64(1 << 62)
	for _, u := range members {
		if u.Enabled {
			anyEnabled = true
		}
		if u.LastSeenAt != nil && *u.LastSeenAt > lastSeen {
			lastSeen = *u.LastSeenAt
		}
		for _, r := range credRows[u.ID] {
			totalCreds++
			if r.Status == string(store.CredActive) {
				activeSum++
			}
			if r.CreatedAt > 0 && r.CreatedAt < primaryCreated {
				primaryCreated = r.CreatedAt
				primary = u
			}
		}
		for _, pr := range httpapi.AdminModelSourceProtocols(u.Kind, providerIDOf(key)) {
			protos[pr] = struct{}{}
		}
	}
	anyContributed = totalCreds > 0

	// 模型并集按 upstream_model_id 排序输出（稳定、可断言）。
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	modelList := make([]store.RealModel, 0, len(models))
	for _, id := range ids {
		modelList = append(modelList, models[id])
	}

	src := modelSourceOf(primary, store.UpstreamCredentialSummary{
		Provider: key, // providerIDOf 从它取目录 id；漏了它 ProviderID 会落成空串
		Total:    totalCreds,
		Active:   int(activeSum),
	}, modelList)
	idsList := make([]string, 0, len(members))
	credList := make([]httpapi.AdminSourceCredential, 0, totalCreds)
	for _, u := range members {
		idsList = append(idsList, u.ID)
		credList = append(credList, sourceCredentialsOf(u.ID, credRows[u.ID])...)
	}
	src.UpstreamIDs = idsList
	src.Credentials = credList
	src.Enabled = anyEnabled
	src.Contributed = anyContributed
	// 头部「凭据 N」= 凭据**总条数**（与右栏列表一致）——2026-10-29 前只数
	// active，pending 凭据（待审核）让头部显示 0，与「凭据（1）」自相矛盾。
	src.CredentialCount = totalCreds
	_ = activeSum
	if lastSeen > 0 {
		src.LastSeenAt = &lastSeen
	}
	src.Protocols = make([]string, 0, len(protos))
	for pr := range protos {
		src.Protocols = append(src.Protocols, pr)
	}
	sort.Strings(src.Protocols)
	return src
}

// earliestCreatedAt 是主行判定的辅助：该上游凭据清单的首行创建时间
// （清单已按 created_at 升序排，空清单返回极大值表示"没有凭据不参与主行竞争"）。
func earliestCreatedAt(u store.Upstream, credRows map[string][]store.UpstreamCredentialRow) int64 {
	rows := credRows[u.ID]
	if len(rows) == 0 || rows[0].CreatedAt == 0 {
		return int64(1 << 62)
	}
	return rows[0].CreatedAt
}

// sourceCredentialsOf 把凭据全行翻成来源内嵌的 DTO 切片。
//
// 为什么恒给空切片：前端对「没有凭据」渲染空态，对 null 渲染错误——
// 同一个语义两种 JSON 形状会让前端多一个分支，而 null 在这里没有额外含义
// （「未知」不成立：查询失败时整条请求已经 500 了）。
func sourceCredentialsOf(upstreamID string, rows []store.UpstreamCredentialRow) []httpapi.AdminSourceCredential {
	out := make([]httpapi.AdminSourceCredential, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		item := httpapi.AdminSourceCredential{
			ID:            r.ID,
			Provider:      r.Provider,
			ExternalLabel: r.ExternalLabel,
			Status:        r.Status,
			VerifyStatus:  r.VerifyStatus,
			BalanceText:   r.BalanceText,
		}
		if r.CreatedAt > 0 {
			ts := r.CreatedAt
			item.CreatedAt = &ts
		}
		if r.LastUsedAt > 0 {
			ts := r.LastUsedAt
			item.LastUsedAt = &ts
		}
		if r.BalanceCheckedAt > 0 {
			ts := r.BalanceCheckedAt
			item.BalanceCheck = &ts
		}
		item.CheckinText = r.CheckinText
		item.CheckinDate = r.CheckinDate
		if r.CheckinAt > 0 {
			ts := r.CheckinAt
			item.CheckinAt = &ts
		}
		out = append(out, item)
	}
	return out
}

// modelSourceOf 把一个上游行 + 它的凭据聚合 + 它的真实模型行翻成契约 §1.1 的来源。
//
// 为什么目录增强只认 provider_id：上游名由 contributeUpstreamName 拼成
// "api:<目录 id>-contrib-<指纹>"，从名字里正则回捞目录 id 也能"猜"出来，但猜
// 错（例如自定义贡献的 custom 形态）会把一个自建端点标成某家供应商的官方端点，
// 而界面上没有任何东西能让人看出这是猜的。
func modelSourceOf(u store.Upstream, cred store.UpstreamCredentialSummary, models []store.RealModel) httpapi.AdminModelSource {
	pid := providerIDOf(cred.Provider)
	protocols := httpapi.AdminModelSourceProtocols(u.Kind, pid)

	src := httpapi.AdminModelSource{
		ID:           u.ID,
		Kind:         u.Kind,
		Name:         u.Name, // 目录未命中时用上游行名；命中了在下面换掉
		UpstreamName: u.Name,
		ProviderID:   pid,
		BaseURL:      u.BaseURL,
		Enabled:      u.Enabled,
		Contributed:  cred.Total > 0,
		// OAuth 型判定：凭据 Provider 列是裸 provider id（如 'loomy'）。
		// api: 前缀是目录型贡献（用户粘 Key），custom 是自定义端点——
		// 三者里的前一种才走 sidecar 账号会话。详见 AdminModelSource.OAuth。
		OAuth:              cred.Provider != "" && cred.Provider != "custom" && !strings.HasPrefix(cred.Provider, "api:"),
		CredentialCount:    cred.Active,
		Protocols:          protocols,
		AvailableInstances: httpapi.AdminModelSourceInstances(u.Kind, u.Enabled, cred.Active),
		LastSeenAt:         u.LastSeenAt,
	}
	if p, ok := apidir.Get(pid); ok {
		// 目录是展示事实的权威：命中时用它的展示名与三个链接字段。不回退上游行名，
		// 否则界面会把 "api:deepseek-com-contrib-key_de2c" 当成供应商名显示。
		src.Name = p.Name
		src.Region = p.Region
		src.Website = p.Website
		src.APIDoc = p.APIDoc
	}
	if src.OAuth && cred.Provider != "" {
		// 反代来源的展示名 = 凭据 provider 列的裸 id（2026-10-30 用户决定）：
		// 上游行名是 "<provider>-oauth-<指纹>" / DSH 凭据 ref（"-oauth-key_<短码>"）
		// 的机器形态，后缀只服务于「确定性反查 upstream id」（见
		// contributeUpstreamName 的注释），界面上是噪音——与 API 供应商的
		// 目录展示名同待遇。api:/custom 不进这个分支：前者上面已被目录名
		// 覆盖，后者的名字本来就是用户自己起的。悬空凭据（Provider 空）
		// 不进：没有更可信的名字，保留上行原名。
		src.Name = cred.Provider
	}

	src.Models = make([]httpapi.AdminModelSourceModel, 0, len(models))
	src.Models = make([]httpapi.AdminModelSourceModel, 0, len(models))
	for i := range models {
		m := &models[i]
		src.Models = append(src.Models, httpapi.AdminModelSourceModel{
			RealModelID:            m.ID,
			UpstreamModelID:        m.UpstreamModelID,
			DisplayName:            m.DisplayName,
			Channel:                m.Channel,
			ContextWindow:          m.ContextWindow,
			Modalities:             jsonStringList(m.Modalities),
			Efforts:                jsonStringList(m.Efforts),
			Status:                 string(m.Status),
			ExcludedFromCandidates: m.ExcludedFromCandidates,
			// 模型级协议事实目前不存在，继承来源；复制一份而不是共享切片，
			// 免得将来某处改一张卡片把同一来源的其它卡片一起改了。
			Protocols: append([]string(nil), protocols...),
			AvailableInstances: httpapi.AdminModelSourceModelInstances(
				src.AvailableInstances, string(m.Status), m.ExcludedFromCandidates),
		})
	}
	// model_count 就是 models 长度：桥是唯一同时知道两者的人，让接口层再数一次
	// 等于给"两个数不一致"留一个可乘之机。
	src.ModelCount = len(src.Models)
	return src
}

// providerIDOf 从凭据的 provider 列取出目录 id。
//
// 两种落库形态（契约 v2 §4）："api:<目录 id>" 与 "custom"。只有前者能对上目录；
// "custom"、空串与历史脏数据一律返回空串——空串是"不是目录供应商"的表达，
// 不是"解析失败"，所以没有错误分支（调用方也不该为它写容错）。
func providerIDOf(provider string) string {
	const apiPrefix = "api:"
	if !strings.HasPrefix(provider, apiPrefix) {
		return ""
	}
	return strings.TrimPrefix(provider, apiPrefix)
}

// CreateRealModel 实现 httpapi.AdminRealModelCreator（手工登记模型）。
//
// 落库走既有 UpsertRealModel + source=override：这条来源标记让刷新器的
// ON CONFLICT「只在 source='auto' 行上覆盖」保护生效——管理员手工登记的
// 显示名/上下文不会被下一轮自动目录擦掉（01-catalog.md §3.3 的字段级
// 保护语义）。
//
// 幂等：先按上游列一次行、比对 upstream_model_id，命中就返回既有行 +
// created=false。为什么不在 Upsert 之后用 RowsAffected 判新建：upsert 的
// DO UPDATE 分支对 override 行**静默跳过**（WHERE 挡住），RowsAffected 给 0
// 既可能是"命中既有 override 行"也可能是"命中 auto 行并更新了"——拿它判
// created 会把后者误报成未创建。显式先查一次，两条路径的语义都干净。
//
// 上游行不存在 → ErrAdminUpstreamNotFound（对外 404）：存在性校验只有桥
// 拿得到仓储，handler 的 idgen 形状校验拦的是"连形状都不对"。
func (mb *modelsBridge) CreateRealModel(ctx context.Context, upstreamID string, upstreamModelID, displayName string, contextWindow *int64, audit httpapi.AdminAuditEntry) (*httpapi.AdminRealModel, bool, string, error) {
	if _, err := mb.b.repos.Models.GetUpstreamByID(ctx, upstreamID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, "", httpapi.ErrAdminUpstreamNotFound
		}
		return nil, false, "", mapStoreErr(err)
	}
	if displayName == "" {
		displayName = upstreamModelID
	}
	var out *httpapi.AdminRealModel
	var created bool
	var auditID string
	err := mb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		// 命中既有行（无论 auto/override）：幂等返回，不写审计——什么都没变。
		// 接管一条 auto 行走 PATCH（有字段可改、值得留痕），这里只做
		// "有则返回、无则登记"：重放同一请求必须同结果。
		rows, err := tx.Models.ListRealModelsByUpstream(ctx, upstreamID)
		if err != nil {
			return err
		}
		for i := range rows {
			if rows[i].UpstreamModelID == upstreamModelID {
				out = realModelToAdmin(&rows[i])
				return nil
			}
		}
		id, err := tx.Models.UpsertRealModel(ctx, store.RealModel{
			UpstreamID:      upstreamID,
			UpstreamModelID: upstreamModelID,
			DisplayName:     displayName,
			ContextWindow:   contextWindow,
			Status:          store.RealModelActive,
			Source:          "override",
		})
		if err != nil {
			return err
		}
		fresh, err := tx.Models.GetRealModel(ctx, id)
		if err != nil {
			return err
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		out = realModelToAdmin(fresh)
		created = true
		return nil
	})
	if err != nil {
		return nil, false, "", mapStoreErr(err)
	}
	return out, created, auditID, nil
}

// jsonStringList 解析 real_models 的 JSON 数组原文（modalities / efforts）。
//
// 契约要求「解析失败给 [] 而不是 null」，这里把三种情况统一成 []：列为 NULL、
// JSON 坏、数组里出现非字符串元素。为什么连第三种也算失败而不是跳过坏元素：
// 本列只承载字符串枚举，能给前端渲染的值要么全对要么没有——
// 半份枚举会让界面显示"支持 2 种模态"而实际支持 3 种，这是比空白更坏的错。
func jsonStringList(raw *string) []string {
	out := []string{}
	if raw == nil {
		return out
	}
	var arr []string
	if err := json.Unmarshal([]byte(*raw), &arr); err != nil {
		return out
	}
	if arr == nil {
		// 原文是 `null`：与解析失败同义（都没有值），统一成 []。
		return out
	}
	return arr
}
