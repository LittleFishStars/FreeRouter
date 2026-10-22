package meter

import (
	"context"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// usage.go 的契约测试：ir.Usage 折算的"宁少算"纪律与结算包形状。
// 落库/扣减的端到端归 adapter 与 store 的契约测试。

func TestContractUsageNilFieldsUndercount(t *testing.T) {
	// 宁少算：nil 一律按 0 折算（上游不报账不等于零消耗——但没有可靠
	// 数字时不编造；真实消耗由 requests 行与上游对账兜底）。
	u := &ir.Usage{OutputTokens: ir.Ptr(7), Source: ir.UsageUpstream}
	tot := TotalFromUsage(u)
	if tot.Input != 0 || tot.Output != 7 {
		t.Fatalf("nil 字段应折 0: %+v", tot)
	}
	if tot.TokenCost() != 7 {
		t.Fatalf("TokenCost = %d，期望 7", tot.TokenCost())
	}
}

func TestContractUsageNilUsageIsUnknown(t *testing.T) {
	// 整个 Usage 为 nil（流断开且没收到 EvUsage）→ 全零 + unknown 来源。
	// 断开路径结算时传这个形状：记一次"发生过"（request 维度由调用方定），
	// token 不编造。
	tot := TotalFromUsage(nil)
	if tot.TokenCost() != 0 || tot.Source != ir.UsageUnknown {
		t.Fatalf("nil Usage 应全零+unknown: %+v", tot)
	}
}

func TestContractUsageZeroIsNotNil(t *testing.T) {
	// ir 三态的核心：0 是"上游明确说零"（可信），nil 是"没报"（按零折算
	// 但来源仍记 upstream/estimated）。折算后数值相同——差异保留在
	// Source 上，对账层分离两类数字。
	zero := &ir.Usage{InputTokens: ir.Ptr(0), OutputTokens: ir.Ptr(0), Source: ir.UsageUpstream}
	tot := TotalFromUsage(zero)
	if tot.TokenCost() != 0 || tot.Source != ir.UsageUpstream {
		t.Fatalf("0 应保留 upstream 来源: %+v", tot)
	}
}

func TestContractUsageCacheAndReasoningDetailed(t *testing.T) {
	// cache/reasoning 是明细：TokenCost 只含 in+out（usage_daily 口径），
	// 明细在 Totals 上不丢（requests 行/对账用）。
	u := &ir.Usage{
		InputTokens:      ir.Ptr(100),
		OutputTokens:     ir.Ptr(20),
		CacheReadTokens:  ir.Ptr(50),
		CacheWriteTokens: ir.Ptr(5),
		ReasoningTokens:  ir.Ptr(30),
		Source:           ir.UsageUpstream,
	}
	tot := TotalFromUsage(u)
	if tot.TokenCost() != 120 {
		t.Fatalf("TokenCost = %d，期望 120（in+out，明细不进日汇总口径）", tot.TokenCost())
	}
	if tot.CacheRead != 50 || tot.CacheWrite != 5 || tot.Reasoning != 30 {
		t.Fatalf("明细丢失: %+v", tot)
	}
}

func TestContractUsageEstimatedSourcePreserved(t *testing.T) {
	// estimated 与 upstream 都记账（额度闸不区分——上游成本真实发生），
	// 但 Source 原样带出供对账分离（§9：两类数字永不可混）。
	est := &ir.Usage{InputTokens: ir.Ptr(10), Source: ir.UsageEstimated}
	if got := TotalFromUsage(est).Source; got != ir.UsageEstimated {
		t.Fatalf("estimated 来源应保留: %v", got)
	}
}

// 结算面：负消耗拒绝（红线"宁少算"的反面是多算——负数会凭空造额度）。
func TestContractSettleRejectsNegativeCost(t *testing.T) {
	rec := NewSettlementRecorder(&fakeLedger{}, nil)
	err := rec.SettleUsage(t.Context(), SettledUsage{
		AccountID: "1", QuotaRequest: -1, Totals: UsageTotals{}, EndedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("负 request 消耗必须拒")
	}
	err = rec.SettleUsage(t.Context(), SettledUsage{
		AccountID: "1", Totals: UsageTotals{Input: -5}, EndedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("负 token 消耗必须拒")
	}
}

// 未装配的 recorder 第一枪报 internal_error（不静默丢账也不静默记账）。
func TestContractSettleUnwiredFails(t *testing.T) {
	rec := NewSettlementRecorder(nil, nil)
	if err := rec.SettleUsage(t.Context(), SettledUsage{AccountID: "1", EndedAt: time.Now()}); err == nil {
		t.Fatal("未装配必须报错")
	}
}

// fakeLedger：结算面的最小假体（记录调用；TryDebit/Credit 的真实语义
// 归 adapter 契约测试）。
type fakeLedger struct {
	credits []CreditContext
	debits  []DebitContext
}

func (f *fakeLedger) TryDebit(_ context.Context, dc DebitContext) error {
	f.debits = append(f.debits, dc)
	return nil
}
func (f *fakeLedger) Credit(_ context.Context, cc CreditContext) error {
	f.credits = append(f.credits, cc)
	return nil
}

func TestContractSettleCarriesActualTotals(t *testing.T) {
	fl := &fakeLedger{}
	rec := NewSettlementRecorder(fl, nil)
	err := rec.SettleUsage(t.Context(), SettledUsage{
		AccountID:    "1",
		ModelID:      7,
		QuotaRequest: 1,
		Totals:       UsageTotals{Input: 100, Output: 20},
		CreditCost:   3,
		EndedAt:      time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.credits) != 1 {
		t.Fatalf("应恰一次 Credit，得 %d", len(fl.credits))
	}
	cc := fl.credits[0]
	if cc.Cost.Token != 120 || cc.Cost.Request != 1 || cc.Cost.Credit != 3 {
		t.Fatalf("结算数字错误: %+v", cc.Cost)
	}
	if cc.AbstractModelID != 7 {
		t.Fatalf("模型 id 应透传: %d", cc.AbstractModelID)
	}
}

func TestContractSettlePoolScopeFromSource(t *testing.T) {
	// poolsOf 提供的组范围决定池补账面（nil/未命中=无池——宁少算）。
	fl := &fakeLedger{}
	poolsCalled := 0
	rec := NewSettlementRecorder(fl, func(context.Context, string) ([]int64, bool) {
		poolsCalled++
		return []int64{11, 12}, true
	})
	_ = rec.SettleUsage(t.Context(), SettledUsage{AccountID: "1", EndedAt: time.Now(),
		Totals: UsageTotals{Input: 1}})
	if poolsCalled != 1 || len(fl.credits) != 1 || len(fl.credits[0].GroupIDs) != 2 {
		t.Fatalf("池范围应从 source 取: called=%d credits=%v", poolsCalled, fl.credits)
	}
	// 不命中（ok=false）→ 无池。
	rec2 := NewSettlementRecorder(fl, func(context.Context, string) ([]int64, bool) {
		return nil, false
	})
	_ = rec2.SettleUsage(t.Context(), SettledUsage{AccountID: "1", EndedAt: time.Now(),
		Totals: UsageTotals{Input: 1}})
	if len(fl.credits[1].GroupIDs) != 0 {
		t.Fatalf("未命中池 source 应空范围: %v", fl.credits[1].GroupIDs)
	}
}
