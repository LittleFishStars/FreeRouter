// repo_unavailability.go —— 不可用模型台账（迁移 0014）。
//
// 语义（与迁移文件头一致）：**行在 = 该路径当前不可用**。
//   - MarkUnavailable 是 upsert：attempts 自增、next_check_at 按退避曲线推进；
//   - MarkAvailable 是删行（恢复即出册）；
//   - ListDue 返回「已到点该探测」的行，供后台扫描。
//
// 为什么 attempts 自增放在 SQL 里而不是先读后写：流量侧的失败判定是并发的
// （多个 goroutine 同时判定同一条路径不可用），读-改-写会丢计数，而丢计数
// 直接让退避曲线退化成「永远 1 分钟一次」——正是这套机制要防的打上游。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Unavailability 是一条不可用记录（model_unavailability 一行的读面）。
type Unavailability struct {
	// RealModelID 是 real_models 行 id（作用域 = 一条上游路径）。
	RealModelID string
	// Reason 是稳定原因码（取上游错误类，如 upstream_auth / upstream_5xx）。
	Reason string
	// Detail 是人读诊断（脱敏，仅供管理端）。
	Detail string
	// FailedAt 是最近一次判定不可用的时刻（Unix 秒）。
	FailedAt int64
	// Attempts 是连续判定不可用的次数（退避曲线的指数）。
	Attempts int64
	// NextCheckAt 是下次探测时刻（Unix 秒）。
	NextCheckAt int64
}

// UnavailabilityRepo 是不可用台账的读写面。
type UnavailabilityRepo struct{ base }

const unavailabilityColumns = `real_model_id, reason, detail, failed_at, attempts, next_check_at`

// Get 读一条记录；不存在返回 (nil, nil)。
//
// 为什么不存在返回 nil,nil 而不是 ErrNotFound：调用方（路由装配层）在每个
// 候选上都查一次，把「不在册」当普通控制流而不是错误——每次请求产出一个
// 错误对象既浪费又诱导调用方用错误忽略来写正常分支。
func (r *UnavailabilityRepo) Get(ctx context.Context, realModelID string) (*Unavailability, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+unavailabilityColumns+` FROM model_unavailability WHERE real_model_id = ?`,
		realModelID)
	u, err := scanUnavailability(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: 读不可用记录 %s: %w", realModelID, err)
	}
	return u, nil
}

// List 列出全部不可用记录（管理端展示与诊断用）。
func (r *UnavailabilityRepo) List(ctx context.Context) ([]Unavailability, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+unavailabilityColumns+` FROM model_unavailability ORDER BY next_check_at`)
	if err != nil {
		return nil, fmt.Errorf("store: 列出不可用记录: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Unavailability
	for rows.Next() {
		var u Unavailability
		if err := rows.Scan(&u.RealModelID, &u.Reason, &u.Detail, &u.FailedAt, &u.Attempts, &u.NextCheckAt); err != nil {
			return nil, fmt.Errorf("store: 扫描不可用记录: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListDue 列出 next_check_at 已到点的记录（探测循环每 tick 调用）。
//
// 为什么带 limit：探测是真实推理调用（花额度、占上游并发），一轮不该把
// 全部到期项一次性放出去——limit 让单轮探测有可预期的上界。
func (r *UnavailabilityRepo) ListDue(ctx context.Context, now int64, limit int) ([]Unavailability, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+unavailabilityColumns+` FROM model_unavailability
		  WHERE next_check_at <= ? ORDER BY next_check_at LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("store: 列到期不可用记录失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Unavailability
	for rows.Next() {
		var u Unavailability
		if err := rows.Scan(&u.RealModelID, &u.Reason, &u.Detail, &u.FailedAt, &u.Attempts, &u.NextCheckAt); err != nil {
			return nil, fmt.Errorf("store: 扫描到期不可用记录: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// MarkUnavailable 记一条不可用（upsert）：不存在则建行，存在则 attempts+1
// 并推进 next_check_at。
//
// nextCheckAt 由调用方（装配层的退避曲线）算好传入——曲线属于「探测策略」，
// 存储层不该知道「1m→2m→4m→1h」这个数字。
func (r *UnavailabilityRepo) MarkUnavailable(ctx context.Context, u Unavailability) error {
	if u.RealModelID == "" {
		return errors.New("store: 不可用记录缺 real_model_id")
	}
	if u.Reason == "" {
		// 空原因会让台账退化成「一堆不知道为什么不用的模型」，管理端无从下手。
		u.Reason = "unknown"
	}
	var detail *string
	if u.Detail != "" {
		detail = &u.Detail
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO model_unavailability(real_model_id, reason, detail, failed_at, attempts, next_check_at)
		 VALUES(?,?,?,?,1,?)
		 ON CONFLICT(real_model_id) DO UPDATE SET
		   reason        = excluded.reason,
		   detail        = excluded.detail,
		   failed_at     = excluded.failed_at,
		   attempts      = model_unavailability.attempts + 1,
		   next_check_at = excluded.next_check_at`,
		u.RealModelID, u.Reason, detail, u.FailedAt, u.NextCheckAt); err != nil {
		return fmt.Errorf("store: 记不可用 %s: %w", u.RealModelID, err)
	}
	return nil
}

// MarkAvailable 把一条路径移出台账（探测成功 / 流量成功）。
//
// 幂等：不在册时无操作。这条被**每次成功请求**调用（见装配层的路由桥），
// 所以它必须廉价且不打日志——一次 DELETE 未命中行是毫秒级且无副作用。
func (r *UnavailabilityRepo) MarkAvailable(ctx context.Context, realModelID string) error {
	if realModelID == "" {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM model_unavailability WHERE real_model_id = ?`, realModelID); err != nil {
		return fmt.Errorf("store: 清除不可用 %s: %w", realModelID, err)
	}
	return nil
}

// Reschedule 只推进 next_check_at（探测失败但还没到「重新判失败」的场合）。
//
// 为什么单列一个方法：探测失败要推进排期，但仍要保留原有 reason/attempts
// 语义——用 MarkUnavailable 会把 attempts 再加一次，让退避多翻一倍。
func (r *UnavailabilityRepo) Reschedule(ctx context.Context, realModelID string, nextCheckAt int64) error {
	if realModelID == "" {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE model_unavailability SET next_check_at = ? WHERE real_model_id = ?`,
		nextCheckAt, realModelID); err != nil {
		return fmt.Errorf("store: 推进探测排期 %s: %w", realModelID, err)
	}
	return nil
}

// scanUnavailability 扫一行（detail 可空）。
func scanUnavailability(sc interface{ Scan(...any) error }) (*Unavailability, error) {
	var u Unavailability
	var detail sql.NullString
	if err := sc.Scan(&u.RealModelID, &u.Reason, &detail, &u.FailedAt, &u.Attempts, &u.NextCheckAt); err != nil {
		return nil, err
	}
	u.Detail = detail.String
	return &u, nil
}
