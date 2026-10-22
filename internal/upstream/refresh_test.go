// 模型目录刷新契约测试（task-7：TestContract 前缀 ≥6）。
//
// 覆盖：upsert 幂等 / last_seen_at 更新 / 消失模型标记与恢复 /
// 刷新失败不 panic 不误标 / override 保护 / 非 relay 上游跳过 / 空目录。
// 全部用 t.TempDir()，不污染仓库。
package upstream

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// fakeCatalog 是可控行为的目录源：返回固定目录或错误，并记录调用。
type fakeCatalog struct {
	mu      sync.Mutex
	catalog map[string][]dshprovider.ModelInfo
	err     error
	calls   []string
}

func (f *fakeCatalog) ModelsList(ctx context.Context, provider string) ([]dshprovider.ModelInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, provider)
	if f.err != nil {
		return nil, f.err
	}
	return f.catalog[provider], nil
}

func (f *fakeCatalog) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeCatalog) setCatalog(provider string, models []dshprovider.ModelInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catalog[provider] = models
}

func (f *fakeCatalog) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// openRefreshFixture 建一个临时库 + 一个 relay 上游 + 一个 vendor 上游。
// 返回 store、refresh、fake 源与 relay 上游名。
func openRefreshFixture(t *testing.T) (*store.Store, *ModelRefresher, *fakeCatalog, string) {
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
	// relay 上游：目录刷新的目标；vendor 上游：应被跳过（不产生 models.list 调用）。
	if _, err := st.Repos().Models.UpsertUpstream(ctx, "relay", "fake_provider", "sidecar://fake_provider", nil, true); err != nil {
		t.Fatalf("建 relay 上游失败: %v", err)
	}
	if _, err := st.Repos().Models.UpsertUpstream(ctx, "vendor", "openai", "https://api.example.invalid/v1", nil, true); err != nil {
		t.Fatalf("建 vendor 上游失败: %v", err)
	}

	fake := &fakeCatalog{catalog: map[string][]dshprovider.ModelInfo{}}
	r := NewModelRefresher(st, fake, time.Minute, nil)
	return st, r, fake, "fake_provider"
}

