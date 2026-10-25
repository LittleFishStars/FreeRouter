// model_sources_contract_test.go —— GET /api/admin/model-sources 的契约守卫
// （tmp/candidate-sources-contract.md §1/§1.1/§1.2）。
//
// 守的是**对外可见的响应形状**，而不是内部实现：字段集合（不增不减）、null 与
// `[]` 的区分、sources 的排序、channels 的升序、relay 的 available_instances 恒
// null、选不进候选集的行恒 0、未装配时的错误码。这些都是前端会依赖、而单看代码
// 看不出来的约定——改内部实现不该让它们失败，改对外行为必须让它们失败。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// sourcesFake 是 AdminModelSourceStore 的假体：数据由测试直接摆（窄接口只有一个
// 方法，穷举实现比反射/嵌入直白——与其它 admin 假体同一写法）。
type sourcesFake struct {
	rows     []AdminModelSource
	channels []string
	err      error
}

func (f *sourcesFake) ListModelSources(context.Context) ([]AdminModelSource, []string, error) {
	return f.rows, f.channels, f.err
}

// newSourcesHarness 组装带 Sources 假体的管理面（复用 admin_test.go 的 harness）。
func newSourcesHarness(t *testing.T, f *sourcesFake) (*adminHarness, string) {
	t.Helper()
	h := newAdminHarness(t)
	h.deps.Sources = f
	tok := h.issueSession(t, "sources-admin", auth.ClassAdmin, auth.ClassTenant)
	return h, tok
}

// fetchSources 发一次 GET 并返回响应体对象（同时断言它不是 data 信封）。
func fetchSources(t *testing.T, h *adminHarness, tok string) map[string]any {
	t.Helper()
	w := h.req(t, http.MethodGet, "/api/admin/model-sources", tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/model-sources 应 200，得 %d：%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是 JSON：%s", w.Body.String())
	}
	// 契约 §1.1 把这一块直接标成「响应体（200）」，§3 又写明它不是 ListEnvelope：
	// 裸 {sources, channels}，**不包**管理面其它端点的 data 信封。前端的
	// getModelSources 走 getEnvelope 不拆封，多包一层会让它拿到 undefined——
	// 这条断言就是那次集成事故的守卫。
	if _, wrapped := body["data"]; wrapped {
		t.Fatalf("本端点应发裸对象 {sources, channels}，不该包 data 信封：%s", w.Body.String())
	}
	return body
}

// asObjects / asStrings 把 JSON 解出来的 any 收敛成可用形状（形状不对就是红的）。
func asObjects(t *testing.T, what string, v any) []map[string]any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("%s 应是 JSON 数组，得 %T（%v）——null 与 [] 在契约里是两种意思", what, v, v)
	}
	out := make([]map[string]any, 0, len(arr))
	for i, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%s 第 %d 个元素不是对象：%v", what, i, e)
		}
		out = append(out, m)
	}
	return out
}

func asStrings(t *testing.T, what string, v any) []string {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("%s 应是 JSON 数组，得 %T（%v）", what, v, v)
	}
	out := make([]string, 0, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("%s 第 %d 个元素不是字符串：%v", what, i, e)
		}
		out = append(out, s)
	}
	return out
}

// assertKeys 断言 JSON 对象的键集合与契约完全一致：少一个前端渲染不出，多一个
// 说明有人在契约外加了字段（前端不敢用，等于白写）。
func assertKeys(t *testing.T, what string, got map[string]any, want ...string) {
	t.Helper()
	seen := make([]string, 0, len(got))
	for k := range got {
		seen = append(seen, k)
	}
	sort.Strings(seen)
	w := append([]string(nil), want...)
	sort.Strings(w)
	if strings.Join(seen, ",") != strings.Join(w, ",") {
		t.Fatalf("%s 的字段集合不符（§1.1 不增不减）\n实得：%v\n期望：%v", what, seen, w)
	}
}

// sourceKeys / modelKeys 是 §1.1 的字段全集（顺序无关，这里按契约文档的书写顺序）。
// 只多不少——它们是契约的另一种写法，改契约必须同时改这里。
func sourceKeys() []string {
	return []string{
		"id", "kind", "name", "upstream_name", "provider_id", "region", "website",
		"api_doc", "base_url", "enabled", "contributed", "credential_count",
		"model_count", "protocols", "available_instances", "last_seen_at", "models",
		// credentials（凭据池改版 2026-10-26）：每个来源内嵌凭据行，右栏凭据列直接用。
		"credentials",
	}
}

