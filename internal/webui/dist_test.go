//go:build webui_dist

package webui

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestContractDistServesIndex 固化「构建形态」的对外行为：
//   - / 返回 200 + index.html（真实产物，不是占位页）；
//   - HTML 引用的相对路径资源（vite base:'./' 产物形态 ./assets/xxx）
//     全部可从同一 Handler 解析——这是「挂在任意 URL 前缀都成立」的客户端侧前提。
//
// 与 placeholder 测试互补：两个形态各测各的契约，构建方用
// `go test -tags webui_dist ./internal/webui/` 验证嵌入链路完整。
func TestContractDistServesIndex(t *testing.T) {
	if !Built() {
		t.Fatal("webui_dist 形态下 Built() 应为 true")
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("/ 期望 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "前端未构建") {
		t.Fatal("构建形态不应返回占位页——dist 嵌入出了问题")
	}
	if !strings.Contains(body, `<div id="root">`) {
		t.Fatal("响应不是 vite 产物 index.html")
	}

	// 解析 ./assets/… 引用并逐个取回
	re := regexp.MustCompile(`\./assets/[\w.-]+`)
	refs := re.FindAllString(body, -1)
	if len(refs) == 0 {
		t.Fatal("index.html 未引用任何 ./assets/ 资源，检查 vite base 配置")
	}
	for _, ref := range refs {
		r2 := httptest.NewRecorder()
		Handler().ServeHTTP(r2, httptest.NewRequest("GET", "/"+ref, nil))
		if r2.Code != 200 {
			t.Fatalf("资源 %s 期望 200，实际 %d（相对路径解析失败）", ref, r2.Code)
		}
	}
}

// TestContractDistCachePolicy 固化分路径的缓存策略。
//
// 这条契约的价值不在于「头对不对」，而在于它挡住的那类事故：
//   - index.html 被缓存 → 浏览器拿旧清单去找已被新构建删掉的 chunk，
//     表现为白屏或「页面永远是旧版」（2026-10-25 实际踩到过）；
//   - 带 hash 的 assets 不给长缓存 → 每次导航都发条件请求，白吃一次往返。
func TestContractDistCachePolicy(t *testing.T) {
	// 根路径与 .html：no-store（清单文件不许缓存）
	for _, path := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s 的 Cache-Control = %q，期望 no-store", path, got)
		}
	}

	// 带内容 hash 的产物：长缓存 + immutable
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	refs := regexp.MustCompile(`\./assets/[\w.-]+`).FindAllString(rec.Body.String(), -1)
	if len(refs) == 0 {
		t.Fatal("index.html 未引用任何 ./assets/ 资源")
	}
	for _, ref := range refs {
		r2 := httptest.NewRecorder()
		Handler().ServeHTTP(r2, httptest.NewRequest("GET", "/"+ref, nil))
		got := r2.Header().Get("Cache-Control")
		if !strings.Contains(got, "immutable") || !strings.Contains(got, "max-age=") {
			t.Errorf("%s 的 Cache-Control = %q，期望 public, max-age=…, immutable", ref, got)
		}
	}
}
