package app

import (
	"context"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

func TestProviderFromSidecarURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sidecar://deepseek", "deepseek"},
		{"sidecar://", ""},        // 有前缀无 provider
		{"https://x.test/v1", ""}, // 非 relay
		{"", ""},
	}
	for _, c := range cases {
		if got := providerFromSidecarURL(c.in); got != c.want {
			t.Errorf("providerFromSidecarURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// balanceTestSource 构造一个固定映射的 balanceSource（测试用）。
func balanceTestSource(upToProv map[int64]string) balanceSource {
	return balanceSource{
		relayUpstreams: func(ctx context.Context) (map[int64]string, error) {
			out := make(map[int64]string, len(upToProv))
			for id, p := range upToProv {
				out[id] = p
			}
			return out, nil
		},
		providerOfUpstream: func(ctx context.Context, upstreamID int64) string {
			return upToProv[upstreamID]
		},
	}
}

func TestBalanceLoopBalanceOfUpstream(t *testing.T) {
	probe := upstream.NewQuotaProbe(nil)
	// 手动写入余额快照（模拟探测结果），验证 BalanceOfUpstream 经 provider 反查。
	probe.RecordSnapshot([]upstream.Quota{
		{Provider: "deepseek", Balance: f64ptr(10.5), Source: upstream.QuotaFromSidecar},
		{Provider: "kimi", Balance: f64ptr(5.0), Source: upstream.QuotaFromSidecar},
	})
	loop := NewBalanceLoop(probe, balanceTestSource(map[int64]string{1: "deepseek", 2: "kimi"}), 0)

	br, ok := loop.BalanceOfUpstream(context.Background(), 1)
	if !ok || br.Balance != 10.5 {
		t.Fatalf("BalanceOfUpstream(1) = %+v, %v; want balance=10.5 ok=true", br, ok)
	}
	br, ok = loop.BalanceOfUpstream(context.Background(), 2)
	if !ok || br.Balance != 5.0 {
		t.Fatalf("BalanceOfUpstream(2) = %+v, %v; want 5.0 ok=true", br, ok)
	}
	// 未知上游 -> ok=false。
	if _, ok = loop.BalanceOfUpstream(context.Background(), 999); ok {
		t.Fatal("未知上游应 ok=false")
	}
}

func TestSyncOnceWritesSnapshot(t *testing.T) {
	probe := upstream.NewQuotaProbe(nil)
	loop := NewBalanceLoop(probe, balanceTestSource(map[int64]string{1: "p1"}), 0)
	// 无 client 时 ProbeAll 仍返回「unverified」的 Quota（provider 名保留），
	// 所以 SyncOnce 返回 1——探测是旁路，拿不到余额不 panic、不返回 0。
	if n := loop.SyncOnce(context.Background()); n != 1 {
		t.Fatalf("SyncOnce 应返回探测的 provider 数 1，got %d", n)
	}
	// 快照应包含 p1（unverified 也占位，供 /readyz 显示）。
	_, ok := probe.Last("p1")
	if !ok {
		t.Fatal("快照应含 provider p1")
	}
}
func f64ptr(v float64) *float64 { return &v }
