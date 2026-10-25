package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ModelStatus 是 abstract_models.status 的取值。
type ModelStatus string

const (
	ModelActive     ModelStatus = "active"
	ModelDeprecated ModelStatus = "deprecated"
	ModelDisabled   ModelStatus = "disabled"
)

// RealModelStatus 是 real_models.status 的取值。
//
// 注意 missing 与 disabled 的区别：missing 是"上游目录里没有它了"（客观事实，
// 探测得出），disabled 是"管理员关掉了它"（主观策略）。两者运维动作完全不同。
type RealModelStatus string

const (
	RealModelActive   RealModelStatus = "active"
	RealModelMissing  RealModelStatus = "missing"
	RealModelDisabled RealModelStatus = "disabled"
)

// aggregateChannel 是上游的聚合渠道名（01-catalog.md §5、总纲 5.1 实测 20 个
// 渠道之一）。它下的条目默认 excluded_from_candidates=true：上游自己的一层
// 策略与我们的抽象模型策略层重叠后无法归因。
const aggregateChannel = "aggregate"

// AbstractModel 是对外暴露的抽象模型。
type AbstractModel struct {
	ID                 int64
	Name               string
	DisplayName        string
	Strategy           string
	CapabilityMode     string
	Status             ModelStatus
	ReplacementModelID *int64
	DeprecatedAt       *int64
	SunsetAt           *int64
	Note               *string
}

// RealModel 是上游池子里的真实模型（只有管理员可见）。
type RealModel struct {
	ID              int64
	UpstreamID      int64
	UpstreamModelID string
	DisplayName     string
	GroupName       *string
	// Channel 是模型级渠道（01-catalog.md §2.3：owned_by 派生的那 20 个前缀，
	// 如 deepseek-official / aggregate / cline）。可为 NULL——vendor/selfhosted
	// 的目录条目不保证带 `渠道/模型` 形态，派生不出来就是 NULL。
	//
	// 为什么不用 GroupName 顶替：group_name 是管理员自由填的「目录分组」
	// （01-catalog.md:167 人工展示字段，改过即冻结、刷新不覆盖）。渠道是上游
	// 事实，管理员改个标签不该让"按渠道分组"静默失真。
	Channel                *string
	ContextWindow          *int64
	Modalities             *string // JSON 数组原文
	Efforts                *string // JSON 数组原文
	Status                 RealModelStatus
	Hidden                 bool
	ExcludedFromCandidates bool
	SortOrder              int64
	Source                 string
	LastSeenAt             *int64
	MissingSince           *int64
	// —— 目录侧鉴定三列（task-24，0004 迁移）。字段名是 sidecar
	// VerificationWriter 接口的消费契约，不得改名。
	//
	// 与凭据侧（upstream_credentials.verify_*）回答不同的问题：那边是
	// L1（这把 key 是不是它声称的模型，挂贡献流程）；这边是目录条目与
	// 指纹表的比对结果。挂 real_models 而不是凭据行，否则一条凭据服务
	// N 个模型时互相覆盖，前一个模型的 suspect 证据被冲掉（数据丢失）。
	VerifyStatus VerifyStatus // NOT NULL DEFAULT 'unverified'（四态与凭据行同形，共用类型）
	// VerifyDetail 是判定 evidence 原文（纯文本，不强制 JSON）。
	VerifyDetail *string
	// VerifySource 是指纹库版本标识（如 builtin_v1）。
	// **NULL = 从未鉴定（连参考指纹都没有）；'unverified' = 跑过判定但无指纹**
	// （已进复检队列）。两者运维动作不同：前者无可重判，后者指纹库升级后
	// 要重跑。混用会让复检队列白跑或漏跑。
	VerifySource *string
}

// Candidate 是抽象模型的一个候选。
//
// Weight 是历史字段（03-strategy.md §3.3.2）：内置四种策略全不读它，
// 界面的输入框也已删除（2026-10-26 用户决定「不设权重」）。字段位保留
// 只为不破坏既有 DTO 形状与历史数据，写入时恒 0。
type Candidate struct {
	AbstractModelID int64
	RealModelID     int64
	ConstraintJSON  *string
	Weight          int64
	// SortOrder 是声明序：管理员在候选集编辑器里排列的次序。排序条件
	// 次序相同时按它决定先后。写入时由 SetCandidates 按入参顺序编号。
	SortOrder int64
}

// ModelRepo 是模型目录（抽象模型、候选、真实模型）的仓储。
type ModelRepo struct{ base }

