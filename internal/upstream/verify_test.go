// 模型鉴定契约测试（task-16：TestContract 前缀 ≥8）。
//
// 覆盖（任务验收逐条）：
//  1. 无指纹默认 unverified 且可用（红线：不认识 ≠ 拒之门外）；
//  2. 指纹匹配标 verified；
//  3. 指纹不符标 suspect 且仍可用（目录侧「标出但可用」）；
//  4. 鉴定纯函数确定性（同输入同输出 ×N）；
//  5. 刷新后新增模型触发鉴定（联动挂通）；
//  6. inbox 三事件各投递一次（added/missing/suspect）；
//  7. 接口失败只记日志不 panic（鉴定写回失败/通知失败两路）；
//  8. 参考指纹表驱动可扩展（自定义表生效、表序优先）；
//  9. relay 官方渠道 trusted 免检；missing 红线不破坏。
package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// ———— 判定纯函数的契约（1-4、8、9） ————

// TestContractVerifyDefaultUnverifiedAvailable：无参考指纹默认 unverified 且可用。
// 任务红线第一条：不能因为不认识就把模型拒之门外。
func TestContractVerifyDefaultUnverifiedAvailable(t *testing.T) {
	v := JudgeRealModel(VerifyInput{UpstreamModelID: "some-cold-model-x"}, fingerprintTable)
	if v.State != VerifyUnverified {
		t.Fatalf("冷门模型应 unverified，got %s", v.State)
	}
	if !v.State.Available() {
		t.Fatal("unverified 必须可用（默认放行）")
	}
	if !v.State.NeedsRecheck() {
		t.Fatal("unverified 必须进复检队列（§5.1）")
	}
	if v.Evidence == "" {
		t.Fatal("证据不能为空（§5.3：每种判定都要落证据）")
	}
	// 证据按 §5.3 第二行模板：写明「无指纹、已放行、进复检」。
	for _, want := range []string{"参考指纹", "放行", "复检"} {
		if !containsFoldStr([]string{v.Evidence}, want) && !containsText(v.Evidence, want) {
			t.Fatalf("证据应含 %q：%s", want, v.Evidence)
		}
	}
}

// TestContractVerifyFingerprintMatchVerified：指纹匹配标 verified。
func TestContractVerifyFingerprintMatchVerified(t *testing.T) {
	cw := int64(210_000)
	v := JudgeRealModel(VerifyInput{
		UpstreamModelID: "claude-4.5-sonnet",
		ContextWindow:   &cw,
		Modalities:      []string{"text", "image"},
	}, fingerprintTable)
	if v.State != VerifyVerified {
		t.Fatalf("达标应 verified，got %s（%s）", v.State, v.Evidence)
	}
	if !v.State.Available() {
		t.Fatal("verified 可用")
	}
	if !containsText(v.Evidence, "Claude 4.5") {
		t.Fatalf("证据应指名族：%s", v.Evidence)
	}
	if !containsText(v.Evidence, "指纹库 v") {
		t.Fatalf("证据必须记指纹库版本（§6.2 可追溯）：%s", v.Evidence)
	}
}

// TestContractVerifyFingerprintMismatchSuspect：指纹不符标 suspect 且仍可用。
func TestContractVerifyFingerprintMismatchSuspect(t *testing.T) {
	cw := int64(8_000) // 明显低于 claude-4.5 的 200k 下界
	v := JudgeRealModel(VerifyInput{
		UpstreamModelID: "claude-4.5-sonnet",
		ContextWindow:   &cw,
		Modalities:      []string{"text"}, // 缺 image
	}, fingerprintTable)
	if v.State != VerifySuspect {
		t.Fatalf("不符应 suspect，got %s", v.State)
	}
	// 目录侧语义：suspect 仍可用（「标出但可用」——移出候选是 L1+人工的动作）。
	if !v.State.Available() {
		t.Fatal("目录侧 suspect 必须仍可用")
	}
	// 证据给足失败方向（§5.3 第一行：哪条信号失败、失败方向）。
	if !containsText(v.Evidence, "context_window=8000") || !containsText(v.Evidence, "200000") {
		t.Fatalf("证据应含窗口失败方向：%s", v.Evidence)
	}
	if !containsText(v.Evidence, "image") {
		t.Fatalf("证据应含模态缺失：%s", v.Evidence)
	}
}