func modelKeys() []string {
	return []string{
		"real_model_id", "upstream_model_id", "display_name", "channel",
		"context_window", "modalities", "efforts", "status",
		"excluded_from_candidates", "protocols", "available_instances",
	}
}

// sourcesFixture 是三条来源，各自覆盖一种形状：
//   - up_1：目录命中的 vendor，两个协议、有模型、字段齐全；
//   - up_2：relay，available_instances 必须原样保 null（"池内可展开"）；
//   - up_3：停用的自建端点，全是 nil 切片——出站必须规范化成 []。
//
// 假体故意给 nil 切片：handler 的空数组口径如果哪天被删掉，这一组会立刻变红，
// 而不是等到某个库恰好为空时才在前端炸出一个 null。
func sourcesFixture() ([]AdminModelSource, []string) {
	one := 1
	zero := 0
	seen := int64(1761350400)
	chanOfficial := "deepseek-official"
	ctxWin := int64(131072)

	rows := []AdminModelSource{
		{
			ID: "up_1", Kind: "vendor", Name: "DeepSeek",
			UpstreamName: "api:deepseek-com-contrib-key_de2c",
			ProviderID:   "deepseek-com", Region: "cn",
			Website: "https://platform.deepseek.com", APIDoc: "https://api-docs.deepseek.com",
			BaseURL: "https://api.deepseek.com/v1", Enabled: true, Contributed: true,
			CredentialCount: 1, ModelCount: 1,
			Protocols: []string{"openai", "anthropic"}, AvailableInstances: &one,
			LastSeenAt: &seen,
			// 凭据池改版（pool-merge 契约 §1.2）：up_1 带一条凭据，
			// up_2/up_3 故意不给（nil）——出站必须规范化成 []。
			Credentials: []AdminSourceCredential{{
				ID: "cred_1", Provider: "api:deepseek-com", ExternalLabel: "贡献凭据",
				Status: "active", VerifyStatus: "unverified", CreatedAt: &seen,
			}},
			Models: []AdminModelSourceModel{{
				RealModelID: "rm_1", UpstreamModelID: "deepseek-flash",
				DisplayName: "DeepSeek-V4.1-Flash", Channel: &chanOfficial,
				ContextWindow: &ctxWin, Modalities: []string{"text"}, Efforts: []string{},
				Status: "active", Protocols: []string{"openai", "anthropic"},
				AvailableInstances: &one,
			}},
		},
		{
			ID: "up_2", Kind: "relay", Name: "cline", UpstreamName: "cline",
			BaseURL: "sidecar://cline", Enabled: true, Contributed: false,
			Protocols: []string{"openai"}, AvailableInstances: nil,
			Models: []AdminModelSourceModel{{
				RealModelID: "rm_2", UpstreamModelID: "cline-x", DisplayName: "cline-x",
				Channel: nil, ContextWindow: nil, Modalities: nil, Efforts: nil,
				Status: "active", Protocols: []string{"openai"},
				AvailableInstances: nil,
			}},
		},
		{
			ID: "up_3", Kind: "selfhosted", Name: "自建端点", UpstreamName: "my-endpoint",
			BaseURL: "https://llm.example.com/v1", Enabled: false, Contributed: false,
			CredentialCount: 0, ModelCount: 0,
			Protocols: []string{"openai"}, AvailableInstances: &zero,
			Models: []AdminModelSourceModel{},
		},
	}
	// 渠道故意乱序给：去重是产出方（桥）的责任，**升序由出站层定型**（同 sources），
	// 所以这里给一个已去重但倒序的集合。
	return rows, []string{"deepseek-official", "cline"}
}

