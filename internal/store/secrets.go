// secrets.go —— 上游凭据密文仓储（task-19）。
//
// 定位（docs/architecture/05-storage.md 3.5、docs/contributions/02-credentials.md §6）：
// upstream_secrets 存 AES-GCM 密文，主密钥来自 0600 文件或环境变量，**永不入库**。
// 明文只在调用期内存活，禁止入日志（§6.4）。
//
// 三条不能破的红线：
//  1. 明文永不落库——这里只写 ciphertext + nonce；
//  2. 解密失败必须失败——GCM 认证失败时返回错误，**不返回**部分明文或垃圾字节
//     （返回垃圾等于让上层把乱码当凭据发出去，故障会现场漂移到上游而不是本地）；
//  3. 主密钥绝不进错误信息、不进日志、不进返回值——错误里只带"哪个 id 失败"这类
//     不含机密的信息。
//
// 密文形态选择 AES-256-GCM 而非其它 AEAD：GCM 的认证标签让"密文被篡改"变成
// 解密失败而不是"解出坏数据"（02-credentials §6.1）；密钥 32 字节 = AES-256，
// 与 masterkey.go 的 MasterKeySize 一致。
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"io"
)

// ErrSecretNotFound 密文行不存在。
var ErrSecretNotFound = errors.New("store: 密文行不存在")

// ErrSecretDecrypt 密文解密/认证失败。
//
// 为什么单独一个类型：调用方需要区分"没有这条密文"（配置缺失，走补录流程）与
// "密文坏了"（安全事件，走告警与轮换流程）。两者混在一个 error 里，
// 运维就只能按最坏情况处理。
var ErrSecretDecrypt = errors.New("store: 密文解密失败（密钥不符或密文被篡改）")

// SecretRef 是密钥材料的引用（不含量）。
//
// 为什么只存引用不存密文：upstream_credentials.secret_ref 指向这里，
// 而"谁能取到密钥"由一处决定（02-credentials §6.1：密钥材料统一落
// upstream_secrets，本表只存引用）。散落多处就等于散了多处权限判断。
type SecretRef struct {
	SecretID   string
	UpstreamID string
}

// SealSecret 用给定密钥加密明文，返回 ciphertext 与 nonce。
//
// 纯函数形态（不碰库）：便于单测直接验证"密文里搜不到明文"、"同一明文两次
// 加密结果不同"这类不变量，而不必要求先建库。
//
// nonce 每次随机（crypto/rand）：GCM 用 (key, nonce) 唯一确定密文，同一密钥
// 下 nonce 重复会泄漏明文关系（xor 两个密文得明文异或）。这是 GCM 的硬要求，
// 不是"最好随机"。
//
// 返回的 nonce 必须与 ciphertext 一起保存——丢了 nonce 就解不开，而密文与
// nonce 分开存正是 upstream_secrets 的两列形态。
func SealSecret(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("store: 生成 nonce 失败: %w", err)
	}
	// Seal 会把认证标签追加在密文尾部。
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// OpenSecret 用给定密钥解密密文。
//
// 认证失败时返回 ErrSecretDecrypt，**不返回任何明文片段**——GCM 的 Seal/Open
// 本身就是全有全无的，但错误处理路径必须守住：万一将来换成别的 AEAD 或
// 手工拼接，这里不能变成"返回前半截"。
func OpenSecret(key, ciphertext, nonce []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w: nonce 长度 %d != %d", ErrSecretDecrypt, len(nonce), gcm.NonceSize())
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// 错误信息里只说"解密失败"，不带密文、nonce、密钥的任何片段——
		// 错误信息会进日志，日志是"谁都可能读到"的地方。
		return nil, ErrSecretDecrypt
	}
	return plain, nil
}

// newGCM 构造 AES-256-GCM。密钥长度必须是 32 字节。
func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != MasterKeySize {
		// 错误里只报长度，不报密钥内容。
		return nil, fmt.Errorf("store: 主密钥长度 %d != %d（AES-256）", len(key), MasterKeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("store: 构造 AES 块失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("store: 构造 GCM 失败: %w", err)
	}
	return gcm, nil
}

// SecretRepo 是密文仓储。
type SecretRepo struct{ base }

// PutSecret 为某上游写入（或替换）密钥材料，返回 secret_id。
//
// 为什么用 upsert 而不是纯 insert：upstream_secrets 上有
// UNIQUE(upstream_id)（一条上游只有一份密钥材料），"换密钥"是常规运营动作
// （同一个上游账号换了 token）。纯 insert 会在第二次写入时撞唯一约束，
// 而"先删后插"会丢掉旧行 id 打断 upstreams.secret_id 的引用。
//
// 整个操作在单事务里：写密文与把 upstreams.secret_id 指过去必须同生同死，
// 否则中途失败会留下"上游指向不存在的密文"。
func (r *SecretRepo) PutSecret(ctx context.Context, upstreamID string, plaintext []byte) (string, error) {
	if upstreamID == "" {
		return "", errors.New("store: 密文必须归属某个上游")
	}
	if len(plaintext) == 0 {
		return "", errors.New("store: 拒绝写入空明文（空密钥会让上游返回难以归因的 401）")
	}
	key := r.s.MasterKey()
	if len(key) == 0 {
		return "", errors.New("store: 主密钥不可用（Store 未初始化或已关闭）")
	}
	ciphertext, nonce, err := SealSecret(key, plaintext)
	if err != nil {
		return "", fmt.Errorf("store: 加密密钥材料失败: %w", err)
	}

	// 在事务里做 upsert + 指引用。
	if r.s != nil && r.s.tx == nil {
		var id string
		err := r.s.WithTx(ctx, func(tx *Repos) error {
			var e error
			id, e = tx.Secrets.putSecretInner(ctx, upstreamID, ciphertext, nonce)
			return e
		})
		return id, err
	}
	return r.putSecretInner(ctx, upstreamID, ciphertext, nonce)
}

func (r *SecretRepo) putSecretInner(ctx context.Context, upstreamID string, ciphertext, nonce []byte) (string, error) {
	id := idgen.New()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO upstream_secrets(id, upstream_id, ciphertext, nonce)
		 VALUES(?,?,?,?)
		 ON CONFLICT(upstream_id) DO UPDATE SET
		     ciphertext = excluded.ciphertext,
		     nonce      = excluded.nonce`,
		id, upstreamID, ciphertext, nonce); err != nil {
		return "", fmt.Errorf("store: 写密文失败: %w", err)
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT id FROM upstream_secrets WHERE upstream_id = ?`, upstreamID).Scan(&id); err != nil {
		return "", fmt.Errorf("store: 回查密文 id 失败: %w", err)
	}
	// 把上游指到这份密文（同一事务内）。
	if _, err := r.db.ExecContext(ctx,
		`UPDATE upstreams SET secret_id = ? WHERE id = ?`, id, upstreamID); err != nil {
		return "", fmt.Errorf("store: 关联上游密文失败: %w", err)
	}
	return id, nil
}

