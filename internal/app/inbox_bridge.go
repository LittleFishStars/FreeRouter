package app

import (
	"context"
	"strconv"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// inboxBridge 把 upstream 域的 InboxWriter（NotifyInbox 字符串形态）
// 接到 store.InboxRepo.Append（类型化 + 可空 relatedID）。
//
// 为什么要有这层：upstream 不 import store 的类型化事件（它只认识字符串
// 契约），store 不接受任意字符串（kind/severity 值域硬校验）。桥负责
// 两侧都有的唯一翻译：字符串 → 枚举常量、"" → nil、severity 校验错误上抛。
// 不在这里做"降级丢弃"：写不进去要能被刷新器看到并记日志，而不是静默丢事件。
type refreshInboxBridge struct {
	repo *store.InboxRepo
}

func newRefreshInboxBridge(st *store.Store) *refreshInboxBridge {
	return &refreshInboxBridge{repo: st.Repos().Inbox}
}

// NotifyInbox 实现 upstream.InboxWriter。
func (b *refreshInboxBridge) NotifyInbox(ctx context.Context, kind, relatedID, title, payloadJSON, severity string) error {
	if b == nil || b.repo == nil {
		return errInboxNotWired
	}
	var rel *int64
	if relatedID != "" {
		if id, err := strconv.ParseInt(relatedID, 10, 64); err == nil {
			rel = &id
		}
		// 解析失败不报错：相关行 id 只是导航线索，丢导航比丢整条通知好。
	}
	_, err := b.repo.Append(ctx,
		store.InboxKind(kind), store.InboxSeverity(severity), title, payloadJSON, rel)
	return err
}

// errInboxNotWired 是桥未绑定时的结构化错误（刷新器会记日志并继续）。
type inboxNotWiredErr struct{}

func (inboxNotWiredErr) Error() string { return "app: inbox 桥未装配" }

var errInboxNotWired = inboxNotWiredErr{}

var _ upstream.InboxWriter = (*refreshInboxBridge)(nil)
