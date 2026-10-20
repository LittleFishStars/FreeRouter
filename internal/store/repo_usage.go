package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UsageRepo 是计量仓储：请求落库、日汇总、贡献统计、保留期清理。
//
// 边界（2026-10-20 文档改定后收窄）：额度的判定与记账在 QuotaRepo
// （account_quotas / group_quota_pools / group_model_quota_caps），
// 本仓储只管"发生了什么请求"与"聚合成什么数"。分开的原因是两者写频与语义都不同：
// 汇总是报表口径（不可变事实的累加），额度是闸门（可被管理员调小、跨周期归零）。
type UsageRepo struct {
	base
	// quotas 由 newRepos 接线：结算与额度同一个事务视图，不能让调用方自己拼。
	quotas *QuotaRepo
}

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

// Settlement 是一次终态结算要记账的额度量。
//
// 为什么由调用方算好消耗量传进来而不是仓储自己判定：消耗量取决于组策略与
// 计费维度（request/token/credit），那是路由与策略层的知识；持久层只负责
// "在同一个事务里把这些数字加上去"。
type Settlement struct {
	AccountQuotas []QuotaCredit // 账户侧（含合计行与按模型行）
	Pools         []QuotaCredit // 组级共享池侧
}

// usageDelta 是 usage_daily 的累加量。
type usageDelta struct {
	day             string
	accountID       int64
	abstractModelID int64
	realModelID     int64
	requests        int64
	inTokens        int64
	outTokens       int64
}

