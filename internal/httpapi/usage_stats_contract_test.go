package httpapi

// 本文件守 GET /api/usage/stats（仪表盘读模型）的参数翻译与越权面：
//
//   - from/to/bucket/tz_offset 逐个落到 store 调用参数（「前端发了、后端真的读」）；
//   - scope=all 是 admin 类专属：没有 admin 类的调用必须 403，不给探测面；
//   - 平均延迟在无样本时输出 null（没有样本 ≠ 平均 0ms）；
//   - 空集合输出 [] 而不是 null（信封纪律，与列表端点一致）。
//
// 与 usage_logs_contract_test.go 同理：SQL 条件由 store 包的契约测试守，
// 这里用假 store 只验 handler 的翻译与判定。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// fakeStatsStore 记录最后一次 RequestStats 收到的全部参数，其余方法零值。
type fakeStatsStore struct {
	gotAccountID string
	gotAll       bool
	gotFilter    UsageLogFilter
	gotBucket    int64
	gotTZ        int64
	stats        UsageStats
}

var _ TenantUsageStore = (*fakeStatsStore)(nil)

func (f *fakeStatsStore) AccountSnapshot(context.Context, string) (string, int64, int64, int64, error) {
	return "租户", -1, -1, -1, nil
}
func (f *fakeStatsStore) GroupNames(context.Context, string) ([]string, error) { return nil, nil }
func (f *fakeStatsStore) KeyStats(context.Context, string) (int64, int64, int64, error) {
	return 0, 0, 0, nil
}
func (f *fakeStatsStore) QuotaRows(context.Context, string) ([]UsageQuotaRow, error) {
	return nil, nil
}
func (f *fakeStatsStore) RequestLogs(context.Context, string, UsageLogFilter) ([]UsageLogRow, int64, error) {
	return nil, 0, nil
}
func (f *fakeStatsStore) RequestStats(_ context.Context, accountID string, all bool, filter UsageLogFilter, bucket, tz int64) (UsageStats, error) {
	f.gotAccountID, f.gotAll, f.gotFilter, f.gotBucket, f.gotTZ = accountID, all, filter, bucket, tz
	return f.stats, nil
}
func (f *fakeStatsStore) SessionsOf(context.Context, string) ([]UsageSessionRow, error) {
	return nil, nil
}
func (f *fakeStatsStore) RevokeSession(context.Context, string, string) error { return nil }
func (f *fakeStatsStore) AllowedModels(context.Context, string, *string) ([]UsageModelRow, error) {
	return nil, nil
}

// statsGET 以给定权限类直接调用 handler（不经中间件：本测试只关心翻译与判定）。
func statsGET(t *testing.T, store *fakeStatsStore, classes ...auth.Class) *httptest.ResponseRecorder {
	t.Helper()
	if len(classes) == 0 {
		classes = []auth.Class{auth.ClassTenant}
	}
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	s := New(Deps{Config: config.Default(), Logger: discardLogger()})
	req := httptest.NewRequest(http.MethodGet, "/api/usage/stats", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeySessionPrincipal, &auth.Principal{
		AccountID:   "acc-1",
		DisplayName: "租户",
		Classes:     auth.NewClassSet(classes...),
		ActingClass: classes[0],
	}))
	rec := httptest.NewRecorder()
	s.HandleUsageStats(&TenantUsageDeps{Store: store, Now: func() time.Time { return now }}).ServeHTTP(rec, req)
	return rec
}

