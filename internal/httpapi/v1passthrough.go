package httpapi

// v1passthrough.go —— 非推理端点的**原样转发**（Files / Search & Fetch）。
//
// 口径（2026-11-02 用户明确）：**直接把请求对接到供应商，我们不做处理**。
// 所以这里没有 IR、没有编解码、没有字段校验——网关只做三件事：
//   1. 鉴权（客户端持 fr- Key）；
//   2. 选一个上游（沿用同一套模型/账号归属口径）；
//   3. 把请求与响应原样搬运（只有 Authorization 被换成上游凭据）。
//
// 为什么不做校验/转换：这些端点的语义（multipart 边界、图片二进制、
// 搜索的 query 运算符、fetch 的 markdown 转换）全都是上游自己的事。
// 网关一旦开始"理解"它们，就要为每个上游的差异各自维护一套代码，
// 而收益为零——我们不转换文件，也不重排搜索结果。
//
// 与推理端点的差别（刻意保留）：不落 usage、不参与候选冷却、不换候选重试。
// 理由：文件上传不可重放（body 是流），搜索/抓取有上游计费但无 token 语义。
// 失败就如实返回上游的状态码——那是这些 API 契约的一部分。

import (
	"context"
	"io"
	"net/http"
)

// PassthroughUpstream 是装配层提供的透传执行面。
//
// 实现归 internal/app：它需要 store（上游行/凭据）与 upstream（HTTP 传输），
// 而 httpapi 不该认识其中任何一个（依赖方向铁律）。
//
// 为什么不复用 Upstream.Call：Call 的入参是 ir.Request（已编码的方言模型），
// 而透传的入参是原始 HTTP 请求——两者形状不同是**设计目标**，不是遗漏。
// 硬塞进 Call 会逼着 Upstream 实现去判"这次是不是透传"。
type PassthroughUpstream interface {
	// Passthrough 把请求原样转发到一个可用上游，并返回上游原始响应。
	//
	// model 为空表示该端点不按模型选上游（Files/Search 没有模型概念），
	// 此时由实现方按"该账号可用的上游"挑一个。
	Passthrough(ctx context.Context, in PassthroughInput) (*PassthroughResponse, error)
}

// PassthroughInput 是一次透传的入参（httpapi → 装配层）。
type PassthroughInput struct {
	// AccountID / KeyID 用于归属与审计（透传不落 usage，但日志要能归因）。
	AccountID string
	KeyID     string
	// Model 是请求里出现的模型名（Files/Search 没有该概念，可为空）。
	Model string
	// Endpoint 是上游路径（如 "/files"、"/v1/tools/search"）。
	// 为什么由 handler 显式给出而不是从 r.URL 推断：同一个上游路径可能要
	// 对应不同的入站路径，且装配层的上游可能是另一套 base_url 布局。
	Endpoint string
	// RequestID 用于日志与对账。
	RequestID string
	// Method / RawQuery / Header / Body 全部是客户端原始值，原样转发。
	Method   string
	RawQuery string
	Header   http.Header
	Body     io.Reader
	// ContentLength 原样带出（multipart 上传需要它，否则 chunked 编码
	// 会让部分上游拒绝或退化为缓冲）。
	ContentLength int64
}

// PassthroughResponse 是上游原始响应（装配层 → httpapi）。
//
// Status/Header/Body 三者都原样来自上游：handler 直接写回客户端，
// 不重新分类状态码、不翻译错误体——那是上游 API 契约的一部分。
type PassthroughResponse struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
	// UpstreamID 是命中上游的行 id（日志归因用，不外发）。
	UpstreamID string
}

// SetPassthrough 注入透传执行面（装配层调用；未注入时端点回 501）。
func (s *Server) SetPassthrough(p PassthroughUpstream) { s.passthrough = p }

func (s *Server) v1Passthrough() PassthroughUpstream { return s.passthrough }

// servePassthrough 是 Files/Search 端点共享的执行骨架。
//
// 三件事：鉴权 → 交给装配层转发 → 原样写回。
// 为什么共享：四个文件端点 + 两个搜索端点的 HTTP 面完全相同，
// 差异只有"上游路径"与"HTTP 方法"两个字符串。
func (s *Server) servePassthrough(w http.ResponseWriter, r *http.Request, upstreamEndpoint string) {
	requestID := requestIDOf(r)
	ensureHeaders(w, requestID)

	if !hasAuthedAccount(r) {
		s.writeV1Error(w, "invalid_api_key", "缺少有效的 API Key", requestID)
		return
	}

	p := s.v1Passthrough()
	if p == nil {
		// 未装配 = 部署态缺件。用 no_candidate（503）而不是新造错误码：
		// §03-errors 的码表是客户端按分支处理的契约，凭空加一个码会让所有
		// 客户端的 switch 落到 default。503 的语义（"现在不行，稍后重试"）
		// 正好描述"部署还没接上这个能力"。
		s.writeV1Error(w, "no_candidate", "该端点未在当前部署中启用", requestID)
		return
	}

	pr := KeyPrincipalFromContext(r.Context())
	in := PassthroughInput{
		RequestID:     requestID,
		Endpoint:      upstreamEndpoint,
		Method:        r.Method,
		RawQuery:      r.URL.RawQuery,
		Header:        r.Header,
		Body:          r.Body,
		ContentLength: r.ContentLength,
	}
	if pr != nil {
		in.AccountID = pr.AccountID
		in.KeyID = pr.KeyID
	}

	res, err := p.Passthrough(r.Context(), in)
	if err != nil {
		// 失败时客户端必须知道"是你能重试还是你的请求错了"：
		// 本轮错归一到与推理端点同一套（classifyErr），保持一致的客户端体验。
		payload := classifyErr(err)
		s.writeV1Error(w, payload.code, payload.message, requestID)
		return
	}
	defer func() { _ = res.Body.Close() }()

	// 原样写回：头逐条复制（逐跳头由装配层/传输层的 CopyHeader 剔除），
	// 状态码照抄，体直接流式搬运——不读进内存，不解压，不重编码。
	for k, vs := range res.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.Status)
	_, _ = io.Copy(w, res.Body)
}
