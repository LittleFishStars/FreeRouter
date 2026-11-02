// candidates.go —— 候选集：声明展开 × 请求能力过滤（02-abstract.md §3、§5；
// 03-strategy.md §2 F1/F2）。
//
// 本文件回答「这次请求面前，哪些候选是可用路径」；排位与切换是
// order.go 的事——三段式各段职责不混合（03-strategy.md §1 归因要求）。
package routing

import (
	"strings"
)

// RequestProfile 是本次请求的能力画像——过滤的输入（03-strategy.md §2.1
// F1「能力匹配」/F2「上下文够长」的判据，从 IR 请求提取的字段子集）。
//
// 为什么是独立结构而不是直接收 ir.Request：routing 不 import httpapi/ir
// 之外的重型类型（ir.Request 字段还在演化），画像字段由装配层从 IR 提取。
// 窄输入让「过滤逻辑」本身可以被纯表驱动测试覆盖。
type RequestProfile struct {
	// NeedModalities 本次请求实际需要的模态（含图片 → ["text","image"]）。
	NeedModalities []string
	// EstInputTokens 估算输入 token（F2：输入 + 声明最大输出 ≤ 候选上下文）。
	EstInputTokens int64
	// MaxOutputTokens 请求声明的最大输出（0 = 未声明）。
	MaxOutputTokens int64
	// NeedTools 请求是否带 tool 定义或 tool_call 历史。
	NeedTools bool
	// NeedEffort 请求的思考档位（"" = 未指定）。
	NeedEffort string
}

// Capabilities 是单个候选的运行时能力面（01-catalog.md §2.3 探测结果 +
// real_models 行字段的归一）。
type Capabilities struct {
	// ContextWindow nil = 未知（01-catalog.md §2.3 已知空缺——上游目录不提供）。
	ContextWindow *int64
	// Modalities 已知模态列表；nil = 未知。
	Modalities []string
	// Efforts 支持的思考档位；nil = 未知（未声明档位的模型按支持 off/low 处理
	// 的口径归装配层，这里只做保守判定）。
	Efforts []string
	// SupportsTools 是否支持工具调用。
	SupportsTools bool
	// SupportsImages 是否接受图片输入。
	SupportsImages bool
}

// Path 是一条可路由路径（02-abstract.md §3.2：候选 × 渠道/凭据展开后的
// 具体路径）。凭据维度归装配层展开——本包只见「候选级」路径，排序键
// （到期/余额/延迟/价格）由装配层附带，本包不查任何外部系统。
type Path struct {
	// RealModel 展开后的真实模型读面。
	RealModel RealModelInfo
	// Constraint 该候选的声明约束（渠道范围等）。
	Constraint Constraint
	// Caps 该路径的能力面（探针/目录归一结果）。
	Caps Capabilities

	// —— 排位标记（2026-11-01 定案：不再有排序键，只有「在不在册」） ——
	//
	// 原此处的五个排序键（ExpiresAt / Balance / BalanceKnown / TTFTMs /
	// TotalMs / PriceMultiplier）随四种自动排序策略一起退役：候选次序就是
	// 管理员手动排的声明序，没有任何可测量量参与比较。

	// Unavailable 该路径当前**不可用**（上游侧失败判定在册——装配层从
	// model_unavailability 读，见 internal/store/repo_unavailability.go）。
	// 语义是「跳过但保留兜底」：排到手动序末尾，前面还有在册路径时不用它；
	// 全池都不在册时才轮到它——一次误判不该让整个池子 503。
	Unavailable bool

	// CooledDown 冷却中（cooldown.go 写入——排末尾不剔除）。
	CooledDown bool
}

// FilterOut 记录一条路径被过滤的原因（03-strategy.md §8「Filter 返回被剔除
// 的路径及原因」——2.3 节的「险些可用」诊断与第 7 节切换率统计都从它派生）。
type FilterOut struct {
	// PathID 被剔除路径的 RealModel.ID。
	PathID string // UUID（指向真实模型行）
	// Reason 稳定原因码：modality | context | tools | effort | constraint。
	Reason string
	// Detail 人读诊断（进 trace，不外发）。
	Detail string
}

