// candidates.go —— 候选集：声明展开 × 请求能力过滤（02-abstract.md §3、§5；
// 03-strategy.md §2 F1/F2）。
//
// 本文件回答「这次请求面前，哪些候选是可用路径」；排序与切换是
// strategy.go 的事——三段式各段职责不混合（03-strategy.md §1 归因要求）。
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

	// —— 排序键（03-strategy.md §3.1 四种策略的数据面，全部可测量外部事实） ——

	// ExpiresAt 额度作废时间（expiry-first 升序键）；零值 = 无到期概念
	//（纯 API Key）→ 排最后（用不掉的浪费无从谈起，先花有期限的）。
	ExpiresAt int64
	// Balance 余额（balance 升序键）；负数 = 上游不报余额 → 视为 +∞ 排最后。
	Balance float64
	// BalanceKnown Balance 是否已知（false 时 Balance 字段无意义）。
	BalanceKnown bool
	// TTFTMs 最近 TTFT 毫秒（speed 升序键）；-1 = 无样本 → 排最后。
	TTFTMs int64
	// TotalMs 最近总耗时毫秒（speed 次级键，抓 TTFT 快但吐字慢的模型）。
	TotalMs int64
	// PriceMultiplier Credits 价格乘数（price 升序键）；表里没有默认 1
	//（03-strategy.md §2.5.2：默认 1 = 按基准价的「不被惩罚中间态」）。
	PriceMultiplier float64

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
		// F2 上下文够长：估算输入 + 声明最大输出 ≤ 候选上下文；未知上下文
		// 按不满足处理（保守下界——宁可 503 让客户端换小请求，不可中途截断）。
		if p.Caps.ContextWindow != nil {
			need := profile.EstInputTokens + profile.MaxOutputTokens
			if need > *p.Caps.ContextWindow {
				outs = append(outs, FilterOut{PathID: p.RealModel.ID, Reason: "context",
					Detail: "请求估算超出候选上下文窗口"})
				continue
			}
		} else {
			outs = append(outs, FilterOut{PathID: p.RealModel.ID, Reason: "context",
				Detail: "候选上下文窗口未知，按保守下界剔除"})
			continue
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

// effortSupported 档位判定：候选未声明档位集合时按「仅 off/low」保守处理
// （无思考能力的模型占池子多数，宁缺勿滥；真支持 high 的模型目录必然带
// efforts 字段——01-catalog.md §2.3 的映射保证）。
func effortSupported(caps Capabilities, need string) bool {
	if len(caps.Efforts) == 0 {
		return need == "off" || need == "low"
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
