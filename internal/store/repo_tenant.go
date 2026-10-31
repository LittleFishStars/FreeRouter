package store

// repo_tenant.go —— 租户面/账户自助面的只读查询（05-storage 读模型）。
//
// 为什么集中一个文件：这些查询都是「给控制台看」的读模型（列表/统计），
// 与写路径（RecordRequest/SetKeyStatus）的职责不同；集中放置让
// 「读面有哪些」一眼可见，避免在写路径文件里堆查询。
//
// 约定：
//   - 时间一律 Unix 秒（与 accounts/requests 既有列一致），RFC3339 由 httpapi 层转换；
//   - 「不存在」统一返回 ErrNotFound；列表空返回空切片不是 nil；
//   - 分页 total 与行一起返回（两次查询），调用方不拼二次查询。

import (
	"context"
	"fmt"
)

// ---------------------------------------------------------------------------
// 会话列表（账户自助「我的会话」）
// ---------------------------------------------------------------------------

// SessionRow 是一条会话的展示行。刻意不含 token_hash：展示面不需要，
// 且哈希离开判定面（SessionRepo.GetSessionByTokenHash）就是多余敏感数据。
type SessionRow struct {
	ID        string
	CreatedAt int64
	ExpiresAt int64
	IP        *string
	UA        *string
}

