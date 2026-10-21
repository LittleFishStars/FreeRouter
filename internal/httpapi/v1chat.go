package httpapi

// v1chat.go —— POST /v1/chat/completions（task-8 范围①）。
//
// 入站契约（docs/api/01-inbound.md §2/§4）：OpenAI Chat Completions 方言，
// 流式 + 非流式。编解码全部走 ir（Decoder→上游→Encoder），本文件只做
// HTTP 面的薄壳：解析、鉴权占位、交给公共编排骨架 proxyV1。

import (
	"net/http"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai"
)

// HandleV1Chat 处理 POST /v1/chat/completions。
// 路由挂载归 Lead（server.go 铁律），这里导出 handler 方法。
func (s *Server) HandleV1Chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}

	requestID := requestIDOf(r)
	ensureHeaders(w, requestID)

	// Key 鉴权在 mw_keyauth.go（authn 任务域）；这里防御性检查 ctx 里
	// 是否已有账户——没有就 401 invalid_api_key（01-inbound §2.2：不区分
	// 缺失/无效/禁用文案，防探测）。测试注入 ctx 模拟已鉴权。
	if !hasAuthedAccount(r) {
		s.writeV1Error(w, "invalid_api_key", "缺少有效的 API Key", requestID)
		return
	}

	body, ok := decodeInboundBody(w, r)
	if !ok {
		return
	}

	ad := openai.Adapter()
	req, warns, err := ad.DecodeRequest(body)
	if err != nil {
		// 解码失败 = 客户端方言非法：400 invalid_request（能力检查
		// C6 的 UnsupportedError 也可能从 DecodeRequest 出来——统一
		// 走 classifyErr 拿正确细码）。
		payload := classifyErr(err)
		s.writeV1Error(w, payload.code, payload.message, requestID)
		return
	}
	for _, warn := range warns {
		s.log.Warn("v1 chat decode warning", "request_id", requestID, "param", warn.Param, "message", warn.Message)
	}
	req.Stream = isStreamRequest(r, body)

	// 真实模型名 404：模型解析前先判「请求里的模型名是否是 real_models
	// 域的 id」——M5 契约（03-milestones：不泄露池结构）。M6 抽象模型
	// 路由上线后此检查移除。
	if s.v1Candidates() != nil && isRealModelName(r.Context(), req.Model) {
		s.writeV1Error(w, "model_not_found", "模型不存在", requestID)
		return
	}

	if err := req.Validate(); err != nil {
		payload := classifyErr(err)
		s.writeV1Error(w, payload.code, payload.message, requestID)
		return
	}

	// SSE 透传（task-8 §5）：chunked 直写、不在中间缓冲——流式头先立
	// 起来，后续字节经 StreamWriter 逐帧写出（proxyV1→streamOnce 内）。
	// 2026-10-21 装配收口（code-review 发现 C）：改调 setSSEHeaders 统一——
	// Connection: keep-alive 由 net/http 自理，显式设在 HTTP/2 下非法。
	if req.Stream {
		setSSEHeaders(w)
	}

	s.proxyV1(r.Context(), w, proxyReq{
		req:     *req,
		inbound: adapterFor(ir.ProtocolOpenAIChat),
	}, requestID)
}
