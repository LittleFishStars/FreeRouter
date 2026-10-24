package upstream

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
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

	// mu 保护快照。快照是「最近一轮探测的配额读数」，供 /readyz 明细与
	// 排序策略的 balance 维度读，不落库（05-storage.md 第 1 节：健康/配额是
	// 短周期信号，落库会带来写放大与陈旧状态）。
	mu     sync.RWMutex
	snap   map[string]Quota
	snapAt time.Time
}

// NewQuotaProbe 建探测器。client 允许为 nil——nil 时全部返回 unverified，
// 装配层在 sidecar 不可用时仍可安全持有探测器（失败不阻塞启动，任务红线）。
func NewQuotaProbe(client CreditsQuerier) *QuotaProbe {
	return &QuotaProbe{client: client, snap: make(map[string]Quota)}
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
	if out.ExpiresAt == "" && credits.Raw != nil {
		// 到期时间通常只在 raw 里（credits.query 的契约没有独立字段）。能拿到就带：
		// 它比余额更早决定「这条凭据还有没有用」（03-upstream.md 任务第 4 点）。
		out.ExpiresAt = expiryFromRaw(credits.Raw)
	}
	return out
}

// ProbeAll 对多个 provider 并行探测，顺序与输入一致。
//
// 为什么并行：providers.list 有多家（2026-10-24 起 15 家），串行探测最坏
// 家数×15s；探测是旁路任务，
// 慢可以被容忍，但一个 provider 卡死拖住整轮探测会让 /readyz 的配额明细
// 永远处于陈旧状态。
func (q *QuotaProbe) ProbeAll(ctx context.Context, providers []string) []Quota {
	if len(providers) == 0 {
		return nil
	}
	results := make([]Quota, len(providers))

	// 有界并发：全量并行会在 sidecar 侧排队（串行一问一答），17 个 goroutine
	// 等一个 mutex 没有收益；给 4 个并发度让超时能各自走完。
	const concurrency = 4
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range providers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = q.Probe(ctx, providers[i])
		}(i)
	}
	wg.Wait()
	q.RecordSnapshot(results)
	return results
}

// QuotaSnapshot 是一轮探测的不可变结果（按 provider 名升序）。
//
// 为什么需要快照：余额与到期时间是排序策略的输入（01-catalog.md §8 的 balance），
// 但探测是低频旁路任务；让读方直接调 Probe 会把「排序一次打 17 个公网请求」变成热路径。
type QuotaSnapshot struct {
	// GeneratedAt 是快照写入时刻（零值 = 从未探测过）。
	GeneratedAt time.Time
	Quotas      []Quota
}

// Lookup 按 provider 查一条配额。
func (s QuotaSnapshot) Lookup(provider string) (Quota, bool) {
	for _, q := range s.Quotas {
		if q.Provider == provider {
			return q, true
		}
	}
	return Quota{}, false
}

// Len 返回快照里的 provider 数。
func (s QuotaSnapshot) Len() int { return len(s.Quotas) }

// RecordSnapshot 把一轮探测结果并入快照。导出的原因是：装配层可能用自己的
// 调度器（而不是 ProbeAll）驱动探测，它仍应能以同一形状发布读数供 /readyz 使用。
func (q *QuotaProbe) RecordSnapshot(results []Quota) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.snap == nil {
		q.snap = make(map[string]Quota)
	}
	for _, r := range results {
		if r.Provider != "" {
			q.snap[r.Provider] = r
		}
	}
	q.snapAt = time.Now()
}

