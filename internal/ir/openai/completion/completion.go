// Package completion 实现 OpenAI 兼容的文本补全（FIM）协议与 IR 的双向编解码。
//
// 为什么单独成包而不并进 openai/：补全与对话是**两个协议**——请求体（prompt/suffix
// vs messages）、响应体（choices[].text vs choices[].message）、流式帧（delta 里的
// text 而不是 content）三处形状全不同。并进 openai/ 会让每个编码函数都要先判
// "这是哪种请求"，方言判定就会渗回内核，正是 IR 要消除的东西。
//
// 对应 DeepSeek 官方 FIM Completion API（Beta）：POST /beta/completions，
// 入参 model/prompt/suffix/echo/max_tokens/stop/temperature/top_p，出参纯文本。
package completion

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// adapter 实现文本补全的编解码。
type adapter struct{}

// Adapter 返回文本补全适配器。
func Adapter() ir.Adapter { return adapter{} }

func (adapter) Name() ir.Protocol { return ir.ProtocolOpenAICompletion }

// Capabilities 是补全方言的能力自省。
//
// 除补全本身外全部为 false：FIM 不接受 messages、工具、图片、思考、结构化输出。
// 这不是"还没做"，而是协议没有这些位置——声明成 true 会让请求被放行到上游，
// 然后在那边得到指向上游的 400。
func (adapter) Capabilities() ir.Capabilities {
	return ir.Capabilities{
		Protocol:          ir.ProtocolOpenAICompletion,
		Tools:             false,
		ParallelToolCalls: false,
		Images:            false,
		ImageURL:          false,
		Prefill:           false,
		Thinking:          false,
		ThinkingSignature: false,
		JSONObject:        false,
		JSONSchema:        false,
		StopSequences:     true,
		TopK:              false,
		Seed:              false,
		StreamUsage:       true, // 官方 FIM 流式与 chat 同一套 stream_options/include_usage
		TextCompletion:    true,
		CompletionEcho:    true,
		// 官方 FIM 的 stop 上限同样取 16（与 chat 面同一份契约表）。
		MaxStopSequences: 16,
	}
}

// ---------------------------------------------------------------------------
// 线上类型（wire）
// ---------------------------------------------------------------------------

// request 是 /completions 请求体。
//
// 三态原则与 openai/types.go 相同：可选字段用指针，"没给"与"给了零值"不可混淆
// （temperature: 0 是贪心采样，没给是上游默认）。
type request struct {
	Model            string          `json:"model"`
	Prompt           string          `json:"prompt"`
	Suffix           *string         `json:"suffix,omitempty"`
	Echo             *bool           `json:"echo,omitempty"`
	MaxTokens        *int            `json:"max_tokens,omitempty"`
	Stop             json.RawMessage `json:"stop,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	Stream           bool            `json:"stream,omitempty"`
	StreamOptions    *streamOptions  `json:"stream_options,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	Logprobs         *int            `json:"logprobs,omitempty"`
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
	Text         string          `json:"text"`
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
}

type completionToken struct {
	ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
}

// chunk 是流式帧。
type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage,omitempty"`
}

// chunkChoice 是流式帧的 choices 元素。
//
// 与 chat 的 chunkChoice 差别：这里没有 delta 对象，增量直接是 text 字段。
// 为什么不用同一个结构：chat 的 delta 是嵌套对象，补全的增量是扁平字符串；
// 共用会让编码器每帧都要判"该填哪个字段"，那正是本包独立存在的理由。
type chunkChoice struct {
	Index        int     `json:"index"`
	Text         string  `json:"text,omitempty"`
	FinishReason *string `json:"finish_reason"`
}

// ---------------------------------------------------------------------------
// DecodeRequest
// ---------------------------------------------------------------------------

