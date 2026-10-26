package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// 本文件是 task-16 第 2/3 点：VerificationWriter / InboxWriter 窄接口 +
// 刷新联动（写回 state/evidence）与 admin_inbox 三事件投递。
//
// 写域铁律：不改 internal/store——两个窄接口的定义在这里，实现归 Lead 装配
//（与 UpstreamWriter/AdminNotifier 同模式：功能包定义接口面，装配层钉实现）。
//
// 红线（任务原文）：鉴定失败不阻塞刷新；通知失败只记日志；拉取失败绝不标
// missing（refresh.go 已有，本联动不得破坏——联动挂在 MarkMissing **之前**
// 的 upsert 路径上，不碰 missing 语义）。

// VerificationWriter 是鉴定结果写回的窄接口（任务第 2 点）。
//
// 作用对象是 real_models 行的 verify_status/verify_detail 同构字段
// （store 侧列归 Lead 决定——若 real_models 无此列需 Lead 加迁移，接口
// 不预设存储形态）。为什么窄：装配层只需一个 UPDATE，接口宽了实现方
// 就要暴露不必要的仓储内部。
type VerificationWriter interface {
	// WriteVerification 写回一条 real_model 的鉴定结果。
	// realModelID 是 real_models.id；state/evidence 来自 JudgeRealModel。
	// fingerprintVersion 是判定所用指纹库版本（§6.2：判定可追溯）。
	WriteVerification(ctx context.Context, realModelID string, state VerifyState, evidence string, fingerprintVersion int) error
}

// InboxWriter 是 admin_inbox 投递的窄接口（任务第 3 点）。
//
// 三事件（kind/severity 取自 docs/security/06-audit.md §3.1 的**唯一权威词表**，
// b701374 裁决；03-upstream.md §3.3 现在只是同一词表的引用视图）：
//   - 新模型出现 model_added（info）
//   - 模型消失 model_missing（warning）
//   - 鉴定变 suspect verification_failed（warning——§3.1 用它覆盖「鉴定 suspect
//     或复检回归」；它标在目录里影响可用性判断）
//
// 去重规则归实现侧（store 的 InboxRepo.Append）：它按
// `ON CONFLICT(kind, related_id) WHERE read_at IS NULL AND related_id IS NOT NULL`
// 去重——两个要点对本接口的形状有约束：
//  1. **related_id 为空串时去重不生效**（部分索引不覆盖 NULL），实现侧会把它传成
//     nil，那每轮刷新都会新建一条——所以必须给行 id 的就给（三事件都有
//     real_models.id）；bootstrap 的自举失败没有行 id（provider 还未落库），
//     它重复投递会刷屏，已在 bootstrap.go 内做进程内节流；
//  2. 去重键是 (kind, related_id)，**title 不参与**（旧注释写的三元组与实现不符）。
type InboxWriter interface {
	// NotifyInbox 投递一条站内通知。kind 事件类型（值域见下方常量）；
	// relatedID 是关联行 id 的**字符串形态**（real_models.id；空串=无关联行），
	// 实现侧需转成 *int64（store.Append 收 *int64）；payloadJSON 是载荷（模型 id/
	// 上游/证据等，store 原样存不校验）；severity 取 info|warning|error
	//（06-audit.md §3.1，秩递增）；title 不得为空（store 拒空标题）。
	NotifyInbox(ctx context.Context, kind, relatedID, title, payloadJSON, severity string) error
}

