package app

// admin_bridge_nested_contract_test.go —— 嵌套候选的装配契约（真 SQLite）。
//
// 钉住用户需求（2026-11-01）的保存侧行为：抽象模型可以把其他抽象模型加为
// 候选；**会形成循环依赖的项在保存时被拒绝**，并且详情端点的
// blocked_abstract_ids 把它们列出来（前端据此在选择列表里隐藏）。
//
// 为什么这些测试跑真 SQLite：成环判据读的是候选表（ListCandidates 递归），
// fake 内存表测不出「外键 CASCADE / 事务可见性」这些装配事实。

import (
	"context"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// newNestedBridge 给测试 app 造一个 modelsBridge（候选读写走它）。
func newNestedBridge(a *App) *modelsBridge {
	return &modelsBridge{b: &adminBridge{st: a.store, repos: a.store.Repos()}}
}

// seedNestedModels 建两个各挂一条真实候选的抽象模型，返回 (aID, bID)。
func seedNestedModels(t *testing.T, a *App, nameA, nameB string) (string, string) {
	t.Helper()
	amA, _ := seedCandidateChain(t, a, nameA, "model-a")
	amB, _ := seedCandidateChain(t, a, nameB, "model-b")
	return amA, amB
}

func putCands(t *testing.T, mb *modelsBridge, amID string, cands ...httpapi.AdminCandidateItem) error {
	t.Helper()
	return mb.putCandidates(context.Background(), mb.b.repos.Models, amID, cands)
}

// TestContractNestedCandidateAcceptedAndExpanded A 可以把 B 加为候选；
// ListCandidates 回读出 kind=abstract 与目标名。
func TestContractNestedCandidateAcceptedAndExpanded(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	amA, amB := seedNestedModels(t, a, "na", "nb")
	mb := newNestedBridge(a)

	if err := putCands(t, mb, amA,
		httpapi.AdminCandidateItem{RealModelID: amB, Kind: "abstract"}); err != nil {
		t.Fatalf("合法嵌套候选不应被拒: %v", err)
	}

	items, err := mb.ListCandidates(context.Background(), amA)
	if err != nil {
		t.Fatalf("读候选失败: %v", err)
	}
	if len(items) != 1 || items[0].Kind != "abstract" || items[0].TargetName != "nb" {
		t.Fatalf("候选应为 abstract/nb: %+v", items)
	}
	// 叶子计数：B 挂了一条真实候选 → 展开数 = 1。
	if items[0].AvailableInstances != 1 {
		t.Fatalf("嵌套候选的展开数应为 1: %+v", items[0])
	}
}

// TestContractNestedCycleRejected 成环的保存必须被拒绝：A→B 已存在时，
// B 再把 A 加为候选 = A→B→A，拒绝并给出成环路径。
func TestContractNestedCycleRejected(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	amA, amB := seedNestedModels(t, a, "na", "nb")
	mb := newNestedBridge(a)

	if err := putCands(t, mb, amA,
		httpapi.AdminCandidateItem{RealModelID: amB, Kind: "abstract"}); err != nil {
		t.Fatalf("建 A→B 失败: %v", err)
	}
	err := putCands(t, mb, amB,
		httpapi.AdminCandidateItem{RealModelID: amA, Kind: "abstract"})
	if err == nil {
		t.Fatal("A→B→A 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "循环依赖") || !strings.Contains(err.Error(), "na → nb") {
		t.Fatalf("错误信息应含成环路径: %v", err)
	}

	// 间接环：A→B→C 之后，C 再指回 A 同样被拒。
	amC, _ := seedCandidateChain(t, a, "nc", "model-c")
	if err := putCands(t, mb, amB,
		httpapi.AdminCandidateItem{RealModelID: amC, Kind: "abstract"}); err != nil {
		t.Fatalf("建 B→C 失败: %v", err)
	}
	err = putCands(t, mb, amC,
		httpapi.AdminCandidateItem{RealModelID: amA, Kind: "abstract"})
	if err == nil || !strings.Contains(err.Error(), "循环依赖") {
		t.Fatalf("A→B→C→A 必须被拒绝: %v", err)
	}
}

// TestContractNestedSelfRejected 自己不能加自己。
func TestContractNestedSelfRejected(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	amA, _ := seedNestedModels(t, a, "na", "nb")
	mb := newNestedBridge(a)

	err := putCands(t, mb, amA,
		httpapi.AdminCandidateItem{RealModelID: amA, Kind: "abstract"})
	if err == nil || !strings.Contains(err.Error(), "自己") {
		t.Fatalf("自引用必须被拒绝: %v", err)
	}
}

// TestContractBlockedAbstractIDs 详情端点的隐藏集合：对 B 而言，A（能经
// A→B 回到 B）和 B 自己都在 blocked 里；无关的 C 不在。
func TestContractBlockedAbstractIDs(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	amA, amB := seedNestedModels(t, a, "na", "nb")
	amC, _ := seedCandidateChain(t, a, "nc", "model-c")
	mb := newNestedBridge(a)

	if err := putCands(t, mb, amA,
		httpapi.AdminCandidateItem{RealModelID: amB, Kind: "abstract"}); err != nil {
		t.Fatalf("建 A→B 失败: %v", err)
	}

	blocked, err := mb.BlockedAbstractIDs(context.Background(), amB)
	if err != nil {
		t.Fatalf("读 blocked 失败: %v", err)
	}
	got := map[string]bool{}
	for _, id := range blocked {
		got[id] = true
	}
	if !got[amB] || !got[amA] {
		t.Fatalf("blocked 应含 B 自己与 A（A→B 已存在，B 再加 A 会成环）: %v", blocked)
	}
	if got[amC] {
		t.Fatalf("无关联的 C 不应被拦: %v", blocked)
	}
}

// TestContractNestedUnknownKindRejected 未知 kind 拒绝（静默忽略会让管理员
// 以为加进去了，实际是同 UUID 的真实模型行不存在）。
func TestContractNestedUnknownKindRejected(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	amA, amB := seedNestedModels(t, a, "na", "nb")
	mb := newNestedBridge(a)

	err := putCands(t, mb, amA,
		httpapi.AdminCandidateItem{RealModelID: amB, Kind: "abstact"})
	if err == nil || !strings.Contains(err.Error(), "candidate_kind") {
		t.Fatalf("拼错的 kind 必须被拒绝: %v", err)
	}
}

// TestContractNestedKindRoundTrip store 层 roundtrip：kind=abstract 落库后
// 读回仍是 abstract（防「写入时被归一成 real」的回归）。
func TestContractNestedKindRoundTrip(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	amA, amB := seedNestedModels(t, a, "na", "nb")
	ctx := context.Background()
	if err := a.store.Repos().Models.SetCandidates(ctx, amA, []store.Candidate{
		{AbstractModelID: amA, TargetID: amB, Kind: store.KindAbstract},
	}); err != nil {
		t.Fatalf("写嵌套候选失败: %v", err)
	}
	rows, err := a.store.Repos().Models.ListCandidates(ctx, amA)
	if err != nil || len(rows) != 1 {
		t.Fatalf("读候选失败: %v %v", rows, err)
	}
	if rows[0].Kind != store.KindAbstract || rows[0].TargetID != amB {
		t.Fatalf("kind 应为 abstract 且目标不变: %+v", rows[0])
	}
}
