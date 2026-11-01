package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文件锁定 Files 与 Search & Fetch 的 HTTP 面（口径见 v1passthrough.go 文件头）：
// 网关只做鉴权 → 选上游 → 原样搬运。所以测试全部围绕「不改写」：
// 入站路径/方法/查询串整条传到装配层，上游响应的状态码/头/体整条回给客户端。

// fakePassthrough 记录收到的透传入参并按脚本回放响应。
type fakePassthrough struct {
	got    []PassthroughInput
	status int
	header http.Header
	body   string
	// err 非空时直接返回错误（验证失败映射）。
	err error
}

func (f *fakePassthrough) Passthrough(ctx context.Context, in PassthroughInput) (*PassthroughResponse, error) {
	f.got = append(f.got, in)
	if f.err != nil {
		return nil, f.err
	}
	h := f.header
	if h == nil {
		h = http.Header{}
	}
	st := f.status
	if st == 0 {
		st = http.StatusOK
	}
	return &PassthroughResponse{
		Status: st,
		Header: h,
		Body:   io.NopCloser(strings.NewReader(f.body)),
	}, nil
}

// TestPassthroughFileUploadForwardsMultipart：上传的文件体原样送出去，网关不解析。
func TestPassthroughFileUploadForwardsMultipart(t *testing.T) {
	const multipartBody = "--BOUNDARY\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.png\"\r\n\r\nPNGDATA\r\n--BOUNDARY--\r\n"
	f := &fakePassthrough{body: `{"id":"file-api-1","object":"file","purpose":"user_data"}`}
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	s.SetPassthrough(f)

	req := v1Request("POST", "/files", multipartBody)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=BOUNDARY")
	rec := httptest.NewRecorder()
	s.HandleV1FileUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if len(f.got) != 1 {
		t.Fatalf("透传调用次数 = %d，期望 1", len(f.got))
	}
	in := f.got[0]
	if in.Endpoint != "/files" {
		t.Errorf("上游路径 = %q，期望 /files（官方路径不带 /v1）", in.Endpoint)
	}
	if in.Method != http.MethodPost {
		t.Errorf("方法 = %q，期望 POST", in.Method)
	}
	// multipart 的 Content-Type（含 boundary）必须原样带出，否则上游解析不了。
	if ct := in.Header.Get("Content-Type"); !strings.Contains(ct, "boundary=BOUNDARY") {
		t.Errorf("Content-Type 未原样带出：%q", ct)
	}
	// 响应原样：Object/ID 是上游字段，网关不该增删。
	if !strings.Contains(rec.Body.String(), "file-api-1") {
		t.Errorf("响应体被改写：%s", rec.Body.String())
	}
}

