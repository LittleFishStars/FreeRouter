// task-12 装配缺口的契约测试：GroupIDsOfAccount / ListVisibleRealModels /
// relay base_url 语义。全部 t.TempDir()，不污染仓库。
// 组成员关系的写路径（AddMember 等）在 M7——测试直接 SQL 插入 group_members，
// 因为 FK 要求账户与组先存在（这也顺便验证了 FK 生效）。

package store

import (
	"context"
	"fmt"
	"testing"
)

var seedGroupSeq int

// seedGroup 插一个组并返回 id（组写路径在 M7，测试直插）。
// 名字带递增序号：同一测试里多次调用，groups.name 有 UNIQUE。
func seedGroup(t *testing.T, s *Store) int64 {
	t.Helper()
	seedGroupSeq++
	res, err := s.writeDB.Exec(`INSERT INTO groups(name, created_at) VALUES(?, ?)`,
		fmt.Sprintf("g-%s-%d", t.Name(), seedGroupSeq), now())
	if err != nil {
		t.Fatalf("建测试组失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("读组 id 失败: %v", err)
	}
	return id
}

// addMember 直插成员关系（同上，M7 前测试直插）。
func addMember(t *testing.T, s *Store, groupID, accountID int64) {
	t.Helper()
	if _, err := s.writeDB.Exec(
		`INSERT INTO group_members(group_id, account_id) VALUES(?, ?)`,
		groupID, accountID); err != nil {
		t.Fatalf("插成员关系失败: %v", err)
	}
}

// TestContractGroupIDsOfAccount：空账户返回空切片非 nil 非 nil 错误；
// 多组账户返回全部组 id 且升序；删组（CASCADE）后成员关系消失。
func TestContractGroupIDsOfAccount(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	acct := mustAccount(t, s)

	// 空账户：空切片 + nil（不是 nil 切片——调用方 len() 两可，但断言语义
	// 上空切片是「查过了、确实没有」，nil 切片是「没查」）。
	got, err := s.Repos().Groups.GroupIDsOfAccount(ctx, acct)
	if err != nil {
		t.Fatalf("空账户查询报错: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("空账户 = %v，期望空切片", got)
	}

	// 多组：三个组乱序插入，返回按 group_id 升序。
	g1, g2, g3 := seedGroup(t, s), seedGroup(t, s), seedGroup(t, s)
	addMember(t, s, g3, acct)
	addMember(t, s, g1, acct)
	addMember(t, s, g2, acct)
	got, err = s.Repos().Groups.GroupIDsOfAccount(ctx, acct)
	if err != nil {
		t.Fatalf("多组查询报错: %v", err)
	}
	if len(got) != 3 || got[0] != g1 || got[1] != g2 || got[2] != g3 {
		t.Errorf("组 id = %v，期望 [%d %d %d] 升序", got, g1, g2, g3)
	}

	// 删组 → 成员行 CASCADE 消失 → 查询结果同步收窄（「组的停用=删除」语义，
	// 03-groups §6；这也是 GroupIDsOfAccount 不过滤组状态的依据——不存在
	// 「组还在但停用」的数据形态）。
	if _, err := s.writeDB.Exec(`DELETE FROM groups WHERE id = ?`, g2); err != nil {
		t.Fatalf("删组失败: %v", err)
	}
	got, err = s.Repos().Groups.GroupIDsOfAccount(ctx, acct)
	if err != nil {
		t.Fatalf("删组后查询报错: %v", err)
	}
	if len(got) != 2 || got[0] != g1 || got[1] != g3 {
		t.Errorf("删组后组 id = %v，期望 [%d %d]", got, g1, g3)
	}

	// 不存在的账户：空切片非错误（同「账户不在任何组」语义，调用方决定口径）。
	got, err = s.Repos().Groups.GroupIDsOfAccount(ctx, 99999)
	if err != nil || len(got) != 0 {
		t.Errorf("不存在账户 = %v, %v，期望空切片 nil", got, err)
	}
}

// TestContractListVisibleRealModels：enabled 上游 + status=active + hidden=0
// 三重过滤；missing/disabled/hidden/停用上游的行都不出现在对外目录。
func TestContractListVisibleRealModels(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	// 两个上游：一个 enabled relay（base_url=sidecar://provider 定案值），
	// 一个 disabled 的。真实模型行的 status/hidden 各形态都铺一排。
	up1, err := s.Repos().Models.UpsertUpstream(ctx, "relay", "fake_provider", "sidecar://fake_provider", nil, true)
	if err != nil {
		t.Fatalf("建 relay 上游失败: %v", err)
	}
	up2, err := s.Repos().Models.UpsertUpstream(ctx, "vendor", "openai", "https://api.example.invalid/v1", nil, false)
	if err != nil {
		t.Fatalf("建 disabled vendor 上游失败: %v", err)
	}

	visible, err := s.Repos().Models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up1, UpstreamModelID: "visible-chat", DisplayName: "Visible",
		Status: RealModelActive})
	if err != nil {
		t.Fatalf("建可见模型失败: %v", err)
	}
	_, _ = visible, up2

	// 隐藏行：hidden=1 —— 直插（UpsertRealModel 不暴露 hidden，那是运维面）。
	if _, err := s.writeDB.Exec(
		`UPDATE real_models SET hidden = 1 WHERE id = ?`, visible); err != nil {
		t.Fatalf("隐藏 visible-chat 失败: %v", err)
	}
	// missing 行：目录刷新的正常产物。keepIDs 故意不含 gone-chat 的 id
	// （模拟它从目录消失）；visible-chat 与稍后的 ok-chat 都在 keepIDs 里。
	if _, err := s.Repos().Models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up1, UpstreamModelID: "gone-chat", DisplayName: "Gone",
		Status: RealModelActive}); err != nil {
		t.Fatalf("建 missing 前身模型失败: %v", err)
	}
	// ok-chat 先建（进 keepIDs，不能被连带标 missing）。
	if _, err := s.Repos().Models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up1, UpstreamModelID: "ok-chat", DisplayName: "OK",
		Status: RealModelActive}); err != nil {
		t.Fatalf("建 ok-chat 失败: %v", err)
	}
	var okID int64
	if err := s.writeDB.QueryRow(
		`SELECT id FROM real_models WHERE upstream_model_id='ok-chat'`).Scan(&okID); err != nil {
		t.Fatalf("读 ok-chat id 失败: %v", err)
	}
	if _, err := s.Repos().Models.MarkMissingRealModels(ctx, up1, []int64{visible, okID}); err != nil {
		t.Fatalf("标记 missing 失败: %v", err)
	}
	// disabled 状态行：人工停用。
	if _, err := s.writeDB.Exec(
		`UPDATE real_models SET status='disabled' WHERE upstream_model_id='gone-chat'`); err != nil {
		t.Fatalf("停用 gone-chat 失败: %v", err)
	}
	// 停用上游的 active 行。
	if _, err := s.Repos().Models.UpsertRealModel(ctx, RealModel{
		UpstreamID: up2, UpstreamModelID: "on-disabled-upstream", DisplayName: "D",
		Status: RealModelActive}); err != nil {
		t.Fatalf("建停用上游模型失败: %v", err)
	}
	rows, err := s.Repos().Models.ListVisibleRealModels(ctx)
	if err != nil {
		t.Fatalf("列可见模型失败: %v", err)
	}
	var ids []string
	for _, m := range rows {
		ids = append(ids, m.UpstreamModelID)
	}
	if len(ids) != 1 || ids[0] != "ok-chat" {
		t.Errorf("可见模型 = %v，期望只有 [ok-chat]（hidden/missing/disabled/停用上游全滤掉）", ids)
	}
}