// RecordRequest 在一次事务里写 requests、累加 usage_daily，并按 Settlement 记账额度。
//
// 为什么必须原子（05-storage.md 7.2）：三段分别落库会出现"有请求记录但日汇总少一笔"
// 或"池子扣了但用量没记"，两种都对不上账，而账是我们唯一的产品事实。
//
// 为什么必须在流结束后一次落库、而不是请求开始时先写一行：那会让每次请求产生两个事务、
// 两倍写入；代价是进程崩溃时丢失在途请求的记录——而在途请求没有对账价值（token 尚未产生）。
//
// 终态由调用方的 defer 保证：正常结束、上游报错、客户端断开、超时、drain 全部要走到这里
// （7.3）。一个"客户端断开导致没记账"的漏洞，在计费产品里就是事故。
func (r *UsageRepo) RecordRequest(ctx context.Context, rec RequestRecord, st Settlement) error {
	// 在事务里做；已在事务里则直接执行。
	if r.s != nil && r.s.tx == nil {
		return r.s.WithTx(ctx, func(tx *Repos) error {
			return tx.Usage.RecordRequest(ctx, rec, st)
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

	// 日汇总只在归属三元组齐时累加：usage_daily 的主键全部 NOT NULL，
	// 缺账户/模型的请求（如鉴权失败的 401）没有汇总价值，明细已足够回溯。
	if rec.AccountID != nil && rec.VirtualModelID != nil && rec.ResolvedRealModelID != nil {
		d := usageDelta{
			day:             time.Unix(ts, 0).UTC().Format("2006-01-02"),
			accountID:       *rec.AccountID,
			abstractModelID: *rec.VirtualModelID,
			realModelID:     *rec.ResolvedRealModelID,
			requests:        1,
			inTokens:        derefInt64(rec.InTokens), // NULL 聚合为 0，原值仍可在 requests 回溯
			outTokens:       derefInt64(rec.OutTokens),
		}
		if err := r.addUsageDaily(ctx, d); err != nil {
			return err
		}
	}

	// 额度记账：无条件累加实际消耗（不做闸门判定——判定发生在请求开始前的
	// QuotaRepo.TryDebit*，见 7.4 的"本周期总量可能超上限"）。
	if err := r.quotas.CreditQuota(ctx, st.AccountQuotas, st.Pools); err != nil {
		return err
	}

	return nil
}

// addUsageDaily 用 upsert 累加。读-改-写会在并发下丢更新，upsert 由 DB 保证原子。
func (r *UsageRepo) addUsageDaily(ctx context.Context, d usageDelta) error {
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO usage_daily(day, account_id, abstract_model_id, real_model_id,
		                         requests, in_tokens, out_tokens)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(day, account_id, abstract_model_id, real_model_id) DO UPDATE SET
		     requests   = requests + excluded.requests,
		     in_tokens  = in_tokens + excluded.in_tokens,
		     out_tokens = out_tokens + excluded.out_tokens`,
		d.day, d.accountID, d.abstractModelID, d.realModelID,
		d.requests, d.inTokens, d.outTokens); err != nil {
		return fmt.Errorf("store: 累加日汇总失败: %w", err)
	}
	return nil
}

// UsageRow 是一行日汇总。
//
// 2026-10-20：聚合维度由 key_id 改为 account_id（额度判定目标就是账户，见
// security/06-audit 第 1 节）。按 Key 的明细在 requests 表，不在此表。
type UsageRow struct {
	Day             string
	AccountID       int64
	AbstractModelID int64
	RealModelID     int64
	Requests        int64
	InTokens        int64
	OutTokens       int64
}

// UsageByAccountRange 按账户与日期区间读日汇总（看板与用量页）。
//
// 走 ix_usage_daily_account(account_id, day)：索引前导列是 account_id，与查询条件同序
// （05-storage.md 第 4 节：时间序列索引的末列是 ts/day，不是首列）。
func (r *UsageRepo) UsageByAccountRange(ctx context.Context, accountID int64, fromDay, toDay string) ([]UsageRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT day, account_id, abstract_model_id, real_model_id, requests, in_tokens, out_tokens
		   FROM usage_daily
		  WHERE account_id = ? AND day >= ? AND day <= ?
		  ORDER BY day, abstract_model_id, real_model_id`,
		accountID, fromDay, toDay)
	if err != nil {
		return nil, fmt.Errorf("store: 读取用量失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []UsageRow
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.Day, &u.AccountID, &u.AbstractModelID, &u.RealModelID,
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

// UsageByDay 读某一天的全量汇总（日报表，按账户聚合后交给上层做展示）。
func (r *UsageRepo) UsageByDay(ctx context.Context, day string) ([]UsageRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT day, account_id, abstract_model_id, real_model_id, requests, in_tokens, out_tokens
		   FROM usage_daily
		  WHERE day = ?
		  ORDER BY account_id, abstract_model_id, real_model_id`, day)
	if err != nil {
		return nil, fmt.Errorf("store: 读取日报失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []UsageRow
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.Day, &u.AccountID, &u.AbstractModelID, &u.RealModelID,
			&u.Requests, &u.InTokens, &u.OutTokens); err != nil {
			return nil, fmt.Errorf("store: 扫描日报失败: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历日报失败: %w", err)
	}
	return out, nil
}

// ContributorStat 是贡献统计的一行（来自 v_contributor_stats 视图）。
//
// 不落汇总表：统计是纯读取，落表就必须在请求落库的同一事务里维护它，
// 而任何一处漏更新都会让数字与事实永久分叉。视图的答案永远与事实表同源。
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

// ---------------------------------------------------------------- 保留期清理

// PruneOptions 是清理参数（05-storage.md 8.3）。
type PruneOptions struct {
	// RequestLogRetainDays：requests 明细保留天数，0 = 永久（不删）。
	RequestLogRetainDays int
	// AuditRetainDays：审计保留天数，0 = 永久（默认！非 0 时必须走归档通道）。
	AuditRetainDays int
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

// Prune 按保留策略清理过期数据。
//
// 保留策略（05-storage.md 8.3）：
//   - sessions：过期即清（纯垃圾，无保留价值）
//   - requests：按 request_log_retain_days，0 = 永久不删（更久靠 usage_daily）
//   - audit_log：按 audit_retain_days，0 = 永久（默认）；非 0 时转交 LogRepo.PruneAudit
//     的归档受控通道，这里不直接删——审计的删除会消灭"谁删了审计"的答案。
//
// 为什么整个清理在一个事务里：会话与请求的清理虽然各自独立，但"半清理"状态
// （requests 删了、审计没写）会让运维无法判断上次清理到底做到哪一步。
func (r *UsageRepo) Prune(ctx context.Context, opts PruneOptions) (*PruneResult, error) {
	if r.s != nil && r.s.tx == nil {
		var res *PruneResult
		err := r.s.WithTx(ctx, func(tx *Repos) error {
			var err error
			res, err = tx.Usage.Prune(ctx, opts)
			return err
		})
		if err != nil {
			return nil, err
		}
		// 审计走独立通道（它在事务外先导出文件，不能塞进本事务）。
		if opts.AuditRetainDays > 0 {
			audit, err := r.s.Repos().Logs.PruneAudit(ctx, opts.AuditRetainDays, opts.AuditArchiveDir)
			if err != nil {
				return res, err
			}
			if audit != nil {
				res.Audit = audit.Audit
				res.AuditCutoff = audit.AuditCutoff
				res.AuditArchive = audit.AuditArchive
			}
		}
		return res, nil
	}

	out := &PruneResult{At: now()}

	n, err := r.prune(ctx, `DELETE FROM sessions WHERE expires_at < ?`, out.At)
	if err != nil {
		return nil, err
	}
	out.Sessions = n

	if opts.RequestLogRetainDays > 0 {
		cutoff := out.At - int64(opts.RequestLogRetainDays)*86400
		n, err := r.prune(ctx, `DELETE FROM requests WHERE ts < ?`, cutoff)
		if err != nil {
			return nil, err
		}
		out.Requests = n
		out.RequestsCutoff = cutoff
	}

	// 在事务视图里不碰审计：审计清理必须是顶层调用（要导出文件到库外）。
	return out, nil
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
