package httpapi

// contribution_v2_contract_test.go —— 贡献入口三分类 / kind=api / 暂停恢复 /
// 统计 range 的 **HTTP 形状契约**（tmp/CONTRACT-contribution-v2.md §1 §3 §5 §6）。
//
// 为什么形状要单独守：形状就是前端契约，形状错了前端直接白屏；而
// internal/app/contribution_contract_test.go 守的是「真库 + 真探测之后状态对不对」。
// 两层各管一件事——这一层用 fake，失败原因一眼能看出来是形状问题还是落库问题。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// —— fake：只覆盖本组用例触达的方法 ——

// fakeProviderOAuth 只实现 Providers：本文件不碰授权链路。
type fakeProviderOAuth struct{ choices []ProviderChoice }

func (f fakeProviderOAuth) Providers(context.Context) []ProviderChoice { return f.choices }

func (f fakeProviderOAuth) Authorize(context.Context, AuthorizeInput) (*AuthorizeResult, error) {
	return nil, errors.New("本用例不应发起授权")
}

func (f fakeProviderOAuth) Callback(context.Context, string, string, string, string) (*OAuthOutcome, error) {
	return nil, errors.New("本用例不应处理回调")
}

func (f fakeProviderOAuth) Poll(context.Context, PollInput) (*PollResult, error) {
	return nil, errors.New("本用例不应轮询会话")
}

// fakeContributionStore 是贡献面存储窄接口的内存实现。
type fakeContributionStore struct {
	owner int64
	creds []ContributionCredentialRow
	// notFoundID 使 CredentialOf 对它报错（模拟非属主/不存在）。
	notFoundID int64
	// usageSince 记录上一次 ContributionUsage 收到的 since（断言 range 真的传下去了）。
	usageSince int64
}

// AccountIDOf 按会话里的账户 id 原样翻译（认证面字符串 id → 贡献链路 int64）。
// 不返回固定 f.owner：那会把「别人的会话」也算成本账户，属主校验的用例就失效了。
func (f *fakeContributionStore) AccountIDOf(_ context.Context, accountID string) (int64, error) {
	return strconv.ParseInt(accountID, 10, 64)
}

func (f *fakeContributionStore) CreateContributor(context.Context, string) (int64, error) {
	return f.owner, nil
}

func (f *fakeContributionStore) SetRecoveryCodeHash(context.Context, int64, string) error { return nil }

func (f *fakeContributionStore) RecoveryHashOf(context.Context, int64) (string, error) {
	return "", nil
}

func (f *fakeContributionStore) RecoveryStateOf(context.Context, int64) (bool, error) {
	return false, nil
}

func (f *fakeContributionStore) CredentialsOf(context.Context, int64) ([]ContributionCredentialRow, error) {
	return f.creds, nil
}

func (f *fakeContributionStore) CredentialOf(_ context.Context, owner, id int64) (*ContributionCredentialRow, error) {
	if owner != f.owner || id == f.notFoundID {
		return nil, errors.New("not found")
	}
	for i := range f.creds {
		if f.creds[i].ID == id {
			return &f.creds[i], nil
		}
	}
	return nil, errors.New("not found")
}

func (f *fakeContributionStore) ContributionUsage(_ context.Context, _ int64, since int64) (int64, int64, int64, error) {
	f.usageSince = since
	return 3, 30, 300, nil
}

// fakeSubmitter 记录收到的提交（断言端点被后端解析成了什么）。
type fakeSubmitter struct{ got []ContributionSubmission }

func (f *fakeSubmitter) Submit(_ context.Context, sub ContributionSubmission) (ContributionOutcome, error) {
	f.got = append(f.got, sub)
	return ContributionOutcome{
		CredentialID: 1, Status: "active", VerifyStatus: "verified", InPool: true,
	}, nil
}

// fakePauser 记录切换请求，并返回固定状态。
type fakePauser struct {
	status string
	paused []bool
}

func (f *fakePauser) SetPaused(_ context.Context, _, _ int64, paused bool) (string, error) {
	f.paused = append(f.paused, paused)
	return f.status, nil
}

