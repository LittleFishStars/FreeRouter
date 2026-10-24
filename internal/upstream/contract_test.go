package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// 契约测试：task-6 验收第 6 点「TestContract 前缀 ≥8 条」。
//
// 覆盖面（任务描述逐项）：
//  1. 抽象类型与 §2 接口形状；
//  2. 连接池上限（单上游信号量 + idle_timeout ≤ server.idle_timeout 红线）；
//  3. 重试仅未吐内容（C7）；
//  4. 退避序列（§6.2 决策表 + jitter 边界）；
//  5. 健康标记（连续失败计数→cooling/degraded，成功复位）；
//  6. 配额探测拿不到不报错只记 unverified；
//  7. 出站边界（§9.3 三条红线）；
//  8. 错误分类（http=000 与状态码两侧）+ K6 精神的 http=000 端到端复现。

// ———— 1. 抽象类型契约 ————

// TestContractTypesAndInterface 验证 §2 类型形状与 Upstream 接口的可实现性。
func TestContractTypesAndInterface(t *testing.T) {
	// Kind 三个取值与 upstreams.kind 列的字符串形态必须一致（改了就是数据不兼容）。
	if KindRelay != "relay" || KindVendor != "vendor" || KindSelfHosted != "selfhosted" {
		t.Fatalf("Kind 常量漂移：%s/%s/%s", KindRelay, KindVendor, KindSelfHosted)
	}
	// Dialect 三个取值同上（Target 序列化与候选声明都用它）。
	if DialectOpenAIChat != "openai-chat" || DialectOpenAIResponses != "openai-responses" || DialectAdapter != "adapter" {
		t.Fatalf("Dialect 常量漂移")
	}

	// 接口可实现性：一个最小实现必须能赋给 Upstream（接口方法集签名锁定）。
	var _ Upstream = stubUpstream{}

	// Target 的 relay 语义：CredentialID=0 且 SecretRef 为空是合法（账号池在 sidecar 侧）。
	tgt := Target{UpstreamID: 1, Kind: KindRelay, RealModel: "buddy/deepseek-v4.1", Dialect: DialectAdapter}
	if err := ValidateTarget(tgt); err != nil {
		t.Fatalf("relay 目标应合法：%v", err)
	}
	// StreamStats 的 http=000 语义：连接类失败 UpstreamStatus 必须能表达 0。
	s := StreamStats{UpstreamStatus: 0, VisibleWritten: false}
	if s.UpstreamStatus != 0 {
		t.Fatal("http=000 必须可表达为 0")
	}
}

// stubUpstream 是接口形状的最小实现（仅用于接口契约，不模拟行为）。
type stubUpstream struct{}

func (stubUpstream) Probe(context.Context) ProbeResult { return ProbeResult{OK: true} }
func (stubUpstream) ListModels(context.Context) ([]ModelInfo, error) {
	return nil, nil
}
func (stubUpstream) Open(context.Context, Target, *ir.Request) (Stream, error) {
	return nil, nil
}

// ———— 2. 出站边界（§9.3 三条） ————

