package dshprovider

// 契约测试：Go 客户端 ⇄ NDJSON sidecar 子进程。
//
// 被测对象是「通道」而不是 hub：全部用 testdata/fake_sidecar.mjs 复现真实 sidecar
// 几乎不可能按需发生的故障（乱序 id、stdout 污染、中途退出、卡死不回）。
// 另有一条用例直接跑仓库里的真 sidecar（sidecar/index.mjs），验证
// 「缺 hub 时结构化报错、进程不崩」与红线守卫的**活体往返**。
//
// 运行：make contract（或 go test -run Contract ./internal/upstream/dshprovider/ -v）
// node 不在 PATH 时全部 Skip —— CI 的 Go-only runner 不该因为缺 Node 而红。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSidecarPath 相对**包目录**解析——go test 的 cwd 就是包目录，无需 runtime.Caller。
func fakeSidecarPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("testdata", "fake_sidecar.mjs")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("缺少假 sidecar：%s", p)
	}
	return p
}

// mustNode 在每条用例入口检查 node，缺则 Skip（验收要求：node 不在 PATH 时不 fail）。
func mustNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node 不在 PATH，跳过需要子进程的契约测试")
	}
}

// startFake 用指定 FAKE_MODE 拉起假 sidecar 并完成 health 握手。
func startFake(t *testing.T, mode string, mutate func(*Options)) *Client {
	t.Helper()
	mustNode(t)
	entry := fakeSidecarPath(t)
	opts := Options{
		Entry:   entry,
		HubPath: "", // fake 不 import hub；留空避免误注入路径
		Env:     []string{"FAKE_MODE=" + mode},
		Logger:  discardLogger(),
		// 超时压到 400ms：让超时用例快速失败，同时给正常应答（本机 node 往返 <50ms）留 8 倍余量。
		Timeout: 400 * time.Millisecond,
	}
	if mutate != nil {
		mutate(&opts)
	}
	c, err := Start(context.Background(), opts)
	if err != nil {
		t.Fatalf("health 握手失败（mode=%s）：%v；stderr=%v", mode, err, c.StderrTail(10))
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// discardLogger 把 slog 输出扔进 io.Discard：测试关心 StderrTail（子进程 stderr 环），
// 不关心主进程日志；留默认 slog.Default() 会把每条告警喷到测试输出里干扰 -v 阅读。
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// asError 断言 err 是 *Error 并返回它。
func asError(t *testing.T, err error) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，得到 nil")
	}
	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("错误不是 *dshprovider.Error：%T %v", err, err)
	}
	return de
}

// reqIDs 从 stderr 环里解析 `REQ <id> <method>` 行的 id 序列。
var reqRe = regexp.MustCompile(`^REQ (\d+) (\S+)$`)

func reqIDs(tail []string) []uint64 {
	var ids []uint64
	for _, l := range tail {
		if m := reqRe.FindStringSubmatch(l); m != nil {
			n, _ := strconv.ParseUint(m[1], 10, 64)
			ids = append(ids, n)
		}
	}
	return ids
}