// CreateAbstractModel 新建抽象模型，返回 id。
func (r *ModelRepo) CreateAbstractModel(ctx context.Context, m AbstractModel) (int64, error) {
	if m.Name == "" {
		return 0, errors.New("store: 抽象模型名不得为空")
	}
	if m.DisplayName == "" {
		// 展示名留空会让控制台出现一行没有标题的模型，运维无法判断它是哪个。
		return 0, errors.New("store: 抽象模型必须有 display_name")
	}
	status := m.Status
	if status == "" {
		status = ModelActive
	}
	if !status.valid() {
		return 0, fmt.Errorf("store: 非法抽象模型状态 %q", status)
	}
	mode := m.CapabilityMode
	if mode == "" {
		mode = "strict"
	}
	strategy := m.Strategy
	if strategy == "" {
		strategy = "expiry-first"
	}

	res, err := r.db.ExecContext(ctx,
		`INSERT INTO abstract_models(name, display_name, strategy, capability_mode, status,
		                            replacement_model_id, deprecated_at, sunset_at, note)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		m.Name, m.DisplayName, strategy, mode, string(status),
		m.ReplacementModelID, m.DeprecatedAt, m.SunsetAt, m.Note)
	if err != nil {
		return 0, fmt.Errorf("store: 创建抽象模型失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: 读取新抽象模型 id 失败: %w", err)
	}
	return id, nil
}

const abstractModelColumns = `id, name, display_name, strategy, capability_mode, status,
                              replacement_model_id, deprecated_at, sunset_at, note`

func scanAbstractModel(sc interface{ Scan(...any) error }) (*AbstractModel, error) {
	var m AbstractModel
	var status string
	if err := sc.Scan(&m.ID, &m.Name, &m.DisplayName, &m.Strategy, &m.CapabilityMode,
		&status, &m.ReplacementModelID, &m.DeprecatedAt, &m.SunsetAt, &m.Note); err != nil {
		return nil, err
	}
	m.Status = ModelStatus(status)
	return &m, nil
}

// GetAbstractModel 按 id 读抽象模型。
func (r *ModelRepo) GetAbstractModel(ctx context.Context, id int64) (*AbstractModel, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+abstractModelColumns+` FROM abstract_models WHERE id = ?`, id)
	m, err := scanAbstractModel(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 读取抽象模型 %d 失败: %w", id, err)
	}
	return m, nil
}

// GetAbstractModelByName 按对外名读抽象模型（GET /v1/models 与转发解析使用）。
func (r *ModelRepo) GetAbstractModelByName(ctx context.Context, name string) (*AbstractModel, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+abstractModelColumns+` FROM abstract_models WHERE name = ?`, name)
	m, err := scanAbstractModel(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 按名读取抽象模型失败: %w", err)
	}
	return m, nil
}

// ListAbstractModels 列出抽象模型。
//
// includeDisabled=false 时只返回 active 与 deprecated：对外（租户）只应看到可用的，
// 而 deprecated 必须可见——它的存在本身就是"请迁移"的通知。
func (r *ModelRepo) ListAbstractModels(ctx context.Context, includeDisabled bool) ([]AbstractModel, error) {
	q := `SELECT ` + abstractModelColumns + ` FROM abstract_models`
	if !includeDisabled {
		q += ` WHERE status <> 'disabled'`
	}
	q += ` ORDER BY id`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: 列出抽象模型失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AbstractModel
	for rows.Next() {
		m, err := scanAbstractModel(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 扫描抽象模型失败: %w", err)
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历抽象模型失败: %w", err)
	}
	return out, nil
}

// DeprecateAbstractModel 把抽象模型标记为弃用并指向替代者。
//
// 两条业务规则在这里（以及调用方的事务里）成立：
//  1. 弃用只能指向此刻 status = active 的抽象模型；
//  2. 弃用了就不能再改回 active。
//
// 这两条把替代关系变成一条只出不进的有向路径，因此多跳环无法形成——
// 环需要某一步回到 active，而回到 active 是被禁止的。
// 05-storage.md 3.4 还要求调用方在同一个事务里做一次 visited 游走作为兜底（防并发写入
// 绕过规则）；那一步需要读取整条链，属于业务层，这里只提供单跳的写入。
func (r *ModelRepo) DeprecateAbstractModel(ctx context.Context, id int64, replacementID *int64, deprecatedAt, sunsetAt int64) error {
	cur, err := r.GetAbstractModel(ctx, id)
	if err != nil {
		return err
	}
	if cur.Status == ModelDeprecated {
		return fmt.Errorf("store: 抽象模型 %d 已经是弃用状态", id)
	}
	if cur.Status == ModelDisabled {
		return fmt.Errorf("store: 抽象模型 %d 已禁用，不能改为弃用", id)
	}

	if replacementID != nil {
		if *replacementID == id {
			// DB 的 CHECK 也挡这个，但在应用层先拒绝能给出可读的错误。
			return errors.New("store: 替代者不能是自己")
		}
		rep, err := r.GetAbstractModel(ctx, *replacementID)
		if err != nil {
			return err
		}
		if rep.Status != ModelActive {
			return fmt.Errorf(
				"store: 替代者 %d 的状态是 %s，弃用只能指向此刻 active 的抽象模型",
				*replacementID, rep.Status)
		}
	}

	if deprecatedAt == 0 {
		deprecatedAt = now()
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE abstract_models
		    SET status = 'deprecated', replacement_model_id = ?, deprecated_at = ?, sunset_at = ?
		  WHERE id = ? AND status IN ('active')`,
		replacementID, deprecatedAt, sunsetAt, id)
	if err != nil {
		return fmt.Errorf("store: 标记弃用失败: %w", err)
	}
	return requireAffected(res, "抽象模型", id)
}

// SetAbstractModelStatus 改抽象模型状态（禁用/启用）。
//
// 明确禁止 deprecated → active：这是上面第 2 条规则的落实点。允许改回去的话，
// 替代链就会重新可以成环，而环会让"沿替代链解析最终模型"变成死循环。
func (r *ModelRepo) SetAbstractModelStatus(ctx context.Context, id int64, status ModelStatus) error {
	if !status.valid() {
		return fmt.Errorf("store: 非法抽象模型状态 %q", status)
	}
	cur, err := r.GetAbstractModel(ctx, id)
	if err != nil {
		return err
	}
	if cur.Status == ModelDeprecated && status == ModelActive {
		return fmt.Errorf(
			"store: 抽象模型 %d 已弃用，不能改回 active——"+
				"否则替代链可以成环（05-storage.md 3.4）", id)
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE abstract_models SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return fmt.Errorf("store: 更新抽象模型状态失败: %w", err)
	}
	return requireAffected(res, "抽象模型", id)
}

