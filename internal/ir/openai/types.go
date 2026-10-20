// Package openai 实现 OpenAI Chat Completions 协议与内部 IR 的双向编解码。
//
// 为什么单独成包而不并进 ir：OpenAI 兼容是"最脏"的一族——大量第三方上游自称兼容但字段
// 有出入（max_tokens vs max_completion_tokens、tool_call id 分片时机）。把这些方言集中
// 在一个包里，方言的代价就局限在一处，不会渗进路由核心。
package openai

import (
	"encoding/json"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 本文件是 Chat Completions 的线上类型（wire types）。
//
// 为什么每个可选字段都用指针或 json.RawMessage：三态。`temperature: 0` 与"没给"语义不同，
// 用 float64 零值会把两者混为一谈；`content` 既可能是字符串也可能是数组，用 RawMessage
// 才能在解码时分清"字段缺失"与"字段是空数组"。

// request 是 /v1/chat/completions 请求体。
type request struct {
	Model               string          `json:"model"`
	Messages            []message       `json:"messages"`
	Tools               []tool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	Seed                *int64          `json:"seed,omitempty"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort     *string         `json:"reasoning_effort,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *streamOptions  `json:"stream_options,omitempty"`
	N                   *int            `json:"n,omitempty"`
	PresencePenalty     *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty,omitempty"`
	Logprobs            *bool           `json:"logprobs,omitempty"`
	TopLogprobs         *int            `json:"top_logprobs,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	User                string          `json:"user,omitempty"`
}

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []toolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Refusal    *string         `json:"refusal,omitempty"`
}

// contentPart 是消息内容数组里的一个元素。只声明我们认识的类型；
// 其余类型留在原始字段里，由解码层判为不支持而不是静默跳过。
type contentPart struct {
	Type     string       `json:"type"`
	Text     *string      `json:"text,omitempty"`
	ImageURL *imageURLRef `json:"image_url,omitempty"`
}

type imageURLRef struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type toolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Arguments   string          `json:"arguments"`
	Strict      *bool           `json:"strict,omitempty"`
	Params      json.RawMessage `json:"parameters,omitempty"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
	// 顶层 strict 是较新的方言，部分上游放在 function 内，两处都读。
	Strict *bool `json:"strict,omitempty"`
}

type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *jsonSchema `json:"json_schema,omitempty"`
}

type jsonSchema struct {
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict *bool           `json:"strict,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// response 是非流式响应体。
type response struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage,omitempty"`
}

type choice struct {
	Index        int             `json:"index"`
	Message      *message        `json:"message,omitempty"`
	Delta        *message        `json:"delta,omitempty"`
	FinishReason *string         `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

type usage struct {
	PromptTokens            *int             `json:"prompt_tokens,omitempty"`
	CompletionTokens        *int             `json:"completion_tokens,omitempty"`
	TotalTokens             *int             `json:"total_tokens,omitempty"`
	PromptTokensDetails     *promptDetails   `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *completionToken `json:"completion_tokens_details,omitempty"`
}

type promptDetails struct {
	CachedTokens *int `json:"cached_tokens,omitempty"`
	AudioTokens  *int `json:"audio_tokens,omitempty"`
}

type completionToken struct {
	ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
	AudioTokens     *int `json:"audio_tokens,omitempty"`
}

// errorEnvelope 是上游错误体；OpenAI 在流里也用同一形状。
type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Param   any    `json:"param"`
	} `json:"error"`
}

// finishReasonToStop 把 OpenAI 的 finish_reason 归一到 IR。
//
// 为什么未知值映射为 StopEndTurn 而不是留空：StopReason 会进审计与计费归因，
// 留空会让"这次为什么结束"永远查不出来；而 end_turn 是"正常说完"的保守解释，
// 同时把原始值写进告警，信息不丢。
func finishReasonToStop(s string) (ir.StopReason, bool) {
	switch s {
	case "stop":
		return ir.StopEndTurn, true
	case "length":
		return ir.StopMaxTokens, true
	case "tool_calls", "function_call":
		return ir.StopToolUse, true
	case "content_filter":
		return ir.StopContentFilter, true
	case "":
		return ir.StopUnspecified, true
	}
	return ir.StopEndTurn, false
}

// stopToFinishReason 是反向映射。
func stopToFinishReason(s ir.StopReason) string {
	switch s {
	case ir.StopMaxTokens:
		return "length"
	case ir.StopToolUse:
		return "tool_calls"
	case ir.StopContentFilter:
		return "content_filter"
	case ir.StopRefusal:
		return "content_filter"
	case ir.StopError:
		return "stop"
	}
	return "stop"
}
