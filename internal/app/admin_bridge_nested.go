// admin_bridge_nested.go —— 抽象模型的嵌套候选（抽象模型作为候选）。
//
// 需求（2026-11-01 用户提出）：抽象模型的候选集除了真实模型，还可以把**其他
// 抽象模型**加为候选。运行时按声明序展开它们的候选（分层回退：A 里排第一的
// 都不行就退到 B）。为什么不是「把 B 的候选抄进 A」：手抄副本会在 B 改配置后
// 悄悄失真——A 声称覆盖了 B，实际抄的是那一刻的快照。
//
// 循环依赖治理（用户要求「避免循环依赖，在选择时就隐藏会循环依赖的项」）：
//
//  1. **保存时拒绝**（assertNoCycle）：把这条候选加进去后会成环就不让保存，
//     并给出成环路径（「A → B → C → A」）。这是主闸——管理员在界面上做不出
//     循环，就不必在运行时天天猜。
//  2. **选择时隐藏**（blockedAbstractIDs）：候选编辑弹窗里，凡是「加进去就会
//     成环」的抽象模型不出现在可选列表里。不是置灰而是**不显示**：一个永远
//     保存不成功的选择项是纯粹的噪声，而用户的原话就是「在选择时就隐藏」。
//  3. **运行时兜底**（routing.expandDepth 的路径栈）：手工改库绕过了前两道闸
//     时，成环的那一条候选被跳过（不报错、不连坐同层其它候选）。
//
// 为什么成环判据是「我 → … → 目标 → 我 是否存在路径」而不是深度上限：
// 深度上限会让「合法的深分层」静默丢候选，而环是二值事实——可达即可判。
package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// assertNoCycle 校验「把 target 作为 abstractID 的候选」不会成环。
//
// 判据：从 target 出发沿它的嵌套候选能否走回 abstractID（或走到 target 自己）。
// 自环（A 把 A 加为候选）被第一条判据直接拒绝。
//
// 为什么在事务里跑：读的是候选表，而写候选集是「删光再插」——不在同一个事务里
// 校验的话，两个并发保存可以各自通过校验、合起来成环。校验读的必须是本事务
// 的视图（tx.Models）。
func assertNoCycle(ctx context.Context, models *store.ModelRepo, abstractID, target string) error {
	if abstractID == target {
		return fmt.Errorf("抽象模型不能把自己加为候选")
	}
	// 逐个抽象模型的名字（错误信息要给人看，不能只给 UUID）。
	nameOf := func(id string) string {
		if m, err := models.GetAbstractModel(ctx, id); err == nil && m != nil {
			return m.Name
		}
		return id
	}
	type frame struct {
		id   string
		path []string
	}
	start := frame{id: target, path: []string{nameOf(abstractID), nameOf(target)}}
	visited := map[string]bool{target: true, abstractID: true}
	queue := []frame{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		decls, err := models.ListCandidates(ctx, cur.id)
		if err != nil {
			return fmt.Errorf("查询候选声明失败: %w", err)
		}
		for _, d := range decls {
			if d.Kind != store.KindAbstract {
				continue
			}
			if d.TargetID == abstractID {
				// 走回了起点 = 会成环。路径用名字拼：UUID 对管理员没有意义。
				return fmt.Errorf("会形成循环依赖：%s",
					strings.Join(append(cur.path, nameOf(abstractID)), " → "))
			}
			if visited[d.TargetID] {
				continue
			}
			visited[d.TargetID] = true
			queue = append(queue, frame{
				id:   d.TargetID,
				path: append(append([]string(nil), cur.path...), nameOf(d.TargetID)),
			})
		}
	}
	return nil
}

