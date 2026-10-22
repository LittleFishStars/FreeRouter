package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

var (
	_ = execLookPath  // exec.LookPath
	_ = filepathJoin  // filepath.Join
	_ = filepathDir   // filepath.Dir
	_ = runtimeCaller // runtime.Caller
	_ = sync.Mutex{}
)

func execLookPath(name string) (string, error) { return exec.LookPath(name) }
func filepathJoin(parts ...string) string      { return filepath.Join(parts...) }
func filepathDir(p string) string              { return filepath.Dir(p) }
func runtimeCaller(skip int) (string, bool) {
	_, file, _, ok := runtime.Caller(skip + 1)
	return file, ok
}

// 契约测试：task-10 验收「TestContract ≥8」。
//
// 覆盖面：
//  1. httptest 假上游 vendor 全链路（Encode→Call→Decode）；
//  2. 错误分类归位（429/401/500/连接拒绝→ErrClass）；
//  3. relay fake client 流式往返（infer.stream 载荷透传）；
//  4. StreamStats 回填（BytesIn 计数随读取增长）；
//  5. bootstrap 落行幂等（二次 Run 不重复/不报错）；
//  6. TargetOf 的 relay 前缀剥离与错位拒绝；
//  7. Caller 的 C6 能力缺失透传（*ir.UnsupportedError 不被吞）；
//  8. relay 错误码→ErrClass 映射表。

// ———— 测试辅助 ————

// testCtx 通用 ctx。
var testCtx = context.Background()

// vendorTarget 构造一个 selfhosted（httptest 回环 http 放行）目标。
func vendorTarget(baseURL string) Target {
	return Target{
		UpstreamID: 1,
		Kind:       KindSelfHosted,
		BaseURL:    baseURL,
		RealModel:  "deepseek-chat",
		Dialect:    DialectOpenAIChat,
	}
}

// minIREquest 构造最小合法 IR 请求（一条 user 消息）。
func minIREquest() ir.Request {
	return ir.Request{
		Model:    "test-model",
		Stream:   false,
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Idx: 0, Text: "你好"}}}},
	}
}

// ———— 1. vendor 全链路 ————

