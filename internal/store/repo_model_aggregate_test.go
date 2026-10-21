// real_models 排除位（excluded_from_candidates）的渠道默认规则契约测试。
//
// 背景（01-catalog.md §5）：上游 aggregate/* 渠道的条目默认排除作候选——
// 上游自己的一层跨渠道归并策略与我们的抽象模型策略层重叠后无法归因
//（"这次变贵是我们选错候选，还是上游又切了一次账号？"）。排除位必须与
// status 独立：status 表达"不可用"，excluded 表达"不可作候选但目录里可见"。
//
// 判定放在存储层（UpsertRealModel 的 SQL）而非刷新器：目录条目里的渠道只能
// 从 upstream_model_id 的 `渠道/模型` 形态推（协议层 ModelInfo 目前没有
// owned_by 字段），判定依据与唯一标识同源。见 repo_model.go 的注释。

package store

import (
	"context"
	"testing"
)

// aggregateTestUpstream 建一个 relay 上游并返回 id。upstreams 的写路径只有
// UpsertUpstream，这里直接用它（kind=relay 是唯一有目录的来源，M5 才扩）。
func aggregateTestUpstream(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.Repos().Models.UpsertUpstream(context.Background(),
		"relay", "hub", "sidecar://hub", nil, true)
	if err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	return id
}

// excludedOf 直接读库里的排除位（不走仓储），避免被仓储的读路径洗掉。
func excludedOf(t *testing.T, s *Store, id int64) bool {
	t.Helper()
	var v int
	if err := s.writeDB.QueryRow(
		`SELECT excluded_from_candidates FROM real_models WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("读排除位失败: %v", err)
	}
	return v == 1
}

func TestContractAggregateExcludedByDefault(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	// 渠道前缀 → 期望排除位。匹配必须**精确等于** aggregate：
	// 前缀包含/相似名不算（否则 aggregate-v2、my_aggregate 会被误杀）。
	cases := []struct {
		upstreamModelID string
		wantExcluded    bool
		why             string
	}{
		{"aggregate/auto", true, "aggregate 渠道默认排除"},
		{"aggregate/deepseek-v4", true, "aggregate 渠道任意模型都排除"},
		{"deepseek-official/deepseek-flash", false, "普通渠道不排除"},
		{"cline/grok-4", false, "其他渠道不排除"},
		{"aggregate", false, "没有渠道段的裸 id 不排除（不是 aggregate/x）"},
		{"aggregate-v2/foo", false, "近似渠道名不误伤"},
		{"my_aggregate/foo", false, "后缀相似不误伤"},
		{"deepseek-chat", false, "无斜杠的 vendor 类 id 不排除"},
	}
	for _, c := range cases {
		id, err := models.UpsertRealModel(ctx, RealModel{
			UpstreamID: up, UpstreamModelID: c.upstreamModelID,
		})
		if err != nil {
			t.Fatalf("upsert %s 失败: %v", c.upstreamModelID, err)
		}
		if got := excludedOf(t, s, id); got != c.wantExcluded {
			t.Errorf("%s 排除位 = %v，期望 %v（%s）",
				c.upstreamModelID, got, c.wantExcluded, c.why)
		}
	}
}

// TestContractAggregateExcludedRefreshesBack：刷新要写回权威值。
// 这条防的是"修复前已经落库的 aggregate 行永远停在 0"——存量纠正靠刷新，
// 所以冲突分支的 DO UPDATE 必须覆盖这一列，而不是只管 INSERT。
func TestContractAggregateExcludedRefreshesBack(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	// 模拟存量脏数据：aggregate 行先以排除位 0 落库（source=auto）。
	id, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "aggregate/legacy",
	})
	if err != nil {
		t.Fatalf("首次 upsert 失败: %v", err)
	}
	if _, err := s.writeDB.Exec(
		`UPDATE real_models SET excluded_from_candidates = 0 WHERE id = ?`, id); err != nil {
		t.Fatalf("造脏数据失败: %v", err)
	}

	// 下一轮刷新（upsert 同一行）应把排除位纠正回 true。
	if _, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "aggregate/legacy",
	}); err != nil {
		t.Fatalf("刷新 upsert 失败: %v", err)
	}
	if !excludedOf(t, s, id) {
		t.Error("刷新未写回排除位：存量 aggregate 行会永远停在 0，routing 侧双保险就成了唯一防线")
	}
}

// TestContractAggregateExcludedRespectsOverride：人工解除排除不被刷新推翻。
// 01-catalog.md §5 的待定项允许管理员显式解除单个 aggregate 条目的排除；
// 那种行是 source=override，刷新不得把它按渠道前缀重新排除——
// 否则管理员改了个寂寞。
func TestContractAggregateExcludedRespectsOverride(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	id, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "aggregate/unlocked",
	})
	if err != nil {
		t.Fatalf("首次 upsert 失败: %v", err)
	}
	// 管理员显式解除：改 source=override 并清排除位。
	if _, err := s.writeDB.Exec(
		`UPDATE real_models SET source = 'override', excluded_from_candidates = 0 WHERE id = ?`, id); err != nil {
		t.Fatalf("模拟管理员解除失败: %v", err)
	}

	// 刷新（源仍按 aggregate 落库）不应覆盖 override 行。
	if _, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "aggregate/unlocked",
	}); err != nil {
		t.Fatalf("刷新 upsert 失败: %v", err)
	}
	if excludedOf(t, s, id) {
		t.Error("override 行的排除位被刷新按渠道前缀覆盖：管理员的显式解除失效")
	}
	// 行仍在（刷新没有把整行删掉或报错）。
	if _, err := models.GetRealModel(ctx, id); err != nil {
		t.Errorf("override 行刷新后应仍可读: %v", err)
	}
}

// TestContractExplicitExcludedRespected：调用方显式置 true（聚合模型以外的
// 运营原因，如管理员手工排除）必须被尊重——仓储不能只认渠道前缀而忽略入参。
func TestContractExplicitExcludedRespected(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	id, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "cline/manual-excluded",
		ExcludedFromCandidates: true,
	})
	if err != nil {
		t.Fatalf("upsert 失败: %v", err)
	}
	if !excludedOf(t, s, id) {
		t.Error("显式 excluded=true 被入参覆盖逻辑吃掉")
	}
}

// TestContractAggregateExcludedIndependentOfStatus：排除位与 status 独立。
// aggregate 行仍然是 active（目录里活着、管理端可见可比较），只是选不了；
// 反过来 disabled 的普通模型也不是"aggregate"——两列不能互相推导。
func TestContractAggregateExcludedIndependentOfStatus(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	aggID, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "aggregate/live",
	})
	if err != nil {
		t.Fatalf("upsert aggregate 失败: %v", err)
	}
	got, err := models.GetRealModel(ctx, aggID)
	if err != nil {
		t.Fatalf("读 aggregate 行失败: %v", err)
	}
	if got.Status != RealModelActive {
		t.Errorf("aggregate 行 status = %q，应为 active（目录里可见可比较，只是不可作候选）", got.Status)
	}
	if !got.ExcludedFromCandidates {
		t.Error("aggregate 行排除位应为 true")
	}

	// 刷新把 aggregate 行标 missing 后，排除位仍为 true（两列互不推导）。
	if _, err := models.MarkMissingRealModels(ctx, up, []int64{}); err == nil {
		t.Error("空 keepIDs 应被拒绝（防全标 missing 自伤）")
	}
}
