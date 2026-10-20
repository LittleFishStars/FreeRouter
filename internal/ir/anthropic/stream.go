package anthropic

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// ---- 流式帧的线上类型 ----

// streamFrame 是所有具名事件 data 负载的公共形状：type 判别后各取所需。
//
// 为什么 message_delta 的停止信息也走 Delta：协议把它嵌在 "delta" 字段里
// （{"delta":{"stop_reason":...}}），与 content_block_delta 的 delta 同名——
// 复用同一个 streamDelta（含 stop 子字段）按事件名分派，避免两个字段同标 "delta"。
type streamFrame struct {
	Type  string `json:"type"`
	Index *int   `json:"index,omitempty"`

	// message_start
	Message *response `json:"message,omitempty"`

	// content_block_start
	ContentBlock *contentBlock `json:"content_block,omitempty"`

	// content_block_delta / message_delta
	Delta *streamDelta `json:"delta,omitempty"`

	// message_delta 的 usage
	Usage *usage `json:"usage,omitempty"`

	// error
	Error *errorBody `json:"error,omitempty"`
}

// streamDelta 承载四种增量：text / thinking / signature / input_json，
// 外加 message_delta 的 stop_reason。signature 与 thinking 同块出现
// （Anthropic 用它回传思考签名），所以放同一结构。
type streamDelta struct {
	// omitempty：content_block_delta 路径永远显式设 Type；message_delta 的 delta 里
	// 协议没有 type 字段（01-inbound.md §6.3 示例），空串会编出非法形状。
	Type         string  `json:"type,omitempty"`
	Text         string  `json:"text,omitempty"`
	Thinking     string  `json:"thinking,omitempty"`
	Signature    string  `json:"signature,omitempty"`
	PartialJSON  string  `json:"partial_json,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// streamEventName 是 `event:` 行的具名事件；Anthropic 客户端按它分派，而不是按 data.type。
type streamEventName string

const (
	evMessageStart      streamEventName = "message_start"
	evContentBlockStart streamEventName = "content_block_start"
	evContentBlockDelta streamEventName = "content_block_delta"
	evContentBlockStop  streamEventName = "content_block_stop"
	evMessageDelta      streamEventName = "message_delta"
	evMessageStop       streamEventName = "message_stop"
	evError             streamEventName = "error"
	evPing              streamEventName = "ping"
)

// ---- 编码器：IR 事件 → 具名 SSE 帧 ----

// streamWriter 维护"当前打开的内容块"。
//
// 为什么必须显式管理块生命周期：Anthropic 要求每个 content_block_start/stop 成对
// （01-inbound.md §6.3），而 IR 事件流只有 text_delta、没有"文本块开始"事件——
// 块边界由事件类型切换推导：类型变 → 先 close 再 open。这与 openai 侧"滚 index"
// 是同族问题，但方向相反：那边收尾是隐式的，这边收尾是硬要求。
type streamWriter struct {
	w     *ir.SSEWriter
	st    *ir.EncoderState
	model string

	msgID     string
	openKind  string // 当前打开块的类型：text | thinking | tool_use；"" 表示无
	openIndex int    // 当前块 index；按 open 顺序自增（协议要求递增，与 OpenAI 的 tool_calls index 不同层）
	nextIndex int

	closed bool
	stop   ir.StopReason
	usage  *ir.Usage
}

// NewStreamWriter 构造流式编码器。
func (adapter) NewStreamWriter(w io.Writer, opt ir.EncodeOptions) ir.StreamWriter {
	return &streamWriter{
		w:         ir.NewSSEWriter(w),
		st:        &ir.EncoderState{},
		model:     opt.UpstreamModel,
		openIndex: -1,
	}
}

func (s *streamWriter) State() *ir.EncoderState { return s.st }

// Write 编码一个 IR 事件。
func (s *streamWriter) Write(ev ir.Event) error {
	if s.closed {
		return ir.ErrStreamClosed
	}
	if !s.st.Accept(ev) {
		return nil // I5：done 之后丢弃，不报错
	}
	switch ev.Type {
	case ir.EvStart:
		return s.writeMessageStart()

	case ir.EvTextDelta:
		if s.openKind != "text" {
			if err := s.closeBlock(); err != nil {
				return err
			}
			if err := s.openBlock("text"); err != nil {
				return err
			}
		}
		return s.writeDelta(&streamDelta{Type: "text_delta", Text: ev.Text})

	case ir.EvThinkingDelta:
		if ev.Text == "" && ev.Signature != "" {
			// 签名终帧：发 signature_delta，不开新块也不记账（非可见内容）。
			// 协议要求签名属于尚未关闭的 thinking 块；块已关时事件顺序已乱，
			// 丢弃签名是唯一不产生非法帧的选择（丢了只影响下轮回传，不影响本次输出）。
			if s.openKind != "thinking" {
				return nil
			}
			return s.writeFrame(evContentBlockDelta, streamFrame{
				Type: string(evContentBlockDelta), Index: ir.Ptr(s.openIndex),
				Delta: &streamDelta{Type: "signature_delta", Signature: ev.Signature},
			})
		}
		if s.openKind != "thinking" {
			if err := s.closeBlock(); err != nil {
				return err
			}
			if err := s.openBlock("thinking"); err != nil {
				return err
			}
		}
		return s.writeDelta(&streamDelta{Type: "thinking_delta", Thinking: ev.Text})

	case ir.EvToolCallStart:
		if ev.Call == nil {
			return &ir.InvalidRequestError{Param: "event.call", Detail: "tool_call_start 缺少 call"}
		}
		if err := s.closeBlock(); err != nil {
			return err
		}
		if err := s.openToolBlock(ev.Call); err != nil {
			return err
		}
		// start 事件若携带初始参数（openai 首片常带），立刻补 delta——参数不丢。
		if ev.Call.Arguments != "" {
			return s.writeDelta(&streamDelta{Type: "input_json_delta", PartialJSON: ev.Call.Arguments})
		}
		return nil

	case ir.EvToolCallDelta:
		if ev.Call == nil {
			return &ir.InvalidRequestError{Param: "event.call", Detail: "tool_call_delta 缺少 call"}
		}
		if s.openKind != "tool_use" {
			// 参数增量必须属于已打开的 tool_use 块；没有就开一个，用事件里的
			// id/name 补全块头，保证 start/stop 成对且 id 不丢。
			if err := s.closeBlock(); err != nil {
				return err
			}
			if err := s.openToolBlock(ev.Call); err != nil {
				return err
			}
		}
		if ev.Call.Arguments == "" {
			return nil
		}
		return s.writeDelta(&streamDelta{Type: "input_json_delta", PartialJSON: ev.Call.Arguments})

	case ir.EvToolCallEnd:
		// 显式收尾：协议要求成对，工具调用结束是明确的 close 时机。
		return s.closeBlock()

	case ir.EvUsage:
		if ev.Usage == nil {
			return nil
		}
		// 不单独成帧：协议把 output usage 定义在 message_delta 上，攒到收尾合并发。
		s.usage = ev.Usage
		return nil

	case ir.EvError:
		if ev.Err == nil {
			return nil
		}
		// type 查表填 §2.4 粗枚举，细码走 code 扩展字段（与 openai 侧共用同一张表，不各自 switch）。
		f := streamFrame{Type: string(evError), Error: &errorBody{
			Type: ir.CodeToErrorType(ev.Err.Code), Code: ev.Err.Code, Message: ev.Err.Message}}
		s.st.MarkWritten(ev)
		return s.writeFrame(evError, f)

	case ir.EvDone:
		if ev.Stop != ir.StopUnspecified {
			s.stop = ev.Stop
		}
		return s.finish()
	}
	return nil
}

func (s *streamWriter) writeMessageStart() error {
	f := streamFrame{Type: string(evMessageStart), Message: &response{
		ID: s.id(), Type: "message", Role: "assistant", Model: s.model,
		// content 必须是空数组而不是 null：协议客户端按数组遍历，null 会直接崩。
		Content: []contentBlock{},
	}}
	// I1：start 是协议壳、不算可见内容；MarkWritten 内部按 Visible() 区分。
	s.st.MarkWritten(ir.Event{Type: ir.EvStart})
	return s.writeFrame(evMessageStart, f)
}

func (s *streamWriter) id() string {
	if s.msgID == "" {
		s.msgID = "msg_freerouter"
	}
	return s.msgID
}

// openBlock 打开一个 text/thinking 块。
func (s *streamWriter) openBlock(kind string) error {
	var cb *contentBlock
	switch kind {
	case "thinking":
		cb = &contentBlock{Type: "thinking", Thinking: ""}
	default:
		cb = &contentBlock{Type: "text", Text: ""}
	}
	idx := s.nextIndex
	f := streamFrame{Type: string(evContentBlockStart), Index: ir.Ptr(idx), ContentBlock: cb}
	// 块头帧本身不新增可见信息（内容随后由 delta 携带并记账），所以不 MarkWritten；
	// 真正的记账在 writeDelta。
	if err := s.writeFrame(evContentBlockStart, f); err != nil {
		return err
	}
	s.openKind = kind
	s.openIndex = idx
	s.nextIndex++
	return nil
}

// openToolBlock 打开 tool_use 块；块头携带 id/name，这就是该调用的"开始可见"。
func (s *streamWriter) openToolBlock(call *ir.ToolCallPart) error {
	// input 在协议里是对象；参数未至，空对象占位（规范形状，01-inbound.md §6.3）。
	cb := &contentBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: json.RawMessage("{}")}
	idx := s.nextIndex
	f := streamFrame{Type: string(evContentBlockStart), Index: ir.Ptr(idx), ContentBlock: cb}
	s.st.MarkWritten(ir.Event{Type: ir.EvToolCallStart, Index: idx, Call: call})
	if err := s.writeFrame(evContentBlockStart, f); err != nil {
		return err
	}
	s.openKind = "tool_use"
	s.openIndex = idx
	s.nextIndex++
	return nil
}

// closeBlock 若有打开的块则补 content_block_stop。幂等：无块时是 no-op。
// stop 帧不携带内容，不重复记账（可见性已由 start/delta 记过）。
func (s *streamWriter) closeBlock() error {
	if s.openKind == "" {
		return nil
	}
	f := streamFrame{Type: string(evContentBlockStop), Index: ir.Ptr(s.openIndex)}
	s.openKind = ""
	return s.writeFrame(evContentBlockStop, f)
}

func (s *streamWriter) writeDelta(d *streamDelta) error {
	f := streamFrame{Type: string(evContentBlockDelta), Index: ir.Ptr(s.openIndex), Delta: d}
	// 写出前记状态（与 openai 侧同理：写失败时状态已推进是保守方向，
	// 宁可少一次重试，不可在客户端已见半个 chunk 后重发）。
	switch d.Type {
	case "text_delta":
		s.st.MarkWritten(ir.Event{Type: ir.EvTextDelta})
	case "thinking_delta":
		s.st.MarkWritten(ir.Event{Type: ir.EvThinkingDelta})
	case "input_json_delta":
		s.st.MarkWritten(ir.Event{Type: ir.EvToolCallDelta, Index: s.openIndex})
	}
	return s.writeFrame(evContentBlockDelta, f)
}

// finish 收尾：关块 → message_delta(stop_reason+usage) → message_stop。
// 协议没有 [DONE]（01-inbound.md §6.3），多发一帧只会让严格客户端困惑。
func (s *streamWriter) finish() error {
	if err := s.closeBlock(); err != nil {
		return err
	}
	f := streamFrame{
		Type:  string(evMessageDelta),
		Delta: &streamDelta{StopReason: stopToStopReason(s.stop)},
		Usage: encodeUsage(orUnknownUsage(s.usage)),
	}
	// 只有真带 usage 才记 EvUsage，否则"没有 usage 的收尾"会误占 I4 的去重槽位。
	if s.usage != nil {
		s.st.MarkWritten(ir.Event{Type: ir.EvUsage})
	}
	if err := s.writeFrame(evMessageDelta, f); err != nil {
		return err
	}
	s.closed = true
	s.st.MarkWritten(ir.Event{Type: ir.EvDone})
	return s.writeFrame(evMessageStop, streamFrame{Type: string(evMessageStop)})
}

func orUnknownUsage(u *ir.Usage) ir.Usage {
	if u == nil {
		return ir.Usage{Source: ir.UsageUnknown}
	}
	return *u
}

// writeFrame 写一帧具名事件。
func (s *streamWriter) writeFrame(name streamEventName, f streamFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return s.w.Write(ir.SSEEvent{Event: string(name), Data: string(b)})
}

// Close 补齐收尾。幂等：重复调用不再写帧，否则客户端见到两个 message_stop。
func (s *streamWriter) Close() error {
	if s.closed {
		return nil
	}
	return s.finish()
}

// ---- 解码器：具名 SSE 帧 → IR 事件 ----

type blockState struct {
	kind string // text | thinking | tool_use
	id   string
	name string
}

// streamReader 把 Anthropic 流解成 IR 事件。
//
// 为什么复用 ToolCallReassembler：Anthropic 的 content_block index 与 OpenAI 的
// tool_calls index 不是同一层语义（§6.3 对照表），但 id 一定在 content_block_start
// 就给全——重组器在这里退化为"start/delta/end 事件合成 + CloseAll 兜底"，语义吻合。
type streamReader struct {
	r     *ir.SSEReader
	reasm *ir.ToolCallReassembler

	blocks   map[int]*blockState
	usage    *ir.Usage // message_start 的 input 与 message_delta 的 output 在此合并（I4：usage 只发一次）
	stop     ir.StopReason
	started  bool
	finished bool
	pending  []ir.Event
	// signatures 按块聚合 thinking 块的签名碎片；content_block_stop 时作为带
	// Signature 的 EvThinkingDelta 终帧发出（Event.Signature 是 §10 允许的追加）。
	// 为什么不在 delta 里逐帧发签名：签名不是内容，逐帧发会让编码器把它渲染进正文。
	signatures []sigEntry
	warnings   []ir.Warning
}

// NewStreamReader 构造流式解码器。
func (adapter) NewStreamReader(r io.Reader, opt ir.DecodeOptions) ir.StreamReader {
	return &streamReader{
		r:      ir.NewSSEReader(r),
		reasm:  ir.NewToolCallReassembler(opt.RequestID),
		blocks: make(map[int]*blockState),
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
			// 上游断流没发 message_stop：补 CloseAll（I2）与 done，
			// 否则客户端永远等一个不会来的收尾。
			s.pending = append(s.pending, s.reasm.CloseAll()...)
			if s.usage != nil {
				s.pending = append(s.pending, ir.Event{Type: ir.EvUsage, Usage: s.usage})
			}
			s.pending = append(s.pending, ir.Event{Type: ir.EvDone, Stop: s.stop})
			s.finished = true
			continue
		}
		if err != nil {
			return ir.Event{}, err
		}

		name := streamEventName(frame.Event)
		data := strings.TrimSpace(frame.Data)
		if data == "" || name == evPing {
			continue // MVP 不发 ping；收到也忽略（01-inbound.md §6.3 待定项）
		}

		var f streamFrame
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			return ir.Event{}, &ir.InvalidRequestError{Param: "stream", Detail: "帧不是合法 JSON：" + err.Error()}
		}
		if name == "" {
			name = streamEventName(f.Type) // 宽容形态：部分兼容网关只发 data.type
		}

		// I1：start 由解码层在首个有效帧合成，使四个协议的 start 语义一致。
		if !s.started {
			s.started = true
			s.pending = append(s.pending, ir.Event{Type: ir.EvStart})
		}

		switch name {
		case evMessageStart:
			if f.Message != nil {
				s.mergeUsage(f.Message.Usage)
			}

		case evContentBlockStart:
			idx := 0
			if f.Index != nil {
				idx = *f.Index
			}
			cb := f.ContentBlock
			if cb == nil {
				cb = &contentBlock{}
			}
			s.blocks[idx] = &blockState{kind: cb.Type, id: cb.ID, name: cb.Name}
			if cb.Type == "tool_use" {
				evs, err := s.reasm.Accept(ir.Fragment{Index: idx, ID: cb.ID, Name: cb.Name})
				if err != nil {
					return ir.Event{}, err
				}
				s.pending = append(s.pending, evs...)
			}

		case evContentBlockDelta:
			idx := 0
			if f.Index != nil {
				idx = *f.Index
			}
			if f.Delta == nil {
				continue
			}
			switch f.Delta.Type {
			case "text_delta":
				if f.Delta.Text != "" {
					s.pending = append(s.pending, ir.Event{Type: ir.EvTextDelta, Index: idx, Text: f.Delta.Text})
				}
			case "thinking_delta":
				if f.Delta.Thinking != "" {
					s.pending = append(s.pending, ir.Event{Type: ir.EvThinkingDelta, Index: idx, Text: f.Delta.Thinking})
				}
			case "signature_delta":
				// 聚合，不发事件：签名不是内容。
				s.signatures = appendSignature(s.signatures, idx, f.Delta.Signature)
			case "input_json_delta":
				evs, err := s.reasm.Accept(ir.Fragment{Index: idx, Arguments: f.Delta.PartialJSON})
				if err != nil {
					return ir.Event{}, err
				}
				s.pending = append(s.pending, evs...)
			}

		case evContentBlockStop:
			idx := 0
			if f.Index != nil {
				idx = *f.Index
			}
			st := s.blocks[idx]
			if st == nil {
				continue
			}
			switch st.kind {
			case "tool_use":
				evs, err := s.reasm.Accept(ir.Fragment{Index: idx, Complete: true})
				if err != nil {
					return ir.Event{}, err
				}
				s.pending = append(s.pending, evs...)
			case "thinking":
				if sig := takeSignature(&s.signatures, idx); sig != "" {
					// 终帧：Text 为空、只携带签名——编码侧（openai/anthropic）对
					// Text=="" 且 Signature!="" 的 thinking 事件只更新签名缓冲不写正文。
					s.pending = append(s.pending, ir.Event{Type: ir.EvThinkingDelta, Index: idx, Signature: sig})
				}
			}

		case evMessageDelta:
			if f.Delta != nil && f.Delta.StopReason != "" {
				stop, _ := stopReasonToStop(f.Delta.StopReason)
				s.stop = stop
			}
			s.mergeUsage(f.Usage)

		case evMessageStop:
			s.pending = append(s.pending, s.reasm.CloseAll()...)
			if s.usage != nil {
				s.pending = append(s.pending, ir.Event{Type: ir.EvUsage, Usage: s.usage})
			}
			s.pending = append(s.pending, ir.Event{Type: ir.EvDone, Stop: s.stop})
			s.finished = true

		case evError:
			if f.Error != nil {
				// 细码优先：我们自己的错误体带 code；上游原生的只有粗 type，反查归一。
				code := f.Error.Code
				if code == "" {
					code = ir.ErrorTypeToCode(f.Error.Type)
				}
				s.pending = append(s.pending, ir.Event{Type: ir.EvError,
					Err: &ir.Error{Code: code, Message: f.Error.Message}})
				s.pending = append(s.pending, ir.Event{Type: ir.EvDone, Stop: ir.StopError})
				s.finished = true
			}

		default:
			// 未知具名事件：跳过但记告警——协议将来加事件时，"丢"必须是可见的。
			s.warnings = append(s.warnings, ir.Warning{
				Param:   "stream.event",
				Message: "未知具名事件 " + string(name) + "，已跳过",
			})
		}
	}
}

// mergeUsage 把一帧里的 usage 并入累积值。
//
// Anthropic 的 usage 分两处：message_start 给 input，message_delta 给 output 与
// cache 项。IR 的 I4 要求 usage 只发一次，所以解码器负责合并、在 done 前一次性发。
func (s *streamReader) mergeUsage(u *usage) {
	if u == nil {
		return
	}
	if s.usage == nil {
		s.usage = &ir.Usage{Source: ir.UsageUpstream}
	}
	if u.InputTokens != nil {
		s.usage.InputTokens = u.InputTokens
	}
	if u.OutputTokens != nil {
		s.usage.OutputTokens = u.OutputTokens
	}
	if u.CacheReadInputTokens != nil {
		s.usage.CacheReadTokens = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		s.usage.CacheWriteTokens = u.CacheCreationInputTokens
	}
}

// Warnings 暴露重组器与解码层告警，供上层进审计（接口外判型取用，同 openai 侧）。
func (s *streamReader) Warnings() []ir.Warning {
	return append(s.reasm.Warnings(), s.warnings...)
}

// ---- 签名缓冲小工具 ----

type sigEntry struct {
	idx int
	sig strings.Builder
}

func appendSignature(list []sigEntry, idx int, sig string) []sigEntry {
	for i := range list {
		if list[i].idx == idx {
			list[i].sig.WriteString(sig)
			return list
		}
	}
	var e sigEntry
	e.idx = idx
	e.sig.WriteString(sig)
	return append(list, e)
}

// takeSignature 取出并移除某块的签名（一次性交付，防重复发）。
func takeSignature(list *[]sigEntry, idx int) string {
	for i, e := range *list {
		if e.idx == idx {
			*list = append((*list)[:i], (*list)[i+1:]...)
			return e.sig.String()
		}
	}
	return ""
}
