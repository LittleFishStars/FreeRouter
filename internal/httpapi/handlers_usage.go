package httpapi

import (
	"context"
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// handlers_usage.go —— 租户面/账户自助面（docs/ui/04-tenant.md 的 HTTP 出口）。
//
// 端点（全部挂 mwSession；写操作经 CSRF 自定义头检查）：
//
//	GET  /api/usage/overview              用量总览（首页摘要 + 用量页）
//	GET  /api/usage/logs                  调用日志（分页 + 筛选）
//	GET  /api/usage/models                可用抽象模型（组授权 ∩ Key 白名单）
//	GET  /api/account/sessions            我的会话列表
//	POST /api/account/sessions/{id}/revoke 吊销我的某条会话
//
// 契约来源：前端 web/src/api/types.ts 的 UsageOverview/UsageLog/AvailableModel/
// AccountSession（界面按契约先行，本文件按同一形状交付后端）。
//
// 为什么是窄接口而不是直接用 store：本包不 import internal/store（架构边界），
// 数据形状在装配层桥接；handler 只关心「按账户 id 拿什么」。accountID 用
// Principal 里的字符串（十进制），转换归桥。

// TenantUsageDeps 是租户面的依赖。
type TenantUsageDeps struct {
	Store TenantUsageStore
	// KeyLimit 是「每账户 Key 上限」（config.limits.default_keys_per_account）。
	// 0 = 不限（overview 的 key_limit 输出 null）。
	KeyLimit int
	// Now 注入时钟（reset_at/keys_used_7d 计算；测试可注入）。
	Now func() time.Time
}

// TenantUsageStore 是租户面读模型的窄接口（装配层桥到 store 仓储）。
type TenantUsageStore interface {
	// AccountSnapshot 取显示名与账户级速率哨兵（-1 不限 / 0 全拒）。
	AccountSnapshot(ctx context.Context, accountID string) (displayName string, rpm, tpm, maxConcurrency int64, err error)
	// GroupNames 取账户所属组名（只读展示，不解释策略来源）。
	GroupNames(ctx context.Context, accountID string) ([]string, error)
	// KeyStats 取 Key 总数 / active 数 / 近 7 天用过的 Key 数。
	KeyStats(ctx context.Context, accountID string) (total, active, used7d int64, err error)
	// QuotaRows 取账户额度行（含合计行与按模型行）。Model 为空串表示合计行。
	QuotaRows(ctx context.Context, accountID string) ([]UsageQuotaRow, error)
	// RequestLogs 按筛选取调用日志（行 + 总数）。
	RequestLogs(ctx context.Context, accountID string, f UsageLogFilter) ([]UsageLogRow, int64, error)
	// RequestStats 聚合请求统计（仪表盘：总量 + 时间桶 + 按模型）。
	// accountAll=true 时忽略 accountID 做整站聚合（调用前 handler 已验过
	// admin 类——桥只翻译不判定）。
	RequestStats(ctx context.Context, accountID string, accountAll bool, f UsageLogFilter, bucketSeconds, tzOffsetSeconds int64) (UsageStats, error)
	// SessionsOf 取我的会话列表（不含 token）。
	SessionsOf(ctx context.Context, accountID string) ([]UsageSessionRow, error)
	// RevokeSession 吊销我的指定会话（id 必须属于该账户，否则 not_found）。
	RevokeSession(ctx context.Context, accountID, sessionID string) error
	// AllowedModels 取该账户可见的抽象模型（组授权 ∩ 可选 Key 白名单）。
	AllowedModels(ctx context.Context, accountID string, keyID *string) ([]UsageModelRow, error)
}

// UsageQuotaRow 是一条额度行。Model 为空 = 合计行。
type UsageQuotaRow struct {
	ModelID string // 抽象模型 UUID（空串=合计行）
	Model   string
	Unit    string // request | token | credit
	Period  string // day | month
	Limit   int64
	Used    int64
	Anchor  string // 'YYYY-MM-DD'
}

// UsageLogRow 是一条调用日志行。
type UsageLogRow struct {
	ID            string // UUID
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

// UsageLogFilter 是调用日志筛选（httpapi 侧形状，桥翻译成 SQL 条件）。
type UsageLogFilter struct {
	From, To time.Time
	KeyID    *string // Key UUID
	Model    string  // 抽象模型名（桥负责名字→id）
	// ErrorCode 精确匹配错误码（03-errors 的机器码，如 no_candidate）；
	// 空串 = 不筛。为什么它不归在 Failed 里：failed 是“有没有错”，错误码是
	// “错在哪”，两者正交——“所有失败”与“no_candidate 失败”都是合法查询。
	ErrorCode string
	Failed    bool
	Limit     int
	Offset    int
}

// UsageSessionRow 是我的会话行。
type UsageSessionRow struct {
	ID        string
	CreatedAt int64
	ExpiresAt int64
	IP        *string
	UA        *string
}

// UsageModelRow 是一条可用模型行。
type UsageModelRow struct {
	Name        string
	DisplayName string
	Status      string
	Replacement string // 弃用替代模型名；空 = 无
}

// HandleUsageOverview GET /api/usage/overview。
func (s *Server) HandleUsageOverview(deps *TenantUsageDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := requireTenant(w, r, deps)
		if !ok {
			return
		}
		displayName, rpm, tpm, mc, err := deps.Store.AccountSnapshot(r.Context(), p.AccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取账户失败", nil)
			return
		}
		groups, err := deps.Store.GroupNames(r.Context(), p.AccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取组失败", nil)
			return
		}
		total, active, used7d, err := deps.Store.KeyStats(r.Context(), p.AccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取 Key 统计失败", nil)
			return
		}
		rows, err := deps.Store.QuotaRows(r.Context(), p.AccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取额度失败", nil)
			return
		}
		now := s.now(deps)
		var dims []map[string]any
		var modelLimits []map[string]any
		for _, row := range rows {
			entry := map[string]any{
				"unit":     row.Unit,
				"period":   row.Period,
				"limit":    row.Limit,
				"used":     row.Used,
				"reset_at": resetAtOf(row.Anchor, row.Period, now),
			}
			if row.ModelID == "" {
				if dims == nil {
					dims = make([]map[string]any, 0, 4)
				}
				dims = append(dims, entry)
				continue
			}
			if modelLimits == nil {
				modelLimits = make([]map[string]any, 0, 4)
			}
			modelLimits = append(modelLimits, map[string]any{
				"model":  row.Model,
				"unit":   row.Unit,
				"period": row.Period,
				"limit":  row.Limit,
				"used":   row.Used,
			})
		}
		if dims == nil {
			dims = []map[string]any{}
		}
		data := map[string]any{
			"display_name":     displayName,
			"group_names":      groups,
			"key_count":        total,
			"active_key_count": active,
			"keys_used_7d":     used7d,
			"dimensions":       dims,
			"rpm_limit":        rpm,
			"tpm_limit":        tpm,
			"max_concurrency":  mc,
		}
		if deps.KeyLimit > 0 {
			data["key_limit"] = deps.KeyLimit
		} else {
			data["key_limit"] = nil
		}
		if modelLimits != nil {
			data["model_limits"] = modelLimits
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
}

// HandleUsageLogs GET /api/usage/logs?limit=&offset=&from=&to=&key_id=&model=&status=&error_code=。
// from/to 是 RFC3339；status 是 ok|failed；error_code 是精确匹配（空 = 不筛）。
// limit 上限 200；offset 从 0 开始（分页只有这一种语义，没有 cursor）。
func (s *Server) HandleUsageLogs(deps *TenantUsageDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := requireTenant(w, r, deps)
		if !ok {
			return
		}
		q := r.URL.Query()
		f := UsageLogFilter{}
		if v, err := time.Parse(time.RFC3339, q.Get("from")); err == nil {
			f.From = v
		}
		if v, err := time.Parse(time.RFC3339, q.Get("to")); err == nil {
			f.To = v
		}
		if q.Get("key_id") != "" {
			if id, err := idgen.Parse(q.Get("key_id")); err == nil {
				f.KeyID = &id
			} else {
				writeError(w, http.StatusBadRequest, "invalid_request", "key_id 非法", nil)
				return
			}
		}
		f.Model = q.Get("model")
		// error_code 精确匹配：前端“错误码”输入框发的就是它；未传/空串 = 不筛。
		f.ErrorCode = q.Get("error_code")
		switch q.Get("status") {
		case "ok":
		case "failed":
			f.Failed = true
		case "":
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "status 取值须为 ok/failed", nil)
			return
		}
		limit := 50
		if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
			limit = v
		}
		if limit > 200 {
			limit = 200
		}
		offset := 0
		if v, err := strconv.Atoi(q.Get("offset")); err == nil && v > 0 {
			offset = v
		}
		f.Limit, f.Offset = limit, offset

		rows, total, err := deps.Store.RequestLogs(r.Context(), p.AccountID, f)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取调用日志失败", nil)
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			entry := map[string]any{
				"id":             row.ID,
				"ts":             unixRFC3339(row.TS),
				"endpoint":       row.Endpoint,
				"abstract_model": row.AbstractModel,
				"key_id":         nil,
				"key_name":       row.KeyName,
				"key_prefix":     row.KeyPrefix,
				"ok":             row.Status == "ok",
				"error_code":     row.ErrorCode,
				"ttft_ms":        row.TTFTMs,
				"duration_ms":    row.TotalMs,
				"input_tokens":   row.InTokens,
				"output_tokens":  row.OutTokens,
			}
			if row.KeyID != nil {
				entry["key_id"] = *row.KeyID
			}
			out = append(out, entry)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": out, "total": total, "next_cursor": nil,
		})
	}
}

