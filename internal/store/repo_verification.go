// repo_verification.go —— 模型鉴定写入（task-15，M2）。
//
// 鉴定四态（docs/models/05-verification.md §10）落在 upstream_credentials
// 的 verify_status/verify_detail 列上——不是 real_models：鉴定的对象是
// 「某个 key 提供的模型是否与声称的名字相符」，即 (凭据, 模型) 对，
// 凭据行天然是归属点；real_models 是目录层（上游刷新产物），不携带
// 任何「这把 key 是不是假的」信息。
//
// 本文件只做**落库**：suspect/verified/trusted 的判定是 Judge（调用方）
// 的事；「无参考指纹默认放行并标 unverified」的语义在读取/判定侧——
// 存储层只保证四态可写可读、CHECK 枚举兜底、证据 JSON 原样存取。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// VerifyStatus 鉴定四态（CHECK 枚举的权威集合，05-verification.md）。
type VerifyStatus string

const (
	VerifyUnverified VerifyStatus = "unverified" // 初始态/无参考指纹的冷门模型（读取侧默认放行）
	VerifyVerified   VerifyStatus = "verified"   // 探针判定与声称相符
	VerifySuspect    VerifyStatus = "suspect"    // 判定不符或证据矛盾（处理由调用方定）
	VerifyTrusted    VerifyStatus = "trusted"    // 人工确认（覆盖机器判定）
)

func (v VerifyStatus) valid() bool {
	switch v {
	case VerifyUnverified, VerifyVerified, VerifySuspect, VerifyTrusted:
		return true
	}
	return false
}

// ErrCredentialNotFound 凭据行不存在（MarkVerified/LookupCredential 按唯一键未命中）。
var ErrCredentialNotFound = errors.New("store: 凭据不存在")

// MarkVerified 把鉴定结论落库：状态四选一 + 证据 JSON 原样存。
//   - evidence 允许空串（L0 只有 pass/fail 时证据可能就是空对象）；
//   - status 必须 ∈ 四态（DB CHECK 也兜底，这里先给 Go 侧友好错误）；
//   - trusted 是人工态：调用方（管理员动作）传，本方法不加语义。
//
// 不带 verified_at 时间列的表结构下，何时鉴定由 verify_detail JSON 或
// 审计日志承载——存储层不发明时间戳。
func (r *ModelRepo) MarkVerified(ctx context.Context, credentialID int64, status VerifyStatus, evidence string) error {
	if !status.valid() {
		return fmt.Errorf("store: 非法鉴定状态 %q（四态：verified/suspect/unverified/trusted）", status)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE upstream_credentials SET verify_status = ?, verify_detail = ? WHERE id = ?`,
		string(status), evidence, credentialID)
	if err != nil {
		return fmt.Errorf("store: 写鉴定结论失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// Credential 是 upstream_credentials 的读取形态（鉴定相关列 + 定位列）。
// secret_ref 只是引用不含明文（主密钥管的事，这里原样转交）。
type Credential struct {
	ID             int64
	OwnerAccountID int64
	Provider       string
	ExternalID     string
	ExternalLabel  string
	Status         string
	LoginEnabled   bool
	VerifyStatus   VerifyStatus
	VerifyDetail   string
	CreatedAt      int64
	LastUsedAt     int64 // 0 = 从未用过
}

// LookupCredential 按 id 读一条凭据（鉴定流水线的输入定位）。
func (r *ModelRepo) LookupCredential(ctx context.Context, id int64) (*Credential, error) {
	c, err := r.scanCredential(r.db.QueryRowContext(ctx, credentialColumns+` WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return c, nil
}

const credentialColumns = `SELECT id, owner_account_id, provider, external_id, external_label,
       status, login_enabled, verify_status, verify_detail, created_at, last_used_at
  FROM upstream_credentials`

// ListUnverified 列出待鉴定凭据（verify_status = 'unverified'）。
// provider 为空串时不过滤；externalIDPattern 是 LIKE 模式（%/_ 通配），
// 空串 = 全部。默认按 id 升序（重放同一查询得到同一顺序，排障友好）。
//
// 为什么 pattern 是 LIKE 而不是精确等值：鉴定的调度面（ScheduleReverify/
// 批量拉单）需要「external_id 形如 sk-xxx*」这类前缀圈定；SQL LIKE 走
// ux_upstream_credentials_external 前缀段时仍可用（模式带前缀常量段）。
// 转义由调用方负责（%/_ 想按字面匹配先 ESCAPE）——不在这儿猜意图。
func (r *ModelRepo) ListUnverified(ctx context.Context, provider, externalIDPattern string) ([]Credential, error) {
	q := credentialColumns + ` WHERE verify_status = 'unverified'`
	var args []any
	if provider != "" {
		q += ` AND provider = ?`
		args = append(args, provider)
	}
	if externalIDPattern != "" {
		q += ` AND external_id LIKE ?`
		args = append(args, externalIDPattern)
	}
	q += ` ORDER BY id ASC`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 列未鉴定凭据失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		c, err := scanCredentialRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// scanCredentialRow 是 *sql.Row 与 *sql.Rows 共用的扫描（两者都实现
// Scan）；ErrNoRows 原样冒泡，LookupCredential 在外层转 ErrCredentialNotFound。
func scanCredentialRow(sc interface{ Scan(dest ...any) error }) (*Credential, error) {
	return scanCredential(sc)
}

func (r *ModelRepo) scanCredential(row interface{ Scan(dest ...any) error }) (*Credential, error) {
	c, err := scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCredentialNotFound
	}
	return c, err
}

func scanCredential(sc interface{ Scan(dest ...any) error }) (*Credential, error) {
	var c Credential
	var login int
	var detail sql.NullString
	var label sql.NullString
	var lastUsed sql.NullInt64
	err := sc.Scan(&c.ID, &c.OwnerAccountID, &c.Provider, &c.ExternalID, &label,
		&c.Status, &login, &c.VerifyStatus, &detail, &c.CreatedAt, &lastUsed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err // 语义区分交给上层（scanCredentialRow → ErrCredentialNotFound）
		}
		return nil, fmt.Errorf("store: 扫描凭据行失败: %w", err)
	}
	c.ExternalLabel = label.String
	c.LoginEnabled = login == 1
	c.VerifyDetail = detail.String
	c.LastUsedAt = lastUsed.Int64
	return &c, nil
}

