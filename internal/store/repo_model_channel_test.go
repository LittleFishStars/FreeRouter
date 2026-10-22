// real_models 渠道列（0002 迁移）的契约测试。
//
// 背景（01-catalog.md §2.3）：`owned_by` → 派生「渠道」，限定名里的渠道段取自
// 这里。渠道是**模型级**属性（20 个前缀），与上游级标识（relay 的 provider 名、
// vendor 的行名）不是一回事——同一个 deepseek 模型在两个上游下是两条行，
// 渠道相同而上游不同。路由层此前用 channelOf(upstream) 得到的是上游级值。
//
// 三条语义必须锁住：①存量回填（有 `渠道/模型` 形态的补齐，裸 id 留 NULL）；
// ②显式入参优先于前缀派生（sidecar 拿到 owned_by 后走这条）；
// ③group_name 不参与——它是管理员标签，与渠道互不推导。

package store

import (
	"context"
	"strings"
	"testing"
)

// strp 造 *string：本文件多处断言"渠道有值/为 NULL"，可空指针表达期望最直白
// （nil 期望 = 期望落库 NULL，两者同一个 nil，不需要两套写法）。
func strp(s string) *string { return &s }

// channelOf 直接读列，绕开仓储读路径（仓储把 NULL 转成 nil 指针，
// 断言时用 != nil 就够，但显式读库更接近"迁移到底写没写"）。
func channelCol(t *testing.T, s *Store, id int64) *string {
	t.Helper()
	var v *string
	if err := s.writeDB.QueryRow(
		`SELECT channel FROM real_models WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("读 channel 列失败: %v", err)
	}
	return v
}

// TestContractChannelDerivedFromPrefix：入参不给 channel 时从前缀派生。
// 这条同时锁住"派生不出就是 NULL、不回退上游名"——vendor/selfhosted 的裸 id
// 回退上游级值会让同一列混两种语义，routing_bridge 就没法按渠道分组了。
func TestContractChannelDerivedFromPrefix(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	cases := []struct {
		id   string
		want *string // nil = 期望落 NULL
		why  string
	}{
		{"deepseek-official/deepseek-flash", strp("deepseek-official"), "标准 `渠道/模型`"},
		{"aggregate/auto", strp("aggregate"), "aggregate 渠道"},
		{"cline/grok-4", strp("cline"), "其他渠道"},
		{"a/b/c", strp("a"), "只取第一段（限定名只带一个渠道段）"},
		{"openai/gpt/x", strp("openai"), "模型 id 自身含斜杠不影响渠道段"},
		{"deepseek-chat", nil, "裸 id 派不出 → NULL"},
		{"/leading-slash", nil, "渠道段为空 → NULL（不留空串）"},
	}
	for _, c := range cases {
		id, err := models.UpsertRealModel(ctx, RealModel{
			UpstreamID: up, UpstreamModelID: c.id,
		})
		if err != nil {
			t.Fatalf("upsert %s 失败: %v", c.id, err)
		}
		got := channelCol(t, s, id)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s 渠道 = %q，期望 NULL（%s）", c.id, *got, c.why)
		case c.want != nil && got == nil:
			t.Errorf("%s 渠道 = NULL，期望 %q（%s）", c.id, *c.want, c.why)
		case c.want != nil && *got != *c.want:
			t.Errorf("%s 渠道 = %q，期望 %q（%s）", c.id, *got, *c.want, c.why)
		}
	}
}

// TestContractChannelExplicitWins：调用方显式给的 channel 优先于前缀派生。
// sidecar 给 OwnedBy 后走这条路径；派生只是"上游没说渠道"时的兜底。
// 这里用一个**故意与前缀矛盾**的值：显式值必须赢，否则 owned_by 一到位
// 派生值就会把它盖掉，等于白加字段。
func TestContractChannelExplicitWins(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	id, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "weird-prefix/model-x",
		Channel: strp("owned-by-says-this"),
	})
	if err != nil {
		t.Fatalf("upsert 失败: %v", err)
	}
	got := channelCol(t, s, id)
	if got == nil || *got != "owned-by-says-this" {
		t.Fatalf("显式 channel 未生效：got = %v", got)
	}

	// 下一轮刷新若不再传 channel，应回落到派生值（"上游没再说渠道"）。
	if _, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "weird-prefix/model-x",
	}); err != nil {
		t.Fatalf("刷新 upsert 失败: %v", err)
	}
	got = channelCol(t, s, id)
	if got == nil || *got != "weird-prefix" {
		t.Errorf("刷新后应回落到派生渠道 weird-prefix，got = %v", got)
	}
}

// TestContractChannelIndependentOfGroupName：group_name 与 channel 互不推导。
// group_name 是管理员自由填的目录分组（01-catalog.md:167，人工展示字段，
// 改过即冻结）；渠道是上游事实。管理员改个标签不该影响按渠道分组。
func TestContractChannelIndependentOfGroupName(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	// 只给 group_name，不给 channel → 渠道仍按前缀派生。
	id, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "deepseek-official/x",
		GroupName: strp("官方直连"),
	})
	if err != nil {
		t.Fatalf("upsert 失败: %v", err)
	}
	got, err := models.GetRealModel(ctx, id)
	if err != nil {
		t.Fatalf("读行失败: %v", err)
	}
	if got.Channel == nil || *got.Channel != "deepseek-official" {
		t.Errorf("渠道应按前缀派生，got = %v", got.Channel)
	}
	if got.GroupName == nil || *got.GroupName != "官方直连" {
		t.Errorf("group_name 未保留，got = %v", got.GroupName)
	}

	// 反向：只给 channel，group_name 仍为 NULL（不互相填充）。
	id2, err := models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up, UpstreamModelID: "cline/y",
		Channel: strp("cline"),
	})
	if err != nil {
		t.Fatalf("upsert 失败: %v", err)
	}
	got2, err := models.GetRealModel(ctx, id2)
	if err != nil {
		t.Fatalf("读行失败: %v", err)
	}
	if got2.GroupName != nil {
		t.Errorf("group_name 不该被渠道填充，got = %v", *got2.GroupName)
	}
}

// TestContractMigrationBackfillsChannel：0002 迁移的存量回填。
// 构造方式与 TestContractMigrationBackup 同款：先让 0001 落库并塞行，
// 再删掉 0002 的 schema_migrations 记录使其成为 pending，重开触发回填。
func TestContractMigrationBackfillsChannel(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	// 先用"列还不存在"的旧路径落数据不可能——0002 已经加列了。
	// 改为：插入时显式把 channel 写成 NULL，模拟"迁移前落库的老行"，
	// 再删 0002 记录让回填语句在真实迁移语义下重跑。
	seed := []string{
		"deepseek-official/deepseek-flash",
		"aggregate/auto",
		"bare-model-no-slash",
	}
	for _, mid := range seed {
		if _, err := models.UpsertRealModel(ctx, RealModel{
			UpstreamID: up, UpstreamModelID: mid,
		}); err != nil {
			t.Fatalf("播种 %s 失败: %v", mid, err)
		}
	}
	if _, err := s.writeDB.Exec(`UPDATE real_models SET channel = NULL`); err != nil {
		t.Fatalf("造 NULL 存量失败: %v", err)
	}
	// 回填语句直接从 embed 的迁移文件里抠出来执行，不在测试里手抄一份 SQL。
	// 为什么：手抄的副本与迁移文件一旦漂移（改了取值口径、漏了 WHERE 条件），
	// 测试照样绿——它验的是副本而不是发布物，白测。抠不到就 Fail，漂移也会红。
	backfill := backfillStatement(t)
	if _, err := s.writeDB.Exec(backfill); err != nil {
		t.Fatalf("回填语句执行失败: %v", err)
	}
	want := map[string]string{
		"deepseek-official/deepseek-flash": "deepseek-official",
		"aggregate/auto":                   "aggregate",
	}
	rows, err := s.writeDB.Query(`SELECT upstream_model_id, channel FROM real_models`)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		var mid string
		var ch *string
		if err := rows.Scan(&mid, &ch); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		seen++
		exp, ok := want[mid]
		if !ok {
			if ch != nil {
				t.Errorf("裸 id %s 不该回填渠道，got = %q", mid, *ch)
			}
			continue
		}
		if ch == nil || *ch != exp {
			t.Errorf("%s 渠道 = %v，期望 %q", mid, ch, exp)
		}
	}
	if seen != len(seed) {
		t.Errorf("回填范围应恰好 %d 行，实到 %d", len(seed), seen)
	}
	// 重跑一次必须无变化（幂等）：它是 WHERE channel IS NULL，重跑命中 0 行。
	res, err := s.writeDB.Exec(backfill)
	if err != nil {
		t.Fatalf("回填重跑失败: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Errorf("回填重跑又改了 %d 行，不幂等", n)
	}
}

// backfillStatement 从 0002 迁移文件里取出真正的回填 UPDATE 语句。
//
// 为什么非得从发布物里取：这条测试的全部价值在于"迁移里那条 UPDATE 确实把
// 存量渠道填对了"。在测试里手抄一条形似的 UPDATE，等于把发布物换成替身来验——
// 迁移改了取值口径而替身没改，测试仍绿，存量回填错了也无人发现。
//
// 为什么用"抠 UPDATE 子句"而不是"整份迁移跑一遍"：整份含 ALTER TABLE，而列已存在
// 时 SQLite 没有 ADD COLUMN IF NOT EXISTS，重放必然 duplicate column（这是有意为之，
// 见迁移注释）。这里要验的只是 DML 部分。
func backfillStatement(t *testing.T) string {
	t.Helper()
	raw, err := migrationsFS.ReadFile("migrations/0002_real_model_channel.sql")
	if err != nil {
		t.Fatalf("读 0002 迁移失败: %v", err)
	}
	var sb strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(trimmed), "ALTER TABLE") ||
			strings.HasPrefix(strings.ToUpper(trimmed), "CREATE INDEX") {
			continue
		}
		sb.WriteString(trimmed + "\n")
	}
	st := strings.TrimSpace(sb.String())
	if !strings.HasPrefix(strings.ToUpper(st), "UPDATE REAL_MODELS") {
		t.Fatalf("0002 里没找到回填 UPDATE 语句（抠到的是 %q）", st)
	}
	return st
}
