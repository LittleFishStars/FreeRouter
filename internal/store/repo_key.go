package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// KeyStatus 是 api_keys.status 的取值。
type KeyStatus string

const (
	KeyActive   KeyStatus = "active"
	KeyDisabled KeyStatus = "disabled"
	KeyExpired  KeyStatus = "expired"
	// KeyRevoked 是单向吊销（05-keys §4）：签发后不可恢复。
	// 为什么与 disabled 分开：disabled 是运营动作（先停一下，可恢复），
	// revoked 是安全动作（疑似泄露，不可恢复）；合并会让"可恢复"污染吊销语义。
	KeyRevoked KeyStatus = "revoked"
)

// APIKey 是 Key 的读模型。
//
// 注意这里没有明文 key：明文只在创建时返回一次（PLAN 9），库里只存 SHA-256。
// 2026-10-20 定案：Key 不持有任何额度/速率字段（旧版 quota_limit/quota_used/rpm_limit/
// tpm_limit/daily_token_cap/max_concurrency 六列已删）——额度一律在 account_quotas
// 按账户计，与本次请求用的是哪把 Key 无关；若每把 Key 自带额度，
// 建 10 把 Key 就得到 10 份额度，账户级上限形同虚设（05-storage.md 3.6）。
type APIKey struct {
	ID        int64
	AccountID int64
	Prefix    string
	Name      *string
	Status    KeyStatus
	ExpiresAt *int64
}

// KeyRepo 是 API Key 的仓储。
type KeyRepo struct{ base }

// CreateKey 插入一条 Key 并返回新 id。
//
// keyHash 必须是 SHA-256 明文的 32 字节；这里不校验长度之外的形状，
// 因为哈希的生成属于认证层（不属于持久层的职责）。
func (r *KeyRepo) CreateKey(ctx context.Context, k APIKey, keyHash []byte) (int64, error) {
	if k.AccountID == 0 {
		return 0, errors.New("store: Key 必须归属某个账户")
	}
	if len(keyHash) != 32 {
		return 0, fmt.Errorf("store: key_hash 必须是 32 字节（SHA-256），实际 %d", len(keyHash))
	}
	if k.Prefix == "" {
		return 0, errors.New("store: Key 的 prefix 不得为空（展示与排障需要）")
	}
	status := k.Status
	if status == "" {
		status = KeyActive
	}
	if !status.valid() {
		return 0, fmt.Errorf("store: 非法 Key 状态 %q", status)
	}

	res, err := r.db.ExecContext(ctx,
		`INSERT INTO api_keys(account_id, key_hash, prefix, name, status, expires_at)
		 VALUES(?,?,?,?,?,?)`,
		k.AccountID, keyHash, k.Prefix, k.Name, string(status), k.ExpiresAt)
	if err != nil {
		return 0, fmt.Errorf("store: 创建 Key 失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: 读取新 Key id 失败: %w", err)
	}
	return id, nil
}

// keyColumns 集中列定义，避免 scanKey 与 SELECT 的列顺序漂移。
// 顺序一旦不一致，Scan 不会报错而是把值塞进错误的字段——这类 bug 极难发现。
const keyColumns = `id, account_id, prefix, name, status, expires_at`

func scanKey(sc interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var status string
	if err := sc.Scan(&k.ID, &k.AccountID, &k.Prefix, &k.Name, &status, &k.ExpiresAt); err != nil {
		return nil, err
	}
	k.Status = KeyStatus(status)
	return &k, nil
}

// GetKeyByHash 按 key_hash 查 Key。这是转发热路径的第一跳。
//
// 必须走 ux_api_keys_key_hash 唯一索引（一次 B 树探测）；写成 LIKE 或全表扫
// 会在高 QPS 下把转发路径的 p99 直接拖垮。
func (r *KeyRepo) GetKeyByHash(ctx context.Context, keyHash []byte) (*APIKey, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys WHERE key_hash = ?`, keyHash)
	k, err := scanKey(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 按 hash 查 Key 失败: %w", err)
	}
	return k, nil
}

// GetKey 按 id 查 Key（管理面使用）。
func (r *KeyRepo) GetKey(ctx context.Context, id int64) (*APIKey, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys WHERE id = ?`, id)
	k, err := scanKey(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 读取 Key %d 失败: %w", id, err)
	}
	return k, nil
}

// ListKeysByAccount 列出账户名下的 Key（最新在前）。
//
// 为什么按 id 倒序而不是 created_at：api_keys 表里没有 created_at 列
// （以 PLAN 第 10 章为准），id 是唯一可排序的单调量。
func (r *KeyRepo) ListKeysByAccount(ctx context.Context, accountID int64) ([]APIKey, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys WHERE account_id = ? ORDER BY id DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: 列出 Key 失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 扫描 Key 失败: %w", err)
		}
		out = append(out, *k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 Key 失败: %w", err)
	}
	return out, nil
}

