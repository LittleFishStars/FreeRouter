package app

// 贡献流程的端到端契约测试（M8）：真 SQLite + 真 AES-GCM + 真 HTTP 探测（httptest）。
//
// 为什么这一层必须用真库（而不是 upstream 包里的 fake）：
//   - 「加密入库无明文」只能靠读**库文件字节**来证明——fake 里没有加密；
//   - 「撤销真的把凭据移出候选」依赖 upstreams.enabled 与外键/唯一约束的真实行为；
//   - 「连续失败禁用」要确认写回的是 upstream_credentials.status 而不是内存状态。
// upstream/contribution_test.go 覆盖判定与编排，这里覆盖「落库后到底发生了什么」。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// newContributionTestApp 起一个最小可测 App（内存目录 SQLite + 真主密钥文件）。
func newContributionTestApp(t *testing.T, logBuf *bytes.Buffer) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Store.Path = filepath.Join(dir, "t.db")
	cfg.Store.MasterKeyFile = filepath.Join(dir, "m.key")
	cfg.Sidecar.Enabled = false // 贡献链路与 sidecar 无关

	opts := Options{Config: cfg}
	if logBuf != nil {
		opts.Logger = slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	a, err := Setup(context.Background(), opts)
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// newContributorAccount 建一个 contributor 类账户（匿名贡献的最终形态：零权限容器）。
// otherAccount 现建一个与 owner 无关的 contributor 账户，用于越权断言。
// UUID 主键下不能再靠 owner+N 造"另一个账户"。
func otherAccount(t *testing.T, a *App) string {
	t.Helper()
	seqNo++
	return newContributorAccount(t, a, fmt.Sprintf("other-contrib-%d", seqNo))
}

var seqNo int

func newContributorAccount(t *testing.T, a *App, username string) string {
	t.Helper()
	id, err := a.store.Repos().Accounts.CreateAccount(context.Background(), store.AccountAuth{
		// 匿名贡献者没有密码（01-flow 2.2：contributor 类没有密码，只有恢复码）；
		// 列是 NOT NULL，所以放一个哨兵值，任何登录尝试都不会命中任何哈希。
		Account:      store.Account{DisplayName: "匿名贡献者", Username: username},
		PasswordHash: "!contributor-no-password",
	}, []store.Class{store.ClassContributor})
	if err != nil {
		t.Fatalf("建 contributor 账户失败: %v", err)
	}
	return id
}

// newCatalogServer 起一个只提供 /v1/models 的 OpenAI 兼容端点。
func newCatalogServer(t *testing.T, models []string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		type item struct {
			ID string `json:"id"`
		}
		var data []item
		for _, m := range models {
			data = append(data, item{ID: m})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestContractContributionAnonymousEncrypted 覆盖验收三条：
// 匿名贡献可提交 → 入池 → 加密入库（库里搜不到明文）→ 解密可用。
func TestContractContributionAnonymousEncrypted(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	owner := newContributorAccount(t, a, "anonymous-contrib-1")
	srv := newCatalogServer(t, []string{"m1", "m2"}, http.StatusOK)

	const key = "sk-live-secret-abcdef1234567890" // 唯一串，便于全库字节搜索
	res, err := a.Contribution().Submit(ctx, upstream.Submission{
		OwnerAccountID: owner,
		Provider:       "demo-provider",
		BaseURL:        srv.URL + "/v1",
		APIKey:         key,
		ClaimedModels:  []string{"m1"},
		Label:          "我的魔搭 key",
	})
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if !res.InPool || res.Status != store.CredActive || res.VerifyStatus != store.VerifyVerified {
		t.Fatalf("应入池 verified，得 %+v", res)
	}

	// 凭据行：secret_ref 必须是引用（十进制密文 id），不是明文。
	cred, err := a.store.Repos().Models.LookupCredential(ctx, res.CredentialID)
	if err != nil {
		t.Fatalf("读凭据行失败：%v", err)
	}
	if strings.Contains(cred.VerifyDetail, key) {
		t.Fatalf("证据里出现明文 key：%s", cred.VerifyDetail)
	}
	if cred.ExternalLabel != "我的魔搭 key" {
		t.Fatalf("标签不应被丢弃：%q", cred.ExternalLabel)
	}

	// 入池 = 上游行 enabled；否则「凭据说 active 但候选里没有」是假状态。
	up := contributeUpstreamOf(t, a, "demo-provider", cred)
	if !up.Enabled {
		t.Error("入池后上游行应 enabled=true")
	}
	if up.OwnerAccountID == nil || *up.OwnerAccountID != owner {
		t.Error("上游行必须记贡献归属（owner_account_id）")
	}

	// 解密往返：密文必须能用主密钥解回原值（这正是 vendor 出站要用的路径）。
	plain, err := a.store.Repos().Secrets.GetSecretByUpstream(ctx, up.ID)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	if string(plain) != key {
		t.Fatalf("解密值与提交值不一致（长度 %d vs %d）", len(plain), len(key))
	}
	// secretSource 走的是「引用 → 解密」这条正式路径：same key 必须能出来。
	// SecretID 是指针：解引用后才是密文行 id（与 contribution.go 写入口径一致）
	tok, err := a.secretSource()(ctx, fmt.Sprint(*up.SecretID))
	if err != nil {
		t.Fatalf("secretSource 失败：%v", err)
	}
	if tok != key {
		t.Fatalf("secretSource 解出的值不一致")
	}

	// —— 加密入库无明文（验收第 3 条，code-review 复验后重写）——
	//
	// ⚠️ 为什么必须连 -wal/-shm 一起搜：连接开着时，最新写入**还在 WAL 里**
	// （store 用 WAL 模式；migrate.go:236 自己就写了「WAL 下直接 cp 主库会拿到不含
	// 最新提交的快照」）。只读主库文件会得到一个 4KB 的库头，任何断言都恒真——
	// 这就是这条用例上一版的问题：加密实现被改坏它也照样通过。
	// 所以先搜（主库+wal+shm），再 Close() 触发 checkpoint(TRUNCATE) 后重搜一遍，
	// 并用「同一块字节里必须能搜到标签」做**正向对照**，证明扫描不是空快照。
	cfg := a.cfg
	assertNoSecretInStoreFiles(t, cfg.Store.Path, key)

	// 关闭 store 触发 checkpoint：之后所有已提交数据都在主库里，扫描才覆盖全量。
	if err := a.store.Close(); err != nil {
		t.Fatalf("关闭 store 失败：%v", err)
	}
	mainDB, wal, shm := readStoreFiles(t, cfg.Store.Path)
	if len(mainDB) < 64<<10 {
		t.Fatalf("checkpoint 后主库只有 %d 字节，扫描仍可能是空快照", len(mainDB))
	}
	// 正向对照：标签必须真的在库字节里（证明下面「搜不到明文」不是搜了个空文件）。
	if !bytes.Contains(append(append(mainDB, wal...), shm...), []byte("我的魔搭 key")) {
		t.Fatal("库里搜不到我们自己写的标签——扫描目标不对，加密断言不可信")
	}
	if bytes.Contains(append(append(mainDB, wal...), shm...), []byte(key)) {
		t.Fatal("checkpoint 后库文件里出现明文 key——加密入库被破坏")
	}
	// 主密钥同理不得出现在库里（05-storage 8.2：锁和钥匙不能放同一抽屉）。
	if mk, err := os.ReadFile(cfg.Store.MasterKeyFile); err == nil && len(mk) > 0 {
		if bytes.Contains(mainDB, mk) || bytes.Contains(wal, mk) || bytes.Contains(shm, mk) {
			t.Fatal("库文件里出现了主密钥——加密等于裸存")
		}
	}
}

// readStoreFiles 读主库与 WAL/SHM（存在才读）。
//
// 为什么不假设 -wal 一定在：checkpoint(TRUNCATE) 之后 WAL 会被清空/删除；
// 「存在即读、不存在算空」才能在两种时序下都给出正确结论。
func readStoreFiles(t *testing.T, dbPath string) (main, wal, shm []byte) {
	t.Helper()
	var err error
	main, err = os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("读主库失败：%v", err)
	}
	wal, _ = os.ReadFile(dbPath + "-wal")
	shm, _ = os.ReadFile(dbPath + "-shm")
	return main, wal, shm
}

// assertNoSecretInStoreFiles 断言三处字节里都没有给定密钥。
func assertNoSecretInStoreFiles(t *testing.T, dbPath, secret string) {
	t.Helper()
	if where, size := findSecretInStoreFiles(dbPath, secret); where != "" {
		t.Fatalf("%s 里出现了明文密钥（%d 字节）——加密入库被破坏", where, size)
	}
}

// findSecretInStoreFiles 返回第一个含明文的文件（main/wal/shm）与它的字节数；
// where 为空串表示未命中。
//
// 单独抽出来的理由：扫描器本身要能被测试（见 TestContractStoreByteScanCoversWAL）——
// 否则「断言恒真」这类问题只会从一种形态变成另一种形态。
func findSecretInStoreFiles(dbPath, secret string) (where string, size int) {
	for _, name := range []string{"main", "wal", "shm"} {
		path := dbPath
		switch name {
		case "wal":
			path = dbPath + "-wal"
		case "shm":
			path = dbPath + "-shm"
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 {
			continue // 不存在/空文件：该形态在这台机器上没出现，不算命中
		}
		if bytes.Contains(b, []byte(secret)) {
			return path, len(b)
		}
	}
	return "", 0
}

// contributeUpstreamOf 找到贡献凭据对应的上游行（名字是确定性的，见 contribution.go）。
//
// 为什么不用 secret_ref：store.Credential 的读模型**有意不含** secret_ref
// （引用不进通用读模型）。要验解密往返，走「凭据 → 确定性上游名 → 上游密文」这条公开路径，
// 顺带也验证了「撤销靠名字反查上游」这个设计本身成立。
func contributeUpstreamOf(t *testing.T, a *App, provider string, cred *store.Credential) store.Upstream {
	t.Helper()
	want := fmt.Sprintf("%s-contrib-%s", provider, cred.ExternalID[:8])
	ups, err := a.store.Repos().Models.ListUpstreams(context.Background())
	if err != nil {
		t.Fatalf("列上游失败：%v", err)
	}
	for _, u := range ups {
		if u.Name == want {
			return u
		}
	}
	t.Fatalf("找不到贡献上游行 %q", want)
	return store.Upstream{}
}

// TestContractContributionRevokeReal 撤销的真效果：状态 disabled + 上游行停用 + 密文被墓碑替换。
func TestContractContributionRevokeReal(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	owner := newContributorAccount(t, a, "anonymous-contrib-2")
	srv := newCatalogServer(t, []string{"m1"}, http.StatusOK)

	const key = "sk-revoke-me-abcdef1234567890"
	res, err := a.Contribution().Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "p", BaseURL: srv.URL + "/v1",
		APIKey: key, ClaimedModels: []string{"m1"},
	})
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}

	// 越权撤销：另一个账户不行（01-flow 里的承诺是「自己的凭据随时可收回」，
	// 不是「任何人的都能收」）。id 是 UUID，不能再靠 owner+1000 造"另一个账户"，
	// 真建一个。
	other := newContributorAccount(t, a, "anonymous-contrib-2-other")
	if other == owner {
		t.Fatal("两个账户的 id 撞了")
	}
	if err := a.Contribution().Revoke(ctx, res.CredentialID, upstream.RevokeActor{AccountID: other}); err == nil {
		t.Fatal("他人撤销应被拒绝")
	}

	if err := a.Contribution().Revoke(ctx, res.CredentialID, upstream.RevokeActor{AccountID: owner}); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}

	cred, err := a.store.Repos().Models.LookupCredential(ctx, res.CredentialID)
	if err != nil {
		t.Fatalf("撤销后仍应能读行（归因链不能断）：%v", err)
	}
	if cred.Status != string(store.CredDisabled) {
		t.Errorf("撤销后状态应为 disabled，得 %s", cred.Status)
	}

	up := contributeUpstreamOf(t, a, "p", cred)
	if up.Enabled {
		t.Error("撤销后上游行必须 enabled=false（否则凭据仍在候选里）")
	}

	// 密钥材料被墓碑替换：解密能成功（是合法密文），但值不再是原 key。
	plain, err := a.store.Repos().Secrets.GetSecretByUpstream(ctx, up.ID)
	if err != nil {
		t.Fatalf("撤销后密文行应仍可解（墓碑），得错误：%v", err)
	}
	if string(plain) == key {
		t.Fatal("撤销后仍能解出原 key——密钥材料没有被销毁")
	}
	if !strings.HasPrefix(string(plain), "revoked:") {
		t.Errorf("墓碑形态应可识别，得 %q", plain)
	}
}

