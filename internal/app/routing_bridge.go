// routing_bridge.go —— M6（抽象模型与策略路由）的装配桥（lead 写域）。
//
// 三件事，全部是「跨类型搬运」，所以只能待在装配层：
//  1. abstractModelBridge：store.ModelRepo → routing.AbstractModelStore。
//     唯一的翻译点是「不存在」的表示：store 返回 (nil, ErrNotFound)，
//     routing 契约要求 (nil, nil)——这两种语义在决策层分别对应 500 与
//     404/「继续找」，翻错是行为错误而不是风格问题。
//  2. routingCandidates：httpapi.CandidateSource 的策略实现。把解析
//     （Resolver，含弃用链）→ 过滤（FilterPaths）→ 排序（Rank）→ 冷却
//     （Cooldowns.Apply）串起来，并在候选失败时回填冷却账本
//     （CandidateFailureNotifier）。
//  3. profileFromRequest / pathsOf / capsOf：从 ir.Request 与 store 行
//     抽出 routing 需要的画像与能力面。
//
// 为什么排序键（余额/延迟/到期）现在还是零值：M4 的用量账本与 TTFT 采样
// 尚未落地（05-storage.md §6 usage_daily 有表，但 speed 采样与 credits
// 余额查询分别属 M4/M8）。零值在 routing 里是「无键垫底」——正是「没有
// 测量数据就不参与排序」的忠实在场，数据面接上再填。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/routing"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// upstreamSnapshotTTL 是上游表快照的有效期。
//
// 为什么 1s：它管的是**凭据/上游启停的生效窗口**，不是目录刷新周期
// （目录新鲜度由 upstream.model_refresh_interval 管，10 分钟）。控制台点
// 「禁用」后新请求最迟 1 秒生效——远小于任何人工或自动化切换的容差；且错
// 误方向不对称：多放行 1 秒 = 一个请求打到刚禁用的上游（上游拒绝，客户端
// 可重试），少放行 = 该上游整段 503。
//
// 一次请求会为每个候选查一次 GetRealModel（各自要上游 kind/channel），
// 不加缓存就是 N 次全表 SELECT。1 秒窗口足够覆盖单请求，又短到「刚禁用
// 的上游」最多晚 1 秒生效——目录刷新是 10 分钟一轮，1 秒的偏差可忽略。
const upstreamSnapshotTTL = time.Second

// ============ ① store → routing 读面 ============

// abstractModelBridge 实现 routing.AbstractModelStore。
type abstractModelBridge struct {
	models *store.ModelRepo
	log    *slog.Logger

	mu        sync.Mutex
	upstreams map[int64]store.Upstream
	snapAt    time.Time
}

func newAbstractModelBridge(app *App) *abstractModelBridge {
	return &abstractModelBridge{models: app.store.Repos().Models, log: app.log}
}

// GetAbstractModelByName 按对外名查。不存在 → (nil, nil)（routing 契约；
// store 的 ErrNotFound 在这里翻译掉，见文件头）。
func (b *abstractModelBridge) GetAbstractModelByName(ctx context.Context, name string) (*routing.AbstractModel, error) {
	m, err := b.models.GetAbstractModelByName(ctx, name)
	return convertAbstractModel(m, err)
}

// GetAbstractModel 按 id 查（弃用链逐跳）。
func (b *abstractModelBridge) GetAbstractModel(ctx context.Context, id int64) (*routing.AbstractModel, error) {
	m, err := b.models.GetAbstractModel(ctx, id)
	return convertAbstractModel(m, err)
}

// convertAbstractModel 做行 → 读面转换并把 ErrNotFound 归一成 nil,nil。
func convertAbstractModel(m *store.AbstractModel, err error) (*routing.AbstractModel, error) {
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("app: 读取抽象模型失败: %w", err)
	}
	if m == nil {
		return nil, nil
	}
	return &routing.AbstractModel{
		ID:                 m.ID,
		Name:               m.Name,
		Status:             routing.AbstractModelStatus(m.Status),
		Strategy:           parseStringList(m.Strategy),
		CapabilityMode:     routing.CapabilityMode(m.CapabilityMode),
		ReplacementModelID: m.ReplacementModelID,
		DeprecatedAt:       unixTime(m.DeprecatedAt),
		SunsetAt:           unixTime(m.SunsetAt),
	}, nil
}

