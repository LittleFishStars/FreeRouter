package httpapi

// v1_test.go —— M5 v1 出口面板的契约测试（task-8 验收：≥12 条 TestContract）。
//
// 断言对象是「客户端可观察的契约」（01-inbound.md / 03-errors.md）：
// 状态码、错误体形状、SSE 帧序列、失败切换语义。上游全部注入 fake
// （Upstream/CandidateSource/ModelCatalog 接口），不依赖真实 sidecar。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/anthropic"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai"
)

// ---------------------------------------------------------------------------
// fake 基建
// ---------------------------------------------------------------------------

// fakeUpstream 按脚本回放：每次 Call 消费 script 里一个条目。
type fakeUpstream struct {
	// script 是候选调用序列的回放条目（第 n 次调用取 script[n]）。
	script []fakeCall
	calls  int
}

// fakeCall 是一次上游调用的回放：要么返回非流式响应体，要么返回
// SSE 字节流，要么直接失败。
type fakeCall struct {
	// Response 非空：非流式路径整读它。
	Response string
	// Stream 非空：流式路径逐帧读它。
	Stream string
	// Err 非 nil：Call 直接返回错误（连接层失败）。
	Err error
	// CallErr 非 nil：UpstreamCall.Err（上游开口后失败）。
	CallErr error
}

func (f *fakeUpstream) Call(ctx context.Context, cand Candidate, req ir.Request, stream bool) (*UpstreamCall, error) {
	entry := f.script[min(f.calls, len(f.script)-1)]
	f.calls++
	if entry.Err != nil {
		return nil, entry.Err
	}
	if entry.CallErr != nil {
		return &UpstreamCall{Body: io.NopCloser(strings.NewReader("")), Err: entry.CallErr}, nil
	}
	var body string
	if stream && entry.Stream != "" {
		body = entry.Stream
	} else {
		body = entry.Response
	}
	return &UpstreamCall{Body: io.NopCloser(strings.NewReader(body))}, nil
}

// fakeUpstreamBodyCapture 包装版：额外捕获上行请求体（Decode 前的原始字节）。
// 为什么单独一个类型：不是每个测试都需要捕获——基线 fake 保持最小面。
type fakeCandidates struct {
	models map[string][]Candidate
	// notFound 非空时所有请求都返回 ErrModelNotFound（M6 接线：跑
	// 「真实模型名/不存在模型 → 404」契约，替代 M5 的 isRealModelName
	// 前置判定路径）。
	notFound bool
	// failOn 非空时记录每次上游失败回传（CandidateFailureNotifier）。
	failed []string
}

func (f *fakeCandidates) Candidates(ctx context.Context, model string, _ ir.Request) ([]Candidate, error) {
	if f.notFound {
		return nil, ErrModelNotFound
	}
	return f.models[model], nil
}

// NotifyCandidateFailure 让 fake 也满足 CandidateFailureNotifier——
// 契约测试据此断言「失败候选确实被回传」（冷却账本入口）。
func (f *fakeCandidates) NotifyCandidateFailure(cand Candidate) {
	f.failed = append(f.failed, cand.UpstreamModelID)
}

// fakeCatalog 是 ModelCatalog 的内存实现。
type fakeCatalog struct {
	ids []string
}

func (f *fakeCatalog) ListModelIDs(ctx context.Context) ([]string, error) {
	return f.ids, nil
}

// newV1Server 建 Server + 注入 fake 依赖 + 返回直调 handler 的测试台。
// 为什么不走 Handler()：v1 路由还没挂（Lead 装配域）——直接调 handler
// 方法就是「路由挂好之后」的语义，测试先行锁定契约。
func newV1Server(t *testing.T, up Upstream, cs CandidateSource) *Server {
	t.Helper()
	s := New(Deps{Config: testConfigV1(t), Logger: discardLogger()})
	s.AttachV1(up, cs)
	return s
}

// testConfig 与 contract_test.newTestServer 同源：Default+Validate。
func testConfigV1(t *testing.T) *config.Config {
	t.Helper()
	c := config.Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("测试配置不合法：%v", err)
	}
	return c
}