// SetCandidates 覆盖式设置抽象模型的候选集。
//
// 整体覆盖的理由同 Key 白名单：候选是一个集合，增量 API 在并发下会丢更新。
// 覆盖必须在事务里完成，否则 DELETE 成功而 INSERT 失败会留下一个"没有任何候选"
// 的抽象模型——表现为该模型 100% 不可用。
func (r *ModelRepo) SetCandidates(ctx context.Context, abstractModelID int64, cands []Candidate) error {
	if r.s != nil && r.s.tx == nil {
		return r.s.WithTx(ctx, func(tx *Repos) error {
			return tx.Models.SetCandidates(ctx, abstractModelID, cands)
		})
	}

	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM abstract_candidates WHERE abstract_model_id = ?`, abstractModelID); err != nil {
		return fmt.Errorf("store: 清空候选失败: %w", err)
	}
	// 声明序：按入参切片顺序编号（0 起）。权重已退役（03-strategy §3.3.2），
	// 恒写 0 —— 保留字段位只为兼容历史数据与人工备注，不再参与任何决策。
	for i, c := range cands {
		if _, err := r.db.ExecContext(ctx,
			`INSERT INTO abstract_candidates(abstract_model_id, real_model_id, constraint_json, weight, sort_order)
			 VALUES(?,?,?,?,?)`,
			abstractModelID, c.RealModelID, c.ConstraintJSON, 0, i); err != nil {
			return fmt.Errorf("store: 写候选失败: %w", err)
		}
	}
	return nil
}

// ListCandidates 列出抽象模型的候选，**按声明序**（sort_order 升序）。
//
// 为什么按声明序而不是权重：权重已退役（03-strategy.md §3.3.2——内置四种
// 策略全不读它，界面的输入框也已删除）。排序条件次序相同时按管理员在候选集
// 编辑器里排列的顺序决定先后（2026-10-26 用户决定）——声明的次序本身就是
// 声明的一部分。
//
// real_model_id 只是在 sort_order 相同（理论不该发生：整表替换按入参顺序
// 编号，编号必不重复）时的防御性决胜，保证读序确定。
//
// 读出的 Weight 恒为 0（写入时恒写 0）：字段位留着只为不破坏既有 DTO 形状。
func (r *ModelRepo) ListCandidates(ctx context.Context, abstractModelID int64) ([]Candidate, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT abstract_model_id, real_model_id, constraint_json, weight, sort_order
		   FROM abstract_candidates
		  WHERE abstract_model_id = ?
		  ORDER BY sort_order, real_model_id`, abstractModelID)
	if err != nil {
		return nil, fmt.Errorf("store: 列出候选失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.AbstractModelID, &c.RealModelID, &c.ConstraintJSON, &c.Weight, &c.SortOrder); err != nil {
			return nil, fmt.Errorf("store: 扫描候选失败: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历候选失败: %w", err)
	}
	return out, nil
}

const realModelColumns = `id, upstream_id, upstream_model_id, display_name, group_name, channel,
                          context_window, modalities, efforts, status, hidden,
                          excluded_from_candidates, sort_order, source,
                          last_seen_at, missing_since, verify_status, verify_detail, verify_source`

func scanRealModel(sc interface{ Scan(...any) error }) (*RealModel, error) {
	var m RealModel
	var status string
	var verifyStatus string
	var hidden, excluded int
	if err := sc.Scan(&m.ID, &m.UpstreamID, &m.UpstreamModelID, &m.DisplayName, &m.GroupName, &m.Channel,
		&m.ContextWindow, &m.Modalities, &m.Efforts, &status, &hidden,
		&excluded, &m.SortOrder, &m.Source, &m.LastSeenAt, &m.MissingSince,
		&verifyStatus, &m.VerifyDetail, &m.VerifySource); err != nil {
		return nil, err
	}
	m.Status = RealModelStatus(status)
	m.Hidden = hidden != 0
	m.ExcludedFromCandidates = excluded != 0
	m.VerifyStatus = VerifyStatus(verifyStatus)
	return &m, nil
}