// ReverifyQueue 是复检排队的轻量视图（ScheduleReverify 落库侧）：
// 不另建表——「待复检」= verify_status ∈ {verified, suspect} 且需要
// 按理由重跑的行，理由进审计不进库（05-verification §10 复检接口由
// 调用方编排）。这里只给一个批量取回的读。
func (r *ModelRepo) ListCredentialsByStatus(ctx context.Context, status VerifyStatus) ([]Credential, error) {
	if !status.valid() {
		return nil, fmt.Errorf("store: 非法鉴定状态 %q", status)
	}
	rows, err := r.db.QueryContext(ctx,
		credentialColumns+` WHERE verify_status = ? ORDER BY id ASC`, string(status))
	if err != nil {
		return nil, fmt.Errorf("store: 按状态列凭据失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		c, err := scanCredentialRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// UpsertPlatformCredential 建平台自有的凭据行。
//
// 归属口径（与 05-storage.md 3.5 / 02-credentials.md §2.1 对齐后定案）：
//   - upstreams.owner_account_id = NULL 表达「平台自有」——来源属性
//     （03-upstream.md §1「owner_account_id = NULL 表示平台自有」）；
//   - upstream_credentials.owner_account_id 表达「贡献归属」——人属性，
//     05-storage 的 DDL 是 NOT NULL：凭据表里没有 NULL 行。
//   - 05-verification §10 SampleFromPlatformCredentials 的「owner_account_id
//     = NULL 的平台自有凭据」指 upstreams 行（凭据表同构但 NOT NULL——
//     文档张力在这里显式记录：凭据行的「平台自有」由 ownerAccountID 指向
//     管理员账户表达，与 upstreams 的 NULL 行配对）。
//
// ownerAccountID 必须是正整数（NOT NULL 列）：传管理员账户。幂等 upsert
// 以 (provider, external_id) 为键。
func (r *ModelRepo) UpsertPlatformCredential(ctx context.Context, ownerAccountID int64, provider, externalID, label, secretRef, status string) (int64, error) {
	if ownerAccountID <= 0 {
		return 0, errors.New("store: 平台凭据行必须挂账户（NOT NULL 列；平台自有语义在 upstreams.owner=NULL，见 03-upstream §1）")
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(externalID) == "" {
		return 0, errors.New("store: provider 与 external_id 不得为空")
	}
	if strings.TrimSpace(secretRef) == "" {
		return 0, errors.New("store: secret_ref 不得为空（引用也要有指向）")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO upstream_credentials
		   (owner_account_id, provider, external_id, external_label, secret_ref, status, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(provider, external_id) DO UPDATE SET
		   external_label = excluded.external_label,
		   secret_ref     = excluded.secret_ref,
		   status         = excluded.status`,
		ownerAccountID, provider, externalID, label, secretRef, status, now())
	if err != nil {
		return 0, fmt.Errorf("store: 写平台凭据失败: %w", err)
	}
	// ON CONFLICT 更新时 LastInsertId 无意义——回查真 id。
	var id int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT id FROM upstream_credentials WHERE provider = ? AND external_id = ?`,
		provider, externalID).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: 回查平台凭据 id 失败: %w", err)
	}
	return id, nil
}

// CreateCredential 建贡献者提交的凭据行（owner 是账户）。初始态
// status='pending'、verify_status='unverified'（DEFAULT 兜底，显式传参
// 防止调用方猜默认）。
func (r *ModelRepo) CreateCredential(ctx context.Context, ownerAccountID int64, provider, externalID, label, secretRef string) (int64, error) {
	if ownerAccountID <= 0 {
		return 0, errors.New("store: 贡献者凭据必须挂账户（平台凭据走 UpsertPlatformCredential）")
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(externalID) == "" {
		return 0, errors.New("store: provider 与 external_id 不得为空")
	}
	if strings.TrimSpace(secretRef) == "" {
		return 0, errors.New("store: secret_ref 不得为空")
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO upstream_credentials
		   (owner_account_id, provider, external_id, external_label, secret_ref, status, created_at)
		 VALUES(?, ?, ?, ?, ?, 'pending', ?)`,
		ownerAccountID, provider, externalID, label, secretRef, now())
	if err != nil {
		return 0, fmt.Errorf("store: 建凭据失败: %w", err)
	}
	return res.LastInsertId()
}

// ================================================================ 凭据读筛选与状态写入（管理面）

// CredentialStatus 是凭据的可调用状态。
//
// 值域就是 upstream_credentials.status 的 DB CHECK 枚举——不自造（06-audit 与
// 管理面按这套值发审计）。02-credentials §5.2 那套（pending/active/degraded/
// revoked）是建议口径，与 0001 已发布的 CHECK 不一致，差异已回报 lead；
// 在文档统一之前，**以库里的 CHECK 为准**：写了 CHECK 外的值会被 SQLite 拒掉，
// 而错误信息会漏进 API 响应。
type CredentialStatus string

const (
	CredPending  CredentialStatus = "pending"  // 已提交，鉴定未走完
	CredActive   CredentialStatus = "active"   // 可被调用
	CredInvalid  CredentialStatus = "invalid"  // 密钥失效（上游 401 持续等）
	CredExpired  CredentialStatus = "expired"  // 到期
	CredDisabled CredentialStatus = "disabled" // 人工停用（suspend / ban 的落点）
)

func (s CredentialStatus) valid() bool {
	switch s {
	case CredPending, CredActive, CredInvalid, CredExpired, CredDisabled:
		return true
	}
	return false
}

// SetCredentialStatus 写凭据状态。
//
// suspend / ban 的落点：两者都映射到 disabled。为什么不各加一个枚举值——
// status 列有 DB CHECK，加值必须新迁移，而"暂停"与"封禁"的差别在**原因与
// 是否可恢复**，不在状态机：这两件事由审计事件的 detail 承载（reason/operator），
// 而不是把一个维度拆成两个状态值让所有判定逻辑去猜（02-credentials §5.2 对
// 布尔 enabled 的批评在这里同样适用）。
//
// **必须由调用方在同一 WithTx 里写审计**（02-admin §9 的 ban 是 🔒 二次确认级）：
// 管理动作必留痕的唯一实现手段是同事务——本方法若在内部自行写审计，
// 就把"写没写审计"变成仓储的隐式行为，调用方无法感知也无法补字段。
// 事务传播已经就绪：WithTx 内取到的仓储绑定同一个 tx，直接调即可。
func (r *ModelRepo) SetCredentialStatus(ctx context.Context, id int64, status CredentialStatus) error {
	if !status.valid() {
		return fmt.Errorf("store: 非法凭据状态 %q（值域 pending/active/invalid/expired/disabled）", status)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE upstream_credentials SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return fmt.Errorf("store: 改凭据状态失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// CredentialFilter 是管理面凭据列表的筛选条件（02-admin §9 的 owner/provider/
// verify_status/status 四个筛选位）。零值 = 不筛。
type CredentialFilter struct {
	OwnerAccountID *int64
	Provider       string
	Status         CredentialStatus
	VerifyStatus   VerifyStatus
	// Limit <=0 取 100，硬上限 500（管理面列表不应当拉全表）。
	Limit  int
	Offset int
}

// ListCredentials 按条件列凭据（管理面）。
//
// 走的索引：owner 筛 → ix_upstream_credentials_owner；status+verify_status 筛
// → ix_upstream_credentials_status（前导列正是 status）。两个筛选位都留了
// 索引可用形状，不需要为管理面再加索引。
//
// 返回的行**不含任何密文**：secret_ref 是引用，密文在 upstream_secrets，
// 解密要主密钥且只在发起上游请求时按需取。
func (r *ModelRepo) ListCredentials(ctx context.Context, f CredentialFilter) ([]Credential, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := strings.Builder{}
	q.WriteString(credentialColumns + ` WHERE 1=1`)
	var args []any
	if f.OwnerAccountID != nil {
		q.WriteString(" AND owner_account_id = ?")
		args = append(args, *f.OwnerAccountID)
	}
	if f.Provider != "" {
		q.WriteString(" AND provider = ?")
		args = append(args, f.Provider)
	}
	if f.Status != "" {
		if !f.Status.valid() {
			return nil, fmt.Errorf("store: 非法凭据状态筛选 %q", f.Status)
		}
		q.WriteString(" AND status = ?")
		args = append(args, string(f.Status))
	}
	if f.VerifyStatus != "" {
		if !f.VerifyStatus.valid() {
			return nil, fmt.Errorf("store: 非法鉴定状态筛选 %q", f.VerifyStatus)
		}
		q.WriteString(" AND verify_status = ?")
		args = append(args, string(f.VerifyStatus))
	}
	q.WriteString(" ORDER BY id DESC LIMIT ? OFFSET ?")
	args = append(args, limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 列凭据失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		c, err := scanCredentialRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}