// TestContractContributionFailureDisablesReal 连续失败写回真库（不是内存假状态）。
func TestContractContributionFailureDisablesReal(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	owner := newContributorAccount(t, a, "anonymous-contrib-3")
	srv := newCatalogServer(t, []string{"m1"}, http.StatusOK)
	res, err := a.Contribution().Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "p", BaseURL: srv.URL + "/v1",
		APIKey: "sk-flaky-abcdef123456", ClaimedModels: []string{"m1"},
	})
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}

	// 用阈值 2 的服务，避免跑 5 次。
	svc := upstream.NewContributionService(contributionStore{repos: a.store.Repos()}, nil,
		upstream.ContributionOptions{FailThreshold: 2})
	if dis, n, err := svc.RecordFailure(ctx, res.CredentialID, "上游 502"); err != nil || dis || n != 1 {
		t.Fatalf("第 1 次失败不应禁用：dis=%v n=%d err=%v", dis, n, err)
	}
	dis, n, err := svc.RecordFailure(ctx, res.CredentialID, "上游 502: Bearer sk-leak-abcdefgh123")
	if err != nil || !dis || n != 2 {
		t.Fatalf("第 2 次失败应禁用：dis=%v n=%d err=%v", dis, n, err)
	}

	cred, err := a.store.Repos().Models.LookupCredential(ctx, res.CredentialID)
	if err != nil {
		t.Fatalf("读凭据失败：%v", err)
	}
	if cred.Status != string(store.CredDisabled) {
		t.Errorf("连续失败后库里状态应为 disabled，得 %s", cred.Status)
	}
	if cred.VerifyStatus != store.VerifySuspect {
		t.Errorf("连续失败应留 suspect 证据（复检不达标的口径），得 %s", cred.VerifyStatus)
	}
	if strings.Contains(cred.VerifyDetail, "sk-leak-abcdefgh123") {
		t.Errorf("失败证据泄露了密钥：%s", cred.VerifyDetail)
	}
}

