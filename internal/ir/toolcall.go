package ir

import (
	"fmt"
	"strings"
	"time"
)

// 本文件实现 tool_call 的 id 重组（docs/architecture/02-ir.md §6）。
//
// 问题：OpenAI 流式的工具调用是分片到达的，而各上游实现不一致，实测存在首片不带 id、
// name 被分片、多 index 交错等变形。若按"到达顺序"或"每片重建 id"，会得到凭空生成的 id，
// 客户端无法把工具结果对回调用，表现为"工具调用错乱"。
//
// 解法：以 index 为唯一键维护缓冲表。index 是协议内的稳定标识，而到达顺序受网络分片与
// 上游并发写影响——用 index 做键，即使 0/1 交错也能各自正确重组。

// 缓冲阈值。
//
// 为什么要有阈值：等待 id 的成本是延迟，但缓冲的成本是内存。恶意或异常的上游可以一直不发 id
// 地灌参数，把我们的内存拖爆。阈值到达时放弃等待、用合成 id 发出 start（§6.2）。
//
// 这两个值是拍定的初值，需用真实上游"id 出现在第几片"的分布来校准；校准前不要当调参项对外暴露。
const (
	DefaultToolCallBufferBytes = 8 * 1024
	DefaultToolCallBufferDelay = 2 * time.Second
)

// toolCallBuffer 是单个 index 的重组缓冲。
type toolCallBuffer struct {
	Index     int
	ID        string
	Name      string
	Arguments strings.Builder
	Started   bool
	Bytes     int
	// firstSeen 用于阈值超时的判定；由调用方注入时间，便于测试。
	firstSeen time.Time
	// warned 记录"后续片 id 与权威 id 不同"是否已告警，避免同一 index 刷屏。
	warned bool
}

// ToolCallReassembler 是一份流内的重组器。
//
// 为什么按流一份而不是全局一份：id 的权威性只在单次请求内成立；跨请求复用状态会在并发下
// 串台（这正是 §6.2 要求合成 id 带 request_id 的原因）。
type ToolCallReassembler struct {
	requestID string
	maxBytes  int
	maxDelay  time.Duration
	now       func() time.Time
	bufs      map[int]*toolCallBuffer
	order     []int // 首次出现的顺序，保证输出顺序稳定
	warnings  []Warning
}

// NewToolCallReassembler 构造重组器。requestID 用于合成 id；为空时合成 id 退化为
// call_unknown_{index}——仍然确定，只是可对账性变差，因此上层应当总是传 request id。
func NewToolCallReassembler(requestID string) *ToolCallReassembler {
	return &ToolCallReassembler{
		requestID: requestID,
		maxBytes:  DefaultToolCallBufferBytes,
		maxDelay:  DefaultToolCallBufferDelay,
		now:       time.Now,
		bufs:      make(map[int]*toolCallBuffer),
	}
}

// SetLimits 覆盖缓冲阈值，供测试构造"阈值触发"的场景。
func (r *ToolCallReassembler) SetLimits(maxBytes int, maxDelay time.Duration) {
	if maxBytes > 0 {
		r.maxBytes = maxBytes
	}
	if maxDelay > 0 {
		r.maxDelay = maxDelay
	}
}

// SetClock 注入时钟，避免测试依赖真实时间。
func (r *ToolCallReassembler) SetClock(now func() time.Time) {
	if now != nil {
		r.now = now
	}
}

// Warnings 返回重组过程中记录的告警（id 冲突、name 重复等）。
func (r *ToolCallReassembler) Warnings() []Warning { return r.warnings }

// Fragment 是一片到达的工具调用增量。
//
// 字段全部可空，因为各上游只发变化的部分：id 可能首片给也可能后续片才给，name 可能被拆开。
type Fragment struct {
	Index     int
	ID        string
	Name      string
	Arguments string
	// Complete 表示上游明确说这一片是该调用的收尾（OpenAI 没有这个信号，Anthropic 有
	// content_block_stop；由编码器补齐语义）。
	Complete bool
}

// syntheticID 生成确定性合成 id。
//
// 为什么带 request_id：若两台并发请求都产生 call_..._0，客户端日志与会话记录会混淆；
// 带请求 ID 后 id 全局唯一且可反查归因（§6.2）。
func (r *ToolCallReassembler) syntheticID(index int) string {
	rid := r.requestID
	if rid == "" {
		rid = "unknown"
	}
	return fmt.Sprintf("call_%s_%d", rid, index)
}

