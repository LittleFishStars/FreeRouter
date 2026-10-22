// relay 流式帧 → IR 事件（task-3 第 1 点：relay 的 infer.stream 帧转换落地）。
//
// 边界（任务书原话）：**C7「已吐可见内容后不切候选」由上层状态机判定，本文件只负责
// 帧转换**。因此这里不持有 RetryController、不调 Pool.Health、不做候选决策，
// 只回答三个问题：
//  1. sidecar 的这条帧对应哪个 ir.Event；
//  2. 这条事件是否属于「可见内容」（ir.Event.Visible）；
//  3. 流的统计口径（BytesIn / EventsOut / TTFT / VisibleWritten）是什么。
//
// 为什么把帧格式与映射放在 upstream 而不是 dshprovider：dshprovider 只讲「怎么跟
// 子进程说话」（NDJSON、id、超时、进程死亡），它不该认识 ir。帧的**语义**
// （start/delta/usage/done/error）属于上游适配层——放在这里，dshprovider 的契约
// 测试可以继续不依赖 ir。
//
// 线上帧形状（docs/ops/03-milestones.md §2「invoke 流式 → 事件帧形状
// start/delta/usage/done」的逐字落地；error 是 §2.4 错误码在流内的表达）：
//
//	{"event":"start"}
//	{"event":"delta","kind":"text","index":0,"text":"你好"}
//	{"event":"delta","kind":"thinking","index":0,"text":"想一想","signature":""}
//	{"event":"delta","kind":"tool_call_start","index":1,"call":{"id":"call_1","name":"read_file"}}
//	{"event":"delta","kind":"tool_call_delta","index":1,"call":{"arguments":"{\"pa"}}
//	{"event":"delta","kind":"tool_call_end","index":1,"call":{"id":"call_1","arguments":"{\"path\":\"/tmp\"}"}}
//	{"event":"usage","usage":{"inputTokens":120,"outputTokens":30}}
//	{"event":"done","stop":"end_turn"}
//	{"event":"error","error":{"code":"E_UPSTREAM_HTTP","message":"上游 502","status":502}}
//
// 宽容度（04-sidecar.md §3「方法只加不改」「未知返回字段必须被忽略」的同一精神）：
//   - `event` 与 `type` 同义；两者都缺且没有任何已知字段时按心跳跳过；
//   - `text` 与 `content` 同义；delta 的 kind 接受 text_delta / thinking_delta /
//     tool_start / tool_delta / tool_end 等别名；
//   - usage 同时接受 camelCase、snake_case 与 OpenAI 的 prompt_tokens/completion_tokens；
//   - 一行可以是裸帧，也可以是 IPC 封装帧 {"id":1,"ok":true,"result":{…}}；
//     ok:false 的封装帧按 error 帧处理（与 dshprovider 的错误语义一致）；
//   - 整个 payload 可以是 NDJSON、JSON 数组、`{"frames":[…]} `或 JSON 字符串。
//
// 流的四条不变式（02-ir.md §5.2）在这里兜底：
//
//	I1 start 必须第一个且只一次 —— 首帧不是 start 时合成；重复 start 报协议错。
//	I4 usage 至多一次且必在 done 前 —— 缓冲至 done（多次时取最后一次）。
//	I5 done 之后不再有事件 —— Recv 在 done 后返回 io.EOF。
//	I6 error 之后只允许 done —— error 之后补一个 Stop=error 的 done 再 EOF。
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// relayFrameMaxBytes 是单帧上限。与 dshprovider.readBufSize 同量级：
// 一条帧不该接近 1 MiB，越过它说明对方在灌没有换行的垃圾，必须让流以协议错结束，
// 而不是把它整个吃进内存。
const relayFrameMaxBytes = 1 << 20

