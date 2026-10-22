package upstream

import (
	"context"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// 本文件守的是 task-3 第 2 点：配额探测「余额/到期，能拿到多少拿多少」以及
// 「quota 快照」的对外形状；外加连接池 idle_timeout ≤ 120s 红线的显式断言。

// TestContractQuotaExpiryFromRaw 覆盖到期时间从 raw 的多种形态中提取。
//
// 为什么到期时间必须尽量拿到：它比余额更早决定「这条凭据还有没有用」——
// 余额充足但订阅已过期的凭据继续参与候选，只会把失败打到用户脸上。
func TestContractQuotaExpiryFromRaw(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"RFC3339 camelCase", `{"expiresAt":"2027-01-02T03:04:05Z"}`, "2027-01-02T03:04:05Z"},
		{"snake_case 嵌套", `{"account":{"expires_at":"2027-01-02T03:04:05Z"}}`, "2027-01-02T03:04:05Z"},
		{"unix 秒", `{"expiry":1800000000}`, "2027-01-15T08:00:00Z"},
		{"unix 毫秒", `{"endTime":1800000000000}`, "2027-01-15T08:00:00Z"},
		{"日期字符串", `{"validUntil":"2027-01-02"}`, "2027-01-02T00:00:00Z"},
		{"不可知", `{"balance":12.5}`, ""},
		{"非法值", `{"expiresAt":"不是时间"}`, ""},
		{"零值不当作到期", `{"expiresAt":0}`, ""},
		{"过远期拒绝", `{"expiresAt":"2101-01-01T00:00:00Z"}`, ""},
	}
	for _, tc := range cases {
		if got := expiryFromRaw([]byte(tc.raw)); got != tc.want {
			t.Errorf("%s：expiryFromRaw=%q，want %q", tc.name, got, tc.want)
		}
	}

	// ExpiresAtTime / Expired 的三态语义：空 = 不可知（不判过期）。
	q := Quota{ExpiresAt: "2027-01-02T03:04:05Z"}
	at, ok := q.ExpiresAtTime()
	if !ok || !at.Equal(time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("ExpiresAtTime 解析错误：%v %v", at, ok)
	}
	if !q.Expired(at.Add(time.Second)) {
		t.Fatal("到期后 Expired 应为 true")
	}
	if q.Expired(at.Add(-time.Second)) {
		t.Fatal("到期前 Expired 应为 false")
	}
	if (Quota{}).Expired(time.Now()) {
		t.Fatal("不可知的到期时间不得判为已过期")
	}
}

// TestContractQuotaProbeFillsExpiryAndSnapshot 验证 Probe 把 raw 里的到期时间带出来，
// 且 ProbeAll 发布按 provider 升序的快照。
func TestContractQuotaProbeFillsExpiryAndSnapshot(t *testing.T) {
	fake := &fakeCreditsQuerier{defaultF: func(provider string) (*dshprovider.Credits, error) {
		balance := 1.0
		return &dshprovider.Credits{
			Balance: &balance,
			Unit:    "credits",
			Raw:     []byte(`{"account":{"expiresAt":"2027-01-02T03:04:05Z"}}`),
		}, nil
	}}
	probe := NewQuotaProbe(fake)

	results := probe.ProbeAll(context.Background(), []string{"zcode", "buddy", "cline"})
	if len(results) != 3 {
		t.Fatalf("结果数 %d", len(results))
	}
	for _, r := range results {
		if r.ExpiresAt != "2027-01-02T03:04:05Z" {
			t.Fatalf("%s 的到期时间未从 raw 提取：%+v", r.Provider, r)
		}
	}

	snap := probe.Snapshot()
	if snap.Len() != 3 {
		t.Fatalf("快照条数 %d，want 3", snap.Len())
	}
	wantOrder := []string{"buddy", "cline", "zcode"}
	for i, q := range snap.Quotas {
		if q.Provider != wantOrder[i] {
			t.Fatalf("快照顺序漂移：%v", snap.Quotas)
		}
	}
	if snap.GeneratedAt.IsZero() {
		t.Fatal("快照必须带 GeneratedAt")
	}
	if q, ok := snap.Lookup("buddy"); !ok || q.Source != QuotaFromSidecar {
		t.Fatalf("Lookup 未命中或来源错误：%+v", q)
	}
	if q, ok := probe.Last("cline"); !ok || q.Balance == nil || *q.Balance != 1.0 {
		t.Fatalf("Last 未命中：%+v", q)
	}

	// unverified 也要进快照（它是要进 /readyz 明细的状态，不是“没探测过”）。
	failing := NewQuotaProbe(&fakeCreditsQuerier{defaultF: func(string) (*dshprovider.Credits, error) {
		return nil, &dshprovider.Error{Code: "E_DEPENDENCY_MISSING", Message: "hub 不可装载"}
	}})
	bad := failing.ProbeAll(context.Background(), []string{"buddy"})
	if len(bad) != 1 || bad[0].Source != QuotaUnverified {
		t.Fatalf("失败探测应返回 unverified：%+v", bad)
	}
	if q, ok := failing.Last("buddy"); !ok || q.Source != QuotaUnverified || q.Detail == "" {
		t.Fatalf("unverified 必须进快照且带原因：%+v", q)
	}
}

// TestContractQuotaSnapshotNilSafe 验证 nil 探测器/空输入不 panic。
func TestContractQuotaSnapshotNilSafe(t *testing.T) {
	var nilProbe *QuotaProbe
	if snap := nilProbe.Snapshot(); snap.Len() != 0 || !snap.GeneratedAt.IsZero() {
		t.Fatalf("nil 探测器应返回空快照：%+v", snap)
	}
	nilProbe.RecordSnapshot([]Quota{{Provider: "x"}}) // 不得 panic
	if _, ok := nilProbe.Last("x"); ok {
		t.Fatal("nil 探测器的 Last 应返回 false")
	}
	probe := NewQuotaProbe(nil)
	if got := probe.ProbeAll(context.Background(), nil); got != nil {
		t.Fatalf("空输入应返回 nil，got %+v", got)
	}
}

// TestContractPoolIdleTimeoutNeverAbove120s 是任务红线「idle_timeout ≤ 120s」的显式断言。
//
// 既有 TestContractPoolIdleTimeoutCappedByServer 断言的是“跟随 server 的 min 语义”；
// 这里补的是**绝对上界**：无论 server 配多大，连接池都不得把空闲连接留过 120s——
// 超过对端淘汰区间（60–120s）就必然出现复用半开连接导致的偶发 http=000。
func TestContractPoolIdleTimeoutNeverAbove120s(t *testing.T) {
	for _, server := range []time.Duration{
		0, -1 * time.Second, 30 * time.Second, 60 * time.Second,
		90 * time.Second, 120 * time.Second, 121 * time.Second,
		5 * time.Minute, time.Hour,
	} {
		cfg := PoolConfig{ServerIdleTimeout: server}
		got := cfg.effectiveIdleTimeout()
		if got <= 0 {
			t.Errorf("server=%v：IdleConnTimeout 必须为正，got %v", server, got)
		}
		if got > 120*time.Second {
			t.Errorf("server=%v：IdleConnTimeout=%v 超过 120s 红线", server, got)
		}
	}
	// 走真实 Transport 的路径再确认一次（配置计算与落到 http.Transport 不脱节）。
	p := NewPool(PoolConfig{ServerIdleTimeout: time.Hour})
	tr, err := p.Transport(KindVendor, "https://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.IdleConnTimeout > 120*time.Second {
		t.Fatalf("Transport.IdleConnTimeout=%v 超过 120s", tr.IdleConnTimeout)
	}
}