// UpsertRealModel 按 (upstream_id, upstream_model_id) 同步一条真实模型。
//
// 为什么用 ON CONFLICT 而不是"先查再插/改"：目录同步是每个刷新周期一次的全量比对，
// 读-改-写会让"两个并发刷新"互相覆盖。upsert 由 DB 保证原子，且只走唯一索引。
//
// 关键约束（PLAN 11.1）：管理员改过的行 source=override，刷新不覆盖它。
// 落实方式是 DO UPDATE 里带 `WHERE real_models.source = 'auto'`——
// 被管理员接管过的行在冲突时什么都不做，从而同时保住 status/hidden/sort_order
// 等全部人工字段，而不是逐列判断。
func (r *ModelRepo) UpsertRealModel(ctx context.Context, m RealModel) (int64, error) {
	if m.UpstreamID == 0 {
		return 0, errors.New("store: 真实模型必须归属某个上游")
	}
	if m.UpstreamModelID == "" {
		return 0, errors.New("store: upstream_model_id 不得为空")
	}
	status := m.Status
	if status == "" {
		status = RealModelActive
	}
	source := m.Source
	if source == "" {
		source = "auto"
	}
	seenAt := m.LastSeenAt
	if seenAt == nil {
		ts := now()
		seenAt = &ts
	}

	// excluded_from_candidates：aggregate/* 渠道默认排除（01-catalog.md §5）。
	//
	// 为什么放在存储层而不是刷新器：这是**目录事实**而非人工决定。
	// ① 刷新器只 import dshprovider 类型，而 ModelInfo 目前没有 owned_by
	//    字段（协议层 11.1 第 2 条实测 hub listModels 不给），渠道只能从
	//    upstream_model_id 的 `渠道/模型` 形态推——放在 SQL 里做前缀判定，
	//    判定依据与唯一标识同源，不会因为解析口径分叉。
	// ② 人工解除排除（01-catalog.md §5 待定项）发生在管理 API，走
	//    source=override 行；刷新必须写回权威值，否则管理员解除后下一轮
	//    刷新又悄悄被按渠道重新排除，他改了个寂寞。
	// ③ 排序上把它排在 source=override 保护**之后**判定：override 行整体
	//    不被刷新覆盖，管理员的显式解除不会被渠道前缀逻辑推翻。
	//
	// 渠道前缀只认第一个 `/` 之前的部分，且必须精确等于 aggregate——避免
	// `aggregate-v2/xxx`、`my_aggregate/x` 这类近似名被误排除。
	// 渠道（01-catalog.md §2.3）：上游条目不带显式渠道时，从 `渠道/模型` 形态派生。
	// 调用方（刷新器）拿到 owned_by 后会显式传 Channel，这里只是兜底——两种来源
	// 落同一列，routing_bridge 因此不必关心某行是从哪条路径来的。
	// 派生不出来就留 NULL（"上游没说渠道"），不回退到上游级标识：回退会让同一列
	// 混着"模型级渠道"与"上游名"两种语义。
	channel := m.Channel
	if channel == nil {
		if ch, _, ok := strings.Cut(m.UpstreamModelID, "/"); ok && ch != "" {
			channel = &ch
		}
	}

	excluded := m.ExcludedFromCandidates
	if !excluded {
		if ch, _, ok := strings.Cut(m.UpstreamModelID, "/"); ok && ch == aggregateChannel {
			excluded = true
		}
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO real_models(upstream_id, upstream_model_id, display_name, group_name, channel,
		                         context_window, modalities, efforts, status, hidden,
		                         excluded_from_candidates, sort_order, source,
		                         last_seen_at, missing_since)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(upstream_id, upstream_model_id) DO UPDATE SET
		     -- ── 上游权威字段（01-catalog.md §3.2「永远随刷新更新」）──
		     -- 旧实现用 WHERE real_models.source='auto' 冻结**整行**，与 §3.3
		     -- 「保护的必须是人工字段，不是整行」直接冲突，造成两类真实故障：
		     -- ① 手工登记行（override）在目录重新出现后永远停在 missing——
		     --   status 是上游事实，冻住它等于否认"它回来了"（巡检标 missing
		     --   的逆过程被卡死）；
		     -- ② 人工改过一次显示名之后，上游新增的模态/档位永远同步不进来，
		     --   过滤阶段按过期能力剔除本来可用的候选（§3.3 的原论证）。
		     -- channel 只在入参非 NULL 时覆盖：一次上游抖动（拿不到渠道）
		     -- 不该擦掉权威值（§2.3 的映射纪律）。
		     channel        = CASE WHEN excluded.channel IS NOT NULL
			                      THEN excluded.channel ELSE real_models.channel END,
		     modalities     = excluded.modalities,
		     efforts        = excluded.efforts,
		     status         = excluded.status,
		     last_seen_at   = excluded.last_seen_at,
		     -- missing_since 只在"回来了"（status 不再是 missing）时清空；
		     -- 仍 missing 时保留首次标记时间（COALESCE 语义与 MarkMissing 同源）。
		     missing_since  = CASE WHEN excluded.status = 'missing'
			                      THEN COALESCE(real_models.missing_since, excluded.missing_since)
			                      ELSE NULL END,
		     -- ── 人工字段与人工决定 ──
		     -- 写回条件：存量行还是 auto（catalog 值照常覆盖），**或**本次入参
		     -- 本身就是人工写（source=override——PATCH 是读-改-写，必须能改
		     -- override 行的人工字段；刷新器恒传 auto，碰不到这一支）。
		     -- excluded_from_candidates 同规则：管理员的显式解除（override）
		     -- 不被刷新的渠道前缀逻辑覆盖（repo_model_aggregate_test 钉住）。
		     display_name   = CASE WHEN real_models.source = 'auto' OR excluded.source = 'override' THEN excluded.display_name   ELSE real_models.display_name   END,
		     group_name     = CASE WHEN real_models.source = 'auto' OR excluded.source = 'override' THEN excluded.group_name     ELSE real_models.group_name     END,
		     context_window = CASE WHEN real_models.source = 'auto' OR excluded.source = 'override' THEN excluded.context_window ELSE real_models.context_window END,
		     hidden         = CASE WHEN real_models.source = 'auto' OR excluded.source = 'override' THEN excluded.hidden         ELSE real_models.hidden         END,
		     sort_order     = CASE WHEN real_models.source = 'auto' OR excluded.source = 'override' THEN excluded.sort_order     ELSE real_models.sort_order     END,
		     excluded_from_candidates = CASE WHEN real_models.source = 'auto' OR excluded.source = 'override' THEN excluded.excluded_from_candidates ELSE real_models.excluded_from_candidates END,
		     -- source 由人工写翻成 override（§3.2 的行级标记语义）。旧实现的
		     -- SET 里没有 source——桥注释一直声称"人工改写后 source 翻
		     -- override"，实际从未落库；这里把它兑现。刷新（auto 入参）不
		     -- 降级 override 标记。
		     source         = CASE WHEN excluded.source = 'override' THEN 'override' ELSE real_models.source END`,
		m.UpstreamID, m.UpstreamModelID, m.DisplayName, m.GroupName, channel,
		m.ContextWindow, m.Modalities, m.Efforts, string(status),
		boolInt(m.Hidden), boolInt(excluded), m.SortOrder, source,
		seenAt, m.MissingSince)
	if err != nil {
		return 0, fmt.Errorf("store: 同步真实模型 %s 失败: %w", m.UpstreamModelID, err)
	}

	// 冲突且被 override 保护时上面不会写入；这里再读一次拿准确 id。
	var id int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT id FROM real_models WHERE upstream_id = ? AND upstream_model_id = ?`,
		m.UpstreamID, m.UpstreamModelID).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: 回读真实模型 id 失败: %w", err)
	}
	return id, nil
}