// RelayStream 把 sidecar 的流式帧解成 ir.Event，并实现 upstream.Stream。
//
// 为什么实现 Stream 而不是只提供「切片 → []ir.Event」的纯函数：调用方（call 模块）
// 需要在收到 usage/done 之前就能把 delta 立刻转发给客户端——一次性转换会强制等到
// 流结束，把流式退化成非流式。Recv 的拉取语义与 HTTP body 的读取语义一致，
// 也让「已吐内容」的判定发生在事件真正交付的那一刻。
type RelayStream struct {
	rc  io.ReadCloser
	br  *bufio.Reader
	t0  time.Time
	seq int

	// pending 是「已经解出但还没交付」的事件（合成 start、usage→done 的排序）。
	pending []ir.Event

	started  bool // 已交付/已排队过 start（I1）
	finished bool // 已交付/已排队 done（I5）
	failed   bool // 已交付 error，下一步只允许 done（I6）
	closed   bool

	// usage 缓冲到 done 才交付：I4 要求「上游给多次时取最后一次」，
	// 边到边发会让第一次的数字先被客户端看到，后面那个才是真的。
	usage        *ir.Usage
	usageEmitted bool

	ttftSet bool
	stats   StreamStats
}

// NewRelayStream 用一份流式应答载荷构造 RelayStream。rc 为 nil 时流立即以空结束
// （构造方应先判空；这里不 panic 是装配层的安全网）。
func NewRelayStream(rc io.ReadCloser) *RelayStream {
	s := &RelayStream{rc: rc, t0: time.Now(), stats: StreamStats{UpstreamStatus: 200}}
	if rc != nil {
		s.br = bufio.NewReaderSize(rc, 64<<10)
	}
	return s
}

// NewRelayStreamFromPayload 把 sidecar 返回的整段载荷（NDJSON / JSON 数组 /
// {"frames":[…]} / JSON 字符串）包成流。它存在的意义是契约测试与「一问一答
// 通道已经拿到全量载荷」的降级路径：两者都不需要拉起 Node 进程。
func NewRelayStreamFromPayload(payload []byte) *RelayStream {
	body := relayPayloadToNDJSON(payload)
	return NewRelayStream(io.NopCloser(bytes.NewReader(body)))
}

// relayPayloadToNDJSON 把几种可接受的载荷形态归一成「一行一帧」。
func relayPayloadToNDJSON(payload []byte) []byte {
	p := bytes.TrimSpace(payload)
	if len(p) == 0 {
		return nil
	}
	// JSON 数组：[{…},{…}]
	if p[0] == '[' {
		var frames []json.RawMessage
		if err := json.Unmarshal(p, &frames); err == nil {
			var buf bytes.Buffer
			for _, f := range frames {
				buf.Write(f)
				buf.WriteByte('\n')
			}
			return buf.Bytes()
		}
	}
	// {"frames":[…]}：sidecar 若把多帧收在一个 result 里，这是最省事的形状。
	var wrap struct {
		Frames []json.RawMessage `json:"frames"`
	}
	if err := json.Unmarshal(p, &wrap); err == nil && len(wrap.Frames) > 0 {
		var buf bytes.Buffer
		for _, f := range wrap.Frames {
			buf.Write(f)
			buf.WriteByte('\n')
		}
		return buf.Bytes()
	}
	// JSON 字符串："{…}\n{…}"
	if p[0] == '"' {
		var s string
		if err := json.Unmarshal(p, &s); err == nil {
			return []byte(s)
		}
	}
	return p
}

// relayFrame 是一帧的线上形状。字段全部可空——各 provider adapter 只发变化的部分，
// 强制齐全只会把「没给」变成错误。
type relayFrame struct {
	Event string `json:"event"`
	// Type 与 Event 同义。为什么要兼容两个名字：milestones 文档里的方法名是 invoke，
	// 而早期 sidecar 草稿用过 type；让读取方接受两者，写侧就不必为一个字改名。
	Type string `json:"type"`

	Index int `json:"index"`

	// Kind 是 delta 的子类型：text / thinking / tool_call_start / …（见 deltaToEvent）。
	Kind string `json:"kind"`

	// Text 与 Content 同义（OpenAI 的 delta.content 与内部命名的 text）。
	Text    string `json:"text"`
	Content string `json:"content"`

	// Signature 只在 thinking 的收尾帧上有意义（02-ir.md §5.1 的追加字段）。
	Signature string `json:"signature"`

	Call  *relayFrameCall  `json:"call"`
	Usage *relayFrameUsage `json:"usage"`

	// Stop 与 Reason 同义：前者是 IR 的命名，后者是 OpenAI finish_reason 的命名。
	Stop   string `json:"stop"`
	Reason string `json:"reason"`

	Err *relayFrameError `json:"error"`
}

type relayFrameCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// Partial 可空：缺省按 true 处理（流式工具调用在 end 之前都是分片）。
	Partial *bool `json:"partial"`
}

// relayFrameUsage 同时接受三种命名。
//
// 为什么允许别名而不是只认一种：usage 是最容易漂移的字段（OpenAI 用
// prompt_tokens/completion_tokens，hub 的 StreamChunk 用 inputTokens/outputTokens，
// 我们的 IR 用 input_tokens）。让读取方一次兼容，写侧升级就不必同步改 Go。
type relayFrameUsage struct {
	InputTokens       *int `json:"inputTokens"`
	InputTokensSnake  *int `json:"input_tokens"`
	PromptTokens      *int `json:"prompt_tokens"`
	OutputTokens      *int `json:"outputTokens"`
	OutputTokensSnake *int `json:"output_tokens"`
	CompletionTokens  *int `json:"completion_tokens"`

	CacheReadTokens       *int `json:"cacheReadTokens"`
	CacheReadTokensSnake  *int `json:"cache_read_tokens"`
	CacheWriteTokens      *int `json:"cacheWriteTokens"`
	CacheWriteTokensSnake *int `json:"cache_write_tokens"`
	ReasoningTokens       *int `json:"reasoningTokens"`
	ReasoningTokensSnake  *int `json:"reasoning_tokens"`

	Source string `json:"source"`
}

type relayFrameError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f relayFrame) eventName() string {
	name := strings.ToLower(strings.TrimSpace(f.Event))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(f.Type))
	}
	return name
}

func (f relayFrame) textOf() string {
	if f.Text != "" {
		return f.Text
	}
	return f.Content
}

func (f relayFrame) stopOf() string {
	if f.Stop != "" {
		return f.Stop
	}
	return f.Reason
}

// toIREvent 把一帧映射成 IR 事件。返回值 ok=false 表示「这帧是心跳/空 delta，
// 不产生事件但也不是错误」——上游的 keep-alive 心跳不该让客户端看到空帧。
func (f relayFrame) toIREvent() (ir.Event, bool, error) {
	switch f.eventName() {
	case "", "ping", "heartbeat", "keepalive":
		return ir.Event{}, false, nil

	case "start":
		return ir.Event{Type: ir.EvStart, Index: 0}, true, nil

	case "delta":
		return f.deltaToEvent()

	case "usage":
		if f.Usage == nil {
			return ir.Event{}, false, fmt.Errorf("usage 帧缺少 usage 对象")
		}
		return ir.Event{Type: ir.EvUsage, Usage: f.Usage.toIR()}, true, nil

	case "done", "end", "finish":
		return ir.Event{Type: ir.EvDone, Stop: relayStopReason(f.stopOf())}, true, nil

	case "error":
		return ir.Event{Type: ir.EvError, Err: f.toIRError()}, true, nil

	default:
		return ir.Event{}, false, fmt.Errorf("未知的 relay 流事件 %q", f.eventName())
	}
}

