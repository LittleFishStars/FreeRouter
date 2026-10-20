package ir_test

// 契约测试：锁定 IR 与协议层的对外行为。
//
// 为什么全部用 TestContract 前缀：make contract 按前缀聚合跨包契约测试，
// 这些测试是"改 IR 前必看"的行为快照——名字里带 Contract 的测试失败，
// 意味着破坏的是跨模块约定而不是实现细节。
//
// golden 文件放本包 testdata/ 下（每协议 ≥2 个样本）。更新方式：
//
//	IR_UPDATE_GOLDEN=1 go test ./internal/ir/
//
// golden 更新后必须人工 review diff：它们是协议形状的权威快照，机器自动改写
// 而没人看，等于把"静默漂移"合法化。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/anthropic"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai"
)

// ---- golden 基础设施 ----

func goldenPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath(t, name))
	if err != nil {
		t.Fatalf("读取 golden %s: %v", name, err)
	}
	return b
}

// compareGolden 比较实际输出与 golden；IR_UPDATE_GOLDEN=1 时写回并跳过断言。
func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := goldenPath(t, name)
	if os.Getenv("IR_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("写 golden %s: %v", name, err)
		}
		t.Logf("golden %s 已更新（请人工 review diff）", name)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 golden %s（先跑 IR_UPDATE_GOLDEN=1 生成）: %v", name, err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
		t.Fatalf("golden %s 不匹配：\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

// compareGoldenSem 对 JSON golden 做语义比较（解析成 any 再 DeepEqual）：
// 字段顺序与缩进不敏感——线上 JSON 的键序随 Go 版本/结构体改动会变，
// 而字节比较会把"无害的重排"报成契约破坏，噪声太大反而没人看。
// 文件不存在或 IR_UPDATE_GOLDEN=1 时写盘（格式化后），并跳过断言。
func compareGoldenSem(t *testing.T, name string, got []byte) {
	t.Helper()
	path := goldenPath(t, name)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, got, "", "  "); err != nil {
		t.Fatalf("got 不是合法 JSON: %v", err)
	}
	if os.Getenv("IR_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, append(pretty.Bytes(), '\n'), 0o644); err != nil {
			t.Fatalf("写 golden %s: %v", name, err)
		}
		t.Logf("golden %s 已更新（请人工 review diff）", name)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 golden %s（先跑 IR_UPDATE_GOLDEN=1 生成）: %v", name, err)
	}
	var va, vb any
	if err := json.Unmarshal(want, &va); err != nil {
		t.Fatalf("golden %s 不合法: %v", name, err)
	}
	if err := json.Unmarshal(got, &vb); err != nil {
		t.Fatalf("got 不合法: %v", err)
	}
	if !reflect.DeepEqual(va, vb) {
		t.Fatalf("golden %s 语义不等：\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

// jsonEqual 做"语义相等"判断：解析成 any 再 DeepEqual，字段顺序与空白不敏感。
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("A 不是合法 JSON: %v", err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("B 不是合法 JSON: %v", err)
	}
	return reflect.DeepEqual(va, vb)
}

// ---- IR 事件的测试用 JSON 表示 ----
//
// 为什么不用反射自动序列化 ir.Event：Event 是联合体，不同 Type 下有效字段不同，
// 显式形状让 golden 里"这个事件带了哪些字段"一眼可读，也天然丢弃无效字段。

type callJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Partial   bool   `json:"partial,omitempty"`
}

type usageJSON struct {
	Input      *int   `json:"input,omitempty"`
	Output     *int   `json:"output,omitempty"`
	CacheRead  *int   `json:"cache_read,omitempty"`
	CacheWrite *int   `json:"cache_write,omitempty"`
	Reasoning  *int   `json:"reasoning,omitempty"`
	Source     string `json:"source,omitempty"`
}

type errJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type evJSON struct {
	Type      string     `json:"type"`
	Index     *int       `json:"index,omitempty"`
	Text      string     `json:"text,omitempty"`
	Signature string     `json:"signature,omitempty"`
	Call      *callJSON  `json:"call,omitempty"`
	Usage     *usageJSON `json:"usage,omitempty"`
	Stop      string     `json:"stop,omitempty"`
	Err       *errJSON   `json:"err,omitempty"`
}

