package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// EncodeRequest 把 IR 编码成上游 Messages 请求体。
//
// 顺序与 openai 侧一致：先能力复核（Check）再编码——复核通过后编码器可以假定
// 每个字段都有落脚点，不存在"顺手丢掉不支持的东西"的诱惑（C6）。
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

	// max_tokens 协议必填（01-inbound.md §6.1）。入站 Anthropic 客户端必须给；
	// 从别的协议转来时（IR 里可选），这里补一个保守默认而不是拒绝——
	// 为什么不算静默降级：补的是"输出上限"而非改变模型行为语义，且必然记告警，
	// 审计能看到"这次的上限是我们补的"。
	if req.Sampling.MaxOutput != nil {
		out.MaxTokens = req.Sampling.MaxOutput
	} else {
		out.MaxTokens = ir.Ptr(defaultMaxTokens)
		warns = append(warns, ir.Warning{Param: "max_tokens",
			Message: fmt.Sprintf("IR 未指定输出上限，补默认 %d（Anthropic 协议必填）", defaultMaxTokens)})
	}

	sys, msgs, w, err := encodeMessages(req.Messages)
	warns = append(warns, w...)
	if err != nil {
		return nil, warns, err
	}
	if len(sys) > 0 {
		out.System = sys
	}
	out.Messages = msgs

	for _, t := range req.Tools {
		schema := json.RawMessage(t.Schema)
		if len(schema) == 0 || !json.Valid(schema) {
			return nil, warns, &ir.InvalidRequestError{Param: "tools." + t.Name + ".input_schema",
				Detail: "JSON Schema 不是合法 JSON"}
		}
		out.Tools = append(out.Tools, tool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}

	tc, err := encodeToolChoice(req.Choice)
	if err != nil {
		return nil, warns, err
	}
	out.ToolChoice = tc

	out.Temperature = req.Sampling.Temperature
	out.TopP = req.Sampling.TopP
	out.TopK = req.Sampling.TopK
	out.StopSequences = req.Sampling.Stop

	if req.Effort != ir.EffortUnspecified {
		cfg, w := encodeThinking(req.Effort)
		if cfg != nil {
			out.Thinking = cfg
			warns = append(warns, w...)
		}
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil, warns, &ir.InvalidRequestError{Param: "body", Detail: "编码失败：" + err.Error()}
	}
	return body, warns, nil
}

// defaultMaxTokens 是跨协议转来时补的输出上限。取一个"不至于截断正常回答、
// 又不会大到超出常见候选窗口"的中间值；真正的上限裁剪是候选能力的事。
const defaultMaxTokens = 4096

// encodeThinking 把 IR 档位映射回 budget_tokens。
//
// 阈值表与 decodeThinking 同一套待定项来源（02-ir.md §7.1）：双向都记告警，
// 保证"档位↔预算"的换算损失在审计里始终可见。off → thinking.type=disabled，
// 显式关闭而不是省略字段：省略时上游模型可能按自己的默认开启思考。
func encodeThinking(e ir.Effort) (*thinkingConfig, []ir.Warning) {
	switch e {
	case ir.EffortOff:
		return &thinkingConfig{Type: "disabled"}, nil
	case ir.EffortLow:
		return &thinkingConfig{Type: "enabled", BudgetTokens: ir.Ptr(2048)},
			[]ir.Warning{{Param: "thinking.budget_tokens", Message: "档位 low 按临时表编码为 budget=2048"}}
	case ir.EffortHigh:
		return &thinkingConfig{Type: "enabled", BudgetTokens: ir.Ptr(8192)},
			[]ir.Warning{{Param: "thinking.budget_tokens", Message: "档位 high 按临时表编码为 budget=8192"}}
	case ir.EffortMax:
		return &thinkingConfig{Type: "enabled", BudgetTokens: ir.Ptr(32768)},
			[]ir.Warning{{Param: "thinking.budget_tokens", Message: "档位 max 按临时表编码为 budget=32768"}}
	}
	// unknown 在能力复核已被拦（候选 Efforts 表不含它）；走到这里是防御分支：
	// 宁可不发 thinking 字段并告警，也不猜一个预算。
	return nil, []ir.Warning{{Param: "effort", Message: "档位 " + string(e) + " 无法编码为 thinking，已省略该字段"}}
}

