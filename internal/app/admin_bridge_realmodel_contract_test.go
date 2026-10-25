package app

// admin_bridge_realmodel_contract_test.go —— 手工登记模型（CreateRealModel）
// 的装配契约（tmp/pool-merge-contract.md §1.3）。
//
// 跑真 SQLite，钉的是「桥落库的形状」而不是 handler 的校验（后者在
// internal/httpapi 的 real_model_create_contract_test.go 里锁）：
//   - 新建行 source=override（刷新保护的前提：UpsertRealModel 的
//     ON CONFLICT 只在 source='auto' 行上覆盖）；
//   - 幂等：同 (上游, 模型 id) 二次调用返回既有行、created=false、
//     审计不再追加；
//   - 上游不存在 → ErrAdminUpstreamNotFound（对外 404）；
//   - display_name 缺省取模型 id（界面不至于渲染空串）。

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// TestContractCreateRealModelOverrideSource 钉住新建行的落库形状。
func TestContractCreateRealModelOverrideSource(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()
	mb := &modelsBridge{b: newAdminBridge(a.store)}

	// 建一条上游（不建模型）。
	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "manual-u",
		"https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}

	row, created, auditID, err := mb.CreateRealModel(ctx, up, "hidden-model", "", nil,
		httpapi.AdminAuditEntry{AccountID: "acc_1", Action: "model.manual_add", Result: "ok"})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if !created {
		t.Fatalf("首次登记 created 应为 true")
	}
	if auditID == "" {
		t.Fatalf("新建必须落审计（auditID 为空）")
	}
	if row.Source != "override" {
		t.Fatalf("手工行 source 应为 override（刷新字段级保护的前提），得 %q", row.Source)
	}
	if row.Status != "active" {
		t.Fatalf("手工行 status 应为 active，得 %q", row.Status)
	}
	if row.DisplayName != "hidden-model" {
		t.Fatalf("display_name 缺省应取模型 id，得 %q", row.DisplayName)
	}

	// 库里确实只有一行。
	rows, err := a.store.Repos().Models.ListRealModelsByUpstream(ctx, up)
	if err != nil || len(rows) != 1 {
		t.Fatalf("应恰好 1 行，得 %d（err=%v）", len(rows), err)
	}
}

// TestContractCreateRealModelIdempotent 钉住幂等：二次调用返回既有行、
// created=false、不追加审计、不产生第二行。
func TestContractCreateRealModelIdempotent(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()
	mb := &modelsBridge{b: newAdminBridge(a.store)}
	up, _ := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "idem-u",
		"https://example.com/v1", nil, true)

	first, created, _, err := mb.CreateRealModel(ctx, up, "same-model", "名字", nil,
		httpapi.AdminAuditEntry{AccountID: "acc_1", Action: "model.manual_add", Result: "ok"})
	if err != nil || !created {
		t.Fatalf("首次登记应 created=true：%v (err=%v)", created, err)
	}
	second, created2, auditID2, err := mb.CreateRealModel(ctx, up, "same-model", "另一个名字", nil,
		httpapi.AdminAuditEntry{AccountID: "acc_1", Action: "model.manual_add", Result: "ok"})
	if err != nil {
		t.Fatalf("二次登记失败: %v", err)
	}
	if created2 {
		t.Fatalf("二次调用 created 应为 false（幂等）")
	}
	if auditID2 != "" {
		t.Fatalf("幂等命中不应写审计，实得 auditID=%q", auditID2)
	}
	if second.ID != first.ID {
		t.Fatalf("应返回同一行：%s vs %s", first.ID, second.ID)
	}
	// 幂等命中时 display_name 不能被"顺手改一下"——那是 PATCH 的职责。
	if second.DisplayName != "名字" {
		t.Fatalf("幂等命中不得改显示名，实得 %q", second.DisplayName)
	}
}

// TestContractCreateRealModelUpstreamMissing 钉住上游不存在 → 404 哨兵。
func TestContractCreateRealModelUpstreamMissing(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()
	mb := &modelsBridge{b: newAdminBridge(a.store)}
	_, _, _, err := mb.CreateRealModel(ctx, 99999, "m", "", nil,
		httpapi.AdminAuditEntry{AccountID: "acc_1", Action: "model.manual_add", Result: "ok"})
	if !errors.Is(err, httpapi.ErrAdminUpstreamNotFound) {
		t.Fatalf("上游不存在应 ErrAdminUpstreamNotFound，得 %v", err)
	}
}