func (f relayFrame) deltaToEvent() (ir.Event, bool, error) {
	kind := normalizeDeltaKind(f.Kind)
	text := f.textOf()

	switch kind {
	case "text":
		if text == "" {
			// 空文本 delta 是心跳（OpenAI 的首帧 role-only 也会落到这里）。
			return ir.Event{}, false, nil
		}
		return ir.Event{Type: ir.EvTextDelta, Index: f.Index, Text: text}, true, nil

	case "thinking":
		if text == "" && f.Signature == "" {
			return ir.Event{}, false, nil
		}
		// Signature 与 Text 同帧：纯签名终帧（Text 空）按 02-ir.md §5.3 不算可见内容，
		// 判定在 ir.Event.Visible() 里，这里只负责搬运。
		return ir.Event{
			Type:      ir.EvThinkingDelta,
			Index:     f.Index,
			Text:      text,
			Signature: f.Signature,
		}, true, nil

	case "tool_call_start", "tool_call_delta", "tool_call_end":
		if f.Call == nil {
			return ir.Event{}, false, fmt.Errorf("%s 帧缺少 call 对象", kind)
		}
		partial := true
		if f.Call.Partial != nil {
			partial = *f.Call.Partial
		}
		evType := ir.EvToolCallDelta
		switch kind {
		case "tool_call_start":
			evType = ir.EvToolCallStart
		case "tool_call_end":
			evType = ir.EvToolCallEnd
		}
		return ir.Event{
			Type:  evType,
			Index: f.Index,
			Call: &ir.ToolCallPart{
				Idx:       f.Index,
				ID:        f.Call.ID,
				Name:      f.Call.Name,
				Arguments: f.Call.Arguments,
				Partial:   partial,
			},
		}, true, nil

	case "":
		// 未声明 kind 的兼容路径：有 call 按 tool_call_delta，有文本按 text，
		// 两者都空就是心跳。猜错方向的代价是客户端看到一个类型不对的事件，
		// 所以只在字段存在时才猜。
		if f.Call != nil {
			return relayFrame{Event: "delta", Kind: "tool_call_delta", Index: f.Index, Call: f.Call}.deltaToEvent()
		}
		if text != "" {
			return ir.Event{Type: ir.EvTextDelta, Index: f.Index, Text: text}, true, nil
		}
		return ir.Event{}, false, nil

	default:
		return ir.Event{}, false, fmt.Errorf("未知的 delta kind %q", f.Kind)
	}
}

func normalizeDeltaKind(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	switch k {
	case "text_delta", "content", "content_delta":
		return "text"
	case "thinking_delta", "reasoning", "reasoning_delta":
		return "thinking"
	case "tool_start", "toolcall_start", "tool_call_begin":
		return "tool_call_start"
	case "tool_delta", "toolcall_delta":
		return "tool_call_delta"
	case "tool_end", "toolcall_end", "tool_call_stop":
		return "tool_call_end"
	}
	return k
}

func (f relayFrame) toIRError() *ir.Error {
	e := f.Err
	out := &ir.Error{Code: "upstream_error", Message: f.textOf()}
	if e != nil {
		if e.Code != "" {
			out.Code = e.Code
		}
		if e.Message != "" {
			out.Message = e.Message
		}
	}
	if out.Message == "" {
		out.Message = "sidecar 返回错误帧（未附错误信息）"
	}
	return out
}

// relayStopReason 把各家的结束原因归一成 IR 的 StopReason。
//
// 为什么同时接受 IR 名（end_turn）与 OpenAI finish_reason（stop/length/tool_calls）：
// 线上帧由 Node sidecar 产出，它最容易拿到的是 adapter 的原始 finish_reason；
// 强迫它翻译一次、Go 再翻译一次只会多一处漂移点。
func relayStopReason(s string) ir.StopReason {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "end_turn", "endturn", "stop":
		return ir.StopEndTurn
	case "max_tokens", "max_output_tokens", "length":
		return ir.StopMaxTokens
	case "tool_use", "tool_calls", "function_call":
		return ir.StopToolUse
	case "stop_sequence":
		return ir.StopStopSequence
	case "content_filter", "safety":
		return ir.StopContentFilter
	case "refusal":
		return ir.StopRefusal
	case "error", "failed":
		return ir.StopError
	}
	return ir.StopUnspecified
}

func (u *relayFrameUsage) toIR() *ir.Usage {
	out := &ir.Usage{Source: ir.UsageUpstream}
	out.InputTokens = firstInt(u.InputTokens, u.InputTokensSnake, u.PromptTokens)
	out.OutputTokens = firstInt(u.OutputTokens, u.OutputTokensSnake, u.CompletionTokens)
	out.CacheReadTokens = firstInt(u.CacheReadTokens, u.CacheReadTokensSnake)
	out.CacheWriteTokens = firstInt(u.CacheWriteTokens, u.CacheWriteTokensSnake)
	out.ReasoningTokens = firstInt(u.ReasoningTokens, u.ReasoningTokensSnake)
	if strings.EqualFold(u.Source, string(ir.UsageEstimated)) {
		out.Source = ir.UsageEstimated
	}
	return out
}

