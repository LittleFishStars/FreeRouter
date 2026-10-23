package store

// repo_merge.go —— 账户合并与回滚（01-accounts §4）。
//
// ## 合并语义（文档定案，逐条对应本文件的实现）
//
//	source.classes ∪ target.classes   → target.classes
//	source 名下资源（Key/凭据/组关系） → 改挂 target
//	source.status                     → merged
//	留痕                               → account_merges(source,target,merged_at,reversible_until)
//
// 审计**不重写历史**：旧记录仍带 source_id（append-only，改历史等于销毁证据）。
//
// ## 冲突处理（定案：预检 + 分项选择，不一律拒绝整次合并）
//
//	同 provider 的凭据（UNIQUE(provider,external_id) 真撞）→ 拒绝整次合并
//	Key 同名                                             → 自动改名，不阻塞
//	同名组关系                                           → 无冲突（多对多）
//
// 为什么这样分层：整次拒绝会让「一把同名 Key」把带几十个凭据的合并全部卡死，
// 而合并的动机往往是「这个人失联了、东西都在那边」；反过来凭据撞的是**身份**
// （provider+external_id 唯一标识一个上游账号），静默合并会把两个自然人变成
// 一个，不可逆。
//
// ## 回滚范围（只回滚归属与类，不回滚用量）
//
// 已发生的请求确实以 target 身份消费了额度、也真的执行了。把用量搬走会让
// 对账失去可信锚点。撤销本身写 undone_at 而**不删行**——撤销也是审计事件。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// mergeReversibleWindow 是合并撤销窗口（定案 7 天）。
//
// 为什么是 7 天而不是无限：撤销是给「误操作」准备的，不是给「后悔」准备的。
// 一周足够容纳「合并后第几天才发现不对劲」；再长，用量/审计/组关系已经以
// target 为中心产生了新历史，拆回去会把那些历史变成无法归因的孤儿。
const mergeReversibleWindow = 7 * 24 * time.Hour

// MergeConflict 是一条预检发现的阻塞项。
type MergeConflict struct {
	Kind         string // 目前只有 credential_overlap
	CredentialID int64
	Provider     string
	ExternalID   string
	Detail       string
}

// MergePlan 是预检结果（管理员在确认页看到的就是它）。
type MergePlan struct {
	SourceID int64
	TargetID int64
	// Blocking 非空时**不得**执行合并（01-accounts §4：只有阻塞项为空才能点确认）。
	Blocking []MergeConflict
	// RenamedKeys 是将被自动改名的 Key（🟡 自动处理项，明示将做什么）。
	RenamedKeys []string
	// Counts 是本次会改挂的资源数量（给确认页一句人话）。
	Keys      int
	Creds     int
	GroupRels int
	// MovedKeys / MovedCreds / MovedGroups 是**将被改挂的资源 id**（0007 迁移
	// 的三列）。回滚靠它们精确还原，而不是靠猜（见 RollbackMerge 注释）。
	MovedKeys   []int64
	MovedCreds  []int64
	MovedGroups []int64
	// SourceClasses / TargetClasses 用于展示并集结果。
	SourceClasses []string
	TargetClasses []string
}

// ok 报告预检是否放行。
func (p MergePlan) ok() bool { return len(p.Blocking) == 0 }

// ErrMergeBlocked 表示预检有阻塞项（调用方据此回 409 并带明细）。
var ErrMergeBlocked = errors.New("store: 合并被预检阻塞")

// ErrMergeNotReversible 表示没有可回滚的合并，或已超出撤销窗口。
var ErrMergeNotReversible = errors.New("store: 没有可回滚的合并（或已超出 7 天窗口）")

