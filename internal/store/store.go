// Package store 是 FreeRouter 的持久层：SQLite(WAL) 上的迁移、主密钥管理与类型化仓储。
//
// 唯一事实源：docs/architecture/05-storage.md（表结构、索引、迁移机制、WAL/连接配置）
// 与 docs/ops/02-deploy.md §2.7（store.* 配置项）。
//
// 本包的边界（为什么这么切）：
//   - 上层只看到类型化方法，看不到 *sql.DB 与任何 SQL 文本。SQL 一旦泄漏到转发路径，
//     索引是否命中、事务边界在哪里就没人能保证了（05-storage.md 第 4、7 节）。
//   - 连接分成 writeDB / readDB 两个池。SQLite 任意时刻只有一个写者，允许 N 条并发写
//     连接不会提升吞吐，只会把排队从应用层搬进 busy_timeout，把确定性的串行化换成
//     随机的 SQLITE_BUSY。读池用 mode=ro，读连接无法升级为写事务，因此不会意外阻塞
//     checkpoint——这是"长报表把 -wal 撑大"的根治手段。
//   - 主密钥永不入库（PLAN 9）：落到同一个库里，等于把锁和钥匙放进同一个抽屉。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动，CGO-free（PLAN 7）
)

// Options 是打开存储层需要的一切。刻意只依赖这几个标量，而不是整个 config.Config：
// store 包不应该反向依赖配置包的结构，否则配置一改就要动持久层。
type Options struct {
	// Path 是主库文件路径（store.path）。父目录不存在时会被创建。
	Path string
	// MasterKeyFile 是主密钥文件路径（store.master_key_file），必须 0600 且与库分开存放。
	MasterKeyFile string
	// BusyTimeout 是写冲突时的等待上限（store.busy_timeout）。
	BusyTimeout time.Duration
	// BackupDir 非空时，迁移前会用 VACUUM INTO 生成一份带时间戳的快照。
	BackupDir string
	// ReadConns 是只读池的连接上限；<=0 时取默认值 4（05-storage.md 6.1）。
	ReadConns int
	// AuditRetainDays / RequestLogRetainDays 为 0 表示永久保留（0 = 不删）。
	AuditRetainDays      int
	RequestLogRetainDays int
}

// defaultReadConns 与 05-storage.md 6.1 的表格一致。
const defaultReadConns = 4

// Store 是持久层的门面。零值不可用，必须由 Open 构造。
type Store struct {
	opts Options

	// writeDB 上限为 1：显式串行化所有写。
	writeDB *sql.DB
	// readDB 是 mode=ro 的只读池，只服务报表与控制台列表。
	readDB *sql.DB

	// masterKey 只在内存里；绝不写库、绝不打日志。
	masterKey []byte
	// tx 非空时表示这个 Store 是一个事务视图，newRepos 会用 tx 代替 writeDB。
	// 正常路径上它是 nil。
	tx *sql.Tx

	// repos 在 WithTx 期间复用同一组仓储，保证事务原子性。
	repos *Repos

	closeOnce sync.Once
	closeErr  error
}

// Repos 汇总所有类型化仓储，省去上层逐个构造。
type Repos struct {
	Accounts *AccountRepo
	Keys     *KeyRepo
	Sessions *SessionRepo
	Models   *ModelRepo
	Usage    *UsageRepo
	Logs     *LogRepo
	Quotas   *QuotaRepo
	// Groups 组成员关系读路径（task-12 装配缺口；写路径在 M7）。
	Groups *GroupRepo
	// Inbox 待处理事项（task-19）：只追加，无删除接口。
	Inbox *InboxRepo
	// Secrets 上游密钥材料的 AES-GCM 密文读写（task-19）。
	Secrets *SecretRepo
	// Settings 运行时开关的 key→JSON 原文（M10 设置页）。
	Settings *SettingsRepo
}