// Snapshot 返回当前快照的副本（按 provider 升序，调用方改不动内部状态）。
func (q *QuotaProbe) Snapshot() QuotaSnapshot {
	if q == nil {
		return QuotaSnapshot{}
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	providers := make([]string, 0, len(q.snap))
	for p := range q.snap {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	out := QuotaSnapshot{GeneratedAt: q.snapAt, Quotas: make([]Quota, 0, len(providers))}
	for _, p := range providers {
		out.Quotas = append(out.Quotas, q.snap[p])
	}
	return out
}

// Last 返回某 provider 的最近一次探测结果。
func (q *QuotaProbe) Last(provider string) (Quota, bool) {
	if q == nil {
		return Quota{}, false
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	v, ok := q.snap[provider]
	return v, ok
}

// ExpiresAtTime 解析 ExpiresAt（空/不可解析时 ok=false）。
func (q Quota) ExpiresAtTime() (time.Time, bool) {
	if q.ExpiresAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, q.ExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Expired 报告配额/订阅是否已到期。不可知时不判过期（拿不到不是过期）。
func (q Quota) Expired(now time.Time) bool {
	t, ok := q.ExpiresAtTime()
	return ok && !now.Before(t)
}

// expiryKeys 是 raw 里可能承载到期时间的键（统一转小写后比较）。
//
// 为什么要做一张候选表而不是只认 expiresAt：credits.query 的 raw 是「上游原始响应的
// 子集」，字段名由各家端点决定（expiresAt / expiry / endTime / validUntil…）。
// 只认一个名字的后果不是“少一个字段”，而是“到期凭据继续被当成可用”。
var expiryKeys = []string{
	"expiresat", "expires_at", "expireat", "expire_at",
	"expiry", "expiryat", "expiry_at", "expires",
	"endtime", "end_time", "validuntil", "valid_until",
	"quotaexpiresat", "quota_expires_at", "subscriptionexpiresat",
	"subscription_expires_at",
}

// expiryFromRaw 从 upstream 原始响应里取到期时间，归一成 RFC3339（UTC）。
// 拿不到就返回空串——与 Quota.ExpiresAt 的三态语义一致：空 = 不可知，不是“永不过期”。
func expiryFromRaw(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	if s, ok := findExpiry(v, 0); ok {
		return s
	}
	return ""
}

// findExpiry 先按候选键名找，再递归进嵌套对象/数组。
// 深度上限 3 是防环预算：raw 是外部输入，不设上限时一个自引用的巨型对象可以吃光栈。
func findExpiry(v any, depth int) (string, bool) {
	if depth > 3 {
		return "", false
	}
	switch x := v.(type) {
	case map[string]any:
		lower := make(map[string]any, len(x))
		for k, val := range x {
			lower[strings.ToLower(strings.TrimSpace(k))] = val
		}
		for _, k := range expiryKeys {
			if val, ok := lower[k]; ok {
				if s, ok := parseExpiryValue(val); ok {
					return s, true
				}
			}
		}
		// 嵌套查找按 key 排序，保证同一份 raw 每次得到同一个答案（map 遍历无序）。
		keys := make([]string, 0, len(lower))
		for k := range lower {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := findExpiry(lower[k], depth+1); ok {
				return s, true
			}
		}
	case []any:
		for _, val := range x {
			if s, ok := findExpiry(val, depth+1); ok {
				return s, true
			}
		}
	}
	return "", false
}

// parseExpiryValue 接受 RFC3339、常见日期格式与 unix 秒/毫秒。
func parseExpiryValue(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return "", false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return formatExpiry(t)
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return formatExpiry(unixExpiry(n))
		}
	case float64:
		return formatExpiry(unixExpiry(int64(x)))
	}
	return "", false
}

func unixExpiry(n int64) time.Time {
	if n > 1e12 { // 毫秒时间戳
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}

// formatExpiry 拒绝明显不可能是到期时间的值（0、1970、2100 以后）：
// raw 里的数字字段可能是配额数量或序号，把 0 当成到期时间会让整个 provider 被误剔。
func formatExpiry(t time.Time) (string, bool) {
	if t.IsZero() || t.Year() < 2000 || t.Year() > 2100 {
		return "", false
	}
	return t.UTC().Format(time.RFC3339), true
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