// SetKeyStatus 改 Key 状态。
func (r *KeyRepo) SetKeyStatus(ctx context.Context, id int64, status KeyStatus) error {
	if !status.valid() {
		return fmt.Errorf("store: 非法 Key 状态 %q", status)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE api_keys SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return fmt.Errorf("store: 更新 Key 状态失败: %w", err)
	}
	return requireAffected(res, "Key", id)
}

// 2026-10-20：旧版 AddQuotaUsed（api_keys.quota_used 递增）已随六列删除而移除：
// 额度计数在 account_quotas，由 UsageRepo 在同一结算事务里递增。

// SetKeyModelAllow 覆盖式设置 Key 的模型白名单（在组授权之外再收窄）。
//
// 为什么整体覆盖而不是增量 add/remove：白名单是一个集合，增量 API 会让调用方
// 在"我先读了、再改了、中间别人也改了"时静默丢失对方的修改。整体覆盖把语义
// 变成"设置成这个集合"，配合调用方在表单里提交完整集合用起来是确定的。
func (r *KeyRepo) SetKeyModelAllow(ctx context.Context, keyID int64, abstractModelIDs []int64) error {
	// 在事务里做"删干净 + 重插"，否则中途失败会留下半个白名单。
	if r.s != nil && r.s.tx == nil {
		return r.s.WithTx(ctx, func(tx *Repos) error {
			return tx.Keys.SetKeyModelAllow(ctx, keyID, abstractModelIDs)
		})
	}

	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM key_model_allow WHERE key_id = ?`, keyID); err != nil {
		return fmt.Errorf("store: 清空 Key 白名单失败: %w", err)
	}
	for _, mid := range abstractModelIDs {
		if _, err := r.db.ExecContext(ctx,
			`INSERT INTO key_model_allow(key_id, abstract_model_id) VALUES(?,?)`,
			keyID, mid); err != nil {
			return fmt.Errorf("store: 写 Key 白名单失败: %w", err)
		}
	}
	return nil
}

// KeyModelAllow 读 Key 的模型白名单。空切片 = Key 没有额外收窄（不代表"没有可用模型"）。
func (r *KeyRepo) KeyModelAllow(ctx context.Context, keyID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT abstract_model_id FROM key_model_allow WHERE key_id = ? ORDER BY abstract_model_id`,
		keyID)
	if err != nil {
		return nil, fmt.Errorf("store: 读取 Key 白名单失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]int64, 0, 8)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: 扫描 Key 白名单失败: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 Key 白名单失败: %w", err)
	}
	return out, nil
}

func (s KeyStatus) valid() bool {
	switch s {
	case KeyActive, KeyDisabled, KeyExpired, KeyRevoked:
		return true
	}
	return false
}

// SetKeyAllowlist 是 storeiface 里 authn 使用的名字：写 Key 的模型白名单。
// 与 SetKeyModelAllow 同一实现——接口名对齐仓储名，避免 Lead 合并时写适配器；
// 语义仍是「整体覆盖」：authn 在管理面收到的就是完整集合。
func (r *KeyRepo) SetKeyAllowlist(ctx context.Context, keyID int64, abstractModelIDs []int64) error {
	return r.SetKeyModelAllow(ctx, keyID, abstractModelIDs)
}

// KeyAllowlist 读 Key 的模型白名单（storeiface 对齐别名）。语义见 KeyModelAllow。
func (r *KeyRepo) KeyAllowlist(ctx context.Context, keyID int64) ([]int64, error) {
	return r.KeyModelAllow(ctx, keyID)
}

// SetKeyName 改展示名（不影响鉴权）。名字为空串等价于清空。
//
// 为什么单独一个方法而不是复用 Update：Key 的其余字段没有可改的语义
// （哈希/前缀不可变），只暴露"改名"这一条写路径比一个通用 Update 更难误用。
func (r *KeyRepo) SetKeyName(ctx context.Context, id int64, name *string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE api_keys SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("store: 改名失败: %w", err)
	}
	return requireAffected(res, "Key", id)
}

// SetKeyExpiry 写/清除 Key 的过期时间（租户自助改期）。
//
// t == nil 是「清空」（写 NULL = 永不过期），不是「不改」：本方法一定会执行
// 一条 UPDATE，调用它就等于宣告过期时间的新值。这与 HTTP 层的三值语义（缺省/
// null/时刻）分工明确——「缺省=不改」在 httpapi 的解码层就已经返回，永远走不到
// 这里；能到这里只剩「设成某时刻」和「清成 NULL」两种确定意图。
//
// 列语义与 CreateKey 写入的 expires_at 一致（NULL = 永不过期），读侧
// APIKey.ExpiresAt 为 nil 时也按同一语义解释。
func (r *KeyRepo) SetKeyExpiry(ctx context.Context, id int64, t *int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE api_keys SET expires_at = ? WHERE id = ?`, t, id)
	if err != nil {
		return fmt.Errorf("store: 更新 Key 过期时间失败: %w", err)
	}
	return requireAffected(res, "Key", id)
}
