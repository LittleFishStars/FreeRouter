package meter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// Contract 测试：meter 的对外语义（纯判定面 + 进程内闸门）。
// 每条断言对应 docs 的一个定案；0 语义表是评审点名的复查重点。

var base = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func fixedClock(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

func movingClock(start time.Time) (func() time.Time, func(time.Duration)) {
	now := start
	return func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }
}

func errCode(t *testing.T, err error, want auth.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望 %s，得到 nil", want)
	}
	if got := auth.CodeOf(err); got != want {
		t.Fatalf("错误码 = %s，期望 %s（%v）", got, want, err)
	}
}

// ---------------------------------------------------------------------------
// 1. 额度域哨兵表：无行=不限（调用方跳过）、0=全拒、-1=装配错误
// ---------------------------------------------------------------------------

func TestContractQuotaRowSentinelTable(t *testing.T) {
	day := PeriodDay
	now := base
	cases := []struct {
		name    string
		row     QuotaRow
		cost    int64
		wantErr auth.Code // "" = 放行
		// 断言面：拒绝时四元组与 Retry-After 是否指向重置时刻。
	}{
		{"limit=0 且 used=0：零额度全拒（默认拒绝）", QuotaRow{Total: true, Unit: "token", Period: day, Limit: 0, Used: 0}, 1, auth.CodeQuotaExceeded},
		{"limit=0 连 cost=0 的探测也拒（额度 0 不打无效请求）", QuotaRow{Total: true, Unit: "request", Period: day, Limit: 0}, 0, auth.CodeQuotaExceeded},
		{"恰好等于上限放行", QuotaRow{Total: true, Unit: "token", Period: day, Limit: 100, Used: 60}, 40, ""},
		{"超 1 个即拒", QuotaRow{Total: true, Unit: "token", Period: day, Limit: 100, Used: 60}, 41, auth.CodeQuotaExceeded},
		{"负 limit 是速率域哨兵流入：internal_error 而非猜意图", QuotaRow{Total: true, Unit: "token", Period: day, Limit: -1, Used: 0}, 1, auth.CodeInternal},
		{"按模型行：命中拒（带 model 供换模型降级）", QuotaRow{ModelID: "smart", Unit: "request", Period: day, Limit: 50, Used: 50}, 1, auth.CodeQuotaExceeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ra, err := CheckQuotaRow(c.row, c.cost, now)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("应放行: %v", err)
				}
				return
			}
			errCode(t, err, c.wantErr)
			if c.wantErr == auth.CodeQuotaExceeded {
				if ra <= 0 {
					t.Fatalf("quota_exceeded 必须带重置时刻的 Retry-After，得 %v", ra)
				}
				var ae *auth.Error
				if !errors.As(err, &ae) || ae.Quota == nil {
					t.Fatal("错误必须携带 quota 四元组（§6 双通道）")
				}
				if ae.Quota.Unit != c.row.Unit || ae.Quota.Period != string(c.row.Period) || ae.Quota.Model != c.row.ModelID {
					t.Fatalf("quota 对象 = %+v，期望行 %+v", ae.Quota, c.row)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. Q1 判定形状：合计行 + 命中模型行双闸，其他模型行与无行都跳过
// ---------------------------------------------------------------------------

func TestContractPrecheckShape(t *testing.T) {
	now := base
	rows := []QuotaRow{
		{Total: true, Unit: "request", Period: PeriodDay, Limit: 100, Used: 10},
		{ModelID: "smart", Unit: "request", Period: PeriodDay, Limit: 50, Used: 50},
		{ModelID: "fast", Unit: "request", Period: PeriodDay, Limit: 50, Used: 0},
	}
	cost := UsageCost{Request: 1, Token: 10, Credit: 2}

	// 调 smart：合计行过、smart 行拒 → 错误带 model=smart（客户端可换 fast）。
	_, err := PrecheckQuota(rows, "smart", cost, now)
	errCode(t, err, auth.CodeQuotaExceeded)
	var ae *auth.Error
	if !errors.As(err, &ae) || ae.Quota == nil || ae.Quota.Model != "smart" {
		t.Fatalf("按模型行拒绝必须指名 model: %v", err)
	}
	// 调 fast：两道闸都过。
	if _, err := PrecheckQuota(rows, "fast", cost, now); err != nil {
		t.Fatalf("fast 应放行: %v", err)
	}
	// 空 rows = 全部维度无行 = 不限（"无行=不限"由遍历表达）。
	if _, err := PrecheckQuota(nil, "any", cost, now); err != nil {
		t.Fatalf("无行应全放行: %v", err)
	}
	// 未知计量维度是装配错误，不放行也不误拒成额度问题。
	_, err = PrecheckQuota([]QuotaRow{{Total: true, Unit: "byte", Period: PeriodDay, Limit: 10}}, "x", cost, now)
	errCode(t, err, auth.CodeInternal)
}

// ---------------------------------------------------------------------------
// 3. 周期锚点与重置时刻（day/month 的 UTC 边界）
// ---------------------------------------------------------------------------

func TestContractPeriodAnchorAndReset(t *testing.T) {
	// 锚点：UTC 口径。23:59 与 00:01 属于**不同**锚点。
	d := PeriodDay
	if a1, a2 := d.Anchor(base), d.Anchor(base.Add(12*time.Hour)); a1 == a2 {
		t.Fatal("day 锚点必须跨 UTC 日切换")
	}
	m := PeriodMonth
	if got := m.Anchor(base); got != "2026-10-01" { // 与 store periodAnchor 同口径（当月 1 日全格式）
		t.Fatalf("month 锚点 = %s", got)
	}
	// ResetAt：日重置 = 次日 UTC 00:00。
	if got := PeriodDay.ResetAt(base).Sub(base); got != 12*time.Hour {
		t.Fatalf("day ResetAt 距离 = %v，期望 12h（12:00 UTC 起）", got)
	}
	// 月重置：10 月 20 日 12:00 → 11 月 1 日 00:00 UTC = 11.5 天。
	if got := PeriodMonth.ResetAt(base).Sub(base); got != 276*time.Hour {
		t.Fatalf("month ResetAt 距离 = %v", got)
	}
	// 周期不合法时 ResetAt 返回保守值而不是 panic（映射层可能拿到脏 period）。
	if got := Period("fortnight").ResetAt(base).Sub(base); got <= 0 {
		t.Fatal("未知周期必须给正值")
	}
}

// ---------------------------------------------------------------------------
// 4. 速率域：-1 不限 / 0 全拒 / 窗口滚动 / 判定即递增
// ---------------------------------------------------------------------------

func TestContractRateSentinelsAndWindow(t *testing.T) {
	clk, advance := movingClock(base)
	l := NewLimiter(Config{}).WithClock(clk)

	// -1 不限：一分钟内随便打。
	unlimited := AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: -1}
	for i := 0; i < 500; i++ {
		if _, err := l.Allow("acc-u", unlimited, 1000); err != nil {
			t.Fatalf("RPM/TPM=-1 应不限: %v", err)
		}
	}
	// 0 全拒：第一枪就拒，且带 rate_limited。
	zero := AccountLimits{RPM: 0, TPM: -1, MaxConcurrency: 1}
	ra, err := l.Allow("acc-z", zero, 1)
	errCode(t, err, auth.CodeRateLimited)
	if ra <= 0 {
		t.Fatal("rate_limited 必有 Retry-After")
	}
	// 恰好等于上限 / 超 1 个。
	lim := AccountLimits{RPM: 3, TPM: 100, MaxConcurrency: 1}
	for i := 0; i < 3; i++ {
		if _, err := l.Allow("acc-a", lim, 30); err != nil {
			t.Fatalf("第 %d 个请求应放行: %v", i+1, err)
		}
	}
	if _, err := l.Allow("acc-a", lim, 1); err == nil {
		t.Fatal("RPM 第 4 个必须拒")
	}
	// TPM 独立成闸：RPM 未过但 token 超。
	if _, err := l.Allow("acc-b", lim, 101); err == nil {
		t.Fatal("TPM 超限必须拒")
	}
	// 窗口滚动：整分边界后计数清零（不 sleep，时钟推进）。
	advance(time.Minute)
	if _, err := l.Allow("acc-a", lim, 30); err != nil {
		t.Fatalf("跨分钟窗口应重置: %v", err)
	}
	// 账户隔离：acc-a 的窗口不影响 acc-b 的请求维。
	if _, err := l.Allow("acc-b", lim, 1); err != nil {
		t.Fatalf("acc-b 请求维应独立: %v", err)
	}
}

func TestContractGlobalRPMZeroMeansUnlimited(t *testing.T) {
	clk, _ := movingClock(base)
	// 全局域口径与账户速率域**不同**：这里 0 = 不限制（config.limits 口径）。
	l := NewLimiter(Config{GlobalRPM: 0}).WithClock(clk)
	lim := AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: -1}
	for i := 0; i < 2000; i++ {
		if _, err := l.Allow("acc-x", lim, 1); err != nil {
			t.Fatalf("GlobalRPM=0 应不限制: %v", err)
		}
	}
	// 配了 1000 后第 1001 个拒且归因到全局。
	l2 := NewLimiter(Config{GlobalRPM: 1000}).WithClock(clk)
	var lastErr error
	for i := 0; i < 1001; i++ {
		_, lastErr = l2.Allow("acc-x", lim, 1)
	}
	errCode(t, lastErr, auth.CodeRateLimited)
	var ae *auth.Error
	if errors.As(lastErr, &ae) && ae.Dimension != "rpm(global)" {
		t.Fatalf("全局拒的 Dimension = %q，期望 rpm(global)", ae.Dimension)
	}
}

// ---------------------------------------------------------------------------
// 5. 并发闸门：上限计数 / 排队超时 / 取消归还 / release 幂等
// ---------------------------------------------------------------------------

func TestConcurrencyAcquireReleaseAndQueue(t *testing.T) {
	clk, advance := movingClock(base)
	l := NewLimiter(Config{QueueTimeout: 5 * time.Second, GlobalConcurrency: -1}).WithClock(clk)
	lim := AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: 2}
	ctx := context.Background()

	r1, err := l.AcquireConcurrency(ctx, "acc-c", lim)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := l.AcquireConcurrency(ctx, "acc-c", lim)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		r3, err := l.AcquireConcurrency(ctx, "acc-c", lim)
		if err == nil {
			r3()
		}
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // 给等待者进入排队（此处 sleep 是测等待态，非测窗口）
	// 释放一个，等待者应在广播后拿到槽位。
	r1()
	if err := <-done; err != nil {
		t.Fatalf("释放后排队者应成功: %v", err)
	}
	// 排队超时：占满两个 + 时钟推过 QueueTimeout。
	r2()
	rA, err := l.AcquireConcurrency(ctx, "acc-c", lim)
	if err != nil {
		t.Fatal(err)
	}
	rB, err := l.AcquireConcurrency(ctx, "acc-c", lim)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rA(); rB() })
	_, err = l.AcquireConcurrency(ctx, "acc-c", lim) // 满了
	errCode(t, err, auth.CodeRateLimited)
	if ra := auth.RetryAfterOf(err); ra <= 0 {
		t.Fatal("排队超时也必须带 Retry-After")
	}
	_ = advance // 窗口由注入时钟控制，不依赖真实超时

	// ctx 取消必须立刻返回并且**归还已占的账户槽位**。
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = l.AcquireConcurrency(cctx, "acc-c", lim)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应原样上抛: %v", err)
	}
	// release 幂等（sync.Once）。
	rA()
	rA()
}