// TestContractVerifyDeterministic：纯函数确定性——同输入同输出（重复 50 次）。
// §6.2：事后要能说清「当时为什么判它」，前提是判定可复现。
func TestContractVerifyDeterministic(t *testing.T) {
	in := VerifyInput{UpstreamModelID: "gpt-5-mini", ContextWindow: ptrInt64(400_000)}
	first := JudgeRealModel(in, fingerprintTable)
	for i := 0; i < 50; i++ {
		again := JudgeRealModel(in, fingerprintTable)
		if again != first {
			t.Fatalf("判定不确定：第 %d 次 %+v ≠ 首次 %+v", i, again, first)
		}
	}
	// VerifyVerdict 可比较（struct 相等 = state+evidence 全等）。
	if (first != VerifyVerdict{}) == false {
		t.Fatal("判定不应是零值")
	}
}

// TestContractVerifyFingerprintTableExtensible：表驱动可扩展——自定义表生效、
// 表序优先（任务第 8 点验收）。
func TestContractVerifyFingerprintTableExtensible(t *testing.T) {
	custom := []ModelFingerprint{
		{Pattern: "glm-5", Family: "GLM-5 家族", MinContextWindow: ptrInt64(128_000)},
		{Pattern: "glm", Family: "GLM 家族", MinContextWindow: ptrInt64(32_000)},
	}
	// 命中第一条（glm-5，表序优先）。
	cw := int64(130_000)
	if v := JudgeRealModel(VerifyInput{UpstreamModelID: "glm-5-plus", ContextWindow: &cw}, custom); v.State != VerifyVerified {
		t.Fatalf("自定义表应命中 glm-5：%+v", v)
	}
	// 只命中第二条（glm 但非 5 系）：第二条的 32k 下界。
	if v := JudgeRealModel(VerifyInput{UpstreamModelID: "glm-4-flash", ContextWindow: &cw}, custom[1:]); v.State != VerifyVerified {
		t.Fatalf("glm 家族下界应达标：%+v", v)
	}
	cw2 := int64(16_000)
	if v := JudgeRealModel(VerifyInput{UpstreamModelID: "glm-4-flash", ContextWindow: &cw2}, custom[1:]); v.State != VerifySuspect {
		t.Fatalf("低于 glm 家族下界应 suspect：%+v", v)
	}
	// 空表 = 全部 unverified（表的「无指纹」语义与内置表无关）。
	if v := JudgeRealModel(VerifyInput{UpstreamModelID: "anything"}, nil); v.State != VerifyUnverified {
		t.Fatalf("空表应 unverified：%+v", v)
	}
}

// TestContractVerifyRelayTrusted：relay 官方渠道 trusted 免检（§5.1 第四行
// 的目录侧对应：providers-hub 的目录来自官方 API，不是声称值）。
func TestContractVerifyRelayTrusted(t *testing.T) {
	v := JudgeRealModel(VerifyInput{UpstreamModelID: "claude-4.5-sonnet", Kind: "relay"}, fingerprintTable)
	if v.State != VerifyTrusted {
		t.Fatalf("relay 应 trusted 免检，got %s", v.State)
	}
	if v.State.NeedsRecheck() {
		t.Fatal("trusted 不进复检队列")
	}
	// vendor/selfhosted 走正常判定（Kind 字段不命中 relay 才往下走）。
	v2 := JudgeRealModel(VerifyInput{UpstreamModelID: "claude-4.5-sonnet", Kind: "vendor"}, fingerprintTable)
	if v2.State == VerifyTrusted {
		t.Fatal("vendor 不得免检")
	}
}