// seedCatalog 是三轮测试用的基准目录。
func seedCatalog() []dshprovider.ModelInfo {
	cw := 200000
	return []dshprovider.ModelInfo{
		{UpstreamModelID: "deepseek-chat", DisplayName: "DeepSeek Chat", ContextWindow: &cw, Modalities: []string{"text"}, Efforts: []string{"low", "high"}},
		{UpstreamModelID: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner", Modalities: []string{"text"}},
		{UpstreamModelID: "aggregate/auto", DisplayName: "", Modalities: nil, Efforts: nil},
	}
}

func realRows(t *testing.T, st *store.Store, upstreamID int64) []store.RealModel {
	t.Helper()
	rows, err := st.Repos().Models.ListRealModelsByUpstream(context.Background(), upstreamID)
	if err != nil {
		t.Fatalf("列真实模型失败: %v", err)
	}
	return rows
}

// TestContractRefreshUpsertsCatalog：首轮刷新把目录逐条落库。
// 断言行数、字段映射（display_name 空回退 id、ContextWindow *int→*int64、
// modalities/efforts JSON 原文）、status=active、last_seen_at 非空。
func TestContractRefreshUpsertsCatalog(t *testing.T) {
	st, r, fake, provider := openRefreshFixture(t)
	fake.setCatalog(provider, seedCatalog())

	res, err := r.RefreshOnce(context.Background())
	if err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if res.Upstreams != 1 || res.Models != 3 {
		t.Errorf("结果 = %+v，期望 Upstreams=1 Models=3", res)
	}

	stRows(t, st, res, provider)
}

// stRows 是 TestContractRefreshUpsertsCatalog 的续体（避免行超长拆出）。
func stRows(t *testing.T, st *store.Store, res RefreshResult, provider string) {
	t.Helper()
	ups, err := st.Repos().Models.ListUpstreams(context.Background())
	if err != nil {
		t.Fatalf("列上游失败: %v", err)
	}
	var relayID int64
	for _, u := range ups {
		if u.Kind == "relay" {
			relayID = u.ID
		}
	}
	rows := realRows(t, st, relayID)
	if len(rows) != 3 {
		t.Fatalf("real_models = %d 行，期望 3", len(rows))
	}
	byID := map[string]store.RealModel{}
	for _, row := range rows {
		byID[row.UpstreamModelID] = row
	}
	chat := byID["deepseek-chat"]
	if chat.Status != store.RealModelActive || chat.LastSeenAt == nil {
		t.Errorf("deepseek-chat = %+v，期望 active 且 last_seen_at 非空", chat)
	}
	if chat.ContextWindow == nil || *chat.ContextWindow != 200000 {
		t.Errorf("context_window = %v，期望 200000", chat.ContextWindow)
	}
	if chat.Modalities == nil || *chat.Modalities != `["text"]` {
		t.Errorf("modalities = %v，期望 [\"text\"] 原文", chat.Modalities)
	}
	if chat.Efforts == nil || *chat.Efforts != `["low","high"]` {
		t.Errorf("efforts = %v，期望 [\"low\",\"high\"] 原文", chat.Efforts)
	}
	// 渠道落库（01-catalog.md §2.3）：aggregate/auto 的渠道段是 aggregate，
	// 裸 id 的 deepseek-chat 派不出渠道 → NULL。两层（ModelOwnedBy → 仓储兜底）
	// 都拿不出时就是 NULL，不回退上游名——回退会让 channel 列混两种语义。
	agg := byID["aggregate/auto"]
	if agg.DisplayName != "aggregate/auto" {
		t.Errorf("display_name = %q，期望回退到 upstream_model_id", agg.DisplayName)
	}
	if agg.Channel == nil || *agg.Channel != "aggregate" {
		t.Errorf("aggregate/auto 渠道 = %v，期望 aggregate", agg.Channel)
	}
	if chat.Channel != nil {
		t.Errorf("deepseek-chat 渠道应为空（裸 id 派不出），实际 = %q", *chat.Channel)
	}
	if agg.ContextWindow != nil || agg.Modalities != nil {
		t.Errorf("缺省字段应为 NULL，实际 = %+v", agg)
	}
	_ = provider
	_ = res
}

// TestContractRefreshIdempotent：同一目录连刷三轮，行数不增长（upsert 幂等）。
func TestContractRefreshIdempotent(t *testing.T) {
	st, r, fake, provider := openRefreshFixture(t)
	fake.setCatalog(provider, seedCatalog())

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := r.RefreshOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮刷新失败: %v", i+1, err)
		}
	}
	ups, err := st.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		t.Fatalf("列上游失败: %v", err)
	}
	var relayID int64
	for _, u := range ups {
		if u.Kind == "relay" {
			relayID = u.ID
		}
	}
	if rows := realRows(t, st, relayID); len(rows) != 3 {
		t.Fatalf("三轮同目录后 real_models = %d 行，期望仍 3（幂等）", len(rows))
	}
}

// TestContractRefreshUpdatesLastSeen：目录里仍在的模型，last_seen_at 随每轮刷新推进。
// 手工把 last_seen_at 推回旧值再刷新，断言被写回新值（ALIGN-SPEC H：last_seen_at 必有）。
func TestContractRefreshUpdatesLastSeen(t *testing.T) {
	st, r, fake, provider := openRefreshFixture(t)
	fake.setCatalog(provider, seedCatalog())
	ctx := context.Background()

	if _, err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("首轮刷新失败: %v", err)
	}
	ups, _ := st.Repos().Models.ListUpstreams(ctx)
	var relayID int64
	for _, u := range ups {
		if u.Kind == "relay" {
			relayID = u.ID
		}
	}
	// 把第一轮的 last_seen_at 推回明显旧的值（当前时间 -1h）。
	old := time.Now().Add(-time.Hour).Unix() // store.now() 单位是 Unix 秒
	if err := st.WithTx(ctx, func(tx *store.Repos) error {
		_, err := tx.Models.UpsertRealModel(ctx, store.RealModel{
			UpstreamID: relayID, UpstreamModelID: "deepseek-chat",
			DisplayName: "DeepSeek Chat", Status: store.RealModelActive,
			LastSeenAt: &old,
		})
		return err
	}); err != nil {
		t.Fatalf("推回 last_seen_at 失败: %v", err)
	}
	before := time.Now().Unix()
	if _, err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("次轮刷新失败: %v", err)
	}
	for _, row := range realRows(t, st, relayID) {
		if row.UpstreamModelID != "deepseek-chat" {
			continue
		}
		if row.LastSeenAt == nil || *row.LastSeenAt < before {
			t.Errorf("last_seen_at = %v，期望 ≥ 次轮刷新时刻 %d", row.LastSeenAt, before)
		}
	}
}

