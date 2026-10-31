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
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"strings"

	"encoding/json"
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
func (c realModelCandidates) Candidates(ctx context.Context, model string, _ ir.Request) ([]httpapi.Candidate, error) {
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
	kindByUpstream := make(map[string]string, len(ups))
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
			Display: fmt.Sprintf("%s/%s#%s", kind, r.UpstreamModelID, r.ID),
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
	// 启动期先自检凭据链路：装配了但解不开的上游会以**无鉴权**请求出站，
	// 那是最难排查的一类故障（现场在上游、原因在本地）。自检失败只告警不阻塞——
	// 无鉴权 selfhosted 是合法配置，凭据问题不该挡住整个进程。
	a.auditCredentialWiring(context.Background())
	http := upstream.NewHTTPTransport(nil, a.secretSource())
	// relay 传输带凭据源（2026-10-31 qodercn 移植）：贡献凭据解密后单帧传给
	// sidecar 的 adapter——relay 类上游不再依赖 DSH 账号池。
	relay := upstream.NewRelayTransport(a.sidecar, a.credentialJSONSource(), a.credentialWriteBack())
	// relay 目标识别：Candidate→Target 需要 kind/base_url/real_model——
	// 这里从行查（与 Candidates 同一数据源，口径一致）。
	src := &appRealModelSource{app: a}
	candidates := &realModelCandidates{models: src}
	caller := upstream.NewCaller(relay, http, a.targetOf())
	return upstreamShim{caller: caller}, candidates
}

// appRealModelSource 把 App.store 的 ModelRepo 适配到 realModelSource
// （store 类型经 bridges.go 的别名进来，避免本文件再 import store）。
type appRealModelSource struct{ app *App }

func (s appRealModelSource) ListVisibleRealModels(ctx context.Context) ([]store.RealModel, error) {
	return s.app.store.Repos().Models.ListVisibleRealModels(ctx)
}

func (s appRealModelSource) ListUpstreams(ctx context.Context) ([]store.Upstream, error) {
	return s.app.store.Repos().Models.ListUpstreams(ctx)
}

// secretSource 是 HTTPTransport 的凭据源闭包：secretRef → 明文 Bearer。
//
// ## 接线（M8 贡献流程）
//
// secretRef 由贡献流程写库时确定（internal/upstream/contribution.go 的 Submit）：
// 纯十进制 = upstream_secrets.id。解密在 store 侧（AES-256-GCM，主密钥来自 0600 文件、
// 不入库），本闭包只做「引用 → 解密 → 明文」这一次翻译，拿到的明文**只在一次请求的
// 生命周期内存活**（02-credentials 6.4），不进日志、不缓存、不回显。
//
// ## 红线（04-sidecar 4.1 / 02-credentials 6.4）
//
// 这里**只解密**，没有对应的写回路径：DSH 侧的续期与 FreeRouter 自己的提交是两条
// 互不相交的写路径（前者写 .credentials.yaml，后者写 upstream_secrets），
// 我们绝不把 token 写回 DSH。
//
// ## 为什么旧实现是「恒返回空串」而现在必须显式报
//
// 旧实现的注释写着「M5 过渡期先返回空 token」——那是一条**静默降级**：
// transport.go 只在 token 非空时带 Authorization 头，于是配了 vendor 凭据的上游会以
// **无鉴权**请求打到上游，用户看到的是上游 401/502（upstream_error/upstream_unavailable），
// 而真实原因是「我们没带凭据」。故障现场因此漂到上游，排查方向全错（C6 精神）。
//
// 现在：能解密就带 Bearer；解不出来就**返回错误**（transport 归到 ErrAuth，
// 上层按鉴权失败换候选/告警），不再假装成功。剩余的唯一「无鉴权」情形是
// secretRef 为空——那是 selfhosted（如本机 ollama）的正当用法。
func (a *App) secretSource() upstream.SecretSource {
	return func(ctx context.Context, secretRef string) (string, error) {
		ref := strings.TrimSpace(secretRef)
		if ref == "" {
			// 无引用 = 该上游没有密钥材料：无鉴权 selfhosted 的合法形态。
			return "", nil
		}
		secretID, err := parseSecretRefID(ref)
		if err != nil {
			// 引用形态不认识：属于数据/装配错误（不是"没有凭据"），如实报出来。
			// 错误信息里只带引用本身，不带任何密钥片段。
			return "", fmt.Errorf("app: secretRef %q 不是合法的密文引用：%w", ref, err)
		}
		plain, err := a.store.Repos().Secrets.GetSecret(ctx, secretID)
		if err != nil {
			// 三种失败各自可归因（store 已把它们分成不同类型）：
			//   不存在 → 配置缺失，凭据应标 invalid；
			//   解密失败 → 安全事件（密钥不符/密文被篡改），要告警而非重试；
			//   其它 → 主密钥不可用等，运维处理。
			return "", fmt.Errorf("app: 解密上游凭据失败（secret id=%s）：%w", secretID, err)
		}
		token := strings.TrimSpace(string(plain))
		if token == "" {
			return "", fmt.Errorf("app: 上游凭据解密结果为空（secret id=%s）", secretID)
		}
		// OAuth 反代型凭据的明文是 JSON 对象（oauth.exchange 产出）——Bearer 需要的
		// 是其中的 access_token，不是整段 JSON（2026-10-31 实测：整段 JSON 当
		// Bearer 发给 loomy 网关等于没认证）。API 型凭据是裸 key 字符串，
		// 不是 JSON，原样返回。无 access_token 的 JSON 原样返回（宁可上游
		// 401 也不要猜字段）。
		var obj map[string]any
		if json.Unmarshal([]byte(token), &obj) == nil {
			if at, ok := obj["access_token"].(string); ok && strings.TrimSpace(at) != "" {
				return strings.TrimSpace(at), nil
			}
		}
		return token, nil
	}
}