func TestContractOutboundBoundary(t *testing.T) {
	cases := []struct {
		name string
		tgt  Target
		ok   bool
	}{
		{"relay 无地址合法", Target{Kind: KindRelay}, true},
		{"relay 带地址违规（推翻 ALIGN-SPEC H）", Target{Kind: KindRelay, BaseURL: "http://127.0.0.1:8326"}, false},
		{"vendor https 合法", Target{Kind: KindVendor, BaseURL: "https://api.example.com/v1"}, true},
		{"selfhosted https 合法", Target{Kind: KindSelfHosted, BaseURL: "https://llm.example.com"}, true},
		{"vendor 公网 http 违规", Target{Kind: KindVendor, BaseURL: "http://api.example.com/v1"}, false},
		{"回环 http 放行（本地联调例外）", Target{Kind: KindSelfHosted, BaseURL: "http://127.0.0.1:11434"}, true},
		{"localhost http 放行", Target{Kind: KindSelfHosted, BaseURL: "http://localhost:11434"}, true},
		{"0.0.0.0 通配违规", Target{Kind: KindVendor, BaseURL: "https://0.0.0.0/v1"}, false},
		{":: 通配违规", Target{Kind: KindVendor, BaseURL: "https://[::]/v1"}, false},
		{"未知 kind 违规", Target{Kind: Kind("weird")}, false},
		{"vendor 缺 base_url 违规", Target{Kind: KindVendor}, false},
		{"ftp scheme 违规", Target{Kind: KindVendor, BaseURL: "ftp://example.com"}, false},
	}
	for _, tc := range cases {
		err := ValidateTarget(tc.tgt)
		if tc.ok && err != nil {
			t.Errorf("%s：应合法，got %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s：应违规", tc.name)
		}
	}
}

// ———— 3. 连接池 ————

// TestContractPoolIdleTimeoutCappedByServer 验证红线：idle_timeout ≤ server.idle_timeout。
func TestContractPoolIdleTimeoutCappedByServer(t *testing.T) {
	cases := []struct {
		server time.Duration
		want   time.Duration
	}{
		{server: 120 * time.Second, want: 90 * time.Second}, // 默认：90s（对端淘汰区间 60–120s 的保守侧）
		{server: 60 * time.Second, want: 60 * time.Second},  // server 更小：跟着更小（min 语义）
		{server: 30 * time.Second, want: 30 * time.Second},  // 极小值也要跟随
		{server: 0, want: 90 * time.Second},                 // 未配置：回落默认 90s
	}
	for _, tc := range cases {
		cfg := PoolConfig{ServerIdleTimeout: tc.server}
		if got := cfg.effectiveIdleTimeout(); got != tc.want {
			t.Errorf("server=%v：IdleConnTimeout=%v，want %v", tc.server, got, tc.want)
		}
	}
}

// TestContractPoolTransportReuse 验证 (kind,host) 复用与 relay 拒绝。
func TestContractPoolTransportReuse(t *testing.T) {
	p := NewPool(PoolConfig{})
	t1, err := p.Transport(KindVendor, "https://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	t2, err := p.Transport(KindVendor, "https://api.example.com/v1/")
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Fatal("同 (kind,host) 必须复用同一 Transport（§5.3 选定方案）")
	}
	// 同 host 不同 path 也复用（缓存键只有 scheme|host）。
	t3, err := p.Transport(KindVendor, "https://api.example.com/v2")
	if err != nil || t3 != t1 {
		t.Fatalf("同 host 不同 path 应复用：%v", err)
	}
	// selfhosted 同 host 与 vendor 不复用（kind 进缓存键：两类信任语义不同）。
	t4, err := p.Transport(KindSelfHosted, "https://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if t4 == t1 {
		t.Fatal("不同 kind 不应共享 Transport")
	}
	// relay 被拒绝：它不走 Go 的 Transport（§5.1 末段）。
	if _, err := p.Transport(KindRelay, ""); err == nil {
		t.Fatal("relay 必须被连接池拒绝")
	}
	// Transport 参数契约（§5.1 表中可编程断言的项）。
	if t1.MaxIdleConns != 128 || t1.MaxIdleConnsPerHost != 64 || t1.MaxConnsPerHost != 128 {
		t.Fatalf("连接数参数漂移：%d/%d/%d", t1.MaxIdleConns, t1.MaxIdleConnsPerHost, t1.MaxConnsPerHost)
	}
	if !t1.DisableCompression {
		t.Fatal("DisableCompression 必须为 true：gzip 会破坏 SSE 的 Flush 实时性（§5.1）")
	}
	if !t1.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 必须为 true（§5.1）")
	}
	if t1.IdleConnTimeout != 90*time.Second {
		t.Fatalf("IdleConnTimeout=%v，want 90s", t1.IdleConnTimeout)
	}
}

