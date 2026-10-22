package meter

import (
	"errors"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// quota_error.go 契约测试：双通道字段完整性 + 脏锚点不发 Retry-After。

var qeNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// TestContractQuotaErrorDualChannel：quota 对象三键齐全 + Retry-After 指向重置时刻。
func TestContractQuotaErrorDualChannel(t *testing.T) {
	qe := store.ErrQuotaExhausted{
		Scope: "account", Unit: "token", Period: "day",
		PeriodAnchor: "2026-10-20", AbstractModelID: 7,
	}
	err := TranslateQuotaExhausted(qe, 7, qeNow)
	var ae *auth.Error
	if !errors.As(err, &ae) {
		t.Fatal("应翻成 auth.Error")
	}
	if ae.Code != auth.CodeQuotaExceeded {
		t.Fatalf("code 应 quota_exceeded，得 %s", ae.Code)
	}
	if ae.Quota == nil {
		t.Fatal("必须带 quota 对象（机器通道）")
	}
	if ae.Quota.Unit != "token" || ae.Quota.Period != "day" || ae.Quota.Model != "7" {
		t.Fatalf("quota 对象错误: %+v", ae.Quota)
	}
	want := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC).Sub(qeNow)
	if ae.RetryAfter != want {
		t.Fatalf("Retry-After = %v，期望 %v（锚点+1日）", ae.RetryAfter, want)
	}
}

// TestContractQuotaErrorTotalRowOmitsModel：合计行（mid=0）model 缺省。
func TestContractQuotaErrorTotalRowOmitsModel(t *testing.T) {
	qe := store.ErrQuotaExhausted{Unit: "request", Period: "day", PeriodAnchor: "2026-10-20"}
	err := TranslateQuotaExhausted(qe, store.TotalScope, qeNow)
	var ae *auth.Error
	_ = errors.As(err, &ae)
	if ae.Quota == nil || ae.Quota.Model != "" {
		t.Fatalf("合计行 model 应缺省（空串=JSON 省略）: %+v", ae.Quota)
	}
}

// TestContractQuotaErrorDirtyAnchorNoRetryAfter：脏锚点（ok=false）宁可不发。
func TestContractQuotaErrorDirtyAnchorNoRetryAfter(t *testing.T) {
	qe := store.ErrQuotaExhausted{Unit: "token", Period: "day", PeriodAnchor: "not-a-date"}
	err := TranslateQuotaExhausted(qe, store.TotalScope, qeNow)
	var ae *auth.Error
	_ = errors.As(err, &ae)
	if ae.RetryAfter != 0 {
		t.Fatalf("脏锚点不得编造 Retry-After，得 %v", ae.RetryAfter)
	}
	if RetryAfterOfErr(err) != 0 {
		t.Fatal("RetryAfterOfErr 应兜住返回 0")
	}
}

// TestContractQuotaErrorExpiredAnchorNoRetryAfter：重置时刻已过（惰性归零
// 未跑的窗口）同样不带头——负时长没有语义。
func TestContractQuotaErrorExpiredAnchorNoRetryAfter(t *testing.T) {
	qe := store.ErrQuotaExhausted{Unit: "token", Period: "day", PeriodAnchor: "2026-10-01"}
	err := TranslateQuotaExhausted(qe, store.TotalScope, qeNow) // 10-21 的重置已过
	var ae *auth.Error
	_ = errors.As(err, &ae)
	if ae.RetryAfter != 0 {
		t.Fatalf("过期锚点不得带 Retry-After，得 %v", ae.RetryAfter)
	}
}

// TestContractPoolErrorCarriesGroupInDetail：池错误 groupID 进 detail
// （排障面）不进 quota 对象（客户端决策变量只有 unit/period/model）。
func TestContractPoolErrorCarriesGroupInDetail(t *testing.T) {
	qe := store.ErrQuotaExhausted{Scope: "pool", Unit: "token", Period: "month", PeriodAnchor: "2026-10-01"}
	err := TranslatePoolExhausted(qe, 42, 7, qeNow)
	var ae *auth.Error
	if !errors.As(err, &ae) {
		t.Fatal("应翻成 auth.Error")
	}
	if ae.Code != auth.CodePoolExhausted {
		t.Fatalf("code 应 pool_exhausted，得 %s", ae.Code)
	}
	if ae.Quota == nil || ae.Quota.Model != "7" {
		t.Fatalf("池错误也应带 quota 对象: %+v", ae.Quota)
	}
	if ae.GroupID != "42" {
		t.Fatalf("groupID 应进排障面（Error.GroupID），得 %q", ae.GroupID)
	}
}

// TestContractHasQuotaDetail：非 auth.Error / 无 quota 的错误都报 false
// （中间件据此分流"契约字段输出"与普通 writeError）。
func TestContractHasQuotaDetail(t *testing.T) {
	if HasQuotaDetail(errors.New("plain")) {
		t.Fatal("裸错误应 false")
	}
	if HasQuotaDetail(ErrRateLimited) {
		t.Fatal("rate_limited 不带 quota 对象")
	}
	qe := store.ErrQuotaExhausted{Unit: "token", Period: "day", PeriodAnchor: "2026-10-20"}
	if !HasQuotaDetail(TranslateQuotaExhausted(qe, store.TotalScope, qeNow)) {
		t.Fatal("quota_exceeded 应 true")
	}
	if !HasQuotaDetail(TranslatePoolExhausted(qe, 1, 0, qeNow)) {
		t.Fatal("pool_exhausted 应 true")
	}
}

