package upstream

import (
	"fmt"
	"strings"
)

// 本文件是 task-16 第 1 点：模型鉴定四态判定（docs/models/05-verification.md §5.1）。
//
// 目标收窄（§1）：不是「这是哪个模型」（开集识别，不可行），而是「这个目录条目
// 是否与它声称的名字相符」。我们防的是**冒充**（拿便宜模型套贵模型的名字），
// 不是识别未知模型。
//
// 关键语义（§5.2，任务红线）：**无参考指纹默认放行并标 unverified**——
// 「库里没有」不等于「可疑」：参考库覆盖的是热门模型，冷门模型天然不在库里；
// 若「不在库」即拒绝，等于宣布只接受热门模型，而众包贡献的价值恰在长尾。
// 误杀成本高于误放：suspect 是拒绝入池，正常贡献者被打回且理由不可理解。
//
// M2 落地范围说明：05-verification.md 的完整流水线是 L0/L1/L2 三级（探针、
// 数值指纹、金丝雀……）。本文件先落**目录刷新侧的元数据指纹比对**——
// real_model 行的 context_window/模态/思考档位 vs 参考指纹表；L1 的运行时
// 探针（需要真实调用上游）归贡献流程（M3/M4）。两级共享判定语义与四态。

// VerifyState 是鉴定的四态（05-verification.md §5.1 判定表）。
type VerifyState string

const (
	// VerifyVerified：库中有参考指纹且达标 → 入池。
	VerifyVerified VerifyState = "verified"
	// VerifySuspect：有指纹但不达标 → 拒绝入池并给证据。
	// M2 目录侧语义：仍可用（目录标出），候选排序由策略引擎降权。
	VerifySuspect VerifyState = "suspect"
	// VerifyUnverified：库中无指纹（冷门模型）→ 默认放行入池，标「未鉴定」，
	// 进复检队列（§5.2：不能靠过滤实现降权，否则复检永远没有输入）。
	VerifyUnverified VerifyState = "unverified"
	// VerifyTrusted：官方域名 → 免检（05-verification.md §5.1 第四行）。
	// M2 目录侧：relay 类上游（providers-hub 官方渠道）免检——凭据在
	// sidecar 账号池，来源是平台自有（owner_account_id=NULL 的 §6.1 硬约束面）。
	VerifyTrusted VerifyState = "trusted"
)

// Available 报告该态是否仍参与候选。
//
// 为什么 suspect 在目录侧返回 true：05-verification.md §7.3 的「不达标→移出候选」
// 作用对象是**贡献凭据**（upstream_credentials.verify_status）；本表作用对象是
// real_models 行（目录条目）。目录层的 suspect 是「标出但可用」——移出候选
// 是 L1 判定+人工复核后的动作，不是元数据比对就能定的。
func (s VerifyState) Available() bool {
	return s != "" // 四态全部可用；空串是未初始化，按 unverified 处理但不该出现
}

// NeedsRecheck 报告是否进复检队列（§5.1：unverified 进复检队列）。
func (s VerifyState) NeedsRecheck() bool {
	return s == VerifyUnverified
}

// ModelFingerprint 是一条参考指纹（指纹库的一个条目）。
//
// 为什么字段全部是指针/带 Min 前缀：指纹表达的是**下界/包含关系**而不是精确值
// ——上游目录对同族模型可能报不同别名与微调后的窗口（如 128k 家族报 131072
// 或 200000）。精确等值比对会把正常目录条目判成 suspect，违反「误杀成本高」。
type ModelFingerprint struct {
	// Pattern 是模型 id 的匹配模式（小写包含匹配，见 matchModel）。
	Pattern string
	// Family 是模型族名（证据文案里用，如 "claude-4.5 家族"）。
	Family string
	// MinContextWindow：声称的上下文窗口不得低于此值。
	// nil = 该族不检查窗口（如变体太多的入门族）。
	MinContextWindow *int64
	// RequiresModalities：目录声称的输入模态必须**包含**这些（如视觉模型必须含 image）。
	// nil = 不检查模态。
	RequiresModalities []string
	// RequiresEffort：目录声称的思考档位必须包含该档位（强推理族的标志）。
	RequiresEffort string
}

