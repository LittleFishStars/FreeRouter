package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// 本文件守 PATCH /api/account/keys/{id} 的 expires_at 三值语义（审计 P1）：
//
//	字段缺省 → 不变      "expires_at": null → 清空      字符串 → 设定
//
// 为什么必须有一条端到端（解码 → 判定 → 写）契约测试：用 *string 解码时 null 与
// 缺省都变成 nil，「清空」这条意图在类型上根本不存在——前端的清空按钮因此永远
// 无效，而后端每条语句单独看都是对的。测试必须把「三种语义各产生什么写入」钉死。

// fakeKeyRepo 是 auth.KeyRepository 的内存实现（只覆盖本端点的读写路径）。
//
// 为什么不复用 store 包的真实仓储：httpapi 不 import internal/store（架构边界），
// 而这里要验的是 HTTP 层的意图翻译，不是 SQL 行为（SQL 侧另有一条 store 契约测试）。
type fakeKeyRepo struct {
	byID map[string]*auth.APIKey
}

var _ auth.KeyRepository = (*fakeKeyRepo)(nil)

func newFakeKeyRepo(keys ...*auth.APIKey) *fakeKeyRepo {
	f := &fakeKeyRepo{byID: map[string]*auth.APIKey{}}
	for _, k := range keys {
		f.byID[k.ID] = k
	}
	return f
}

func (f *fakeKeyRepo) CreateKey(_ context.Context, k *auth.APIKey) error {
	f.byID[k.ID] = k
	return nil
}

func (f *fakeKeyRepo) KeyByHash(_ context.Context, _ []byte) (*auth.APIKey, error) {
	return nil, auth.ErrNotFound
}

func (f *fakeKeyRepo) KeyByID(_ context.Context, keyID string) (*auth.APIKey, error) {
	k, ok := f.byID[keyID]
	if !ok {
		return nil, auth.ErrNotFound
	}
	return k, nil
}

func (f *fakeKeyRepo) ListKeysByAccount(_ context.Context, accountID string) ([]auth.APIKey, error) {
	out := []auth.APIKey{}
	for _, k := range f.byID {
		if k.AccountID == accountID {
			out = append(out, *k)
		}
	}
	return out, nil
}

func (f *fakeKeyRepo) SetKeyStatus(_ context.Context, keyID string, status auth.KeyStatus) error {
	if k, ok := f.byID[keyID]; ok {
		k.Status = status
	}
	return nil
}

func (f *fakeKeyRepo) RenameKey(_ context.Context, keyID, name string) error {
	if k, ok := f.byID[keyID]; ok {
		k.Name = name
	}
	return nil
}

func (f *fakeKeyRepo) RotateKey(_ context.Context, _ string, _ *auth.APIKey) error {
	return auth.ErrNotFound // 本文件的端点不涉及轮换
}

func (f *fakeKeyRepo) SetKeyAllowlist(_ context.Context, _ string, _ []string) error { return nil }

func (f *fakeKeyRepo) KeyAllowlist(_ context.Context, _ string) ([]string, error) { return nil, nil }

// keyPatchHarness 把服务器、路由与 fake 仓储拼起来。
type keyPatchHarness struct {
	handler http.Handler
	repo    *fakeKeyRepo
	// expiryCalls 记录每次 SetKeyExpiry 的参数（nil = 清空），顺序即调用顺序。
	expiryCalls []*time.Time
}

func newKeyPatchHarness(t *testing.T, keys ...*auth.APIKey) *keyPatchHarness {
	t.Helper()
	s := New(Deps{Config: config.Default(), Logger: discardLogger()})
	repo := newFakeKeyRepo(keys...)
	h := &keyPatchHarness{repo: repo}
	deps := &AccountKeysDeps{
		Manager: auth.NewKeyManager(auth.KeyPolicy{}, auth.Stores{Keys: repo}),
		SetKeyExpiry: func(_ context.Context, keyID string, at *time.Time) error {
			h.expiryCalls = append(h.expiryCalls, at)
			// 与真实仓储同语义：nil = 写 NULL（永不过期）。
			if k, ok := repo.byID[keyID]; ok {
				k.ExpiresAt = at
			}
			return nil
		},
	}
	r := chi.NewRouter()
	r.Patch("/api/account/keys/{id}", s.HandleAccountKeyPatch(deps))
	h.handler = r
	return h
}

// patch 发一次 PATCH，主体固定为 acc-1（会话中间件已把 Principal 放进 ctx）。
func (h *keyPatchHarness) patch(id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, "/api/account/keys/"+id, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxKeySessionPrincipal, &auth.Principal{
		AccountID:   "acc-1",
		DisplayName: "租户",
		Classes:     auth.NewClassSet(auth.ClassTenant),
		ActingClass: auth.ClassTenant,
	}))
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// decodeKeyEnvelope 取 {data:{...}} 里的 key 对象。
func decodeKeyEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得 %d：%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（原文 %s）", err, rec.Body.String())
	}
	if env.Data == nil {
		t.Fatalf("响应缺少 data 对象：%s", rec.Body.String())
	}
	return env.Data
}

func newTestKey(id string) *auth.APIKey {
	return &auth.APIKey{
		ID:        id,
		AccountID: "acc-1",
		Prefix:    "fr-abcd…wxyz",
		Name:      "测试",
		Status:    auth.KeyActive,
	}
}