// Open 打开主库：建目录 → 打开写池 → 设 PRAGMA → 迁移 → 打开读池 → 载入主密钥。
//
// 顺序为什么这样：迁移必须在读池出现之前完成（否则读池可能看到一个半迁移的 schema），
// 而主密钥放在最后——迁移与schema 都不需要它，早点读只会让"库坏了"和"密钥坏了"
// 两种故障在启动日志里混在一起。
func Open(ctx context.Context, opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("store: path 不得为空")
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = 5 * time.Second // 与配置默认值一致；防御性兜底
	}
	if opts.ReadConns <= 0 {
		opts.ReadConns = defaultReadConns
	}

	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o750); err != nil {
		return nil, fmt.Errorf("store: 创建数据目录失败: %w", err)
	}

	s := &Store{opts: opts}

	writeDB, err := openWritePool(opts)
	if err != nil {
		return nil, err
	}
	s.writeDB = writeDB

	// 库文件权限：SQLite 自己按 umask 创建（常见 0644），而同机其他用户能读库
	// 就等于能拿走全部凭据密文（05-storage 8.2 / 安全基线 C7）。打开后立即收窄
	// 到 0600，并把 WAL 文件一并处理（它们同样含数据）。
	// 为什么不在 DSN 里做：SQLite 没有文件模式参数，只能由进程自己设。
	if err := chmodStoreFiles(opts.Path); err != nil {
		_ = s.writeDB.Close()
		return nil, err
	}

	// 迁移失败要立刻关池，否则测试里 t.TempDir() 清理会因句柄未关而留下 -wal 文件。
	if err := s.migrate(ctx); err != nil {
		_ = s.writeDB.Close()
		return nil, err
	}

	readDB, err := openReadPool(opts)
	if err != nil {
		_ = s.writeDB.Close()
		return nil, err
	}
	s.readDB = readDB

	key, err := LoadOrCreateMasterKey(opts.MasterKeyFile)
	if err != nil {
		_ = s.readDB.Close()
		_ = s.writeDB.Close()
		return nil, err
	}
	s.masterKey = key

	s.repos = newRepos(s)
	return s, nil
}

// pragmas 组装 DSN 里的 _pragma 参数。
//
// 为什么把 PRAGMA 塞进 DSN 而不是连上之后 Exec：PRAGMA 大部分是连接级设置，
// database/sql 的连接池随时会新建连接，新连接不会继承你在另一条连接上设过的 PRAGMA。
// 走 DSN 才能保证每一条新连接都带上（05-storage.md 6.1 选定做法 1 + 分池）。
func pragmas(busyTimeout time.Duration, readonly bool) string {
	ms := busyTimeout.Milliseconds()
	if ms <= 0 {
		ms = 5000
	}
	// synchronous=NORMAL：WAL + FULL 会让每次 commit 都 fsync，转发路径的尾延迟会变成
	// 磁盘延迟；而计量数据可以从上游账本对账补齐，不值得为此付 fsync 的代价。
	ps := []string{
		"_pragma=busy_timeout(" + strconv.FormatInt(ms, 10) + ")",
		"_pragma=foreign_keys(1)",
	}
	if !readonly {
		ps = append(ps,
			"_pragma=journal_mode(WAL)",
			"_pragma=synchronous(NORMAL)",
			"_pragma=wal_autocheckpoint(1000)", // 约 4 MiB 触发，避免 -wal 无限膨胀
		)
	}
	ps = append(ps,
		"_pragma=cache_size(-65536)", // 负值 = KiB，即 64 MiB
		"_pragma=temp_store(MEMORY)",
	)
	return strings.Join(ps, "&")
}

// dsn 构造 modernc.org/sqlite 的连接串。
func dsn(path string, busyTimeout time.Duration, readonly bool) string {
	q := pragmas(busyTimeout, readonly)
	if readonly {
		// mode=ro：只读连接无法升级为写事务，因此不会阻塞 checkpoint。
		q = "mode=ro&" + q
	}
	return "file:" + path + "?" + q
}