// MarkMissingRealModels 把某上游下、不在 keepIDs 里的模型标记为 missing。
//
// 为什么用"标记"而不是删除：删除会连带删掉 abstract_candidates（ON DELETE CASCADE），
// 于是一次上游抖动就会永久摧毁管理员配置好的候选集。missing 保留行，上游恢复时
// UpsertRealModel 会自动把 status 写回 active 并清空 missing_since。
//
// keepIDs 为空时返回错误：那意味着"这次刷新一个模型都没拿到"，几乎必然是
// 上游故障或认证失效，此时把全部模型标 missing 是一次自伤。
func (r *ModelRepo) MarkMissingRealModels(ctx context.Context, upstreamID int64, keepIDs []int64) (int64, error) {
	if len(keepIDs) == 0 {
		return 0, errors.New(
			"store: keepIDs 为空，拒绝把整个上游的模型标记为 missing（这通常是拉取失败而非真的全没了）")
	}

	// 用临时条件拼 NOT IN。参数个数随模型数增长（上限几百），SQLite 的变量上限是 32766，
	// 足够；这里只做数量上的防御。
	if len(keepIDs) > 5000 {
		return 0, fmt.Errorf("store: keepIDs 过多（%d），拒绝构造该查询", len(keepIDs))
	}

	// 构造与 keepIDs 等长的占位符串，并按 SQL 里的出现顺序装配参数：
	// missing_since 的时间戳 → upstream_id → NOT IN 的那一串 id。
	// 顺序必须与占位符严格一致，写反了不会报错，只会静默地拿错误的值去比较。
	placeholders := make([]byte, 0, len(keepIDs)*2)
	args := make([]any, 0, len(keepIDs)+2)
	args = append(args, now(), upstreamID)
	for i, id := range keepIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE real_models
		    SET status = 'missing', missing_since = COALESCE(missing_since, ?)
		  WHERE upstream_id = ? AND id NOT IN (`+string(placeholders)+`)
		    AND status <> 'missing'`,
		args...)
	if err != nil {
		return 0, fmt.Errorf("store: 标记 missing 失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取标记行数失败: %w", err)
	}
	return n, nil
}

// GetRealModel 按 id 读真实模型。
func (r *ModelRepo) GetRealModel(ctx context.Context, id int64) (*RealModel, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+realModelColumns+` FROM real_models WHERE id = ?`, id)
	m, err := scanRealModel(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 读取真实模型 %d 失败: %w", id, err)
	}
	return m, nil
}

// ListRealModelsByUpstream 列出某上游的真实模型（管理面）。
func (r *ModelRepo) ListRealModelsByUpstream(ctx context.Context, upstreamID int64) ([]RealModel, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+realModelColumns+` FROM real_models
		  WHERE upstream_id = ? ORDER BY sort_order, upstream_model_id`, upstreamID)
	if err != nil {
		return nil, fmt.Errorf("store: 列出真实模型失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RealModel
	for rows.Next() {
		m, err := scanRealModel(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 扫描真实模型失败: %w", err)
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历真实模型失败: %w", err)
	}
	return out, nil
}

// ListVisibleRealModels 列出对外可见的真实模型（GET /v1/models 的数据源，
// task-12 装配缺口；口径：04-sidecar.md 启动步骤⑤ 异步拉目录写 real_models）。
//
// 过滤条件（httpapi.ModelCatalog 的存储侧实现面）：
//   - 上游 enabled：停用上游的模型不列（拉目录刷新也只刷 enabled relay，
//     但 disabled 上游的存量行可能还是 active——这里在查询层再拦一道，
//     不依赖刷新器的纪律）；
//   - status = 'active'：missing（目录里消失）与 disabled（人工停用）不列；
//   - hidden = 0：运维隐藏（内部模型/过渡期模型）不列。
//   - TODO(M6)：/v1/models 换抽象模型路由（v1models.go 的既定标记）。
func (r *ModelRepo) ListVisibleRealModels(ctx context.Context) ([]RealModel, error) {
	// realModelColumns 是裸列名列表（供单表查询复用）；JOIN 后 id/hidden 等名
	// 与 upstreams 撞名，SQLite 会报 ambiguous column——给 real_models 的每一列
	// 加 m. 前缀。写法是按逗号切分再拼（列清单含换行与多空格，直接字符串
	// 替换覆盖不了全部分隔形态），而不是维护两份列清单：两份清单必然漂移。
	cols := strings.Split(realModelColumns, ",")
	for i := range cols {
		cols[i] = "m." + strings.TrimSpace(cols[i])
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+strings.Join(cols, ", ")+` FROM real_models m
		  JOIN upstreams u ON u.id = m.upstream_id
		  WHERE u.enabled = 1 AND m.status = 'active' AND m.hidden = 0
		  ORDER BY m.upstream_id, m.sort_order, m.upstream_model_id`)
	if err != nil {
		return nil, fmt.Errorf("store: 列可见真实模型失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RealModel
	for rows.Next() {
		m, err := scanRealModel(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 扫描可见真实模型失败: %w", err)
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历可见真实模型失败: %w", err)
	}
	return out, nil
}

// UpsertUpstream 同步一条上游配置，返回 id（按 kind+name 唯一）。
//
// 保留两返回值签名：既有调用方（upstream/bootstrap、路由装配、仓储测试）只关心 id。
// 需要「新建 vs 更新」的调用方用 UpsertUpstreamWithCreated。
func (r *ModelRepo) UpsertUpstream(ctx context.Context, kind, name, baseURL string, ownerAccountID *int64, enabled bool) (int64, error) {
	id, _, err := r.UpsertUpstreamWithCreated(ctx, kind, name, baseURL, ownerAccountID, enabled)
	return id, err
}

// UpsertUpstreamWithCreated 与 UpsertUpstream 语义完全相同，额外返回 created：
// 本次调用是否**新建**了行（true = INSERT，false = 命中冲突走 UPDATE）。
//
// 为什么单独一个方法而不是把 UpsertUpstream 改成三返回值：管理面（admin-api）
// 需要 created 决定 201/200 与审计语义，但既有自举与路由装配调用只接两个值。
// Go 没有重载，改签名会把所有调用方拖进同一次改动；旧签名 + 新方法既满足新需求，
// 也避免「改一处编译过、另一处悄悄失效」。
func (r *ModelRepo) UpsertUpstreamWithCreated(ctx context.Context, kind, name, baseURL string, ownerAccountID *int64, enabled bool) (int64, bool, error) {
	if kind != "relay" && kind != "vendor" && kind != "selfhosted" {
		return 0, false, fmt.Errorf("store: 非法上游类型 %q（kind 只能是 relay/vendor/selfhosted）", kind)
	}
	if name == "" || baseURL == "" {
		return 0, false, errors.New("store: 上游的 name 与 base_url 都不得为空")
	}
	created, err := r.upstreamAbsent(ctx, kind, name)
	if err != nil {
		return 0, false, err
	}

	// relay 上游 base_url 定案（task-12，承接 task-7 遗留）：存 provider 标识
	// "sidecar://" + provider（如 "sidecar://fake_provider"）——ALIGN-SPEC H 后
	// relay 没有可配端点（02-deploy §2.5：base_url/api_key_file 已删），该列对
	// relay 的语义是「sidecar 内的 provider 锚点」而不是 HTTP 端点；vendor/
	// selfhosted 仍是真实 base_url。校验只查非空（上面），不强制前缀——前缀
	// 是写入方（装配/管理 API）的约定，读取方按 kind 解释，写在 DDL 外面
	// 避免 CHECK 约束把后续演进锁死。
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO upstreams(kind, name, base_url, owner_account_id, enabled, last_seen_at)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(kind, name) DO UPDATE SET
		     base_url         = excluded.base_url,
		     owner_account_id = excluded.owner_account_id,
		     enabled          = excluded.enabled,
		     last_seen_at     = excluded.last_seen_at`,
		kind, name, baseURL, ownerAccountID, boolInt(enabled), now())
	if err != nil {
		return 0, false, fmt.Errorf("store: 同步上游失败: %w", err)
	}

	var id int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT id FROM upstreams WHERE kind = ? AND name = ?`, kind, name).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("store: 回读上游 id 失败: %w", err)
	}
	return id, created, nil
}

// upstreamAbsent 报告 (kind,name) 当前是否不存在（即下一次 upsert 会 INSERT）。
//
// 为什么在写前查一次：SQLite 的 upsert 不返回「插入还是更新」，changes() 也分不出来
// （ON CONFLICT DO UPDATE 同样计一次变更）。写池上限为 1 且管理面调用方通常已包
// WithTx，这条 SELECT 与后面的 upsert 之间不会有第二个写者插进来；即便出现，
// 最坏也只是 created 标记偏保守，不影响任何数据语义。
func (r *ModelRepo) upstreamAbsent(ctx context.Context, kind, name string) (bool, error) {
	var id int64
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM upstreams WHERE kind = ? AND name = ?`, kind, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: 查询上游是否存在失败: %w", err)
	}
	return false, nil
}