// credentialJSONSource 按 SecretRef 取**凭据明文原文**——relay 推理专用。
// 与 secretSource 的差异：OAuth 反代凭据的明文是 JSON 对象（含 access_token/
// machine_id/uid），sidecar 的 adapter 要吃完整对象；secretSource 走的是
// 「提取 Bearer token」的 vendor 口径。解密与错误归同一份实现。
func (a *App) credentialJSONSource() upstream.RawCredentialSource {
	return func(ctx context.Context, secretRef string) (json.RawMessage, error) {
		ref := strings.TrimSpace(secretRef)
		if ref == "" {
			return nil, fmt.Errorf("app: relay 候选缺少凭据引用")
		}
		secretID, err := parseSecretRefID(ref)
		if err != nil {
			return nil, fmt.Errorf("app: secretRef %q 不是合法的密文引用：%w", ref, err)
		}
		plain, err := a.store.Repos().Secrets.GetSecret(ctx, secretID)
		if err != nil {
			return nil, fmt.Errorf("app: 解密上游凭据失败（secret id=%s）：%w", secretID, err)
		}
		return json.RawMessage(plain), nil
	}
}

// credentialWriteBack 把 sidecar 续期出的新凭据写回密文（relay 推理的
// 凭据轮换，2026-10-31）。secretRef 就是 upstream_secrets.id，直接按行更新；
// 写回失败由调用方记日志（不影响本次推理结果）。
func (a *App) credentialWriteBack() upstream.CredentialWriteBack {
	return func(ctx context.Context, secretRef string, fresh json.RawMessage) error {
		secretID, err := parseSecretRefID(secretRef)
		if err != nil {
			return fmt.Errorf("app: secretRef %q 不是合法的密文引用：%w", secretRef, err)
		}
		return a.store.Repos().Secrets.UpdatePlaintextBySecretID(ctx, secretID, fresh)
	}
}

// parseSecretRefID 校验 secretRef 的 id 形状。
//
// 2026-10-26 UUID 改造后 secret_ref 列**直接存 upstream_secrets.id（UUID 文本）**，
// 不再需要十进制解析——旧实现在这里把 "42" 折成整数并拒绝含非数字字符的形态，
// 那层值域约定随整型主键一起作废。函数保留（名字与调用点不变）只做形状校验：
// 非法形状 = 装配写坏了引用，报错而不是拿去查库。
func parseSecretRefID(ref string) (string, error) {
	return idgen.Parse(ref)
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
		kindByUpstream := make(map[string]store.Upstream, len(ups))
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
				// SecretRef 必须随行带出：漏掉会让 Target.SecretRef 恒为空，
				// transport 判空后**不带 Authorization 头**出站——上游 401，
				// 故障现场漂到上游（2026-10-31 生产实测，凭据/上游/网络全部
				// 正确却拿 401，解密比对才定位到这一行）。
				SecretRef: func() string {
					if u.SecretID != nil {
						return *u.SecretID
					}
					return "" // 无凭据 selfhosted 的合法形态
				}(),
				CredentialID: r.UpstreamID, // 凭据作用域是上游路径（cooldown 同口径）
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
	_ httpapi.Upstream = upstreamShim{}
	// M6 起生产候选源是 routingCandidates（routing_bridge.go）；这里只钉
	// M5 过渡实现与 httpapi 消费面的编译期兼容（测试仍覆盖它的匹配逻辑）。
	_ httpapi.CandidateSource = (*realModelCandidates)(nil)
)