// TestContractPoolAcquireLimit 验证单上游并发上限。
func TestContractPoolAcquireLimit(t *testing.T) {
	p := NewPool(PoolConfig{MaxConnsPerUpstream: 3})
	ctx := context.Background()
	var releases []func()
	for i := 0; i < 3; i++ {
		r, ok := p.Acquire(ctx, 42)
		if !ok {
			t.Fatal("前 3 个名额应全部拿到")
		}
		releases = append(releases, r)
	}
	// 第 4 个：带短超时的 ctx 应拿不到（硬限）。
	ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, ok := p.Acquire(ctx2, 42); ok {
		t.Fatal("超限后不应再拿到名额")
	}
	// 释放一个后能再拿到。
	releases[0]()
	if r, ok := p.Acquire(ctx, 42); !ok {
		t.Fatal("释放后应能再拿到")
	} else {
		r()
	}
	for _, r := range releases[1:] {
		r()
	}
	// 不同上游互不影响（上限按上游实体独立）。
	if _, ok := p.Acquire(ctx, 43); !ok {
		t.Fatal("不同上游的名额互不影响")
	}
}

// ———— 4. 健康标记 ————

func TestContractHealthStateMachine(t *testing.T) {
	h := NewHealth(0) // 0 → 默认阈值 3
	// 连续连接类失败 2 次：还在 healthy（未到阈值）。
	h.RecordFailure(ErrConnect)
	h.RecordFailure(ErrConnect)
	if h.State() != HealthHealthy || !h.Available() {
		t.Fatal("2/3 次失败应保持 healthy")
	}
	// 第 3 次：连接类 → cooling（不参与候选展开）。
	h.RecordFailure(ErrConnect)
	if h.State() != HealthCooling || h.Available() {
		t.Fatal("连续 3 次连接类失败应进入 cooling")
	}
	// 一次成功复位（§7.1：任一次成功即复位）。
	h.RecordSuccess()
	if h.State() != HealthHealthy || h.ConsecutiveFails() != 0 {
		t.Fatal("成功应复位 healthy 并清零计数")
	}

	// 5xx/429 连续失败 → degraded（降权但保留：摘除等于 503）。
	for i := 0; i < 3; i++ {
		h.RecordFailure(ErrUpstream5xx)
	}
	if h.State() != HealthDegraded {
		t.Fatal("连续 5xx 应进入 degraded")
	}
	if !h.Available() {
		t.Fatal("degraded 仍参与候选展开（只是降权）")
	}

	// 429 单次不动状态（凭据根因，健康计数只在连续时说话）。
	h2 := NewHealth(0)
	h2.RecordFailure(ErrRateLimited)
	if h2.State() != HealthHealthy {
		t.Fatal("单次 429 不应动健康状态")
	}
	// 但连续 429 到阈值 → degraded。
	h2.RecordFailure(ErrRateLimited)
	h2.RecordFailure(ErrRateLimited)
	if h2.State() != HealthDegraded {
		t.Fatal("连续 429 到阈值应 degraded")
	}

	// nil Health 的全部方法安全（装配层可能持有 nil 接口）。
	var nilH *Health
	nilH.RecordFailure(ErrConnect)
	nilH.RecordSuccess()
	if nilH.State() != HealthHealthy || !nilH.Available() || nilH.ConsecutiveFails() != 0 {
		t.Fatal("nil Health 必须安全且视为 healthy（可用）")
	}
}

// ———— 5. 重试决策 ————

