// auto_model_contract_test.go —— 系统托管的 auto 抽象模型（2026-10-31 用户定案）
// 的真库契约：建模、候选集口径、授权时机与撤回终态。
//
// 为什么真库：候选集口径（可见/排除/missing）与授权读-改-写都是 SQL 行为，
// fake 存储证明不了「disabled 上游的模型不进候选」这类口径。
package app

import (
	"context"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// seedPoolModel 造一条真实模型（属新建上游），返回 real_model id。
func seedPoolModel(t *testing.T, a *App, name string, upstreamEnabled bool, excluded bool, status store.RealModelStatus) string {
	t.Helper()
	ctx := context.Background()
	uid, err := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "up-"+name,
		"https://"+name+".example/v1", nil, upstreamEnabled)
	if err != nil {
		t.Fatalf("建上游 %s 失败：%v", name, err)
	}
	id, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID:             uid,
		UpstreamModelID:        name,
		DisplayName:            name,
		Status:                 status,
		ExcludedFromCandidates: excluded,
	})
	if err != nil {
		t.Fatalf("建真实模型 %s 失败：%v", name, err)
	}
	return id
}

func TestContractAutoModelSync(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	var gid string

	// 模拟用户已有配置：默认组由存储自举兜底创建（这里直接复用）+ 一个普通组，
	// 加一条手工抽象模型授权给默认组。
	gid = defaultGroupID(t, a)
	otherID, err := a.store.Repos().Groups.CreateGroup(ctx, "受限组", "", false)
	if err != nil {
		t.Fatalf("建第二组失败：%v", err)
	}
	manual, err := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "deepseek-v4.1-flash", DisplayName: "DS-V41-F", Strategy: `["expiry-first","balance"]`,
	})
	if err != nil {
		t.Fatalf("建手工模型失败：%v", err)
	}
	if err := a.store.Repos().Groups.SetGroupModels(ctx, gid, []string{manual}); err != nil {
		t.Fatalf("授权手工模型失败：%v", err)
	}

	// 池内四态：可路由 / 上游停用 / 被排除 / missing——只有第一种该进候选。
	good := seedPoolModel(t, a, "model-good", true, false, store.RealModelActive)
	seedPoolModel(t, a, "model-offup", false, false, store.RealModelActive)
	seedPoolModel(t, a, "model-excluded", true, true, store.RealModelActive)
	seedPoolModel(t, a, "model-missing", true, false, store.RealModelMissing)

	// —— 首轮同步：建模 + 候选集 = 可路由集合 + 授权给全部组 ——
	if err := a.syncAutoModel(ctx); err != nil {
		t.Fatalf("首轮同步失败：%v", err)
	}
	m, err := a.store.Repos().Models.GetAbstractModelByName(ctx, "auto")
	if err != nil {
		t.Fatalf("auto 应已创建：%v", err)
	}
	cands, err := a.store.Repos().Models.ListCandidates(ctx, m.ID)
	if err != nil || len(cands) != 1 || cands[0].RealModelID != good {
		t.Fatalf("候选集应只含可路由模型，得 %d 条（want %s）", len(cands), good)
	}
	for _, g := range []string{gid, otherID} {
		if !groupHasModel(t, a, g, m.ID) {
			t.Errorf("组 %s 应在建模时被授予 auto", g)
		}
	}

	// —— 撤回终态：管理员把 auto 从受限组撤掉，后续同步不得加回 ——
	if err := a.store.Repos().Groups.SetGroupModels(ctx, otherID, nil); err != nil {
		t.Fatalf("清空授权失败：%v", err)
	}
	seedPoolModel(t, a, "model-new", true, false, store.RealModelActive) // 新模型入池
	if err := a.syncAutoModel(ctx); err != nil {
		t.Fatalf("第二轮同步失败：%v", err)
	}
	if mids := groupModels(t, a, otherID); len(mids) != 0 {
		t.Errorf("同步不得重授被撤回的 auto，得 %v", mids)
	}
	if cands, _ = a.store.Repos().Models.ListCandidates(ctx, m.ID); len(cands) != 2 {
		t.Errorf("新入池模型应被下一轮同步收录（候选 2 条），得 %d", len(cands))
	}

	// —— 停用 = 下线开关（抽象模型没有 DELETE 端点，停用是唯一的下线手段；
	// 同步循环对 disabled 的 auto 只读不写）——
	if err := a.store.Repos().Models.SetAbstractModelStatus(ctx, m.ID, store.ModelDisabled); err != nil {
		t.Fatalf("停用 auto 失败：%v", err)
	}
	seedPoolModel(t, a, "model-after-disable", true, false, store.RealModelActive)
	if err := a.syncAutoModel(ctx); err != nil {
		t.Fatalf("停用后同步失败：%v", err)
	}
	if cands, _ = a.store.Repos().Models.ListCandidates(ctx, m.ID); len(cands) != 2 {
		t.Errorf("停用的 auto 不得被同步改写候选集（仍 2 条），得 %d", len(cands))
	}
}