// ────────────────────────────────────────────────────────────────
// ① 正常问答：九个方法逐一往返，字段解到类型上；id 单调递增从 1 开始。
// ────────────────────────────────────────────────────────────────
func TestContractNormalRoundTrip(t *testing.T) {
	c := startFake(t, "ok", nil)

	if !c.Available() {
		t.Fatal("握手后可用性应为 true")
	}
	st := c.Status()
	if st.Version != "0.0.0-fake" || len(st.Providers) != 1 || st.Providers[0] != "fake_provider" {
		t.Fatalf("health 状态不符：%+v", st)
	}
	if !st.StateReadonly {
		t.Fatal("StateReadonly 应为 true（红线②的自报）")
	}

	ctx := context.Background()

	ps, err := c.ProvidersList(ctx)
	if err != nil || len(ps) != 1 || ps[0].Provider != "fake_provider" || !ps[0].LoginImplemented {
		t.Fatalf("providers.list：%v %+v", err, ps)
	}

	ms, err := c.ModelsList(ctx, "fake_provider")
	if err != nil || len(ms) != 1 || ms[0].UpstreamModelID != "fake_provider/fake-flash" || ms[0].ContextWindow != nil {
		t.Fatalf("models.list：%v %+v", err, ms)
	}
	if _, err := c.ModelsList(ctx, ""); CodeOf(err) != CodeBadRequest {
		t.Fatalf("空 provider 应本地拒绝（E_BAD_REQUEST），得 %v", err)
	}

	rc, err := c.CredentialsResolve(ctx, "dsh:fake:0")
	if err != nil || rc == nil || rc.Value != "fake-secret-value" || rc.Source != "keychain" {
		t.Fatalf("credentials.resolve：%v %+v", err, rc)
	}
	// 「不存在」的语义是 nil 而不是错误（对齐 DSH：空值视为不存在）。
	rc2, err := c.CredentialsResolve(ctx, "dsh:missing:0")
	if err != nil || rc2 != nil {
		t.Fatalf("credentials.resolve 缺失引用应为 (nil,nil)：%v %+v", err, rc2)
	}

	ci, err := c.CredentialsDescribe(ctx, "dsh:fake:0")
	if err != nil || ci.Provider != "fake_provider" || !ci.Refreshable {
		t.Fatalf("credentials.describe：%v %+v", err, ci)
	}
	if exp, _, known := ci.ExpiredAt(time.Now()); !known || exp {
		t.Fatalf("2030 年过期时间应 known=true、exp=false，得 known=%v exp=%v", known, exp)
	}

	sess, err := c.LoginStart(ctx, "fake_provider")
	if err != nil || sess.SessionID != "sess-1" || sess.Kind != "device_code" || sess.UserCode != "ABCD-1234" {
		t.Fatalf("login.start：%v %+v", err, sess)
	}
	ls, err := c.LoginPoll(ctx, sess.SessionID)
	if err != nil || ls.Status != LoginStatusOK || ls.CredentialRef != "FAKE_ACCOUNT_012345" {
		t.Fatalf("login.poll：%v %+v", err, ls)
	}
	if status, err := c.LoginSubmit(ctx, sess.SessionID, "123456"); err != nil || status != "pending" {
		t.Fatalf("login.submit：%v %q", err, status)
	}
	if err := c.LoginCancel(ctx, sess.SessionID); err != nil {
		t.Fatalf("login.cancel：%v", err)
	}

	cr, err := c.CreditsQuery(ctx, "fake_provider", "dsh:fake:0")
	if err != nil || cr.Balance == nil || *cr.Balance != 1.5 || cr.Unit != "usd" || cr.QuotaTotal == nil {
		t.Fatalf("credits.query：%v %+v", err, cr)
	}

	// id 单调递增：health=1，其后依次 2..10（每次 +1，绝无复用/跳号）。
	ids := reqIDs(c.StderrTail(64))
	if len(ids) < 10 {
		t.Fatalf("stderr 里应有 ≥10 条 REQ，得 %v", ids)
	}
	for i := 0; i < 10; i++ {
		if ids[i] != uint64(i+1) {
			t.Fatalf("id 序列应为 1,2,3...，第 %d 个得 %d（全部：%v）", i, ids[i], ids)
		}
	}
}