// postJSON 让请求变成带 body 的 POST（调用方已带会话上下文时用）。
func postJSON(accountID, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	p := &auth.Principal{AccountID: accountID}
	return r.WithContext(context.WithValue(r.Context(), ctxKeySessionPrincipal, p))
}

// dataOf 解 `{"data": ...}` 外壳，返回 data 的原始 JSON。
//
// 为什么不用 admin_test.go 里的 decodeData：那个 helper 把 data 解成 map，
// 而这里要断言 categories/**items 的原始形状（含 `[]` 与字段缺失），必须先拿到字节。
func dataOf(t *testing.T, w *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（body=%s）", err, w.Body.String())
	}
	return env.Data
}

// TestContributionProvidersCategoriesShape 钉住三分类的形状（契约 §1）。
func TestContributionProvidersCategoriesShape(t *testing.T) {
	deps := &ContributionDeps{
		OAuth: fakeProviderOAuth{choices: []ProviderChoice{
			{ID: "trae", Name: "TRAE", Flow: "browser_callback", Available: true},
			{
				ID: "gemini", Name: "Google Gemini", Flow: "browser", Needs: []string{"port"},
				Available: false, UnavailableReason: "未配置 OAuth 客户端",
				UnavailableKind: UnavailableKindConfig,
			},
		}},
	}
	srv := &Server{}
	w := httptest.NewRecorder()
	srv.HandleContributeProviders(deps)(w, httptest.NewRequest(http.MethodGet, "/api/contributions/providers", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", w.Code, w.Body.String())
	}
	var data struct {
		Categories []struct {
			ID          string          `json:"id"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Items       json.RawMessage `json:"items"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(dataOf(t, w), &data); err != nil {
		t.Fatalf("data 形状应为 {categories:[...]}：%v", err)
	}
	if len(data.Categories) != 3 {
		t.Fatalf("应有 3 个分类，得 %d", len(data.Categories))
	}
	wantIDs := []string{ContributeCategoryReverseProxy, ContributeCategoryAPI, ContributeCategoryCustom}
	for i, want := range wantIDs {
		if data.Categories[i].ID != want {
			t.Errorf("第 %d 个分类 id = %q，期望 %q（顺序是前端 Tab 的顺序）", i, data.Categories[i].ID, want)
		}
		if data.Categories[i].Name == "" || data.Categories[i].Description == "" {
			t.Errorf("分类 %s 缺 name/description（前端 Tab 与说明都要它）", want)
		}
	}

	// reverse_proxy：字段发全（空值给零值），不含 api_key 伪条目。
	var reverse []map[string]any
	if err := json.Unmarshal(data.Categories[0].Items, &reverse); err != nil {
		t.Fatalf("reverse_proxy.items 应是数组：%v", err)
	}
	if len(reverse) != 2 {
		t.Fatalf("reverse_proxy.items 应有 2 条，得 %d", len(reverse))
	}
	for _, item := range reverse {
		for _, key := range []string{
			"id", "name", "flow", "needs", "available",
			"unavailable_reason", "unavailable_kind", "suggested_endpoint", "steps",
		} {
			if _, ok := item[key]; !ok {
				t.Errorf("reverse_proxy 条目 %v 缺字段 %q（契约要求字段发全）", item["id"], key)
			}
		}
		if item["id"] == "api-key" || item["flow"] == "api_key" {
			t.Errorf("api-key 伪条目已删除，不应再出现：%v", item)
		}
	}
	if needs, ok := reverse[0]["needs"].([]any); !ok || needs == nil {
		t.Errorf("needs 应是空数组而不是 null，得 %v", reverse[0]["needs"])
	}
	if reverse[1]["available"] != false || reverse[1]["unavailable_reason"] == "" {
		t.Errorf("不可用条目必须带原因（否则界面只能说「不可用」而说不出为什么）：%v", reverse[1])
	}

	// api：来自内置目录，带端点/协议/模型，不带 available。
	var api []map[string]any
	if err := json.Unmarshal(data.Categories[1].Items, &api); err != nil {
		t.Fatalf("api.items 应是数组：%v", err)
	}
	if len(api) != 55 {
		t.Fatalf("api.items 应来自内置目录的 55 家（2026-10-25 回加 modelscope-cn/opencode-ai/opencode-go），得 %d", len(api))
	}
	for _, item := range api {
		if _, ok := item["available"]; ok {
			t.Errorf("api 条目不应带 available（目录是本地事实数据，恒可用）：%v", item["id"])
		}
		if item["base_url"] == "" || item["protocol"] != "openai" {
			t.Errorf("api 条目缺 base_url 或协议不是 openai：%v", item)
		}
		if _, ok := item["models"].([]any); !ok {
			t.Errorf("models 应是数组（空目录也要给 []）：%v", item["id"])
		}
	}
	ids := make([]string, 0, len(api))
	for _, item := range api {
		ids = append(ids, item["id"].(string))
	}
	if !sort.StringsAreSorted(ids) {
		t.Error("api.items 应保持目录的 id 升序（List 不重排）")
	}

	// custom：恒为空数组（入口是表单）。
	if string(data.Categories[2].Items) != "[]" {
		t.Errorf("custom.items 应为空数组，得 %s", data.Categories[2].Items)
	}
}

// TestContributionProvidersWithoutOAuth 没有 OAuth 桥时仍要给出三个分类（形状不随装配变）。
func TestContributionProvidersWithoutOAuth(t *testing.T) {
	srv := &Server{}
	w := httptest.NewRecorder()
	srv.HandleContributeProviders(&ContributionDeps{})(w, httptest.NewRequest(http.MethodGet, "/", nil))

	var data struct {
		Categories []struct {
			Items json.RawMessage `json:"items"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(dataOf(t, w), &data); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(data.Categories) != 3 {
		t.Fatalf("应有 3 个分类，得 %d", len(data.Categories))
	}
	if string(data.Categories[0].Items) != "[]" {
		t.Errorf("没有 OAuth 桥时 reverse_proxy 应为空数组，得 %s", data.Categories[0].Items)
	}
}

// TestContributionSubmitKindAPI 覆盖 kind=api 的端点解析与 kind=custom 的回落（契约 §3）。
func TestContributionSubmitKindAPI(t *testing.T) {
	newDeps := func() (*ContributionDeps, *fakeSubmitter, *fakeContributionStore) {
		sub := &fakeSubmitter{}
		st := &fakeContributionStore{owner: 7}
		return &ContributionDeps{
			Submitter: sub,
			Store:     st,
			Hasher:    auth.NewPasswordHasher(8),
		}, sub, st
	}

	t.Run("kind=api 端点由目录解析、provider 落成 api:<id>", func(t *testing.T) {
		deps, sub, _ := newDeps()
		w := httptest.NewRecorder()
		// 请求体里的 endpoint 故意写成错值：后端必须以目录为准。
		body := `{"kind":"api","provider":"deepseek-com","endpoint":"https://evil.example/v1","api_key":"sk-0123456789abcdef","claimed_models":["deepseek-flash","deepseek-reasoner"]}`
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON("7", body))
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200（body=%s）", w.Code, w.Body.String())
		}
		if len(sub.got) != 1 {
			t.Fatalf("应提交一次，得 %d", len(sub.got))
		}
		got := sub.got[0]
		if !strings.HasPrefix(got.Provider, "api:") || got.Provider != "api:deepseek-com" {
			t.Errorf("Provider 应为 api:deepseek-com，得 %q", got.Provider)
		}
		if !strings.Contains(got.BaseURL, "deepseek") || strings.Contains(got.BaseURL, "evil.example") {
			t.Errorf("BaseURL 必须来自目录而不是请求体，得 %q", got.BaseURL)
		}
		if len(got.ClaimedModels) != 2 || got.ClaimedModels[0] != "deepseek-flash" || got.ClaimedModels[1] != "deepseek-reasoner" {
			t.Errorf("声称模型应全量透传（不再取第一个），得 %v", got.ClaimedModels)
		}
	})

	t.Run("kind=api 未知 provider → 400 invalid_request", func(t *testing.T) {
		deps, sub, _ := newDeps()
		w := httptest.NewRecorder()
		body := `{"kind":"api","provider":"no-such-vendor","api_key":"sk-0123456789abcdef"}`
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON("7", body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，期望 400", w.Code)
		}
		if !strings.Contains(w.Body.String(), "未知的 API 供应商：no-such-vendor") {
			t.Errorf("错误文案应点名未知的 id，得 %s", w.Body.String())
		}
		if len(sub.got) != 0 {
			t.Error("校验失败不应进入提交链路")
		}
	})

	t.Run("kind 缺省 = custom（向后兼容旧客户端）", func(t *testing.T) {
		deps, sub, _ := newDeps()
		w := httptest.NewRecorder()
		body := `{"endpoint":"https://my-gw.example/v1","api_key":"sk-0123456789abcdef","label":"我的网关"}`
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON("7", body))
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200（body=%s）", w.Code, w.Body.String())
		}
		if len(sub.got) != 1 || sub.got[0].Provider != "custom" {
			t.Fatalf("缺省 kind 应落成 provider=custom，得 %+v", sub.got)
		}
		if sub.got[0].BaseURL != "https://my-gw.example/v1" {
			t.Errorf("custom 的端点应原样透传，得 %q", sub.got[0].BaseURL)
		}
	})

	t.Run("custom 缺 endpoint → 400", func(t *testing.T) {
		deps, sub, _ := newDeps()
		w := httptest.NewRecorder()
		body := `{"kind":"custom","api_key":"sk-0123456789abcdef"}`
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON("7", body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，期望 400", w.Code)
		}
		if len(sub.got) != 0 {
			t.Error("校验失败不应进入提交链路")
		}
	})

	t.Run("未知 kind → 400（不静默当 custom）", func(t *testing.T) {
		deps, sub, _ := newDeps()
		w := httptest.NewRecorder()
		body := `{"kind":"oauth","endpoint":"https://x.example/v1","api_key":"sk-0123456789abcdef"}`
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON("7", body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，期望 400", w.Code)
		}
		if !strings.Contains(w.Body.String(), "未知的贡献类型") {
			t.Errorf("错误文案应说明 kind 不合法，得 %s", w.Body.String())
		}
		if len(sub.got) != 0 {
			t.Error("未知 kind 不应进入提交链路")
		}
	})
}

// TestCredentialPauseResumeHTTP 覆盖暂停/恢复的 HTTP 形状与属主校验（契约 §5）。
func TestCredentialPauseResumeHTTP(t *testing.T) {
	creds := []ContributionCredentialRow{{ID: 11, Status: "active"}, {ID: 12, Status: "disabled"}}

	cases := []struct {
		name     string
		handler  func(*Server, *ContributionDeps) http.HandlerFunc
		status   string
		wantPaus []bool
	}{
		{
			name: "pause",
			handler: func(s *Server, d *ContributionDeps) http.HandlerFunc {
				return s.HandleContributionCredentialPause(d)
			},
			status: "paused", wantPaus: []bool{true},
		},
		{
			name: "resume",
			handler: func(s *Server, d *ContributionDeps) http.HandlerFunc {
				return s.HandleContributionCredentialResume(d)
			},
			status: "enabled", wantPaus: []bool{false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeContributionStore{owner: 7, creds: creds}
			pr := &fakePauser{status: tc.status}
			deps := &ContributionDeps{Store: st, Pauser: pr}

			r := postJSON("7", "")
			r = withURLParam(r, "11")
			w := httptest.NewRecorder()
			tc.handler(&Server{}, deps)(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200（body=%s）", w.Code, w.Body.String())
			}
			var data struct {
				OK     bool   `json:"ok"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(dataOf(t, w), &data); err != nil {
				t.Fatalf("解析 data 失败：%v", err)
			}
			if !data.OK || data.Status != tc.status {
				t.Errorf("应答应为 {ok:true,status:%q}，得 %+v", tc.status, data)
			}
			if len(pr.paused) != 1 || pr.paused[0] != tc.wantPaus[0] {
				t.Errorf("应调用一次 SetPaused(paused=%v)，得 %v", tc.wantPaus[0], pr.paused)
			}
		})
	}

	t.Run("非属主 → 404 not_found（不确认他人凭据存在）", func(t *testing.T) {
		st := &fakeContributionStore{owner: 7, creds: creds}
		pr := &fakePauser{status: "paused"}
		deps := &ContributionDeps{Store: st, Pauser: pr}

		r := postJSON("999", "") // 会话属于另一个账户
		r = withURLParam(r, "11")
		w := httptest.NewRecorder()
		(&Server{}).HandleContributionCredentialPause(deps)(w, r)

		if w.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d，期望 404", w.Code)
		}
		if !strings.Contains(w.Body.String(), "not_found") {
			t.Errorf("应回 not_found 码，得 %s", w.Body.String())
		}
		if len(pr.paused) != 0 {
			t.Error("非属主不应触达状态切换")
		}
	})

	t.Run("未登录 → 401 session_expired", func(t *testing.T) {
		deps := &ContributionDeps{Store: &fakeContributionStore{owner: 7}, Pauser: &fakePauser{}}
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
		r = withURLParam(r, "11")
		w := httptest.NewRecorder()
		(&Server{}).HandleContributionCredentialPause(deps)(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("状态码 = %d，期望 401", w.Code)
		}
	})
}

// TestContributionStatsRange 覆盖 range 的解析与 400 的 param（契约 §6）。
func TestContributionStatsRange(t *testing.T) {
	utc := time.Now().UTC()
	wantDay := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).Unix()
	wantMonth := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()

	cases := []struct {
		query string
		want  int64
	}{
		{"", wantDay},
		{"?range=day", wantDay},
		{"?range=month", wantMonth},
	}
	for _, tc := range cases {
		st := &fakeContributionStore{owner: 7}
		deps := &ContributionDeps{Store: st}
		r := httptest.NewRequest(http.MethodGet, "/api/contributions/stats"+tc.query, nil)
		p := &auth.Principal{AccountID: "7"}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeySessionPrincipal, p))
		w := httptest.NewRecorder()
		(&Server{}).HandleContributionStats(deps)(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("%q 的状态码 = %d，期望 200（body=%s）", tc.query, w.Code, w.Body.String())
		}
		if st.usageSince != tc.want {
			t.Errorf("%q 的 since = %d，期望 %d（UTC 日界）", tc.query, st.usageSince, tc.want)
		}
	}

	t.Run("非法 range → 400 且 param=range", func(t *testing.T) {
		st := &fakeContributionStore{owner: 7}
		deps := &ContributionDeps{Store: st}
		r := httptest.NewRequest(http.MethodGet, "/api/contributions/stats?range=week", nil)
		p := &auth.Principal{AccountID: "7"}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeySessionPrincipal, p))
		w := httptest.NewRecorder()
		(&Server{}).HandleContributionStats(deps)(w, r)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，期望 400", w.Code)
		}
		var env struct {
			Error struct {
				Code  string `json:"code"`
				Param string `json:"param"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("错误体不是合法 JSON：%v", err)
		}
		if env.Error.Code != "invalid_request" || env.Error.Param != "range" {
			t.Errorf("应回 invalid_request + param=range，得 %+v", env.Error)
		}
		if st.usageSince != 0 {
			t.Error("参数非法时不应去查用量")
		}
	})
}

// withURLParam 给请求塞 chi 的路径参数（handler 用 chi.URLParam(r, "id") 取值）。
//
// 为什么直接构造 RouteContext：本文件不挂 chi 路由器，而路径参数是 handler
// 唯一的入参来源；伪造一个只有一个参数的 RouteContext 比起一整条路由链更贴近
// 被测对象（失败原因也只可能出在 handler 自己）。
func withURLParam(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
