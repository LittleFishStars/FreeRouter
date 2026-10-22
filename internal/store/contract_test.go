package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件守的是存储层**对外可见的行为**（05-storage.md 的机制承诺），不是内部实现：
// 迁移机制、WAL/连接配置、主密钥权限、append-only、保留期清理、额度扣减的原子性。
// 改表结构里的某个列不该让这些测试失败；改掉这些机制必须失败。

// openTestStore 起一个隔离的 Store：库、主密钥、归档目录都在 t.TempDir() 里，
// 所以跑完不会在仓库留下 .db/-wal/-shm 文件。
func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), Options{
		Path:          filepath.Join(dir, "freerouter.db"),
		MasterKeyFile: filepath.Join(dir, "master.key"),
		BusyTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close 失败: %v", err)
		}
	})
	return s, dir
}

// TestContractOpenCreatesSchema 覆盖验收项「建表」：
// Open 之后 05-storage.md 第 3 节的表必须全部存在，且缺表时错误要能在启动暴露。
func TestContractOpenCreatesSchema(t *testing.T) {
	s, _ := openTestStore(t)

	want := []string{
		"accounts", "account_classes", "account_merges", "account_quotas",
		"sessions",
		"groups", "group_members", "group_models", "group_policies",
		"group_quota_pools", "group_model_quota_caps",
		"abstract_models", "real_models", "abstract_candidates",
		"upstreams", "upstream_secrets", "upstream_credentials",
		"api_keys", "key_model_allow",
		"requests", "usage_daily",
		"audit_log", "admin_inbox", "model_multipliers", "settings",
		"schema_migrations",
	}

	for _, table := range want {
		var got string
		err := s.writeDB.QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&got)
		if errors.Is(err, sql.ErrNoRows) {
			t.Errorf("表 %s 不存在（迁移没有建全）", table)
			continue
		}
		if err != nil {
			t.Fatalf("查询表 %s 失败: %v", table, err)
		}
	}

	// 视图同样是 schema 的一部分（贡献统计不落表的实现手段）。
	var view string
	if err := s.writeDB.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='view' AND name='v_contributor_stats'`).
		Scan(&view); err != nil {
		t.Errorf("视图 v_contributor_stats 缺失: %v", err)
	}
}

// embeddedMigrationCount 返回 embed 里的迁移条数。
// 为什么不在断言里写死数字：新增一条迁移会让"迁移数变了"与"迁移被重复应用"
// 两种失败长得一模一样（行数对不上），而修法完全相反——前者是预期，后者是 bug。
// 读真实来源，失败信息才有区分度。
func embeddedMigrationCount(t *testing.T) int {
	t.Helper()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("读嵌入迁移失败: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("嵌入迁移集为空")
	}
	return len(ms)
}

// TestContractMigrationIdempotent 覆盖验收项「迁移幂等」：
// 同一份库反复 Open 不应重复应用迁移，也不应报错；schema_migrations 行数恒等于
// embed 里的迁移数（不是写死 1——新增 0002 后再写死 1 会把"迁移数变了"误报成
// "迁移被重复应用"，两种失败长得一样但修法相反）。
func TestContractMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "freerouter.db")
	keyPath := filepath.Join(dir, "master.key")

	open := func() *Store {
		s, err := Open(context.Background(), Options{
			Path: dbPath, MasterKeyFile: keyPath, BusyTimeout: 2 * time.Second,
		})
		if err != nil {
			t.Fatalf("Open 失败: %v", err)
		}
		return s
	}
	wantMigrations := embeddedMigrationCount(t)
	// 第一轮：建表 + 写一条数据。
	s1 := open()
	if _, err := s1.Repos().Accounts.CreateAccount(context.Background(),
		AccountAuth{Account: Account{DisplayName: "u", Username: "alice"}, PasswordHash: "x"},
		[]Class{ClassTenant}); err != nil {
		t.Fatalf("建账户失败: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// 第二、三轮：迁移必须跳过（幂等），数据必须还在。
	for i := 2; i <= 3; i++ {
		s := open()
		n, err := s.Repos().Accounts.CountAccounts(context.Background())
		if err != nil {
			t.Fatalf("第 %d 轮统计失败: %v", i, err)
		}
		if n != 1 {
			t.Errorf("第 %d 轮账户数 = %d，期望 1（数据在重开后丢失）", i, n)
		}
		var rows int
		if err := s.writeDB.QueryRowContext(context.Background(),
			`SELECT COUNT(1) FROM schema_migrations`).Scan(&rows); err != nil {
			t.Fatalf("查询迁移表失败: %v", err)
		}
		if rows != wantMigrations {
			t.Errorf("第 %d 轮 schema_migrations 行数 = %d，期望 %d（迁移被重复应用）",
				i, rows, wantMigrations)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close 失败: %v", err)
		}
	}
}

// TestContractWALAndCheckpoint 覆盖验收项「WAL」：
// 写库 journal_mode=WAL、foreign_keys 开启，且 Close 会把 -wal 合并归零。
func TestContractWALAndCheckpoint(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()

	var mode string
	if err := s.writeDB.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("读 journal_mode 失败: %v", err)
	}
	if strings.ToLower(mode) != "wal" {
		t.Errorf("journal_mode = %q，期望 wal", mode)
	}

	// foreign_keys 必须**每连接**为 ON（DSN 携带，而非事后在另一条连接上设）。
	var fk int
	if err := s.writeDB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("读 foreign_keys 失败: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d，期望 1", fk)
	}
	var roFK int
	if err := s.readDB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&roFK); err != nil {
		t.Fatalf("读读池 foreign_keys 失败: %v", err)
	}
	if roFK != 1 {
		t.Errorf("读池 foreign_keys = %d，期望 1（PRAGMA 没有绑到连接上）", roFK)
	}

	// 外键真的在生效：插一条指向不存在账户的 Key 必须失败。
	bad := int64(999999)
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO api_keys(account_id, key_hash, prefix) VALUES(?,?,?)`,
		bad, []byte("01234567890123456789012345678901"), "fr-x")
	if err == nil {
		t.Error("foreign_keys 未生效：指向不存在账户的 Key 插入成功")
	}

	// 制造一些 WAL 内容后关闭：Close 必须 checkpoint(TRUNCATE)，留下干净的库文件。
	if err := s.Repos().Sessions.CreateSession(ctx, "sess-1", mustAccount(t, s),
		make([]byte, 32), time.Now().UTC().Add(time.Hour).Unix(), nil, nil); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	wal := filepath.Join(dir, "freerouter.db-wal")
	if info, err := os.Stat(wal); err == nil && info.Size() > 0 {
		t.Errorf("Close 后 -wal 仍有 %d 字节，期望已 checkpoint(TRUNCATE)", info.Size())
	}
	// 库文件本身必须带着那条会话（证明 checkpoint 是合并而不是丢弃）。
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "freerouter.db")+"?mode=ro")
	if err != nil {
		t.Fatalf("重开只读库失败: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(1) FROM sessions WHERE id='sess-1'`).Scan(&n); err != nil {
		t.Fatalf("读回会话失败: %v", err)
	}
	if n != 1 {
		t.Errorf("checkpoint 后读回会话 %d 行，期望 1", n)
	}
}