// ListSessionsByAccount 列出该账户全部会话（按创建时间倒序）。
func (r *SessionRepo) ListSessionsByAccount(ctx context.Context, accountID string) ([]SessionRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, created_at, expires_at, ip, ua
		   FROM sessions WHERE account_id = ? ORDER BY created_at DESC, id DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: 列出账户会话失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []SessionRow{}
	for rows.Next() {
		var row SessionRow
		if err := rows.Scan(&row.ID, &row.CreatedAt, &row.ExpiresAt, &row.IP, &row.UA); err != nil {
			return nil, fmt.Errorf("store: 扫描会话失败: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历会话失败: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 请求日志（租户「调用日志」）
// ---------------------------------------------------------------------------

// RequestLogFilter 是调用日志的筛选与分页。
//
// From/To 是 Unix 秒（0 = 不限）；KeyID/ModelID nil = 不筛；
// Failed = 只看失败（status != 'ok'）；ErrorCode 非空 = 按错误码精确匹配；
// Limit/Offset 由调用方 clamp。
//
// 为什么 ErrorCode 用空串而不是 *string 表示「不筛」：失败行上的错误码总是
// 一个非空机器码（如 no_candidate），空串不可能误伤某个合法取值；而多一个
// 指针只会让每个调用方多写一层 nil 判断。
type RequestLogFilter struct {
	From, To       int64
	KeyID, ModelID *string
	Failed         bool
	ErrorCode      string
	Limit, Offset  int
}

// RequestLogRow 是一条请求日志的展示行。
//
// AbstractModel/KeyName/KeyPrefix 来自 LEFT JOIN：历史行引用的对象可能已被删，
// 缺失时是空串而不是错误（日志必须能显示，哪怕对象没了）。
type RequestLogRow struct {
	ID            string
	TS            int64
	Endpoint      string
	AbstractModel string
	KeyID         *string
	KeyName       string
	KeyPrefix     string
	Status        string
	HTTPStatus    *int64
	ErrorCode     *string
	RetryCount    int64
	TTFTMs        *int64
	TotalMs       *int64
	InTokens      *int64
	OutTokens     *int64
}

const requestLogColumns = `SELECT r.id, r.ts, r.endpoint, COALESCE(m.name, ''),
       r.key_id, COALESCE(k.name, ''), COALESCE(k.prefix, ''),
       r.status, r.http_status, r.error_code, r.retry_count,
       r.ttft_ms, r.total_ms, r.in_tokens, r.out_tokens
  FROM requests r
  LEFT JOIN abstract_models m ON m.id = r.virtual_model_id
  LEFT JOIN api_keys k ON k.id = r.key_id`

// ListRequestLogsByAccount 按账户列请求日志（筛选 + 分页 + 总数）。
func (r *UsageRepo) ListRequestLogsByAccount(ctx context.Context, accountID string, f RequestLogFilter) ([]RequestLogRow, int64, error) {
	where := " r.account_id = ?"
	var args []any
	args = append(args, accountID)
	if f.From > 0 {
		where += " AND r.ts >= ?"
		args = append(args, f.From)
	}
	if f.To > 0 {
		where += " AND r.ts <= ?"
		args = append(args, f.To)
	}
	if f.KeyID != nil {
		where += " AND r.key_id = ?"
		args = append(args, *f.KeyID)
	}
	if f.ModelID != nil {
		where += " AND r.virtual_model_id = ?"
		args = append(args, *f.ModelID)
	}
	if f.Failed {
		where += " AND r.status <> 'ok'"
	}
	if f.ErrorCode != "" {
		// 精确匹配而不是 LIKE：错误码是闭集合的机器码（03-errors），
		// 前缀/模糊匹配只会把 no_candidate 与 no_candidate_x 这类未来取值混淆。
		where += " AND r.error_code = ?"
		args = append(args, f.ErrorCode)
	}
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM requests r WHERE`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计请求日志失败: %w", err)
	}

	limit, offset := f.Limit, f.Offset
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := r.db.QueryContext(ctx, requestLogColumns+
		` WHERE`+where+fmt.Sprintf(` ORDER BY r.ts DESC, r.id DESC LIMIT %d OFFSET %d`, limit, offset),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 列出请求日志失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []RequestLogRow{}
	for rows.Next() {
		var row RequestLogRow
		if err := rows.Scan(&row.ID, &row.TS, &row.Endpoint, &row.AbstractModel,
			&row.KeyID, &row.KeyName, &row.KeyPrefix,
			&row.Status, &row.HTTPStatus, &row.ErrorCode, &row.RetryCount,
			&row.TTFTMs, &row.TotalMs, &row.InTokens, &row.OutTokens); err != nil {
			return nil, 0, fmt.Errorf("store: 扫描请求日志失败: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: 遍历请求日志失败: %w", err)
	}
	return out, total, nil
}

// CountKeysUsedSince 统计某账户在 since（Unix 秒）以来用过的 Key 数（DISTINCT）。
// keys_used_7d 用；没有请求 = 0。
func (r *UsageRepo) CountKeysUsedSince(ctx context.Context, accountID string, since int64) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT key_id) FROM requests WHERE account_id = ? AND key_id IS NOT NULL AND ts >= ?`,
		accountID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: 统计近用 Key 失败: %w", err)
	}
	return n, nil
}

// RequestStats 是仪表盘（GET /api/usage/stats）的一次聚合结果。
//
// 三块一起返回（总量 / 时间桶 / 按模型）而不是三个方法：它们共用同一个
// WHERE 前缀，拆开会让调用方在三次请求里重复传同一组筛选，也容易漂。
type RequestStats struct {
	Totals RequestStatsTotals
	// Buckets 按时间升序；空窗（没有任何请求的桶）**不会**出现在结果里，
	// 补零是展示层的事（前端把 from..to 铺满再填 0，服务端不知道展示粒度）。
	Buckets []RequestStatsBucket
	// Models 按请求数降序；只含有请求的模型。
	Models []RequestStatsModelRow
}

// RequestStatsTotals 是筛选范围内的合计。Token 列 COALESCE 为 0（空范围 = 0
// 而不是 NULL）；平均延迟用「和 ÷ 个数」两列分开返回——NULL 与 0 在这里
// 语义不同（没有样本 ≠ 平均 0ms），由调用方除。
type RequestStatsTotals struct {
	Requests    int64
	Failed      int64
	InTokens    int64
	OutTokens   int64
	ActiveKeys  int64
	TTFTSum     int64 // ttft_ms 之和；TTFTN 是非空样本数
	TTFTN       int64
	DurationSum int64
	DurationN   int64
}

// RequestStatsBucket 是一个时间桶。Start 是桶起点（Unix 秒）。
type RequestStatsBucket struct {
	Start     int64
	Requests  int64
	Failed    int64
	InTokens  int64
	OutTokens int64
}

// RequestStatsModelRow 是按抽象模型聚合的一行。
type RequestStatsModelRow struct {
	Model     string
	Requests  int64
	Failed    int64
	InTokens  int64
	OutTokens int64
}

// RequestStats 聚合请求日志（仪表盘）。
//
// accountID 为 nil = 全站（管理员的 scope=all）；非 nil = 只算该账户。
// bucketSeconds 是时间桶宽度（3600 或 86400）；tzOffsetSeconds 是调用方
// 时区相对 UTC 的偏移（东八区 = +28800）——按天分桶必须按「调用方的当地
// 零点」切，否则中国用户的“天”会从 UTC 零点（北京 08:00）开始。小时桶
// 偏移不影响对齐（3600 整除 86400 的位移），传了也无妨。
// From/To 语义与 RequestLogFilter 相同（Unix 秒，0 = 不限）。
func (r *UsageRepo) RequestStats(ctx context.Context, accountID *string, f RequestLogFilter, bucketSeconds, tzOffsetSeconds int64) (RequestStats, error) {
	if bucketSeconds <= 0 {
		bucketSeconds = 86400
	}
	where := " 1=1"
	var args []any
	if accountID != nil {
		where += " AND r.account_id = ?"
		args = append(args, *accountID)
	}
	if f.From > 0 {
		where += " AND r.ts >= ?"
		args = append(args, f.From)
	}
	if f.To > 0 {
		where += " AND r.ts <= ?"
		args = append(args, f.To)
	}

	var out RequestStats
	// 总量：一次扫出全部合计列。status='ok' 计数用布尔表达式（SQLite 里
	// 真=1），与 ListRequestLogsByAccount 的 status <> 'ok' 判失败同一口径。
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1), COALESCE(SUM(r.status <> 'ok'),0),
		       COALESCE(SUM(r.in_tokens),0), COALESCE(SUM(r.out_tokens),0),
		       COUNT(DISTINCT r.key_id),
		       COALESCE(SUM(r.ttft_ms),0), COUNT(r.ttft_ms),
		       COALESCE(SUM(r.total_ms),0), COUNT(r.total_ms)
		  FROM requests r WHERE`+where, args...).
		Scan(&out.Totals.Requests, &out.Totals.Failed,
			&out.Totals.InTokens, &out.Totals.OutTokens,
			&out.Totals.ActiveKeys,
			&out.Totals.TTFTSum, &out.Totals.TTFTN,
			&out.Totals.DurationSum, &out.Totals.DurationN)
	if err != nil {
		return out, fmt.Errorf("store: 聚合请求总量失败: %w", err)
	}

	// 时间桶：本地化按天/按小时切桶。(ts+off)/bucket 是整除向下取整，
	// 乘回 bucket 得到 UTC 标尺上的桶起点——展示层拿它格式化时间。
	rows, err := r.db.QueryContext(ctx,
		`SELECT (r.ts + ?) / ? * ?, COUNT(1), COALESCE(SUM(r.status <> 'ok'),0),
		       COALESCE(SUM(r.in_tokens),0), COALESCE(SUM(r.out_tokens),0)
		  FROM requests r WHERE`+where+` GROUP BY 1 ORDER BY 1`,
		append([]any{tzOffsetSeconds, bucketSeconds, bucketSeconds}, args...)...)
	if err != nil {
		return out, fmt.Errorf("store: 聚合请求时间桶失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out.Buckets = []RequestStatsBucket{}
	for rows.Next() {
		var b RequestStatsBucket
		if err := rows.Scan(&b.Start, &b.Requests, &b.Failed, &b.InTokens, &b.OutTokens); err != nil {
			return out, fmt.Errorf("store: 扫描请求时间桶失败: %w", err)
		}
		out.Buckets = append(out.Buckets, b)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("store: 遍历请求时间桶失败: %w", err)
	}

	// 按模型：LEFT JOIN 保持与日志行同源——模型被删后历史行仍然算数。
	mrows, err := r.db.QueryContext(ctx,
		`SELECT COALESCE(m.name, ''), COUNT(1), COALESCE(SUM(r.status <> 'ok'),0),
		       COALESCE(SUM(r.in_tokens),0), COALESCE(SUM(r.out_tokens),0)
		  FROM requests r
		  LEFT JOIN abstract_models m ON m.id = r.virtual_model_id
		  WHERE`+where+`
		  GROUP BY 1 ORDER BY 4 DESC, 3 DESC LIMIT 50`, args...)
	if err != nil {
		return out, fmt.Errorf("store: 聚合按模型用量失败: %w", err)
	}
	defer func() { _ = mrows.Close() }()
	out.Models = []RequestStatsModelRow{}
	for mrows.Next() {
		var m RequestStatsModelRow
		if err := mrows.Scan(&m.Model, &m.Requests, &m.Failed, &m.InTokens, &m.OutTokens); err != nil {
			return out, fmt.Errorf("store: 扫描按模型用量失败: %w", err)
		}
		out.Models = append(out.Models, m)
	}
	if err := mrows.Err(); err != nil {
		return out, fmt.Errorf("store: 遍历按模型用量失败: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 贡献者面（我的凭据 / 贡献统计）
// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// 贡献者面（我的凭据 / 贡献统计）
// ---------------------------------------------------------------------------

// ListCredentialsByOwner 列出某账户贡献的凭据（按创建时间倒序）。
// 与管理面 ListCredentials 不同：这里按属主过滤，只看自己的。
func (r *ModelRepo) ListCredentialsByOwner(ctx context.Context, ownerAccountID string) ([]Credential, error) {
	rows, err := r.db.QueryContext(ctx, credentialColumns+
		` WHERE owner_account_id = ? AND removed_at IS NULL ORDER BY created_at DESC, id DESC`, ownerAccountID)
	if err != nil {
		return nil, fmt.Errorf("store: 列出贡献凭据失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []Credential{}
	for rows.Next() {
		c, err := scanCredentialRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历贡献凭据失败: %w", err)
	}
	return out, nil
}

// ContributionUsageByOwner 汇总该账户贡献凭据自 since 以来的调用量（贡献统计页）。
//
// requests.resolved_credential_id 指向被使用的凭据；owner 是凭据属主。
// since 是 Unix 秒，**0 = 不限**（保留旧行为，也让「全量」不需要另一个方法）。
// 问询走 ix_requests_credential_ts(resolved_credential_id, ts)：先按凭据等值命中，
// 再在该凭据的请求上做 ts 区间扫描，不带因 COUNT(1) 退化成全表扫。
func (r *UsageRepo) ContributionUsageByOwner(ctx context.Context, ownerAccountID string, since int64) (calls, inTokens, outTokens int64, err error) {
	where := ` WHERE c.owner_account_id = ?`
	args := []any{ownerAccountID}
	if since > 0 {
		where += ` AND r.ts >= ?`
		args = append(args, since)
	}
	err = r.db.QueryRowContext(ctx,
		`SELECT COUNT(1), COALESCE(SUM(in_tokens),0), COALESCE(SUM(out_tokens),0)
		   FROM requests r
		   JOIN upstream_credentials c ON c.id = r.resolved_credential_id`+where, args...).
		Scan(&calls, &inTokens, &outTokens)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("store: 汇总贡献用量失败: %w", err)
	}
	return calls, inTokens, outTokens, nil
}

// ---------------------------------------------------------------------------
// 账户状态（待审核页 / 会话响应）
// ---------------------------------------------------------------------------

// AccountStatusOf 读账户状态与权限类（自助面不透出 hash/速率等大字段）。
type AccountStatusInfo struct {
	Status  string
	Classes []string
}

func (r *AccountRepo) AccountStatusOf(ctx context.Context, id string) (AccountStatusInfo, error) {
	a, err := r.GetAccount(ctx, id)
	if err != nil {
		return AccountStatusInfo{}, err
	}
	classes := make([]string, 0, len(a.Classes))
	for _, c := range a.Classes {
		classes = append(classes, string(c))
	}
	return AccountStatusInfo{Status: string(a.Status), Classes: classes}, nil
}
