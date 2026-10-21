package httpapi

// v1sse.go —— SSE 透传细节（task-8 范围⑤）。
//
// 编排骨架（v1.go streamOnce）已经实现「不在中间缓冲」：上游事件经
// StreamReader→StreamWriter 逐帧转写，每帧 Flush。本文件收敛 SSE 相关的
// HTTP 面细节：响应头、flush 语义、TTFT 记录的日志字段约定。
//
// 为什么独立文件：SSE 的 HTTP 语义（头、flush、连接保活）与编排骨架
// （候选切换、C7 判定）是两个关注点——分开后 streamOnce 里只读得到
// 「协议语义」，HTTP 细节在这里一眼看全。

import (
	"net/http"
)

// SSE 响应头常量（01-inbound §4.1/§6.3 共同要求）。
const (
	sseContentType  = "text/event-stream"
	sseCacheControl = "no-cache"
	// Connection: keep-alive 由 net/http 自动处理（HTTP/1.1 持久连接
	// 默认开）；显式设置在 HTTP/2 下反而非法——只在需要的地方设置。
)

// setSSEHeaders 给流式响应立头。
// 为什么流式头必须在「第一帧写出前」立好：WriteHeader 之后 headers
// 就锁死了——先写正文再补头无效；编排骨架在 proxyV1 之前调用本函数。
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", sseContentType)
	w.Header().Set("Cache-Control", sseCacheControl)
}

// flusher 断言 ResponseWriter 支持 Flush（ir.SSEWriter 构造时会做同样
// 断言——这里的断言是为 httptest.ResponseRecorder 兜底：它实现 Flush）。
// 返回 false 的 writer（不支持 flush 的中间层）会让 SSE 变成「攒完再发」
// ——TTFT 直接不可测，调用方需要能感知。
func flusher(w http.ResponseWriter) http.Flusher {
	f, _ := w.(http.Flusher)
	return f
}

// TTFT 日志字段约定（01-inbound §7.2：首字节时间戳进请求日志）：
// streamOnce 里已记 "v1 stream ttft" {request_id, ttft_ms}。
// 这里只补充字段名常量，保证日志查询入口统一（03-errors §2.2 同理：
// 两边共用同一套排障语言）。
const (
	ttftLogField   = "ttft_ms"
	requestIDField = "request_id"
)
