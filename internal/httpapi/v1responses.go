package httpapi

// v1responses.go —— POST /v1/responses（task-8 范围②）。
//
// OpenAI Responses API 入站（docs/api/01-inbound.md §9 P1 / 02-ir.md §7.1）。
// internal/ir 没有 responses 适配器（M5 范围只有 openai_chat/anthropic）——
// 所以入站解码在本文件自做：Responses wire → ir.Request（§7.1 映射表），
// 上游仍走 openai_chat 方言（候选 EncodeRequest），出站把 ir.Response
// 编回 Responses 形状。这是「方言网关」模式：入站/出站 Responses，
// 中间 IR，上游 Chat。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai"
)

// ---------------------------------------------------------------------------
// Responses wire → IR（02-ir.md §7.1 映射表的落地）
// ---------------------------------------------------------------------------

// applyPatchToolName 是官方唯一支持的 custom 工具（Codex 兼容面）。
const applyPatchToolName = "apply_patch"

// applyPatchSchema 把 apply_patch 的自由文本入参表达成单字段 JSON Schema。
// 为什么这样声明：官方 custom 工具的 input 是一个字符串（不是 JSON 对象），
// 而 IR 的工具定义只承载 JSON Schema；用单字符串字段是对该形状最小且无损的翻译。
const applyPatchSchema = `{"type":"object","properties":{"__raw_text":{"type":"string","description":"完整的 apply_patch 文本补丁"}},"required":["__raw_text"]}`