// ListCandidates 列候选声明（行序即配置序），约束 JSON 在这里解析成
// routing.Constraint——store 存原文是刻意的（05-storage.md §4：约束是不透明
// 声明，只在读时解释），解释责任落装配层。
func (b *abstractModelBridge) ListCandidates(ctx context.Context, abstractModelID int64) ([]routing.CandidateDecl, error) {
	rows, err := b.models.ListCandidates(ctx, abstractModelID)
	if err != nil {
		return nil, fmt.Errorf("app: 查询候选声明 %d 失败: %w", abstractModelID, err)
	}
	out := make([]routing.CandidateDecl, 0, len(rows))
	for _, r := range rows {
		out = append(out, routing.CandidateDecl{
			RealModelID: r.RealModelID,
			Constraint:  parseConstraint(r.ConstraintJSON),
			Weight:      r.Weight,
		})
	}
	return out, nil
}

// GetRealModel 按 id 查真实模型读面；不存在 → (nil, nil)。
func (b *abstractModelBridge) GetRealModel(ctx context.Context, id int64) (*routing.RealModelInfo, error) {
	rm, err := b.models.GetRealModel(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("app: 读取真实模型 %d 失败: %w", id, err)
	}
	if rm == nil {
		return nil, nil
	}
	ups, err := b.upstreamSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	u, ok := ups[rm.UpstreamID]
	if !ok {
		// 外键悬空（迁移期/手工删行）：按「不存在」处理，让路由剔除该候选
		// 而不是把一次查询失败变成全池 500。
		b.log.Warn("routing: 真实模型的上游行缺失", "real_model_id", rm.ID, "upstream_id", rm.UpstreamID)
		return nil, nil
	}
	return &routing.RealModelInfo{
		ID:                     rm.ID,
		UpstreamID:             rm.UpstreamID,
		UpstreamModelID:        rm.UpstreamModelID,
		UpstreamKind:           u.Kind,
		Channel:                channelOf(rm.Channel, u),
		ContextWindow:          rm.ContextWindow,
		Modalities:             parseJSONList(rm.Modalities),
		Efforts:                parseJSONList(rm.Efforts),
		Status:                 string(rm.Status),
		ExcludedFromCandidates: rm.ExcludedFromCandidates,
		UpstreamEnabled:        u.Enabled,
	}, nil
}

// upstreamSnapshot 返回上游表快照（TTL 内复用）。
func (b *abstractModelBridge) upstreamSnapshot(ctx context.Context) (map[int64]store.Upstream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.upstreams != nil && time.Since(b.snapAt) < upstreamSnapshotTTL {
		return b.upstreams, nil
	}
	rows, err := b.models.ListUpstreams(ctx)
	if err != nil {
		return nil, fmt.Errorf("app: 查询上游列表失败: %w", err)
	}
	snap := make(map[int64]store.Upstream, len(rows))
	for _, u := range rows {
		snap[u.ID] = u
	}
	b.upstreams = snap
	b.snapAt = time.Now()
	return snap, nil
}

// channelOf 取渠道名，两级来源（01-catalog.md §2.3）。
//
// 权威来源是模型级 real_models.channel（0002 迁移起落库）：由刷新器
// 写 owned_by 的权威值，拿不到时从限定名「渠道/模型」首段派生。
// 渠道不可知时该列为 NULL（不是空串）。
//
// 上游行级渠道（upstreams.name / sidecar://provider）只作兜底——
// 它是管理员的运营命名，可能与上游实际归属不一致（一个 provider
// 里既能拉 deepseek 也能拉 openai 时就是这种情况），所以是次级来源。
func channelOf(ch *string, u store.Upstream) string {
	if ch != nil && *ch != "" {
		return *ch
	}
	return u.Name
}

