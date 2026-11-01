package completion

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 本文件锁定文本补全（FIM）协议面，对齐 DeepSeek 官方 FIM Completion API（Beta）。
// 为什么值得单独测：FIM 的请求体/响应体/流式帧三处形状都与 chat 不同，
// 任何一处串位都会让补全请求变成"没有 messages 的 chat 请求"——上游 400，
// 而错误现场指向上游，排查成本极高。

// TestFIMRequestDecode：prompt/suffix/echo 与采样参数必须落到 IR。
func TestFIMRequestDecode(t *testing.T) {
	body := `{"model":"deepseek-flash","prompt":"def fib(n):","suffix":"    return fib(n-1)+fib(n-2)",
		"max_tokens":64,"temperature":0.2,"top_p":0.9,"stop":["\n\n"]}`
	req, _, err := Adapter().DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if req.Completion == nil {
		t.Fatalf("Completion 为 nil（FIM 语义丢失）")
	}
	if req.Completion.Prompt != "def fib(n):" {
		t.Errorf("Prompt = %q", req.Completion.Prompt)
	}
	if req.Completion.Suffix != "    return fib(n-1)+fib(n-2)" {
		t.Errorf("Suffix = %q（丢掉 suffix 会静默退化成普通续写）", req.Completion.Suffix)
	}
	if len(req.Messages) != 0 {
		t.Errorf("补全请求不应产生 messages，得到 %d 条", len(req.Messages))
	}
	if req.Sampling.MaxOutput == nil || *req.Sampling.MaxOutput != 64 {
		t.Errorf("max_tokens 未落到 Sampling.MaxOutput")
	}
	if len(req.Sampling.Stop) != 1 || req.Sampling.Stop[0] != "\n\n" {
		t.Errorf("stop 未落到 Sampling.Stop: %v", req.Sampling.Stop)
	}
}

// TestFIMRequestPromptRequired：prompt 必填（空 prompt 在 FIM 里没有意义）。
func TestFIMRequestPromptRequired(t *testing.T) {
	_, _, err := Adapter().DecodeRequest([]byte(`{"model":"m","prompt":""}`))
	if err == nil {
		t.Fatalf("空 prompt 应报错")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Errorf("错误应指向 prompt，得到 %v", err)
	}
}

// TestFIMRequestEchoConflict：官方限制 echo 不能与 suffix 同时使用。
func TestFIMRequestEchoConflict(t *testing.T) {
	_, _, err := Adapter().DecodeRequest([]byte(
		`{"model":"m","prompt":"a","suffix":"b","echo":true}`))
	if err == nil {
		t.Fatalf("echo 与 suffix 同时给出应报错（官方限制）")
	}
	if !strings.Contains(err.Error(), "echo") {
		t.Errorf("错误应指向 echo，得到 %v", err)
	}
}

// TestFIMRequestIgnoresPenalties：已弃用的 penalties 与 logprobs 必须忽略并告警。
func TestFIMRequestIgnoresPenalties(t *testing.T) {
	body := `{"model":"m","prompt":"a","presence_penalty":1,"frequency_penalty":1,"logprobs":5}`
	_, warns, err := Adapter().DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("这些参数应被忽略而不是报错，得到 %v", err)
	}
	seen := map[string]bool{}
	for _, w := range warns {
		seen[w.Param] = true
	}
	for _, p := range []string{"presence_penalty", "frequency_penalty", "logprobs"} {
		if !seen[p] {
			t.Errorf("参数 %s 被静默忽略（无告警）", p)
		}
	}
}

// TestFIMCapabilities：补全方言必须有 TextCompletion，且没有工具/图片/思考位。
func TestFIMCapabilities(t *testing.T) {
	c := Adapter().Capabilities()
	if !c.TextCompletion {
		t.Errorf("TextCompletion=false：补全方言必须声明它，否则被自家能力复核拒掉")
	}
	if !c.CompletionEcho {
		t.Errorf("CompletionEcho=false：官方 FIM 支持 echo")
	}
	if c.Tools || c.Images || c.Thinking || c.JSONSchema {
		t.Errorf("补全协议没有工具/图片/思考/结构化输出的位置，不该声明: %+v", c)
	}
}

// TestFIMChatRequestRejected：把对话请求错派给补全方言必须报错（不静默丢弃 messages）。
func TestFIMChatRequestRejected(t *testing.T) {
	req := &ir.Request{
		Model:    "m",
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "hi"}}}},
	}
	if _, _, err := Adapter().EncodeRequest(req, ir.EncodeOptions{UpstreamModel: "m"}); err == nil {
		t.Fatalf("补全方言不应接受对话请求")
	}
}