// Accept 处理一片增量，返回应当立即向下游发出的事件。
//
// 核心决策：**在拿到 id 与 name 之前不发 tool_call_start**。
// 为什么：C7 要求已吐可见内容后不能切换候选；若先用合成 id 发出 start，后面拿到真 id 就无法
// 回头改（客户端已经收到）。推迟发 start 把"等 id"的成本限制在缓冲里，而缓冲的失败成本只是延迟。
func (r *ToolCallReassembler) Accept(f Fragment) ([]Event, error) {
	b, ok := r.bufs[f.Index]
	if !ok {
		b = &toolCallBuffer{Index: f.Index, firstSeen: r.now()}
		r.bufs[f.Index] = b
		r.order = append(r.order, f.Index)
	}

	// id 的权威性：首个非空 id 为准。后续不同 id 保留首个并告警——很可能是上游复用 index 的错误。
	if f.ID != "" {
		switch {
		case b.ID == "":
			b.ID = f.ID
		case b.ID != f.ID && !b.warned:
			b.warned = true
			r.warnings = append(r.warnings, Warning{
				Param:   fmt.Sprintf("tool_calls[%d].id", f.Index),
				Message: fmt.Sprintf("同一 index 出现不同 id（保留 %q，忽略 %q）", b.ID, f.ID),
			})
		}
	}

	// name 分片：拼接。同一 index 出现第二个"非分片 name"只有在已 Started 后才是违规，
	// 因为 Started 之前我们还没有把 name 交付出去，拼接是安全的。
	if f.Name != "" {
		if b.Name == "" || strings.HasPrefix(f.Name, b.Name) {
			b.Name = f.Name
		} else if b.Started {
			return nil, &InvalidRequestError{
				Param:  fmt.Sprintf("tool_calls[%d].function.name", f.Index),
				Detail: fmt.Sprintf("同一 index 出现第二个函数名（已有 %q，又来 %q）", b.Name, f.Name),
			}
		} else {
			// 尚未发出：当作分片拼接，比在 start 之前就报错更宽容，且不产生错误语义。
			b.Name += f.Name
		}
	}

	var events []Event
	if !b.Started && b.ID != "" && b.Name != "" {
		b.Started = true
		events = append(events, Event{Type: EvToolCallStart, Index: b.Index, Call: &ToolCallPart{
			Idx: b.Index, ID: b.ID, Name: b.Name, Partial: true,
		}})
	}

	if f.Arguments != "" {
		b.Bytes += len(f.Arguments)
		if b.Started {
			events = append(events, Event{Type: EvToolCallDelta, Index: b.Index, Text: f.Arguments, Call: &ToolCallPart{
				Idx: b.Index, ID: b.ID, Name: b.Name, Arguments: f.Arguments, Partial: true,
			}})
		} else {
			// 未发出 start 时也要把参数留在缓冲区，否则 flush 时参数会丢——那就是静默降级。
			b.Arguments.WriteString(f.Arguments)
		}
	}

	if f.Complete {
		more, err := r.close(b)
		if err != nil {
			return events, err
		}
		events = append(events, more...)
	}
	return events, nil
}

// FlushExpired 处理超过阈值的缓冲：放弃等待 id，用合成 id 发出 start（§6.2）。
//
// 为什么不让调用方自己判：阈值是"字节数或时间"两个条件的合取，散在调用方会出现只判字节
// 不判时间的实现，结果是慢速上游永远占着内存。
func (r *ToolCallReassembler) FlushExpired() []Event {
	var events []Event
	for _, idx := range r.order {
		b := r.bufs[idx]
		if b.Started {
			continue
		}
		if b.ID == "" && (b.Bytes >= r.maxBytes || r.now().Sub(b.firstSeen) >= r.maxDelay) {
			b.ID = r.syntheticID(idx)
			if b.Name == "" {
				// 连函数名都没有：给一个显式占位而不是空串，让客户端的错误信息可读。
				b.Name = ""
			}
			b.Started = true
			r.warnings = append(r.warnings, Warning{
				Param:   fmt.Sprintf("tool_calls[%d].id", idx),
				Message: "等待 id 超过阈值，已使用合成 id " + b.ID,
			})
			events = append(events, Event{Type: EvToolCallStart, Index: idx, Call: &ToolCallPart{
				Idx: idx, ID: b.ID, Name: b.Name, Partial: true,
			}})
		}
	}
	return events
}

// CloseAll 在流结束时收尾所有未完成的调用，实现 I2（每个 start 必有 end）。
//
// 流在中间结束时（上游断连、客户端取消）必须补发 end（Partial=true）：否则客户端会一直等
// 一个永远不会来的收尾，表现为"工具调用挂死"。
func (r *ToolCallReassembler) CloseAll() []Event {
	var events []Event
	for _, idx := range r.order {
		b := r.bufs[idx]
		if b.ID == "" {
			b.ID = r.syntheticID(idx)
		}
		if !b.Started {
			b.Started = true
			events = append(events, Event{Type: EvToolCallStart, Index: idx, Call: &ToolCallPart{
				Idx: idx, ID: b.ID, Name: b.Name, Partial: true,
			}})
		}
		events = append(events, Event{Type: EvToolCallEnd, Index: idx, Call: &ToolCallPart{
			Idx:       idx,
			ID:        b.ID,
			Name:      b.Name,
			Arguments: b.Arguments.String(),
			Partial:   true,
		}})
	}
	return events
}

// close 收尾单个 index。
func (r *ToolCallReassembler) close(b *toolCallBuffer) ([]Event, error) {
	if b.ID == "" {
		// 上游说了"这个调用结束了"却始终没给 id：用合成 id 补齐，不能让 end 没有 id。
		b.ID = r.syntheticID(b.Index)
		r.warnings = append(r.warnings, Warning{
			Param:   fmt.Sprintf("tool_calls[%d].id", b.Index),
			Message: "工具调用结束时仍无 id，已使用合成 id " + b.ID,
		})
	}
	var events []Event
	if !b.Started {
		b.Started = true
		events = append(events, Event{Type: EvToolCallStart, Index: b.Index, Call: &ToolCallPart{
			Idx: b.Index, ID: b.ID, Name: b.Name, Partial: true,
		}})
	}
	events = append(events, Event{Type: EvToolCallEnd, Index: b.Index, Call: &ToolCallPart{
		Idx:       b.Index,
		ID:        b.ID,
		Name:      b.Name,
		Arguments: b.Arguments.String(),
	}})
	delete(r.bufs, b.Index)
	return events, nil
}