// GetSecret 解密并返回明文。密文行不存在 → ErrSecretNotFound；
// 认证失败 → ErrSecretDecrypt（不返回部分明文）。
//
// 明文的生命周期由调用方负责：拿到之后**不要**打进日志、不要存进长期对象
// （02-credentials §6.4「明文只在调用期内存活」）。
func (r *SecretRepo) GetSecret(ctx context.Context, secretID string) ([]byte, error) {
	var ciphertext, nonce []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT ciphertext, nonce FROM upstream_secrets WHERE id = ?`, secretID).
		Scan(&ciphertext, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读密文失败: %w", err)
	}
	key := r.s.MasterKey()
	if len(key) == 0 {
		return nil, errors.New("store: 主密钥不可用（Store 未初始化或已关闭）")
	}
	plain, err := OpenSecret(key, ciphertext, nonce)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

// GetSecretByUpstream 按上游取明文（便捷形态：调用方通常手上是 upstream id）。
func (r *SecretRepo) GetSecretByUpstream(ctx context.Context, upstreamID string) ([]byte, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM upstream_secrets WHERE upstream_id = ?`, upstreamID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查上游密文失败: %w", err)
	}
	return r.GetSecret(ctx, id)
}

// GetPlaintextByCredentialID 按凭据行 id 取明文凭据（凭据池「刷新余额」的
// 解密入口：凭据行 → secret_ref → 密文 → 明文）。
//
// 与 GetSecret 同级的安全面：明文只返回给服务端内部的调用方（sidecar stdio
// 边界），从不进 API 应答、不落日志。
func (r *SecretRepo) GetPlaintextByCredentialID(ctx context.Context, credentialID string) ([]byte, error) {
	var ciphertext, nonce []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT s.ciphertext, s.nonce
		   FROM upstream_credentials c
		   JOIN upstream_secrets s ON s.id = c.secret_ref
		  WHERE c.id = ?`, credentialID).
		Scan(&ciphertext, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 按凭据查密文失败: %w", err)
	}
	key := r.s.MasterKey()
	if len(key) == 0 {
		return nil, errors.New("store: 主密钥不可用（Store 未初始化或已关闭）")
	}
	return OpenSecret(key, ciphertext, nonce)
}

// DeleteSecret 删除密文行，并把上游的 secret_id 置空。
//
// 为什么要一起清引用：upstreams.secret_id 是指向密文的外键，留着会指向
// 一条不存在的行（虽然 FK 允许 NULL，但指向已删的行会让"这个上游有密钥吗"
// 这个问题无法从库里回答）。
//
// 删除不等于"这条上游从此没有密钥"——重新写入会生成新行与新 id。
func (r *SecretRepo) DeleteSecret(ctx context.Context, secretID string) error {
	if r.s != nil && r.s.tx == nil {
		return r.s.WithTx(ctx, func(tx *Repos) error {
			return tx.Secrets.deleteSecretInner(ctx, secretID)
		})
	}
	return r.deleteSecretInner(ctx, secretID)
}

func (r *SecretRepo) deleteSecretInner(ctx context.Context, secretID string) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE upstreams SET secret_id = NULL WHERE secret_id = ?`, secretID); err != nil {
		return fmt.Errorf("store: 解除上游密文引用失败: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM upstream_secrets WHERE id = ?`, secretID)
	if err != nil {
		return fmt.Errorf("store: 删除密文失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSecretNotFound
	}
	return nil
}

// HasSecret 报告某上游是否配有密文（不解密）。
//
// 为什么需要不解密的探测：健康检查要回答"这个上游配没配密钥"，而解密会
// 引入"密钥对不上"这种与"没配"不同的失败——两者混起来会让巡检把配置问题
// 报成密钥损坏。
func (r *SecretRepo) HasSecret(ctx context.Context, upstreamID string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM upstream_secrets WHERE upstream_id = ?`, upstreamID).Scan(&n); err != nil {
		return false, fmt.Errorf("store: 查上游密文存在性失败: %w", err)
	}
	return n > 0, nil
}
