package ir

import (
	"errors"
	"fmt"
	"strings"
)

// Protocol 标识一个协议端点形状。
//
// 值为字符串而不是 int：它会出现在日志、错误体与审计里，"anthropic_messages" 比 2 可读，
// 而且跨语言（Node sidecar）时不需要同步一份枚举表。
type Protocol string

const (
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolAnthropic       Protocol = "anthropic_messages"
	ProtocolGemini          Protocol = "gemini_generate_content"
)

// Capability 是"请求需要、候选不一定具备"的一项能力。
//
// 用字符串的理由同 Protocol：它是 C6 报错体里的字段，客户端开发者会看到它，
// 稳定的字符串比数字更适合做文档与排障的锚点。
type Capability string

const (
	CapTools             Capability = "tools"               // 支持工具（函数）调用
	CapParallelToolCalls Capability = "parallel_tool_calls" // 支持一轮多个工具调用
	CapImageInput        Capability = "image_input"         // 支持图片输入
	CapImageURL          Capability = "image_url_input"     // 支持 URL 直取图片（否则必须内联）
	CapPrefill           Capability = "assistant_prefill"   // 支持 assistant 预填充
	CapThinking          Capability = "thinking"            // 支持思考内容
	CapThinkingSignature Capability = "thinking_signature"  // 支持思考签名回传
	CapJSONObject        Capability = "json_object"         // 支持 json_object 模式
	CapJSONSchema        Capability = "json_schema"         // 支持严格 JSON Schema 输出
	CapStopSequences     Capability = "stop_sequences"      // 支持停止词
	CapTopK              Capability = "top_k"               // 支持 top_k 采样
	CapSeed              Capability = "seed"                // 支持 seed 采样
	CapReasoningEffort   Capability = "reasoning_effort"    // 支持思考档位
	CapStreamUsage       Capability = "stream_usage"        // 流式响应里能给出 usage
	CapSystemInMessages  Capability = "system_in_messages"  // 系统提示放在 messages 里

	// 以下四项**故意不给 IR 提供表达位**：它们会改变生成行为，却没有跨协议的公共语义。
	// 与其在 IR 里堆各家方言字段（§1.2「不做超集」），不如在入站解析时显式拒绝。
	CapMultipleChoices  Capability = "multiple_choices" // n>1：一次要多条候选回复
	CapPresencePenalty  Capability = "presence_penalty"
	CapFrequencyPenalty Capability = "frequency_penalty"
	CapLogprobs         Capability = "logprobs"

	// CapImageOutput 与 CapImageToolResult 用于**响应方向**：模型产出图片、工具结果带图片。
	// 请求方向的能力复核拦不住它们，必须在编码响应时再查一次。
	CapImageOutput     Capability = "image_output"
	CapImageToolResult Capability = "image_tool_result"
)

// Capabilities 是一个协议适配器的能力自省结果。
//
// 为什么让适配器自报而不是在上层写 if protocol == "anthropic"：协议与能力不是一一对应
// （同一协议的不同上游也可能差能力），把判断放在适配器里，上层就只剩"在候选集里过滤"。
type Capabilities struct {
	Protocol          Protocol
	Tools             bool
	ParallelToolCalls bool
	Images            bool
	ImageURL          bool
	Prefill           bool
	Thinking          bool
	ThinkingSignature bool
	JSONObject        bool
	JSONSchema        bool
	StopSequences     bool
	TopK              bool
	Seed              bool
	StreamUsage       bool
	// Efforts 是可接受的思考档位集合。为 nil 表示"不认档位"，此时非空 Effort 一律不支持。
	Efforts map[Effort]bool
	// MaxStopSequences 是停止词个数上限；0 表示不限。
	MaxStopSequences int
}

// Has 报告某个能力是否具备。
func (c Capabilities) Has(cap Capability) bool {
	switch cap {
	case CapTools:
		return c.Tools
	case CapParallelToolCalls:
		return c.ParallelToolCalls
	case CapImageInput:
		return c.Images
	case CapImageURL:
		return c.ImageURL
	case CapPrefill:
		return c.Prefill
	case CapThinking:
		return c.Thinking
	case CapThinkingSignature:
		return c.ThinkingSignature
	case CapJSONObject:
		return c.JSONObject
	case CapJSONSchema:
		return c.JSONSchema
	case CapStopSequences:
		return c.StopSequences
	case CapTopK:
		return c.TopK
	case CapSeed:
		return c.Seed
	case CapStreamUsage:
		return c.StreamUsage
	}
	return false
}

// UnsupportedError 是 C6 的唯一表达：能力名 + 目标协议 + 定位，三者缺一不可。
//
// 为什么错误里必须带协议名：同一个请求打到不同的上游，缺的能力可能不同；运维看到
// "image_input: openai_chat 不支持" 才能立刻知道该改路由还是该改客户端。
// 为什么带 PartIndex：客户端要在自己拼的 JSON 里找到那一个 part，没有序号的报错只能靠猜。
type UnsupportedError struct {
	Capability Capability
	Protocol   Protocol
	PartIndex  int // -1 表示与具体 part 无关
	Detail     string
}