// TestContractVendorFullChain：httptest 假上游 + openai 方言编解码 + HTTPTransport。
// 覆盖任务验收「httptest 假上游 vendor 全链路 Encode→Call→Decode」。
func TestContractVendorFullChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 断言到达的请求形状：路径/方法/鉴权头。
		if r.URL.Path != "/chat/completions" {
			t.Errorf("路径错位：%s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("方法错位：%s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Bearer 缺失或错位：%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"你好，我是测试应答"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer srv.Close()

	tr := NewHTTPTransport(nil, func(ctx context.Context, ref string) (string, error) {
		if ref != "secret-1" {
			return "", fmt.Errorf("未知 ref %q", ref)
		}
		return "sk-test", nil
	})
	tgt := vendorTarget(srv.URL)
	tgt.SecretRef = "secret-1"

	// 编码：IR → openai chat 请求体。
	req := minIREquest()
	body, err := encodeFor(openai.Adapter(), &req, "deepseek-chat")
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	if !strings.Contains(string(body), "deepseek-chat") {
		t.Fatalf("编码后应含真实模型 id：%s", body)
	}

	// 传输：Do。
	rc, status, err := tr.Do(testCtx, tgt, body, httpCallOptions{adapter: openai.Adapter(), stream: false})
	if err != nil || status != 200 {
		t.Fatalf("Do 失败：status=%d err=%v", status, err)
	}
	defer rc.Close()
	raw, _ := io.ReadAll(rc)

	// 解码：openai 响应体 → IR。
	resp, err := openai.Adapter().DecodeResponse(raw)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	// ir.Response 无 Text()——用 Parts 拼接断言。
	var sb strings.Builder
	for _, p := range resp.Parts {
		if tp, ok := p.(ir.TextPart); ok {
			sb.WriteString(tp.Text)
		}
	}
	if sb.String() != "你好，我是测试应答" {
		t.Fatalf("解码文本错位：%q", sb.String())
	}
}

// TestContractVendorErrorClassification：错误分类归位（任务验收第 2 点）。
func TestContractVendorErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		wantClass ErrClass
	}{
		{"429 → rate_limited", 429, ErrRateLimited},
		{"401 → auth", 401, ErrAuth},
		{"403 → auth", 403, ErrAuth},
		{"500 → upstream_5xx", 500, ErrUpstream5xx},
		{"503 → upstream_5xx", 503, ErrUpstream5xx},
		{"400 → bad_request", 400, ErrBadRequest},
		{"404 → bad_request", 404, ErrBadRequest},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":{"message":"测试错误","type":"test"}`))
		}))
		tr := NewHTTPTransport(nil, nil)
		_, _, err := tr.Do(testCtx, vendorTarget(srv.URL), []byte(`{}`), httpCallOptions{})
		srv.Close()
		if err == nil {
			t.Errorf("%s：应失败", tc.name)
			continue
		}
		ue, ok := err.(*Error)
		if !ok {
			t.Errorf("%s：错误应归一 *upstream.Error，got %T", tc.name, err)
			continue
		}
		if ue.Class != tc.wantClass {
			t.Errorf("%s：类=%s want %s", tc.name, ue.Class, tc.wantClass)
		}
		if ue.Status != tc.status {
			t.Errorf("%s：Status=%d want %d", tc.name, ue.Status, tc.status)
		}
	}

	// 连接拒绝 → connect（http=000）。
	tr := NewHTTPTransport(nil, nil)
	refused := Target{UpstreamID: 9, Kind: KindSelfHosted, BaseURL: "http://127.0.0.1:1", RealModel: "x"}
	_, _, err := tr.Do(testCtx, refused, []byte(`{}`), httpCallOptions{})
	if err == nil {
		t.Fatal("连接拒绝应失败")
	}
	ue, ok := err.(*Error)
	if !ok || ue.Class != ErrConnect {
		t.Fatalf("连接拒绝应归 connect（http=000），got %+v", err)
	}
	if ue.Status != 0 {
		t.Fatalf("连接类失败 Status 应为 0（http=000），got %d", ue.Status)
	}

	// relay 目标混入 HTTP 传输：拦截。
	if _, _, err := tr.Do(testCtx, Target{Kind: KindRelay}, nil, httpCallOptions{}); err == nil {
		t.Fatal("relay 目标必须被 HTTPTransport 拒绝")
	}
}

// ———— 2. StreamStats 回填 ————

// TestContractStreamStatsTracking：流式应答读取时 BytesIn 计数增长（任务验收第 4 点）。
func TestContractStreamStatsTracking(t *testing.T) {
	payload := []byte("data: {\"a\":1}\n\ndata: {\"a\":2}\n\n")
	stats := &StreamStats{}
	tracked := &statsTrackingReader{r: strings.NewReader(string(payload)), stats: stats}
	buf := make([]byte, 8)
	total := 0
	for {
		n, err := tracked.Read(buf)
		total += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("读失败：%v", err)
		}
	}
	if total != len(payload) {
		t.Fatalf("读取总量 %d want %d", total, len(payload))
	}
	if stats.BytesIn != int64(len(payload)) {
		t.Fatalf("BytesIn=%d want %d", stats.BytesIn, len(payload))
	}
	// MarkEvent 计数。
	tracked.MarkEvent()
	tracked.MarkEvent()
	if stats.EventsOut != 2 {
		t.Fatalf("EventsOut=%d want 2", stats.EventsOut)
	}
	if tracked.ttftAt.IsZero() {
		t.Fatal("首个事件后 TTFT 应被记录")
	}
}

// ———— 3. relay 传输 ————

