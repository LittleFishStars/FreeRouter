package httpapi

// v1completions.go —— POST /v1/completions（DeepSeek 官方 FIM Completion API，Beta）。
//
// 入站方言：OpenAI 兼容的文本补全（prompt + suffix → 中间文本），
// 与 /v1/chat/completions 是**两个协议**：请求体用 prompt/suffix 而不是 messages，
// 响应体用 choices[].text 而不是 choices[].message。
//
// 为什么单独开文件而不是在 v1chat.go 里加分支：两个端点的方言不同、错误体定位
// 不同（prompt vs messages），共用一个 handler 会让每个校验都变成 if 分支。
// 编排骨架（候选循环、流式、结算）仍然共用 proxyV1——那才是真正该共享的部分。

import (
	"net/http"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai/completion"
)

// HandleV1Completions 处理 POST /v1/completions。
func (s *Server) HandleV1Completions(w http.ResponseWriter, r *http.Request) {
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

	ad := completion.Adapter()
	req, warns, err := ad.DecodeRequest(body)
	if err != nil {
		payload := classifyErr(err)
		s.writeV1Error(w, payload.code, payload.message, requestID)
		return
	}
	for _, warn := range warns {
		s.log.Warn("v1 completions decode warning", "request_id", requestID, "param", warn.Param, "message", warn.Message)
	}
	req.Stream = isStreamRequest(r, body)

	if err := req.Validate(); err != nil {
		payload := classifyErr(err)
		s.writeV1Error(w, payload.code, payload.message, requestID)
		return
	}

	if req.Stream {
		setSSEHeaders(w)
	}

	s.proxyV1(r.Context(), w, proxyReq{
		req:            *req,
		inbound:        adapterFor(ir.ProtocolOpenAICompletion),
		endpoint:       "openai-completion", // requests.endpoint 词表见 0001_init.sql
		traceRequested: wantsTrace(r),
	}, requestID)
}