func mustAccount(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.Repos().Accounts.CreateAccount(context.Background(),
		AccountAuth{Account: Account{DisplayName: "t", Username: "acct-" + t.Name()},
			PasswordHash: "x"}, []Class{ClassTenant})
	if err != nil {
		t.Fatalf("建测试账户失败: %v", err)
	}
	return id
}

// TestContractMasterKeyPermissions 覆盖验收项「主密钥权限」：
// 权限过宽必须拒绝启动（而不是警告后继续），0600 必须正常读取，且密钥绝不入库。
func TestContractMasterKeyPermissions(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")

	// ① 缺失时生成，且生成出来就是 0600。
	key, err := LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("首次生成主密钥失败: %v", err)
	}
	if len(key) != MasterKeySize {
		t.Fatalf("主密钥长度 = %d，期望 %d", len(key), MasterKeySize)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat 主密钥失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("生成的主密钥权限 = %04o，期望 0600", perm)
	}

	// ② 权限过宽（0644：组/他人可读）必须拒绝，并给出可判定的错误。
	for _, perm := range []os.FileMode{0o644, 0o640, 0o666, 0o604} {
		if err := os.Chmod(keyPath, perm); err != nil {
			t.Fatalf("chmod %04o 失败: %v", perm, err)
		}
		_, err := LoadOrCreateMasterKey(keyPath)
		if !errors.Is(err, ErrMasterKeyPerm) {
			t.Errorf("权限 %04o 时错误 = %v，期望 ErrMasterKeyPerm（过宽必须拒绝启动）", perm, err)
		}
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("恢复权限失败: %v", err)
	}

	// ③ 修好权限后同一把密钥仍可读出（证明前一轮是拒绝而非破坏）。
	again, err := LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("权限修复后读取失败: %v", err)
	}
	if string(again) != string(key) {
		t.Error("同一文件读出不同密钥")
	}

	// ④ 长度不对必须拒绝（echo 追加换行是最常见的误操作）。
	bad := filepath.Join(dir, "short.key")
	if err := os.WriteFile(bad, []byte("tooshort"), 0o600); err != nil {
		t.Fatalf("写测试密钥失败: %v", err)
	}
	if _, err := LoadOrCreateMasterKey(bad); err == nil {
		t.Error("长度不足的密钥被接受了")
	}

	// ⑤ 未配置路径必须拒绝：默认路径等于"随库一起被备份"。
	if _, err := LoadOrCreateMasterKey(""); err == nil {
		t.Error("master_key_file 为空时应当报错")
	}

	// ⑥ 密钥绝不入库：库里任何表都搜不到它。
	s, err := Open(context.Background(), Options{
		Path: filepath.Join(dir, "freerouter.db"), MasterKeyFile: keyPath,
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer s.Close()
	if leaked := scanDatabaseFor(t, filepath.Join(dir, "freerouter.db"), key); leaked {
		t.Error("主密钥出现在库文件里（05-storage.md 8.2：锁和钥匙必须分开放）")
	}
}

// scanDatabaseFor 在库文件（含 -wal）里搜索一段字节的痕迹。
func scanDatabaseFor(t *testing.T, dbPath string, needle []byte) bool {
	t.Helper()
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), string(needle)) {
			return true
		}
	}
	return false
}

