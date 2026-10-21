package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// adapter 实现 Messages 的编解码。
type adapter struct{}

// Adapter 返回 Messages 适配器。
func Adapter() ir.Adapter { return adapter{} }

func (adapter) Name() ir.Protocol { return ir.ProtocolAnthropic }

// Capabilities 是 Messages 的能力自省。
//
// 为什么 Thinking/ThinkingSignature 为 true：thinking block 带 signature 回传是该协议的
// 原生语义（02-ir.md §3.5），Claude Code 在多轮里就依赖它。
// 为什么 Efforts 只有 low/high/max：Anthropic 只有 budget_tokens 连续值，档位归一阈值是
// 文档待定项，这里按临时表（<4096→low、<16384→high、≥16384→max）并双向记告警；
// off 对应 thinking.type=disabled。
// 为什么 Prefill 为 true：协议允许以 assistant 消息预填充结尾。
func (adapter) Capabilities() ir.Capabilities {
	return ir.Capabilities{
		Protocol:          ir.ProtocolAnthropic,
		Tools:             true,
		ParallelToolCalls: true,
		Images:            true,
		ImageURL:          true,
		Prefill:           true,
		Thinking:          true,
		ThinkingSignature: true,
		JSONObject:        false, // 无原生 json_object：§7.1 走工具模拟，不属协议能力
		JSONSchema:        false,
		StopSequences:     true,
		TopK:              true,
		Seed:              false, // 协议没有 seed 字段
		StreamUsage:       true,
		Efforts: map[ir.Effort]bool{
			ir.EffortOff:  true,
			ir.EffortLow:  true,
			ir.EffortHigh: true,
			ir.EffortMax:  true,
		},
		MaxStopSequences: 0, // 协议未规定上限
	}
}

// DecodeRequest 解析入站请求。
func (a adapter) DecodeRequest(body []byte) (*ir.Request, []ir.Warning, error) {
	var raw request
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, &ir.InvalidRequestError{Param: "body", Detail: "不是合法 JSON 对象：" + err.Error()}
	}
	var warns []ir.Warning
	req := &ir.Request{Model: raw.Model, Stream: raw.Stream}

	// 顶层 system → 一条 RoleSystem 消息（§2.2：系统提示在四协议里都位于对话之外）。
	// 为什么放在最前而不是保留原相对位置：Anthropic 的 system 本来就在对话之外，
	// 不存在"中途改系统提示"的历史形状；OpenAI 侧的 system 消息位置在 encode 时保序。
	if len(raw.System) > 0 && string(raw.System) != "null" {
		parts, err := decodeSystem(raw.System)
		if err != nil {
			return nil, warns, err
		}
		if len(parts) > 0 {
			req.Messages = append(req.Messages, ir.Message{Role: ir.RoleSystem, Parts: parts})
		}
	}

	msgs, w, err := decodeMessages(raw.Messages)
	warns = append(warns, w...)
	if err != nil {
		return nil, warns, err
	}
	req.Messages = append(req.Messages, msgs...)

	if len(raw.Messages) == 0 {
		return nil, warns, &ir.InvalidRequestError{Param: "messages", Detail: "消息不能为空"}
	}

	for i, t := range raw.Tools {
		if t.Name == "" {
			return nil, warns, &ir.InvalidRequestError{Param: fmt.Sprintf("tools[%d].name", i), Detail: "工具名不能为空"}
		}
		schema := "{}"
		if len(t.InputSchema) > 0 && string(t.InputSchema) != "null" {
			schema = string(t.InputSchema)
		}
		req.Tools = append(req.Tools, ir.ToolDef{Name: t.Name, Description: t.Description, Schema: schema})
		if len(t.CacheControl) > 0 {
			// prompt caching 是我们的候选不保证支持的旁路能力；忽略并告警，
			// 与 §8.4 白名单同性质（不改变模型"做了什么"，只可能改变计费）。
			warns = append(warns, ir.Warning{Param: fmt.Sprintf("tools[%d].cache_control", i), Message: "已忽略（不做 prompt 缓存中转）"})
		}
	}

	choice, err := decodeToolChoice(raw.ToolChoice)
	if err != nil {
		return nil, warns, err
	}
	req.Choice = choice

	req.Sampling.MaxOutput = raw.MaxTokens
	req.Sampling.Temperature = raw.Temperature
	req.Sampling.TopP = raw.TopP
	req.Sampling.TopK = raw.TopK
	req.Sampling.Stop = raw.StopSequences

	if raw.Metadata != nil && raw.Metadata.UserID != "" {
		// §8.4 白名单：滥用追踪用途，与模型行为无关。
		warns = append(warns, ir.Warning{Param: "metadata.user_id", Message: "已忽略（不参与上游编码）"})
	}

	if raw.Thinking != nil {
		e, w := decodeThinking(raw.Thinking)
		req.Effort = e
		warns = append(warns, w...)
	}

	if err := req.Validate(); err != nil {
		return nil, warns, err
	}
	return req, warns, nil
}

