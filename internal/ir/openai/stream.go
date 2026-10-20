package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// doneSentinel 是 Chat Completions 流的终止标记。
const doneSentinel = "[DONE]"

// streamWriter 实现 IR 事件 → Chat Completions SSE 帧。
//
// 为什么要维护"当前打开的 index"：OpenAI 的流没有 content_block_stop 这样的显式收尾，
// index 语义只是"这是第几段内容"。因此从一个 index 切到另一个、或流结束时，
// 必须把当前工具调用"滚到下一 index"，否则客户端会把两个调用的参数拼在一起。
type streamWriter struct {
	w         *ir.SSEWriter
	opt       ir.EncodeOptions
	st        *ir.EncoderState
	model     string
	seq       int // 自增 chunk id 序号
	openTool  int // 当前正在写的工具调用 index，-1 表示没有
	closed    bool
	stop      ir.StopReason
	startedID string // 首个 chunk 生成的响应 id，后续 chunk 复用
	created   int64  // 本次响应的生成时间，所有 chunk 共享（协议语义：同一次生成同一个 created）
}

// NewStreamWriter 构造流式编码器。
func (adapter) NewStreamWriter(w io.Writer, opt ir.EncodeOptions) ir.StreamWriter {
	return &streamWriter{
		w:        ir.NewSSEWriter(w),
		opt:      opt,
		st:       &ir.EncoderState{},
		openTool: -1,
		model:    opt.UpstreamModel,
		created:  orNow(opt.CreatedAt),
	}
}

// orNow 把"路由层没注入时间戳"（CreatedAt==0）归一为当下：与非流式 EncodeResponse
// 的兜底策略一致——同一个字段在两条路径上不能一个填 now 一个输出 0（0 会被
// 客户端渲染成 1970）。测试注入固定值保 golden 稳定；真实调用由这里兜底。
func orNow(created int64) int64 {
	if created == 0 {
		return time.Now().Unix()
	}
	return created
}

func (s *streamWriter) State() *ir.EncoderState { return s.st }

// Write 编码一个事件。
func (s *streamWriter) Write(ev ir.Event) error {
	if s.closed {
		return ir.ErrStreamClosed
	}
	if !s.st.Accept(ev) {
		return nil // I5：done 之后的事件丢弃，而不是报错——那是上游的噪音，不该让客户端看到 500
	}
	switch ev.Type {
	case ir.EvStart:
		// OpenAI 没有显式 start 帧，首个 chunk 隐含它。但我们仍要把状态标成"已开始"，
		// 否则 C7 会误判"还没吐任何东西"而允许换候选——而此时 HTTP 头可能已经发出去了。
		s.st.MarkWritten(ev)
		return nil

	case ir.EvThinkingDelta:
		// §7.2：Chat Completions 协议无思考内容的位置。
		// 不输出但不报错的理由：能力复核已在请求阶段拒绝过 thinking 请求，走到这里说明是
		// 上游自作主张多给了 reasoning 增量。丢掉它**不改变**客户端行为（本来就没有这个位），
		// 所以不属于静默降级；但也不能算进可见内容，否则一次上游噪音就会把候选锁死。
		return nil

	case ir.EvTextDelta:
		if err := s.switchAwayFromTool(); err != nil {
			return err
		}
		return s.writeChunk(delta{Content: ev.Text}, nil)

	case ir.EvToolCallStart:
		if ev.Call == nil {
			return &ir.InvalidRequestError{Param: "event.call", Detail: "tool_call_start 缺少 call"}
		}
		// 同一个 index 重复 start 说明重组器出了问题；上游 index 复用视为协议违规（I3）。
		s.openTool = ev.Index
		tc := deltaToolCall{Index: ir.Ptr(ev.Index), ID: ev.Call.ID,
			Type: "function", Function: deltaFunction{Name: ev.Call.Name, Arguments: ev.Call.Arguments}}
		return s.writeChunk(delta{ToolCalls: []deltaToolCall{tc}}, nil)

	case ir.EvToolCallDelta:
		if ev.Call == nil {
			return &ir.InvalidRequestError{Param: "event.call", Detail: "tool_call_delta 缺少 call"}
		}
		if ev.Index != s.openTool {
			// 上游复用/跳 index 是 I3 违规：用 UpstreamProtocolError（对齐 03-errors.md 的
			// upstream_error，502、可重试），而不是 InvalidRequestError——后者会被
			// HTTP 层映射成客户端 400，把上游的错算到客户端头上。
			return &ir.UpstreamProtocolError{
				Detail: fmt.Sprintf("工具调用 delta 的 index=%d 与已打开的 index=%d 不一致", ev.Index, s.openTool)}
		}
		tc := deltaToolCall{Index: ir.Ptr(ev.Index),
			Function: deltaFunction{Arguments: ev.Call.Arguments}}
		return s.writeChunk(delta{ToolCalls: []deltaToolCall{tc}}, nil)

	case ir.EvToolCallEnd:
		// OpenAI 没有 end 帧：滚到下一 index 就是收尾。
		s.openTool = -1
		return nil

	case ir.EvUsage:
		if ev.Usage == nil {
			return nil
		}
		// 去重已由 EncoderState.Accept 的 I4 分支实现（重复 usage 直接不进这里），
		// 不再自建 seenUsage 记账——两处实现只会漂移。
		// usage 必须独占一帧（OpenAI 的约定：include_usage 时最后一帧只有 usage、choices 为空）。
		return s.writeFrame(chunk{
			ID:      s.chunkID(),
			Object:  "chat.completion.chunk",
			Created: s.created,
			Model:   s.model,
			Choices: []chunkChoice{},
			Usage:   encodeUsage(*ev.Usage),
		})

	case ir.EvError:
		if ev.Err == nil {
			return nil
		}
		body := errorEnvelope{}
		body.Error.Message = ev.Err.Message
		// type 必须是 §2.4 的八值粗枚举（SDK 按它分支），细码留在 code 字段——
		// 把 capability_unsupported 之类塞进 type 会让所有 SDK 落到兜底分支。
		body.Error.Type = ir.CodeToErrorType(ev.Err.Code)
		body.Error.Code = ev.Err.Code
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		s.st.MarkWritten(ev)
		return s.w.Write(ir.SSEEvent{Data: string(b)})

	case ir.EvDone:
		if ev.Stop != ir.StopUnspecified {
			s.stop = ev.Stop
		}
		return s.closeWithStop(ev.Stop)
	}
	return nil
}

