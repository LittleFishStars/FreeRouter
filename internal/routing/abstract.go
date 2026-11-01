// Package routing —— M6 抽象模型与策略路由（docs/models/02-abstract.md、
// 03-strategy.md、04-deprecation.md；里程碑 docs/ops/03-milestones.md §8）。
//
// 分层位置：本包是「纯决策层」——输入是声明式数据（抽象模型、候选、运行态），
// 输出是排好序的候选与可解释的 trace。它不 import store/httpapi/app/upstream
// 的实现：所有数据来源都收敛为窄接口（AbstractModelStore 等），实现在装配层
// （internal/app，Lead 域）接线。为什么：路由是项目灵魂，必须可以被纯表驱动
// 测试覆盖（03-strategy.md §3.4 确定性要求），任何对具体存储的依赖都会让
// 契约测试变成集成测试。
package routing

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ============ 窄接口（装配层实现，见文件尾「装配清单」注释） ============

// AbstractModelStatus 是抽象模型三态（04-deprecation.md §1.1）。
type AbstractModelStatus string

const (
	// StatusActive 正常路由。
	StatusActive AbstractModelStatus = "active"
	// StatusDeprecated 仍接受请求，路由走替代链终点（弃用不是关闭）。
	StatusDeprecated AbstractModelStatus = "deprecated"
	// StatusDisabled 不出现在 /v1/models，请求 404/410。
	StatusDisabled AbstractModelStatus = "disabled"
)

// CapabilityMode 能力模式（02-abstract.md §5）。
type CapabilityMode string

const (
	// ModeStrict 严格模式（默认）：声明取 min/交集，所有客户端被声明值限制。
	ModeStrict CapabilityMode = "strict"
	// ModeFilter 过滤模式：声明取并集，不支持能力的候选在路由前被筛掉。
	ModeFilter CapabilityMode = "filter"
)

// AbstractModel 是抽象模型行的读面（02-abstract.md §2.2 的子集——只取
// 路由需要的字段；capabilities 等管理字段不藏在模型行里，避免「读半截
// 配置」的歧义）。
//
// 2026-11-01 定案：原来的 Strategy []string（排序策略链）已随迁移 0014
// 退役——候选的使用顺序来自候选集的手动声明序，不再有「策略链参数」。
type AbstractModel struct {
	ID             string // UUID（对外 id 与库内主键同值，2026-10-26 改造）
	Name           string // 对外名称，唯一，禁止含 ':'（02-abstract.md §2.3）
	Status         AbstractModelStatus
	CapabilityMode CapabilityMode
	// 原 Strategy []string（排序策略链）已退役（2026-11-01 定案）：
	// 候选的使用顺序改由候选集的手动声明序决定，模型行上不再有排序配置。
	ReplacementModelID *string // deprecated 时指向替代目标（只能指向 active）
	DeprecatedAt       *time.Time
	SunsetAt           *time.Time
}

// Constraint 是候选的声明式约束（02-abstract.md §3.1）——不绑凭据
// （凭据是流动的，绑定会让「撤贡献」变「修配置」，见 §3.3 三条理由）。
type Constraint struct {
	// ChannelScope 渠道范围：any/include/exclude + 渠道名列表。
	ChannelMode string // "any" | "include" | "exclude"
	Channels    []string
	// RequireModalities 请求需要、候选必须具备的模态（text/image/…）。
	RequireModalities []string
	// MinContext 候选上下文窗口下限（0 = 不约束）。
	MinContext int64
	// RequireEfforts 思考档位要求（可选）。
	RequireEfforts []string
}

// CandidateKind 是候选类别（迁移 0015 的 candidate_kind 列）。
//
// real = 候选是真实模型；abstract = 候选是另一个抽象模型（嵌套回退）。
type CandidateKind string

const (
	// KindReal 候选是真实模型（零值即它，见 CandidateDecl.Kind）。
	KindReal CandidateKind = "real"
	// KindAbstract 候选是抽象模型。
	KindAbstract CandidateKind = "abstract"
)

