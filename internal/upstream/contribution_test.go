package upstream

// 贡献流程的契约测试（M8）：判定表、提交流程、撤销、连续失败禁用。
//
// 本文件用**内存 fake** 隔离存储与网络，只验「编排与判定」；
// 「密文真的不落明文」「撤销真的把上游移出候选」这类断言需要真 SQLite + AES-GCM，
// 放在 internal/app/upstream_shim_test.go（同一批用例名前缀 TestContractContribution*）。
//
// 为什么分两层：判定表是纯函数，值得用 30 行表驱动穷举；而加密/外键/唯一约束的行为
// 只由真库决定——用 fake 模拟这些等于把「我以为 store 怎么工作」写进测试。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// ———— 判定表 ————

func TestContractContributionJudgeProbe(t *testing.T) {
	cases := []struct {
		name    string
		claimed string
		out     ProbeOutput
		err     error
		wantSt  store.CredentialStatus
		wantVer store.VerifyStatus
	}{
		{
			name: "连不上 → 拒绝（L0 不通过）",
			out:  ProbeOutput{Reachable: false, Detail: "dial tcp: connection refused"},
			err:  nil,
			// 端点都不通，谈不上「声称是否相符」，所以 verify 仍是 unverified
			wantSt: store.CredDisabled, wantVer: store.VerifyUnverified,
		},
		{
			name:    "401 → 拒绝",
			claimed: "m1",
			out:     ProbeOutput{Reachable: true, AuthOK: false, Detail: "HTTP 401"},
			wantSt:  store.CredDisabled, wantVer: store.VerifyUnverified,
		},
		{
			name:    "声称模型命中 → 入池 verified",
			claimed: "m1",
			out:     ProbeOutput{Reachable: true, AuthOK: true, Models: []string{"m1", "m2"}, UsedModels: []string{"m1"}},
			wantSt:  store.CredActive, wantVer: store.VerifyVerified,
		},
		{
			name:    "有目录但声称模型缺失 → 拒绝 suspect",
			claimed: "gpt-4",
			out:     ProbeOutput{Reachable: true, AuthOK: true, Models: []string{"m1", "m2"}},
			wantSt:  store.CredDisabled, wantVer: store.VerifySuspect,
		},
		{
			name:    "无目录（冷门）→ 放行 unverified",
			claimed: "cold-model",
			out:     ProbeOutput{Reachable: true, AuthOK: true},
			wantSt:  store.CredActive, wantVer: store.VerifyUnverified,
		},
		{
			name: "未声明模型且鉴权通过 → 放行 unverified",
			out:  ProbeOutput{Reachable: true, AuthOK: true, Models: []string{"m1"}},
			// 没声称就没有「相符」可判：不因为没声明而拒绝（贡献门槛要低到「任何人」）
			wantSt: store.CredActive, wantVer: store.VerifyUnverified,
		},
		{
			name:    "探测自身失败（非法 URL）→ 拒绝，证据脱敏",
			claimed: "m1",
			err:     errors.New("构造探测请求失败：parse url"),
			wantSt:  store.CredDisabled, wantVer: store.VerifyUnverified,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ver, ev := judgeProbe(tc.claimed, tc.out, tc.err)
			if st != tc.wantSt || ver != tc.wantVer {
				t.Fatalf("判定 = (%s,%s)，want (%s,%s)；evidence=%s", st, ver, tc.wantSt, tc.wantVer, ev)
			}
			if !strings.HasPrefix(ev, "{") {
				t.Errorf("证据应是 JSON，得 %q", ev)
			}
			// 证据里不得出现"我们怎么探的"（05-verification 5.3 脱敏要求）。
			if strings.Contains(ev, "UsedModels") || strings.Contains(ev, "Authorization") {
				t.Errorf("证据泄露探针细节：%s", ev)
			}
		})
	}
}