func openWritePool(opts Options) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(opts.Path, opts.BusyTimeout, false))
	if err != nil {
		return nil, fmt.Errorf("store: 打开写池失败: %w", err)
	}
	// 单写者：SQLite 任意时刻只有一个写者，显式串行化比让 DB 层随机报 BUSY 好——
	// 排队是可观测的（队列长度、等待时间），BUSY 不是。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: 写池连通性检查失败: %w", err)
	}
	return db, nil
}

func openReadPool(opts Options) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(opts.Path, opts.BusyTimeout, true))
	if err != nil {
		return nil, fmt.Errorf("store: 打开读池失败: %w", err)
	}
	db.SetMaxOpenConns(opts.ReadConns)
	db.SetMaxIdleConns(opts.ReadConns)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: 读池连通性检查失败: %w", err)
	}
	return db, nil
}

// WithTx 在写连接上跑一个事务。fn 拿到的仓储与 s.Repos() 是同一类型，
// 但这条回调里所有方法都走 tx——这是"审计追加与业务写必须同生同死"的唯一实现手段。
//
// 为什么把仓储当参数传进去而不是让调用方自己拿 s.Repos() 再拼事务：
// s.Repos() 绑定的是 writeDB，在事务里用它就等于绕开事务，写进去的东西不会被回滚。
func (s *Store) WithTx(ctx context.Context, fn func(*Repos) error) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启事务失败: %w", err)
	}
	defer func() {
		// 回滚在已经提交的情况下会返回 ErrTxDone，忽略是安全的。
		_ = tx.Rollback()
	}()

	txRepos := newRepos(&Store{opts: s.opts, writeDB: s.writeDB, masterKey: s.masterKey, tx: tx})
	if err := fn(txRepos); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交事务失败: %w", err)
	}
	return nil
}

// Repos 返回绑定写连接的仓储集合（自动提交模式）。需要原子性的多语句操作请用 WithTx。
func (s *Store) Repos() *Repos { return s.repos }

// MasterKey 返回内存中的主密钥副本。调用方不得持久化它。
func (s *Store) MasterKey() []byte {
	out := make([]byte, len(s.masterKey))
	copy(out, s.masterKey)
	return out
}

// Path 返回主库路径，供备份与运维日志引用。
func (s *Store) Path() string { return s.opts.Path }

// Close 关闭两个连接池，并在关闭前做一次 WAL checkpoint(TRUNCATE)。
//
// 为什么 checkpoint 必须在 Close 之前、且用 TRUNCATE：进程退出后没人再合并 -wal，
// 把它留着意味着"库文件 + 一个可能很大的 -wal"才是完整数据。这在 cp 备份、
// 迁移目录、排查"数据怎么少了一截"时都是陷阱。TRUNCATE 让 -wal 归零而不是留在盘上。
//
// 这一步是同步的，因此只能发生在优雅关闭的最后一步（05-storage.md 7.3：
// 不允许在转发热路径上做同步 checkpoint）。
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		var errs []error

		if s.writeDB != nil {
			// 用独立 context：调用方的 ctx 可能已经随关闭流程一起被取消。
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if _, err := s.writeDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				errs = append(errs, fmt.Errorf("store: WAL checkpoint 失败: %w", err))
			}
			cancel()
		}
		if s.readDB != nil {
			if err := s.readDB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("store: 关闭读池失败: %w", err))
			}
		}
		if s.writeDB != nil {
			if err := s.writeDB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("store: 关闭写池失败: %w", err))
			}
		}
		// 尽早点抹掉内存里的密钥副本。
		for i := range s.masterKey {
			s.masterKey[i] = 0
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// ---------------------------------------------------------------- 内部工具

// dbtx 是 *sql.DB 与 *sql.Tx 的公共子集。
// 有了它，仓储在自动提交与事务里是同一份代码，不需要写两遍。
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// collectIDs 执行一条只返回单个 int64 列的查询，把结果收成 int64 切片。
//
// 为什么有这个函数：多个仓储里都出现过同一段「QueryContext → defer
// rows.Close → for rows.Next → Scan(&id) → append → rows.Err」样板。收在这里
// 以后调用点只剩一行 SQL，也不会再出现「抄一份时漏掉 rows.Err」这类事故。
//
// 返回值语义与原调用点一致：**空切片而非 nil**（管理面把 nil 与 [] 当两种
// 东西渲染），因此出参是 []int64{} 而不是 var out []int64。
//
// op 是业务名，只用于错误文案「查询/扫描/遍历<op>失败」——日志里带上业务名，
// 才能从一条 sqlite 报错认出是哪张关联表出的问题。
func collectIDs(ctx context.Context, db dbtx, op, q string, args ...any) ([]int64, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询%s失败: %w", op, err)
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: 扫描%s失败: %w", op, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历%s失败: %w", op, err)
	}
	return out, nil
}