func (a adapter) DecodeRequest(body []byte) (*ir.Request, []ir.Warning, error) {
	var raw request
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, &ir.InvalidRequestError{Param: "body", Detail: "不是合法 JSON 对象：" + err.Error()}
	}
	var warns []ir.Warning

	if raw.Prompt == "" {
		// 官方要求 prompt 必填（Minimum items 不适用，它是字符串必填）。
		// 空 prompt 在 FIM 里没有意义——"补全空文本"就是纯生成，那是 chat 的活。
		return nil, warns, &ir.InvalidRequestError{Param: "prompt", Detail: "prompt 不能为空"}
	}

	req := &ir.Request{Model: raw.Model, Stream: raw.Stream}
	comp := &ir.Completion{Prompt: raw.Prompt}
	if raw.Suffix != nil {
		comp.Suffix = *raw.Suffix
	}
	if raw.Echo != nil {
		comp.Echo = *raw.Echo
	}
	// 官方限制：echo 不能与 suffix 或 logprobs 同时使用。
	if comp.Echo && comp.Suffix != "" {
		return nil, warns, &ir.InvalidRequestError{Param: "echo",
			Detail: "echo 不能与 suffix 同时使用（官方限制）"}
	}
	req.Completion = comp

	// 其余气泡参数：n/seed/logprobs 等 IR 不承载，按 §8.4 白名单忽略并告警。
	if raw.Logprobs != nil {
		if comp.Echo {
			return nil, warns, &ir.InvalidRequestError{Param: "logprobs",
				Detail: "logprobs 不能与 echo 同时使用（官方限制）"}
		}
		warns = append(warns, ir.Warning{Param: "logprobs",
			Message: "已忽略（IR 不承载 token 级对数概率，响应里不会出现 logprobs 字段）"})
	}
	if raw.PresencePenalty != nil {
		warns = append(warns, ir.Warning{Param: "presence_penalty",
			Message: "已忽略（官方已弃用该参数，不影响生成结果）"})
	}
	if raw.FrequencyPenalty != nil {
		warns = append(warns, ir.Warning{Param: "frequency_penalty",
			Message: "已忽略（官方已弃用该参数，不影响生成结果）"})
	}

	req.Sampling.MaxOutput = raw.MaxTokens
	req.Sampling.Temperature = raw.Temperature
	req.Sampling.TopP = raw.TopP
	stops, w := decodeStop(raw.Stop)
	warns = append(warns, w...)
	req.Sampling.Stop = stops

	if raw.StreamOptions != nil && !raw.StreamOptions.IncludeUsage {
		warns = append(warns, ir.Warning{Param: "stream_options.include_usage",
			Message: "我们总是回 usage，忽略该开关"})
	}

	if err := req.Validate(); err != nil {
		return nil, warns, err
	}
	return req, warns, nil
}

// decodeStop 接受 string 与 []string 两种形状（协议规定 stop 是 oneOf）。
func decodeStop(raw json.RawMessage) ([]string, []ir.Warning) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if one == "" {
			return nil, nil
		}
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many, nil
	}
	return nil, []ir.Warning{{Param: "stop", Message: "stop 形状无法识别（应为 string 或 string[]），已忽略"}}
}

// ---------------------------------------------------------------------------
// EncodeRequest
// ---------------------------------------------------------------------------

func (a adapter) EncodeRequest(req *ir.Request, opt ir.EncodeOptions) ([]byte, []ir.Warning, error) {
	if req == nil {
		return nil, nil, &ir.InvalidRequestError{Param: "request", Detail: "请求为空"}
	}
	warns, err := a.Capabilities().Check(req)
	if err != nil {
		return nil, warns, err
	}
	if req.Completion == nil {
		// 防御：走到这里说明上层把对话请求错派给了补全方言。
		return nil, warns, &ir.InvalidRequestError{Param: "request",
			Detail: "补全方言只能编码文本补全请求（缺少 prompt/suffix）"}
	}

	model := opt.UpstreamModel
	if model == "" {
		model = req.Model
	}
	out := request{Model: model, Prompt: req.Completion.Prompt, Stream: req.Stream}

	// suffix 只在非空时发：官方把"没给 suffix"当作普通续写，
	// 而发一个空串在部分兼容上游会被当成 "suffix 存在但不是 FIM"。
	if req.Completion.Suffix != "" {
		out.Suffix = ir.Ptr(req.Completion.Suffix)
	}
	if req.Completion.Echo {
		out.Echo = ir.Ptr(true)
	}

	out.MaxTokens = req.Sampling.MaxOutput
	out.Temperature = req.Sampling.Temperature
	out.TopP = req.Sampling.TopP
	if len(req.Sampling.Stop) > 0 {
		// 单值编成字符串（官方 oneOf 的两种形状都合法，字符串更紧凑）。
		if len(req.Sampling.Stop) == 1 {
			out.Stop, _ = json.Marshal(req.Sampling.Stop[0])
		} else {
			out.Stop, _ = json.Marshal(req.Sampling.Stop)
		}
	}
	if req.Stream {
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil, warns, &ir.InvalidRequestError{Param: "body", Detail: "编码失败：" + err.Error()}
	}
	return body, warns, nil
}

