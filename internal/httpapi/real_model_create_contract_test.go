// real_model_create_contract_test.go —— POST /api/admin/real-models 的契约守卫
// （tmp/pool-merge-contract.md §1.3）。
//
// 守的是「手工登记模型」这条对外通道的行为面：
//   - 校验表逐条（upstream_id 形状、模型 id 非空/无空白、aggregate/* 拒绝、
//     context_window 给了必须 >0）——这些是弹窗提交前拦不住、必须在服务端
//     成立的最后一道；
//   - 幂等语义：命中既有行返回 200 + created=false，不写第二行也不写审计
//     （重放必须同结果，否则"已登记"提示会随点击次数变成 201/200 抖动）；
//   - 新建 201 + created=true + 审计 model.manual_add 恰好一条；
//   - 上游不存在 → 404（与未装配同形，不确认存在）；
//   - 未装配（ModelCreate=nil）→ 404 not_found。
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// createFake 是 AdminRealModelCreator 的假体。
//
// 它模拟桥的幂等判定结果（existing=true 时 created=false），并记录审计调用
// 次数——"命中既有行不写审计"是这条通道的行为承诺，假体必须能被断言到。
type createFake struct {
	existing  bool // true = 命中既有行（返回既有形状、created=false）
	upMissing bool // true = 上游不存在（返回 ErrAdminUpstreamNotFound）
	audits    int
	calls     int
	lastModel string
	lastAudit AdminAuditEntry
	err       error
}

func (f *createFake) CreateRealModel(_ context.Context, _ string, upstreamModelID, _ string, _ *int64, audit AdminAuditEntry) (*AdminRealModel, bool, string, error) {
	f.calls++
	f.lastModel = upstreamModelID
	if f.upMissing {
		return nil, false, "", ErrAdminUpstreamNotFound
	}
	if f.err != nil {
		return nil, false, "", f.err
	}
	if f.existing {
		return &AdminRealModel{ID: "00000060-0000-4000-8000-000000000009", UpstreamModelID: upstreamModelID, Status: "active", Source: "auto"}, false, "", nil
	}
	f.audits++
	f.lastAudit = audit
	return &AdminRealModel{ID: "00000060-0000-4000-8000-000000000001", UpstreamModelID: upstreamModelID, DisplayName: upstreamModelID, Status: "active", Source: "override"}, true, fmt.Sprintf("00000090-0000-4000-8000-%012d", f.audits), nil
}

func newCreateHarness(t *testing.T, f *createFake) (*adminHarness, string) {
	t.Helper()
	h := newAdminHarness(t)
	h.deps.ModelCreate = f
	tok := h.issueSession(t, "create-admin", auth.ClassAdmin, auth.ClassTenant)
	return h, tok
}

// TestContractRealModelCreateValidation 钉住 §1.3 的校验表：每条非法输入
// 必须 400 且 param 指明是哪个字段——弹窗要能把错误落到对的输入框上。
func TestContractRealModelCreateValidation(t *testing.T) {
	cases := []struct {
		name, body, wantParam string
	}{
		{"upstream_id 形状", `{"upstream_id":"nope","upstream_model_id":"m1"}`, "upstream_id"},
		// 旧用例「upstream_id 填 rm_ 前缀 → 400」随前缀体系一起消失：跨资源误用
		// 现在由「上游表里查不到这一行 → 404」承担（见 UpstreamMissing 用例）。
		{"模型 id 空", `{"upstream_id":"00000050-0000-4000-8000-000000000001","upstream_model_id":"  "}`, "upstream_model_id"},
		{"模型 id 含空白", `{"upstream_id":"00000050-0000-4000-8000-000000000001","upstream_model_id":"a b"}`, "upstream_model_id"},
		// aggregate/* 恒不可选（01-catalog §5）：手工通道也不能绕过。
		{"aggregate 前缀", `{"upstream_id":"00000050-0000-4000-8000-000000000001","upstream_model_id":"aggregate/x"}`, "upstream_model_id"},
		{"上下文 0", `{"upstream_id":"00000050-0000-4000-8000-000000000001","upstream_model_id":"m1","context_window":0}`, "context_window"},
		{"上下文负数", `{"upstream_id":"00000050-0000-4000-8000-000000000001","upstream_model_id":"m1","context_window":-5}`, "context_window"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &createFake{}
			h, tok := newCreateHarness(t, f)
			w := h.req(t, http.MethodPost, "/api/admin/real-models", tok, c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("应 400，得 %d：%s", w.Code, w.Body.String())
			}
			var env struct {
				Error struct {
					Param string `json:"param"`
					Code  string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("解析错误体失败：%s", w.Body.String())
			}
			if env.Error.Param != c.wantParam {
				t.Fatalf("param 应是 %q，得 %q（%s）", c.wantParam, env.Error.Param, w.Body.String())
			}
			if env.Error.Code != "invalid_request" {
				t.Fatalf("code 应是 invalid_request，得 %q", env.Error.Code)
			}
			// 校验失败绝不能已经打到桥（存在性/幂等判定是桥的事，形状错就地拦）。
			if f.calls != 0 {
				t.Fatalf("校验失败不应调用桥，实得 %d 次", f.calls)
			}
		})
	}
}