// TestContractSecretSourceWiring 覆盖代码评审一般 6：secretSource 真的解密，
// 且三种异常各自报错（不再静默返回空串）。
func TestContractSecretSourceWiring(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	src := a.secretSource()

	// 空引用 = 无鉴权 selfhosted（合法形态，不报错）。
	if tok, err := src(ctx, ""); err != nil || tok != "" {
		t.Fatalf("空引用应返回空 token 且不报错，得 %q/%v", tok, err)
	}

	// 先建一条上游 + 密文，模拟贡献流程落库后的状态。
	upID, err := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "wire-test", "https://api.example.com/v1", nil, true)
	if err != nil {
		t.Fatalf("建上游失败：%v", err)
	}
	secretID, err := a.store.Repos().Secrets.PutSecret(ctx, upID, []byte("sk-wired-secret-123456"))
	if err != nil {
		t.Fatalf("写密文失败：%v", err)
	}
	tok, err := src(ctx, fmt.Sprint(secretID))
	if err != nil || tok != "sk-wired-secret-123456" {
		t.Fatalf("应解出明文，得 %q / %v", tok, err)
	}

	// 引用形态不认识：报错（旧实现是静默空串，会把配置问题伪装成上游 401）。
	if _, err := src(ctx, "secret:abc"); err == nil {
		t.Error("非法引用应报错")
	}
	// 引用不存在：报错（配置缺失，凭据应标 invalid）。
	if _, err := src(ctx, "999999"); err == nil {
		t.Error("不存在的密文 id 应报错")
	}
}