// ============ ② 候选源（策略引擎） ============

// routingCandidates 是 httpapi.CandidateSource 的 M6 实现
// （替换 M5 的 realModelCandidates）。
type routingCandidates struct {
	app      *App
	bridge   *abstractModelBridge
	resolver *routing.Resolver
	cd       *routing.Cooldowns
	// balance 是后台余额探测（balance 排序键）。nil = 未装配。
	balance *BalanceLoop

	mu  sync.Mutex
	rrs map[int64]*routing.RoundRobinCursor // 按抽象模型持有游标（03-strategy §3.4）
}

func newRoutingCandidates(app *App) *routingCandidates {
	b := newAbstractModelBridge(app)
	return &routingCandidates{
		app:      app,
		bridge:   b,
		resolver: routing.NewResolver(b),
		cd:       routing.NewCooldowns(),
		balance:  app.balanceLoop,
		rrs:      map[int64]*routing.RoundRobinCursor{},
	}
}

// Candidates 解析模型名并返回排好序的候选序列。
//
// 错误语义（v1 面板据此写状态码）：
//   - ErrModelNotFound → 404 model_not_found（换模型是客户端的正确动作）；
//   - 空切片 → 503 no_candidate（稍后重试，不是改代码）；
//   - 其余 error → 500（存储故障）。
func (c *routingCandidates) Candidates(ctx context.Context, model string, req ir.Request) ([]httpapi.Candidate, error) {
	resolved, rerr, err := c.resolver.Resolve(ctx, model)
	if err != nil {
		return nil, fmt.Errorf("app: 解析抽象模型 %q 失败: %w", model, err)
	}
	if rerr != nil {
		if rerr.Code == "model_not_found" {
			return nil, httpapi.ErrModelNotFound
		}
		// no_candidate：空集 → 503（03-errors.md §3.1）。
		c.app.log.Info("routing: 无可用候选", "model", model, "detail", rerr.Detail)
		return nil, nil
	}

	profile := profileFromRequest(req)
	paths := c.pathsOf(ctx, resolved)
	kept, outs := routing.FilterPaths(profile, paths)
	chain := resolved.Model.Strategy
	ordered := routing.Rank(kept, chain, c.cursorOf(resolved.Model.ID))
	ordered = c.cd.Apply(ordered)

	// 诊断回填：无论有没有候选都写（「为什么 503」的答案就在这里）。
	httpapi.FillV1Trace(ctx, routing.BuildTrace(resolved, kept, outs, ordered, chain))

	if len(ordered) == 0 {
		c.app.log.Info("routing: 候选全部被过滤",
			"model", model, "declared", len(paths), "filtered_out", len(outs))
		return nil, nil
	}

	out := make([]httpapi.Candidate, 0, len(ordered))
	for _, p := range ordered {
		adapter := adapterForKind(p.RealModel.UpstreamKind)
		if adapter == nil {
			// 未知 kind（坏行）：跳过而不是让请求 502。
			c.app.log.Warn("routing: 候选上游 kind 未知",
				"real_model_id", p.RealModel.ID, "kind", p.RealModel.UpstreamKind)
			continue
		}
		out = append(out, httpapi.Candidate{
			UpstreamModelID: p.RealModel.UpstreamModelID,
			// Display 走渠道名而非上游行 id：日志可读且不泄露行号对外
			//（Display 只在内部日志/trace 出现，v1.go Candidate 注释）。
			Display: fmt.Sprintf("%s/%s#%d", p.RealModel.Channel, p.RealModel.UpstreamModelID, p.RealModel.ID),
			Adapter: adapter,
			ID:      p.RealModel.ID,
		})
	}
	return out, nil
}

// NotifyCandidateFailure 回填冷却账本（httpapi 在候选失败后调用）。
//
// 只记 real_models 行 ID：冷却的作用域是「这条上游路径」，不是模型名——
// 同名模型可能有多条路径，一条坏了不该牵连其它（cooldown.go 实体=路径）。
func (c *routingCandidates) NotifyCandidateFailure(cand httpapi.Candidate) {
	if cand.ID <= 0 {
		return // 来源不提供行 ID（测试 fake/旧源）：跳过冷却。
	}
	c.cd.MarkFailure(cand.ID, 0)
}

