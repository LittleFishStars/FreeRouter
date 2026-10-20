package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// adapter 实现 Chat Completions 的编解码。
type adapter struct{}

// Adapter 返回 Chat Completions 适配器。
func Adapter() ir.Adapter { return adapter{} }

func (adapter) Name() ir.Protocol { return ir.ProtocolOpenAIChat }

// Capabilities 是 Chat Completions 的能力自省。
//
// 为什么 thinking 为 false：这个协议**没有**承载思考块的字段位。部分上游私加
// `reasoning_content`，但那是方言而非协议；把它当通用能力会让"回传 thinking 签名"在
// 大多数上游上静默失败。需要 thinking 的请求应当被过滤到 Anthropic 系候选或明确 400。
//
// 为什么档位只有 off/low/high：OpenAI 的 `reasoning_effort` 可取 minimal/low/medium/high，
// 与 IR 的 off/low/high/max 不是一一对应。只声明能无损表达的三档，是"缺能力就报错"而不是
// "把 max 悄悄降成 high"——后者正是 C6 禁止的静默降级。
func (adapter) Capabilities() ir.Capabilities {
	return ir.Capabilities{
		Protocol:          ir.ProtocolOpenAIChat,
		Tools:             true,
		ParallelToolCalls: true,
		Images:            true,
		ImageURL:          true,
		Prefill:           true,
		Thinking:          false,
		ThinkingSignature: false,
		JSONObject:        true,
		JSONSchema:        true,
		StopSequences:     true,
		TopK:              false,
		Seed:              true,
		StreamUsage:       true,
		Efforts:           map[ir.Effort]bool{ir.EffortOff: true, ir.EffortLow: true, ir.EffortHigh: true},
		MaxStopSequences:  4,
	}
}