// TestContractCredentialWiringAudit 覆盖「凭据链路未接」的显式告警。
//
// 判定逻辑（credentialWiringIssues）是纯函数：用构造的 Upstream 行覆盖三种形态
// （无引用 / 引用可解 / 引用解不开），不依赖真库的悬空外键；
// 再用真 App 的日志验证「有 broken 时确实 Warn 出来」。
func TestContractCredentialWiringAudit(t *testing.T) {
	ctx := context.Background()
	id1, id2, id3 := "sec-1", "sec-2", "sec-3"
	ups := []store.Upstream{
		{ID: "up-10", Kind: "vendor", Name: "no-secret", SecretID: nil}, // 无鉴权：不计入
		{ID: "up-11", Kind: "selfhosted", Name: "local", SecretID: nil}, // 无鉴权：不计入
		{ID: "up-12", Kind: "relay", Name: "buddy", SecretID: &id1},     // relay 的凭据在 sidecar 池里：不计入
		{ID: "up-13", Kind: "vendor", Name: "ok", SecretID: &id2},       // 有引用且解得到
		{ID: "up-14", Kind: "vendor", Name: "broken", SecretID: &id3},   // 有引用但解不到
	}
	// 这个自检只看「声明了 secret_id 的上游能否解到密文行」，id 只是行标识的
	// 载体，用可读字符串即可（不经过 idgen 校验面）。
	has := func(_ context.Context, upstreamID string) (bool, error) {
		return upstreamID == "up-13", nil
	}
	need, wired, broken := credentialWiringIssues(ctx, ups, has, true)
	if need != 2 || wired != 1 || broken != 1 {
		t.Fatalf("自检计数应为 need=2 wired=1 broken=1，得 %d/%d/%d", need, wired, broken)
	}
	// 主密钥不可用（Store.Close 会抹零）→ 全部 broken。
	if _, _, b := credentialWiringIssues(ctx, ups, has, false); b != 2 {
		t.Fatalf("主密钥不可用时两个声明了密钥的上游都应算 broken，得 %d", b)
	}
	// 全是无鉴权 selfhosted：need=0，不该告警。
	if n, _, _ := credentialWiringIssues(ctx, ups[:2], has, true); n != 0 {
		t.Fatalf("无引用上游不应计入，得 need=%d", n)
	}
	if !allZero(make([]byte, 32)) || allZero([]byte{0, 1}) {
		t.Fatal("allZero 判定错误（它决定「主密钥是否可用」）")
	}

	// 日志侧：文案本身就是运维契约（grep「凭据链路未接」就能发现静默降级）。
	var buf bytes.Buffer
	a := newContributionTestApp(t, &buf)
	buf.Reset()
	a.logCredentialWiring(2, 1, 1) // 有 broken
	if !strings.Contains(buf.String(), "凭据链路未接") {
		t.Fatalf("broken>0 时必须 Warn「凭据链路未接」，实际：%s", buf.String())
	}
	buf.Reset()
	a.logCredentialWiring(2, 2, 0) // 全部接线
	if strings.Contains(buf.String(), "凭据链路未接") {
		t.Fatalf("接线正常时不应告警，实际：%s", buf.String())
	}
	buf.Reset()
	a.logCredentialWiring(0, 0, 0) // 没有需要凭据的上游
	if buf.Len() != 0 {
		t.Fatalf("无需凭据时不应有任何输出，实际：%s", buf.String())
	}
}

