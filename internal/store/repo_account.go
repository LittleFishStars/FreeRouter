package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound 是仓储层统一的"不存在"信号。
//
// 为什么不用 sql.ErrNoRows 直接向上传：sql.ErrNoRows 是 database/sql 的实现细节，
// 上层若用 errors.Is 判定它，就等于依赖了"我们底层一定是 database/sql"这个假设。
// 换成自己的哨兵错误后，将来换驱动或加缓存层都不影响调用方。
var ErrNotFound = errors.New("store: 记录不存在")

// AccountStatus 是 accounts.status 的取值（DDL 里的 CHECK 是权威枚举）。
type AccountStatus string

const (
	AccountPending  AccountStatus = "pending"
	AccountActive   AccountStatus = "active"
	AccountDisabled AccountStatus = "disabled"
	AccountRejected AccountStatus = "rejected"
	AccountMerged   AccountStatus = "merged"
)

// Class 是权限类（account_classes.class 的 CHECK 是权威枚举）。
type Class string

const (
	ClassAdmin       Class = "admin"
	ClassTenant      Class = "tenant"
	ClassContributor Class = "contributor"
)

// Account 是账户的读模型。
//
// 为什么不带 password_hash 之外的敏感列到通用读模型里：这是刻意的最小化——
// 认证路径需要 hash，但控制台列表不需要。分成 Account 与 AccountAuth 两个形状，
// 可以让"把 hash 传到不该传的地方"变成编译期不可能。
type Account struct {
	ID           int64
	DisplayName  string
	Username     string
	Status       AccountStatus
	CreatedAt    int64
	LastLoginAt  *int64
	Note         *string
	Classes      []Class
}

// AccountAuth 是认证路径专用的形状：带 password_hash 与 recovery_code_hash。
// 唯一的调用点是登录校验，不要把它用在列表/展示路径上。
type AccountAuth struct {
	Account
	PasswordHash     string
	RecoveryCodeHash *string
}

// AccountRepo 是账户与权限类的仓储。
type AccountRepo struct{ base }

// CreateAccount 插入账户并写权限类，返回新 id。
//
// 为什么账户与权限类必须在同一个事务里：一个"没有任何权限类的账户"既不能登录也不能
// 被授权，是纯粹的垃圾数据；而它又占着 username 的唯一约束，让用户无法重新注册。
// 所以要么两者都成立，要么都不成立。
func (r *AccountRepo) CreateAccount(ctx context.Context, a AccountAuth, classes []Class) (int64, error) {
	if a.Username == "" {
		return 0, errors.New("store: username 不得为空")
	}
	if len(classes) == 0 {
		return 0, errors.New("store: 账户至少要有一个权限类")
	}
	for _, c := range classes {
		if !c.valid() {
			return 0, fmt.Errorf("store: 非法权限类 %q", c)
		}
	}

	// 嵌套事务在 SQLite 里不可用（无 SAVEPOINT 封装），所以这里检测自己是否已在事务里：
	// 在事务里就直接用现有连接，否则用单条语句 + 后续插入。为了让"两条语句原子"成立，
	// 非事务路径用显式事务包住。
	if r.s != nil && r.s.tx == nil {
		var id int64
		err := r.s.WithTx(ctx, func(tx *Repos) error {
			var err error
			id, err = tx.Accounts.CreateAccount(ctx, a, classes)
			return err
		})
		return id, err
	}

	createdAt := a.CreatedAt
	if createdAt == 0 {
		createdAt = now()
	}
	status := a.Status
	if status == "" {
		status = AccountPending
	}

	res, err := r.db.ExecContext(ctx,
		`INSERT INTO accounts(display_name, username, password_hash, status,
		                      recovery_code_hash, created_at, last_login_at, note)
		 VALUES(?,?,?,?,?,?,?,?)`,
		a.DisplayName, a.Username, a.PasswordHash, string(status),
		a.RecoveryCodeHash, createdAt, a.LastLoginAt, a.Note)
	if err != nil {
		return 0, fmt.Errorf("store: 创建账户失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: 读取新账户 id 失败: %w", err)
	}

	for _, c := range classes {
		if _, err := r.db.ExecContext(ctx,
			`INSERT INTO account_classes(account_id, class) VALUES(?,?)`, id, string(c),
		); err != nil {
			return 0, fmt.Errorf("store: 写权限类 %q 失败: %w", c, err)
		}
	}
	return id, nil
}