// DecodeRequest 解析入站请求。
func (a adapter) DecodeRequest(body []byte) (*ir.Request, []ir.Warning, error) {
	var raw request
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, &ir.InvalidRequestError{Param: "body", Detail: "不是合法 JSON 对象：" + err.Error()}
	}
	req := &ir.Request{Model: raw.Model, Stream: raw.Stream}

	var warns []ir.Warning

	// n > 1 我们没有表示能力（IR 只有一条候选回复），必须拒绝而不是只取第一条。
	if raw.N != nil && *raw.N > 1 {
		return nil, warns, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapMultipleChoices, -1,
			fmt.Sprintf("n=%d 需要多条候选回复，IR 只表示单条", *raw.N))
	}

	// 这几个字段会改变采样行为，而 IR 没有表达位。丢弃它们等于静默改变生成结果，
	// 所以按 §8.3 报 400；白名单（§8.4）里没有它们，就不该开这个口子。
	if raw.PresencePenalty != nil {
		return nil, warns, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapPresencePenalty, -1, "")
	}
	if raw.FrequencyPenalty != nil {
		return nil, warns, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapFrequencyPenalty, -1, "")
	}
	if raw.Logprobs != nil && *raw.Logprobs {
		return nil, warns, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapLogprobs, -1, "")
	}
	if raw.TopLogprobs != nil {
		return nil, warns, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapLogprobs, -1, "")
	}
	if raw.User != "" {
		// §8.4 白名单：上游用它做滥用追踪，与模型行为无关。
		warns = append(warns, ir.Warning{Param: "user", Message: "已忽略（不参与上游编码）"})
	}
	if raw.StreamOptions != nil && !raw.StreamOptions.IncludeUsage {
		warns = append(warns, ir.Warning{Param: "stream_options.include_usage",
			Message: "我们总是回 usage，忽略该开关"})
	}

	msgs, w, err := decodeMessages(raw.Messages)
	warns = append(warns, w...)
	if err != nil {
		return nil, warns, err
	}
	req.Messages = msgs

	for i, t := range raw.Tools {
		if t.Type != "" && t.Type != "function" {
			return nil, warns, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapTools, -1,
				fmt.Sprintf("tools[%d].type=%q 不是 function", i, t.Type))
		}
		if t.Function.Name == "" {
			return nil, warns, &ir.InvalidRequestError{Param: fmt.Sprintf("tools[%d].function.name", i), Detail: "工具名不能为空"}
		}
		strict := t.Function.Strict != nil && *t.Function.Strict
		if t.Strict != nil && *t.Strict {
			strict = true
		}
		schema := "{}"
		if len(t.Function.Params) > 0 {
			schema = string(t.Function.Params)
		}
		req.Tools = append(req.Tools, ir.ToolDef{
			Name: t.Function.Name, Description: t.Function.Description, Schema: schema, Strict: strict,
		})
	}

	choice, err := decodeToolChoice(raw.ToolChoice)
	if err != nil {
		return nil, warns, err
	}
	req.Choice = choice

	// max_tokens 与 max_completion_tokens 并存时以后者为准（新字段语义更明确）。
	if raw.MaxCompletionTokens != nil {
		req.Sampling.MaxOutput = raw.MaxCompletionTokens
		if raw.MaxTokens != nil {
			warns = append(warns, ir.Warning{Param: "max_tokens", Message: "同时给了 max_completion_tokens，已以其为准"})
		}
	} else {
		req.Sampling.MaxOutput = raw.MaxTokens
	}
	req.Sampling.Temperature = raw.Temperature
	req.Sampling.TopP = raw.TopP
	req.Sampling.Seed = raw.Seed

	stop, err := decodeStop(raw.Stop)
	if err != nil {
		return nil, warns, err
	}
	req.Sampling.Stop = stop

	if raw.ResponseFormat != nil {
		switch raw.ResponseFormat.Type {
		case "text", "":
			req.Format = &ir.ResponseFormat{Kind: ir.FormatText}
		case "json_object":
			req.Format = &ir.ResponseFormat{Kind: ir.FormatJSONObject}
		case "json_schema":
			if raw.ResponseFormat.JSONSchema == nil {
				return nil, warns, &ir.InvalidRequestError{Param: "response_format.json_schema", Detail: "缺少 json_schema 字段"}
			}
			req.Format = &ir.ResponseFormat{
				Kind:   ir.FormatJSONSchema,
				Schema: string(raw.ResponseFormat.JSONSchema.Schema),
				Name:   raw.ResponseFormat.JSONSchema.Name,
			}
		default:
			return nil, warns, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapJSONSchema, -1,
				"未知 response_format.type="+raw.ResponseFormat.Type)
		}
	}

	if raw.ReasoningEffort != nil {
		req.Effort = decodeEffort(*raw.ReasoningEffort, &warns)
	}

	if len(raw.Messages) == 0 {
		return nil, warns, &ir.InvalidRequestError{Param: "messages", Detail: "消息不能为空"}
	}
	// 结构自洽性（工具结果配对等）在解析阶段就查：越早报错，排障成本越低。
	if err := req.Validate(); err != nil {
		return nil, warns, err
	}
	return req, warns, nil
}

// decodeEffort 把 OpenAI 的档位归一到 IR。
//
// medium 与 minimal 在 IR 里没有对应档位：归一后**必须**留告警，否则"用户要 medium 却拿到
// high"就变成了无迹可查的行为改变。
func decodeEffort(s string, warns *[]ir.Warning) ir.Effort {
	switch strings.ToLower(s) {
	case "none", "off":
		return ir.EffortOff
	case "minimal":
		*warns = append(*warns, ir.Warning{Param: "reasoning_effort", Message: "minimal 归一到 low"})
		return ir.EffortLow
	case "low":
		return ir.EffortLow
	case "medium":
		*warns = append(*warns, ir.Warning{Param: "reasoning_effort", Message: "medium 归一到 high"})
		return ir.EffortHigh
	case "high":
		return ir.EffortHigh
	case "xhigh", "max":
		return ir.EffortMax
	}
	// 不认识的档位不猜：交给能力匹配去过滤候选（§4）。
	return ir.EffortUnknown
}