func TestContractContributionFingerprintAndValidation(t *testing.T) {
	// 指纹稳定、不可逆、不同 key 不同指纹。
	a, b := KeyFingerprint("sk-aaaa1111"), KeyFingerprint("sk-aaaa1111")
	if a != b {
		t.Fatal("同一 key 的指纹必须稳定（幂等判定依赖它）")
	}
	if a == KeyFingerprint("sk-bbbb2222") {
		t.Fatal("不同 key 不得得到同一指纹")
	}
	if strings.Contains(a, "sk-aaaa1111") {
		t.Fatal("指纹不得包含明文")
	}

	// L0 本地校验：结构性垃圾必须被挡，且都是「用户能看懂」的原因。
	cases := []struct {
		name string
		sub  Submission
	}{
		{"无归属账户", Submission{Provider: "p", BaseURL: "https://x/v1", APIKey: "sk-123456789"}},
		{"provider 空", Submission{OwnerAccountID: 1, BaseURL: "https://x/v1", APIKey: "sk-123456789"}},
		{"URL 不合法", Submission{OwnerAccountID: 1, Provider: "p", BaseURL: "://x", APIKey: "sk-123456789"}},
		{"key 为空", Submission{OwnerAccountID: 1, Provider: "p", BaseURL: "https://x/v1"}},
		{"key 太短", Submission{OwnerAccountID: 1, Provider: "p", BaseURL: "https://x/v1", APIKey: "short"}},
		{"标签含明文 key", Submission{OwnerAccountID: 1, Provider: "p", BaseURL: "https://x/v1", APIKey: "sk-123456789", Label: "我的 sk-123456789"}},
	}
	for _, tc := range cases {
		if err := validateSubmission(tc.sub); err == nil {
			t.Errorf("%s：应被 L0 拒绝", tc.name)
		}
	}
	ok := Submission{OwnerAccountID: 1, Provider: "p", BaseURL: "https://api.example.com/v1", APIKey: "sk-1234567890", Label: "我的魔搭 key"}
	if err := validateSubmission(ok); err != nil {
		t.Fatalf("合法提交被误杀：%v", err)
	}
	// 公网明文 http 必须走不出去（凭据会裸奔在链路上）。
	svc := NewContributionService(newFakeContributionStore(), nil, ContributionOptions{})
	if _, err := svc.Submit(context.Background(), Submission{
		OwnerAccountID: 1, Provider: "p", BaseURL: "http://api.example.com/v1", APIKey: "sk-1234567890",
	}); err == nil {
		t.Fatal("公网 http 端点应被出站边界拒绝")
	}
	// 回环 http 是合法 selfhosted。
	if kind := defaultKindFor("http://127.0.0.1:11434/v1"); kind != string(KindSelfHosted) {
		t.Errorf("回环应判 selfhosted，得 %s", kind)
	}
	if kind := defaultKindFor("https://api.example.com/v1"); kind != string(KindVendor) {
		t.Errorf("公网应判 vendor，得 %s", kind)
	}
}

// ———— 编排（fake store + fake probe） ————

func TestContractContributionSubmitFlow(t *testing.T) {
	ctx := context.Background()
	fs := newFakeContributionStore()
	svc := NewContributionService(fs, fakeProbe{out: ProbeOutput{
		Reachable: true, AuthOK: true,
		Models: []string{"m1"}, UsedModels: []string{"m1"},
	}}, ContributionOptions{})

	res, err := svc.Submit(ctx, Submission{
		OwnerAccountID: 7, Provider: "demo-provider", BaseURL: "https://api.example.com/v1",
		APIKey: "sk-secret-value-123", ClaimedModel: "m1", Label: "我的 key",
	})
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if !res.InPool || res.Status != store.CredActive || res.VerifyStatus != store.VerifyVerified {
		t.Fatalf("判定应为入池 verified，得 %+v", res)
	}
	// 密文只经 PutSecret 落库一次；凭据行拿到的是**十进制密文 id**（不是明文）。
	if len(fs.secretPlaintext) != 1 || fs.secretPlaintext[0] != "sk-secret-value-123" {
		t.Fatalf("应把明文交给 PutSecret 加密，实际 %v", fs.secretPlaintext)
	}
	if fs.lastSecretRef == "sk-secret-value-123" || strings.Contains(fs.lastSecretRef, "sk-") {
		t.Fatalf("secret_ref 不得是明文，得 %q", fs.lastSecretRef)
	}
	if _, ok := parseSecretRefDecimal(fs.lastSecretRef); !ok {
		t.Fatalf("secret_ref 应是十进制 id，得 %q", fs.lastSecretRef)
	}
	// 入池才启用上游行（01-flow 5.2「落库与可调用分开」）。
	if !fs.upstreamEnabled[1] {
		t.Error("入池后上游行应 enabled=true")
	}
	// 未过判定的凭据绝不能可调用：建行时必须是 disabled。
	if !strings.Contains(fs.trace, "upsert enabled=false") {
		t.Errorf("上游行应先以 disabled 建：%s", fs.trace)
	}

	// 幂等：同账户同 key 再提交一次 → 复用同一行，不新建。
	before := len(fs.credentials)
	res2, err := svc.Submit(ctx, Submission{
		OwnerAccountID: 7, Provider: "demo-provider", BaseURL: "https://api.example.com/v1",
		APIKey: "sk-secret-value-123", ClaimedModel: "m1",
	})
	if err != nil {
		t.Fatalf("幂等提交失败：%v", err)
	}
	if !res2.Reused || res2.CredentialID != res.CredentialID {
		t.Fatalf("同 key 重复提交应幂等复用，得 %+v（原 id=%d）", res2, res.CredentialID)
	}
	if len(fs.credentials) != before {
		t.Error("幂等路径不得新建凭据行")
	}
}

