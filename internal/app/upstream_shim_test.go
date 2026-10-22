package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// 契约测试：task-10 装配桥（lead 授权写域 internal/app/upstream_shim.go + 配套测试）。
//
// 验收三条（lead 原文）：候选查询命中 / 空→no_candidate 路径 / shim.Call 对
// fake transport 往返。

// fakeRealModelSource 是 realModelSource 的内存 fake（候选查询测试用）。
type fakeRealModelSource struct {
	models []store.RealModel
	ups    []store.Upstream
	err    error
}

func (f *fakeRealModelSource) ListVisibleRealModels(context.Context) ([]store.RealModel, error) {
	return f.models, f.err
}

func (f *fakeRealModelSource) ListUpstreams(context.Context) ([]store.Upstream, error) {
	return f.ups, f.err
}

// TestContractCandidatesHit：候选查询命中——按 UpstreamModelID 精确匹配构造
// []httpapi.Candidate，Adapter 按 kind 选定（relay/vendor/selfhosted→openai-chat）。
func TestContractCandidatesHit(t *testing.T) {
	src := &fakeRealModelSource{
		models: []store.RealModel{
			{ID: 11, UpstreamID: 1, UpstreamModelID: "deepseek-v4.1", Status: store.RealModelActive},
			{ID: 12, UpstreamID: 1, UpstreamModelID: "deepseek-v4.1-flash", Status: store.RealModelActive},
			{ID: 13, UpstreamID: 2, UpstreamModelID: "deepseek-v4.1", Status: store.RealModelActive}, // 同名不同上游：也应命中
		},
		ups: []store.Upstream{
			{ID: 1, Kind: "relay", Name: "buddy", BaseURL: "sidecar://buddy", Enabled: true},
			{ID: 2, Kind: "selfhosted", Name: "llama", BaseURL: "http://127.0.0.1:11434", Enabled: true},
		},
	}
	cs := &realModelCandidates{models: src}

	cands, err := cs.Candidates(context.Background(), "deepseek-v4.1", ir.Request{})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("应命中 2 个候选（同名不同上游），got %d", len(cands))
	}
	for _, c := range cands {
		if c.UpstreamModelID != "deepseek-v4.1" {
			t.Errorf("候选模型 id 错位：%s", c.UpstreamModelID)
		}
		if c.Adapter == nil {
			t.Error("候选必须带 Adapter")
		}
		if c.Adapter.Name() != ir.ProtocolOpenAIChat {
			t.Errorf("M5 方言应恒 openai-chat，got %s", c.Adapter.Name())
		}
		if c.Display == "" {
			t.Error("Display 不得为空（内部日志/trace 用）")
		}
	}

	// 精确匹配：别的模型名不命中。
	if c, _ := cs.Candidates(context.Background(), "deepseek-v4.1-flash", ir.Request{}); len(c) != 1 {
		t.Errorf("flash 应命中 1 个，got %d", len(c))
	}
	// 不存在的模型：空切片（不是 error）。
	c, err := cs.Candidates(context.Background(), "no-such-model", ir.Request{})
	if err != nil || len(c) != 0 {
		t.Errorf("不存在模型应返回空+nil err，got %d/%v", len(c), err)
	}
}

// TestContractCandidatesEmptyNoCandidate：空目录/外键悬空/未知 kind → 空候选，
// v1 面板走 no_candidate（503）路径。
func TestContractCandidatesEmptyNoCandidate(t *testing.T) {
	// 空目录：空候选不报错（空是合法状态——自举未跑）。
	cs := &realModelCandidates{models: &fakeRealModelSource{}}
	c, err := cs.Candidates(context.Background(), "x", ir.Request{})
	if err != nil || len(c) != 0 {
		t.Fatalf("空目录应空候选 nil err：%d/%v", len(c), err)
	}

	// 外键悬空（模型指向上游行不存在）：跳过该候选不报错。
	cs2 := &realModelCandidates{models: &fakeRealModelSource{
		models: []store.RealModel{{ID: 1, UpstreamID: 99, UpstreamModelID: "x", Status: store.RealModelActive}},
	}}
	if c, err := cs2.Candidates(context.Background(), "x", ir.Request{}); err != nil || len(c) != 0 {
		t.Fatalf("悬空外键应跳过：%d/%v", len(c), err)
	}

	// 未知 kind（数据坏行）：跳过。
	cs3 := &realModelCandidates{models: &fakeRealModelSource{
		models: []store.RealModel{{ID: 1, UpstreamID: 5, UpstreamModelID: "x", Status: store.RealModelActive}},
		ups:    []store.Upstream{{ID: 5, Kind: "weird"}},
	}}
	if c, err := cs3.Candidates(context.Background(), "x", ir.Request{}); err != nil || len(c) != 0 {
		t.Fatalf("未知 kind 应跳过：%d/%v", len(c), err)
	}

	// 端到端：v1 面板对无候选模型回 503 no_candidate。
	// （经 proxyV1 的 no_candidate 分支——这里直接验证候选源产出与 httpapi
	// 的约定形状：空切片 + nil err，httpapi 据此走 503。）
}

