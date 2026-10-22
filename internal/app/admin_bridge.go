// admin_bridge.go —— AdminDeps 窄接口 → store 仓储的桥（task-23，M7 装配）。
//
// 桥的三条纪律（与 bridges.go 相同）：
//  1. 只翻译，不判定——「什么算有效」属于 httpapi（它已在 handler 层做完
//     契约校验）；桥只做 idcode 编解码、字段逐个搬运、错误哨兵换壳。
//  2. 业务写与审计写**同事务**（06-audit §5）：AdminDeps 的写方法都带
//     audit 入参、回 auditID——实现统一走 store.WithTx（先业务后审计，
//     Appendf 用 ActionCategory* 常量拼 action），崩溃窗口为零。
//  3. 出入站 id 一律 internal/idcode：入参 Decode（前缀校验在 httpapi 已做，
//     这里解出 int64），出参 Encode（Store 行的 int64 → "acc_1" 形态）。
//
// 错误哨兵映射（集中在此一处）：
//   store.ErrNotFound / ErrGroupNotFound / ErrCredentialNotFound → httpapi.ErrAdmin*
//   store.ErrDefaultGroupProtected                              → ErrAdminDefaultGroupProtected
//   SQLite UNIQUE 冲突                                          → ErrAdminConflict
//   store 值域校验错误（含非法 kind/status 枚举）                → 原样透传（httpapi 500）
//
// 写域纪律：不碰 internal/store、internal/auth、routing_bridge.go、
// bridges.go 的 rateLimitsOf 段（authn task-22 同域并行）。

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/idcode"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// ---------------------------------------------------------------------------
// 公共小工具
// ---------------------------------------------------------------------------

// adminBridge 汇总全部子桥，构造一次、注入 httpapi.AdminDeps。
//
// 为什么持有 *store.Store 而不是 *store.Repos：写方法要开事务
// （WithTx 只在 Store 上）；读方法用 repos 快照即可。
type adminBridge struct {
	st    *store.Store
	repos *store.Repos
}

func newAdminBridge(st *store.Store) *adminBridge {
	b := &adminBridge{st: st, repos: st.Repos()}
	// 绑 absModelToAdmin 的替代名反查通道（纯翻译函数不背 receiver；
	// 每次构造重绑——测试各建各的 Store，包级单例会读到别人的库）。
	mb := &modelsBridge{b: b}
	modelsLookup = mb.replacementNameOf
	return b
}

// mapStoreErr 把 store 错误换壳成 httpapi 管理面哨兵。
// 逐条 errors.Is：桥不做文案拼接（判定的责任在 httpapi 的 mapAdminError）。
func mapStoreErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrGroupNotFound),
		errors.Is(err, store.ErrCredentialNotFound):
		return fmt.Errorf("%w: %v", httpapi.ErrAdminNotFound, err)
	case errors.Is(err, store.ErrDefaultGroupProtected):
		return httpapi.ErrAdminDefaultGroupProtected
	}
	if isDuplicateRow(err) {
		return fmt.Errorf("%w: %v", httpapi.ErrAdminConflict, err)
	}
	return err
}

// isDuplicateRow 判 SQLite 唯一约束冲突（与 console_routes.go 同判据：
// 按错误串判，不引驱动依赖——装配层不绑死 modernc）。
func isDuplicateRow(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint")
}

// decodeID 解对外 id（前缀已由 httpapi 校验，这里只取数字）。
func decodeID(prefix, s string) (int64, error) {
	return idcode.Decode(prefix, s)
}

// encodeID 是 idcode.Encode 的直通（集中一处便于 grep 审计出入站点）。
func encodeID(prefix string, id int64) string { return idcode.Encode(prefix, id) }

// adminStrPtr 是字段搬运的指针小工具（NULL 列 ↔ 指针）。
// 为什么带 admin 前缀：同包其它桥文件（后续 admin_bridge_*.go）共用这一个，
// 命名空间与 bridges.go 的 unixTime 等既有小工具隔开，grep 友好。
func adminStrPtr(v string) *string { return &v }

// jsonUnmarshal 是 json.Unmarshal 的别名（appendAudit 用；抽函数便于读）。
func jsonUnmarshal(b []byte, m *map[string]any) error { return json.Unmarshal(b, m) }

// auditInToStore 把 httpapi 的审计入参翻成 store 的行形状。
// Target 拆成 target 字符串（audit_log.target 一列即可，06-audit §2）。
func auditInToStore(e httpapi.AdminAuditEntry) (accountID *int64, actingClass, target string) {
	if e.AccountID != "" {
		if id, err := idcode.Decode(idcode.Account, e.AccountID); err == nil {
			accountID = &id
		}
	}
	actingClass = e.ActingClass
	if e.Target != nil {
		target = *e.Target
	}
	return
}

// appendAudit 在事务内写一条管理面审计（Appendf + ActionCategory* 常量）。
// 返回 auditID（对外 aud_<n> 形态）；错误原样返回——调用方决定回滚。
func appendAudit(ctx context.Context, logs *store.LogRepo, e httpapi.AdminAuditEntry) (string, error) {
	accountID, actingClass, _ := auditInToStore(e)
	action := e.Action
	if action == "" {
		action = string(store.ActionCategorySystem) + ".unknown"
	}
	detail := map[string]any{}
	if len(e.Detail) > 0 {
		var m map[string]any
		if err := jsonUnmarshal(e.Detail, &m); err == nil {
			detail = m
		}
	}
	if e.IP != "" {
		detail["ip"] = e.IP
	}
	if e.UA != "" {
		detail["ua"] = e.UA
	}
	n, err := logs.Appendf(ctx, accountID, actingClass, action, e.Result, detail)
	if err != nil {
		return "", err
	}
	return encodeID(idcode.Audit, n), nil
}