func TestContractContributionRejectKeepsEvidence(t *testing.T) {
	ctx := context.Background()
	fs := newFakeContributionStore()
	svc := NewContributionService(fs, fakeProbe{out: ProbeOutput{
		Reachable: true, AuthOK: true, Models: []string{"m1"}, // 声称的 gpt-4 不在目录
	}}, ContributionOptions{})

	res, err := svc.Submit(ctx, Submission{
		OwnerAccountID: 9, Provider: "fake", BaseURL: "https://api.example.com/v1",
		APIKey: "sk-not-gpt4-key", ClaimedModel: "gpt-4",
	})
	if err != nil {
		t.Fatalf("拒绝不是 error：%v", err)
	}
	if res.InPool || res.Status != store.CredDisabled || res.VerifyStatus != store.VerifySuspect {
		t.Fatalf("应拒绝为 disabled/suspect，得 %+v", res)
	}
	// 拒绝也保留行与证据（02-credentials 7 节：它是申诉与惯犯识别的证据）。
	if len(fs.credentials) != 1 {
		t.Fatal("拒绝的凭据行必须保留")
	}
	if !strings.Contains(res.Evidence, "不在上游目录") {
		t.Errorf("证据应可执行（说明为什么被拒）：%s", res.Evidence)
	}
	// 被拒的凭据不得可调用：上游行保持 disabled。
	if fs.upstreamEnabled[1] {
		t.Error("被拒凭据的上游行必须保持 disabled")
	}
}

func TestContractContributionRevoke(t *testing.T) {
	ctx := context.Background()
	fs := newFakeContributionStore()
	svc := NewContributionService(fs, fakeProbe{out: ProbeOutput{
		Reachable: true, AuthOK: true, Models: []string{"m1"}, UsedModels: []string{"m1"},
	}}, ContributionOptions{})
	res, err := svc.Submit(ctx, Submission{
		OwnerAccountID: 3, Provider: "p", BaseURL: "https://api.example.com/v1",
		APIKey: "sk-revoke-me-1234", ClaimedModel: "m1",
	})
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}

	// 越权撤销必须被拒（别人不能收回你的凭据）。
	if err := svc.Revoke(ctx, res.CredentialID, RevokeActor{AccountID: 99}); !errors.Is(err, ErrContributionForbidden) {
		t.Fatalf("他人撤销应 ErrContributionForbidden，得 %v", err)
	}

	if err := svc.Revoke(ctx, res.CredentialID, RevokeActor{AccountID: 3}); err != nil {
		t.Fatalf("本人撤销失败：%v", err)
	}
	// 三件事都要发生：状态 disabled、上游行停用（唯一能移出候选的开关）、密文被替换。
	if fs.credentials[res.CredentialID].Status != string(store.CredDisabled) {
		t.Error("撤销后凭据应为 disabled")
	}
	if fs.upstreamEnabled[1] {
		t.Error("撤销后上游行必须 enabled=false（否则凭据仍能被调用）")
	}
	if fs.secretPlaintext[len(fs.secretPlaintext)-1] == "sk-revoke-me-1234" {
		t.Error("撤销应把密钥材料替换成墓碑，而不是原样保留")
	}
	// 重复撤销幂等（不报错）。
	if err := svc.Revoke(ctx, res.CredentialID, RevokeActor{AccountID: 3}); err != nil {
		t.Errorf("重复撤销应幂等，得 %v", err)
	}
}