// GetAccount 按 id 读账户（含权限类）。
func (r *AccountRepo) GetAccount(ctx context.Context, id int64) (*Account, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, display_name, username, status, created_at, last_login_at, note
		   FROM accounts WHERE id = ?`, id)

	var a Account
	var status string
	if err := row.Scan(&a.ID, &a.DisplayName, &a.Username, &status,
		&a.CreatedAt, &a.LastLoginAt, &a.Note); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 读取账户 %d 失败: %w", id, err)
	}
	a.Status = AccountStatus(status)

	classes, err := r.classesOf(ctx, id)
	if err != nil {
		return nil, err
	}
	a.Classes = classes
	return &a, nil
}

// GetAccountByUsername 按用户名读账户（含权限类）。登录路径使用。
func (r *AccountRepo) GetAccountByUsername(ctx context.Context, username string) (*Account, error) {
	var id int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT id FROM accounts WHERE username = ?`, username).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 按用户名查账户失败: %w", err)
	}
	return r.GetAccount(ctx, id)
}

// GetAccountAuth 读认证所需字段（含 password_hash）。只给登录校验用。
func (r *AccountRepo) GetAccountAuth(ctx context.Context, username string) (*AccountAuth, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, display_name, username, password_hash, status,
		        recovery_code_hash, created_at, last_login_at, note
		   FROM accounts WHERE username = ?`, username)

	var a AccountAuth
	var status string
	if err := row.Scan(&a.ID, &a.DisplayName, &a.Username, &a.PasswordHash, &status,
		&a.RecoveryCodeHash, &a.CreatedAt, &a.LastLoginAt, &a.Note); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 读取认证信息失败: %w", err)
	}
	a.Status = AccountStatus(status)

	classes, err := r.classesOf(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	a.Classes = classes
	return &a, nil
}

// classesOf 读账户的全部权限类。权限类数量极少（≤3），不需要额外缓存。
func (r *AccountRepo) classesOf(ctx context.Context, accountID int64) ([]Class, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT class FROM account_classes WHERE account_id = ? ORDER BY class`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: 读取权限类失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Class
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("store: 扫描权限类失败: %w", err)
		}
		out = append(out, Class(c))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历权限类失败: %w", err)
	}
	return out, nil
}

// SetAccountStatus 改状态。禁用后名下 Key 立即 401 靠的是每次请求实时查账户状态
// （05-storage.md 7.2），所以这里不需要额外的失效通知机制。
func (r *AccountRepo) SetAccountStatus(ctx context.Context, id int64, status AccountStatus) error {
	if !status.valid() {
		return fmt.Errorf("store: 非法账户状态 %q", status)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE accounts SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return fmt.Errorf("store: 更新账户状态失败: %w", err)
	}
	return requireAffected(res, "账户", id)
}

// TouchLastLogin 记录最近登录时间。
func (r *AccountRepo) TouchLastLogin(ctx context.Context, id int64, ts int64) error {
	if ts == 0 {
		ts = now()
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE accounts SET last_login_at = ? WHERE id = ?`, ts, id)
	if err != nil {
		return fmt.Errorf("store: 更新 last_login_at 失败: %w", err)
	}
	return nil
}

// AddClass 追加权限类（幂等：已存在则不报错）。
func (r *AccountRepo) AddClass(ctx context.Context, accountID int64, c Class) error {
	if !c.valid() {
		return fmt.Errorf("store: 非法权限类 %q", c)
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO account_classes(account_id, class) VALUES(?,?)
		 ON CONFLICT(account_id, class) DO NOTHING`, accountID, string(c))
	if err != nil {
		return fmt.Errorf("store: 追加权限类失败: %w", err)
	}
	return nil
}

// HasClass 判断账户是否拥有某个权限类。鉴权路径使用。
func (r *AccountRepo) HasClass(ctx context.Context, accountID int64, c Class) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM account_classes WHERE account_id = ? AND class = ?`,
		accountID, string(c)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: 判定权限类失败: %w", err)
	}
	return n > 0, nil
}

// CountAccounts 返回账户总数。启动时的初始化向导用它判定"库中无任何账户"
// （01-overview.md 6.2 第 3 步：无账户时只监听回环）。
func (r *AccountRepo) CountAccounts(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM accounts`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: 统计账户数失败: %w", err)
	}
	return n, nil
}

func (c Class) valid() bool {
	switch c {
	case ClassAdmin, ClassTenant, ClassContributor:
		return true
	}
	return false
}

func (s AccountStatus) valid() bool {
	switch s {
	case AccountPending, AccountActive, AccountDisabled, AccountRejected, AccountMerged:
		return true
	}
	return false
}

// requireAffected 把"UPDATE/DELETE 影响 0 行"翻译成 ErrNotFound。
//
// 为什么需要它：SQLite 的 UPDATE 命中 0 行不是错误。若不检查，上层会以为"操作成功"
// 而实际上目标行不存在——禁用账户这类操作静默失败是最危险的。
func requireAffected(res sql.Result, what string, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取 %s 影响行数失败: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%w：%s %d", ErrNotFound, what, id)
	}
	return nil
}
