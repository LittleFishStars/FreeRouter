package upstream

// latency.go —— 路由排序键「speed」的延迟采样器（EWMA）。
//
// ## 为什么需要这个文件
//
// 03-strategy.md 要求 speed 策略的排序键（TTFT / 总耗时）是**可测量的外部
// 事实**，而不是管理员声明。而 caller.go 每成功一次都在计时（elapsed），
// relay_stream.go 也算了 TTFT——但这些数字只进了健康日志，从没被存成可
// 排序的样本。于是 routing 的 speed 键在装配层只能硬编码「无样本」(-1)，
// 一旦管理员把 speed 配进策略链，排序退化成「全部垫底」的假结果。
//
// 本文件补上「测量 → 样本 → 读数」这条链路：采样器从 Call 路径拿到每次
// 成功调用的 (目标, TTFT, 总耗时)，存成按目标聚合的 EWMA；路由装配层
// （internal/app/routing_bridge.go）调 Latency() 喂给 routing.Path。
//
// ## 为什么用 EWMA 而不是滑动窗口平均
//
// TTFT/总耗时的目标是「最近一段时间的上游实际表现」，聚合值应该是近期样本
// 的平滑：太长会掩盖「上游刚从慢变快」，太短会被单次抖动带偏。EWMA 单 float
// 即可、零内存扩增、不依赖墙钟；α=0.3 对近期敏感又不被毛刺打飞。
//
// 「N 分钟内无样本即失效」由调用方基于 last 判断：本文件只负责「我有多少
// 样本、最新的是多晚」。

import (
	"context"
	"math"
	"sync"
	"time"
)

// DefaultAlpha 是默认 EWMA 系数。α 越大对近期越敏感。
const DefaultAlpha = 0.3

// SamplerKey 标识一条「可采样的目标」= 上游 × 真实模型。
//
// 与 Target/RealModelInfo 的关系：Target.UpstreamID 与 Target.RealModel
// 唯一确定一条上游路径；routing 的 RealModelInfo 带 UpstreamID +
// UpstreamModelID。两者能互相换算，这里统一用 (UpstreamID, RealModel)。
type SamplerKey struct {
	UpstreamID string // UUID
	RealModel  string
}

// latencyEWMA 是一个目标的滑动平均值（LatencySampler 内部使用）。
type latencyEWMA struct {
	ttft  float64   // 平滑后的 TTFT（毫秒）
	total float64   // 平滑后的总耗时（毫秒）
	n     int       // 已吸收样本数（0=无样本）
	last  time.Time // 最近一次样本时刻
}

// record 吸收一次观测。
func (e *latencyEWMA) record(ttftMs, totalMs float64, now time.Time, alpha float64) {
	if e.n == 0 {
		e.ttft = ttftMs
		e.total = totalMs
	} else {
		e.ttft = e.ttft*(1-alpha) + ttftMs*alpha
		e.total = e.total*(1-alpha) + totalMs*alpha
	}
	e.n++
	e.last = now
}

// LatencySnapshot 是一次读数的结果。
type LatencySnapshot struct {
	TTFTMs     float64
	TotalMs    float64
	LastSample time.Time
}

// LatencyObserver 是采样器的窄输入接口（防依赖倒置，Call 路径测试可替换）。
type LatencyObserver interface {
	// RecordSuccess 记录一次成功调用的 TTFT 与总耗时（毫秒）。
	RecordSuccess(ctx context.Context, target SamplerKey, ttftMs, totalMs float64, now time.Time)
}

// LatencyQuery 是采样器的读接口（简述可有值读）。
type LatencyQuery interface {
	// Latency 返回某目标的平滑 TTFT 与总耗时（毫秒），以及最近样本时刻。
	// 无样本时返回（-1, -1, zero）——与 routing 的「无样本垫底」语义一致。
	Latency(key SamplerKey) LatencySnapshot
}

// LatencySampler 是并发安全的 EWMA 采样器。
type LatencySampler struct {
	alpha float64

	mu   sync.RWMutex
	rows map[SamplerKey]*latencyEWMA
}

// NewLatencySampler 建采样器。alpha <= 0 时用 DefaultAlpha。
func NewLatencySampler(alpha float64) *LatencySampler {
	if alpha <= 0 {
		alpha = DefaultAlpha
	}
	return &LatencySampler{
		alpha: alpha,
		rows:  make(map[SamplerKey]*latencyEWMA),
	}
}

// RecordSuccess 实现 LatencyObserver。
func (s *LatencySampler) RecordSuccess(_ context.Context, key SamplerKey, ttftMs, totalMs float64, now time.Time) {
	// 非法样本（负值）拒绝记录：调用方传 -1 是「无样本」哨兵，不该进窗口。
	if math.IsNaN(ttftMs) || ttftMs < 0 || totalMs < 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	e, ok := s.rows[key]
	if !ok {
		e = &latencyEWMA{}
		s.rows[key] = e
	}
	e.record(ttftMs, totalMs, now, s.alpha)
	s.mu.Unlock()
}

// Latency 实现 LatencyQuery。
func (s *LatencySampler) Latency(key SamplerKey) LatencySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.rows[key]
	if !ok {
		return LatencySnapshot{TTFTMs: -1, TotalMs: -1}
	}
	return LatencySnapshot{
		TTFTMs:     e.ttft,
		TotalMs:    e.total,
		LastSample: e.last,
	}
}

// 编译期断言：实现两个接口。
var _ LatencyObserver = (*LatencySampler)(nil)
var _ LatencyQuery = (*LatencySampler)(nil)

// SamplingSink 是 httpapi.LatencySink 的装配适配器：把「按 real_model id」
// 的 httpapi 延迟样本，映射成「按 (upstreamID, modelID)」的 SamplerKey。
//
// 为什么需要：httpapi 在 settle 时拿到的是 realModelID（real_models 行 id），
// 而采样器按 (UpstreamID, RealModel) 存——routing 的 RealModelInfo 带这两者。
// 中间差一个「realModelID → (UpstreamID, RealModel)」的查表。装配层提供这个
// 函数；本适配器持有 table 并缓存结果。
//
// table 返回空串/key 不合法（查不到真实模型）时，该样本丢弃——不是错误。
type LatencySinkKeySampler struct {
	sampler *LatencySampler
	// resolve 把 realModelID 解析成 SamplerKey；返回 ok=false 表示未知行。
	resolve func(realModelID string) (SamplerKey, bool)
}

func (l *LatencySinkKeySampler) RecordLatency(ctx context.Context, realModelID string, ttftMs, totalMs int64) {
	key, ok := l.resolve(realModelID)
	if !ok {
		return
	}
	l.sampler.RecordSuccess(ctx, key, float64(ttftMs), float64(totalMs), time.Now())
}