// responsesRequest 是入站 wire 形状（只列我们消费的字段；未知字段
// 忽略——Responses 面很大，M5 只实现最小集，其余留白由后续版本补）。
type responsesRequest struct {
	Model           string              `json:"model"`
	Instructions    string              `json:"instructions"`
	Input           json.RawMessage     `json:"input"` // string 或 []responsesInputItem
	Tools           []responsesTool     `json:"tools"`
	ToolChoice      json.RawMessage     `json:"tool_choice"`
	MaxOutputTokens *int                `json:"max_output_tokens"`
	Temperature     *float64            `json:"temperature"`
	TopP            *float64            `json:"top_p"`
	Reasoning       *responsesReasoning `json:"reasoning"`
	Text            *responsesText      `json:"text"`
	Store           *bool               `json:"store"`
	// previous_response_id：P1 无状态先 400（§7.1 待定——无状态转发
	// 不能假装记得上一轮；明确了报错比静默丢上下文诚实）。
	PreviousResponseID string          `json:"previous_response_id"`
	Metadata           json.RawMessage `json:"metadata"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesText struct {
	Format json.RawMessage `json:"format"` // text.format → ResponseFormat
}

// responsesInputItem 是 input 数组元素：字符串简写或完整 item。
type responsesInputItem struct {
	Type    string          `json:"type"` // message / function_call / function_call_output
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	// function_call item：
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// function_call_output / custom_tool_call_output item：
	// Output 用 RawMessage：官方允许它是字符串或 content part 数组
	//（数组里可带 input_image，让工具把截图回喂给模型）。
	Output json.RawMessage `json:"output"`
	// custom_tool_call item 的入参是原始文本补丁（apply_patch），字段名是 input。
	Input string `json:"input"`
}

type responsesTool struct {
	Type        string          `json:"type"` // function
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
}

// decodeResponsesRequest 把 Responses wire 解成 ir.Request（§7.1）。
func decodeResponsesRequest(body []byte) (*ir.Request, []ir.Warning, error) {
	var raw responsesRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, &ir.InvalidRequestError{Param: "body", Detail: fmt.Sprintf("请求体不是合法 JSON：%v", err)}
	}
	var warns []ir.Warning

	// store:true 必须 400（§8.4 白名单表外硬规则：客户端会拿
	// previous_response_id 取回，无状态转发不能假装收了）。
	if raw.Store != nil && *raw.Store {
		return nil, nil, &ir.InvalidRequestError{Param: "store", Detail: "无状态转发不支持 store=true（服务端不会保存响应，previous_response_id 将不可用）"}
	}
	if raw.PreviousResponseID != "" {
		// TODO(P1)：有状态会话落地后支持（§7.1 待定项）。
		return nil, nil, &ir.InvalidRequestError{Param: "previous_response_id", Detail: "无状态模式下 previous_response_id 不可用"}
	}
	if raw.Store == nil || !*raw.Store {
		// store:false/缺省 → 白名单忽略（§8.4：客户端只是提示「别替我存」，
		// 我们本来就不存——忽略并告警保证可见性）。
		if raw.Store != nil {
			warns = append(warns, ir.Warning{Param: "store", Message: "store=false 已忽略（无状态转发本来不存储）"})
		}
	}

	req := &ir.Request{Model: raw.Model}

	// instructions → RoleSystem（§7.1：Responses 用顶层 instructions 承载
	// 系统指令，IR 归一为 system 消息，排在最前）。
	if raw.Instructions != "" {
		req.Messages = append(req.Messages, ir.Message{
			Role: ir.RoleSystem,
			Parts: []ir.Part{
				ir.TextPart{Text: raw.Instructions},
			},
		})
	}

	// input：string 简写或 item 数组。
	if len(raw.Input) > 0 {
		var asStr string
		if err := json.Unmarshal(raw.Input, &asStr); err == nil && asStr != "" {
			req.Messages = append(req.Messages, ir.Message{
				Role: ir.RoleUser,
				Parts: []ir.Part{
					ir.TextPart{Text: asStr},
				},
			})
		} else {
			var items []responsesInputItem
			if err := json.Unmarshal(raw.Input, &items); err != nil {
				return nil, nil, &ir.InvalidRequestError{Param: "input", Detail: "input 必须是字符串或输入项数组"}
			}
			for _, item := range items {
				decodeResponsesItem(req, item)
			}
		}
	}

	// tools：Responses 平铺 → ir.ToolDef。
	for _, t := range raw.Tools {
		switch t.Type {
		case "function", "":
			req.Tools = append(req.Tools, ir.ToolDef{
				Name:        t.Name,
				Description: t.Description,
				Schema:      string(t.Parameters),
			})
		case "custom":
			// 官方只支持 name=apply_patch 这一种 custom 工具（Codex 兼容，api-docs
			// /guides/responses_api）。它的入参是自由文本补丁而非 JSON Schema，
			// 用 schema 声明成「一个字符串字段」才能在上游侧无损表达；
			// 其余 custom 名字官方要求 400，我们照做。
			if t.Name != applyPatchToolName {
				return nil, warns, &ir.UnsupportedError{
					Capability: ir.CapTools,
					Protocol:   ir.ProtocolOpenAIResponses,
					Detail:     fmt.Sprintf("custom 工具 %q 不支持（官方只接受 name=%s）", t.Name, applyPatchToolName),
				}
			}
			req.Tools = append(req.Tools, ir.ToolDef{
				Name:        t.Name,
				Description: t.Description,
				Schema:      applyPatchSchema,
			})
		case "web_search":
			// web_search 是服务端工具：由上游自己执行（官方 deepseek-v4-pro 支持）。
			// 它不是面向模型的函数调用，IR 没有服务端工具的表达位——按 §8.4 白名单
			// 忽略并告警，而不是 400：官方明确「不支持的参数被静默忽略，
			// 存量客户端无需改动就能接」。
			warns = append(warns, ir.Warning{Param: "tools.web_search",
				Message: "web_search 是服务端工具，网关不代为执行；如需联网请由客户端直接调用上游"})
		default:
			// file_search / code_interpreter / computer_use / mcp 等内置工具
			// 官方同样「ignored」，我们跟随官方语义忽略并告警。
			warns = append(warns, ir.Warning{Param: "tools." + t.Type,
				Message: "Responses 内置工具类型 " + t.Type + " 已忽略（官方语义同样忽略）"})
		}
	}

	// max_output_tokens → Sampling.MaxOutput；temperature/top_p 同理。
	req.Sampling.MaxOutput = raw.MaxOutputTokens
	req.Sampling.Temperature = raw.Temperature
	req.Sampling.TopP = raw.TopP

	// reasoning.effort → ir.Effort（§7.1）。
	if raw.Reasoning != nil && raw.Reasoning.Effort != "" {
		req.Effort = parseResponsesEffort(raw.Reasoning.Effort)
	}

	// text.format → ResponseFormat（§7.1）。
	if raw.Text != nil && len(raw.Text.Format) > 0 {
		var fmtProbe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw.Text.Format, &fmtProbe); err == nil && fmtProbe.Type != "" {
			req.Format = &ir.ResponseFormat{
				Kind: parseResponsesFormatKind(fmtProbe.Type),
				// 原样透传 schema（IR 不二次解析 JSON Schema——上游自己判）。
				Schema: string(raw.Text.Format),
			}
		}
	}

	return req, warns, nil
}

// reasoningItemText 取 reasoning item 的纯文本 content（官方只支持纯文本思考残留）。
func reasoningItemText(item responsesInputItem) string {
	var s string
	if json.Unmarshal(item.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(item.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// responsesOutputText 把 tool output 摊平成文本（自定义工具回执只需文本）。
func responsesOutputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

// responsesOutputParts 把 tool output 解成 IR parts：字符串 → 文本 part；
// 数组 → 文本与图片 part（input_image 走与 Chat 同一套解析，规则不能两处各写一份）。
func responsesOutputParts(raw json.RawMessage) []ir.Part {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []ir.Part{ir.TextPart{Text: s}}
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return []ir.Part{ir.TextPart{Text: string(raw)}}
	}
	out := make([]ir.Part, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text":
			out = append(out, ir.TextPart{Text: p.Text})
		case "input_image":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				out = append(out, ir.TextPart{Text: "[图片输入缺失 image_url]"})
				continue
			}
			img, err := openai.DecodeImageURL(p.ImageURL.URL)
			if err != nil {
				out = append(out, ir.TextPart{Text: "[图片输入无法解析: " + err.Error() + "]"})
			} else {
				out = append(out, img)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, ir.TextPart{Text: string(raw)})
	}
	return out
}

// decodeResponsesItem 解单个 input item（message / function_call / output）。
func decodeResponsesItem(req *ir.Request, item responsesInputItem) {
	switch item.Type {
	case "reasoning":
		// 官方：input 里 type="reasoning" 的纯文本 content 合并进相邻 assistant 消息
		// （api-docs /guides/responses_api）。summary 与 encrypted_content 不支持。
		// 单独成 assistant 的 ThinkingPart，让多轮思考历史在跨协议转发时仍可见。
		text := reasoningItemText(item)
		if text != "" {
			req.Messages = append(req.Messages, ir.Message{
				Role:  ir.RoleAssistant,
				Parts: []ir.Part{ir.ThinkingPart{Idx: 0, Text: text}},
			})
		}
	case "custom_tool_call":
		// apply_patch 的调用历史（官方 custom_tool_call item，call_id 配对）。
		req.Messages = append(req.Messages, ir.Message{
			Role: ir.RoleAssistant,
			Parts: []ir.Part{ir.ToolCallPart{
				ID:   item.CallID,
				Name: applyPatchToolName,
				// 官方 custom 调用的入参是原始文本（input 字段），IR 里统一按字符串承载。
				Arguments: string(item.Input),
			}},
		})
	case "custom_tool_call_output":
		text := responsesOutputText(item.Output)
		req.Messages = append(req.Messages, ir.Message{
			Role: ir.RoleUser,
			Parts: []ir.Part{ir.ToolResultPart{
				ToolCallID: item.CallID,
				Parts:      []ir.Part{ir.TextPart{Text: text}},
			}},
		})
	case "function_call":
		// §7.1：function_call item = 历史 assistant 工具调用。
		req.Messages = append(req.Messages, ir.Message{
			Role: ir.RoleAssistant,
			Parts: []ir.Part{
				ir.ToolCallPart{
					ID:        item.CallID,
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			},
		})
	case "function_call_output":
		// §7.1：function_call_output = 工具回执（user 消息，IR 契约）。
		// output 可以是字符串，也可以是 content part 数组（官方支持 output 里带
		// input_image，让工具能把截图回喂给模型）。两种形状都要解。
		req.Messages = append(req.Messages, ir.Message{
			Role: ir.RoleUser,
			Parts: []ir.Part{
				ir.ToolResultPart{
					ToolCallID: item.CallID,
					Parts:      responsesOutputParts(item.Output),
				},
			},
		})
	default:
		// message item（type 缺省也是 message）。
		role := parseResponsesRole(item.Role)
		// content：字符串简写或 content part 数组。
		var asStr string
		if err := json.Unmarshal(item.Content, &asStr); err == nil && asStr != "" || len(item.Content) == 0 {
			if len(item.Content) == 0 {
				asStr = ""
			}
			req.Messages = append(req.Messages, ir.Message{
				Role:  role,
				Parts: []ir.Part{ir.TextPart{Text: asStr}},
			})
			return
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			// input_image 的图片 URL/data（Responses 的 image_url 对象）。
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(item.Content, &parts); err == nil {
			msg := ir.Message{Role: role}
			for _, p := range parts {
				switch p.Type {
				case "input_text", "output_text", "text":
					msg.Parts = append(msg.Parts, ir.TextPart{Text: p.Text})
				case "input_image":
					// Responses 的 input_image 与 Chat 的 image_url 是同一张图——
					// 复用 openai 的解析（data URL 内联解码 / http(s) URL 保留
					// 引用），规则必须一致，不能两处各写一份。
					if p.ImageURL == nil || p.ImageURL.URL == "" {
						msg.Parts = append(msg.Parts, ir.TextPart{Text: "[图片输入缺失 image_url]"})
						continue
					}
					img, ierr := openai.DecodeImageURL(p.ImageURL.URL)
					if ierr != nil {
						// §10：无法表达的 part 不静默丢弃——转成可读占位并继续。
						msg.Parts = append(msg.Parts, ir.TextPart{Text: "[图片输入无法解析: " + ierr.Error() + "]"})
					} else {
						msg.Parts = append(msg.Parts, img)
					}
				}
			}
			req.Messages = append(req.Messages, msg)
		}
	}
}

// ---------------------------------------------------------------------------
// IR → Responses wire（出站方向）
// ---------------------------------------------------------------------------

// encodeResponsesResponse 把 ir.Response 编成 Responses 形状（非流式）。
// M5 最小面：output_text / function_call / reasoning 三类 part。
func encodeResponsesResponse(resp *ir.Response, reqID string) ([]byte, error) {
	out := responsesOut{
		ID:     "resp_" + reqID,
		Object: "response",
		Model:  resp.Model,
		Status: "completed",
	}
	// usage 三态（02-ir §9）：nil/0/estimated——Responses 面只报 in/out/total；
	// 指针为 nil 表示上游没给，整个 usage 省略（不虚报 0）。
	if resp.Usage.InputTokens != nil || resp.Usage.OutputTokens != nil {
		in, outT := 0, 0
		if resp.Usage.InputTokens != nil {
			in = *resp.Usage.InputTokens
		}
		if resp.Usage.OutputTokens != nil {
			outT = *resp.Usage.OutputTokens
		}
		out.Usage = &responsesUsage{InputTokens: in, OutputTokens: outT, TotalTokens: in + outT}
	}

	for _, part := range resp.Parts {
		switch p := part.(type) {
		case ir.TextPart:
			out.Output = append(out.Output, responsesOutItem{
				Type: "message",
				Role: "assistant",
				Content: []responsesOutContent{{
					Type: "output_text",
					Text: p.Text,
				}},
			})
		case ir.ToolCallPart:
			out.Output = append(out.Output, responsesOutItem{
				Type:      "function_call",
				CallID:    p.ID,
				Name:      p.Name,
				Arguments: p.Arguments,
			})
		case ir.ThinkingPart:
			// reasoning 摘要面：Responses 的 reasoning item 带 summary 数组；
			// IR ThinkingPart 是正文思考——M5 先以 summary 形式透出。
			out.Output = append(out.Output, responsesOutItem{
				Type: "reasoning",
				Summary: []responsesOutContent{{
					Type: "summary_text",
					Text: p.Text,
				}},
			})
		}
	}
	return json.Marshal(out)
}

type responsesOut struct {
	ID     string             `json:"id"`
	Object string             `json:"object"`
	Status string             `json:"status"`
	Model  string             `json:"model"`
	Output []responsesOutItem `json:"output"`
	Usage  *responsesUsage    `json:"usage,omitempty"`
}

type responsesOutItem struct {
	Type      string                `json:"type"`
	Role      string                `json:"role,omitempty"`
	Content   []responsesOutContent `json:"content,omitempty"`
	CallID    string                `json:"call_id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Arguments string                `json:"arguments,omitempty"`
	Summary   []responsesOutContent `json:"summary,omitempty"`
}

type responsesOutContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

// HandleV1Responses 处理 POST /v1/responses。
func (s *Server) HandleV1Responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}
	requestID := requestIDOf(r)
	ensureHeaders(w, requestID)

	if !hasAuthedAccount(r) {
		s.writeV1Error(w, "invalid_api_key", "缺少有效的 API Key", requestID)
		return
	}

	body, ok := decodeInboundBody(w, r)
	if !ok {
		return
	}

	req, warns, err := decodeResponsesRequest(body)
	if err != nil {
		payload := classifyErr(err)
		s.writeV1Error(w, payload.code, payload.message, requestID)
		return
	}
	for _, warn := range warns {
		s.log.Warn("v1 responses decode warning", "request_id", requestID, "param", warn.Param, "message", warn.Message)
	}
	req.Stream = isStreamRequest(r, body)

	if err := req.Validate(); err != nil {
		payload := classifyErr(err)
		s.writeV1Error(w, payload.code, payload.message, requestID)
		return
	}

	// 候选统一先查（流式与非流式都要），再按 Stream 分流。
	cs := s.v1Candidates()
	cands, cerr := cs.Candidates(r.Context(), req.Model, *req)
	if errors.Is(cerr, ErrModelNotFound) {
		s.writeV1Error(w, "model_not_found", "模型不存在", requestID) // 与 proxyV1 同口径
		return
	}
	if cerr != nil || len(cands) == 0 {
		s.writeV1Error(w, "no_candidate", "当前无可用上游候选", requestID)
		return
	}

	if req.Stream {
		// 流式：用 response.output_text.delta 等具名事件。候选循环内由
		// proxyResponsesStreamOnce 逐帧写 SSE；所有上游候选失败时 onExhausted
		// 输出 response.failed 事件帧（此刻头可能已按流式发出，不能写 JSON 体）。
		s.setSSEOnce(w)
		s.runCandidates(r.Context(), w, requestID, cs, cands, func(cand Candidate) (bool, *AttemptFailure) {
			return s.proxyResponsesStreamOnce(r.Context(), w, *req, cand, requestID)
		}, func() {
			s.writeResponsesStreamError(w, requestID, "全部上游候选失败")
		})
		return
	}

	// 非流式走 proxyOnce 逐候选；出站编码是 Responses 形状。
	s.runCandidates(r.Context(), w, requestID, cs, cands, func(cand Candidate) (bool, *AttemptFailure) {
		return s.proxyResponsesOnce(r.Context(), w, *req, cand, requestID)
	}, func() {
		s.writeV1Error(w, "upstream_unavailable", "所有上游候选均失败", requestID)
	})
}

