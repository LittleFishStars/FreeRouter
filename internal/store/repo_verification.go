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
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
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
func (r *ModelRepo) MarkVerified(ctx context.Context, credentialID string, status VerifyStatus, evidence string) error {
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

// SetRealModelVerification 写回目录侧鉴定三列（real_models.verify_status/
// verify_detail/verify_source，0004 迁移）。
//
// 与 MarkVerified 的区别（两者共用同一套 VerifyStatus 四态，但不是同一件事）：
//   - MarkVerified：L1 凭据侧（upstream_credentials），回答「这把 key 是不是它声称的模型」；
//   - 本方法：目录侧（real_models），回答「目录条目与指纹表/声明的结构是否相符」。
//     一条凭据服务 N 个模型，把目录结论写进凭据行会 N×M 互相覆盖（0004 的证据性理由）。
//
// source 由调用方传入（upstream.VerifySourceOf 是唯一映射点）：列是 TEXT 且语义是
// 「版本标识」而不是数字，存储层不发明格式，也不把 int 版本的翻译散落到调用方。
// evidence 为空时写 NULL 而不是空串：verify_detail 的 NULL 表示「没有说明」，
// 空串会被读方当成「有一条空证据」。
func (r *ModelRepo) SetRealModelVerification(ctx context.Context, realModelID string, status VerifyStatus, evidence, source string) error {
	if realModelID == "" {
		return errors.New("store: real_model id 不得为 0")
	}
	if !status.valid() {
		return fmt.Errorf("store: 非法鉴定状态 %q（四态：verified/suspect/unverified/trusted）", status)
	}
	var detail, src any
	if evidence != "" {
		detail = evidence
	}
	if source != "" {
		src = source
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE real_models SET verify_status = ?, verify_detail = ?, verify_source = ? WHERE id = ?`,
		string(status), detail, src, realModelID)
	if err != nil {
		return fmt.Errorf("store: 写目录鉴定结论失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Credential 是 upstream_credentials 的读取形态（鉴定相关列 + 定位列）。
// secret_ref 只是引用不含明文（主密钥管的事，这里原样转交）。
type Credential struct {
	ID             string
	OwnerAccountID string
	Provider       string
	ExternalID     string
	ExternalLabel  string
	Status         string
	LoginEnabled   bool
	VerifyStatus   VerifyStatus
	VerifyDetail   string
	CreatedAt      int64
	LastUsedAt     int64 // 0 = 从未用过
	// 余额（2026-10-28 凭据池「刷新余额」）：balanceText 由 Go 侧把上游应答
	// 格式化成展示串（如 "1,234 积分"）；空串 = 从未查询过或查询失败。
	// checkedAt 是该串的落库时刻（UTC 秒，0 = 同上）。
	BalanceText    string
	BalanceCheckedAt int64
}

// LookupCredential 按 id 读一条凭据（鉴定流水线的输入定位）。
func (r *ModelRepo) LookupCredential(ctx context.Context, id string) (*Credential, error) {
	c, err := r.scanCredential(r.db.QueryRowContext(ctx, credentialColumns+` WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return c, nil
}

const credentialColumns = `SELECT id, owner_account_id, provider, external_id, external_label,
       status, login_enabled, verify_status, verify_detail, created_at, last_used_at,
       COALESCE(balance_text, ''), COALESCE(balance_checked_at, 0)
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
	q += ` ORDER BY id ASC` // 字典序（UUID 无时间序，仅要求确定性）

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
	var balance sql.NullString
	var balanceAt sql.NullInt64
	err := sc.Scan(&c.ID, &c.OwnerAccountID, &c.Provider, &c.ExternalID, &label,
		&c.Status, &login, &c.VerifyStatus, &detail, &c.CreatedAt, &lastUsed,
		&balance, &balanceAt)
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
		credentialColumns+` WHERE verify_status = ? ORDER BY created_at ASC, id ASC`, string(status))
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
func (r *ModelRepo) UpsertPlatformCredential(ctx context.Context, ownerAccountID string, provider, externalID, label, secretRef, status string) (string, error) {
	if ownerAccountID == "" {
		return "", errors.New("store: 平台凭据行必须挂账户（NOT NULL 列；平台自有语义在 upstreams.owner=NULL，见 03-upstream §1）")
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(externalID) == "" {
		return "", errors.New("store: provider 与 external_id 不得为空")
	}
	if strings.TrimSpace(secretRef) == "" {
		return "", errors.New("store: secret_ref 不得为空（引用也要有指向）")
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO upstream_credentials
		   (id, owner_account_id, provider, external_id, external_label, secret_ref, status, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(provider, external_id) DO UPDATE SET
		   external_label = excluded.external_label,
		   secret_ref     = excluded.secret_ref,
		   status         = excluded.status`,
		idgen.New(), ownerAccountID, provider, externalID, label, secretRef, status, now()); err != nil {
		return "", fmt.Errorf("store: 写平台凭据失败: %w", err)
	}
	// ON CONFLICT 更新时回查真 id（LastInsertId 在 TEXT 主键 + upsert 下都无意义）。
	var id string
	if err := r.db.QueryRowContext(ctx,
		`SELECT id FROM upstream_credentials WHERE provider = ? AND external_id = ?`,
		provider, externalID).Scan(&id); err != nil {
		return "", fmt.Errorf("store: 回查平台凭据 id 失败: %w", err)
	}
	return id, nil
}

