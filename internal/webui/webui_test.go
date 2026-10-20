package webui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestContractPlaceholderServesPage 固化「未构建形态」的对外行为：
// 任意路径都返回 200 + text/html + 占位文案（而不是 404 或 500），
// Built() 报告 false。!webui_dist 的默认构建下恒成立；带 tag 构建时跳过。
func TestContractPlaceholderServesPage(t *testing.T) {
	if built {
		t.Skip("webui_dist 生效时本包服务的是真实产物，跳过占位断言")
	}
	for _, path := range []string{"/", "/console/", "/assets/x.js", "/login"} {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: 期望 200，实际 %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: Content-Type=%q，应为 text/html", path, ct)
		}
		if !strings.Contains(rec.Body.String(), "前端未构建") {
			t.Fatalf("%s: 响应不含占位文案", path)
		}
	}
	if Built() {
		t.Fatal("未构建形态下 Built() 应为 false")
	}
}