// proxyResponsesOnce 单候选非流式代理（Responses 出站编码版 proxyOnce）。
func (s *Server) proxyResponsesOnce(ctx context.Context, w http.ResponseWriter, req ir.Request, cand Candidate, reqID string) (bool, *AttemptFailure) {
	upOpt := ir.EncodeOptions{UpstreamModel: cand.UpstreamModelID, RequestID: reqID}
	_, warns, err := cand.Adapter.EncodeRequest(&req, upOpt)
	if err != nil {
		return false, &AttemptFailure{Kind: AttemptFailureRequest, Err: err,
			Detail: "请求编码失败（能力缺失/方言冲突）"}
	}
	for _, warn := range warns {
		s.log.Warn("v1 responses upstream encode warning", "request_id", reqID, "param", warn.Param, "message", warn.Message)
	}
	call, err := s.v1Upstream().Call(ctx, cand, req, false)
	if err != nil || call == nil {
		return false, &AttemptFailure{Err: err, Detail: "上游调用未返回结果"}
	}
	defer call.Body.Close()
	if call.Err != nil {
		return false, &AttemptFailure{Err: call.Err, Detail: "上游返回失败"}
	}
	raw, err := io.ReadAll(call.Body)
	if err != nil {
		return false, &AttemptFailure{Err: err, Detail: "读取上游响应体失败"}
	}
	resp, err := cand.Adapter.DecodeResponse(raw)
	if err != nil {
		return false, &AttemptFailure{Kind: AttemptFailureProtocol, Err: err, Detail: "上游响应体不可解析"}
	}
	// §4.1：出站 model 恒为抽象名（与 proxyOnce 同规则——防泄露池结构）。
	resp.Model = req.Model
	out, err := encodeResponsesResponse(resp, reqID)
	if err != nil {
		return false, &AttemptFailure{Kind: AttemptFailureRequest, Err: err, Detail: "响应转码失败"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return true, nil
}

// proxyResponsesStreamOnce 单候选流式代理（Responses 出站编码版 streamOnce）。
//
// 上游是 Chat-compatible 流（cand.Adapter.NewStreamReader），出站是 Responses
// 具名事件流（proxyResponsesStreamWriter，见 internal/ir/openai/responses_stream.go）。
//
// 返回 true = 已写出可见内容（C7 不可再换候选）；false = 该候选可跳过（换下一个）。
func (s *Server) proxyResponsesStreamOnce(ctx context.Context, w http.ResponseWriter, req ir.Request, cand Candidate, reqID string) (bool, *AttemptFailure) {
	// 起时点用于 TTFT：必须在编码/上游握手前取，否则首包延迟被低估（同 streamOnce）。
	start := time.Now()
	upOpt := ir.EncodeOptions{UpstreamModel: cand.UpstreamModelID, RequestID: reqID}
	_, warns, err := cand.Adapter.EncodeRequest(&req, upOpt)
	if err != nil {
		return false, &AttemptFailure{Kind: AttemptFailureRequest, Err: err,
			Detail: "请求编码失败（能力缺失/方言冲突）"}
	}
	for _, warn := range warns {
		s.log.Warn("v1 responses stream encode warning", ttftLogField, reqID, "param", warn.Param, "message", warn.Message)
	}
	call, err := s.v1Upstream().Call(ctx, cand, req, true)
	if err != nil || call == nil {
		return false, &AttemptFailure{Err: err, Detail: "上游调用未返回结果"}
	}
	defer call.Body.Close()
	if call.Err != nil {
		return false, &AttemptFailure{Err: call.Err, Detail: "上游返回失败"}
	}

	// 上游 Chat 流事件 → IR 事件 → Responses 出站 writer。
	reader := cand.Adapter.NewStreamReader(call.Body, ir.DecodeOptions{RequestID: reqID})
	writer := openai.NewResponsesStreamWriter(w, req.Model)

	ttft := time.Now()
	first := true
	for {
		ev, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // 上游正常结束
			}
			break
		}
		if ev.Type == ir.EvDone {
			_ = writer.Write(ev)
			break
		}
		if !writer.State().Accept(ev) {
			continue
		}
		if err := writer.Write(ev); err != nil {
			break
		}
		if first && writer.State().Visible() {
			// 首段可见内容时刻——TTFT 从「发起请求」算到这个瞬间才准确。
			ttft = time.Now()
			s.log.Info("v1 responses stream ttft", "request_id", reqID, "ttft_ms", ttft.Sub(start).Milliseconds())
			first = false
		}
	}
	_ = ttft
	_ = writer.Close()
	// 已开流即成功（与 streamOnce 同一口径：中途断开可能是客户端，不记不可用）。
	return true, nil
}