func TestConcurrencyZeroRejectsWithoutQueueing(t *testing.T) {
	clk, _ := movingClock(base)
	l := NewLimiter(Config{QueueTimeout: time.Hour}).WithClock(clk)
	start := time.Now()
	_, err := l.AcquireConcurrency(context.Background(), "acc-0", AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: 0})
	errCode(t, err, auth.CodeRateLimited)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("MaxConcurrency=0 是「全拒」不是「排队一小时」，耗时 %v", d)
	}
}

// ---------------------------------------------------------------------------
// 6. 共享池：ALL_AND 逐个满足 / 先全判后全扣
// ---------------------------------------------------------------------------

func TestContractPoolsAllAndAndAtomicCharge(t *testing.T) {
	g := NewLedger().WithClock(fixedClock(base))
	pools := []auth.PoolRef{
		{GroupID: "ga", Unit: "token", Period: "day", Limit: 1000, Used: 900},
		{GroupID: "gb", Unit: "credit", Period: "month", Limit: 100, Used: 50},
	}
	// 先把 ga 池的账本用量播种到 900（合计扣减）。
	if _, err := g.ChargePools(pools[:1], 900); err != nil {
		t.Fatalf("播种 ga=900 失败: %v", err)
	}
	// cost=200：ga 拒（900+200>1000）→ **gb 一分不扣**（先全判后全扣）。
	denied, err := g.ChargePools(pools, 200)
	errCode(t, err, auth.CodePoolExhausted)
	if denied == nil || denied.GroupID != "ga" {
		t.Fatalf("必须归因到先拒的池 ga: %v", denied)
	}
	if g.PoolUsed("gb", "", "credit", "month") != 0 {
		t.Fatal("B 池被 A 池拖累时必须零写入（免回滚）")
	}
	// cost=50 全过（ga 900+50≤1000、gb 0+50≤100）。
	if _, err := g.ChargePools(pools, 50); err != nil {
		t.Fatalf("均应扣减: %v", err)
	}
	if g.PoolUsed("ga", "", "token", "day") != 950 || g.PoolUsed("gb", "", "credit", "month") != 50 {
		t.Fatalf("ga=950 gb=50，实得 %d / %d", g.PoolUsed("ga", "", "token", "day"), g.PoolUsed("gb", "", "credit", "month"))
	}
	// §4.5 硬规则：同一模型上"合计池"与"模型级池"是两道独立闸（交集）。
	// 模型池先拒也要归因到它，且 quota.model 指名模型（客户端 pool 项 model:null 判合计）。
	mid := []auth.PoolRef{
		{GroupID: "ga", AbstractModelID: "", Unit: "token", Period: "day", Limit: 100000},
		{GroupID: "ga", AbstractModelID: tidA, Unit: "token", Period: "day", Limit: 100},
	}
	denied, err = g.ChargePools(mid, 150)
	errCode(t, err, auth.CodePoolExhausted)
	if denied == nil || denied.AbstractModelID != tidA {
		t.Fatalf("必须归因到模型池 42: %+v", denied)
	}
	var pae *auth.Error
	if !errors.As(err, &pae) || pae.Quota == nil || pae.Quota.Model != tidA {
		t.Fatalf("模型池错误必须带 quota.model=42: %+v", pae.Quota)
	}
	// ga 合计池此前已被扣到 950；模型池这次拒绝后它必须**不再增长**。
	if g.PoolUsed("ga", "", "token", "day") != 950 {
		t.Fatalf("合计池在模型池拒绝时必须零写入（保持 950），实得 %d", g.PoolUsed("ga", "", "token", "day"))
	}

	// 纯函数面：CheckPools 对非法上限（<=0）报 internal_error——"不设池=删行"，
	// 有行必正整数（03-groups.md §4）。
	_, err = CheckPools([]auth.PoolRef{{GroupID: "g", Unit: "token", Period: "day", Limit: 0}}, 1)
	errCode(t, err, auth.CodeInternal)
}

