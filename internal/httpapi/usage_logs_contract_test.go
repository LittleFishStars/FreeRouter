package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// 本文件守 GET /api/usage/logs 的「前端发了、后端真的读」这一侧（审计 P3）：
//
//   - 分页只有 limit/offset（没有 cursor 语义）；
//   - error_code 是精确匹配筛选，且与 status=ok|failed 正交；
//   - 响应信封保持 {data, total, next_cursor}（前端 ListEnvelope 的形状）。
//
// 为什么用假 store 而不是真库：这里要验的是「查询参数 → 筛选结构」的翻译，
// SQL 条件由 store 包的契约测试守（repo_request_logs_contract_test.go）。

// fakeTenantStore 只实现 RequestLogs 的真实行为，其余方法返回零值。
// TenantUsageStore 是窄接口（8 个方法），穷举实现比反射/嵌入更直白。
type fakeTenantStore struct {
	rows []UsageLogRow
	// got 记录最后一次收到的筛选条件，供断言「后端到底读没读这个参数」。
	got UsageLogFilter
}

var _ TenantUsageStore = (*fakeTenantStore)(nil)

func (f *fakeTenantStore) AccountSnapshot(context.Context, string) (string, int64, int64, int64, error) {
	return "租户", -1, -1, -1, nil
}

func (f *fakeTenantStore) GroupNames(context.Context, string) ([]string, error) { return nil, nil }

func (f *fakeTenantStore) KeyStats(context.Context, string) (int64, int64, int64, error) {
	return 0, 0, 0, nil
}

func (f *fakeTenantStore) QuotaRows(context.Context, string) ([]UsageQuotaRow, error) {
	return nil, nil
}

func (f *fakeTenantStore) RequestLogs(_ context.Context, _ string, filter UsageLogFilter) ([]UsageLogRow, int64, error) {
	f.got = filter
	return f.rows, int64(len(f.rows)), nil
}

func (f *fakeTenantStore) SessionsOf(context.Context, string) ([]UsageSessionRow, error) {
	return nil, nil
}

func (f *fakeTenantStore) RevokeSession(context.Context, string, string) error { return nil }

func (f *fakeTenantStore) AllowedModels(context.Context, string, *string) ([]UsageModelRow, error) {
	return nil, nil
}

// logsGET 以租户主体直接调用 handler（不经中间件：本测试只关心参数翻译）。
func logsGET(t *testing.T, store *fakeTenantStore, query string) *httptest.ResponseRecorder {
	t.Helper()
	s := New(Deps{Config: config.Default(), Logger: discardLogger()})
	req := httptest.NewRequest(http.MethodGet, "/api/usage/logs"+query, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeySessionPrincipal, &auth.Principal{
		AccountID:   "acc-1",
		DisplayName: "租户",
		Classes:     auth.NewClassSet(auth.ClassTenant),
		ActingClass: auth.ClassTenant,
	}))
	rec := httptest.NewRecorder()
	s.HandleUsageLogs(&TenantUsageDeps{Store: store}).ServeHTTP(rec, req)
	return rec
}

// TestContractUsageLogsParamsReachStore 钉住三个参数的翻译与信封形状。
func TestContractUsageLogsParamsReachStore(t *testing.T) {
	code := "no_candidate"
	ttft := int64(120)
	store := &fakeTenantStore{rows: []UsageLogRow{{
		ID: 7, TS: 1_700_000_000, Endpoint: "openai-chat", AbstractModel: "fast",
		KeyName: "cli", KeyPrefix: "fr-abcd…wxyz", Status: "error",
		ErrorCode: &code, TTFTMs: &ttft,
	}}}

	rec := logsGET(t, store, "?error_code=no_candidate&status=failed&limit=10&offset=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d：%s", rec.Code, rec.Body.String())
	}

	// —— 参数翻译 ——
	if store.got.ErrorCode != "no_candidate" {
		t.Fatalf("error_code 没有传到 store：%+v", store.got)
	}
	if !store.got.Failed {
		t.Fatalf("status=failed 没有传到 store：%+v", store.got)
	}
	if store.got.Limit != 10 || store.got.Offset != 20 {
		t.Fatalf("limit/offset 翻译错：limit=%d offset=%d，期望 10/20", store.got.Limit, store.got.Offset)
	}

	// —— 响应信封（前端 ListEnvelope 契约：data/total/next_cursor）——
	var env struct {
		Data       []map[string]any `json:"data"`
		Total      *int64           `json:"total"`
		NextCursor any              `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（原文 %s）", err, rec.Body.String())
	}
	if env.Total == nil || *env.Total != 1 {
		t.Fatalf("total = %v，期望 1（翻页依赖它）", env.Total)
	}
	if env.NextCursor != nil {
		t.Fatalf("next_cursor = %v，期望 null（分页只有 offset 一种语义）", env.NextCursor)
	}
	if len(env.Data) != 1 {
		t.Fatalf("data 长度 = %d，期望 1", len(env.Data))
	}
	row := env.Data[0]
	// 行的字段名是前端 UsageLog 的契约，不能在这个修复里被顺手改名。
	for _, k := range []string{"id", "ts", "endpoint", "abstract_model", "key_prefix", "ok", "error_code", "ttft_ms"} {
		if _, ok := row[k]; !ok {
			t.Fatalf("响应行缺少字段 %q：%v", k, row)
		}
	}
	if row["error_code"] != "no_candidate" || row["ok"] != false {
		t.Fatalf("响应行内容不符：%v", row)
	}
}

// TestContractUsageLogsDefaults 钉住「不传筛选 = 不筛」与 offset 的默认值。
func TestContractUsageLogsDefaults(t *testing.T) {
	store := &fakeTenantStore{}
	rec := logsGET(t, store, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d：%s", rec.Code, rec.Body.String())
	}
	if store.got.ErrorCode != "" {
		t.Fatalf("未传 error_code 时不应筛，得 %q", store.got.ErrorCode)
	}
	if store.got.Failed {
		t.Fatalf("未传 status 时不应只看失败：%+v", store.got)
	}
	if store.got.Limit != 50 || store.got.Offset != 0 {
		t.Fatalf("默认分页 = %d/%d，期望 50/0", store.got.Limit, store.got.Offset)
	}
}

// TestContractUsageLogsRejectsBadStatus 守既有的 400 分支没被改动。
func TestContractUsageLogsRejectsBadStatus(t *testing.T) {
	rec := logsGET(t, &fakeTenantStore{}, "?status=weird")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 status 应 400，得 %d：%s", rec.Code, rec.Body.String())
	}
	if e := decodeError(t, rec); e["code"] != "invalid_request" {
		t.Fatalf("错误码 = %v，期望 invalid_request", e["code"])
	}
}
