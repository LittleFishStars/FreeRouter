// repo_group.go —— 组成员关系读路径（装配缺口补齐，task-12）。
//
// 为什么单独一个文件而不是塞进 repo_account/repo_model：组成员关系是
// 组域（03-groups.md）的数据，账户域和模型域都只是它的消费者；放在
// 谁名下都会让那个域凭空长出另一个域的写域。本文件先只落读方法，
// 组的写路径（建组/改成员）在 M7 任务。
package store

import (
	"context"
	"fmt"
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
