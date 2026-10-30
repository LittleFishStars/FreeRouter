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
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
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
	items, err := mb.ListCandidates(ctx, amID)
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
	itemsNo, err := mb.ListCandidates(ctx, amIDNoCred)
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
	m, err := mb.GetAbstractModel(ctx, amID)
	if err != nil {
		t.Fatalf("读抽象模型失败: %v", err)
	}
	if m.CandidateCount != 1 {
		t.Fatalf("候选数应为 1，实得 %d", m.CandidateCount)
	}
	if m.AvailableCandidateCount != 1 {
		t.Fatalf("有凭据支撑时可用候选应为 1，实得 %d", m.AvailableCandidateCount)
	}

	mNo, err := mb.GetAbstractModel(ctx, amIDNo)
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
	items, err := mb.ListCandidates(ctx, amID)
	if err != nil {
		t.Fatalf("列候选失败: %v", err)
	}
	if len(items) != 1 || items[0].AvailableInstances != 0 {
		t.Fatalf("停用上游的候选可用实例应为 0，实得 %+v", items)
	}
}

// TestContractSourceMergeByProvider 钉住「来源合并」在真库上的聚合形状
// （2026-10-27 用户决定：相同来源的不同凭据合并，左栏不再一行一把 key）。
//
// 聚合键是凭据行的 provider 列（契约 §4 的来源口径）；模型并集按
// upstream_model_id 去重——同 provider 不同 key 的目录高度重叠，逐行复制
// 会让模型计数翻倍。
func TestContractSourceMergeByProvider(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()

	// 同一 provider（api:deepseek-com）贡献两把不同的 key → 两条上游行 + 两条凭据。
	// key 不同（指纹不同）是关键：同 key 会命中幂等不建行。
	up1, err := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "api:deepseek-com-contrib-key_aaaa1111",
		"https://api.deepseek.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游 1 失败: %v", err)
	}
	up2, err := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "api:deepseek-com-contrib-key_bbbb2222",
		"https://api.deepseek.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游 2 失败: %v", err)
	}
	// 各挂一条 active 凭据（provider 列同值 → 聚合成一个来源）。
	for i, up := range []string{up1, up2} {
		sec, err := a.store.Repos().Secrets.PutSecret(ctx, up, []byte("sk-merge-test-0123456789"))
		if err != nil {
			t.Fatalf("写密文 %d 失败: %v", i, err)
		}
		cid, err := a.store.Repos().Models.CreateCredential(ctx, mustAccount2(t, a), "api:deepseek-com",
			fmt.Sprintf("fp-merge-%d", i), "凭据", sec)
		if err != nil {
			t.Fatalf("建凭据 %d 失败: %v", i, err)
		}
		if err := a.store.Repos().Models.SetCredentialStatus(ctx, cid, store.CredActive); err != nil {
			t.Fatalf("置 active %d 失败: %v", i, err)
		}
	}
	// 各挂一条同名模型（目录重叠）+ 一条独有模型：并集去重后应为 2 条。
	for _, up := range []string{up1, up2} {
		if _, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
			UpstreamID: up, UpstreamModelID: "deepseek-chat", DisplayName: "DeepSeek Chat",
		}); err != nil {
			t.Fatalf("落模型失败: %v", err)
		}
	}
	if _, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: up2, UpstreamModelID: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner",
	}); err != nil {
		t.Fatalf("落独有模型失败: %v", err)
	}

	mb := &modelsBridge{b: newAdminBridge(a.store)}
	srcs, _, err := mb.ListModelSources(ctx)
	if err != nil {
		t.Fatalf("列来源失败: %v", err)
	}
	var merged *httpapi.AdminModelSource
	for i := range srcs {
		if srcs[i].ProviderID == "deepseek-com" {
			merged = &srcs[i]
			break
		}
	}
	if merged == nil {
		t.Fatalf("应有一条 provider=api:deepseek-com 的合并来源，实际：%+v", srcs)
	}
	// 两把 key 的两条上游行合并成一条来源。
	if len(merged.UpstreamIDs) != 2 {
		t.Errorf("合并来源应含 2 个成员上游，得 %v", merged.UpstreamIDs)
	}
	// 凭据数合并（2 条 active）。
	if merged.CredentialCount != 2 {
		t.Errorf("凭据数应合并为 2，得 %d", merged.CredentialCount)
	}
	// 模型并集去重：deepseek-chat 两行重叠 + reasoner 独有 = 2 条，不是 3。
	if merged.ModelCount != 2 {
		t.Errorf("模型并集去重后应 2 条（重叠的 chat 不翻倍），得 %d", merged.ModelCount)
	}
	if len(merged.Models) != 2 {
		t.Errorf("模型清单应 2 条，得 %d", len(merged.Models))
	}
	// 右栏凭据列 = 该来源下全部凭据（两把 key 的都在）。
	if len(merged.Credentials) != 2 {
		t.Errorf("合并来源的凭据列应含两把 key 的凭据，得 %d", len(merged.Credentials))
	}
	// Name 走目录名（apidir 命中 api:deepseek-com），不带贡献指纹后缀。
	if strings.Contains(merged.Name, "contrib") {
		t.Errorf("合并来源的名称不应带贡献指纹（目录名是权威），得 %q", merged.Name)
	}
}

