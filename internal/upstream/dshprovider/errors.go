package dshprovider

import (
	"errors"
	"fmt"
	"sync"
)

// 错误码。取值与语义**逐条**对齐 docs/architecture/04-sidecar.md 2.4 那张表：
// 这张表是跨进程契约的一部分，改名即破坏兼容（sidecar 可独立升级）。
const (
	CodeBadRequest       = "E_BAD_REQUEST"
	CodeUnknownMethod    = "E_UNKNOWN_METHOD"
	CodeBusy             = "E_BUSY"
	CodeNotLoggedIn      = "E_NOT_LOGGED_IN"
	CodeLoginInProgress  = "E_LOGIN_IN_PROGRESS"
	CodeLoginFailed      = "E_LOGIN_FAILED"
	CodeLoginTimeout     = "E_LOGIN_TIMEOUT"
	CodeSubmitRejected   = "E_SUBMIT_REJECTED"
	CodeNoBrowser        = "E_NO_BROWSER"
	CodeCredentialMissng = "E_CREDENTIAL_MISSING"
	CodeCredentialExpred = "E_CREDENTIAL_EXPIRED"
	CodeUpstreamHTTP     = "E_UPSTREAM_HTTP"
	CodeTimeout          = "E_UPSTREAM_TIMEOUT"
	CodeRateLimited      = "E_RATE_LIMITED"
	CodeDependencyMissng = "E_DEPENDENCY_MISSING"
	CodeNotImplemented   = "E_NOT_IMPLEMENTED"
	// CodeCredentialReadonly / CodeStateForbidden 是**守卫**，不是「不会发生」。
	// 它们出现在日志里就意味着代码里多了一个违规调用点——那正是我们想最早知道的事。
	CodeCredentialReadonly = "E_CREDENTIAL_READONLY"
	CodeStateForbidden     = "E_STATE_FORBIDDEN"
	CodeProtocol           = "E_PROTOCOL"
	CodeInternal           = "E_INTERNAL"
	// CodeSidecarDown 是**本客户端新增**的码（文档的 2.4 表里没有）。
	// 为什么必须新增而不是复用 E_PROTOCOL：调用方要能一眼区分
	// 「sidecar 根本没起来，去修环境」与「说好了的协议对不上，去修代码」。
	// 复用会把二者混成同一个告警，运维与开发就会互相等对方修。
	CodeSidecarDown = "E_SIDECAR_DOWN"
)

// allCodes 是「客户端认识的全部错误码」。
// 存在的理由只有一个：knownCode 用它做白名单，
// 从而让 E_CREDENTIAL_READONLY / E_STATE_FORBIDDEN 也能被正常识别与告警（而不是被
// 当成未知码降级成 E_INTERNAL，那样红线告警就静默了）。
var allCodes = []string{
	CodeBadRequest, CodeUnknownMethod, CodeBusy, CodeNotLoggedIn,
	CodeLoginInProgress, CodeLoginFailed, CodeLoginTimeout, CodeSubmitRejected,
	CodeNoBrowser, CodeCredentialMissng, CodeCredentialExpred, CodeUpstreamHTTP,
	CodeTimeout, CodeRateLimited, CodeDependencyMissng, CodeNotImplemented,
	CodeCredentialReadonly, CodeStateForbidden, CodeProtocol, CodeInternal,
	CodeSidecarDown,
}

var codeSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(allCodes))
	for _, c := range allCodes {
		m[c] = struct{}{}
	}
	return m
}()

func knownCode(code string) bool {
	_, ok := codeSet[code]
	return ok
}

// Error 是本包唯一的错误类型。
//
// 为什么不用 errors.Is 的一串哨兵错误：跨进程调用的失败原因必须能被**原样**带到
// 上层的错误响应里（客户端要看到 code），而哨兵错误在打包/解包过程中只剩一个字符串。
// 带 Code 字符串的结构体可以直接进 JSON。
type Error struct {
	Code      string
	Method    string
	Message   string
	Retryable bool
	// Cause 保留底层 err（io.EOF、exec 错误等），供 errors.Is 判断。
	Cause error
}

func (e *Error) Error() string {
	if e.Method == "" {
		return e.Code + ": " + e.Message
	}
	return fmt.Sprintf("%s(%s): %s", e.Code, e.Method, e.Message)
}

// Unwrap 让 errors.Is(err, io.EOF) 这类判断仍然可用。
func (e *Error) Unwrap() error { return e.Cause }

// IsRetryable 说明这个失败重试是否有意义（E_BUSY / 超时 / 进程已死）。
func (e *Error) IsRetryable() bool { return e.Retryable }

// errDown 构造「sidecar 不可用」错误，统一走 CodeSidecarDown。
func errDown(method, reason string) error {
	if reason == "" {
		reason = "sidecar 不可用（贡献功能已降级，转发不受影响）"
	}
	return &Error{Code: CodeSidecarDown, Method: method, Retryable: true, Message: reason}
}

// AsError 把任意 error 归一成本包的 *Error，供上层统一取 Code 写进响应。
// 非本包错误一律归为 E_INTERNAL —— 与 04-sidecar.md §3「未知错误码按 E_INTERNAL 处理」同源。
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	return &Error{Code: CodeInternal, Message: err.Error(), Cause: err}
}

// CodeOf 只取错误码，取不到时给 E_INTERNAL。
// 这是 /healthz 与错误响应最常用的那一行，故单独给个短函数。
func CodeOf(err error) string {
	if de := AsError(err); de != nil {
		return de.Code
	}
	return ""
}

// stderrRing 是 sidecar stderr 的定长环形缓冲。
//
// 为什么要缓冲而不是只打日志：sidecar 起不来时，唯一能说明原因的就是它
// 自己在退出前打到 stderr 的那几行（例如「Cannot find module」）。
// 只进 slog 的话，运维得在海量日志里翻；缓冲一份就能在 /healthz 明细里直接给出。
type stderrRing struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
	cap   int
}

func newStderrRing(capacity int) *stderrRing {
	if capacity <= 0 {
		capacity = defaultStderrLines
	}
	return &stderrRing{lines: make([]string, capacity), cap: capacity}
}

func (r *stderrRing) push(line string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines[r.next] = line
	r.next = (r.next + 1) % r.cap
	if r.next == 0 {
		r.full = true
	}
}

// tail 返回最近 n 行，按发生顺序（旧→新）。
func (r *stderrRing) tail(n int) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	size := r.next
	if r.full {
		size = r.cap
	}
	if n <= 0 || n > size {
		n = size
	}
	out := make([]string, 0, n)
	start := (r.next - n + r.cap) % r.cap
	for i := 0; i < n; i++ {
		out = append(out, r.lines[(start+i)%r.cap])
	}
	return out
}