func decodeToolChoice(raw json.RawMessage) (ir.ToolChoice, error) {
	if len(raw) == 0 {
		return ir.ToolChoice{}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto":
			return ir.ToolChoice{Mode: ir.ToolChoiceAuto}, nil
		case "none":
			return ir.ToolChoice{Mode: ir.ToolChoiceNone}, nil
		case "required", "any":
			// "any" 是 Anthropic 的说法，部分兼容层会直接透传；这里一并接受。
			return ir.ToolChoice{Mode: ir.ToolChoiceRequired}, nil
		}
		return ir.ToolChoice{}, &ir.InvalidRequestError{Param: "tool_choice", Detail: "未知取值 " + s}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ir.ToolChoice{}, &ir.InvalidRequestError{Param: "tool_choice", Detail: "形状无法识别"}
	}
	name := obj.Function.Name
	if name == "" {
		name = obj.Name
	}
	if obj.Type == "function" || name != "" {
		if name == "" {
			return ir.ToolChoice{}, &ir.InvalidRequestError{Param: "tool_choice.function.name", Detail: "指定工具选择必须给出工具名"}
		}
		return ir.ToolChoice{Mode: ir.ToolChoiceSpecific, Name: name}, nil
	}
	return ir.ToolChoice{}, &ir.InvalidRequestError{Param: "tool_choice", Detail: "形状无法识别"}
}

// decodeStop 接受 string 或 []string 两种形状。
func decodeStop(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return []string{s}, nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, &ir.InvalidRequestError{Param: "stop", Detail: "必须是字符串或字符串数组"}
}

// decodeMessages 把 OpenAI 的 messages 转成 IR。
//
// 关键决策：连续的 role=tool 消息**合并**成一条 RoleUser 消息里的多个 tool_result part。
// 为什么：Anthropic 要求一条 user 消息携带本轮全部工具结果；拆成多条消息再映射回去就要合并，
// 多一次有损往返。合并在解析侧做一次，之后所有协议都受益。
func decodeMessages(in []message) ([]ir.Message, []ir.Warning, error) {
	var out []ir.Message
	var warns []ir.Warning

	for i, m := range in {
		switch m.Role {
		case "system", "developer":
			// developer 是 system 的新名字；当成 system 处理并留告警，避免旧客户端拿不到行为。
			if m.Role == "developer" {
				warns = append(warns, ir.Warning{Param: fmt.Sprintf("messages[%d].role", i),
					Message: "developer 角色按 system 处理"})
			}
			parts, err := decodeContent(m.Content, i)
			if err != nil {
				return nil, warns, err
			}
			out = append(out, ir.Message{Role: ir.RoleSystem, Parts: parts})

		case "user":
			parts, err := decodeContent(m.Content, i)
			if err != nil {
				return nil, warns, err
			}
			out = append(out, ir.Message{Role: ir.RoleUser, Parts: parts})

		case "assistant":
			parts, err := decodeContent(m.Content, i)
			if err != nil {
				return nil, warns, err
			}
			for j, tc := range m.ToolCalls {
				if tc.ID == "" {
					return nil, warns, &ir.InvalidRequestError{
						Param:  fmt.Sprintf("messages[%d].tool_calls[%d].id", i, j),
						Detail: "历史里的工具调用必须有 id，否则无法与工具结果配对",
					}
				}
				parts = append(parts, ir.ToolCallPart{
					Idx: len(parts), ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
				})
			}
			out = append(out, ir.Message{Role: ir.RoleAssistant, Parts: parts})

		case "tool":
			if m.ToolCallID == "" {
				return nil, warns, &ir.InvalidRequestError{
					Param: fmt.Sprintf("messages[%d].tool_call_id", i), Detail: "工具消息缺少 tool_call_id",
				}
			}
			sub, err := decodeContent(m.Content, i)
			if err != nil {
				return nil, warns, err
			}
			tr := ir.ToolResultPart{Idx: 0, ToolCallID: m.ToolCallID, Parts: sub}
			// 与上一条 user 消息合并，保持"一条 user 消息带多个工具结果"的规范形状。
			if n := len(out); n > 0 && out[n-1].Role == ir.RoleUser && onlyToolResults(out[n-1]) {
				tr.Idx = len(out[n-1].Parts)
				out[n-1].Parts = append(out[n-1].Parts, tr)
				continue
			}
			out = append(out, ir.Message{Role: ir.RoleUser, Parts: []ir.Part{tr}})

		default:
			return nil, warns, &ir.InvalidRequestError{
				Param: fmt.Sprintf("messages[%d].role", i), Detail: "未知角色 " + m.Role,
			}
		}
	}
	return out, warns, nil
}

