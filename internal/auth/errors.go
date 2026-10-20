package auth

import (
	"errors"
	"fmt"
	"time"
)

// Code 是错误码。取值与 docs/api/03-errors.md §3.1/§3.2 对齐：
// HTTP 中间件靠它决定状态码与 type，**不靠错误文案**。
//
// 为什么在本包内自带一套 Code 而不复用 internal/httpapi 的映射：
// auth 是纯领域层，不依赖协议层；协议层的映射表已经存在（errorTypes），
// 它按字符串匹配，因此两处只需要约定同一批字符串。
type Code string

const (
	// 凭据层面不可用（HTTP 401）。
	CodeInvalidCredentials Code = "invalid_credentials"
	CodeSessionExpired     Code = "session_expired"
	CodeInvalidAPIKey      Code = "invalid_api_key"

	// 身份有效但权限不足（HTTP 403）。
	CodeForbidden Code = "forbidden"

	// 资源层面（HTTP 404 / 409）。
	CodeNotFound Code = "not_found"
	CodeConflict Code = "conflict"

	// 限速与额度（HTTP 429）。这四者必须可区分，因为客户端动作完全不同：
	//   rate_limited      → 按 Retry-After 退避重试是对的
	//   quota_exceeded    → 等额度重置或请管理员调高，重试无意义
	//   pool_exhausted    → 组级共享池耗尽，换 Key / 改参数全都无效
	//   too_many_attempts → 登录/提交类防爆破退避生效，换用户名无效
	//
	// 契约（docs/api/03-errors.md §5.2，评审点名）：这三个码构造时**必须**带
	// Retry-After（用 WithRetryAfter）：rate_limited / too_many_attempts 必有；
	// quota_exceeded / pool_exhausted 在能算出重置时刻时给，全局熔断算不出
	// 就留空（编造数字是误导）。映射层据此校验：Code 是这三个而 RetryAfterOf==0
	// 且非全局熔断路径，就是构造点漏了 WithRetryAfter。
	CodeRateLimited     Code = "rate_limited"
	CodeQuotaExceeded   Code = "quota_exceeded"
	CodePoolExhausted   Code = "pool_exhausted"
	CodeTooManyAttempts Code = "too_many_attempts"

	// 入参不合法（HTTP 400）。
	CodeInvalidRequest Code = "invalid_request"

	// 服务端内部故障（HTTP 500）。依赖缺失、不变式被存储层破坏等
	// 「调用方什么做错都没有」的情况走这里，与入参错误（400）严格区分。
	CodeInternal Code = "internal_error"
)

// Error 是本包唯一的错误类型。
//
// 为什么不直接用 fmt.Errorf 字符串：上层需要按**类别**分支
// （401 / 403 / 404 / 429 的映射、是否带 Retry-After、是否记审计），
// 靠字符串包含判断会在文案改动时静默失效。
type Error struct {
	Code Code
	Msg  string

	// RetryAfter 是可选的"多久之后可重试"。
	// 只有 rate_limited / too_many_attempts 必须有值；quota_exceeded 在
	// 能算出额度重置时刻时给，全局熔断时留空（解除依赖人工，编造数字是误导）。
	RetryAfter time.Duration

	// Dimension 记录是哪个额度维度打满（request / token / credit）。
	// 为什么放在错误里而不新增错误码：三个维度下客户端动作完全相同（等重置），
	// 按 api/03-errors.md 的边界原则"动作不同才拆码"。
	Dimension string

	// GroupID 只在 pool_exhausted 上有值，且**只进日志，不进响应体**：
	// 暴露它会泄露调用方在哪些组里。
	GroupID string

	// Quota 携带命中行的机器可读四元组（unit / period / model），供错误体
	// `quota` 对象组装（docs/api/03-errors.md §6 双通道定案 2026-10-20：
	// message 给人读、quota 给客户端自动降级用——合计行 Model 为空，
	// 按模型限额行 Model 指名可换的模型）。仅 quota_exceeded 使用；
	// 指针是为了区分"没带"与"带了空值"，映射层据此省略 JSON 字段。
	Quota *QuotaDetail

	// Wrapped 保留底层原因（store 的 not found、argon2 解码失败等）。
	Wrapped error
}