// CandidateDecl 是 abstract_candidates 一行的读面（声明，非运行态）。
//
// 2026-11-01：TargetKind/TargetID 取代原 RealModelID——抽象模型的候选集
// 可以放**其他抽象模型**（分层回退：A 里排第一的都不行就退到 B）。同一列
// 在两种 kind 下指向两张表（迁移 0015），所以字段名按「目标」而非「真实模型」。
type CandidateDecl struct {
	TargetID   string // UUID：Kind=real 时是 real_models.id，abstract 时是 abstract_models.id
	TargetKind CandidateKind
	Constraint Constraint
	// Weight 历史字段：内置四种策略已不读它（03-strategy.md §3.3.2）。
	// 保留字段位只为让装配层搬运行时不丢信息，注释必须写明否则下一个
	// 实现者会以为它是活的。
	Weight int64
}

// aggregateChannel 是上游的聚合渠道段名（01-catalog.md §5）：
// 它下的条目默认不进候选集——上游自己的一层策略与我们的策略层重叠后
// 无法归因（两层都做策略，出问题时没有单一归因点）。
const aggregateChannel = "aggregate"

// RealModelInfo 是 real_models 行的路由读面（01-catalog.md §2.2 子集）。
type RealModelInfo struct {
	ID              string // UUID
	UpstreamID      string // UUID
	UpstreamModelID string
	// UpstreamKind 上游 kind（relay/vendor/selfhosted）——决定候选方言。
	UpstreamKind string
	// Channel 渠道名（限定名的渠道段，如 "deepseek-official"）。
	Channel                string
	ContextWindow          *int64 // nil = 未知（01-catalog.md §2.3 已知空缺）
	Modalities             []string
	Efforts                []string
	Status                 string // "active" | "missing" | "disabled"（05-storage.md DDL）
	ExcludedFromCandidates bool   // aggregate/* 默认 true（01-catalog.md §5）
	UpstreamEnabled        bool
}

// AbstractModelStore 是抽象模型+候选声明的读面。
// 为什么窄：路由只需要「按名查模型 + 按模型查候选 + 按 id 查真实模型」
// 三个读；收整 Store 会让「哪些方法被用到」变模糊，测试 fake 也更贵。
type AbstractModelStore interface {
	// GetAbstractModelByName 按对外名称查；不存在返回 nil, nil（区分
	// 「不存在」与「查询失败」——前者是 404 语义，后者是 500）。
	GetAbstractModelByName(ctx context.Context, name string) (*AbstractModel, error)
	// GetAbstractModel 按 id 查（弃用链解析逐跳用）。
	GetAbstractModel(ctx context.Context, id string) (*AbstractModel, error)
	// ListCandidates 列出抽象模型的候选声明（行序即配置序）。
	ListCandidates(ctx context.Context, abstractModelID string) ([]CandidateDecl, error)
	// GetRealModel 按 id 查真实模型；不存在返回 nil, nil。
	GetRealModel(ctx context.Context, id string) (*RealModelInfo, error)
}

// ============ 错误（对齐 03-errors.md 码表） ============

// ResolveError 是抽象模型解析失败的分类结果——错误码字段而非 error 包装，
// 因为调用方（httpapi v1 面板）需要的是「码 + HTTP 状态」的确定性映射，
// error 链在这里没有增值（没有跨层归因需求）。
type ResolveError struct {
	Code    string // no_candidate | model_not_found（404 语义）
	Message string
	// Detail 补充诊断（不外发，供日志/trace）。
	Detail string
}

func (e *ResolveError) Error() string {
	return fmt.Sprintf("routing: %s: %s", e.Code, e.Message)
}

// ============ 解析结果 ============