// TestContractStoreByteScanCoversWAL 是扫描器自身的守卫：明文藏在 -wal 里必须被发现。
//
// 为什么需要它：上一版「全库字节断言」只读主库，而 WAL 模式下写入还在 -wal 里，
// 于是断言恒真、实现被改坏也抓不到。修法不能只是「顺手把 -wal 也读一下」——
// 还要有一条用例钉住「读 WAL」这件事本身，否则将来重构又会悄悄退回去。
func TestContractStoreByteScanCoversWAL(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "planted.db")
	const secret = "sk-planted-secret-abcdef123456"
	if err := os.WriteFile(dbPath, []byte("clean-main-db"), 0o600); err != nil {
		t.Fatalf("写主库占位失败：%v", err)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("x"+secret+"y"), 0o600); err != nil {
		t.Fatalf("写 WAL 占位失败：%v", err)
	}
	where, size := findSecretInStoreFiles(dbPath, secret)
	if where != dbPath+"-wal" || size == 0 {
		t.Fatalf("扫描器漏掉了 WAL 里的明文（where=%q size=%d）——断言会变成假绿", where, size)
	}
	// 反向：干净文件不误报。
	if where, _ := findSecretInStoreFiles(dbPath, "definitely-not-present"); where != "" {
		t.Fatalf("干净数据被误报为泄露：%s", where)
	}
}

