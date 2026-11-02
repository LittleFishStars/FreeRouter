package httpapi

// settle_contract_test.go —— M1「终态结算整链」的契约测试（httpapi 侧）。
//
// 断言对象是**可观察的结算契约**，不是实现细节：
//   - 一次请求恰一次结算（defer 唯一点；换候选不得重复）；
//   - 成功/失败/404/503 的 Status 与 HTTPStatus/ErrorCode 对应 03-errors.md；
//   - usage 与 TTFT 从事件流真实回传（非流式 TTFT=-1）；
//   - 客户端断开（ctx 已取消）时结算仍发生，且结算 ctx 本身未被取消。
//
// 为什么用 fakeSettler 而不是真 store：本测试守的是 httpapi 侧的接线语义
// （调几次、带什么），桥的落库语义由 app 侧负责；把临时 SQLite 拖进来
// 只会让失败原因难以定位。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// openAIStreamWithUsage 是带终态 usage 的流式样本（usage 在
// choices:[] 的尾帧里，stream_options.include_usage 的线上形状）。
// 不复用 openAIStreamOK：那条样本刻意不含 usage，改它会动到既有契约测试。
const openAIStreamWithUsage = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]
`

// fakeSettler 记录每次 Settle 的输入，并可断言 ctx 是否被取消。
type fakeSettler struct {
	mu     sync.Mutex
	got    []SettleInput
	ctxErr []error // 每次调用时 ctx.Err() 的快照（nil = 未取消）
}

func (f *fakeSettler) Settle(ctx context.Context, in SettleInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, in)
	f.ctxErr = append(f.ctxErr, ctx.Err())
	return nil
}

func (f *fakeSettler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *fakeSettler) only(t *testing.T) SettleInput {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) != 1 {
		t.Fatalf("结算次数 = %d，期望恰 1 次（一次请求恰一次结算）", len(f.got))
	}
	return f.got[0]
}

// withKeyPrincipal 往请求里注入生产链同款的 Key 鉴权主体。
// 直接构造 ctxKeyKeyPrincipal 而不是走 mwKeyAuth：本测试不关心鉴权判定，
// 只关心结算能否拿到归属。
func withKeyPrincipal(r *http.Request, accountID, keyID string) *http.Request {
	p := &auth.KeyPrincipal{AccountID: accountID, KeyID: keyID}
	return r.WithContext(context.WithValue(r.Context(), ctxKeyKeyPrincipal, p))
}

// TestContractSettleOnceOnNonStreamSuccess：非流式成功 → 恰一次结算，
// Status=ok / 200 / 无错误码 / usage 回传 / TTFT=-1（非流式无样本）。
func TestContractSettleOnceOnNonStreamSuccess(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Response: openAIResponseOK}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs)
	fs := &fakeSettler{}
	s.SetSettler(fs)

	req := withKeyPrincipal(v1Request("POST", "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"你好"}]}`), "42", "7")
	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	in := fs.only(t)
	if in.Status != "ok" || in.HTTPStatus != 200 || in.ErrorCode != "" {
		t.Fatalf("终态 = %q/%d/%q，期望 ok/200/空", in.Status, in.HTTPStatus, in.ErrorCode)
	}
	if in.AccountID != "42" || in.KeyID != "7" {
		t.Fatalf("归属 = acc %q key %q，期望 42/7（C4 归因链）", in.AccountID, in.KeyID)
	}
	if in.Endpoint != "openai-chat" {
		t.Fatalf("endpoint = %q，期望 openai-chat（requests 表 CHECK 词表）", in.Endpoint)
	}
	if in.Usage == nil || in.Usage.InputTokens == nil || *in.Usage.InputTokens != 10 {
		t.Fatalf("usage 未回传或错误：%+v（上游报 input=10）", in.Usage)
	}
	if in.TTFTMs != -1 {
		t.Fatalf("非流式 TTFT = %d，期望 -1（无样本不编造）", in.TTFTMs)
	}
	if in.TotalMs < 0 {
		t.Fatalf("TotalMs = %d，期望 >=0", in.TotalMs)
	}
}