// TestContractRetryOnlyBeforeVisibleContent 验证 C7：已吐可见内容后任何错误 0 次重试。
func TestContractRetryOnlyBeforeVisibleContent(t *testing.T) {
	b := DefaultRetryBudget()
	ctl := NewRetryController(b)

	// 未吐内容的 conn_reset：允许同候选重试（半开连接剥离率最高的路径）。
	d := ctl.OnFailure(ErrConnReset, 0, 0, false)
	if d.Action != RetrySameCandidate {
		t.Fatalf("未吐内容 conn_reset 应同候选重试，got %v", d.Action)
	}
	// 已吐可见内容：即使是「最适合重试」的 conn_reset 也直接终止（C7 流式串话）。
	d = ctl.OnFailure(ErrConnReset, 0, 0, true)
	if d.Action != FailRequest {
		t.Fatalf("已吐内容后应 FailRequest，got %v", d.Action)
	}
	if b.CanRetry(ErrConnReset, 0, 0, true) {
		t.Fatal("CanRetry 对 visibleWritten=true 必须为 false")
	}
	// 换候选同样被禁：OnFailure 的 FailRequest 是唯一出路。
	ctl2 := NewRetryController(DefaultRetryBudget())
	if d := ctl2.OnFailure(ErrRateLimited, 0, 0, true); d.Action != FailRequest {
		t.Fatal("已吐内容后 429 也必须 FailRequest（不能换候选）")
	}
}

// TestContractRetryBudgetAndSequence 验证预算上限与退避序列。
func TestContractRetryBudgetAndSequence(t *testing.T) {
	b := DefaultRetryBudget()
	ctl := NewRetryController(b)

	// conn_reset：2 次同候选重试后转 SwitchCandidate。
	d := ctl.OnFailure(ErrConnReset, 0, 0, false)
	if d.Action != RetrySameCandidate || d.Backoff < 50*time.Millisecond {
		t.Fatalf("第 1 次重试退避应 ≥50ms，got %v", d.Backoff)
	}
	if d.Backoff > 75*time.Millisecond { // 50ms 基 × 1.5 jitter 上限
		t.Fatalf("第 1 次退避越界：%v", d.Backoff)
	}
	d = ctl.OnFailure(ErrConnReset, 1, 1, false)
	if d.Action != RetrySameCandidate {
		t.Fatalf("第 2 次仍应同候选重试，got %v", d.Action)
	}
	if d.Backoff < 150*time.Millisecond {
		t.Fatalf("第 2 次退避应 ≥150ms，got %v", d.Backoff)
	}
	if d.Backoff > 225*time.Millisecond { // 150ms × 1.5
		t.Fatalf("第 2 次退避越界：%v", d.Backoff)
	}
	d = ctl.OnFailure(ErrConnReset, 2, 2, false)
	if d.Action != SwitchCandidate {
		t.Fatalf("同候选 2 次用尽应换候选，got %v", d.Action)
	}

	// 请求内总预算 4：totalRetries≥4 时即使同候选账清零也终止。
	d = ctl.OnFailure(ErrConnReset, 0, 4, false)
	if d.Action != FailRequest {
		t.Fatalf("请求内预算耗尽应终止，got %v", d.Action)
	}

	// 429/auth/quota/bad_request/protocol：0 次同候选重试。
	for _, class := range []ErrClass{ErrRateLimited, ErrAuth, ErrQuota, ErrBadRequest, ErrProtocol} {
		if b.CanRetry(class, 0, 0, false) {
			t.Errorf("%s 不应同候选重试", class)
		}
	}
	// bad_request 直接 FailRequest（还客户端，换候选也无意义）。
	if d := ctl.OnFailure(ErrBadRequest, 0, 0, false); d.Action != FailRequest {
		t.Fatal("bad_request 应直接还客户端")
	}
	// timeout 允许 1 次且无退避（换候选更优）。
	if d := ctl.OnFailure(ErrTimeout, 0, 0, false); d.Action != RetrySameCandidate || d.Backoff != 0 {
		t.Fatal("timeout 应允许 1 次同候选重试且无退避")
	}
	// ResetCandidate：同候选账清零、请求内账保留。Total 取最近一次 OnFailure 的
	// totalRetries 快照——先喂一次带 4 的失败再复位，验证账本行为。
	ctl.OnFailure(ErrConnReset, 2, 4, false)
	ctl.ResetCandidate()
	if ctl.Total() != 4 {
		t.Fatal("ResetCandidate 不应清请求内计数")
	}
}