// TestContractContributionPauseResumeReal 覆盖暂停/恢复的真效果（契约 v2 §5）。
//
// 守三件事，缺一条这个功能就是错的：
//  1. 落点确实是 upstreams.enabled —— 候选池的唯一入口（与撤销同一个开关）；
//  2. 暂停**不动密钥材料**（密文还能解回原 key）—— 它和撤销的区别就在这里；
//  3. 未通过鉴定的凭据不能被「恢复」进池 —— 否则一次点击就能把坏 key 放回候选。
func TestContractContributionPauseResumeReal(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	owner := newContributorAccount(t, a, "anonymous-contrib-pause")
	svc := a.Contribution()

	srv := newCatalogServer(t, []string{"m1"}, http.StatusOK)
	const key = "sk-pause-me-abcdef1234567890"
	res, err := svc.Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "demo-provider", BaseURL: srv.URL + "/v1",
		APIKey: key, ClaimedModels: []string{"m1"},
	})
	if err != nil || !res.InPool {
		t.Fatalf("前置提交应入池：err=%v res=%+v", err, res)
	}
	cred, err := a.store.Repos().Models.LookupCredential(ctx, res.CredentialID)
	if err != nil {
		t.Fatalf("读凭据失败：%v", err)
	}
	if up := contributeUpstreamOf(t, a, "demo-provider", cred); !up.Enabled {
		t.Fatal("入池后上游行应 enabled=true")
	}

	// —— 暂停 ——
	callable, err := svc.SetPaused(ctx, res.CredentialID, upstream.RevokeActor{AccountID: owner}, true)
	if err != nil || callable {
		t.Fatalf("暂停应成功且返回不可调用：callable=%v err=%v", callable, err)
	}
	up := contributeUpstreamOf(t, a, "demo-provider", cred)
	if up.Enabled {
		t.Error("暂停后上游行必须 enabled=false（否则凭据仍在候选里）")
	}
	// 凭据本身仍是 active：暂停是运行时开关，不是生命周期状态。
	if got, _ := a.store.Repos().Models.LookupCredential(ctx, res.CredentialID); got.Status != string(store.CredActive) {
		t.Errorf("暂停不应改凭据状态（应保持 active），得 %s", got.Status)
	}
	// 密钥材料还在：与撤销（写墓碑）的区别就在这里。
	if plain, err := a.store.Repos().Secrets.GetSecretByUpstream(ctx, up.ID); err != nil || string(plain) != key {
		t.Errorf("暂停不应销毁密钥材料：err=%v value=%q", err, plain)
	}

	// —— 幂等：重复暂停 ——
	if callable, err := svc.SetPaused(ctx, res.CredentialID, upstream.RevokeActor{AccountID: owner}, true); err != nil || callable {
		t.Fatalf("重复暂停应幂等（仍不可调用）：callable=%v err=%v", callable, err)
	}
	if up := contributeUpstreamOf(t, a, "demo-provider", cred); up.Enabled {
		t.Error("重复暂停后上游行应仍为 enabled=false")
	}

	// —— 非属主 ——
	if _, err := svc.SetPaused(ctx, res.CredentialID, upstream.RevokeActor{AccountID: otherAccount(t, a)}, true); !errors.Is(err, upstream.ErrContributionForbidden) {
		t.Fatalf("他人操作应被拒绝（ErrContributionForbidden），得 %v", err)
	}

	// —— 恢复 ——
	callable, err = svc.SetPaused(ctx, res.CredentialID, upstream.RevokeActor{AccountID: owner}, false)
	if err != nil || !callable {
		t.Fatalf("恢复应成功且返回可调用：callable=%v err=%v", callable, err)
	}
	if up := contributeUpstreamOf(t, a, "demo-provider", cred); !up.Enabled {
		t.Error("恢复后上游行必须 enabled=true")
	}

	// —— 未通过鉴定的凭据恢复不起来 ——
	// 否则「恢复」就是一条把没验过的 key 放回候选池的捷径。
	badSrv := newCatalogServer(t, nil, http.StatusUnauthorized)
	bad, err := svc.Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "demo-provider-bad", BaseURL: badSrv.URL + "/v1",
		APIKey: "sk-bad-key-abcdef1234567890",
	})
	if err != nil || bad.InPool {
		t.Fatalf("401 的 key 不应入池：err=%v res=%+v", err, bad)
	}
	badCred, err := a.store.Repos().Models.LookupCredential(ctx, bad.CredentialID)
	if err != nil {
		t.Fatalf("读被拒凭据失败：%v", err)
	}
	callable, err = svc.SetPaused(ctx, bad.CredentialID, upstream.RevokeActor{AccountID: owner}, false)
	if err != nil || callable {
		t.Fatalf("未通过鉴定的凭据不应被恢复成可调用：callable=%v err=%v", callable, err)
	}
	if up := contributeUpstreamOf(t, a, "demo-provider-bad", badCred); up.Enabled {
		t.Error("被拒凭据的上游行必须保持 enabled=false")
	}
}