func firstInt(vals ...*int) *int {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// parseRelayLine 把一行解析成帧，同时接受裸帧与 IPC 封装帧。
func parseRelayLine(line string) (relayFrame, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return relayFrame{}, false, nil
	}

	var probe struct {
		OK     *bool            `json:"ok"`
		Result json.RawMessage  `json:"result"`
		Error  *relayFrameError `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &probe); err != nil {
		return relayFrame{}, false, fmt.Errorf("relay 帧不是合法 JSON: %w", err)
	}

	// 封装帧：{"id":N,"ok":true,"result":{…}} / {"id":N,"ok":false,"error":{…}}。
	// 为什么支持它：dshprovider 的通道是封装帧；流式实现既可以在一行内带 result，
	// 也可以把整段 result 一次性返回（本次落地两种都接受）。
	if probe.OK != nil {
		if !*probe.OK {
			return relayFrame{Event: "error", Err: probe.Error}, true, nil
		}
		if len(probe.Result) == 0 || string(probe.Result) == "null" {
			return relayFrame{}, false, nil
		}
		return parseRelayFrameObject(probe.Result)
	}
	return parseRelayFrameObject([]byte(line))
}

func parseRelayFrameObject(raw []byte) (relayFrame, bool, error) {
	var f relayFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return relayFrame{}, false, fmt.Errorf("relay 帧不是预期形状: %w", err)
	}
	return f, true, nil
}

// Recv 实现 upstream.Stream。返回值语义与 HTTP body 的读取一致：
// io.EOF 表示上游正常结束；*Error 表示协议/连接错误。
func (s *RelayStream) Recv() (ir.Event, error) {
	if s == nil {
		return ir.Event{}, &Error{Class: ErrInternalBug, Message: "RelayStream 未初始化"}
	}
	for {
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			// error 可能是排队的（首帧即 error 时要先合成 start）；在这里置位，
			// 否则下一条上游帧会在 error 之后被照常交付，违反 I6。
			if ev.Type == ir.EvError {
				s.failed = true
			}
			return s.emit(ev), nil
		}
		if s.finished {
			return ir.Event{}, io.EOF
		}
		if s.failed {
			// I6：error 之后只允许 done。补一个 Stop=error 的 done，
			// 让客户端/编码器有一个确定的收尾（OpenAI 的 [DONE] / Anthropic 的 message_stop）。
			s.finished = true
			return s.emit(ir.Event{Type: ir.EvDone, Stop: ir.StopError}), nil
		}
		if s.br == nil {
			return ir.Event{}, &Error{Class: ErrProtocol, Message: "relay 流没有应答体"}
		}

		line, err := s.readFrameLine()
		if err == io.EOF {
			return s.finishOnEOF()
		}
		if err != nil {
			return ir.Event{}, err
		}

		f, ok, perr := parseRelayLine(line)
		if perr != nil {
			return ir.Event{}, &Error{Class: ErrProtocol, Message: "relay 流帧解析失败: " + perr.Error(), Cause: perr}
		}
		if !ok {
			continue
		}
		ev, ok, perr := f.toIREvent()
		if perr != nil {
			return ir.Event{}, &Error{Class: ErrProtocol, Message: perr.Error(), Cause: perr}
		}
		if !ok {
			continue
		}

		// I1：start 必须是第一个且只出现一次。
		if ev.Type == ir.EvStart {
			if s.started {
				return ir.Event{}, &Error{Class: ErrProtocol, Message: "relay 流出现重复 start"}
			}
			s.started = true
			return s.emit(ev), nil
		}
		if !s.started {
			// 上游没有显式 start：按 I1 由我们合成，当前帧排到 start 之后。
			s.started = true
			s.pending = append(s.pending, ev)
			return s.emit(ir.Event{Type: ir.EvStart}), nil
		}

		switch ev.Type {
		case ir.EvUsage:
			// I4：缓冲到 done，多次时取最后一次。
			s.usage = ev.Usage
			continue

		case ir.EvError:
			s.failed = true
			return s.emit(ev), nil

		case ir.EvDone:
			s.finished = true
			if s.usage != nil && !s.usageEmitted {
				s.usageEmitted = true
				s.pending = append(s.pending, ev)
				return s.emit(ir.Event{Type: ir.EvUsage, Usage: s.usage}), nil
			}
			return s.emit(ev), nil

		case ir.EvToolCallStart, ir.EvToolCallDelta, ir.EvToolCallEnd,
			ir.EvTextDelta, ir.EvThinkingDelta:
			return s.emit(ev), nil

		default:
			return ir.Event{}, &Error{Class: ErrProtocol, Message: fmt.Sprintf("relay 流出现未映射的事件类型 %q", ev.Type)}
		}
	}
}

// finishOnEOF 处理「上游没发 done 就断流」。
//
// 有内容时补 done（否则客户端永远等不到收尾，I2/I5 的同一取舍）；一个事件都没有时
// 报协议错——那说明 sidecar 根本没开始回答，不该被伪装成正常空响应。
func (s *RelayStream) finishOnEOF() (ir.Event, error) {
	if !s.started {
		return ir.Event{}, &Error{Class: ErrProtocol, Message: "relay 流在 start 之前结束（sidecar 未返回任何事件）"}
	}
	if s.usage != nil && !s.usageEmitted {
		s.usageEmitted = true
		s.finished = true
		s.pending = append(s.pending, ir.Event{Type: ir.EvDone})
		return s.emit(ir.Event{Type: ir.EvUsage, Usage: s.usage}), nil
	}
	s.finished = true
	return s.emit(ir.Event{Type: ir.EvDone}), nil
}

// readFrameLine 读一行；空行跳过由 Recv 的循环承担（这里只返回非空行）。
func (s *RelayStream) readFrameLine() (string, error) {
	for {
		line, err := s.br.ReadString('\n')
		if len(line) > 0 {
			s.stats.BytesIn += int64(len(line))
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if len(trimmed) > relayFrameMaxBytes {
				return "", &Error{Class: ErrProtocol, Message: fmt.Sprintf("relay 单帧超过 %d 字节上限", relayFrameMaxBytes)}
			}
			return trimmed, nil
		}
		if err != nil {
			return "", err
		}
	}
}

// emit 更新统计并交付事件。VisibleWritten 是 C7 的唯一输入（02-ir.md §5.3）。
func (s *RelayStream) emit(ev ir.Event) ir.Event {
	s.seq++
	s.stats.EventsOut++
	if !s.ttftSet {
		s.ttftSet = true
		s.stats.TTFT = time.Since(s.t0)
	}
	if ev.Visible() {
		s.stats.VisibleWritten = true
	}
	return ev
}

// Close 幂等地关闭底层应答体。
func (s *RelayStream) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if s.rc == nil {
		return nil
	}
	return s.rc.Close()
}

// Stats 返回统计快照（StreamStats 是值类型，直接拷贝）。
func (s *RelayStream) Stats() StreamStats {
	if s == nil {
		return StreamStats{}
	}
	return s.stats
}

// Open 是 relay 类上游的 IR 原生流式入口：经 sidecar infer.stream 拿到载荷后
// 直接产出 ir.Event。Caller（httpapi 形态的字节流消费面）仍走 InferStream，
// 两条入口共用同一份帧解析——协议知识只有一份。
func (t *RelayTransport) Open(ctx context.Context, tgt Target, body []byte, stream bool) (Stream, error) {
	raw, err := t.inferPayload(ctx, tgt, body, stream)
	if err != nil {
		return nil, err
	}
	return NewRelayStream(io.NopCloser(bytes.NewReader(relayPayloadToNDJSON(raw)))), nil
}

// OpenFromPayload 是 Open 的纯函数版：把已经拿到的载荷直接包成流，供装配层
// 「先取载荷再消费」与契约测试使用。
func OpenFromPayload(payload []byte) Stream {
	return NewRelayStreamFromPayload(payload)
}
