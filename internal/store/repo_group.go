// repo_group.go —— 组成员关系读路径（装配缺口补齐，task-12）。
//
// 为什么单独一个文件而不是塞进 repo_account/repo_model：组成员关系是
// 组域（03-groups.md）的数据，账户域和模型域都只是它的消费者；放在
// 谁名下都会让那个域凭空长出另一个域的写域。本文件先只落读方法，
// 组的写路径（建组/改成员）在 M7 任务。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GroupRepo 组成员关系仓储（读路径，task-12 装配缺口；写路径在 M7）。
type GroupRepo struct {
	base
}

// GroupIDsOfAccount 返回账户所属的全部组 id。
//
// 语义（docs/security/03-groups.md §5/§6，装配方 meter.StoreAdapter 与
// httpapi mwQuota 的 GroupIDsSource 都依赖这个口径）：
//   - groups 表**没有 disabled 列**——组的「停用」不是数据形态而是删除：
//     删组时 group_members 行随 ON DELETE CASCADE 一并消失，成员退回
//     默认组（03-groups §6「删组不删人」）。因此这里**不需要**过滤组状态：
//     存在于 group_members 的行本身就是「成员关系当前有效」的唯一事实。
//   - 「组不存在但成员行还在」在 FK 开启（Open 强制 PRAGMA foreign_keys=ON）
//     的库里不可能出现；测试里也用 FK 生效性兜底过这一点。
//   - 返回顺序按 group_id 升序：调用方（池扣减遍历）对顺序无语义要求，
//     排序只为断言与日志的确定性。
//   - 账户不在任何组：返回空切片、nil 错误（03-groups §5「默认组保证任何
//     账户至少有一组」是**服务层约定**——注册批准时入组；数据库层不设约束，
//     这里如实返回空，由调用方决定是否视为异常。meter 的池遍历对空列表
//     天然跳过，不 fail-open 也不误拒）。
func (r *GroupRepo) GroupIDsOfAccount(ctx context.Context, accountID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT m.group_id
		   FROM group_members m
		   JOIN groups g ON g.id = m.group_id
		  WHERE m.account_id = ?
		  ORDER BY m.group_id ASC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: 查账户所属组失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: 读组 id 失败: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历组 id 失败: %w", err)
	}
	return out, nil
}

// ================================================================ M7 组写路径

// Group 是 groups 表的行形态（03-groups §9）。
// ID/Name/Description/IsDefault/CreatedAt；组「停用」不存在——组的
// 危险操作就是删除（03-groups §6），没有 is_disabled 列。
type Group struct {
	ID          int64
	Name        string
	Description string
	IsDefault   bool
	CreatedAt   int64
}

// ErrDefaultGroupProtected 默认组的删除/撤销保护（03-groups §5：
// is_default=1 的组唯一且不可删除——「删组不删人→成员退回默认组」
// 必须有落点，默认组可删等于存在「无处可退」的账户状态）。
var ErrDefaultGroupProtected = errors.New("store: 默认组不可删除/不可撤销默认")

// ErrGroupNotFound 按唯一键（id 或 name）查组未命中。
var ErrGroupNotFound = errors.New("store: 组不存在")

// CreateGroup 建组。name 必须非空且唯一（ux_groups_name）；isDefault=true
// 时若已有默认组则报错（ux_groups_default 部分唯一索引也会兜底，先查后写
// 只为给出更友好的错误）。
func (r *GroupRepo) CreateGroup(ctx context.Context, name, description string, isDefault bool) (int64, error) {
	if strings.TrimSpace(name) == "" {
		return 0, errors.New("store: 组名不得为空")
	}
	if isDefault {
		var n int
		if err := r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM groups WHERE is_default = 1`).Scan(&n); err != nil {
			return 0, fmt.Errorf("store: 查默认组占用失败: %w", err)
		}
		if n > 0 {
			return 0, fmt.Errorf("%w（已存在默认组）", ErrDefaultGroupProtected)
		}
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO groups(name, description, is_default, created_at) VALUES(?,?,?,?)`,
		name, description, boolInt(isDefault), now())
	if err != nil {
		return 0, fmt.Errorf("store: 建组失败: %w", err)
	}
	return res.LastInsertId()
}

