package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationsFS 把 SQL 迁移文件打进二进制。
//
// 为什么 embed 而不是运行时读目录：部署形态是单个二进制 + 一个数据目录。
// 如果迁移文件在二进制外面，就多了一个"文件丢了/版本不对"的故障面，而且
// 部署包与库结构的对应关系会失去唯一事实。打进二进制后，二进制就是 schema 的定义。
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationFilePattern 是文件名的唯一合法形状：NNNN_描述.sql。
//
// 强约束的原因：05-storage.md 5.2 要求"编号连续 + 启动时校验"。
// 两个分支各自新增 0007 是并行开发的必然事件，宁可启动失败也不要静默跳过——
// 静默跳过会让两个环境的表结构在没有报错的情况下分叉。
var migrationFilePattern = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// bootstrapSQL 建 schema_migrations 本身。
//
// 为什么不把它放进 0001_init.sql：迁移器必须先读这张表才知道该不该跑 0001，
// 放进 0001 就成了循环依赖（要读表 → 要先建表 → 建表是 0001 → 要知道该不该跑 0001）。
const bootstrapSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
);`

// migration 是一条待应用（或已应用）的迁移。
type migration struct {
	version  int
	name     string
	filename string
	sql      string
	checksum string
}

// loadMigrations 从 embed 里读出全部迁移，按 version 升序，并校验编号连续、无重号。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: 读取嵌入的迁移目录失败: %w", err)
	}

	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			// 非 .sql 文件（如预留的 README）直接忽略，不当作错误：目录里放说明文件是正常习惯。
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf(
				"store: 迁移文件名 %q 不符合 NNNN_description.sql 约定（四位编号 + 小写描述）", name)
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("store: 迁移 %q 的编号无法解析: %w", name, err)
		}
		if v == 0 {
			// 0000 预留给 bootstrap（schema_migrations 自身）。
			return nil, fmt.Errorf("store: 迁移 %q 使用了保留编号 0000", name)
		}
		if prev, dup := seen[v]; dup {
			// 这正是"两个分支各自新增 0007"的场景，必须拒绝启动。
			return nil, fmt.Errorf("store: 迁移编号 %04d 重复（%q 与 %q）", v, prev, name)
		}
		seen[v] = name

		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("store: 读取迁移 %q 失败: %w", name, err)
		}
		sum := sha256.Sum256(body)
		out = append(out, migration{
			version:  v,
			name:     m[2],
			filename: name,
			sql:      string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}

	if len(out) == 0 {
		return nil, errors.New("store: 嵌入的迁移集为空——go:embed 的目录没有被打进二进制")
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })

	// 编号必须从 1 开始连续无洞。
	for i, m := range out {
		want := i + 1
		if m.version != want {
			return nil, fmt.Errorf(
				"store: 迁移编号不连续：期望 %04d，实际 %04d（%s）。缺号会让不同环境的表结构静默分叉",
				want, m.version, m.filename)
		}
	}
	return out, nil
}

// migrate 是启动路径上的迁移入口：引导 schema_migrations → 判定是否要迁移 →
// （可选）迁移前备份 → 在单个事务里应用所有未应用迁移。
//
// 为什么"全部未应用迁移放一个事务"而不是每条一个事务：SQLite 的 DDL 是事务性的，
// 一个事务意味着中途失败能整体回滚，不会留下"半升级"的库——那种库既不是 vN 也不是
// vM，除了人工干预没有出路。代价是一次迁移失败会让整批都不生效，但这正是我们要的：
// 失败就是 0 步，不是 N 步。
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.writeDB.ExecContext(ctx, bootstrapSQL); err != nil {
		return fmt.Errorf("store: 创建 schema_migrations 失败: %w", err)
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	// 已应用的迁移文件不能被事后修改，否则不同环境的表结构会因为"谁改了 0003"
	// 而不同，而版本号看起来一模一样。
	for _, m := range all {
		if got, ok := applied[m.version]; ok {
			if got != m.checksum {
				return fmt.Errorf(
					"store: 迁移 %s 的内容已被修改（库中 checksum=%s，当前文件=%s）。"+
						"已应用的迁移必须视为不可变：请新增一个迁移，而不是改旧文件",
					m.filename, got, m.checksum)
			}
		}
	}

	var pending []migration
	for _, m := range all {
		if _, ok := applied[m.version]; !ok {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		// 幂等的正常路径：重启不重复迁移。
		return nil
	}

	// UUID schema 重建迁移的旧库守卫：必须在备份与写入之前。
	if err := s.rejectLegacyIntegerDB(ctx, pending); err != nil {
		return err
	}

	// 备份必须在写入之前。只在"有库且确实要改结构"时才备份：
	// 全新库（还没建任何业务表）备份出来的是一个 0 字节的壳，没有价值。
	if s.opts.BackupDir != "" && len(applied) > 0 {
		path, err := s.backupBeforeMigration(ctx)
		if err != nil {
			// 备份失败必须阻塞迁移。一个"备份没成功但照跑迁移"的行为，等于把
			// 回滚手段在最需要它的时刻悄悄拿掉。
			return fmt.Errorf("store: 迁移前备份失败，已中止迁移: %w", err)
		}
		slog.InfoContext(ctx, "迁移前备份完成", "path", path)
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启迁移事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, m := range pending {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: 应用迁移 %s 失败: %w", m.filename, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES(?,?,?,?)`,
			m.version, m.name, m.checksum, now(),
		); err != nil {
			return fmt.Errorf("store: 记录迁移 %s 失败: %w", m.filename, err)
		}
		slog.InfoContext(ctx, "已应用迁移", "version", m.version, "name", m.name)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交迁移事务失败: %w", err)
	}
	return nil
}

