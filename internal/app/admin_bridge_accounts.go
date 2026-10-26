// admin_bridge_accounts.go —— AdminAccountsStore → store 账户/会话/凭据仓储（task-23）。
//
// 已知 store 缺口（回报 Lead 转达 storage，TODO 占位）：
//   ① ListAccounts（带 class/status/group_id 筛选 + 分页 + 总数）——
//      AccountRepo 只有 CountAccounts/GetAccount*/SetAccountStatus 等单体方法；
//   ② SetAccountClasses（整组替换权限类）——只有 AddClass 单向追加；
//   ③ ListAccountGroups / SetAccountGroups（组的集合读写）——组成员关系
//      在 GroupRepo 侧按组查询，没有按账户聚合的读，也没有按账户整组替换的写。
// 本文件的实现：读侧用「全量 ListGroups→逐组列成员」聚合出 group 视图（单机
// SQLite 上组数是小表，可接受）；写侧（PATCH 改类/组）当前退化为**仅支持
// status/显示名/备注/速率/密码**，classes 与 groups 字段给了返回
// ErrAdminNotWired 语义的明确错误（不静默忽略——§9 同一条理由）。
// 全量筛选列表同样退化为「无筛选全量 + 内存过滤」（同样小表可用），
// 正式的 SQL 筛选等 storage 落方法后替换。

package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// accountsBridge 实现 httpapi.AdminAccountsStore。
type accountsBridge struct{ b *adminBridge }

// errAdminNotImplemented 是 store 缺方法时的显式占位错误（映射 404——
// 与「未装配能力不确认存在」同一口径；消息进服务器日志定位）。
var errAdminNotImplemented = fmt.Errorf("admin 桥占位：%w", httpapi.ErrAdminNotWired)

// accountToAdmin 把 store.Account 翻成管理面 DTO（id 编码 + 类转字符串数组）。
func accountToAdmin(a *store.Account) *httpapi.AdminAccount {
	out := &httpapi.AdminAccount{
		ID:             a.ID,
		DisplayName:    a.DisplayName,
		Username:       a.Username,
		Status:         string(a.Status),
		Groups:         []string{}, // ListAccounts 填充；GetAccount 也聚合
		RPMLimit:       a.RPMLimit,
		TPMLimit:       a.TPMLimit,
		MaxConcurrency: a.MaxConcurrency,
		CreatedAt:      a.CreatedAt,
	}
	if a.LastLoginAt != nil {
		out.LastLoginAt = a.LastLoginAt
	}
	if a.Note != nil {
		out.Note = *a.Note
	}
	classes := make([]string, 0, len(a.Classes))
	for _, c := range a.Classes {
		classes = append(classes, string(c))
	}
	out.Classes = classes
	return out
}

// accountGroups 聚合账户所属组（读侧兜底：无 ListAccountGroups 方法）。
// 单机 SQLite 上组表是小表，全量列组再对成员做包含判断，成本可忽略；
// 正式的按账户 JOIN 查询等 storage 落方法后替换（TODO 占位口径见文件头）。
func (ab *accountsBridge) accountGroups(ctx context.Context, accountID string) ([]string, error) {
	groups, err := ab.b.repos.Groups.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, g := range groups {
		members, err := ab.b.repos.Groups.ListMembers(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if m == accountID {
				out = append(out, g.ID)
				break
			}
		}
	}
	return out, nil
}

func (ab *accountsBridge) ListAccounts(ctx context.Context, f httpapi.AdminAccountFilter) ([]httpapi.AdminAccount, int64, error) {
	// TODO(storage)：正式 ListAccounts（SQL 筛选 + LIMIT/OFFSET + COUNT）。
	// 占位实现：全量 + 内存过滤。accounts 表量级（全站账户）在小规模下
	// 可接受；规模上来前 storage 会落真方法（缺口已回报）。
	rows := ab.allAccounts(ctx)
	var filtered []httpapi.AdminAccount
	for i := range rows {
		row := rows[i]
		if f.Status != "" && row.Status != f.Status {
			continue
		}
		if f.Class != "" && !containsString(row.Classes, f.Class) {
			continue
		}
		if f.GroupID != "" && !containsString(row.Groups, f.GroupID) {
			continue
		}
		filtered = append(filtered, row)
	}
	total := int64(len(filtered))
	lo, hi := pageBounds(len(filtered), f.Limit, f.Offset)
	return filtered[lo:hi], total, nil
}