// —— 贡献流程装配（M8）：upstream.ContributionStore 的 store 实现 ——
//
// 为什么放在装配层：功能包（internal/upstream）只定义窄接口，谁能满足它由这里决定
// （inbox.go 的 UpstreamWriter/AdminNotifier 同一套纪律）。好处是内聚包不 import
// 具体仓储，测试可以塞 fake 而不开 SQLite。
type contributionStore struct {
	repos *store.Repos
}

func (c contributionStore) UpsertUpstream(ctx context.Context, kind, name, baseURL string, ownerAccountID *string, enabled bool) (string, error) {
	return c.repos.Models.UpsertUpstream(ctx, kind, name, baseURL, ownerAccountID, enabled)
}

func (c contributionStore) PutSecret(ctx context.Context, upstreamID string, plaintext []byte) (string, error) {
	return c.repos.Secrets.PutSecret(ctx, upstreamID, plaintext)
}

func (c contributionStore) DeleteSecret(ctx context.Context, secretID string) error {
	return c.repos.Secrets.DeleteSecret(ctx, secretID)
}

func (c contributionStore) CreateCredential(ctx context.Context, ownerAccountID string, provider, externalID, label, secretRef string) (string, error) {
	return c.repos.Models.CreateCredential(ctx, ownerAccountID, provider, externalID, label, secretRef)
}

func (c contributionStore) SetCredentialStatus(ctx context.Context, id string, status store.CredentialStatus) error {
	return c.repos.Models.SetCredentialStatus(ctx, id, status)
}

func (c contributionStore) MarkVerified(ctx context.Context, credentialID string, status store.VerifyStatus, evidence string) error {
	return c.repos.Models.MarkVerified(ctx, credentialID, status, evidence)
}

func (c contributionStore) LookupCredential(ctx context.Context, id string) (*store.Credential, error) {
	return c.repos.Models.LookupCredential(ctx, id)
}

func (c contributionStore) ListCredentials(ctx context.Context, f store.CredentialFilter) ([]store.Credential, error) {
	return c.repos.Models.ListCredentials(ctx, f)
}

func (c contributionStore) ListUpstreams(ctx context.Context) ([]store.Upstream, error) {
	return c.repos.Models.ListUpstreams(ctx)
}

// SeedCatalog 实现 upstream.ContributionStore 的目录种子写入
// （自动获取探到的清单就地落 real_models，理由见该接口方法的注释）。
func (c contributionStore) SeedCatalog(ctx context.Context, upstreamID string, models []string) (int, error) {
	return c.repos.Models.SeedCatalog(ctx, upstreamID, models)
}

func (c contributionStore) UpdateUpstream(ctx context.Context, id string, name, baseURL string, enabled bool) error {
	return c.repos.Models.UpdateUpstream(ctx, id, name, baseURL, enabled)
}

// MarkUpstreamRemoved / MarkCredentialRemoved：移除标记（2026-10-31「移除
// 供应商」/「移除贡献」）的落库通道——留行不留钥。
func (c contributionStore) MarkUpstreamRemoved(ctx context.Context, id string, at int64) error {
	return c.repos.Models.MarkUpstreamRemoved(ctx, id, at)
}

func (c contributionStore) MarkCredentialRemoved(ctx context.Context, id string, at int64) error {
	return c.repos.Models.MarkCredentialRemoved(ctx, id, at)
}