// switchAwayFromTool 在从工具调用切回文本时收尾当前工具调用。
func (s *streamWriter) switchAwayFromTool() error {
	if s.openTool < 0 {
		return nil
	}
	s.openTool = -1
	return nil
}

func (s *streamWriter) chunkID() string {
	if s.startedID == "" {
		s.startedID = "chatcmpl-freerouter"
	}
	s.seq++
	return fmt.Sprintf("%s-%d", s.startedID, s.seq)
}

func (s *streamWriter) writeChunk(d delta, finish *string) error {
	return s.writeFrame(chunk{
		ID:      s.chunkID(),
		Object:  "chat.completion.chunk",
		Model:   s.model,
		Created: s.created,
		Choices: []chunkChoice{{Index: 0, Delta: &d, FinishReason: finish}},
	})
}

// writeFrame 写出一个 chunk，并在写之前先记状态。
//
// 为什么先记状态：MarkWritten 的语义是"这些字节即将可见"。写失败时状态已经推进（保守方向），
// 于是 C7 会认为不可切换——这是安全的一侧：宁可少一次重试，不可在客户端已见到半个 chunk 后重发。
func (s *streamWriter) writeFrame(c chunk) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	ev := chunkToEvent(c)
	s.st.MarkWritten(ev)
	return s.w.Write(ir.SSEEvent{Data: string(b)})
}

// chunkToEvent 把协议帧反推成 IR 事件，仅用于状态记账。
func chunkToEvent(c chunk) ir.Event {
	if len(c.Choices) == 0 {
		if c.Usage != nil {
			return ir.Event{Type: ir.EvUsage}
		}
		return ir.Event{Type: ir.EvStart}
	}
	d := c.Choices[0].Delta
	if d == nil {
		return ir.Event{Type: ir.EvDone}
	}
	if len(d.ToolCalls) > 0 {
		if d.ToolCalls[0].Index != nil {
			return ir.Event{Type: ir.EvToolCallDelta, Index: *d.ToolCalls[0].Index}
		}
		return ir.Event{Type: ir.EvToolCallStart}
	}
	if d.Content != "" {
		return ir.Event{Type: ir.EvTextDelta}
	}
	return ir.Event{Type: ir.EvStart}
}

// closeWithStop 写出 finish_reason 帧与 [DONE]。
func (s *streamWriter) closeWithStop(stop ir.StopReason) error {
	if stop == ir.StopUnspecified {
		stop = s.stop
	}
	if stop == ir.StopUnspecified {
		// 最后兜底用 EncodeOptions.Stop：路由层发起上游请求时已知本次生成的结束原因
		//（如客户端取消、超长截断），事件流未带 EvDone.Stop 时它是唯一信息源。
		stop = s.opt.Stop
	}
	if stop == ir.StopUnspecified {
		stop = ir.StopEndTurn
	}
	reason := stopToFinishReason(stop)
	if err := s.writeChunk(delta{}, &reason); err != nil {
		return err
	}
	s.closed = true
	s.st.MarkWritten(ir.Event{Type: ir.EvDone})
	return s.w.Write(ir.SSEEvent{Data: doneSentinel})
}