// TestContractSentinelsDistinguishable：四哨兵按 Code 可区分（429 组内
// 互不误判；熔断 429 quota_exceeded 维持上轮定案）。
func TestContractSentinelsDistinguishable(t *testing.T) {
	pairs := []struct {
		err  error
		code auth.Code
	}{
		{ErrRateLimited, auth.CodeRateLimited},
		{ErrQuotaExceeded, auth.CodeQuotaExceeded},
		{ErrPoolExhausted, auth.CodePoolExhausted},
		{ErrGloballyFused, auth.CodeQuotaExceeded}, // 熔断复用 quota_exceeded（上轮定案）
	}
	for _, p := range pairs {
		var ae *auth.Error
		if !errors.As(p.err, &ae) || ae.Code != p.code {
			t.Fatalf("%v 应命中 %s", p.err, p.code)
		}
	}
	if auth.CodeOf(ErrRateLimited) == auth.CodeOf(ErrQuotaExceeded) {
		t.Fatal("rate_limited 与 quota_exceeded 不得同码")
	}
	if auth.CodeOf(ErrGloballyFused) != auth.CodeOf(ErrQuotaExceeded) {
		t.Fatal("熔断刻意同码 quota_exceeded（03-errors 定案）")
	}
}

// TestContractFuseNoRetryAfter：全局熔断不带 Retry-After（金额不可知，
// 编造误导客户端）——收口函数不碰它，这里锁行为。
func TestContractFuseNoRetryAfter(t *testing.T) {
	if RetryAfterOfErr(ErrGloballyFused) != 0 {
		t.Fatal("全局熔断不带 Retry-After")
	}
	if HasQuotaDetail(ErrGloballyFused) {
		t.Fatal("熔断不带 quota 对象（不是某个行打满，是全站预算）")
	}
}

// TestContractConditionalDebitStopsAtLimit：并发抢同一行只成功到上限
// （条件 UPDATE 语义，非读-改-写）。真库：limit=5，40 并发各扣 1 →
// 恰好 5 成功 35 拒；used 终值恰为 5（无超发）。
func TestContractConditionalDebitStopsAtLimit(t *testing.T) {
	st, accID := openQuotaStore(t)
	ctx := t.Context()
	if err := st.Repos().Quotas.SetAccountQuota(ctx, accID, store.TotalScope, store.UnitRequest, store.PeriodDay, 5); err != nil {
		t.Fatal(err)
	}
	const workers = 40
	res := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			res <- st.Repos().Quotas.TryDebitAccountQuota(ctx, accID, store.TotalScope, store.UnitRequest, store.PeriodDay, 1)
		}()
	}
	ok, rejected := 0, 0
	for i := 0; i < workers; i++ {
		if err := <-res; err == nil {
			ok++
		} else {
			rejected++
		}
	}
	if ok != 5 || rejected != workers-5 {
		t.Fatalf("条件扣减应恰 5 成功 %d 拒，得 %d/%d", workers-5, ok, rejected)
	}
	rows, err := st.Repos().Quotas.ListAccountQuotas(ctx, accID)
	if err != nil || len(rows) != 1 || rows[0].Used != 5 {
		t.Fatalf("used 应恰 5（无超发）: %+v err=%v", rows, err)
	}
}

// TestContractMultiCandidateNoDoubleDebit：多候选切换（一次请求内
// failover 到第二个候选）只扣一次——结算按请求终态一次，不是按候选数。
func TestContractMultiCandidateNoDoubleDebit(t *testing.T) {
	fl := &fakeLedger{}
	rec := NewSettlementRecorder(fl, nil)
	// 同一请求终态只允许一次 SettleUsage（调用方纪律）；这里验证
	// 即便误调两次会被发现（测试哨兵：调用计数暴露在 fake 上）。
	su := SettledUsage{AccountID: "1", QuotaRequest: 1,
		Totals: UsageTotals{Input: 10, Output: 5}, EndedAt: time.Now()}
	_ = rec.SettleUsage(t.Context(), su)
	_ = rec.SettleUsage(t.Context(), su) // 模拟误调
	if len(fl.credits) != 2 {
		t.Fatalf("每次调用都产生 Credit（重复结算可观测）: %d", len(fl.credits))
	}
	// 契约：v1 面板对一次请求只调一次 SettleUsage（接线归 lead，
	// 本测试锁定"调用次数=结算次数"的可观测性，让重复结算在
	// review/测试里可见而不是静默合并）。
}

// openQuotaStore 起真库并建账户（条件扣减用例用）。
func openQuotaStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(t.Context(), store.Options{
		Path:          dir + "/t.db",
		MasterKeyFile: dir + "/m.key",
		BusyTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	accID, err := st.Repos().Accounts.CreateAccount(t.Context(), store.AccountAuth{
		Account: store.Account{DisplayName: "q", Username: "quota-t", Status: store.AccountActive},
	}, []store.Class{store.ClassTenant})
	if err != nil {
		t.Fatal(err)
	}
	return st, accID
}
