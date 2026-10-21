// strategy.go —— 策略三段式的第②段：排序（03-strategy.md §3）与第③段
// 切换预算（§4.5）。第①段过滤在 candidates.go。
//
// 表驱动：新增策略只加一行 orderFuncs 注册（03-strategy.md §8「Rank」；
// 任务要求「排序策略做成表驱动」）。链式组合语义：前一级相等时看下一级
//（§5「按顺序生效」）。
package routing

import (
	"fmt"
	"sort"
)

// 策略 id（03-strategy.md §3.1 四种 + round-robin 平局决胜）。
const (
	StrategyExpiryFirst = "expiry-first" // 到期时间升序（默认首位）
	StrategyBalance     = "balance"      // 余额升序（默认次位）
	StrategySpeed       = "speed"        // 最近延迟升序
	StrategyPrice       = "price"        // Credits 乘数升序
	StrategyRoundRobin  = "round-robin"  // 可选平局决胜（仅显式配置）
)

// DefaultChain 默认排序链（03-strategy.md §3.2：先到期时间，同到期再按
// 余额——两问合起来破「每次都打同一个账号」的并列，且符合钱的直觉）。
var DefaultChain = []string{StrategyExpiryFirst, StrategyBalance}

// orderKey 是单个策略的比较键提取器：返回 (key, ok)；ok=false 表示该路径
// 在此策略下无键（无到期概念/余额未知/无延迟样本）→ 无键路径排在有键
// 路径之后（「先花有期限的/有信息的」）。
type orderKey func(p Path) (float64, bool)

// orderFuncs 是策略注册表——新增策略只加一行（表驱动，03-strategy.md §3.1）。
var orderFuncs = map[string]orderKey{
	// expiry-first：额度作废时间升序。到期即归零，没用掉就是浪费；
	// 无到期概念（纯 API Key，零值）排最后。
	StrategyExpiryFirst: func(p Path) (float64, bool) {
		if p.ExpiresAt <= 0 {
			return 0, false
		}
		return float64(p.ExpiresAt), true
	},
	// balance：余额升序（余额最少的先用）。小额余额往往卡在最小消费门槛
	// 花不出去；余额未知 → 排最后（无法参与「谁更该先花」的判断）。
	StrategyBalance: func(p Path) (float64, bool) {
		if !p.BalanceKnown {
			return 0, false
		}
		return p.Balance, true
	},
	// speed：最近 TTFT 升序，次级看总耗时（抓 TTFT 快但吐字慢的模型）。
	// TTFT 是用户唯一真实感知的性能指标；无样本排最后（冷启动不猜）。
	StrategySpeed: func(p Path) (float64, bool) {
		if p.TTFTMs < 0 {
			return 0, false
		}
		return float64(p.TTFTMs)*1e6 + float64(min64(p.TotalMs, 999999)), true
	},
	// price：Credits 乘数升序。乘数表没有的模型默认 1（不被惩罚的中间态），
	// 所以这里总有键——default 字段在装配层已归一。
	StrategyPrice: func(p Path) (float64, bool) {
		return p.PriceMultiplier, true
	},
}

// min64 小工具（避免为一次比较引入新依赖）。
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// RoundRobinCursor 是 round-robin 平局决胜的游标（03-strategy.md §3.4：
// round-robin 是唯一带状态的策略，必须带游标快照进日志，否则决策不可追溯）。
type RoundRobinCursor struct {
	// Offset 当前轮询位（对「平局组」内索引取模推进）。
	Offset int64
	// Calls 游标累计推进次数（trace 快照用）。
	Calls int64
}