// TestFIMRequestEncode：编码出的请求体必须有 prompt，且 suffix 只在非空时出现。
func TestFIMRequestEncode(t *testing.T) {
	req := &ir.Request{
		Model:      "m",
		Completion: &ir.Completion{Prompt: "def add(a,b):", Suffix: "return c"},
	}
	body, _, err := Adapter().EncodeRequest(req, ir.EncodeOptions{UpstreamModel: "deepseek-flash"})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("编出的请求不是合法 JSON: %v", err)
	}
	if wire["prompt"] != "def add(a,b):" {
		t.Errorf("prompt = %v", wire["prompt"])
	}
	if wire["suffix"] != "return c" {
		t.Errorf("suffix = %v（FIM 的 suffix 必须发出）", wire["suffix"])
	}

	// 无 suffix：普通续写，不该发一个空串（部分兼容上游会把它当成"有 suffix 但不是 FIM"）。
	req2 := &ir.Request{Model: "m", Completion: &ir.Completion{Prompt: "x = "}}
	body2, _, err := Adapter().EncodeRequest(req2, ir.EncodeOptions{UpstreamModel: "deepseek-flash"})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if strings.Contains(string(body2), `"suffix"`) {
		t.Errorf("空 suffix 不应出现在请求体；body=%s", body2)
	}
}

// TestFIMResponseRoundtrip：非流式响应走 choices[].text，且 usage 三态保持。
func TestFIMResponseRoundtrip(t *testing.T) {
	body := `{"id":"cmpl-1","object":"text_completion","created":1,"model":"deepseek-flash",
		"choices":[{"index":0,"text":"    return a+b","finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	resp, err := Adapter().DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if len(resp.Parts) != 1 {
		t.Fatalf("parts = %d，期望 1（choices[].text 应包成 TextPart）", len(resp.Parts))
	}
	if tp, ok := resp.Parts[0].(ir.TextPart); !ok || tp.Text != "    return a+b" {
		t.Fatalf("正文 = %+v", resp.Parts[0])
	}
	if resp.Usage.InputTokens == nil || *resp.Usage.InputTokens != 10 {
		t.Errorf("in_tokens 未解析")
	}

	out, err := Adapter().EncodeResponse(resp)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var wire struct {
		Object  string `json:"object"`
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("编出的响应不是合法 JSON: %v", err)
	}
	if wire.Object != "text_completion" {
		t.Errorf("object = %q，期望 text_completion", wire.Object)
	}
	if wire.Choices[0].Text != "    return a+b" {
		t.Errorf("text = %q", wire.Choices[0].Text)
	}
}

// TestFIMFinishReasonMapping：官方两个补全专属结束原因必须映射成 StopError。
//
// insufficient_system_resource / aborted 不是"模型说完了"：把它们伪装成 end_turn
// 会让审计与计量看不到"这次是上游中断"，重试决策也会判错。
func TestFIMFinishReasonMapping(t *testing.T) {
	cases := map[string]ir.StopReason{
		"stop":                         ir.StopEndTurn,
		"length":                       ir.StopMaxTokens,
		"content_filter":               ir.StopContentFilter,
		"insufficient_system_resource": ir.StopError,
		"aborted":                      ir.StopError,
	}
	for in, want := range cases {
		if got := finishToStop(in); got != want {
			t.Errorf("finishToStop(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestFIMStreamDecode：补全 SSE 帧（choices[].text）必须解成 text_delta。
func TestFIMStreamDecode(t *testing.T) {
	sse := "data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"    \"}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"return a+b\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"\",\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	rd := Adapter().NewStreamReader(strings.NewReader(sse), ir.DecodeOptions{})
	var text strings.Builder
	var sawDone bool
	for {
		ev, err := rd.Next()
		if err != nil {
			break
		}
		switch ev.Type {
		case ir.EvTextDelta:
			text.WriteString(ev.Text)
		case ir.EvDone:
			sawDone = true
			if ev.Stop != ir.StopEndTurn {
				t.Errorf("done.Stop = %q，期望 end_turn", ev.Stop)
			}
		}
	}
	if text.String() != "    return a+b" {
		t.Errorf("正文增量 = %q", text.String())
	}
	if !sawDone {
		t.Errorf("没有收到 done 事件")
	}
}

// TestFIMStreamEncode：IR text_delta 必须编成 choices[].text 帧，且以 [DONE] 收尾。
func TestFIMStreamEncode(t *testing.T) {
	var out strings.Builder
	w := Adapter().NewStreamWriter(&out, ir.EncodeOptions{UpstreamModel: "deepseek-flash", CreatedAt: 1})
	for _, ev := range []ir.Event{
		{Type: ir.EvStart},
		{Type: ir.EvTextDelta, Text: "return"},
		{Type: ir.EvTextDelta, Text: " a+b"},
		{Type: ir.EvDone, Stop: ir.StopEndTurn},
	} {
		if err := w.Write(ev); err != nil {
			t.Fatalf("写入 %s 失败: %v", ev.Type, err)
		}
	}
	_ = w.Close()
	body := out.String()
	if !strings.Contains(body, `"text":"return"`) {
		t.Errorf("增量未编进 choices[].text；SSE=\n%s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("缺少 [DONE] 终止帧；SSE=\n%s", body)
	}
}

var _ ir.Adapter = adapter{}