// Resolved 是一次抽象模型解析的产物（02-abstract.md §7「Resolve 唯一出口」）。
type Resolved struct {
	// Model 是用户请求的那个抽象模型（响应 model 字段回显它——04-deprecation.md
	// §6「头里用 A、路由用 C」；04 待定项「回显 A 还是 C」按 02-abstract §6.2
	// 的精神取 A）。
	Model *AbstractModel
	// Terminal 是链终点（active）；未弃用时 Terminal == Model。
	Terminal *AbstractModel
	// Chain 是弃用链的每一跳（含起点；未弃用时长度 1）。
	Chain []string
	// Candidates 是展开后的候选（已过保存级校验的声明展开；排序前的原始序）。
	Candidates []RealModelInfo
	// Constraints 是「真实模型 id → 它那条候选声明的约束」。
	//
	// 为什么单独一张表而不是塞进 RealModelInfo：声明（约束）与运行态（能力面、
	// 上游）是两个概念，塞进同一个结构会让「读半截配置」的歧义回来
	// （AbstractModel 的注释里有同一条纪律）。但展开是递归的（嵌套候选），
	// 叶子可能来自别的抽象模型的候选集——只查终点模型的声明会漏掉它们的约束，
	// 所以约束在展开时顺手收集，装配层直接用这张表，不再回查一次。
	Constraints map[string]Constraint
	// CycleBroken 为 true 时表示链解析遇到环，已降级为「无替代」
	//（04-deprecation.md §2.5：降级不报错 + 告警信号）。
	CycleBroken bool
}

// Resolver 把入站模型名解析成可路由的抽象模型（含弃用链）。
//
// 真实模型名对外必须 404（02-abstract.md §6.2 / 任务红线 C2）：判据是
// 「名字含 ':'（限定名 dsh:…）」或「不是任何抽象模型名」——两层判据都
// 收敛在 Resolve 里，调用方不需要重复实现。
type Resolver struct {
	store AbstractModelStore
}

func NewResolver(store AbstractModelStore) *Resolver {
	return &Resolver{store: store}
}

// Resolve 解析入站模型名（04-deprecation.md §6 运行时解析）。
//
// 返回 *ResolveError 的两种情况：
//   - model_not_found：名字不存在 / disabled / 是真实模型名（404 语义，
//     不区分「不存在」与「不可见」防泄露池结构）；
//   - no_candidate：模型存在但候选集为空（503 语义，02-abstract.md §6.5
//     ——404 说「换模型」，503 说「稍后重试同一模型」，混在一起客户端
//     会在池子抖动时永久切走）。
//
// 其余失败（存储错误）返回普通 error（调用方按 500 处理）。
func (res *Resolver) Resolve(ctx context.Context, name string) (*Resolved, *ResolveError, error) {
	if name == "" {
		return nil, &ResolveError{Code: "model_not_found", Message: "模型名为空"}, nil
	}
	// 判据 1：含 ':' 的一定是真实模型限定名（dsh:渠道/模型）——抽象模型名
	// 禁止 ':'（02-abstract.md §2.3），字符串层面互斥，不必查库。
	if strings.Contains(name, ":") {
		return nil, &ResolveError{
			Code:    "model_not_found",
			Message: "模型不存在",
			Detail:  "入站名含 ':'，按真实模型限定名处理（C2 对外只暴露抽象模型）",
		}, nil
	}

	m, err := res.store.GetAbstractModelByName(ctx, name)
	if err != nil {
		return nil, nil, fmt.Errorf("routing: 查询抽象模型 %q: %w", name, err)
	}
	if m == nil {
		// 判据 2：库里没有这个抽象模型名——含裸真实模型 id 的情形（客户端
		// 拿着上游原样 id 请求）也落在这里。与 disabled 同样回 404，不区分。
		return nil, &ResolveError{Code: "model_not_found", Message: "模型不存在"}, nil
	}
	if m.Status == StatusDisabled {
		// disabled：不出现在 /v1/models，直接请求 404/410（待定项取 404——
		// 410 会泄露「这个模型曾经存在」；与「不存在」同码还能防探测）。
		return nil, &ResolveError{Code: "model_not_found", Message: "模型不存在"}, nil
	}

	// 弃用链解析（deprecated → 终点 active；visited 兑底防环）。
	terminal, chain, cycle, err := res.resolveChain(ctx, m)
	if err != nil {
		return nil, nil, err
	}

	// 候选展开：链终点（未弃用时就是本模型）的候选集。
	cands, cons, rerr, err := res.expandCandidates(ctx, terminal)
	if err != nil {
		return nil, nil, err
	}
	if rerr != nil {
		return nil, rerr, nil
	}

	return &Resolved{
		Model:       m,
		Terminal:    terminal,
		Chain:       chain,
		Candidates:  cands,
		Constraints: cons,
		CycleBroken: cycle,
	}, nil, nil
}

