// remove_contract_test.go —— 「移除」（2026-10-31）的真库契约：
// 管理员移除供应商 / 贡献者移除贡献，两侧共享「留行不留钥」语义。
//
// 为什么真库：removed_at 过滤、外键存活、密文墓碑三件事都是 SQL/crypto 行为。
package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

func TestContractRemoveUpstreamAndRevoke(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	owner := newContributorAccount(t, a, "anonymous-contrib-remove")

	// 贡献一把 key（完整链路入池）。
	svc := a.Contribution()
	srv := newCatalogServer(t, []string{"m1"}, 200)
	res, err := svc.Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "demo-remove", BaseURL: srv.URL + "/v1",
		APIKey: "sk-remove-me-abcdef12345678", ClaimedModels: []string{"m1"},
	})
	if err != nil || !res.InPool {
		t.Fatalf("前置提交应入池：err=%v res=%+v", err, res)
	}

	// —— 贡献者移除自己的贡献（svc.Revoke）——
	if err := svc.Revoke(ctx, res.CredentialID, upstream.RevokeActor{AccountID: owner}); err != nil {
		t.Fatalf("移除贡献失败：%v", err)
	}
	cred, err := a.store.Repos().Models.LookupCredential(ctx, res.CredentialID)
	if err != nil {
		t.Fatalf("凭据行必须保留（归因外键）：%v", err)
	}
	if cred.Status != string(store.CredDisabled) {
		t.Errorf("凭据状态应 disabled，得 %s", cred.Status)
	}
	// 属主列表里消失（removed_at 过滤）。
	own, err := a.store.Repos().Models.ListCredentialsByOwner(ctx, owner)
	if err != nil || len(own) != 0 {
		t.Fatalf("属主列表应空（已移除），得 %d err=%v", len(own), err)
	}
	// 上游行：enabled=0 + removed_at 置位；密文是墓碑（解不出原文）。
	up := findUpstreamByName(t, a, "demo-remove-contrib-") // contrib 命名前缀
	if up.Enabled {
		t.Error("上游行应 enabled=false")
	}
	if up.RemovedAt == nil {
		t.Error("上游行应标记 removed_at")
	}
	// 全池可见模型不再含它（候选池退出）。
	vis, err := a.store.Repos().Models.ListVisibleRealModels(ctx)
	if err != nil {
		t.Fatalf("列可见模型失败：%v", err)
	}
	for _, m := range vis {
		if m.UpstreamID == up.ID {
			t.Error("已移除上游的模型不应再可见")
		}
	}

	// —— 管理员移除供应商（桥 RemoveUpstream）——
	ub := &upstreamsBridge{b: newAdminBridge(a.store)}
	actor := auditActor(t, a)
	// 用另一条贡献建行，验证整源移除（含墓碑）。
	res2, err := svc.Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "demo-src", BaseURL: srv.URL + "/v1",
		APIKey: "sk-src-remove-abcdef1234567890", ClaimedModels: []string{"m1"},
	})
	if err != nil {
		t.Fatalf("第二条提交失败：%v", err)
	}
	up2 := findUpstreamByName(t, a, "demo-src-contrib-")
	_, _, err = ub.RemoveUpstream(ctx, up2.ID, httpapi.AdminAuditEntry{
		AccountID: actor, Action: "upstream.remove", Result: "ok",
	})
	if err != nil {
		t.Fatalf("移除供应商失败：%v", err)
	}
	// 凭据行 disabled + removed；密文解不出原 key。
	cred2, _ := a.store.Repos().Models.LookupCredential(ctx, res2.CredentialID)
	if cred2 == nil || cred2.Status != string(store.CredDisabled) {
		t.Fatalf("被移除上游的凭据应 disabled，得 %+v", cred2)
	}
	own2, _ := a.store.Repos().Models.ListCredentialsByOwner(ctx, owner)
	for _, c := range own2 {
		if c.ID == res2.CredentialID {
			t.Error("被移除上游的凭据不应出现在属主列表")
		}
	}
	plain, err := a.store.Repos().Secrets.GetSecretByUpstream(ctx, up2.ID)
	if err != nil {
		// 解不开也是墓碑生效的一种（随机串的 GCM 认证可能失败）。
		t.Logf("密文读取/解密失败（可接受的墓碑形态）：%v", err)
	} else if strings.Contains(string(plain), "sk-src-remove") {
		t.Error("密文应是墓碑，不含原 key")
	}
}