// TestContractRealModelCreateSuccess 钉住新建路径：201 + created=true +
// 信封 data.model 形状 + 审计动作 model.manual_add 恰好一条。
func TestContractRealModelCreateSuccess(t *testing.T) {
	f := &createFake{}
	h, tok := newCreateHarness(t, f)
	w := h.req(t, http.MethodPost, "/api/admin/real-models", tok,
		`{"upstream_id":"00000050-0000-4000-8000-000000000001","upstream_model_id":"my-model-x","display_name":"我的模型"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("新建应 201，得 %d：%s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Model   map[string]any `json:"model"`
			Created bool           `json:"created"`
		} `json:"data"`
		AuditID string `json:"audit_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析失败：%s", w.Body.String())
	}
	if !env.Data.Created {
		t.Fatalf("created 应为 true：%s", w.Body.String())
	}
	if env.Data.Model["id"] != "00000060-0000-4000-8000-000000000001" || env.Data.Model["upstream_model_id"] != "my-model-x" {
		t.Fatalf("data.model 形状不对：%v", env.Data.Model)
	}
	// 契约 §1.3：source=override 是刷新保护的前提，响应必须回显它。
	if env.Data.Model["source"] != "override" {
		t.Fatalf("手工登记的行 source 应是 override，得 %v", env.Data.Model["source"])
	}
	if env.AuditID == "" {
		t.Fatalf("新建必须带 audit_id：%s", w.Body.String())
	}
	if f.audits != 1 || f.lastAudit.Action != "model.manual_add" {
		t.Fatalf("审计应恰好一条 model.manual_add，实得 %d 条（action=%q）", f.audits, f.lastAudit.Action)
	}
}

// TestContractRealModelCreateIdempotent 钉住幂等：命中既有行返回 200 +
// created=false，且不写审计（什么都没变）。弹窗双击/重放不能产生第二行。
func TestContractRealModelCreateIdempotent(t *testing.T) {
	f := &createFake{existing: true}
	h, tok := newCreateHarness(t, f)
	w := h.req(t, http.MethodPost, "/api/admin/real-models", tok,
		`{"upstream_id":"00000050-0000-4000-8000-000000000001","upstream_model_id":"deepseek-flash"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("命中既有行应 200（幂等），得 %d：%s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Created bool `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析失败：%s", w.Body.String())
	}
	if env.Data.Created {
		t.Fatalf("created 应为 false：%s", w.Body.String())
	}
	if f.audits != 0 {
		t.Fatalf("命中既有行不应写审计，实得 %d 条", f.audits)
	}
}

// TestContractRealModelCreateUpstreamMissing 钉住上游不存在 → 404。
func TestContractRealModelCreateUpstreamMissing(t *testing.T) {
	f := &createFake{upMissing: true}
	h, tok := newCreateHarness(t, f)
	w := h.req(t, http.MethodPost, "/api/admin/real-models", tok,
		`{"upstream_id":"00000050-0000-4000-8000-000000000999","upstream_model_id":"m1"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("上游不存在应 404，得 %d：%s", w.Code, w.Body.String())
	}
}

// TestContractRealModelCreateUnwired 钉住未装配：ModelCreate=nil 时端点
// 按既有惯例 404 not_found（不确认能力存在）。
func TestContractRealModelCreateUnwired(t *testing.T) {
	h := newAdminHarness(t) // 默认不接 ModelCreate
	tok := h.issueSession(t, "plain-admin", auth.ClassAdmin, auth.ClassTenant)
	w := h.req(t, http.MethodPost, "/api/admin/real-models", tok,
		`{"upstream_id":"00000050-0000-4000-8000-000000000001","upstream_model_id":"m1"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("未装配应 404，得 %d：%s", w.Code, w.Body.String())
	}
}

// TestContractRealModelCreateReadProducesNoAudit 钉住读端点不发审计：
// GET /real-models 与 GET /model-sources 各来一次，审计计数必须纹丝不动。
// （03-admin.md §1.1：每一个写操作产生审计，读操作不产生。）
func TestContractRealModelCreateReadProducesNoAudit(t *testing.T) {
	f := &createFake{}
	h, tok := newCreateHarness(t, f)
	before := len(h.audit.entries)
	if w := h.req(t, http.MethodGet, "/api/admin/real-models", tok, ""); w.Code != http.StatusOK {
		t.Fatalf("列表应 200，得 %d", w.Code)
	}
	if w := h.req(t, http.MethodGet, "/api/admin/model-sources", tok, ""); w.Code != http.StatusNotFound && w.Code != http.StatusOK {
		// 本 harness 没接 Sources 假体时 404 是合法形状（未装配不确认存在）。
		t.Fatalf("model-sources 应 200/404，得 %d", w.Code)
	}
	if len(h.audit.entries) != before {
		t.Fatalf("读操作不得产生审计行，前 %d 后 %d", before, len(h.audit.entries))
	}
}
