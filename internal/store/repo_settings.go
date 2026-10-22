package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SettingsRepo 是 settings 表（key TEXT PRIMARY KEY, value TEXT JSON）的读写面。
//
// 为什么只有 key→原文的窄接口：settings 的值语义属于 config 域（分组、类型、
// 默认值全在 internal/config），存储层只负责「按 key 原样存一段 JSON」。
// 在这里解释键的语义，等于把会漂移的第二份配置镜像塞进持久层。
type SettingsRepo struct {
	s *Store
}

// ErrSettingNotFound 表示该键从未写过（与「写了空值」不同）。
var ErrSettingNotFound = errors.New("store: 设置项不存在")

// Get 读一个键的 JSON 原文；不存在返回 ErrSettingNotFound。
func (r *SettingsRepo) Get(ctx context.Context, key string) (string, error) {
	if r == nil || r.s == nil {
		return "", errors.New("store: SettingsRepo 未绑定")
	}
	var v string
	err := r.s.writeDB.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSettingNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: 读设置 %s: %w", key, err)
	}
	return v, nil
}

// GetAll 读全部设置（key → JSON 原文）。
func (r *SettingsRepo) GetAll(ctx context.Context) (map[string]string, error) {
	if r == nil || r.s == nil {
		return nil, errors.New("store: SettingsRepo 未绑定")
	}
	rows, err := r.s.writeDB.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("store: 读设置表: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: 扫描设置行: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetInTx 在调用方事务内写一个键（upsert）。
//
// 为什么必须由调用方给事务：设置变更与审计是同一笔业务（06-audit §3），
// 两写必须同生同死；仓储自己开事务会让「审计写失败但设置已生效」成为可能。
func (r *SettingsRepo) SetInTx(ctx context.Context, key, valueJSON string) error {
	if r == nil || r.s == nil {
		return errors.New("store: SettingsRepo 未绑定")
	}
	// 已在事务内（WithTx 回调里）就直接用 tx：再开一层 WithTx 会向单写池
	// 再要一个连接，而 WithTx 持有唯一写连接 → 自我阻塞（实测 60s 超时）。
	if r.s.tx != nil {
		_, err := r.s.tx.ExecContext(ctx,
			`INSERT INTO settings(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, valueJSON)
		if err != nil {
			return fmt.Errorf("store: 写设置 %s: %w", key, err)
		}
		return nil
	}
	return r.s.WithTx(ctx, func(tx *Repos) error {
		return tx.Settings.SetInTx(ctx, key, valueJSON)
	})
}
