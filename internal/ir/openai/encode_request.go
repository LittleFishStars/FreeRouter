package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// EncodeRequest 把 IR 编码成上游 Chat Completions 请求体。
//
// 顺序很重要：先做能力复核（Check），再编码。先编码后复核会诱使编码器"顺手丢掉不支持的东西"
// —— 那正是 C6 禁止的。复核通过后，编码器可以假定每个字段都有落脚点。
func (a adapter) EncodeRequest(req *ir.Request, opt ir.EncodeOptions) ([]byte, []ir.Warning, error) {
	if req == nil {
		return nil, nil, &ir.InvalidRequestError{Param: "request", Detail: "请求为空"}
	}
	warns, err := a.Capabilities().Check(req)
	if err != nil {
		return nil, warns, err
	}

	model := opt.UpstreamModel
	if model == "" {
		model = req.Model
	}
	out := request{Model: model, Stream: req.Stream}

	msgs, err := encodeMessages(req.Messages)
	if err != nil {
		return nil, warns, err
	}
	out.Messages = msgs

	for _, t := range req.Tools {
		params := json.RawMessage(t.Schema)
		if len(params) == 0 || !json.Valid(params) {
			// 工具 schema 不合法是我们能在本地发现的错误；透传给上游只会得到指向上游的 400，
			// 排障成本高得多。
			return nil, warns, &ir.InvalidRequestError{Param: "tools." + t.Name + ".parameters", Detail: "JSON Schema 不是合法 JSON"}
		}
		var strict *bool
		if t.Strict {
			strict = ir.Ptr(true)
		}
		out.Tools = append(out.Tools, tool{
			Type: "function",
			Function: toolFunction{
				Name: t.Name, Description: t.Description, Params: params, Strict: strict,
			},
		})
	}

	choice, err := encodeToolChoice(req.Choice)
	if err != nil {
		return nil, warns, err
	}
	out.ToolChoice = choice

	out.Temperature = req.Sampling.Temperature
	out.TopP = req.Sampling.TopP
	// 统一用 max_tokens：兼容性最好——新名字在大量第三方兼容上游上还不认，
	// 而旧名字在新端点仍被接受（OpenAI 官方两个都收）。
	out.MaxTokens = req.Sampling.MaxOutput
	out.Seed = req.Sampling.Seed
	if len(req.Sampling.Stop) > 0 {
		out.Stop, _ = json.Marshal(req.Sampling.Stop)
	}

	if req.Format != nil {
		switch req.Format.Kind {
		case ir.FormatText:
			out.ResponseFormat = &responseFormat{Type: "text"}
		case ir.FormatJSONObject:
			out.ResponseFormat = &responseFormat{Type: "json_object"}
		case ir.FormatJSONSchema:
			out.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: &jsonSchema{
				Name: req.Format.Name, Schema: json.RawMessage(req.Format.Schema), Strict: ir.Ptr(true),
			}}
		}
	}

	if req.Effort != ir.EffortUnspecified {
		effort, ok := encodeEffort(req.Effort)
		if !ok {
			// 走到这里说明 Check 漏了一项；宁可报错也不要把档位猜成别的值。
			return nil, warns, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapReasoningEffort, -1,
				"档位 "+string(req.Effort)+" 无法编码")
		}
		out.ReasoningEffort = ir.Ptr(effort)
	}

	if req.Stream {
		// 我们总是想要 usage（I4 与计量都需要）。显式发这个开关，而不是指望上游默认给。
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil, warns, &ir.InvalidRequestError{Param: "body", Detail: "编码失败：" + err.Error()}
	}
	return body, warns, nil
}

// encodeEffort 只编码 IR 里语义确定的档位；max/unknown 走不到这里（能力复核已拦）。
func encodeEffort(e ir.Effort) (string, bool) {
	switch e {
	case ir.EffortOff:
		return "none", true
	case ir.EffortLow:
		return "low", true
	case ir.EffortHigh:
		return "high", true
	case ir.EffortMax:
		return "high", true // 只在候选声明了 max 时才会到；OpenAI 侧不存在该值，故降为 high
	}
	return "", false
}

func encodeToolChoice(c ir.ToolChoice) (json.RawMessage, error) {
	switch c.Mode {
	case "":
		return nil, nil
	case ir.ToolChoiceAuto:
		return json.RawMessage(`"auto"`), nil
	case ir.ToolChoiceNone:
		return json.RawMessage(`"none"`), nil
	case ir.ToolChoiceRequired:
		return json.RawMessage(`"required"`), nil
	case ir.ToolChoiceSpecific:
		if c.Name == "" {
			return nil, &ir.InvalidRequestError{Param: "tool_choice.name", Detail: "指定工具选择必须给出工具名"}
		}
		b, err := json.Marshal(map[string]any{
			"type":     "function",
			"function": map[string]string{"name": c.Name},
		})
		return b, err
	}
	return nil, &ir.InvalidRequestError{Param: "tool_choice", Detail: "未知模式 " + string(c.Mode)}
}