// TestContractRefreshMarksMissing：目录里消失的模型被标 missing（保留行），
// 上游恢复时写回 active 并清 missing_since。
func TestContractRefreshMarksMissing(t *testing.T) {
	st, r, fake, provider := openRefreshFixture(t)
	fake.setCatalog(provider, seedCatalog())
	ctx := context.Background()

	if _, err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("首轮刷新失败: %v", err)
	}
	ups, _ := st.Repos().Models.ListUpstreams(ctx)
	var relayID int64
	for _, u := range ups {
		if u.Kind == "relay" {
			relayID = u.ID
		}
	}

	// 第二轮：目录里去掉 deepseek-reasoner。
	fake.setCatalog(provider, seedCatalog()[:1])
	if _, err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("次轮刷新失败: %v", err)
	}
	// 第二轮目录只剩 deepseek-chat：其余两个（含 aggregate/* 条目——它只是
	// 不作候选，目录里没了照样算消失）都应标 missing 且行保留。
	var reasoner, aggregate *store.RealModel
	for i, row := range realRows(t, st, relayID) {
		switch row.UpstreamModelID {
		case "deepseek-chat":
			if row.Status != store.RealModelActive {
				t.Errorf("目录内模型 %s 应保持 active，实际 %s", row.UpstreamModelID, row.Status)
			}
		case "deepseek-reasoner":
			reasoner = &realRows(t, st, relayID)[i]
		case "aggregate/auto":
			aggregate = &realRows(t, st, relayID)[i]
		}
	}
	for _, gone := range []*store.RealModel{reasoner, aggregate} {
		if gone == nil {
			t.Fatal("消失模型行应保留（标记而非删除）")
		}
		if gone.Status != store.RealModelMissing || gone.MissingSince == nil {
			t.Errorf("消失模型 %s = %+v，期望 missing 且 missing_since 非空", gone.UpstreamModelID, gone)
		}
	}

	// 第三轮：模型恢复 → active + missing_since 清空。
	fake.setCatalog(provider, seedCatalog())
	if _, err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("恢复轮刷新失败: %v", err)
	}
	for _, row := range realRows(t, st, relayID) {
		if row.Status != store.RealModelActive || row.MissingSince != nil {
			t.Errorf("恢复轮后 %s = %+v，期望全部 active 且 missing_since 清空", row.UpstreamModelID, row)
		}
	}
}

// TestContractRefreshFailureNoPanic：sidecar 崩溃（源返回错误）时，
// 刷新返回错误、不 panic、不误标 missing（拉取失败≠模型真的没了）。
func TestContractRefreshFailureNoPanic(t *testing.T) {
	st, r, fake, provider := openRefreshFixture(t)
	fake.setCatalog(provider, seedCatalog())
	ctx := context.Background()

	if _, err := r.RefreshOnce(ctx); err != nil {
		t.Fatalf("首轮刷新失败: %v", err)
	}
	ups, _ := st.Repos().Models.ListUpstreams(ctx)
	var relayID int64
	for _, u := range ups {
		if u.Kind == "relay" {
			relayID = u.ID
		}
	}

	boom := errors.New("sidecar 崩溃模拟")
	fake.setErr(boom)
	res, err := r.RefreshOnce(ctx)
	if err == nil && len(res.Failures) == 0 {
		t.Fatal("源故障时应有失败记录")
	}
	if len(res.Failures) == 1 {
		if !errors.Is(res.Failures[0].Err, boom) {
			t.Errorf("失败原因应包含原始错误，实际 %v", res.Failures[0].Err)
		}
		if res.Failures[0].Upstream != provider {
			t.Errorf("失败归属 = %q，期望 %q", res.Failures[0].Upstream, provider)
		}
	}
	// 关键断言：上一轮的目录行必须原样保留（不被误标 missing、不删除）。
	for _, row := range realRows(t, st, relayID) {
		if row.Status != store.RealModelActive {
			t.Errorf("%s 应保持 active（失败不误标），实际 %s", row.UpstreamModelID, row.Status)
		}
	}
}