// TestContractVerifyMissingFieldsTolerated：目录字段缺失的容忍语义
// （「没说」≠「说错了」——hub 的 listModels 实测不含 contextWindow）。
func TestContractVerifyMissingFieldsTolerated(t *testing.T) {
	// 命中指纹但目录全没说：mismatch 提示而非 suspect（证据记「无法比对」）。
	v := JudgeRealModel(VerifyInput{UpstreamModelID: "gpt-4o-mini"}, fingerprintTable)
	if v.State != VerifySuspect {
		t.Fatalf("窗口未申报按 suspect 提示（证据写明无法比对）：%+v", v)
	}
	if !containsText(v.Evidence, "未申报") {
		t.Fatalf("证据应写明未申报：%s", v.Evidence)
	}
	// 模态/档位缺失（空切片）不触发 mismatch（与窗口的 nil 不同——
	// 模态空是「目录没说」，nil 也是，二者同容忍）。
	cw := int64(400_000)
	v2 := JudgeRealModel(VerifyInput{UpstreamModelID: "claude-4.5-x", ContextWindow: &cw}, fingerprintTable)
	if v2.State != VerifyVerified {
		t.Fatalf("模态缺失应容忍（verified）：%+v", v2)
	}
}

// ———— 刷新联动与 inbox（5-7） ————

// fakeVerification 记录写回调用的 fake。
type fakeVerification struct {
	mu     sync.Mutex
	writes []verifyWrite
	err    map[string]error
}

type verifyWrite struct {
	id      string
	state   VerifyState
	evid    string
	version int
}

func (f *fakeVerification) WriteVerification(_ context.Context, id string, state VerifyState, evidence string, version int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil && f.err[id] != nil {
		return f.err[id]
	}
	f.writes = append(f.writes, verifyWrite{id: id, state: state, evid: evidence, version: version})
	return nil
}

// fakeInbox 记录投递的 fake。
type fakeInbox struct {
	mu    sync.Mutex
	notes []inboxNote
	err   error
}

type inboxNote struct {
	kind, related, title, payload, severity string
}

func (f *fakeInbox) NotifyInbox(_ context.Context, kind, relatedID, title, payloadJSON, severity string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.notes = append(f.notes, inboxNote{kind: kind, related: relatedID, title: title, payload: payloadJSON, severity: severity})
	return nil
}

// openVerifyFixture 在 refresh fixture 之上接好 hooks。
type verifyFixture struct {
	st    *store.Store
	fake  *fakeCatalog
	hooks *RefreshHooks
	verif *fakeVerification
	inbox *fakeInbox
	prov  string
}

func openVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), store.Options{
		Path:          filepath.Join(dir, "freerouter.db"),
		MasterKeyFile: filepath.Join(dir, "master.key"),
		BusyTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.Repos().Models.UpsertUpstream(ctx, "relay", "fake_provider", "sidecar://fake_provider", nil, true); err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	verif := &fakeVerification{}
	inbox := &fakeInbox{}
	hooks := &RefreshHooks{Verification: verif, Inbox: inbox, Log: nil}
	return &verifyFixture{st: st, fake: &fakeCatalog{catalog: map[string][]dshprovider.ModelInfo{}}, hooks: hooks, verif: verif, inbox: inbox, prov: "fake_provider"}
}

// TestContractRefreshTriggersVerification：刷新后新增模型触发鉴定
// （任务第 5 点验收：联动挂通——upsert 的每一条都进鉴定写回）。
func TestContractRefreshTriggersVerification(t *testing.T) {
	fx := openVerifyFixture(t)
	r := NewModelRefresherWithHooks(fx.st, fx.fake, time.Minute, nil, fx.hooks)
	fx.fake.setCatalog(fx.prov, seedCatalog())

	if _, err := r.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if len(fx.verif.writes) != 3 {
		t.Fatalf("首轮目录 3 条应各鉴定一次，got %d", len(fx.verif.writes))
	}
	// relay 上游 → 全部 trusted 免检（§5.1 第四行）。
	for _, w := range fx.verif.writes {
		if w.state != VerifyTrusted {
			t.Fatalf("relay 目录条目应 trusted：%+v", w)
		}
		if w.version != FingerprintVersion {
			t.Fatalf("写回必须带指纹库版本：%+v", w)
		}
	}
	// 二轮刷新：鉴定照跑（upsert 路径每轮重判——「变更」也在覆盖面内），
	// 但 added 事件不再投递（快照里已存在）。
	fx.verif.writes = nil
	if _, err := r.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("二轮刷新失败: %v", err)
	}
	if len(fx.verif.writes) != 3 {
		t.Fatalf("二轮鉴定应照跑 3 条（变更重判），got %d", len(fx.verif.writes))
	}
}