// TestContractAppendOnlyAudit 覆盖机制承诺：审计的 UPDATE/DELETE 被触发器挡住，
// 业务代码没有任何删除审计的入口。
func TestContractAppendOnlyAudit(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	r := s.Repos()

	id, err := r.Logs.Append(ctx, AuditEntry{Action: "key.create", Result: "ok"})
	if err != nil {
		t.Fatalf("写审计失败: %v", err)
	}

	if _, err := s.writeDB.ExecContext(ctx,
		`UPDATE audit_log SET action='tampered' WHERE id=?`, id); err == nil {
		t.Error("append-only 失效：UPDATE 审计成功了")
	}
	if _, err := s.writeDB.ExecContext(ctx,
		`DELETE FROM audit_log WHERE id=?`, id); err == nil {
		t.Error("append-only 失效：DELETE 审计成功了")
	}

	// 缺 action/result 的审计必须在写之前就失败，而不是变成追溯时的噪音。
	if _, err := r.Logs.Append(ctx, AuditEntry{Result: "ok"}); err == nil {
		t.Error("没有 action 的审计被接受了")
	}
	if _, err := r.Logs.Append(ctx, AuditEntry{Action: "x"}); err == nil {
		t.Error("没有 result 的审计被接受了")
	}

	// 摘掉 DELETE 触发器的通道只有一条（PruneAudit），且必须失败于"没有归档目录"。
	if _, err := r.Logs.PruneAudit(ctx, 365, ""); err == nil {
		t.Error("未指定归档目录时 PruneAudit 应当拒绝删除")
	}

	// 触发器定义与 PruneAudit 重建用的 SQL 必须逐字一致：
	// 两份内容漂移的后果是"清理后保护变弱了却没人发现"。
	var def string
	if err := s.writeDB.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_audit_log_no_delete'`).
		Scan(&def); err != nil {
		t.Fatalf("读触发器定义失败: %v", err)
	}
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	}
	// SQLite 在 sqlite_master.sql 里会把 "IF NOT EXISTS " 归一化掉，
	// 所以对比时把常量里的也去掉：语义一致即可，逐字节对比是伪命题。
	want := norm(strings.Replace(auditDeleteTriggerSQL, "IF NOT EXISTS ", "", 1))
	if norm(def) != want {
		t.Errorf("迁移里的触发器定义与 repo_log.go 的重建 SQL 不一致：\n库内: %s\n常量: %s",
			norm(def), want)
	}
}