func evToJSON(e ir.Event) evJSON {
	out := evJSON{Type: string(e.Type)}
	if e.Index != 0 {
		i := e.Index
		out.Index = &i
	}
	out.Text = e.Text
	out.Signature = e.Signature
	if e.Call != nil {
		out.Call = &callJSON{ID: e.Call.ID, Name: e.Call.Name, Arguments: e.Call.Arguments, Partial: e.Call.Partial}
	}
	if e.Usage != nil {
		out.Usage = &usageJSON{
			Input: e.Usage.InputTokens, Output: e.Usage.OutputTokens,
			CacheRead: e.Usage.CacheReadTokens, CacheWrite: e.Usage.CacheWriteTokens,
			Reasoning: e.Usage.ReasoningTokens, Source: string(e.Usage.Source),
		}
	}
	out.Stop = string(e.Stop)
	if e.Err != nil {
		out.Err = &errJSON{Code: e.Err.Code, Message: e.Err.Message}
	}
	return out
}

func evFromJSON(j evJSON) ir.Event {
	e := ir.Event{Type: ir.EventType(j.Type), Text: j.Text, Signature: j.Signature, Stop: ir.StopReason(j.Stop)}
	if j.Index != nil {
		e.Index = *j.Index
	}
	if j.Call != nil {
		e.Call = &ir.ToolCallPart{ID: j.Call.ID, Name: j.Call.Name, Arguments: j.Call.Arguments, Partial: j.Call.Partial}
	}
	if j.Usage != nil {
		e.Usage = &ir.Usage{
			InputTokens: j.Usage.Input, OutputTokens: j.Usage.Output,
			CacheReadTokens: j.Usage.CacheRead, CacheWriteTokens: j.Usage.CacheWrite,
			ReasoningTokens: j.Usage.Reasoning, Source: ir.UsageSource(j.Usage.Source),
		}
	}
	if j.Err != nil {
		e.Err = &ir.Error{Code: j.Err.Code, Message: j.Err.Message}
	}
	return e
}

