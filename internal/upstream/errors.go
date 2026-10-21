// Package upstream 定义 L3 上游适配层（事实源 docs/architecture/03-upstream.md）。
//
// 为什么 M1 只放类型、决策与守卫，不放具体传输实现：三类上游（relay/vendor/selfhosted）
// 共用同一份「能不能重试、有没有吐过可见内容、该不该计健康失败」的判定；具体传输
// （vendor/selfhosted 的 HTTPS、relay 的 sidecar IPC）属于 M2。把判定收敛在这里，
// M2 的两个实现者就不会各写一套并漂移（03-upstream.md 2.3 的核心主张）。
package upstream

import (
	"errors"
	"net"
	"strings"
)

// ErrClass 是上游错误的分类（03-upstream.md §2.2）。
//
// 为什么分类而不是只看 HTTP 状态码：http=000 根本没有状态码；429 与「额度耗尽」
// 可能都是 429，但前者等一会儿能好、后者要换凭据。分类值直接映射到第 6 节的
// 重试表（retry.go）与第 7 节的健康状态（pool.go），也落 requests.error_code。
type ErrClass string

const (
	ErrConnReset   ErrClass = "conn_reset"     // 连接被重置 / 半开连接（含 GOAWAY）
	ErrConnect     ErrClass = "connect"        // 连不上（拒绝 / 连接超时）
	ErrTLS         ErrClass = "tls"            // 证书 / 握手问题
	ErrTimeout     ErrClass = "timeout"        // 阶段超时（TTFT 等）
	ErrUpstream5xx ErrClass = "upstream_5xx"   // 上游 5xx
	ErrRateLimited ErrClass = "rate_limited"   // 429
	ErrAuth        ErrClass = "upstream_auth"  // 401/403，凭据坏了
	ErrBadRequest  ErrClass = "bad_request"    // 4xx 其它，请求本身的问题
	ErrQuota       ErrClass = "upstream_quota" // 额度耗尽类错误
	ErrProtocol    ErrClass = "protocol"       // 上游返回了不可解析的流
)

// Error 是上游侧的结构化错误。
//
// 为什么带 Class 与 Status 两个字段：重试决策表（§6.2）按 Class 查；落库与日志
// 要 Status（http=000 类失败记 0——PLAN 13 风险表要求观察这个频率）。
// 两者不互相替代：ErrRateLimited 的 Status 是 429，而 ErrConnReset 的 Status 恒为 0。
type Error struct {
	Class   ErrClass
	Status  int // 上游 HTTP 状态码；连接类失败（没有状态码）为 0，即 "http=000"
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Status != 0 {
		return "upstream " + string(e.Class) + "(http=" + itoa(e.Status) + "): " + e.Message
	}
	// http=000：连接类失败，状态码缺席本身就是重要信息，日志里显式写 000。
	return "upstream " + string(e.Class) + "(http=000): " + e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// itoa 避免仅为一个整数引入 strconv 的视觉噪声（该包用法稳定，直接格式化）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Classify 把一次传输层失败归入 §2.2 的错误类。
//
// 为什么按错误文案而不是 errors.Is 判：EOF / connection reset / GOAWAY 在标准库里
// 大多是裸错误或 io.EOF，没有稳定的哨兵可 Is；文案匹配是这里唯一可靠的判据。
// 顺序有讲究：net.Error.Timeout 先判（超时的文案可能也含 reset 字样），refused
// 先于 EOF（refused 的消息不含 EOF，但明确先分流）。
func Classify(err error) ErrClass {
	if err == nil {
		return ""
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return ErrConnect
	case strings.Contains(msg, "x509"), strings.Contains(msg, "tls:"):
		return ErrTLS
	case strings.Contains(msg, "EOF"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "GOAWAY"),
		strings.Contains(msg, "broken pipe"):
		return ErrConnReset
	}
	// 归不进已知类的传输失败按协议错处理：它同样不可同候选重试（§6.2 protocol 行），
	// 直接换候选是保守且正确的处置。
	return ErrProtocol
}

// ClassifyStatus 把上游 HTTP 状态码归入错误类；0（无状态码）与 2xx 返回空串，
// 调用方应改用 Classify(err) 的结果。
func ClassifyStatus(status int) ErrClass {
	switch {
	case status == 429:
		return ErrRateLimited
	case status == 401 || status == 403:
		return ErrAuth
	case status >= 500:
		return ErrUpstream5xx
	case status >= 400:
		return ErrBadRequest
	}
	return ""
}