func TestContractCreateGroupGrantsAuto(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	// 先让 auto 存在（模拟已运行过的实例）。
	if err := a.syncAutoModel(ctx); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	gb := &groupsBridge{b: newAdminBridge(a.store)}
	actor := auditActor(t, a)

	// 建组桥：显式授权 auto 的新组不重复追加。
	gid, _, err := gb.CreateGroup(ctx, httpapi.AdminGroup{Name: "vip"},
		[]string{"auto"}, nil, httpapi.AdminAuditEntry{AccountID: actor, Action: "group.create", Result: "ok"})
	if err != nil {
		t.Fatalf("建组失败：%v", err)
	}
	if mids := groupModels(t, a, gid); len(mids) != 1 {
		t.Fatalf("显式授权 auto 的组应有且只有 auto，得 %v", mids)
	}

	// 复制组同样带 auto（源组有授权，副本继承，auto 不重复）。
	dupID, _, err := gb.DuplicateGroup(ctx, gid, "vip2",
		httpapi.AdminAuditEntry{AccountID: actor, Action: "group.duplicate", Result: "ok"})
	if err != nil {
		t.Fatalf("复制组失败：%v", err)
	}
	if dmids := groupModels(t, a, dupID.ID); len(dmids) != 1 {
		t.Fatalf("复制组应继承 auto 授权（不重复追加），得 %v", dmids)
	}

	// 建组时未给任何模型：auto 也要默认授予（「每个组出生即有」）。
	emptyID, _, err := gb.CreateGroup(ctx, httpapi.AdminGroup{Name: "bare"},
		nil, nil, httpapi.AdminAuditEntry{AccountID: actor, Action: "group.create", Result: "ok"})
	if err != nil {
		t.Fatalf("建空授权组失败：%v", err)
	}
	if !groupHasModel(t, a, emptyID, autoModelID(t, a)) {
		t.Error("空授权的新组也应默认拿到 auto")
	}
}

// —— 测试小工具 ——

func groupModels(t *testing.T, a *App, groupID string) []string {
	t.Helper()
	mids, err := a.store.Repos().Groups.ListGroupModels(context.Background(), groupID)
	if err != nil {
		t.Fatalf("读组授权失败：%v", err)
	}
	return mids
}

func groupHasModel(t *testing.T, a *App, groupID, modelID string) bool {
	t.Helper()
	for _, id := range groupModels(t, a, groupID) {
		if id == modelID {
			return true
		}
	}
	return false
}

func autoModelID(t *testing.T, a *App) string {
	t.Helper()
	m, err := a.store.Repos().Models.GetAbstractModelByName(context.Background(), autoModelName)
	if err != nil {
		t.Fatalf("读 auto 失败：%v", err)
	}
	return m.ID
}

// defaultGroupID 取存储自举兜底建的默认组。
func defaultGroupID(t *testing.T, a *App) string {
	t.Helper()
	groups, err := a.store.Repos().Groups.ListGroups(context.Background())
	if err != nil {
		t.Fatalf("列组失败：%v", err)
	}
	for _, g := range groups {
		if g.IsDefault {
			return g.ID
		}
	}
	t.Fatal("自举应兜底建默认组")
	return ""
}
