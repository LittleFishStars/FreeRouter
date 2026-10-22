package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// TestContractRefreshHooksWired 守住「生产装配的刷新器必须带 hooks」。
//
// 为什么需要它：旧实现用 NewModelRefresher（不带 hooks），生产上 0004 三列
// 恒为 unverified、inbox 事件也不投递——而这个缺口当时没有任何测试能发现
// （upstream 侧只测了「hooks 传进去会怎样」，没测「装配点到底传没传」）。
// 断言的是可观察契约：Verification 非 nil（鉴定写回有实现）、Log 非 nil
// （两半告警不会 panic）。
func TestContractRefreshHooksWired(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), store.Options{Path: dir + "/t.db", MasterKeyFile: dir + "/master.key"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	h := refreshHooksFor(st, nil)
	if h == nil {
		t.Fatal("refreshHooksFor 返回 nil：生产装配将不带 hooks")
	}
	if h.Verification == nil {
		t.Fatal("Verification 为 nil：0004 三列会恒 unverified")
	}
	if h.Inbox == nil {
		t.Fatal("Inbox 为 nil：目录变动不会进待处理事项")
	}
	if h.Log == nil {
		t.Fatal("Log 为 nil：联动告警会落到 slog.Default 之外或 panic")
	}
	// 显式传 logger 时用传入的那个，不被默认值覆盖。
	l := slog.Default()
	if got := refreshHooksFor(st, l); got.Log != l {
		t.Fatal("显式传入的 logger 未被采用")
	}
}