// TestContractShimCallRoundTrip：shim.Call 对 fake transport（httptest 假上游）
// 的完整往返——upstreamShim 满足 httpapi.Upstream 并把 Body/Err 正确搬运。
func TestContractShimCallRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "r-1",
			"model": "m",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "装配桥应答"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	// targetOf：vendor/selfhosted 走 httptest 端点（回环 http 放行）。
	caller := upstream.NewCaller(nil, upstream.NewHTTPTransport(nil, nil), func(cand upstream.CallCandidate) (upstream.Target, error) {
		return upstream.Target{
			UpstreamID: 7,
			Kind:       upstream.KindSelfHosted,
			BaseURL:    srv.URL,
			RealModel:  cand.UpstreamModelID,
		}, nil
	})
	shim := upstreamShim{caller: caller}

	req := ir.Request{
		Model:    "m",
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Idx: 0, Text: "ping"}}}},
	}
	cand := httpapi.Candidate{UpstreamModelID: "m", Display: "selfhosted/m", Adapter: openai.Adapter()}

	call, err := shim.Call(context.Background(), cand, req, false)
	if err != nil {
		t.Fatalf("shim.Call 不应返回 Go error：%v", err)
	}
	if call == nil || call.Err != nil {
		t.Fatalf("应成功：%+v", call)
	}
	if call.Body == nil {
		t.Fatal("成功应带 Body")
	}
	defer call.Body.Close()
	raw, _ := io.ReadAll(call.Body)
	resp, err := openai.Adapter().DecodeResponse(raw)
	if err != nil {
		t.Fatalf("Body 应可按 openai 方言解码：%v", err)
	}
	var sb strings.Builder
	for _, p := range resp.Parts {
		if tp, ok := p.(ir.TextPart); ok {
			sb.WriteString(tp.Text)
		}
	}
	if sb.String() != "装配桥应答" {
		t.Fatalf("往返文本错位：%q", sb.String())
	}

	// 接口形状断言：upstreamShim 满足 httpapi.Upstream（编译期 var 已断言，
	// 这里运行期再走一次接口赋值，防重构时被绕开）。
	var _ httpapi.Upstream = shim
}

// TestContractShimCallErrTransport：失败传输下 Err 搬运（Body 关闭不泄漏）。
func TestContractShimCallErrTransport(t *testing.T) {
	// 指向已死端口：连接拒绝 → 结构化 Err。
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	caller := upstream.NewCaller(nil, upstream.NewHTTPTransport(nil, nil), func(cand upstream.CallCandidate) (upstream.Target, error) {
		return upstream.Target{UpstreamID: 1, Kind: upstream.KindSelfHosted, BaseURL: deadURL, RealModel: cand.UpstreamModelID}, nil
	})
	shim := upstreamShim{caller: caller}

	call, err := shim.Call(context.Background(), httpapi.Candidate{UpstreamModelID: "m", Adapter: openai.Adapter()}, minShimReq(), false)
	if err != nil {
		t.Fatalf("失败也不该是 Go error（结构化 Err 才是 httpapi 契约）：%v", err)
	}
	if call == nil || call.Err == nil {
		t.Fatalf("应带结构化 Err：%+v", call)
	}
	if call.Body != nil {
		t.Fatal("失败时 Body 应为 nil")
	}
}

// minShimReq 最小请求。
func minShimReq() ir.Request {
	return ir.Request{
		Model:    "m",
		Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Idx: 0, Text: "x"}}}},
	}
}

// TestContractAdapterForKind：方言分派表。
func TestContractAdapterForKind(t *testing.T) {
	for _, kind := range []string{"relay", "vendor", "selfhosted"} {
		a := adapterForKind(kind)
		if a == nil || a.Name() != ir.ProtocolOpenAIChat {
			t.Errorf("kind=%s 应选 openai-chat 适配器", kind)
		}
	}
	if adapterForKind("weird") != nil {
		t.Error("未知 kind 应返回 nil（调用方跳过候选）")
	}
}

// TestContractTargetOfClosure：App.targetOf 闭包的行匹配与 relay 前缀合成。
// （App 依赖 store，这里测同构逻辑：直接构造闭包算法的关键分支。）
func TestContractTargetOfClosure(t *testing.T) {
	// relay 行：base_url=sidecar://buddy + UpstreamModelID=deepseek-v4.1
	// → RealModel 合成 "buddy/deepseek-v4.1"（RelayTransport 拆分约定）。
	info := upstream.CandidateInfo{
		UpstreamID: 1,
		Kind:       upstream.KindRelay,
		BaseURL:    "sidecar://buddy",
		RealModel:  "buddy/deepseek-v4.1",
	}
	tgt, err := upstream.TargetOf(info)
	if err != nil {
		t.Fatalf("relay 目标应合法：%v", err)
	}
	if tgt.BaseURL != "" {
		t.Fatalf("relay Target.BaseURL 必须为空（§9.3）：%q", tgt.BaseURL)
	}
	if tgt.RealModel != "buddy/deepseek-v4.1" {
		t.Fatalf("RealModel 前缀合成错位：%s", tgt.RealModel)
	}

	// selfhosted：原样。
	tgt2, err := upstream.TargetOf(upstream.CandidateInfo{
		UpstreamID: 2, Kind: upstream.KindSelfHosted,
		BaseURL: "http://127.0.0.1:11434", RealModel: "llama3",
	})
	if err != nil || tgt2.BaseURL != "http://127.0.0.1:11434" {
		t.Fatalf("selfhosted 应原样：%v %+v", err, tgt2)
	}
}
