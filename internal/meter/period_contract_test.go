package meter

import (
	"testing"
	"time"
)

// period.go 的契约测试：meter 的锚点与重置时刻必须与 store 的
// periodAnchor / ErrQuotaExhausted.ResetAt **同口径**（只读不另算）。
// store 的口径（repo_quota.go periodAnchor）：
//   - day:  UTC "2006-01-02"
//   - month: 当月 1 日的 "2006-01-02"
// ResetAt 的口径（repo_quota.go ResetAt 方法）：锚点 + 粒度（day +1d，
// month +1 月）——承诺与惰性归零机制同源。

// TestContractPeriodAnchorMatchesStore 用 store 的公式逐例对照
// meter.Period.Anchor（公式复制经得起 diff 审查；两边漂移会在
// store 的惰性重置与 meter 的预检之间撕开"半行旧账"）。
func TestContractPeriodAnchorMatchesStore(t *testing.T) {
	cases := []struct {
		now time.Time
		day string
		mon string
	}{
		{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), "2026-10-20", "2026-10-01"},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "2026-01-01", "2026-01-01"},
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), "2026-12-31", "2026-12-01"},
		// 非 UTC 时区输入：锚点必须按 UTC 折算（store 的 periodAnchor
		// 吃 UTC 时刻；meter 的 Anchor 内部 .UTC()——同一时刻两种时区
		// 包装必须给出同一锚点）。
		{time.Date(2026, 10, 21, 7, 30, 0, 0, time.FixedZone("CST", 8*3600)), "2026-10-20", "2026-10-01"},
	}
	for _, c := range cases {
		if got := PeriodDay.Anchor(c.now); got != c.day {
			t.Fatalf("day 锚点 = %s，期望 %s（now=%v）", got, c.day, c.now)
		}
		if got := PeriodMonth.Anchor(c.now); got != c.mon {
			t.Fatalf("month 锚点 = %s，期望 %s（now=%v）", got, c.mon, c.now)
		}
	}
}

// TestContractPeriodResetMatchesStore 对 store.ResetAt 的公式：
// anchor + AddDate(0,0,1)（day）/ anchor.AddDate(0,1,0)（month）。
// store 解析锚点是 UTC（time.Parse 默认 UTC），meter 的 ResetAt 也必须
// 落在 UTC 语义——本地时区的"次日 0 点"会让 Retry-After 差出时区偏移。
func TestContractPeriodResetMatchesStore(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	dayReset := PeriodDay.ResetAt(now)
	// store: anchor="2026-10-20" → Parse → AddDate(0,0,1) = 2026-10-21 00:00 UTC。
	want := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	if !dayReset.Equal(want) {
		t.Fatalf("day ResetAt = %v，期望 %v", dayReset, want)
	}

	monReset := PeriodMonth.ResetAt(now)
	wantM := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !monReset.Equal(wantM) {
		t.Fatalf("month ResetAt = %v，期望 %v", monReset, wantM)
	}

	// 月末边界：1 月 31 日的月重置 = 2 月 1 日（AddDate(0,1,0) 归一化）。
	eom := time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC)
	if got := PeriodMonth.ResetAt(eom); !got.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("1 月末月重置 = %v", got)
	}
	// 闰年：2 月 29 日的月重置 = 3 月 1 日。
	leap := time.Date(2024, 2, 29, 10, 0, 0, 0, time.UTC)
	if got := PeriodMonth.ResetAt(leap); !got.Equal(time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("闰 2 月重置 = %v", got)
	}
}

// TestContractPeriodResetAfterAnchor 用"取锚点 → 从锚点推重置"的组合
// 验证承诺与归零机制同源（store 的 Retry-After = ResetAt - now；
// 若 meter 另算一套"下一个自然日"，跨时区/月末就会与 store 的
// anchor+粒度错位）。
func TestContractPeriodResetAfterAnchor(t *testing.T) {
	for _, now := range []time.Time{
		time.Date(2026, 3, 5, 0, 0, 1, 0, time.UTC),
		time.Date(2026, 3, 5, 23, 59, 59, 0, time.UTC),
	} {
		anchor := PeriodDay.Anchor(now)
		// store 侧的同源公式：Parse(anchor) + 1d。
		want, err := time.Parse("2006-01-02", anchor)
		if err != nil {
			t.Fatal(err)
		}
		want = want.AddDate(0, 0, 1)
		if got := PeriodDay.ResetAt(now); !got.Equal(want) {
			t.Fatalf("ResetAt(%v) = %v，期望锚点+1日 %v", now, got, want)
		}
	}
}

// TestContractPeriodWeekDocumentedIsolation：week 常量已在 lead 侧定案
// （DDL CHECK 只有 day/month，组策略不产生 week；锚点 W 前缀是显式
// 挂靠点）。这里只测它不污染 day/month 的口径（换错周期会静默错账）。
func TestContractPeriodWeekDocumentedIsolation(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if PeriodWeek.Anchor(now) == PeriodDay.Anchor(now) {
		t.Fatal("week 与 day 锚点不得相同（错用周期会静默错账）")
	}
	if !PeriodWeek.Valid() || !PeriodDay.Valid() || !PeriodMonth.Valid() {
		t.Fatal("三个周期都应 Valid（week 的有效性是常量自洽）")
	}
	if Period("fortnight").Valid() {
		t.Fatal("未知周期不得 Valid")
	}
}