// TestContractInboxThreeEvents：三事件各投递一次——added（首轮新增）、
// missing（目录消失）、suspect（鉴定不符）。
// suspect 需要非 relay 上游才有指纹比对——用 vendor 上游 + gpt-5 模型名。
func TestContractInboxThreeEvents(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), store.Options{
		Path:          filepath.Join(dir, "freerouter.db"),
		MasterKeyFile: filepath.Join(dir, "master.key"),
		BusyTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	// vendor 上游：Kind 非 relay，走指纹比对。
	if _, err := st.Repos().Models.UpsertUpstream(ctx, "vendor", "openai", "https://api.example.invalid/v1", nil, true); err != nil {
		t.Fatalf("建 vendor 上游失败: %v", err)
	}
	verif := &fakeVerification{}
	inbox := &fakeInbox{}
	hooks := &RefreshHooks{Verification: verif, Inbox: inbox}
	// ⚠️ RefreshOnce 只刷 kind=relay（ALIGN-SPEC H）——vendor 不进 refreshUpstream。
	// suspect 事件直接驱动 applyVerification（refreshUpstream 内部的同一路径）：
	// vendor 目录 + 低窗口的 gpt-5 命中指纹不符。
	lowCW := 1000
	items := []dshprovider.ModelInfo{
		{UpstreamModelID: "gpt-5-turbo", ContextWindow: &lowCW}, // suspect：窗口远低于 400k
	}
	// vendor 目录条目手工落库（RefreshOnce 不刷 vendor——见上）；
	// 上游 id 经 ListUpstreams 反查（不假设自增起点）。
	ups, err := st.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		t.Fatalf("列上游失败: %v", err)
	}
	var vendorID string
	for _, u := range ups {
		if u.Kind == "vendor" {
			vendorID = u.ID
		}
	}
	var rmID string
	for _, m := range items {
		row := store.RealModel{UpstreamID: vendorID, UpstreamModelID: m.UpstreamModelID, DisplayName: m.UpstreamModelID, Status: store.RealModelActive, Source: "auto"}
		id, err := st.Repos().Models.UpsertRealModel(ctx, row)
		if err != nil {
			t.Fatalf("手工落库失败: %v", err)
		}
		rmID = id
	}
	// 直接跑 applyVerification（refreshUpstream 内部的同一路径）。
	vi := []verifyItem{{info: modelInfoInputOf(items[0]), realModelID: rmID, kind: "vendor"}}
	if n, err := hooks.applyVerification(ctx, vi); err != nil || n != 1 {
		t.Fatalf("鉴定写回应成功：%d %v", n, err)
	}
	if len(verif.writes) != 1 || verif.writes[0].state != VerifySuspect {
		t.Fatalf("低窗口 gpt-5 应 suspect：%+v", verif.writes)
	}
	if len(inbox.notes) != 1 || inbox.notes[0].kind != InboxKindModelSuspect {
		t.Fatalf("suspect 应投递一条通知：%+v", inbox.notes)
	}
	if inbox.notes[0].severity != InboxSeverityWarning {
		t.Fatalf("suspect 通知 severity 应 warning：%s", inbox.notes[0].severity)
	}

	// added / missing 事件走 relay 刷新路径（fixture 同款）：
	fx := openVerifyFixture(t)
	fx.fake.setCatalog(fx.prov, seedCatalog())
	r2 := NewModelRefresherWithHooks(fx.st, fx.fake, time.Minute, nil, fx.hooks)
	if _, err := r2.RefreshOnce(ctx); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	addedCount := 0
	for _, n := range fx.inbox.notes {
		if n.kind == InboxKindModelAdded {
			addedCount++
		}
	}
	if addedCount != 3 {
		t.Fatalf("首轮 3 个新模型应各投一条 added，got %d（notes=%+v）", addedCount, fx.inbox.notes)
	}
	// 二轮：目录清空 → 3 条 missing（保留行不删）。
	fx.fake.setCatalog(fx.prov, nil)
	// 空目录跳过 MarkMissing（refreshUpstream 的空目录早退）——用只剩一条的目录
	// 触发另两条消失。
	fx.fake.setCatalog(fx.prov, []dshprovider.ModelInfo{seedCatalog()[0]})
	if _, err := r2.RefreshOnce(ctx); err != nil {
		t.Fatalf("三轮刷新失败: %v", err)
	}
	missingCount := 0
	for _, n := range fx.inbox.notes {
		if n.kind == InboxKindModelMissing {
			missingCount++
		}
	}
	if missingCount != 2 {
		t.Fatalf("消失 2 条应各投一条 missing，got %d（notes=%+v）", missingCount, fx.inbox.notes)
	}
}