// CreateCredential 建贡献者提交的凭据行（owner 是账户）。初始态
// status='pending'、verify_status='unverified'（DEFAULT 兜底，显式传参
// 防止调用方猜默认）。
func (r *ModelRepo) CreateCredential(ctx context.Context, ownerAccountID string, provider, externalID, label, secretRef string) (string, error) {
	if ownerAccountID == "" {
		return "", errors.New("store: 贡献者凭据必须挂账户（平台凭据走 UpsertPlatformCredential）")
	}
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(externalID) == "" {
		return "", errors.New("store: provider 与 external_id 不得为空")
	}
	if strings.TrimSpace(secretRef) == "" {
		return "", errors.New("store: secret_ref 不得为空")
	}
	id := idgen.New()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO upstream_credentials
		   (id, owner_account_id, provider, external_id, external_label, secret_ref, status, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, 'pending', ?)`,
		id, ownerAccountID, provider, externalID, label, secretRef, now()); err != nil {
		return "", fmt.Errorf("store: 建凭据失败: %w", err)
	}
	return id, nil
}

// ReplaceCredential 把同一外部身份（provider + external_id）的已有凭据行替换成
// 新密文与新归属，返回 (行 id, 旧密文引用)；行不存在返回 ("", "")（调用方回退到
// CreateCredential）。旧密文引用供调用方删除废弃密文，避免无主密钥残留。
//
// 为什么需要它：OAuth 型重新授权同一账号时，供应商发的 access_token 可能与
// 旧凭据完全相同（MiniMax 实测：device_code 对同一账号重发未过期 token），
// fingerprint 相同 → 撞 (provider, external_id) 唯一索引。语义上这不是错误
// 而是「同一身份换新凭据」：密文、归属、label 都应更新为本次授权的结果，
// 状态重置回 pending 重新走鉴定（旧凭据的鉴定结论对新密文不成立）。
func (r *ModelRepo) ReplaceCredential(ctx context.Context, ownerAccountID string, provider, externalID, label, secretRef string) (string, string, error) {
	var id, oldRef string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, secret_ref FROM upstream_credentials WHERE provider = ? AND external_id = ?`,
		provider, externalID).Scan(&id, &oldRef)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("store: 回查待替换凭据失败: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE upstream_credentials
		    SET owner_account_id = ?, external_label = ?, secret_ref = ?,
		        status = 'pending', verify_status = 'unverified', verify_detail = NULL,
		        created_at = ?, last_used_at = NULL
		  WHERE id = ?`,
		ownerAccountID, label, secretRef, now(), id); err != nil {
		return "", "", fmt.Errorf("store: 替换凭据失败: %w", err)
	}
	return id, oldRef, nil
}

// UpdateCredentialBalance 落库一次余额查询的结果（凭据池「刷新余额」）。
// text 为空串表示「查询失败/上游未报」——也落库（checkedAt 有值而 text 空 =
// 查过但没拿到，前端显示「未报余额」而不是「从未查询」）。
func (r *ModelRepo) UpdateCredentialBalance(ctx context.Context, id, text string, checkedAt int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE upstream_credentials SET balance_text = ?, balance_checked_at = ? WHERE id = ?`,
		text, checkedAt, id)
	if err != nil {
		return fmt.Errorf("store: 写凭据余额失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCredentialNotFound
	}
	return nil
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
func (r *ModelRepo) SetCredentialStatus(ctx context.Context, id string, status CredentialStatus) error {
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
	OwnerAccountID *string
	Provider       string
	Status         CredentialStatus
	VerifyStatus   VerifyStatus
	// ExcludeRelay 排除挂在 relay 上游上的凭据（鉴定队列专用）：relay 凭据由
	// 账号体系背书（OAuth settle 直接入池，见 02-credentials 的来源口径），
	// 鉴定流水线对它们永不发生——verify_status 恒为 unverified。把它们列进
	// 鉴定队列等于一批永久占位的假待办（2026-10-30 用户实测：14 条里 9 条
	// 是永远不动的反代行）。
	ExcludeRelay bool
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
	if f.ExcludeRelay {
		// 经 secret_ref → upstream_secrets.upstream_id → upstreams.kind 的全库
		// 唯一关联边判定（与凭据池左栏的分组同一条边）。secret_ref 悬空（无
		// 上游指向）的行不受影响：子查询空 → NOT EXISTS 成立 → 照常列出。
		q.WriteString(` AND NOT EXISTS (
			SELECT 1 FROM upstream_secrets s
			JOIN upstreams u ON u.id = s.upstream_id
			WHERE s.id = upstream_credentials.secret_ref AND u.kind = 'relay')`)
	}
	// 最新在前：created_at 倒序；id 只作并列时的确定 tiebreak（UUID 无时间序）。
	q.WriteString(" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?")
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
