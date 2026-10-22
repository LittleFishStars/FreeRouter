// server_build.go —— buildServer：把中间件链、路由与 v1 面板钉到 httpapi.Server。
//
// 这是 L1 固定顺序（01-overview.md 契约）的**唯一装配点**：
//
//	请求ID → 安全头 → 可信代理 → body上限 → 鉴权 → 限速 → 配额
//	→ [v1 面板内部：模型解析/IR 解析] → L2 → 响应编码
//
// 各中间件函数由功能包提供（mw_*.go），但「挂在哪、什么顺序」只在这里说
// 了算——这正是 task-8/9 约定「不改 server.go、装配归 Lead」的兑现。
package app

import (
	"context"
	"errors"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
	"github.com/LittleFishStars/FreeRouter/internal/webui"
)

// buildServer 构造 httpapi.Server 并完成三件事：
//  1. Server 本体（New——只带基础 Deps）；
//  2. v1 面板注入（AttachV1/AttachV1Models——M2 传输层就位前先挂目录，
//     推理候选源等 task-10 交付后补 AttachV1(up, cs)）；
//  3. 中间件与路由经 server.go 的 Handler() 挂载点。
//
// 中间件依赖注入采用「构造在装配层、注入在 Handler()」的模式：mwKeyAuth
// 等都是 Server 方法返回闭包，这里只负责造出它们的参数（authn/limiter/adapter）。
func (a *App) buildServer() *httpapi.Server {
	srv := httpapi.New(httpapi.Deps{
		Config:       a.cfg,
		Logger:       a.log,
		Ready:        a.readyCheck,
		ShuttingDown: func() bool { return false }, // main 侧原子标志经 SetShuttingDown 注入
	})

	// 判定链第 5 步（task-22）：候选源与模型目录都过可见性闸——
	// gatedCandidates 在解析前判（不可见 → ErrModelNotFound → 404，
	// 与"模型不存在"同形）；gatedCatalog 列表 ∩ 可见集。testBypass=true
	// 是占位通道兼容层（httpapi 既有 v1 测试用 InjectTestAccount 无
	// KeyPrincipal；生产带 Key 流量走严格判定），迁移后删。
	vis := a.newVisibilityDeps(
		modelCatalog{models: a.store.Repos().Models},
		newRoutingCandidates(a),
	)

	// v1 面板：模型目录先接（GET /v1/models 立即有数据）。
	srv.AttachV1Models(vis.catalog) // 第 5 步接线：列表 ∩ 主体可见集

	// 推理上游（task-10 M2 传输 + upstream_shim 装配桥）：Caller 同构面钉到
	// httpapi.Upstream，候选源查 real_models 按 UpstreamModelID 精确匹配
	//（M5 过渡——M6 换抽象模型路由后由策略引擎接管，接口不变）。
	// M6 接线：候选源换成策略引擎（Resolver→Filter→Rank→Cooldown），
	// realModelCandidates 保留在 upstream_shim.go 里作为 M5 形态的
	// 独立测试对象与回退参考（不再进生产路径）。
	shim, _ := a.newUpstreamShim()
	srv.AttachV1(shim, vis.candidates) // 候选源过第 5 步闸

	// 鉴权/限速/配额中间件的参数构造：
	//   keyAuthBridge —— store 两读 → auth.KeyAuthStore；
	//   meter.Limiter —— 全局闸三参数（0=不限制的 config 语义直传）；
	//   StoreAdapter  —— QuotaRepo + 组归属 → TryDebit。
	//
	// tokenBudget（token 预检估计值）：M5 用 max_body_bytes 的粗上界
	//（32 MiB 文本 ≈ 8M token 的量级，实际被账号行 tpm 闸收敛）；
	// M6 起换 per-model 预算表。留 TODO 不阻塞：预算值大 → 预检偏松，
	// 结算路径（多退少不补）会兜住总量。
	a.keyAuthn = auth.NewKeyAuthenticator(keyAuthBridge{
		keys:     a.store.Repos().Keys,
		accounts: a.store.Repos().Accounts,
	})
	a.limiter = meter.NewLimiter(meter.Config{
		GlobalRPM:         a.cfg.Limits.GlobalRPM,
		GlobalConcurrency: a.cfg.Limits.GlobalConcurrency,
		GlobalDailyTokens: a.cfg.Limits.GlobalDailyTokens,
		QueueTimeout:      a.cfg.Limits.QueueTimeout.Std(),
	})
	// 同一个 StoreAdapter 实例只建一次：debiter（判定面）与 quotaLedger
	// （记账面）必须是同一个对象——两个实例会让额度行的内存态与库态分叉。
	ledger := meter.NewStoreAdapter(a.store.Repos().Quotas, a.store.Repos().Groups)
	a.debiter = ledger
	a.quotaLedger = ledger

	// 终态结算出口（M1）：一次请求恰一次，额度面 + requests/usage_daily。
	// 不注入时 v1 请求照常服务但不记账（测试与降级态）。
	srv.SetSettler(a.newSettler())

	// 注册到 Server 的挂载点（Handler() 在 server.go 里逐条 r.Use）。
	srv.SetAuthChain(
		srv.MiddlewareKeyAuth(a.keyAuthn),
		srv.MiddlewareRate(a.limiter, a.rateLimitsOf, defaultTokenBudget),
		srv.MiddlewareQuota(a.debiter, a.modelIDOf, a.groupIDsSource, defaultTokenBudget),
	)
	// v1 路由由 Handler() 在鉴权链就位时自动挂载（server.go）——
	// 不需要独立的 MountV1 调用。
	// 控制台面（登录/登出/setup）：直接调 SetConsoleRoutes——不能走
	// a.AttachConsole()，它读 a.server，而 buildServer 里 a.server 尚未
	// 赋值（Setup 最后一步才 a.server = 返回值），nil 解引用。
	srv.SetConsoleRoutes(a.MountConsoleRoutes)

	// 控制台前端：注入 internal/webui 的静态处理器。两种构建形态由 webui
	// 自己的 build tag 决定（裸 go build 得到占位页，-tags webui_dist 得到
	// 真产物），装配层不知情、只负责注入；built 标志透给 /api/version 让
	// 运维一眼分辨「忘了 pnpm build 就重新编译」——这是本项目最容易出现的
	// 部署态错误，且两种情况的页面都能打开。
	srv.SetWebUI(webui.Handler(), webui.Built())
	return srv
}

