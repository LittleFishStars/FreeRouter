package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// settle_bridge.go：把 httpapi 的终态结算事件（httpapi.Settler）接到
// meter 的额度面与 store 的请求面。
//
// 为什么桥在 app 而不在 httpapi：httpapi 的两个边界都很硬——它不认识
// meter（额度语义）也不认识 store（持久化）。由装配层把两者接起来，是
// 本项目「协议层不碰持久层、持久层不碰 HTTP 形状」的一贯切法。
//
// 两个面的分工（都来自 docs/architecture/05-storage.md §7.2）：
//  1. 额度面：meter.SettlementRecorder.Credit → store.QuotaRepo 的条件
//     UPDATE（账户合计行 + 按模型行 + 组池）。**失败不阻断请求面**——
//     账的两半各自独立，丢一半好过全丢（额度面失败只拿到日志告警）。
//  2. 请求面：store.UsageRepo.RecordRequest → requests 行 + usage_daily
//     累加。它的 Settlement 参数留空：额度记账已由第 1 步完成，同一份
//     QuotaCredit 再传一次会双记（行内 upsert 是累加语义）。
//
// 为什么两半不能共用一个事务：meter 的 Credit 是「逐行条件 UPDATE」，
// 它按 (mid, unit, period) 展开成 6 行 ×(合计+按模型) 的写集合，
// 而 RecordRequest 是一个长事务（INSERT + upsert）。把前者塞进后者的
// 事务里，需要一个跨包的 tx 句柄穿透——那是把 store 的私有 Repos 暴露
// 给 meter，代价大于收益。当前取舍：同库但非同事务；不一致的窗口是
// 「额度扣了、requests 没写」，由对账告警发现（量级远小于双记风险）。
// **发现后的动作必须可执行**：告警文案要带排障线索（见 Settle 的返回包装）——
// 额度面已扣而 requests 缺行时，按 account_id + ended_at 时间窗查 requests/usage_daily
// 的差集（usage_daily 有行而 requests 无行 = 请求面丢了），补行前先确认该窗口没有
// 同一请求的既有行，避免把一次请求补成两行。

// settleBridge 实现 httpapi.Settler。
type settleBridge struct {
	// recorder 负责额度面（Credit 通道；nil = 未装配，跳过并报错一次）。
	recorder *meter.SettlementRecorder
	// usage 是请求面仓储（RecordRequest 自开事务）。
	usage *store.UsageRepo
	// modelIDOf 把抽象模型名翻成 store 主键（与配额中间件同一个解析器）。
	modelIDOf func(ctx context.Context, name string) (string, bool)
	// log 用于两半各自的结构性告警（errors.Join 后调用方分不清哪一半失败）。
	log *slog.Logger
}

// newSettler 构造终态结算出口（server_build 里注入 Server.SetSettler）。
func (a *App) newSettler() httpapi.Settler {
	return &settleBridge{
		recorder:  meter.NewSettlementRecorder(a.quotaLedger, a.groupIDsSource),
		usage:     a.store.Repos().Usage,
		modelIDOf: a.modelIDOf,
		log:       a.log,
	}
}

