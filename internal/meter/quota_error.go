package meter

import (
	"errors"
	"fmt"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// quota_error.go —— quota 错误体的双通道收口（docs/api/03-errors.md 定案）：
//
//	顶层 message（自然语言，"账户的 token 额度已用尽，X 小时后重置"）
//	+ 机器可读 quota 对象 {"unit","period","model"?}（客户端自动降级驱动）
//	+ Retry-After 头（指向命中行的重置时刻）。
//
// 为什么收口在这里而不是中间件：错误翻译的知识全部在 meter——
// store.ErrQuotaExhausted 的行字段（Unit/Period/AbstractModelID/锚点）、
// meter.QuotaRow 的行化语义、合计行 model 缺省的定案，跨层散抄
// 每一处都是一个"忘了带 quota 对象的 429"。任何 meter 路径产生的
// quota/pool 错误都必须经过这里的构造器，评审只看一个文件。

// TranslateQuotaExhausted 把 store 的额度耗尽错误翻成 auth.Error
// （quota_exceeded + quota 对象 + Retry-After）。
//
//   - modelID：命中行的抽象模型 id（store.TotalScope=合计行 → model 字段
//     缺省；>0 → 数字 id 串——名字解析在调用方按需查，429 热路径不碰模型表）；
//   - ResetAt 的锚点脏（解析失败 ok=false）→ **不带 Retry-After**：
//     编造一个错误的重置时刻比没有更糟（客户端会按时回来再吃一个 429）；
//   - 已知为负的时间差（重置时刻已过：惰性归零还没跑的窗口）同样不带头。
func TranslateQuotaExhausted(qe store.ErrQuotaExhausted, modelID string, now time.Time) error {
	e := ErrQuotaExceeded.WithQuota(
		string(qe.Unit), string(qe.Period), modelIDString(modelID))
	if reset, ok := qe.ResetAt(); ok {
		if d := reset.Sub(now); d > 0 {
			e = e.WithRetryAfter(d)
		}
	}
	return e
}

// TranslatePoolExhausted 同形翻译组池耗尽（pool_exhausted 码）。
// groupID 进 detail（排障面），不进 quota 对象（客户端只需要知道
// 哪个 unit/period/model 打满——池属于哪个组不是客户端的决策变量）。
func TranslatePoolExhausted(qe store.ErrQuotaExhausted, groupID, modelID string, now time.Time) error {
	e := ErrPoolExhausted.
		WithQuota(string(qe.Unit), string(qe.Period), modelIDString(modelID)).
		WithDetail(string(qe.Unit), fmt.Sprintf("%s", groupID))
	if reset, ok := qe.ResetAt(); ok {
		if d := reset.Sub(now); d > 0 {
			e = e.WithRetryAfter(d)
		}
	}
	return e
}

// HasQuotaDetail 报告错误是否携带 quota 对象（中间件面判断是否走
// 契约字段输出路径用；details 排障字段里不算数）。
func HasQuotaDetail(err error) bool {
	var ae *auth.Error
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Quota != nil
}

// RetryAfterOfErr 提取 Retry-After（0 = 不带；脏锚点/已过期路径返回 0）。
// 与 auth.RetryAfterOf 的区别：这里兜住非 auth.Error 的输入返回 0
// （调用方写响应头的分支不需要再做类型断言）。
func RetryAfterOfErr(err error) time.Duration {
	var ae *auth.Error
	if !errors.As(err, &ae) {
		return 0
	}
	return ae.RetryAfter
}
