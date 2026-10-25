// 候选声明序（0008 迁移）的契约测试。
//
// 背景（2026-10-26 用户决定 + 03-strategy.md §3.3.2）：候选集**不设权重**了。
// 排序条件（expiry-first / balance / speed / price）次序相同时，按管理员在
// 候选集编辑器里排列的顺序决定先后——声明的次序本身就是声明的一部分。
//
// 这条测试守的是「声明序真的被保存、真的被读回」，而不是「某条 SQL 的形状」：
//
//	旧实现：ListCandidates 用 ORDER BY weight DESC, real_model_id。
//	于是「声明序」被偷换成了「权重序 + 行 id 序」——两个都不是管理员排的顺序。
//	更要命的是 weight 平时恒为 0（界面上没有输入框），所有候选挤在同一个
//	权重值上，真正决定先后的是 real_model_id，也就是**建库时哪条先插**。
//
//	新实现：写时按入参切片顺序编号（sort_order = 0,1,2…），
//	读时 ORDER BY sort_order, real_model_id。
//
// 为什么这张测试必须有：声明序是**唯一**的平局决胜依据，而它不体现在任何
// 业务字段上——一旦有人把排序键改回去（哪怕是为了"性能"），线上次序会静默
// 变化，而所有功能测试照样通过。这类漂移只能靠断言顺序本身来钉。

package store

import (
	"context"
	"testing"
)

// seedCandidateModels 建 n 个真实模型行（同一上游），返回它们的行 id。
// 用真实模型当候选是因为 abstract_candidates.real_model_id 有外键约束。
func seedCandidateModels(t *testing.T, s *Store, n int) []int64 {
	t.Helper()
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	out := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		id, err := s.Repos().Models.UpsertRealModel(ctx, RealModel{
			UpstreamID:      up,
			UpstreamModelID: "cand-" + string(rune('a'+i)),
		})
		if err != nil {
			t.Fatalf("建真实模型 %d 失败: %v", i, err)
		}
		out = append(out, id)
	}
	return out
}

// TestContractCandidateDeclarationOrderRoundTrip 钉住「声明序 = 保存顺序」。
//
// 关键设计：三条候选的 weight **完全相同**（都是 0），所以旧实现
// （ORDER BY weight DESC, real_model_id）的结果是 real_model_id 序——
// 恰好与入参顺序不同（入参是 b, a, c 对应的 id 升序 a<b<c）。
// 于是这条测试在旧实现下必然失败：它不是"碰巧不通过"，而是精确地
// 区分了「按保存顺序读」与「按 id 读」。
func TestContractCandidateDeclarationOrderRoundTrip(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	models := s.Repos().Models

	ids := seedCandidateModels(t, s, 3)
	amID, err := models.CreateAbstractModel(ctx, AbstractModel{
		Name: "ordered", DisplayName: "ordered", Status: ModelActive, CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}

	// 故意按「b, a, c」保存（即 id 序是 a<b<c，与保存顺序相反）。
	saveOrder := []int64{ids[1], ids[0], ids[2]}
	cands := make([]Candidate, 0, len(saveOrder))
	for _, id := range saveOrder {
		cands = append(cands, Candidate{AbstractModelID: amID, RealModelID: id})
	}
	if err := models.SetCandidates(ctx, amID, cands); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}

	got, err := models.ListCandidates(ctx, amID)
	if err != nil {
		t.Fatalf("列候选失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应有 3 条候选，得 %d", len(got))
	}
	for i, want := range saveOrder {
		if got[i].RealModelID != want {
			t.Fatalf("第 %d 条候选应是保存顺序里的 %d，实得 %d（读序变成了 id 序，说明声明序没落库）",
				i, want, got[i].RealModelID)
		}
	}
	// sort_order 应是 0,1,2（声明序的显式载体）。
	for i, c := range got {
		if c.SortOrder != int64(i) {
			t.Fatalf("第 %d 条候选的 sort_order 应为 %d，实得 %d", i, i, c.SortOrder)
		}
	}
}