// allAccounts 读全量并填充派生字段。
func (ab *accountsBridge) allAccounts(ctx context.Context) []httpapi.AdminAccount {
	groups, _ := ab.b.repos.Groups.ListGroups(ctx)
	// 组→成员一次读全（ListGroups 是小表；每组一次 ListMembers）。
	groupOf := map[string][]string{} // accountID → groupIDs
	for _, g := range groups {
		members, err := ab.b.repos.Groups.ListMembers(ctx, g.ID)
		if err != nil {
			continue
		}
		for _, m := range members {
			groupOf[m] = append(groupOf[m], g.ID)
		}
	}
	accounts := []httpapi.AdminAccount{}
	// 一次真实查询列出全部账户。旧实现是「CountAccounts + GetAccount(1..n+32)
	// 逐个试」的稀疏 id 扫描——UUID 主键下没有连续区间可扫，那套写法一条都
	// 取不到（2026-10-26 改造带出的真实缺口，见 ListAllAccounts 的注释）。
	all, err := ab.b.repos.Accounts.ListAllAccounts(ctx)
	if err != nil {
		return accounts
	}
	for _, a := range all {
		row := accountToAdmin(a)
		gs := []string{}
		for _, gid := range groupOf[a.ID] {
			gs = append(gs, gid)
		}
		row.Groups = gs
		ab.fillQuotaOnly(ctx, row, a.ID)
		accounts = append(accounts, *row)
	}
	return accounts
}

// fillQuotaOnly 只填额度对象（allAccounts 内组已另行聚合）。
func (ab *accountsBridge) fillQuotaOnly(ctx context.Context, row *httpapi.AdminAccount, id string) {
	best := (*httpapi.AdminQuota)(nil)
	for _, u := range []store.QuotaUnit{store.UnitRequest, store.UnitToken, store.UnitCredit} {
		for _, p := range []store.QuotaPeriod{store.PeriodDay, store.PeriodMonth} {
			q, err := ab.b.repos.Quotas.AccountQuota(ctx, id, store.TotalScope, u, p)
			if err != nil || q == nil {
				continue
			}
			cand := &httpapi.AdminQuota{Unit: string(u), Limit: q.Limit, Used: q.Used, Period: string(p)}
			if best == nil || cand.Limit < best.Limit {
				best = cand
			}
		}
	}
	row.Quota = best
}

// pageBounds 计算内存分页区间（clamp 到 [0,len]）。
func pageBounds(n, limit, offset int) (lo, hi int) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	lo = offset
	if lo > n {
		lo = n
	}
	hi = lo + limit
	if hi > n {
		hi = n
	}
	return
}