// ── 仪表盘（GET /api/usage/stats）─────────────────────────────────

// UsageStatsTotals 是筛选范围内的合计。平均延迟由 Sum/N 两列由 handler 除出
// （没有样本 = null，不冒充 0ms）；Token 是输入+输出之和（缓存归输入侧，
// 上游未拆时不造数）。
type UsageStatsTotals struct {
	Requests    int64
	Failed      int64
	InTokens    int64
	OutTokens   int64
	ActiveKeys  int64
	TTFTSum     int64
	TTFTN       int64
	DurationSum int64
	DurationN   int64
}

// UsageStatsBucket 是一个时间桶（Start 为 Unix 秒；响应时转 RFC3339）。
type UsageStatsBucket struct {
	Start     int64
	Requests  int64
	Failed    int64
	InTokens  int64
	OutTokens int64
}

// UsageStatsModel 是按抽象模型聚合的一行。
type UsageStatsModel struct {
	Model     string
	Requests  int64
	Failed    int64
	InTokens  int64
	OutTokens int64
}

// UsageStats 是仪表盘的一次聚合（总量 + 时间桶 + 按模型）。
type UsageStats struct {
	Totals  UsageStatsTotals
	Buckets []UsageStatsBucket
	Models  []UsageStatsModel
}

// HandleUsageStats GET /api/usage/stats?from=&to=&bucket=&tz_offset=&scope=
//
// 仪表盘读模型（对齐 Tingly Box 的用量仪表盘：五张合计卡 + Token 时间序列 +
// 按模型用量）。参数：
//
//	from/to      RFC3339，可选；缺省 [now-24h, now]；跨度上限 90 天
//	bucket       hour|day（缺省按跨度：≤72h 用 hour，否则 day）
//	tz_offset    调用方时区相对 UTC 的秒数（东八区 = 28800）——按天切桶
//	             必须按调用方的当地零点，否则「今天」会从 UTC 零点开始
//	scope        own（缺省，只看自己）| all（仅 admin 类，整站聚合）
//
// 为什么聚合在后端而不是前端拉日志自己算：日志端点 limit 上限 200，
// 前端翻页聚合既慢又会把「超过 200 条的部分」静默丢掉——仪表盘要的是
// 准确的总量，这只有 SQL GROUP BY 能给。
func (s *Server) HandleUsageStats(deps *TenantUsageDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := requireTenant(w, r, deps)
		if !ok {
			return
		}
		q := r.URL.Query()
		now := s.now(deps)
		from, to := now.Add(-24*time.Hour), now
		if v, err := time.Parse(time.RFC3339, q.Get("from")); err == nil {
			from = v
		}
		if v, err := time.Parse(time.RFC3339, q.Get("to")); err == nil {
			to = v
		}
		if !from.Before(to) {
			writeError(w, http.StatusBadRequest, "invalid_request", "from 必须早于 to", nil)
			return
		}
		const maxSpan = 90 * 24 * time.Hour
		if to.Sub(from) > maxSpan {
			writeError(w, http.StatusBadRequest, "invalid_request", "时间跨度上限 90 天", nil)
			return
		}

		// 桶宽：显式指定优先；否则按跨度自动选（与前端的时间范围选择器同节奏）。
		var bucketSeconds int64 = 86400
		if to.Sub(from) <= 72*time.Hour {
			bucketSeconds = 3600
		}
		switch q.Get("bucket") {
		case "hour":
			bucketSeconds = 3600
		case "day":
			bucketSeconds = 86400
		case "":
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "bucket 取值须为 hour/day", nil)
			return
		}

		// 时区偏移：秒数（local-UTC）。abs 上限 ±14h（现实时区表的边界），
		// 超界按 UTC 处理——分桶错一档好过让畸形参数变成 500。
		tzOffset := int64(0)
		if v, err := strconv.ParseInt(q.Get("tz_offset"), 10, 64); err == nil {
			if v >= -14*3600 && v <= 14*3600 {
				tzOffset = v
			}
		}

		// scope=all 只给 admin 类：整站聚合包含其他账户的调用量，与 01-ia
		// 「跨类资源不聚合展示」不冲突（这里只有数字没有行），但仍按能力类
		// 把关——没有 admin 类的调用一律 403，不给探测面。
		scopeAll := false
		switch q.Get("scope") {
		case "all":
			if !p.Classes.Has(auth.ClassAdmin) {
				writeError(w, http.StatusForbidden, "forbidden", "scope=all 需要 admin 类", nil)
				return
			}
			scopeAll = true
		case "", "own":
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "scope 取值须为 own/all", nil)
			return
		}

		stats, err := deps.Store.RequestStats(r.Context(), p.AccountID, scopeAll,
			UsageLogFilter{From: from, To: to}, bucketSeconds, tzOffset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "聚合用量统计失败", nil)
			return
		}

		// 平均延迟：样本数为 0 时输出 null（没有样本 ≠ 平均 0ms）。
		avgTTFT, avgDur := any(nil), any(nil)
		if stats.Totals.TTFTN > 0 {
			avgTTFT = stats.Totals.TTFTSum / stats.Totals.TTFTN
		}
		if stats.Totals.DurationN > 0 {
			avgDur = stats.Totals.DurationSum / stats.Totals.DurationN
		}
		buckets := make([]map[string]any, 0, len(stats.Buckets))
		for _, b := range stats.Buckets {
			// ⚠️ Store 返回的 Start 是**平移尺度**的裸值（(ts+tz)/step*step，见
			// repo_tenant.go 的注释）——它等于「当地桶起点 + tz」，直接格式化会把
			// 每个桶起点推后 tz（北京用户拿到 08:00 而不是 00:00）。这里必须减回
			// tz 折成真实时刻，前端铺轴才与服务端桶对得上（2026-10-31 图表平零
			// 复盘的两处错之一：前端刻度错位 + 本处未折回，两边凑成了一个
			// 「数值恰好命中、但含数据的最后一桶被 to 裁掉」的复合缺陷）。
			buckets = append(buckets, map[string]any{
				"start": unixRFC3339(b.Start - tzOffset), "requests": b.Requests,
				"failed": b.Failed, "input_tokens": b.InTokens, "output_tokens": b.OutTokens,
			})
		}
		models := make([]map[string]any, 0, len(stats.Models))
		for _, m := range stats.Models {
			models = append(models, map[string]any{
				"model": m.Model, "requests": m.Requests, "failed": m.Failed,
				"input_tokens": m.InTokens, "output_tokens": m.OutTokens,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"from": unixRFC3339(from.Unix()), "to": unixRFC3339(to.Unix()),
			"bucket_seconds": bucketSeconds,
			"totals": map[string]any{
				"requests": stats.Totals.Requests, "failed": stats.Totals.Failed,
				"input_tokens": stats.Totals.InTokens, "output_tokens": stats.Totals.OutTokens,
				"active_keys": stats.Totals.ActiveKeys,
				"avg_ttft_ms": avgTTFT, "avg_duration_ms": avgDur,
			},
			"buckets": buckets, "models": models,
		}})
	}
}

