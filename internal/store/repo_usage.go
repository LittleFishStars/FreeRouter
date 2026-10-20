package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UsageRepo 是计量仓储：请求落库、日汇总、组级共享池、贡献统计。
type UsageRepo struct{ base }

// RequestRecord 是一次已完成请求的落库形状。
//
// 字段全部用指针表达"上游没报"：in_tokens/out_tokens 为 nil 与 0 在配额护栏与
// 对账上语义完全不同（05-storage.md 3.7）。
type RequestRecord struct {
	TS                   int64
	KeyID                *int64
	AccountID            *int64
	Endpoint             string
	VirtualModelID       *int64
	ResolvedRealModelID  *int64
	ResolvedCredentialID *int64
	Status               string
	HTTPStatus           *int64
	ErrorCode            *string
	RetryCount           int64
	TTFTMs               *int64
	TotalMs              *int64
	InTokens             *int64
	OutTokens            *int64
}

// usageDelta 是 usage_daily 的累加量。
type usageDelta struct {
	day               string
	keyID             int64
	abstractModelID   int64
	realModelID       int64
	requests          int64
	inTokens          int64
	outTokens         int64
}

// RecordRequest 在一次事务里写 requests 并累加 usage_daily，可选再递增 Key 额度
// 与扣减组级共享池。
//
// 为什么必须原子（05-storage.md 7.2）：四段分别落库会出现"有请求记录但日汇总少一笔"
// 或"池子扣了但用量没记"，两种都对不上账，而账是我们唯一的产品事实。
//
// 为什么必须在流结束后一次落库、而不是请求开始时先写一行：那会让每次请求产生两个事务、
// 两倍写入；代价是进程崩溃时丢失在途请求的记录——而在途请求没有对账价值（token 尚未产生）。
func (r *UsageRepo) RecordRequest(ctx context.Context, rec RequestRecord, quotaDelta int64, pools []PoolDebit) error {
	// 在事务里做；已在事务里则直接执行。
	if r.s != nil && r.s.tx == nil {
		return r.s.WithTx(ctx, func(tx *Repos) error {
			return tx.Usage.RecordRequest(ctx, rec, quotaDelta, pools)
		})
	}

	ts := rec.TS
	if ts == 0 {
		ts = now()
	}

	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO requests(ts, key_id, account_id, endpoint, virtual_model_id,
		                      resolved_real_model_id, resolved_credential_id, status,
		                      http_status, error_code, retry_count, ttft_ms, total_ms,
		                      in_tokens, out_tokens)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ts, rec.KeyID, rec.AccountID, rec.Endpoint, rec.VirtualModelID,
		rec.ResolvedRealModelID, rec.ResolvedCredentialID, rec.Status,
		rec.HTTPStatus, rec.ErrorCode, rec.RetryCount, rec.TTFTMs, rec.TotalMs,
		rec.InTokens, rec.OutTokens); err != nil {
		return fmt.Errorf("store: 写入请求记录失败: %w", err)
	}

	// 日汇总只在有归属三元组时累加：usage_daily 的主键全部 NOT NULL，
	// 缺 key/model 的请求（如鉴权失败的 401）没有汇总价值，明细已足够回溯。
	if rec.KeyID != nil && rec.VirtualModelID != nil && rec.ResolvedRealModelID != nil {
		d := usageDelta{
			day:             time.Unix(ts, 0).UTC().Format("2006-01-02"),
			keyID:           *rec.KeyID,
			abstractModelID: *rec.VirtualModelID,
			realModelID:     *rec.ResolvedRealModelID,
			requests:        1,
			inTokens:        derefInt64(rec.InTokens),  // NULL 聚合为 0，原值仍可在 requests 回溯
			outTokens:       derefInt64(rec.OutTokens),
		}
		if err := r.addUsageDaily(ctx, d); err != nil {
			return err
		}
	}

	if quotaDelta != 0 && rec.KeyID != nil {
		if _, err := r.db.ExecContext(ctx,
			`UPDATE api_keys SET quota_used = quota_used + ? WHERE id = ?`,
			quotaDelta, *rec.KeyID); err != nil {
			return fmt.Errorf("store: 递增 Key 额度失败: %w", err)
		}
	}

	for _, p := range pools {
		if err := r.debitPool(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// addUsageDaily 用 upsert 累加。读-改-写会在并发下丢更新，upsert 由 DB 保证原子。
func (r *UsageRepo) addUsageDaily(ctx context.Context, d usageDelta) error {
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO usage_daily(day, key_id, abstract_model_id, real_model_id,
		                         requests, in_tokens, out_tokens)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(day, key_id, abstract_model_id, real_model_id) DO UPDATE SET
		     requests   = requests + excluded.requests,
		     in_tokens  = in_tokens + excluded.in_tokens,
		     out_tokens = out_tokens + excluded.out_tokens`,
		d.day, d.keyID, d.abstractModelID, d.realModelID,
		d.requests, d.inTokens, d.outTokens); err != nil {
		return fmt.Errorf("store: 累加日汇总失败: %w", err)
	}
	return nil
}

// PoolDebit 是一次组级共享池扣减请求。
type PoolDebit struct {
	GroupID  int64
	Dimension string // requests | tokens
	Period    string // day | month
	Cost      int64
}

// ErrPoolExhausted 表示组级共享池额度不足（扣减影响 0 行）。
var ErrPoolExhausted = errPoolExhausted{}

type errPoolExhausted struct{}

func (errPoolExhausted) Error() string { return "store: 组级共享池额度不足" }

// debitPool 是组级共享池的并发扣减：判定与递增压进同一条带条件的 UPDATE，
// 以受影响行数为成败（05-storage.md 7.4）。
//
// 为什么不用"先 SELECT 判定、再 UPDATE 扣减"：两步之间存在窗口，N 个并发请求会都读到
// pool_used < limit 然后各自扣减成功，池子被超发，且超发量随并发度线性增长。
// 更坑的是它在开发环境永远不出现（并发低），只在真实流量峰值时出现。
//
// 重置必须先于扣减且同事务：分开写会出现"已跨周期但尚未归零"的窗口，窗口内的请求
// 会拿上期的 pool_used 去比本期的 pool_limit。
func (r *UsageRepo) debitPool(ctx context.Context, p PoolDebit) error {
	if p.GroupID == 0 {
		return fmt.Errorf("store: 池扣减缺少 group_id")
	}
	if p.Cost < 0 {
		return fmt.Errorf("store: 池扣减量不能为负（%d）", p.Cost)
	}
	anchor := poolAnchor(p.Period, time.Now().UTC())
	ts := now()

	// ① 惰性重置：幂等，且跨周期前受影响行数为 0。
	if _, err := r.db.ExecContext(ctx,
		`UPDATE group_quota_pools
		    SET pool_used = 0, period_anchor = ?, updated_at = ?
		  WHERE group_id = ? AND dimension = ? AND period = ?
		    AND period_anchor <> ?`,
		anchor, ts, p.GroupID, p.Dimension, p.Period, anchor); err != nil {
		return fmt.Errorf("store: 池惰性重置失败: %w", err)
	}

	// ② 条件扣减：0 行 = 额度不足（拒绝）；1 行 = 成功（放行）。
	res, err := r.db.ExecContext(ctx,
		`UPDATE group_quota_pools
		    SET pool_used = pool_used + ?, updated_at = ?
		  WHERE group_id = ? AND dimension = ? AND period = ?
		    AND period_anchor = ?
		    AND pool_used + ? <= pool_limit`,
		p.Cost, ts, p.GroupID, p.Dimension, p.Period, anchor, p.Cost)
	if err != nil {
		return fmt.Errorf("store: 池扣减失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取池扣减行数失败: %w", err)
	}
	if n == 0 {
		// 区分"没有这一行（不设池）"与"额度不足"：前者放行，后者拒绝。
		// 不设池 = 没有这一行（05-storage.md 3.3），因此 0 行且无行时不是错误。
		var exists int
		if err := r.db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM group_quota_pools
			  WHERE group_id = ? AND dimension = ? AND period = ?`,
			p.GroupID, p.Dimension, p.Period).Scan(&exists); err != nil {
			return fmt.Errorf("store: 判定池是否存在失败: %w", err)
		}
		if exists == 0 {
			return nil // 未设池：不限制
		}
		return ErrPoolExhausted
	}
	return nil
}