// fingerprintTable 是内置参考指纹表（任务第 4 点：表驱动，先放常见族示范）。
//
// 为什么只有 5 条：M2 是**机制落地**，指纹库的完整建设（lmfpd 导出 / zing YAML /
// 自建采样，§6 的三来源）是后续增量——每条指纹的数值都需要用自己池子标定
// （§3.1 待定项：权重与阈值不能照抄上游默认值）。现在放的是**公开无争议的
// 结构性下界**（族内最低窗口/必带能力），宁可少而稳。
//
// 补充在 M2 后续增量（注释锚点）：每加一族改这张表即可，判定函数不动。
var fingerprintTable = []ModelFingerprint{
	{
		Pattern:            "claude-4.5",
		Family:             "Claude 4.5 家族",
		MinContextWindow:   ptrInt64(200_000), // 4.5 全系 200k 起
		RequiresModalities: []string{"image"}, // 4.5 全系视觉
	},
	{
		Pattern:          "claude-",
		Family:           "Claude 家族",
		MinContextWindow: ptrInt64(200_000), // 3.7+ 全系 200k 起（3.5 是 200k）
	},
	{
		Pattern:          "gpt-5",
		Family:           "GPT-5 家族",
		MinContextWindow: ptrInt64(400_000), // 5 系 400k 起
	},
	{
		Pattern:          "gpt-4o",
		Family:           "GPT-4o 家族",
		MinContextWindow: ptrInt64(128_000), // 4o/4o-mini 128k
	},
	{
		Pattern:        "o3",
		Family:         "o3 推理族",
		RequiresEffort: "high", // o3 档位推理是结构标志
	},
}

// VerifyInput 是判定函数的输入：real_model 行的同构视图。
//
// 为什么不直接收 store.RealModel：纯函数的输入应该是自描述的——
// store 行的 Modalities/Efforts 是 JSON 原文（*string），这里收已解析的
// []string；调用方（refresh 联动）负责解析。判定函数不依赖 store 包，
// 测试就不用拉起存储。
type VerifyInput struct {
	// UpstreamModelID 是目录声称的模型 id（匹配指纹表的 Pattern）。
	UpstreamModelID string
	// ContextWindow 是目录声称的窗口；nil = 目录没说。
	ContextWindow *int64
	// Modalities 是目录声称的输入模态；nil/空 = 目录没说。
	Modalities []string
	// Efforts 是目录声称的思考档位；nil/空 = 目录没说。
	Efforts []string
	// Kind 是上游类型：relay（官方渠道）→ trusted 免检（§5.1 第四行的
	// 目录侧对应：providers-hub 的 adapter 目录来自官方 API，不是声称）。
	Kind string
}

// VerifyVerdict 是判定的输出：state + 证据（§5.3：每种判定都要落证据，
// 且证据要能让贡献者自己行动而不泄露探针细节）。
type VerifyVerdict struct {
	State VerifyState
	// Evidence 是证据文案（落 verify_detail 同构字段）。
	Evidence string
}