// TestContractSettleStreamCarriesUsageAndTTFT：流式成功 → usage 从事件流
// 回传、TTFT 是首段可见内容耗时（>=0）。
func TestContractSettleStreamCarriesUsageAndTTFT(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Stream: openAIStreamWithUsage}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs)
	fs := &fakeSettler{}
	s.SetSettler(fs)

	req := withKeyPrincipal(v1Request("POST", "/v1/chat/completions",
		`{"model":"smart","stream":true,"messages":[{"role":"user","content":"你好"}]}`), "42", "7")
	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, req)

	in := fs.only(t)
	if in.Status != "ok" {
		t.Fatalf("Status = %q，期望 ok", in.Status)
	}
	if in.Usage == nil {
		t.Fatalf("流式 usage 未回传（I4：事件里恰一次，循环里必须留）")
	}
	if in.TTFTMs < 0 {
		t.Fatalf("流式 TTFT = %d，期望 >=0（首段可见内容耗时）", in.TTFTMs)
	}
}

// TestContractSettleOnceOnCandidateSwitch：首个候选失败、第二个成功 →
// 仍只结算一次，且 usage/RealModelID 取成功候选的那次（换路不重复记账）。
func TestContractSettleOnceOnCandidateSwitch(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{
		{Err: errors.New("连接被拒")},    // 候选 1：连接层失败
		{Response: openAIResponseOK}, // 候选 2：成功
	}}
	cand1 := chatCandidate("deepseek-chat")
	cand1.ID = "11"
	cand2 := chatCandidate("qwen-max")
	cand2.ID = "22"
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {cand1, cand2},
	}}
	s := newV1Server(t, up, cs)
	fs := &fakeSettler{}
	s.SetSettler(fs)

	req := withKeyPrincipal(v1Request("POST", "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"你好"}]}`), "42", "7")
	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, req)

	in := fs.only(t)
	if in.Status != "ok" {
		t.Fatalf("Status = %q，期望 ok（第二候选成功）", in.Status)
	}
	if in.RealModelID != "22" {
		t.Fatalf("RealModelID = %s，期望 22（成功候选，不是失败候选 11）", in.RealModelID)
	}
	if in.RetryCount != 1 {
		t.Fatalf("RetryCount = %d，期望 1（换过一次候选）", in.RetryCount)
	}
}

// TestContractSettleFailureMarks502：全部候选失败 → Status=error、
// 502 upstream_unavailable、Usage=nil（失败尝试不上报用量）。
func TestContractSettleFailureMarks502(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{
		{Err: errors.New("连接被拒")},
		{Err: errors.New("连接被拒")},
	}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("a"), chatCandidate("b")},
	}}
	s := newV1Server(t, up, cs)
	fs := &fakeSettler{}
	s.SetSettler(fs)

	req := withKeyPrincipal(v1Request("POST", "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`), "42", "7")
	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, req)

	in := fs.only(t)
	if in.Status != "error" || in.HTTPStatus != 502 || in.ErrorCode != "upstream_unavailable" {
		t.Fatalf("终态 = %q/%d/%q，期望 error/502/upstream_unavailable",
			in.Status, in.HTTPStatus, in.ErrorCode)
	}
	if in.Usage != nil {
		t.Fatalf("失败尝试不应携带 usage：%+v", in.Usage)
	}
}

// TestContractSettleNoCandidateMarks503：无候选 → Status=error、503。
func TestContractSettleNoCandidateMarks503(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	fs := &fakeSettler{}
	s.SetSettler(fs)

	req := withKeyPrincipal(v1Request("POST", "/v1/chat/completions",
		`{"model":"missing","messages":[{"role":"user","content":"hi"}]}`), "42", "7")
	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, req)

	in := fs.only(t)
	if in.Status != "error" || in.HTTPStatus != 503 || in.ErrorCode != "no_candidate" {
		t.Fatalf("终态 = %q/%d/%q，期望 error/503/no_candidate",
			in.Status, in.HTTPStatus, in.ErrorCode)
	}
}

// TestContractSettleModelNotFoundMarks404：模型不可见 → 404 model_not_found。
func TestContractSettleModelNotFoundMarks404(t *testing.T) {
	cs := &fakeCandidates{notFound: true}
	s := newV1Server(t, &fakeUpstream{}, cs)
	fs := &fakeSettler{}
	s.SetSettler(fs)

	req := withKeyPrincipal(v1Request("POST", "/v1/chat/completions",
		`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`), "42", "7")
	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, req)

	in := fs.only(t)
	if in.Status != "error" || in.HTTPStatus != 404 || in.ErrorCode != "model_not_found" {
		t.Fatalf("终态 = %q/%d/%q，期望 error/404/model_not_found",
			in.Status, in.HTTPStatus, in.ErrorCode)
	}
}