// TestContractMyCredentialsBridgeState 钉住「我的贡献」页依赖的桥状态计算
// （2026-10-31）：桥必须用确定性命名的上游行回答「这条凭据是否被属主停止」，
// 与撤销/暂停共用同一份命名事实（upstream.ContributeUpstreamName）。
//
// 用真库的原因与文件头一致：暂停/入池的落点都是 upstreams.enabled，
// fake 里没有这个行为，只有真库能证明「暂停后列表立刻反映为已停止」。
func TestContractMyCredentialsBridgeState(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	owner := newContributorAccount(t, a, "anonymous-contrib-bridge")
	svc := a.Contribution()
	bridge := newContributionBridge(a.store, svc, 10)

	srv := newCatalogServer(t, []string{"m1"}, http.StatusOK)
	res, err := svc.Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "demo-bridge", BaseURL: srv.URL + "/v1",
		APIKey: "sk-bridge-state-abcdef123456", ClaimedModels: []string{"m1"},
	})
	if err != nil || !res.InPool {
		t.Fatalf("前置提交应入池：err=%v res=%+v", err, res)
	}

	assertRow := func(wantPaused bool, phase string) {
		t.Helper()
		rows, err := bridge.CredentialsOf(ctx, owner)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s：读桥行失败 err=%v n=%d", phase, err, len(rows))
		}
		row := rows[0]
		if row.Provider != "demo-bridge" {
			t.Errorf("%s：provider 应原样透传，得 %q", phase, row.Provider)
		}
		if !row.UpstreamFound {
			t.Errorf("%s：确定性命名的上游行应能反查到", phase)
		}
		if row.Paused != wantPaused {
			t.Errorf("%s：paused = %v，期望 %v", phase, row.Paused, wantPaused)
		}
	}

	// 入池初始态：贡献中。
	assertRow(false, "入池后")

	// 暂停后：同一行立刻读出「已停止」——页面上的停止/恢复按钮以此为据。
	if _, err := svc.SetPaused(ctx, res.CredentialID, upstream.RevokeActor{AccountID: owner}, true); err != nil {
		t.Fatalf("暂停失败：%v", err)
	}
	assertRow(true, "暂停后")

	// 恢复后回到贡献中。
	if _, err := svc.SetPaused(ctx, res.CredentialID, upstream.RevokeActor{AccountID: owner}, false); err != nil {
		t.Fatalf("恢复失败：%v", err)
	}
	assertRow(false, "恢复后")

	// 判定未通过（从未入池）的凭据：上游行存在但 enabled=false。桥如实报
	// paused=true，但这类行的 status 不是 active——页面按 status 显示
	// 「鉴定未通过」，不会把它冒充成属主自己停掉的。
	badSrv := newCatalogServer(t, nil, http.StatusUnauthorized)
	bad, err := svc.Submit(ctx, upstream.Submission{
		OwnerAccountID: owner, Provider: "demo-bridge-bad", BaseURL: badSrv.URL + "/v1",
		APIKey: "sk-bridge-bad-abcdef1234567",
	})
	if err != nil || bad.InPool {
		t.Fatalf("401 的 key 不应入池：err=%v res=%+v", err, bad)
	}
	rows, err := bridge.CredentialsOf(ctx, owner)
	if err != nil || len(rows) != 2 {
		t.Fatalf("读两行失败 err=%v n=%d", err, len(rows))
	}
	for i := range rows {
		if rows[i].ID == bad.CredentialID {
			if !rows[i].UpstreamFound || !rows[i].Paused {
				t.Errorf("被拒凭据应报 found+paused（上游行在且停用），得 %+v", rows[i])
			}
			if rows[i].Status == "active" {
				t.Error("被拒凭据的 status 不应是 active（页面据此区分「你停的」与「没通过」）")
			}
		}
	}
}

