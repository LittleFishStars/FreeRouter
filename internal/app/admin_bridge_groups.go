// admin_bridge_groups.go —— AdminGroupsStore → store.GroupRepo（task-23）。
//
// 桥要点：
//   - 组 id 出入站走 idcode(grp_)；成员账户 id 同（acc_）。
//   - CreateGroup 的 policies 文档：store 的策略是 group_policies 键值表
//     （policy_key/policy_value JSON 文档），管理面整文档替换——落库时
//     摊平为单键 "admin_doc"（整份 JSON 存一个值），读侧原样取回。
//     为什么不逐键摊平：键语义（放开类/限制类/池）属于 auth.GroupPolicy
//     的强类型域，桥逐键解释就是第二份合并口径；整文档存取让 03-groups
//     的策略校验留在 authz 侧（task-22 在做），两边零耦合。
//   - DuplicateGroup：store 无此方法——用 WithTx 组合现有方法实现
//     （CreateGroup + ListGroupModels/SetGroupModels + 策略键复制 + 成员复制），
//     任务书明确允许。
//   - 删组不删人：DeleteGroup 的 CASCADE 只删关系行；「成员退回默认组」
//     由本桥在**同一事务**里先读成员、删组、再批量 AddMember 进默认组。
//
// store 缺口（回报 Lead 转达 storage）：UpdateGroup 的 setDefault（改默认组）
// 走 SetDefault（已有）；「is_default=false 撤销默认」无对应方法（撤默认
// 在 03-groups 语义里本就非法——默认组唯一且必须存在，桥直接拒绝）。

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/idcode"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// groupDocKey 是管理面策略整文档在 group_policies 里的存储键。
const groupDocKey = store.PolicyKey("admin_doc")

type groupsBridge struct{ b *adminBridge }

// groupToAdmin 翻组行（MemberCount 由调用方填——列表与详情各自取数）。
func groupToAdmin(g *store.Group) httpapi.AdminGroup {
	return httpapi.AdminGroup{
		ID:          encodeID(idcode.Group, g.ID),
		Name:        g.Name,
		Description: g.Description,
		IsDefault:   g.IsDefault,
		CreatedAt:   g.CreatedAt,
	}
}

func (gb *groupsBridge) ListGroups(ctx context.Context, limit, offset int) ([]httpapi.AdminGroup, int64, error) {
	rows, err := gb.b.repos.Groups.ListGroups(ctx)
	if err != nil {
		return nil, 0, mapStoreErr(err)
	}
	total := int64(len(rows))
	lo, hi := pageBounds(len(rows), limit, offset)
	out := make([]httpapi.AdminGroup, 0, hi-lo)
	for _, g := range rows[lo:hi] {
		ag := groupToAdmin(&g)
		if n, err := gb.b.repos.Groups.ListMembers(ctx, g.ID); err == nil {
			ag.MemberCount = int64(len(n))
		}
		out = append(out, ag)
	}
	return out, total, nil
}