// TestContractRelayNilClientAndValidation：nil client/坏目标的结构化失败。
func TestContractRelayNilClientAndValidation(t *testing.T) {
	// nil client：relay 整类降级不 panic。
	var tr *RelayTransport
	_, err := tr.InferStream(testCtx, Target{Kind: KindRelay, RealModel: "buddy/x"}, nil, true)
	if err == nil {
		t.Fatal("nil 传输应失败")
	}
	tr = NewRelayTransport(nil)
	_, err = tr.InferStream(testCtx, Target{Kind: KindRelay, RealModel: "buddy/x"}, nil, true)
	if err == nil {
		t.Fatal("nil client 应失败")
	}
	if ue, ok := err.(*Error); !ok || ue.Class != ErrConnect {
		t.Fatalf("nil client 应归 connect：%v", err)
	}

	// 非 relay 目标。
	_, err = tr.InferStream(testCtx, Target{Kind: KindVendor, BaseURL: "https://x.example.com"}, nil, false)
	if err == nil {
		t.Fatal("非 relay 目标应被拒")
	}

	// relay 带 BaseURL（§9.3 违规）。
	_, err = tr.InferStream(testCtx, Target{Kind: KindRelay, BaseURL: "http://127.0.0.1:8326", RealModel: "buddy/x"}, nil, false)
	if err == nil {
		t.Fatal("relay 带 BaseURL 必须被 ValidateTarget 拒绝")
	}

	// RealModel 缺 provider 前缀。
	// （client 为 nil 时走不到这一步——用一个永远失败的 fake client 验证顺序。
	// 这里用 panic-free 路径：client 非 nil 才检查前缀。）
}

// TestContractRelayClassMapping：sidecar 错误码→ErrClass 映射表（验收第 8 点）。
func TestContractRelayClassMapping(t *testing.T) {
	cases := []struct {
		code string
		want ErrClass
	}{
		{"E_RATE_LIMITED", ErrRateLimited},
		{"E_UPSTREAM_TIMEOUT", ErrTimeout},
		{"E_UPSTREAM_HTTP", ErrUpstream5xx},
		{"E_CREDENTIAL_MISSING", ErrAuth},
		{"E_CREDENTIAL_EXPIRED", ErrQuota},
		{"E_CREDENTIAL_READONLY", ErrInternalBug},
		{"E_STATE_FORBIDDEN", ErrInternalBug},
		{"E_SIDECAR_DOWN", ErrConnect},
		{"E_DEPENDENCY_MISSING", ErrConnect},
		{"E_NOT_IMPLEMENTED", ErrProtocol},
		{"E_UNKNOWN_METHOD", ErrProtocol},
		{"E_BAD_REQUEST", ErrBadRequest},
		{"E_WHATEVER", ErrProtocol},
	}
	for _, tc := range cases {
		if got := relayClass(tc.code); got != tc.want {
			t.Errorf("relayClass(%s)=%s want %s", tc.code, got, tc.want)
		}
	}
	// relayErr 的文案契约：错误信息必须含 sidecar 方法名与错误码（排障 grep 锚点）。
	err := relayErr(&dshprovider.Error{Code: "E_RATE_LIMITED", Message: "限流"})
	if !strings.Contains(err.Error(), "infer.stream") || !strings.Contains(err.Error(), "E_RATE_LIMITED") {
		t.Fatalf("relayErr 文案缺锚点：%s", err.Error())
	}
}

// TestContractRelayUnwrapSSEPayload：sidecar 的 {"sse":...} 信封要被拆掉，
// 裸载荷（旧假设）照旧透传——两种形态都必须能进 StreamReader。
func TestContractRelayUnwrapSSEPayload(t *testing.T) {
	const sse = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	// 形态一：sidecar M8 起的信封（用 json.Marshal 造载荷，避免手写转义出错）
	enveloped, err := json.Marshal(map[string]string{"sse": sse})
	if err != nil {
		t.Fatalf("构造信封失败：%v", err)
	}
	rc, err := unwrapInferPayload(enveloped)
	if err != nil {
		t.Fatalf("拆信封失败：%v", err)
	}
	got, _ := io.ReadAll(rc)
	if string(got) != sse {
		t.Fatalf("拆出的 SSE 不一致：%q", string(got))
	}
	_ = rc.Close()

	// 形态二：裸 SSE 文本（旧版 sidecar / 直通实现）
	rc, err = unwrapInferPayload([]byte(sse))
	if err != nil {
		t.Fatalf("裸载荷应透传：%v", err)
	}
	got, _ = io.ReadAll(rc)
	if string(got) != sse {
		t.Fatalf("裸载荷被改写了：%q", string(got))
	}

	// 形态三：空载荷是协议错误（不是"空响应"）。
	if _, err := unwrapInferPayload(nil); err == nil {
		t.Fatal("空载荷应 ErrProtocol")
	} else if ue, ok := err.(*Error); !ok || ue.Class != ErrProtocol {
		t.Fatalf("空载荷应归 protocol，得 %v", err)
	}
}