func encodeToolChoice(c ir.ToolChoice) (json.RawMessage, error) {
	switch c.Mode {
	case "":
		return nil, nil
	case ir.ToolChoiceAuto:
		return json.RawMessage(`{"type":"auto"}`), nil
	case ir.ToolChoiceNone:
		return json.RawMessage(`{"type":"none"}`), nil
	case ir.ToolChoiceRequired:
		return json.RawMessage(`{"type":"any"}`), nil
	case ir.ToolChoiceSpecific:
		if c.Name == "" {
			return nil, &ir.InvalidRequestError{Param: "tool_choice.name", Detail: "指定工具选择必须给出工具名"}
		}
		b, err := json.Marshal(map[string]string{"type": "tool", "name": c.Name})
		return b, err
	}
	return nil, &ir.InvalidRequestError{Param: "tool_choice", Detail: "未知模式 " + string(c.Mode)}
}

// encodeMessages 把 IR 消息转成 Anthropic 形状。
//
// 两条关键映射（都是 openai 侧对应操作的镜像）：
//   - RoleSystem 消息 → 顶层 system 字符串。多条系统消息拼接（协议只有一个槽位），
//     拼接而非拒绝：语义等价（都是"对话之外的指令"），顺序按出现序用换行分隔。
//   - role=tool 的碎片在 IR 里已经是 user 消息内的 tool_result part（解析层合并过），
//     这里原位输出，无需重组——这就是 §2.2 选 Anthropic 形状做规范形状的红利。
func encodeMessages(msgs []ir.Message) (json.RawMessage, []message, []ir.Warning, error) {
	var sysText string
	var out []message

	for mi, m := range msgs {
		switch m.Role {
		case ir.RoleSystem:
			sysText += m.Text() + "\n"

		case ir.RoleUser, ir.RoleAssistant:
			role := "user"
			if m.Role == ir.RoleAssistant {
				role = "assistant"
			}
			blocks := make([]contentBlock, 0, len(m.Parts))
			for _, p := range m.Parts {
				b, err := encodePart(p, mi)
				if err != nil {
					return nil, nil, nil, err
				}
				if b != nil {
					blocks = append(blocks, *b)
				}
			}
			// content 为空数组的消息（比如全空文本的占位消息）在 Anthropic 是 400；
			// 补一个空 text block 保序，而不是丢消息——丢了会破坏 user/assistant 交替约束。
			if len(blocks) == 0 {
				blocks = append(blocks, contentBlock{Type: "text", Text: ""})
			}
			c, err := json.Marshal(blocks)
			if err != nil {
				return nil, nil, nil, err
			}
			out = append(out, message{Role: role, Content: c})

		default:
			return nil, nil, nil, &ir.InvalidRequestError{
				Param:  fmt.Sprintf("messages[%d].role", mi),
				Detail: "未知角色 " + string(m.Role),
			}
		}
	}

	var warns []ir.Warning
	var sys json.RawMessage
	if sysText != "" {
		// 去掉拼接引入的最后一个换行；这不是内容换行。
		for len(sysText) > 0 && sysText[len(sysText)-1] == '\n' {
			sysText = sysText[:len(sysText)-1]
		}
		if sysText != "" {
			b, err := json.Marshal(sysText)
			if err != nil {
				return nil, nil, nil, err
			}
			sys = b
		}
	}
	return sys, out, warns, nil
}