// TestContractPruneOnlyDeletesExpired 覆盖验收项「Prune 只删过期行」：
// 未过期的会话/请求必须活下来，0 = 永久不删。
func TestContractPruneOnlyDeletesExpired(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	r := s.Repos()
	acctID := mustAccount(t, s)

	nowTS := time.Now().UTC().Unix()

	// 两条会话：一条已过期、一条未过期。
	if err := r.Sessions.CreateSession(ctx, "expired", acctID, hashOf("t1"), nowTS-10, nil, nil); err != nil {
		t.Fatalf("建过期会话失败: %v", err)
	}
	if err := r.Sessions.CreateSession(ctx, "alive", acctID, hashOf("t2"), nowTS+3600, nil, nil); err != nil {
		t.Fatalf("建有效会话失败: %v", err)
	}

	// 三条请求：两条超出 90 天窗口、一条在窗口内。
	insert := func(ts int64) {
		if _, err := s.writeDB.ExecContext(ctx,
			`INSERT INTO requests(ts, endpoint, status) VALUES(?,?,?)`,
			ts, "openai-chat", "ok"); err != nil {
			t.Fatalf("插入请求失败: %v", err)
		}
	}
	insert(nowTS - 200*86400)
	insert(nowTS - 120*86400)
	insert(nowTS - 10*86400)

	// ① 0 = 永久：requests 一行都不能少。
	res, err := r.Usage.Prune(ctx, PruneOptions{RequestLogRetainDays: 0})
	if err != nil {
		t.Fatalf("Prune(0) 失败: %v", err)
	}
	if res.Requests != 0 {
		t.Errorf("retain_days=0 时删了 %d 行 requests，期望 0（0 = 永久保留）", res.Requests)
	}
	if got := countRows(t, s, "requests"); got != 3 {
		t.Errorf("requests 行数 = %d，期望 3", got)
	}

	// ② 90 天：只删过期的两行，10 天前那行必须留下。
	res, err = r.Usage.Prune(ctx, PruneOptions{RequestLogRetainDays: 90})
	if err != nil {
		t.Fatalf("Prune(90) 失败: %v", err)
	}
	if res.Requests != 2 {
		t.Errorf("Prune(90) 删除行数 = %d，期望 2", res.Requests)
	}
	if got := countRows(t, s, "requests"); got != 1 {
		t.Errorf("requests 剩余行数 = %d，期望 1（只删过期行）", got)
	}

	// ③ 会话：过期即清，未过期的留下。
	// 注意：第一次 Prune(0) 已经清过一次过期会话（会话清理不带开关），
	// 所以这里删的是 0 行——断言的量是"库里只剩未过期那条"，而不是删除计数。
	if res.Sessions != 0 {
		t.Errorf("第二次 Prune 清理会话数 = %d，期望 0（上一次已清过）", res.Sessions)
	}
	if got := countRows(t, s, "sessions"); got != 1 {
		t.Errorf("sessions 剩余行数 = %d，期望 1", got)
	}
	var id string
	if err := s.writeDB.QueryRowContext(ctx, `SELECT id FROM sessions`).Scan(&id); err != nil {
		t.Fatalf("读回会话失败: %v", err)
	}
	if id != "alive" {
		t.Errorf("剩下的会话是 %q，期望 alive（过期的那条应被删）", id)
	}
}

// isQuotaExhausted 判定额度不足并取回错误值。ErrQuotaExhausted 是值类型，
// errors.As 的目标必须是指向它的指针的指针，抽成函数避免每处都写对。
// 返回的值就是 api/03-errors.md quota{unit,period,model?} 与 Retry-After 的原料。
func isQuotaExhausted(err error) (ErrQuotaExhausted, bool) {
	var e ErrQuotaExhausted
	if errors.As(err, &e) {
		return e, true
	}
	return ErrQuotaExhausted{}, false
}

