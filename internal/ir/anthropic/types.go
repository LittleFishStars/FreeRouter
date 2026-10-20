// Package anthropic 实现 Anthropic Messages 协议与内部 IR 的双向编解码。
//
// 为什么单独成包：与 openai/ 同理——方言集中在一处。Anthropic 的"方言"与 OpenAI 差异极大：
// 系统提示在顶层、工具结果内联在 user 消息、思考块带必须回传的 signature、流式用具名事件
// 且没有 [DONE]。这些差异若散进路由核心，C6 的能力判定就无从收敛。
package anthropic

import (
	"encoding/json"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 本文件是 Messages API 的线上类型（wire types）。
//
// 三态原则与 openai/types.go 相同：可选字段用指针或 RawMessage，"没给"与"给了零值"不可混淆。

// request 是 /v1/messages 请求体。
type request struct {
	Model         string          `json:"model"`
	Messages      []message       `json:"messages"`
	System        json.RawMessage `json:"system,omitempty"` // string 或 text block 数组，两种形状都要接受
	Tools         []tool          `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	MaxTokens     *int            `json:"max_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Metadata      *metadata       `json:"metadata,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Thinking      *thinkingConfig `json:"thinking,omitempty"`
}

type metadata struct {
	UserID string `json:"user_id,omitempty"`
}

// thinkingConfig 是思考开关。budget_tokens 与 IR 档位的换算阈值是文档待定项
// （02-ir.md §7.1），这里给出临时表并在两个方向都记告警。
type thinkingConfig struct {
	Type         string `json:"type"` // enabled | disabled
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string 或 block 数组
}

// contentBlock 是 content 数组的一个元素。各类型共用一个结构、按 type 取用字段——
// 为什么不用 json.RawMessage 逐类型延迟解析：block 种类有限且稳定，一次性解析能让
// "未知 type" 在解码层就被拒绝（§10：未知 part 不忽略）。
type contentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image
	Source *source `json:"source,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string 或 block 数组
	IsError   *bool           `json:"is_error,omitempty"`

	// thinking / redacted_thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"` // redacted_thinking 的密文，原样保留
}

type source struct {
	Type      string `json:"type"` // base64 | url
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// tool 是工具声明：Anthropic 是平铺形状（name/description/input_schema），
// 与 OpenAI 的嵌套 function 形状正好是"协议无关 IR"要抹平的第一个差异。
type tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

// response 是非流式响应体。
type response struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []contentBlock `json:"content"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        *usage         `json:"usage,omitempty"`
	Error        *errorBody     `json:"error,omitempty"`
}

type usage struct {
	InputTokens              *int `json:"input_tokens,omitempty"`
	OutputTokens             *int `json:"output_tokens,omitempty"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens,omitempty"`
}

type errorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// stopReasonToStop 把 Anthropic 的 stop_reason 归一到 IR。
//
// 未知值按 end_turn 处理并把原始值留给调用方记告警——理由与 openai 侧相同：
// StopReason 进审计与计费归因，留空会让"这次为什么结束"永远查不出来。
func stopReasonToStop(s string) (ir.StopReason, bool) {
	switch s {
	case "end_turn":
		return ir.StopEndTurn, true
	case "max_tokens":
		return ir.StopMaxTokens, true
	case "stop_sequence":
		return ir.StopStopSequence, true
	case "tool_use":
		return ir.StopToolUse, true
	case "refusal":
		return ir.StopRefusal, true
	case "pause_turn":
		// 长对话被暂停：语义最接近"说完了"，原始值进告警。
		return ir.StopEndTurn, false
	case "":
		return ir.StopUnspecified, true
	}
	return ir.StopEndTurn, false
}

// stopToStopReason 是反向映射。IR 的 content_filter 在 Anthropic 没有对应值，
// 最近的是 refusal——两者都会引导客户端"换一个说法重试"，行为一致，故归一到它。
func stopToStopReason(s ir.StopReason) string {
	switch s {
	case ir.StopMaxTokens:
		return "max_tokens"
	case ir.StopStopSequence:
		return "stop_sequence"
	case ir.StopToolUse:
		return "tool_use"
	case ir.StopContentFilter, ir.StopRefusal:
		return "refusal"
	case ir.StopError:
		return "end_turn"
	}
	return "end_turn"
}