// ListUpstreams 列出全部上游（含 base_url 与启用状态）。
//
// 返回的模型里不含任何密文：upstream_secrets 是独立表，解密需要主密钥，
// 由调用方在真正发起上游请求时按需取用。
func (r *ModelRepo) ListUpstreams(ctx context.Context) ([]Upstream, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, kind, name, base_url, secret_id, owner_account_id, enabled, last_seen_at
		   FROM upstreams ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: 列出上游失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Upstream
	for rows.Next() {
		var u Upstream
		var enabled int
		if err := rows.Scan(&u.ID, &u.Kind, &u.Name, &u.BaseURL, &u.SecretID,
			&u.OwnerAccountID, &enabled, &u.LastSeenAt); err != nil {
			return nil, fmt.Errorf("store: 扫描上游失败: %w", err)
		}
		u.Enabled = enabled != 0
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历上游失败: %w", err)
	}
	return out, nil
}

// UpstreamCredentialSummary 是一个上游下的凭据聚合（管理面的贡献归因用）。
type UpstreamCredentialSummary struct {
	UpstreamID int64
	// Provider 是该上游下凭据的 provider 列值（值形如 "api:<目录 id>" 或 "custom"）。
	Provider string
	Total    int // 该上游下全部凭据条数
	Active   int // 其中 status='active' 的条数
}