func findUpstreamByName(t *testing.T, a *App, prefix string) store.Upstream {
	t.Helper()
	ups, err := a.store.Repos().Models.ListUpstreams(context.Background())
	if err != nil {
		t.Fatalf("列上游失败：%v", err)
	}
	for _, u := range ups {
		if strings.HasPrefix(u.Name, prefix) {
			return u
		}
	}
	t.Fatalf("找不到前缀 %s 的上游", prefix)
	return store.Upstream{}
}

// 编译期引用 time（将来断言 removed_at 值域用）。
var _ = time.Now

// TestContractRemovedCredentialLeavesPoolViews 钉住 2026-11-01 的修复：
// 贡献者移除凭据后，**凭据池的四个读面都不再出现它**——行列表、凭据聚合
// （可用实例数/来源计数）、管理端凭据列表、待鉴定队列；而单点读仍可拿到
// （审计与 requests 归因要它）。
//
// 为什么这条要单独测：移除只写了 removed_at（行保留），读面若忘记过滤，
// 界面就出现「已禁用」的僵尸行——用户实测到过，且它诱发「这条还能不能救」
// 的无效排查（密文已是墓碑，救不回来）。
func TestContractRemovedCredentialLeavesPoolViews(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	owner := newContributorAccount(t, a, "anonymous-contrib-poolviews")

	svc := a.Contribution()
	srv := newCatalogServer(t, []string{"m1"}, 200)
	res, err := svc.Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "demo-poolview", BaseURL: srv.URL + "/v1",
		APIKey: "sk-poolview-abcdef1234567890", ClaimedModels: []string{"m1"},
	})
	if err != nil || !res.InPool {
		t.Fatalf("前置提交应入池：err=%v res=%+v", err, res)
	}
	// 移除（贡献者侧撤销）。
	if err := svc.Revoke(ctx, res.CredentialID, upstream.RevokeActor{AccountID: owner}); err != nil {
		t.Fatalf("移除失败：%v", err)
	}

	// ① 凭据池行列表：该行消失，且它所属的来源也不再出现。
	rows, err := a.store.Repos().Models.ListUpstreamCredentialRows(ctx)
	if err != nil {
		t.Fatalf("列凭据行失败：%v", err)
	}
	for upID, list := range rows {
		for _, r := range list {
			if r.ID == res.CredentialID {
				t.Fatalf("已移除的凭据不应出现在凭据池行列表（上游 %s）", upID)
			}
		}
	}

	// ② 凭据聚合（可用实例数/来源计数）：该来源整体消失。
	agg, err := a.store.Repos().Models.ListUpstreamCredentials(ctx)
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	up := findUpstreamByName(t, a, "demo-poolview-contrib-")
	if _, ok := agg[up.ID]; ok {
		t.Fatalf("已移除的来源不应出现在凭据聚合（%s）", up.Name)
	}

	// ③ 管理端凭据列表：按状态/全量都查不到。
	for _, f := range []store.CredentialFilter{{}, {Status: store.CredDisabled}} {
		list, err := a.store.Repos().Models.ListCredentials(ctx, f)
		if err != nil {
			t.Fatalf("管理端列表失败：%v", err)
		}
		for _, c := range list {
			if c.ID == res.CredentialID {
				t.Fatalf("已移除的凭据不应出现在管理端列表（filter=%+v）", f)
			}
		}
	}

	// ④ 待鉴定队列：也不该有它。
	pendingList, err := a.store.Repos().Models.ListUnverified(ctx, "", "")
	if err != nil {
		t.Fatalf("待鉴定列表失败：%v", err)
	}
	for _, c := range pendingList {
		if c.ID == res.CredentialID {
			t.Fatal("已移除的凭据不应进入鉴定队列")
		}
	}

	// ⑤ 单点读仍可拿到（审计与归因依赖行存在）。
	got, err := a.store.Repos().Models.LookupCredential(ctx, res.CredentialID)
	if err != nil {
		t.Fatalf("LookupCredential 应仍可读（归因链）：%v", err)
	}
	if got.Status != string(store.CredDisabled) {
		t.Errorf("状态应为 disabled，得 %s", got.Status)
	}
}
