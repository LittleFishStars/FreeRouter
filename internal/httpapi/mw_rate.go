package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
)

// mw_rate.go：L1 链第 6 步（限速）中间件（docs/architecture/01-overview.md）。
//
// 壳职责：把 context 里的 Key 主体与请求参数接给 meter.Limiter；
// 判定与 0 语义（RPM/TPM/并发 -1 不限、0 全拒；GlobalRPM 0=不限制）
// 全部在 meter——这里不写任何"多少算超"的分支。
//
// 时序契约（middleware.go 头注）：鉴权在限速**之前**——否则无效凭据
// 可以刷爆计数器、误伤真实账户；所以本中间件拿不到主体时是装配错误
// （500），不是"未认证"（401 已由上游给出）。
//
// 装配归 Lead：Handler() 里在 mwKeyAuth 之后挂 s.mwRate(limiter, limitsOf)。

// RateLimitsSource 供装配层给定"账户生效速率"的回调。
//
// 为什么是回调而不是中间件自己读库：生效值 = 组策略 min 与账户列的叠加，
// 是 auth.EffectivePolicy 的知识；meter 只认数字（AccountLimits 的定案），
// httpapi 不该知道叠加规则。装配层（lead）用 authz + store 组出这个函数。
// 返回 (AccountLimits, ok)：ok=false 表示该账户无速率配置，跳过账户闸
// （例如迁移期/未装配策略），全局闸照常生效——**跳过 ≠ 拒绝**，
// 未配置在读侧的语义是不限（-1），不是 0 全拒。
type RateLimitsSource func(ctx Context, accountID string) (meter.AccountLimits, bool)

// Context 是限速中间件需要的外围参数（避免让回调依赖 *http.Request）。
type Context = context.Context

// rateWriter 包装 ResponseWriter，用于在响应头补 Retry-After。
type rateWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (rw *rateWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.wroteHeader = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *rateWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.wroteHeader = true
	}
	return rw.ResponseWriter.Write(b)
}

// mwRate 是限速中间件：判定+递增（Allow，非 CheckRate）在业务处理前完成。
//
// tokenCost 是**预检估计值**（in+out 的上界或配置的默认预算）；
// 终态实际用量由结算路径（mw_quota / RecordRequest）记账，
// 两者的差值是"预检宽松一点"的已知代价（06-runtime §2.3 的口径）。
func (s *Server) mwRate(l *meter.Limiter, limitsOf RateLimitsSource, tokenCost int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := KeyPrincipalFromContext(r.Context())
			if p == nil {
				// 装配顺序错误（限速挂在鉴权前）——暴露给日志而不是让
				// 计数器挂在空主体上把所有请求算进一个桶。
				writeError(w, http.StatusInternalServerError,
					string(auth.CodeInternal), "限速中间件缺少鉴权主体（装配顺序错误）", nil)
				return
			}

			lim := meter.AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: -1} // 缺省=不限
			if limitsOf != nil {
				if got, ok := limitsOf(r.Context(), p.AccountID); ok {
					lim = got
				}
			}

			// 全局熔断预检（GlobalDailyTokens=0 → 恒不熔断，meter 保证）。
			if err := l.CheckGlobalFuse(); err != nil {
				s.writeAuthErr(w, err)
				return
			}

			// RPM/TPM：判定+递增同锁（Allow）。
			if _, err := l.Allow(p.AccountID, lim, tokenCost); err != nil {
				s.writeAuthErr(w, err)
				return
			}

			// 并发闸：账户层 429 / 全局层 503（bc219ba 拆码定案，此处只调用）。
			// release 挂在 rw 上由 defer 调用；中间件不能提前 release——
			// 槽位占满的是"在途请求"，含整个业务处理。
			release, err := l.AcquireConcurrency(r.Context(), p.AccountID, lim)
			if err != nil {
				s.writeAuthErr(w, err)
				return
			}
			defer release()

			next.ServeHTTP(&rateWriter{ResponseWriter: w}, r)
		})
	}
}

// writeAuthErr 把 auth.Error 翻成统一错误体（限速/配额/并发闸共用出口）。
//
// 状态映射按 docs/api/03-errors.md §4：rate_limited/quota_exceeded/
// pool_exhausted/too_many_attempts → 429；overloaded → 503；
// 其余（internal_error）→ 500。Retry-After 有值时写响应头（§5.2）。
func (s *Server) writeAuthErr(w http.ResponseWriter, err error) {
	var ae *auth.Error
	if !asAuthError(err, &ae) {
		writeError(w, http.StatusInternalServerError, string(auth.CodeInternal), err.Error(), nil)
		return
	}
	status := http.StatusInternalServerError
	switch ae.Code {
	case auth.CodeRateLimited, auth.CodeQuotaExceeded, auth.CodePoolExhausted, auth.CodeTooManyAttempts:
		status = http.StatusTooManyRequests
	case auth.CodeOverloaded:
		status = http.StatusServiceUnavailable
	}
	if ae.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(
			(ae.RetryAfter+time.Second-1)/time.Second))) // 向上取整：宁等多 1s 不早 1ms
	}
	msg := ae.Msg
	if msg == "" {
		msg = err.Error()
	}
	writeError(w, status, string(ae.Code), msg, nil)
}

// asAuthError 提取 *auth.Error（errors.As 的薄封装，避免本包散落反射式断言）。
func asAuthError(err error, target **auth.Error) bool {
	if e, ok := err.(*auth.Error); ok {
		*target = e
		return true
	}
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if e, ok := err.(*auth.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