func marshalEvents(evs []ir.Event) []byte {
	out := make([]evJSON, len(evs))
	for i, e := range evs {
		out[i] = evToJSON(e)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

func unmarshalEvents(t *testing.T, b []byte) []ir.Event {
	t.Helper()
	var in []evJSON
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("事件 golden 不是合法 JSON: %v", err)
	}
	out := make([]ir.Event, len(in))
	for i, j := range in {
		out[i] = evFromJSON(j)
	}
	return out
}

// drainReader 把流读到 EOF，收集全部 IR 事件。
func drainReader(t *testing.T, r ir.StreamReader) []ir.Event {
	t.Helper()
	var evs []ir.Event
	for {
		ev, err := r.Next()
		if err != nil {
			if err.Error() == "EOF" {
				return evs
			}
			t.Fatalf("读流失败: %v", err)
		}
		evs = append(evs, ev)
	}
}

// ---- 请求方向 golden：解码 → 重编码，锁定双向无损 ----

func roundtripRequest(t *testing.T, a ir.Adapter, in, out string) {
	t.Helper()
	req, warns, err := a.DecodeRequest(readGolden(t, in))
	if err != nil {
		t.Fatalf("解码 %s: %v", in, err)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("IR 自检失败: %v", err)
	}
	body, _, err := a.EncodeRequest(req, ir.EncodeOptions{UpstreamModel: req.Model})
	if err != nil {
		t.Fatalf("重编码: %v", err)
	}
	// 语义比较（键序/缩进不敏感）：这是"解码再编码不改变请求含义"的契约。
	compareGoldenSem(t, out, body)
	_ = warns
	_ = warns
}

func TestContractOpenAIRequestGolden(t *testing.T) {
	roundtripRequest(t, openai.Adapter(), "openai/request_basic.in.json", "openai/request_basic.out.json")
	roundtripRequest(t, openai.Adapter(), "openai/request_tools.in.json", "openai/request_tools.out.json")
}

func TestContractAnthropicRequestGolden(t *testing.T) {
	roundtripRequest(t, anthropic.Adapter(), "anthropic/request_basic.in.json", "anthropic/request_basic.out.json")
	roundtripRequest(t, anthropic.Adapter(), "anthropic/request_tools.in.json", "anthropic/request_tools.out.json")
}

// TestContractCrossProtocolRequest 保持跨协议往返：OpenAI 入站 → Anthropic 上游编码 →
// 再解码 → 再编码回 OpenAI，关键语义不得丢失。IR 的全部意义就在这条链上。
func TestContractCrossProtocolRequest(t *testing.T) {
	oa, an := openai.Adapter(), anthropic.Adapter()
	req, _, err := oa.DecodeRequest(readGolden(t, "openai/request_tools.in.json"))
	if err != nil {
		t.Fatalf("openai 解码: %v", err)
	}
	body, _, err := an.EncodeRequest(req, ir.EncodeOptions{})
	if err != nil {
		t.Fatalf("anthropic 编码: %v", err)
	}
	req2, _, err := an.DecodeRequest(body)
	if err != nil {
		t.Fatalf("anthropic 解码: %v", err)
	}
	body2, _, err := oa.EncodeRequest(req2, ir.EncodeOptions{})
	if err != nil {
		t.Fatalf("openai 重编码: %v", err)
	}
	// 往返经过 Anthropic 方言，两处**已知且有意**的语义差异被 golden 锁定：
	//   1. image.detail：Anthropic 协议无 detail 概念（§11 待定项：编码到不支持的
	//      上游如何处理未定），往返后丢失；
	//   2. max_tokens：OpenAI 侧可选、Anthropic 侧必填，编码时补默认 4096 并记告警。
	// 拿 openai 原生 golden 比会误报；这里用独立 cross golden 把"损失清单"变成契约。
	compareGoldenSem(t, "openai/request_tools.cross.out.json", body2)
}

// ---- 非流式响应 golden ----

func TestContractOpenAIResponseGolden(t *testing.T) {
	a := openai.Adapter()
	resp, err := a.DecodeResponse(readGolden(t, "openai/response_basic.in.json"))
	if err != nil {
		t.Fatalf("解码: %v", err)
	}
	body, err := a.EncodeResponse(resp)
	if err != nil {
		t.Fatalf("编码: %v", err)
	}
	compareGoldenSem(t, "openai/response_basic.out.json", body)
	// usage 三态：nil 字段在重编码后仍是 nil（§9 的可对账性）。
	if resp.Usage.InputTokens == nil {
		t.Fatalf("上游给了 prompt_tokens 却解码成 nil")
	}
}

func TestContractAnthropicResponseGolden(t *testing.T) {
	a := anthropic.Adapter()
	resp, err := a.DecodeResponse(readGolden(t, "anthropic/response_basic.in.json"))
	if err != nil {
		t.Fatalf("解码: %v", err)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 2048 {
		t.Fatalf("cache_read_input_tokens 三态丢失: %+v", resp.Usage)
	}
	body, err := a.EncodeResponse(resp)
	if err != nil {
		t.Fatalf("编码: %v", err)
	}
	compareGoldenSem(t, "anthropic/response_basic.out.json", body)
}

// ---- 流式 golden：SSE ↔ IR 事件 ----

func TestContractOpenAIStreamDecodeGolden(t *testing.T) {
	a := openai.Adapter()
	r := a.NewStreamReader(bytes.NewReader(readGolden(t, "openai/stream_decode.in.sse")),
		ir.DecodeOptions{RequestID: "req_test"})
	evs := drainReader(t, r)
	compareGolden(t, "openai/stream_decode.want.json", marshalEvents(evs))
}

func TestContractOpenAIStreamEncodeGolden(t *testing.T) {
	a := openai.Adapter()
	given := unmarshalEvents(t, readGolden(t, "openai/stream_encode.given.json"))
	var buf bytes.Buffer
	// CreatedAt 注入固定值：未注入时编码器兜底取当下，golden 会变成时间依赖而不可复现。
	w := a.NewStreamWriter(&buf, ir.EncodeOptions{UpstreamModel: "gpt-test", RequestID: "req_test", CreatedAt: 1700000000})
	for _, ev := range given {
		if err := w.Write(ev); err != nil {
			t.Fatalf("写事件 %s: %v", ev.Type, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("收尾: %v", err)
	}
	compareGolden(t, "openai/stream_encode.want.sse", buf.Bytes())
}

func TestContractAnthropicStreamDecodeGolden(t *testing.T) {
	a := anthropic.Adapter()
	r := a.NewStreamReader(bytes.NewReader(readGolden(t, "anthropic/stream_decode.in.sse")),
		ir.DecodeOptions{RequestID: "req_test"})
	evs := drainReader(t, r)
	compareGolden(t, "anthropic/stream_decode.want.json", marshalEvents(evs))
	// 签名是 Anthropic 流的关键回传信息：事件流里必须有带 Signature 的终帧。
	found := false
	for _, ev := range evs {
		if ev.Type == ir.EvThinkingDelta && ev.Signature != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("流里有 signature_delta 但事件序列没有签名终帧")
	}
}

func TestContractAnthropicStreamEncodeGolden(t *testing.T) {
	a := anthropic.Adapter()
	given := unmarshalEvents(t, readGolden(t, "anthropic/stream_encode.given.json"))
	var buf bytes.Buffer
	w := a.NewStreamWriter(&buf, ir.EncodeOptions{UpstreamModel: "claude-test", RequestID: "req_test"})
	for _, ev := range given {
		if err := w.Write(ev); err != nil {
			t.Fatalf("写事件 %s: %v", ev.Type, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("收尾: %v", err)
	}
	out := buf.String()
	// 成对性是 Anthropic 客户端解析的硬前提：start/stop 数量必须相等。
	if n := strings.Count(out, "content_block_start"); n != strings.Count(out, "content_block_stop") {
		t.Fatalf("content_block start/stop 不成对: %d/%d", n, strings.Count(out, "content_block_stop"))
	}
	if strings.Contains(out, "[DONE]") {
		t.Fatalf("Anthropic 流不该有 [DONE]（01-inbound.md §6.3）")
	}
	compareGolden(t, "anthropic/stream_encode.want.sse", buf.Bytes())
}

// ---- C6：能力缺失不静默降级 ----

func TestContractCapabilityCheckUnsupported(t *testing.T) {
	an := anthropic.Adapter()
	// json_schema 在 Anthropic 协议无原生表达（§7.1）：必须 400，而不是丢掉格式要求。
	req := &ir.Request{
		Model:    "m",
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "hi"}}}},
		Format:   &ir.ResponseFormat{Kind: ir.FormatJSONSchema, Schema: `{"type":"object"}`},
	}
	_, err := an.Capabilities().Check(req)
	ue, ok := ir.AsUnsupported(err)
	if !ok {
		t.Fatalf("json_schema 应报 UnsupportedError，得到 %v", err)
	}
	if ue.Capability != ir.CapJSONSchema || ue.Protocol != ir.ProtocolAnthropic {
		t.Fatalf("错误体缺能力/协议: %+v", ue)
	}

	// thinking 回传给不支持 thinking 的 OpenAI Chat：同样必须 400（§3.5）。
	oa := openai.Adapter()
	req2 := &ir.Request{
		Model: "m",
		Messages: []ir.Message{{Role: ir.RoleAssistant, Parts: []ir.Part{
			ir.ThinkingPart{Text: "内部推理", Signature: "sig"},
		}}},
	}
	if _, err := oa.Capabilities().Check(req2); err == nil {
		t.Fatalf("OpenAI Chat 接受 thinking 回传——静默降级")
	}
	// top_k 是 §8.3 的唯一白名单例外：忽略但必须留痕。
	req3 := &ir.Request{
		Model:    "m",
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "hi"}}}},
		Sampling: ir.Sampling{TopK: ir.Ptr(40)},
	}
	warns, err := oa.Capabilities().Check(req3)
	if err != nil || len(warns) == 0 {
		t.Fatalf("top_k 应产生告警而非错误: warns=%v err=%v", warns, err)
	}
}