// v1Request 构造带账户 ctx 的请求（鉴权中间件不在本任务——测试直接注入）。
func v1Request(method, target string, body string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(InjectTestAccount(r.Context()))
	return r
}

// chatCandidate 是 openai_chat 方言的测试候选。
func chatCandidate(modelID string) Candidate {
	return Candidate{UpstreamModelID: modelID, Display: "test:" + modelID, Adapter: openai.Adapter()}
}

// anthropicCandidate 是 anthropic 方言的测试候选。
func anthropicCandidate(modelID string) Candidate {
	return Candidate{UpstreamModelID: modelID, Display: "test:" + modelID, Adapter: anthropic.Adapter()}
}

// 非流式 openai 响应样本（golden 同款形状）。
const openAIResponseOK = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"你好！"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

// 流式 openai SSE 样本（§4.1 契约形状：role 首帧→text→[DONE]）。
const openAIStreamOK = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`

// anthropic 流式 SSE 样本（§6.3 具名事件）。
const anthropicStreamOK = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":128,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}

event: message_stop
data: {"type":"message_stop"}
`

// ---------------------------------------------------------------------------
// 契约测试
// ---------------------------------------------------------------------------

// TestContractV1ChatNonStreamOK：/v1/chat/completions 非流式 200。
// 断言：状态码、Content-Type、响应体是合法 chat.completion、model 字段
// 恒为请求里的模型名（§4.1——上游返回真实模型名也必须替换）。
func TestContractV1ChatNonStreamOK(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Response: openAIResponseOK}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"你好"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
	}
	if body.Model != "smart" {
		t.Fatalf("model 字段 = %q，期望恒为请求里的抽象模型名 %q（§4.1）", body.Model, "smart")
	}
}