// resolveChain 沿 replacement_model_id 走到 active 终点（04-deprecation.md §2）。
//
// 不设跳数上限：环由两条状态规则结构性排除（§2.2——「每条边都从已死节点
// 指向活节点，死节点不复活」），visited 集合只是纯兜底（挡直接改库/老数据
// 迁移这类「绕过状态机的写入」，触发时按缺陷信号告警，见 §2.4–2.5）。
func (res *Resolver) resolveChain(ctx context.Context, m *AbstractModel) (*AbstractModel, []string, bool, error) {
	chain := []string{m.Name}
	visited := map[string]bool{m.ID: true}
	cur := m
	for cur.Status == StatusDeprecated {
		if cur.ReplacementModelID == nil {
			// deprecated 但没配替代目标：按「无替代」处理——路由用本模型
			// 自身的候选集（语义明确：无候选时 503，而不是 500）。
			return cur, chain, false, nil
		}
		next, err := res.store.GetAbstractModel(ctx, *cur.ReplacementModelID)
		if err != nil {
			return nil, nil, false, fmt.Errorf("routing: 解析弃用链查询 %s: %w", *cur.ReplacementModelID, err)
		}
		if next == nil {
			// 替代目标行不存在（外键悬空——直接改库类损伤）：降级为无替代。
			return cur, chain, false, nil
		}
		if visited[next.ID] {
			// 环：状态机正常路径下不可能（规则 1+2），触发即配置事故。
			// 降级为「无替代」不报错（§2.5）——「这个模型没有替代目标」指
			// 被请求的起点模型 m，所以停回 m 用它自身的候选集，而不是停在
			// 链尾 cur（cur 是环上的 deprecated 节点，它的候选集是另一个
			// 模型的配置，落在它上面对用户是「悄悄换模型」，落在 m 上才是
			// 「无替代」的字面语义）。CycleBroken 让上层告警。
			return m, chain, true, nil
		}
		visited[next.ID] = true
		chain = append(chain, next.Name)
		// disabled 的中间节点：链走不下去，同样降级为无替代（停在被替换前
		// 的最后一个可用节点——它的候选集仍可服务）。
		if next.Status == StatusDisabled {
			return cur, chain, false, nil
		}
		cur = next
	}
	return cur, chain, false, nil
}