// HandleUsageModels GET /api/usage/models?key_id=。
//
// 响应行只有 {name, display_name, status, replacement}。
//
// 为什么不带 capabilities（审计 2.5 的 E 项）：这里曾恒输出
// "capabilities": {}，但租户侧没有可信数据源——abstract_models 表没有
// capabilities 列（管理面的能力文档是从 note 的私有前缀折出来的，属于
// 管理员编辑面，不应透给租户），而前端按 {} 渲染出来的是“上下文窗口
// 未知、能力未知”的假空行。无值比假值诚实：整条字段删除。
// 将来真有能力列且可对租户展示时，再加回同名字段。
func (s *Server) HandleUsageModels(deps *TenantUsageDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := requireTenant(w, r, deps)
		if !ok {
			return
		}
		var keyID *string
		if v := r.URL.Query().Get("key_id"); v != "" {
			keyID = &v
		}
		rows, err := deps.Store.AllowedModels(r.Context(), p.AccountID, keyID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取可用模型失败", nil)
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"name":         row.Name,
				"display_name": row.DisplayName,
				"status":       row.Status,
				"replacement":  row.Replacement,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nil})
	}
}

// HandleAccountSessions GET /api/account/sessions。
func (s *Server) HandleAccountSessions(deps *TenantUsageDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := requireTenant(w, r, deps)
		if !ok {
			return
		}
		rows, err := deps.Store.SessionsOf(r.Context(), p.AccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "读取会话失败", nil)
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"id":         row.ID,
				"ip":         row.IP,
				"ua":         row.UA,
				"created_at": unixRFC3339(row.CreatedAt),
				"expires_at": unixRFC3339(row.ExpiresAt),
				"current":    row.ID == p.SessionID,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nil})
	}
}