// Error 实现 error。
func (e *Error) Error() string {
	switch {
	case e == nil:
		return "<nil>"
	case e.Wrapped != nil && e.Msg != "":
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Msg, e.Wrapped)
	case e.Wrapped != nil:
		return fmt.Sprintf("%s: %v", e.Code, e.Wrapped)
	default:
		return fmt.Sprintf("%s: %s", e.Code, e.Msg)
	}
}

// Unwrap 支持 errors.Is / errors.As 穿透到底层原因。
func (e *Error) Unwrap() error { return e.Wrapped }

// Is 让 errors.Is(err, ErrForbidden) 按 **Code** 匹配。
//
// 为什么按 Code 而不是按指针：同一个 Code 会在很多调用点被构造
// （带不同的 msg 与底层原因），指针相等永远为假，而按 Code 匹配
// 才是调用方真正想要的语义。
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code
}

// 哨兵错误：调用方用 errors.Is(err, auth.ErrForbidden) 判断类别。
var (
	// ErrInvalidCredentials 是**登录**失败的唯一出口。
	// 防枚举要求：用户名不存在、密码错、账户未激活都返回它，
	// 且文案与耗时一致（哑哈希见 password.go）。
	ErrInvalidCredentials = &Error{Code: CodeInvalidCredentials, Msg: "用户名或密码不正确"}

	// ErrSessionExpired 与 ErrInvalidCredentials 分开：前者是"登录过但过期了"，
	// 客户端应跳登录页而不是提示密码错。
	ErrSessionExpired = &Error{Code: CodeSessionExpired, Msg: "会话已过期，请重新登录"}

	// ErrInvalidAPIKey 覆盖"没有 Key / 格式非法 / 不存在 / 已吊销 / 已过期"。
	// 为什么不细分：Key 是机器凭据，区分这几种只会给攻击者一个探测面。
	ErrInvalidAPIKey = &Error{Code: CodeInvalidAPIKey, Msg: "API Key 无效"}

	// ErrForbidden 是"有身份但不该做这件事"（HTTP 403）。
	ErrForbidden = &Error{Code: CodeForbidden, Msg: "权限不足"}

	ErrNotFound = &Error{Code: CodeNotFound, Msg: "资源不存在"}

	ErrConflict = &Error{Code: CodeConflict, Msg: "资源冲突"}

	// ErrTooManyAttempts 是防爆破退避/锁定的统一出口（429 + Retry-After）。
	// 为什么单独一个哨兵而不是复用 ErrForbidden：客户端动作不同——
	// 前者等 Retry-After 后重试是正解，后者重试永远无意义。
	ErrTooManyAttempts = &Error{Code: CodeTooManyAttempts, Msg: "尝试次数过多，请稍后再试"}
)

// Errorf 构造带底层原因的错误；底层原因会进日志但不应进响应体。
func Errorf(code Code, wrapped error, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...), Wrapped: wrapped}
}

// WithRetryAfter 返回一个带退避时长的副本（不修改原错误，哨兵是共享实例）。
func (e *Error) WithRetryAfter(d time.Duration) *Error {
	clone := *e
	clone.RetryAfter = d
	return &clone
}

// WithDetail 返回一个附带额度维度 / 组归因的副本。
func (e *Error) WithDetail(dimension, groupID string) *Error {
	clone := *e
	clone.Dimension = dimension
	clone.GroupID = groupID
	return &clone
}

// QuotaDetail 是错误体机器可读 `quota` 对象的载荷（docs/api/03-errors.md §6 定案）。
// Model 为空 = 命中的是合计行（abstract_model_id=0）；非空 = 按模型限额行，
// 客户端据此知道“换一个模型还能继续”。
type QuotaDetail struct {
	Unit   string
	Period string
	Model  string
}

// WithQuota 返回携带额度四元组的副本（副本语义同 WithRetryAfter：
// 哨兵是共享实例，原地写会污染所有后续命中）。
func (e *Error) WithQuota(unit, period, model string) *Error {
	clone := *e
	clone.Quota = &QuotaDetail{Unit: unit, Period: period, Model: model}
	return &clone
}

// CodeOf 取错误的 Code；非本包错误统一回落为 invalid_request。
//
// 为什么回落而不是 panic：错误处理路径上再崩一次是最糟的失败模式。
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInvalidRequest
}

// RetryAfterOf 取错误的建议退避时长；没有则返回 0。
func RetryAfterOf(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.RetryAfter
	}
	return 0
}
