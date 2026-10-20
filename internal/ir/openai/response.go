package openai

import (
	"encoding/json"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// DecodeResponse 解析非流式响应。
func (adapter) DecodeResponse(body []byte) (*ir.Response, error) {
	var raw response
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &ir.InvalidRequestError{Param: "body", Detail: "不是合法 JSON：" + err.Error()}
	}
	if len(raw.Choices) == 0 {
		// 没有 choices 的 200 响应是上游异常；当成空响应返回会让客户端以为"模型什么都没说"。
		return nil, &ir.InvalidRequestError{Param: "choices", Detail: "响应里没有 choices"}
	}
	// n>1 在入站已被拒绝（IR 只表示单条）；这里若上游仍给了多条，取第一条并忠实告知。
	resp := &ir.Response{ID: raw.ID, Model: raw.Model, CreatedAt: raw.Created}
	c := raw.Choices[0]
	if c.Message != nil {
		parts, err := decodeAssistantMessage(*c.Message)
		if err != nil {
			return nil, err
		}
		resp.Parts = parts
	}
	if c.FinishReason != nil {
		stop, known := finishReasonToStop(*c.FinishReason)
		resp.Stop = stop
		_ = known // 未知值的原始字符串已在 finishReasonToStop 的注释里说明处理策略
	}
	if raw.Usage != nil {
		resp.Usage = decodeUsage(raw.Usage)
	} else {
		resp.Usage = ir.Usage{Source: ir.UsageUnknown}
	}
	return resp, nil
}

// decodeAssistantMessage 把 assistant 的 message 转成 IR parts。
func decodeAssistantMessage(m message) ([]ir.Part, error) {
	var parts []ir.Part
	if len(m.Content) > 0 {
		sub, err := decodeContent(m.Content, 0)
		if err != nil {
			return nil, err
		}
		parts = append(parts, sub...)
	}
	for _, tc := range m.ToolCalls {
		if tc.ID == "" {
			// 非流式响应里 id 缺失是不可恢复的：客户端无法把结果对回调用。
			// 流式路径有重组器兜底，非流式没有，所以这里必须报错而不是合成。
			return nil, &ir.InvalidRequestError{Param: "choices[0].message.tool_calls[].id",
				Detail: "上游未给工具调用 id，无法与工具结果配对"}
		}
		parts = append(parts, ir.ToolCallPart{
			Idx: len(parts), ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
		})
	}
	return parts, nil
}

// decodeUsage 把 OpenAI 的 usage 归一。
//
// 三态保持：字段缺失 → nil；字段为 0 → 指向 0 的指针。这是计量正确性的基础（§9）。
func decodeUsage(u *usage) ir.Usage {
	out := ir.Usage{Source: ir.UsageUpstream}
	out.InputTokens = u.PromptTokens
	out.OutputTokens = u.CompletionTokens
	if u.PromptTokensDetails != nil {
		out.CacheReadTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

// EncodeResponse 把 IR 响应编码成 Chat Completions 响应体。
func (adapter) EncodeResponse(resp *ir.Response) ([]byte, error) {
	if resp == nil {
		return nil, &ir.InvalidRequestError{Param: "response", Detail: "响应为空"}
	}
	msg := message{Role: "assistant"}
	var text string
	for _, p := range resp.Parts {
		switch v := p.(type) {
		case ir.TextPart:
			text += v.Text
		case ir.ToolCallPart:
			msg.ToolCalls = append(msg.ToolCalls, toolCall{
				ID: v.ID, Type: "function",
				Function: toolFunction{Name: v.Name, Arguments: v.Arguments},
			})
		case ir.ThinkingPart:
			// 不把 thinking 当文本回显：会泄露模型内部状态并改变客户端行为（§3.5）。
			return nil, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapThinking, v.Idx,
				"响应里含思考内容，Chat Completions 无承载字段")
		case ir.ImagePart:
			return nil, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapImageOutput, v.Idx, "")
		}
	}
	if text != "" {
		b, _ := json.Marshal(text)
		msg.Content = b
	}
	finish := stopToFinishReason(resp.Stop)
	out := response{
		ID:      resp.ID,
		Object:  "chat.completion",
		Model:   resp.Model,
		Created: resp.CreatedAt,
		Choices: []choice{{Index: 0, Message: &msg, FinishReason: &finish}},
		Usage:   encodeUsage(resp.Usage),
	}
	if out.Created == 0 {
		// 上游没报 created（或转发自 Anthropic——该协议无此字段）：填当下而不是留 0。
		// SDK 把 created 当必填解析，缺字段直接报错、0 渲染成 1970，两种都不可用；
		// "完成时刻"是我们确实知道的唯一诚实时间。
		out.Created = time.Now().Unix()
	}
	if out.ID == "" {
		out.ID = "chatcmpl-freerouter"
	}
	return json.Marshal(out)
}

// encodeUsage 把 IR 用量编码回去。全 nil 时返回 nil：让客户端看到"没有 usage"，
// 而不是看到一堆 0 —— 后者会让客户端账本算错。
func encodeUsage(u ir.Usage) *usage {
	if u.InputTokens == nil && u.OutputTokens == nil {
		return nil
	}
	out := &usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens}
	// total 可以由分项相加得出，但我们只在两项都有时才合成它，避免把未知当 0。
	if u.InputTokens != nil && u.OutputTokens != nil {
		total := *u.InputTokens + *u.OutputTokens
		out.TotalTokens = &total
	}
	if u.CacheReadTokens != nil {
		out.PromptTokensDetails = &promptDetails{CachedTokens: u.CacheReadTokens}
	}
	if u.ReasoningTokens != nil {
		out.CompletionTokensDetails = &completionToken{ReasoningTokens: u.ReasoningTokens}
	}
	return out
}
