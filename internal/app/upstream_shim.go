// Package app 内的 upstream_shim.go —— task-10 后续装配桥（lead 授权写域：
// internal/app/upstream_shim.go + 配套测试）。
//
// 它做两件事（caller.go 尾注释的装配薄壳，lead 指定落位于此）：
//  1. upstreamShim：把 upstream.Caller（同构 Call）钉到 httpapi.Upstream 接口；
//  2. realModelCandidates：httpapi.CandidateSource 的 real_models 查询版
//     （M5 过渡——M6 换抽象模型路由后由策略引擎接管，接口不变）。
package app

import (
	"context"
	"fmt"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// realModelSource 是候选查询对存储的最小读面（*store.ModelRepo 天然满足）。
//
// 为什么窄接口而不是直接收 *store.Store：shim 只需要两个读方法，
// 收整 Store 会把「哪些方法被装配层用到」这件事变模糊；测试 fake 也更容易。
type realModelSource interface {
	ListVisibleRealModels(ctx context.Context) ([]store.RealModel, error)
	ListUpstreams(ctx context.Context) ([]store.Upstream, error)
}

// 说明：app 是唯一允许同时 import store 与 httpapi 的层（app.go 包注释），
// 这里直接用 store.RealModel / store.Upstream——字段名与本文件用法编译期对齐。

// upstreamShim 把 upstream.Caller 适配成 httpapi.Upstream。
//
// 为什么需要一层壳而不是直接让 Caller 实现 httpapi.Upstream：
// httpapi.Candidate 与 upstream.CallCandidate 是两个包各自的类型，
// 依赖方向（L3 不 import L1）决定了必须在装配层搬运字段——这里就是
// caller.go 尾注释里那 ~20 行的正式落位。
type upstreamShim struct {
	caller *upstream.Caller
}

// Call 满足 httpapi.Upstream 接口（v1.go 的消费面）。
func (s upstreamShim) Call(ctx context.Context, cand httpapi.Candidate, req ir.Request, stream bool) (*httpapi.UpstreamCall, error) {
	res, err := s.caller.Call(ctx, upstream.CallCandidate{
		UpstreamModelID: cand.UpstreamModelID,
		Display:         cand.Display,
		Adapter:         cand.Adapter,
	}, req, stream)
	if err != nil {
		// Go error 仅编程错误（nil targetOf 等）——原样上抛（v1 面板 classifyErr
		// 兜底 internal_error），不吞成空应答。
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("app: upstream.Caller 返回空结果（编程错误）")
	}
	if res.Body != nil && res.Err != nil {
		// Body 与 Err 并存按 Err 优先（httpapi 的 proxyOnce 只看 Err；Body 留着
		// 会 defer Close 一个没人读的流）。
		_ = res.Body.Close()
	}
	return &httpapi.UpstreamCall{Body: res.Body, Err: res.Err}, nil
}

// realModelCandidates 是 M5 过渡的候选源：Candidates(ctx, model) 查
// ListVisibleRealModels 按 UpstreamModelID 精确匹配。
//
// M6 换抽象模型路由后由策略引擎接管（能力过滤/排序/健康降权），接口不变——
// 这正是 httpapi.CandidateSource 设计成接口的原因（v1.go 注释）。
type realModelCandidates struct {
	models realModelSource
	// 为什么每次查询 ListUpstreams 而不是构造时快照：上游行是低频写
	//（自举/管理台），逐次查的代价是一次毫秒级 SELECT，换来「新落行的
	// 上游立即生效」——目录刷新 10 分钟一轮，快照会让新上游晚两轮生效。
}

// Candidates 返回该模型名的候选序列（按 real_models 行序）。
func (c realModelCandidates) Candidates(ctx context.Context, model string) ([]httpapi.Candidate, error) {
	rows, err := c.models.ListVisibleRealModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("app: 查询可见真实模型失败: %w", err)
	}
	if len(rows) == 0 {
		// 空目录 → 空候选 → v1 面板走 no_candidate（503）。
		// 不在这里报错：空是合法状态（自举未跑/全部 missing）。
		return nil, nil
	}
	// 上游 kind 表：决定候选方言（relay→OpenAI Chat 约定；vendor/selfhosted
	// 按各自 base_url 的端点家族——当前 M5 全部 openai-chat，见下方注释）。
	ups, err := c.models.ListUpstreams(ctx)
	if err != nil {
		return nil, fmt.Errorf("app: 查询上游列表失败: %w", err)
	}
	kindByUpstream := make(map[int64]string, len(ups))
	for _, u := range ups {
		kindByUpstream[u.ID] = u.Kind
	}

	var out []httpapi.Candidate
	for _, r := range rows {
		if r.UpstreamModelID != model {
			continue
		}
		kind := kindByUpstream[r.UpstreamID]
		if kind == "" {
			// 上游行不存在（外键悬空——迁移期或手工删行）：跳过该模型，
			// 不让它变成「永远 502 的候选」。
			continue
		}
		adapter := adapterForKind(kind)
		if adapter == nil {
			continue
		}
		out = append(out, httpapi.Candidate{
			UpstreamModelID: r.UpstreamModelID,
			// Display 是内部日志/trace 用的落点描述（不外发，防泄露池结构——
			// v1.go Candidate 注释的同一纪律）。
			Display: fmt.Sprintf("%s/%s#%d", kind, r.UpstreamModelID, r.ID),
			Adapter: adapter,
		})
	}
	return out, nil
}