// TestContractRefreshEmptyCatalog：空目录是合法应答——无行、无失败、不 panic。
func TestContractRefreshEmptyCatalog(t *testing.T) {
	_, r, fake, provider := openRefreshFixture(t)
	fake.setCatalog(provider, nil)

	res, err := r.RefreshOnce(context.Background())
	if err != nil {
		t.Fatalf("空目录不应报错: %v", err)
	}
	if len(res.Failures) != 0 {
		t.Errorf("空目录不应计失败: %+v", res.Failures)
	}
	if res.Models != 0 {
		t.Errorf("空目录落库数 = %d，期望 0", res.Models)
	}
}

// TestContractRefreshOnlyRelay：只刷 relay 类；vendor/selfhosted 与 disabled 的
// 上游不产生 models.list 调用（provider 名只对应 sidecar provider）。
func TestContractRefreshOnlyRelay(t *testing.T) {
	st, r, fake, _ := openRefreshFixture(t)
	ctx := context.Background()

	// vendor 上游给目录也没用——刷新器不应调用它。
	fake.setCatalog("openai", seedCatalog())
	// 一个 disabled 的 relay。
	if _, err := st.Repos().Models.UpsertUpstream(ctx, "relay", "disabled_provider", "sidecar://disabled_provider", nil, false); err != nil {
		t.Fatalf("建 disabled 上游失败: %v", err)
	}

	res, err := r.RefreshOnce(ctx)
	if err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if res.Upstreams != 1 {
		t.Errorf("参与上游数 = %d，期望 1（只有 relay 且 enabled）", res.Upstreams)
	}
	if fake.callCount() != 1 {
		t.Errorf("models.list 调用 = %d 次，期望 1 次（vendor/disabled 跳过）", fake.callCount())
	}
}

// TestContractRefresherDefaults：interval<=0 兜底为 DefaultModelRefreshInterval（10m）。
func TestContractRefresherDefaults(t *testing.T) {
	st, _, _, _ := openRefreshFixture(t)
	r := NewModelRefresher(st, &fakeCatalog{}, 0, nil)
	if r.Interval() != DefaultModelRefreshInterval {
		t.Errorf("兜底间隔 = %v，期望 %v", r.Interval(), DefaultModelRefreshInterval)
	}
	neg := NewModelRefresher(st, &fakeCatalog{}, -time.Second, nil)
	if neg.Interval() != DefaultModelRefreshInterval {
		t.Errorf("负值兜底间隔 = %v，期望 %v", neg.Interval(), DefaultModelRefreshInterval)
	}
}

// TestContractRefreshStartStopsCleanly：Start 的周期循环随 ctx 取消干净退出
// （失败不阻塞启动：Start 不返回错误、不阻塞调用方）。
func TestContractRefreshStartStopsCleanly(t *testing.T) {
	_, r, fake, provider := openRefreshFixture(t)
	fake.setCatalog(provider, seedCatalog())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Start(ctx)
		close(done)
	}()
	// Start 是旁路 goroutine：立即返回、不阻塞。
	cancel()
	select {
	case <-done:
		// goroutine 退出（可能在下一 tick 前）。
	case <-time.After(3 * DefaultModelRefreshInterval):
		t.Fatal("周期循环未随 ctx 取消退出")
	}
}
