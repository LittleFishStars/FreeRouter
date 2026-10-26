package upstream

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// 本文件守的是 task-3 追加点（巡检 [一般 7]）：WriteVerification 必须有真实写入方，
// 且 int 指纹库版本 → verify_source 文本的映射只有一处。
//
// 断言对象是「写入后 real_models 三列可读」与 0004 的默认值语义，不是 SQL 文本。

// openVerificationFixture 起临时库 + 一条 relay 上游 + 一条未鉴定的目录行。
func openVerificationFixture(t *testing.T) (*store.Store, string) {
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
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	upID, err := st.Repos().Models.UpsertUpstream(ctx, "relay", "p", "sidecar://p", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	id, err := st.Repos().Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID:      upID,
		UpstreamModelID: "p/m1",
		DisplayName:     "m1",
	})
	if err != nil {
		t.Fatalf("建目录行失败: %v", err)
	}
	return st, id
}

func TestContractStoreVerificationWriterWritesThreeColumns(t *testing.T) {
	st, id := openVerificationFixture(t)
	ctx := context.Background()
	w := NewStoreVerificationWriter(st)

	// ① 0004 的默认值语义：未判定的行是 unverified；detail/source 均为 NULL
	//（NULL = 从未鉴定，与「跑过判定但无指纹」的 unverified 行是两种状态）。
	row, err := st.Repos().Models.GetRealModel(ctx, id)
	if err != nil {
		t.Fatalf("读默认行失败: %v", err)
	}
	if row.VerifyStatus != store.VerifyUnverified {
		t.Fatalf("默认 verify_status 应为 unverified，got %q", row.VerifyStatus)
	}
	if row.VerifyDetail != nil || row.VerifySource != nil {
		t.Fatalf("默认行 verify_detail/verify_source 应为 NULL，got %v/%v", row.VerifyDetail, row.VerifySource)
	}

	// ② suspect 写入：三列同时可读，source 是 builtin_v1 而不是 "1"。
	if err := w.WriteVerification(ctx, id, VerifySuspect, "上下文窗口低于声明", FingerprintVersion); err != nil {
		t.Fatalf("WriteVerification(suspect) 失败: %v", err)
	}
	row, err = st.Repos().Models.GetRealModel(ctx, id)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if row.VerifyStatus != store.VerifySuspect {
		t.Fatalf("verify_status 应为 suspect，got %q", row.VerifyStatus)
	}
	if row.VerifyDetail == nil || *row.VerifyDetail != "上下文窗口低于声明" {
		t.Fatalf("verify_detail 未落库：%v", row.VerifyDetail)
	}
	if row.VerifySource == nil || *row.VerifySource != "builtin_v1" {
		t.Fatalf("verify_source 应为 builtin_v1，got %v", row.VerifySource)
	}

	// ③ evidence 为空 → verify_detail 写 NULL（不是空串）；版本映射随参数变化。
	if err := w.WriteVerification(ctx, id, VerifyVerified, "", 2); err != nil {
		t.Fatalf("WriteVerification(verified) 失败: %v", err)
	}
	row, err = st.Repos().Models.GetRealModel(ctx, id)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if row.VerifyStatus != store.VerifyVerified {
		t.Fatalf("verify_status 应为 verified，got %q", row.VerifyStatus)
	}
	if row.VerifyDetail != nil {
		t.Fatalf("空 evidence 应写 NULL，got %v", row.VerifyDetail)
	}
	if row.VerifySource == nil || *row.VerifySource != "builtin_v2" {
		t.Fatalf("verify_source 应为 builtin_v2，got %v", row.VerifySource)
	}
}