// cursorOf 取（或创建）抽象模型的 round-robin 游标。
func (c *routingCandidates) cursorOf(abstractModelID int64) *routing.RoundRobinCursor {
	c.mu.Lock()
	defer c.mu.Unlock()
	rr, ok := c.rrs[abstractModelID]
	if !ok {
		rr = &routing.RoundRobinCursor{}
		c.rrs[abstractModelID] = rr
	}
	return rr
}

// pathsOf 把展开后的候选配上门声明约束与能力面。
//
// 为什么要再查一次 ListCandidates：routing.Resolved.Candidates 只带
// RealModelInfo（不含约束——约束是声明层数据，决策层只在 FilterPaths 里
// 经 Path.Constraint 消费）。同一请求内两次读取同一批行是刻意的取舍：
// 让 routing 的读面保持「无未使用字段」（多带一个 Constraint 字段会让
// 声明/运行时两个概念在同一个结构里混淆）。
func (c *routingCandidates) pathsOf(ctx context.Context, resolved *routing.Resolved) []routing.Path {
	decls, err := c.bridge.ListCandidates(ctx, resolved.Terminal.ID)
	if err != nil {
		// 读不到约束不等于没候选：继续走下去（约束全零=any，放行）——
		// 一次读失败不该让整池 503，日志留痕供排障。
		c.app.log.Warn("routing: 读取候选声明失败，按无约束继续",
			"model", resolved.Model.Name, "error", err)
	}
	byModel := make(map[int64]routing.Constraint, len(decls))
	for _, d := range decls {
		byModel[d.RealModelID] = d.Constraint
	}

	paths := make([]routing.Path, 0, len(resolved.Candidates))
	for _, rm := range resolved.Candidates {
		paths = append(paths, routing.Path{
			RealModel:  rm,
			Constraint: byModel[rm.ID],
			Caps:       capsOf(rm),
			// 排序键：TTFT/TotalMs 来自 latencySampler，Balance/ExpiresAt 来自
			// balanceLoop（均为各自的后台测量，无样本时保持 -1/零值垫底）。
			//
			// 注意 TTFTMs 无样本时必须 -1：speed 键判 <0 为无样本垫底，
			// 零值 0 会被读成「TTFT 0 毫秒的极速模型」排到最前。
			ExpiresAt:       c.expiresAtOf(rm.UpstreamID),
			Balance:         c.balanceOf(rm.UpstreamID),
			BalanceKnown:    c.balanceKnown(rm.UpstreamID),
			TTFTMs:          c.ttftMs(rm),
			TotalMs:         c.totalMs(rm),
			PriceMultiplier: 1,     // 无价格数据时取基线 1（03-strategy §2.5.2）
			CooledDown:      false, // 冷却由 Cooldowns.Apply 统一标注
		})
	}
	return paths
}

// capsOf 归一候选能力面（01-catalog §2.3 探测 + real_models 行字段）。
//
// 两个已知空缺，都在这里显式 fail-open/fail-closed 而不是留给下游猜：
//   - SupportsTools：目录不提供 tools 能力字段（探测未落地），按支持放行。
//     理由：带工具请求打到不支持的模型会拿到上游 400（invalid_request，
//     客户端看到的是准确错误），比「全池 503」好得多；
//   - ContextWindow：nil 时 FilterPaths 已按保守下界剔除——这是刻意的
//     （宁可 503 让客户端换小请求，不可中途截断），本函数不做兜底。
func capsOf(rm routing.RealModelInfo) routing.Capabilities {
	return routing.Capabilities{
		ContextWindow:  rm.ContextWindow,
		Modalities:     rm.Modalities,
		Efforts:        rm.Efforts,
		SupportsTools:  true,
		SupportsImages: containsString(rm.Modalities, "image"),
	}
}

// ============ ③ 请求画像 ============