// poolAnchor 返回周期起点。day 用 UTC 日期；month 用当月 1 日。
// 与 usage_daily.day 共用同一日界（当前设计，见 05-storage.md 第 9 节的待定项）。
func poolAnchor(period string, t time.Time) string {
	switch period {
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	default:
		return t.Format("2006-01-02")
	}
}

// SetPool 设置/更新组级共享池上限。
//
// 允许把上限调到低于当前 pool_used（应急关闸）：后续请求全被拒是正确表现，
// 而拒绝管理员关闸不是（05-storage.md 3.3）。
func (r *UsageRepo) SetPool(ctx context.Context, groupID int64, dimension, period string, limit int64) error {
	if limit <= 0 {
		return fmt.Errorf("store: pool_limit 必须为正数（%d）。停用池请用 DeletePool 删行", limit)
	}
	if dimension != "requests" && dimension != "tokens" {
		return fmt.Errorf("store: 非法 dimension %q", dimension)
	}
	if period != "day" && period != "month" {
		return fmt.Errorf("store: 非法 period %q", period)
	}
	anchor := poolAnchor(period, time.Now().UTC())

	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO group_quota_pools(group_id, dimension, period, pool_limit, pool_used,
		                               period_anchor, updated_at)
		 VALUES(?,?,?,?,0,?,?)
		 ON CONFLICT(group_id, dimension, period) DO UPDATE SET
		     pool_limit = excluded.pool_limit,
		     updated_at = excluded.updated_at`,
		groupID, dimension, period, limit, anchor, now()); err != nil {
		return fmt.Errorf("store: 设置池失败: %w", err)
	}
	return nil
}

// DeletePool 删池行 = 停用池（保留配置但停用这个需求主动不做）。
func (r *UsageRepo) DeletePool(ctx context.Context, groupID int64, dimension, period string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM group_quota_pools WHERE group_id = ? AND dimension = ? AND period = ?`,
		groupID, dimension, period); err != nil {
		return fmt.Errorf("store: 删除池失败: %w", err)
	}
	return nil
}

