// cooldown.go —— 冷却：上游失败后的短期冷却窗口（任务要点④；
// 03-strategy.md §2.1 F5/F6 的 cooldown 侧 + lead 指令的排位语义）。
//
// 与 internal/upstream Health 状态机的关系（任务要点：只读其状态，不重写）：
// upstream.Health 是「连续失败计数驱动的状态机」（healthy/degraded/cooling），
// 归上游层所有；本包定义只读接口 HealthView 供装配层把 Health.State() 的
// 快照搬进来。routing 不复刻第二套计数——两套状态机只会漂移出
// 「路由认为在冷却、上游认为健康」的不可解释分裂。
package routing

import (
	"sync"
	"time"
)

// HealthView 是上游健康状态的只读面（upstream.HealthState 的镜像子集）。
//
// 为什么不直接 import internal/upstream：routing 是纯决策层（包注释），
// upstream 的 State() 返回具体类型 HealthState——引它会把 upstream 整包
// 拉进 routing 的依赖闭包，测试就得构造 upstream 的运行件。接口收窄到
// 一个方法后装配层只需要一行适配。
type HealthView interface {
	// StateOf 返回实体当前健康状态快照："healthy" | "degraded" | "cooling"。
	StateOf(entityID int64) string
}

// 冷却默认参数。
const (
	// DefaultCooldownMs 默认冷却窗口。为什么短：冷却不是熔断——它是
	// 「别立刻再打刚失败的目标」的喘息；任务语义是排末尾不是摘除，
	// 窗口过长等于变相摘除（排末尾的候选在别人都失败时还是要顶上）。
	DefaultCooldownMs = 30_000
	// DefaultCooldownJitterMs 冷却时长抖动上限。为什么抖动：同一批请求
	// 同时撞同一失败目标时，固定窗口会让它们在窗口结束的同一毫秒集体
	// 重试（thundering herd）；抖动把重试摊开。
	DefaultCooldownJitterMs = 10_000
)

// CooldownEntry 是一次冷却记录。
type CooldownEntry struct {
	// EntityID 冷却实体 id（候选的 RealModel.ID 或上游 id——口径由装配层
	// 定，本包只按 id 记账）。
	EntityID int64
	// Until 冷却截止时刻（Unix 毫秒）。
	Until int64
	// Reason 稳定原因码："failure" | "health_cooling" | "health_degraded"。
	// degraded 也进冷却表的理由：lead 指令「排末尾而非剔除」覆盖两类
	// 不可优先态（cooling 与 degraded 都不该被优先选中，但都保留在场）。
	Reason string
}

// Cooldowns 是冷却账本：进程内并发安全的 id → 截止时刻表。
//
// 为什么进程内而不是落库：冷却是秒级运行态（30s 量级），落库的写放大
// 与读延迟都配不上它的生命周期；重启丢冷却是可接受的（重启本身就该
// 给上游一次重新开始的机会）。
type Cooldowns struct {
	mu      sync.Mutex
	entries map[int64]CooldownEntry
	nowMs   func() int64 // 时钟注入点（测试确定性）
	jitter  func() int64 // [0, DefaultCooldownJitterMs) 抖动源
}

// NewCooldowns 构造冷却账本（nil 时钟 = 真实时钟）。
func NewCooldowns() *Cooldowns {
	return &Cooldowns{
		entries: map[int64]CooldownEntry{},
		nowMs: func() int64 {
			return time.Now().UnixMilli()
		},
		jitter: func() int64 {
			// 简单抖动：纳秒时钟截断取模。不用 math/rand 全局源——
			// 并发安全已在 mu 内，且避免给测试引入不可复现性。
			return time.Now().UnixNano() % DefaultCooldownJitterMs
		},
	}
}

// SetClock 注入时钟（测试专用；生产路径不调用）。
func (c *Cooldowns) SetClock(nowMs func() int64) *Cooldowns {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowMs = nowMs
	return c
}

