package upstream

// passthrough.go —— 原样转发（Files / Search & Fetch 等非推理端点）。
//
// 设计口径（2026-11-02 用户明确）：**网关不解析、不改写、不转换**——
// 客户端请求体原样送到上游，上游响应原样回给客户端，只有鉴权头由网关
// 用自己持有的凭据替换。理由：这些端点的语义（multipart 边界、图片二进制、
// 搜索排序分、fetch 内容）都是上游自己的事；网关一旦尝试"理解"它们，
// 就要为每个上游的方言差异、二进制体、分块编码各自维护一套代码，
// 而收益为零——我们不做文件转换，也不做搜索重排。

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// PassthroughRequest 是一次原样转发的入参。
//
// Body 用 io.Reader 而不是 []byte：文件上传最大 64 MiB（官方），
// 整读进内存等于把一条上传变成一份常驻堆占用。透传不做重试，
// 所以不需要可重放的 body。
type PassthroughRequest struct {
	Method string
	// Path 是上游相对路径（如 "/files"、"/files/file-api-…"、"/v1/tools/search"）。
	Path string
	// RawQuery 是原始查询串（已编码，原样拼到 URL 后）。
	RawQuery string
	// Header 是客户端原始头。转发时会剔除与网关相关的那些（见 hopHeaders）。
	Header http.Header
	// Body 是请求体（nil = 无体）。
	Body          io.Reader
	ContentLength int64
}

// PassthroughResult 是上游的原始响应。
//
// Status/Header/Body 三者都原样来自上游：网关只做搬运。
type PassthroughResult struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

// hopHeaders 是逐跳头（RFC 7230 §6.1）：它们只在单跳连接内有效，
// 转发时必须剔除，否则会污染下一跳的连接语义。
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// Passthrough 把请求原样转发到上游，并原样返回上游响应。
//
// 与 Do 的差异（三条，全部是"不处理"的落地）：
//   - 不调 ir.Adapter：没有编解码，没有 IR，客户端说什么就发什么；
//   - 不改路径：Path 由调用方按上游口径给出（网关只拼 BaseURL）；
//   - 不改响应：状态码、头、体原样返回，由调用方直接写回客户端。
//
// 唯一被改写的头是 Authorization：客户端持的是我们的 fr- Key，
// 上游要的是它自己的凭据——这一步必须做，否则上游 401。
func (t *HTTPTransport) Passthrough(ctx context.Context, tgt Target, in PassthroughRequest) (*PassthroughResult, error) {
	if err := ValidateTarget(tgt); err != nil {
		return nil, &Error{Class: ErrBadRequest, Message: err.Error()}
	}
	if tgt.Kind == KindRelay {
		// relay 的线上格式由 sidecar 进程内的 provider adapter 决定，
		// 且 Target.BaseURL 对它恒为空——没有可直接转发的地址。
		// 报错而不是猜一个地址：猜会让请求打到错的机器上。
		return nil, &Error{Class: ErrBadRequest,
			Message: "relay 类上游不支持原样转发（它没有可直连的地址，推理与文件都经 sidecar adapter）"}
	}
	if t.pool == nil {
		return nil, &Error{Class: ErrInternalBug, Message: "连接池未初始化"}
	}
	tr, err := t.pool.Transport(tgt.Kind, tgt.BaseURL)
	if err != nil {
		return nil, &Error{Class: ErrBadRequest, Message: err.Error()}
	}
	release, ok := t.pool.Acquire(ctx, tgt.UpstreamID)
	if !ok {
		return nil, &Error{Class: ErrTimeout, Message: "等待上游并发名额超时（单上游连接上限）"}
	}
	defer release()

	endpoint := strings.TrimRight(tgt.BaseURL, "/") + in.Path
	if in.RawQuery != "" {
		endpoint += "?" + in.RawQuery
	}
	if _, err := url.Parse(endpoint); err != nil {
		return nil, &Error{Class: ErrBadRequest, Message: "拼出的上游地址非法: " + err.Error()}
	}

	method := in.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, in.Body)
	if err != nil {
		return nil, &Error{Class: ErrBadRequest, Message: "构造出站请求失败: " + err.Error()}
	}
	if in.ContentLength > 0 {
		req.ContentLength = in.ContentLength
	}

	// 逐跳头不转发；Authorization 由下面统一设置（见函数注释）。
	for k, vs := range in.Header {
		if isHopHeader(k) || strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	// 鉴权：用网关自己持有的上游凭据覆盖客户端的 fr- Key。
	// 无凭据的 selfhosted（本机端点）合法：此时不发 Authorization，
	// 而不是发一个空 "Bearer "（多数端点会 401）。
	if t.secrets != nil && tgt.SecretRef != "" {
		token, err := t.secrets(ctx, tgt.SecretRef)
		if err != nil {
			return nil, &Error{Class: ErrAuth, Message: "解密凭据失败: " + err.Error()}
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &Error{Class: Classify(err), Message: err.Error(), Cause: err}
	}
	// 非 200 也原样返回：状态码与错误体都是上游的语义（如 404 file_not_found、
	// 400 参数错误）。由调用方决定怎么呈现——网关不重新分类，
	// 因为那等于替上游翻译错误，而这些端点的错误码是上游 API 的一部分。
	return &PassthroughResult{Status: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

// PassthroughBytes 是 Passthrough 的整读版本：调用方已经拿到完整字节
// （例如它需要先检查/记录正文），此时给的是 bytes.Reader。
//
// 为什么保留两个入口：io.Reader 版本让超大上传可以流式转发（不占堆），
// 而有些调用点天然已经是整读的。两个入口共用同一实现，不各自复制一份。
func (t *HTTPTransport) PassthroughBytes(ctx context.Context, tgt Target, in PassthroughRequest, body []byte) (*PassthroughResult, error) {
	if len(body) > 0 {
		in.Body = bytes.NewReader(body)
		in.ContentLength = int64(len(body))
	}
	return t.Passthrough(ctx, tgt, in)
}

// isHopHeader 判定逐跳头（大小写不敏感）。
func isHopHeader(k string) bool {
	for _, h := range hopHeaders {
		if strings.EqualFold(k, h) {
			return true
		}
	}
	return false
}

// CopyHeader 把上游响应头原样写给客户端（同样剔除逐跳头）。
//
// 为什么导出：调用方是 httpapi（它不 import 本包的内部约定），
// 而"哪些头不能转发"是 HTTP 语义而不是业务判断，只有一处定义才不会漂移。
func CopyHeader(dst, src http.Header) {
	for k, vs := range src {
		if isHopHeader(k) {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
