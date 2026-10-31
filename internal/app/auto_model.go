// auto_model.go —— 系统托管的 auto 抽象模型（2026-10-31 用户定案）。
//
// 需求：每个组默认拥有一个 auto 抽象模型，包含这个组的所有可用模型。
//
// 为什么是「一个全局 auto + 授权给每个组」而不是每组建一份：
//  1. 抽象模型名全局唯一且不可改（02-abstract.md §2.3），每组建一份必须叫
//     auto-<组名>——组名是自由文本（真实库里有「默认组」），名字字符集
//     （小写字母/数字/-/_）装不下，起名规则会立刻失控；
//  2. 组在数据模型上不圈定真实模型（组只授权抽象模型），所以「每个组的
//     可用真实模型」的并集对任何组都是同一个全池集合——N 份相同候选集
//     只会带来 N 倍的同步与漂移面；
//  3. 对客户端一个稳定的 "auto" 名比按组变名友好（换组不用改配置）。
//     授权差异仍由 admin 控制：auto 只在「建模时」与「建组时」默认授予，
//     同步循环永不重授——管理员撤回后不会被悄悄加回来。
//
// 候选集口径：全池可路由真实模型 = ListVisibleRealModels（上游启用 + active +
// 未隐藏）且未被排除（excluded_from_candidates，aggregate/* 由存储层落库时
// 置位）。运行时还有第二道闸（routing.expandCandidates 会剔除
// missing/禁用/排除行），所以候选声明短暂滞后只影响「新模型入池的收录
// 延迟」，不会把失效模型路由出去——这是 60s 同步周期足够的安全性来源。
//
// 生命周期语义：
//   - auto 缺失（首次启动 / 被管理员删除）→ 重建并授权给**当前全部组**；
//     「删除即重建」是刻意的：它是系统默认，停用（status=disabled）才是
//     下线开关，同步循环对 disabled 的 auto 只读不写。
//   - 候选集每轮与全池对齐（差异才写，避免无谓的行重写）。
//   - 名为 "auto" 的手工模型会被收编为托管（名字即预留位）——文档已注明。
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// autoModel 的对外名与固定属性（02-abstract.md §2.3：名全局唯一不可改）。
const (
	autoModelName       = "auto"
	autoModelDisplay    = "自动路由"
	autoModelSyncPeriod = time.Minute
	autoModelStrategy   = `["expiry-first","balance"]`
	autoModelNote       = "系统自动维护：候选集每分钟与全池可路由模型对齐（2026-10-31 定案）。" +
		"手工编辑候选集会被覆盖；要下线请「停用」而不是删除——删除会在下一轮同步重建。"
)

// startAutoModelSync 启动 auto 模型后台同步（启动即跑一轮 + 固定周期）。
// 放在 startCheckin 之后：它只碰本地库，不依赖 sidecar，晚一步只是晚一轮。
func (a *App) startAutoModelSync(ctx context.Context) {
	go func() {
		if err := a.syncAutoModel(ctx); err != nil {
			a.log.Warn("app: auto 模型首轮同步失败（下一轮重试）", "error", err)
		}
		t := time.NewTicker(autoModelSyncPeriod)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := a.syncAutoModel(ctx); err != nil {
					a.log.Warn("app: auto 模型同步失败", "error", err)
				}
			}
		}
	}()
}

// syncAutoModel 跑一轮对齐：建模（缺失时）→ 对齐候选集。授权只在建模时发生。
func (a *App) syncAutoModel(ctx context.Context) error {
	m, err := a.store.Repos().Models.GetAbstractModelByName(ctx, autoModelName)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("app: 查询 auto 模型失败: %w", err)
		}
		// 缺失 → 建模 + 授权给当前全部组（含已有其它授权的组：这是本
		// 特性的交付点「为每一个组默认生成」；此后同步循环不再碰授权，
		// 管理员的撤回是终态）。
		note := autoModelNote
		id, cerr := a.store.Repos().Models.CreateAbstractModel(ctx, store.AbstractModel{
			Name:        autoModelName,
			DisplayName: autoModelDisplay,
			Strategy:    autoModelStrategy,
			Note:        &note,
		})
		if cerr != nil {
			return fmt.Errorf("app: 创建 auto 模型失败: %w", cerr)
		}
		if gerr := a.grantAutoToAllGroups(ctx, id); gerr != nil {
			return gerr
		}
		a.log.Info("app: 已创建 auto 抽象模型并授权给全部组", "id", id)
		m, err = a.store.Repos().Models.GetAbstractModelByName(ctx, autoModelName)
		if err != nil {
			return fmt.Errorf("app: 复读 auto 模型失败: %w", err)
		}
	}
	// 停用是管理员的下线开关：只读不写（候选集对齐对 disabled 的行没有意义，
	// 也不该在管理员重新启用时带来意外变更）。
	if m.Status != store.ModelActive {
		return nil
	}
	return a.alignAutoCandidates(ctx, m.ID)
}

