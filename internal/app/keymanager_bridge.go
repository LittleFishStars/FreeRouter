package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// keymanager_bridge.go —— auth.KeyRepository 的实现（store.KeyRepo 适配）。
//
// 为什么要这一层：auth 的 Key 面用字符串 id 与 time.Time（会话面统一口径），
// store 用 int64 与 unix 秒（持久层统一口径）。桥只做两侧的机械翻译，
// 判定（数量上限/归属/白名单收窄）全在 auth.KeyManager 里，不在桥里。
//
// 为什么不用 keyAuthBridge：那个桥只实现了鉴权需要的 KeyByHash 一条读路径；
// Key 生命周期需要写与列表，职责不同（只读 vs 读写合并会让鉴权面吃到写接口）。

type keymanagerBridge struct {
	keys   *store.KeyRepo
	models *store.ModelRepo
}

func newKeyManagerBridge(st *store.Store) *keymanagerBridge {
	return &keymanagerBridge{keys: st.Repos().Keys, models: st.Repos().Models}
}

func (b *keymanagerBridge) CreateKey(ctx context.Context, k *auth.APIKey) error {
	id, err := b.keys.CreateKey(ctx, store.APIKey{
		AccountID: mustParseID(k.AccountID),
		Prefix:    k.Prefix,
		Name:      ptrString(k.Name),
		Status:    store.KeyStatus(k.Status),
		ExpiresAt: ptrUnix(k.ExpiresAt),
	}, k.KeyHash)
	if err != nil {
		return err
	}
	k.ID = strconv.FormatInt(id, 10)
	return nil
}

func (b *keymanagerBridge) KeyByHash(ctx context.Context, keyHash []byte) (*auth.APIKey, error) {
	k, err := b.keys.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, err
	}
	return toAuthKey(k), nil
}

func (b *keymanagerBridge) KeyByID(ctx context.Context, keyID string) (*auth.APIKey, error) {
	id, err := strconv.ParseInt(keyID, 10, 64)
	if err != nil {
		return nil, store.ErrNotFound
	}
	k, err := b.keys.GetKey(ctx, id)
	if err != nil {
		return nil, err
	}
	return toAuthKey(k), nil
}

func (b *keymanagerBridge) ListKeysByAccount(ctx context.Context, accountID string) ([]auth.APIKey, error) {
	rows, err := b.keys.ListKeysByAccount(ctx, mustParseID(accountID))
	if err != nil {
		return nil, err
	}
	out := make([]auth.APIKey, 0, len(rows))
	for i := range rows {
		out = append(out, *toAuthKey(&rows[i]))
	}
	return out, nil
}

func (b *keymanagerBridge) SetKeyStatus(ctx context.Context, keyID string, status auth.KeyStatus) error {
	id, err := strconv.ParseInt(keyID, 10, 64)
	if err != nil {
		return store.ErrNotFound
	}
	return b.keys.SetKeyStatus(ctx, id, store.KeyStatus(status))
}

// RenameKey 只改展示名（不影响鉴权）。store 没有专门的改名方法：
// 名字在 api_keys.name，一次 UPDATE 足够；放桥里而不是让 store 多一个
// 只被这里用一次的方法。
func (b *keymanagerBridge) RenameKey(ctx context.Context, keyID, name string) error {
	id, err := strconv.ParseInt(keyID, 10, 64)
	if err != nil {
		return store.ErrNotFound
	}
	return b.keys.SetKeyName(ctx, id, ptrString(name))
}

// RotateKey 在一个事务里吊销旧 Key 并写入新 Key（auth 侧已在同一方法里
// 构造好 newKey；这里只负责两笔写的原子性）。
func (b *keymanagerBridge) RotateKey(ctx context.Context, oldKeyID string, newKey *auth.APIKey) error {
	oldID, err := strconv.ParseInt(oldKeyID, 10, 64)
	if err != nil {
		return store.ErrNotFound
	}
	id, err := b.keys.CreateKey(ctx, store.APIKey{
		AccountID: mustParseID(newKey.AccountID),
		Prefix:    newKey.Prefix,
		Name:      ptrString(newKey.Name),
		Status:    store.KeyStatus(newKey.Status),
		ExpiresAt: ptrUnix(newKey.ExpiresAt),
	}, newKey.KeyHash)
	if err != nil {
		return err
	}
	newKey.ID = strconv.FormatInt(id, 10)
	return b.keys.SetKeyStatus(ctx, oldID, store.KeyRevoked)
}

// SetKeyAllowlist 写白名单。
//
// 白名单两侧的语言是**抽象模型名**（auth.KeyManager.SetAllowlist 用组授权的
// 名字集合做 ⊆ 校验），store 存的是 id——桥负责名字↔id 翻译。
// 旧实现把名字字符串直接 strconv.ParseInt，全部 parse 失败后白名单恒为空
// （租户设了等于没设，静默失效）；这里显式查表，名字不存在时报错而不是吞掉。
func (b *keymanagerBridge) SetKeyAllowlist(ctx context.Context, keyID string, abstractModelNames []string) error {
	id, err := strconv.ParseInt(keyID, 10, 64)
	if err != nil {
		return store.ErrNotFound
	}
	ids := make([]int64, 0, len(abstractModelNames))
	for _, name := range abstractModelNames {
		m, err := b.models.GetAbstractModelByName(ctx, name)
		if err != nil {
			return fmt.Errorf("app: 白名单模型 %q 不存在: %w", name, err)
		}
		ids = append(ids, m.ID)
	}
	return b.keys.SetKeyAllowlist(ctx, id, ids)
}

// KeyAllowlist 读白名单（翻译成模型名，与 auth 层/前端的语言一致）。
func (b *keymanagerBridge) KeyAllowlist(ctx context.Context, keyID string) ([]string, error) {
	id, err := strconv.ParseInt(keyID, 10, 64)
	if err != nil {
		return nil, store.ErrNotFound
	}
	ids, err := b.keys.KeyAllowlist(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, v := range ids {
		if m, err := b.models.GetAbstractModel(ctx, v); err == nil {
			out = append(out, m.Name)
		} else {
			// 引用的模型已被删：保留占位而不是静默少一行（界面上看得见异常）。
			out = append(out, fmt.Sprintf("#%d", v))
		}
	}
	return out, nil
}

var _ auth.KeyRepository = (*keymanagerBridge)(nil)

func toAuthKey(k *store.APIKey) *auth.APIKey {
	out := &auth.APIKey{
		ID:        strconv.FormatInt(k.ID, 10),
		AccountID: strconv.FormatInt(k.AccountID, 10),
		Prefix:    k.Prefix,
		Status:    auth.KeyStatus(k.Status),
	}
	if k.Name != nil {
		out.Name = *k.Name
	}
	if k.ExpiresAt != nil {
		t := time.Unix(*k.ExpiresAt, 0).UTC()
		out.ExpiresAt = &t
	}
	return out
}

func mustParseID(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func ptrString(s string) *string { return &s }

func ptrUnix(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}