// expandCandidates 把终点的候选声明展开成真实模型读面（02-abstract.md §3.2
// 的「声明 → 运行时展开」第一步：候选 × 真实模型；凭据维度归装配层展开）。
//
// 展开时执行保存级校验的运行时兜底（§3.4 列了保存时校验，但「直接改库」
// 绕过保存路径的可能仍在——运行时再拦一道，剔除而不是报错，理由同
// 弃用链的降级哲学：坏配置不该变成用户可见的 500）：
//   - real_model 行不存在 / status=missing / excluded_from_candidates → 剔除；
//   - 上游 disabled → 剔除（F4 渠道未禁用）；
//   - aggregate/* 排除（01-catalog.md §5：上游自己的策略层与我们重叠后
//     无法归因——两层策略只留我们这层）。
func (res *Resolver) expandCandidates(ctx context.Context, m *AbstractModel) ([]RealModelInfo, map[string]Constraint, *ResolveError, error) {
	raw, err := res.expandDepth(ctx, m, nil, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	out := &expandResult{
		decl:         raw.decl,
		nestedHidden: raw.nestedHidden,
		cands:        dedupeByID(raw.cands),
		constraints:  raw.constraints,
	}
	if len(out.cands) == 0 {
		// 空候选集 → no_candidate（503）而非 500（02-abstract.md §6.5）。
		return nil, nil, &ResolveError{
			Code:    "no_candidate",
			Message: "该模型当前无可用候选",
			Detail:  fmt.Sprintf("abstract=%s 声明候选 %d 条，展开后 0 条", m.Name, out.decl),
		}, nil
	}
	return out.cands, out.constraints, nil, nil
}

// expandResult 是一次递归展开的统计（诊断用：detail 要说清「声明几条、展开几条」）。
type expandResult struct {
	cands []RealModelInfo
	decl  int // 本次展开消费的声明条数（不含嵌套模型内部的声明）
	// constraints 收集叶子候选的声明约束（见 Resolved.Constraints）。
	constraints map[string]Constraint
	// nestedHidden 是被剔除的嵌套候选（会成环 / 嵌套模型不存在 / 它自己展开为空）。
	nestedHidden []string
}

// expandDepth 递归展开一个抽象模型的候选集。
//
// 嵌套语义（2026-11-01 用户需求）：候选里若是**抽象模型**，就把它自己的
// 候选（再递归）按声明序插进结果里——等价于「把 B 的候选抄进 A 的尾部」，
// 但 B 改配置时自动跟随，不产生失真副本。递归不设深度上限：环由路径栈挡掉，
// 上限只会让「合法但很深的分层」静默丢候选。
//
// 防环用**路径栈**（onStack）而不是全局 visited：菱形依赖（A→B、A→C、
// B→D、C→D）完全合法，D 展开两次是对的（同一真实模型出现两次由
// dedupeByID 收口，不会真的重复尝试）；只有「同一条路径上又回到自己」
// 才是环。
//
// 环的处理（用户要求「选择时隐藏会循环依赖的项」）：走到一个已在路径上的
// 抽象模型**跳过该条**，不报错、不中断——与弃用链的降级哲学一致
// （04-deprecation.md §2.5：坏配置降级，不变成用户可见的 500）。保存路径另有
// 一道拒绝（管理桥 putCandidates 成环校验），这里只兜「手工改库绕过了保存」。
//
// 为什么读不到就跳过而不报错：这三类失败（抽象模型行不存在、已停用、自己
// 展开为空）都是「这一条候选不成立」，不该连坐同层的其它候选——后面的真实
// 模型候选还可能是可用的。
func (res *Resolver) expandDepth(ctx context.Context, m *AbstractModel, onStack map[string]bool, stack []string) (*expandResult, error) {
	if onStack == nil {
		onStack = map[string]bool{}
	}
	// 进入即入栈、返回时出栈（defer）。出栈是必须的：兄弟分支不共享路径，
	// 漏掉出栈会把合法的菱形依赖误判成环。
	onStack[m.ID] = true
	defer delete(onStack, m.ID)
	// stack 只用于诊断文案（「A → B → A」比「有环」有用得多）。拷贝而非
	// 追加到共享切片：兄弟分支的路径不能混进彼此的成因描述。
	path := append(append([]string(nil), stack...), m.Name)

	decls, err := res.store.ListCandidates(ctx, m.ID)
	if err != nil {
		return nil, fmt.Errorf("routing: 查询候选声明 %s: %w", m.ID, err)
	}
	out := &expandResult{decl: len(decls), constraints: map[string]Constraint{}}
	for _, d := range decls {
		if d.TargetKind == KindAbstract {
			if onStack[d.TargetID] {
				// 成环：这条候选展开会绕回自己，跳过。
				out.nestedHidden = append(out.nestedHidden,
					fmt.Sprintf("%s（%s → %s 成环，已跳过该条）",
						d.TargetID, strings.Join(path, " → "), nameOf(res, ctx, d.TargetID)))
				continue
			}
			sub, err := res.store.GetAbstractModel(ctx, d.TargetID)
			if err != nil {
				return nil, fmt.Errorf("routing: 查询嵌套候选抽象模型 %s: %w", d.TargetID, err)
			}
			if sub == nil {
				out.nestedHidden = append(out.nestedHidden,
					fmt.Sprintf("%s（嵌套候选的抽象模型行不存在）", d.TargetID))
				continue
			}
			// 嵌套目标自己 disabled → 跳过（与「disabled 抽象模型对外 404」
			// 一致：它当前不服务，也就不能作为回退目标）。
			if sub.Status == StatusDisabled {
				out.nestedHidden = append(out.nestedHidden,
					fmt.Sprintf("%s（嵌套候选 %s 已停用）", d.TargetID, sub.Name))
				continue
			}
			inner, err := res.expandDepth(ctx, sub, onStack, path)
			if err != nil {
				return nil, err
			}
			// 空的嵌套候选不算错误，但一定要留痕：否则管理员看到「配了 B
			// 却一条候选都没多」时无从判断是 B 空还是展开失败。
			if len(inner.cands) == 0 {
				out.nestedHidden = append(out.nestedHidden,
					fmt.Sprintf("%s（嵌套候选 %s 展开后为空）", d.TargetID, sub.Name))
			}
			out.cands = append(out.cands, inner.cands...)
			out.nestedHidden = append(out.nestedHidden, inner.nestedHidden...)
			for id, cst := range inner.constraints {
				out.constraints[id] = cst
			}
			continue
		}

		rm, err := res.store.GetRealModel(ctx, d.TargetID)
		if err != nil {
			return nil, fmt.Errorf("routing: 查询真实模型 %s: %w", d.TargetID, err)
		}
		// status 值域以存储 DDL 为准（05-storage.md §4：
		// CHECK (status IN ('active','missing','disabled'))）；01-catalog.md
		// 里的 normal 是语义概念（「目录里活着」），落到库里就是 active。
		// 早期按 "normal" 判定会把全部候选剔成空池——这是装配时实测到的
		// 口径冲突，以 DDL 为事实源修正。
		if rm == nil || rm.Status != "active" || rm.ExcludedFromCandidates || !rm.UpstreamEnabled {
			continue
		}
		// aggregate/* 排除的第二道闸（01-catalog.md §5）。
		//
		// 判据是 upstream_model_id 的首斜杠段（aggregate/xxx），与第一道闸
		// （UpsertRealModel 的 SQL 前缀判定）同源。两道的数据源不同但**判据
		// 表达式必须同源**：早期这里判的是渠道名 == "aggregate"，而渠道名由
		// upstream 行的 base_url/provider 推出，永远产不出 "aggregate"——
		// 那不是冗余保险，是恒假的死代码（code-review 巡检发现），它给的
		// 是虚假安全感：注释写着「刷新器 bug 时路由层兜底」，实际兜不住。
		//
		// 现在两道都判模型 id 前缀：手工改库把 excluded_from_candidates 清成
		// 0 时，这道仍挡住（这才是双保险的用途）；而渠道列（real_models.channel，
		// 0002 落地）即便将来语义再变也不必动这里——本判定读的是 id 前缀
		// 不是渠道列，与渠道来源（权威 owned_by vs 前缀派生）正交。
		if ch, _, ok := strings.Cut(rm.UpstreamModelID, "/"); ok && ch == aggregateChannel {
			continue
		}
		out.cands = append(out.cands, *rm)
		out.constraints[rm.ID] = d.Constraint
	}
	return out, nil
}

// dedupeByID 按真实模型行 id 去重，保留**首见位置**（声明序即使用序）。
//
// 为什么必须去重：嵌套候选让同一个真实模型能从多条路径进来（菱形依赖
// A→B、A→C 两边都含 D，或 A 自己直接挂了 D 又嵌套了含 D 的 B）。不去重的话
// 同一条上游路径会被连续尝试两次——第一次刚因冷却/不可用被排到末尾，第二次
// 又原样重试，纯粹浪费一次上游调用。
//
// 为什么保留首见而不是「深度优先序」：首见位置就是管理员写在候选集里的
// 第一个能找到它的地方，与手抄副本的直觉一致。
func dedupeByID(in []RealModelInfo) []RealModelInfo {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]RealModelInfo, 0, len(in))
	for _, rm := range in {
		if seen[rm.ID] {
			continue
		}
		seen[rm.ID] = true
		out = append(out, rm)
	}
	return out
}

// nameOf 取抽象模型的对外名（诊断文案用）；查不到就回 id，绝不猜。
func nameOf(res *Resolver, ctx context.Context, id string) string {
	m, err := res.store.GetAbstractModel(ctx, id)
	if err != nil || m == nil {
		return id
	}
	return m.Name
}