func TestContractContributionFailureDisables(t *testing.T) {
	ctx := context.Background()
	fs := newFakeContributionStore()
	svc := NewContributionService(fs, fakeProbe{out: ProbeOutput{
		Reachable: true, AuthOK: true, Models: []string{"m1"}, UsedModels: []string{"m1"},
	}}, ContributionOptions{FailThreshold: 3})
	res, err := svc.Submit(ctx, Submission{
		OwnerAccountID: 5, Provider: "p", BaseURL: "https://api.example.com/v1",
		APIKey: "sk-flaky-key-12345", ClaimedModel: "m1",
	})
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}

	// 前两次不触发（单次失败可能只是抖动）。
	for i := 1; i <= 2; i++ {
		disabled, n, err := svc.RecordFailure(ctx, res.CredentialID, "上游 502")
		if err != nil || disabled || n != i {
			t.Fatalf("第 %d 次失败不应禁用：disabled=%v n=%d err=%v", i, disabled, n, err)
		}
	}
	// 成功会清零（健康复位）。
	svc.RecordSuccess(res.CredentialID)
	if got := svc.Failures(res.CredentialID); got != 0 {
		t.Fatalf("成功后计数应清零，得 %d", got)
	}

	// 连续 3 次 → 自动禁用并留证据。
	for i := 1; i <= 3; i++ {
		disabled, n, err := svc.RecordFailure(ctx, res.CredentialID, "上游 502: Bearer sk-leak-abcdefgh123")
		if err != nil {
			t.Fatalf("第 %d 次失败：%v", i, err)
		}
		if i < 3 && disabled {
			t.Fatalf("第 %d 次就禁用了（阈值 3）", i)
		}
		if i == 3 {
			if !disabled || n != 3 {
				t.Fatalf("第 3 次应禁用，得 disabled=%v n=%d", disabled, n)
			}
		}
	}
	if fs.credentials[res.CredentialID].Status != string(store.CredDisabled) {
		t.Error("连续失败阈值后凭据应 disabled")
	}
	// 证据里的原因必须脱敏（失败原因常带上游回显的 key 前缀）。
	if strings.Contains(fs.credentials[res.CredentialID].VerifyDetail, "sk-leak-abcdefgh123") {
		t.Errorf("失败证据泄露了密钥：%s", fs.credentials[res.CredentialID].VerifyDetail)
	}
	if !strings.Contains(fs.credentials[res.CredentialID].VerifyDetail, "连续失败 3 次") {
		t.Errorf("证据应写明失败次数：%s", fs.credentials[res.CredentialID].VerifyDetail)
	}
}

// ———— fake ————

// fakeProbe 固定返回一份探测结果（判定表用表驱动，编排用固定值）。
type fakeProbe struct{ out ProbeOutput }

func (p fakeProbe) Probe(context.Context, ProbeInput) (ProbeOutput, error) { return p.out, nil }

// fakeContributionStore 是内存实现：记录每次调用的关键形态，便于断言「服务做了什么」。
type fakeContributionStore struct {
	mu              sync.Mutex
	upstreamEnabled map[int64]bool
	upstreams       map[int64]store.Upstream
	credentials     map[int64]*store.Credential
	secretPlaintext []string
	lastSecretRef   string
	nextID          int64
	trace           string
}

func newFakeContributionStore() *fakeContributionStore {
	return &fakeContributionStore{
		upstreamEnabled: map[int64]bool{},
		upstreams:       map[int64]store.Upstream{},
		credentials:     map[int64]*store.Credential{},
	}
}

func (f *fakeContributionStore) id() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeContributionStore) UpsertUpstream(_ context.Context, kind, name, baseURL string, owner *int64, enabled bool) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trace += fmt.Sprintf("upsert enabled=%v; ", enabled)
	// 按 (kind,name) 幂等，模拟 store 的 UNIQUE(kind,name)
	for id, u := range f.upstreams {
		if u.Kind == kind && u.Name == name {
			u.BaseURL = baseURL
			u.Enabled = enabled
			f.upstreams[id] = u
			f.upstreamEnabled[id] = enabled
			return id, nil
		}
	}
	id := f.id()
	f.upstreams[id] = store.Upstream{ID: id, Kind: kind, Name: name, BaseURL: baseURL, Enabled: enabled}
	f.upstreamEnabled[id] = enabled
	return id, nil
}

func (f *fakeContributionStore) PutSecret(_ context.Context, upstreamID int64, plaintext []byte) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secretPlaintext = append(f.secretPlaintext, string(plaintext))
	id := f.id()
	f.lastSecretRef = fmt.Sprintf("%d", id)
	return id, nil
}

func (f *fakeContributionStore) DeleteSecret(_ context.Context, secretID int64) error { return nil }

func (f *fakeContributionStore) CreateCredential(_ context.Context, owner int64, provider, externalID, label, secretRef string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, c := range f.credentials {
		if c.Provider == provider && c.ExternalID == externalID {
			return id, fmt.Errorf("UNIQUE constraint failed: %s/%s", provider, externalID)
		}
	}
	id := f.id()
	f.credentials[id] = &store.Credential{
		ID: id, OwnerAccountID: owner, Provider: provider, ExternalID: externalID,
		ExternalLabel: label, Status: string(store.CredPending), VerifyStatus: store.VerifyUnverified,
	}
	return id, nil
}