// ---------------------------------------------------------------------------
// 响应编解码
// ---------------------------------------------------------------------------

func (adapter) DecodeResponse(body []byte) (*ir.Response, error) {
	var raw response
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &ir.InvalidRequestError{Param: "body", Detail: "不是合法 JSON：" + err.Error()}
	}
	if len(raw.Choices) == 0 {
		return nil, &ir.InvalidRequestError{Param: "choices", Detail: "响应里没有 choices"}
	}
	resp := &ir.Response{ID: raw.ID, Model: raw.Model, CreatedAt: raw.Created}
	c := raw.Choices[0]
	// 补全响应的正文是 choices[].text（不是 message.content）：把它包成一个
	// TextPart，让 IR 的下游（计量、审计、跨协议编码）不必认识"补全"这一形态。
	if c.Text != "" {
		resp.Parts = []ir.Part{ir.TextPart{Text: c.Text}}
	}
	if c.FinishReason != nil {
		resp.Stop = finishToStop(*c.FinishReason)
	}
	if raw.Usage != nil {
		resp.Usage = decodeUsage(raw.Usage)
	} else {
		resp.Usage = ir.Usage{Source: ir.UsageUnknown}
	}
	return resp, nil
}

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

func (adapter) EncodeResponse(resp *ir.Response) ([]byte, error) {
	if resp == nil {
		return nil, &ir.InvalidRequestError{Param: "response", Detail: "响应为空"}
	}
	var text string
	for _, p := range resp.Parts {
		switch v := p.(type) {
		case ir.TextPart:
			text += v.Text
		default:
			// 补全协议只有一个 text 字段，思考/工具/图片都无处安放。
			return nil, ir.NewUnsupported(ir.ProtocolOpenAICompletion, CapOf(v), -1,
				"补全协议的响应只有 choices[].text，无法承载该 part")
		}
	}
	finish := stopToFinish(resp.Stop)
	out := response{
		ID:      resp.ID,
		Object:  "text_completion",
		Created: resp.CreatedAt,
		Model:   resp.Model,
		Choices: []choice{{Index: 0, Text: text, FinishReason: &finish}},
	}
	if resp.Usage.InputTokens != nil || resp.Usage.OutputTokens != nil {
		u := &usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
		}
		if u.PromptTokens != nil && u.CompletionTokens != nil {
			u.TotalTokens = ir.Ptr(*u.PromptTokens + *u.CompletionTokens)
		}
		out.Usage = u
	}
	return json.Marshal(out)
}

// CapOf 把 part 类型映射成能力名（用于响应编码方向的错误定位）。
func CapOf(p ir.Part) ir.Capability {
	switch p.(type) {
	case ir.ThinkingPart:
		return ir.CapThinking
	case ir.ToolCallPart:
		return ir.CapTools
	case ir.ImagePart:
		return ir.CapImageOutput
	}
	return ir.CapTextCompletion
}

// ---------------------------------------------------------------------------
// finish_reason 映射
// ---------------------------------------------------------------------------

