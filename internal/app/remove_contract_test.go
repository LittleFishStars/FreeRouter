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
