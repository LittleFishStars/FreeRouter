package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// 以下用例全部带 TestContract 前缀（make contract 只跑这些）。
// 依赖 mw_contract_test.go 的 m5Harness 脚手架。

// 1. 合法 Key 穿透全链：context 注入 account/acting_class/key_id（C4 审计面）。
func TestContractKeyAuthInjectsPrincipal(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-1", auth.StatusActive, auth.ClassTenant)
	key := h.putKey(t, "key-1", "acc-1", auth.KeyActive)

	rec := h.do(t, key)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("合法 Key 应穿透全链（418 探针），得 %d", rec.Code)
	}
	p := KeyPrincipalFromContext(h.gotCtx)
	if p == nil {
		t.Fatal("context 缺少鉴权主体")
	}
	if p.AccountID != "acc-1" || p.KeyID != "key-1" || p.ActingClass != auth.ClassTenant {
		t.Fatalf("主体三元组错误: %+v", p)
	}
}

// 2. Key 格式错误 401 invalid_api_key（格式错不浪费查表）。
func TestContractKeyAuthBadFormatIs401(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-1", auth.StatusActive, auth.ClassTenant)

	for _, bad := range []string{"", "not-a-key", "fr-short", "fr-" + "九九九九"} {
		rec := h.do(t, bad)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("格式 %q 应 401，得 %d", bad, rec.Code)
		}
		e := decodeError(t, rec)
		if e["code"] != string(auth.CodeInvalidAPIKey) {
			t.Fatalf("code 应 invalid_api_key，得 %v", e["code"])
		}
	}
}

// 3. 查无此 Key / 状态不可用（disabled/revoked）全部同码 401（防枚举）。
func TestContractKeyAuthUnknownAndDisabledSame401(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-1", auth.StatusActive, auth.ClassTenant)
	disabled := h.putKey(t, "key-d", "acc-1", auth.KeyDisabled)

	rec := h.do(t, disabled)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled Key 应 401，得 %d", rec.Code)
	}
	recUnknown := h.do(t, "fr-0000000000000000000000000000000")
	if recUnknown.Code != http.StatusUnauthorized {
		t.Fatalf("未知 Key 应 401，得 %d", recUnknown.Code)
	}
	// 同码同文案：探测者无法区分"存在但停用"与"不存在"。
	e1, e2 := decodeError(t, rec), decodeError(t, recUnknown)
	if e1["code"] != e2["code"] || e1["message"] != e2["message"] {
		t.Fatalf("防枚举破坏：%v vs %v", e1, e2)
	}
}

// 4. 账户禁用即时 401（05-keys §2 联动：名下全部 Key 立即失效），同码不泄露原因。
func TestContractKeyAuthDisabledAccountIsInstant401(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-9", auth.StatusActive, auth.ClassTenant)
	key := h.putKey(t, "key-9", "acc-9", auth.KeyActive)

	// 先证明可用，再禁用账户（同一把 Key）——联动是**实时**的。
	if rec := h.do(t, key); rec.Code != http.StatusTeapot {
		t.Fatalf("前置：禁用前应可用，得 %d", rec.Code)
	}
	h.putAccount("acc-9", auth.StatusDisabled, auth.ClassTenant)
	rec := h.do(t, key)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("账户禁用后 Key 应即时 401，得 %d", rec.Code)
	}
	if e := decodeError(t, rec); e["code"] != string(auth.CodeInvalidAPIKey) {
		t.Fatalf("账户禁用同码 invalid_api_key（防枚举），得 %v", e["code"])
	}
}

// 5. 限速 429：RPM 到顶 → rate_limited + Retry-After 头（§5.2 必带）。
func TestContractRateLimitReturns429WithRetryAfter(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-r", auth.StatusActive, auth.ClassTenant)
	key := h.putKey(t, "key-r", "acc-r", auth.KeyActive)
	h.limitsOf = func(_ context.Context, _ string) (meter.AccountLimits, bool) {
		return meter.AccountLimits{RPM: 2, TPM: -1, MaxConcurrency: -1}, true
	}

	// 第 3 发必须 429（第 1、2 发已过配额闸与并发闸）。
	for i := 0; i < 2; i++ {
		if rec := h.do(t, key); rec.Code != http.StatusTeapot {
			t.Fatalf("第 %d 发应放行，得 %d", i+1, rec.Code)
		}
	}
	rec := h.do(t, key)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("RPM 到顶应 429，得 %d", rec.Code)
	}
	e := decodeError(t, rec)
	if e["code"] != string(auth.CodeRateLimited) {
		t.Fatalf("code 应 rate_limited，得 %v", e["code"])
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("rate_limited 必须带 Retry-After 头")
	}
}