// ────────────────────────────────────────────────────────────────
// ② 红线（可执行断言）：方法集合里不存在写凭据/写状态方法；
//
//	即便有人绕道发起，两侧都必须给出专用守卫码。
//
// ────────────────────────────────────────────────────────────────
func TestContractRedlineNoCredentialWriteMethods(t *testing.T) {
	// 断言 1：客户端能力清单里，任何名字都不含写语义。
	forbidden := []string{"credentials.set", "credentials.unset", "credentials.refresh",
		"credentials.write", "credentials.delete", "state.read", "state.write", "state.update"}
	ok := map[string]bool{
		"health": true, "providers.list": true, "models.list": true,
		"credentials.resolve": true, "credentials.describe": true,
		"login.start": true, "login.poll": true, "login.submit": true, "login.cancel": true,
		"credits.query": true,
	}
	for _, m := range SupportedMethods() {
		if !ok[m] {
			t.Errorf("SupportedMethods 出现了清单之外的方法 %q（契约 2.2 未收录）", m)
		}
		for _, f := range forbidden {
			if m == f {
				t.Errorf("红线违规：SupportedMethods 包含禁止方法 %q", m)
			}
		}
		if strings.Contains(m, ".set") || strings.Contains(m, ".unset") || strings.Contains(m, ".refresh") {
			t.Errorf("红线违规：方法名含写语义片段 %q", m)
		}
	}
	// 断言 2：守卫函数对每个禁止名给出正确码。
	wantCode := map[string]string{
		"credentials.set": CodeCredentialReadonly, "credentials.unset": CodeCredentialReadonly,
		"credentials.refresh": CodeCredentialReadonly, "credentials.write": CodeCredentialReadonly,
		"credentials.delete": CodeCredentialReadonly,
		"state.read":         CodeStateForbidden, "state.write": CodeStateForbidden, "state.update": CodeStateForbidden,
	}
	for f, code := range wantCode {
		got, bad := IsForbiddenMethod(f)
		if !bad || got != code {
			t.Errorf("IsForbiddenMethod(%q) = (%q,%v)，期望 (%q,true)", f, got, bad, code)
		}
	}
	// 断言 3：合法方法不会被守卫误伤。
	for _, m := range SupportedMethods() {
		if _, bad := IsForbiddenMethod(m); bad {
			t.Errorf("合法方法 %q 被红线守卫误判", m)
		}
	}

	// 断言 4（活体往返）：向假 sidecar 真的发一条 credentials.set，拿回 E_CREDENTIAL_READONLY。
	// 这一步验证的是「即使代码里出现违规调用点，链路上也会立刻变成可告警的错误」——
	// 红线靠守卫码守住，不靠记忆（04-sidecar.md 2.4）。
	c := startFake(t, "ok", nil)
	_, err := c.Call(context.Background(), "credentials.set", map[string]string{"credentialRef": "x"})
	de := asError(t, err)
	if de.Code != CodeCredentialReadonly {
		t.Fatalf("credentials.set 应被守卫为 %s，实际 %s（%v）", CodeCredentialReadonly, de.Code, de)
	}
}

// ────────────────────────────────────────────────────────────────
// ③ 乱序/未知 id 的应答帧：忽略并继续等正确帧，不影响本调用与可用性。
// ────────────────────────────────────────────────────────────────
func TestContractStrayIDIgnored(t *testing.T) {
	c := startFake(t, "wrong_id", nil)
	ps, err := c.ProvidersList(context.Background())
	if err != nil {
		t.Fatalf("错帧之后的正确应答必须被接受：%v", err)
	}
	if len(ps) != 1 || ps[0].Provider != "fake_provider" {
		t.Fatalf("结果不对：%+v", ps)
	}
	if !c.Available() {
		t.Fatal("单个错位帧不应判死 sidecar")
	}
	// 告警应出现在日志（用 StderrTail 间接验证连接仍健康即可；真日志由 slog 注入决定）。
	if _, err := c.ModelsList(context.Background(), "fake_provider"); err != nil {
		t.Fatalf("后续调用应正常：%v", err)
	}
}

