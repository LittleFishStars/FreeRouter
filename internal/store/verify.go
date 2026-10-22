package store

import (
	"context"
	"fmt"
	"sort"
)

// VerifyReport 是 --verify-store 的自检结果（M10 §12「恢复演练」验收项）。
type VerifyReport struct {
	// Migrations 是 schema_migrations 的已应用版本数（应与 migrations 目录一致）。
	Migrations int
	// Tables 是实际存在的关键表（缺失即视为库不可用）。
	Tables []string
	// Missing 是应当存在但缺失的表。
	Missing []string
}

// keyTables 是「库可用」的最低表集合。
//
// 为什么不是全部表：迁移新增表时这份清单不该跟着改（否则每次加表都要改自检，
// 而自检的价值在稳定）。这里只放**缺了就无法提供服务**的表——账户/密钥/模型/
// 上游/请求/审计，任何一张缺了都说明迁移没跑完或库是坏的。
var keyTables = []string{
	"accounts", "api_keys", "sessions", "account_quotas",
	"abstract_models", "real_models", "upstreams",
	"requests", "usage_daily", "audit_log", "schema_migrations",
}

// Verify 校验库可用性：迁移是否全部应用、关键表是否齐备。
//
// 为什么单独一个方法而不是让 --verify-store 直接跑 SQL：
// store 的写池是包内私有（单写者语义靠它保证），把裸 SQL 暴露给 main
// 等于让装配层可以绕过事务边界；自检逻辑放在包内，边界不破。
func (s *Store) Verify(ctx context.Context) (VerifyReport, error) {
	var rep VerifyReport
	if s == nil || s.writeDB == nil {
		return rep, fmt.Errorf("store: 库未打开")
	}

	if err := s.writeDB.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&rep.Migrations); err != nil {
		return rep, fmt.Errorf("store: 读 schema_migrations 失败（库不是本项目的或迁移未跑）: %w", err)
	}

	for _, t := range keyTables {
		var n int
		// 表名是编译期常量（keyTables），不是外部输入——这里不接受参数化的原因见上。
		q := fmt.Sprintf(`SELECT count(*) FROM %s LIMIT 1`, t)
		if err := s.writeDB.QueryRowContext(ctx, q).Scan(&n); err != nil {
			rep.Missing = append(rep.Missing, t)
			continue
		}
		rep.Tables = append(rep.Tables, t)
	}
	sort.Strings(rep.Tables)
	if len(rep.Missing) > 0 {
		return rep, fmt.Errorf("store: 关键表缺失 %v（迁移未跑完或库被替换）", rep.Missing)
	}
	return rep, nil
}