// TestContractModelSourcesWireShape 锁 §1.1 的字段集合与 null/[] 语义。
func TestContractModelSourcesWireShape(t *testing.T) {
	rows, channels := sourcesFixture()
	h, tok := newSourcesHarness(t, &sourcesFake{rows: rows, channels: channels})
	body := fetchSources(t, h, tok)
	assertKeys(t, "响应体", body, "sources", "channels")

	srcs := asObjects(t, "响应体.sources", body["sources"])
	if len(srcs) != 3 {
		t.Fatalf("应有 3 条来源，得 %d", len(srcs))
	}
	for _, s := range srcs {
		assertKeys(t, "source", s, sourceKeys()...)
		for _, m := range asObjects(t, "source.models", s["models"]) {
			assertKeys(t, "model", m, modelKeys()...)
		}
	}

	// ① 目录命中的 vendor：字段逐个落位（含 epoch 秒的时间戳）。
	deepseek := srcs[0]
	if deepseek["name"] != "DeepSeek" || deepseek["upstream_name"] != "api:deepseek-com-contrib-key_de2c" {
		t.Fatalf("name 应用目录展示名、upstream_name 应回发上游行名，得 %v / %v",
			deepseek["name"], deepseek["upstream_name"])
	}
	if deepseek["provider_id"] != "deepseek-com" || deepseek["region"] != "cn" {
		t.Fatalf("provider_id/region 不符：%v / %v", deepseek["provider_id"], deepseek["region"])
	}
	if got := deepseek["last_seen_at"]; got != float64(1761350400) {
		t.Fatalf("last_seen_at 应是 epoch 秒，得 %v", got)
	}
	if got := asStrings(t, "source.protocols", deepseek["protocols"]); strings.Join(got, ",") != "openai,anthropic" {
		t.Fatalf("protocols 应恒 openai 在前，得 %v", got)
	}
	if got := deepseek["model_count"]; got != float64(1) {
		t.Fatalf("model_count 应等于 models 长度，得 %v", got)
	}
	m0 := asObjects(t, "source.models", deepseek["models"])[0]
	if m0["channel"] != "deepseek-official" || m0["context_window"] != float64(131072) {
		t.Fatalf("模型级 channel/context_window 不符：%v / %v", m0["channel"], m0["context_window"])
	}
	if got := asStrings(t, "model.protocols", m0["protocols"]); strings.Join(got, ",") != "openai,anthropic" {
		t.Fatalf("模型协议事实应继承来源，得 %v", got)
	}

	// 凭据池改版（pool-merge 契约 §1.2）：来源内嵌的凭据行字段逐个落位。
	c0 := asObjects(t, "source.credentials", deepseek["credentials"])
	if len(c0) != 1 {
		t.Fatalf("up_1 应有 1 条内嵌凭据，得 %d", len(c0))
	}
	assertKeys(t, "credential", c0[0], "id", "provider", "external_label", "status", "verify_status", "created_at", "last_used_at")
	if c0[0]["id"] != "cred_1" || c0[0]["provider"] != "api:deepseek-com" || c0[0]["status"] != "active" {
		t.Fatalf("凭据行字段不符：%v", c0[0])
	}
	if c0[0]["created_at"] != float64(1761350400) {
		t.Fatalf("created_at 应是 epoch 秒，得 %v", c0[0]["created_at"])
	}
	if v, present := c0[0]["last_used_at"]; !present || v != nil {
		t.Fatalf("last_used_at 未用过应是 null，得 %v（present=%v）", v, present)
	}
	// 没给凭据的来源（up_2/up_3）必须是 [] 而不是 null：同一个「没有」在全站
	// 只有一种 JSON 形状，前端才能只写一个空态分支。
	if got := asObjects(t, "relay.credentials", srcs[1]["credentials"]); len(got) != 0 {
		t.Fatalf("relay 的 credentials 应是 []，得 %v", got)
	}
	// ② relay：available_instances 必须是 null——"数量由 sidecar 账号池决定"这件事
	// 在线上只有 null 能表达；压成 0 会让界面显示「无凭据」，正是这次要修的故障。
	relay := srcs[1]
	if v, present := relay["available_instances"]; !present || v != nil {
		t.Fatalf("relay 的 available_instances 应为 null，得 %v（present=%v）", v, present)
	}
	relayModel := asObjects(t, "source.models", relay["models"])[0]
	if v, present := relayModel["available_instances"]; !present || v != nil {
		t.Fatalf("relay 的模型 available_instances 应继承 null，得 %v（present=%v）", v, present)
	}
	if relayModel["channel"] != nil || relayModel["context_window"] != nil {
		t.Fatalf("channel/context_window 可空列应是 null，得 %v / %v",
			relayModel["channel"], relayModel["context_window"])
	}
	// 可空列是 null，而数组列必须是 []：两者是同一份契约里的两种"没有"。
	if got := asStrings(t, "model.modalities", relayModel["modalities"]); len(got) != 0 {
		t.Fatalf("modalities 解析不出时应给 []，得 %v", got)
	}
	if got := asStrings(t, "model.efforts", relayModel["efforts"]); len(got) != 0 {
		t.Fatalf("efforts 解析不出时应给 []，得 %v", got)
	}

	// ③ 停用的自建端点：0（不是 null——"停用了"是确定结论）+ 空数组而不是 null。
	self := srcs[2]
	if v, present := self["available_instances"]; !present || v != float64(0) {
		t.Fatalf("停用来源的 available_instances 应为 0，得 %v（present=%v）", v, present)
	}
	if got := asObjects(t, "source.models", self["models"]); len(got) != 0 {
		t.Fatalf("无模型来源应给 []，得 %v", got)
	}
	if got := asStrings(t, "source.protocols", self["protocols"]); len(got) != 1 || got[0] != "openai" {
		t.Fatalf("目录未命中的来源应退化为 [openai]，得 %v", got)
	}

	// ④ channels：去重 + 升序（前端拿它做渠道多选，顺序即展示顺序）。
	if got := asStrings(t, "响应体.channels", body["channels"]); strings.Join(got, ",") != "cline,deepseek-official" {
		t.Fatalf("channels 应去重升序，得 %v", got)
	}
}