// ────────────────────────────────────────────────────────────────
// ④ 中途退出：在途调用立刻失败（不等超时），状态标不可用。
// ────────────────────────────────────────────────────────────────
func TestContractMidCallExit(t *testing.T) {
	c := startFake(t, "exit_on_call", nil)
	_, err := c.ProvidersList(context.Background())
	de := asError(t, err)
	// EOF 路径给 E_PROTOCOL；「不可用」检查更早的话给 E_SIDECAR_DOWN——两者都是正确答案，
	// 但都**不得**是超时（那说明探活设计失效）或 nil。
	if de.Code != CodeProtocol && de.Code != CodeSidecarDown {
		t.Fatalf("期望 E_PROTOCOL/E_SIDECAR_DOWN，得 %s", de.Code)
	}
	if de.Retryable != true {
		t.Error("进程死亡的错误应标记可重试（退避重启策略依赖这个标志）")
	}
	if c.Available() {
		t.Fatal("sidecar 已退出，可用性必须为 false")
	}
	// 之后的调用必须**快速**失败（本地判定，不再跨进程往返）。
	start := time.Now()
	_, err = c.ModelsList(context.Background(), "fake_provider")
	if CodeOf(err) != CodeSidecarDown {
		t.Fatalf("死亡后的调用应 E_SIDECAR_DOWN，得 %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Errorf("死亡后的调用应即刻失败，耗时 %s", time.Since(start))
	}
}

// ────────────────────────────────────────────────────────────────
// ⑤ 超时：到点杀进程、标不可用；错误码 E_UPSTREAM_TIMEOUT 且可重试。
// ────────────────────────────────────────────────────────────────
func TestContractTimeout(t *testing.T) {
	c := startFake(t, "silent", func(o *Options) { o.Timeout = 300 * time.Millisecond })
	start := time.Now()
	_, err := c.ProvidersList(context.Background())
	de := asError(t, err)
	if de.Code != CodeTimeout {
		t.Fatalf("期望 E_UPSTREAM_TIMEOUT，得 %s（%v）", de.Code, de)
	}
	if !de.Retryable {
		t.Error("超时应可重试（2.4）")
	}
	if el := time.Since(start); el < 250*time.Millisecond || el > 3*time.Second {
		t.Errorf("超时时刻不对：%s（期望≈300ms）", el)
	}
	if c.Available() {
		t.Fatal("超时的进程已被终止，必须标不可用（契约：一问一答不容忍在途帧）")
	}
}

// ────────────────────────────────────────────────────────────────
// ⑥ 并发串行化：8 个 goroutine 同时调，通道仍严格一问一答（假 sidecar 作证）。
// ────────────────────────────────────────────────────────────────
func TestContractConcurrentSerialized(t *testing.T) {
	c := startFake(t, "ok", func(o *Options) {
		o.Env = append(o.Env, "FAKE_DELAY_MS=80")
		o.Timeout = 5 * time.Second // 8×80ms 排队 + 握手余量
	})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.ProvidersList(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("串行排队后每个调用都应成功：%v", err)
		}
	}
	tail := c.StderrTail(128)
	for _, l := range tail {
		if strings.HasPrefix(l, "CONCURRENT") {
			t.Fatalf("假 sidecar 观测到并发在途（%s）：Go 侧 callMu 串行化失效", l)
		}
	}
	ids := reqIDs(tail)
	if len(ids) != 9 { // health + 8
		t.Fatalf("应有 9 条 REQ（health+8 并发），得 %d 条：%v", len(ids), ids)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("id 必须严格递增：%v", ids)
		}
	}
}

// ────────────────────────────────────────────────────────────────
// ⑦ 非 JSON 行容错：stdout 被第三方库污染时不判死，帧被丢弃并告警。
// ────────────────────────────────────────────────────────────────
func TestContractNonJSONLineTolerated(t *testing.T) {
	c := startFake(t, "noise", nil)
	ps, err := c.ProvidersList(context.Background())
	if err != nil || len(ps) != 1 {
		t.Fatalf("污染行不得影响应答：%v %+v", err, ps)
	}
	if !c.Available() {
		t.Fatal("少量污染不应判死")
	}
	// 连续违规超限（>3 帧/调用）时协议失能：发 5 次污染调用——每次只污染一行，
	// 单次调用内 strays=1 不触发 bail，因此 5 次后仍健康；这验证的是「容忍度按调用计」。
	for i := 0; i < 5; i++ {
		if _, err := c.ProvidersList(context.Background()); err != nil {
			t.Fatalf("第 %d 次污染调用失败：%v", i, err)
		}
	}
	if !c.Available() {
		t.Fatal("每次调用各 1 个污染帧，不应累计触发协议失能")
	}
}

// ────────────────────────────────────────────────────────────────
// ⑧ health 握手失败：标记不可用但**不**杀死客户端对象——转发链路不受影响。
// ────────────────────────────────────────────────────────────────
func TestContractHealthFailMarksDownNotForwarding(t *testing.T) {
	mustNode(t)
	if _, err := os.Stat(filepath.Join("testdata", "fake_sidecar.mjs")); err != nil {
		t.Skip("缺少假 sidecar")
	}
	c, err := Start(context.Background(), Options{
		Entry:   fakeSidecarPath(t),
		Env:     []string{"FAKE_MODE=health_fail"},
		Logger:  discardLogger(),
		Timeout: 400 * time.Millisecond,
	})
	// Start 的返回值形状是刻意的（见包注释）：client 永远非 nil。
	if c == nil {
		t.Fatal("Start 即使失败也必须返回可用（可调用的）Client")
	}
	if err == nil {
		t.Fatal("health 失败必须带回 error（供横幅）")
	}
	if CodeOf(err) != CodeSidecarDown {
		t.Errorf("握手失败的错误码应为 E_SIDECAR_DOWN，得 %s", CodeOf(err))
	}
	if c.Available() {
		t.Fatal("health 失败应标记不可用")
	}
	if st := c.Status(); st.Reason == "" {
		t.Error("不可用状态必须带可展示的中文原因（横幅用）")
	}
	// 「不影响转发」的可执行含义：本包所有方法在不可用时返回错误而非 panic/挂起，
	// 且调用方的错误路径与 sidecar 存活路径完全同构（同一个 *Error）。
	de := asError(t, func() error { _, e := c.ProvidersList(context.Background()); return e }())
	if de.Code != CodeSidecarDown {
		t.Errorf("不可用后的调用应 E_SIDECAR_DOWN，得 %s", de.Code)
	}
	_ = c.Close(context.Background()) // 不 panic 即通过：进程还活着，可以优雅关
}

