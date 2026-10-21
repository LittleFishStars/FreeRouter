// trace.go —— x-fr-trace 调试头（任务要点⑤；02-abstract.md §6.3）。
//
// 输出「抽象模型 → 弃用链 → 过滤 → 排序 → 最终选择」的完整决策过程，
// 让管理员能回答「为什么这单走了 B 而不是 A」（03-strategy.md §1 归因
// 要求——过滤与排序对管理员是完全不同的信号）。
//
// 红线（02-abstract.md §6.3 两条约束）：
//  1. 不含凭据明文，也不含能反查具体账户的信息（用内部 id 不用显示名）；
//  2. 不含 base_url（渠道构成免费送出去等于把池子抄走的图纸）。
package routing

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TraceStage 记录决策链每一阶段的摘要。
type TraceStage struct {
	// Stage 阶段名：resolve | filter | order | select。
	Stage string `json:"stage"`
	// In 进入该阶段的路径数。
	In int `json:"in"`
	// Out 离开该阶段的路径数。
	Out int `json:"out"`
	// Dropped 被剔除的数量（filter 阶段）。
	Dropped int `json:"dropped,omitempty"`
}

// TraceFilteredOut 是被过滤路径的脱敏记录（02-abstract.md §2.3 的
// 「险些可用」诊断——管理员一眼看出是池子空了、健康全红了还是额度全干了）。
type TraceFilteredOut struct {
	// RealModelID 内部 id（可反查管理端，不可反查贡献者账户）。
	RealModelID int64 `json:"real_model_id"`
	// Channel 渠道名。为什么渠道名可以出：管理端目录本来就按渠道分组，
	// x-fr-trace 的可见范围仅管理端与白名单组（§6.3）——租户侧拿不到这个头。
	Channel string `json:"channel"`
	// Reason 稳定原因码（FilterOut.Reason）。
	Reason string `json:"reason"`
}

// TraceOrder 记录排序后的最终序列（只有渠道与 id，无凭据无 URL）。
type TraceOrder struct {
	RealModelID int64  `json:"real_model_id"`
	Channel     string `json:"channel"`
	// UpstreamModelID 真实模型 id：trace 面向管理员（§6.3 可见范围），
	// 管理端目录本来就显示它——不含它反而让 trace 无法回答「落到了谁」。
	UpstreamModelID string `json:"upstream_model_id"`
	// CooledDown 是否冷却中（排末尾的标记）。
	CooledDown bool `json:"cooled_down,omitempty"`
}

// Trace 是一次路由决策的完整记录。
type Trace struct {
	// RequestedModel 用户请求的抽象模型名。
	RequestedModel string `json:"requested_model"`
	// Chain 弃用链（未弃用时长度 1）。为什么把链放进 trace：用户请求 A
	// 落到 C 时管理员需要看到中间每一跳（04-deprecation.md §2.3 调试需要）。
	Chain []string `json:"chain,omitempty"`
	// CycleBroken 弃用链检出环（已降级无替代——缺陷信号，§2.4）。
	CycleBroken bool `json:"cycle_broken,omitempty"`
	// Stages 阶段摘要序列。
	Stages []TraceStage `json:"stages"`
	// FilteredOut 被过滤路径（脱敏）。
	FilteredOut []TraceFilteredOut `json:"filtered_out,omitempty"`
	// Order 排序后序列（最终选择是 Order[0]）。
	Order []TraceOrder `json:"order"`
	// SelectedID 最终选中的 RealModel.ID（Order 非空时等于 Order[0].RealModelID；
	// 单列字段让消费方不必做空切片判断）。
	SelectedID int64 `json:"selected_id,omitempty"`
	// Strategy 实际生效的策略链（含默认链展开）。
	Strategy []string `json:"strategy"`
}

