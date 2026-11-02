// order.go —— 策略三段式的第②段：排序（03-strategy.md §3）与第③段切换预算（§4.5）。
// 第①段过滤在 candidates.go。
//
// # 2026-11-01 定案：排序改为「管理员手动排序」
//
// 原设计的内置四种自动排序策略（expiry-first / balance / speed / price）
// 与 round-robin 平局决胜**整体退役**。现在的语义只有一条：
//
//	候选的使用顺序 = 管理员在候选集编辑器里排列的声明序；
//	仅当前一个不可用时才使用下一个。
//
// 为什么退役而不是保留成「可选模式」（用户定案的原话是「改为手动排序」）：
//   - 两套排序语义并存时，管理员必须理解「什么时候该用哪套」，而自动排序
//     的输入（余额/到期/TTFT）本身就是**近似值**——同一份配置在不同测量
//     状态下会给出不同顺序，故障反而更难复现。手动序是唯一「配置即事实」
//     的表达。
//   - 手动序天然可解释：trace 里的顺序就是管理员排的顺序，不需要再解释
//     「为什么 B 排在 A 前面」——那是排序策略的输出，不是人的决定。
//
// 声明序的载体是 abstract_candidates.sort_order（迁移 0008 落地）：store 的
// SetCandidates 按入参顺序编号，装配层 pathsOf 按 ListCandidates 的读序
// （sort_order 升序）构造 Path 切片，本文件的 Order 只负责**保持这个次序**
// （同时保持「不在册的排末尾」这一层叠加）。
package routing

// SwitchBudget 是第③段切换的预算（03-strategy.md §4.5：次数与总时长都
// 必须有上限——无上限会把「一批候选同时坏」放大成对上游的小规模打击）。
type SwitchBudget struct {
	// MaxSwitches 最大切换次数（0 = 用默认）。
	MaxSwitches int
	// MaxDuration 总时长预算；零值 = 不限（装配层按客户端超时画像注入）。
	MaxDurationMs int64
	// ElapsedMs 已消耗时长（切换决策时由调用方累计）。
	ElapsedMs int64
}

// DefaultMaxSwitches 默认切换上限。
//
// 2026-11-02 从 3 提到 8，起因是一次生产事故：抽象模型 `latest` 聚合了
// 8 个子模型、展开后几十个候选，但上限 3 意味着**只允许试前 3 个**。
// 而那 3 个恰好都是欠费/凭据过期的上游（402、credential expired），
// 真正能用的第 4 个永远轮不到，用户看到的是稳定的 502「所有上游候选均失败」。
//
// 为什么「3」当初看起来合理、实际不合理：
//   - 它按「候选是一个池子里的等价物」设计，换 3 次还失败就认定池子坏了；
//   - 但真实池子是按**声明序**排的，且不可用候选只被挪到末尾而**不剔除**
//     （03-strategy.md §2 的「跳过但保留兜底」）——于是前几位可能是
//     长期欠费的僵尸上游，而它们每个失败要花 3~5 秒，3 个就吃光了时长预算。
//   - 聚合模型（latest/auto）把这条放大了 8 倍：子模型越多，前几位被
//     僵尸体占据的概率越大。
//
// 为什么提到 8 而不是不限：§4.5 的红线仍然成立——无上限会把「一批候选同时坏」
// 放大成对上游的小规模打击并触发风控。8 是一个「覆盖常见聚合模型前几位坏掉」
// 又不至于打遍全池的量级，且必须与时长预算（AssemblyLayer 的
// SwitchBudget.MaxDurationMs）配合：次数与时长任一先到即停，
// 两道闸共同保证不会无限尝试。
const DefaultMaxSwitches = 8

// CanSwitch 报告预算是否还允许下一次切换。
//
// C7 红线（任务要求）：已吐可见内容不切候选——判定只读
// upstream.StreamStats.VisibleWritten（调用方传入），本包不自造判定。
// 为什么签名是参数而不是引 internal/upstream：routing 是纯决策层，
// VisibleWritten 是布尔事实，接口收窄到 bool 让它不 import upstream
// （装配层搬一个字段即可，测试也无需构造 StreamStats）。
func (b SwitchBudget) CanSwitch(visibleWritten bool, switchesDone int) bool {
	if visibleWritten {
		// 03-strategy.md §4.1：流式串话是已踩过的坑——明确失败好过
		// 悄悄给出错误答案。这条判在预算之前：不可切换与「预算耗尽」
		// 是两种失败，日志里要能区分。
		return false
	}
	max := b.MaxSwitches
	if max <= 0 {
		max = DefaultMaxSwitches
	}
	if switchesDone >= max {
		return false
	}
	if b.MaxDurationMs > 0 && b.ElapsedMs >= b.MaxDurationMs {
		return false
	}
	return true
}

// Order 按**管理员手动声明的次序**返回候选路径（第②段）。
//
// 语义（2026-11-01 定案）：
//   - 入参 paths 的切片顺序就是声明序（装配层按 sort_order 升序构造），
//     本函数**保持**这个次序，不做任何比较；
//   - 不在册（不可用）的路径排到末尾：它们仍然参与尝试（「跳过但保留兜底」
//     ——一次误判不该让整个池子 503），但只有在所有在册路径都试过之后才轮到；
//   - 冷却中的路径同样排末尾（由 Cooldowns.Apply 叠加，见 cooldown.go）。
//
// 为什么返回副本而不是原地改：调用方（装配层）持有的 paths 切片还会被用于
// trace 与诊断；原地改会让「排序前后」这两个观察点变成同一段内存，日志里
// 就再也回答不了「它本来排在几位」。
//
// 稳定性由 slice 的**构造**保证而不是 sort：既然不用比较器，就没有
// 「稳定排序」这回事——切片的先后就是唯一的次序来源（确定性要求 §3.4
// 自动满足）。这正是手动排序比策略链更强的地方。
func Order(paths []Path) []Path {
	if len(paths) == 0 {
		return nil
	}
	out := make([]Path, 0, len(paths))
	var tail []Path
	for _, p := range paths {
		if p.Unavailable {
			tail = append(tail, p)
			continue
		}
		out = append(out, p)
	}
	// 不在册的保持原相对序追加在末尾（同 Order 的主序，不做二次比较）。
	out = append(out, tail...)
	return out
}

// OrderModeManual 是唯一的排位模式（2026-11-01 定案）。
// trace 里如实标注它，让「次序是谁决定的」永远可回答。
const OrderModeManual = "manual"
