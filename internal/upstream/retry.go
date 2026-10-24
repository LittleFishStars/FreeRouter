package upstream

import (
	"math"
	"math/rand"
	"time"
)

// 本文件是 03-upstream.md §6「偶发 http=000 的重试策略」的决策表落地。
//
// 为什么决策表独立成文件而不是写在传输实现里：重试是**跨候选**的决策（先同候选
// 重试还是换候选、退避多少、总预算剩多少），属于 call 模块。若让每个上游实现自己
// 重试，就会与「未吐可见内容前可切换候选」的判定打架：上游重试成功了我们不知道，
// 上游重试失败了 call 又重试一次，同一个候选被打了 6 次（§2.1 表第 4 行）。

// RetryPolicy 是一个错误类别的重试规则（§6.2 决策表的一行）。
type RetryPolicy struct {
	// MaxSameCandidate 是同候选重试上限（0 = 直接换候选）。
	MaxSameCandidate int
	// Backoffs 是每次重试前的退避序列（长度应等于 MaxSameCandidate；越界取最后一个）。
	Backoffs []time.Duration
	// CountsAsFailure：该类失败是否计入上游健康计数。
	// 429/auth/quota 不计——根因在凭据，健康计数只在「上游本身连续挂」时说话。
	CountsAsFailure bool
}

// retryTable 是 §6.2 的逐行落地。映射表形态：规则随文档增删只改此表。
var retryTable = map[ErrClass]RetryPolicy{
	// 半开连接：同候选用**新建连接**重试的剥离率很高（§6.3 规则 3）。
	// 不计健康失败：它是复用长连接的固有代价，不代表上游坏了。
	ErrConnReset: {MaxSameCandidate: 2, Backoffs: []time.Duration{50 * time.Millisecond, 150 * time.Millisecond}, CountsAsFailure: false},
	// 连不上：可能是端点重启。计入健康失败（真连不上说明上游或网络有病）。
	ErrConnect: {MaxSameCandidate: 2, Backoffs: []time.Duration{200 * time.Millisecond, 500 * time.Millisecond}, CountsAsFailure: true},
	ErrTLS:     {MaxSameCandidate: 1, Backoffs: []time.Duration{200 * time.Millisecond}, CountsAsFailure: false},
	// TTFT 前超时：换候选更优（该上游生成不动）。退避为空——等待没有意义。
	ErrTimeout:     {MaxSameCandidate: 1, Backoffs: nil, CountsAsFailure: true},
	ErrUpstream5xx: {MaxSameCandidate: 2, Backoffs: []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}, CountsAsFailure: true},
	// 429：不重试直接换候选（等一会儿不解决任何问题），凭据冷却由调用方读 Retry-After。
	ErrRateLimited: {MaxSameCandidate: 0, CountsAsFailure: false},
	// 额度耗尽：重试只会烧同一个空账户。
	ErrQuota: {MaxSameCandidate: 0, CountsAsFailure: false},
	// 凭据坏了：唯一出路是换凭据/候选 + 标 invalid 通知贡献者。
	ErrAuth: {MaxSameCandidate: 0, CountsAsFailure: false},
	// 请求本身的问题：直接返回给客户端，重试无意义（§6.2 决策表原文）。
	ErrBadRequest: {MaxSameCandidate: 0, CountsAsFailure: false},
	// 不可解析的流：上游协议错，换候选。
	ErrProtocol: {MaxSameCandidate: 0, CountsAsFailure: false},
}

// RetryBudget 是一次用户请求的重试总预算（§6.3 规则 1）。
//
// 为什么需要两条上限：我们的上游请求是「生成」语义而非幂等读，每次重试都在真实
// 消耗额度；一次用户请求打 6 次上游，在额度成本上不可接受。候选内上限防「同候选
// 死磕」，请求内上限防「广撒网烧光所有候选」。
type RetryBudget struct {
	// PerCandidate 是单个候选的同候选重试上限（≤2，§6.3）。
	PerCandidate int
	// PerRequest 是整次请求的总重试上限（≤4，§6.3）。
	PerRequest int
	// PerClassClamp：按错误类的更严上限（retryTable 的 MaxSameCandidate 会在
	// 运行时与 PerCandidate 取 min）。为什么不在表里直接写死 2：预算是 call 模块
	// 的调度参数，应可配；表里是「该类错误的语义上限」，两者取严者。
	PerClassClamp bool
}

// DefaultRetryBudget 返回 §6.3 规则 1 的硬上限：≤2 次/候选、≤4 次/请求。
func DefaultRetryBudget() RetryBudget {
	return RetryBudget{PerCandidate: 2, PerRequest: 4, PerClassClamp: true}
}

// CanRetry 判定一次失败后能否同候选重试。
//
// 三条不可放松的规则（§6.3）在这里逐条执行：
//  1. 重试总预算 ≤2 次/候选、≤4 次/请求——budget 参数；
//  2. 只有 VisibleWritten == false 才允许重试或换候选（C7 流式串话）——visibleWritten 参数；
//     判定完全依赖调用方传入的 Stats().VisibleWritten，不接受「我觉得还没吐出去」的推断；
//  3. 先同候选重试一次连接类错误，再换候选——由 retryTable 的 MaxSameCandidate>0 表达。
//
// 返回的 bool 是「同候选重试」；「换候选」永远允许（只要预算没耗尽且未吐内容），
// 由 call 模块在 CanRetry 为 false 时自行决定。
func (b RetryBudget) CanRetry(class ErrClass, sameCandidateRetries, totalRetries int, visibleWritten bool) bool {
	// §6.3 规则 2：已吐可见内容后的任何错误，重试次数为 0（终止并发 error 帧，C7）。
	if visibleWritten {
		return false
	}
	// §6.3 规则 1：请求内总预算。
	if totalRetries >= b.PerRequest {
		return false
	}
	pol, ok := retryTable[class]
	if !ok {
		return false // 未知类不重试：保守处理，换候选由调用方决定
	}
	limit := pol.MaxSameCandidate
	if b.PerClassClamp && b.PerCandidate < limit {
		limit = b.PerCandidate
	}
	return sameCandidateRetries < limit
}