// encodeMessages 把 IR 消息转成 OpenAI 形状。
//
// 两条关键映射：
//   - RoleSystem → role=system 的消息（位置保持原样，因为 OpenAI 允许系统消息出现在任意位置，
//     而 Anthropic 只允许在顶层；保序能让"中途改变系统提示"的会话不被破坏）。
//   - 一条 user 消息里混着文本与 tool_result → 拆成"一条 user 文本消息 + 若干 role=tool 消息"。
//     这是唯一有损的方向：拆分后顺序信息由 tool_call_id 保证，语义不失。
func encodeMessages(msgs []ir.Message) ([]message, error) {
	var out []message
	for mi, m := range msgs {
		switch m.Role {
		case ir.RoleSystem:
			txt := m.Text()
			b, _ := json.Marshal(txt)
			out = append(out, message{Role: "system", Content: b})

		case ir.RoleUser:
			var parts []contentPart
			var toolResults []message
			var calls []toolCall
			haveToolCall := false
			for _, p := range m.Parts {
				switch v := p.(type) {
				case ir.TextPart:
					if v.Text == "" {
						continue // §3.1：空文本不参与上游编码
					}
					parts = append(parts, textPart(v.Text))
				case ir.ImagePart:
					cp, err := encodeImagePart(v)
					if err != nil {
						return nil, err
					}
					parts = append(parts, cp)
				case ir.ToolResultPart:
					tm, err := encodeToolResultMessage(v)
					if err != nil {
						return nil, err
					}
					toolResults = append(toolResults, tm)
				case ir.ToolCallPart:
					// 出现在 user 消息里的工具调用不是合法形状；静默丢弃会让模型看不到它，
					// 因此显式报错。
					return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d]", mi),
						Detail: "user 消息里不能有工具调用"}
				case ir.ThinkingPart:
					// 能力复核已经拦过；保留这个分支是为了将来 CapThinking 打开时编译器能提醒。
					return nil, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapThinking, v.Idx, "")
				}
			}
			_ = haveToolCall
			_ = calls
			if len(parts) > 0 {
				b, err := json.Marshal(parts)
				if err != nil {
					return nil, err
				}
				out = append(out, message{Role: "user", Content: b})
			}
			out = append(out, toolResults...)

		case ir.RoleAssistant:
			var txt string
			var calls []toolCall
			for _, p := range m.Parts {
				switch v := p.(type) {
				case ir.TextPart:
					txt += v.Text
				case ir.ToolCallPart:
					calls = append(calls, toolCall{
						ID: v.ID, Type: "function",
						Function: toolFunction{Name: v.Name, Arguments: v.Arguments},
					})
				case ir.ImagePart:
					return nil, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapImageOutput, v.Idx,
						"OpenAI 的 assistant 消息不能携带图片")
				case ir.ThinkingPart:
					return nil, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapThinking, v.Idx, "")
				case ir.ToolResultPart:
					return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d]", mi),
						Detail: "assistant 消息里不能有工具结果"}
				}
			}
			m2 := message{Role: "assistant", ToolCalls: calls}
			if txt != "" {
				b, _ := json.Marshal(txt)
				m2.Content = b
			}
			out = append(out, m2)

		default:
			return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].role", mi), Detail: "未知角色 " + string(m.Role)}
		}
	}
	return out, nil
}

func textPart(s string) contentPart { return contentPart{Type: "text", Text: ir.Ptr(s)} }

func encodeImagePart(v ir.ImagePart) (contentPart, error) {
	var url string
	switch v.Source {
	case ir.ImageURL:
		url = v.URL
	case ir.ImageInline, "":
		if v.MIME == "" {
			return contentPart{}, &ir.InvalidRequestError{Param: "image", Detail: "内联图片必须给出 MIME"}
		}
		url = "data:" + v.MIME + ";base64," + base64.StdEncoding.EncodeToString(v.Data)
	default:
		return contentPart{}, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapImageInput, v.Idx,
			"未知图片来源 "+string(v.Source))
	}
	ref := &imageURLRef{URL: url}
	if v.Detail != "" {
		ref.Detail = v.Detail
	}
	return contentPart{Type: "image_url", ImageURL: ref}, nil
}

func encodeToolResultMessage(v ir.ToolResultPart) (message, error) {
	// OpenAI 的 tool 消息只接受字符串 content；tool_result 里的图片无处安放。
	// 这里显式报错而不是悄悄只取文本——丢图片正是 PLAN 6.1 明令禁止的。
	for _, sub := range v.Parts {
		if img, ok := sub.(ir.ImagePart); ok {
			return message{}, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapImageToolResult, img.Idx,
				"OpenAI 的 tool 消息 content 只能是字符串，无法携带图片结果")
		}
	}
	txt := v.Text()
	b, _ := json.Marshal(txt)
	return message{Role: "tool", Content: b, ToolCallID: v.ToolCallID}, nil
}
