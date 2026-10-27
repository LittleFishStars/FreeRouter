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

func (f fakeProviderOAuth) Callback(context.Context, string, string, string, string, string) (*OAuthOutcome, error) {
	return nil, errors.New("本用例不应处理回调")
}

func (f fakeProviderOAuth) Poll(context.Context, PollInput) (*PollResult, error) {
	return nil, errors.New("本用例不应轮询会话")
}

// fakeContributionStore 是贡献面存储窄接口的内存实现。
type fakeContributionStore struct {
	owner string
	creds []ContributionCredentialRow
	// notFoundID 使 CredentialOf 对它报错（模拟非属主/不存在）。
	notFoundID string
	// usageSince 记录上一次 ContributionUsage 收到的 since（断言 range 真的传下去了）。
	usageSince int64
	// contributorCreated 计数：自动获取闸门必须在**建账户之前**退回，
	// 否则每次"拿不到目录"的尝试都会留下一个无凭据的孤儿 contributor 账户。
	contributorCreated int
}

// AccountIDOf 按会话里的账户 id 原样翻译（认证面字符串 id → 贡献链路 int64）。
// 不返回固定 f.owner：那会把「别人的会话」也算成本账户，属主校验的用例就失效了。
func (f *fakeContributionStore) AccountIDOf(_ context.Context, accountID string) (string, error) {
	return accountID, nil
}

func (f *fakeContributionStore) CreateContributor(context.Context, string) (string, error) {
	f.contributorCreated++
	return f.owner, nil
}

func (f *fakeContributionStore) SetRecoveryCodeHash(context.Context, string, string) error {
	return nil
}

func (f *fakeContributionStore) RecoveryHashOf(context.Context, string) (string, error) {
	return "", nil
}

func (f *fakeContributionStore) RecoveryStateOf(context.Context, string) (bool, error) {
	return false, nil
}

func (f *fakeContributionStore) CredentialsOf(context.Context, string) ([]ContributionCredentialRow, error) {
	return f.creds, nil
}

func (f *fakeContributionStore) CredentialOf(_ context.Context, owner, id string) (*ContributionCredentialRow, error) {
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

func (f *fakeContributionStore) ContributionUsage(_ context.Context, _ string, since int64) (int64, int64, int64, error) {
	f.usageSince = since
	return 3, 30, 300, nil
}

// fakeSubmitter 记录收到的提交（断言端点被后端解析成了什么）。
type fakeSubmitter struct {
	got []ContributionSubmission
	// discover 是自动获取闸门的 canned 应答（零值 = 探到两个模型、不要求补声明）。
	discoverModels  []string
	discoverNeed    bool
	discoverReason  string
	discoverErr     error
	discoverCalls   int
	lastDiscoverReq struct{ provider, baseURL, apiKey string }
}

func (f *fakeSubmitter) Submit(_ context.Context, sub ContributionSubmission) (ContributionOutcome, error) {
	f.got = append(f.got, sub)
	return ContributionOutcome{
		CredentialID: "id1", Status: "active", VerifyStatus: "verified", InPool: true,
	}, nil
}

func (f *fakeSubmitter) Discover(_ context.Context, provider, baseURL, apiKey string) ([]string, bool, string, any, error) {
	f.discoverCalls++
	f.lastDiscoverReq.provider, f.lastDiscoverReq.baseURL, f.lastDiscoverReq.apiKey = provider, baseURL, apiKey
	if f.discoverErr != nil {
		return nil, false, "", nil, f.discoverErr
	}
	models := f.discoverModels
	if models == nil {
		models = []string{"m-a", "m-b"}
	}
	return models, f.discoverNeed, f.discoverReason, "pre-token", nil
}

// fakePauser 记录切换请求，并返回固定状态。
type fakePauser struct {
	status string
	paused []bool
}

func (f *fakePauser) SetPaused(_ context.Context, _, _ string, paused bool) (string, error) {
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
		st := &fakeContributionStore{owner: "id7"}
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
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, body))
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
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, body))
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
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, body))
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
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, body))
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
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, body))
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
//
// 路径参数 id 会过 idgen.Parse（形状硬校验），所以夹具必须用合法 UUID；
// owner 不过形状校验（fake 的 AccountIDOf 直接透传），但必须与 postJSON 的
// principal 一致，否则属主判定失败 → 404。
const (
	pauseOwner   = "00000010-0000-4000-8000-000000000007"
	pauseCred1   = "00000040-0000-4000-8000-000000000011"
	pauseCred2   = "00000040-0000-4000-8000-000000000012"
	pauseNotMine = "00000010-0000-4000-8000-000000000099"
)

