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
// thinking 现为 true：DeepSeek 官方 Chat API 把 `reasoning_content` 写进了协议
// （响应体与流式 delta 都有），并新增 `thinking` 开关字段。所以思考内容不再是
// 「上游方言」而是协议能力，客户端可安全依赖它。
// 但 ThinkingSignature 仍为 false：OpenAI 侧没有承载思考签名的字段位，
// 带 signature 的 ThinkingPart 回传仍必须报错（签名的用途是跨轮次校验，
// 丢掉它会让上游怀疑上下文被算改——§3.5）。
//
// 档位覆盖 none/low/high/max：官方 `reasoning_effort` 的合法取值就是这四个，
// 少声明 max 会让官方合法的 effort=max 请求被候选过滤掉并最终 503。
// 官方另接受 minimal→low、medium/xhigh→high 的兼容写法，由 decodeEffort 归一旦记告警。
func (adapter) Capabilities() ir.Capabilities {
	return ir.Capabilities{
		Protocol:          ir.ProtocolOpenAIChat,
		Tools:             true,
		ParallelToolCalls: true,
		Images:            true,
		ImageURL:          true,
		Prefill:           true,
		Thinking:          true,  // 官方 Chat API 的 reasoning_content 是协议字段，非方言
		ThinkingSignature: false, // OpenAI 侧无签名承载位；带签名的回传仍在 Check 报错
		JSONObject:        true,
		JSONSchema:        true,
		StopSequences:     true,
		TopK:              false,
		Seed:              true,
		StreamUsage:       true,
		// Efforts 覆盖官方四档（none/low/high/max）——官方 API 的
		// reasoning_effort 明列 none/low/high/max，少声明 max 会让
		// effort=max 的请求被候选过滤掉并最终 503，而它本就是官方合法参数。
		Efforts: map[ir.Effort]bool{ir.EffortOff: true, ir.EffortLow: true, ir.EffortHigh: true, ir.EffortMax: true},
		// MaxStopSequences 取官方上限 16（api-docs /create-chat-completion）。
		MaxStopSequences: 16,
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

	// presence/frequency_penalty：官方已弃用（传了也不生效），但大量存量客户端仍会带——
	// 按 §8.4 白名单忽略并告警，而不是把整条请求变成 400。
	// logprobs / top_logprobs：官方支持（返回 token 级对数概率），IR 不承载该结果面；
	// 忽略后行为退化但语义正确（客户端只少了一份诊断信息），同样入白名单。
	// 为什么不报 Unsupported：那会把官方兼容客户端的正常参数变成硬错误，
	// 与「支持 DeepSeek 官方 API 全集」的目标直接冲突。
	if raw.PresencePenalty != nil {
		warns = append(warns, ir.Warning{Param: "presence_penalty",
			Message: "已忽略（官方已弃用该参数，不影响生成结果）"})
	}
	if raw.FrequencyPenalty != nil {
		warns = append(warns, ir.Warning{Param: "frequency_penalty",
			Message: "已忽略（官方已弃用该参数，不影响生成结果）"})
	}
	if (raw.Logprobs != nil && *raw.Logprobs) || raw.TopLogprobs != nil {
		warns = append(warns, ir.Warning{Param: "logprobs",
			Message: "已忽略（IR 不承载 token 级对数概率，响应里不会出现 logprobs 字段）"})
	}
	if raw.User != "" {
		// §8.4 白名单：上游用它做滥用追踪，与模型行为无关。
		warns = append(warns, ir.Warning{Param: "user", Message: "已忽略（不参与上游编码）"})
	}
	if raw.ParallelToolCalls != nil && !*raw.ParallelToolCalls {
		// parallel_tool_calls=false 是行为开关，但 IR 没有表达位；官方对它的处置也是
		// ignored（并行能力由上游决定）。白名单忽略 + 告警，不拒绝。
		warns = append(warns, ir.Warning{Param: "parallel_tool_calls",
			Message: "已忽略（并行工具调用由上游决定）"})
	}
	if raw.StreamOptions != nil && !raw.StreamOptions.IncludeUsage {
		warns = append(warns, ir.Warning{Param: "stream_options.include_usage",
			Message: "我们总是回 usage，忽略该开关"})
	}

	// thinking：官方 Chat API 的思考开关。官方语义是 thinking.type 优先于
	// reasoning_effort（后者只在 thinking 缺省时生效），因此这里覆盖 req.Effort。
	if raw.Thinking != nil {
		switch strings.ToLower(raw.Thinking.Type) {
		case "enabled":
			req.Effort = ir.EffortHigh // 官方缺省 effort 即 high
			warns = append(warns, ir.Warning{Param: "thinking.type",
				Message: "thinking=enabled 映射为档位 high（官方缺省 effort，精确预算由 reasoning_effort 给出时以它为准）"})
		case "disabled":
			req.Effort = ir.EffortOff
		case "":
			// 官方契约：该对象出现时 type 必填。空串不猜，直接按非法请求拒绝。
			return nil, warns, &ir.InvalidRequestError{Param: "thinking.type",
				Detail: "thinking 对象必须给出 type（enabled/disabled）"}
		default:
			return nil, warns, &ir.InvalidRequestError{Param: "thinking.type",
				Detail: "thinking.type 取值 " + raw.Thinking.Type + " 无法识别（允许 enabled/disabled）"}
		}
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

	// reasoning_effort 与 thinking 的优先级是官方契约的一部分：thinking.type 优先，
	// reasoning_effort 只在 thinking 缺省时决定模式。所以这里只能在 thinking
	// 未给出时生效——反过来写会让 thinking=disabled 被 effort=high 覆盖成开启思考，
	// 正是 C6 禁止的静默改变行为。
	if raw.Thinking == nil {
		if raw.ReasoningEffort != nil {
			req.Effort = decodeEffort(*raw.ReasoningEffort, &warns)
		}
	} else if raw.ReasoningEffort != nil {
		// thinking 已给出时，effort 仍可细调档位（官方：thinking 只切开关，effort 定档），
		// 但 disabled 必须保持关闭——force off 不能被 effort 打开。
		if req.Effort != ir.EffortOff {
			req.Effort = decodeEffort(*raw.ReasoningEffort, &warns)
		}
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