// encodePart 把一个 IR part 编成 Anthropic block；nil 表示"空文本，省略"。
func encodePart(p ir.Part, mi int) (*contentBlock, error) {
	switch v := p.(type) {
	case ir.TextPart:
		if v.Text == "" {
			return nil, nil // §3.1：空文本不参与编码
		}
		return &contentBlock{Type: "text", Text: v.Text}, nil

	case ir.ImagePart:
		b, err := encodeImage(v)
		if err != nil {
			return nil, err
		}
		return &contentBlock{Type: "image", Source: b}, nil

	case ir.ToolCallPart:
		// Arguments 是 JSON 原文（§3.3）；Anthropic 的 input 是对象，需要解析回结构。
		// 解析失败说明上游给的参数本来就不是合法 JSON（模型被截断）：原样放进 RawMessage
		// 会编出非法请求体，所以这里报错——这是"我们的请求体会坏"，不是"透传客户端"方向。
		var input json.RawMessage
		args := v.Arguments
		if args == "" {
			input = json.RawMessage("{}")
		} else if !json.Valid([]byte(args)) {
			return nil, &ir.InvalidRequestError{
				Param:  fmt.Sprintf("messages[%d].parts.tool_call[%s].arguments", mi, v.ID),
				Detail: "工具参数不是合法 JSON，无法编码为 Anthropic 的 input 对象",
			}
		} else {
			input = json.RawMessage(args)
		}
		return &contentBlock{Type: "tool_use", ID: v.ID, Name: v.Name, Input: input}, nil

	case ir.ToolResultPart:
		return encodeToolResult(v, mi)

	case ir.ThinkingPart:
		if v.Redacted {
			// 密文借用 Signature 字段（见 request.go 的说明）：还原到 data。
			return &contentBlock{Type: "redacted_thinking", Data: v.Signature}, nil
		}
		return &contentBlock{Type: "thinking", Thinking: v.Text, Signature: v.Signature}, nil
	}
	return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].parts", mi),
		Detail: "未知 part 类型 " + string(p.PartType())}
}

func encodeImage(v ir.ImagePart) (*source, error) {
	switch v.Source {
	case ir.ImageURL:
		if v.URL == "" {
			return nil, &ir.InvalidRequestError{Param: "image", Detail: "URL 图片来源缺少 url"}
		}
		return &source{Type: "url", URL: v.URL}, nil
	case ir.ImageInline, "":
		if v.MIME == "" {
			return nil, &ir.InvalidRequestError{Param: "image", Detail: "内联图片必须给出 MIME"}
		}
		return &source{
			Type:      "base64",
			MediaType: v.MIME,
			Data:      base64.StdEncoding.EncodeToString(v.Data),
		}, nil
	}
	return nil, ir.NewUnsupported(ir.ProtocolAnthropic, ir.CapImageInput, v.Idx,
		"未知图片来源 "+string(v.Source))
}

// encodeToolResult 把工具结果编回 user 消息里的 tool_result block。
//
// 图片结果原生支持（Anthropic 是四协议里唯一 tool_result 能带图的——IR 选它做规范
// 形状的原因之一）；但 thinking 不能出现在结果里，显式拒绝。
func encodeToolResult(v ir.ToolResultPart, mi int) (*contentBlock, error) {
	var sub []contentBlock
	var textOnly []string
	hasImage := false
	for _, p := range v.Parts {
		switch q := p.(type) {
		case ir.TextPart:
			if q.Text != "" {
				textOnly = append(textOnly, q.Text)
				sub = append(sub, contentBlock{Type: "text", Text: q.Text})
			}
		case ir.ImagePart:
			hasImage = true
			s, err := encodeImage(q)
			if err != nil {
				return nil, err
			}
			sub = append(sub, contentBlock{Type: "image", Source: s})
		case ir.ThinkingPart:
			return nil, ir.NewUnsupported(ir.ProtocolAnthropic, ir.CapThinking, q.Idx,
				"工具结果里不能携带思考内容")
		default:
			return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].parts.tool_result.content", mi),
				Detail: "工具结果里不允许 " + string(p.PartType())}
		}
	}
	b := &contentBlock{Type: "tool_result", ToolUseID: v.ToolCallID}
	if v.IsError {
		b.IsError = ir.Ptr(true)
	}
	switch {
	case len(sub) == 0:
		// 空结果：content 字段省略（协议允许），而不是空字符串——空串会让模型
		// 以为"工具返回了空文本"，与"工具没返回内容"语义不同（三态）。
	case !hasImage && len(textOnly) == 1:
		// 纯文本单块回字符串形状：比 block 数组更短，也更接近多数客户端的期望。
		c, err := json.Marshal(textOnly[0])
		if err != nil {
			return nil, err
		}
		b.Content = c
	default:
		c, err := json.Marshal(sub)
		if err != nil {
			return nil, err
		}
		b.Content = c
	}
	return b, nil
}