func TestUsageStatsDefaults(t *testing.T) {
	store := &fakeStatsStore{}
	rec := statsGET(t, store)
	if rec.Code != http.StatusOK {
		t.Fatalf("缺省参数应 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	// 缺省范围 [now-24h, now]；≤72h 自动选小时桶；时区缺省 0（UTC）。
	if !store.gotFilter.From.Equal(time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("缺省 from 应为 now-24h，得到 %v", store.gotFilter.From)
	}
	if !store.gotFilter.To.Equal(time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("缺省 to 应为 now，得到 %v", store.gotFilter.To)
	}
	if store.gotBucket != 3600 {
		t.Fatalf("≤72h 缺省桶宽应为 3600，得到 %d", store.gotBucket)
	}
	if store.gotTZ != 0 {
		t.Fatalf("缺省时区偏移应为 0，得到 %d", store.gotTZ)
	}
	if store.gotAll || store.gotAccountID != "acc-1" {
		t.Fatalf("缺省 scope=own：应按账户过滤（all=%v, account=%q）", store.gotAll, store.gotAccountID)
	}
}

func TestUsageStatsExplicitParams(t *testing.T) {
	store := &fakeStatsStore{}
	q := "/api/usage/stats?from=2026-10-01T00%3A00%3A00%2B08%3A00&to=2026-10-20T00%3A00%3A00%2B08%3A00&bucket=day&tz_offset=28800"
	req := httptest.NewRequest(http.MethodGet, q, nil)
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	s := New(Deps{Config: config.Default(), Logger: discardLogger()})
	req = req.WithContext(context.WithValue(req.Context(), ctxKeySessionPrincipal, &auth.Principal{
		AccountID: "acc-1", Classes: auth.NewClassSet(auth.ClassTenant), ActingClass: auth.ClassTenant,
	}))
	s.HandleUsageStats(&TenantUsageDeps{Store: store, Now: func() time.Time { return now }}).ServeHTTP(httptest.NewRecorder(), req)

	from, err := time.Parse(time.RFC3339, "2026-10-01T00:00:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if !store.gotFilter.From.Equal(from) {
		t.Fatalf("显式 from 未透传：%v", store.gotFilter.From)
	}
	if store.gotBucket != 86400 {
		t.Fatalf("显式 bucket=day 应为 86400，得到 %d", store.gotBucket)
	}
	if store.gotTZ != 28800 {
		t.Fatalf("显式 tz_offset 未透传：%d", store.gotTZ)
	}
}

func TestUsageStatsScopeGuard(t *testing.T) {
	// 没有 admin 类：scope=all 必须 403，且不能触达 store。
	store := &fakeStatsStore{}
	rec := statsGET(t, store, auth.ClassTenant)
	if rec.Code != http.StatusOK {
		t.Fatalf("前置调用应成功，得到 %d", rec.Code)
	}
	rec = statsGETWithQuery(t, store, "scope=all", auth.ClassTenant)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非 admin 请求 scope=all 应 403，得到 %d：%s", rec.Code, rec.Body.String())
	}
	if store.gotAll {
		t.Fatal("403 的调用不应触达 store")
	}
	// admin 类：放行，且以整站形状调用（accountAll=true）。
	rec = statsGETWithQuery(t, store, "scope=all", auth.ClassAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin 请求 scope=all 应 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	if !store.gotAll {
		t.Fatal("admin 的 scope=all 应以整站形状调用 store")
	}
}

func statsGETWithQuery(t *testing.T, store *fakeStatsStore, query string, classes ...auth.Class) *httptest.ResponseRecorder {
	t.Helper()
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	s := New(Deps{Config: config.Default(), Logger: discardLogger()})
	req := httptest.NewRequest(http.MethodGet, "/api/usage/stats?"+query, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeySessionPrincipal, &auth.Principal{
		AccountID:   "acc-1",
		Classes:     auth.NewClassSet(classes...),
		ActingClass: classes[0],
	}))
	rec := httptest.NewRecorder()
	s.HandleUsageStats(&TenantUsageDeps{Store: store, Now: func() time.Time { return now }}).ServeHTTP(rec, req)
	return rec
}

func TestUsageStatsParamValidation(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"from 不早于 to", "from=2026-10-30T00:00:00Z&to=2026-10-30T00:00:00Z", http.StatusBadRequest},
		{"跨度超 90 天", "from=2026-01-01T00:00:00Z&to=2026-10-30T00:00:00Z", http.StatusBadRequest},
		{"bucket 非法", "bucket=minute", http.StatusBadRequest},
		{"scope 非法", "scope=everyone", http.StatusBadRequest},
	}
	store := &fakeStatsStore{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := statsGETWithQuery(t, store, c.query, auth.ClassTenant)
			if rec.Code != c.want {
				t.Fatalf("%s 应 %d，得到 %d：%s", c.name, c.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUsageStatsEnvelope(t *testing.T) {
	// 有样本：平均延迟除出来；空桶/空模型输出 [] 而不是 null。
	store := &fakeStatsStore{stats: UsageStats{
		Totals: UsageStatsTotals{Requests: 3, Failed: 1, InTokens: 100, OutTokens: 200, ActiveKeys: 2, TTFTSum: 600, TTFTN: 3, DurationSum: 3000, DurationN: 2},
		Buckets: []UsageStatsBucket{
			{Start: 1790000000, Requests: 3, Failed: 1, InTokens: 100, OutTokens: 200},
		},
		Models: []UsageStatsModel{
			{Model: "smart", Requests: 3, Failed: 1, InTokens: 100, OutTokens: 200},
		},
	}}
	rec := statsGET(t, store)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d", rec.Code)
	}
	var body struct {
		Data struct {
			Totals  map[string]any   `json:"totals"`
			Buckets []map[string]any `json:"buckets"`
			Models  []map[string]any `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Totals["avg_ttft_ms"].(float64) != 200 {
		t.Fatalf("avg_ttft_ms 应为 600/3=200，得到 %v", body.Data.Totals["avg_ttft_ms"])
	}
	if _, ok := body.Data.Totals["avg_duration_ms"].(float64); !ok {
		t.Fatalf("avg_duration_ms 应为数值 3000/2，得到 %v", body.Data.Totals["avg_duration_ms"])
	}
	if len(body.Data.Buckets) != 1 || len(body.Data.Models) != 1 {
		t.Fatalf("桶与模型行数不对：%v / %v", body.Data.Buckets, body.Data.Models)
	}

	// 无样本：平均延迟输出 null；空集合输出 []。
	empty := &fakeStatsStore{}
	rec = statsGET(t, empty)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if v, ok := body.Data.Totals["avg_ttft_ms"]; !ok || v != nil {
		t.Fatalf("无样本时 avg_ttft_ms 应为 null，得到 %v", v)
	}
	if body.Data.Buckets == nil || body.Data.Models == nil {
		t.Fatalf("空集合应为 [] 而不是 null：%v / %v", body.Data.Buckets, body.Data.Models)
	}
}