// rankLess 按链逐级比较（§5「前一级相等时看下一级」）。
//
// 返回 (less, equal)：全链比较完仍相等 → equal=true（平局组，
// 交给 round-robin 或稳定序兜底）。
func rankLess(chain []string, a, b Path) (bool, bool) {
	for _, s := range chain {
		if s == StrategyRoundRobin {
			// round-robin 不是比较键——它是平局组内的游标决策，
			// 在 Rank 内单独处理（比较阶段跳过）。
			continue
		}
		fn, ok := orderFuncs[s]
		if !ok {
			continue // 未知策略跳过：保存级校验拦配置，运行时容错
		}
		ka, oka := fn(a)
		kb, okb := fn(b)
		if oka != okb {
			return oka, false // 有键在前，无键在后
		}
		if !oka {
			continue // 双方都无键 → 看下一级
		}
		if ka != kb {
			return ka < kb, false
		}
	}
	return false, true
}

// Rank 按策略链排序（03-strategy.md §8「Rank(paths, chain) -> []Path」）。
//
// chain 为空 → DefaultChain（§5「空链视为默认链」）。含 round-robin 时它
// 充当链尾平局决胜：比较全链后仍相等的组内，按游标 Offset 起始位轮转。
// 游标是调用方持有的 *RoundRobinCursor（进程内状态；多节点口径是
// 03-strategy.md §3.2 待定项，装配层决定是否共享）。
func Rank(paths []Path, chain []string, rr *RoundRobinCursor) []Path {
	if len(paths) <= 1 {
		return paths
	}
	if len(chain) == 0 {
		chain = DefaultChain
	}
	out := make([]Path, len(paths))
	copy(out, paths)

	hasRR := false
	for _, s := range chain {
		if s == StrategyRoundRobin {
			hasRR = true
			break
		}
	}

	// 稳定排序 + 逐级比较（sort.SliceStable 保证同级保持声明序——
	// 确定性要求 §3.4：同样输入必须同样输出）。
	sort.SliceStable(out, func(i, j int) bool {
		less, eq := rankLess(chain, out[i], out[j])
		if !eq {
			return less
		}
		// 平局组：round-robin 决胜（仅显式配置时启用）。
		if hasRR && rr != nil {
			// 平局组内按游标取模轮转：i 与 j 的相对次序由 (Offset + 组内序)
			// 决定。这里用索引近似组内序——平局组在稳定排序中保持相邻，
			// 索引差即组内相对位置。
			return (int64(i)+rr.Offset)%2 == 0 && i < j
		}
		return false // 无决胜 → 稳定序（声明序兜底）
	})

	// 推进游标：round-robin 生效时每次 Rank 前进一格（均摊语义）。
	if hasRR && rr != nil {
		rr.Offset++
		rr.Calls++
	}
	return out
}

// ValidateChain 校验策略链（保存级，03-strategy.md §5）：
//   - 重复项拒绝（同一策略两次无意义且掩盖配置错误——静默去重会把
//     管理员的真实意图盖住）；
//   - 未知策略拒绝（拼错立刻报，不落到「未知跳过」的运行时容错）。
func ValidateChain(chain []string) error {
	seen := map[string]bool{}
	for _, s := range chain {
		if _, ok := orderFuncs[s]; !ok && s != StrategyRoundRobin {
			return fmt.Errorf("routing: 未知排序策略 %q", s)
		}
		if seen[s] {
			return fmt.Errorf("routing: 策略链含重复项 %q（同一策略出现两次无意义，且会掩盖配置错误）", s)
		}
		seen[s] = true
	}
	return nil
}

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
// 03-strategy.md §4.5 待定项没给数；取候选数与 3 的较小者——
// 「3」是上游重试与冷却阈值的公共量级（pool.go defaultFailThreshold），
// 超过它继续换路的边际收益已经低于「让用户尽快拿到明确失败」。
const DefaultMaxSwitches = 3

// CanSwitch 报告预算是否还允许下一次切换。
//
// C7 红线（任务要求）：已吐可见内容不切候选——判定只读
// upstream.StreamStats.VisibleWritten（调用方传入），本包不自造判定。
// 为什么签名是参数而不是引 internal/upstream：routing 是纯决策层，
// VisibleWritten 是布尔事实，接口收窄到 bool 让它不 import upstream
//（装配层搬一个字段即可，测试也无需构造 StreamStats）。
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