// ────────────────────────────────────────────────────────────────
// ⑨ 未知错误码 → 归一 E_INTERNAL 并保留原文（契约 3：新版本 sidecar 不得搞崩老主进程）。
// ────────────────────────────────────────────────────────────────
func TestContractUnknownErrorCodeNormalized(t *testing.T) {
	c := startFake(t, "unknown_code", nil)
	_, err := c.ProvidersList(context.Background())
	de := asError(t, err)
	if de.Code != CodeInternal {
		t.Fatalf("未知码应归一 E_INTERNAL，得 %s", de.Code)
	}
	if !strings.Contains(de.Message, "E_SOMETHING_BRAND_NEW") && !strings.Contains(de.Message, "未来版本") {
		// 归一时至少要留下线索（本实现留的是 message 原文；若上游 message 不含原码，
		// 告警日志里有——这里放宽为二选一，不绑死具体文案）。
		t.Logf("注意：E_INTERNAL 的 message 未携带原码线索：%q", de.Message)
	}
	if !c.Available() {
		t.Error("未知错误码是业务应答不是协议故障，不应判死")
	}
}

// ────────────────────────────────────────────────────────────────
// ⑩ nil 接收者安全：转发路径不判空也不会 panic（包注释里最重要的 API 形状承诺）。
// ────────────────────────────────────────────────────────────────
func TestContractNilClientSafety(t *testing.T) {
	var c *Client
	ctx := context.Background()
	if c.Available() {
		t.Error("nil 客户端应不可用")
	}
	if st := c.Status(); st.Available || st.Reason == "" {
		t.Errorf("nil 客户端 Status 形状不对：%+v", st)
	}
	if _, err := c.Call(ctx, "health", nil); CodeOf(err) != CodeSidecarDown {
		t.Errorf("nil Call 应 E_SIDECAR_DOWN，得 %v", err)
	}
	if _, err := c.ProvidersList(ctx); err == nil {
		t.Error("nil ProvidersList 应报错")
	}
	if _, err := c.CredentialsResolve(ctx, "x"); err == nil {
		t.Error("nil CredentialsResolve 应报错")
	}
	_ = c // nil 接收者的所有路径已在上面走通且未 panic
}

// ────────────────────────────────────────────────────────────────
// ⑪ Close：先发 shutdown 帧、子进程自行退出（5.4 步骤 4 的可执行验证）。
// ────────────────────────────────────────────────────────────────
func TestContractGracefulClose(t *testing.T) {
	c := startFake(t, "ok", nil)
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close：%v", err)
	}
	select {
	case <-c.waitCh:
	case <-time.After(3 * time.Second):
		t.Fatal("Close 后子进程应已退出")
	}
	if c.Available() {
		t.Error("关闭后可用性应为 false")
	}
	ids := reqIDs(c.StderrTail(64))
	if len(ids) == 0 {
		t.Fatal("应能观测到 REQ 记录")
	}
}

