package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
)

// mw_keyauth / mw_rate / mw_quota 的契约测试（task-9，TestContract ≥8）。
//
// 断言面是**对外可见行为**：401/429/503 状态码、error.code、quota 对象、
// Retry-After 头、context 主体注入——内部实现（查表方式、扣减 SQL）归
// auth/meter/store 自己的契约测试。

// ---------------------------------------------------------------------------
// 假体：KeyAuthStore 的内存实现（auth 侧已有 fakeStore，但那个在 auth 包
// 内部不导出；httpapi 侧用独立轻量假体，形状对齐 KeyAuthStore 两读）。
// ---------------------------------------------------------------------------

type fakeKeyAuthStore struct {
	keys     map[string]*auth.APIKey // key: hex(HashToken)
	accounts map[string]*auth.Account
}

func (f *fakeKeyAuthStore) KeyByHash(_ context.Context, keyHash []byte) (*auth.APIKey, error) {
	k, ok := f.keys[fmt.Sprintf("%x", keyHash)]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *k
	return &cp, nil
}

func (f *fakeKeyAuthStore) AccountByID(_ context.Context, id string) (*auth.Account, error) {
	a, ok := f.accounts[id]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

// newM5Harness 建一条 mwKeyAuth→mwRate→mwQuota→probe 的链（不经 Handler()，
// 与 chainContext 同理：路由会把未知路径 404，探针拿不到 context）。
type m5Harness struct {
	s        *Server
	keys     *fakeKeyAuthStore
	limiter  *meter.Limiter
	debiter  *fakeDebiter
	gotCtx   context.Context
	status   int
	chain    http.Handler
	now      time.Time
	limitsOf RateLimitsSource
	modelOf  ModelIDSource
	groupOf  GroupIDsSource
}

func newM5Harness(t *testing.T) *m5Harness {
	t.Helper()
	h := &m5Harness{
		s:       New(Deps{Config: testConfig()}),
		keys:    &fakeKeyAuthStore{keys: map[string]*auth.APIKey{}, accounts: map[string]*auth.Account{}},
		limiter: meter.NewLimiter(meter.Config{}),
		debiter: &fakeDebiter{},
		now:     time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	}
	h.chain = h.build(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.gotCtx = r.Context()
		w.WriteHeader(http.StatusTeapot) // 探针标记：穿过全链 = 418
	}))
	return h
}

func (h *m5Harness) build(next http.Handler) http.Handler {
	authn := auth.NewKeyAuthenticator(h.keys).WithClock(func() time.Time { return h.now })
	// 回调走**间接层**（每次请求读 h 的当前字段），而不是构造时传值快照：
	// 测试在构造 harness 之后才设置 limitsOf/modelOf/groupOf/script，
	// 传值快照会把 nil 永久封进闭包——那批"回调不生效"的假失败就是这么来的。
	limitsOf := func(ctx context.Context, accountID string) (meter.AccountLimits, bool) {
		if h.limitsOf == nil {
			return meter.AccountLimits{}, false
		}
		return h.limitsOf(ctx, accountID)
	}
	modelOf := func(ctx context.Context, name string) (int64, bool) {
		if h.modelOf == nil {
			return 0, false
		}
		return h.modelOf(ctx, name)
	}
	groupOf := func(ctx context.Context, accountID string) ([]int64, bool) {
		if h.groupOf == nil {
			return nil, false
		}
		return h.groupOf(ctx, accountID)
	}
	return h.s.mwKeyAuth(authn)(h.s.mwRate(h.limiter, limitsOf, 10)(h.s.mwQuota(h.debiter, modelOf, groupOf, 100)(next)))
}

func (h *m5Harness) do(t *testing.T, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	h.chain.ServeHTTP(rec, req)
	h.status = rec.Code
	return rec
}

// keyBody 用 id 派生 **43** 字符 base62 体（keyWidth：32 字节熵的 base62
// 固定宽度）。之前按 32 字符写是把它误当成了"字节数"——LooksLikeKey
// 逐字符校验长度与字母表，43 才是合法形状。
func keyBody(id string) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	h := fnv32(id)
	b := make([]byte, 43)
	for i := range b {
		b[i] = alphabet[int(h)%len(alphabet)]
		h = h*1103515245 + 12345 + uint32(i)
	}
	return string(b)
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func (h *m5Harness) putKey(t *testing.T, id, accountID string, st auth.KeyStatus) string {
	t.Helper()
	plaintext := "fr-" + keyBody(id)
	k := &auth.APIKey{ID: id, AccountID: accountID, KeyHash: auth.HashToken(plaintext), Status: st, CreatedAt: h.now}
	h.keys.keys[fmt.Sprintf("%x", k.KeyHash)] = k
	return plaintext
}

func (h *m5Harness) putAccount(id string, st auth.AccountStatus, classes ...auth.Class) {
	h.keys.accounts[id] = &auth.Account{ID: id, Username: id, Status: st, Classes: auth.NewClassSet(classes...)}
}

func testConfig() *config.Config {
	return config.Default()
}

// fakeDebiter 记录扣减调用并按脚本回放错误。
type fakeDebiter struct {
	calls  []meter.DebitContext
	script func(dc meter.DebitContext) error
}

func (f *fakeDebiter) TryDebit(_ context.Context, dc meter.DebitContext) error {
	f.calls = append(f.calls, dc)
	if f.script != nil {
		return f.script(dc)
	}
	return nil
}