func onlyToolResults(m ir.Message) bool {
	for _, p := range m.Parts {
		if _, ok := p.(ir.ToolResultPart); !ok {
			return false
		}
	}
	return len(m.Parts) > 0
}

// decodeContent 解析 content：字符串或 part 数组。
func decodeContent(raw json.RawMessage, msgIdx int) ([]ir.Part, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			// 空文本允许存在但不参与上游编码（§3.1）：占位消息不该让整段历史失败。
			return nil, nil
		}
		return []ir.Part{ir.TextPart{Idx: 0, Text: s}}, nil
	}
	var ps []contentPart
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content", msgIdx), Detail: "既不是字符串也不是数组"}
	}
	parts := make([]ir.Part, 0, len(ps))
	for j, p := range ps {
		switch p.Type {
		case "text", "input_text", "output_text":
			txt := ""
			if p.Text != nil {
				txt = *p.Text
			}
			parts = append(parts, ir.TextPart{Idx: j, Text: txt})
		case "image_url":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				return nil, &ir.InvalidRequestError{Param: fmt.Sprintf("messages[%d].content[%d].image_url", msgIdx, j), Detail: "缺少 url"}
			}
			img, err := decodeImageURL(p.ImageURL.URL)
			if err != nil {
				return nil, err
			}
			img.Idx = j
			img.Detail = p.ImageURL.Detail
			parts = append(parts, img)
		default:
			// §10：未知 part 不忽略——它可能携带关键语义。
			return nil, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.Capability("content_part:"+p.Type), j,
				fmt.Sprintf("messages[%d].content[%d] 的类型 %q 无法表达", msgIdx, j, p.Type))
		}
	}
	return parts, nil
}

// decodeImageURL 把 data URL 解码成字节，把 http(s) URL 保留为 URL 引用。
//
// 为什么内联时就解码：IR 约定存原始字节（省 4/3 膨胀、可多处复用）；URL 则保留引用，
// 让上游直取，避免我们中转大对象。
func decodeImageURL(u string) (ir.ImagePart, error) {
	if strings.HasPrefix(u, "data:") {
		comma := strings.IndexByte(u, ',')
		if comma < 0 {
			return ir.ImagePart{}, &ir.InvalidRequestError{Param: "image_url.url", Detail: "data URL 缺少逗号分隔"}
		}
		meta := u[len("data:"):comma]
		payload := u[comma+1:]
		if !strings.Contains(meta, ";base64") {
			return ir.ImagePart{}, ir.NewUnsupported(ir.ProtocolOpenAIChat, ir.CapImageInput, -1,
				"只支持 base64 编码的 data URL（URL 编码的内联图片需要先转码）")
		}
		mime := strings.TrimSuffix(meta, ";base64")
		if mime == "" {
			mime = "application/octet-stream"
		}
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return ir.ImagePart{}, &ir.InvalidRequestError{Param: "image_url.url", Detail: "base64 解码失败：" + err.Error()}
		}
		return ir.ImagePart{MIME: mime, Data: data, Source: ir.ImageInline}, nil
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return ir.ImagePart{Source: ir.ImageURL, URL: u}, nil
	}
	return ir.ImagePart{}, &ir.InvalidRequestError{Param: "image_url.url", Detail: "只接受 data URL 或 http(s) URL"}
}
