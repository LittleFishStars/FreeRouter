package upstream

import (
	"context"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/logging"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// 本文件是 task-6 第 4 点「配额探测」的落地：经 sidecar credits.query() 拿余额。
//
// 为什么主进程不直连官方端点：凭据只在 DSH/sidecar 侧（红线①——我们只读最新值，
// 不落库不续期）。配额探测与凭据读取走同一条 IPC 路径，才能保证「能读凭据就能读
// 配额」，不会出现一边降级另一边还在打公网的劈叉。
//
// 拿不到不报错只记 unverified（任务要求）：配额是**增强信息**不是必需信息——
// relay 上游的可用性由账号池自己管，我们的候选排序不该因为「问不到余额」把
// 一整个 provider 拒之门外。

// QuotaSource 是配额数字的来源。
type QuotaSource string

const (
	// QuotaFromSidecar：来自 sidecar credits.query 的真实读数。
	QuotaFromSidecar QuotaSource = "sidecar"
	// QuotaUnverified：没拿到——缺 hub / provider 不支持 / 查询失败。
	// 为什么单独一个值而不是留空：空串无法区分「没探测过」与「探测了但拿不到」，
	// 而 unverified 恰恰是要进日志与 /readyz 明细的状态。
	QuotaUnverified QuotaSource = "unverified"
)

// Quota 是一次配额探测的结果（03-upstream.md 任务第 4 点：能拿到多少拿多少）。
type Quota struct {
	Provider string
	Source   QuotaSource
	// Balance / Used / Total：指针全 nil = 上游不给这个字段。
	// 三态语义与 ir.Usage 一致：nil 与 0 不同（0 是明确的「没余额」）。
	Balance *float64
	Used    *float64
	Total   *float64
	// Unit 是计价单位（credits / requests / tokens…），空 = 上游没说。
	Unit string
	// ExpiresAt：账户或订阅到期时间（RFC3339），空 = 不可知。能拿到就带——
	// 它比余额更早决定「这条凭据还有没有用」。
	ExpiresAt string
	// CheckedAt：本次探测的时刻。
	CheckedAt time.Time
	// Detail：unverified 时的原因（错误类或文案），成功时空。
	Detail string
}

// CreditsQuerier 是配额探测对 sidecar 客户端的窄接口。
//
// 为什么自己定义而不直接用 *dshprovider.Client：本包与 dshprovider 之间保持
// 接口依赖（依赖倒置），call/探测循环在测试里可以用 fake 替换，不需要拉起
// 真实 sidecar 进程。真实装配由 M2 装配层完成（此时 client 满足此接口）。
type CreditsQuerier interface {
	// CreditsQuery 查询某 provider 的余额（dshprovider.Client 的同名方法签名）。
	CreditsQuery(ctx context.Context, provider, credentialRef string) (*dshprovider.Credits, error)
}

// QuotaProbe 用 sidecar 客户端探测 relay 类 provider 的配额。
type QuotaProbe struct {
	client CreditsQuerier
}

// NewQuotaProbe 建探测器。client 允许为 nil——nil 时全部返回 unverified，
// 装配层在 sidecar 不可用时仍可安全持有探测器（失败不阻塞启动，任务红线）。
func NewQuotaProbe(client CreditsQuerier) *QuotaProbe {
	return &QuotaProbe{client: client}
}

// Probe 对一个 provider 做一次配额探测。
//
// credentialRef 为空时查该 provider 的「默认/聚合」读数（sidecar 语义）。
// 任何失败（client nil、sidecar 报错、超时）都返回 Source==QuotaUnverified 的
// 结果而不是 error——调用方（探测循环）对失败的处置是「跳过本次、下轮再来」，
// 抛 error 只会把一个旁路任务变成需要 recover 的异常路径。
func (q *QuotaProbe) Probe(ctx context.Context, provider string) Quota {
	out := Quota{
		Provider:  provider,
		Source:    QuotaUnverified,
		CheckedAt: time.Now(),
	}
	if q == nil || q.client == nil || provider == "" {
		out.Detail = "sidecar 客户端不可用或 provider 为空"
		return out
	}
	credits, err := q.client.CreditsQuery(ctx, provider, "")
	if err != nil {
		out.Detail = err.Error()
		return out
	}
	if credits == nil {
		out.Detail = "sidecar 返回空结果"
		return out
	}
	out.Source = QuotaFromSidecar
	out.Balance = credits.Balance
	out.Used = credits.QuotaUsed
	out.Total = credits.QuotaTotal
	out.Unit = credits.Unit
	if out.Unit == "" && credits.Raw != nil {
		// Raw 仅供排障，但 unit 缺失时从 raw 里捞一次值得：unit 决定数字的可读性。
		out.Unit = guessUnitFromRaw(credits.Raw)
	}
	return out
}

// ProbeAll 对多个 provider 并行探测，顺序与输入一致。
//
// 为什么并行：providers.list 有 17 家，串行探测最坏 17×15s；探测是旁路任务，
// 慢可以被容忍，但一个 provider 卡死拖住整轮探测会让 /readyz 的配额明细
// 永远处于陈旧状态。
func (q *QuotaProbe) ProbeAll(ctx context.Context, providers []string) []Quota {
	if len(providers) == 0 {
		return nil
	}
	results := make([]Quota, len(providers))
	done := make(chan struct{})
	go func() {
		defer close(done)
		type slot struct{ idx int }
		sem := make(chan slot, len(providers))
		for i := range providers {
			sem <- slot{idx: i}
		}
		// 有界并发：全量并行会在 sidecar 侧排队（串行一问一答），17 个 goroutine
		// 等一个 mutex 没有收益；给 4 个并发度让超时能各自走完。
		const concurrency = 4
		var wg chan struct{}
		_ = wg
		work := make(chan int)
		go func() {
			defer close(work)
			for i := range providers {
				work <- i
			}
		}()
		var fin = make(chan int, len(providers))
		for w := 0; w < concurrency; w++ {
			go func() {
				for i := range work {
					results[i] = q.Probe(ctx, providers[i])
					fin <- i
				}
			}()
		}
		for range providers {
			<-fin
		}
	}()
	<-done
	return results
}

// guessUnitFromRaw 从原始响应里猜 unit 字段（仅排障辅助，猜不中就保持空）。
// 为什么不做完整 JSON 解析：raw 是「上游原始响应的子集」，形态不受我们控制；
// 一个前缀匹配足够给运维一个可读的提示。
func guessUnitFromRaw(raw []byte) string {
	for _, cand := range []string{"credit", "token", "request", "usd", "cny"} {
		if containsFold(string(raw), cand) {
			return cand
		}
	}
	return ""
}

func containsFold(s, sub string) bool {
	if len(s) < len(sub) || sub == "" {
		return false
	}
	ls, lsub := lower(s), lower(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// LogQuotaResult 把一次探测结果记进请求级 logger（§6.4 的配额侧对应物）。
//
// 为什么 unverified 也记 Info 而不是 Warn：unverified 是**预期内**的常态
// （hub 不可装载、provider 不支持 credits.query 都是已知世界），用 Warn 会把
// 健康的部署刷成告警噪声。真正的异常（比如 sidecar 突然从可用变不可用）由
// dshprovider 包的 E_DEPENDENCY_MISSING 告警路径负责。
func LogQuotaResult(ctx context.Context, quota Quota) {
	attrs := []any{
		"provider", quota.Provider,
		"source", string(quota.Source),
		"checked_at", quota.CheckedAt.Format(time.RFC3339),
	}
	switch quota.Source {
	case QuotaFromSidecar:
		if quota.Balance != nil {
			attrs = append(attrs, "balance", *quota.Balance)
		}
		if quota.Total != nil {
			attrs = append(attrs, "total", *quota.Total)
		}
		if quota.ExpiresAt != "" {
			attrs = append(attrs, "expires_at", quota.ExpiresAt)
		}
		if quota.Unit != "" {
			attrs = append(attrs, "unit", quota.Unit)
		}
	default:
		attrs = append(attrs, "detail", quota.Detail)
	}
	logging.FromContext(ctx).Info("配额探测完成", attrs...)
}