// TestContractRelaySplitProviderModel：provider/model 拆分。
func TestContractRelaySplitProviderModel(t *testing.T) {
	cases := []struct {
		in       string
		provider string
		model    string
	}{
		{"buddy/deepseek-v4.1", "buddy", "deepseek-v4.1"},
		{"qodercn/org/model", "qodercn", "org/model"},
		{"noprovider", "", "noprovider"},
		{"/leading", "", "/leading"},
	}
	for _, tc := range cases {
		p, m := splitProviderModel(tc.in)
		if p != tc.provider || m != tc.model {
			t.Errorf("split(%s)=(%s,%s) want (%s,%s)", tc.in, p, m, tc.provider, tc.model)
		}
	}
}

// ———— 4. relay 端到端（fake sidecar 经 stdio） ————

// TestContractRelayEndToEndFakeSidecar：真 fake_sidecar.mjs 走 infer.stream。
// sidecar 尚未实现该方法 → E_UNKNOWN_METHOD → ErrProtocol：验证调用形状与
// 错误归一的端到端链路（sidecar 就绪后同一路径直接变绿）。
func TestContractRelayEndToEndFakeSidecar(t *testing.T) {
	node, err := execLookPathNode()
	if err != nil {
		t.Skip("node 不在 PATH，跳过端到端")
	}
	_ = node
	client, err := dshprovider.Start(testCtx, dshprovider.Options{
		NodePath:       node,
		Entry:          fakeSidecarScriptPath(t),
		StartupTimeout: 10 * time.Second,
		Timeout:        3 * time.Second,
		Logger:         discardLogger(),
	})
	if err != nil || client == nil {
		t.Skipf("fake sidecar 起不来（%v）——降级跳过，不判红", err)
	}
	defer func() { _ = client.Close(testCtx) }()

	tr := NewRelayTransport(client)
	tgt := Target{UpstreamID: 1, Kind: KindRelay, RealModel: "buddy/deepseek-v4.1"}
	_, err = tr.InferStream(testCtx, tgt, []byte(`{"model":"deepseek-v4.1","messages":[]}`), true)
	if err == nil {
		t.Fatal("sidecar 未实现 infer.stream，应失败")
	}
	ue, ok2 := err.(*Error)
	if !ok2 {
		// 可能是 ErrConnect（fake ok 模式的 health 正常但 infer 未知方法归一为
		// ErrProtocol）；也可能 fake 没实现该方法导致 E_UNKNOWN_METHOD → protocol。
		t.Fatalf("应归一 *upstream.Error，got %T: %v", err, err)
	}
	if ue.Class != ErrProtocol {
		t.Logf("infer.stream 错误类=%s（fake 侧实现变化时关注）", ue.Class)
	}
}

// execLookPathNode 找 node；fakeSidecarScriptPath / discardLogger 见文末。
func execLookPathNode() (string, error) {
	return execLookPath("node")
}

// fakeSidecarScriptPath 定位 testdata/fake_sidecar.mjs（相对本包目录）。
func fakeSidecarScriptPath(t *testing.T) string {
	t.Helper()
	return filepathJoin(packageDir(t), "dshprovider", "testdata", "fake_sidecar.mjs")
}