// TestPassthroughFileRetrieveKeepsID：GET /files/{id} 的 id 原样拼到上游路径。
func TestPassthroughFileRetrieveKeepsID(t *testing.T) {
	f := &fakePassthrough{body: `{"id":"file-api-abc","object":"file"}`}
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	s.SetPassthrough(f)

	rec := httptest.NewRecorder()
	s.HandleV1FileRetrieve(rec, v1Request("GET", "/files/file-api-abc", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if len(f.got) != 1 || f.got[0].Endpoint != "/files/file-api-abc" {
		t.Fatalf("上游路径 = %+v，期望 /files/file-api-abc", f.got)
	}
}

// TestPassthroughFileDelete：DELETE /files/{id} 方法与路径都对。
func TestPassthroughFileDelete(t *testing.T) {
	f := &fakePassthrough{body: `{"id":"file-api-abc","deleted":true}`}
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	s.SetPassthrough(f)

	rec := httptest.NewRecorder()
	s.HandleV1FileDelete(rec, v1Request("DELETE", "/files/file-api-abc", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if len(f.got) != 1 {
		t.Fatalf("透传调用次数 = %d", len(f.got))
	}
	if f.got[0].Method != http.MethodDelete || f.got[0].Endpoint != "/files/file-api-abc" {
		t.Errorf("方法/路径错位：%q %q", f.got[0].Method, f.got[0].Endpoint)
	}
}

// TestPassthroughSearchForwardsQuery：搜索的查询参数原样带出，网关不改写查询。
func TestPassthroughSearchForwardsQuery(t *testing.T) {
	f := &fakePassthrough{body: `{"results":[{"url":"https://example.com","rerank_score":2.1}]}`}
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	s.SetPassthrough(f)

	body := `{"query":"site:example.com \"quoted phrase\" -site:spam.com","count":5,"need_content":true}`
	req := v1Request("POST", "/v1/tools/search?trace=1", body)
	req.URL.RawQuery = "trace=1"
	rec := httptest.NewRecorder()
	s.HandleV1Search(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	in := f.got[0]
	if in.Endpoint != "/v1/tools/search" {
		t.Errorf("上游路径 = %q", in.Endpoint)
	}
	if in.RawQuery != "trace=1" {
		t.Errorf("查询串 = %q，期望原样 trace=1", in.RawQuery)
	}
	// 响应原样：rerank_score 是上游的排序分，网关不该丢弃或重排。
	if !strings.Contains(rec.Body.String(), "rerank_score") {
		t.Errorf("响应体被改写：%s", rec.Body.String())
	}
}

// TestPassthroughFetch：抓取端点映射到 /v1/tools/fetch。
func TestPassthroughFetch(t *testing.T) {
	f := &fakePassthrough{body: `{"content":"# Title\n\nbody text"}`}
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	s.SetPassthrough(f)

	rec := httptest.NewRecorder()
	s.HandleV1Fetch(rec, v1Request("POST", "/v1/tools/fetch", `{"url":"https://example.com"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if len(f.got) != 1 || f.got[0].Endpoint != "/v1/tools/fetch" {
		t.Fatalf("上游路径 = %+v", f.got)
	}
}

// TestPassthroughKeepsUpstreamErrorStatus：上游错误状态与错误体原样回给客户端。
//
// 为什么必须这样：file_not_found / 参数错误 / 额度不足都是**上游 API 契约**
// 的一部分，客户端要按上游的 code 分支处理。网关重新分类等于替上游翻译错误，
// 会让客户端的重试/提示逻辑全部失效。
func TestPassthroughKeepsUpstreamErrorStatus(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	f := &fakePassthrough{status: http.StatusNotFound, header: h,
		body: `{"error":{"message":"file not found","code":"file_not_found"}}`}
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	s.SetPassthrough(f)

	rec := httptest.NewRecorder()
	s.HandleV1FileRetrieve(rec, v1Request("GET", "/files/nope", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望原样 404", rec.Code)
	}
	var probe struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
		t.Fatalf("响应体不是上游原文：%v（%s）", err, rec.Body.String())
	}
	if probe.Error.Code != "file_not_found" {
		t.Errorf("上游错误码被改写：%q（body=%s）", probe.Error.Code, rec.Body.String())
	}
}

// TestPassthroughUnauthenticated：未鉴权 → 401（不透传到上游）。
func TestPassthroughUnauthenticated(t *testing.T) {
	f := &fakePassthrough{body: `{}`}
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	s.SetPassthrough(f)

	r := httptest.NewRequest("GET", "/files", nil)
	rec := httptest.NewRecorder()
	s.HandleV1FileList(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", rec.Code)
	}
	if len(f.got) != 0 {
		t.Errorf("未鉴权不该走到透传：调用 %d 次", len(f.got))
	}
}

// TestPassthroughNotConfigured：未装配透传面 → 501 not_implemented。
func TestPassthroughNotConfigured(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	rec := httptest.NewRecorder()
	s.HandleV1FileList(rec, v1Request("GET", "/files", ""))
	// 503 no_candidate：不新造错误码（码表是客户端 switch 的契约），
	// 语义上「现在不行、稍后重试」正好描述「部署还没接上这个能力」。
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 503（能力未接上）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no_candidate") {
		t.Errorf("错误码应为 no_candidate；body=%s", rec.Body.String())
	}
}

// TestPathTail：路径尾段提取（含空路径与无斜杠）。
func TestPathTail(t *testing.T) {
	cases := map[string]string{
		"/files/file-api-1": "file-api-1",
		"/files/":           "",
		"file-api-1":        "file-api-1",
		"":                  "",
	}
	for in, want := range cases {
		if got := pathTail(in); got != want {
			t.Errorf("pathTail(%q) = %q，期望 %q", in, got, want)
		}
	}
}
