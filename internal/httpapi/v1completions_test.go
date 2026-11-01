package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir/openai/completion"
)

// 本文件锁定 POST /v1/completions（DeepSeek 官方 FIM Completion API，Beta）的 HTTP 契约。
// 为什么要在 HTTP 层再测一遍（IR 层已有解码/编码单测）：端点契约包含
// 「候选方言必须是补全方言」「出站 model 是抽象名」「流式帧形状」三件
// 只在编排路径上才成立的事。

// completionCandidate 是补全方言的测试候选。
func completionCandidate(modelID string) Candidate {
	return Candidate{UpstreamModelID: modelID, Display: "test:" + modelID, Adapter: completion.Adapter()}
}

// TestContractV1CompletionsNonStream：非流式补全返回 choices[].text。
func TestContractV1CompletionsNonStream(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Response: `{
		"id":"cmpl-1","object":"text_completion","created":1,"model":"deepseek-flash",
		"choices":[{"index":0,"text":"    return a+b","finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`}}}
	cs := &fakeCandidates{models: map[string][]Candidate{"smart": {completionCandidate("deepseek-flash")}}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Completions(rec, v1Request("POST", "/v1/completions",
		`{"model":"smart","prompt":"def add(a,b):\n","suffix":"\nadd(1,2)"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Object  string `json:"object"`
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
	}
	if body.Object != "text_completion" {
		t.Errorf("object = %q，期望 text_completion", body.Object)
	}
	if len(body.Choices) == 0 || body.Choices[0].Text != "    return a+b" {
		t.Errorf("choices[0].text 缺失或错误；body=%s", rec.Body.String())
	}
	// 出站 model 必须是抽象名（不透池结构）。
	if !strings.Contains(rec.Body.String(), `"model":"smart"`) {
		t.Errorf("响应 model 应为抽象名 smart；body=%s", rec.Body.String())
	}
}

// TestContractV1CompletionsStream：流式补全返回 text_completion 帧并以 [DONE] 收尾。
func TestContractV1CompletionsStream(t *testing.T) {
	upstreamSSE := "data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"    \"}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"return a+b\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"\",\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	up := &fakeUpstream{script: []fakeCall{{Stream: upstreamSSE}}}
	cs := &fakeCandidates{models: map[string][]Candidate{"smart": {completionCandidate("deepseek-flash")}}}
	s := newV1Server(t, up, cs)

	rec := httptest.NewRecorder()
	s.HandleV1Completions(rec, v1Request("POST", "/v1/completions",
		`{"model":"smart","prompt":"def add(a,b):\n","stream":true}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q，期望 text/event-stream", ct)
	}
	seg := rec.Body.String()
	if !strings.Contains(seg, `"text":"    "`) || !strings.Contains(seg, `"text":"return a+b"`) {
		t.Errorf("流式帧里没有 choices[].text；body=%s", seg)
	}
	if !strings.Contains(seg, "[DONE]") {
		t.Errorf("缺少 [DONE] 终止帧；body=%s", seg)
	}
}

// TestContractV1CompletionsPromptRequired：缺 prompt → 400 且指向 prompt。
func TestContractV1CompletionsPromptRequired(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	rec := httptest.NewRecorder()
	s.HandleV1Completions(rec, v1Request("POST", "/v1/completions", `{"model":"smart"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400（prompt 必填）；body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "prompt") {
		t.Errorf("错误信息应指明 prompt；body=%s", rec.Body.String())
	}
}

// TestContractV1CompletionsUnauthenticated：未鉴权 → 401。
func TestContractV1CompletionsUnauthenticated(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	r := httptest.NewRequest("POST", "/v1/completions", strings.NewReader(`{"model":"smart","prompt":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.HandleV1Completions(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", rec.Code)
	}
}

// TestContractV1CompletionsNoCandidate：无候选 → 503。
func TestContractV1CompletionsNoCandidate(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	rec := httptest.NewRecorder()
	s.HandleV1Completions(rec, v1Request("POST", "/v1/completions", `{"model":"smart","prompt":"x"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 503（无候选）", rec.Code)
	}
}