// Backoff 返回下一次同候选重试前的等待时长（含 jitter）。
//
// 为什么必须带 jitter：多个并发请求同时遇到半开连接，无 jitter 的退避会让它们
// 在同一毫秒一起重试，把上游的连接风暴从「一次」放大成「节律性的 N 次」。
// jitter 范围 +0~50%（§6.2 conn_reset 行的既有约定，推广到全部类）。
func (b RetryBudget) Backoff(class ErrClass, nextRetry int) time.Duration {
	pol, ok := retryTable[class]
	if !ok || len(pol.Backoffs) == 0 {
		return 0
	}
	idx := nextRetry
	if idx < 0 {
		idx = 0
	}
	if idx >= len(pol.Backoffs) {
		idx = len(pol.Backoffs) - 1
	}
	base := pol.Backoffs[idx]
	// +0~50% jitter：溢出防护（Backoffs 是我们自己的常量表，正常不会到 1<<62）。
	if base <= 0 || base > math.MaxInt64/2 {
		return base
	}
	// #nosec G404 -- jitter 的熵源质量无关紧要（只用于错开重试时刻，非安全用途）。
	return base + time.Duration(rand.Int63n(int64(base)/2+1))
}

// CountsAsUpstreamFailure 报告该错误类是否计入上游健康计数（§6.4 表「健康计数」行）。
//
// connect / timeout / 5xx 计入（上游或网络确实坏了）；conn_reset / tls 不计
// （半开连接是复用的代价，与上游健康无关）；429 / auth / quota 不计（根因在凭据）。
func CountsAsUpstreamFailure(class ErrClass) bool {
	pol, ok := retryTable[class]
	return ok && pol.CountsAsFailure
}

// RetryAction 是失败后的下一步。
type RetryAction int

const (
	// RetrySameCandidate：同候选用新连接重试（半开连接剥离率最高的路径，§6.3 规则 3）。
	RetrySameCandidate RetryAction = iota
	// SwitchCandidate：换下一个候选。
	SwitchCandidate
	// FailRequest：终止请求并向客户端发 error（已吐内容后的任何错误、预算耗尽、
	// bad_request 类）。
	FailRequest
)

// String 供日志与测试输出。
func (a RetryAction) String() string {
	switch a {
	case RetrySameCandidate:
		return "retry_same_candidate"
	case SwitchCandidate:
		return "switch_candidate"
	default:
		return "fail_request"
	}
}

// RetryDirective 是 RetryController 给 call 模块的下一步指令。
type RetryDirective struct {
	Action RetryAction
	// Backoff 是 Action == RetrySameCandidate 时的等待时长（已含 jitter）。
	Backoff time.Duration
}

// RetryController 记一个候选（或整次请求）的重试进度并给出下一步指令。
//
// 为什么是带状态的控制器而不是纯函数：call 模块需要在候选切换时**重置同候选计数
// 但保留请求内计数**——只有持有状态的控制器才能把这两本账分开（ResetCandidate）。
type RetryController struct {
	budget    RetryBudget
	sameCand  int
	total     int
	lastClass ErrClass
	lastVis   bool
}

// NewRetryController 建一个控制器。零值预算回落默认。
func NewRetryController(budget RetryBudget) *RetryController {
	if budget.PerCandidate <= 0 || budget.PerRequest <= 0 {
		budget = DefaultRetryBudget()
	}
	return &RetryController{budget: budget}
}

// OnFailure 是失败后的决策入口。
//
// visibleWritten 必须传 Stats().VisibleWritten——C7 的判定完全依赖它，不接受推断。
// sameCandidateRetries / totalRetries 由调用方记账（M1 不自动累加：call 模块可能
// 对同一失败既做同候选重试又做换候选，自动累加会把账算重）。
func (r *RetryController) OnFailure(class ErrClass, sameCandidateRetries, totalRetries int, visibleWritten bool) RetryDirective {
	r.sameCand, r.total, r.lastClass, r.lastVis = sameCandidateRetries, totalRetries, class, visibleWritten
	if visibleWritten {
		// §6.3 规则 2：已吐可见内容 → 终止，绝不换候选（客户端已看到一半的答案）。
		return RetryDirective{Action: FailRequest}
	}
	if class == ErrBadRequest {
		// 我们写错了请求，重试与换候选都无意义，直接还客户端。
		return RetryDirective{Action: FailRequest}
	}
	if r.budget.CanRetry(class, sameCandidateRetries, totalRetries, visibleWritten) {
		return RetryDirective{
			Action:  RetrySameCandidate,
			Backoff: r.budget.Backoff(class, sameCandidateRetries),
		}
	}
	// 同候选不可重试时仍有两条路：请求内总预算没耗尽 → 换候选；耗尽 → 终止。
	if totalRetries >= r.budget.PerRequest {
		return RetryDirective{Action: FailRequest}
	}
	return RetryDirective{Action: SwitchCandidate}
}

// ResetCandidate 在切换到新候选时调用：同候选计数归零，请求内计数保留。
func (r *RetryController) ResetCandidate() { r.sameCand = 0 }

// Total 返回请求内累计重试次数快照。
func (r *RetryController) Total() int { return r.total }

// LastFailure 返回最近一次失败的类与可见内容标志（测试断言用）。
func (r *RetryController) LastFailure() (ErrClass, bool) { return r.lastClass, r.lastVis }