// Settle 实现 httpapi.Settler。
//
// 幂等性：**不幂等**，也不该幂等——调用方（proxyV1 的 defer）保证一次
// 请求只调一次。若将来出现重试需求，去重键必须是 request_id（尚未入表，
// 记在 requests 注释里由后续里程碑决定）。
func (b *settleBridge) Settle(ctx context.Context, in httpapi.SettleInput) error {
	if b == nil || b.usage == nil {
		return errors.New("settle bridge 未装配 usage 仓储")
	}

	// 账户 id：KeyPrincipal 给的就是 store 主键（UUID 文本）本身
	// （bridges.go 的 keyAuthBridge 直接透传）。parseID 与 groupIDsSource
	// 共用同一个形状校验口径。
	var accountID string
	if in.AccountID != "" {
		id, err := parseID(in.AccountID)
		if err != nil {
			return fmt.Errorf("结算账户 id %q: %w", in.AccountID, err)
		}
		accountID = id
	}

	var modelID string
	if in.ModelName != "" {
		if id, ok := b.modelIDOf(ctx, in.ModelName); ok {
			modelID = id
		}
	}

	// ---- 面 1：额度 ----
	// 失败请求 QuotaRequest=0（不扣 request 维度）；token/credit 维度仍按
	// Usage 实记——上游可能已经算过 token，不记就是少算。
	var quotaErr error
	if accountID != "" {
		quotaReq := int64(0)
		if in.Status == "ok" {
			quotaReq = 1
		}
		quotaErr = b.recorder.SettleUsage(ctx, meter.SettledUsage{
			AccountID:    accountID,
			ModelID:      modelID,
			QuotaRequest: quotaReq,
			Totals:       meter.TotalFromUsage(in.Usage),
			// CreditCost 留给价格乘数表（models 层，M6 决策）；当前按 0
			// 只记 token——宁少算。
			CreditCost: 0,
			EndedAt:    in.EndedAt,
		})
	}

	// ---- 面 2：请求记录 ----
	rec := store.RequestRecord{
		TS:       in.EndedAt.Unix(),
		Endpoint: in.Endpoint,
		Status:   in.Status,
	}
	if accountID != "" {
		rec.AccountID = ptrStr(accountID)
	}
	if in.KeyID != "" {
		if id, err := parseID(in.KeyID); err == nil {
			rec.KeyID = ptrStr(id)
		}
	}
	if modelID != "" {
		rec.VirtualModelID = ptrStr(modelID)
	}
	if in.RealModelID != "" {
		rec.ResolvedRealModelID = ptrStr(in.RealModelID)
	}
	// ResolvedCredentialID 留空：候选展开的凭据归属尚未回传（03-upstream
	// 的 credential id 在 relay 路径上是 sidecar 内部概念）。等 M8 贡献
	// 凭据落地后再补——现在编造一个 id 比留空更糟（会指错行）。
	if in.HTTPStatus > 0 {
		rec.HTTPStatus = ptrInt64(int64(in.HTTPStatus))
	}
	if in.ErrorCode != "" {
		code := in.ErrorCode
		rec.ErrorCode = &code
	}
	rec.RetryCount = int64(in.RetryCount)
	if in.TTFTMs >= 0 {
		rec.TTFTMs = ptrInt64(in.TTFTMs)
	}
	if in.TotalMs >= 0 {
		rec.TotalMs = ptrInt64(in.TotalMs)
	}
	if in.Usage != nil {
		// nil 与 0 的语义差别由 store 字段的指针表达（05-storage 3.7）：
		// 上游没报就留 nil，不写 0——0 会在对账里变成"确实用了 0 token"。
		if in.Usage.InputTokens != nil {
			rec.InTokens = ptrInt64(int64(*in.Usage.InputTokens))
		}
		if in.Usage.OutputTokens != nil {
			rec.OutTokens = ptrInt64(int64(*in.Usage.OutputTokens))
		}
	}

	// 请求面失败才是真失败（账本主记录）；额度面失败合并进去。
	// 两半各自记一条结构性 warn：errors.Join 后调用方无法区分哪一半失败，
	// 而单独一半失败（尤其是额度面）也需要在值班视角里独立可见。
	if quotaErr != nil {
		b.log.Warn("settle.quota_failed", "account_id", in.AccountID,
			"model", in.ModelName, "ts", in.EndedAt.Unix(), "error", quotaErr)
	}
	reqErr := b.usage.RecordRequest(ctx, rec, store.Settlement{})
	if reqErr != nil {
		b.log.Warn("settle.requests_failed", "account_id", in.AccountID,
			"model", in.ModelName, "ts", in.EndedAt.Unix(), "error", reqErr)
	}
	merged := errors.Join(quotaErr, reqErr)
	if merged == nil {
		return nil
	}
	// 把可执行排障线索带进调用方的 warn（httpapi.settleRequest 只打一行日志）。
	// 两半非同事务：唯一会制造「额度面已扣、请求面缺行」的窗口就在本函数内，
	// 告警若只写“结算失败”，值班得从头猜是哪一半、怎么补。
	return fmt.Errorf("%w（对账线索：额度面与请求面非同事务；若额度已扣而 requests 缺行，"+
		"按 account_id + requests.ts/usage_daily.day 的时间窗查差集后补行；"+
		"补行前先确认该窗口无同一请求既有行，避免双记）", merged)
}

func ptrInt64(v int64) *int64 { return &v }

// ptrStr 装 *string（id 列的可空引用）。
func ptrStr(v string) *string { return &v }

// compile-time 断言：settleBridge 满足 httpapi.Settler。
var _ httpapi.Settler = (*settleBridge)(nil)
