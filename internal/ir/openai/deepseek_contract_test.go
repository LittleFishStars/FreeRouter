package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 本文件锁定「DeepSeek 官方 Chat API 面」的契约（2026-11-02 对齐 api-docs
// /api/create-chat-completion）。为什么单独成文件：官方文档的字段比 OpenAI 上游的
// 通用方言更具体（thinking 开关、reasoning_content、reasoning_effort 的四档、
// stop 上限 16、penalties 已弃用），而这些都是客户端会真的发出来的东西——
// 任何一项处理错，官方客户端就会在网关处拿 400 或看不到思考过程。

// TestDeepSeekRequestThinkingDecode：thinking 开关必须解码成显式档位。
func TestDeepSeekRequestThinkingDecode(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ir.Effort
	}{
		{"enabled", `{"model":"m","thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`, ir.EffortHigh},
		{"disabled", `{"model":"m","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`, ir.EffortOff},
		// 官方优先级：thinking.type 优先于 reasoning_effort。
		// disabled + effort=high 必须是 off（开关关掉时 effort 不能把它打开）。
		{"disabled_wins_over_effort",
			`{"model":"m","thinking":{"type":"disabled"},"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`,
			ir.EffortOff},
		// thinking 缺省时，reasoning_effort 决定模式。
		{"effort_without_thinking",
			`{"model":"m","reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`,
			ir.EffortLow},
	}
	for _, c := range cases {
		req, _, err := Adapter().DecodeRequest([]byte(c.body))
		if err != nil {
			t.Fatalf("%s: 解码失败 %v", c.name, err)
		}
		if req.Effort != c.want {
			t.Errorf("%s: Effort=%q, want %q", c.name, req.Effort, c.want)
		}
	}
}

// TestDeepSeekRequestThinkingInvalid：官方要求 thinking 对象里的 type 必填且只允许两值。
func TestDeepSeekRequestThinkingInvalid(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","thinking":{},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","thinking":{"type":"maybe"},"messages":[{"role":"user","content":"hi"}]}`,
	} {
		_, _, err := Adapter().DecodeRequest([]byte(body))
		if err == nil {
			t.Fatalf("非法 thinking 应报错，body=%s", body)
		}
		if !strings.Contains(err.Error(), "thinking.type") {
			t.Errorf("错误应指向 thinking.type，得到 %v", err)
		}
	}
}

// TestDeepSeekRequestEffortMaxAccepted：官方 reasoning_effort 的四档必须全部可编解码。
//
// 以前 Efforts 表只声明 off/low/high，effort=max 会被候选过滤掉并最终 503，
// 而它本来就是官方合法参数——那是把合法请求当成错误。
func TestDeepSeekRequestEffortMaxAccepted(t *testing.T) {
	for _, e := range []string{"none", "low", "high", "max"} {
		req, _, err := Adapter().DecodeRequest([]byte(
			`{"model":"m","reasoning_effort":"` + e + `","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatalf("effort=%s 解码失败 %v", e, err)
		}
		if warns, err := Adapter().Capabilities().Check(req); err != nil {
			t.Errorf("effort=%s 应被能力表接受，得到 err=%v warns=%v", e, err, warns)
		}
	}
}

// TestDeepSeekRequestEffortEncoderKeepsMax：max 必须原样透传，不能降成 high。
func TestDeepSeekRequestEffortEncoderKeepsMax(t *testing.T) {
	got, ok := encodeEffort(ir.EffortMax)
	if !ok || got != "max" {
		t.Fatalf("encodeEffort(max)=(%q,%v)，期望 (max,true)", got, ok)
	}
}