// BuildTrace 从三段式的中间产物构造 trace（纯函数——确定性要求
// 03-strategy.md §3.4，同样的路由决策必须产出同样的 trace）。
//
// 参数：
//   - resolved：Resolve 的产物（模型/链/候选）；
//   - filtered/outs：FilterPaths 的产物；
//   - ordered：Rank+Cooldowns.Apply 之后的最终序列；
//   - chain：实际生效的策略链（空 = 默认链，这里展开记录）。
func BuildTrace(resolved *Resolved, filtered []Path, outs []FilterOut, ordered []Path, chain []string) *Trace {
	t := &Trace{
		Stages:   make([]TraceStage, 0, 3),
		Strategy: chain,
	}
	if len(t.Strategy) == 0 {
		t.Strategy = DefaultChain
	}
	if resolved != nil {
		t.RequestedModel = resolved.Model.Name
		t.Chain = resolved.Chain
		t.CycleBroken = resolved.CycleBroken
		// resolve 阶段：声明候选 → 展开候选。
		t.Stages = append(t.Stages, TraceStage{
			Stage: "resolve", In: len(resolved.Candidates), Out: len(resolved.Candidates),
		})
	}

	// filter 阶段。
	t.Stages = append(t.Stages, TraceStage{
		Stage: "filter", In: len(filtered) + len(outs), Out: len(filtered), Dropped: len(outs),
	})
	for _, o := range outs {
		fo := TraceFilteredOut{RealModelID: o.PathID, Reason: o.Reason}
		// 渠道名从过滤输入里找（outs 只带 id——找一遍是 O(n²) 但 n 是
		// 候选数量级（几十），可接受；换 map 会在纯函数里引入重复数据面）。
		for _, p := range filtered {
			if p.RealModel.ID == o.PathID {
				fo.Channel = normalizeChannel(p.RealModel.Channel)
				break
			}
		}
		// 被过滤的路径不在 filtered 里（它们被剔了）——从原始候选找渠道。
		if fo.Channel == "" && resolved != nil {
			for _, rm := range resolved.Candidates {
				if rm.ID == o.PathID {
					fo.Channel = normalizeChannel(rm.Channel)
					break
				}
			}
		}
		t.FilteredOut = append(t.FilteredOut, fo)
	}

	// order 阶段 + 最终序列。
	t.Stages = append(t.Stages, TraceStage{Stage: "order", In: len(filtered), Out: len(ordered)})
	for _, p := range ordered {
		t.Order = append(t.Order, TraceOrder{
			RealModelID:     p.RealModel.ID,
			Channel:         normalizeChannel(p.RealModel.Channel),
			UpstreamModelID: p.RealModel.UpstreamModelID,
			CooledDown:      p.CooledDown,
		})
	}
	if len(ordered) > 0 {
		t.SelectedID = ordered[0].RealModel.ID
	}
	return t
}

// HeaderName 是 trace 响应头的名字（02-abstract.md §6.3）。
const HeaderName = "x-fr-trace"

// HeaderValue 把 trace 编码为响应头值（单行 JSON）。
//
// JSON 而不是逗号拼接：决策过程是结构化数据，拼接格式在字段增多后
// 无法自描述；JSON 消费方（管理端面板/curl -D- 人眼）都能直接读。
func (t *Trace) HeaderValue() (string, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("routing: 编码 trace 头失败: %w", err)
	}
	return string(b), nil
}

// ParseTraceHeader 解析 trace 头值（测试与消费方校验用）。
func ParseTraceHeader(v string) (*Trace, error) {
	var t Trace
	if err := json.Unmarshal([]byte(v), &t); nil != err {
		return nil, fmt.Errorf("routing: 解析 trace 头失败: %w", err)
	}
	return &t, nil
}

// RedactScan 对 trace 文本做泄露自检（测试用，也是装配层可复用的守门断言）：
// 返回命中的敏感片段列表（空 = 干净）。
//
// 检查两类红线（§6.3）：凭据样 token（sk-…/Bearer）与 URL 形态
// （base_url）。注意它检的是「形状」而非「值」——trace 结构体本身不携带
// 这些字段（编译期保证），这一层是防未来加字段时无意识带入。
func RedactScan(traceJSON string) []string {
	var hits []string
	for _, pat := range []string{"sk-", "Bearer ", "http://", "https://", "sidecar://", "api_key", "credential"} {
		if strings.Contains(traceJSON, pat) {
			hits = append(hits, pat)
		}
	}
	return hits
}