// TestContractV1ChatStreamSequence：流式 SSE 具名分片顺序（§4.1）。
// 断言：首帧 delta 含 role:assistant、有 text 分片、结尾 data: [DONE]、
// Content-Type 是 text/event-stream。
func TestContractV1ChatStreamSequence(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Stream: openAIStreamOK}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"smart","stream":true,"messages":[{"role":"user","content":"你好"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q，期望 text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Fatalf("首帧 delta 应含 role:assistant（§4.1）；body=%s", body)
	}
	if !strings.Contains(body, `"content":"你"`) || !strings.Contains(body, `"content":"好"`) {
		t.Fatalf("流应包含 text 分片；body=%s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("流必须以 data: [DONE] 结尾（§4.1）；尾部=%q", body[len(body)-40:])
	}
	// model 恒抽象名（§4.1）：上游 chunk 里是 deepseek-chat，出站必须是 smart。
	if strings.Contains(body, `"model":"deepseek-chat"`) {
		t.Fatalf("流里 model 字段不得出现真实模型名（防泄露池结构）；body=%s", body)
	}
}

// TestContractV1MessagesStreamEvents：/v1/messages 具名事件序列（§6.3）。
// 断言：message_start → content_block_delta → message_stop 顺序出现、
// 无 [DONE]、message_start 的 stop_reason 是 null（壳帧）。
func TestContractV1MessagesStreamEvents(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Stream: anthropicStreamOK}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {anthropicCandidate("claude-test")}, // 上游 anthropic 方言（候选方言与流样本一致）
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Messages(rec, v1Request("POST", "/v1/messages", `{"model":"smart","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"你好"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// 具名事件顺序（message_start 在最前、message_stop 在最后）。
	if !strings.HasPrefix(body, "event: message_start") {
		t.Fatalf("流必须以 event: message_start 开头（§6.3）；头部=%q", body[:min(40, len(body))])
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "event: message_stop\ndata: {\"type\":\"message_stop\"}") {
		t.Fatalf("流必须以 message_stop 事件结尾（§6.3）")
	}
	if strings.Contains(body, "content_block_delta") == false {
		t.Fatal("流应包含 content_block_delta 事件（§6.3）")
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("Anthropic 流不得含 [DONE]（§6.3）；body=%s", body)
	}
	if !strings.Contains(body, `"stop_reason":null`) {
		t.Fatalf("message_start 壳帧 stop_reason 应为 null；body=%s", body)
	}
}

// TestContractV1MessagesNonStream：/v1/messages 非流式 200（跨方言：入站
// Anthropic、上游 openai_chat——IR 层完成转换）。
func TestContractV1MessagesNonStream(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Response: openAIResponseOK}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Messages(rec, v1Request("POST", "/v1/messages", `{"model":"smart","max_tokens":64,"messages":[{"role":"user","content":"你好"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Type  string `json:"type"`
		Role  string `json:"role"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
	}
	if body.Type != "message" || body.Role != "assistant" {
		t.Fatalf("响应形状错误：type=%q role=%q（期望 message/assistant）", body.Type, body.Role)
	}
	if body.Model != "smart" {
		t.Fatalf("model = %q，期望恒为抽象模型名", body.Model)
	}
}

// TestContractV1MessagesMaxTokensRequired：max_tokens 缺失 400（§6.1 必填）。
func TestContractV1MessagesMaxTokensRequired(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})

	rec := httptest.NewRecorder()
	s.HandleV1Messages(rec, v1Request("POST", "/v1/messages", `{"model":"smart","messages":[{"role":"user","content":"你好"}]}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400（max_tokens 必填）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "max_tokens") {
		t.Fatalf("错误信息应指明 max_tokens；body=%s", rec.Body.String())
	}
}

// TestContractV1MessagesVersionEcho：anthropic-version 回显（§3.3）。
func TestContractV1MessagesVersionEcho(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})

	rec := httptest.NewRecorder()
	req := v1Request("POST", "/v1/messages", `{"model":"smart","max_tokens":64,"messages":[{"role":"user","content":"x"}]}`)
	req.Header.Set("anthropic-version", "2023-06-01")
	s.HandleV1Messages(rec, req)

	if got := rec.Header().Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version 回显 = %q，期望 2023-06-01（§3.3）", got)
	}
}

// TestContractV1KeyMissing401：Key 缺失 401 invalid_api_key（§2.2：
// 不区分缺失/无效文案——防探测）。
func TestContractV1KeyMissing401(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})

	// 无账户 ctx 的裸请求。
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"smart","messages":[{"role":"user","content":"x"}]}`))
	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON：%v", err)
	}
	if body.Error.Code != "invalid_api_key" {
		t.Fatalf("code = %q，期望 invalid_api_key", body.Error.Code)
	}
}

// TestContractV1NoCandidate503：无候选 503 no_candidate（03-errors §3.1：
// 客户端动作是稍后重试——与 404 区分）。
func TestContractV1NoCandidate503(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"ghost","messages":[{"role":"user","content":"x"}]}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 503（无候选）", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON：%v", err)
	}
	if body.Error.Code != "no_candidate" {
		t.Fatalf("code = %q，期望 no_candidate", body.Error.Code)
	}
}

// TestContractV1AllCandidatesFailed502：候选全失败且未吐内容 → 502
// upstream_unavailable（可重试——换过全部候选了）。
func TestContractV1AllCandidatesFailed502(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{
		{Err: fmt.Errorf("connect refused")},
		{Err: fmt.Errorf("connect refused")},
	}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("d1"), chatCandidate("d2")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"x"}]}`))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502（全部候选失败）", rec.Code)
	}
	if up.calls != 2 {
		t.Fatalf("应尝试全部 2 个候选（尝试数 = %d）", up.calls)
	}
}

// TestContractV1FailoverBeforeVisibleContent：失败切换仅未吐内容时
// （C7；03-milestones M5 关键验收：第一候选 200 后中途失败不串话——
// 已吐内容后不再切换，错误以流内事件帧交付）。
func TestContractV1FailoverBeforeVisibleContent(t *testing.T) {
	// 第一候选：流开到一半断（吐了「你」之后上游断流）。
	// 第二候选：完整流。
	brokenStream := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"d1","choices":[{"index":0,"delta":{"role":"assistant","content":"你"},"finish_reason":null}]}