// GetGroup 按 id 查组；未命中返回 ErrGroupNotFound（调用方用 errors.Is）。
func (r *GroupRepo) GetGroup(ctx context.Context, id int64) (*Group, error) {
	g, err := r.getGroupWhere(ctx, `id = ?`, id)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// GetGroupByName 按 name 查组（ux_groups_name 唯一）。
func (r *GroupRepo) GetGroupByName(ctx context.Context, name string) (*Group, error) {
	return r.getGroupWhere(ctx, `name = ?`, name)
}

func (r *GroupRepo) getGroupWhere(ctx context.Context, where string, arg any) (*Group, error) {
	var g Group
	var isDef int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, description, is_default, created_at FROM groups WHERE `+where, arg).
		Scan(&g.ID, &g.Name, &g.Description, &isDef, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGroupNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: 查组失败: %w", err)
	}
	g.IsDefault = isDef == 1
	return &g, nil
}

// ListGroups 列全部组（id 升序）。console 管理页用。
func (r *GroupRepo) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, description, is_default, created_at FROM groups ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: 列组失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Group
	for rows.Next() {
		var g Group
		var isDef int
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &isDef, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: 扫描组行失败: %w", err)
		}
		g.IsDefault = isDef == 1
		out = append(out, g)
	}
	return out, rows.Err()
}

// UpdateGroup 改名/描述。isDefault 不在此改——默认组转移是独立的管理动作
// （SetDefault），混在改名里会让「改个描述」顺带迁移整个默认组语义。
func (r *GroupRepo) UpdateGroup(ctx context.Context, id int64, name, description string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("store: 组名不得为空")
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE groups SET name = ?, description = ? WHERE id = ?`, name, description, id)
	if err != nil {
		return fmt.Errorf("store: 改组失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// DeleteGroup 删组（03-groups §6「删组不删人」）：
//   - group_members / group_models / group_policies / group_quota_pools /
//     group_model_quota_caps 行随 ON DELETE CASCADE 一并消失；
//   - 成员**不删**——退回默认组的动作由服务层（调用方）在删组前后补
//     （存储层只保证 FK 联动；「退回默认组」需要知道默认组 id，那是
//     服务层上下文）。为什么不在存储层自动补：删组 + 成员回迁必须是
//     一个事务里的两个显式动作，自动隐补会把「删组失败但成员已动」
//     变成不可见状态；
//   - 默认组拒绝删除（ErrDefaultGroupProtected）。
//
// 审计由调用方（服务层）写——仓储不越权发审计事件。
func (r *GroupRepo) DeleteGroup(ctx context.Context, id int64) error {
	var isDef int
	if err := r.db.QueryRowContext(ctx,
		`SELECT is_default FROM groups WHERE id = ?`, id).Scan(&isDef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGroupNotFound
		}
		return fmt.Errorf("store: 查组默认标记失败: %w", err)
	}
	if isDef == 1 {
		return ErrDefaultGroupProtected
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 删组失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// SetDefault 转移默认组：旧默认清标记 → 新组打标记 → 旧组成员整体迁入
// 新默认组（INSERT OR IGNORE 防重复成员行）。同一个事务，由调用方包
// WithTx；单独调用也原子（两条语句 + 迁移是三个写，见内部 BEGIN）。
//
// 为什么迁移用 INSERT OR IGNORE 而不是先 DELETE 再 INSERT：成员可能
// 已在其它组，回默认组是「保证至少一组」而不是「只能一组」。
func (r *GroupRepo) SetDefault(ctx context.Context, newDefaultID int64) error {
	var isDef int
	if err := r.db.QueryRowContext(ctx,
		`SELECT is_default FROM groups WHERE id = ?`, newDefaultID).Scan(&isDef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGroupNotFound
		}
		return fmt.Errorf("store: 查新默认组失败: %w", err)
	}
	if isDef == 1 {
		return nil // 幂等：已是默认组
	}
	// 三个写动作放一个显式事务（仓储自身无事务上下文时）。
	if r.s == nil || r.s.tx == nil {
		// 借 Store.WithTx 保持与其它多步写一致的语义。
		return r.s.WithTx(ctx, func(tx *Repos) error {
			return tx.Groups.setDefaultInner(ctx, newDefaultID)
		})
	}
	return r.setDefaultInner(ctx, newDefaultID)
}

func (r *GroupRepo) setDefaultInner(ctx context.Context, newDefaultID int64) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE groups SET is_default = 0 WHERE is_default = 1`); err != nil {
		return fmt.Errorf("store: 清旧默认组失败: %w", err)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE groups SET is_default = 1 WHERE id = ?`, newDefaultID)
	if err != nil {
		return fmt.Errorf("store: 打默认组标记失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGroupNotFound
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO group_members(group_id, account_id)
		  SELECT ?, m.account_id FROM group_members m
		   WHERE m.group_id IN (SELECT id FROM groups WHERE is_default = 0 AND id = ?)`,
		newDefaultID, newDefaultID); err != nil {
		return fmt.Errorf("store: 迁移旧默认组成员失败: %w", err)
	}
	return nil
}

// AddMember 把账户加进组。幂等（INSERT OR IGNORE）：重复加组不是错误，
// 「保证至少一组」的语义下重复成员行无意义，静默收敛。
// 成员必须是带 tenant 类的账户（03-groups §9）——校验放这里（存储层
// 能一次查清）而不是留给每个调用方自查。
func (r *GroupRepo) AddMember(ctx context.Context, groupID, accountID int64) error {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account_classes WHERE account_id = ? AND class = 'tenant'`,
		accountID).Scan(&n); err != nil {
		return fmt.Errorf("store: 查账户权限类失败: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: 账户 %d 无 tenant 类，不能入组", accountID)
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO group_members(group_id, account_id) VALUES(?, ?)`,
		groupID, accountID); err != nil {
		return fmt.Errorf("store: 加组成员失败: %w", err)
	}
	return nil
}

// RemoveMember 移出成员。未在组中 = 无操作（幂等）。**注意**：调用方
// 负责保证账户仍至少属于一组（默认组兜底）——存储层不自动回默认组，
// 理由同 DeleteGroup：回迁是服务层的显式动作。
func (r *GroupRepo) RemoveMember(ctx context.Context, groupID, accountID int64) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM group_members WHERE group_id = ? AND account_id = ?`,
		groupID, accountID); err != nil {
		return fmt.Errorf("store: 移出组成员失败: %w", err)
	}
	return nil
}

// ListMembers 列组成员的账户 id（升序）。管理面用（03-groups §7：
// 成员列表只有管理员可见——可见性是 httpapi 的事，这里只给数据）。
func (r *GroupRepo) ListMembers(ctx context.Context, groupID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT account_id FROM group_members WHERE group_id = ? ORDER BY account_id ASC`,
		groupID)
	if err != nil {
		return nil, fmt.Errorf("store: 列组成员失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: 扫描成员行失败: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ================================================================ 组策略键值读写

// GetPolicy 读一个组的单条策略的原始 JSON 值。未设置返回 ("", nil)——
// 「未设」与「设了空串」不同语义，调用方区分。
func (r *GroupRepo) GetPolicy(ctx context.Context, groupID int64, key PolicyKey) (string, error) {
	var v string
	err := r.db.QueryRowContext(ctx,
		`SELECT policy_value FROM group_policies WHERE group_id = ? AND policy_key = ?`,
		groupID, string(key)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: 读组策略失败: %w", err)
	}
	return v, nil
}

// SetPolicy 写一条组策略（整体覆盖，键值表无增量语义）。
// 形状校验在应用层做（DDL 注释：值无法由 DB 约束校验）——这里按
// PolicyKey 闭集合分三路：布尔键收 JSON true/false；数字键收 JSON 整数
// （-1 = 显式不限，合法）；类别列表收 JSON 字符串数组。其余键名拒绝。
func (r *GroupRepo) SetPolicy(ctx context.Context, groupID int64, key PolicyKey, rawValue string) error {
	if _, known := policyKindOf(key); !known {
		return fmt.Errorf("store: 未知策略键 %q（新增键必须先归入放开/限制类）", key)
	}
	if err := validatePolicyValue(key, rawValue); err != nil {
		return fmt.Errorf("store: 策略 %s 的值不合法: %w", key, err)
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO group_policies(group_id, policy_key, policy_value) VALUES(?,?,?)
		 ON CONFLICT(group_id, policy_key) DO UPDATE SET policy_value = excluded.policy_value`,
		groupID, string(key), rawValue); err != nil {
		return fmt.Errorf("store: 写组策略失败: %w", err)
	}
	return nil
}

// DeletePolicy 删一条策略 = 该组该键回到「未设」（限制类不参与 min、
// 布尔回 fail-closed 的默认）。幂等。
func (r *GroupRepo) DeletePolicy(ctx context.Context, groupID int64, key PolicyKey) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM group_policies WHERE group_id = ? AND policy_key = ?`,
		groupID, string(key)); err != nil {
		return fmt.Errorf("store: 删组策略失败: %w", err)
	}
	return nil
}

// ListPolicies 列一个组的全部策略键值（升序）。
func (r *GroupRepo) ListPolicies(ctx context.Context, groupID int64) (map[PolicyKey]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT policy_key, policy_value FROM group_policies WHERE group_id = ? ORDER BY policy_key ASC`,
		groupID)
	if err != nil {
		return nil, fmt.Errorf("store: 列组策略失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[PolicyKey]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: 扫描组策略行失败: %w", err)
		}
		out[PolicyKey(k)] = v
	}
	return out, rows.Err()
}

