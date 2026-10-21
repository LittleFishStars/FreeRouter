package upstream

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// 本文件是 task-10 第 3 点：relay 自举（03-upstream.md §3.2）。
//
// 自举步骤（§3.2 表）：
//  1. sidecar health 就绪（dshprovider.Start 内含，失败 → relay 标不可用，不阻塞主进程）；
//  2. providers.list → 写 upstreams 一行（kind='relay', owner_account_id=NULL）；
//  3. models.list → 目录同步（复用 task-7 ModelRefresher.RefreshOnce）；
//  4. 周期刷新由 refresher 自管（Start/runLoop）。
//
// 红线：写库失败只记错误下次启动重试（§3.2 表第 2 行），绝不阻塞启动。

// ProviderSource 是自举对 provider 目录的最小依赖（*dshprovider.Client 天然满足）。
// 为什么不直接收 client：单测要 fake（返回错误/返回可控目录），
// 自举逻辑不该知道拨号细节。
type ProviderSource interface {
	ProvidersList(ctx context.Context) ([]dshprovider.ProviderInfo, error)
}

// UpstreamWriter 是 upstreams 落行的窄接口。
//
// 为什么按 (kind, name) 幂等 upsert 而不是 INSERT：自举每个启动周期都跑，
// 第二次启动必须与第一次的行**合并**而不是报唯一冲突（§3.2「写 upstreams 一行」
// 的语义是「确保存在」）；store.ModelRepo.UpsertUpstream 已按 kind+name 唯一。
// 实现归装配层（Lead 把 *store.Store 的 ModelRepo 接进来）。
type UpstreamWriter interface {
	// UpsertUpstream 同步一条上游配置（kind/name/baseURL/owner/enabled）。
	UpsertUpstream(ctx context.Context, kind, name, baseURL string, ownerAccountID *int64, enabled bool) (int64, error)
}

// AdminNotifier 是 admin_inbox 通知的窄接口（§3.3：目录/健康事件的唯一发现点）。
//
// ALIGN-SPEC G：relay 连续失败写 upstream_failing。自举阶段能发的通知有限
// （目录拉不下来 = warn），实现归装配层；接口为 nil 时跳过通知（不阻塞主流程，
// §3.3「通知是旁路」）。
type AdminNotifier interface {
	// NotifyAdminInbox 写一条待处理通知。kind/severity/detail 的口径见 03-upstream.md §3.3 表。
	NotifyAdminInbox(ctx context.Context, kind, relatedID, title, detail string, severity string) error
}

// RelayBaseURLPrefix 是 relay 行 base_url 列的定案值前缀（task-12 语义定案：
// relay 行存 provider 标识如 "sidecar://buddy"——不是占位而是定案值，
// 因为列 NOT NULL 且 relay 没有真实 URL；前缀让人一眼看出「这不是地址」）。
const RelayBaseURLPrefix = "sidecar://"

// RelayBaseURLOf 由 provider 名构造 relay 行的 base_url 定案值。
func RelayBaseURLOf(provider string) string {
	return RelayBaseURLPrefix + provider
}

// IsRelayBaseURL 判定一行 upstreams 的 base_url 是否 relay 定案语义。
// 读侧（candidate 展开、目录刷新）用它区分 relay 与真实端点。
func IsRelayBaseURL(baseURL string) bool {
	return len(baseURL) > len(RelayBaseURLPrefix) && baseURL[:len(RelayBaseURLPrefix)] == RelayBaseURLPrefix
}

// BootstrapResult 汇总一次自举的结果（日志与测试断言用）。
type BootstrapResult struct {
	// Providers 是本轮落库的 provider 行数（= providers.list 的条数）。
	Providers int
	// Refresh 复用 ModelRefresher 的结果（目录同步）。
	// 指针：refresher 可为 nil（装配层未接线目录同步时跳过步骤 3）。
	Refresh *RefreshResult
	// SkippedReason 非空表示自举整体跳过（sidecar 不可用等）及其原因。
	SkippedReason string
}

// RelayBootstrapper 执行 relay 自举。
type RelayBootstrapper struct {
	source    ProviderSource
	writer    UpstreamWriter
	refresher Refresher
	notifier  AdminNotifier
	log       *slog.Logger
}

// Refresher 是目录同步的最小接口（*ModelRefresher 天然满足；窄接口便于单测 fake）。
type Refresher interface {
	RefreshOnce(ctx context.Context) (RefreshResult, error)
}

// NewRelayBootstrapper 建自举器。writer 为 nil 时自举只拉目录不落行
// （装配未接线 store 的降级形态——不报错，写库失败本就只记日志）。
func NewRelayBootstrapper(source ProviderSource, writer UpstreamWriter, refresher Refresher, notifier AdminNotifier, log *slog.Logger) *RelayBootstrapper {
	return &RelayBootstrapper{source: source, writer: writer, refresher: refresher, notifier: notifier, log: log}
}

