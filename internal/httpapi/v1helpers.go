package httpapi

// v1helpers.go —— v1 面板的 HTTP 面小工具（requestID 取值、鉴权占位、
// 流式判定、真实模型名判定）。
//
// 为什么独立文件：这些是三个端点共享的「HTTP 壳层」逻辑，与编排骨架
// （v1.go）和方言端点（v1chat/v1messages/v1responses）都不同域。

import (
	"context"
	"encoding/json"
	"net/http"
)

// requestIDOf 从 ctx 取请求 ID（mwRequestID 注入；直接调 handler 的测试
// 没有 ID 时回退空串——错误体的 request_id 字段是 omitempty）。
func requestIDOf(r *http.Request) string {
	if id := RequestIDFromContext(r.Context()); id != "" {
		return id
	}
	return r.Header.Get("X-Request-ID")
}

// accountKey 是 ctx 里的账户标记（测试专用：InjectTestAccount 模拟
// mw_keyauth 已注入的状态）。生产通道是 ctxKeyKeyPrincipal 里的
// KeyPrincipal——hasAuthedAccount 双通道都认（2026-10-21 发现 A 修复后）。
type v1AccountKey struct{}

// InjectTestAccount 测试注入账户 ctx（生产路径由 mw_keyauth.go 注入）。
func InjectTestAccount(ctx context.Context) context.Context {
	return context.WithValue(ctx, v1AccountKey{}, true)
}

// hasAuthedAccount 判 ctx 是否已有已鉴权账户（生产：mw_keyauth.go 注入的
// KeyPrincipal；测试：InjectTestAccount 模拟同一判定语义）。
//
// 2026-10-21 装配修正（code-review 巡检发现 A）：原实现只查占位键
// v1AccountKey{}，而生产链 mw_keyauth 注入的是 ctxKeyKeyPrincipal——两套
// 键不通用导致正式 KeyPrincipal 永不命中、v1 三端点恒 401。现在双通道
// 都认：KeyPrincipalFromContext（生产）优先，占位键（测试兼容）兕底。
func hasAuthedAccount(r *http.Request) bool {
	if KeyPrincipalFromContext(r.Context()) != nil {
		return true
	}
	v, ok := r.Context().Value(v1AccountKey{}).(bool)
	return ok && v
}

// isStreamRequest 判请求是否流式：URL query stream=1 或 body 里
// "stream":true（双通道都认——OpenAI 客户端两种用法都有）。
func isStreamRequest(r *http.Request, body []byte) bool {
	if r.URL.Query().Get("stream") == "1" || r.URL.Query().Get("stream") == "true" {
		return true
	}
	var probe struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err == nil {
		return probe.Stream
	}
	return false
}


// wantsTrace 判定请求是否要路由诊断（x-fr-trace: 1）。
//
// 为什么只在值为 1 时开：trace 头含候选池结构与上游模型 id（管理端可见
// 面），无条件回写等于把它变成对租户的默认暴露面（02-abstract.md §6.2）。
func wantsTrace(r *http.Request) bool {
	return r.Header.Get("X-FR-Trace") == "1"
}
