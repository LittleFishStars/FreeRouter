package app

// checkin_test.go —— 定时签到的纯函数单测（2026-10-31）。
//
// 只测不需要 sidecar / 数据库的三块：UTC+8 日界、到期判定、结果文案。
// 桥与调度的端到端行为由 fake sidecar 的契约测试与管理面路由穷举兜底。

import (
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

func TestUTC8DateString(t *testing.T) {
	// 固定时刻：UTC 2026-10-30 15:59:59 = UTC+8 2026-10-30 23:59:59（还差 1 秒跨日）
	//           UTC 2026-10-30 16:00:00 = UTC+8 2026-10-31 00:00:00（刚跨日）
	// 日界必须落在 UTC 16:00 而不是本机时区——这正是算术平移要锁住的行为。
	before := time.Date(2026, 10, 30, 15, 59, 59, 0, time.UTC)
	after := time.Date(2026, 10, 30, 16, 0, 0, 0, time.UTC)
	// 带非 UTC 时区输入，验证只看绝对时刻：
	zoned := time.Date(2026, 10, 31, 8, 30, 0, 0, time.FixedZone("CST", 8*3600))
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"跨日前一秒", before, "2026-10-30"},
		{"跨日瞬间", after, "2026-10-31"},
		{"带时区输入按绝对时刻", zoned, "2026-10-31"},
	}
	for _, c := range cases {
		if got := UTC8DateString(c.in); got != c.want {
			t.Errorf("%s: UTC8DateString = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCheckinDue(t *testing.T) {
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	today := UTC8DateString(now)
	backoffAgo := now.Add(-CheckinRetryBackoff).Unix()      // 恰好满退避期
	recentAgo := now.Add(-CheckinRetryBackoff / 2).Unix()   // 退避期内
	yesterday := "2026-10-30"

	cases := []struct {
		name string
		row  store.CheckinDueRow
		want bool
	}{
		{"今天已覆盖 → 不签", store.CheckinDueRow{CheckinDate: today, CheckinAt: recentAgo}, false},
		// 昨天签过的行，其 CheckinAt 必然在昨天（退避早已期满）→ 今天到期重签；
		// 若昨天 23:50 刚签，今天 00:05 会因退避多等 ≤30min，属可接受的有界延迟。
		{"昨天签过 → 到期重签", store.CheckinDueRow{CheckinDate: yesterday, CheckinAt: backoffAgo}, true},
		{"从未尝试 → 立即", store.CheckinDueRow{}, true},
		{"失败后退避期内 → 不签", store.CheckinDueRow{CheckinAt: recentAgo}, false},
		{"失败且退避期满 → 重试", store.CheckinDueRow{CheckinAt: backoffAgo}, true},
		{"昨天失败且退避已满 → 重试", store.CheckinDueRow{CheckinDate: yesterday, CheckinAt: backoffAgo}, true},
	}
	for _, c := range cases {
		if got := checkinDue(c.row, today, now); got != c.want {
			t.Errorf("%s: checkinDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDescribeClaim(t *testing.T) {
	credit := 200.0
	tokenCredit := 120000000.0
	cases := []struct {
		name string
		in   httpapi.CheckinClaimResult
		want string
	}{
		{"领到积分", httpapi.CheckinClaimResult{Kind: "claimed", Credit: &credit, Unit: "credit"}, "+200 积分"},
		{"领到 token", httpapi.CheckinClaimResult{Kind: "claimed", Credit: &tokenCredit, Unit: "token"}, "+120000000 token"},
		{"已签过", httpapi.CheckinClaimResult{Kind: "already-claimed", Message: "今天已签到"}, "今天已签到"},
		{"活动未开启", httpapi.CheckinClaimResult{Kind: "inactive", Message: "签到活动未开启"}, "签到活动未开启"},
		{"失败带原因", httpapi.CheckinClaimResult{Kind: "failed", Message: "凭据失效（401）"}, "签到失败：凭据失效（401）"},
		{"失败无原因", httpapi.CheckinClaimResult{Kind: "failed"}, "签到失败"},
		{"未知 kind", httpapi.CheckinClaimResult{Kind: "weird", Message: "怪"}, "怪"},
	}
	for _, c := range cases {
		if got := describeClaim(&c.in); got != c.want {
			t.Errorf("%s: describeClaim = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDescribeRound(t *testing.T) {
	cases := []struct {
		name  string
		res   httpapi.CheckinRoundResult
		gains map[string]float64
		want  string
	}{
		{
			"混合结果按固定顺序拼接",
			httpapi.CheckinRoundResult{Claimed: 2, AlreadyClaimed: 1, Failed: 1},
			map[string]float64{"credit": 400},
			"+400 积分，新领 2，已签 1，失败 1",
		},
		{
			"token 与积分分列",
			httpapi.CheckinRoundResult{Claimed: 2},
			map[string]float64{"credit": 300, "token": 1},
			"+300 积分，+1 token，新领 2",
		},
		{"全空轮", httpapi.CheckinRoundResult{}, nil, "本轮没有可签到的凭据"},
	}
	for _, c := range cases {
		if got := describeRound(&c.res, c.gains); got != c.want {
			t.Errorf("%s: describeRound = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{200, "200"},
		{200.5, "200.50"},
		{0, "0"},
	}
	for _, c := range cases {
		if got := formatAmount(c.in); got != c.want {
			t.Errorf("formatAmount(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