func (f *fakeContributionStore) SetCredentialStatus(_ context.Context, id int64, status store.CredentialStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.credentials[id]
	if !ok {
		return store.ErrCredentialNotFound
	}
	c.Status = string(status)
	return nil
}

func (f *fakeContributionStore) MarkVerified(_ context.Context, id int64, status store.VerifyStatus, evidence string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.credentials[id]
	if !ok {
		return store.ErrCredentialNotFound
	}
	c.VerifyStatus = status
	c.VerifyDetail = evidence
	return nil
}

func (f *fakeContributionStore) LookupCredential(_ context.Context, id int64) (*store.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.credentials[id]
	if !ok {
		return nil, store.ErrCredentialNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *fakeContributionStore) ListCredentials(_ context.Context, filter store.CredentialFilter) ([]store.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Credential
	for _, c := range f.credentials {
		if filter.OwnerAccountID != nil && c.OwnerAccountID != *filter.OwnerAccountID {
			continue
		}
		if filter.Provider != "" && c.Provider != filter.Provider {
			continue
		}
		out = append(out, *c)
	}
	return out, nil
}

func (f *fakeContributionStore) ListUpstreams(context.Context) ([]store.Upstream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Upstream
	for _, u := range f.upstreams {
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeContributionStore) UpdateUpstream(_ context.Context, id int64, name, baseURL string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.upstreams[id]
	if !ok {
		return store.ErrNotFound
	}
	u.Name, u.BaseURL, u.Enabled = name, baseURL, enabled
	f.upstreams[id] = u
	f.upstreamEnabled[id] = enabled
	return nil
}

// parseSecretRefDecimal 是测试侧对引用形态的独立断言（不复用被测实现的解析器，
// 否则「解析器坏了」与「引用写错了」会同时通过）。
func parseSecretRefDecimal(ref string) (int64, bool) {
	if ref == "" {
		return 0, false
	}
	var n int64
	for _, c := range ref {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, n > 0
}

// TestContractRedactionSingleSource 钉住「Go 侧只有一份遮蔽模式」（code-review 建议 ④）。
//
// 背景：同一组正则原先在 dshprovider/client.go 与 contribution.go 各写一份、内容逐字相同，
// 加上 sidecar/redact.mjs 共三处。复制的那份一旦漂移，表现是「一边遮、一边漏」且没人发现。
// 现在 contribution 的 redactText 是转调 dshprovider.RedactLine，本用例做两件事：
//  1. 相等性：同一输入两边输出必须一致（防止有人又在本地写第二份正则）；
//  2. 行为锚点：证据（verify_detail）里的错误码不被抹掉——这是 M8 联调最需要的字段。
func TestContractRedactionSingleSource(t *testing.T) {
	corpus := []string{
		"Authorization: Bearer sk-live-abcdefgh12345678",
		"Authorization: Basic dXNlcjpwYXNzd29yZA==",
		"GET /cb?code=abcd1234efghijkl&state=x",
		`{"access_token":"ya29.A0AfH6SMBx"}`,
		`{"error_code":"upstream_unavailable","message":"上游 502"}`,
		"upstream_error code=no_candidate",
		"code=E_DEPENDENCY_MISSING",
		"password=hunter2hunter2",
		"普通文本：provider=demo-provider 装载成功",
	}
	for _, in := range corpus {
		if got, want := redactText(in), dshprovider.RedactLine(in); got != want {
			t.Errorf("两份实现漂移：\n  in   = %q\n  local= %q\n  hub  = %q", in, got, want)
		}
	}
	// 行为锚点：探测失败的原因里带错误码时，证据必须保留「为什么」。
	ev := evidenceJSON("L0", "端点不可达：error_code=upstream_unavailable detail=no_candidate")
	for _, keep := range []string{"upstream_unavailable", "no_candidate"} {
		if !strings.Contains(ev, keep) {
			t.Errorf("证据把排障字段抹掉了：缺 %q，实际 %s", keep, ev)
		}
	}
	// 反向：真凭据形态必须被遮（否则这条测试只是「都在做无用功」）。
	ev = evidenceJSON("L0", "上游拒绝鉴权（401/403）：Bearer sk-live-abcdefgh12345678")
	if strings.Contains(ev, "sk-live-abcdefgh12345678") {
		t.Errorf("证据泄露了密钥：%s", ev)
	}
}