// TestContractModelSourcesSorting 锁 §1.1 的三级排序：contributed 降序 →
// name 升序 → id 升序。前端左栏按接口给的顺序直接分组渲染，不自己排。
func TestContractModelSourcesSorting(t *testing.T) {
	one := 1
	mk := func(id, name string, contributed bool) AdminModelSource {
		return AdminModelSource{
			ID: id, Kind: "vendor", Name: name, UpstreamName: name,
			BaseURL: "https://example.com/v1", Enabled: true, Contributed: contributed,
			CredentialCount: 1, Protocols: []string{"openai"},
			AvailableInstances: &one, Models: []AdminModelSourceModel{},
		}
	}
	// 乱序给：期望 contributed 组在前（组内 Alpha < Zed），非 contributed 组按
	// name（两个 Alpha 撞名）再用 id 决胜。
	rows := []AdminModelSource{
		mk("up_3", "Beta", false),
		mk("up_1", "Zed", true),
		mk("up_9", "Alpha", false),
		mk("up_2", "Alpha", true),
		mk("up_10", "Alpha", false),
	}
	h, tok := newSourcesHarness(t, &sourcesFake{rows: rows})
	srcs := asObjects(t, "响应体.sources", fetchSources(t, h, tok)["sources"])

	got := make([]string, 0, len(srcs))
	for _, s := range srcs {
		got = append(got, s["id"].(string))
	}
	// 末位按**对外 id 字符串**比较：up_10 在 up_9 之前（字符串序，"1"<"9"）。
	// 这条断言把口径钉死——id 是响应里可见的值，前端不解码也能复现同一顺序。
	want := "up_2,up_1,up_10,up_9,up_3"
	if strings.Join(got, ",") != want {
		t.Fatalf("sources 排序不符 §1.1\n实得：%v\n期望：%s", got, want)
	}
}

// TestContractModelSourcesEmptyPool：空池也要给数组（不是 null）。
//
// 空池是最容易漏测的形状：真实库里总有几条上游，于是 null 只在全新部署时出现，
// 而那时前端拿到 null 会走进空态分支之外的分支。
func TestContractModelSourcesEmptyPool(t *testing.T) {
	h, tok := newSourcesHarness(t, &sourcesFake{rows: nil, channels: nil})
	body := fetchSources(t, h, tok)
	if got := asObjects(t, "响应体.sources", body["sources"]); len(got) != 0 {
		t.Fatalf("空池应给 []，得 %v", got)
	}
	if got := asStrings(t, "响应体.channels", body["channels"]); len(got) != 0 {
		t.Fatalf("空池的 channels 应给 []，得 %v", got)
	}
}

// TestContractModelSourcesUnwired：未装配 Sources 时按既有惯例 404 not_found
// （lead 裁决：未上线的功能不确认其存在，与真 404 同形）。
func TestContractModelSourcesUnwired(t *testing.T) {
	h := newAdminHarness(t) // harness 默认不接 Sources
	tok := h.issueSession(t, "sources-admin", auth.ClassAdmin, auth.ClassTenant)
	w := h.req(t, http.MethodGet, "/api/admin/model-sources", tok, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("未装配应 404，得 %d：%s", w.Code, w.Body.String())
	}
	if code, _ := errorCode(t, w); code != "not_found" {
		t.Fatalf("码应 not_found，实得 %q", code)
	}
}

// TestContractModelSourcesStoreError：桥报错时是 500 internal_error（不是把
// 半张数据当成功发出去——那会让前端以为"这家确实没有模型"）。
func TestContractModelSourcesStoreError(t *testing.T) {
	h, tok := newSourcesHarness(t, &sourcesFake{err: errors.New("库炸了")})
	w := h.req(t, http.MethodGet, "/api/admin/model-sources", tok, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("存储错误应 500，得 %d：%s", w.Code, w.Body.String())
	}
	if code, _ := errorCode(t, w); code != "internal_error" {
		t.Fatalf("码应 internal_error，实得 %q", code)
	}
}

