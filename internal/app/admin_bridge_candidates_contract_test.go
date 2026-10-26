package app

// admin_bridge_candidates_contract_test.go —— 候选集「可用实例数」的装配契约。
//
// 钉住的是一次真实的静默故障：候选集的 available_instances 与抽象模型的
// available_candidate_count 曾长期是**硬编码的 0**（桥里的静态占位）。它带来
// 三个后果，且都不报错：
//
//  1. 候选集编辑器与抽象模型列表上的「可用实例 / 可用候选」恒为 0，管理员
//     看不到「配了但永远选不中」（02-admin.md §8.2 明写这个字段就是为了让
//     预览能看出有没有凭据支撑）；
//  2. 保存前的干跑（§8.3）按 available_instances>0 分桶，静态 0 让**每一条**
//     候选都落进 no_candidate，于是这个防呆机制恒报红灯，等于没有；
//  3. 列表页的「候选 / 可用」徽标恒为 "N / 0"，被当成"一个都不可用"。
//
// 所以这里的测试不是"断言一个数字"，而是断言**这个数字来自事实**：
// 有 active 凭据的上游 → 候选有支撑；没有凭据的上游 → 候选没支撑；
// 停用的上游 → 没支撑；relay → 有支撑（Go 侧数不出账号池规模，但"有一个
// provider 入口"是事实，记 0 等于谎报"没有"）。
//
// 跑真 SQLite：凭据↔上游的边是 upstream_secrets（凭据的 secret_ref 存密文
// 行 id 的十进制字符串），照 internal/upstream/contribution.go 的落库顺序建。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// newCandidateInstanceTestApp 起一个最小可测 App（真 SQLite + 真主密钥文件）。
func newCandidateInstanceTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Store.Path = filepath.Join(dir, "c.db")
	cfg.Store.MasterKeyFile = filepath.Join(dir, "c.key")
	cfg.Sidecar.Enabled = false
	a, err := Setup(context.Background(), Options{Config: cfg})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// seedCandidateChain 建「上游 → 真实模型 → 抽象模型 → 候选」这条链路。
//
// 返回抽象模型 id 与真实模型行 id（都是 UUID 文本）。
func seedCandidateChain(t *testing.T, a *App, abstractName, upstreamModel string) (string, string) {
	t.Helper()
	ctx := context.Background()
	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "selfhosted", "u-"+abstractName,
		"https://example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	rmID, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID:      up,
		UpstreamModelID: upstreamModel,
	})
	if err != nil {
		t.Fatalf("建真实模型失败: %v", err)
	}
	amID, err := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name:           abstractName,
		DisplayName:    abstractName,
		Status:         store.ModelActive,
		CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	if err := a.store.Repos().Models.SetCandidates(ctx, amID, []store.Candidate{
		{AbstractModelID: amID, RealModelID: rmID, Weight: 100},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}
	return amID, rmID
}

// seedActiveCredential 给某个上游建一条 status=active 的贡献凭据。
//
// 落库顺序照 contribution.go：upstreams 行 → upstream_secrets（带 upstream_id）
// → upstream_credentials（secret_ref = 密文行 id 本身，即 UUID 文本）。
// 少任何一步都接不上「凭据↔上游」这条唯一的边，聚合就会认为这个上游没有凭据。
func seedActiveCredential(t *testing.T, a *App, upstreamID string, provider string) {
	t.Helper()
	ctx := context.Background()
	acctID, err := a.store.Repos().Accounts.CreateAccount(ctx, store.AccountAuth{
		Account:      store.Account{DisplayName: "贡献者", Username: "contrib-" + provider},
		PasswordHash: "!contributor-no-password",
	}, []store.Class{store.ClassContributor})
	if err != nil {
		t.Fatalf("建贡献者账户失败: %v", err)
	}
	secretID, err := a.store.Repos().Secrets.PutSecret(ctx, upstreamID, []byte("sk-test-key"))
	if err != nil {
		t.Fatalf("写密文失败: %v", err)
	}
	credID, err := a.store.Repos().Models.CreateCredential(ctx, acctID, provider,
		"fp-"+provider, "标签", secretID)
	if err != nil {
		t.Fatalf("建凭据失败: %v", err)
	}
	if err := a.store.Repos().Models.SetCredentialStatus(ctx, credID, store.CredActive); err != nil {
		t.Fatalf("置凭据 active 失败: %v", err)
	}
}

// idcodeAbs 现在是恒等：桥对外的 id 就是库内主键（UUID 文本），不再有
// am_<n> 这层编码（2026-10-26 UUID 改造）。函数保留只为少改调用点。
func idcodeAbs(t *testing.T, id string) string {
	t.Helper()
	return id
}