`
	up := &fakeUpstream{script: []fakeCall{
		{Stream: brokenStream}, // 吐了可见内容后 EOF（无 [DONE]）
		{Stream: openAIStreamOK},
	}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("d1"), chatCandidate("d2")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"smart","stream":true,"messages":[{"role":"user","content":"你好"}]}`))

	body := rec.Body.String()
	if up.calls != 1 {
		t.Fatalf("已吐可见内容后不得切换候选（C7）：实际尝试 %d 个候选", up.calls)
	}
	// 已吐内容 → 状态码不可改（§4.2），流内交付错误帧。
	if rec.Code != http.StatusOK {
		t.Fatalf("已开流后状态码不可改（§4.2）：实际 %d", rec.Code)
	}
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Fatalf("第一候选已吐内容应保留（不串话）；body=%s", body)
	}
}

// TestContractV1FailoverOnEmptyCandidate：未吐任何可见内容时切换成功
// （第一候选连接失败 → 第二候选完整交付）。
func TestContractV1FailoverOnEmptyCandidate(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{
		{Err: fmt.Errorf("connect refused")}, // 第一候选连接层失败
		{Stream: openAIStreamOK},             // 第二候选成功
	}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("d1"), chatCandidate("d2")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"smart","stream":true,"messages":[{"role":"user","content":"你好"}]}`))

	if up.calls != 2 {
		t.Fatalf("未吐内容时应切换到第二候选（尝试 %d 次）", up.calls)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("切换后应 200；实际 %d；body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("第二候选的流应完整交付（含 [DONE]）")
	}
}

// TestContractV1ErrorTypeFromIRTable：错误 type 字段实际出自
// ir.CodeToErrorType（端到端断言锁消费点——双表裁决的验收）。
// 拿真实端点产出的错误体，断言其 type 与 ir 表对同一 code 的产出一致，
// 而非硬编码字串——httpapi 若自养第二张表，这条立刻红。
func TestContractV1ErrorTypeFromIRTable(t *testing.T) {
	cases := []struct {
		code   string
		status int
	}{
		{"no_candidate", http.StatusServiceUnavailable},
		{"invalid_api_key", http.StatusUnauthorized},
		{"upstream_unavailable", http.StatusBadGateway},
	}
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		switch tc.code {
		case "no_candidate":
			s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"ghost","messages":[{"role":"user","content":"x"}]}`))
		case "invalid_api_key":
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"smart","messages":[{"role":"user","content":"x"}]}`))
			s.HandleV1Chat(rec, req)
		case "upstream_unavailable":
			up := &fakeUpstream{script: []fakeCall{{Err: fmt.Errorf("x")}}}
			s2 := newV1Server(t, up, &fakeCandidates{models: map[string][]Candidate{"smart": {chatCandidate("d")}}})
			rec = httptest.NewRecorder()
			s2.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"x"}]}`))
		}
		var body struct {
			Error struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("[%s] 错误体不是 JSON：%v（%s）", tc.code, err, rec.Body.String())
		}
		if body.Error.Code != tc.code {
			t.Fatalf("code = %q，期望 %q", body.Error.Code, tc.code)
		}
		// 核心断言：type 必须与 ir.CodeToErrorType 同 code 的产出逐字相等
		//（唯一映射表——httpapi 不养第二张表）。
		wantType := ir.CodeToErrorType(tc.code)
		if body.Error.Type != wantType {
			t.Fatalf("[%s] type = %q，期望 ir.CodeToErrorType 产出 %q——httpapi 出现了第二张表", tc.code, body.Error.Type, wantType)
		}
		if rec.Code != tc.status {
			t.Fatalf("[%s] 状态码 = %d，期望 %d", tc.code, rec.Code, tc.status)
		}
	}
}