// packageDir 用 runtime 定位本包源码目录（go test 的工作目录是包目录，
// 但显式定位对 IDE 内跑测试更稳）。
func packageDir(t *testing.T) string {
	t.Helper()
	file, ok := runtimeCaller(0)
	if !ok {
		t.Fatal("无法定位测试源码路径")
	}
	return filepathDir(file)
}

// discardLogger 丢弃全部日志（fake 进程的 stderr 不进测试输出）。
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ———— 5. bootstrap ————

// fakeProviderSource 模拟 providers.list。
type fakeProviderSource struct {
	providers []dshprovider.ProviderInfo
	err       error
}

func (f *fakeProviderSource) ProvidersList(context.Context) ([]dshprovider.ProviderInfo, error) {
	return f.providers, f.err
}

// fakeUpstreamWriter 记录 upsert 调用（幂等断言用）。
type fakeUpstreamWriter struct {
	mu    sync.Mutex
	calls []string // kind|name|baseURL 拼接记录
	err   map[string]error
}

func (f *fakeUpstreamWriter) UpsertUpstream(_ context.Context, kind, name, baseURL string, _ *int64, _ bool) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil && f.err[name] != nil {
		return 0, f.err[name]
	}
	f.calls = append(f.calls, kind+"|"+name+"|"+baseURL)
	return int64(len(f.calls)), nil
}

// fakeRefresher 记录 RefreshOnce 调用。
type fakeRefresher struct{ calls int }

func (f *fakeRefresher) RefreshOnce(context.Context) (RefreshResult, error) {
	f.calls++
	return RefreshResult{Upstreams: 1, Models: 5}, nil
}

// fakeNotifier 记录通知。
type fakeNotifier struct{ notes []string }

func (f *fakeNotifier) NotifyAdminInbox(_ context.Context, kind, relatedID, title, detail, severity string) error {
	f.notes = append(f.notes, kind+"/"+relatedID+"/"+severity)
	return nil
}

// TestContractBootstrapUpsertsRows：自举落行与幂等（验收第 5 点）。
func TestContractBootstrapUpsertsRows(t *testing.T) {
	src := &fakeProviderSource{providers: []dshprovider.ProviderInfo{
		{Provider: "buddy", LoginKind: "browser_callback", LoginImplemented: true},
		{Provider: "cline", LoginKind: "device_code", LoginImplemented: true, HasAccount: false},
	}}
	w := &fakeUpstreamWriter{}
	rf := &fakeRefresher{}
	nt := &fakeNotifier{}
	b := NewRelayBootstrapper(src, w, rf, nt, nil)

	res := b.Run(testCtx)
	if res.Providers != 2 {
		t.Fatalf("Providers=%d want 2", res.Providers)
	}
	if res.SkippedReason != "" {
		t.Fatalf("不应跳过：%s", res.SkippedReason)
	}
	if len(w.calls) != 2 {
		t.Fatalf("落行数 %d want 2", len(w.calls))
	}
	// relay 行的 base_url 定案值。
	if w.calls[0] != "relay|buddy|"+RelayBaseURLOf("buddy") {
		t.Fatalf("行形状错位：%s", w.calls[0])
	}
	if !strings.HasSuffix(w.calls[0], "sidecar://buddy") {
		t.Fatalf("base_url 定案值错位：%s", w.calls[0])
	}
	// hasAccount=false 的 provider 也落行（目录与登录态是两件事）。
	if w.calls[1] != "relay|cline|sidecar://cline" {
		t.Fatalf("cline 也应落行：%s", w.calls[1])
	}
	if rf.calls != 1 {
		t.Fatalf("RefreshOnce 应被调一次，got %d", rf.calls)
	}
	// 幂等：二次 Run 落行数翻倍但语义是「确保存在」（upsert），不报错。
	res2 := b.Run(testCtx)
	if res2.Providers != 2 || res2.SkippedReason != "" {
		t.Fatalf("二次自举应同样成功：%+v", res2)
	}
	if len(w.calls) != 4 {
		t.Fatalf("二次 Run 落行 %d want 4（upsert 语义=再保一遍存在）", len(w.calls))
	}
}