// TestCredentialPauseResumeHTTP 覆盖暂停/恢复的 HTTP 形状与属主校验（契约 §5）。
func TestCredentialPauseResumeHTTP(t *testing.T) {
	creds := []ContributionCredentialRow{{ID: pauseCred1, Status: "active"}, {ID: pauseCred2, Status: "disabled"}}

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
			st := &fakeContributionStore{owner: pauseOwner, creds: creds}
			pr := &fakePauser{status: tc.status}
			deps := &ContributionDeps{Store: st, Pauser: pr}

			r := postJSON(pauseOwner, "")
			r = withURLParam(r, pauseCred1)
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
		st := &fakeContributionStore{owner: pauseOwner, creds: creds}
		pr := &fakePauser{status: "paused"}
		deps := &ContributionDeps{Store: st, Pauser: pr}

		r := postJSON("999", "") // 会话属于另一个账户
		r = withURLParam(r, pauseCred1)
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
		deps := &ContributionDeps{Store: &fakeContributionStore{owner: pauseOwner}, Pauser: &fakePauser{}}
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
		r = withURLParam(r, pauseCred1)
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
		st := &fakeContributionStore{owner: "id7"}
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
		st := &fakeContributionStore{owner: "id7"}
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

// TestContributionCustomAutoDiscover 覆盖「自定义供应商的声称模型改为非必填，
// 没填就自动获取；拿不到就退回让用户填，否则不允许提交」（2026-10-26 用户决定）。
func TestContributionCustomAutoDiscover(t *testing.T) {
	newDeps := func() (*ContributionDeps, *fakeSubmitter, *fakeContributionStore) {
		sub := &fakeSubmitter{}
		st := &fakeContributionStore{owner: pauseOwner}
		return &ContributionDeps{Submitter: sub, Store: st, Hasher: auth.NewPasswordHasher(8)}, sub, st
	}
	customBody := `{"kind":"custom","endpoint":"https://gw.example/v1","api_key":"sk-0123456789abcdef","label":"我的网关"}`

	t.Run("未填 + 探到目录 → 提交成功并回显模型", func(t *testing.T) {
		deps, sub, st := newDeps()
		sub.discoverModels = []string{"deepseek-chat", "deepseek-flash"}
		w := httptest.NewRecorder()
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, customBody))
		if w.Code != http.StatusOK {
			t.Fatalf("应 200，得 %d：%s", w.Code, w.Body.String())
		}
		if len(sub.got) != 1 {
			t.Fatalf("应提交一次，得 %d", len(sub.got))
		}
		// 本次仍按"未声明"提交：判定口径不变（L0 放行 + 复检队列）。
		if len(sub.got[0].ClaimedModels) != 0 {
			t.Errorf("未填时不得把探测结果当用户声明提交，得 %v", sub.got[0].ClaimedModels)
		}
		// 探测结果作为不透明令牌传回，Submit 不必再打一次网络。
		if sub.got[0].PreProbe == nil {
			t.Error("PreProbe 令牌必须随提交传回（否则用户等待路径上会探测两次）")
		}
		// 注：本 harness 未接 deps.Sess，contributeOwner 走的是「无会话 →
		// 建 contributor」分支，所以这里不断言建账户次数；「闸门早于建账户」
		// 由下一个子测试（拿不到目录 → created==0）钉住。
		_ = st
		var env struct {
			Data struct {
				Accepted         bool     `json:"accepted"`
				DiscoveredModels []string `json:"discovered_models"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if len(env.Data.DiscoveredModels) != 2 || env.Data.DiscoveredModels[0] != "deepseek-chat" {
			t.Errorf("discovered_models 应回显探测到的目录，得 %v", env.Data.DiscoveredModels)
		}
	})

	t.Run("未填 + 拿不到目录 → 400 且不落任何库", func(t *testing.T) {
		deps, sub, st := newDeps()
		sub.discoverNeed = true
		sub.discoverReason = "端点可达且鉴权通过，但没有列出任何模型"
		w := httptest.NewRecorder()
		// 匿名请求（无会话）：这条路径原本会先建 contributor 账户。
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(customBody))
		r.Header.Set("Content-Type", "application/json")
		(&Server{}).HandleContributeSubmit(deps)(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("应 400，得 %d：%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "claimed_models") {
			t.Errorf("param 应指到 claimed_models 让前端落到那个输入框，得 %s", w.Body.String())
		}
		if len(sub.got) != 0 {
			t.Errorf("拿不到目录时不得进入提交链路，得 %d 次", len(sub.got))
		}
		// 这是本用例的核心：闸门必须在建账户之前。
		if st.contributorCreated != 0 {
			t.Errorf("退回不得留下孤儿 contributor 账户，实际建了 %d 个", st.contributorCreated)
		}
	})

	t.Run("已填 → 完全不探测（不增加延迟，判定口径不变）", func(t *testing.T) {
		deps, sub, _ := newDeps()
		w := httptest.NewRecorder()
		body := `{"kind":"custom","endpoint":"https://gw.example/v1","api_key":"sk-0123456789abcdef","claimed_models":["m1"]}`
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, body))
		if w.Code != http.StatusOK {
			t.Fatalf("应 200，得 %d：%s", w.Code, w.Body.String())
		}
		if sub.discoverCalls != 0 {
			t.Errorf("用户已声明时不该再探测，实际探了 %d 次", sub.discoverCalls)
		}
		if len(sub.got) != 1 || len(sub.got[0].ClaimedModels) != 1 {
			t.Fatalf("声明应原样提交，得 %+v", sub.got)
		}
	})

	t.Run("kind=api 未填 → 不探测（既有语义：L0 放行 + 复检队列）", func(t *testing.T) {
		deps, sub, _ := newDeps()
		w := httptest.NewRecorder()
		body := `{"kind":"api","provider":"deepseek-com","api_key":"sk-0123456789abcdef"}`
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, body))
		if w.Code != http.StatusOK {
			t.Fatalf("应 200，得 %d：%s", w.Code, w.Body.String())
		}
		if sub.discoverCalls != 0 {
			t.Errorf("API 供应商栏的空声明是既有有意语义，不该触发闸门，实际探了 %d 次", sub.discoverCalls)
		}
		if len(sub.got) != 1 || sub.got[0].PreProbe != nil {
			t.Errorf("api 路径不应带 PreProbe 令牌，得 %+v", sub.got)
		}
	})

	t.Run("探测自身故障 → 500 且不落库（不能替用户猜）", func(t *testing.T) {
		deps, sub, st := newDeps()
		sub.discoverErr = errors.New("sidecar 未就绪")
		w := httptest.NewRecorder()
		(&Server{}).HandleContributeSubmit(deps)(w, postJSON(pauseOwner, customBody))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("应 500，得 %d：%s", w.Code, w.Body.String())
		}
		if len(sub.got) != 0 || st.contributorCreated != 0 {
			t.Errorf("探测故障不得留下任何写入，得 got=%d created=%d", len(sub.got), st.contributorCreated)
		}
	})
}