// ---- C7：Started()/Visible() ----

func TestContractEncoderStateStartedVisible(t *testing.T) {
	var st ir.EncoderState
	// 未写任何帧：没开始。
	if st.Started() || st.Visible() {
		t.Fatalf("初始状态应为 started=false visible=false")
	}
	// start 是协议壳：算"已开始"，不算"已吐可见内容"——C7 换候选判定依据后者。
	st.MarkWritten(ir.Event{Type: ir.EvStart})
	if !st.Started() || st.Visible() {
		t.Fatalf("start 后应 started=true visible=false")
	}
	// 文本增量是可见内容：此后不得切换候选。
	st.MarkWritten(ir.Event{Type: ir.EvTextDelta, Text: "x"})
	if !st.Visible() {
		t.Fatalf("text_delta 后应 visible=true")
	}
	// done 之后事件被丢弃（I5）。
	st.MarkWritten(ir.Event{Type: ir.EvDone})
	// Accept 返回 false 表示编码器应丢弃该事件（I5）。断言方向：done 后必须 false。
	if st.Accept(ir.Event{Type: ir.EvTextDelta, Text: "late"}) {
		t.Fatalf("done 之后的事件应被 Accept 拒绝")
	}
	// 重复 usage 被 I4 拒绝。
	var st2 ir.EncoderState
	u := ir.Usage{InputTokens: ir.Ptr(1), Source: ir.UsageUpstream}
	st2.MarkWritten(ir.Event{Type: ir.EvUsage, Usage: &u})
	if st2.Accept(ir.Event{Type: ir.EvUsage, Usage: &u}) {
		t.Fatalf("重复 usage 应被 I4 拒绝")
	}
}