// TestContractOAuthNamedUpstreamReachable 钉住 2026-10-31 的双命名修复：
// OAuth 反代凭据的上游行按 "<provider>-oauth-<指纹前8>" 落库（app/oauth_codearts.go
// persist），与 API/custom 提交链路的 "-contrib-" 命名并存。撤销/暂停（findContributeUpstream）
// 与账户「我的贡献」页的反查必须两种都认——只认 contrib 时，反代凭据会被显示成
// 「未挂载」，且暂停/恢复对它们静默无效（真库实测）。
//
// 用真库的原因：暂停的落点是 upstreams.enabled，fake 里没有这个行为；
// 这里同时验证读面（桥找到行）与写面（SetPaused 真的翻开关）。
func TestContractOAuthNamedUpstreamReachable(t *testing.T) {
	a := newContributionTestApp(t, nil)
	ctx := context.Background()
	owner := newContributorAccount(t, a, "anonymous-contrib-oauthname")
	svc := a.Contribution()
	bridge := newContributionBridge(a.store, svc, 10)

	// 直接按 OAuth 落库形态造数据：上游行 -oauth- 命名 + 同指纹的凭据行。
	const externalID = "key_oauthface1234567890" // 前 8 位进名字
	uid, err := a.store.Repos().Models.UpsertUpstream(ctx, "vendor",
		"demooauth-oauth-key_oaut", "https://gw.example/v1", &owner, true)
	if err != nil {
		t.Fatalf("建 oauth 命名上游行失败：%v", err)
	}
	cid, err := a.store.Repos().Models.CreateCredential(ctx, owner, "demooauth",
		externalID, "demooauth OAuth", "secret-row-1")
	if err != nil {
		t.Fatalf("建凭据行失败：%v", err)
	}
	if err := a.store.Repos().Models.SetCredentialStatus(ctx, cid, store.CredActive); err != nil {
		t.Fatalf("置 active 失败：%v", err)
	}

	// 读面：桥必须通过 oauth 命名找到这行，报「在池、未停止」。
	rows, err := bridge.CredentialsOf(ctx, owner)
	if err != nil || len(rows) != 1 {
		t.Fatalf("读桥行失败 err=%v n=%d", err, len(rows))
	}
	if !rows[0].UpstreamFound || rows[0].Paused {
		t.Fatalf("oauth 命名上游行应可反查且未停止，得 found=%v paused=%v",
			rows[0].UpstreamFound, rows[0].Paused)
	}

	// 写面：暂停必须真的翻 upstreams.enabled（修复前这里是查无此行的 no-op）。
	callable, err := svc.SetPaused(ctx, cid, upstream.RevokeActor{AccountID: owner}, true)
	if err != nil || callable {
		t.Fatalf("暂停应成功且返回不可调用：callable=%v err=%v", callable, err)
	}
	up := upstreamRowByID(t, a, uid)
	if up.Enabled {
		t.Fatal("暂停后上游行必须 enabled=false")
	}
	rows, _ = bridge.CredentialsOf(ctx, owner)
	if !rows[0].Paused {
		t.Error("暂停后桥应报 paused=true（页面据此显示「已停止贡献」）")
	}

	// 恢复对称成立。
	if callable, err := svc.SetPaused(ctx, cid, upstream.RevokeActor{AccountID: owner}, false); err != nil || !callable {
		t.Fatalf("恢复应成功且返回可调用：callable=%v err=%v", callable, err)
	}
	if up := upstreamRowByID(t, a, uid); !up.Enabled {
		t.Error("恢复后上游行必须 enabled=true")
	}
}

// upstreamRowByID 按 id 取上游行（测试辅助；GetUpstreamByID 是包内小写，跨包只能遍历）。
func upstreamRowByID(t *testing.T, a *App, id string) store.Upstream {
	t.Helper()
	ups, err := a.store.Repos().Models.ListUpstreams(context.Background())
	if err != nil {
		t.Fatalf("列上游失败：%v", err)
	}
	for _, u := range ups {
		if u.ID == id {
			return u
		}
	}
	t.Fatalf("找不到上游行 %s", id)
	return store.Upstream{}
}