// TestContractRelayBaseURLSemantics：base_url 定案回归——
// relay 行存 "sidecar://<provider>"（provider 锚点，非 HTTP 端点），
// UpsertUpstream 对该形状放行且幂等（同 kind+name 冲突走 UPDATE）。
func TestContractRelayBaseURLSemantics(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	id1, err := s.Repos().Models.UpsertUpstream(ctx, "relay", "fake_provider", "sidecar://fake_provider", nil, true)
	if err != nil {
		t.Fatalf("sidecar:// 形状应放行: %v", err)
	}
	// 同 kind+name 再 upsert：走 ON CONFLICT 更新而非报错（幂等装配）。
	id2, err := s.Repos().Models.UpsertUpstream(ctx, "relay", "fake_provider", "sidecar://fake_provider", nil, true)
	if err != nil {
		t.Fatalf("重复 upsert 应幂等: %v", err)
	}
	if id1 != id2 {
		t.Errorf("同 kind+name 的 upsert 应返回同一 id：%d vs %d", id1, id2)
	}

	ups, err := s.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		t.Fatalf("列上游失败: %v", err)
	}
	found := false
	for _, u := range ups {
		if u.Kind == "relay" && u.Name == "fake_provider" {
			found = true
			if u.BaseURL != "sidecar://fake_provider" {
				t.Errorf("relay base_url = %q，期望定案值 sidecar://fake_provider", u.BaseURL)
			}
		}
	}
	if !found {
		t.Fatal("relay 上游应存在")
	}

	// 空串仍拒绝（校验没被定案削弱）。
	if _, err := s.Repos().Models.UpsertUpstream(ctx, "relay", "x", "", nil, true); err == nil {
		t.Error("空 base_url 应拒绝")
	}
}