// TestContractSettleDetachedContextSurvivesCancel：客户端断开（ctx 已取消）
// 时结算仍发生，且结算用的 ctx 未被取消——否则写库第一步就失败、账丢失。
func TestContractSettleDetachedContextSurvivesCancel(t *testing.T) {
	s := newV1Server(t, &fakeUpstream{}, &fakeCandidates{models: map[string][]Candidate{}})
	fs := &fakeSettler{}
	s.SetSettler(fs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 模拟客户端已断开

	st := &settleState{obs: attemptObs{TTFTMs: -1}, status: "error", httpStatus: 502, errorCode: "upstream_unavailable"}
	s.settleRequest(ctx, proxyReq{endpoint: "openai-chat"}, "req-cancel", st)

	if fs.count() != 1 {
		t.Fatalf("结算次数 = %d，期望 1（断开也要走到 defer）", fs.count())
	}
	if err := fs.ctxErr[0]; err != nil {
		t.Fatalf("结算 ctx 已取消（%v）——必须用 context.WithoutCancel 脱离取消", err)
	}
}

// TestContractSettleSkippedWithoutInjection：未注入 Settler 时请求照常服务、
// 不 panic（降级态；装配缺失不该变成用户可见故障）。
func TestContractSettleSkippedWithoutInjection(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Response: openAIResponseOK}}}
	cs := &fakeCandidates{models: map[string][]Candidate{
		"smart": {chatCandidate("deepseek-chat")},
	}}
	s := newV1Server(t, up, cs) // 不 SetSettler

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（无 Settler 不影响服务）", rec.Code)
	}
}

// TestContractSettleEstimatesWhenUpstreamSilent 上游不报 usage 时按估算落账。
//
// 2026-11-02「使全部模型都有 token 统计」：多数 adapter 不向
// 上游请求 usage，`requests.in_tokens` 恒为 NULL，仪表盘按 token 聚合的图
// 永远是平的。这里锁住补位行为——但估算值**必须**带 Source=estimated，
// 与上游上报严格分离（02-ir.md §9）。
func TestContractSettleEstimatesWhenUpstreamSilent(t *testing.T) {
	// 假上游返回一条**不含 usage** 的正常响应。
	up := &fakeUpstream{script: []fakeCall{{Response: `{"id":"c1","model":"m","choices":[
		{"index":0,"message":{"role":"assistant","content":"这是模型给出的回答正文，用于估算输出 token。"},
		 "finish_reason":"stop"}]}`}}}
	cs := &fakeCandidates{models: map[string][]Candidate{"smart": {chatCandidate("a")}}}
	s := newV1Server(t, up, cs)
	fs := &fakeSettler{}
	s.SetSettler(fs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"请介绍一下你自己，尽量详细一些。"}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", rec.Code, rec.Body.String())
	}

	in := fs.only(t)
	if in.Status != "ok" {
		t.Fatalf("Status = %q", in.Status)
	}
	if in.Usage == nil {
		t.Fatalf("上游没报 usage 时应回填估算值，否则仪表盘永远没数")
	}
	if in.Usage.Source != ir.UsageEstimated {
		t.Fatalf("Source = %q，估算值必须标记 estimated（不能与上游上报混同）", in.Usage.Source)
	}
	if in.Usage.InputTokens == nil || *in.Usage.InputTokens <= 0 {
		t.Errorf("InputTokens = %v，应为正的估算值", in.Usage.InputTokens)
	}
	if in.Usage.OutputTokens == nil || *in.Usage.OutputTokens <= 0 {
		t.Errorf("OutputTokens = %v，应为正的估算值", in.Usage.OutputTokens)
	}
}

// TestContractSettlePrefersUpstreamUsage 上游报了就以它为准，绝不覆盖成估算。
func TestContractSettlePrefersUpstreamUsage(t *testing.T) {
	up := &fakeUpstream{script: []fakeCall{{Response: `{"id":"c1","model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"回答"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":111,"completion_tokens":22,"total_tokens":133}}`}}}
	cs := &fakeCandidates{models: map[string][]Candidate{"smart": {chatCandidate("a")}}}
	s := newV1Server(t, up, cs)
	fs := &fakeSettler{}
	s.SetSettler(fs)

	rec := httptest.NewRecorder()
	s.HandleV1Chat(rec, v1Request("POST", "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	in := fs.only(t)
	if in.Usage == nil {
		t.Fatalf("usage 丢失")
	}
	if in.Usage.Source == ir.UsageEstimated {
		t.Errorf("上游给了权威数字时不该标 estimated")
	}
	if in.Usage.InputTokens == nil || *in.Usage.InputTokens != 111 {
		t.Errorf("InputTokens = %v，应为上游上报的 111（估算不得覆盖）", in.Usage.InputTokens)
	}
}
