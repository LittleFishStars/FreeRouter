package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Session 是会话的读模型。
//
// 不带 TokenHash 到读模型里：会话校验只需要"按 hash 找到那行"，找到之后
// 上层要的是账户与到期时间。把 hash 带出来只会扩大它的传播面。
type Session struct {
	ID        string
	AccountID string
	CreatedAt int64
	ExpiresAt int64
	IP        *string
	UA        *string
}

// SessionRepo 是会话仓储。
type SessionRepo struct{ base }

// CreateSession 写入一条会话。tokenHash 是 SHA-256(token) 的 32 字节。
func (r *SessionRepo) CreateSession(ctx context.Context, id string, accountID string, tokenHash []byte, expiresAt int64, ip, ua *string) error {
	if id == "" {
		return errors.New("store: session id 不得为空")
	}
	if accountID == "" {
		return errors.New("store: 会话必须归属某个账户")
	}
	if len(tokenHash) != 32 {
		return fmt.Errorf("store: token_hash 必须是 32 字节（SHA-256），实际 %d", len(tokenHash))
	}
	if expiresAt == 0 {
		return errors.New("store: 会话必须有到期时间（默认 24h，见 02-deploy.md）")
	}

	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions(id, account_id, token_hash, created_at, expires_at, ip, ua)
		 VALUES(?,?,?,?,?,?,?)`,
		id, accountID, tokenHash, now(), expiresAt, ip, ua); err != nil {
		return fmt.Errorf("store: 创建会话失败: %w", err)
	}
	return nil
}

// GetSessionByTokenHash 按 token hash 查会话与账户状态。每个管理请求的热路径。
//
// 为什么要在这里 join accounts 拿到 status，而不是分两次查：
// 会话校验与"账户是否仍 active"必须在同一次读取里得到。分两次查会引入一个窗口——
// 管理员在两步之间禁用了账户，而请求拿着第一步的旧状态通过了校验。
// （PLAN 9：账户转 disabled 后一切立即失效。）
func (r *SessionRepo) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*Session, AccountStatus, error) {
	if len(tokenHash) != 32 {
		return nil, "", fmt.Errorf("store: token_hash 必须是 32 字节（SHA-256），实际 %d", len(tokenHash))
	}

	row := r.db.QueryRowContext(ctx,
		`SELECT s.id, s.account_id, s.created_at, s.expires_at, s.ip, s.ua, a.status
		   FROM sessions s
		   JOIN accounts a ON a.id = s.account_id
		  WHERE s.token_hash = ?`, tokenHash)

	var sess Session
	var status string
	if err := row.Scan(&sess.ID, &sess.AccountID, &sess.CreatedAt, &sess.ExpiresAt,
		&sess.IP, &sess.UA, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("store: 校验会话失败: %w", err)
	}
	return &sess, AccountStatus(status), nil
}

// DeleteSession 删除单条会话（登出）。
func (r *SessionRepo) DeleteSession(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 删除会话失败: %w", err)
	}
	return requireAffected(res, "会话", id)
}

// DeleteSessionByTokenHash 按 token hash 删除（登出时手上只有 token，没有 id）。
func (r *SessionRepo) DeleteSessionByTokenHash(ctx context.Context, tokenHash []byte) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("store: 按 hash 删除会话失败: %w", err)
	}
	return nil
}

// DeleteSessionsByAccount 删除账户的全部会话。
//
// 改密/禁用即吊销（PLAN 10）：这是"改密码后其它设备立刻掉线"的实现。
// 判定影响 0 行会失败没有意义——"删掉一个本来就没有会话的账户"是正常情况。
func (r *SessionRepo) DeleteSessionsByAccount(ctx context.Context, accountID string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE account_id = ?`, accountID); err != nil {
		return fmt.Errorf("store: 吊销账户会话失败: %w", err)
	}
	return nil
}

// DeleteExpiredSessions 清理过期会话，返回删除行数。后台任务使用（05-storage.md 8.3）。
//
// 为什么这里可以直接 DELETE 而 audit_log 不行：会话是纯垃圾，没有任何追溯价值；
// 而审计的删除会消灭"谁删了审计"的答案，所以必须走归档通道。
func (r *SessionRepo) DeleteExpiredSessions(ctx context.Context, before int64) (int64, error) {
	if before == 0 {
		before = now()
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("store: 清理过期会话失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取清理行数失败: %w", err)
	}
	return n, nil
}
