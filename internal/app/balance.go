package app

// balance.go —— 装配层：把「按 provider 探测余额」接成可排序的数据源。
//
// ## 链条
//
//	internal/upstream/quota.go 已有 QuotaProbe（ProbeAll 探测 + RecordSnapshot
//	快照 + Last(provider) 读取）——但**没有任何代码实例化它**，余额探测一直
//	悬空。本文件补上：
//
//	  1. 后台 Run：周期收集 relay 上游的 provider 集合，ProbeAll → RecordSnapshot；
//	  2. balanceOf：按 provider 查余额，routingCandidates 填 Path.Balance。
//
// ## provider 的权威来源
//
// relay 上游的 base_url 是 `sidecar://<provider>`（task-12 定案值，admin 桥
// 保证）。所以「上游行 → provider」= 解析 base_url 前缀。为什么不用 Channel/
// UpstreamModelID 反推：Channel 是「渠道名」（可手工改名），模型 id 是
// 「provider/模型」拼接——都可能跟 provider 对不上；base_url 的定案值有
// admin 桥强校验。
//
// 探测失败不 panic、不阻断排序：余额是增强信息，拿不到按无样本垫底
// （与 quota.go 的成训一致）。

import (
	"context"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// providerFromSidecarURL 从 `sidecar://<provider>` 解析 provider 名。空返回空串。
func providerFromSidecarURL(baseURL string) string {
	const p = "sidecar://"
	if !strings.HasPrefix(baseURL, p) {
		return ""
	}
	return strings.TrimPrefix(baseURL, p)
}

// balanceSource 探测循环读「上游行 → provider」的窄数据接口（装配层注入闭包）。
//
// relayUpstreams 返回所有 relay 上游的 (upstreamID, provider) 对；provider
// 由 base_url 的 sidecar:// 前缀解析。非 relay（vendor/selfhosted）不参与
// balance 探测——它们用自己的凭据、额度语义各异，余额没有可比较口径。
type balanceSource struct {
	// relayUpstreams 列出所有 relay 上游（id → provider）。每次探测重新读，
	// 保证上游增删/改 kind 后快照仍跟得上。
	relayUpstreams func(ctx context.Context) (map[string]string, error)
	// providerOfUpstream 按上游 id 查 provider（非 relay 或未知返回空）。
	// balanceOf 用：routing 的 candidate 只带 upstreamID，要靠它反查 provider。
	providerOfUpstream func(ctx context.Context, upstreamID string) string
}

// BalanceLoop 是后台余额探测循环 + 面向 routing 的读取门面。
type BalanceLoop struct {
	probe  *upstream.QuotaProbe
	source balanceSource
	// interval 探测周期；<=0 用默认 60s。
	interval time.Duration
}

// NewBalanceLoop 建循环。
func NewBalanceLoop(probe *upstream.QuotaProbe, source balanceSource, interval time.Duration) *BalanceLoop {
	if probe == nil {
		probe = upstream.NewQuotaProbe(nil)
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &BalanceLoop{probe: probe, source: source, interval: interval}
}

// SyncOnce 做一轮探测：收集 relay providers → ProbeAll → RecordSnapshot。
// 返回本轮新增/刷新的 provider 数（供日志观测）。
func (b *BalanceLoop) SyncOnce(ctx context.Context) int {
	if b == nil || b.probe == nil || b.source.relayUpstreams == nil {
		return 0
	}
	upToProv, err := b.source.relayUpstreams(ctx)
	if err != nil || len(upToProv) == 0 {
		return 0
	}
	providers := make([]string, 0, len(upToProv))
	seen := map[string]bool{}
	for _, p := range upToProv {
		if p != "" && !seen[p] {
			seen[p] = true
			providers = append(providers, p)
		}
	}
	if len(providers) == 0 {
		return 0
	}
	results := b.probe.ProbeAll(ctx, providers)
	b.probe.RecordSnapshot(results)
	return len(results)
}

// Run 阻塞循环，直到 ctx 取消。用于 App 后台任务（bgCancel）。
func (b *BalanceLoop) Run(ctx context.Context) {
	if b == nil {
		return
	}
	t := time.NewTicker(b.interval)
	defer t.Stop()
	b.SyncOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.SyncOnce(ctx)
		}
	}
}

// Last 返回某 provider 的最近一次余额读数，供路由读。
// provider 未知或 probe==nil 返回 ok=false（无样本垫底）。
func (b *BalanceLoop) Last(provider string) (upstream.Quota, bool) {
	if b == nil || b.probe == nil || provider == "" {
		return upstream.Quota{}, false
	}
	return b.probe.Last(provider)
}

// ProviderBalance 是路由读余额与到期的便捷封装。
// 没有余额值（上游不报）视为未知（不是 0 余额——0 会被 routing 误读成「已耗尽」）。
func (b *BalanceLoop) ProviderBalance(provider string) (BalanceReading, bool) {
	q, ok := b.Last(provider)
	if !ok || q.Balance == nil {
		return BalanceReading{}, false
	}
	out := BalanceReading{Balance: *q.Balance, Known: true}
	if t, ok := q.ExpiresAtTime(); ok {
		out.ExpiresAt = t
	}
	return out, true
}

// BalanceOfUpstream 按上游 id 查余额读数（routing 用）。
// 上游不是 relay、未接探测，或余额未知时 ok=false（路由按无样本垫底）。
func (b *BalanceLoop) BalanceOfUpstream(ctx context.Context, upstreamID string) (BalanceReading, bool) {
	if b == nil || b.probe == nil || b.source.providerOfUpstream == nil {
		return BalanceReading{}, false
	}
	provider := b.source.providerOfUpstream(ctx, upstreamID)
	if provider == "" {
		return BalanceReading{}, false
	}
	return b.ProviderBalance(provider)
}

// BalanceReading 是给路由的一条余额读数（只投出会进排序键的字段）。
type BalanceReading struct {
	Balance   float64
	Known     bool
	ExpiresAt time.Time
}
