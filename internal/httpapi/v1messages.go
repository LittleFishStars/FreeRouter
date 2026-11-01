package httpapi

// v1messages.go —— POST /v1/messages（task-8 范围③）。
//
// 入站契约（docs/api/01-inbound.md §6）：Anthropic Messages 方言。
// - x-api-key 或 Authorization Bearer 二选一（Claude Code 发 x-api-key——
//   只支持 Authorization 会让验收第 2 条在客户端侧直接失败）。
// - anthropic-version 头接受并回显（SDK 校验它）。
// - max_tokens 必填且不补默认（§6.1：补默认值是帮客户端掩盖成本预算）。
// - SSE 具名事件（message_start→content_block_*→message_delta→message_stop），
//   无 [DONE]、不发 ping（§6.3 待定项确认）。
// - 错误帧 event: error（03-errors §2.2 Anthropic 形状）。

import (
	"encoding/json"
	"net/http"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/anthropic"
)

// HandleV1Messages 处理 POST /v1/messages。
func (s *Server) HandleV1Messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeV1AnthropicError(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}

	requestID := requestIDOf(r)
	ensureHeaders(w, requestID)
	// anthropic-version 回显（§3.3）：接受任何值并原样回写——Anthropic
	// SDK 会校验响应里这个头是否存在，不回显会让客户端把成功当失败。
	if v := r.Header.Get("anthropic-version"); v != "" {
		w.Header().Set("anthropic-version", v)
	}

	// 鉴权：x-api-key 或 Authorization 二选一（authn 中间件注入 ctx；
	// 这里防御性检查，缺失 401 invalid_api_key，文案不区分防探测）。
	if !hasAuthedAccount(r) {
		s.writeV1AnthropicError(w, "invalid_api_key", "缺少有效的 API Key", requestID)
		return
	}

	body, ok := decodeInboundBody(w, r)
	if !ok {
		return
	}

	// max_tokens 不再强制（2026-11-02）：DeepSeek 官方 Anthropic 兼容面已把它改成
	// 请求可选（api-docs /guides/anthropic_api：Optional in HTTP requests，默认与
	// 有效上限取决于模型、思考模式与 effort）。以前在这里硬判会让官方 SDK
	// 省略该字段的调用直接 400——这是把「上游放宽的契约」变成我们的硬错误。
	// 缺失时由上游取默认值；我们不补默认（补默认值等于替用户定成本预算）。
	var probe struct {
		MaxTokens *int `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &probe); err == nil && probe.MaxTokens != nil && *probe.MaxTokens <= 0 {
		// 显式给了非正数才是非法：官方要求它是正整数上限。
		s.writeV1AnthropicError(w, "invalid_request", "max_tokens 必须是正整数", requestID)
		return
	}

	ad := anthropic.Adapter()
	req, warns, err := ad.DecodeRequest(body)
	if err != nil {
		payload := classifyErr(err)
		s.writeV1AnthropicError(w, payload.code, payload.message, requestID)
		return
	}
	for _, warn := range warns {
		s.log.Warn("v1 messages decode warning", "request_id", requestID, "param", warn.Param, "message", warn.Message)
	}
	req.Stream = isStreamRequest(r, body)

	if err := req.Validate(); err != nil {
		payload := classifyErr(err)
		s.writeV1AnthropicError(w, payload.code, payload.message, requestID)
		return
	}

	// SSE 头（§6.3）：text/event-stream；无 [DONE]、不发 ping 由
	// anthropic StreamWriter 自理（ir 层已按具名事件实现）。
	if req.Stream {
		setSSEHeaders(w) // 同 v1chat：统一收口，Connection 头由 net/http 自理
	}

	s.proxyV1(r.Context(), w, proxyReq{
		req:            *req,
		inbound:        adapterFor(ir.ProtocolAnthropic),
		anthropicShape: true,
		traceRequested: wantsTrace(r),
		endpoint:       "anthropic-messages", // 同上：词表受 CHECK 约束
	}, requestID)
}