// TestContractCreateRealModelSurvivesRefresh 钉住契约 §1.3 的闭环承诺：
// 手工行（override）在「目录里没有它」的那轮刷新后被标 missing（诚实状态），
// 但**不被删除**、候选引用保留；目录重新出现后自动回到 active。
//
// 为什么这条要钉：手工加模型的动机是"自动发现拿不到"，如果某轮刷新把
// 它当噪声清掉（删除或永久 missing），管理员的配置就凭空蒸发——那是比
// 标 missing 更坏的形态。这里用存储层原语（UpsertRealModel +
// MarkMissingRealModels）复现刷新器的两条写路径，不引 upstream 包
// （它的刷新器要 sidecar，装配不起，且行为面就是这两条 SQL）。
func TestContractCreateRealModelSurvivesRefresh(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()
	mb := &modelsBridge{b: newAdminBridge(a.store)}
	up, _ := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "refresh-u",
		"https://example.com/v1", nil, true)
	row, _, _, err := mb.CreateRealModel(ctx, up, "manual-only", "手工模型", nil,
		httpapi.AdminAuditEntry{AccountID: "acc_1", Action: "model.manual_add", Result: "ok"})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	// 把这条手工行挂进一个抽象模型的候选集（验证 CASCADE 安全）。
	amID, err := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "manual-host", DisplayName: "manual-host", Status: store.ModelActive, CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	// row.ID 形如 rm_<n>，解码回行 id。
	var rmNum int64
	if _, err := fmt.Sscanf(row.ID, "rm_%d", &rmNum); err != nil {
		t.Fatalf("解析行 id 失败: %q", row.ID)
	}
	if err := a.store.Repos().Models.SetCandidates(ctx, amID, []store.Candidate{
		{AbstractModelID: amID, RealModelID: rmNum},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}

	// 第一轮"刷新"：目录里只有一条 auto 行，手工行不在 keepIDs 里。
	autoID, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up, UpstreamModelID: "auto-seen", DisplayName: "auto", Source: "auto",
	})
	if err != nil {
		t.Fatalf("建 auto 行失败: %v", err)
	}
	if _, err := a.store.Repos().Models.MarkMissingRealModels(ctx, up, []int64{autoID}); err != nil {
		t.Fatalf("标 missing 失败: %v", err)
	}
	got, err := a.store.Repos().Models.GetRealModel(ctx, rmNum)
	if err != nil {
		t.Fatalf("手工行应保留（不删除），读失败: %v", err)
	}
	if got.Status != store.RealModelMissing {
		t.Fatalf("目录没有它时应标 missing（诚实状态），得 %q", got.Status)
	}
	// 候选引用必须还在（missing 不 CASCADE）。
	cands, err := a.store.Repos().Models.ListCandidates(ctx, amID)
	if err != nil || len(cands) != 1 {
		t.Fatalf("候选引用应保留 1 条，得 %d（err=%v）", len(cands), err)
	}

	// 第二轮"刷新"：目录里出现了它（upsert 命中同 (上游,id)，auto 参数
	// 覆盖 status 回 active——这正是「上游恢复时自动写回」的机制）。
	if _, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up, UpstreamModelID: "manual-only", DisplayName: "manual-only", Source: "auto",
	}); err != nil {
		t.Fatalf("第二轮 upsert 失败: %v", err)
	}
	got2, err := a.store.Repos().Models.GetRealModel(ctx, rmNum)
	if err != nil {
		t.Fatalf("读失败: %v", err)
	}
	if got2.Status != store.RealModelActive {
		t.Fatalf("目录重新出现后应自动 active，得 %q", got2.Status)
	}
	// 但 source 仍是 override：ON CONFLICT 的 WHERE 挡住 auto 覆盖，
	// 人工登记的行不被自动目录"收编"（01-catalog.md §3.3 字段级保护）。
	if got2.Source != "override" {
		t.Fatalf("override 标记不应被刷新抹掉，得 %q", got2.Source)
	}
}

// 编译期哨兵：桥必须实现 httpapi 的窄接口（签名漂移在装配前就红）。
var _ httpapi.AdminRealModelCreator = (*modelsBridge)(nil)