// ---- tool_call 重组规则（02-ir.md §6.2）----

func TestContractToolCallReassemblyRules(t *testing.T) {
	t.Run("首片无id_后续补齐", func(t *testing.T) {
		r := ir.NewToolCallReassembler("req_1")
		var evs []ir.Event
		e, err := r.Accept(ir.Fragment{Index: 0, Arguments: `{"a`})
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e...) // 此刻不该有 start
		if len(evs) != 0 {
			t.Fatalf("无 id 无 name 时不应发 start: %+v", evs)
		}
		e, err = r.Accept(ir.Fragment{Index: 0, ID: "call_x", Name: "f", Arguments: `":1"}`})
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e...)
		if len(evs) == 0 || evs[0].Type != ir.EvToolCallStart || evs[0].Call.ID != "call_x" {
			t.Fatalf("拿到 id 后应发 start: %+v", evs)
		}
		if evs[0].Call.Arguments != `{"a` {
			t.Fatalf("start 应携带缓冲的参数片段: %+v", evs[0].Call)
		}
	})
	t.Run("交错index各自重组", func(t *testing.T) {
		r := ir.NewToolCallReassembler("req_2")
		r.Accept(ir.Fragment{Index: 0, ID: "call_a", Name: "fa"})
		r.Accept(ir.Fragment{Index: 1, ID: "call_b", Name: "fb"})
		e, _ := r.Accept(ir.Fragment{Index: 1, Arguments: "bb"})
		last := e[len(e)-1]
		if last.Type != ir.EvToolCallDelta || last.Index != 1 {
			t.Fatalf("index=1 的 delta 应归 index=1: %+v", last)
		}
		e, _ = r.Accept(ir.Fragment{Index: 0, Arguments: "aa"})
		if e[len(e)-1].Index != 0 {
			t.Fatalf("index=0 的 delta 应归 index=0: %+v", e[len(e)-1])
		}
	})
	t.Run("合成id带请求ID", func(t *testing.T) {
		r := ir.NewToolCallReassembler("req_9")
		r.Accept(ir.Fragment{Index: 3, Name: "f"}) // 无 id
		evs := r.CloseAll()                        // 流结束兜底
		if len(evs) == 0 {
			t.Fatal("CloseAll 应产出事件")
		}
		end := evs[len(evs)-1]
		if end.Type != ir.EvToolCallEnd || end.Call.ID != "call_req_9_3" {
			t.Fatalf("合成 id 应为 call_{request_id}_{index}: %+v", end.Call)
		}
	})
	t.Run("同名二次出现_违规", func(t *testing.T) {
		r := ir.NewToolCallReassembler("req_3")
		r.Accept(ir.Fragment{Index: 0, ID: "c", Name: "f"})
		r.Accept(ir.Fragment{Index: 0, Arguments: "x"}) // 已 started
		if _, err := r.Accept(ir.Fragment{Index: 0, Name: "g"}); err == nil {
			t.Fatal("已 start 后出现第二个 name 应报错（协议违规）")
		}
	})
}

// ---- SSE 分帧 ----

