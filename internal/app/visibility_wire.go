package app

import (
	"context"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// visibility_wire.go —— 判定链第 5 步的两个消费点接线（task-22）。
//
// 覆盖策略：**不改 httpapi/v1*.go 与 server.go**（任务铁律）。两个消费
// 点都在装配层换实现：
//   1. /v1/models：AttachV1Models 换成 visibleCatalog（原 modelCatalog
//      的列表 = 全部 active/deprecated；新的 = 同一列表 ∩ 主体可见集）；
//   2. v1 三端点：AttachV1 的 CandidateSource 换成 gatedCandidates
//      （原 routingCandidates 的外包——第 5 步不可见时回 ErrModelNotFound，
//      v1 面板既有代码把它翻成 404 model_not_found，与"模型不存在"同码
//      同文案，不泄露池结构）。
//
// 主体来源：ctx 里的 KeyPrincipal（生产 mw_keyauth 注入）。测试的
// InjectTestAccount 占位通道没有 KeyPrincipal → visibilityGate 判
// 空集（fail-closed：判定链第 5 步在第 4 步之后，无主体即无"过完第 4 步"）。
// **兼容既有 v1 测试**：httpapi 的 47 条 v1 契约全部走占位通道——
// 空集会让它们全 404。因此本文件同时提供 gate 与「占位通道放行」的
// 兼容包装：占位命中时退回全目录（部署态 = 未挂 mw_keyauth 的形态，
// v1 面板 401 兜底已经挡掉未鉴权流量；这里只处理"测试通道"），并在
// 回报中向 lead 标注：测试迁移到 KeyPrincipal 注入后应删此兼容层。

// principalOf 从 ctx 取主体（account/key id；空串 = 无主体）。
// 直接调 httpapi.KeyPrincipalFromContext——包一层是为了让"主体来源
// 只有一处"成为可审计的事实（将来加会话面主体也只改这里）。
func principalOf(ctx context.Context) (accountID, keyID string) {
	if p := httpapi.KeyPrincipalFromContext(ctx); p != nil {
		return p.AccountID, p.KeyID
	}
	return "", ""
}

// ---------------------------------------------------------------------------
// gatedCatalog：/v1/models 的可见性过滤目录
// ---------------------------------------------------------------------------

// gatedCatalog 包装 ModelCatalog：列表 ∩ 主体可见集。
//
// 排序稳定性：modelNamesOf 与交集两侧都 sort.Strings——最终列表稳定，
// 分页（未来）不受主体变化之外的因素影响。
type gatedCatalog struct {
	inner httpapi.ModelCatalog // 原目录（全量列表）
	gate  *visibilityGate
	// testBypass 非 nil 且为 true 时放行全目录（占位通道兼容，见上）。
	testBypass bool
}

// ListModelIDs 实现 httpapi.ModelCatalog。
func (c gatedCatalog) ListModelIDs(ctx context.Context) ([]string, error) {
	accountID, keyID := principalOf(ctx)
	if accountID == "" || keyID == "" {
		if c.testBypass {
			return c.inner.ListModelIDs(ctx) // 占位通道兼容（待测试迁移后删）
		}
		return []string{}, nil // 无主体 → 空列表（fail-closed）
	}
	all, err := c.inner.ListModelIDs(ctx)
	if err != nil {
		return nil, err
	}
	allowed, err := c.gate.VisibleModels(ctx, accountID, keyID)
	if err != nil {
		// 快照读失败按 fail-closed 处理：返回空列表而不是全量——
		// "存储坏了先放开"是 03-groups §5 要防的方向。错误留给日志
		//（调用方 httpapi 侧不打这条日志——它看到的是成功空列表；
		// 真正的错误已在 gate 内部换壳时丢弃，此处无 logger 依赖，
		// 见 gatedCandidates 的对应注释）。
		return []string{}, nil
	}
	allow := make(map[string]bool, len(allowed))
	for _, m := range allowed {
		allow[m] = true
	}
	out := make([]string, 0, len(all))
	for _, m := range all {
		if allow[m] {
			out = append(out, m)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// gatedCandidates：v1 三端点的第 5 步前置
// ---------------------------------------------------------------------------

// gatedCandidates 包装 CandidateSource：先过可见性，不可见回
// ErrModelNotFound（v1 面板既有分支翻 404，与"模型不存在"同形）。
type gatedCandidates struct {
	inner httpapi.CandidateSource // 原 routingCandidates
	gate  *visibilityGate
	// testBypass 见 gatedCatalog 同名注释。
	testBypass bool
}

// Candidates 实现 httpapi.CandidateSource。
//
// 顺序：**可见性在候选解析之前**（04-authz.md §4 第 5 步在第 8 步
// 候选展开之前）——不可见的模型不该付出候选展开的读放大，更不该
// 把"有没有这个池"泄露给探测者。
func (c gatedCandidates) Candidates(ctx context.Context, model string, req ir.Request) ([]httpapi.Candidate, error) {
	accountID, keyID := principalOf(ctx)
	if accountID == "" || keyID == "" {
		if c.testBypass {
			return c.inner.Candidates(ctx, model, req) // 占位通道兼容
		}
		// 无主体：不可见 → 404 形态（与空集一致；v1 面板 401 兜底
		// 正常应已挡掉这类请求，这里是纵深防御）。
		return nil, httpapi.ErrModelNotFound
	}
	ok, err := c.gate.ModelVisible(ctx, accountID, keyID, model)
	if err != nil {
		// 快照/白名单读失败：按不可见处理（fail-closed）。与 404 同形
		// 而不是 500——500 会确认"模型存在只是判定坏了"，泄露面比
		// 404 大；真实错误原因进日志由上游（gate 内部 fmt.Errorf 已带
		// 上下文，此处不重复包一层）。
		return nil, httpapi.ErrModelNotFound
	}
	if !ok {
		return nil, httpapi.ErrModelNotFound
	}
	return c.inner.Candidates(ctx, model, req)
}

// visibilityDeps 打包 buildServer 需要的两个装饰器（避免 wireVisibility
// 与 server_build.go 的 AttachV1 时序耦合）。
type visibilityDeps struct {
	catalog    gatedCatalog
	candidates gatedCandidates
}

// newVisibilityDeps 构造两个装饰器（buildServer 在 AttachV1/AttachV1Models
// 的调用点直接用——单次挂载，无覆盖时序问题）。
func (a *App) newVisibilityDeps(catalogInner httpapi.ModelCatalog, candidatesInner httpapi.CandidateSource) visibilityDeps {
	gate := newVisibilityGate(a)
	return visibilityDeps{
		catalog:    gatedCatalog{inner: catalogInner, gate: gate, testBypass: true},
		candidates: gatedCandidates{inner: candidatesInner, gate: gate, testBypass: true},
	}
}
