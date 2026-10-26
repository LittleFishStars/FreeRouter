package store

// migrate_uuid_guard_test.go —— 0009「UUID schema 重建」的迁移守卫测试。
//
// 0009 的做法是按依赖逆序 DROP 全部业务表再按 UUID 重建，**不搬数据**。
// 这与「迁移必须保住数据」的一般期待相反，所以它需要两道配套保证：
//
//  1. 空库上能正常跑完，产出 UUID schema（id 列是 TEXT，漏 id 的插入被拒）；
//  2. 有数据的旧库必须被**挡住**，而不是被静默清空。
//
// 第 2 点是这个文件存在的全部理由。判据也值得钉住：不是「id 列类型是 INTEGER」
// （全新库也是 INTEGER，那样会把新库一起拦死），而是「业务表里有没有行」。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestContractFreshDBGetsUUIDSchema 钉住空库跑完全部迁移后的最终形状。
func TestContractFreshDBGetsUUIDSchema(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	// id 列必须是 TEXT（UUID schema 落地了）。
	var idType string
	if err := s.writeDB.QueryRowContext(ctx,
		`SELECT type FROM pragma_table_info('accounts') WHERE name='id'`).Scan(&idType); err != nil {
		t.Fatalf("读 accounts.id 列类型失败: %v", err)
	}
	if !strings.EqualFold(idType, "TEXT") {
		t.Fatalf("accounts.id 应是 TEXT（UUID），实得 %q", idType)
	}

	// 漏 id 的插入必须被拒：SQLite 的 TEXT PRIMARY KEY **不强制 NOT NULL**，
	// 而多行 NULL 在唯一约束里互不冲突——不补 NOT NULL 就会造出永远查不到的
	// 幽灵行。这一条是 0009 里最容易漏的一处（实测过）。
	if _, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO accounts(display_name, username, password_hash, created_at)
		 VALUES('x','ghost','h',1)`); err == nil {
		t.Fatal("漏 id 的插入应被 NOT NULL 拒绝，否则会造出幽灵行")
	}

	// 合计哨兵的默认值是空串（不是 0，0 不是合法 UUID）。
	for _, tbl := range []string{"account_quotas", "group_quota_pools"} {
		var def any
		if err := s.writeDB.QueryRowContext(ctx,
			`SELECT dflt_value FROM pragma_table_info(?) WHERE name='abstract_model_id'`, tbl,
		).Scan(&def); err != nil {
			t.Fatalf("读 %s.abstract_model_id 默认值失败: %v", tbl, err)
		}
		if def == nil || def != "''" {
			t.Fatalf("%s.abstract_model_id 默认值应是空串哨兵，实得 %v", tbl, def)
		}
	}

	// append-only 触发器必须在新表上重建（DROP TABLE 会带走它）。
	if _, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO audit_log(id, ts, action, result) VALUES('a1', 1, 'x', 'ok')`); err != nil {
		t.Fatalf("审计插入失败: %v", err)
	}
	if _, err := s.writeDB.ExecContext(ctx, `UPDATE audit_log SET result='y' WHERE id='a1'`); err == nil {
		t.Fatal("audit_log 的 append-only 触发器没在新表上重建")
	}
}