// legacyGuardVersion 是「拆表重建为 UUID schema」那条迁移的编号。
// 写成常量而不是扫名字：名字会被改写，编号不会（改了就是新迁移）。
const legacyGuardVersion = 9

// legacyProbeTables 是守卫要查的表。为什么选这几张而不是全部：它们分别是
// 身份侧（accounts/api_keys）、供给侧（upstreams/upstream_credentials/real_models）
// 与事实侧（requests/audit_log）的代表，旧库里只要真用过，这几张不可能全空；
// 而它们都在 0001 里建，不存在「这张表还没建所以查不了」的分支。
var legacyProbeTables = []string{
	"accounts", "api_keys", "upstreams", "upstream_credentials",
	"real_models", "requests", "audit_log",
}

// rejectLegacyIntegerDB 拦住「已有数据的整型主键旧库」。
//
// 为什么必须拦：0009 是按依赖逆序 DROP 全部业务表再按 UUID 重建，它不搬数据。
// 旧库跑上去就是静默清空——而「静默清空一个用过的库」是任何存储层都不该做的
// 事，哪怕它会在启动日志里留下一行「已应用迁移 0009」。
//
// 为什么判据是「有没有行」而不是「id 列是不是 INTEGER」（这一点值得写下来，
// 因为后者是更直觉的写法、也是错的）：全新库也会先跑 0001~0008，那些表建出来
// 就是 INTEGER 主键——按列类型检测会把**全新库一起拦死**，程序永远起不来。
// 按「有没有数据」检测才同时满足两个要求：新库放行（表都是空的）、旧库报错
// （有数据不能悄悄丢）。
//
// 只在 0009 属于 pending 时才探测：否则每次启动都要对七张表跑一次 COUNT，
// 而 requests 在长期部署下是百万行量级——COUNT(*) 无 WHERE 在 InnoDB 上有
// 优化器捷径，SQLite 没有，那是真扫。空表上它很便宜，但只在必要时刻花。
func (s *Store) rejectLegacyIntegerDB(ctx context.Context, pending []migration) error {
	needGuard := false
	for _, m := range pending {
		if m.version == legacyGuardVersion {
			needGuard = true
			break
		}
	}
	if !needGuard {
		return nil
	}

	for _, table := range legacyProbeTables {
		// 表可能还不存在（全新库跑到一半、或用户手工删过表）：不存在就是没有数据，
		// 跳过而不是报错——这里的职责是「有没有旧数据」，不是「schema 对不对」。
		var has bool
		if err := s.writeDB.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table,
		).Scan(&has); err != nil {
			return fmt.Errorf("store: 迁移前检查旧库失败（表 %s 存在性）: %w", table, err)
		}
		if !has {
			continue
		}
		var n int64
		if err := s.writeDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			return fmt.Errorf("store: 迁移前检查旧库失败（表 %s 行数）: %w", table, err)
		}
		if n > 0 {
			return fmt.Errorf(
				"store: 检测到已有数据（%s 表 %d 行）的整型主键旧库，而本次改造只支持 UUID schema、不做数据迁移；"+
					"已中止迁移。请备份数据目录后删除 freerouter.db / .db-wal / .db-shm 让程序重建空库",
				table, n)
		}
	}
	return nil
}

// appliedMigrations 返回已应用迁移的 version → checksum 映射。
func (s *Store) appliedMigrations(ctx context.Context) (map[int]string, error) {
	rows, err := s.writeDB.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询已应用迁移失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, fmt.Errorf("store: 读取已应用迁移失败: %w", err)
		}
		out[v] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历已应用迁移失败: %w", err)
	}
	return out, nil
}

// backupBeforeMigration 用 VACUUM INTO 生成一份一致快照。
//
// 为什么不是 cp：WAL 模式下数据可能还在 -wal 里，直接 cp 主库文件会拿到一个
// 不含最新提交的、内部不一致的快照。VACUUM INTO 由 SQLite 保证一致性，且不需要停写。
//
// 文件名带时间戳前缀，天然可排序；同秒重复触发时加序号避免覆盖。
func (s *Store) backupBeforeMigration(ctx context.Context) (string, error) {
	if err := ensureDir(s.opts.BackupDir); err != nil {
		return "", err
	}

	base := strings.TrimSuffix(filepath.Base(s.opts.Path), filepath.Ext(s.opts.Path))
	stamp := time.Now().UTC().Format("20060102T150405Z")

	target := ""
	for i := 0; ; i++ {
		name := fmt.Sprintf("%s-%s-pre-migrate.sqlite", base, stamp)
		if i > 0 {
			name = fmt.Sprintf("%s-%s-pre-migrate-%d.sqlite", base, stamp, i)
		}
		candidate := filepath.Join(s.opts.BackupDir, name)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			target = candidate
			break
		} else if err != nil {
			return "", fmt.Errorf("store: 检查备份路径 %s 失败: %w", candidate, err)
		}
		if i > 100 {
			return "", errors.New("store: 无法为备份文件找到可用名称")
		}
	}

	// VACUUM INTO 不接受占位参数，只能拼字面量；quoteLiteral 负责转义单引号。
	if _, err := s.writeDB.ExecContext(ctx, "VACUUM INTO "+quoteLiteral(target)); err != nil {
		return "", fmt.Errorf("store: VACUUM INTO %s 失败: %w", target, err)
	}

	// 校验：05-storage.md 8.1 要求"备份也要被验证，否则它只是一个文件"。
	if err := verifySQLiteFile(ctx, target); err != nil {
		return "", fmt.Errorf("store: 备份文件校验失败（%s）: %w", target, err)
	}
	return target, nil
}

// verifySQLiteFile 对快照跑 integrity_check，确认它是一个可用的库。
func verifySQLiteFile(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check 返回 %q", result)
	}
	return nil
}