// PoolState 是池的当前状态（组详情页的"还剩多少"）。
type PoolState struct {
	GroupID      int64
	Dimension    string
	Period       string
	PoolLimit    int64
	PoolUsed     int64
	PeriodAnchor string
	UpdatedAt    int64
}

// GetPool 读池状态。返回 ErrNotFound 表示该组没有设这个池（= 不限制）。
func (r *UsageRepo) GetPool(ctx context.Context, groupID int64, dimension, period string) (*PoolState, error) {
	var p PoolState
	err := r.db.QueryRowContext(ctx,
		`SELECT group_id, dimension, period, pool_limit, pool_used, period_anchor, updated_at
		   FROM group_quota_pools
		  WHERE group_id = ? AND dimension = ? AND period = ?`,
		groupID, dimension, period).Scan(
		&p.GroupID, &p.Dimension, &p.Period, &p.PoolLimit, &p.PoolUsed,
		&p.PeriodAnchor, &p.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 读取池状态失败: %w", err)
	}
	return &p, nil
}

// UsageRow 是一行日汇总。
type UsageRow struct {
	Day             string
	KeyID           int64
	AbstractModelID int64
	RealModelID     int64
	Requests        int64
	InTokens        int64
	OutTokens       int64
}

// UsageByKeyRange 按 key 与日期区间读日汇总（看板与用量页）。
//
// 走 ix_usage_daily_key(key_id, day)：索引前导列是 key_id，与查询条件同序。
func (r *UsageRepo) UsageByKeyRange(ctx context.Context, keyID int64, fromDay, toDay string) ([]UsageRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT day, key_id, abstract_model_id, real_model_id, requests, in_tokens, out_tokens
		   FROM usage_daily
		  WHERE key_id = ? AND day >= ? AND day <= ?
		  ORDER BY day, abstract_model_id, real_model_id`,
		keyID, fromDay, toDay)
	if err != nil {
		return nil, fmt.Errorf("store: 读取用量失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []UsageRow
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.Day, &u.KeyID, &u.AbstractModelID, &u.RealModelID,
			&u.Requests, &u.InTokens, &u.OutTokens); err != nil {
			return nil, fmt.Errorf("store: 扫描用量失败: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历用量失败: %w", err)
	}
	return out, nil
}

// ContributorStat 是贡献统计的一行（来自 v_contributor_stats 视图）。
//
// 不落汇总表：统计是纯读取，落表就必须在请求落库的同一事务里维护它，
// 而任何一处漏更新都会让数字与事实永久分叉。
type ContributorStat struct {
	AccountID     int64
	CredentialID  int64
	Provider      string
	Status        string
	VerifyStatus  string
	RequestCount  int64
	InTokens      int64
	OutTokens     int64
	LastRequestAt *int64
}

// ContributorStats 读贡献统计视图。
func (r *UsageRepo) ContributorStats(ctx context.Context, accountID int64) ([]ContributorStat, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT account_id, credential_id, provider, status, verify_status,
		        request_count, in_tokens, out_tokens, last_request_at
		   FROM v_contributor_stats
		  WHERE account_id = ?
		  ORDER BY credential_id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: 读取贡献统计失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ContributorStat
	for rows.Next() {
		var s ContributorStat
		if err := rows.Scan(&s.AccountID, &s.CredentialID, &s.Provider, &s.Status,
			&s.VerifyStatus, &s.RequestCount, &s.InTokens, &s.OutTokens,
			&s.LastRequestAt); err != nil {
			return nil, fmt.Errorf("store: 扫描贡献统计失败: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历贡献统计失败: %w", err)
	}
	return out, nil
}

// Prune 按保留策略清理过期数据，返回各类删除行数。
//
// 保留策略（05-storage.md 8.3）：
//   - requests：按 request_log_retain_days，0 = 永久不删
//   - audit_log：按 audit_retain_days，0 = 永久不删（默认！），非 0 时必须走归档通道
//   - sessions：过期即清（纯垃圾，无保留价值）
//
// usage_daily 永不清理：requests 按 90 天清掉后，它就是唯一的长期事实。
func (r *UsageRepo) Prune(ctx context.Context, opts PruneOptions) (*PruneResult, error) {
	// 清理是全库操作，必须在事务里保证"要么都成功要么都不做"。
	if r.s != nil && r.s.tx == nil {
		var res *PruneResult
		err := r.s.WithTx(ctx, func(tx *Repos) error {
			var err error
			res, err = tx.Usage.Prune(ctx, opts)
			return err
		})
		return res, err
	}

	out := &PruneResult{At: now()}

	// 会话：过期即清。
	n, err := r.prune(ctx, `DELETE FROM sessions WHERE expires_at < ?`, out.At)
	if err != nil {
		return nil, err
	}
	out.Sessions = n

	// requests：0 = 永久，不删。
	if opts.RequestLogRetainDays > 0 {
		cutoff := out.At - int64(opts.RequestLogRetainDays)*86400
		n, err := r.prune(ctx, `DELETE FROM requests WHERE ts < ?`, cutoff)
		if err != nil {
			return nil, err
		}
		out.Requests = n
		out.RequestsCutoff = cutoff
	}

	// audit_log：默认永久。非 0 时走归档通道（见 PruneAudit）。
	return out, nil
}

// PruneOptions 是清理参数。
type PruneOptions struct {
	RequestLogRetainDays int
	AuditRetainDays      int
	// AuditArchiveDir 非空时允许按归档通道清理审计；为空且 AuditRetainDays > 0 时
	// Prune 拒绝删除审计并返回错误——没有归档就没有证据，宁可不删。
	AuditArchiveDir string
}

// PruneResult 是清理结果，用于审计与可观测。
type PruneResult struct {
	At             int64
	Sessions       int64
	Requests       int64
	RequestsCutoff int64
	Audit          int64
	AuditCutoff    int64
	AuditArchive   string
}

func (r *UsageRepo) prune(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("store: 清理失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取清理行数失败: %w", err)
	}
	return n, nil
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// isNoRows 收敛 sql.ErrNoRows 的判定。
// 用 errors.Is 而不是比较错误字符串：字符串比较会在驱动换实现或多一层包装时静默失效。
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