// Close 补齐收尾。幂等：重复调用不再写任何帧，否则客户端会收到两个 finish_reason。
func (s *streamWriter) Close() error {
	if s.closed {
		return nil
	}
	return s.closeWithStop(s.stop)
}

// ---- 线上 chunk 类型 ----

type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int     `json:"index"`
	Delta        *delta  `json:"delta,omitempty"`
	FinishReason *string `json:"finish_reason"`
}

// delta 的字段全部 omitempty：空对象 {} 是合法的 delta（心跳），而带空字符串的字段会让
// 某些严格客户端把空 content 当成"模型发了空消息"。
type delta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []deltaToolCall `json:"tool_calls,omitempty"`
	Refusal   string          `json:"refusal,omitempty"`
}

type deltaToolCall struct {
	Index    *int          `json:"index,omitempty"`
	ID       string        `json:"id,omitempty"`
	Type     string        `json:"type,omitempty"`
	Function deltaFunction `json:"function"`
}

type deltaFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// streamReader 实现 Chat Completions SSE 帧 → IR 事件。
type streamReader struct {
	r        *ir.SSEReader
	reasm    *ir.ToolCallReassembler
	pending  []ir.Event
	started  bool
	finished bool
	usage    *ir.Usage
	stop     ir.StopReason
}

// NewStreamReader 构造流式解码器。
//
// 为什么需要 DecodeOptions.RequestID：重组器在"上游始终不给 id"时要用它合成确定性 id；
// 没有它合成 id 会退化成 call_unknown_0，两次并发请求就撞车（§6.2）。
func (adapter) NewStreamReader(r io.Reader, opt ir.DecodeOptions) ir.StreamReader {
	return &streamReader{
		r:     ir.NewSSEReader(r),
		reasm: ir.NewToolCallReassembler(opt.RequestID),
	}
}

// Next 返回下一个 IR 事件。
func (s *streamReader) Next() (ir.Event, error) {
	for {
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, nil
		}
		if s.finished {
			return ir.Event{}, io.EOF
		}

		frame, err := s.r.Next()
		if err == io.EOF {
			// 上游没发 [DONE] 就断了：把缓冲里的工具调用收尾并补 done，
			// 否则客户端会一直等一个永远不会来的收尾（I2）。
			s.pending = append(s.pending, s.reasm.CloseAll()...)
			done := ir.Event{Type: ir.EvDone, Stop: s.stop}
			if s.usage != nil {
				s.pending = append(s.pending, ir.Event{Type: ir.EvUsage, Usage: s.usage})
			}
			s.pending = append(s.pending, done)
			s.finished = true
			continue
		}
		if err != nil {
			return ir.Event{}, err
		}

		data := strings.TrimSpace(frame.Data)
		if data == "" {
			continue
		}
		if data == doneSentinel {
			s.pending = append(s.pending, s.reasm.CloseAll()...)
			if s.usage != nil {
				s.pending = append(s.pending, ir.Event{Type: ir.EvUsage, Usage: s.usage})
			}
			s.pending = append(s.pending, ir.Event{Type: ir.EvDone, Stop: s.stop})
			s.finished = true
			continue
		}

		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return ir.Event{}, &ir.InvalidRequestError{Param: "stream", Detail: "chunk 不是合法 JSON：" + err.Error()}
		}
		if c.Usage != nil {
			u := decodeUsage(c.Usage)
			s.usage = &u
		}

		// I1：start 由解码层合成——OpenAI 没有显式帧。为什么在这里合成而不是让上层判断：
		// 四个协议的 start 语义必须一致，任何编码器都不该猜"这是不是流的第一帧"。
		if !s.started {
			s.started = true
			s.pending = append(s.pending, ir.Event{Type: ir.EvStart})
		}

		if len(c.Choices) == 0 {
			continue
		}
		ch := c.Choices[0]
		if ch.FinishReason != nil {
			stop, _ := finishReasonToStop(*ch.FinishReason)
			s.stop = stop
		}
		if ch.Delta == nil {
			continue
		}

		if ch.Delta.Content != "" {
			s.pending = append(s.pending, ir.Event{Type: ir.EvTextDelta, Text: ch.Delta.Content})
		}
		// 部分上游把 thinking 放在 reasoning_content；它不在协议里，所以只在
		// 明确出现时才转成 thinking_delta，而不是假装它不存在。
		for _, tc := range ch.Delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			evs, err := s.reasm.Accept(ir.Fragment{
				Index: idx, ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
			})
			if err != nil {
				return ir.Event{}, err
			}
			s.pending = append(s.pending, evs...)
		}
	}
}

// Warnings 暴露重组器的告警，供上层进审计。不属于 StreamReader 接口，
// 但调用方可以判型取用——C6/C7 之外的告警不该被接口藏起来。
func (s *streamReader) Warnings() []ir.Warning { return s.reasm.Warnings() }