// 6. tpm=-1 不限（用户定案）：token 闸永不拒。
func TestContractRateTPMUnlimitedNeverRejects(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-t", auth.StatusActive, auth.ClassTenant)
	key := h.putKey(t, "key-t", "acc-t", auth.KeyActive)
	h.limitsOf = func(_ context.Context, _ string) (meter.AccountLimits, bool) {
		return meter.AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: -1}, true
	}
	for i := 0; i < 20; i++ {
		if rec := h.do(t, key); rec.Code != http.StatusTeapot {
			t.Fatalf("tpm=-1 第 %d 发不应被拒，得 %d", i+1, rec.Code)
		}
	}
	// 对照：RPM=0 是全拒（0 与 -1 相反方向）。
	h.limitsOf = func(_ context.Context, _ string) (meter.AccountLimits, bool) {
		return meter.AccountLimits{RPM: 0, TPM: -1, MaxConcurrency: -1}, true
	}
	rec := h.do(t, key)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rpm=0 应全拒 429，得 %d", rec.Code)
	}
}

// 7. 配额 429 quota_exceeded：错误体带 quota{unit,period,model} 与 Retry-After 头。
func TestContractQuotaExceededCarriesQuotaObject(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-q", auth.StatusActive, auth.ClassTenant)
	key := h.putKey(t, "key-q", "acc-q", auth.KeyActive)
	h.modelOf = func(_ context.Context, _ string) (string, bool) { return "7", true }
	h.debiter.script = func(dc meter.DebitContext) error {
		return meter.ErrQuotaExceeded.
			WithQuota("token", "day", fmt.Sprintf("%s", dc.AbstractModelID)).
			WithRetryAfter(90 * time.Minute)
	}

	rec := h.do(t, key)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("配额应 429，得 %d", rec.Code)
	}
	e := decodeError(t, rec)
	if e["code"] != string(auth.CodeQuotaExceeded) {
		t.Fatalf("code 应 quota_exceeded，得 %v", e["code"])
	}
	// quota 对象：unit/period/model 三键（§6 双通道定案）。
	q, ok := e["quota"].(map[string]any)
	if !ok {
		t.Fatalf("错误体缺 quota 对象: %v", e)
	}
	if q["unit"] != "token" || q["period"] != "day" || q["model"] != "7" {
		t.Fatalf("quota 对象错误: %v", q)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("quota_exceeded 必须带 Retry-After 头（指向命中行重置时刻）")
	}
	// 扣减参数确实带了模型 id（按模型行才进错误体）。
	if len(h.debiter.calls) == 0 || h.debiter.calls[0].AbstractModelID != "7" {
		t.Fatalf("TryDebit 应收到 mid=7: %+v", h.debiter.calls)
	}
}

// 8. daily=0 全拒（0 语义定案）：零额度连 cost=0 都拒。
func TestContractQuotaZeroLimitRejectsEverything(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-z", auth.StatusActive, auth.ClassTenant)
	key := h.putKey(t, "key-z", "acc-z", auth.KeyActive)
	h.debiter.script = func(_ meter.DebitContext) error {
		return meter.ErrQuotaExceeded.WithQuota("request", "day", "")
	}
	rec := h.do(t, key)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("零额度应 429，得 %d", rec.Code)
	}
	e := decodeError(t, rec)
	if e["code"] != string(auth.CodeQuotaExceeded) {
		t.Fatalf("code 应 quota_exceeded，得 %v", e["code"])
	}
	q, ok := e["quota"].(map[string]any)
	if !ok {
		t.Fatalf("合计行也应带 quota 对象（unit/period），得 %v", e)
	}
	if _, has := q["model"]; has {
		// 合计行 model 缺省：错误体应省略 model 字段（§6：合计行无 model）。
		t.Fatalf("合计行 quota.model 应缺省，得 %v", q)
	}
	if q["unit"] != "request" || q["period"] != "day" {
		t.Fatalf("quota 的 unit/period 错误: %v", q)
	}
}