// TestDeepSeekRequestIgnoredParams：官方已弃用/不承载的参数必须忽略并告警，不能 400。
//
// 这些是存量客户端的常见参数：官方对忽略项的态度是「不报错，存量客户端无需改动」。
// 我们若报 400，等于把官方兼容客户端挡在门外。
func TestDeepSeekRequestIgnoredParams(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],
		"presence_penalty":0.5,"frequency_penalty":0.5,"logprobs":true,"top_logprobs":3,
		"user":"u1","parallel_tool_calls":false}`
	req, warns, err := Adapter().DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("这些参数应被忽略而不是报错，得到 %v", err)
	}
	_ = req
	// 每个被忽略的参数都要留痕（§8.4 白名单要求「忽略并记 warn」）。
	seen := map[string]bool{}
	for _, w := range warns {
		seen[w.Param] = true
	}
	for _, p := range []string{"presence_penalty", "frequency_penalty", "logprobs", "user", "parallel_tool_calls"} {
		if !seen[p] {
			t.Errorf("参数 %s 被静默忽略（没有告警）", p)
		}
	}
}

// TestDeepSeekRequestStopLimit16：官方 stop 上限是 16 个序列。
func TestDeepSeekRequestStopLimit16(t *testing.T) {
	build := func(n int) *ir.Request {
		stops := make([]string, n)
		for i := range stops {
			stops[i] = "s"
		}
		return &ir.Request{
			Model:    "m",
			Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "hi"}}}},
			Sampling: ir.Sampling{Stop: stops},
		}
	}
	if _, err := Adapter().Capabilities().Check(build(16)); err != nil {
		t.Fatalf("16 个停止词应被接受（官方上限），得到 %v", err)
	}
	if _, err := Adapter().Capabilities().Check(build(17)); err == nil {
		t.Fatalf("17 个停止词应超限报错")
	}
}

// TestDeepSeekReasoningContentStreamDecode：流式 reasoning_content 必须转成 thinking_delta。
//
// 这是本批改动最关键的一条：以前流里把 thinking 增量整个丢掉，客户端（含官方
// 客户端与 Claude Code 类工具）在网关后面永远看不到推理过程。
func TestDeepSeekReasoningContentStreamDecode(t *testing.T) {
	sse := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"先想\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"一下\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"答案\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	rd := Adapter().NewStreamReader(strings.NewReader(sse), ir.DecodeOptions{RequestID: "req1"})
	var evs []ir.Event
	for {
		ev, err := rd.Next()
		if err != nil {
			break
		}
		evs = append(evs, ev)
	}
	var thinking, text strings.Builder
	for _, ev := range evs {
		switch ev.Type {
		case ir.EvThinkingDelta:
			thinking.WriteString(ev.Text)
		case ir.EvTextDelta:
			text.WriteString(ev.Text)
		}
	}
	if thinking.String() != "先想一下" {
		t.Errorf("思考增量 = %q，期望 %q（reasoning_content 被丢掉了）", thinking.String(), "先想一下")
	}
	if text.String() != "答案" {
		t.Errorf("正文增量 = %q，期望 %q", text.String(), "答案")
	}
	// 顺序：思考必须全部先于正文（否则客户端会把答案渲染进思考面板）。
	firstText := -1
	lastThinking := -1
	for i, ev := range evs {
		if ev.Type == ir.EvThinkingDelta {
			lastThinking = i
		}
		if ev.Type == ir.EvTextDelta && firstText < 0 {
			firstText = i
		}
	}
	if lastThinking > firstText {
		t.Errorf("思考增量出现在正文之后（thinking idx=%d, text idx=%d）", lastThinking, firstText)
	}
}

// TestDeepSeekReasoningContentStreamEncode：IR thinking_delta 必须编成 reasoning_content 帧。
func TestDeepSeekReasoningContentStreamEncode(t *testing.T) {
	var out strings.Builder
	w := Adapter().NewStreamWriter(&out, ir.EncodeOptions{UpstreamModel: "deepseek-flash", CreatedAt: 1})
	for _, ev := range []ir.Event{
		{Type: ir.EvStart},
		{Type: ir.EvThinkingDelta, Text: "先想"},
		{Type: ir.EvTextDelta, Text: "答案"},
		{Type: ir.EvDone, Stop: ir.StopEndTurn},
	} {
		if err := w.Write(ev); err != nil {
			t.Fatalf("写入 %s 失败: %v", ev.Type, err)
		}
	}
	_ = w.Close()
	body := out.String()
	if !strings.Contains(body, `"reasoning_content":"先想"`) {
		t.Errorf("思考增量未编进 reasoning_content；SSE=\n%s", body)
	}
	// 思考帧必须排在正文帧之前。
	if strings.Index(body, "reasoning_content") > strings.Index(body, `"content":"答案"`) {
		t.Errorf("reasoning_content 帧排在正文之后；SSE=\n%s", body)
	}
}

// TestDeepSeekReasoningContentNonStream：非流式 reasoning_content 必须解成 ThinkingPart 并可按原字段编回。
func TestDeepSeekReasoningContentNonStream(t *testing.T) {
	body := `{"id":"c1","object":"chat.completion","created":1,"model":"deepseek-flash",
		"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant",
		"reasoning_content":"思考","content":"答案"}}],
		"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`
	resp, err := Adapter().DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	var sawThinking bool
	for _, p := range resp.Parts {
		if tp, ok := p.(ir.ThinkingPart); ok && tp.Text == "思考" {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Fatalf("reasoning_content 未解成 ThinkingPart；parts=%+v", resp.Parts)
	}

	// 编回去：reasoning_content 与 content 必须都在。
	out, err := Adapter().EncodeResponse(resp)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var wire struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("编出的响应不是合法 JSON: %v", err)
	}
	if wire.Choices[0].Message.ReasoningContent != "思考" {
		t.Errorf("编出的 reasoning_content = %q，期望 思考", wire.Choices[0].Message.ReasoningContent)
	}
	if wire.Choices[0].Message.Content != "答案" {
		t.Errorf("编出的 content = %q，期望 答案", wire.Choices[0].Message.Content)
	}
}

// TestDeepSeekThinkingEncodeRequest：IR 档位必须编成 thinking/effort 落到上游请求体。
func TestDeepSeekThinkingEncodeRequest(t *testing.T) {
	req := &ir.Request{
		Model:    "m",
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "hi"}}}},
		Effort:   ir.EffortOff,
	}
	body, _, err := Adapter().EncodeRequest(req, ir.EncodeOptions{UpstreamModel: "deepseek-flash"})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("编出的请求不是合法 JSON: %v", err)
	}
	// 关闭思考必须显式发 thinking.type=disabled：只发 effort=none 时部分兼容上游
	// 会把空字段忽略，思考照旧开启（静默改变行为）。
	if th, ok := wire["thinking"].(map[string]any); !ok || th["type"] != "disabled" {
		t.Errorf("effort=off 应编出 thinking.type=disabled；body=%s", body)
	}
	if wire["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v，期望 none", wire["reasoning_effort"])
	}
}

// TestDeepSeekThinkingCapabilities：OpenAI Chat 声明思考能力，但不声明签名能力。
func TestDeepSeekThinkingCapabilities(t *testing.T) {
	caps := Adapter().Capabilities()
	if !caps.Thinking {
		t.Errorf("Thinking=false：官方 reasoning_content 是协议字段，必须声明为能力")
	}
	if caps.ThinkingSignature {
		t.Errorf("ThinkingSignature=true：OpenAI 侧没有签名承载位，声明它会让签名被静默丢弃")
	}
}
