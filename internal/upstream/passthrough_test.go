package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件锁定「原样转发」的三条硬约束（2026-11-02 用户口径：
// 直接把请求对接到供应商，网关不做处理）：
//   1. 请求体、查询串、客户端头（除鉴权/逐跳头）原样到达上游；
//   2. 鉴权头被换成网关持有的上游凭据（否则上游 401）；
//   3. 上游响应（状态码、头、体）原样返回，网关不重新分类。

// passthroughTarget 构造一个 selfhosted 上游（带可解凭据）。
func passthroughTarget(baseURL, secretRef string) Target {
	return Target{
		UpstreamID: "u-pt",
		Kind:       KindSelfHosted,
		BaseURL:    baseURL,
		SecretRef:  secretRef,
		// 透传不涉及模型；ValidateTarget 不要求它非空（见用例）。
		RealModel: "",
	}
}

// TestPassthroughForwardsRawRequest：请求形状原样到达上游。
func TestPassthroughForwardsRawRequest(t *testing.T) {
	const body = "query=%E4%B8%AD%E6%96%87&count=5"
	const wantQuery = "debug=1&x=%E4%B8%AD"
	var gotPath, gotQuery, gotBody, gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Client-Trace")
		w.Header().Set("X-Upstream-Marker", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"file-api-1"}`))
	}))
	defer srv.Close()

	tr := NewHTTPTransport(nil, func(ctx context.Context, ref string) (string, error) {
		return "sk-upstream", nil
	})
	header := http.Header{}
	header.Set("X-Client-Trace", "abc")
	header.Set("Authorization", "Bearer fr-client-key") // 必须被替换掉
	header.Set("Connection", "keep-alive")              // 逐跳头必须剔除

	res, err := tr.Passthrough(testCtx, passthroughTarget(srv.URL, "secret-1"), PassthroughRequest{
		Method:        http.MethodPost,
		Path:          "/v1/tools/search",
		RawQuery:      wantQuery,
		Header:        header,
		Body:          strings.NewReader(body),
		ContentLength: int64(len(body)),
	})
	if err != nil {
		t.Fatalf("透传失败: %v", err)
	}
	defer res.Body.Close()

	if gotPath != "/v1/tools/search" {
		t.Errorf("上游路径 = %q，期望原样 /v1/tools/search", gotPath)
	}
	if gotQuery != wantQuery {
		t.Errorf("上游查询串 = %q，期望原样 %q", gotQuery, wantQuery)
	}
	if gotBody != body {
		t.Errorf("上游请求体 = %q，期望原样 %q（网关不该改写搜索参数）", gotBody, body)
	}
	if gotAuth != "Bearer sk-upstream" {
		t.Errorf("鉴权头 = %q，期望被换成上游凭据（保留客户端 fr- Key 会让上游 401）", gotAuth)
	}
	if gotCustom != "abc" {
		t.Errorf("客户端自定义头丢失：X-Client-Trace = %q", gotCustom)
	}

	// 响应原样：状态码 201 与上游头都不该被网关改写或归类。
	if res.Status != http.StatusCreated {
		t.Errorf("响应状态 = %d，期望原样 201（网关不重新分类）", res.Status)
	}
	if res.Header.Get("X-Upstream-Marker") != "yes" {
		t.Errorf("上游响应头丢失")
	}
	rb, _ := io.ReadAll(res.Body)
	if string(rb) != `{"id":"file-api-1"}` {
		t.Errorf("响应体 = %q，期望原样", string(rb))
	}
}

// TestPassthroughKeepsErrorStatus：上游 4xx/5xx 原样返回，不被改写成我们的错误体。
func TestPassthroughKeepsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"file not found","code":"file_not_found"}}`))
	}))
	defer srv.Close()

	tr := NewHTTPTransport(nil, func(ctx context.Context, ref string) (string, error) { return "k", nil })
	res, err := tr.Passthrough(testCtx, passthroughTarget(srv.URL, "s"), PassthroughRequest{
		Method: http.MethodGet, Path: "/files/file-api-x",
	})
	if err != nil {
		t.Fatalf("透传失败（4xx 不该被当成传输错误）: %v", err)
	}
	defer res.Body.Close()
	if res.Status != http.StatusNotFound {
		t.Errorf("状态 = %d，期望原样 404", res.Status)
	}
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "file_not_found") {
		t.Errorf("上游错误体被改写：%s", string(b))
	}
}

// TestPassthroughNoAuthForSelfHosted：无凭据上游不发 Authorization 头。
//
// 为什么要测：发一个空 "Bearer " 会被多数端点 401，而"本机 ollama 无鉴权"
// 是完全合法的部署形态（03-upstream.md §4）。
func TestPassthroughNoAuthForSelfHosted(t *testing.T) {
	var gotAuth string
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, sawHeader = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tr := NewHTTPTransport(nil, func(ctx context.Context, ref string) (string, error) { return "", nil })
	res, err := tr.Passthrough(testCtx, passthroughTarget(srv.URL, ""), PassthroughRequest{
		Method: http.MethodGet, Path: "/files",
	})
	if err != nil {
		t.Fatalf("透传失败: %v", err)
	}
	defer res.Body.Close()
	if sawHeader || gotAuth != "" {
		t.Errorf("无凭据上游不该带 Authorization 头，得到 %q", gotAuth)
	}
}

// TestPassthroughRejectsRelay：relay 类上游不可透传（它没有可直连地址）。
func TestPassthroughRejectsRelay(t *testing.T) {
	tr := NewHTTPTransport(nil, nil)
	_, err := tr.Passthrough(testCtx, Target{
		UpstreamID: "u-relay",
		Kind:       KindRelay,
		BaseURL:    "", // relay 红线：地址恒为空
	}, PassthroughRequest{Method: http.MethodGet, Path: "/files"})
	if err == nil {
		t.Fatalf("relay 上游应拒绝透传（没有可直连地址）")
	}
	if !strings.Contains(err.Error(), "relay") {
		t.Errorf("错误信息应说明 relay 不支持，得到 %v", err)
	}
}

// TestCopyHeaderDropsHopHeaders：逐跳头不转发到客户端。
func TestCopyHeaderDropsHopHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("Transfer-Encoding", "chunked")
	src.Set("Connection", "keep-alive")
	src.Set("X-Keep", "1")
	dst := http.Header{}
	CopyHeader(dst, src)
	if dst.Get("Content-Type") != "application/json" || dst.Get("X-Keep") != "1" {
		t.Errorf("普通头应保留：%v", dst)
	}
	if dst.Get("Transfer-Encoding") != "" || dst.Get("Connection") != "" {
		t.Errorf("逐跳头应剔除：%v", dst)
	}
}