func (e *UnsupportedError) Error() string {
	where := ""
	if e.PartIndex >= 0 {
		where = fmt.Sprintf("（part %d）", e.PartIndex)
	}
	detail := e.Detail
	if detail == "" {
		detail = "该能力在目标协议上没有等价表达"
	}
	return fmt.Sprintf("目标协议 %s 不支持能力 %s%s: %s", e.Protocol, e.Capability, where, detail)
}

// Code 是给 HTTP 层用的错误码。
//
// 注意：docs/architecture/02-ir.md §8.3 写的是 capability_missing，而
// docs/api/03-errors.md 与 internal/httpapi 的错误码表里是 capability_unsupported。
// 事实源优先级（PLAN.md 高于分册）下取后者，冲突已在回报中提出。
func (e *UnsupportedError) Code() string { return "capability_unsupported" }

// InvalidRequestError 是"请求本身写错了"（400 invalid_request）。
// 与 UnsupportedError 的区别：前者换客户端参数就能解决，后者要换候选或换模型。
type InvalidRequestError struct {
	Param  string
	Detail string
}

func (e *InvalidRequestError) Error() string {
	if e.Param == "" {
		return "invalid_request: " + e.Detail
	}
	return fmt.Sprintf("invalid_request: %s: %s", e.Param, e.Detail)
}

// Code 返回与 HTTP 错误码表一致的字符串。
func (e *InvalidRequestError) Code() string { return "invalid_request" }

// UpstreamProtocolError 是"上游给的流/响应违反协议不变式"（如 index 跨块复用、
// 非法分片顺序）。为什么不能复用 InvalidRequestError：那会被映射成客户端 400，
// 把上游的错算到客户端头上；它应对齐 docs/api/03-errors.md 的 upstream_error（502、
// 可重试）——归属与重试策略都相反，必须分开建模。
type UpstreamProtocolError struct {
	Detail string
}

func (e *UpstreamProtocolError) Error() string {
	return "upstream_error: 上游流违反协议不变式: " + e.Detail
}

// Code 返回与 HTTP 错误码表一致的字符串。
func (e *UpstreamProtocolError) Code() string { return "upstream_error" }

// AsUpstreamProtocol 把任意 error 规约为 *UpstreamProtocolError。
func AsUpstreamProtocol(err error) (*UpstreamProtocolError, bool) {
	var ue *UpstreamProtocolError
	if errors.As(err, &ue) {
		return ue, true
	}
	return nil, false
}

// Warning 是一条"忽略但有记录"的告警。
//
// 为什么单独建模而不是 log 一行了事：§8.4 的白名单字段允许忽略，但"允许忽略"和
// "没人知道被忽略了"是两件事。把告警作为返回值交给调用方，它才能进审计。
type Warning struct {
	Param   string
	Message string
}