// defaultTokenBudget 是 token 维度预检估计值。
const defaultTokenBudget = 1 << 23 // 8M token：32MiB 纯文本的保守上界

// modelIDOf 把请求体里的模型名翻成抽象模型 id。
// M5 阶段（无抽象模型表数据）返回 false：配额走合计行（mid=0），
// 404 判定归 v1 面板的 isRealModelName 接口位——两不抢。
func (a *App) modelIDOf(ctx context.Context, modelName string) (int64, bool) {
	m, err := a.store.Repos().Models.GetAbstractModelByName(ctx, modelName)
	if err != nil || m == nil {
		return 0, false
	}
	return m.ID, true
}

// readyCheck 供 readyz：sidecar 状态不作为门槛（sidecar 失败不阻塞
// 启动的同一条纪律——探针只回答「本进程能否服务」）。
func (a *App) readyCheck(ctx context.Context) error {
	if a.store == nil {
		return errNotReady
	}
	// 库打不开在 store.Open 就终止了启动，走不到这里；运行中库文件
	// 被删的极端场景由下一次写请求自然暴露——Store 不设独立 Ping。
	return nil
}

// groupMemberAdapter 满足 meter 的 groupMemberReader
// （GroupIDsOfAccount 由 task-12 落在 ModelRepo 上）。
type groupMemberAdapter struct {
	models groupMemberReader
}

type groupMemberReader interface {
	GroupIDsOfAccount(ctx context.Context, accountID int64) ([]int64, error)
}

func (g groupMemberAdapter) GroupIDsOfAccount(ctx context.Context, accountID int64) ([]int64, error) {
	return g.models.GroupIDsOfAccount(ctx, accountID)
}

// errNotReady 是 readyz 的兜底错误。
var errNotReady = errors.New("app: 存储未初始化")