// alignAutoCandidates 把 auto 的候选集对齐到当前全池可路由集合（差异才写）。
func (a *App) alignAutoCandidates(ctx context.Context, abstractID string) error {
	rows, err := a.store.Repos().Models.ListVisibleRealModels(ctx)
	if err != nil {
		return fmt.Errorf("app: 列可见真实模型失败: %w", err)
	}
	desired := make([]store.Candidate, 0, len(rows))
	for _, r := range rows {
		if r.ExcludedFromCandidates {
			continue
		}
		desired = append(desired, store.Candidate{RealModelID: r.ID})
	}
	cur, err := a.store.Repos().Models.ListCandidates(ctx, abstractID)
	if err != nil {
		return fmt.Errorf("app: 查询 auto 候选集失败: %w", err)
	}
	if candidateSetEqual(cur, desired) {
		return nil
	}
	if err := a.store.Repos().Models.SetCandidates(ctx, abstractID, desired); err != nil {
		return fmt.Errorf("app: 写 auto 候选集失败: %w", err)
	}
	a.log.Info("app: auto 模型候选集已对齐全池", "candidates", len(desired))
	return nil
}

// candidateSetEqual 比较候选集合（只看 real_model_id 集合，忽略顺序与约束）。
// 顺序敏感的比较会在目录刷新重排行序时产生无谓的重写；声明序对 auto 无意义
// （排序归策略链，不归声明序）。
func candidateSetEqual(cur, desired []store.Candidate) bool {
	if len(cur) != len(desired) {
		return false
	}
	seen := make(map[string]bool, len(cur))
	for _, c := range cur {
		seen[c.RealModelID] = true
	}
	for _, c := range desired {
		if !seen[c.RealModelID] {
			return false
		}
	}
	return true
}

// grantAutoToAllGroups 把 auto 追加进每个组的授权集合（读-改-写，按组事务）。
// SetGroupModels 是整体覆盖语义，先读现集合再追加——绝不整组替换，
// 否则一次授权动作会把管理员配好的模型列表抹掉。
func (a *App) grantAutoToAllGroups(ctx context.Context, autoID string) error {
	groups, err := a.store.Repos().Groups.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("app: 列组失败: %w", err)
	}
	for _, g := range groups {
		gerr := a.store.WithTx(ctx, func(tx *store.Repos) error {
			mids, err := tx.Groups.ListGroupModels(ctx, g.ID)
			if err != nil {
				return err
			}
			for _, mid := range mids {
				if mid == autoID {
					return nil
				}
			}
			return tx.Groups.SetGroupModels(ctx, g.ID, append(mids, autoID))
		})
		if gerr != nil {
			return fmt.Errorf("app: 给组 %s 授予 auto 失败: %w", g.ID, gerr)
		}
	}
	return nil
}

// ensureAutoModelGrant 在建组/复制组事务里把 auto 追加进新组的授权集合。
// auto 不存在（被删且同步循环还没跑）时静默跳过：下一轮同步重建 auto 时
// 会授权给全部组，这个窗口自动闭合。策略链序列化失败等错误照常上抛——
// 建组是显式写操作，失败要可见。
func ensureAutoModelGrant(ctx context.Context, models *store.ModelRepo, groups *store.GroupRepo, groupID string) error {
	m, err := models.GetAbstractModelByName(ctx, autoModelName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	mids, err := groups.ListGroupModels(ctx, groupID)
	if err != nil {
		return err
	}
	for _, mid := range mids {
		if mid == m.ID {
			return nil
		}
	}
	return groups.SetGroupModels(ctx, groupID, append(mids, m.ID))
}