// mustAccount2 是来源合并测试用的账户（与 seedActiveCredential 的建账户同形，
// 独立出来是因为那个 helper 把账户+凭据+密文捆在一起，这里只要账户）。
func mustAccount2(t *testing.T, a *App) string {
	t.Helper()
	id, err := a.store.Repos().Accounts.CreateAccount(context.Background(), store.AccountAuth{
		Account:      store.Account{DisplayName: "贡献者", Username: fmt.Sprintf("contrib-merge-%d", time.Now().UnixNano())},
		PasswordHash: "!contributor-no-password",
	}, []store.Class{store.ClassContributor})
	if err != nil {
		t.Fatalf("建贡献者账户失败: %v", err)
	}
	return id
}

// TestContractRelaySourceDisplayName 钉住反代来源的展示名（2026-10-30 用户
// 决定）：上游行名是 "<provider>-oauth-<指纹>" 的机器形态，界面上只显示
// 凭据 provider 列的裸 id——与 API 供应商的目录展示名同待遇。
func TestContractRelaySourceDisplayName(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	ctx := context.Background()
	owner := mustAccount2(t, a)

	up, err := a.store.Repos().Models.UpsertUpstream(ctx, "relay", "qodercn-oauth-key_0a83",
		"sidecar://qodercn", &owner, true)
	if err != nil {
		t.Fatalf("建 relay 上游失败: %v", err)
	}
	sec, err := a.store.Repos().Secrets.PutSecret(ctx, up, []byte("sk-relay-display-0123456789"))
	if err != nil {
		t.Fatalf("写密文失败: %v", err)
	}
	cid, err := a.store.Repos().Models.CreateCredential(ctx, owner, "qodercn", "fp-relay-display-1", "qodercn OAuth", sec)
	if err != nil {
		t.Fatalf("建凭据失败: %v", err)
	}
	if err := a.store.Repos().Models.SetCredentialStatus(ctx, cid, store.CredActive); err != nil {
		t.Fatalf("置 active 失败: %v", err)
	}

	mb := &modelsBridge{b: newAdminBridge(a.store)}
	srcs, _, err := mb.ListModelSources(ctx)
	if err != nil {
		t.Fatalf("列来源失败: %v", err)
	}
	var relay *httpapi.AdminModelSource
	for i := range srcs {
		if srcs[i].OAuth && srcs[i].UpstreamName == "qodercn-oauth-key_0a83" {
			relay = &srcs[i]
			break
		}
	}
	if relay == nil {
		t.Fatalf("应有一条 oauth 来源（上行 qodercn-oauth-key_0a83），实际：%+v", srcs)
	}
	if relay.Name != "qodercn" {
		t.Errorf("反代来源展示名 = %q，期望裸 provider id \"qodercn\"（不带 -oauth-key 机器后缀）", relay.Name)
	}
	// 机器名仍保留在 UpstreamName 上：反查/编辑等需要真行名的地方不丢事实。
	if relay.UpstreamName != "qodercn-oauth-key_0a83" {
		t.Errorf("UpstreamName 应保留机器名，得 %q", relay.UpstreamName)
	}
}