// blockedAbstractIDs 返回「作为 abstractID 的候选会成环」的抽象模型 id 集合。
//
// 供详情端点（GET /abstract-models/{id}）回给前端：候选编辑弹窗据此把那批
// 项从可选列表里**隐藏**（用户要求），而不是让人点下去再吃一次保存失败。
//
// 口径与 assertNoCycle 同源：同一个判据、同一份「抽象模型 → 它的嵌套候选」
// 读法。区别只是这里不报错而是收集。同源很重要——两处各写一份判据时，
// 隐藏的集合与拒绝的集合迟早分叉，表现为「界面里能选、保存永远失败」。
func blockedAbstractIDs(ctx context.Context, models *store.ModelRepo, abstractID string) ([]string, error) {
	all, err := models.ListAbstractModels(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("列出抽象模型失败: %w", err)
	}
	out := make([]string, 0, len(all))
	for _, m := range all {
		if m.ID == abstractID {
			// 自己：界面上本就不该出现「把自己加为候选」，但列进 blocked
			// 让前端的判断只剩一个规则（看了 blocked 就隐藏），不必再写特例。
			out = append(out, m.ID)
			continue
		}
		if err := assertNoCycle(ctx, models, abstractID, m.ID); err != nil {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// nestedLeafStats 计算一个抽象模型当前展开出的候选条数（叶子数）。
//
// 供管理界面显示「这个抽象候选展开后有几条」：嵌套候选的「可用实例数」不能
// 取它自己所属的某个上游（抽象模型没有上游），只能数叶子——否则界面上
// abstract 候选的实例数恒 0，看起来像「配了但永远选不中」的静默故障
// （这正是 available_instances 当初存在的意义，见 02-admin.md §8.2）。
//
// 成环 / 停用 / 不存在的嵌套目标一律跳过（与运行时 expandDepth 同口径），
// 因此这里算出来的条数与真实路由能展开的条数一致。
func nestedLeafStats(ctx context.Context, models *store.ModelRepo, abstractID string) (leaves int64, ok bool) {
	type frame struct {
		id string
	}
	onStack := map[string]bool{abstractID: true}
	queue := []frame{{id: abstractID}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		decls, err := models.ListCandidates(ctx, cur.id)
		if err != nil {
			return 0, false
		}
		for _, d := range decls {
			if d.Kind != store.KindAbstract {
				leaves++
				continue
			}
			if onStack[d.TargetID] {
				continue // 成环：运行时也会跳过，不计数
			}
			sub, err := models.GetAbstractModel(ctx, d.TargetID)
			if err != nil || sub == nil || sub.Status == store.ModelDisabled {
				continue
			}
			onStack[d.TargetID] = true
			queue = append(queue, frame{id: d.TargetID})
		}
	}
	return leaves, true
}

// candidateItemKind 把候选行的 kind 归一成 HTTP DTO 的两态字符串。
func candidateItemKind(k store.CandidateKind) string {
	if k == store.KindAbstract {
		return string(store.KindAbstract)
	}
	return string(store.KindReal)
}

// parseCandidateItemKind 把请求里的 candidate_kind 解析成 store 的两态。
//
// 缺省与空串都是 real（历史客户端只发 real_model_id，不能因为没给新字段
// 就被判错）；未知取值直接拒绝——静默忽略一个拼错的枚举，结果是管理员以为
// 「把抽象模型加进去了」，实际加进去的是同 UUID 的真实模型（不存在，于是候选
// 恒不生效）。
func parseCandidateItemKind(raw string) (store.CandidateKind, error) {
	switch raw {
	case "", string(store.KindReal):
		return store.KindReal, nil
	case string(store.KindAbstract):
		return store.KindAbstract, nil
	}
	return "", fmt.Errorf("candidate_kind 取值非法 %q（只接受 real / abstract）", raw)
}

// putCandidates 翻候选载荷并整表写入（含嵌套候选的存在性与成环校验）。
//
// 校验项（保存即校验，02-abstract.md §3.4 的同一条纪律）：
//   - kind=real → 目标真实模型存在；
//   - kind=abstract → 目标抽象模型存在、不是自己、加进去不成环。
//
// 为什么不能等到运行时再拦：缺一条候选的表现是「模型少了条回退路径」，
// 而不是报错——管理员永远不知道该去修。保存时拒绝是唯一能让错误可见的位置。
func (mb *modelsBridge) putCandidates(ctx context.Context, models *store.ModelRepo, abstractID string, cands []httpapi.AdminCandidateItem) error {
	out := make([]store.Candidate, 0, len(cands))
	for i, c := range cands {
		kind, err := parseCandidateItemKind(c.Kind)
		if err != nil {
			return err
		}
		rid, err := decodeID(c.RealModelID)
		if err != nil {
			return fmt.Errorf("候选 real_model_id %q 非法: %w", c.RealModelID, err)
		}
		switch kind {
		case store.KindAbstract:
			// 目标抽象模型必须存在（同样的 id 形状在真实模型表里也可能有行，
			// 但那是另一个实体——kind 分支决定去哪儿查）。
			m, err := models.GetAbstractModel(ctx, rid)
			if err != nil || m == nil {
				return fmt.Errorf("第 %d 条候选：抽象模型 %s 不存在", i+1, c.RealModelID)
			}
			if err := assertNoCycle(ctx, models, abstractID, rid); err != nil {
				return fmt.Errorf("第 %d 条候选（%s）：%w", i+1, m.Name, err)
			}
		default:
			if _, err := models.GetRealModel(ctx, rid); err != nil {
				return fmt.Errorf("第 %d 条候选：真实模型 %s 不存在", i+1, c.RealModelID)
			}
		}
		// 声明序由 SetCandidates 按**入参切片顺序**编号，所以这里不能传
		// weight（已退役，恒写 0）也不能自己编 sort_order——入参顺序就是
		// 管理员在界面上排的顺序。
		sc := store.Candidate{AbstractModelID: abstractID, TargetID: rid, Kind: kind}
		if len(c.Constraint) > 0 {
			sc.ConstraintJSON = adminStrPtr(string(c.Constraint))
		}
		out = append(out, sc)
	}
	return models.SetCandidates(ctx, abstractID, out)
}
