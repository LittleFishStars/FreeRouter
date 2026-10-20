// Package ir 定义 FreeRouter 的内部中间表示（IR）。
//
// 为什么需要这一层：入站协议与上游协议的组合数是 N×M，收敛到 IR 后是 N+M。但比"少写代码"
// 更重要的是——C6（能力缺失不静默降级）需要一个统一的地方回答"这个请求需要哪些能力"；
// 如果能力判定散在四个映射器里，它就会像补丁一样散落，必然漏项。
//
// 本包刻意不认识 HTTP：这里只有字节、对象与事件。协议层（openai/、anthropic/）依赖它，
// 而它不依赖任何协议；这样路由核心与协议编解码可以各自独立测试。
//
// 事实源：docs/architecture/02-ir.md。§10 规定 IR **只加不改**，本包的字段只允许追加。
package ir

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Role 是说话人角色。
//
// 为什么只有三种：它是四个协议公共子集的形状。系统提示统一提升为 RoleSystem 消息；
// 工具结果用 tool_result part 承载并留在 RoleUser 里（Anthropic 的形状），
// 因为"工具结果是内容而非说话人"——设个 RoleTool 会让"一条 user 消息里同时有文本和
// 工具结果"没有合法表示，映射回 Anthropic 时多一次有损往返。见 §2.2。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Valid 用于解析层拒绝不认识的 role：未知 role 若被忽略，就属于静默降级。
func (r Role) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant:
		return true
	}
	return false
}

// PartType 是内容块的种类。
//
// 新增取值时四个映射器必须同时给出映射或"明确 400"（§10），否则新 part 会在某个协议上被
// 静默处理——这正是我们要避免的。
type PartType string

const (
	PartText       PartType = "text"
	PartImage      PartType = "image"
	PartToolCall   PartType = "tool_call"
	PartToolResult PartType = "tool_result"
	PartThinking   PartType = "thinking"
)

// Valid 供解析层对未知 part type 报 400（§10：未知 part 不忽略）。
func (p PartType) Valid() bool {
	switch p {
	case PartText, PartImage, PartToolCall, PartToolResult, PartThinking:
		return true
	}
	return false
}

// Part 是内容块的公共接口。
//
// Index() 由解析层填充一次、之后不再改。为什么由我们编号而不是复用协议给的 index：
// Anthropic 给 content_block index、OpenAI 给 tool_calls[].index、Gemini 根本没有；
// 只有统一编号，错误信息（"第 3 个 part 的图片不被支持"）才能在四个协议里语义一致。
type Part interface {
	PartType() PartType
	Index() int
}

// TextPart 是一段文本。
//
// 空文本允许存在但不参与上游编码（§3.1）：某些客户端会发空的 assistant 消息占位，
// 拒绝它会让整段历史对话失败。
type TextPart struct {
	Idx  int
	Text string
}

func (p TextPart) PartType() PartType { return PartText }
func (p TextPart) Index() int         { return p.Idx }

// ImageSource 表示图片是内联字节还是外部 URL。
type ImageSource string

const (
	ImageInline ImageSource = "inline"
	ImageURL    ImageSource = "url"
)

// ImagePart 是一张图片。
//
// 为什么保存原始字节而不是 base64 字符串：base64 有 4/3 膨胀，且一次解码后要供多处复用
// （能力探针、鉴定流水线、重试时重新编码）。保留 Source 则是为了在上游支持 URL 直取时
// 避免中转大对象，不支持时退回内联。
type ImagePart struct {
	Idx    int
	MIME   string // image/png | image/jpeg | image/webp | image/gif
	Data   []byte // Source == ImageInline 时有效
	Detail string // 协议方言原样保留（OpenAI detail / Gemini mediaResolution…）
	Source ImageSource
	URL    string // Source == ImageURL 时有效
}

func (p ImagePart) PartType() PartType { return PartImage }
func (p ImagePart) Index() int         { return p.Idx }

// ToolCallPart 是模型发起的一次工具调用。
//
// Arguments 是上游给的 JSON 原文，**不做解析**：上游可能给出被截断的非法 JSON，正确行为是
// 原样透传给客户端让它自己决定，而不是我们报 500（§3.3）。只有内部需要读参数的场景
// （能力探针）才尝试解析，失败按"无法判定"处理。
type ToolCallPart struct {
	Idx       int
	ID        string // 稳定 id；流式下由 index 重组得出，见 toolcall.go
	Name      string
	Arguments string
	Partial   bool // 流式中为 true，重组完成后为 false
}

func (p ToolCallPart) PartType() PartType { return PartToolCall }
func (p ToolCallPart) Index() int         { return p.Idx }

// ToolResultPart 是工具执行结果。
//
// 为什么允许多 part：Anthropic 的 tool_result.content 可以是 block 数组并可带图片
// （截图类工具的结果），只允许字符串会让这类客户端在最常见场景直接失败（§3.4）。
type ToolResultPart struct {
	Idx        int
	ToolCallID string // 必须对应某个 ToolCallPart.ID，否则 400
	Parts      []Part
	IsError    bool
}

