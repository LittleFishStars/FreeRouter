package store

import (
	"context"
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"testing"
)

// TestContractRequestStatsBuckets 守仪表盘聚合（GET /api/usage/stats 的底层）。
//
// 为什么必须在存储层守：聚合 SQL 的三处细节在 handler 的假 store 测试里根本
// 触不到——(1) 按天分桶必须吃 tz_offset（否则「天」从 UTC 零点切）；(2) 失败
// 计数口径必须与列表端点一致（status <> 'ok'）；(3) scope=all（accountID=nil）
// 与「某个不存在账户」必须形状不同（前者有数据、后者恒空）。
func TestContractRequestStatsBuckets(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	acct := mustAccount(t, s)
	// 第二个账户：mustAccount 用 t.Name() 当用户名，同一测试里调两次会撞
	// UNIQUE 约束——这里手动建，只用来验证 scope=all 的整站口径。
	acct2, err := s.Repos().Accounts.CreateAccount(ctx,
		AccountAuth{Account: Account{DisplayName: "t2", Username: "acct2-" + t.Name()}, PasswordHash: "x"}, []Class{ClassTenant})
	if err != nil {
		t.Fatalf("建第二个测试账户失败: %v", err)
	}

	// 两条抽象模型（id 直接手插，与既有契约测试同法）。
	if _, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO abstract_models(id, name, display_name) VALUES(1,'smart','Smart')`); err != nil {
		t.Fatalf("插入模型失败: %v", err)
	}
	if _, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO abstract_models(id, name, display_name) VALUES(2,'fast','Fast')`); err != nil {
		t.Fatalf("插入模型失败: %v", err)
	}

	insert := func(accountID string, ts int64, status string, modelID any, in, out int64) {
		t.Helper()
		if _, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO requests(id, ts, account_id, endpoint, status, virtual_model_id, in_tokens, out_tokens)
			 VALUES(?,?,?,'openai-chat',?,?,?,?)`,
			idgen.New(), ts, accountID, status, modelID, in, out); err != nil {
			t.Fatalf("插入请求日志失败: %v", err)
		}
	}

	// 两个账户的数据：账户 1 三条（同一天两条成功 + 次日凌晨一条失败），
	// 账户 2 一条。时间戳故意贴着日界两侧：22:15 / 23:38 在同一天，
	// 00:13 在次日——这样 tz_offset 的合并效果可直接用桶数断言。
	insert(acct, 1_700_000_100, "ok", 1, 100, 200) // 2023-11-14 22:15 UTC
	insert(acct, 1_700_005_100, "ok", 2, 10, 20)   // 同日 23:38 UTC
	insert(acct, 1_700_036_000, "error", 1, 0, 0)  // 次日 00:13 UTC（失败）
	insert(acct2, 1_700_000_500, "ok", 1, 5, 5)
	// 1) 按账户：只统计自己的三条；失败口径 status <> 'ok'。
	stats, err := s.Repos().Usage.RequestStats(ctx, &acct, RequestLogFilter{}, 86400, 0)
	if err != nil {
		t.Fatalf("RequestStats 失败: %v", err)
	}
	if stats.Totals.Requests != 3 || stats.Totals.Failed != 1 {
		t.Fatalf("按账户合计 requests=%d failed=%d，期望 3/1", stats.Totals.Requests, stats.Totals.Failed)
	}
	if stats.Totals.InTokens != 110 || stats.Totals.OutTokens != 220 {
		t.Fatalf("Token 合计 in=%d out=%d，期望 110/220", stats.Totals.InTokens, stats.Totals.OutTokens)
	}
	if len(stats.Buckets) != 2 {
		t.Fatalf("两个 UTC 日应得 2 个桶，得到 %d", len(stats.Buckets))
	}
	if len(stats.Models) != 2 || stats.Models[0].Model != "smart" {
		t.Fatalf("按模型应 2 行且 smart（请求数 2）在前：%v", stats.Models)
	}

	// 2) scope=all（nil）：四个请求都进合计。
	all, err := s.Repos().Usage.RequestStats(ctx, nil, RequestLogFilter{}, 86400, 0)
	if err != nil {
		t.Fatalf("RequestStats(all) 失败: %v", err)
	}
	if all.Totals.Requests != 4 {
		t.Fatalf("整站合计应 4 条，得到 %d", all.Totals.Requests)
	}

	// 3) tz_offset 改变按天切桶：UTC 的两天（22:15/23:38 + 次日 00:13），
	//    加 +8h 后前两条落到次日早晨，与第三条同桶——桶数 2 → 1。这就是
	//    「按调用方当地零点切天」的直接证据。
	shifted, err := s.Repos().Usage.RequestStats(ctx, &acct, RequestLogFilter{}, 86400, 8*3600)
	if err != nil {
		t.Fatalf("RequestStats(tz) 失败: %v", err)
	}
	if len(shifted.Buckets) != 1 {
		t.Fatalf("+8h 后三条应合并进同一个当地日，得到 %d 个桶", len(shifted.Buckets))
	}
	// 小时桶：同一份数据拆回 3 个桶（22 点 / 23 点 / 次日 0 点）。
	hourly, err := s.Repos().Usage.RequestStats(ctx, &acct, RequestLogFilter{}, 3600, 0)
	if err != nil {
		t.Fatalf("RequestStats(hour) 失败: %v", err)
	}
	if len(hourly.Buckets) != 3 {
		t.Fatalf("小时桶应 3 个，得到 %d", len(hourly.Buckets))
	}

	// 4) 不存在账户：恒空形状（空切片 + 零值），不是错误。
	ghost := "999999"
	empty, err := s.Repos().Usage.RequestStats(ctx, &ghost, RequestLogFilter{}, 86400, 0)
	if err != nil {
		t.Fatalf("不存在账户不应报错: %v", err)
	}
	if empty.Totals.Requests != 0 || empty.Buckets == nil || empty.Models == nil {
		t.Fatalf("不存在账户应得零值 + 空切片，得到 %+v", empty)
	}

	// 5) from/to 语义与列表端点一致（0 = 不限，>0 = 闭区间）。
	ranged, err := s.Repos().Usage.RequestStats(ctx, &acct,
		RequestLogFilter{From: 1_700_000_000, To: 1_700_000_200}, 86400, 0)
	if err != nil {
		t.Fatalf("RequestStats(range) 失败: %v", err)
	}
	if ranged.Totals.Requests != 1 {
		t.Fatalf("范围内应只有 1 条，得到 %d", ranged.Totals.Requests)
	}
}
