package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// 本文件的测试都带 Contract 前缀：`make contract` 只跑这些。
// 它们守的是**对外可见的行为**（状态码、错误码、响应头、中间件语义），
// 而不是内部实现——改内部实现不该让这些测试失败，改对外行为必须让它们失败。

func newTestServer(t *testing.T, mutate func(*config.Config)) http.Handler {
	t.Helper()
	cfg := config.Default()
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("测试配置本身不合法：%v", err)
	}
	return New(Deps{Config: cfg, Logger: discardLogger()}).Handler()
}

func do(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// chainContext 让请求完整穿过中间件链的前四步，并返回最终 context。
// 为什么要单跑这四步而不是走 Handler()：Handler() 的路由会把不存在的路径变成 404，
// 于是探针 handler 永远不会被调用，拿不到 context。
func chainContext(t *testing.T, cfg *config.Config, req *http.Request) context.Context {
	t.Helper()
	s := New(Deps{Config: cfg, Logger: discardLogger()})

	var got context.Context
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Context()
	})
	chain := s.mwRequestID(s.mwSecurityHeaders(s.mwTrustedProxy(s.mwBodyLimit(probe))))
	chain.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("探针 handler 未被调用，中间件链提前返回了")
	}
	return got
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（原文 %s）", err, rec.Body.String())
	}
	if body.Error == nil {
		t.Fatalf("响应缺少 error 对象：%s", rec.Body.String())
	}
	return body.Error
}

func TestContractHealthzAndReadyz(t *testing.T) {
	h := newTestServer(t, nil)

	if rec := do(t, h, httptest.NewRequest(http.MethodGet, "/healthz", nil)); rec.Code != http.StatusOK {
		t.Fatalf("healthz 状态码 = %d，期望 200", rec.Code)
	}
	if rec := do(t, h, httptest.NewRequest(http.MethodGet, "/readyz", nil)); rec.Code != http.StatusOK {
		t.Fatalf("readyz 状态码 = %d，期望 200", rec.Code)
	}
}

func TestContractReadyzReflectsShutdown(t *testing.T) {
	h := New(Deps{
		Config:       config.Default(),
		Logger:       discardLogger(),
		ShuttingDown: func() bool { return true },
	}).Handler()

	rec := do(t, h, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("关闭中 readyz 状态码 = %d，期望 503", rec.Code)
	}
	if code := decodeError(t, rec)["code"]; code != "shutting_down" {
		t.Fatalf("code = %v，期望 shutting_down", code)
	}
}

func TestContractReadyzReflectsFailedBootstrap(t *testing.T) {
	// 自举失败（例如 sidecar 没起来）必须让 readyz 变红：
	// 否则负载均衡会把流量送进一个跑不动的进程。
	h := New(Deps{
		Config: config.Default(),
		Logger: discardLogger(),
		Ready:  func(context.Context) error { return errors.New("sidecar 未就绪") },
	}).Handler()

	rec := do(t, h, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("自举失败时 readyz 状态码 = %d，期望 503", rec.Code)
	}
	if code := decodeError(t, rec)["code"]; code != "not_ready" {
		t.Fatalf("code = %v，期望 not_ready", code)
	}
}

func TestContractRequestIDAlwaysPresent(t *testing.T) {
	h := newTestServer(t, nil)

	// 不带 X-Request-ID：服务端必须补一个。
	rec := do(t, h, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := rec.Header().Get("X-Request-ID"); !strings.HasPrefix(got, "req-") {
		t.Fatalf("自动生成的 X-Request-ID = %q，期望 req- 前缀", got)
	}

	// 带合法 X-Request-ID：必须原样沿用（跨进程排障依赖它）。
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "trace-abc-123")
	rec = do(t, h, req)
	if got := rec.Header().Get("X-Request-ID"); got != "trace-abc-123" {
		t.Fatalf("X-Request-ID = %q，期望沿用 trace-abc-123", got)
	}

	// 带非法 X-Request-ID（含换行，会污染日志）：必须被替换掉。
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "bad\nvalue")
	rec = do(t, h, req)
	if got := rec.Header().Get("X-Request-ID"); got == "bad\nvalue" || !strings.HasPrefix(got, "req-") {
		t.Fatalf("非法 X-Request-ID 未被替换，得到 %q", got)
	}
}

func TestContractSecurityHeadersOnEveryResponse(t *testing.T) {
	h := newTestServer(t, nil)

	// 404 也必须带安全头：只在成功响应上加头是常见疏漏。
	for _, path := range []string{"/healthz", "/api/version", "/does-not-exist"} {
		rec := do(t, h, httptest.NewRequest(http.MethodGet, path, nil))
		for _, name := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Content-Security-Policy"} {
			if rec.Header().Get(name) == "" {
				t.Errorf("%s 的响应缺少安全头 %s", path, name)
			}
		}
	}
}