func (gb *groupsBridge) GetGroup(ctx context.Context, id string) (*httpapi.AdminGroupDetail, error) {
	gid, err := decodeID(idcode.Group, id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	g, err := gb.b.repos.Groups.GetGroup(ctx, gid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	detail := &httpapi.AdminGroupDetail{AdminGroup: groupToAdmin(g)}
	if members, err := gb.b.repos.Groups.ListMembers(ctx, gid); err == nil {
		for _, m := range members {
			detail.Members = append(detail.Members, encodeID(idcode.Account, m))
		}
	}
	if detail.Members == nil {
		detail.Members = []string{}
	}
	// 模型授权：抽象模型 id → 对外名（契约 §6.1 的 Models 是名字数组）。
	detail.Models = []string{}
	if mids, err := gb.b.repos.Groups.ListGroupModels(ctx, gid); err == nil {
		for _, mid := range mids {
			if m, err := gb.b.repos.Models.GetAbstractModel(ctx, mid); err == nil {
				detail.Models = append(detail.Models, m.Name)
			}
		}
	}
	// 策略整文档：admin_doc 键存 JSON 原文；无键时空对象。
	detail.Policies = json.RawMessage(`{}`)
	if doc, err := gb.b.repos.Groups.GetPolicy(ctx, gid, groupDocKey); err == nil && doc != "" {
		detail.Policies = json.RawMessage(doc)
	}
	return detail, nil
}

func (gb *groupsBridge) CreateGroup(ctx context.Context, in httpapi.AdminGroup, models []string, policies json.RawMessage, audit httpapi.AdminAuditEntry) (string, string, error) {
	var newID int64
	var auditID string
	err := gb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		id, err := tx.Groups.CreateGroup(ctx, in.Name, in.Description, in.IsDefault)
		if err != nil {
			return err
		}
		newID = id
		if len(models) > 0 {
			mids, merr := gb.resolveModelNames(ctx, tx.Models, models)
			if merr != nil {
				return merr
			}
			if err := tx.Groups.SetGroupModels(ctx, id, mids); err != nil {
				return err
			}
		}
		if len(policies) > 0 {
			if err := tx.Groups.SetPolicy(ctx, id, groupDocKey, string(policies)); err != nil {
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
	return encodeID(idcode.Group, newID), auditID, nil
}

// resolveModelNames 把抽象模型名数组翻成 id 数组（创建/替换共用）。
// 名字查不到即报错（整单拒——半套授权比全拒更难归因）。
func (gb *groupsBridge) resolveModelNames(ctx context.Context, models *store.ModelRepo, names []string) ([]int64, error) {
	ids := make([]int64, 0, len(names))
	for _, n := range names {
		m, err := models.GetAbstractModelByName(ctx, n)
		if err != nil {
			return nil, fmt.Errorf("抽象模型 %q 不存在（授权集合拒绝半套落库）: %w", n, err)
		}
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func (gb *groupsBridge) UpdateGroup(ctx context.Context, id string, name, description *string, setDefault *bool, audit httpapi.AdminAuditEntry) (*httpapi.AdminGroup, string, error) {
	gid, err := decodeID(idcode.Group, id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var auditID string
	err = gb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		cur, err := tx.Groups.GetGroup(ctx, gid)
		if err != nil {
			return err
		}
		n, d := cur.Name, cur.Description
		if name != nil {
			n = *name
		}
		if description != nil {
			d = *description
		}
		if n != cur.Name || d != cur.Description {
			if err := tx.Groups.UpdateGroup(ctx, gid, n, d); err != nil {
				return err
			}
		}
		if setDefault != nil {
			if *setDefault {
				if !cur.IsDefault {
					if err := tx.Groups.SetDefault(ctx, gid); err != nil {
						return err
					}
				}
			} else if cur.IsDefault {
				// 撤销默认组在 03-groups 语义里非法（默认组唯一且必须存在
				// ——「删组不删人」需要它作退路）。显式拒绝，不静默忽略。
				return fmt.Errorf("is_default=false 拒绝：默认组必须存在（03-groups §5）: %w", store.ErrDefaultGroupProtected)
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
	// 读回新行（值没变时也是一次轻量读）。
	g, err := gb.b.repos.Groups.GetGroup(ctx, gid)
	if err != nil {
		return nil, auditID, nil
	}
	out := groupToAdmin(g)
	return &out, auditID, nil
}

func (gb *groupsBridge) DeleteGroup(ctx context.Context, id string, audit httpapi.AdminAuditEntry) (string, error) {
	gid, err := decodeID(idcode.Group, id)
	if err != nil {
		return "", errAdminNotImplemented
	}
	var auditID string
	err = gb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		cur, err := tx.Groups.GetGroup(ctx, gid)
		if err != nil {
			return err
		}
		if cur.IsDefault {
			return store.ErrDefaultGroupProtected
		}
		// 删组不删人（03-groups §6）：先取成员，删组（CASCADE 清关系行），
		// 再把成员批量退回默认组——三步同事务，中途崩溃即整体回滚。
		members, err := tx.Groups.ListMembers(ctx, gid)
		if err != nil {
			return err
		}
		def, err := gb.defaultGroupID(ctx, tx.Groups)
		if err != nil {
			return err
		}
		if err := tx.Groups.DeleteGroup(ctx, gid); err != nil {
			return err
		}
		for _, m := range members {
			if err := tx.Groups.AddMember(ctx, def, m); err != nil {
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
		return "", mapStoreErr(err)
	}
	return auditID, nil
}

// defaultGroupID 取当前默认组 id（无默认组 = 数据不变式破坏，报错）。
func (gb *groupsBridge) defaultGroupID(ctx context.Context, groups *store.GroupRepo) (int64, error) {
	rows, err := groups.ListGroups(ctx)
	if err != nil {
		return 0, err
	}
	for _, g := range rows {
		if g.IsDefault {
			return g.ID, nil
		}
	}
	return 0, errors.New("admin 桥：库中无默认组（不变式破坏——03-groups §5 要求恒有一个）")
}

func (gb *groupsBridge) DuplicateGroup(ctx context.Context, id, newName string, audit httpapi.AdminAuditEntry) (*httpapi.AdminGroup, string, error) {
	gid, err := decodeID(idcode.Group, id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var newID int64
	var auditID string
	err = gb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		src, err := tx.Groups.GetGroup(ctx, gid)
		if err != nil {
			return err
		}
		name := newName
		if name == "" {
			name = src.Name + " 副本"
		}
		// 复制出的永远是普通组（is_default=false）：默认组唯一索引兜底。
		nid, err := tx.Groups.CreateGroup(ctx, name, src.Description, false)
		if err != nil {
			return err
		}
		newID = nid
		if mids, err := tx.Groups.ListGroupModels(ctx, gid); err == nil && len(mids) > 0 {
			if err := tx.Groups.SetGroupModels(ctx, nid, mids); err != nil {
				return err
			}
		}
		if pols, err := tx.Groups.ListPolicies(ctx, gid); err == nil {
			for k, v := range pols {
				if err := tx.Groups.SetPolicy(ctx, nid, k, v); err != nil {
					return err
				}
			}
		}
		// 成员不复制：组是授权单元，「复制组」复制的是策略面不是人——
		// 把人一起拷走等于一次未经确认的批量授权变更（§2.3 纪律）。
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
	g, err := gb.b.repos.Groups.GetGroup(ctx, newID)
	if err != nil {
		return nil, encodeID(idcode.Group, newID), nil
	}
	out := groupToAdmin(g)
	return &out, auditID, nil
}

func (gb *groupsBridge) ListGroupMembers(ctx context.Context, groupID string) ([]string, error) {
	gid, err := decodeID(idcode.Group, groupID)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	members, err := gb.b.repos.Groups.ListMembers(ctx, gid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, encodeID(idcode.Account, m))
	}
	return out, nil
}

func (gb *groupsBridge) AddGroupMembers(ctx context.Context, groupID string, accountIDs []string, audit httpapi.AdminAuditEntry) (string, error) {
	gid, err := decodeID(idcode.Group, groupID)
	if err != nil {
		return "", errAdminNotImplemented
	}
	var auditID string
	err = gb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		for _, a := range accountIDs {
			aid, derr := decodeID(idcode.Account, a)
			if derr != nil {
				return derr
			}
			if err := tx.Groups.AddMember(ctx, gid, aid); err != nil {
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
		return "", mapStoreErr(err)
	}
	return auditID, nil
}

func (gb *groupsBridge) RemoveGroupMember(ctx context.Context, groupID, accountID string, audit httpapi.AdminAuditEntry) (string, error) {
	gid, err := decodeID(idcode.Group, groupID)
	if err != nil {
		return "", errAdminNotImplemented
	}
	aid, err := decodeID(idcode.Account, accountID)
	if err != nil {
		return "", errAdminNotImplemented
	}
	var auditID string
	err = gb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Groups.RemoveMember(ctx, gid, aid); err != nil {
			return err
		}
		// 移出后兜底进默认组（无组账户可见集为空 = 被静默停用，03-groups §6）。
		def, err := gb.defaultGroupID(ctx, tx.Groups)
		if err != nil {
			return err
		}
		if def != gid {
			if err := tx.Groups.AddMember(ctx, def, aid); err != nil {
				return err
			}
		}
		aid2, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid2
		return nil
	})
	if err != nil {
		return "", mapStoreErr(err)
	}
	return auditID, nil
}

func (gb *groupsBridge) ReplaceGroupModels(ctx context.Context, groupID string, modelNames []string, audit httpapi.AdminAuditEntry) ([]string, string, error) {
	gid, err := decodeID(idcode.Group, groupID)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var oldNames []string
	var auditID string
	err = gb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		oldIDs, err := tx.Groups.ListGroupModels(ctx, gid)
		if err != nil {
			return err
		}
		for _, mid := range oldIDs {
			if m, err := tx.Models.GetAbstractModel(ctx, mid); err == nil {
				oldNames = append(oldNames, m.Name)
			}
		}
		mids, merr := gb.resolveModelNames(ctx, tx.Models, modelNames)
		if merr != nil {
			return merr
		}
		if err := tx.Groups.SetGroupModels(ctx, gid, mids); err != nil {
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
		return nil, "", mapStoreErr(err)
	}
	if oldNames == nil {
		oldNames = []string{}
	}
	return oldNames, auditID, nil
}

func (gb *groupsBridge) ReplaceGroupPolicies(ctx context.Context, groupID string, doc json.RawMessage, audit httpapi.AdminAuditEntry) (json.RawMessage, string, error) {
	gid, err := decodeID(idcode.Group, groupID)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var old json.RawMessage
	var auditID string
	err = gb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if prev, err := tx.Groups.GetPolicy(ctx, gid, groupDocKey); err == nil && prev != "" {
			old = json.RawMessage(prev)
		}
		if err := tx.Groups.SetPolicy(ctx, gid, groupDocKey, string(doc)); err != nil {
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
		return nil, "", mapStoreErr(err)
	}
	if old == nil {
		old = json.RawMessage(`{}`)
	}
	return old, auditID, nil
}