// TestContractV1MessagesErrorAnthropicShape：/v1/messages 错误体用
// Anthropic 形状（03-errors §2.2：{"type":"error","error":{...}}）。
func TestContractV1MessagesErrorAnthropicShape(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})

	rec := httptest.NewRecorder()
	s.HandleV1Messages(rec, v1Request("POST", "/v1/messages", `{"model":"ghost","max_tokens":64,"messages":[{"role":"user","content":"x"}]}`))

	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON：%v（%s）", err, rec.Body.String())
	}
	if body.Type != "error" {
		t.Fatalf("顶层 type = %q，期望 \"error\"（Anthropic 形状）", body.Type)
	}
	if body.Error.Code != "no_candidate" {
		t.Fatalf("error.code = %q，期望 no_candidate", body.Error.Code)
	}
	if body.Error.Type != ir.CodeToErrorType("no_candidate") {
		t.Fatalf("error.type = %q，期望 ir 表产出 %q", body.Error.Type, ir.CodeToErrorType("no_candidate"))
	}
}

// TestContractV1ModelsList：/v1/models 形状（§5：object list、
// owned_by 固定 free-router）。
func TestContractV1ModelsList(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{})
	s.AttachV1Models(&fakeCatalog{ids: []string{"smart", "fast"}})

	rec := httptest.NewRecorder()
	s.HandleV1Models(rec, v1Request("GET", "/v1/models", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if body.Object != "list" {
		t.Fatalf("object = %q，期望 list", body.Object)
	}
	if len(body.Data) != 2 {
		t.Fatalf("模型数 = %d，期望 2", len(body.Data))
	}
	for _, m := range body.Data {
		if m.Object != "model" {
			t.Fatalf("条目 object = %q，期望 model", m.Object)
		}
		if m.OwnedBy != "free-router" {
			t.Fatalf("owned_by = %q，期望固定值 free-router（§5 防泄露渠道）", m.OwnedBy)
		}
	}
}

// TestContractV1ModelsAuth：/v1/models 鉴权必须（§5）。
func TestContractV1ModelsAuth(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{})
	s.AttachV1Models(&fakeCatalog{ids: []string{"smart"}})

	req := httptest.NewRequest("GET", "/v1/models", nil) // 无账户 ctx
	rec := httptest.NewRecorder()
	s.HandleV1Models(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401（models 鉴权必须）", rec.Code)
	}
}

// TestContractV1ResponsesNonStream：/v1/responses 非流式（§7.1 最小面）。
func TestContractV1ResponsesNonStream(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Response: openAIResponseOK}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Responses(rec, v1Request("POST", "/v1/responses", `{"model":"smart","instructions":"你是助手","input":"你好"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Output []struct {
			Type string `json:"type"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
	}
	if body.Object != "response" {
		t.Fatalf("object = %q，期望 response", body.Object)
	}
	if body.Model != "smart" {
		t.Fatalf("model = %q，期望抽象模型名", body.Model)
	}
	if len(body.Output) == 0 || body.Output[0].Type != "message" {
		t.Fatalf("output[0].type = %q，期望 message", body.Output[0].Type)
	}
}