// validatePolicyValue 按 key 的形状校验 raw JSON。
// 数字键：整数（含 -1）；布尔键：true/false；类别键：字符串数组。
func validatePolicyValue(key PolicyKey, raw string) error {
	switch {
	case isBoolPolicyKey(key):
		var b bool
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			return fmt.Errorf("需要 JSON 布尔: %w", err)
		}
	case isNumberPolicyKey(key):
		var n int64
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			return fmt.Errorf("需要 JSON 整数: %w", err)
		}
	case isClassListPolicyKey(key):
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err != nil {
			return fmt.Errorf("需要 JSON 字符串数组: %w", err)
		}
	default:
		return fmt.Errorf("未知形状的键 %q", key)
	}
	return nil
}

// ================================================================ 组模型授权

// SetGroupModels 整体覆盖组的抽象模型授权集合（放开类，合成取并集）。
// group_models 行随组删除 CASCADE；空列表 = 清空授权（合法——该组成员
// 什么模型都看不到，比删行更强的显式拒绝）。
func (r *GroupRepo) SetGroupModels(ctx context.Context, groupID int64, abstractModelIDs []int64) error {
	// 先验全部模型存在（FK 会兜底，但错误信息更友好且不写一半）。
	for _, id := range abstractModelIDs {
		var n int
		if err := r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM abstract_models WHERE id = ?`, id).Scan(&n); err != nil {
			return fmt.Errorf("store: 查抽象模型失败: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("store: 抽象模型 %d 不存在", id)
		}
	}
	if r.s != nil && r.s.tx != nil {
		return r.setGroupModelsInner(ctx, groupID, abstractModelIDs)
	}
	return r.s.WithTx(ctx, func(tx *Repos) error {
		return tx.Groups.setGroupModelsInner(ctx, groupID, abstractModelIDs)
	})
}

func (r *GroupRepo) setGroupModelsInner(ctx context.Context, groupID int64, ids []int64) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM group_models WHERE group_id = ?`, groupID); err != nil {
		return fmt.Errorf("store: 清组模型授权失败: %w", err)
	}
	for _, id := range ids {
		if _, err := r.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO group_models(group_id, abstract_model_id) VALUES(?, ?)`,
			groupID, id); err != nil {
			return fmt.Errorf("store: 写组模型授权失败: %w", err)
		}
	}
	return nil
}

// ListGroupModels 列组的抽象模型授权（升序）。合成侧（LoadPolicySnapshot）
// 与 console 都用。
func (r *GroupRepo) ListGroupModels(ctx context.Context, groupID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT abstract_model_id FROM group_models WHERE group_id = ? ORDER BY abstract_model_id ASC`,
		groupID)
	if err != nil {
		return nil, fmt.Errorf("store: 列组模型授权失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: 扫描组模型行失败: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ================================================================ 快照装载

// LoadPolicySnapshot 装配某账户的合成输入快照（effective_policy.go 的
// ComposeEffectivePolicy 吃这个）。一次 JOIN 拿全：
//   - group_members → 所属组
//   - group_policies → 每组键值（raw JSON 解码，坏值报错不静默跳过）
//   - group_models 并集 → VisibleModels
//   - group_model_quota_caps → ModelCaps
//   - group_quota_pools → Pools
//   - accounts 速率三列 → 落回值
//
// 解码放在装载侧而不是合成侧：合成的纯函数性要求输入确定，坏 JSON 在
// 这里显式失败（静默跳过 = 该组凭空消失 = fail-open）。
func (r *GroupRepo) LoadPolicySnapshot(ctx context.Context, accountID int64) (*PolicySnapshot, error) {
	// 账户类与速率三列。
	var rpm, tpm, maxc int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT rpm_limit, tpm_limit, max_concurrency FROM accounts WHERE id = ?`,
		accountID).Scan(&rpm, &tpm, &maxc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: 账户 %d 不存在", accountID)
		}
		return nil, fmt.Errorf("store: 读账户速率列失败: %w", err)
	}
	classes, err := r.s.Repos().Accounts.classesOf(ctx, accountID)
	if err != nil {
		return nil, err
	}

	groupIDs, err := r.GroupIDsOfAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}

	snap := &PolicySnapshot{
		AccountID:             accountID,
		Classes:               classes,
		GroupIDs:              groupIDs,
		AccountRPMLimit:       rpm,
		AccountTPMLimit:       tpm,
		AccountMaxConcurrency: maxc,
	}

	// 每组的键值策略。
	for _, gid := range groupIDs {
		gps, err := r.loadGroupPolicySet(ctx, gid)
		if err != nil {
			return nil, err
		}
		snap.Groups = append(snap.Groups, *gps)
	}

	// 可见模型并集。
	if len(groupIDs) > 0 {
		rows, err := r.db.QueryContext(ctx,
			`SELECT DISTINCT abstract_model_id FROM group_models WHERE group_id IN (`+
				placeholders(len(groupIDs))+`) ORDER BY abstract_model_id ASC`, int64sToAny(groupIDs)...)
		if err != nil {
			return nil, fmt.Errorf("store: 查组模型并集失败: %w", err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: 扫描可见模型失败: %w", err)
			}
			snap.VisibleModels = append(snap.VisibleModels, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: 遍历可见模型失败: %w", err)
		}
		rows.Close()
	}

	// 每人按模型上限 + 共享池：跨组 IN 查询。
	if len(groupIDs) > 0 {
		args := int64sToAny(groupIDs)
		caps, err := r.db.QueryContext(ctx,
			`SELECT group_id, abstract_model_id, unit, period, cap
			   FROM group_model_quota_caps WHERE group_id IN (`+placeholders(len(groupIDs))+`)`,
			args...)
		if err != nil {
			return nil, fmt.Errorf("store: 查按模型上限失败: %w", err)
		}
		for caps.Next() {
			var c ModelCap
			var u, p string
			if err := caps.Scan(&c.GroupID, &c.AbstractModelID, &u, &p, &c.Cap); err != nil {
				caps.Close()
				return nil, fmt.Errorf("store: 扫描按模型上限失败: %w", err)
			}
			c.Unit, c.Period = QuotaUnit(u), QuotaPeriod(p)
			snap.ModelCaps = append(snap.ModelCaps, c)
		}
		if err := caps.Err(); err != nil {
			caps.Close()
			return nil, fmt.Errorf("store: 遍历按模型上限失败: %w", err)
		}
		caps.Close()

		pools, err := r.db.QueryContext(ctx,
			`SELECT group_id, abstract_model_id, unit, period, pool_limit
			   FROM group_quota_pools WHERE group_id IN (`+placeholders(len(groupIDs))+`)`,
			args...)
		if err != nil {
			return nil, fmt.Errorf("store: 查共享池失败: %w", err)
		}
		for pools.Next() {
			var p PoolRef
			var u, per string
			if err := pools.Scan(&p.GroupID, &p.AbstractModelID, &u, &per, &p.Limit); err != nil {
				pools.Close()
				return nil, fmt.Errorf("store: 扫描共享池失败: %w", err)
			}
			p.Unit, p.Period = QuotaUnit(u), QuotaPeriod(per)
			snap.Pools = append(snap.Pools, p)
		}
		if err := pools.Err(); err != nil {
			pools.Close()
			return nil, fmt.Errorf("store: 遍历共享池失败: %w", err)
		}
		pools.Close()
	}
	return snap, nil
}

// loadGroupPolicySet 解码一组的 group_policies 行到 GroupPolicySet。
// 未知键：跳过（向前兼容——旧库里的键可能是未来版本写入的；不因未知
// 键拒绝整个装载）。坏形状的已知键：报错。
func (r *GroupRepo) loadGroupPolicySet(ctx context.Context, groupID int64) (*GroupPolicySet, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT policy_key, policy_value FROM group_policies WHERE group_id = ?`,
		groupID)
	if err != nil {
		return nil, fmt.Errorf("store: 查组策略失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	gps := &GroupPolicySet{GroupID: groupID, Numbers: map[PolicyKey]int64{}}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: 扫描组策略行失败: %w", err)
		}
		key := PolicyKey(k)
		if _, known := policyKindOf(key); !known {
			continue
		}
		switch {
		case key == PolicyCanCreateKey:
			var b bool
			if err := json.Unmarshal([]byte(v), &b); err != nil {
				return nil, fmt.Errorf("store: 组 %d 策略 %s 坏布尔: %w", groupID, k, err)
			}
			gps.CanCreateKey = &b
		case key == PolicyKeyAllowlistWiden:
			var b bool
			if err := json.Unmarshal([]byte(v), &b); err != nil {
				return nil, fmt.Errorf("store: 组 %d 策略 %s 坏布尔: %w", groupID, k, err)
			}
			gps.KeyAllowlistWiden = &b
		case key == PolicyMaxKeys:
			var n int64
			if err := json.Unmarshal([]byte(v), &n); err != nil {
				return nil, fmt.Errorf("store: 组 %d 策略 %s 坏整数: %w", groupID, k, err)
			}
			gps.MaxKeys = &n
		case key == PolicyModelClass:
			var arr []string
			if err := json.Unmarshal([]byte(v), &arr); err != nil {
				return nil, fmt.Errorf("store: 组 %d 策略 %s 坏数组: %w", groupID, k, err)
			}
			gps.ModelClasses = dedupSortString(arr)
		default: // 限制类数字键
			var n int64
			if err := json.Unmarshal([]byte(v), &n); err != nil {
				return nil, fmt.Errorf("store: 组 %d 策略 %s 坏整数: %w", groupID, k, err)
			}
			if n < 0 {
				continue // -1 = 未设语义，不进 Numbers
			}
			gps.Numbers[key] = n
		}
	}
	return gps, rows.Err()
}

// placeholders 生成 "?,?,?" 形式（SQLite 占位符；IN 子句用）。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// int64sToAny 给 QueryContext 的变长参数切片。
func int64sToAny(in []int64) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