func TestContractStoreVerificationWriterRejectsBadInput(t *testing.T) {
	st, id := openVerificationFixture(t)
	ctx := context.Background()
	w := NewStoreVerificationWriter(st)

	// 未知状态：必须在写库前拒绝，且不改变已有行（防止把漂移后的枚举写成 unverified）。
	if err := w.WriteVerification(ctx, id, VerifyState("weird"), "x", 1); err == nil {
		t.Fatal("未知鉴定状态应报错")
	}
	row, err := st.Repos().Models.GetRealModel(ctx, id)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if row.VerifyStatus != store.VerifyUnverified || row.VerifySource != nil {
		t.Fatalf("非法状态不应写库，got %+v", row)
	}

	// 不存在的行：ErrNotFound 可被 errors.Is 识别（装配层据此区分“行没了”与“库坏了”）。
	if err := w.WriteVerification(ctx, "no-such-row", VerifyVerified, "x", 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在的 real_model 应返回 ErrNotFound，got %v", err)
	}

	// nil 写回器：结构化错误，不 panic（装配渐进期的安全网）。
	var nilWriter *StoreVerificationWriter
	if err := nilWriter.WriteVerification(ctx, id, VerifyVerified, "", 1); err == nil {
		t.Fatal("nil 写回器应返回错误")
	}
	// 空状态按 unverified（默认放行语义），且 source 仍可追溯。
	if err := w.WriteVerification(ctx, id, VerifyState(""), "无状态传入", 1); err != nil {
		t.Fatalf("空状态应按 unverified 放行，got %v", err)
	}
	row, err = st.Repos().Models.GetRealModel(ctx, id)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if row.VerifyStatus != store.VerifyUnverified || row.VerifySource == nil || *row.VerifySource != "builtin_v1" {
		t.Fatalf("空状态应落 unverified + builtin_v1，got %+v", row)
	}
}

// TestContractVerifySourceMapping 锁死「int 版本 → 文本标识」的唯一映射。
func TestContractVerifySourceMapping(t *testing.T) {
	cases := map[int]string{0: "builtin_v0", 1: "builtin_v1", 2: "builtin_v2", 99: "builtin_v99"}
	for in, want := range cases {
		if got := VerifySourceOf(in); got != want {
			t.Errorf("VerifySourceOf(%d)=%q，want %q", in, got, want)
		}
	}
}

// TestContractRefreshWritesVerificationColumns 是端到端接线测试：
// NewModelRefresherWithHooks + 真实 store 写回器 → 刷新后 real_models 三列有值。
// 它回答的正是巡检的质疑：「凡在池中的行都至少过过一遍判定」现在成立。
func TestContractRefreshWritesVerificationColumns(t *testing.T) {
	st, refresher, fake, provider := openRefreshFixture(t)
	ctx := context.Background()

	var upstreamID string
	ups, err := st.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		t.Fatalf("列上游失败: %v", err)
	}
	for _, u := range ups {
		if u.Name == provider {
			upstreamID = u.ID
		}
	}
	if upstreamID == "" {
		t.Fatal("fixture 上游不存在")
	}

	hooks := &RefreshHooks{Verification: NewStoreVerificationWriter(st)}
	r := NewModelRefresherWithHooks(st, fake, time.Minute, nil, hooks)
	fake.setCatalog(provider, []dshprovider.ModelInfo{
		{UpstreamModelID: "cold-model-zzz", DisplayName: "冷门模型"},
		{UpstreamModelID: "claude-4.5-sonnet", DisplayName: "Claude 4.5"},
	})
	if _, err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("RefreshOnce 失败: %v", err)
	}
	_ = refresher // 旧构造器不参与本用例，保留为了 fixture 的初始化副作用

	rows, err := st.Repos().Models.ListRealModelsByUpstream(ctx, upstreamID)
	if err != nil {
		t.Fatalf("列目录行失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应有 2 行目录，got %d", len(rows))
	}
	for _, row := range rows {
		if row.VerifySource == nil || *row.VerifySource != "builtin_v1" {
			t.Errorf("%s：刷新后 verify_source 应为 builtin_v1，got %v", row.UpstreamModelID, row.VerifySource)
		}
		if row.VerifyStatus == "" {
			t.Errorf("%s：刷新后 verify_status 不得为空", row.UpstreamModelID)
		}
	}
}