// TestContractCandidateOrderSurvivesRewrite 钉住「整表替换后顺序按新入参」：
// 候选集是整表替换语义（PUT 全量），所以顺序必须跟着新入参走，
// 不能残留上一次保存的编号。
func TestContractCandidateOrderSurvivesRewrite(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	models := s.Repos().Models

	ids := seedCandidateModels(t, s, 3)
	amID, err := models.CreateAbstractModel(ctx, AbstractModel{
		Name: "rewrite", DisplayName: "rewrite", Status: ModelActive, CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}

	// 第一次：a, b
	if err := models.SetCandidates(ctx, amID, []Candidate{
		{AbstractModelID: amID, RealModelID: ids[0]},
		{AbstractModelID: amID, RealModelID: ids[1]},
	}); err != nil {
		t.Fatalf("首次设候选失败: %v", err)
	}
	// 第二次（整表替换）：c, a —— 顺序应完全按新入参
	if err := models.SetCandidates(ctx, amID, []Candidate{
		{AbstractModelID: amID, RealModelID: ids[2]},
		{AbstractModelID: amID, RealModelID: ids[0]},
	}); err != nil {
		t.Fatalf("二次设候选失败: %v", err)
	}

	got, err := models.ListCandidates(ctx, amID)
	if err != nil {
		t.Fatalf("列候选失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("整表替换后应剩 2 条，得 %d", len(got))
	}
	if got[0].RealModelID != ids[2] || got[1].RealModelID != ids[0] {
		t.Fatalf("替换后顺序应按新入参（c, a），实得 %d, %d", got[0].RealModelID, got[1].RealModelID)
	}
	// 编号必须重排（0,1）而不是沿用旧的（1,2）——否则首条会缺一个 0，
	// 排序时 0 与 1 的相对关系仍对，但缺口说明编号没被重写。
	if got[0].SortOrder != 0 || got[1].SortOrder != 1 {
		t.Fatalf("替换后 sort_order 应重排为 0,1，实得 %d,%d", got[0].SortOrder, got[1].SortOrder)
	}
}

// TestContractCandidateWeightIsRetired 钉住「权重已退役」：
// 写入时恒写 0，读出也恒为 0——即使入参带了非 0 的 weight。
//
// 为什么写侧要丢掉入参的 weight：它是历史字段（03-strategy.md §3.3.2
// 明写内置策略全不读它）。如果写侧照抄入参，库里就会留下一批
// 「看起来还在生效」的数字，而读序已经不按它排了——那正是最坏的形态：
// 数据看着有含义，实际什么都不影响。恒写 0 让「它不生效」在数据上也成立。
func TestContractCandidateWeightIsRetired(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	models := s.Repos().Models

	ids := seedCandidateModels(t, s, 1)
	amID, err := models.CreateAbstractModel(ctx, AbstractModel{
		Name: "noweight", DisplayName: "noweight", Status: ModelActive, CapabilityMode: "strict",
	})
	if err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	if err := models.SetCandidates(ctx, amID, []Candidate{
		{AbstractModelID: amID, RealModelID: ids[0], Weight: 999},
	}); err != nil {
		t.Fatalf("设候选失败: %v", err)
	}
	got, err := models.ListCandidates(ctx, amID)
	if err != nil {
		t.Fatalf("列候选失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应有 1 条候选，得 %d", len(got))
	}
	if got[0].Weight != 0 {
		t.Fatalf("权重已退役，读出应恒为 0，实得 %d", got[0].Weight)
	}
}

// TestContractOverrideRowKeepsHumanFields 钉住 UpsertRealModel 的字段级
// 覆盖规则（01-catalog.md §3.2/§3.3：保护人工字段，不冻整行）。
//
// 旧实现的 ON CONFLICT 带 WHERE source='auto'——整行冻结。两个后果：
// ① 上游后来新增的模态/档位永远同步不进被人工改过的行（能力过期 → 白丢
//
//	可用候选）；② 手工登记行在目录重新出现后永远停在 missing。
//
// 新实现按字段类分流：上游权威字段恒更新，人工字段只在
// 「存量是 auto」或「本次入参是人工写」时更新。
func TestContractOverrideRowKeepsHumanFields(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	models := s.Repos().Models
	up := aggregateTestUpstream(t, s)

	// 管理员手工登记：显示名自定义、上下文自定义。
	id, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "m-x", DisplayName: "我的叫法",
		ContextWindow: ptr64(999999), Source: "override",
	})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}

	// 一轮"刷新"（auto 入参）：人工字段保留，权威字段更新。
	if _, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "m-x", DisplayName: "catalog-name",
		ContextWindow: ptr64(1), Modalities: ptrStr(`["text","image"]`),
		Status: RealModelActive, Source: "auto",
	}); err != nil {
		t.Fatalf("刷新 upsert 失败: %v", err)
	}
	got, err := models.GetRealModel(ctx, id)
	if err != nil {
		t.Fatalf("读失败: %v", err)
	}
	if got.DisplayName != "我的叫法" {
		t.Fatalf("override 行的显示名应被人工保留，得 %q", got.DisplayName)
	}
	if got.ContextWindow == nil || *got.ContextWindow != 999999 {
		t.Fatalf("override 行的上下文应被人工保留，得 %v", got.ContextWindow)
	}
	if got.Modalities == nil || *got.Modalities != `["text","image"]` {
		t.Fatalf("模态是上游权威字段，刷新必须同步进来，得 %v", strOf(got.Modalities))
	}
	if got.Source != "override" {
		t.Fatalf("auto 刷新不得降级 override 标记，得 %q", got.Source)
	}

	// 管理员再改（PATCH 的读-改-写：override 入参）：人工字段可更新。
	if _, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "m-x", DisplayName: "改过的叫法",
		ContextWindow: ptr64(888888), Modalities: ptrStr(`["text","image"]`),
		Status: RealModelActive, Source: "override",
	}); err != nil {
		t.Fatalf("二次人工写失败: %v", err)
	}
	got2, err := models.GetRealModel(ctx, id)
	if err != nil {
		t.Fatalf("读失败: %v", err)
	}
	if got2.DisplayName != "改过的叫法" {
		t.Fatalf("人工写（override 入参）必须能改 override 行的人工字段，得 %q", got2.DisplayName)
	}
}

// ptr64 / ptrStr：可空列的测试指针小工具。
func ptr64(v int64) *int64    { return &v }
func ptrStr(v string) *string { return &v }
func strOf(v *string) string {
	if v == nil {
		return "<nil>"
	}
	return *v
}