// TestContractBackoffJitterBounds 验证退避 jitter 落在基值 +0~50%。
func TestContractBackoffJitterBounds(t *testing.T) {
	b := DefaultRetryBudget()
	// conn_reset 第 1 次基 50ms：期望 [50, 75]ms。
	lo, hi := 50*time.Millisecond, 75*time.Millisecond
	for i := 0; i < 500; i++ {
		d := b.Backoff(ErrConnReset, 0)
		if d < lo || d > hi {
			t.Fatalf("退避越界：%v ∉ [%v,%v]", d, lo, hi)
		}
	}
	// 越界索引取最后一个退避档。
	if d := b.Backoff(ErrConnReset, 99); d < 150*time.Millisecond {
		t.Fatalf("越界索引应取最后一档 ≥150ms，got %v", d)
	}
	// 无退避配置的类返回 0。
	if d := b.Backoff(ErrTimeout, 0); d != 0 {
		t.Fatal("timeout 无退避配置应返回 0")
	}
	if d := b.Backoff(ErrRateLimited, 0); d != 0 {
		t.Fatal("429 无退避应返回 0")
	}
}

// TestContractCountsAsUpstreamFailure 验证健康计费的分类。
func TestContractCountsAsUpstreamFailure(t *testing.T) {
	should := []ErrClass{ErrConnect, ErrTimeout, ErrUpstream5xx}
	shouldNot := []ErrClass{ErrConnReset, ErrTLS, ErrRateLimited, ErrAuth, ErrQuota, ErrBadRequest, ErrProtocol}
	for _, c := range should {
		if !CountsAsUpstreamFailure(c) {
			t.Errorf("%s 应计入健康失败", c)
		}
	}
	for _, c := range shouldNot {
		if CountsAsUpstreamFailure(c) {
			t.Errorf("%s 不应计入健康失败", c)
		}
	}
}

// ———— 6. 错误分类 ————