// ---------------------------------------------------------------------------
// 7. 全局日预算熔断：0=不熔断 / 终态累加 / 解除前一直拒
// ---------------------------------------------------------------------------

// TestContractConcurrencyGlobalGateIs503 锁定 lead 仲裁①（2026-10-20）：
// 两层闸两个码——账户槽位满 → 429 rate_limited（"你越限"）；
// 进程 global_concurrency 排队超时 → 503 overloaded（"服务过载"）。
// 客户端动作不同（找管理员 vs 退避重试）必须可区分（D11 三态原则）。
func TestContractConcurrencyGlobalGateIs503(t *testing.T) {
	clk, _ := movingClock(base)
	l := NewLimiter(Config{QueueTimeout: 80 * time.Millisecond, GlobalConcurrency: 1}).WithClock(clk)
	lim := AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: -1} // 账户层不限
	ctx := context.Background()

	r1, err := l.AcquireConcurrency(ctx, "acc-g1", lim)
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	// 另一账户来抢全局槽位：账户层放行、全局层排队 → 超时 503。
	_, err = l.AcquireConcurrency(ctx, "acc-g2", lim)
	errCode(t, err, auth.CodeOverloaded)
	if !errors.Is(err, auth.ErrOverloaded) {
		t.Fatalf("全局闸超时必须命中 ErrOverloaded 哨兵: %v", err)
	}
	if ra := auth.RetryAfterOf(err); ra <= 0 {
		t.Fatal("503 overloaded 同样必须带 Retry-After（退避重试是正解）")
	}
	// 对照：账户层满仍是 429，不得串码。
	l2 := NewLimiter(Config{QueueTimeout: 80 * time.Millisecond, GlobalConcurrency: -1}).WithClock(clk)
	lim2 := AccountLimits{RPM: -1, TPM: -1, MaxConcurrency: 1}
	rel, err := l2.AcquireConcurrency(ctx, "acc-a1", lim2)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	_, err = l2.AcquireConcurrency(ctx, "acc-a1", lim2)
	errCode(t, err, auth.CodeRateLimited)
	if errors.Is(err, auth.ErrOverloaded) {
		t.Fatal("账户级并发超限不得误报 503")
	}
}