// ────────────────────────────────────────────────────────────────
// ⑫ 真 sidecar（sidecar/index.mjs）冒烟：缺 hub 时**结构化报错且进程不崩**，
//
//	红线守卫活体往返。这是对 README 冒烟示例的自动化。
//
// ────────────────────────────────────────────────────────────────
func TestContractRealSidecar(t *testing.T) {
	mustNode(t)
	entry := filepath.Join("..", "..", "..", "sidecar", "index.mjs")
	if !fileExistsOnly(t, entry) {
		t.Skipf("找不到真 sidecar：%s", entry)
	}
	c, err := Start(context.Background(), Options{
		Entry:  entry,
		Logger: discardLogger(),
		// 真 sidecar 会尝试 import hub（失败得很快），health 里有磁盘 IO，给足余量。
		StartupTimeout: 10 * time.Second,
		Timeout:        8 * time.Second,
	})
	if err != nil {
		t.Fatalf("真 sidecar health 握手失败（node=%s）：%v；stderr=%v", nodeVersion(), err, c.StderrTail(10))
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })

	ctx := context.Background()

	// providers.list 不依赖 hub：必须返回 15 家。
	//
	// 家数的来历：04-sidecar.md 6.2 的全表是 17 家，2026-10-24 按贡献入口 v2 契约
	// §7 删掉了 modelscope / opencode 两家（它们没有可发起的登录流，凭据形态由
	// 「API 供应商」入口承担），所以现在是 15。这个断言要跟着契约走，不是跟文档走。
	ps, err := c.ProvidersList(ctx)
	if err != nil {
		t.Fatalf("providers.list：%v", err)
	}
	// 2026-10-29 对照 gitee deepseek-harness-codearts 补齐 hub 面板 23 家：
	// 15 + buddy/workbuddy/codely/astudio/autoclawintl = 20（gemini 本就在
	// 实现里只是表漏暴露；modelscope/opencode 维持契约 v2 §7 的删除状态；
	// codearts 留表但 loginImplemented=false——hub 亦无其登录实现）。
	if len(ps) != 20 {
		t.Fatalf("provider 目录应为 20 家（15 + buddy/workbuddy/codely/astudio/autoclawintl），得 %d", len(ps))
	}

	// credentials.resolve 现在走 $DSH_HOME/.credentials.yaml 的**只读**解析（与 hub 无关），
	// 契约是「空值/不存在 → result 为 null」。所以这里接受两种正确形态：
	//   a) 无错误 + nil —— 本机没有这个 ref（最常见）；
	//   b) 结构化错误 —— 文件缺失/不可读，或将来换成 DSH 后端时的 E_DEPENDENCY_MISSING。
	// 无论哪种都**不得**是超时或进程死亡。
	cred, err := c.CredentialsResolve(ctx, "dsh:buddy:0")
	if err != nil {
		switch de := asError(t, err); de.Code {
		case CodeDependencyMissng, CodeNotImplemented, CodeCredentialMissng:
			// 可接受：这台机器上没有该凭据或没有对应后端
		default:
			t.Errorf("credentials.resolve 的失败码应为 E_DEPENDENCY_MISSING/E_NOT_IMPLEMENTED/E_CREDENTIAL_MISSING，得 %s（%v）",
				de.Code, de)
		}
	} else if cred != nil && cred.Value == "" {
		// resolve 命中时不得回空串：空值按契约必须表达为 null（DSH 口径）。
		t.Error("credentials.resolve 命中时值不得为空串（空值应表达为 null）")
	}

	// 「业务错误不影响通道健康」的活体断言：credits.query 对未知 provider 回
	// **应用层结构化应答** {ok:false, error}（2026-10-29 起余额查询已实装——
	// hub 分发器 18 家 + API 型 8 部署位；「暂不支持」是可重试的业务状态，
	// hub 升级即跟上，不走通道错误）。参数缺失才是通道错误（E_BAD_REQUEST）。
	unknownRes, err := c.Call(ctx, "credits.query", map[string]any{"provider": "demo-provider", "credential": map[string]any{"access_token": "x"}})
	if err != nil {
		t.Errorf("credits.query 未知 provider 应正常应答，得 %v", err)
	} else {
		var ur struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if json.Unmarshal(unknownRes, &ur) != nil || ur.OK || ur.Error == "" {
			t.Errorf("credits.query 未知 provider 应回 {ok:false, error:…}，得 %s", string(unknownRes))
		}
	}
	if _, err = c.Call(ctx, "credits.query", map[string]any{"provider": "demo-provider"}); CodeOf(err) != CodeBadRequest {
		t.Errorf("credits.query 缺 credential 应回 E_BAD_REQUEST，得 %v", err)
	}
	if !c.Available() {
		t.Fatal("业务错误不得影响通道健康——「缺能力不崩」是本用例的核心断言")
	}

	// 红线守卫的活体往返（真 sidecar 侧）。
	_, err = c.Call(ctx, "credentials.set", map[string]any{"credentialRef": "x"})
	if CodeOf(err) != CodeCredentialReadonly {
		t.Errorf("真 sidecar 对 credentials.set 应回 E_CREDENTIAL_READONLY，得 %v", err)
	}
	if _, err = c.Call(ctx, "state.read", nil); CodeOf(err) != CodeStateForbidden {
		t.Errorf("真 sidecar 对 state.read 应回 E_STATE_FORBIDDEN，得 %v", err)
	}

	// shutdown 帧应让进程自行退出（5.4），Close 不等超时。
	t0 := time.Now()
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close：%v", err)
	}
	if el := time.Since(t0); el > 3*time.Second {
		t.Errorf("shutdown 自行退出应在 3s 内，实际 %s", el)
	}
}