// newRepos 构造仓储集合。底层为 nil 的方法在调用时会 panic——这是刻意的：
// 仓储只能从 Store 获得，nil 说明是编程错误而非运行时条件。
func newRepos(s *Store) *Repos {
	// tx 非空时，所有仓储都走事务；这就是 WithTx 的实现方式。
	var conn dbtx = s.writeDB
	if s.tx != nil {
		conn = s.tx
	}
	r := &Repos{}
	r.Accounts = &AccountRepo{db: conn, s: s}
	r.Keys = &KeyRepo{db: conn, s: s}
	r.Sessions = &SessionRepo{db: conn, s: s}
	r.Models = &ModelRepo{db: conn, s: s}
	r.Quotas = &QuotaRepo{db: conn, s: s}
	r.Usage = &UsageRepo{db: conn, s: s, quotas: r.Quotas}
	r.Logs = &LogRepo{db: conn, s: s}
	r.Groups = &GroupRepo{db: conn, s: s}
	r.Inbox = &InboxRepo{db: conn, s: s}
	r.Secrets = &SecretRepo{db: conn, s: s}
	r.Settings = &SettingsRepo{s: s}
	return r
}

// base 是所有仓储的公共字段集合（通过嵌入复用）。
type base struct {
	db dbtx
	s  *Store
}

// now 把时间统一裁到 UTC unix 秒。所有时间列都是 UTC 整数（05-storage.md 第 2 节），
// 统一在这里取值可以避免调用方各写一套 time.Now()。
func now() int64 { return time.Now().UTC().Unix() }

// mustDir 确认目录存在且可写，供备份路径使用。
func ensureDir(p string) error {
	if err := os.MkdirAll(p, 0o750); err != nil {
		return fmt.Errorf("store: 创建目录 %s 失败: %w", p, err)
	}
	return nil
}

// migrationsFS 在 migrate.go 里通过 go:embed 提供。
// 这里留一个断言，确保 embed 目录真的被打进二进制（否则启动时迁移集为空，
// 表现为"表全都不存在"这种极难排查的故障）。
var _ fs.FS = migrationsFS

// quoteLiteral 是给 VACUUM INTO / ATTACH 之类不能带占位的语句用的。
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

var _ = url.QueryEscape // 保留 url 导入，DSN 组装演进时使用

// chmodStoreFiles 把主库与 WAL/SHM 收到 0600。
//
// 为什么必须在打开后做而不是“部署文档要求运维 chmod”：进程以什么身份跑就
// 决定它创建的文件权限，而运维不可能在每次迁移、每次 -wal 重建后记得补 chmod；
// SQLite 在 checkpoint 后会删掉并重建 -wal，权限会回到 umask 默认值。
// 因此由进程每次打开时收窄，是唯一能长期成立的做法。
//
// 失败不静默跳过：库文件权限过宽是本册 C7 的红线（同机其他用户可读全部凭据密文），
// 拿不到权限就拒绝启动，而不是带着 0644 继续跑。
func chmodStoreFiles(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // -wal/-shm 只在有写入时存在
			}
			return fmt.Errorf("store: 收窄库文件权限 %s: %w", p, err)
		}
	}
	return nil
}