// NewContributionService 装配贡献服务：真 store（AES-GCM 密文）+ 默认 HTTP 探测
// （GET {base}/models，零 token 成本，见 contribution.go 的 HTTPProbe 说明）。
//
// 探测为什么不用 sidecar 的 models.list：那条路要 provider adapter，而现在的
// adapter 只剩 OAuth 型（依赖 DSH 账号池，本侧没有账号池替身）；贡献物的
// base_url 又可以是任意 OpenAI 兼容端点——用 adapter 反而会把「任意端点」
// 限制成「已知的那几家」。2026-10-24 移除 modelscope/opencode 后，这条边界
// 比原来更明显：apiKey 型 adapter 已经一个都不剩。
func (a *App) newContributionService() *upstream.ContributionService {
	return upstream.NewContributionService(contributionStore{repos: a.store.Repos()}, nil, upstream.ContributionOptions{})
}

// Contribution 返回进程级贡献服务（控制台/管理面用；无状态，可直接复用）。
func (a *App) Contribution() *upstream.ContributionService {
	if a.contribution == nil {
		a.contribution = a.newContributionService()
	}
	return a.contribution
}

// auditCredentialWiring 是「凭据链路未接」的显式告警（code-review 一般 6 的兜底）。
//
// 判据：存在 vendor/selfhosted 上游且它**声明了**密钥材料（upstreams.secret_id 非空），
// 但 secretSource 解不开（主密钥不可用 / 密文行缺失 / 引用形态不认识）。
// 这类上游如果被调用，就会以「无 Authorization」打到上游，故障现场漂到 401/502——
// 所以启动期必须说出来，而不是等用户报「上游 401」再去查。
//
// 为什么不逐条解密：解密有成本且会碰主密钥，启动期只做**存在性**判定
// （HasSecret 不解密），真正解密留在请求路径上（明文只在调用期内存活）。
func (a *App) auditCredentialWiring(ctx context.Context) {
	if a.store == nil {
		return
	}
	ups, err := a.store.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		a.log.Warn("凭据链路自检：读上游列表失败", "err", err)
		return
	}
	masterOK := !allZero(a.store.MasterKey())
	hasSecret := func(ctx context.Context, upstreamID string) (bool, error) {
		if !masterOK {
			return false, nil
		}
		return a.store.Repos().Secrets.HasSecret(ctx, upstreamID)
	}
	need, wired, broken := credentialWiringIssues(ctx, ups, hasSecret, masterOK)
	a.logCredentialWiring(need, wired, broken)
}

// logCredentialWiring 是自检的**输出面**（单独拆出来便于测试：日志文案本身是契约，
// 运维靠 grep「凭据链路未接」来发现静默降级的现场）。
func (a *App) logCredentialWiring(need, wired, broken int) {
	if need == 0 {
		return
	}
	if broken > 0 {
		// Warn 级：这不是「少个功能」，而是**正在以错误形态出站**（无鉴权请求）。
		a.log.Warn("凭据链路未接：部分上游声明了密钥材料但解不开，这些上游会以无鉴权请求出站",
			"upstreams_need_credential", need, "wired", wired, "broken", broken,
			"hint", "检查 store.master_key_file 是否可读、upstream_secrets 是否有对应行")
		return
	}
	// 全部接线：Debug 级即可（正常路径不该在启动日志里占一行 warn）。
	a.log.Debug("凭据链路自检通过", "upstreams_need_credential", need)
}

// credentialWiringIssues 是自检的**纯判定**部分（便于测试：不需要真库的悬空外键）。
//
// 返回 (声明了密钥材料的上游数, 解得到的, 解不到的)。
func credentialWiringIssues(ctx context.Context, ups []store.Upstream, hasSecret func(context.Context, string) (bool, error), masterOK bool) (need, wired, broken int) {
	for _, u := range ups {
		if u.Kind != string(upstream.KindVendor) && u.Kind != string(upstream.KindSelfHosted) {
			continue
		}
		if u.SecretID == nil || *u.SecretID == "" {
			continue // 无鉴权的 selfhosted：正当形态
		}
		need++
		if !masterOK {
			broken++
			continue
		}
		has, err := hasSecret(ctx, u.ID)
		if err != nil || !has {
			broken++
			continue
		}
		wired++
	}
	return need, wired, broken
}

// allZero 判断主密钥是否已不可用（Store.Close 会把内存副本抹零，见 store.Close）。
func allZero(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