func TestContractGlobalFuse(t *testing.T) {
	clk, advance := movingClock(base)
	l := NewLimiter(Config{GlobalDailyTokens: 1000}).WithClock(clk)

	if err := l.CheckGlobalFuse(); err != nil {
		t.Fatalf("用量未到时不应熔断: %v", err)
	}
	if fused := l.RecordGlobalTokens(600); fused {
		t.Fatal("600 < 1000 不应报熔断")
	}
	if !l.RecordGlobalTokens(400) {
		t.Fatal("达到 1000 应报熔断")
	}
	err := l.CheckGlobalFuse()
	errCode(t, err, auth.CodeQuotaExceeded)
	if auth.RetryAfterOf(err) != 0 {
		t.Fatal("全局熔断**不得**带编造的 Retry-After（解除依赖人工）")
	}
	// UTC 跨日：熔断状态随锚点清零。
	advance(12*time.Hour + time.Minute)
	if err := l.CheckGlobalFuse(); err != nil {
		t.Fatalf("跨日后应清零: %v", err)
	}
	// 0 = 不熔断（全局域口径）。
	l0 := NewLimiter(Config{GlobalDailyTokens: 0}).WithClock(fixedClock(base))
	l0.RecordGlobalTokens(1 << 50)
	if err := l0.CheckGlobalFuse(); err != nil {
		t.Fatalf("GlobalDailyTokens=0 必须不熔断: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 8. 判定+递增同锁：并发下不双花
// ---------------------------------------------------------------------------

func TestContractAllowNoDoubleSpend(t *testing.T) {
	clk, _ := movingClock(base)
	l := NewLimiter(Config{}).WithClock(clk)
	lim := AccountLimits{RPM: 100, TPM: -1, MaxConcurrency: -1}

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := l.Allow("acc-hot", lim, 1); err == nil {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 100 {
		t.Fatalf("RPM=100 并发 400 应恰好放行 100，实得 %d（双花或过杀）", allowed)
	}
}

// ---------------------------------------------------------------------------
// 9. PrecheckFromStore：nil 降级 + 错误透传（装配单点）
// ---------------------------------------------------------------------------

type fakeQuotaStore struct {
	rows []QuotaRow
	err  error
}

func (f fakeQuotaStore) AccountQuotaRows(context.Context, string) ([]QuotaRow, error) {
	return f.rows, f.err
}
func (f fakeQuotaStore) GroupPools(context.Context, string) ([]auth.PoolRef, error) {
	return nil, nil
}

func TestContractPrecheckFromStore(t *testing.T) {
	ctx := context.Background()
	cost := UsageCost{Request: 1}
	// store=nil：跳过额度闸（单机降级路径）。
	if _, err := PrecheckFromStore(ctx, nil, "acc", "fast", cost, base); err != nil {
		t.Fatalf("nil store 应跳过: %v", err)
	}
	// 有行按行判。
	st := fakeQuotaStore{rows: []QuotaRow{{Total: true, Unit: "request", Period: PeriodDay, Limit: 0}}}
	_, err := PrecheckFromStore(ctx, st, "acc", "fast", cost, base)
	errCode(t, err, auth.CodeQuotaExceeded)
	// store 错误原样透传（不许伪装成额度问题）。
	sentinel := errors.New("db 抖动")
	_, err = PrecheckFromStore(ctx, fakeQuotaStore{err: sentinel}, "acc", "fast", cost, base)
	if !errors.Is(err, sentinel) {
		t.Fatalf("存储错误必须透传: %v", err)
	}
}