// finishToStop 把补全的 finish_reason 归一到 IR。
//
// 与 chat 面的表一致（官方两侧共用同一份枚举）：stop/length/content_filter，
// 另有 insufficient_system_resource / aborted 两个只在补全面出现的值——
// 前者是资源不足（等价 error），后者是中断（等价 error）。
func finishToStop(s string) ir.StopReason {
	switch s {
	case "stop":
		return ir.StopEndTurn
	case "length":
		return ir.StopMaxTokens
	case "content_filter":
		return ir.StopContentFilter
	case "insufficient_system_resource", "aborted":
		// 这两个不是"模型说完了"：它们是上游中断。映射成 StopError 让
		// 计量与审计能看到"这次不是正常结束"，而不是伪装成 end_turn。
		return ir.StopError
	case "":
		return ir.StopUnspecified
	}
	return ir.StopEndTurn
}

func stopToFinish(s ir.StopReason) string {
	switch s {
	case ir.StopMaxTokens:
		return "length"
	case ir.StopContentFilter, ir.StopRefusal:
		return "content_filter"
	case ir.StopError:
		return "aborted"
	}
	return "stop"
}

// ---------------------------------------------------------------------------
// 流式
// ---------------------------------------------------------------------------

// streamWriter 把 IR 事件写成补全 SSE 帧。
//
// 与 chat 的差异：帧里没有 delta 对象，增量直接放在 choices[].text。
// 状态记账（EncoderState）与 chat 共用同一套 I4/I5 语义。
type streamWriter struct {
	w       *ir.SSEWriter
	st      *ir.EncoderState
	model   string
	created int64
	seq     int
	closed  bool
	stop    ir.StopReason
	opt     ir.EncodeOptions
}

func (adapter) NewStreamWriter(w io.Writer, opt ir.EncodeOptions) ir.StreamWriter {
	created := opt.CreatedAt
	if created == 0 {
		// 时间戳兜底：0 会被客户端渲染成 1970（与 chat 面同一条理由）。
		created = nowUnix()
	}
	return &streamWriter{w: ir.NewSSEWriter(w), st: &ir.EncoderState{}, model: opt.UpstreamModel, created: created, opt: opt}
}

func (s *streamWriter) State() *ir.EncoderState { return s.st }

func (s *streamWriter) Write(ev ir.Event) error {
	if s.closed {
		return ir.ErrStreamClosed
	}
	if !s.st.Accept(ev) {
		return nil
	}
	switch ev.Type {
	case ir.EvStart:
		s.st.MarkWritten(ev)
		return nil
	case ir.EvThinkingDelta:
		// 补全协议没有思考字段位；能力复核已拦过带思考的请求，
		// 走到这里是上游噪音。丢弃不改变客户端行为，也不计入可见内容。
		return nil
	case ir.EvTextDelta:
		return s.writeFrame(chunk{
			ID: s.chunkID(), Object: "text_completion", Created: s.created, Model: s.model,
			Choices: []chunkChoice{{Index: 0, Text: ev.Text}},
		})
	case ir.EvToolCallStart, ir.EvToolCallDelta, ir.EvToolCallEnd:
		// 同上：补全协议没有工具位。
		return nil
	case ir.EvUsage:
		if ev.Usage == nil {
			return nil
		}
		// usage 独占一帧、choices 为空（与 chat 的 include_usage 约定一致）。
		return s.writeFrame(chunk{
			ID: s.chunkID(), Object: "text_completion", Created: s.created, Model: s.model,
			Choices: []chunkChoice{}, Usage: encodeUsage(*ev.Usage),
		})
	case ir.EvError:
		if ev.Err == nil {
			return nil
		}
		body := map[string]any{"error": map[string]any{
			"message": ev.Err.Message,
			"type":    ir.CodeToErrorType(ev.Err.Code),
			"code":    ev.Err.Code,
		}}
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		s.st.MarkWritten(ev)
		return s.w.Write(ir.SSEEvent{Data: string(b)})
	case ir.EvDone:
		if ev.Stop != ir.StopUnspecified {
			s.stop = ev.Stop
		}
		return s.closeWithStop(ev.Stop)
	}
	return nil
}

func (s *streamWriter) Close() error {
	if s.closed {
		return nil
	}
	return s.closeWithStop(s.stop)
}