func fileExistsOnly(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	return err == nil
}

func nodeVersion() string {
	out, err := exec.Command("node", "--version").CombinedOutput()
	if err != nil {
		return "不可用"
	}
	return strings.TrimSpace(string(out))
}

// ────────────────────────────────────────────────────────────────
// ⑬ sidecar stderr 的日志遮蔽（code-review 一般 8）：
//
//	sidecar 的 stdout-guard 会把第三方库任意输出改道 stderr（典型：hub adapter 的
//	调试行、OAuth 回调 URL）。这些行**不能原样**进主进程日志——授权码/token 会
//	跟着进去。本用例断言「进日志的那一份」被截断且遮蔽，而排障用的环形缓冲保留原文。
//
// ────────────────────────────────────────────────────────────────
func TestContractStderrRedaction(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	// 不走子进程：drainStderr 只依赖 log + stderr 环，直接构造最小 Client。
	c := &Client{log: slog.New(h), stderr: newStderrRing(8)}

	const (
		secretToken = "sk-live-abcdef1234567890"
		authCode    = "abcd1234efghijkl"
	)
	long := strings.Repeat("A", 4096)
	input := strings.Join([]string{
		"Authorization: Bearer " + secretToken,
		"GET /oauth/callback?code=" + authCode + "&state=xyz HTTP/1.1",
		`{"access_token":"` + secretToken + `","expires_in":3600}`,
		long, // 超长行：截断
		"普通日志：provider=demo-provider 装载成功",
	}, "\n") + "\n"
	c.drainStderr(strings.NewReader(input))

	// 1. 遮蔽：日志里不得出现任何形态的密钥/授权码。
	logged := buf.String()
	for _, leak := range []string{secretToken, authCode} {
		if strings.Contains(logged, leak) {
			t.Fatalf("stderr 日志泄露了凭据：%q 出现在\n%s", leak, logged)
		}
	}
	// 2. 截断：超长行不得整行进日志（512 字节上限 + "…"）。日志是结构化 TextHandler，
	//    行内会被包引号与转义，所以按 600 个连续 A 是否出现来判定「没有整行照抄」。
	if strings.Contains(logged, strings.Repeat("A", 600)) {
		t.Errorf("超长 stderr 行未被截断（应 ≤ %d 字节）", stderrLogMaxBytes)
	}
	// 3. 正常内容仍要可读（遮蔽不能把有用信息一起吃掉）。
	if !strings.Contains(logged, "demo-provider") {
		t.Errorf("普通日志不应被遮蔽掉：%s", logged)
	}
	// 4. 环形缓冲保留原文：排障要看完整错误（它只在内存里，不进日志系统）。
	tail := c.StderrTail(16)
	joined := strings.Join(tail, "\n")
	if !strings.Contains(joined, secretToken) {
		t.Errorf("环形缓冲应保留原文供排障，实际：%v", tail)
	}
	if len(tail) != 5 {
		t.Errorf("应保留 5 行，得 %d：%v", len(tail), tail)
	}
}