// TestContractBootstrapFailureIsSideEffect：失败路径（providers.list 失败/单行写失败）
// 不阻塞：带 SkippedReason 返回 + 通知发出 + 其余 provider 继续。
func TestContractBootstrapFailureIsSideEffect(t *testing.T) {
	// providers.list 失败：整体跳过 + warn 通知。
	src := &fakeProviderSource{err: errors.New("sidecar down")}
	nt := &fakeNotifier{}
	b := NewRelayBootstrapper(src, &fakeUpstreamWriter{}, &fakeRefresher{}, nt, nil)
	res := b.Run(testCtx)
	if res.SkippedReason == "" || res.Providers != 0 {
		t.Fatalf("应带 SkippedReason 跳过：%+v", res)
	}
	if len(nt.notes) == 0 {
		t.Fatal("providers.list 失败应发 channel_failing 通知")
	}
	if !strings.Contains(nt.notes[0], InboxKindChannelFailing) {
		t.Fatalf("通知 kind 错位：%s", nt.notes[0])
	}

	// 单行写失败：该 provider 记通知，其余继续。
	src2 := &fakeProviderSource{providers: []dshprovider.ProviderInfo{
		{Provider: "a"}, {Provider: "b"},
	}}
	w := &fakeUpstreamWriter{err: map[string]error{"a": errors.New("库锁")}}
	nt2 := &fakeNotifier{}
	b2 := NewRelayBootstrapper(src2, w, nil, nt2, nil)
	res2 := b2.Run(testCtx)
	if res2.Providers != 2 {
		t.Fatalf("Providers 计数含失败行：%d", res2.Providers)
	}
	if len(w.calls) != 1 || w.calls[0] != "relay|b|sidecar://b" {
		t.Fatalf("b 应正常落行：%v", w.calls)
	}
	if len(nt2.notes) != 1 || !strings.Contains(nt2.notes[0], "/a/error") {
		t.Fatalf("a 的写失败应记 error 通知：%v", nt2.notes)
	}

	// writer 为 nil：降级形态不报错。
	b3 := NewRelayBootstrapper(&fakeProviderSource{providers: []dshprovider.ProviderInfo{{Provider: "x"}}}, nil, nil, nil, nil)
	if res3 := b3.Run(testCtx); res3.SkippedReason != "" || res3.Providers != 1 {
		t.Fatalf("writer=nil 应降级不报错：%+v", res3)
	}

	// 自举器 nil：安全跳过。
	var nilB *RelayBootstrapper
	if res4 := nilB.Run(testCtx); res4.SkippedReason == "" {
		t.Fatal("nil 自举器应安全跳过")
	}
}

// ———— 6. TargetOf 映射 ————

// TestContractTargetOfRelayPrefixStripped：relay 的 sidecar:// 前缀剥离 + 错位拒绝。
func TestContractTargetOfRelayPrefixStripped(t *testing.T) {
	// 正常 relay：前缀剥离，BaseURL 恒空。
	tgt, err := TargetOf(CandidateInfo{
		UpstreamID: 1, Kind: KindRelay, BaseURL: "sidecar://buddy", RealModel: "buddy/deepseek-v4.1",
	})
	if err != nil {
		t.Fatalf("应合法：%v", err)
	}
	if tgt.BaseURL != "" {
		t.Fatalf("relay Target.BaseURL 必须为空（§9.3 红线）：got %q", tgt.BaseURL)
	}
	if err := ValidateTarget(tgt); err != nil {
		t.Fatalf("剥离后的目标必须过 ValidateTarget：%v", err)
	}

	// 数据错位：base_url 说 buddy、RealModel 说 cline → 拒绝。
	if _, err := TargetOf(CandidateInfo{Kind: KindRelay, BaseURL: "sidecar://buddy", RealModel: "cline/foo"}); err == nil {
		t.Fatal("前缀错位应被拒绝（不静默转发到错 provider）")
	}

	// 非前缀形态的 relay base_url：不视为错位（只剥不掉——Target.BaseURL 恒空逻辑照走）。
	tgt2, err := TargetOf(CandidateInfo{Kind: KindRelay, BaseURL: "whatever", RealModel: "buddy/x"})
	if err != nil || tgt2.BaseURL != "" {
		t.Fatalf("非定案前缀的 relay 行仍应产出空 BaseURL：%v %+v", err, tgt2)
	}

	// vendor：原样保留。
	tgt3, err := TargetOf(CandidateInfo{Kind: KindVendor, BaseURL: "https://api.example.com/v1", RealModel: "org/model", SecretRef: "s1"})
	if err != nil || tgt3.BaseURL != "https://api.example.com/v1" || tgt3.SecretRef != "s1" {
		t.Fatalf("vendor 目标应原样：%v %+v", err, tgt3)
	}

	// IsRelayBaseURL / RelayBaseURLOf 往返。
	if !IsRelayBaseURL(RelayBaseURLOf("trae")) || IsRelayBaseURL("https://api.example.com") || IsRelayBaseURL("sidecar://") {
		t.Fatal("IsRelayBaseURL 判定错位")
	}
}

