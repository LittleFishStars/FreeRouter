package ir

import (
	"errors"
	"io"
)

// 本文件定义协议适配器的公共形状。
//
// 为什么把接口放在 IR 包而不是各协议包：路由核心需要"持有一个协议适配器"而不知道它是谁。
// 接口放这里，openai/ 与 anthropic/ 各自实现，内核只依赖 IR——依赖方向单向，协议包之间
// 互不认识，也就不可能悄悄出现"OpenAI 的字段泄漏进 Anthropic 编码"。

// ErrStreamClosed 表示流已终结，调用方不应再写事件。
var ErrStreamClosed = errors.New("ir: 流已终结")

// EncodeOptions 是"编码到某个具体上游"时才需要、但不属于 IR 的信息。
//
// 为什么真实模型名不放进 IR：IR 的 Model 是抽象模型名（L1 已解析）。真实模型 id 属于**候选**，
// 同一个抽象模型在不同上游对应不同 id。把它放进 Request 会让内核认识真实模型，
// 也会让审计日志里混进不该出现的上游标识。所以它作为编码参数逐次传入。
type EncodeOptions struct {
	UpstreamModel string // 候选的真实模型 id
	RequestID     string // 合成 tool_call id 时用
	// Stop 是本次生成的结束原因。
	// 为什么要传进来：OpenAI 的 finish_reason 与 Anthropic 的 stop_reason 都是**流末帧必填**
	// 字段，而 IR 的事件联合体里没有 stop 事件（§5.1）。让编码器去猜（“有工具调用就是 tool_use”）
	// 会把“模型真的说完了”与“被长度截断”混为一谈，而这两者在计费与重试决策上不同。
	Stop StopReason
}

// DecodeOptions 是解码上游响应时的上下文。
type DecodeOptions struct {
	// RequestID 用于"上游始终不给 tool_call id"时合成确定性 id（§6.2）。
	// 它必须来自本次路由的请求 ID，否则并发下合成 id 会撞车。
	RequestID string
}

// Adapter 是一个协议的编解码器。
//
// 每个方法都把"该协议不支持"表达为 *UnsupportedError，而不是丢弃字段——这是 C6 的落点。
type Adapter interface {
	// Name 返回协议标识，用于日志与错误体。
	Name() Protocol
	// Capabilities 自报能力，供路由层过滤候选（C6 的另一半：不是所有缺失都要 400）。
	Capabilities() Capabilities

	// DecodeRequest 把入站请求体解析成 IR。
	// 第二个返回值是"允许忽略但有信息量"的告警（§8.4 白名单字段），调用方应当记录它。
	DecodeRequest(body []byte) (*Request, []Warning, error)
	// EncodeRequest 把 IR 编码成上游请求体。编码前复核能力：候选能力往往在解析之后才确定，
	// 所以它是最后一道闸，不能假设解析阶段已经查过。
	EncodeRequest(req *Request, opt EncodeOptions) ([]byte, []Warning, error)

	// DecodeResponse 把非流式响应体解析成 IR。
	DecodeResponse(body []byte) (*Response, error)
	// EncodeResponse 把 IR 响应编码成协议响应体。
	EncodeResponse(resp *Response) ([]byte, error)

	// NewStreamWriter 构造流式编码器：IR 事件 → 协议 SSE 帧。
	NewStreamWriter(w io.Writer, opt EncodeOptions) StreamWriter
	// NewStreamReader 构造流式解码器：协议 SSE 帧 → IR 事件。
	NewStreamReader(r io.Reader, opt DecodeOptions) StreamReader
}

// StreamWriter 把 IR 事件写成协议帧。
//
// State() 是 C7 的接口：HTTP 层在决定"要不要换候选重试"时只能看它，
// 不允许自己数字节——可见内容的判定规则只有 IR 层清楚。
type StreamWriter interface {
	State() *EncoderState
	// Write 编码一个事件。违反不变式（如 done 之后再来事件）时应返回 ErrStreamClosed。
	Write(ev Event) error
	// Close 补齐协议规定的收尾：未闭合的内容块、终止帧（[DONE] / message_stop）。
	// 它是幂等的——上游正常结束与被中断都走这里，重复调用不应产生重复帧。
	Close() error
}

// StreamReader 把协议帧解成 IR 事件。
type StreamReader interface {
	// Next 返回下一个 IR 事件。流正常结束返回 io.EOF。
	Next() (Event, error)
}

// CapabilityChecker 由实现了能力复核的适配器满足；EncodeRequest 内部用它做最后一道闸。
type CapabilityChecker interface {
	Check(req *Request) ([]Warning, error)
}

// AsUnsupported 把任意 error 规约为 *UnsupportedError，便于调用方判型而不必重复 errors.As。
func AsUnsupported(err error) (*UnsupportedError, bool) {
	var ue *UnsupportedError
	if errors.As(err, &ue) {
		return ue, true
	}
	return nil, false
}

// AsInvalidRequest 把任意 error 规约为 *InvalidRequestError。
func AsInvalidRequest(err error) (*InvalidRequestError, bool) {
	var ie *InvalidRequestError
	if errors.As(err, &ie) {
		return ie, true
	}
	return nil, false
}
