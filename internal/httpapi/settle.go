package httpapi

import (
	"context"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// settle.go：v1 请求终态结算的接线点（M1「终态结算整链」的 httpapi 侧）。
//
// 为什么结算必须在 proxyV1 入口的 defer 里、而不是在候选尝试内：
// 一次请求可能换多个候选（C7 只在未吐可见内容时允许换路），attempt 内结算
// 会让换一次候选就写一行 requests、扣一次额度——账翻倍，且与「一次请求
// 一次计费」的产品口径直接冲突。入口 defer 保证「一次请求 = 恰一次结算」，
// 且正常结束、全部候选失败、客户端断开（ctx 取消）都走同一条路径。
//
// 为什么结算用 context.WithoutCancel：客户端断开时请求 ctx 已取消，用它
// 结算会在写库第一步就失败——而「客户端走了但上游已产生 token」正是最需要
// 记账的情形（06-runtime §7.3 要求 drain/断开都要走到终态）。脱离取消、
// 只保留 5 秒超时，是「账不能因为客户端离开而丢」的落地；超时丢弃的代价
// （丢一行对账记录 + 日志告警）小于把取消语义带进写路径造成的静默漏记。
//
// 为什么结算失败只记日志、不影响响应：计量是观测，不是请求的前置条件
// （01-overview.md 一次请求第 13 步）。把「写不进库」变成用户可见故障，
// 等于让数据库抖动直接打挂转发面。

// SettleInput 是一次 v1 请求的终态结算输入（httpapi → 装配层 bridge）。
//
// 字段全部是响应写完之后仍可用的只读快照：settle 发生在 handler 返回前
// 的 defer 里，不能依赖 handler 的局部变量生命周期。
type SettleInput struct {
	RequestID string
	// Endpoint 是 requests.endpoint 的受控词表值（0001_init.sql CHECK：
	// openai-chat / openai-responses / anthropic-messages / gemini-generate）。
	Endpoint string
	// AccountID / KeyID 是对外字符串 id（acc_42 / key_7）。
	// 空串 = 不可归属（未过 Key 鉴权的路径不会走到 v1 面板，这里防御性保留）。
	AccountID string
	KeyID     string
	// ModelName 是请求里的**抽象模型名**（出站恒抽象名，§4.1）。
	// 翻译成 store 的模型 id 是装配层的事——本包不查模型表（配额中间件同理）。
	ModelName string
	// RealModelID 是成功候选的 real_models 行 id；0 = 来源不提供（测试 fake）。
	RealModelID string
	// Status 是请求终态："ok" / "error"。
	Status string
	// HTTPStatus 是对外状态码（200 成功；404/502/503 等失败）。
	HTTPStatus int
	// ErrorCode 是失败码（03-errors.md §3.1 的枚举）；成功时为空。
	ErrorCode string
	// RetryCount 是换候选次数（0 = 首个候选即成功）。
	RetryCount int
	// TTFTMs 是首段可见内容的耗时毫秒；-1 = 无样本（非流式，或流直到
	// 结束都没吐可见内容）。与 routing 的 -1 语义一致：缺样本不编造。
	TTFTMs int64
	// TotalMs 是请求从进入代理编排到结算的总耗时毫秒。
	TotalMs int64
	// Usage 是上游终态 usage；nil = 上游没报（结算按 0 记，宁少算——
	// 05-storage.md 3.7 的「没有可靠数字时不编造」）。
	Usage *ir.Usage
	// EndedAt 是结算时刻（usage_daily 的 day 归属；UTC）。
	EndedAt time.Time
}

// Settler 是终态结算的唯一出口。
//
// 实现归装配层（internal/app）：它同时要碰 meter 的额度面与 store 的请求面，
// 而 httpapi 不该认识其中任何一个。返回 error 只用于日志——调用方不因
// 结算失败改变已写出的响应。
type Settler interface {
	Settle(ctx context.Context, in SettleInput) error
}

// SetSettler 注入结算出口（装配层调用；未注入时 v1 请求不结算，仅日志）。
func (s *Server) SetSettler(v Settler) { s.settler = v }

// LatencySink 是每请求延迟样本的接收口（路由排序键 speed 的数据源）。
//
// 装配层注入实现（internal/app 里通常是共用同一个 upstream.LatencySampler：
// settle 侧写、routing 侧读）。httpapi 只依赖这个窄接口，不设实现——与
// Settler 同一种「装配层填、httpapi 只调」的注入面。
//
// nil = 未装配：延迟样本丢弃（速度排序退回无样本垫底）。
type LatencySink interface {
	// RecordLatency 记录一次成功调用的延迟（路由按 realModelID 聚合）。
	// ttftMs/totalMs 用 -1 表示无样本。
	RecordLatency(ctx context.Context, realModelID string, ttftMs, totalMs int64)
}

// SetLatencySink 注入每请求延迟采样出口。
func (s *Server) SetLatencySink(v LatencySink) { s.latencySink = v }

// settleTimeout 是终态结算的独立超时。
//
// 为什么是 5s 而不是更长：结算是一次本地事务（requests + usage_daily +
// 额度条件 UPDATE），正常路径毫秒级；5s 只用来兜「库被锁/磁盘抖动」的
// 极端情形，避免 defer 把请求 goroutine 拖住。
const settleTimeout = 5 * time.Second

// settleState 是 proxyV1 一次请求的终态累积器（defer 读取）。
//
// 为什么用可变结构而不是返回值：终态在多个分支（404 / 503 / 候选耗尽 /
// 成功）分别确定，返回值要穿透 runCandidates 的回调层，改动面远大于
// 一个只写不读的局部结构。并发安全：只会被 proxyV1 所在的单个 goroutine
// 读写（候选循环是顺序的），不加锁是对的。
type settleState struct {
	obs        attemptObs
	status     string
	httpStatus int
	errorCode  string
	attempts   int
}

// attemptObs 是单次候选尝试的可观测产物（结算与 TTFT 日志共用）。
//
// 为什么用指针回传给调用方：attempt 回调的 bool 语义是「是否已开流」，
// 再塞三个返回值会让每个分支都要拆包；obs 只写不读、nil 安全，调用点
// 保持一行。
type attemptObs struct {
	// Usage 是本次尝试收到的终态 usage（I4：至多一次）。
	// 流式在事件循环里留最后一份被 Accept 的；非流式取 DecodeResponse 的。
	Usage *ir.Usage
	// TTFTMs 是首段可见内容耗时（-1 = 无样本）。
	TTFTMs int64
	// TotalMs 是本次尝试的总耗时毫秒。
	TotalMs int64
	// RealModelID 是本次尝试命中的 real_models 行 UUID（空串 = 未知）。
	RealModelID string
}

// settleRequest 在 proxyV1 的 defer 里执行一次终态结算。
//
// 语义要点（都在这里集中，避免调用点各自解释）：
//   - QuotaRequest 由 Status 推导：成功 1、失败 0（失败尝试不扣 request 维度，
//     但 token 维度仍按 Usage 实记——上游可能已经算过 token）。
//   - 无 Settler（未装配/测试）时什么都不做，不写日志噪音。
//   - 结算用脱离取消的 ctx + settleTimeout，见文件头注释。
func (s *Server) settleRequest(ctx context.Context, pr proxyReq, requestID string, st *settleState) {
	if s == nil || st == nil {
		return
	}
	// 每请求延迟进采样器（路由排序键 speed 的数据源）。
	// 放在 Settler 判定之前：即使结算未装配，延迟采样仍要喂——排序是独立能力。
	// 只在 RealModelID 已知时记（未知行无法归属到速排序，丢弃）。
	if s.latencySink != nil && st.obs.RealModelID != "" {
		s.latencySink.RecordLatency(ctx, st.obs.RealModelID, st.obs.TTFTMs, st.obs.TotalMs)
	}
	if s.settler == nil {
		return
	}

	var accountID, keyID string
	if p := KeyPrincipalFromContext(ctx); p != nil {
		accountID, keyID = p.AccountID, p.KeyID
	}
	retries := st.attempts - 1
	if retries < 0 {
		retries = 0
	}
	in := SettleInput{
		RequestID:   requestID,
		Endpoint:    pr.endpoint,
		AccountID:   accountID,
		KeyID:       keyID,
		ModelName:   pr.req.Model,
		RealModelID: st.obs.RealModelID,
		Status:      st.status,
		HTTPStatus:  st.httpStatus,
		ErrorCode:   st.errorCode,
		RetryCount:  retries,
		TTFTMs:      st.obs.TTFTMs,
		TotalMs:     st.obs.TotalMs,
		Usage:       st.obs.Usage,
		EndedAt:     time.Now().UTC(),
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	if err := s.settler.Settle(sctx, in); err != nil {
		// 告警必须自带对账键：只写“结算失败”+操作说明，值班拿不到 account/ts/model，
		// 还得反查 request_id → account_id（而全仓没有这种关联日志）。
		s.log.Warn("v1 settle failed",
			"request_id", requestID, "endpoint", in.Endpoint,
			"account_id", in.AccountID, "model", in.ModelName,
			"ts", in.EndedAt.Unix(), "status", in.Status, "error", err)
	}
}