// Check 在编码前判定"这个请求能否被该协议完整履约"（C6）。
//
// 返回 error 表示必须拒绝（400/503 由调用方决定）；返回 warnings 表示允许继续但有信息量。
// 严格模式与过滤模式的差别不在本函数：本函数只回答"缺不缺"，过滤候选与否则是路由的策略。
func (c Capabilities) Check(req *Request) ([]Warning, error) {
	if req == nil {
		return nil, &InvalidRequestError{Param: "request", Detail: "请求为空"}
	}
	var warns []Warning

	for mi, msg := range req.Messages {
		for pi, part := range msg.Parts {
			switch p := part.(type) {
			case ImagePart:
				if !c.Images {
					return warns, &UnsupportedError{
						Capability: CapImageInput, Protocol: c.Protocol, PartIndex: pi,
						Detail: fmt.Sprintf("messages[%d].parts[%d] 是图片", mi, pi),
					}
				}
				if p.Source == ImageURL && !c.ImageURL {
					return warns, &UnsupportedError{
						Capability: CapImageURL, Protocol: c.Protocol, PartIndex: pi,
						Detail: fmt.Sprintf("messages[%d].parts[%d] 使用 URL 图片，需要内联为字节", mi, pi),
					}
				}
				// Detail 的方言值是否可映射不在本层判断（§11 待定），只记告警。
				if p.Detail != "" && !c.Images {
					warns = append(warns, Warning{Param: "image.detail", Message: "目标协议不支持 detail，已忽略"})
				}
			case ToolCallPart, ToolResultPart:
				if !c.Tools {
					return warns, &UnsupportedError{
						Capability: CapTools, Protocol: c.Protocol, PartIndex: pi,
						Detail: fmt.Sprintf("messages[%d].parts[%d] 携带工具调用/结果", mi, pi),
					}
				}
			case ThinkingPart:
				if !c.Thinking {
					// 绝不降级成 text：那会泄露模型内部状态并改变客户端行为（§3.5）。
					return warns, &UnsupportedError{
						Capability: CapThinking, Protocol: c.Protocol, PartIndex: pi,
						Detail: fmt.Sprintf("messages[%d].parts[%d] 是思考内容，目标协议无对应表达", mi, pi),
					}
				}
				if p.Signature != "" && !c.ThinkingSignature {
					return warns, &UnsupportedError{
						Capability: CapThinkingSignature, Protocol: c.Protocol, PartIndex: pi,
						Detail: fmt.Sprintf("messages[%d].parts[%d] 携带思考签名，目标协议无法回传", mi, pi),
					}
				}
			}
		}
	}

	if len(req.Tools) > 0 && !c.Tools {
		return warns, &UnsupportedError{Capability: CapTools, Protocol: c.Protocol, PartIndex: -1,
			Detail: fmt.Sprintf("请求带 %d 个工具定义", len(req.Tools))}
	}
	if req.Choice.Mode == ToolChoiceRequired || req.Choice.Mode == ToolChoiceSpecific {
		if !c.Tools {
			return warns, &UnsupportedError{Capability: CapTools, Protocol: c.Protocol, PartIndex: -1,
				Detail: "强制工具选择需要工具能力"}
		}
	}
	if c.Tools && !c.ParallelToolCalls {
		// 并行能力缺失不是硬错误：可以串行化，但必须让调用方知道（否则用户会以为并行被支持）。
		if n := parallelCallCount(req); n > 1 {
			warns = append(warns, Warning{Param: "tools.parallel",
				Message: fmt.Sprintf("请求含 %d 个并行工具调用，目标协议不支持并行，将串行发出", n)})
		}
	}
	if req.Format != nil {
		switch req.Format.Kind {
		case FormatJSONObject:
			if !c.JSONObject {
				return warns, &UnsupportedError{Capability: CapJSONObject, Protocol: c.Protocol, PartIndex: -1}
			}
		case FormatJSONSchema:
			if !c.JSONSchema {
				return warns, &UnsupportedError{Capability: CapJSONSchema, Protocol: c.Protocol, PartIndex: -1,
					Detail: "请求要求 json_schema 结构化输出"}
			}
		}
	}
	if len(req.Sampling.Stop) > 0 {
		if !c.StopSequences {
			return warns, &UnsupportedError{Capability: CapStopSequences, Protocol: c.Protocol, PartIndex: -1,
				Detail: fmt.Sprintf("请求带 %d 个停止词", len(req.Sampling.Stop))}
		}
		if c.MaxStopSequences > 0 && len(req.Sampling.Stop) > c.MaxStopSequences {
			// 这是请求属性而非能力缺失：换候选也一样超限，所以是 invalid_request（§8.3）。
			return warns, &InvalidRequestError{Param: "stop",
				Detail: fmt.Sprintf("停止词 %d 个，超过目标协议上限 %d", len(req.Sampling.Stop), c.MaxStopSequences)}
		}
	}
	if req.Sampling.TopK != nil && !c.TopK {
		// §8.3 的唯一例外：top_k 是采样细节，丢弃只影响多样性，不改变"模型做了什么"。
		// 例外清单必须短且逐条有理由，否则它就成了静默降级的后门。
		warns = append(warns, Warning{Param: "top_k", Message: "目标协议不支持 top_k，已忽略"})
	}
	if req.Sampling.Seed != nil && !c.Seed {
		warns = append(warns, Warning{Param: "seed", Message: "目标协议不支持 seed，已忽略"})
	}
	if req.Effort != EffortUnspecified {
		switch {
		case len(c.Efforts) == 0:
			return warns, &UnsupportedError{Capability: CapReasoningEffort, Protocol: c.Protocol, PartIndex: -1,
				Detail: "目标协议不支持思考档位，收到 " + string(req.Effort)}
		case !c.Efforts[req.Effort]:
			// EffortUnknown 落到这里正是设计意图：不认识档位的候选被过滤掉，而不是被当成"未指定"。
			return warns, &UnsupportedError{Capability: CapReasoningEffort, Protocol: c.Protocol, PartIndex: -1,
				Detail: "目标协议没有档位 " + string(req.Effort)}
		}
	}
	if req.Stream && !c.StreamUsage {
		warns = append(warns, Warning{Param: "stream_options.include_usage", Message: "目标协议流式不给 usage"})
	}
	return warns, nil
}

// parallelCallCount 统计最后一条 assistant 消息里的工具调用数。
//
// 只看最后一条：历史里的并行调用已经发生，不需要再"支持"一次；会被并行发出的是本轮的。
func parallelCallCount(req *Request) int {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != RoleAssistant {
			continue
		}
		n := 0
		for _, p := range req.Messages[i].Parts {
			if _, ok := p.(ToolCallPart); ok {
				n++
			}
		}
		return n
	}
	return 0
}

// NewUnsupported 是构造 UnsupportedError 的便捷函数，主要给协议适配器内部用。
func NewUnsupported(proto Protocol, cap Capability, partIndex int, detail string) *UnsupportedError {
	return &UnsupportedError{Capability: cap, Protocol: proto, PartIndex: partIndex, Detail: detail}
}

// JoinWarnings 把多条告警压成一段文本，供日志一次性打印。
func JoinWarnings(ws []Warning) string {
	if len(ws) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		parts = append(parts, w.Param+": "+w.Message)
	}
	return strings.Join(parts, "; ")
}