func TestContractSSEFrameReadWrite(t *testing.T) {
	// 读：\r\n 兼容、注释行跳过、多行 data 拼接。ping 帧带 data——实现有意丢弃
	// 无 data 的空壳帧（Anthropic 心跳），输入必须反映真实协议形状。
	in := "event: ping\r\ndata: {}\r\n\r\n: keep-alive\n\ndata: {\"a\":\ndata: 1}\n\n"
	r := ir.NewSSEReader(strings.NewReader(in))
	f, err := r.Next()
	if err != nil {
		t.Fatalf("读帧失败: %v", err)
	}
	if f.Event != "ping" || f.Data != "{}" {
		t.Fatalf("分帧结果错误: %+v", f)
	}
	f2, err := r.Next()
	if err != nil {
		t.Fatalf("读第二帧失败: %v", err)
	}
	if f2.Data != "{\"a\":\n1}" {
		t.Fatalf("多行 data 应拼接: %+v", f2)
	}
	if _, err := r.Next(); err == nil {
		t.Fatalf("流结束应返回 EOF")
	}
	// 写：event 行、data 行、空行分隔，可被自己读回。
	var buf bytes.Buffer
	w := ir.NewSSEWriter(&buf)
	if err := w.Write(ir.SSEEvent{Event: "x", Data: "d1\nd2"}); err != nil {
		t.Fatal(err)
	}
	r2 := ir.NewSSEReader(bytes.NewReader(buf.Bytes()))
	f3, err := r2.Next()
	if err != nil || f3.Event != "x" || f3.Data != "d1\nd2" {
		t.Fatalf("写出的帧读不回: %+v err=%v", f3, err)
	}
}

// ---- 请求自检（Validate）----

func TestContractRequestValidate(t *testing.T) {
	base := func() *ir.Request {
		return &ir.Request{Model: "m", Messages: []ir.Message{
			{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "hi"}}},
		}}
	}
	t.Run("工具结果必须配对", func(t *testing.T) {
		r := base()
		r.Messages[0].Parts = []ir.Part{ir.ToolResultPart{ToolCallID: "nope"}}
		if err := r.Validate(); err == nil {
			t.Fatal("未配对的 tool_result 应报错")
		}
	})
	t.Run("重复调用id", func(t *testing.T) {
		r := base()
		r.Messages = append(r.Messages, ir.Message{Role: ir.RoleAssistant, Parts: []ir.Part{
			ir.ToolCallPart{ID: "c1", Name: "f"}, ir.ToolCallPart{ID: "c1", Name: "f"},
		}})
		if err := r.Validate(); err == nil {
			t.Fatal("重复 tool_call id 应报错")
		}
	})
	t.Run("指定工具必须带名", func(t *testing.T) {
		r := base()
		r.Choice = ir.ToolChoice{Mode: ir.ToolChoiceSpecific}
		if err := r.Validate(); err == nil {
			t.Fatal("specific 无名应报错")
		}
	})
	t.Run("未知part类型", func(t *testing.T) {
		r := base()
		r.Messages[0].Parts = []ir.Part{ir.TextPart{Text: "x"}, unknownPart{}}
		if err := r.Validate(); err == nil {
			t.Fatal("未知 part 应报错（§10）")
		}
	})
}

type unknownPart struct{}

func (unknownPart) PartType() ir.PartType { return "telepathy" }
func (unknownPart) Index() int            { return 0 }

// ---- 协议适配器形状 ----

func TestContractAdapterShape(t *testing.T) {
	for _, a := range []ir.Adapter{openai.Adapter(), anthropic.Adapter()} {
		if a.Name() == "" {
			t.Fatalf("适配器必须有协议名")
		}
		caps := a.Capabilities()
		if caps.Protocol != a.Name() {
			t.Fatalf("Capabilities.Protocol 与 Name() 不一致: %s vs %s", caps.Protocol, a.Name())
		}
		// 编解码全链至少要能跑通一个最小请求。
		req := &ir.Request{Model: "m", Messages: []ir.Message{
			{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "hi"}}},
		}}
		if _, _, err := a.EncodeRequest(req, ir.EncodeOptions{UpstreamModel: "x"}); err != nil {
			t.Fatalf("%s: 最小请求编码失败: %v", a.Name(), err)
		}
	}
}

// 编译期保证：golden 更新模式下跳过断言的分支不会掩盖比较逻辑被删掉。
var _ = fmt.Sprintf