// TestContractV1ResponsesStoreTrue400：store:true 必须 400（§8.4）。
func TestContractV1ResponsesStoreTrue400(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})

	rec := httptest.NewRecorder()
	s.HandleV1Responses(rec, v1Request("POST", "/v1/responses", `{"model":"smart","input":"x","store":true}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400（store:true 硬规则）", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是 JSON：%v", err)
	}
	if body.Error.Code != "invalid_request" {
		t.Fatalf("code = %q，期望 invalid_request", body.Error.Code)
	}
}

// TestContractV1UpstreamModelIsRealID：上行给候选的是真实模型 id、
// 出站给客户端的是抽象名（§4.1 的双向验证）。
// 通过 fake Upstream 捕获 EncodeRequest 产物（cand.Adapter 是 openai——
// 上行 body 里 model 字段应为 deepseek-chat）。
func TestContractV1UpstreamModelIsRealID(t *testing.T) {
	up := &captureUpstream{inner: &fakeUpstream{script: []fakeCall{{Response: openAIResponseOK}}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"x"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	// fakeUpstream.Call 的 req 参数已带 Model=deepseek-chat（EncodeRequest
	// 用 cand.UpstreamModelID）——验证捕获的 ir.Request.Model。
	// 上行用真实模型 id：cand.UpstreamModelID 是 EncodeRequest 产物里
	// model 字段的来源（encode_request.go:model := opt.UpstreamModel）——
	// 捕获它即验证「上行真实 id / 出站抽象名」的双向契约。
	if up.lastCandID != "deepseek-chat" {
		t.Fatalf("上行候选模型 id = %q，期望 deepseek-chat（§4.1 只有出站方向恒抽象名）", up.lastCandID)
	}
}

// captureUpstream 包装 fakeUpstream 并捕获候选的真实模型 id（UpstreamModelID
// 是 EncodeRequest 产物的 model 字段来源——捕获它即验证「上行用真实 id」）。
type captureUpstream struct {
	inner      Upstream
	lastCandID string
}

func (c *captureUpstream) Call(ctx context.Context, cand Candidate, req ir.Request, stream bool) (*UpstreamCall, error) {
	c.lastCandID = cand.UpstreamModelID
	return c.inner.Call(ctx, cand, req, stream)
}

// TestContractV1RequestIDEcho：X-Free-Router-Request-Id 回写（§7.2）。
func TestContractV1RequestIDEcho(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})

	rec := httptest.NewRecorder()
	req := v1Request("POST", "/v1/chat/completions", `{"model":"ghost","messages":[{"role":"user","content":"x"}]}`)
	req.Header.Set("X-Request-ID", "req_echo_1")
	s.HandleV1Chat(rec, req)

	if got := rec.Header().Get("X-Free-Router-Request-Id"); got != "req_echo_1" {
		t.Fatalf("X-Free-Router-Request-Id = %q，期望 req_echo_1（§7.2 回写）", got)
	}
}

// TestContractV1ChatStreamErrorFrameAfterVisible：已吐内容后流内失败——
// 错误以事件帧交付（§4.3：错误帧后必须再发 [DONE]，不发会挂起客户端）。
func TestContractV1ChatStreamErrorFrameAfterVisible(t *testing.T) {
	// 一个中途带 error 事件的 openai 上游流。
	errStream := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"d1","choices":[{"index":0,"delta":{"role":"assistant","content":"你"},"finish_reason":null}]}

data: {"error":{"message":"upstream died","type":"server_error","param":null,"code":"internal_error"}}

data: [DONE]
`
	up := &fakeUpstream{script: []fakeCall{{Stream: errStream}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("d1")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions", `{"model":"smart","stream":true,"messages":[{"role":"user","content":"x"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("已吐内容后状态码不可改（§4.2）：实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error"`) {
		t.Fatalf("流内失败应交付错误事件帧；body=%s", body)
	}
	// 错误帧之后必须有 [DONE]（§4.3：不发会挂起客户端）。
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("错误帧后必须以 data: [DONE] 收尾（§4.3）；尾部=%q", body[max(0, len(body)-60):])
	}
}

// TestContractV1ResponsesStream：/v1/responses stream:true（§7.1 流式事件面）。
//
// 上游是 Chat-compatible 流（openAIStreamOK），出站必须是 Responses 具名事件：
// response.output_text.delta / response.completed。契约点：Content-Type 为
// text/event-stream（SSE），且每个 data 行都是带 type 字段的完整 JSON。
func TestContractV1ResponsesStream(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Stream: openAIStreamOK}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Responses(rec, v1Request("POST", "/v1/responses", `{"model":"smart","instructions":"你是助手","input":"你好","stream":true}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != sseContentType {
		t.Fatalf("Content-Type = %q，期望 %q", ct, sseContentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "response.output_text.delta") {
		t.Fatalf("流式响应应含 response.output_text.delta，body=%s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Fatalf("Responses 流式不应以 [DONE] 收尾（那是 Chat 的形状）；body=%s", body)
	}
}
