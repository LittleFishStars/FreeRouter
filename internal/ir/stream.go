package ir

// 本文件是流式事件的联合体与"可见内容"的定义。
//
// 为什么要把不变式（I1–I7）写进注释并让编码层依赖它们：四个协议对"流应该长什么样"要求不同
// （Anthropic 要求 message_start 先发、OpenAI 要求 [DONE] 结尾）。如果上游事件的顺序不保证，
// 每个编码器都要各自防御，等于把这套不变式复制四遍。见 docs/architecture/02-ir.md §5.2。

// EventType 是流式事件类型。
type EventType string

const (
	EvStart         EventType = "start"
	EvThinkingDelta EventType = "thinking_delta"
	EvTextDelta     EventType = "text_delta"
	EvToolCallStart EventType = "tool_call_start"
	EvToolCallDelta EventType = "tool_call_delta"
	EvToolCallEnd   EventType = "tool_call_end"
	EvUsage         EventType = "usage"
	EvError         EventType = "error"
	EvDone          EventType = "done"
)

// Event 是流式事件。
//
// Index 是内容块序号；EvStart / EvUsage / EvDone / EvError 恒为 0。
// 不变式（映射层可以依赖）：
//
//	I1 start 是流的第一个事件，且只出现一次（上游没有对应概念时由编码层合成）
//	I2 每个 tool_call_start 必有配对的 tool_call_end（流中断时编码层补发 Partial=true 的 end）
//	I3 同一 Index 的 delta 归属同一内容块（上游跨块复用 index 视为协议违规 → error 结束）
//	I4 usage 至多一次，且若存在必在 done 之前（上游给多次时取最后一次）
//	I5 done 之后不再有事件（编码层丢弃）
//	I6 error 之后只允许 done
//	I7 事件按生成顺序单向交付，不做重排
type Event struct {
	Type  EventType
	Index int
	Text  string
	Call  *ToolCallPart
	Usage *Usage
	Err   *Error
	// Signature 只在 EvThinkingDelta 上有意义：Text 为空、Signature 非空时是
	// "思考块收尾携带签名"的终帧（Anthropic 的 signature_delta 在块 stop 时才齐）。
	// 为什么用"空 Text + 非空 Signature"而不是新事件类型：§5.1 的事件集合是封闭的，
	// 新类型会迫使四个编码器都加防御分支；而签名本质是 thinking 的附属元信息，
	// 挂在同一事件上最接近它的生成时机。这是对 §5.1 的追加（§10 允许只加不改）。
	Signature string
	// Stop 只在 EvDone 上有意义：它把结束原因带过解码→编码边界。
	// 为什么需要它：§5.1 的事件联合体里没有 stop 事件，而 OpenAI 的 finish_reason 与
	// Anthropic 的 stop_reason 都是流末帧的必填字段。若不在 EvDone 上携带，编码器就只能猜
	// （"有工具调用就是 tool_use"），会把"模型说完了"与"被长度截断"混为一谈。
	// 这也是对 §5.1 的**追加**（§10 允许只加不改），已在回报中作为规格缺口提出。
	Stop StopReason
}

// Visible 判定该事件是否属于"已吐可见内容"。
//
// 为什么 start 不算：它是我们合成的协议壳（Anthropic 的 message_start），不携带模型产出。
// 若把 start 算作可见，任何一次"上游刚建连就断"都会让请求不可恢复地失败；不算的话，
// 我们可以在已开始 SSE 响应的情况下换候选——只要还没写任何模型内容（C7）。
//
// thinking_delta 当前按"算"处理：客户端可能已在渲染。这意味着只产出 thinking 的候选失败后
// 无法切换，这条待真实上游行为确认（§5.3 的待定项）。
func (e Event) Visible() bool {
	switch e.Type {
	case EvThinkingDelta:
		// 纯签名终帧（Text 为空）不算可见内容：它不渲染任何东西，丢了也只是签名缺席，
		// 不值得为此把候选锁死；真正的思考文本增量才是可见的。
		if e.Text == "" && e.Signature != "" {
			return false
		}
		return true
	case EvTextDelta, EvToolCallStart, EvToolCallDelta, EvToolCallEnd:
		return true
	}
	return false
}

// EncoderState 记录一个流式编码器的进度，供 C7 判定"还能不能换候选"。
//
// 为什么把状态放在编码器而不是让 HTTP 层自己数：可见内容的判定规则（start 不算）只有 IR
// 层清楚；上层若自己实现一遍，规则一改（比如 thinking 的取舍）就必然漂移。
type EncoderState struct {
	started  bool // 是否已向客户端写出任何字节（含协议壳）
	visible  bool // 是否已写出可见内容
	usage    bool // 是否已发过 usage（I4）
	finished bool // 是否已发过 done（I5）
}

// Started 报告是否已经开始向客户端输出。
func (s *EncoderState) Started() bool { return s.started }

// Visible 报告是否已吐出可见内容——即 C7 的判定依据：为真时不得切换候选。
func (s *EncoderState) Visible() bool { return s.visible }

// Finished 报告流是否已终结。
func (s *EncoderState) Finished() bool { return s.finished }

// MarkWritten 记录"即将写出一个协议帧"，在帧真正写出前调用。
//
// 编码器应当在**写出前**调用它，而不是写出后：中间件无法回滚已经发到 socket 的字节，
// 若写完再记状态，一次 panic 或写失败就会让状态落后于现实。
func (s *EncoderState) MarkWritten(e Event) {
	s.started = true
	if e.Visible() {
		s.visible = true
	}
	switch e.Type {
	case EvUsage:
		s.usage = true
	case EvDone:
		s.finished = true
	}
}

// Accept 判定一个事件是否还允许写出，实现 I4–I6。
//
// 返回 false 表示编码器应丢弃该事件（不是报错——上游多给一个 usage 不值得让整个流失败，
// 但必须让调用方能计数并告警）。
func (s *EncoderState) Accept(e Event) bool {
	if s.finished {
		// I5：done 之后一律丢弃。
		return false
	}
	if e.Type == EvUsage && s.usage {
		// I4：usage 至多一次——上游给多次时取最后一次，因此允许覆盖但要求编码器去重。
		return true
	}
	return true
}