// HandleAccountSessionRevoke POST /api/account/sessions/{id}/revoke。
//
// 与登出同语义：删别人设备的会话不吊销当前会话；会话不属于自己时按
// 「不确认资源存在」给 404（不给探测面）。
func (s *Server) HandleAccountSessionRevoke(deps *TenantUsageDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := requireTenant(w, r, deps)
		if !ok {
			return
		}
		sessionID := chi.URLParam(r, "id")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "缺少会话 id", nil)
			return
		}
		if err := deps.Store.RevokeSession(r.Context(), p.AccountID, sessionID); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// requireTenant 是租户面 handler 的公共前置：必须有会话主体与装配的依赖。
// requireTenant 是租户面 handler 的公共前置：必须有会话主体与装配的依赖。
func requireTenant(w http.ResponseWriter, r *http.Request, deps *TenantUsageDeps) (*auth.Principal, bool) {
	if deps == nil || deps.Store == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "租户面未装配", nil)
		return nil, false
	}
	p := SessionPrincipalFromContext(r.Context())
	if p == nil {
		writeError(w, http.StatusUnauthorized, "session_expired",
			"会话不存在或已过期，请重新登录", nil)
		return nil, false
	}
	return p, true
}

// now 取当前时刻（deps 可注入；缺省 time.Now）。
func (s *Server) now(deps *TenantUsageDeps) time.Time {
	if deps.Now != nil {
		return deps.Now()
	}
	return time.Now()
}

// resetAtOf 由周期锚点算下一次重置时刻（RFC3339）。
//
// day → 锚点日的 UTC 24:00；month → 锚点月的下一月 1 日 00:00。
// 锚点解析失败返回 nil（前端字段可空，宁缺勿错）。
func resetAtOf(anchor, period string, now time.Time) any {
	day, err := time.ParseInLocation("2006-01-02", anchor, time.UTC)
	if err != nil {
		return nil
	}
	var reset time.Time
	if period == "month" {
		reset = day.AddDate(0, 1, 0)
	} else {
		reset = day.Add(24 * time.Hour)
	}
	if !now.Before(reset) {
		return nil // 周期已过（重置惰性未跑）：不给一个误导的过去时刻
	}
	return reset.UTC().Format(time.RFC3339)
}

// unixRFC3339 把 Unix 秒转 RFC3339（0 → null）。
func unixRFC3339(ts int64) any {
	if ts <= 0 {
		return nil
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}