// decodeThinking 把 thinking.budget_tokens 映射到 IR 档位。
//
// 阈值是临时表（02-ir.md §7.1 待定项）：映射必然有信息损失，所以每次都记告警，
// 让"档位是怎么来的"在审计里可查，而不是等真实上游分布校准完才发现没人记录。
func decodeThinking(tc *thinkingConfig) (ir.Effort, []ir.Warning) {
	switch strings.ToLower(tc.Type) {
	case "disabled":
		return ir.EffortOff, nil
	case "enabled":
		budget := 0
		if tc.BudgetTokens != nil {
			budget = *tc.BudgetTokens
		}
		var effort ir.Effort
		switch {
		case budget == 0:
			effort = ir.EffortLow
		case budget < 4096:
			effort = ir.EffortLow
		case budget < 16384:
			effort = ir.EffortHigh
		default:
			effort = ir.EffortMax
		}
		return effort, []ir.Warning{{
			Param:   "thinking.budget_tokens",
			Message: fmt.Sprintf("budget=%d 按临时表映射为档位 %s（阈值为待定项）", budget, effort),
		}}
	}
	return ir.EffortUnknown, []ir.Warning{{
		Param:   "thinking.type",
		Message: "未知 thinking.type=" + tc.Type + "，按 unknown 档位交给能力匹配",
	}}
}

func decodeToolChoice(raw json.RawMessage) (ir.ToolChoice, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return ir.ToolChoice{}, nil
	}
	var s struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ir.ToolChoice{}, &ir.InvalidRequestError{Param: "tool_choice", Detail: "形状无法识别"}
	}
	switch s.Type {
	case "auto":
		return ir.ToolChoice{Mode: ir.ToolChoiceAuto}, nil
	case "any":
		return ir.ToolChoice{Mode: ir.ToolChoiceRequired}, nil
	case "none":
		return ir.ToolChoice{Mode: ir.ToolChoiceNone}, nil
	case "tool":
		if s.Name == "" {
			return ir.ToolChoice{}, &ir.InvalidRequestError{Param: "tool_choice.name", Detail: "type=tool 必须给出工具名"}
		}
		return ir.ToolChoice{Mode: ir.ToolChoiceSpecific, Name: s.Name}, nil
	}
	return ir.ToolChoice{}, &ir.InvalidRequestError{Param: "tool_choice.type", Detail: "未知取值 " + s.Type}
}

// decodeSystem 接受 string 或 text block 数组两种形状（新版 SDK 会发数组）。
func decodeSystem(raw json.RawMessage) ([]ir.Part, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return []ir.Part{ir.TextPart{Idx: 0, Text: s}}, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, &ir.InvalidRequestError{Param: "system", Detail: "既不是字符串也不是 block 数组"}
	}
	var parts []ir.Part
	for i, b := range blocks {
		if b.Type != "text" {
			return nil, ir.NewUnsupported(ir.ProtocolAnthropic, ir.Capability("system_block:"+b.Type), i,
				"system 数组里只支持 text block")
		}
		if b.Text == "" {
			continue
		}
		parts = append(parts, ir.TextPart{Idx: i, Text: b.Text})
	}
	return parts, nil
}

// decodeMessages 把 messages 转成 IR。
//
// 为什么 user 消息里的 tool_result 原位保留：Anthropic 的形状本来就是"工具结果是 user
// 消息里的 block"，这正是 IR 选择的规范形状（§2.2），入站方向是无损的。
func decodeMessages(in []message) ([]ir.Message, []ir.Warning, error) {
	var out []ir.Message
	var warns []ir.Warning

	for i, m := range in {
		var role ir.Role
		switch m.Role {
		case "user":
			role = ir.RoleUser
		case "assistant":
			role = ir.RoleAssistant
		case "system":
			// 兼容形态：个别客户端把 system 塞进 messages（文档 §2.2 的表里 Anthropic
			// 不该有这种形状）。按 IR 规范提升为 RoleSystem 并告警，而不是拒绝——
			// 语义完全能表达，拒绝只是形式主义。
			role = ir.RoleSystem
			warns = append(warns, ir.Warning{
				Param:   fmt.Sprintf("messages[%d].role", i),
				Message: "messages 里的 system 已提升为系统消息（协议规定应放顶层）",
			})
		default:
			return nil, warns, &ir.InvalidRequestError{
				Param:  fmt.Sprintf("messages[%d].role", i),
				Detail: "未知角色 " + m.Role,
			}
		}
		parts, err := decodeContent(m.Content, i)
		if err != nil {
			return nil, warns, err
		}
		out = append(out, ir.Message{Role: role, Parts: parts})
	}
	return out, warns, nil
}

// decodeContent 解析 content：字符串或 block 数组。
func decodeContent(raw json.RawMessage, msgIdx int) ([]ir.Part, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content", msgIdx), Detail: "不能为空"}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil // §3.1：空文本允许存在但不参与编码
		}
		return []ir.Part{ir.TextPart{Idx: 0, Text: s}}, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content", msgIdx), Detail: "既不是字符串也不是数组"}
	}
	parts := make([]ir.Part, 0, len(blocks))
	for j, b := range blocks {
		part, err := decodeBlock(b, msgIdx, j)
		if err != nil {
			return nil, err
		}
		if part != nil {
			parts = append(parts, part)
		}
	}
	return parts, nil
}