func (ab *accountsBridge) GetAccount(ctx context.Context, id string) (*httpapi.AdminAccount, error) {
	nid, err := decodeID(id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	a, err := ab.b.repos.Accounts.GetAccount(ctx, nid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	row := accountToAdmin(a)
	if gs, err := ab.accountGroups(ctx, nid); err == nil {
		row.Groups = gs
	}
	ab.fillQuotaOnly(ctx, row, nid)
	return row, nil
}

func (ab *accountsBridge) CreateAccount(ctx context.Context, in httpapi.AdminAccount, passwordHash string, audit httpapi.AdminAuditEntry) (string, string, error) {
	classes := make([]store.Class, 0, len(in.Classes))
	for _, c := range in.Classes {
		classes = append(classes, store.Class(c))
	}
	status := store.AccountStatus(in.Status)
	if status == "" {
		status = store.AccountActive // 管理员建号默认 active（§5.3 同口径）
	}
	inAuth := store.AccountAuth{
		Account: store.Account{
			DisplayName: in.DisplayName,
			Username:    in.Username,
			Status:      status,
		},
		PasswordHash: passwordHash,
	}
	if in.Note != "" {
		inAuth.Account.Note = adminStrPtr(in.Note)
	}
	inAuth.Account.RPMLimit = in.RPMLimit
	inAuth.Account.TPMLimit = in.TPMLimit
	inAuth.Account.MaxConcurrency = in.MaxConcurrency
	var newID string
	var auditID string
	err := ab.b.st.WithTx(ctx, func(tx *store.Repos) error {
		id, err := tx.Accounts.CreateAccount(ctx, inAuth, classes)
		if err != nil {
			return err
		}
		newID = id
		// 组成员落位（§5：新建可带组；无组则默认组机制由 storage 兜底）。
		for _, g := range in.Groups {
			gid, derr := decodeID(g)
			if derr != nil {
				return derr
			}
			if err := tx.Groups.AddMember(ctx, gid, id); err != nil {
				return err
			}
		}
		if in.Quota != nil {
			if err := tx.Quotas.SetAccountQuota(ctx, id, store.TotalScope,
				store.QuotaUnit(in.Quota.Unit), store.QuotaPeriod(in.Quota.Period),
				in.Quota.Limit); err != nil {
				return err
			}
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return "", "", mapStoreErr(err)
	}
	return newID, auditID, nil
}

func (ab *accountsBridge) UpdateAccount(ctx context.Context, id string, in httpapi.AdminAccountUpdate, audit httpapi.AdminAuditEntry) (*httpapi.AdminAccount, string, error) {
	nid, err := decodeID(id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	// classes / groups 的整组替换：store 已落 SetAccountClasses /
	// SetAccountGroups，这里接线（此前显式报未接线 → 管理面改类 404）。
	// 两者都在同一个 WithTx 里，不会出现「类改了、组没改」的半套状态。
	var auditID string
	err = ab.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if in.Status != nil {
			if err := tx.Accounts.SetAccountStatus(ctx, nid, store.AccountStatus(*in.Status)); err != nil {
				return err
			}
		}
		if in.Classes != nil {
			classes := make([]store.Class, 0, len(*in.Classes))
			for _, c := range *in.Classes {
				classes = append(classes, store.Class(c))
			}
			if err := tx.Accounts.SetAccountClasses(ctx, nid, classes); err != nil {
				return err
			}
		}
		if in.Groups != nil {
			gids := make([]string, 0, len(*in.Groups))
			for _, g := range *in.Groups {
				gid, derr := decodeID(g)
				if derr != nil {
					return derr
				}
				gids = append(gids, gid)
			}
			if err := tx.Groups.SetAccountGroups(ctx, nid, gids); err != nil {
				return err
			}
		}
		// 显示名/备注：store 已落 SetAccountProfile（nil 列不动）。
		if in.DisplayName != nil || in.Note != nil {
			var note *string
			if in.Note != nil {
				note = in.Note
			}
			if err := tx.Accounts.SetAccountProfile(ctx, nid, in.DisplayName, note); err != nil {
				return err
			}
		}
		if in.RPMLimit != nil || in.TPMLimit != nil || in.MaxConcurrency != nil {
			a, err := tx.Accounts.GetAccount(ctx, nid)
			if err != nil {
				return err
			}
			rpm, tpm, mc := a.RPMLimit, a.TPMLimit, a.MaxConcurrency
			if in.RPMLimit != nil {
				rpm = *in.RPMLimit
			}
			if in.TPMLimit != nil {
				tpm = *in.TPMLimit
			}
			if in.MaxConcurrency != nil {
				mc = *in.MaxConcurrency
			}
			if err := tx.Accounts.SetAccountRateLimits(ctx, nid, rpm, tpm, mc); err != nil {
				return err
			}
		}
		if in.Quota != nil {
			if err := tx.Quotas.SetAccountQuota(ctx, nid, store.TotalScope,
				store.QuotaUnit(in.Quota.Unit), store.QuotaPeriod(in.Quota.Period),
				in.Quota.Limit); err != nil {
				return err
			}
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	row, err := ab.GetAccount(ctx, id)
	if err != nil {
		return nil, auditID, nil // 业务已成功；读回失败不谎报写失败
	}
	return row, auditID, nil
}

func (ab *accountsBridge) ResetPassword(ctx context.Context, id, passwordHash string, revoke bool, audit httpapi.AdminAuditEntry) (int, string, error) {
	nid, err := decodeID(id)
	if err != nil {
		return 0, "", errAdminNotImplemented
	}
	revoked := 0
	var auditID string
	err = ab.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Accounts.UpdatePasswordHash(ctx, nid, passwordHash); err != nil {
			return err
		}
		// 管理员重置出来的是一次性口令（界面回显一次）：下次登录必须先改密，
		// 否则同一串会随聊天/工单长期留存成为共享凭据。
		if err := tx.Accounts.SetMustChangePassword(ctx, nid, true); err != nil {
			return err
		}
		if revoke {
			if err := tx.Sessions.DeleteSessionsByAccount(ctx, nid); err != nil {
				return err
			}
			revoked = -1 // DeleteSessionsByAccount 不回数；-1 表达「已全删、数未知」
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return 0, "", mapStoreErr(err)
	}
	return revoked, auditID, nil
}

func (ab *accountsBridge) RevokeSessions(ctx context.Context, id string, audit httpapi.AdminAuditEntry) (int, string, error) {
	nid, err := decodeID(id)
	if err != nil {
		return 0, "", errAdminNotImplemented
	}
	var auditID string
	err = ab.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Sessions.DeleteSessionsByAccount(ctx, nid); err != nil {
			return err
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return 0, "", mapStoreErr(err)
	}
	return -1, auditID, nil // -1：已吊销全部，行数未知（store 未回数）
}

// ---------------------------------------------------------------------------
// Key 计数桥（§5.2：列表只给数量）
// ---------------------------------------------------------------------------

// keysCountBridge 实现 httpapi.AdminKeyCountStore。
type keysCountBridge struct{ b *adminBridge }

func (kb *keysCountBridge) CountKeys(ctx context.Context, accountID string) (int64, error) {
	nid, err := decodeID(accountID)
	if err != nil {
		return 0, err
	}
	keys, err := kb.b.repos.Keys.ListKeysByAccount(ctx, nid)
	if err != nil {
		return 0, err
	}
	return int64(len(keys)), nil
}

// ---------------------------------------------------------------------------
// CSV 导入（httpapi.ImportStore）
// ---------------------------------------------------------------------------

// ImportAccount 建一个导入账户：active + tenant 类 + 组 + 额度，同一事务。
//
// 为什么单独一个方法而不复用 CreateAccount：导入的默认权限类是 tenant
// （01-accounts §2：管理员导入授予调用能力），而单体创建的类集合由请求
// 指定、状态默认 active 但可给 pending。两者混在一个方法里会让「导入是否
// 可能造出 pending 账户」这种问题需要读代码才能回答。
//
// 组：给了用给的（名字非法即失败，该行计入失败数）；没给进默认组——
// 无组账户的可见模型集合为空，等价于「导入了一堆登进去啥也干不了的账户」。
func (ab *accountsBridge) ImportAccount(ctx context.Context, row httpapi.ImportRow, audit httpapi.AdminAuditEntry) (string, error) {
	if row.PasswordHash == "" {
		return "", errors.New("store: 导入账户必须带口令哈希（生成在 handler 侧）")
	}
	gid, err := ab.b.st.EnsureDefaultGroup(ctx, 0)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(row.Group) != "" {
		parsed, derr := decodeID(row.Group)
		if derr != nil {
			return "", fmt.Errorf("组 id 非法 %q: %w", row.Group, derr)
		}
		gid = parsed
	}
	var newID string
	err = ab.b.st.WithTx(ctx, func(tx *store.Repos) error {
		id, cerr := tx.Accounts.CreateAccount(ctx, store.AccountAuth{
			Account: store.Account{
				DisplayName: row.DisplayName,
				Username:    row.Username,
				Status:      store.AccountActive,
			},
			PasswordHash: row.PasswordHash,
		}, []store.Class{store.ClassTenant})
		if cerr != nil {
			return cerr
		}
		newID = id
		if err := tx.Groups.AddMember(ctx, gid, id); err != nil {
			return err
		}
		if row.QuotaUnit != "" && row.QuotaLimit != nil {
			if err := tx.Quotas.SetAccountQuota(ctx, id, store.TotalScope,
				store.QuotaUnit(row.QuotaUnit), store.PeriodDay, *row.QuotaLimit); err != nil {
				return err
			}
		}
		_, aerr := appendAudit(ctx, tx.Logs, audit)
		return aerr
	})
	if err != nil {
		return "", mapStoreErr(err)
	}
	return newID, nil
}

// ImportBatchAudit 记一条批次事件（06-audit：操作者、行数、成功/失败数）。
//
// 为什么批次与逐行都要记：逐行事件回答「alice 这个账户是谁建的」，
// 批次事件回答「那次操作一共造了多少账户」——后者是异常批量开户的唯一线索。
func (ab *accountsBridge) ImportBatchAudit(ctx context.Context, audit httpapi.AdminAuditEntry) (string, error) {
	aid, err := appendAudit(ctx, ab.b.repos.Logs, audit)
	if err != nil {
		return "", mapStoreErr(err)
	}
	return aid, nil
}

// 编译期断言：accountsBridge 同时是账户面与导入面的实现。
var _ httpapi.ImportStore = (*accountsBridge)(nil)

// ---------------------------------------------------------------------------
// 账户合并与回滚（httpapi.AdminAccountsStore 的合并面）
// ---------------------------------------------------------------------------

// mergePlanToAdmin 把 store 的预检结果翻成对外形状（id 编码 + 冲突成人话）。
func mergePlanToAdmin(p store.MergePlan, sourceID, targetID string) *httpapi.AdminMergePlan {
	blocking := make([]string, 0, len(p.Blocking))
	for _, c := range p.Blocking {
		blocking = append(blocking, c.Detail)
	}
	renamed := p.RenamedKeys
	if renamed == nil {
		renamed = []string{}
	}
	return &httpapi.AdminMergePlan{
		SourceID: sourceID, TargetID: targetID,
		Blocking: blocking, RenamedKeys: renamed,
		Keys: p.Keys, Credentials: p.Creds, GroupRels: p.GroupRels,
		SourceClasses: p.SourceClasses, TargetClasses: p.TargetClasses,
	}
}

func (ab *accountsBridge) PrecheckMerge(ctx context.Context, sourceID, targetID string) (*httpapi.AdminMergePlan, error) {
	sid, err := decodeID(sourceID)
	if err != nil {
		return nil, err
	}
	tid, err := decodeID(targetID)
	if err != nil {
		return nil, err
	}
	p, err := ab.b.repos.Accounts.PrecheckMerge(ctx, sid, tid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return mergePlanToAdmin(p, sourceID, targetID), nil
}

// MergeAccounts 执行合并。
//
// 审计在**业务写成功之后**由同一事务外补记：store.MergeAccount 自带事务，
// 把审计塞进它里面需要改 store 签名；而合并的留痕行本身就是「谁在什么时候
// 把谁并给了谁」的一等记录（account_merges 是回滚依据，比审计行更结构化）。
// 这里仍写审计，是为了让审计流里能看到这次操作（06-audit：账户合并记
// source/target）——两条记录各自服务不同的查询。
func (ab *accountsBridge) MergeAccounts(ctx context.Context, sourceID, targetID string, audit httpapi.AdminAuditEntry) (*httpapi.AdminMergePlan, string, error) {
	sid, err := decodeID(sourceID)
	if err != nil {
		return nil, "", err
	}
	tid, err := decodeID(targetID)
	if err != nil {
		return nil, "", err
	}
	plan, err := ab.b.repos.Accounts.MergeAccount(ctx, sid, tid, time.Time{})
	if err != nil {
		if errors.Is(err, store.ErrMergeBlocked) {
			// 阻塞项明细进消息：管理员要看到「是哪条凭据撞了」才能处理。
			detail := "合并被阻塞：存在身份重叠的凭据"
			if len(plan.Blocking) > 0 {
				detail = "合并被阻塞：" + plan.Blocking[0].Detail
			}
			return mergePlanToAdmin(plan, sourceID, targetID), "", fmt.Errorf("%w: %s", httpapi.ErrAdminConflict, detail)
		}
		return nil, "", mapStoreErr(err)
	}
	aid, aerr := appendAudit(ctx, ab.b.repos.Logs, audit)
	if aerr != nil {
		// 合并已完成，审计写失败不该把它说成失败（同导入批次的判断）。
		return mergePlanToAdmin(plan, sourceID, targetID), "", nil
	}
	return mergePlanToAdmin(plan, sourceID, targetID), aid, nil
}

func (ab *accountsBridge) RollbackMerge(ctx context.Context, sourceID string, audit httpapi.AdminAuditEntry) (*httpapi.AdminMergePlan, string, error) {
	sid, err := decodeID(sourceID)
	if err != nil {
		return nil, "", err
	}
	rec, err := ab.b.repos.Accounts.RollbackMerge(ctx, sid, time.Time{})
	if err != nil {
		if errors.Is(err, store.ErrMergeNotReversible) {
			return nil, "", store.ErrNotFound
		}
		return nil, "", mapStoreErr(err)
	}
	// 回滚结果用同一形状回（target 的类保留并集，见 store 注释）。
	out := &httpapi.AdminMergePlan{
		SourceID: sourceID,
		TargetID: rec.TargetID,
		Keys:     len(rec.MovedKeys), Credentials: len(rec.MovedCreds),
		GroupRels:  len(rec.MovedGroupIDs),
		RolledBack: true,
		Blocking:   []string{}, RenamedKeys: []string{},
	}
	aid, aerr := appendAudit(ctx, ab.b.repos.Logs, audit)
	if aerr != nil {
		return out, "", nil
	}
	return out, aid, nil
}