// MarkFailure 把实体置入冷却（失败驱动的主动冷却）。
//
// durationMs <= 0 时用 DefaultCooldownMs。抖动在窗口上追加。
func (c *Cooldowns) MarkFailure(entityID int64, durationMs int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if durationMs <= 0 {
		durationMs = DefaultCooldownMs
	}
	now := c.nowMs()
	until := now + durationMs + c.jitter()
	// 已有更长冷却时不缩短（失败叠加应该延长或保持，缩短等于把
	// 「连续失败」的计数语义丢掉）。
	if old, ok := c.entries[entityID]; ok && old.Until > until {
		return
	}
	c.entries[entityID] = CooldownEntry{EntityID: entityID, Until: until, Reason: "failure"}
}

// SyncFromHealth 把 upstream Health 状态快照同步进冷却账本（只读镜像，
// 不驱动 Health 状态机——那是 upstream 的职责）。
//
// cooling → 置冷却（upstream 已判定不可优先）；degraded → 同样置冷却但
// 原因不同（排末尾语义一致，trace 里可区分）；healthy → 清除该实体的
// 本包冷却记录（「任一次成功即复位」由 upstream 维护，我们只是镜像）。
//
// durationMs 是镜像冷却的窗口长度：upstream 的 Health 没有携带截止时刻
//（状态机是计数驱动的），镜像窗口用本包默认——窗口到期后若 Health 仍
// 是 cooling，下一次 Sync 会重新置上，语义上无空洞。
func (c *Cooldowns) SyncFromHealth(view HealthView, entityIDs []int64, durationMs int64) {
	if view == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if durationMs <= 0 {
		durationMs = DefaultCooldownMs
	}
	now := c.nowMs()
	for _, id := range entityIDs {
		switch view.StateOf(id) {
		case "cooling":
			c.entries[id] = CooldownEntry{EntityID: id, Until: now + durationMs, Reason: "health_cooling"}
		case "degraded":
			c.entries[id] = CooldownEntry{EntityID: id, Until: now + durationMs, Reason: "health_degraded"}
		case "healthy":
			// 只清本包失败驱动的记录；health_cooling/health_degraded
			// 镜像记录由下一次 Sync 覆盖或到期自然消失——这里不清镜像
			// 反而会跟 upstream 状态机打架（upstream 仍 cooling 而我们清了）。
			if e, ok := c.entries[id]; ok && e.Reason == "failure" {
				delete(c.entries, id)
			}
		}
	}
}

// Cooling 报告实体当前是否在冷却窗口内（过期记录惰性清除）。
func (c *Cooldowns) Cooling(entityID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[entityID]
	if !ok {
		return false
	}
	if e.Until <= c.nowMs() {
		delete(c.entries, entityID)
		return false
	}
	return true
}

// Apply 排位：冷却中的路径排到末尾而非剔除（lead 指令④——最后一个候选
// 冷却时若剔除会 no_candidate，而「冷却」恰恰意味着「它失败过但可能恢复」，
// 排末尾让它在没有更好选择时仍然顶上；503 与「还能试冷的」之间永远选后者）。
//
// 同在冷却的内部保持原相对序（稳定分区——确定性要求 03-strategy.md §3.4）。
// 返回新切片（不修改入参）；同时把 CooledDown 标志写进路径（trace 用）。
func (c *Cooldowns) Apply(paths []Path) []Path {
	c.mu.Lock()
	now := c.nowMs()
	// 先把过期记录清掉（顺带拿一致的快照）。
	for id, e := range c.entries {
		if e.Until <= now {
			delete(c.entries, id)
		}
	}
	cooling := make(map[int64]bool, len(c.entries))
	for id := range c.entries {
		cooling[id] = true
	}
	c.mu.Unlock()

	out := make([]Path, 0, len(paths))
	cold := make([]Path, 0)
	for _, p := range paths {
		if cooling[p.RealModel.ID] {
			p.CooledDown = true
			cold = append(cold, p)
		} else {
			out = append(out, p)
		}
	}
	return append(out, cold...)
}