func decodeBlock(b contentBlock, msgIdx, j int) (ir.Part, error) {
	switch b.Type {
	case "text":
		return ir.TextPart{Idx: j, Text: b.Text}, nil

	case "image":
		if b.Source == nil {
			return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content[%d].source", msgIdx, j), Detail: "图片缺少 source"}
		}
		switch b.Source.Type {
		case "base64":
			if b.Source.MediaType == "" {
				return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content[%d].source.media_type", msgIdx, j), Detail: "base64 图片必须给出 media_type"}
			}
			data, err := base64.StdEncoding.DecodeString(b.Source.Data)
			if err != nil {
				return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content[%d].source.data", msgIdx, j), Detail: "base64 解码失败：" + err.Error()}
			}
			return ir.ImagePart{Idx: j, MIME: b.Source.MediaType, Data: data, Source: ir.ImageInline}, nil
		case "url":
			if b.Source.URL == "" {
				return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content[%d].source.url", msgIdx, j), Detail: "url 图片来源缺少 url"}
			}
			return ir.ImagePart{Idx: j, Source: ir.ImageURL, URL: b.Source.URL}, nil
		default:
			return nil, ir.NewUnsupported(ir.ProtocolAnthropic, ir.CapImageInput, j,
				fmt.Sprintf("messages[%d].content[%d] 的 source.type=%q 无法识别", msgIdx, j, b.Source.Type))
		}

	case "tool_use":
		if b.ID == "" {
			return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content[%d].id", msgIdx, j), Detail: "工具调用缺少 id"}
		}
		// Arguments 保存 JSON 原文、不重新序列化（§3.3）：模型输出被截断时 input 可能非法，
		// 原样透传让客户端自己决定，而不是我们报 500。
		args := string(b.Input)
		if len(b.Input) == 0 || strings.TrimSpace(args) == "" {
			args = "{}"
		}
		return ir.ToolCallPart{Idx: j, ID: b.ID, Name: b.Name, Arguments: args}, nil

	case "tool_result":
		if b.ToolUseID == "" {
			return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content[%d].tool_use_id", msgIdx, j), Detail: "工具结果缺少 tool_use_id"}
		}
		sub, err := decodeToolResultContent(b.Content, msgIdx, j)
		if err != nil {
			return nil, err
		}
		isErr := b.IsError != nil && *b.IsError
		return ir.ToolResultPart{Idx: j, ToolCallID: b.ToolUseID, Parts: sub, IsError: isErr}, nil

	case "thinking":
		return ir.ThinkingPart{Idx: j, Text: b.Thinking, Signature: b.Signature}, nil

	case "redacted_thinking":
		// 上游明确"有思考但内容被隐去"——IR 用 Redacted 表达这个三态（§3.5）。
		// 密文借用 Signature 字段承载：IR §10 规定只加不改，与其给 ThinkingPart 加第二个
		// "opaque 载荷"字段，不如复用现有字段的"不透明回传"语义——两者在协议里的作用完全
		// 相同（我们看不懂内容，只需要原样送回）。编码方向按 Redacted 分支还原 data。
		return ir.ThinkingPart{Idx: j, Redacted: true, Signature: b.Data}, nil

	case "server_tool_use", "web_search_tool_result":
		// Anthropic 服务侧工具：我们既不是发起方也无法代执行，拒绝而不是丢（§10）。
		return nil, ir.NewUnsupported(ir.ProtocolAnthropic, ir.Capability("server_tool:"+b.Type), j,
			"服务侧工具块无法转发")

	default:
		return nil, ir.NewUnsupported(ir.ProtocolAnthropic, ir.Capability("content_block:"+b.Type), j,
			fmt.Sprintf("messages[%d].content[%d] 的类型 %q 无法表达", msgIdx, j, b.Type))
	}
}

// decodeToolResultContent 解析 tool_result 的 content：字符串、block 数组或缺失。
func decodeToolResultContent(raw json.RawMessage, msgIdx, j int) ([]ir.Part, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return []ir.Part{ir.TextPart{Idx: 0, Text: s}}, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, &ir.InvalidRequestError{
			Param:  fmt.Sprintf("messages[%d].content[%d].content", msgIdx, j),
			Detail: "tool_result 的 content 既不是字符串也不是数组",
		}
	}
	parts := make([]ir.Part, 0, len(blocks))
	for k, b := range blocks {
		if b.Type == "tool_result" || b.Type == "tool_use" {
			return nil, &ir.InvalidRequestError{
				Param:  fmt.Sprintf("messages[%d].content[%d].content[%d].type", msgIdx, j, k),
				Detail: "工具结果里不能嵌套工具调用/结果",
			}
		}
		part, err := decodeBlock(b, msgIdx, k)
		if err != nil {
			return nil, err
		}
		if part != nil {
			parts = append(parts, part)
		}
	}
	return parts, nil
}