func TestContractBodyLimitRejectsByContentLength(t *testing.T) {
	h := newTestServer(t, func(c *config.Config) { c.Server.MaxBodyBytes = 1024 })

	req := httptest.NewRequest(http.MethodPost, "/healthz", strings.NewReader(strings.Repeat("a", 2048)))
	req.ContentLength = 2048
	rec := do(t, h, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，期望 413", rec.Code)
	}
	if code := decodeError(t, rec)["code"]; code != "request_too_large" {
		t.Fatalf("code = %v，期望 request_too_large", code)
	}
}

func TestContractNotFoundUsesDocumentedCode(t *testing.T) {
	h := newTestServer(t, nil)

	rec := do(t, h, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", rec.Code)
	}
	errObj := decodeError(t, rec)
	if errObj["code"] != "not_found" {
		t.Fatalf("code = %v，期望 not_found", errObj["code"])
	}
	if errObj["type"] != "not_found_error" {
		t.Fatalf("type = %v，期望 not_found_error", errObj["type"])
	}
	if _, ok := errObj["param"]; !ok {
		t.Fatal("错误体必须始终含 param 字段（无值时显式为 null）")
	}
}

func TestContractMethodNotAllowedReusesInvalidRequest(t *testing.T) {
	h := newTestServer(t, nil)

	// GET /healthz 存在、POST /healthz 不存在：405 必须复用 invalid_request，
	// 并把方法名填进 param（docs/api/03-errors.md §4）。
	rec := do(t, h, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d，期望 405", rec.Code)
	}
	errObj := decodeError(t, rec)
	if errObj["code"] != "invalid_request" {
		t.Fatalf("code = %v，期望 invalid_request", errObj["code"])
	}
	if errObj["param"] != http.MethodPost {
		t.Fatalf("param = %v，期望 %s", errObj["param"], http.MethodPost)
	}
}

func TestContractTrustedProxyIgnoresForgedXFF(t *testing.T) {
	// 未声明 behind_proxy：即使对端是回环、XFF 写了一堆，也必须用直连地址。
	cfg := config.Default()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	if ip := ClientIPFromContext(chainContext(t, cfg, req)); ip != "127.0.0.1" {
		t.Fatalf("未启用可信代理时客户端 IP = %q，期望直连地址 127.0.0.1", ip)
	}
}

func TestContractTrustedProxyUsesRightmostUntrusted(t *testing.T) {
	cfg := config.Default()
	cfg.Server.BehindProxy = true
	cfg.Security.TrustedProxies = []string{"127.0.0.1", "10.0.0.0/8"}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	// 客户端自己伪造了最左边的 203.0.113.7；真正的客户端是 192.0.2.9。
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 192.0.2.9, 10.0.0.5")

	if ip := ClientIPFromContext(chainContext(t, cfg, req)); ip != "192.0.2.9" {
		t.Fatalf("客户端 IP = %q，期望从右往左第一个不可信地址 192.0.2.9", ip)
	}
}

func TestContractErrorCodeTypeMappingMatchesDocs(t *testing.T) {
	// 这份表是 docs/api/03-errors.md 的最小可执行副本。
	// 新增错误码却忘了登记时，这里会立刻失败——而不是等到线上出现一个 type=internal_error
	// 的 404。
	cases := map[string]string{
		"invalid_api_key":      "authentication_error",
		"invalid_credentials":  "authentication_error",
		"session_expired":      "authentication_error",
		"invalid_request":      "invalid_request_error",
		"request_too_large":    "invalid_request_error",
		"forbidden":            "permission_error",
		"model_not_found":      "not_found_error",
		"model_retired":        "not_found_error",
		"not_found":            "not_found_error",
		"conflict":             "conflict_error",
		"rate_limited":         "rate_limit_error",
		"quota_exceeded":       "rate_limit_error",
		"pool_exhausted":       "rate_limit_error",
		"too_many_attempts":    "rate_limit_error",
		"upstream_unavailable": "service_unavailable_error",
		"upstream_timeout":     "service_unavailable_error",
		"no_candidate":         "service_unavailable_error",
		"overloaded":           "service_unavailable_error",
		"internal_error":       "internal_error",
	}
	for code, want := range cases {
		if got := string(TypeForCode(code)); got != want {
			t.Errorf("TypeForCode(%q) = %q，期望 %q", code, got, want)
		}
	}
	if got := string(TypeForCode("完全不存在的码")); got != "internal_error" {
		t.Errorf("未登记的码应退化为 internal_error，得到 %q", got)
	}
}