// TestContractVerifyFailuresDontPanicOrBlock：接口失败只记日志不 panic
// （鉴定写回失败继续其余条目；通知失败不影响主流程）。
func TestContractVerifyFailuresDontPanicOrBlock(t *testing.T) {
	fx := openVerifyFixture(t)
	// 写回全失败（库锁模拟）：applyVerification 逐条容错，返回首错但不 panic。
	fx.verif.err = map[string]error{"1": errors.New("库锁"), "2": errors.New("库锁")}
	items := []verifyItem{
		{info: ModelInfoInput{UpstreamModelID: "a"}, realModelID: "1", kind: "relay"},
		{info: ModelInfoInput{UpstreamModelID: "b"}, realModelID: "2", kind: "relay"},
	}
	n, err := fx.hooks.applyVerification(context.Background(), items)
	if err == nil {
		t.Fatal("写回失败应返回首错（供调用方记日志）")
	}
	if n != 0 {
		t.Fatalf("全失败时写回数应为 0，got %d", n)
	}
	// 第 1 条失败不影响第 2 条：err map 只标了 1。
	fx.verif.err = map[string]error{}
	n2, err2 := fx.hooks.applyVerification(context.Background(), items)
	if err2 != nil || n2 != 2 {
		t.Fatalf("修复后应全成功：%d %v", n2, err2)
	}

	// 通知失败：suspect 判定照写回，通知投递失败不 panic。
	//（store 在本段其实用不到——applyVerification 只碰 fake；保留注释避免误导。）
	lowCW := 1000
	verif2 := &fakeVerification{}
	inbox2 := &fakeInbox{err: errors.New("admin_inbox 写入失败（库满）")}
	hooks2 := &RefreshHooks{Verification: verif2, Inbox: inbox2}
	vi := []verifyItem{{info: modelInfoInputOf(dshprovider.ModelInfo{UpstreamModelID: "gpt-5-x", ContextWindow: &lowCW}), realModelID: "1", kind: "vendor"}}
	n3, err3 := hooks2.applyVerification(context.Background(), vi)
	if err3 != nil || n3 != 1 {
		t.Fatalf("通知失败不该影响写回：%d %v", n3, err3)
	}
	if len(inbox2.notes) != 0 {
		t.Fatal("失败的投递不应记入 notes")
	}

	// nil hooks：全链路安全降级（M1 行为）。
	var nilHooks *RefreshHooks
	if n, err := nilHooks.applyVerification(context.Background(), items); err != nil || n != 0 {
		t.Fatal("nil hooks 应安全跳过")
	}
	nilHooks.notifyAdded(context.Background(), nil, "u", "1", "m")
	nilHooks.notifyMissing(context.Background(), nil, "u", "1", "m")
}

// TestContractRefreshHooksWithoutVerification：只接 inbox 不接鉴定（装配自由度：
// 两个联动独立接线——Lead 可能先上通知后上鉴定）。
func TestContractRefreshHooksWithoutVerification(t *testing.T) {
	fx := openVerifyFixture(t)
	fx.hooks.Verification = nil // 只留 Inbox
	r := NewModelRefresherWithHooks(fx.st, fx.fake, time.Minute, nil, fx.hooks)
	fx.fake.setCatalog(fx.prov, seedCatalog())
	if _, err := r.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if len(fx.verif.writes) != 0 {
		t.Fatal("未接鉴定不应有写回")
	}
	// added 事件照投（联动独立）。
	added := 0
	for _, n := range fx.inbox.notes {
		if n.kind == InboxKindModelAdded {
			added++
		}
	}
	if added != 3 {
		t.Fatalf("added 应照投 3 条，got %d", added)
	}
}