// setSSEOnce 给流式响应立头。幂等：多次调用只第一次设（WriteHeader 后改无效）。
func (s *Server) setSSEOnce(w http.ResponseWriter) {
	w.Header().Set("Content-Type", sseContentType)
	w.Header().Set("Cache-Control", sseCacheControl)
	// 断言 Flush 支持：SSE 依赖它逐帧发出，不支持会攒在缓冲里（TTFT 失真）。
	if f := flusher(w); f != nil {
		f.Flush()
	}
}

// writeResponsesStreamError 在流式响应已发出（头不可改）后写 error 事件。
// 与 completed 同形状，便于客户端统一解析。
func (s *Server) writeResponsesStreamError(w http.ResponseWriter, reqID, msg string) {
	// 走与正常路径同一个 writer，保证事件形状不漂移（手写帧易与 writer 不一致）。
	wr := openai.NewResponsesStreamWriter(w, "")
	_ = wr.Write(ir.Event{Type: ir.EvError, Err: &ir.Error{Code: "upstream_unavailable", Message: msg}})
	_ = wr.Close()
	if f := flusher(w); f != nil {
		f.Flush()
	}
	_ = reqID
}

// parseResponsesEffort 把 Responses 的 reasoning.effort 归一到 ir.Effort。
// 不认识的档位标 EffortUnknown（02-ir §4：交能力匹配而非静默按未指定）。
func parseResponsesEffort(e string) ir.Effort {
	switch e {
	case "low":
		return ir.EffortLow
	case "medium", "high":
		return ir.EffortHigh
	case "minimal":
		return ir.EffortOff
	default:
		return ir.EffortUnknown
	}
}

// parseResponsesFormatKind 把 text.format.type 归一到 ir.ResponseFormatKind。
func parseResponsesFormatKind(t string) ir.ResponseFormatKind {
	switch t {
	case "json_object":
		return ir.FormatJSONObject
	case "json_schema":
		return ir.FormatJSONSchema
	default:
		return ir.FormatText
	}
}

// parseResponsesRole 把 item.role 归一到 ir.Role（非法值按 user——
// Responses 面的 role 枚举比 IR 宽，落不进三种 role 的按 user 处理
// 并保留原文在消息文本里会更糟；直接归 user 与 OpenAI 行为一致）。
func parseResponsesRole(r string) ir.Role {
	switch r {
	case "system", "developer":
		return ir.RoleSystem
	case "assistant":
		return ir.RoleAssistant
	default:
		return ir.RoleUser
	}
}