// FilterPaths 对路径集执行第①段过滤（F1 能力匹配 + F2 上下文 + 声明约束）。
//
// 硬门槛不降权（03-strategy.md §2.2）：不通过 = 本次请求里不存在。
// 健康与冷却不在这里判——健康降权是排序的事（degraded 不剔除），
// 冷却按 lead 指令「排末尾而非剔除」（cooldown.go），都不是过滤条件。
//
// 能力判定口径（02-abstract.md §4.3）：未知按不支持处理（严格模式语义；
// 过滤模式下未知能力的候选同样被剔——「我们唯一能承诺的是确切知道的」）。
func FilterPaths(profile RequestProfile, paths []Path) ([]Path, []FilterOut) {
	var kept []Path
	var outs []FilterOut
	for _, p := range paths {
		// 声明约束：渠道范围（include/exclude）。
		if !channelAllowed(p.Constraint, p.RealModel.Channel) {
			outs = append(outs, FilterOut{PathID: p.RealModel.ID, Reason: "constraint",
				Detail: "渠道不在候选声明范围"})
			continue
		}
		// F1 能力匹配：模态逐项（需要的每一项候选都必须支持；未知=不支持）。
		for _, need := range profile.NeedModalities {
			if !modalitySupported(p.Caps, need) {
				outs = append(outs, FilterOut{PathID: p.RealModel.ID, Reason: "modality",
					Detail: "候选不支持模态 " + need})
				goto next
			}
		}
		// F1 工具调用。
		if profile.NeedTools && !p.Caps.SupportsTools {
			outs = append(outs, FilterOut{PathID: p.RealModel.ID, Reason: "tools",
				Detail: "请求带工具但候选不支持工具调用"})
			continue
		}
		// F1 思考档位（声明了档位要求时才判；未指定档位不构成过滤条件）。
		if profile.NeedEffort != "" && !effortSupported(p.Caps, profile.NeedEffort) {
			outs = append(outs, FilterOut{PathID: p.RealModel.ID, Reason: "effort",
				Detail: "候选不支持思考档位 " + profile.NeedEffort})
			continue
		}
		// F2 上下文够长：估算输入 + 声明最大输出 ≤ 候选上下文。
		// 窗口已知时硬校验（请求超窗必被上游拒，提前失败省一次尝试窗口）；
		// 窗口**未知**时放行——2026-10-30 生产实测：hub 目录只给裸模型 id（无任何能力字段，
		// 见 internal/upstream/dshprovider/methods.go「ContextWindow 可空」），目录来源的
		// 候选 context_window 恒为 NULL，按「未知=不满足」剔除等于整个目录池永远 503。
		// 放行的最坏后果是上游报 context 超限（4xx，客户端可见可自愈）；
		// 剔除的最坏后果是 no_candidate 无自愈路径——两者取其轻。
		if p.Caps.ContextWindow != nil {
			need := profile.EstInputTokens + profile.MaxOutputTokens
			if need > *p.Caps.ContextWindow {
				outs = append(outs, FilterOut{PathID: p.RealModel.ID, Reason: "context",
					Detail: "请求估算超出候选上下文窗口"})
				continue
			}
		}
		kept = append(kept, p)
	next:
	}
	return kept, outs
}

// channelAllowed 判渠道范围约束（02-abstract.md §3.1 channel_scope）。
func channelAllowed(c Constraint, channel string) bool {
	switch c.ChannelMode {
	case "", "any":
		return true
	case "include":
		for _, ch := range c.Channels {
			if ch == channel {
				return true
			}
		}
		return false
	case "exclude":
		for _, ch := range c.Channels {
			if ch == channel {
				return false
			}
		}
		return true
	default:
		// 未知模式：放行（声明数据来自管理台写入，未知值按 any 处理——
		// 把配置笔误变成全剔除会让整池 503，代价不对称）。
		return true
	}
}

// modalitySupported 单模态判定（未知 = 不支持，02-abstract.md §4.3）。
func modalitySupported(caps Capabilities, need string) bool {
	for _, m := range caps.Modalities {
		if m == need {
			return true
		}
	}
	return false
}

// effortSupported 档位判定：**候选未声明档位集合时放行**。
//
// 口径修正（2026-11-02，与同文件 context_window 那条 2026-10-30 的实测教训同构）：
// 原实现按「未声明 = 只支持 off/low」剔除，把「不知道」当成了「不支持」。
// 而 efforts 的实际来源决定了这个假设不成立——relay 上游的目录经
// dsh-providers-hub 的 toModelInfo 只输出 id/name/inputModalities，
// **根本没有档位字段**（不是模型没档位，是我们拿不到）。于是：
//   - 剔除的最坏后果：整个池被剔空 → no_candidate 503，用户完全无法使用，
//     且客户端没有任何自愈路径（改参数也没用）；
//   - 放行的最坏后果：请求打到上游，不支持思考的模型忽略该字段或回 400
//     （invalid_request，错误现场准确、客户端可自愈）。
//
// 两者取其轻——与 SupportsTools 恒真的处理是同一条哲学：**能力未知不构成
// 剔除理由，因为剔除把「上游可能拒绝」升级成了「我们必定拒绝」**。
//
// 声明了档位集合时照旧严格比对（那是确凿事实，不是未知）。
func effortSupported(caps Capabilities, need string) bool {
	if len(caps.Efforts) == 0 {
		return true
	}
	for _, e := range caps.Efforts {
		// canonical 档位映射（off ↔ none 等）在目录刷新器归一，这里只比对。
		if e == need {
			return true
		}
	}
	return false
}

// HasVisibleModality 报告画像是否包含某模态（trace/测试用的小帮手）。
func HasVisibleModality(profile RequestProfile, modality string) bool {
	for _, m := range profile.NeedModalities {
		if m == modality {
			return true
		}
	}
	return false
}

// normalizeChannel 归一渠道名（trace 输出用；空渠道归 "unknown"）。
func normalizeChannel(ch string) string {
	if strings.TrimSpace(ch) == "" {
		return "unknown"
	}
	return ch
}