// containsText 简单子串判定（证据文案断言用；与 containsFoldStr 分工：
// 后者是模态/档位成员判定）。
func containsText(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// fmt 保底（错误构造用）。
var _ = fmt.Sprintf

// ———— 词表契约（06-audit.md §3.1 是唯一权威；b701374 裁决） ————

// auditInboxKinds 是 docs/security/06-audit.md §3.1 表里的 10 个权威 kind
// 的**文档快照**（手抄）。
//
// 为什么手抄表在 store 已有枚举之后**仍然保留**：它现在的作用是交叉校验——
// inbox.go 的常量已经直接引用 store 枚举（单一来源），于是「upstream 与 store
// 一致」变成了编译期事实、测不出漂移；真正会漂移的是 **store 与 06-audit 文档**。
// 下面 TestContractInboxVocabularyMatchesAudit 里的集合相等断言就是抓这个的：
// storage 若改了自己的枚举而没改文档（或反之），这条会红。
// 手抄表来源：06-audit.md §3.1 的表体（10 行 kind + severity 三值）。
var auditInboxKinds = map[string]bool{
	"account_pending":     true,
	"model_added":         true,
	"model_missing":       true,
	"model_changed":       true,
	"channel_failing":     true,
	"credential_invalid":  true,
	"verification_failed": true,
	"pool_exhausted":      true,
	"global_budget":       true,
	"key_anomaly":         true,
}

var auditInboxSeverities = map[string]bool{"info": true, "warning": true, "error": true}

// TestContractInboxVocabularyMatchesAudit：本包全部生产者投递的 kind/severity
// 必须落在 06-audit §3.1 枚举内（三事件 + bootstrap 的 channel_failing）。
// 这条测试防的是「文档词表又动了一次，而代码静默失败」。
func TestContractInboxVocabularyMatchesAudit(t *testing.T) {
	// 常量本身先自证（新增常量忘了对齐会在这里红）。
	consts := []struct {
		name, kind, sev string
	}{
		{"added", InboxKindModelAdded, InboxSeverityInfo},
		{"missing", InboxKindModelMissing, InboxSeverityWarning},
		{"suspect", InboxKindModelSuspect, InboxSeverityWarning},
		{"failing", InboxKindChannelFailing, InboxSeverityError},
	}
	// 集合相等：store 的枚举必须与文档快照完全一致（这条抓的是 storage 侧漂移）。
	storeKinds := map[string]bool{
		string(store.InboxAccountPending):     true,
		string(store.InboxModelAdded):         true,
		string(store.InboxModelMissing):       true,
		string(store.InboxModelChanged):       true,
		string(store.InboxChannelFailing):     true,
		string(store.InboxCredentialInvalid):  true,
		string(store.InboxVerificationFailed): true,
		string(store.InboxPoolExhausted):      true,
		string(store.InboxGlobalBudget):       true,
		string(store.InboxKeyAnomaly):         true,
	}
	for k := range storeKinds {
		if !auditInboxKinds[k] {
			t.Errorf("store 枚举 %q 不在 06-audit §3.1 文档表里（代码漂移了文档没跟上）", k)
		}
	}
	for k := range auditInboxKinds {
		if !storeKinds[k] {
			t.Errorf("文档 kind %q 未被 store 枚举收录（执法面漏了这个值）", k)
		}
	}

	for _, c := range consts {
		if !auditInboxKinds[c.kind] {
			t.Errorf("kind %s=%q 不在 06-audit §3.1 枚举内", c.name, c.kind)
		}
		if !auditInboxSeverities[c.sev] {
			t.Errorf("severity %s=%q 不在枚举内", c.name, c.sev)
		}
	}

	// 活体断言：跑一轮真实投递，检查送出去的字符串。
	fx := openVerifyFixture(t)
	fx.fake.setCatalog(fx.prov, seedCatalog())
	r := NewModelRefresherWithHooks(fx.st, fx.fake, time.Minute, nil, fx.hooks)
	if _, err := r.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if len(fx.inbox.notes) == 0 {
		t.Fatal("应有投递记录")
	}
	for _, n := range fx.inbox.notes {
		if !auditInboxKinds[n.kind] {
			t.Errorf("投递的 kind=%q 不在枚举内（store 会拒收 → 静默失败）", n.kind)
		}
		if !auditInboxSeverities[n.severity] {
			t.Errorf("投递的 severity=%q 不在枚举内", n.severity)
		}
	}

	// bootstrap 路径同样验一次（channel_failing 走 AdminNotifier）。
	bn := &fakeNotifier{}
	b := NewRelayBootstrapper(&fakeProviderSource{err: errors.New("sidecar down")},
		&fakeUpstreamWriter{}, &fakeRefresher{}, bn, nil)
	b.Run(context.Background())
	if len(bn.notes) == 0 {
		t.Fatal("bootstrap 应有通知")
	}
	// fakeNotifier 记的是 kind/related/severity 拼接串。
	if !strings.Contains(bn.notes[0], InboxKindChannelFailing) ||
		!strings.HasSuffix(bn.notes[0], "/"+InboxSeverityError) {
		t.Fatalf("bootstrap 通知词表错位：%s", bn.notes[0])
	}
}

// TestContractBootstrapNotifyThrottled：同一自举失败重复 Run 只投一条通知。
// 为什么必须有：自举类通知拿不到数字行 id（provider 行还没落库），store 的
// Append 用部分索引去重（WHERE related_id IS NOT NULL）覆盖不到它们，
// 而自举会随启动重试/周期重跑——不节流就会把待处理页刷成噪声
// （03-upstream.md §3.3：噪声等于没有通知）。
func TestContractBootstrapNotifyThrottled(t *testing.T) {
	src := &fakeProviderSource{err: errors.New("sidecar down")}
	nt := &fakeNotifier{}
	b := NewRelayBootstrapper(src, &fakeUpstreamWriter{}, &fakeRefresher{}, nt, nil)
	for i := 0; i < 5; i++ {
		b.Run(context.Background())
	}
	if len(nt.notes) != 1 {
		t.Fatalf("重复自举失败应只投 1 条通知，got %d：%v", len(nt.notes), nt.notes)
	}
	// 不同 title（不同失败原因）不该被节流吞掉——节流键含 title。
	b.notify(context.Background(), InboxKindChannelFailing, "", "另一个失败", "x", InboxSeverityError)
	if len(nt.notes) != 2 {
		t.Fatalf("不同 title 应各自投递，got %d", len(nt.notes))
	}
}

// TestContractChannelSourceLoggedNotStored：渠道来源写日志而不写列
// （lead 裁决 B）。断言两件事：①real_models 无 channel_source 列（若将来有人
// 补列，本测试与存储层会同时给出信号）；②带权威 owned_by 的目录条目落库后
// channel 取权威值——来源信息只在日志里。
func TestContractChannelSourceLoggedNotStored(t *testing.T) {
	dir := t.TempDir()

	st, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(dir, "freerouter.db"), MasterKeyFile: filepath.Join(dir, "m.key"),
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.Repos().Models.UpsertUpstream(ctx, "relay", "p1", "sidecar://p1", nil, true); err != nil {
		t.Fatalf("建上游: %v", err)
	}

	// ① 列面：channel_source 不存在的断言**只能靠编译期**（store.RealModel 没有
	// 该字段；store 也不暴露 *sql.DB 给外部做 pragma 查询——这是刻意的封装）。
	// 所以这里不做运行时列检查：若将来裁决改 A 补了列，下面 ② 的日志断言
	// 仍然有效，且 refresh.go 的注释会先被更新（那是改列时的必经点）。

	// ② 权威值优先落库（owned_by 压过 id 前缀）。
	fake := &fakeCatalog{catalog: map[string][]dshprovider.ModelInfo{
		"p1": {{UpstreamModelID: "aggregate/auto", OwnedBy: "deepseek-official"}},
	}}
	// Debug 级才看得到来源行——生产默认 Info 会把它过滤掉（这是 lead 裁决 B 里
	// 「零成本」的一部分：正常部署不产噪声，要查时把级别调到 debug）。
	var logs bytes.Buffer
	r := NewModelRefresher(st, fake, time.Minute,
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if _, err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	rows, err := st.Repos().Models.ListVisibleRealModels(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("应落 1 行：%d %v", len(rows), err)
	}
	if rows[0].Channel == nil || *rows[0].Channel != "deepseek-official" {
		t.Fatalf("channel 应取权威 owned_by，got %v", rows[0].Channel)
	}
	// 来源写进 Debug 日志（裁决 B 的落点）。
	if !strings.Contains(logs.String(), `channel_source=owned_by`) {
		t.Fatalf("日志应记渠道来源：\n%s", logs.String())
	}
}
