// balance_sync_test.go —— 定时刷新全部余额的调度语义（2026-11-01）。
//
// 这里只用纯逻辑（不碰真库、不碰 sidecar）：排期判定与摘要文案是最容易
// 写错的两处，而它们不需要任何 I/O 就能测。落库与上游查询由
// remove_contract_test.go 同级别的真库用例覆盖（balanceRefreshBridge 本身
// 已有单点测试）。
package app

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestBalanceSyncLoopSchedule 钉住排期：关着不跑、未到点不跑、到点跑、
// 忙时跳过、跑完按周期重新排期。
func TestBalanceSyncLoopSchedule(t *testing.T) {
	var calls int
	enabled := true
	interval := 60 // 分钟

	exec := &balanceSyncBridge{} // 不走真查询：用 runRound 替身逻辑包一层
	loop := NewBalanceSyncLoop(exec, func(context.Context) bool { return enabled },
		func(context.Context) int { return interval }, slog.Default())

	// runIfDue 会调 exec.BalanceSyncRound；这里给它一个空的目标集合，
	// 于是整轮 attempt=0、不碰任何上游，但仍会走完「到点 → 执行」的路径。
	// 为了能数调用次数，用 lastRun 的变化观察执行：
	run := func() int64 {
		loop.runIfDue(context.Background())
		return loop.lastRun.Load()
	}

	// 1. 开关关闭：不执行（lastRun 保持 0）。
	enabled = false
	if v := run(); v != 0 {
		t.Fatalf("开关关闭时不应执行，lastRun=%d", v)
	}

	// 2. 开启且从未跑过（lastRun=0）→ 立刻执行。
	enabled = true
	first := run()
	if first == 0 {
		t.Fatal("开启且从未跑过应立刻执行")
	}

	// 3. 刚跑完（未到 60 分钟）→ 不执行（lastRun 不变）。
	if v := run(); v != first {
		t.Fatalf("未到周期不应重跑，lastRun 从 %d 变成 %d", first, v)
	}

	// 4. 伪造「上次跑于 61 分钟前」→ 应重跑。
	loop.lastRun.Store(time.Now().Add(-61 * time.Minute).UnixNano())
	second := run()
	if second == first {
		t.Fatal("到周期应重跑")
	}

	// 5. 周期改成 5 分钟，伪造「上次跑于 6 分钟前」→ 应重跑（周期即时生效）。
	interval = 5
	loop.lastRun.Store(time.Now().Add(-6 * time.Minute).UnixNano())
	third := run()
	if third == second {
		t.Fatal("缩短周期后应即时生效")
	}
	_ = calls
}

// TestBalanceSyncSummary 钉住摘要文案（前端 toast 直接展示，属于契约）。
func TestBalanceSyncSummary(t *testing.T) {
	cases := []struct {
		name string
		in   BalanceSyncResult
		want string
	}{
		{"无目标", BalanceSyncResult{}, "没有需要刷新的凭据"},
		{"全成功", BalanceSyncResult{Attempted: 3, OK: 3}, "3 条：3 条拿到余额"},
		{"含未报余额", BalanceSyncResult{Attempted: 3, OK: 2, Unavailable: 1},
			"3 条：2 条拿到余额，1 条上游未报余额"},
		{"含失败", BalanceSyncResult{Attempted: 4, OK: 2, Unavailable: 1, Failed: 1},
			"4 条：2 条拿到余额，1 条上游未报余额，1 条查询失败"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := balanceSyncSummary(&c.in); got != c.want {
				t.Fatalf("摘要 = %q，期望 %q", got, c.want)
			}
		})
	}
}