// TestContractModelSourcesReadProducesNoAudit：读端点不写审计（03-admin §1.1）。
// 这一条容易在"顺手给每个 handler 加一行审计"时被破坏，且破坏后只有审计页能看出。
func TestContractModelSourcesReadProducesNoAudit(t *testing.T) {
	rows, channels := sourcesFixture()
	h, tok := newSourcesHarness(t, &sourcesFake{rows: rows, channels: channels})
	fetchSources(t, h, tok)
	if len(h.audit.entries) != 0 {
		t.Fatalf("读端点不该产生审计行，得 %d 条：%+v", len(h.audit.entries), h.audit.entries)
	}
}

// ---------------------------------------------------------------------------
// §1.2 的口径（AdminModelSourceProtocols / Instances）
// ---------------------------------------------------------------------------

// TestAdminModelSourceProtocolsRules 覆盖 §1.2 的三条分支：relay 不查目录、
// 目录命中按目录事实、未命中退化 [openai]。这里用**真实目录数据**
// （deepseek-com 有 anthropic 端点、baichuan-com 没有），所以它同时守住了
// "目录数据被动过"这件事。
func TestAdminModelSourceProtocolsRules(t *testing.T) {
	cases := []struct {
		name       string
		kind, pid  string
		wantJoined string
	}{
		{"relay 恒 openai（其 base_url 是 provider 锚点，查目录没有意义）", "relay", "deepseek-com", "openai"},
		{"vendor 命中目录且目录有 anthropic 端点", "vendor", "deepseek-com", "openai,anthropic"},
		{"selfhosted 命中目录时的口径与 vendor 相同", "selfhosted", "deepseek-com", "openai,anthropic"},
		{"目录命中但没有 anthropic 端点", "vendor", "baichuan-com", "openai"},
		{"自定义贡献的 provider 是 custom，剥不出目录 id", "vendor", "custom", "openai"},
		{"没有凭据时 provider_id 是空串", "vendor", "", "openai"},
		{"目录里不存在的 id（历史或手改数据）", "vendor", "no-such-provider", "openai"},
	}
	for _, c := range cases {
		if got := strings.Join(AdminModelSourceProtocols(c.kind, c.pid), ","); got != c.wantJoined {
			t.Errorf("%s：得 %q，期望 %q", c.name, got, c.wantJoined)
		}
	}
}

// TestAdminModelSourceInstancesRules 覆盖 §1.1 的实例数口径：
// 停用优先于一切 → relay 是 null（不知道）→ 其余等于 active 凭据数。
func TestAdminModelSourceInstancesRules(t *testing.T) {
	if got := AdminModelSourceInstances("vendor", true, 3); got == nil || *got != 3 {
		t.Errorf("vendor/启用的实例数应等于凭据数 3，得 %v", got)
	}
	if got := AdminModelSourceInstances("selfhosted", true, 0); got == nil || *got != 0 {
		t.Errorf("selfhosted 没有 active 凭据时应是 0（不是 null），得 %v", got)
	}
	if got := AdminModelSourceInstances("relay", true, 2); got != nil {
		t.Errorf("relay 的实例数应由 sidecar 账号池决定，Go 侧必须给 null，得 %v", *got)
	}
	// 停用优先：连 relay 也是 0 而不是 null——"停用了"是确定结论。
	if got := AdminModelSourceInstances("relay", false, 2); got == nil || *got != 0 {
		t.Errorf("停用的 relay 应是 0（不是 null），得 %v", got)
	}
	if got := AdminModelSourceInstances("vendor", false, 5); got == nil || *got != 0 {
		t.Errorf("停用来源应是 0，得 %v", got)
	}

	// 模型级：继承来源，但选不进候选集的行恒 0。
	three := 3
	if got := AdminModelSourceModelInstances(&three, "active", false); got == nil || *got != three {
		t.Errorf("active 且未排除的模型应继承来源值，得 %v", got)
	}
	if got := AdminModelSourceModelInstances(nil, "active", false); got != nil {
		t.Errorf("来源是 null 时模型应继承 null，得 %v", *got)
	}
	for _, c := range []struct {
		status   string
		excluded bool
	}{
		{"missing", false},
		{"disabled", false},
		{"active", true},
		{"missing", true},
	} {
		if got := AdminModelSourceModelInstances(&three, c.status, c.excluded); got == nil || *got != 0 {
			t.Errorf("status=%s excluded=%v 的行选不进候选集，实例数应恒 0，得 %v",
				c.status, c.excluded, got)
		}
	}
}