// TestContractKeyPatchExpiresAtThreeStates 是 P1 的主契约测试：
// 设定 → 清空 → 缺省不动（缺省那一步之前先重新设定，才能证明「不动」是真的没动）。
func TestContractKeyPatchExpiresAtThreeStates(t *testing.T) {
	h := newKeyPatchHarness(t, newTestKey("key-1"))

	// —— 1) 字符串 = 设定 ——
	first := "2026-12-01T00:00:00Z"
	data := decodeKeyEnvelope(t, h.patch("key-1", `{"expires_at":"`+first+`"}`))
	if data["expires_at"] != first {
		t.Fatalf("设定后 expires_at = %v，期望 %s", data["expires_at"], first)
	}
	if len(h.expiryCalls) != 1 || h.expiryCalls[0] == nil {
		t.Fatalf("设定应当以非 nil 调用一次 SetKeyExpiry，实际 %d 次 / %v", len(h.expiryCalls), h.expiryCalls)
	}
	if got := h.expiryCalls[0].UTC().Format(time.RFC3339); got != first {
		t.Fatalf("写入的时刻 = %s，期望 %s", got, first)
	}

	// —— 2) null = 清空 ——
	data = decodeKeyEnvelope(t, h.patch("key-1", `{"expires_at":null}`))
	if data["expires_at"] != nil {
		t.Fatalf("清空后 expires_at = %v，期望 null（永不过期）", data["expires_at"])
	}
	if len(h.expiryCalls) != 2 || h.expiryCalls[1] != nil {
		t.Fatalf("清空必须以 nil 调用一次 SetKeyExpiry，实际 %d 次 / %v", len(h.expiryCalls), h.expiryCalls)
	}

	// —— 3) 空串 = 清空（旧表单兼容）——
	//     先重新设定，否则「清空一个已清空的值」分不清是兼容生效还是压根没写。
	second := "2027-01-01T00:00:00Z"
	if data = decodeKeyEnvelope(t, h.patch("key-1", `{"expires_at":"`+second+`"}`)); data["expires_at"] != second {
		t.Fatalf("重新设定失败，expires_at = %v", data["expires_at"])
	}
	data = decodeKeyEnvelope(t, h.patch("key-1", `{"expires_at":""}`))
	if data["expires_at"] != nil {
		t.Fatalf("空串应当按清空处理，得 %v", data["expires_at"])
	}

	// —— 4) 缺省 = 不变 ——
	//     先设成 third，再只改 name：expires_at 不能动，SetKeyExpiry 也不能被调。
	third := "2027-02-01T00:00:00Z"
	if data = decodeKeyEnvelope(t, h.patch("key-1", `{"expires_at":"`+third+`"}`)); data["expires_at"] != third {
		t.Fatalf("设定第三段时间失败，expires_at = %v", data["expires_at"])
	}
	callsBefore := len(h.expiryCalls)
	data = decodeKeyEnvelope(t, h.patch("key-1", `{"name":"改个名"}`))
	if data["expires_at"] != third {
		t.Fatalf("缺省 expires_at 时不应改动，得 %v，期望 %s", data["expires_at"], third)
	}
	if data["name"] != "改个名" {
		t.Fatalf("name 应当被改，得 %v", data["name"])
	}
	if len(h.expiryCalls) != callsBefore {
		t.Fatalf("缺省 expires_at 时不应调用 SetKeyExpiry，实际多调了 %d 次", len(h.expiryCalls)-callsBefore)
	}
}

// TestContractKeyPatchExpiresAtRejects 守两条不该写库的分支：
// 非法时刻 → 400（param=expires_at）；他人的 Key → 404 且一次写入都不发生。
func TestContractKeyPatchExpiresAtRejects(t *testing.T) {
	other := newTestKey("key-other")
	other.AccountID = "acc-2" // 别人的 Key：写路径必须按「不存在」拒绝
	h := newKeyPatchHarness(t, newTestKey("key-1"), other)

	// 非法时刻：400 + param 指到字段上（前端表单能高亮到具体输入框）。
	rec := h.patch("key-1", `{"expires_at":"明年"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法时刻应 400，得 %d：%s", rec.Code, rec.Body.String())
	}
	if e := decodeError(t, rec); e["code"] != "invalid_request" || e["param"] != "expires_at" {
		t.Fatalf("错误体 = %v，期望 invalid_request/expires_at", e)
	}
	if len(h.expiryCalls) != 0 {
		t.Fatalf("非法时刻不应写库，实际调用 %d 次", len(h.expiryCalls))
	}

	// 他人的 Key：404（不泄露存在性），且同样一次写入都不发生。
	rec = h.patch("key-other", `{"expires_at":null}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("改他人 Key 的过期时间应 404，得 %d：%s", rec.Code, rec.Body.String())
	}
	if len(h.expiryCalls) != 0 {
		t.Fatalf("改他人 Key 不应写库，实际调用 %d 次", len(h.expiryCalls))
	}
	// 原值必须完好无损（写路径没被「先写后判」）。
	if other.ExpiresAt != nil {
		t.Fatalf("他人 Key 的 expires_at 被改动了：%v", other.ExpiresAt)
	}
}