// 9. 全局闸 503 overloaded（bc219ba 拆码）：与账户 429 不得串。
func TestContractGlobalGate503DistinctFrom429(t *testing.T) {
	h := newM5Harness(t)
	h.putAccount("acc-g", auth.StatusActive, auth.ClassTenant)
	key := h.putKey(t, "key-g", "acc-g", auth.KeyActive)

	// 占满全局在途（GlobalConcurrency=1），下一发排队超时 → 503。
	h.limiter = meter.NewLimiter(meter.Config{
		QueueTimeout:      60 * time.Millisecond,
		GlobalConcurrency: 1,
	}).WithClock(func() time.Time { return h.now })
	h.chain = h.build(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.gotCtx = nil
		w.WriteHeader(http.StatusTeapot)
	}))
	// 手工占一个全局槽（走 limiter 自己的 Acquire，锁内计数）。
	rel, err := h.limiter.AcquireConcurrency(t.Context(), "acc-holder",
		meter.AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer rel()

	rec := h.do(t, key)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("全局闸排队超时应 503，得 %d", rec.Code)
	}
	e := decodeError(t, rec)
	if e["code"] != string(auth.CodeOverloaded) {
		t.Fatalf("code 应 overloaded，得 %v", e["code"])
	}
	if e["type"] != "service_unavailable_error" {
		t.Fatalf("type 应 service_unavailable_error，得 %v", e["type"])
	}
}

// 10. adapter 对接 store 真实扣减（meter.StoreAdapter ↔ store.QuotaRepo）。
// 起真库（内存目录），SetAccountQuota 造行，TryDebit 打满 → 翻译后的 429
// 错误携带行的 unit/period 与可算的 Retry-After。
func TestContractAdapterDebitsRealStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), store.Options{
		Path:          filepath.Join(dir, "freerouter.db"),
		MasterKeyFile: filepath.Join(dir, "master.key"),
		BusyTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close 失败: %v", err)
		}
	}()

	ctx := context.Background()
	accID, err := st.Repos().Accounts.CreateAccount(ctx, store.AccountAuth{Account: store.Account{DisplayName: "适配器测试", Username: "adapter-acct", Status: store.AccountActive}}, []store.Class{store.ClassTenant})
	if err != nil {
		t.Fatal(err)
	}
	// request/day 上限 2（合计行）。
	if err := st.Repos().Quotas.SetAccountQuota(ctx, accID, store.TotalScope, store.UnitRequest, store.PeriodDay, 2); err != nil {
		t.Fatal(err)
	}

	adapt := meter.NewStoreAdapter(st.Repos().Quotas, fakeGroupReader{})
	dc := meter.DebitContext{
		AccountID:       fmt.Sprintf("%s", accID),
		AbstractModelID: store.TotalScope,
		Cost:            meter.UsageCost{Request: 1},
	}
	// 前两发扣成功。
	for i := 0; i < 2; i++ {
		if err := adapt.TryDebit(ctx, dc); err != nil {
			t.Fatalf("第 %d 发应扣成功: %v", i+1, err)
		}
	}
	// 第三发：429 quota_exceeded + quota 对象 + Retry-After 指向重置时刻。
	err = adapt.TryDebit(ctx, dc)
	if err == nil {
		t.Fatal("超限应拒")
	}
	var ae *auth.Error
	if !asAuthError(err, &ae) {
		t.Fatalf("adapter 应翻成 auth.Error: %v", err)
	}
	if ae.Code != auth.CodeQuotaExceeded {
		t.Fatalf("code 应 quota_exceeded，得 %s", ae.Code)
	}
	if ae.Quota == nil || ae.Quota.Unit != "request" || ae.Quota.Period != "day" || ae.Quota.Model != "" {
		t.Fatalf("quota 四元组错误: %+v", ae.Quota)
	}
	if ae.RetryAfter <= 0 {
		t.Fatalf("Retry-After 应指向命中行重置时刻，得 %v", ae.RetryAfter)
	}
	// 读路径对齐：AccountQuotaRows 回来的是行化快照。
	rows, err := adapt.AccountQuotaRows(ctx, fmt.Sprintf("%s", accID))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Total || rows[0].Limit != 2 || rows[0].Used != 2 {
		t.Fatalf("行快照错误: %+v", rows)
	}
}

// fakeGroupReader：无组（GroupPools 返回空）——池路径的零值场景。
type fakeGroupReader struct{}

func (fakeGroupReader) GroupIDsOfAccount(context.Context, string) ([]string, error) {
	return nil, nil
}

var _ = context.Background