// TestContractLegacyDBWithDataIsRejected 钉住旧库守卫：有数据就必须报错，
// 且错误文案要能让运维知道下一步做什么（备份 + 删库重建）。
func TestContractLegacyDBWithDataIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// 先手工造一个「已跑到 0008、且 accounts 有一行」的旧库：
	// 直接按旧顺序执行 0001~0008（它们是嵌入的，可从 migrationsFS 读）。
	s, err := Open(context.Background(), Options{
		Path: path, MasterKeyFile: filepath.Join(dir, "k.key"),
	})
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 用裸连接把库改造成「旧形态」：删掉 0009 的迁移记录，并把 accounts 换成
	// 整型主键 + 一行数据。这样下次 Open 会重新跑 0009，从而触发守卫。
	raw := openRawSQLite(t, path)
	if _, err := raw.Exec(`DELETE FROM schema_migrations WHERE version = 9`); err != nil {
		t.Fatalf("删迁移记录失败: %v", err)
	}
	// 0009 已经把表建成 TEXT 了，这里换成 INTEGER 主键的旧样子（等价于一个
	// 真正从 0001 时代升上来的库）。
	if _, err := raw.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatalf("重建 accounts 失败: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE accounts (
		id INTEGER PRIMARY KEY,
		display_name TEXT NOT NULL, username TEXT NOT NULL,
		password_hash TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		recovery_code_hash TEXT, created_at INTEGER NOT NULL, last_login_at INTEGER,
		note TEXT, rpm_limit INTEGER NOT NULL DEFAULT -1, tpm_limit INTEGER NOT NULL DEFAULT -1,
		max_concurrency INTEGER NOT NULL DEFAULT -1, must_change_password INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("建旧 accounts 失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO accounts(display_name, username, password_hash, created_at)
		VALUES('旧库用户','legacy','h',1)`); err != nil {
		t.Fatalf("写旧数据失败: %v", err)
	}
	_ = raw.Close()

	// 再 Open 一次：守卫必须拦住，且错误文案要含「重建」这个动作提示。
	_, err = Open(context.Background(), Options{
		Path: path, MasterKeyFile: filepath.Join(dir, "k.key"),
	})
	if err == nil {
		t.Fatal("有数据的旧库应被拒绝启动，实际却成功打开了")
	}
	for _, want := range []string{"整型主键旧库", "重建"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误文案应包含 %q 以指导运维动作，实得：%v", want, err)
		}
	}
}

// TestContractEmptyLegacySchemaIsAllowed 钉住守卫的放行侧：库里有表但**没有行**
// 时应该放行——这正是全新库的中间态（0001~0008 建出空表，0009 拆了重建）。
//
// 为什么这条与上一条同等重要：判据写成「id 列类型是 INTEGER 就拦」会让
// 全新库永远起不来；这条测试就是那个错误实现的照妖镜。
func TestContractEmptyLegacySchemaIsAllowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.db")

	s, err := Open(context.Background(), Options{
		Path: path, MasterKeyFile: filepath.Join(dir, "k.key"),
	})
	if err != nil {
		t.Fatalf("空库应能正常打开: %v", err)
	}
	defer func() { _ = s.Close() }()

	// 把 0009 的记录删掉，让下次启动重跑它；表全是空的，守卫应放行。
	if _, err := s.writeDB.Exec(`DELETE FROM schema_migrations WHERE version = 9`); err != nil {
		t.Fatalf("删迁移记录失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	s2, err := Open(context.Background(), Options{
		Path: path, MasterKeyFile: filepath.Join(dir, "k.key"),
	})
	if err != nil {
		t.Fatalf("空库重跑 0009 应放行，实际报错: %v", err)
	}
	defer func() { _ = s2.Close() }()

	// 重跑后仍是 UUID schema（幂等重建）。
	var idType string
	if err := s2.writeDB.QueryRow(
		`SELECT type FROM pragma_table_info('accounts') WHERE name='id'`).Scan(&idType); err != nil {
		t.Fatalf("读列类型失败: %v", err)
	}
	if !strings.EqualFold(idType, "TEXT") {
		t.Fatalf("重跑后 accounts.id 应仍是 TEXT，实得 %q", idType)
	}
}

// openRawSQLite 开一个不经 Store 封面的裸连接（迁移守卫测试要直接改 schema
// 与 schema_migrations，走 Store 的方法反而绕不开它自己的校验）。
//
// 为什么不自带 PRAGMA foreign_keys：本测试改的是 accounts 结构，期间可能短暂
// 违反外键；守卫只查行数，不需要外键语义。用默认值（关闭）让测试专注在被测行为。
func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("开裸连接失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("裸连接不可用: %v", err)
	}
	return db
}