// Run 执行一轮自举：providers.list → 落 upstreams → 目录刷新。
//
// 任何失败都返回**带 SkippedReason 的结果**而不是 error——自举是启动旁路
// （红线：失败不阻塞启动），调用方（装配层）只需记日志并安排下轮重试。
// 幂等：重复 Run 与重复启动都安全（upsert 语义 + RefreshOnce 自身幂等）。
func (b *RelayBootstrapper) Run(ctx context.Context) BootstrapResult {
	if b == nil || b.source == nil {
		return BootstrapResult{SkippedReason: "自举器未装配 provider 源"}
	}
	lg := b.log
	if lg == nil {
		lg = slog.Default()
	}
	// 步骤 1（§3.2 表）：providers.list——它同时是 sidecar 就绪的实测
	//（Start 的 health 握手之外，这里才是「目录真的拉得下来」的证据）。
	providers, err := b.source.ProvidersList(ctx)
	if err != nil {
		res := BootstrapResult{SkippedReason: "providers.list 失败: " + err.Error()}
		lg.Warn("relay 自举失败，下轮重试（不阻塞启动）", "error", err)
		b.notify(ctx, "upstream_failing", "", "relay 自举失败", res.SkippedReason, "warn")
		return res
	}
	// 步骤 2：逐 provider 落 upstreams 行（kind='relay', owner=NULL, enabled=true）。
	//
	// 为什么每行独立 upsert 而不批量：一个 provider 写失败只影响该 provider
	//（下一行继续），与 RefreshOnce 的「单上游失败不中断其余」同哲学。
	// hasAccount=false 的 provider 也落行：目录与登录态是两件事——
	// 行存在才有目录刷新的目标，hasAccount 影响的是候选可用性不是行存在性。
	for _, p := range providers {
		if b.writer == nil {
			break // 未接 writer：跳过落行（降级形态，不报错）
		}
		if _, err := b.writer.UpsertUpstream(ctx, "relay", p.Provider, RelayBaseURLOf(p.Provider), nil, true); err != nil {
			lg.Error("relay 自举止写库失败，该 provider 下轮重试", "provider", p.Provider, "error", err)
			b.notify(ctx, "upstream_failing", p.Provider, "relay 行落库失败", err.Error(), "error")
			continue
		}
	}
	res := BootstrapResult{Providers: len(providers)}
	// 步骤 3：目录同步（§3.2 表第 3 行：失败退避重试 30s→15m 由 refresher 自管）。
	if b.refresher != nil {
		rr, err := b.refresher.RefreshOnce(ctx)
		if err != nil {
			// 目录拉取失败≠模型消失：只记日志，绝不 MarkMissing（RefreshOnce 内部同红线）。
			lg.Warn("relay 自举目录同步失败，周期刷新会重试", "error", err)
		}
		res.Refresh = &rr
	}
	lg.Info("relay 自举完成", "providers", res.Providers)
	return res
}

// notify 发 admin_inbox 通知（§3.3：旁路，失败绝不影响主结果）。
func (b *RelayBootstrapper) notify(ctx context.Context, kind, relatedID, title, detail, severity string) {
	if b.notifier == nil {
		return
	}
	if err := b.notifier.NotifyAdminInbox(ctx, kind, relatedID, title, detail, severity); err != nil {
		// 写入失败（库锁/磁盘满）必须落 slog 且不得影响目录刷新本身（§3.3 末段）。
		slog.Default().Warn("admin_inbox 通知写入失败（旁路）", "kind", kind, "error", err)
	}
}

// —— 装配提示（不改 internal/httpapi 任何文件；这里提供装配层要用的全部件） ——

// CandidateInfo 是装配层展开候选所需的最小信息（httpapi.Candidate 的来源数据）。
//
// 为什么放这里：Candidate→Target 的映射是 upstream 包的知识（kind/base_url 语义
// 都在本包），httpapi 只管消费；装配层拿 CandidateInfo 构造 httpapi.Candidate。
type CandidateInfo struct {
	UpstreamID   int64
	Kind         Kind
	BaseURL      string // relay: "sidecar://provider"；vendor/selfhosted: https 端点
	RealModel    string // relay: "provider/model"；vendor: 原始模型 id
	SecretRef    string
	CredentialID int64
}

// TargetOf 把候选信息映射成一次尝试的 Target。
//
// relay 的 BaseURL（sidecar:// 前缀）在这里被**剥掉**：Target.BaseURL 对 relay
// 恒为空（§9.3 红线——ValidateTarget 会拒绝非空）；provider 名进不了 Target，
// 它由 RealModel 前缀携带（RelayTransport 拆分）。
func TargetOf(c CandidateInfo) (Target, error) {
	t := Target{
		UpstreamID:   c.UpstreamID,
		Kind:         c.Kind,
		CredentialID: c.CredentialID,
		RealModel:    c.RealModel,
		SecretRef:    c.SecretRef,
	}
	switch {
	case c.Kind == KindRelay:
		if IsRelayBaseURL(c.BaseURL) {
			provider := c.BaseURL[len(RelayBaseURLPrefix):]
			if provider != "" && !hasProviderPrefix(c.RealModel, provider) {
				// 行的 base_url 与模型 id 前缀不一致 = 数据错位（目录刷新时
				// 模型挂错了行）。拒绝这一候选而不是静默转发到错的 provider。
				return t, fmt.Errorf("候选 %q 的 base_url(%s) 与 RealModel(%s) 前缀不一致", c.RealModel, c.BaseURL, provider)
			}
		}
		t.BaseURL = "" // 红线：relay 不持有地址
		return t, nil
	default:
		t.BaseURL = c.BaseURL
		return t, nil
	}
}

// hasProviderPrefix 判定 RealModel 是否以 provider 为前缀（"buddy/deepseek" 有前缀 "buddy"）。
func hasProviderPrefix(realModel, provider string) bool {
	return len(realModel) > len(provider)+1 &&
		realModel[:len(provider)] == provider &&
		realModel[len(provider)] == '/'
}