// inbox 三事件的 kind 常量（值域取自 06-audit.md §3.1）。
//
// 为什么常量值直接**引用 store 的枚举**而不是重抄一份字符串：
// store.InboxRepo.Append 按枚举拒非枚举值（severity 是排序键，写错会静默排到
// 末尾——「看起来能用、实际不可用」那一类），而本包的「通知失败只记日志」
// 语义会把这种拒绝**吞成静默失败**：管理员收件箱永远收不到模型变动信号。
// 以前这里重抄了一份（当时 repo_inbox.go 还在 feat/storage 上，引用不到），
// 两套并列就是下一个漂移源；task-19 合入 main 后改成编译期引用，
// 词表变了本包自动跟（单一来源）。权威文档仍是 06-audit.md §3.1，
// store 那份是它的代码化执法点。
//
// 为什么接口参数仍用 string（不用 store.InboxKind）：本接口是给装配层
// 的投递面，窄接口的形状不该绑死实现侧的类型；string→InboxKind 的转换
// 留给装配桥一行完成（那边本来就要做 relatedID string→*int64）。
const (
	InboxKindModelAdded   = string(store.InboxModelAdded)         // 目录新增模型（info）
	InboxKindModelMissing = string(store.InboxModelMissing)       // 目录消失模型（warning）
	InboxKindModelSuspect = string(store.InboxVerificationFailed) // 鉴定 suspect / 复检回归（warning）
	// InboxKindChannelFailing：一个渠道/上游连续失败（relay 整类不可用、
	// selfhosted 端点连不上）。bootstrap.go 的自举失败也走它——severity 取 error。
	InboxKindChannelFailing = string(store.InboxChannelFailing)
)

// inbox 的 severity 常量（同样引用 store 枚举；06-audit.md §3.1：固定三值，
// 列表按 severity DESC, created_at DESC, id DESC 排）。
const (
	InboxSeverityInfo    = string(store.SeverityInfo)
	InboxSeverityWarning = string(store.SeverityWarning)
	InboxSeverityError   = string(store.SeverityError)
)

// RefreshHooks 刷新联动的可选依赖集。
//
// 为什么是 struct 而不是三个散参数：ModelRefresher 的构造签名已经很长，
// hooks 作为一个「可选增强包」整体传入（nil 字段=该联动降级跳过），
// NewModelRefresher 兼容旧调用（详见 NewWithHooks）。
type RefreshHooks struct {
	// Verification 写回鉴定结果。nil = 跳过鉴定（纯刷新，M1 行为）。
	Verification VerificationWriter
	// Inbox 投递通知。nil = 跳过通知。
	Inbox InboxWriter
	// Table 是参考指纹表。nil = 用内置 fingerprintTable。
	Table []ModelFingerprint
	// Log 是联动自身的 logger。nil = slog.Default()。
	Log *slog.Logger
}

// verifyForModel 把一条目录条目转成判定输入（JSON 原文 → []string 解析）。
//
// 为什么这里解析而不在判定函数里：判定函数收自描述输入（纯函数不碰 JSON），
// 解析是「目录行 → 输入」的翻译，属于联动层。解析失败按「目录没说」处理
// （nil）——坏 JSON 不该让整轮刷新的鉴定挂掉（红线：鉴定失败不阻塞刷新）。
func verifyForModel(m ModelInfoInput, kind string) VerifyInput {
	in := VerifyInput{
		UpstreamModelID: m.UpstreamModelID,
		ContextWindow:   m.ContextWindow,
		Kind:            kind,
	}
	if len(m.ModalitiesJSON) > 0 {
		var mods []string
		if json.Unmarshal([]byte(m.ModalitiesJSON), &mods) == nil {
			in.Modalities = mods
		}
	}
	if len(m.EffortsJSON) > 0 {
		var effs []string
		if json.Unmarshal([]byte(m.EffortsJSON), &effs) == nil {
			in.Efforts = effs
		}
	}
	return in
}

// ModelInfoInput 是联动层看到的目录条目（store 行或 dshprovider.ModelInfo
// 的同构视图——JSON 原文形态与 real_models 列一致）。
type ModelInfoInput struct {
	UpstreamModelID string
	ContextWindow   *int64
	// ModalitiesJSON / EffortsJSON 是 JSON 数组原文（real_models 列口径）。
	ModalitiesJSON string
	EffortsJSON    string
}