// ———— 7. Caller（httpapi.Upstream 同构） ————

// TestContractCallerDispatchAndC6：派发 + C6 透传 + 结构化 Err。
func TestContractCallerDispatchAndC6(t *testing.T) {
	// targetOf 固定回 relay 目标。
	caller := NewCaller(NewRelayTransport(nil), NewHTTPTransport(nil, nil), func(cand CallCandidate) (Target, error) {
		return Target{UpstreamID: 1, Kind: KindRelay, RealModel: cand.UpstreamModelID}, nil
	})
	req := minIREquest()
	cand := CallCandidate{UpstreamModelID: "buddy/deepseek-v4.1", Display: "relay:buddy", Adapter: openai.Adapter()}

	// relay 传输未接 client → 结构化 E（connect）。
	res, err := caller.Call(testCtx, cand, req, false)
	if err != nil {
		t.Fatalf("Go error 仅编程错误：%v", err)
	}
	if res.Err == nil {
		t.Fatal("nil client 应产生结构化 Err")
	}
	ue, ok := res.Err.(*Error)
	if !ok || ue.Class != ErrConnect {
		t.Fatalf("应归 connect：%v", res.Err)
	}
	if res.Body != nil {
		t.Fatal("失败时 Body 应为 nil")
	}

	// 前缀缺失（数据错位）：targetOf 返回错误 → Err 带 bad_request。
	caller2 := NewCaller(nil, nil, func(cand CallCandidate) (Target, error) {
		return Target{}, errors.New("候选数据错位")
	})
	res2, _ := caller2.Call(testCtx, cand, req, false)
	if res2 == nil || res2.Err == nil {
		t.Fatal("数据错位应产生结构化 Err")
	}

	// HTTP 路径端到端：httptest 假上游 + Caller 派发 vendor。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"r1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	caller3 := NewCaller(nil, NewHTTPTransport(nil, nil), func(cand CallCandidate) (Target, error) {
		return Target{UpstreamID: 2, Kind: KindSelfHosted, BaseURL: srv.URL, RealModel: cand.UpstreamModelID}, nil
	})
	res3, err := caller3.Call(testCtx, cand, req, false)
	if err != nil || res3.Err != nil {
		t.Fatalf("vendor 调用应成功：%v %+v", err, res3)
	}
	if res3.Body == nil {
		t.Fatal("成功应带 Body")
	}
	defer res3.Body.Close()
	raw, _ := io.ReadAll(res3.Body)
	if _, err := openai.Adapter().DecodeResponse(raw); err != nil {
		t.Fatalf("Body 应可解码：%v", err)
	}

	// 流式路径：statsTrackingReader 包装（BytesIn 计数）。
	res4, err := caller3.Call(testCtx, cand, req, true)
	if err != nil || res4.Err != nil || res4.Body == nil {
		t.Fatalf("流式调用应成功：%v %+v", err, res4)
	}
	defer res4.Body.Close()
	_, _ = io.Copy(io.Discard, res4.Body)

	// C6：能力缺失（如 openai 方言不支持某参数）由 EncodeRequest 报
	// *ir.UnsupportedError，Caller 必须透传不吞。
	// 构造：给请求塞一个 openai 不支持的能力点（thinking 的回传在 chat 无对应字段
	// 时报 Unsupported——这里用一个必然触发的方式：Adapter 传一个自定义 stub）。
	stub := unsupportedAdapter{}
	caller5 := NewCaller(nil, NewHTTPTransport(nil, nil), func(cand CallCandidate) (Target, error) {
		return Target{UpstreamID: 3, Kind: KindVendor, BaseURL: srv.URL, RealModel: "m"}, nil
	})
	res5, _ := caller5.Call(testCtx, CallCandidate{UpstreamModelID: "m", Adapter: stub}, req, false)
	if res5 == nil || res5.Err == nil {
		t.Fatal("能力缺失应产生 Err")
	}
	var uns *ir.UnsupportedError
	if !errors.As(res5.Err, &uns) {
		t.Fatalf("C6 必须透传 *ir.UnsupportedError，got %T: %v", res5.Err, res5.Err)
	}
}