func TestContractErrorClassification(t *testing.T) {
	// 传输层文案 → 类。
	cases := []struct {
		err  error
		want ErrClass
	}{
		{io.EOF, ErrConnReset},
		{errors.New("read: connection reset by peer"), ErrConnReset},
		{errors.New("http2: server sent GOAWAY"), ErrConnReset},
		{errors.New("write: broken pipe"), ErrConnReset},
		{errors.New("dial tcp 127.0.0.1:443: connect: connection refused"), ErrConnect},
		{errors.New("tls: handshake failure"), ErrTLS},
		{errors.New("x509: certificate signed by unknown authority"), ErrTLS},
		{&timeoutError{}, ErrTimeout},
		{errors.New("weird stuff"), ErrProtocol},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("Classify(%v)=%s，want %s", tc.err, got, tc.want)
		}
	}
	// 状态码 → 类。
	sc := []struct {
		status int
		want   ErrClass
	}{
		{429, ErrRateLimited}, {401, ErrAuth}, {403, ErrAuth},
		{500, ErrUpstream5xx}, {503, ErrUpstream5xx}, {400, ErrBadRequest}, {404, ErrBadRequest},
		{200, ""}, {204, ""}, {0, ""},
	}
	for _, tc := range sc {
		if got := ClassifyStatus(tc.status); got != tc.want {
			t.Errorf("ClassifyStatus(%d)=%s，want %s", tc.status, got, tc.want)
		}
	}
	// Error 的 http=000 文案契约（日志与 DB 同词）。
	e := &Error{Class: ErrConnReset, Status: 0, Message: "半开连接"}
	if !strings.Contains(e.Error(), "http=000") {
		t.Fatalf("连接类错误文案必须含 http=000：%s", e.Error())
	}
	e2 := &Error{Class: ErrRateLimited, Status: 429, Message: "限流"}
	if !strings.Contains(e2.Error(), "http=429") {
		t.Fatalf("状态码错误文案必须含状态码：%s", e2.Error())
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// ———— 7. 配额探测 ————

// fakeCreditsQuerier 模拟 sidecar 客户端。
type fakeCreditsQuerier struct {
	mu       sync.Mutex
	defaultF func(provider string) (*dshprovider.Credits, error)
}

func (f *fakeCreditsQuerier) CreditsQuery(_ context.Context, provider, _ string) (*dshprovider.Credits, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.defaultF != nil {
		return f.defaultF(provider)
	}
	return &dshprovider.Credits{Balance: ptrFloat(12.5), Unit: "credits"}, nil
}

func ptrFloat(v float64) *float64 { return &v }

// TestContractQuotaProbeUnverifiedNotError 验证拿不到不报错只记 unverified。
func TestContractQuotaProbeUnverifiedNotError(t *testing.T) {
	// nil client：全 unverified。
	probe := NewQuotaProbe(nil)
	q := probe.Probe(context.Background(), "buddy")
	if q.Source != QuotaUnverified {
		t.Fatalf("nil client 应 unverified，got %s", q.Source)
	}
	if q.Detail == "" {
		t.Fatal("unverified 必须带原因（Detail）")
	}

	// sidecar 报错：unverified 不抛错。
	failing := &fakeCreditsQuerier{defaultF: func(string) (*dshprovider.Credits, error) {
		return nil, &dshprovider.Error{Code: "E_DEPENDENCY_MISSING", Message: "hub 不可装载"}
	}}
	probe2 := NewQuotaProbe(failing)
	q2 := probe2.Probe(context.Background(), "buddy")
	if q2.Source != QuotaUnverified || q2.Detail == "" {
		t.Fatalf("报错应转 unverified+Detail，got %+v", q2)
	}

	// 空结果：unverified。
	probe3 := NewQuotaProbe(&fakeCreditsQuerier{defaultF: func(string) (*dshprovider.Credits, error) {
		return nil, nil
	}})
	if q3 := probe3.Probe(context.Background(), "buddy"); q3.Source != QuotaUnverified {
		t.Fatal("空结果应 unverified")
	}

	// 成功路径：字段搬运。
	ok := &fakeCreditsQuerier{}
	q4 := NewQuotaProbe(ok).Probe(context.Background(), "buddy")
	if q4.Source != QuotaFromSidecar || q4.Balance == nil || *q4.Balance != 12.5 || q4.Unit != "credits" {
		t.Fatalf("成功路径字段搬运错误：%+v", q4)
	}
	if q4.Provider != "buddy" || q4.CheckedAt.IsZero() {
		t.Fatal("Provider 与 CheckedAt 必须填")
	}

	// nil 探测器自身也安全（装配层可持有 nil）。
	var nilProbe *QuotaProbe
	if q5 := nilProbe.Probe(context.Background(), "buddy"); q5.Source != QuotaUnverified {
		t.Fatal("nil 探测器应安全返回 unverified")
	}
}

// TestContractQuotaProbeAllBoundedConcurrency 验证批量探测保序与有界并发。
func TestContractQuotaProbeAllBoundedConcurrency(t *testing.T) {
	var inflight, maxInflight atomic.Int64
	fake := &fakeCreditsQuerier{defaultF: func(p string) (*dshprovider.Credits, error) {
		cur := inflight.Add(1)
		for {
			m := maxInflight.Load()
			if cur <= m || maxInflight.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inflight.Add(-1)
		return &dshprovider.Credits{Unit: p}, nil
	}}
	providers := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	results := NewQuotaProbe(fake).ProbeAll(context.Background(), providers)
	if len(results) != len(providers) {
		t.Fatalf("结果数 %d，want %d", len(results), len(providers))
	}
	for i, r := range results {
		if r.Provider != providers[i] {
			t.Fatalf("顺序漂移：results[%d].Provider=%s", i, r.Provider)
		}
	}
	if max := maxInflight.Load(); max > 4 {
		t.Fatalf("并发越界：%d > 4", max)
	}
}

// ———— 8. http=000 端到端复现与重试（K6 精神） ————

// TestContractHTTP000RetryOverHTTP 用真 HTTP 服务复现「连接被对端关闭→请求报错→
// 分类→决策表允许同候选重试」的完整判定链（不模拟具体上游，只驱动决策表）。
func TestContractHTTP000RetryOverHTTP(t *testing.T) {
	// 一个立即关连接的服务（复现连接类失败）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 直接劫持连接并关闭，模拟对端 FIN。
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			if conn != nil {
				conn.Close()
			}
		}
	}))
	defer srv.Close()

	p := NewPool(PoolConfig{ConnectTimeout: 2 * time.Second, ResponseHeaderTimeout: 2 * time.Second})
	tr, err := p.Transport(KindSelfHosted, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: tr}

	ctl := NewRetryController(DefaultRetryBudget())
	h := NewHealth(0)
	attempts := 0
	var lastErr error
	// 模拟 call 模块的循环：发→分类→决策→（退避→）重试。
	same, total := 0, 0