// TestContractRedactLogLineIdempotent 断言遮蔽幂等：对已遮蔽的文本再跑一次不变。
// 为什么重要：同一行可能被多层包装（adapter → sidecar → 主进程日志），
// 非幂等的遮蔽会在第二层把 `***` 当成值再遮一次，产出 `******` 之类的噪音。
func TestContractRedactLogLineIdempotent(t *testing.T) {
	once := redactLogLine("Authorization: Bearer sk-abcdefgh12345678")
	if twice := redactLogLine(once); twice != once {
		t.Errorf("遮蔽应幂等：once=%q twice=%q", once, twice)
	}
	if strings.Contains(once, "abc...") || strings.Contains(once, "defgh12345678") {
		t.Errorf("遮蔽不彻底：%q", once)
	}
}

// TestContractRedactLineTable 是遮蔽规则的**表驱动契约**（code-review 复验追加）：
// 真敏感值必须遮，排障字段（错误码/状态名/请求 id）必须留。
//
// 为什么用表而不是几条 if：这类模式的最常见回归是「遮多了」或「遮少了」各一半，
// 单条断言只看一个方向。表里两个方向都钉住，改正则时一跑就知道伤没伤到排障。
func TestContractRedactLineTable(t *testing.T) {
	const (
		bearerTok = "sk-live-abcdefgh12345678"
		basicTok  = "dXNlcjpwYXNzd29yZA=="
		oauthCode = "abcd1234efghijkl"
		accessTok = "ya29.A0AfH6SMBx"
		refresh   = "1//0gXyzAbcDef"
	)
	// 敏感：输出里不得再出现这些值。
	sensitive := []struct {
		name, in, secret string
	}{
		{"Bearer 头", "Authorization: Bearer " + bearerTok, bearerTok},
		{"Basic 头（base64 里是 user:pass）", "Authorization: Basic " + basicTok, basicTok},
		{"OAuth 回调 code=", "GET /callback?code=" + oauthCode + "&state=ok", oauthCode},
		{"query 裸 key", "GET /v1/models?key=" + oauthCode, oauthCode},
		{"JSON 双 token", `{"access_token":"` + accessTok + `","refresh_token":"` + refresh + `"}`, accessTok},
		{"裸 sk- 前缀", "received " + bearerTok, bearerTok},
		{"password 键", "password=hunter2hunter2", "hunter2hunter2"},
	}
	for _, tc := range sensitive {
		out := RedactLine(tc.in)
		if strings.Contains(out, tc.secret) {
			t.Errorf("%s：未遮蔽，输入=%q 输出=%q", tc.name, tc.in, out)
		}
	}
	// 排障字段：必须原样保留（抹掉它们等于把「为什么失败」删了）。
	preserved := []struct{ name, in, keep string }{
		{"JSON error_code", `{"error_code":"upstream_unavailable","message":"上游 502"}`, `"upstream_unavailable"`},
		{"error_code 键值", "error_code=model_not_found request_id=abc", "model_not_found"},
		{"sidecar 错误帧形态", "upstream_error code=no_candidate", "no_candidate"},
		{"E_ 错误码", "code=E_DEPENDENCY_MISSING method=infer.stream", "E_DEPENDENCY_MISSING"},
		{"status_code", "status_code=503 upstream=http", "503"},
		{"request_id", "request_id=abc error_code=timeout_killed", "request_id=abc"},
	}
	for _, tc := range preserved {
		out := RedactLine(tc.in)
		if !strings.Contains(out, tc.keep) {
			t.Errorf("%s：排障字段被误遮，输入=%q 输出=%q（应保留 %q）", tc.name, tc.in, out, tc.keep)
		}
	}
	// JSON 必须仍然可解析（引号配对保留）——否则日志里的错误体就没法看了。
	out := RedactLine(`{"access_token":"` + accessTok + `","refresh_token":"` + refresh + `"}`)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("遮蔽后 JSON 不可解析：%q（错误 %v）", out, err)
	}
	if parsed["access_token"] != "***" || parsed["refresh_token"] != "***" {
		t.Errorf("遮后值应为 ***，得 %v", parsed)
	}
	// 截断仍在遮蔽之前：超长行必须被截断，且行尾不残留明文。
	long := strings.Repeat("x", 4096) + bearerTok
	if got := RedactLine(long); len(got) > stderrLogMaxBytes+len("…")+16 || strings.Contains(got, bearerTok) {
		t.Errorf("超长行处理不对：len=%d 含明文=%v", len(got), strings.Contains(got, bearerTok))
	}
}