func (s *streamWriter) closeWithStop(stop ir.StopReason) error {
	if stop == ir.StopUnspecified {
		stop = s.opt.Stop
	}
	if stop == ir.StopUnspecified {
		stop = ir.StopEndTurn
	}
	reason := stopToFinish(stop)
	if err := s.writeFrame(chunk{
		ID: s.chunkID(), Object: "text_completion", Created: s.created, Model: s.model,
		Choices: []chunkChoice{{Index: 0, FinishReason: &reason}},
	}); err != nil {
		return err
	}
	s.closed = true
	s.st.MarkWritten(ir.Event{Type: ir.EvDone})
	return s.w.Write(ir.SSEEvent{Data: "[DONE]"})
}

// nowUnix 取当前时间戳。为什么不在 IR 层直接调 time.Now()：与 openai 包
// 同一套理由——契约 golden 必须逐字节稳定，时间戳由路由层注入，
// 只有"没注入"时才在这里兜底。
func nowUnix() int64 { return time.Now().Unix() }

// encodeUsage 把 IR usage 编成补全协议的 usage 对象（三态：nil 省略，0 保留为 0）。
func encodeUsage(u ir.Usage) *usage {
	out := &usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
	}
	if u.InputTokens != nil && u.OutputTokens != nil {
		out.TotalTokens = ir.Ptr(*u.InputTokens + *u.OutputTokens)
	}
	if u.CacheReadTokens != nil {
		out.PromptTokensDetails = &promptDetails{CachedTokens: u.CacheReadTokens}
	}
	if u.ReasoningTokens != nil {
		out.CompletionTokensDetails = &completionToken{ReasoningTokens: u.ReasoningTokens}
	}
	return out
}

func (s *streamWriter) chunkID() string {
	s.seq++
	return fmt.Sprintf("cmpl-freerouter-%d", s.seq)
}

func (s *streamWriter) writeFrame(c chunk) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	// 先记状态再写：写失败时保守认为已可见（C7 宁可少重试，不可在客户端
	// 已见到半个 chunk 后重发）。
	ev := ir.Event{Type: ir.EvStart}
	if len(c.Choices) > 0 {
		if c.Choices[0].Text != "" {
			ev = ir.Event{Type: ir.EvTextDelta, Text: c.Choices[0].Text}
		} else if c.Choices[0].FinishReason != nil {
			ev = ir.Event{Type: ir.EvDone}
		}
	} else if c.Usage != nil {
		ev = ir.Event{Type: ir.EvUsage}
	}
	s.st.MarkWritten(ev)
	return s.w.Write(ir.SSEEvent{Data: string(b)})
}

// streamReader 把补全 SSE 帧解成 IR 事件。
type streamReader struct {
	r        *ir.SSEReader
	pending  []ir.Event
	stop     ir.StopReason
	finished bool
}

func (adapter) NewStreamReader(r io.Reader, _ ir.DecodeOptions) ir.StreamReader {
	return &streamReader{r: ir.NewSSEReader(r)}
}

func (s *streamReader) Next() (ir.Event, error) {
	for {
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, nil
		}
		if s.finished {
			return ir.Event{}, io.EOF
		}
		se, err := s.r.Next()
		if err != nil {
			if err == io.EOF {
				s.finished = true
				continue
			}
			return ir.Event{}, err
		}
		if strings.TrimSpace(se.Data) == "[DONE]" {
			s.finished = true
			s.pending = append(s.pending, ir.Event{Type: ir.EvDone, Stop: s.stop})
			continue
		}
		var c chunk
		if err := json.Unmarshal([]byte(se.Data), &c); err != nil {
			return ir.Event{}, &ir.UpstreamProtocolError{Detail: "补全流帧不是合法 JSON: " + err.Error()}
		}
		if c.Usage != nil {
			u := decodeUsage(c.Usage)
			s.pending = append(s.pending, ir.Event{Type: ir.EvUsage, Usage: &u})
		}
		if len(c.Choices) == 0 {
			continue
		}
		ch := c.Choices[0]
		if ch.FinishReason != nil {
			s.stop = finishToStop(*ch.FinishReason)
		}
		if ch.Text != "" {
			s.pending = append(s.pending, ir.Event{Type: ir.EvTextDelta, Text: ch.Text})
		}
	}
}

var _ ir.Adapter = adapter{}