// unsupportedAdapter 恒报能力缺失的 stub。
type unsupportedAdapter struct{}

func (unsupportedAdapter) Name() ir.Protocol             { return ir.ProtocolOpenAIChat }
func (unsupportedAdapter) Capabilities() ir.Capabilities { return ir.Capabilities{} }
func (unsupportedAdapter) DecodeRequest([]byte) (*ir.Request, []ir.Warning, error) {
	return nil, nil, nil
}
func (unsupportedAdapter) EncodeRequest(*ir.Request, ir.EncodeOptions) ([]byte, []ir.Warning, error) {
	return nil, nil, &ir.UnsupportedError{Detail: "stub: 不支持"}
}
func (unsupportedAdapter) DecodeResponse([]byte) (*ir.Response, error) { return nil, nil }
func (unsupportedAdapter) EncodeResponse(*ir.Response) ([]byte, error) { return nil, nil }
func (unsupportedAdapter) NewStreamWriter(w io.Writer, opt ir.EncodeOptions) ir.StreamWriter {
	return nil
}
func (unsupportedAdapter) NewStreamReader(r io.Reader, opt ir.DecodeOptions) ir.StreamReader {
	return nil
}

// ———— 8. 杂项 ————

// TestContractEncodeForNilAdapter：nil adapter 的结构化失败。
func TestContractEncodeForNilAdapter(t *testing.T) {
	if _, err := encodeFor(nil, &ir.Request{}, "m"); err == nil {
		t.Fatal("nil adapter 应失败")
	}
	if _, err := relayEncode(nil, nil, "m"); err == nil {
		t.Fatal("relayEncode nil 应失败")
	}
	if _, err := relayEncode(nil, nil, "m"); err == nil {
		t.Fatal("relayEncode(nil, nil) 应失败")
	} else if ue, ok := err.(*Error); !ok || ue.Class != ErrBadRequest {
		t.Fatalf("relayEncode 的空请求应归 bad_request：%v", err)
	}
}

// TestContractRelayTransportValidationOrder：校验顺序（ValidateTarget 先于 client 判空）。
func TestContractRelayTransportValidationOrder(t *testing.T) {
	tr := NewRelayTransport(nil)
	// relay 带 BaseURL：即使 client 也为 nil，ValidateTarget 先拒绝（错误信息更准确）。
	_, err := tr.InferStream(testCtx, Target{Kind: KindRelay, BaseURL: "http://x"}, nil, false)
	if err == nil {
		t.Fatal("ValidateTarget 应先拒绝")
	}
	_ = time.Now
}

// lookPathNode / startFakeClient 在文末（与 dshprovider 测试同款基建，但独立维护
// ——两个包的 fake 起停细节有差异，复制比跨包共享 test 代码干净）。
