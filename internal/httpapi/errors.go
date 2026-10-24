package httpapi

import (
	"encoding/json"
	"net/http"
)

// 本文件实现统一错误体。
//
// 唯一事实源：docs/api/03-errors.md。
// 契约要点：
//   - **`code` 是契约，`message` 不是**。改文案不算不兼容，改/删 `code` 算。
//   - 三个错误面（入站 / 控制台 / 页面）共用同一套 `code`。
//   - 默认形状是 OpenAI 兼容形状；`/v1/messages` 换 Anthropic 形状（在 M5 实现，
//     因为它只在协议解析成功之后才知道该用哪种形状）。
type errorType string

const (
	typeInvalidRequest errorType = "invalid_request_error"
	typeAuthentication errorType = "authentication_error"
	typePermission     errorType = "permission_error"
	typeNotFound       errorType = "not_found_error"
	typeConflict       errorType = "conflict_error"
	typeRateLimit      errorType = "rate_limit_error"
	typeServiceUnavail errorType = "service_unavailable_error"
	typeInternal       errorType = "internal_error"
)

// errorTypes 是 docs/api/03-errors.md §3.1 的 code → type 映射。
//
// 为什么用表而不是在每个调用点手写 type：同一个 code 在不同调用点写成不同 type
// 是最容易发生、也最难发现的契约漂移；集中一处后，新增 code 忘了登记会立刻在测试里暴露。
var errorTypes = map[string]errorType{
	// 入站面（§3.1）
	"invalid_api_key":        typeAuthentication,
	"invalid_request":        typeInvalidRequest,
	"capability_unsupported": typeInvalidRequest,
	"request_too_large":      typeInvalidRequest,
	"model_not_found":        typeNotFound,
	"model_retired":          typeNotFound,
	"quota_exceeded":         typeRateLimit,
	"pool_exhausted":         typeRateLimit,
	"rate_limited":           typeRateLimit,
	"upstream_unavailable":   typeServiceUnavail,
	"upstream_timeout":       typeServiceUnavail,
	"upstream_error":         typeServiceUnavail,
	"no_candidate":           typeServiceUnavail,
	"internal_error":         typeInternal,

	// 控制台面与账户自助（§3.2）
	"invalid_credentials": typeAuthentication,
	"session_expired":     typeAuthentication,
	"forbidden":           typePermission,
	"not_found":           typeNotFound,
	"conflict":            typeConflict,
	"too_many_attempts":   typeRateLimit,
	// password_change_required：首启随机口令/管理员重置后，改密前触达
	// 其它控制台接口（docs/api/02-admin §2 的强制改密窗口）。
	"password_change_required": typePermission,

	// HTTP 层：405 与 415 复用 invalid_request（§4「为什么复用」），
	// 把 param 填成方法名 / Content-Type。
	"not_ready":     typeServiceUnavail,
	"shutting_down": typeServiceUnavail,
	// overloaded：进程级并发闸门排队超时（2026-10-21 定案，§3.1）。
	// 注：M5 装配时本表与 ir.CodeToErrorType 归并为唯一映射源（裁决已定），
	// 在那之前两处同码不得漂：新增码必须同时加进 ir 表并跑 TestContractErrorTypeMapping。
	"overloaded": typeServiceUnavail,
}

// errorBody 是 §2.1 的 OpenAI 兼容错误体。
type errorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    string `json:"code"`
}

// errorEnvelope 包一层 `error`。
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// TypeForCode 返回某个 code 的粗粒度 type；未登记的 code 记 internal_error。
//
// 未登记**不 panic**：一个错误码拼写错误不应该让服务在错误处理路径上再崩一次，
// 而 500 + internal_error 是「我们出了未预期的问题」的正确表达。
func TypeForCode(code string) errorType {
	if t, ok := errorTypes[code]; ok {
		return t
	}
	return typeInternal
}

// writeError 输出统一错误体。
//
// details 是**非契约**的排障附加字段（如 body 上限的具体字节数），
// 客户端不得依赖它做分支——它随时可能被移除。
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	payload := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    string(TypeForCode(code)),
			"param":   nil,
			"code":    code,
		},
	}
	if len(details) > 0 {
		payload["error"].(map[string]any)["details"] = details
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// writeErrorParam 用于 §4 中要求把出错字段填进 `param` 的码（如 405 填方法名、
// 400 填字段名）。
func writeErrorParam(w http.ResponseWriter, status int, code, param, message string) {
	writeJSON(w, status, errorEnvelope{
		Error: errorBody{Message: message, Type: string(TypeForCode(code)), Param: param, Code: code},
	})
}