// applyVerification 对一批目录条目跑鉴定并写回。
//
// 返回（写回条数，首错）。错误**不中断**调用方（红线）——调用方记日志即可；
// 这里返回是为了日志与测试断言，不是让刷新挂掉。
// 每个 realModelID 独立写回：一条失败不影响其余（与 upsert 同哲学）。
func (h *RefreshHooks) applyVerification(ctx context.Context, items []verifyItem) (int, error) {
	if h == nil || h.Verification == nil {
		return 0, nil // 未接线：纯刷新（M1 行为），不是错误
	}
	table := h.Table
	if table == nil {
		table = fingerprintTable
	}
	log := h.Log
	if log == nil {
		log = slog.Default()
	}
	written := 0
	var firstErr error
	for _, it := range items {
		verdict := JudgeRealModel(verifyForModel(it.info, it.kind), table)
		if err := h.Verification.WriteVerification(ctx, it.realModelID, verdict.State, verdict.Evidence, FingerprintVersion); err != nil {
			// 鉴定失败不阻塞刷新（红线）：记日志继续下一条。
			log.Warn("upstream: 鉴定结果写回失败（继续其余条目）",
				"real_model_id", it.realModelID, "model", it.info.UpstreamModelID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written++
		// suspect 是目录侧的可疑标记 → inbox 事件（§3.3：检测到就立刻写）。
		if verdict.State == VerifySuspect && h.Inbox != nil {
			h.notifySuspect(ctx, log, it, verdict)
		}
	}
	return written, firstErr
}

// verifyItem 是鉴定单元：目录条目 + 落库行 id + 上游类型。
type verifyItem struct {
	info        ModelInfoInput
	realModelID string // UUID
	kind        string
}

// notifySuspect 投递「鉴定变 suspect」通知（失败只记日志，§3.3 末段）。
func (h *RefreshHooks) notifySuspect(ctx context.Context, log *slog.Logger, it verifyItem, verdict VerifyVerdict) {
	payload, _ := json.Marshal(map[string]any{
		"model":               it.info.UpstreamModelID,
		"kind":                it.kind,
		"state":               string(verdict.State),
		"evidence":            verdict.Evidence,
		"fingerprint_version": FingerprintVersion,
	})
	if err := h.Inbox.NotifyInbox(ctx, InboxKindModelSuspect, fmt.Sprint(it.realModelID),
		"模型鉴定标记 suspect：", string(payload), InboxSeverityWarning); err != nil {
		log.Warn("upstream: suspect 通知投递失败（旁路）", "model", it.info.UpstreamModelID, "error", err)
	}
}

// notifyAdded / notifyMissing 投递目录事件（刷新联动的另两事件）。
// 与 suspect 同纪律：失败只记日志。
func (h *RefreshHooks) notifyAdded(ctx context.Context, log *slog.Logger, upstream string, realModelID string, model string) {
	if h == nil || h.Inbox == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"upstream": upstream,
		"model":    model,
		"at":       time.Now().Unix(),
	})
	if err := h.Inbox.NotifyInbox(ctx, InboxKindModelAdded, fmt.Sprint(realModelID),
		"目录新增模型：", string(payload), "info"); err != nil {
		log.Warn("upstream: 新模型通知投递失败（旁路）", "model", model, "error", err)
	}
}

func (h *RefreshHooks) notifyMissing(ctx context.Context, log *slog.Logger, upstream string, realModelID string, model string) {
	if h == nil || h.Inbox == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"upstream": upstream,
		"model":    model,
		"at":       time.Now().Unix(),
	})
	if err := h.Inbox.NotifyInbox(ctx, InboxKindModelMissing, fmt.Sprint(realModelID),
		"目录消失模型：", string(payload), InboxSeverityWarning); err != nil {
		log.Warn("upstream: 模型消失通知投递失败（旁路）", "model", model, "error", err)
	}
}