// ListUpstreamCredentials 按上游聚合贡献凭据，键是 upstreams.id。
//
// 关联路径是**全库唯一一条凭据↔上游的边**，要照 internal/upstream/contribution.go
// 的 Submit 落库顺序读：先建 upstreams 行 → 写 upstream_secrets（带 upstream_id）
// → 凭据的 secret_ref 存密文行 id 的十进制字符串。所以
// credential.secret_ref →(CAST)→ upstream_secrets.id → upstream_secrets.upstream_id
// 是唯一走得通的连接。反方向的 upstreams.secret_id 只对「上游自己那把 key」成立，
// 对贡献凭据不成立——拿它当连接边会把贡献凭据算到别的上游头上。
//
// secret_ref 可能是非数字（自定义流程/历史数据）：CAST 得 0，join 自然落空，
// 该行不进聚合。这是**有意的**：给它加兜底（比如退回字符串比对）会把「关不上」
// 变成「关到 id=0 的行上」，那是伪造事实，不是容错。
//
// c.provider 在聚合查询里是 bare column（SQLite 取组内任意一行）。这里可以这么写，
// 是因为上游名由 contributeUpstreamName(provider, 指纹) 生成（provider 进了
// upstreams.name），同一上游不可能挂两种 provider——确定性来自这个不变量，
// 不来自 SQL 本身。若将来同一上游可挂多家 provider，这里要先改成显式取值。
func (r *ModelRepo) ListUpstreamCredentials(ctx context.Context) (map[int64]UpstreamCredentialSummary, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT s.upstream_id, c.provider, COUNT(*) AS total,
		        SUM(CASE WHEN c.status = 'active' THEN 1 ELSE 0 END) AS active
		   FROM upstream_credentials c
		   JOIN upstream_secrets s ON s.id = CAST(c.secret_ref AS INTEGER)
		  GROUP BY s.upstream_id`)
	if err != nil {
		return nil, fmt.Errorf("store: 聚合上游凭据失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64]UpstreamCredentialSummary)
	for rows.Next() {
		var s UpstreamCredentialSummary
		if err := rows.Scan(&s.UpstreamID, &s.Provider, &s.Total, &s.Active); err != nil {
			return nil, fmt.Errorf("store: 扫描上游凭据聚合失败: %w", err)
		}
		out[s.UpstreamID] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历上游凭据聚合失败: %w", err)
	}
	return out, nil
}

// UpstreamCredentialRow 是一条贡献凭据的全行读面（凭据池右栏用，
// 与 UpstreamCredentialSummary 的区别：它要的是每一条而不是聚合计数）。
type UpstreamCredentialRow struct {
	ID             int64
	OwnerAccountID int64
	Provider       string
	ExternalLabel  string
	Status         string
	VerifyStatus   string
	CreatedAt      int64
	LastUsedAt     int64 // 0 = 从未用过
}

// ListUpstreamCredentialRows 按上游列出全部凭据行（不筛状态——管理页
// 要看全貌）。键是 upstreams.id，值按 created_at 升序、id 升序决胜。
//
// 关联边与 ListUpstreamCredentials 相同（secret_ref → upstream_secrets.id
// → upstream_id，全库唯一一条凭据↔上游的边）：这条边上的行才属于该上游。
// secret_ref 非数字的行（自定义流程/历史数据）CAST 得 0，join 落空不返回，
// 与聚合口径一致——两份读面对「哪些凭据属于哪个上游」必须给出同一个答案，
// 否则凭据池右栏和可用实例数会互相矛盾。
func (r *ModelRepo) ListUpstreamCredentialRows(ctx context.Context) (map[int64][]UpstreamCredentialRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT s.upstream_id, c.id, c.owner_account_id, c.provider, COALESCE(c.external_label, ''),
		        c.status, c.verify_status, c.created_at, COALESCE(c.last_used_at, 0)
		   FROM upstream_credentials c
		   JOIN upstream_secrets s ON s.id = CAST(c.secret_ref AS INTEGER)
		  ORDER BY c.created_at, c.id`)
	if err != nil {
		return nil, fmt.Errorf("store: 列出上游凭据行失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64][]UpstreamCredentialRow)
	for rows.Next() {
		var row UpstreamCredentialRow
		var upstreamID int64
		if err := rows.Scan(&upstreamID, &row.ID, &row.OwnerAccountID, &row.Provider, &row.ExternalLabel,
			&row.Status, &row.VerifyStatus, &row.CreatedAt, &row.LastUsedAt); err != nil {
			return nil, fmt.Errorf("store: 扫描上游凭据行失败: %w", err)
		}
		out[upstreamID] = append(out[upstreamID], row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历上游凭据行失败: %w", err)
	}
	return out, nil
}

// GetUpstreamByID 按 id 读上游行（管理面的公开访问器）。
//
// 为什么导出：PATCH / 改形态 / 详情都要「先读旧行」，而之前只有包内的
// getUpstreamByID；跨包调用方只能 ListUpstreams 全表遍历——小表能跑，但错误形态
// 不对（不存在会被当成"列表里没找到"，而不是 ErrNotFound）。这里只是薄包装，
// 语义（含 ErrNotFound）与包内函数完全一致。
func (r *ModelRepo) GetUpstreamByID(ctx context.Context, id int64) (*Upstream, error) {
	return r.getUpstreamByID(ctx, id)
}

// Upstream 是上游的读模型。
type Upstream struct {
	ID             int64
	Kind           string
	Name           string
	BaseURL        string
	SecretID       *int64
	OwnerAccountID *int64
	Enabled        bool
	LastSeenAt     *int64
}

// boolInt 把布尔转成 SQLite 的 0/1。集中一处，避免各处写法不一导致 CHECK 失败。
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s ModelStatus) valid() bool {
	switch s {
	case ModelActive, ModelDeprecated, ModelDisabled:
		return true
	}
	return false
}

// ================================================================ 上游写域（管理面）

// validUpstreamKind 判定 kind 值域（upstreams.kind 有 DB CHECK，Go 层同步一份
// 是为了给出可判定的错误，而不是让 SQLite 的 CHECK 失败信息漏进 API 响应）。
func validUpstreamKind(kind string) bool {
	switch kind {
	case "relay", "vendor", "selfhosted":
		return true
	}
	return false
}

// UpdateUpstream 改上游的名称、base_url 与启用开关（02-admin §9 的 PATCH）。
//
// 为什么 kind 不在这里改：kind 改变的是"这条上游**怎么被调用**"，不是它的名字
// 或开关。就地改会让"我改了 base_url"与"我改了调用方式"看起来是同一类操作，
// 而后者会让所有依赖该上游的候选在下一次请求时走一条完全不同的代码路径
// （02-admin §9 明确论证）。所以 kind 走 SetUpstreamKind 单独一条，管理面对应
// 独立的危险级端点。
//
// 为什么要回读旧行做存在性判定：UPDATE 影响 0 行既可能是"不存在"也可能是
// "值没变"，只看 RowsAffected 会把前者报成成功。
func (r *ModelRepo) UpdateUpstream(ctx context.Context, id int64, name, baseURL string, enabled bool) error {
	if id == 0 {
		return errors.New("store: 上游 id 不得为 0")
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(baseURL) == "" {
		return errors.New("store: 上游 name 与 base_url 不得为空")
	}
	if _, err := r.getUpstreamByID(ctx, id); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE upstreams SET name = ?, base_url = ?, enabled = ? WHERE id = ?`,
		name, baseURL, boolInt(enabled), id)
	if err != nil {
		return fmt.Errorf("store: 改上游失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUpstreamKind 变更上游形态（relay/vendor/selfhosted）。
//
// 为什么单独一条：见 UpdateUpstream 的论证。
//
// 为什么要求 base_url 与新 kind 匹配：relay 的调用路径是 sidecar 进程内直调
// adapter，没有端点；改成 relay 却留着一条 http URL，等于让运行时按一个不会被
// 使用的地址做能力判断。反之 vendor/selfhosted 改完没地址则必然调用失败。
// 这一条把"形态与地址不匹配"这个只能靠日志反推的错误挡在写入时。
func (r *ModelRepo) SetUpstreamKind(ctx context.Context, id int64, kind string) error {
	if !validUpstreamKind(kind) {
		return fmt.Errorf("store: 非法上游类型 %q（kind 只能是 relay/vendor/selfhosted）", kind)
	}
	u, err := r.getUpstreamByID(ctx, id)
	if err != nil {
		return err
	}
	if kind == "relay" && !strings.HasPrefix(u.BaseURL, "sidecar://") {
		// relay 的 base_url 定案值（task-12）是 provider 锚点 sidecar://<provider>。
		// 非空校验已在 UpsertUpstream，这里校的是"与新形态一致"。
		return fmt.Errorf("store: 改成 relay 前请把 base_url 换成 sidecar://<provider> 形态（当前 %q）；relay 不经 HTTP 网关", u.BaseURL)
	}
	if kind != "relay" && strings.HasPrefix(u.BaseURL, "sidecar://") {
		return fmt.Errorf("store: %s 类型需要真实端点地址，而当前 base_url 是 provider 锚点 %q", kind, u.BaseURL)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE upstreams SET kind = ? WHERE id = ?`, kind, id)
	if err != nil {
		return fmt.Errorf("store: 变更上游形态失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ModelRepo) getUpstreamByID(ctx context.Context, id int64) (*Upstream, error) {
	var u Upstream
	var enabled int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, kind, name, base_url, secret_id, owner_account_id, enabled, last_seen_at
		   FROM upstreams WHERE id = ?`, id).
		Scan(&u.ID, &u.Kind, &u.Name, &u.BaseURL, &u.SecretID, &u.OwnerAccountID, &enabled, &u.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读上游 %d 失败: %w", id, err)
	}
	u.Enabled = enabled != 0
	return &u, nil
}

// SetAbstractModelProfile 列级更新抽象模型的可编辑字段（管理面 PATCH 的写面）。
//
// 为什么必须有：CreateAbstractModel 是 INSERT，没有列级 UPDATE 的话
// 「改显示名 / 改策略链 / 改能力模式」在管理面上无从表达——此前这些字段
// 的 PATCH 一律回 404（桥显式报未接线，而不是静默忽略）。
//
// nil = 不改该列（PATCH 语义）。全部为 nil 时不发 SQL 也不报错：
// 「一次什么都没改的 PATCH」是幂等的空操作，不是错误。
//
// 校验与 CreateAbstractModel 同源（同一套枚举与「display_name 非空」判据）：
// 两处各写一份必然漂移，而漂移的表现是「创建时拒的值更新时收下」。
// strategy 是逗号分隔的策略链（02-routing 的顺序语义），这里只保证非空，
// 链的合法性由调用方（管理面 handler）按策略名表校验——存储层不认识策略名。
func (r *ModelRepo) SetAbstractModelProfile(ctx context.Context, id int64, displayName, strategy, capabilityMode *string, note *string) error {
	sets := []string{}
	args := []any{}
	if displayName != nil {
		if strings.TrimSpace(*displayName) == "" {
			return errors.New("store: 抽象模型必须有 display_name")
		}
		sets = append(sets, "display_name = ?")
		args = append(args, *displayName)
	}
	if strategy != nil {
		if strings.TrimSpace(*strategy) == "" {
			return errors.New("store: 策略链不得为空（候选选择没有兜底顺序）")
		}
		sets = append(sets, "strategy = ?")
		args = append(args, *strategy)
	}
	if capabilityMode != nil {
		switch *capabilityMode {
		case "strict", "filter":
		default:
			return fmt.Errorf("store: 非法 capability_mode %q（期望 strict/filter）", *capabilityMode)
		}
		sets = append(sets, "capability_mode = ?")
		args = append(args, *capabilityMode)
	}
	if note != nil {
		sets = append(sets, "note = ?")
		args = append(args, *note)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	res, err := r.db.ExecContext(ctx,
		`UPDATE abstract_models SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("store: 更新抽象模型失败: %w", err)
	}
	return requireAffected(res, "抽象模型", id)
}