// PrecheckMerge 跑合并预检（只读，不写任何东西）。
//
// 为什么预检与执行分开而不是「边写边发现冲突就回滚」：管理员需要在点确认
// **之前**看到「这次会改挂 3 把 Key、12 个凭据，并且有 1 条凭据撞身份」。
// 合并是 🔒 危险写，确认页没有信息就等于没有确认。
func (r *AccountRepo) PrecheckMerge(ctx context.Context, sourceID, targetID int64) (MergePlan, error) {
	if sourceID == targetID {
		return MergePlan{}, errors.New("store: 不能把账户合并到自己")
	}
	src, err := r.GetAccount(ctx, sourceID)
	if err != nil {
		return MergePlan{}, fmt.Errorf("store: 源账户不可用: %w", err)
	}
	tgt, err := r.GetAccount(ctx, targetID)
	if err != nil {
		return MergePlan{}, fmt.Errorf("store: 目标账户不可用: %w", err)
	}
	plan := MergePlan{
		SourceID: sourceID, TargetID: targetID,
		SourceClasses: classNames(src.Classes),
		TargetClasses: classNames(tgt.Classes),
	}

	// 阻塞项：同 (provider, external_id) 的凭据两边都有 = 身份重叠。
	rows, err := r.db.QueryContext(ctx,
		`SELECT s.id, s.provider, s.external_id
		   FROM upstream_credentials s
		   JOIN upstream_credentials t
		     ON t.provider = s.provider AND t.external_id = s.external_id
		  WHERE s.owner_account_id = ? AND t.owner_account_id = ?`,
		sourceID, targetID)
	if err != nil {
		return plan, fmt.Errorf("store: 预检凭据冲突失败: %w", err)
	}
	for rows.Next() {
		var c MergeConflict
		if err := rows.Scan(&c.CredentialID, &c.Provider, &c.ExternalID); err != nil {
			rows.Close()
			return plan, fmt.Errorf("store: 扫描凭据冲突失败: %w", err)
		}
		c.Kind = "credential_overlap"
		c.Detail = fmt.Sprintf("凭据 %s/%s 在两个账户上各有一条", c.Provider, c.ExternalID)
		plan.Blocking = append(plan.Blocking, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return plan, fmt.Errorf("store: 遍历凭据冲突失败: %w", err)
	}
	rows.Close()

	// Key 同名 → 自动改名（名字不是身份，Key 的身份是它的 secret）。
	if ks, err := r.db.QueryContext(ctx,
		`SELECT s.id, s.name FROM api_keys s
		   JOIN api_keys t ON t.account_id = ? AND t.name IS NOT NULL AND s.name = t.name
		  WHERE s.account_id = ? AND s.name IS NOT NULL`, targetID, sourceID); err == nil {
		for ks.Next() {
			var id int64
			var name *string
			if err := ks.Scan(&id, &name); err == nil && name != nil {
				plan.RenamedKeys = append(plan.RenamedKeys, *name)
			}
		}
		ks.Close()
	}

	ids, err := collectIDs(ctx, r.db, `SELECT id FROM api_keys WHERE account_id = ? ORDER BY id`, sourceID)
	if err != nil {
		return plan, err
	}
	plan.MovedKeys, plan.Keys = ids, len(ids)
	if ids, err := collectIDs(ctx, r.db, `SELECT id FROM upstream_credentials WHERE owner_account_id = ? ORDER BY id`, sourceID); err == nil {
		plan.MovedCreds, plan.Creds = ids, len(ids)
	} else {
		return plan, err
	}
	if ids, err := collectIDs(ctx, r.db, `SELECT group_id FROM group_members WHERE account_id = ? ORDER BY group_id`, sourceID); err == nil {
		plan.MovedGroups, plan.GroupRels = ids, len(ids)
	} else {
		return plan, err
	}
	return plan, nil
}

// collectIDs 读一列 int64。
func collectIDs(ctx context.Context, db dbtx, q string, args ...any) ([]int64, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 读取改挂清单失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: 扫描改挂清单失败: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// classNames 把权限类翻成字符串数组（稳定顺序按 Class 常量声明序）。
func classNames(cs []Class) []string {
	out := make([]string, 0, len(cs))
	for _, c := range []Class{ClassAdmin, ClassTenant, ClassContributor} {
		for _, have := range cs {
			if have == c {
				out = append(out, string(c))
			}
		}
	}
	return out
}

// MergeAccount 执行合并（单事务）。
//
// 顺序：预检 → 类并集 → Key 改名 → 资源改挂 → 源转 merged → 写留痕行。
// 预检在事务**内**重跑一次：从确认页到点确认之间可能有别的写入（并发合并、
// 新凭据），只在事务外检过等于拿一张过期地图做决定。
func (r *AccountRepo) MergeAccount(ctx context.Context, sourceID, targetID int64, at time.Time) (MergePlan, error) {
	var plan MergePlan
	if at.IsZero() {
		at = time.Now()
	}
	err := r.s.WithTx(ctx, func(tx *Repos) error {
		p, err := tx.Accounts.PrecheckMerge(ctx, sourceID, targetID)
		plan = p
		if err != nil {
			return err
		}
		if !p.ok() {
			return ErrMergeBlocked
		}

		// 1) 类并集（合并没有降权的理由：两边的能力都是本人已获得的）。
		src, err := tx.Accounts.GetAccount(ctx, sourceID)
		if err != nil {
			return err
		}
		tgt, err := tx.Accounts.GetAccount(ctx, targetID)
		if err != nil {
			return err
		}
		merged := map[Class]bool{}
		for _, c := range tgt.Classes {
			merged[c] = true
		}
		for _, c := range src.Classes {
			merged[c] = true
		}
		list := make([]Class, 0, len(merged))
		for c := range merged {
			list = append(list, c)
		}
		if err := tx.Accounts.SetAccountClasses(ctx, targetID, list); err != nil {
			return err
		}

		// 2) 同名 Key 自动改名（追加 -from-<source_id 短码>）。
		if _, err := tx.Accounts.db.ExecContext(ctx,
			`UPDATE api_keys SET name = name || '-from-' || ?
			  WHERE account_id = ? AND name IS NOT NULL
			    AND name IN (SELECT t.name FROM api_keys t WHERE t.account_id = ? AND t.name IS NOT NULL)`,
			shortCode(sourceID), sourceID, targetID); err != nil {
			return fmt.Errorf("store: Key 改名失败: %w", err)
		}

		// 3) 资源改挂。
		if _, err := tx.Accounts.db.ExecContext(ctx,
			`UPDATE api_keys SET account_id = ? WHERE account_id = ?`, targetID, sourceID); err != nil {
			return fmt.Errorf("store: 改挂 Key 失败: %w", err)
		}
		if _, err := tx.Accounts.db.ExecContext(ctx,
			`UPDATE upstream_credentials SET owner_account_id = ? WHERE owner_account_id = ?`,
			targetID, sourceID); err != nil {
			return fmt.Errorf("store: 改挂凭据失败: %w", err)
		}
		// 组关系：多对多，重复无意义 → 先 IGNORE 插入 target 的行，再删 source 的行。
		// 不走 AddMember：它要求 tenant 类（那是「新建关系」的判据），而这里是
		// 改挂**已存在**的关系行，源账户可能只有 contributor 类。
		if _, err := tx.Accounts.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO group_members(group_id, account_id)
			 SELECT group_id, ? FROM group_members WHERE account_id = ?`, targetID, sourceID); err != nil {
			return fmt.Errorf("store: 复制组关系失败: %w", err)
		}
		if _, err := tx.Accounts.db.ExecContext(ctx,
			`DELETE FROM group_members WHERE account_id = ?`, sourceID); err != nil {
			return fmt.Errorf("store: 清理源组关系失败: %w", err)
		}

		// 4) 源账户转 merged。
		if err := tx.Accounts.SetAccountStatus(ctx, sourceID, AccountMerged); err != nil {
			return err
		}

		// 5) 留痕（回滚依据）：连**被改挂的资源清单**一起存。
		//    没有这三份清单，回滚就只能靠猜（见 0007 迁移与 RollbackMerge）。
		kb, _ := json.Marshal(p.MovedKeys)
		cb, _ := json.Marshal(p.MovedCreds)
		gb, _ := json.Marshal(p.MovedGroups)
		if _, err := tx.Accounts.db.ExecContext(ctx,
			`INSERT INTO account_merges(source_id, target_id, merged_at, reversible_until,
			                            moved_key_ids, moved_credential_ids, moved_group_ids)
			 VALUES(?,?,?,?,?,?,?)`,
			sourceID, targetID, at.Unix(), at.Add(mergeReversibleWindow).Unix(),
			string(kb), string(cb), string(gb)); err != nil {
			return fmt.Errorf("store: 写合并留痕失败: %w", err)
		}
		return nil
	})
	return plan, err
}

// shortCode 是 Key 改名用的源账户短码（8 位以内，够人读出「从哪个账户来」）。
func shortCode(id int64) string {
	s := strconv.FormatInt(id, 10)
	if len(s) > 8 {
		s = s[len(s)-8:]
	}
	return s
}

// MergeRecord 是一条合并留痕。
type MergeRecord struct {
	ID              int64
	SourceID        int64
	TargetID        int64
	MergedAt        int64
	ReversibleUntil int64
	UndoneAt        *int64
	// 被改挂的资源清单（回滚的精确依据）。
	MovedKeys     []int64
	MovedCreds    []int64
	MovedGroupIDs []int64
}

// LatestActiveMerge 取某源账户最近一次**未撤销**的合并（回滚的输入）。
func (r *AccountRepo) LatestActiveMerge(ctx context.Context, sourceID int64) (MergeRecord, error) {
	var m MergeRecord
	var keyJSON, credJSON, groupJSON string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, source_id, target_id, merged_at, reversible_until, undone_at,
		        moved_key_ids, moved_credential_ids, moved_group_ids
		   FROM account_merges WHERE source_id = ? AND undone_at IS NULL
		  ORDER BY merged_at DESC LIMIT 1`, sourceID).
		Scan(&m.ID, &m.SourceID, &m.TargetID, &m.MergedAt, &m.ReversibleUntil, &m.UndoneAt,
			&keyJSON, &credJSON, &groupJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrMergeNotReversible
	}
	if err != nil {
		return m, fmt.Errorf("store: 查合并留痕失败: %w", err)
	}
	// 清单解析失败不阻断回滚：按空清单处理（即该类资源不改挂回去），
	// 比整个回滚失败更可恢复——管理员至少能把账户本身救回来。
	_ = json.Unmarshal([]byte(keyJSON), &m.MovedKeys)
	_ = json.Unmarshal([]byte(credJSON), &m.MovedCreds)
	_ = json.Unmarshal([]byte(groupJSON), &m.MovedGroupIDs)
	return m, nil
}

// RollbackMerge 回滚一次合并（单事务；只回归属与类，不回用量与审计）。
//
// 「谁属于源」的判据是留痕行里的**资源清单**（0007 迁移的三列）：合并时
// 改挂了哪些 id，回滚就按 id 搬回去。只搬清单里的行，所以 target 在合并
// 之后新增的 Key/凭据不会被误搬——那正是「按时间戳猜」会犯的错。
//
// 不回滚的三样（文档定案）：
//   - 用量与审计：已发生的请求确实以 target 身份消费了额度、也真的执行了，
//     把用量搬走会让对账失去可信锚点；
//   - usage_daily 已聚合的旧行：同上，新用量从撤销时刻起回到 source；
//   - target 的权限类并集：并集只会让 target 更宽，留着不会锁死任何人，
//     而事后无法区分哪个类是谁带来的——猜着删反而可能把 target 自己的类删掉。
func (r *AccountRepo) RollbackMerge(ctx context.Context, sourceID int64, at time.Time) (MergeRecord, error) {
	var rec MergeRecord
	if at.IsZero() {
		at = time.Now()
	}
	err := r.s.WithTx(ctx, func(tx *Repos) error {
		m, err := tx.Accounts.LatestActiveMerge(ctx, sourceID)
		if err != nil {
			return err
		}
		if at.Unix() > m.ReversibleUntil {
			return ErrMergeNotReversible
		}
		rec = m

		// 1) 源账户的类行在合并里从未被删（只转了状态），所以这里只需确认
		//    它仍非空——空集合违反不变式，宁可报错也不写进库。
		src, err := tx.Accounts.GetAccount(ctx, m.SourceID)
		if err != nil {
			return err
		}
		if len(src.Classes) == 0 {
			// 源账户的类行被合并流程清过（当前实现不清，但这里兜住）：
			// 空集合违反不变式，宁可报错也不写进库。
			return errors.New("store: 源账户权限类集合为空，无法安全回滚")
		}

		// 2) 按清单把 Key 改挂回 source，并还原合并时的自动改名。
		//    名字还原只截**确实带这个后缀**的行，避免把用户自己起的、
		//    恰好含相似片段的名字改坏。
		suffix := "-from-" + shortCode(m.SourceID)
		if len(m.MovedKeys) > 0 {
			ph := strings.Join(inPlaceholders(len(m.MovedKeys)), ",")
			// UPDATE api_keys SET account_id=? WHERE id IN (...) AND account_id=?
			if _, err := tx.Accounts.db.ExecContext(ctx,
				fmt.Sprintf(`UPDATE api_keys SET account_id = ?
				              WHERE id IN (%s) AND account_id = ?`, ph),
				append([]any{m.SourceID}, append(toAny(m.MovedKeys), m.TargetID)...)...,
			); err != nil {
				return fmt.Errorf("store: 回滚 Key 归属失败: %w", err)
			}
			// SET name = 截掉后缀（LIKE ? 、length>? 、substr 的减数、WHERE IN）
			if _, err := tx.Accounts.db.ExecContext(ctx,
				fmt.Sprintf(`UPDATE api_keys
				             SET name = CASE WHEN name LIKE ? AND length(name) > ?
				                             THEN substr(name, 1, length(name) - ?)
				                             ELSE name END
				              WHERE id IN (%s)`, ph),
				append([]any{suffix + "%", len(suffix), len(suffix)}, toAny(m.MovedKeys)...)...,
			); err != nil {
				return fmt.Errorf("store: 还原 Key 名字失败: %w", err)
			}
		}
		// 3) 按清单改挂凭据。
		if len(m.MovedCreds) > 0 {
			ph := strings.Join(inPlaceholders(len(m.MovedCreds)), ",")
			if _, err := tx.Accounts.db.ExecContext(ctx,
				fmt.Sprintf(`UPDATE upstream_credentials SET owner_account_id = ?
				              WHERE id IN (%s) AND owner_account_id = ?`, ph),
				append([]any{m.SourceID}, append(toAny(m.MovedCreds), m.TargetID)...)...,
			); err != nil {
				return fmt.Errorf("store: 回滚凭据归属失败: %w", err)
			}
		}
		// 4) 按清单恢复组关系（source 重新入组；target 的行保留——它可能
		//    已经以 target 身份产生了用量与策略依赖，撤走会造成静默降权）。
		for _, gid := range m.MovedGroupIDs {
			if _, err := tx.Accounts.db.ExecContext(ctx,
				`INSERT OR IGNORE INTO group_members(group_id, account_id) VALUES(?,?)`,
				gid, m.SourceID); err != nil {
				return fmt.Errorf("store: 恢复组关系失败: %w", err)
			}
		}
		// 5) 源账户转回 active。
		if err := tx.Accounts.SetAccountStatus(ctx, m.SourceID, AccountActive); err != nil {
			return err
		}

		// 6) 留痕写 undone_at（不删行：撤销本身也是审计事件）。
		if _, err := tx.Accounts.db.ExecContext(ctx,
			`UPDATE account_merges SET undone_at = ? WHERE id = ?`, at.Unix(), m.ID); err != nil {
			return fmt.Errorf("store: 写撤销留痕失败: %w", err)
		}
		return nil
	})
	return rec, err
}

// inPlaceholders 生成 n 个 "?"。
func inPlaceholders(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "?"
	}
	return out
}

// toAny 把 int64 切片翻成查询参数切片。
func toAny(ids []int64) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}
