// oauth_reconcile_contract_test.go —— OAuth 贡献行形态对齐的真库契约
// （2026-10-31 批量移植的最后一公里）。
//
// 钉住四件事：vendor 行原地翻转（不另起新行）、非移植行不碰、幂等、
// enabled（属主的停止贡献开关）保持原值。
package app

import (
	"context"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

func TestContractOAuthReconcile(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()

	// 三行老形态 + 一行已对齐 + 一行非贡献行：
	//   minimax / zcode / qodercn 声明 relay → 翻转；
	//   cline 声明 vendor → 不碰；
	//   api:bigmodel 行是 API 型贡献（contrib 命名）→ 不在声明表 → 不碰。
	seedUp := func(name, kind, baseURL string, enabled bool) string {
		id, err := a.store.Repos().Models.UpsertUpstream(ctx, kind, name, baseURL, nil, enabled)
		if err != nil {
			t.Fatalf("建上游 %s 失败：%v", name, err)
		}
		return id
	}
	minimaxID := seedUp("minimax-oauth-key_4565", "vendor", "https://agent.minimax.cn", false)
	zcodeID := seedUp("zcode-oauth-key_b1a6", "vendor", "https://oauth-only.invalid", true)
	qodercnID := seedUp("qodercn-oauth-key_817c", "vendor", "https://oauth-only.invalid", true)
	clineID := seedUp("cline-oauth-key_dd59", "vendor", "https://api.cline.bot", true)
	bigmodelID := seedUp("api:bigmodel-cn-contrib-key_8e7e", "vendor", "https://open.bigmodel.cn", true)
	_ = bigmodelID

	decl := map[string]oauthEndpointDecl{
		"minimax":  {Kind: "relay", APIBase: "sidecar://minimax"},
		"zcode":    {Kind: "relay", APIBase: "sidecar://zcode"},
		"qodercn":  {Kind: "relay", APIBase: "sidecar://qodercn"},
		"cline":    {Kind: "vendor", APIBase: "https://api.cline.bot/api/v1"},
		"bigmodel": {Kind: "vendor", APIBase: "https://open.bigmodel.cn"},
	}
	flipped, err := a.reconcileOAuthUpstreams(ctx, decl)
	if err != nil {
		t.Fatalf("对齐失败：%v", err)
	}
	if len(flipped) != 3 {
		t.Fatalf("应翻转 3 行，得 %v", flipped)
	}

	assertRow := func(id, wantKind, wantURL string, wantEnabled bool, label string) {
		t.Helper()
		u, err := a.store.Repos().Models.GetUpstreamByID(ctx, id)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", label, err)
		}
		if u.Kind != wantKind || u.BaseURL != wantURL || u.Enabled != wantEnabled {
			t.Errorf("%s：kind/base_url/enabled = %s/%s/%v，期望 %s/%s/%v",
				label, u.Kind, u.BaseURL, u.Enabled, wantKind, wantURL, wantEnabled)
		}
	}
	assertRow(minimaxID, "relay", "sidecar://minimax", false, "minimax")
	assertRow(zcodeID, "relay", "sidecar://zcode", true, "zcode")
	assertRow(qodercnID, "relay", "sidecar://qodercn", true, "qodercn")
	assertRow(clineID, "vendor", "https://api.cline.bot", true, "cline（vendor 声明不碰）")

	// 幂等：第二轮零翻转。
	flipped2, err := a.reconcileOAuthUpstreams(ctx, decl)
	if err != nil || len(flipped2) != 0 {
		t.Fatalf("第二轮应幂等零翻转，得 %v err=%v", flipped2, err)
	}

	// 旧行不残留：同名的 relay 行只有一条（原地翻，不是另起）。
	ups, err := a.store.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		t.Fatalf("列上游失败：%v", err)
	}
	names := map[string]int{}
	for _, u := range ups {
		names[u.Name]++
	}
	for n, c := range names {
		if c != 1 {
			t.Errorf("行 %s 出现 %d 次（原地翻转不应造重复行）", n, c)
		}
	}
	_ = store.ModelActive // 保持 store 导入（断言辅助在未来用得上）
}