func (p ToolResultPart) PartType() PartType { return PartToolResult }
func (p ToolResultPart) Index() int         { return p.Idx }

// Text 把 tool_result 里的文本拼起来，供只需要纯文本的协议使用。
func (p ToolResultPart) Text() string {
	var b strings.Builder
	for _, sub := range p.Parts {
		if t, ok := sub.(TextPart); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// ThinkingPart 是模型的思考内容。
//
// 为什么不把它当普通 text：Anthropic 要求把上一轮的 thinking block（含 signature）原样
// 回传才能继续推理，混入 text 会破坏签名校验。Redacted 表达"上游说有思考但不给内容"，
// 这是有信息量的三态，与"没有思考"不同（§3.5）。
//
// 注意：绝不能把 thinking 文本当 text 回显给不支持 thinking 的端点——那会泄露模型内部
// 状态并改变客户端行为；不支持时按 §8 报 400。
type ThinkingPart struct {
	Idx       int
	Text      string
	Signature string // 上游签名（Anthropic 需要回传），可空
	Redacted  bool   // 上游明确表示内容被加密/隐去
}

func (p ThinkingPart) PartType() PartType { return PartThinking }
func (p ThinkingPart) Index() int         { return p.Idx }

// Message 是一条消息。Parts 顺序有意义（文本与图片的相对位置影响多模态理解），
// 映射到任何协议都必须保序。
type Message struct {
	Role  Role
	Parts []Part
}

// Text 拼接消息里的文本部分，供只需要字符串内容的协议与日志使用。
func (m Message) Text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if t, ok := p.(TextPart); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// ToolDef 是一个工具定义。Schema 是 JSON Schema 原文，不解析——解析意味着要引入 schema
// 校验器，那会把"工具定义合法吗"变成我们的责任，而实际决定权在上游。
type ToolDef struct {
	Name        string
	Description string
	Schema      string
	Strict      bool
}

// ToolChoiceMode 是工具选择的意图。
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceSpecific ToolChoiceMode = "specific"
)

// ToolChoice 表达"要不要调工具、能不能随便挑"。
type ToolChoice struct {
	Mode ToolChoiceMode
	Name string // Mode == specific 时有效
}

// Sampling 是采样参数。
//
// 全部用指针：temperature: 0（贪心）与"没给 temperature"（用服务端默认）在上游语义不同，
// 用零值会把两者混为一谈，属于典型的三态丢失（§4）。
type Sampling struct {
	Temperature *float64
	TopP        *float64
	TopK        *int
	MaxOutput   *int // 各协议叫 max_tokens / max_completion_tokens / maxOutputTokens
	Stop        []string
	Seed        *int64
}

// ResponseFormatKind 是结构化输出的种类。
type ResponseFormatKind string

const (
	FormatText       ResponseFormatKind = "text"
	FormatJSONObject ResponseFormatKind = "json_object"
	FormatJSONSchema ResponseFormatKind = "json_schema"
)

// ResponseFormat 是结构化输出要求。nil 表示未指定——与 Kind == FormatText 不同：
// 前者是"客户端没说"，后者是"客户端明确要纯文本"。
type ResponseFormat struct {
	Kind   ResponseFormatKind
	Schema string
	Name   string
}

// Effort 是思考/推理档位。
//
// 为什么必须单独存在 EffortUnknown：各家档位方言不一。客户端给了 xhigh 而我们不认识时，
// 当"未指定"处理属于静默改变行为（用户要的是最强推理）；直接 400 又让客户端没法用。
// 正确做法是标 unknown 交给能力匹配——候选声明支持的档位，不支持的被过滤；
// 全被过滤则 503（请求本身没写错）。见 §4。
type Effort string

const (
	EffortUnspecified Effort = ""
	EffortOff         Effort = "off"
	EffortLow         Effort = "low"
	EffortHigh        Effort = "high"
	EffortMax         Effort = "max"
	EffortUnknown     Effort = "unknown"
)

// RequestMeta 是审计与归因信息。
//
// 为什么它不参与上游编码：account_id / key_id / request_id 是我们的内部归因，
// 一旦进入上游编码路径就有泄露给第三方上游的风险（C4 的隔离要求，§4）。
type RequestMeta struct {
	RequestID    string
	AccountID    string
	KeyID        string
	ClientAPIKey string
}

// Request 是一次推理请求。
type Request struct {
	Model    string // 抽象模型名（L1 已解析）；内核不认识真实模型
	Messages []Message
	Tools    []ToolDef
	Choice   ToolChoice
	Sampling Sampling
	Format   *ResponseFormat // nil = 未指定
	Effort   Effort
	Stream   bool
	Extra    RequestMeta
}

// StopReason 是结束原因。
type StopReason string

const (
	StopUnspecified   StopReason = ""
	StopEndTurn       StopReason = "end_turn"
	StopMaxTokens     StopReason = "max_tokens"
	StopToolUse       StopReason = "tool_use"
	StopStopSequence  StopReason = "stop_sequence"
	StopContentFilter StopReason = "content_filter"
	StopRefusal       StopReason = "refusal"
	StopError         StopReason = "error"
)

// Response 是一次非流式响应。
type Response struct {
	ID    string
	Model string
	Parts []Part
	Stop  StopReason
	Usage Usage
}

// UsageSource 标明用量数字的来源。
//
// 为什么估算值不能和上游上报写进同一种字段：一旦混在一起，后续任何"对不对得上账"的判断
// 都失效。Source 保证两类数据永远可分离（§9）。
type UsageSource string

const (
	UsageUpstream  UsageSource = "upstream"
	UsageEstimated UsageSource = "estimated"
	UsageUnknown   UsageSource = "unknown"
)

// Usage 是用量。
//
// 三态：nil = 上游没给这个字段；0 = 上游明确说是 0；Source == estimated = 我们自己估的。
// 为什么必须区分 nil 与 0：把 nil 当 0 累加，会让"用了很多但上游不报账"的凭据看起来零消耗，
// 配额护栏直接失效（§9）。
type Usage struct {
	InputTokens      *int
	OutputTokens     *int
	CacheReadTokens  *int
	CacheWriteTokens *int
	ReasoningTokens  *int
	Source           UsageSource
}

// Ptr 是取指针的泛型助手：采样参数的三态表达让代码里到处需要 &v，
// 而局部变量取址在复合字面量里不可用。
func Ptr[T any](v T) *T { return &v }

// Error 是错误事件里携带的结构化错误。
//
// Code 与 HTTP 层的错误码表共用同一套字符串（本包不 import httpapi——IR 不认识 HTTP），
// 这样"能力缺失"在日志、错误体、审计里是同一个词。
type Error struct {
	Code       string     `json:"code"`
	Message    string     `json:"message"`
	Capability Capability `json:"capability,omitempty"`
	Index      int        `json:"index,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Capability != "" {
		return fmt.Sprintf("%s: %s (capability=%s)", e.Code, e.Message, e.Capability)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Validate 校验请求的结构自洽性。
//
// 为什么要在解析阶段就查 tool_result 配对：四个协议里"工具结果找不到对应调用"都是硬错误，
// 越早报错越好；等上游返回 400 时，错误信息会指向上游而不是我们的请求，排障成本高得多（§3.4）。
func (r *Request) Validate() error {
	if r == nil {
		return &InvalidRequestError{Param: "request", Detail: "请求为空"}
	}
	seenCalls := make(map[string]int) // tool_call id → part 序号，用于配对与重复检测
	for mi, msg := range r.Messages {
		if !msg.Role.Valid() {
			return &InvalidRequestError{Param: fmt.Sprintf("messages[%d].role", mi), Detail: "未知角色 " + string(msg.Role)}
		}
		for pi, part := range msg.Parts {
			if part == nil {
				return &InvalidRequestError{Param: fmt.Sprintf("messages[%d].parts[%d]", mi, pi), Detail: "part 为空"}
			}
			if !part.PartType().Valid() {
				return &InvalidRequestError{Param: fmt.Sprintf("messages[%d].parts[%d].type", mi, pi), Detail: "未知 part 类型 " + string(part.PartType())}
			}
			switch p := part.(type) {
			case ToolCallPart:
				if p.ID == "" {
					return &InvalidRequestError{Param: fmt.Sprintf("messages[%d].parts[%d].id", mi, pi), Detail: "工具调用缺少 id"}
				}
				if prev, ok := seenCalls[p.ID]; ok {
					return &InvalidRequestError{Param: fmt.Sprintf("messages[%d].parts[%d].id", mi, pi),
						Detail: fmt.Sprintf("工具调用 id %q 重复（首次出现于 part %d）", p.ID, prev)}
				}
				seenCalls[p.ID] = pi
			case ToolResultPart:
				if _, ok := seenCalls[p.ToolCallID]; !ok {
					return &InvalidRequestError{Param: fmt.Sprintf("messages[%d].parts[%d].tool_call_id", mi, pi),
						Detail: fmt.Sprintf("工具结果指向的调用 %q 不存在", p.ToolCallID)}
				}
			}
		}
	}
	if r.Choice.Mode == ToolChoiceSpecific && r.Choice.Name == "" {
		return &InvalidRequestError{Param: "tool_choice.name", Detail: "指定工具选择必须给出工具名"}
	}
	if r.Choice.Mode != "" && r.Choice.Mode != ToolChoiceAuto && r.Choice.Mode != ToolChoiceNone &&
		r.Choice.Mode != ToolChoiceRequired && r.Choice.Mode != ToolChoiceSpecific {
		return &InvalidRequestError{Param: "tool_choice", Detail: "未知工具选择模式 " + string(r.Choice.Mode)}
	}
	if r.Format != nil && r.Format.Kind == FormatJSONSchema {
		if strings.TrimSpace(r.Format.Schema) == "" {
			return &InvalidRequestError{Param: "response_format.schema", Detail: "json_schema 模式必须给出 schema"}
		}
		if !json.Valid([]byte(r.Format.Schema)) {
			return &InvalidRequestError{Param: "response_format.schema", Detail: "schema 不是合法 JSON"}
		}
	}
	return nil
}
