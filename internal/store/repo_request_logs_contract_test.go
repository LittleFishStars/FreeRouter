package store

import (
	"context"
	"testing"
)

// TestContractRequestLogsErrorCodeFilter 守调用日志的 error_code 筛选（审计 P3）。
//
// 为什么必须在存储层守：前端一直在发 error_code，后端曾把它整条丢掉——结果是
// 「筛选项点了没反应」，而 handler 与仓储各自的单元测试都不报错。这里直接对
// SQL 条件与 total 同时断言：筛选必须**同时**作用于 COUNT 与 LIMIT 查询，
// 否则会出现「列表 1 行、total 仍是 3」的翻页错乱。
func TestContractRequestLogsErrorCodeFilter(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	acctID := mustAccount(t, s)

	insert := func(ts int64, status string, code *string) {
		t.Helper()
		if _, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO requests(ts, account_id, endpoint, status, error_code)
			 VALUES(?,?,'openai-chat',?,?)`, ts, acctID, status, code); err != nil {
			t.Fatalf("插入请求日志失败: %v", err)
		}
	}
	str := func(v string) *string { return &v }

	insert(1_700_000_001, "ok", nil)
	insert(1_700_000_002, "error", str("no_candidate"))
	insert(1_700_000_003, "error", str("rate_limited"))
	insert(1_700_000_004, "error", str("no_candidate"))

	list := func(f RequestLogFilter) ([]RequestLogRow, int64) {
		t.Helper()
		rows, total, err := s.Repos().Usage.ListRequestLogsByAccount(ctx, acctID, f)
		if err != nil {
			t.Fatalf("列请求日志失败: %v", err)
		}
		return rows, total
	}

	// 1) 不筛：四条全在（确认三态语义没有把无错误码的行误伤）。
	if rows, total := list(RequestLogFilter{}); len(rows) != 4 || total != 4 {
		t.Fatalf("不筛时 rows=%d total=%d，期望 4/4", len(rows), total)
	}

	// 2) 按错误码精确匹配：两条 no_candidate，total 必须同步（否则翻页错乱）。
	rows, total := list(RequestLogFilter{ErrorCode: "no_candidate"})
	if len(rows) != 2 || total != 2 {
		t.Fatalf("error_code=no_candidate 时 rows=%d total=%d，期望 2/2", len(rows), total)
	}
	for _, row := range rows {
		if row.ErrorCode == nil || *row.ErrorCode != "no_candidate" {
			t.Fatalf("筛选结果里混入了其它错误码: %v", row.ErrorCode)
		}
	}

	// 3) 精确匹配不是前缀匹配：rate_limited 不能被 no_candidate 的查询命中，
	//    也不能被 "no_cand" 这样的前缀命中（错误码是闭集合的机器码）。
	if rows, total := list(RequestLogFilter{ErrorCode: "no_cand"}); len(rows) != 0 || total != 0 {
		t.Fatalf("前缀查询应当 0 行，实际 rows=%d total=%d", len(rows), total)
	}

	// 4) 与 status=failed 组合：两个条件是正交的 AND，不是互相覆盖。
	if rows, total := list(RequestLogFilter{Failed: true, ErrorCode: "rate_limited"}); len(rows) != 1 || total != 1 {
		t.Fatalf("failed+rate_limited 时 rows=%d total=%d，期望 1/1", len(rows), total)
	}

	// 5) 分页与筛选一起用：offset 是筛选结果集内的偏移，不是全表偏移。
	if rows, total := list(RequestLogFilter{ErrorCode: "no_candidate", Limit: 1, Offset: 1}); len(rows) != 1 || total != 2 {
		t.Fatalf("筛选+翻页时 rows=%d total=%d，期望 1/2", len(rows), total)
	}
}
