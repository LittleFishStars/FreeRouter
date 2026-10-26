package meter

import (
	"context"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// usage.go —— 真实用量记账（M4 收口，docs/ops/03-milestones.md §6）。
//
// 与 TryDebit 的分工（05-storage.md 7.4 的既定口径）：
//   - **入站预扣**（mw_quota 的 TryDebit）：request=1 精确；token/credit
//     用估计值——闸门的职责是"别让超发发生"；
//   - **终态结算**（本文件的 SettleUsage）：流结束后按 ir.Usage 的**实际**
//     数字回写一次，记账无条件（CreditQuota），不做闸门判定——
//     "本周期总量可能超上限"是终态结算的必然结果（少扣方向可接受，
//     多扣会把失败请求也计费）。
//
// 宁少算不可多算（task-14 红线）的三条落地：
//   1. nil 与 0 分离：ir.Usage 三态（nil=上游没报/0=明确为零/estimated=我方估），
//     nil 视为 0 参与累计会把"上游不报账"刷成零消耗——额度护栏直接失效
//     （ir/ir.go §9 注释原文）。这里对 nil 一律跳过（少算方向）。
//   2. estimated 也记账，但**走独立的结算通道**（Settlement.Estimated 标记
//     进 UsageSink 的调用参数），对账层可以分离两类数字。
//   3. 客户端断开：只统计断开前**已收到**的最后一个 EvUsage（事件不变式 I4
//     保证 usage 至多一次且在 done 之前；断开时没收到 EvUsage 就按 0 结算——
//     "没有可靠数字时不编造"）。

// UsageTotals 是一次请求终态的用量汇总（从 ir.Usage 折算）。
type UsageTotals struct {
	// Input / Output 是 prompt 与 completion 的 token 数（nil 已按 0 折算——
	// 折算发生在 TotalFromUsage，本 struct 不再保留三态）。
	Input  int64
	Output int64

	// CacheRead / CacheWrite / Reasoning 是明细（usage_daily 只记 in/out，
	// 明细留给 requests 行与对账；这里带出以防丢失）。
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64

	// Source 是来源（upstream / estimated / unknown，ir 定义）。
	Source ir.UsageSource
}

// TotalFromUsage 把 ir.Usage 折算成 Totals。
//
// nil 跳过（=0 计入）是"宁少算"的第一落点；estimated 与 upstream 都收
// （对账靠 Source 区分，额度闸不区分——上游不报账的凭据同样消耗了上游成本）。
func TotalFromUsage(u *ir.Usage) UsageTotals {
	if u == nil {
		return UsageTotals{Source: ir.UsageUnknown}
	}
	t := UsageTotals{Source: u.Source}
	if u.InputTokens != nil {
		t.Input = int64(*u.InputTokens)
	}
	if u.OutputTokens != nil {
		t.Output = int64(*u.OutputTokens)
	}
	if u.CacheReadTokens != nil {
		t.CacheRead = int64(*u.CacheReadTokens)
	}
	if u.CacheWriteTokens != nil {
		t.CacheWrite = int64(*u.CacheWriteTokens)
	}
	if u.ReasoningTokens != nil {
		t.Reasoning = int64(*u.ReasoningTokens)
	}
	return t
}

// TokenCost 是 usage_daily 的记账口径：in + out（05-storage.md 3.1 的
// token 维度定义；cache/reasoning 明细不进日汇总——那份口径对齐 3.7）。
func (t UsageTotals) TokenCost() int64 { return t.Input + t.Output }

// UsageRecorder 是终态结算的执行面：一次请求结束（成功/失败/断开）调一次。
//
// 为什么是接口而不是函数：v1 面板（ir-protocol 域）在流结束时持有的是
// 装配层给的依赖；接口让 meter 的实现（SettlementRecorder）与测试假体
// 可以互换。**独立文件由本文件承担**；v1 接线归 Lead（不改 v1*.go）。
type UsageRecorder interface {
	// SettleUsage 结算一次请求的实际用量。
	//   - cost 里的 Token 维度用 totals.TokenCost()（实际值，替换预扣估计）；
	//   - 多余的预扣（估计 > 实际）不退——总量上界由 daily 汇总兜底
	//     （05-storage 7.4 口径，estimate 偏差是预检的已知代价）；
	//   - 落库（usage_daily/requests）与记账（CreditQuota）的实现由
	//     store.RecordRequest 承担，meter 只做"ir.Usage → Settlement"的
	//     折算编排（参数见 UsageSink 的签名）。
	SettleUsage(ctx context.Context, su SettledUsage) error
}

// SettledUsage 是折算完成的终态结算包（UsageSink → store 的中间形状）。
type SettledUsage struct {
	AccountID string // store 主键串（adapter 翻 int64）
	ModelID   string // 抽象模型 id（""=合计行；名字解析归调用方）
	// QuotaRequest 是 request 维度的实际计费（通常 1：一次请求 = 1；
	// 失败请求调用方传 0——"没有消耗的尝试不扣"，03-groups §4）。
	QuotaRequest int64
	// Totals 是 ir.Usage 的折算（TokenCost() = token 维度实际值）。
	Totals UsageTotals
	// CreditCost 是 credit 维度（价格乘数表归 models 层；乘数未配时=0，
	// 只按 token 记账——宁少算）。
	CreditCost int64
	// EndedAt 是结算时刻（usage_daily 的 day 归属；UTC）。
	EndedAt time.Time
}

// SettlementRecorder 把 SettledUsage 落到 QuotaDebiter 的扣减面 +
// 可选的 store 落库面。
//
// 现状（M4）：额度侧走 debiter（TryDebit 系列的条件 UPDATE 语义在
// CreditQuota 之前由 v1 的预扣承担；终态这里用 debiter 的 Credit 通道
// 补记账）。store.RecordRequest 的三合一事务（requests+usage_daily+
// CreditQuota）由 v1 面板的 defer 调用（接线归 Lead）；本 struct 先提供
// 额度补差的确定性路径。
type SettlementRecorder struct {
	debiter QuotaLedger
	// poolsOf 返回该请求涉及的组 id（池补账范围；nil/空=无池）。
	poolsOf func(ctx context.Context, accountID string) ([]string, bool)
}

// QuotaLedger 是 meter 自己的结算面：TryDebit（入站预扣）+ Credit（终态
// 补差）。为什么不用 httpapi.QuotaDebiter：那个接口只声明 TryDebit——
// httpapi 是 meter 的下游（mw_quota 调 adapter），meter 不能反向 import
// 下游包；终态记账是这个接口的第二方法，独立命名避免与中间件面耦合。
// StoreAdapter 已有两个方法（Credit 见 adapter.go 的实现）。
type QuotaLedger interface {
	TryDebit(ctx context.Context, dc DebitContext) error
	Credit(ctx context.Context, cc CreditContext) error
}

// NewSettlementRecorder 构造。debiter 必须非 nil（nil 时 SettleUsage 返回
// internal_error——装配错误第一枪暴露）。
func NewSettlementRecorder(debiter QuotaLedger, poolsOf func(ctx context.Context, accountID string) ([]string, bool)) *SettlementRecorder {
	return &SettlementRecorder{debiter: debiter, poolsOf: poolsOf}
}

// SettleUsage 终态结算：按实际 token/request/credit 无条件记账。
//
// 与预扣的关系：预扣（TryDebit）已把估计值计入额度行；终态的**实际**值
// 走 CreditQuota（无条件累加，store 语义）。若实际 < 预扣，行内余量偏少
// ——宁少不多（红线）；若实际 > 预扣，差额补进（同样少扣方向可接受）。
// 两者都是"记账无条件"的组成部分。
func (r *SettlementRecorder) SettleUsage(ctx context.Context, su SettledUsage) error {
	if r == nil || r.debiter == nil {
		return ErrorfInternal("SettlementRecorder 未装配")
	}
	if su.QuotaRequest < 0 || su.Totals.TokenCost() < 0 || su.CreditCost < 0 {
		return ErrorfInternal("负消耗进入结算（宁少算不可多算的反面）")
	}
	var gids []string
	if r.poolsOf != nil {
		if got, ok := r.poolsOf(ctx, su.AccountID); ok {
			gids = got
		}
	}
	return r.debiter.Credit(ctx, CreditContext{
		AccountID:       su.AccountID,
		AbstractModelID: su.ModelID,
		GroupIDs:        gids,
		Cost: UsageCost{
			Request: su.QuotaRequest,
			Token:   su.Totals.TokenCost(),
			Credit:  su.CreditCost,
		},
	})
}

// CreditContext 是终态记账的范围描述（TryDebit 的记账版）。
type CreditContext struct {
	AccountID       string
	AbstractModelID string // "" = 合计行（store.TotalScope）
	GroupIDs        []string
	Cost            UsageCost
}

// ErrorfInternal 是包内 internal_error 的紧凑构造（哨兵在 auth.Code）。
func ErrorfInternal(msg string) error {
	return auth.Errorf(auth.CodeInternal, nil, "%s", msg)
}