// adapterForKind 按上游 kind 选 IR 适配器。
//
// 当前口径（lead 指定）：relay→openai chat（04-sidecar 的 provider adapter
// 吐 OpenAI 形态流——DialectAdapter 的 Go 侧消费约定）；vendor/selfhosted
// 的端点家族在 M5 阶段全部按 openai-chat 出站（03-upstream.md §4「默认
// OpenAI 兼容」）。anthropic 方言留给 M6 候选声明（Dialect 列落库后），
// 这里预留分派点而不预制第二张表。
func adapterForKind(kind string) ir.Adapter {
	switch kind {
	case "relay", "vendor", "selfhosted":
		return openai.Adapter()
	default:
		// 未知 kind（数据坏行）：调用方跳过该候选。
		return nil
	}
}

// —— 装配桥（buildServer 用）：把 App 的运行件接成 shim + 候选源 ——

// newUpstreamShim 构造 M5 过渡期的 v1 上游面板依赖。
//
// targetOf 闭包：Candidate → 查行 → upstream.CandidateInfo → upstream.TargetOf。
// relay 的 base_url（sidecar://provider）在 TargetOf 里被剥掉（§9.3 红线）。
// 行缓存不设：一次 Candidates 查询 + 一次闭包内查询都是毫秒级 SQLite 读，
// 设缓存只会让「上游停用/启用」的生效时间变慢。
func (a *App) newUpstreamShim() (upstreamShim, *realModelCandidates) {
	http := upstream.NewHTTPTransport(nil, a.secretSource())
	relay := upstream.NewRelayTransport(a.sidecar)
	// relay 目标识别：Candidate→Target 需要 kind/base_url/real_model——
	// 这里从行查（与 Candidates 同一数据源，口径一致）。
	src := &appRealModelSource{app: a}
	candidates := &realModelCandidates{models: src}
	caller := upstream.NewCaller(relay, http, a.targetOf())
	return upstreamShim{caller: caller}, candidates
}

// appRealModelSource 把 App.store 的 ModelRepo 适配到 realModelSource
//（store 类型经 bridges.go 的别名进来，避免本文件再 import store）。
type appRealModelSource struct{ app *App }

func (s appRealModelSource) ListVisibleRealModels(ctx context.Context) ([]store.RealModel, error) {
	return s.app.store.Repos().Models.ListVisibleRealModels(ctx)
}

func (s appRealModelSource) ListUpstreams(ctx context.Context) ([]store.Upstream, error) {
	return s.app.store.Repos().Models.ListUpstreams(ctx)
}

// secretSource 是 HTTPTransport 的凭据源闭包：upstream_secrets 的 AES-GCM
// 解密在 store 侧。M5 过渡期 vendor 凭据入库路径未通（贡献流程是 M3/M4），
// 先返回空 token（无鉴权 selfhosted 可用；vendor 待贡献流程接通后补）。
//
// 为什么现在就留接口位：secret 的解密 API 形状在 store 侧稳定后，
// 这里只改一行——装配层的「先钉形状后填肉」与 UpstreamWriter 桥同策略。
func (a *App) secretSource() upstream.SecretSource {
	return func(ctx context.Context, secretRef string) (string, error) {
		// TODO(M4 贡献流程)：接 store 的凭据解密（secretRef → 明文 Bearer）。
		// 返回空串 = 无鉴权：与「SecretRef 为空」同效（transport.go 只在
		// token 非空时带 Authorization 头）。
		return "", nil
	}
}

// targetOf 构造 Caller 的候选→Target 闭包。
func (a *App) targetOf() func(upstream.CallCandidate) (upstream.Target, error) {
	src := appRealModelSource{app: a}
	return func(cand upstream.CallCandidate) (upstream.Target, error) {
		// 按 UpstreamModelID 精确匹配可见行（与 Candidates 同口径——
		// 两处用同一个查询保证「展开的候选」与「执行的目标」永远一致）。
		rows, err := src.ListVisibleRealModels(context.Background())
		if err != nil {
			return upstream.Target{}, err
		}
		ups, err := src.ListUpstreams(context.Background())
		if err != nil {
			return upstream.Target{}, err
		}
		kindByUpstream := make(map[int64]store.Upstream, len(ups))
		for _, u := range ups {
			kindByUpstream[u.ID] = u
		}
		for _, r := range rows {
			if r.UpstreamModelID != cand.UpstreamModelID {
				continue
			}
			u, ok := kindByUpstream[r.UpstreamID]
			if !ok {
				continue
			}
			info := upstream.CandidateInfo{
				UpstreamID: r.UpstreamID,
				Kind:       upstream.Kind(u.Kind),
				BaseURL:    u.BaseURL,
				RealModel:  r.UpstreamModelID,
			}
			// relay 行的 RealModel 需要 provider 前缀（RelayTransport 拆分用）：
			// relay 的 base_url 是 sidecar://<provider>，TargetOf 校验一致性。
			if info.Kind == upstream.KindRelay && upstream.IsRelayBaseURL(u.BaseURL) {
				info.RealModel = u.BaseURL[len(upstream.RelayBaseURLPrefix):] + "/" + r.UpstreamModelID
			}
			return upstream.TargetOf(info)
		}
		return upstream.Target{}, fmt.Errorf("app: 候选 %q 在 real_models 中不可见（已停用/隐藏/消失）", cand.UpstreamModelID)
	}
}

// 编译期断言：两个装配桥必须满足 httpapi 的消费面（改接口名会在这里炸）。
var (
	_ httpapi.Upstream        = upstreamShim{}
	_ httpapi.CandidateSource = (*realModelCandidates)(nil)
)