// TestContractCandidateInstancesFollowCredentials 钉住「候选的可用实例数来自
// 凭据事实」：有 active 凭据的上游上的候选 → 有支撑；没有凭据的 → 没支撑。
func TestContractCandidateInstancesFollowCredentials(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()

	// 有凭据的那条链路。
	amID, _ := seedCandidateChain(t, a, "with-cred", "m1")
	// 无凭据的那条链路。
	amIDNoCred, _ := seedCandidateChain(t, a, "no-cred", "m2")

	// 找到「with-cred」那条链路上游的 id：上游名由 seedCandidateChain 拼成 "u-<名>"。
	ups, err := a.store.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		t.Fatalf("列上游失败: %v", err)
	}
	var withCredUp string
	for _, u := range ups {
		if u.Name == "u-with-cred" {
			withCredUp = u.ID
		}
	}
	if withCredUp == "" {
		t.Fatalf("没找到 u-with-cred 上游，现有：%+v", ups)
	}
	seedActiveCredential(t, a, withCredUp, "api:deepseek-com")

	mb := &modelsBridge{b: newAdminBridge(a.store)}
	items, err := mb.ListCandidates(ctx, idcodeAbs(t, amID))
	if err != nil {
		t.Fatalf("列候选失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应有 1 条候选，得 %d", len(items))
	}
	if items[0].AvailableInstances != 1 {
		t.Fatalf("有 1 条 active 凭据时可用实例应为 1，实得 %d（恒 0 就是那个静默故障）",
			items[0].AvailableInstances)
	}

	// 无凭据的那条：必须是 0，且这与"有凭据"那���形成对照——不是所有候选都恒 0。
	itemsNo, err := mb.ListCandidates(ctx, idcodeAbs(t, amIDNoCred))
	if err != nil {
		t.Fatalf("列候选失败: %v", err)
	}
	if len(itemsNo) != 1 || itemsNo[0].AvailableInstances != 0 {
		t.Fatalf("无凭据时可用实例应为 0，实得 %+v", itemsNo)
	}
}

// TestContractAvailableCandidateCountCountsSupported 钉住摘要字段
// available_candidate_count：它数的是「有凭据支撑的候选数」，不是候选条数。
//
// 为什么这条要单独钉：列表页把它渲染成 "候选 / 可用" 徽标，available=0 时
// 徽标变黄并显示"一个都不可用"。它曾是硬编码 0，于是无论配得多好，列表页
// 都显示全灭。
func TestContractAvailableCandidateCountCountsSupported(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()

	amID, _ := seedCandidateChain(t, a, "counted", "m1")
	amIDNo, _ := seedCandidateChain(t, a, "uncounted", "m2")

	ups, err := a.store.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		t.Fatalf("列上游失败: %v", err)
	}
	var up string
	for _, u := range ups {
		if u.Name == "u-counted" {
			up = u.ID
		}
	}
	if up == "" {
		t.Fatalf("没找到 u-counted 上游")
	}
	seedActiveCredential(t, a, up, "api:deepseek-com")

	mb := &modelsBridge{b: newAdminBridge(a.store)}
	m, err := mb.GetAbstractModel(ctx, idcodeAbs(t, amID))
	if err != nil {
		t.Fatalf("读抽象模型失败: %v", err)
	}
	if m.CandidateCount != 1 {
		t.Fatalf("候选数应为 1，实得 %d", m.CandidateCount)
	}
	if m.AvailableCandidateCount != 1 {
		t.Fatalf("有凭据支撑时可用候选应为 1，实得 %d", m.AvailableCandidateCount)
	}

	mNo, err := mb.GetAbstractModel(ctx, idcodeAbs(t, amIDNo))
	if err != nil {
		t.Fatalf("读抽象模型失败: %v", err)
	}
	if mNo.CandidateCount != 1 || mNo.AvailableCandidateCount != 0 {
		t.Fatalf("无凭据时应为 候选 1 / 可用 0，实得 %d / %d",
			mNo.CandidateCount, mNo.AvailableCandidateCount)
	}
}

// TestContractDisabledUpstreamHasNoInstances 钉住「停用的上游不提供实例」：
// 停用是确定结论（一条也展不开），不是「不知道」。
func TestContractDisabledUpstreamHasNoInstances(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()

	amID, _ := seedCandidateChain(t, a, "stopped", "m1")
	ups, err := a.store.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		t.Fatalf("列上游失败: %v", err)
	}
	var up string
	for _, u := range ups {
		if u.Name == "u-stopped" {
			up = u.ID
		}
	}
	if up == "" {
		t.Fatalf("没找到 u-stopped 上游")
	}
	seedActiveCredential(t, a, up, "api:deepseek-com")
	// 停用上游（enabled=0）。
	if err := a.store.Repos().Models.UpdateUpstream(ctx, up, "u-stopped", "https://example.com/v1", false); err != nil {
		t.Fatalf("停用上游失败: %v", err)
	}

	mb := &modelsBridge{b: newAdminBridge(a.store)}
	items, err := mb.ListCandidates(ctx, idcodeAbs(t, amID))
	if err != nil {
		t.Fatalf("列候选失败: %v", err)
	}
	if len(items) != 1 || items[0].AvailableInstances != 0 {
		t.Fatalf("停用上游的候选可用实例应为 0，实得 %+v", items)
	}
}