// profileFromRequest 从 IR 请求抽过滤画像（03-strategy.md §2.1 F1/F2）。
func profileFromRequest(req ir.Request) routing.RequestProfile {
	p := routing.RequestProfile{
		NeedTools:       len(req.Tools) > 0,
		MaxOutputTokens: 0,
	}
	if req.Sampling.MaxOutput != nil && *req.Sampling.MaxOutput > 0 {
		p.MaxOutputTokens = int64(*req.Sampling.MaxOutput)
	}
	if req.Effort != ir.EffortUnspecified {
		// unknown 档位照传：routing 的 effortSupported 会按「未知=不支持」
		// 过滤，被全滤则是 503（请求本身没写错）——与原口径一致。
		p.NeedEffort = string(req.Effort)
	}

	// 只把「真正需要额外模态」的项放进去：text 是默认能力，放进
	// NeedModalities 会让未声明 modalities 的候选（relay 目录不报）在
	// 纯文本请求上被「未知=不支持」误剔。
	var runes int64
	for _, msg := range req.Messages {
		runes += int64(utf8.RuneCountInString(msg.Text()))
		for _, part := range msg.Parts {
			switch p2 := part.(type) {
			case ir.ImagePart:
				if !containsString(p.NeedModalities, "image") {
					p.NeedModalities = append(p.NeedModalities, "image")
				}
			case ir.ToolCallPart:
				// 历史里有工具调用 → 该请求需要工具能力（即使本次
				// Tools 为空，模型仍要能理解这段历史）。
				p.NeedTools = true
			case ir.ToolResultPart:
				p.NeedTools = true
			case ir.TextPart:
				runes += int64(utf8.RuneCountInString(p2.Text))
			}
		}
	}
	// 粗估：中文 1 字≈1 token（保守取上界），英文会被高估——高估方向安全
	// （F2 是「宁可 503 不可截断」），故按 rune 数计而不是 /4。
	p.EstInputTokens = runes + 256 // +256：role/工具定义/协议封装的固定开销

	return p
}

// ============ 小工具 ============

// parseStringList 解析 strategy 列的 JSON 数组（坏数据 → 空链 = 默认链）。
func parseStringList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// 保存级校验拦配置；运行时遇坏数据退化默认链而不是 500
		//（与 FilterPaths 对未知模式的容错同一哲学）。
		return nil
	}
	return out
}

// parseJSONList 解析 *string 形式的 JSON 数组（modalities/efforts）。
func parseJSONList(raw *string) []string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil
	}
	return out
}

// constraintJSON 是 abstract_candidates.constraint 的落库形状
// （02-abstract.md §3.1）。字段名与文档的 JSON 键逐字一致。
type constraintJSON struct {
	ChannelScope struct {
		Mode     string   `json:"mode"`
		Channels []string `json:"channels"`
	} `json:"channel_scope"`
	CredentialScope struct {
		Mode string `json:"mode"`
	} `json:"credential_scope"`
	RequireModalities []string `json:"require_modalities"`
	MinContext        int64    `json:"min_context"`
	RequireEfforts    []string `json:"require_efforts"`
}

// parseConstraint 把约束 JSON 解析成决策层结构。
//
// 坏 JSON / 空值一律退化成零值约束（channel any、无下限）而不是报错：
// 声明数据来自管理台写入（保存时已校验），运行时再拦一道会让「一行坏
// 数据」变成整池 503，代价不对称（与 FilterPaths 的容错口径一致）。
func parseConstraint(raw *string) routing.Constraint {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return routing.Constraint{ChannelMode: "any"}
	}
	var cj constraintJSON
	if err := json.Unmarshal([]byte(*raw), &cj); err != nil {
		return routing.Constraint{ChannelMode: "any"}
	}
	mode := cj.ChannelScope.Mode
	if mode == "" {
		mode = "any"
	}
	return routing.Constraint{
		ChannelMode:       mode,
		Channels:          cj.ChannelScope.Channels,
		RequireModalities: cj.RequireModalities,
		MinContext:        cj.MinContext,
		RequireEfforts:    cj.RequireEfforts,
	}
}

