package auth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 内存假体（Contract 测试替身）
// ---------------------------------------------------------------------------
//
// 为什么在测试里而不是生产代码：storeiface 的四个窄接口就是为"先定契约、
// 后接存储"设计的（storage 分支在另一条线上实现同一组接口）。假体只保证
// **语义**（ErrNotFound 约定、拷贝返回、revoked 单向），不模拟并发事务——
// 那是 storage 侧 contract 测试的职责。

type fakeStore struct {
	mu       sync.Mutex
	accounts map[string]*Account // by ID
	sessions map[string]*Session // by hex-less string key of tokenHash
	keys     map[string]*APIKey  // by ID
	allow    map[string][]string // keyID → 白名单
	groups   map[string][]GroupPolicy
	clock    *time.Time // 与测试共享的"现在"
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		accounts: make(map[string]*Account),
		sessions: make(map[string]*Session),
		keys:     make(map[string]*APIKey),
		allow:    make(map[string][]string),
		groups:   make(map[string][]GroupPolicy),
	}
}

func hashKey(b []byte) string { return fmt.Sprintf("%x", b) }

// ---- AccountRepository ----

func (f *fakeStore) AccountByID(_ context.Context, id string) (*Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.accounts[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *a
	cp.Classes = a.Classes.Union(nil)
	return &cp, nil
}

func (f *fakeStore) AccountByUsername(_ context.Context, username string) (*Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.accounts {
		if a.Username == username {
			cp := *a
			cp.Classes = a.Classes.Union(nil)
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeStore) TouchLastLogin(_ context.Context, accountID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.accounts[accountID]; ok {
		t := at
		a.LastLoginAt = &t
	}
	return nil
}

func (f *fakeStore) UpdatePasswordHash(_ context.Context, accountID, argonHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.accounts[accountID]
	if !ok {
		return ErrNotFound
	}
	a.PasswordHash = argonHash
	return nil
}

// ---- SessionRepository ----

func (f *fakeStore) CreateSession(_ context.Context, s *Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[hashKey(s.TokenHash)] = s
	return nil
}

func (f *fakeStore) SessionByTokenHash(_ context.Context, tokenHash []byte) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[hashKey(tokenHash)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, s := range f.sessions {
		if s.ID == sessionID {
			delete(f.sessions, k)
		}
	}
	return nil
}

func (f *fakeStore) DeleteSessionsByAccount(_ context.Context, accountID, keepSessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, s := range f.sessions {
		if s.AccountID == accountID && s.ID != keepSessionID {
			delete(f.sessions, k)
		}
	}
	return nil
}

// ---- KeyRepository ----

func (f *fakeStore) CreateKey(_ context.Context, k *APIKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *k
	f.keys[k.ID] = &cp
	return nil
}

func (f *fakeStore) KeyByHash(_ context.Context, keyHash []byte) (*APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if hashKey(k.KeyHash) == hashKey(keyHash) {
			cp := *k
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeStore) KeyByID(_ context.Context, keyID string) (*APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[keyID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *k
	return &cp, nil
}

func (f *fakeStore) ListKeysByAccount(_ context.Context, accountID string) ([]APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []APIKey
	for _, k := range f.keys {
		if k.AccountID == accountID {
			out = append(out, *k)
		}
	}
	return out, nil
}

func (f *fakeStore) SetKeyStatus(_ context.Context, keyID string, status KeyStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[keyID]
	if !ok {
		return ErrNotFound
	}
	// revoked 单向由**本层**（KeyManager）守住；假体照写，模拟真实存储的宽松 CHECK。
	k.Status = status
	return nil
}

func (f *fakeStore) RenameKey(_ context.Context, keyID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.keys[keyID]; ok {
		k.Name = name
	}
	return nil
}

func (f *fakeStore) RotateKey(ctx context.Context, oldKeyID string, newKey *APIKey) error {
	f.mu.Lock()
	_, ok := f.keys[oldKeyID]
	f.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if err := f.SetKeyStatus(ctx, oldKeyID, KeyRevoked); err != nil {
		return err
	}
	return f.CreateKey(ctx, newKey)
}

func (f *fakeStore) SetKeyAllowlist(_ context.Context, keyID string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.keys[keyID]; !ok {
		return ErrNotFound
	}
	cp := make([]string, len(ids))
	copy(cp, ids)
	f.allow[keyID] = cp
	return nil
}

func (f *fakeStore) KeyAllowlist(_ context.Context, keyID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.allow[keyID]...), nil
}

// ---- PolicyRepository ----

func (f *fakeStore) GroupsForAccount(_ context.Context, accountID string) ([]GroupPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]GroupPolicy(nil), f.groups[accountID]...), nil
}

func (f *fakeStore) stores() Stores {
	return Stores{Accounts: f, Sessions: f, Keys: f, Policies: f}
}

// ---- 测试辅助 ----

func (f *fakeStore) putAccount(a *Account) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[a.ID] = a
}

func (f *fakeStore) setStatus(id string, st AccountStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[id].Status = st
}

func boolp(b bool) *bool { return &b }
func intp(i int) *int    { return &i }

// testClock 返回可控时钟：测试推进"现在"而不 sleep。
func testClock() (*fakeStore, func(time.Duration)) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f := newFakeStore()
	f.clock = &now
	return f, func(d time.Duration) { now = now.Add(d) }
}