loop:
	for attempts < 6 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/models", nil)
		resp, err := client.Do(req)
		attempts++
		if err == nil {
			resp.Body.Close()
			h.RecordSuccess()
			break loop
		}
		lastErr = err
		class := Classify(err)
		directive := ctl.OnFailure(class, same, total, false)
		switch directive.Action {
		case RetrySameCandidate:
			same++
			total++
			time.Sleep(directive.Backoff)
		case SwitchCandidate, FailRequest:
			break loop
		}
	}
	// 断言：重试路径确实被走到过（决策表允许 conn_reset 同候选重试）。
	if attempts < 2 {
		t.Fatalf("至少应尝试 2 次（首败+重试），got %d 次，lastErr=%v", attempts, lastErr)
	}
	// 连接类失败不计健康（半开连接是复用代价，不是上游病了）。
	if h.ConsecutiveFails() != 0 {
		t.Fatalf("conn_reset 不应计健康失败：%d", h.ConsecutiveFails())
	}
	t.Logf("http=000 复现：attempts=%d lastErr=%v", attempts, lastErr)
}

// ———— 9. 日志路径不 panic（旁路保障） ————

func TestContractLoggingNoPanic(t *testing.T) {
	// 无 logger 的 ctx：FromContext 退回全局 default，绝不 nil。
	ctx := context.Background()
	tgt := Target{UpstreamID: 7, Kind: KindVendor, CredentialID: 9}
	LogUpstreamFailure(ctx, tgt, 1, ErrConnReset, 0, 150*time.Millisecond)
	LogUpstreamSuccess(ctx, tgt, 200, 42*time.Millisecond, 0)
	LogQuotaResult(ctx, Quota{Provider: "buddy", Source: QuotaUnverified, Detail: "x"})
	LogQuotaResult(ctx, Quota{Provider: "buddy", Source: QuotaFromSidecar, Balance: ptrFloat(1.5), Unit: "credits", CheckedAt: time.Now()})
}

// ———— 10. 工具函数 ————

func TestContractItoaAndGuessUnit(t *testing.T) {
	if itoa(0) != "0" || itoa(429) != "429" || itoa(-1) != "-1" {
		t.Fatal("itoa 契约漂移")
	}
	if guessUnitFromRaw([]byte(`{"unit":"credits"}`)) != "credit" {
		t.Fatal("unit 猜测应命中 credit")
	}
	if guessUnitFromRaw([]byte(`{}`)) != "" {
		t.Fatal("猜不中应返回空")
	}
}
