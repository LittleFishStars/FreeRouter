package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// SetWebUI 分流规则的契约测试（M9 装配）。
//
// 为什么值得单列一组断言：管理面/入站面的权限口径都建立在
// 「404 不可区分」这个安全属性上（docs/api/02-admin.md §2.2：不确认路径
// 是否存在）。前端兜底处理器一旦挂上，最容易发生的破坏就是
// 「API 路径的 404 变成了 HTML 页面」——调用方会以为端点存在、
// 只是响应格式坏了。这组测试钉的是那条线。

// stubWebUI 是前端处理器的替身：任何请求都回一段可识别的 HTML。
type stubWebUI struct{ calls int }

func (s *stubWebUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls++
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<!doctype html><title>spa</title>"))
}

func newServerWithWebUI(t *testing.T, ui http.Handler) (*Server, *httptest.ResponseRecorder) {
	t.Helper()
	cfg := config.Default()
	srv := New(Deps{Config: cfg, Logger: discardLogger()})
	if ui != nil {
		srv.SetWebUI(ui, true)
	}
	return srv, httptest.NewRecorder()
}

func doReq(srv *Server, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// 1. 非 API 路径交给前端；/api/* 与 /v1/* 仍是 JSON 404。
func TestContractWebUIOnlyServesNonAPIPaths(t *testing.T) {
	ui := &stubWebUI{}
	srv, _ := newServerWithWebUI(t, ui)

	// SPA 入口路径（前端路由表里的真实路径）必须拿到页面。
	for _, p := range []string{"/", "/login", "/admin/accounts", "/contribute/recovery"} {
		rec := doReq(srv, http.MethodGet, p)
		if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("<title>spa</title>")) {
			t.Fatalf("%s 应交给前端处理，得 %d %q", p, rec.Code, rec.Body.String())
		}
	}

	// API 面：未注册的 /api/*、/v1/* 必须是 JSON 404，不能落到 SPA。
	for _, p := range []string{"/api/nope", "/api/admin/nope", "/v1/nope", "/v1/chat/nope"} {
		rec := doReq(srv, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 状态码 = %d，期望 404（body=%s）", p, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !stringsHas(ct, "application/json") {
			t.Fatalf("%s Content-Type = %q，期望 JSON", p, ct)
		}
		if bytes.Contains(rec.Body.Bytes(), []byte("<title>spa</title>")) {
			t.Fatalf("%s 落到了 SPA：API 面 404 被前端吞掉", p)
		}
	}
}

// 2. 前缀边界：/api 与 /v1 本身（无尾斜杠）也不能落到 SPA。
//
// 为什么单列：strings.HasPrefix(path, "/api/") 是常见写法，但它漏掉
// 恰好等于 "/api" 的请求——那条会被当成 SPA 路径返回 index.html。
func TestContractWebUIPrefixBoundaryIncludesBareSegment(t *testing.T) {
	ui := &stubWebUI{}
	srv, _ := newServerWithWebUI(t, ui)

	for _, p := range []string{"/api", "/v1"} {
		rec := doReq(srv, http.MethodGet, p)
		if bytes.Contains(rec.Body.Bytes(), []byte("<title>spa</title>")) {
			t.Fatalf("%s 落到了 SPA（裸前缀段未走 JSON 404）", p)
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 状态码 = %d，期望 404", p, rec.Code)
		}
	}
	// 反面对照：/apifoo 不是 API 路径，应当给 SPA。
	rec := doReq(srv, http.MethodGet, "/apifoo")
	if !bytes.Contains(rec.Body.Bytes(), []byte("<title>spa</title>")) {
		t.Fatal("/apifoo 应视为前端路径")
	}
}

// 3. 【安全属性】注入前端前后的 /api/* 404 响应必须逐字节一致。
//
// 若挂上前端改变了 API 404 的形状（哪怕只是多一个头），探测者就能用
// 差异判断「这条路径注册了吗」——那正是 §2.2 要防的事。
func TestContractWebUIDoesNotChangeAPI404Bytes(t *testing.T) {
	srvNoUI, _ := newServerWithWebUI(t, nil)
	srvUI, _ := newServerWithWebUI(t, &stubWebUI{})

	paths := []string{"/api/nope", "/api/admin/nope", "/v1/nope"}
	for _, p := range paths {
		a, b := doReq(srvNoUI, http.MethodGet, p), doReq(srvUI, http.MethodGet, p)
		if a.Code != b.Code {
			t.Fatalf("%s 状态码不一致：%d vs %d", p, a.Code, b.Code)
		}
		if !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
			t.Fatalf("%s 响应体不一致：\n%s\n%s", p, a.Body.String(), b.Body.String())
		}
		// 头也必须一致（多一个头同样可用于指纹识别），但排除**设计上
		// 每请求就不同**的几个：请求 ID 是随机串、Content-Length 随
		// 响应体走。把它们算进来会得到一个恒红的测试，那等于没有测试。
		for k, va := range a.Header() {
			if perRequestHeaders[k] || k == "Content-Length" {
				continue
			}
			vb := b.Header().Get(k)
			if len(va) > 0 && va[0] != vb {
				t.Fatalf("%s 响应头 %s 不一致：%q vs %q", p, k, va[0], vb)
			}
		}
	}
}

// 4. 已注册端点不被前端兜底影响（兜底只在 NotFound 上生效）。
func TestContractWebUIDoesNotShadowRegisteredRoutes(t *testing.T) {
	ui := &stubWebUI{}
	srv, _ := newServerWithWebUI(t, ui)

	rec := doReq(srv, http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"ok"`)) {
		t.Fatalf("/healthz 被前端兜底影响：%d %s", rec.Code, rec.Body.String())
	}
	if ui.calls != 0 {
		t.Fatalf("探针请求不该触达前端处理器（calls=%d）", ui.calls)
	}

	// /api/version 同理。
	rec = doReq(srv, http.MethodGet, "/api/version")
	if !bytes.Contains(rec.Body.Bytes(), []byte("freerouter")) {
		t.Fatalf("/api/version 未命中处理器：%s", rec.Body.String())
	}
	if ui.calls != 0 {
		t.Fatalf("已注册路由不应触达前端处理器（calls=%d）", ui.calls)
	}
}

// 5. 未注入前端时行为回退到 JSON 404（裸 go build 的部署态可预期）。
func TestContractWebUIAbsentFallsBackToJSON404(t *testing.T) {
	srv, _ := newServerWithWebUI(t, nil)
	rec := doReq(srv, http.MethodGet, "/login")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未注入前端时 /login 应 404，得 %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("not_found")) {
		t.Fatalf("未注入前端时的 404 必须是 JSON 控制台码：%s", rec.Body.String())
	}
}

// perRequestHeaders 是设计上每请求必然不同的头，不参与一致性比对。
var perRequestHeaders = map[string]bool{
	"X-Request-Id": true, // mwRequestID 生成随机 ID
}

func stringsHas(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