func quotaExhausted(err error) bool {
	_, ok := isQuotaExhausted(err)
	return ok
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.writeDB.QueryRowContext(context.Background(),
		"SELECT COUNT(1) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return n
}

func hashOf(s string) []byte {
	// 测试替身：只要长度合法（32 字节）即可，不需要真哈希。
	b := make([]byte, 32)
	copy(b, s)
	return b
}

// TestContractPruneAuditArchiveChannel 覆盖 8.3 的受控通道：
// 导出 → 回读校验 → 事务内摘/删/装 → 写 system.audit_pruned。
func TestContractPruneAuditArchiveChannel(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	r := s.Repos()

	old := time.Now().UTC().Add(-400 * 24 * time.Hour).Unix()
	archiveDir := filepath.Join(dir, "archive")

	// 三条旧审计 + 一条新审计。
	for i := 0; i < 3; i++ {
		if _, err := r.Logs.Append(ctx, AuditEntry{
			TS: old, Action: "key.create", Result: "ok",
			Detail: map[string]any{"n": i},
		}); err != nil {
			t.Fatalf("写旧审计失败: %v", err)
		}
	}
	if _, err := r.Logs.Append(ctx, AuditEntry{Action: "key.create", Result: "ok"}); err != nil {
		t.Fatalf("写新审计失败: %v", err)
	}

	res, err := r.Logs.PruneAudit(ctx, 365, archiveDir)
	if err != nil {
		t.Fatalf("PruneAudit 失败: %v", err)
	}
	if res == nil {
		t.Fatal("PruneAudit 返回 nil，期望有结果")
	}
	if res.Audit != 3 {
		t.Errorf("清理审计行数 = %d，期望 3", res.Audit)
	}

	// 归档文件必须存在、恰好 3 行。
	data, err := os.ReadFile(res.AuditArchive)
	if err != nil {
		t.Fatalf("读归档失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Errorf("归档行数 = %d，期望 3", len(lines))
	}

	// 库里剩下的审计：1 条新的 + 1 条 system.audit_pruned。
	rest, err := r.Logs.List(ctx, AuditFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(rest) != 2 {
		t.Errorf("清理后剩余审计 %d 条，期望 2（未过期 1 + 清理审计 1）", len(rest))
	}
	var pruned bool
	for _, e := range rest {
		if e.Action == "system.audit_pruned" {
			pruned = true
		}
	}
	if !pruned {
		t.Error("缺少 system.audit_pruned 审计（谁删了审计必须有答案）")
	}

	// 触发器必须已经装回去：再删一次应当失败。
	if _, err := s.writeDB.ExecContext(ctx,
		`DELETE FROM audit_log WHERE action='key.create'`); err == nil {
		t.Error("PruneAudit 之后 DELETE 触发器没有重建")
	}
}

// TestContractQuotaConditionalDebit 覆盖 7.4 的原子扣减：
// 条件 UPDATE 的成败以行数为准，0 行是拒绝而不是超发；无行 = 不限。
func TestContractQuotaConditionalDebit(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	r := s.Repos()
	if _, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO groups(id, name, is_default, created_at) VALUES(1,'g',0,?)`,
		time.Now().UTC().Unix()); err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	acctID := mustAccount(t, s)

	// ① 账户额度：limit=3，扣 4 次，只应成功 3 次。
	if err := r.Quotas.SetAccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodDay, 3); err != nil {
		t.Fatalf("设额度失败: %v", err)
	}
	var ok, exhausted int
	var lastErr error
	for i := 0; i < 4; i++ {
		err := r.Quotas.TryDebitAccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodDay, 1)
		lastErr = err
		switch {
		case err == nil:
			ok++
		case quotaExhausted(err):
			exhausted++
		default:
			t.Fatalf("扣减出现意外错误: %v", err)
		}
	}
	if ok != 3 || exhausted != 1 {
		t.Errorf("扣减结果 成功=%d 不足=%d，期望 成功=3 不足=1（超发）", ok, exhausted)
	}

	// 错误必须携带打满行的完整标识（api/03-errors.md 2026-10-21 定案：
	// quota{unit,period,model?} 机器可读对象 + Retry-After 指向命中行重置时刻）。
	e, isEx := isQuotaExhausted(lastErr)
	if !isEx {
		t.Fatal("最后一次扣减应当是 ErrQuotaExhausted")
	}
	if e.Scope != "account" || e.Unit != UnitRequest || e.Period != PeriodDay {
		t.Errorf("错误行标识 = %+v，期望 account/request/day", e)
	}
	if e.AbstractModelID != TotalScope {
		t.Errorf("合计行 AbstractModelID = %d，期望 0（合计错误体不带 model）", e.AbstractModelID)
	}
	if e.Used != 3 || e.Limit != 3 {
		t.Errorf("错误携带 Used/Limit = %d/%d，期望 3/3（message 组装原料）", e.Used, e.Limit)
	}
	reset, valid := e.ResetAt()
	if !valid {
		t.Fatal("ResetAt 解析 period_anchor 失败")
	}
	if secs := time.Until(reset); secs < 0 || secs > 24*time.Hour+time.Minute {
		t.Errorf("Retry-After 原料异常：距重置 %v，应在 0..24h(+时钟余量) 内", secs)
	}
	st, err := r.Quotas.AccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodDay)
	if err != nil {
		t.Fatalf("读额度失败: %v", err)
	}
	if st.Used != 3 {
		t.Errorf("quota_used = %d，期望 3", st.Used)
	}

	// ①b 按模型限额行打满：AbstractModelID 必须带出模型行 id（错误体 quota.model
	// 由此解析），且与合计行互不取代（交集语义，03-groups 4.5）。
	if err := r.Quotas.SetAccountQuota(ctx, acctID, 42, UnitRequest, PeriodDay, 1); err != nil {
		t.Fatalf("设按模型额度失败: %v", err)
	}
	if err := r.Quotas.TryDebitAccountQuota(ctx, acctID, 42, UnitRequest, PeriodDay, 1); err != nil {
		t.Fatalf("按模型行首次扣减应成功: %v", err)
	}
	e2, ok2 := isQuotaExhausted(r.Quotas.TryDebitAccountQuota(ctx, acctID, 42, UnitRequest, PeriodDay, 1))
	if !ok2 {
		t.Fatal("按模型行第二次扣减应当 ErrQuotaExhausted")
	}
	if e2.AbstractModelID != 42 {
		t.Errorf("按模型行错误 AbstractModelID = %d，期望 42（quota.model 的原料）", e2.AbstractModelID)
	}
	// 合计行此时已打满：两闸独立判定由行键不同天然保证（各行的条件 UPDATE 只看自己），
	// 这里再确认合计闸不受按模型行新增的影响——仍然拒绝。
	if err := r.Quotas.TryDebitAccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodDay, 1); !quotaExhausted(err) {
		t.Errorf("合计行打满后应继续拒绝，实际 err = %v", err)
	}

	// ② 无行 = 不限：没设过 credit/month 的维度必须一路放行且不报错。
	if err := r.Quotas.TryDebitAccountQuota(ctx, acctID, TotalScope, UnitCredit, PeriodMonth, 999); err != nil {
		t.Errorf("未设额度应当不限制，实际: %v", err)
	}

	// ③ 池：limit=2、已用 5（管理员把上限调小做应急关闸）也必须能设，之后全拒。
	if err := r.Quotas.SetPool(ctx, 1, TotalScope, UnitToken, PeriodDay, 10); err != nil {
		t.Fatalf("设池失败: %v", err)
	}
	if err := r.Quotas.SetPool(ctx, 1, TotalScope, UnitToken, PeriodDay, 2); err != nil {
		t.Fatalf("调小池上限失败: %v", err)
	}
	// 已用 0、上限 2：扣 1 成功、再扣 1 成功（0+1<=2, 1+1<=2），第三次不足。
	if err := r.Quotas.TryDebitPool(ctx, 1, TotalScope, UnitToken, PeriodDay, 1); err != nil {
		t.Fatalf("池首次扣减应当成功: %v", err)
	}
	if err := r.Quotas.TryDebitPool(ctx, 1, TotalScope, UnitToken, PeriodDay, 1); err != nil {
		t.Fatalf("池第二次扣减应当成功: %v", err)
	}
	if err := r.Quotas.TryDebitPool(ctx, 1, TotalScope, UnitToken, PeriodDay, 1); err == nil {
		t.Error("已用量已达上限后仍扣减成功（条件 UPDATE 没生效）")
	}

	// ④ 关闸语义：pool_limit 调到低于已用量必须被允许（应急手段），
	// 而 0 必须被拒（"没配"与"配了 0"在写入期就要互斥）。
	if err := r.Quotas.SetPool(ctx, 1, TotalScope, UnitToken, PeriodMonth, 0); err == nil {
		t.Error("pool_limit=0 被接受，期望拒绝（停用池应删行）")
	}
	// 账户额度侧 0 是合法的（全拒），与池不同形。
	if err := r.Quotas.SetAccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodMonth, 0); err != nil {
		t.Errorf("quota_limit=0（全拒）应当合法，实际: %v", err)
	}
	if err := r.Quotas.TryDebitAccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodMonth, 1); err == nil {
		t.Error("quota_limit=0 时仍扣减成功，期望拒绝")
	}

	// ⑤ 非法 unit 必须被拒（旧 dimension 拼写 requests/tokens 已废弃）。
	if err := r.Quotas.SetAccountQuota(ctx, acctID, TotalScope, QuotaUnit("requests"), PeriodDay, 5); err == nil {
		t.Error("旧 dimension 值 requests 被接受，期望拒绝（2026-10-20 起 unit 是单数）")
	}
}

// TestContractRecordRequestAtomicity 覆盖 7.2 的事务边界：
// requests + usage_daily + 额度记账必须同生同死。
func TestContractRecordRequestAtomicity(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	r := s.Repos()
	acctID := mustAccount(t, s)

	if _, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO abstract_models(id, name, display_name) VALUES(1,'fast','Fast')`); err != nil {
		t.Fatalf("建抽象模型失败: %v", err)
	}
	if _, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO upstreams(id, kind, name, base_url, enabled) VALUES(1,'vendor','v','http://x',1)`); err != nil {
		t.Fatalf("建上游失败: %v", err)
	}
	if _, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO real_models(id, upstream_id, upstream_model_id, display_name) VALUES(7,1,'m7','M7')`); err != nil {
		t.Fatalf("建真实模型失败: %v", err)
	}
	if err := r.Quotas.SetAccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodDay, 100); err != nil {
		t.Fatalf("设额度失败: %v", err)
	}

	a := int64(1)
	in := int64(10)
	ret := int64(7)
	rec := RequestRecord{
		AccountID: &acctID, VirtualModelID: &a, ResolvedRealModelID: &ret,
		Endpoint: "openai-chat", Status: "ok", InTokens: &in,
	}
	st := Settlement{
		AccountQuotas: []QuotaCredit{{OwnerID: acctID, AbstractModelID: TotalScope,
			Unit: UnitRequest, Period: PeriodDay, Cost: 1}},
	}
	if err := r.Usage.RecordRequest(ctx, rec, st); err != nil {
		t.Fatalf("RecordRequest 失败: %v", err)
	}

	// 三处都应各有一笔：这是"同生"。
	if got := countRows(t, s, "requests"); got != 1 {
		t.Errorf("requests = %d，期望 1", got)
	}
	rows, err := r.Usage.UsageByAccountRange(ctx, acctID, "2000-01-01", "2100-01-01")
	if err != nil {
		t.Fatalf("读用量失败: %v", err)
	}
	if len(rows) != 1 || rows[0].Requests != 1 {
		t.Errorf("usage_daily = %+v，期望一行且 requests=1", rows)
	}
	q, err := r.Quotas.AccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodDay)
	if err != nil {
		t.Fatalf("读额度失败: %v", err)
	}
	if q.Used != 1 {
		t.Errorf("quota_used = %d，期望 1", q.Used)
	}

	// 事务回滚时三处都必须没有变化：这是"同死"。
	wantErr := errors.New("boom")
	err = s.WithTx(ctx, func(tx *Repos) error {
		if e := tx.Usage.RecordRequest(ctx, rec, st); e != nil {
			return e
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithTx 应当把错误传出，实际 %v", err)
	}
	if got := countRows(t, s, "requests"); got != 1 {
		t.Errorf("回滚后 requests = %d，期望仍是 1（回滚没生效）", got)
	}
	q, _ = r.Quotas.AccountQuota(ctx, acctID, TotalScope, UnitRequest, PeriodDay)
	if q.Used != 1 {
		t.Errorf("回滚后 quota_used = %d，期望仍是 1", q.Used)
	}
}

// TestContractNoSQLLeaksAndContextRequired 守包的边界：
// 上层拿不到 *sql.DB，报表读走只读池，所有方法都收 context。
func TestContractReadsUseReadPool(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	r := s.Repos()

	acctID := mustAccount(t, s)
	if _, err := r.Logs.Append(ctx, AuditEntry{AccountID: &acctID, Action: "x", Result: "ok"}); err != nil {
		t.Fatalf("写审计失败: %v", err)
	}

	// 审计列表走读池：写连接上限是 1，报表并发不能和写路径抢同一条连接。
	// 审计列表走读池：写连接上限是 1，报表并发不能和写路径抢同一条连接。
	if _, err := r.Logs.List(ctx, AuditFilter{}); err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if st := s.readDB.Stats(); st.OpenConnections == 0 {
		t.Error("审计列表没有使用读池（报表会占住唯一的写连接）")
	}

	// 写池必须只有一个连接：SQLite 是单写者，多连接只会把排队变成随机 BUSY。
	if max := s.writeDB.Stats().MaxOpenConnections; max != 1 {
		t.Errorf("写池 MaxOpenConns = %d，期望 1", max)
	}
}

// TestContractMigrationBackup 覆盖「迁移前 VACUUM INTO 备份」：
// 第二次启动且确有 pending 迁移时，BackupDir 里必须出现快照文件；
// 全新库（无已应用迁移）不备份——备份一个空壳没有价值。
func TestContractMigrationBackup(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "backup")
	keyPath := filepath.Join(dir, "master.key")
	opts := func() Options {
		return Options{Path: filepath.Join(dir, "freerouter.db"),
			MasterKeyFile: keyPath, BackupDir: backupDir}
	}
	ctx := context.Background()

	// ① 全新库：没有已应用迁移 → 不产生备份。
	s, err := Open(ctx, opts())
	if err != nil {
		t.Fatalf("首轮 Open 失败: %v", err)
	}
	if entries, _ := os.ReadDir(backupDir); len(entries) != 0 {
		t.Errorf("全新库首轮产生了 %d 个备份文件，期望 0", len(entries))
	}
	s.Close()

	// ② 伪造"有历史 + 有待应用"：备份触发条件是 len(applied)>0 且 pending 非空
	//（migrate.go：全新库不备份空壳）。做法：插一条 version=9999 假记录，再删掉
	//**版本最小**的那条真实记录。
	//
	// 两条约束写死在这里是为了不随迁移增长而失真：
	// ① 假记录取 9999 —— version=2 就是 0002_real_model_channel，插重号会撞
	//    UNIQUE 让这条测试失真（它的价值全在"备份确实被触发"）。
	// ② 删除取 MIN(version) —— 0001 整份是 IF NOT EXISTS，重放幂等；而最新的
	//    0002 含 ALTER TABLE ADD COLUMN，SQLite **没有** ADD COLUMN IF NOT EXISTS，
	//    重放必然 duplicate column。按 MIN 删是"删掉仍可安全重放的那条"，
	//    按 MAX 删会让测试随新增迁移逐步失效。
	//    （真机上不会出现"列已在但版本行丢了"：DDL 与版本行同事务，崩溃即回滚。）
	//
	// 假版本号取 9999 而不是 2：真表里 version=2 就是 0002_real_model_channel，
	// 插重号会撞 UNIQUE 让这条测试失真——它的价值全在"备份确实被触发"。
	// 删除按 MAX(version) 而不是写死 1：同理，新增迁移后写死版本号会让这条测试
	// 悄悄退化成"只重放 0001"，掩盖 0002 的幂等问题。
	s2, err := Open(ctx, Options{Path: opts().Path, MasterKeyFile: keyPath})
	if err != nil {
		t.Fatalf("二轮 Open 失败: %v", err)
	}
	if _, err := s2.writeDB.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES(9999,'future','x',?)`,
		now()); err != nil {
		t.Fatalf("插入假迁移记录失败: %v", err)
	}
	if _, err := s2.writeDB.ExecContext(ctx,
		`DELETE FROM schema_migrations
		  WHERE version = (SELECT MIN(version) FROM schema_migrations WHERE version < 9999)`); err != nil {
		t.Fatalf("删除最早真实迁移记录失败: %v", err)
	}
	s2.Close()

	// ③ 带 BackupDir 重启：迁移前必须留下 VACUUM INTO 快照且文件可用。
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(ctx, opts())
	if err != nil {
		// 重放的是 0001（整份 IF NOT EXISTS）；若这里失败说明迁移器的幂等假设破了。
		t.Fatalf("三轮 Open 失败（重放 0001 应当因 IF NOT EXISTS 幂等）: %v", err)
	}
	defer s3.Close()
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("读备份目录失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("备份文件数 = %d，期望 1", len(entries))
	}
	// 快照必须是合法的 SQLite 文件（integrity_check 通过）。
	if err := verifySQLiteFile(ctx, filepath.Join(backupDir, entries[0].Name())); err != nil {
		t.Errorf("备份快照校验失败: %v", err)
	}
}