// JudgeRealModel 是四态判定函数（纯函数，任务第 2 点）。
//
// 输入 real_model 行的同构视图 + 参考指纹表；输出 state + evidence。
// 确定性：同一输入永远同一输出（无时间/随机/IO）——这是「判定可追溯」
// 的基础（§6.2：事后要能说清当时为什么判它 verified）。
//
// 判定顺序（与 05-verification.md §5.1 表逐行对应）：
//  1. relay（官方渠道）→ trusted 免检——目录来源不是「声称」而是官方 API；
//  2. 无参考指纹 → unverified 默认放行（§5.2 红线：不认识 ≠ 可疑）；
//  3. 有指纹 → 比对全部维度，全过 verified；任一不符 → suspect + 证据。
func JudgeRealModel(in VerifyInput, table []ModelFingerprint) VerifyVerdict {
	// trusted：官方渠道免检（§5.1 第四行）。证据给「命中的渠道形态」。
	if in.Kind == string(KindRelay) {
		return VerifyVerdict{
			State:    VerifyTrusted,
			Evidence: "relay 官方渠道目录（providers-hub adapter 拉取，非声称值），免检",
		}
	}
	fp, hit := matchFingerprint(in.UpstreamModelID, table)
	if !hit {
		// §5.2：冷门模型默认放行。证据按 §5.3 第二行模板。
		return VerifyVerdict{
			State:    VerifyUnverified,
			Evidence: fmt.Sprintf("库中无 %q 的参考指纹，已放行并进入复检队列（指纹库 v%d）", in.UpstreamModelID, FingerprintVersion),
		}
	}
	// 有指纹：逐维度比对。证据累积所有不符项（§5.3：哪条信号失败、失败方向）。
	var mismatches []string
	if fp.MinContextWindow != nil {
		if in.ContextWindow == nil {
			// 目录没说窗口：不算不符（「没说」≠「说错了」），但也不算达标——
			// 记为提示项。为什么容忍：目录字段缺失是常态（hub 的 listModels
			// 实测不含 contextWindow），拒绝它等于拒绝整个目录。
			mismatches = append(mismatches, "目录未申报 context_window（参考下界存在，无法比对）")
		} else if *in.ContextWindow < *fp.MinContextWindow {
			mismatches = append(mismatches, fmt.Sprintf("context_window=%d 低于 %s 参考下界 %d", *in.ContextWindow, fp.Family, *fp.MinContextWindow))
		}
	}
	if len(fp.RequiresModalities) > 0 && len(in.Modalities) > 0 {
		for _, want := range fp.RequiresModalities {
			if !containsFoldStr(in.Modalities, want) {
				mismatches = append(mismatches, fmt.Sprintf("模态缺少 %s（%s 家族必备）", want, fp.Family))
			}
		}
	}
	// 目录没报模态（空）同样容忍——与窗口同理由。
	if fp.RequiresEffort != "" && len(in.Efforts) > 0 {
		if !containsFoldStr(in.Efforts, fp.RequiresEffort) {
			mismatches = append(mismatches, fmt.Sprintf("思考档位缺少 %s（%s 的结构标志）", fp.RequiresEffort, fp.Family))
		}
	}
	if len(mismatches) > 0 {
		return VerifyVerdict{
			State:    VerifySuspect,
			Evidence: strings.Join(mismatches, "; ") + "（指纹库 v" + fmt.Sprint(FingerprintVersion) + "）",
		}
	}
	return VerifyVerdict{
		State:    VerifyVerified,
		Evidence: fmt.Sprintf("与 %s 参考指纹相符（窗口/模态/档位全部达标，指纹库 v%d）", fp.Family, FingerprintVersion),
	}
}

// matchFingerprint 找模型 id 命中的第一条指纹（表序优先）。
//
// 为什么是包含匹配而不是正则：指纹表的 Pattern 是人手维护的族名片段
// （"claude-4.5" 应命中 "claude-4.5-sonnet"），正则会把「简单的事」变成
// 「容易写错的事」；小写包含覆盖了别名大小写差异。
func matchFingerprint(modelID string, table []ModelFingerprint) (ModelFingerprint, bool) {
	lower := strings.ToLower(modelID)
	for _, fp := range table {
		if fp.Pattern != "" && strings.Contains(lower, strings.ToLower(fp.Pattern)) {
			return fp, true
		}
	}
	return ModelFingerprint{}, false
}

// FingerprintVersion 是内置指纹表的版本号（§6.2：判定结果依赖指纹库内容，
// verify_detail 必须记下判定时用哪一版；指纹库更新后 unverified 复检队列
// 按新库重新排队，verified 不回溯）。
//
// 语义：表内容变更时递增（major 不兼容变更 minor 增补）；起始 1。
const FingerprintVersion = 1

// containsFoldStr 大小写不敏感的成员判定（模态/档位目录值与指纹值对齐）。
func containsFoldStr(items []string, want string) bool {
	for _, it := range items {
		if strings.EqualFold(strings.TrimSpace(it), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func ptrInt64(v int64) *int64 { return &v }