// containsString 判切片是否含某值。
func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// latencySyncBridge 把 httpapi 的「按 realModelID 记样本」转成采样器的
// 「按 (UpstreamID, RealModel) 存」。realModelID → 行信息的查表这里用
// store 读 API，不缓存（调用频率低，且依赖 real_models 变更即时可见）。
//
// 为什么需要单独一个类型而不是直接复用 upstream.LatencySampler：
// httpapi.LatencySink 的入参是 realModelID（real_models 行 id），而采样器
// 的键是 (UpstreamID, RealModel)——routing 的 RealModelInfo 带后两者。
// 中间这层翻译就是本类型；若直接改采样器键为 realModelID 也行，但那样
// routing 读的时候还得反查，反而多一次反查。
type latencyDefaultSink struct {
	sampler *upstream.LatencySampler
	bridge  *abstractModelBridge
}

// RecordLatency 实现 httpapi.LatencySink。
func (l latencyDefaultSink) RecordLatency(ctx context.Context, realModelID int64, ttftMs, totalMs int64) {
	if l.sampler == nil || realModelID <= 0 {
		return
	}
	// 查 real_model → (UpstreamID, RealModel)。查询失败按未知行丢弃，
	// 不中断请求（采样是旁路）。
	upID, modelID, ok := l.resolveReal(ctx, realModelID)
	if !ok {
		return
	}
	l.sampler.RecordSuccess(ctx, upstream.SamplerKey{
		UpstreamID: upID,
		RealModel:  modelID,
	}, float64(ttftMs), float64(totalMs), time.Now())
}

// resolveReal 解析 realModelID → (UpstreamID, RealModel)。ok=false 表示未知行。
func (l latencyDefaultSink) resolveReal(ctx context.Context, realModelID int64) (int64, string, bool) {
	if l.bridge == nil {
		return 0, "", false
	}
	info, err := l.bridge.GetRealModel(ctx, realModelID)
	if err != nil || info == nil {
		return 0, "", false
	}
	return info.UpstreamID, info.UpstreamModelID, true
}

// ttftMs / totalMs 从采样器读某候选的平滑 TTFT/总耗时（毫秒）。
//
// 无采样器（参数装配层没建）或无样本时返回 -1（无样本垫底）。
func (c *routingCandidates) ttftMs(rm routing.RealModelInfo) int64 {
	return c.latencyMs(rm, true)
}

func (c *routingCandidates) totalMs(rm routing.RealModelInfo) int64 {
	return c.latencyMs(rm, false)
}

func (c *routingCandidates) latencyMs(rm routing.RealModelInfo, ttft bool) int64 {
	if c.app == nil || c.app.latencySampler == nil {
		return -1
	}
	snap := c.app.latencySampler.Latency(upstream.SamplerKey{
		UpstreamID: rm.UpstreamID,
		RealModel:  rm.UpstreamModelID,
	})
	if ttft {
		return int64(snap.TTFTMs)
	}
	return int64(snap.TotalMs)
}

// balanceOf 返回某上游的余额（weight 量级未知时为 0）。
func (c *routingCandidates) balanceOf(upstreamID int64) float64 {
	if c.balance == nil {
		return 0
	}
	br, ok := c.balance.BalanceOfUpstream(context.Background(), upstreamID)
	if !ok {
		return 0
	}
	return br.Balance
}

// balanceKnown 报告该上游的余额是否已知（未知则 balanceOf 无意义）。
func (c *routingCandidates) balanceKnown(upstreamID int64) bool {
	if c.balance == nil {
		return false
	}
	_, ok := c.balance.BalanceOfUpstream(context.Background(), upstreamID)
	return ok
}

// expiresAtOf 返回该上游的到期时间（无则 0）。
func (c *routingCandidates) expiresAtOf(upstreamID int64) int64 {
	if c.balance == nil {
		return 0
	}
	br, ok := c.balance.BalanceOfUpstream(context.Background(), upstreamID)
	if !ok || br.ExpiresAt.IsZero() {
		return 0
	}
	return br.ExpiresAt.Unix()
}
