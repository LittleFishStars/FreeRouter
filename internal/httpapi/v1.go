package httpapi

// v1.go —— M5 v1 出口面板的公共底座（task-8）。
//
// 为什么单独一个底座文件：三个协议端点（chat/messages/responses）共享同一套
// 编排骨架——解码入站→选候选→调上游→出站编码，只有「wire 方言」不同；
// 把骨架抽到这里，端点文件就只剩方言差异，切换逻辑只写一次（02-ir.md C7）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/ir/anthropic"
	"github.com/LittleFishStars/FreeRouter/internal/ir/openai"
	"github.com/LittleFishStars/FreeRouter/internal/routing"
)

// ---------------------------------------------------------------------------
// 上游与候选：接口抽象（04-sidecar.md L207——Go 主进程不直连任何 LLM 端点，
// 推理经 sidecar；这里只定义 httpapi 消费的面，真实实现（sidecar HTTP 调用 +
// real_models 查询）由后续任务接线，测试注入 fake）。
// ---------------------------------------------------------------------------

// Candidate 是一次可尝试的上游落点。M6 之前直接映射 real_models 行；
// M6 换抽象模型路由后由策略引擎产出，这个结构保持不变。
type Candidate struct {
	// UpstreamModelID 是发给上游的真实模型 id（如 "deepseek-chat"）。
	UpstreamModelID string
	// Display 是内部日志/trace 用的落点描述（不外发，防泄露池结构）。
	Display string
	// Adapter 是该候选使用的 IR 协议适配器（决定 EncodeRequest 目标方言）。
	Adapter ir.Adapter
	// ID 是 real_models 行 ID（M6 接线加）：上游失败时回传给
	// CandidateFailureNotifier（冷却账本按它记账）。0 = 来源不提供
	//（测试 fake/旧候选源），冷却跳过。
	ID int64
}

// CandidateSource 产出候选序列：按顺序尝试，耗尽即 no_candidate（503）。
// 返回空切片 = 该模型无可用候选。为什么是接口：M5 用 real_models 直连，
// M6 换抽象模型策略引擎——调用方（本文件编排骨架）不感知来源。
//
// M6 接线变更：加 req 参数——候选过滤/排序需要请求画像（能力需求/上下文
// 长度/思考档位），这些只有请求本身知道；实现方自己从 ir.Request 提取，
// 本包不预先猜字段（与 routing.RequestProfile 的提取口径归实现方）。
// 返回 ErrModelNotFound 时本面板改写 404（抽象模型不存在/真实名防泄露），
// 其余 error 只记日志并按无候选处理。
type CandidateSource interface {
	Candidates(ctx context.Context, model string, req ir.Request) ([]Candidate, error)
}

// ErrModelNotFound 是 CandidateSource 的 404 信号（M6 接线：抽象模型
// 解析失败——名字不存在/disabled/真实模型名防泄露，02-abstract.md §6.2）。
// 为什么哨兵而不是错误码字段：面板只需要「404 还是无候选 503」一比特，
// errors.Is 一条判定最短路径，不需要为它养一个结构化错误类型。
var ErrModelNotFound = errors.New("v1: model_not_found")

// CandidateFailureNotifier 是候选源的可选扩展：上游调用失败（未吐内容、
// 将切换下一候选）时回传该候选，供实现方记冷却（routing.Cooldowns）。
// 接口断言可选而不是并入 CandidateSource：M5 的 realModelCandidates
// 不需要它，强制实现会污染所有测试 fake。
type CandidateFailureNotifier interface {
	NotifyCandidateFailure(cand Candidate)
}

// traceSink 是 x-fr-trace 的诊断通道（M6 接线）：CandidateSource 实现
// 方从 ctx 取出并把 BuildTrace 产物塞进来（可 nil=调用方没要 trace）。
// 为什么经 ctx 而不是改 Candidates 签名：trace 是诊断功能不是契约，
// 加进签名会让所有 fake 与调用点都背一个几乎恒 nil 的返回值。
// 键类型未导出——外部只能经 WithV1Trace/TraceFromV1 使用。
type traceCtxKey struct{}

// traceSink 收集最近一次 Candidates 的路由诊断（并发安全由「每请求一个
// sink」保证——ctx 是请求作用域的）。
type traceSink struct{ trace *routing.Trace }

// WithV1Trace 在 ctx 上挂诊断 sink（x-fr-trace: 1 时 proxyV1 调用）。
func WithV1Trace(ctx context.Context) context.Context {
	return context.WithValue(ctx, traceCtxKey{}, &traceSink{})
}

// FillV1Trace 让候选源实现方（装配层）回填本次请求的路由诊断。
//
// 未挂 sink（没要 trace）时静默丢弃——trace 是诊断功能，不做契约。
func FillV1Trace(ctx context.Context, t *routing.Trace) {
	if s := traceSinkFrom(ctx); s != nil {
		s.trace = t
	}
}

// V1TraceFrom 取回填的路由诊断（未挂 sink 或未回填 → nil）。
//
// 与 FillV1Trace 对称：装配层/诊断代码用它读回「这次请求为什么这么路由」。
//
// **同步消费假设**：返回的是同一个 *routing.Trace 指针，生命周期绑定在请求
// ctx 上。必须在处理请求的 goroutine 内读出；不得跨 goroutine 持有（例如下
// 游标给异步 access log），否则与回填方竞争（code-review 巡检建议）。
func V1TraceFrom(ctx context.Context) *routing.Trace {
	if s := traceSinkFrom(ctx); s != nil {
		return s.trace
	}
	return nil
}

// traceSinkFrom 取诊断 sink（未挂返回 nil——实现方据此跳过收集）。
func traceSinkFrom(ctx context.Context) *traceSink {
	s, _ := ctx.Value(traceCtxKey{}).(*traceSink)
	return s
}

// UpstreamCall 是一次上游调用的结果。非流式：Body 为完整响应体；
// 流式：Body 为 SSE 字节流（调用方负责读取并转发事件）。
// Decode 走 Candidate.Adapter（同一方言编解码闭环，不猜格式）。
type UpstreamCall struct {
	Body io.ReadCloser
	// Err 携带上游失败（UpstreamProtocolError 或普通错误）。
	// 区分它的意义：协议违规（502 upstream_error）与连接失败（502
	// upstream_unavailable）在 03-errors.md 是两个码，重试语义不同。
	Err error
}

// Upstream 执行一次对候选的上游调用（04-sidecar.md：真实实现经 sidecar）。
//
// 注意：httpapi 不感知 routing 的画像类型（routing.RequestProfile）——
// 请求画像的裁剪归候选源（它同时看到入站请求与候选集），这样 routing 的
// 画像字段增删只改桥一处，不用同步改三个端点。
type Upstream interface {
	Call(ctx context.Context, cand Candidate, req ir.Request, stream bool) (*UpstreamCall, error)
}

// ---------------------------------------------------------------------------
// v1 面板错误体：唯一映射表裁决——type 字段直接出自 ir.CodeToErrorType，
// httpapi 不养第二张表（双表裁决 m00817–m00826；errors.go 本地表只服务
// 控制台面，v1 协议面走这里）。
// ---------------------------------------------------------------------------

// v1Error 是 03-errors.md §2.2 的 OpenAI 形状错误体。
type v1Error struct {
	Error v1ErrorBody `json:"error"`
	// RequestID 让客户端报障时能一次定位（01-inbound.md §7.2 双向对账）。
	RequestID string `json:"request_id,omitempty"`
}

type v1ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    string `json:"code"`
}

// v1AnthropicError 是 03-errors.md §2.2 的 Anthropic 形状错误体。
// code 是我们的扩展字段（Anthropic 原生无 code；§2.2：带上它两边才能
// 共用同一套排障语言；SDK 忽略未知字段，不破坏兼容）。
type v1AnthropicError struct {
	Type  string          `json:"type"` // 恒 "error"
	Error v1AnthropicBody `json:"error"`
}

type v1AnthropicBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// errStatus 按 03-errors.md §3.1 把细码映射为 HTTP 状态码。
// 为什么本地 switch 而不是再养一张 map：状态码只有十几个且与码表同源，
// switch 集中在一个函数里可读性高于再建一张双表要人肉同步的结构。
func errStatus(code string) int {
	switch code {
	case "invalid_api_key":
		return http.StatusUnauthorized
	case "invalid_request", "capability_unsupported":
		return http.StatusBadRequest
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "model_not_found":
		return http.StatusNotFound
	case "model_retired":
		return http.StatusGone
	case "quota_exceeded", "pool_exhausted", "rate_limited":
		return http.StatusTooManyRequests
	case "upstream_unavailable", "upstream_error":
		return http.StatusBadGateway
	case "upstream_timeout":
		return http.StatusGatewayTimeout
	case "no_candidate", "overloaded":
		return http.StatusServiceUnavailable
	default:
		// 未登记细码按「未预期故障」处理（与 ir.CodeToErrorType 兜底同理由：
		// 宁可 500 让客户端重试，不伪装成 400 误导改参数）。
		return http.StatusInternalServerError
	}
}

// writeV1Error 写 OpenAI 形状错误响应（chat / responses 端点用）。
// type 出自 ir.CodeToErrorType——唯一表，不另起第二份（task-8 §7）。
func (s *Server) writeV1Error(w http.ResponseWriter, code, message, requestID string) {
	status := errStatus(code)
	typ := ir.CodeToErrorType(code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v1Error{
		Error:     v1ErrorBody{Message: message, Type: typ, Param: nil, Code: code},
		RequestID: requestID,
	})
}

// writeV1AnthropicError 写 Anthropic 形状错误响应（/v1/messages 用）。
func (s *Server) writeV1AnthropicError(w http.ResponseWriter, code, message, requestID string) {
	status := errStatus(code)
	typ := ir.CodeToErrorType(code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v1AnthropicError{
		Type:  "error",
		Error: v1AnthropicBody{Type: typ, Message: message, Code: code},
	})
}

// ---------------------------------------------------------------------------
// 编排骨架：解码→选候选→逐候选尝试（失败切换）→出站编码。
// ---------------------------------------------------------------------------

// proxyReq 是一次入站请求的解码产物 + 出站方言信息。
type proxyReq struct {
	req ir.Request
	// inbound 是入站方言适配器（决定错误帧形状与出站编码）。
	inbound ir.Adapter
	// anthropicShape=true 时错误体用 Anthropic 形状（/v1/messages）。
	anthropicShape bool
	// maxTokensRequired=true 时缺失 max_tokens 报 400（Anthropic 契约 §6.1：
	// 必填且不补默认——补默认值是帮客户端掩盖成本预算的错误）。
	maxTokensRequired bool
	// traceRequested=true 时请求带了 X-FR-Trace: 1，proxyV1 挂诊断
	// sink 并把路由 trace 落到 X-FR-Trace 响应头（M6 接线）。
	traceRequested bool
	// endpoint 是端点标识（v1.chat / v1.messages），写进 requests.endpoint。
	// 为什么由 handler 传入而不从 URL 推断：三个 v1 端点的路径形状不同
	//（/v1/chat/completions、/v1/messages），且日后新增端点时不该改 proxyV1
	// 的解析逻辑——调用方最知道自己在服务哪个端点。
	endpoint string
}

// errPayload 是从上游/解码层提出来的结构化错误（code+message 已就位）。
type errPayload struct {
	code    string
	message string
}

// classifyErr 把任意 error 归一到 {code, message}。
// 优先级：IR 结构化错误（AsUnsupported/AsInvalidRequest/AsUpstreamProtocol）
// → 上游侧普通 error（upstream_unavailable——上游还没开口就失败，502 可重试）
// → 兜底 internal_error。
func classifyErr(err error) errPayload {
	var uns *ir.UnsupportedError
	if errors.As(err, &uns) {
		return errPayload{code: "capability_unsupported", message: uns.Detail}
	}
	var inv *ir.InvalidRequestError
	if errors.As(err, &inv) {
		return errPayload{code: "invalid_request", message: inv.Detail}
	}
	var upr *ir.UpstreamProtocolError
	if errors.As(err, &upr) {
		return errPayload{code: "upstream_error", message: upr.Detail}
	}
	return errPayload{code: "internal_error", message: err.Error()}
}

// proxyOnce 对单个候选执行一次完整代理（非流式路径）。
// 返回 nil 表示该候选失败（调用方换下一个）；错误已写入 w 的情形只发生在
// 所有候选耗尽后——单候选失败不向客户端吐任何字节（C7：未吐可见内容才可切）。
// 为什么带 obs：终态结算需要本次尝试的 usage / 总耗时 / 候选行 id。
// obs 可能为 nil（未来只关心成败的调用点）——写入前一律判空。
func (s *Server) proxyOnce(ctx context.Context, w http.ResponseWriter, pr proxyReq, cand Candidate, reqID string, obs *attemptObs) bool {
	start := time.Now()
	// 出站给客户端的编码永远用入站方言：客户端说的是 Chat 就回 Chat
	// （跨方言转换在 IR 层完成，HTTP 层不做二次翻译）。
	encOpt := ir.EncodeOptions{
		UpstreamModel: pr.req.Model, // §4.1：model 恒为请求里的抽象模型名
		RequestID:     reqID,
		CreatedAt:     time.Now().Unix(),
	}
	_ = encOpt // 出站 EncodeOptions（CreatedAt 兜底在 ir 层：非流式 0→time.Now）
	upOpt := ir.EncodeOptions{
		UpstreamModel: cand.UpstreamModelID, // 上行给候选的是真实模型 id（§4.1 只有出站方向恒抽象名）
		RequestID:     reqID,
	}
	body, warns, err := cand.Adapter.EncodeRequest(&pr.req, upOpt)
	if err != nil {
		// 能力检查在 EncodeRequest 内完成（C6：不静默降级）。
		// 为什么这里直接终止而不是换候选：能力缺失是「这条请求」与
		// 「该候选方言」的冲突——换一个同类候选大概率还是缺（同为
		// openai_chat 的候选能力面相同）；让调用方决定是否终止整个请求。
		// body 已无意义但保留变量以免下面 warns 循环引用悬空。
		_ = body
		return false
	}
	for _, warn := range warns {
		s.log.Warn("v1 upstream encode warning", "request_id", reqID, "param", warn.Param, "message", warn.Message)
	}

	call, err := s.v1Upstream().Call(ctx, cand, pr.req, pr.req.Stream)
	if err != nil || call == nil {
		return false
	}
	defer call.Body.Close()

	if call.Err != nil {
		return false // 上游已判失败（连接层），换候选
	}
	// 非流式：整读上游响应体→DecodeResponse→EncodeResponse（入站方言出站）。
	raw, err := io.ReadAll(call.Body)
	if err != nil {
		return false
	}
	resp, err := cand.Adapter.DecodeResponse(raw)
	if err != nil {
		// 上游给了无法解析的响应体：协议违规，换候选。
		return false
	}
	// §4.1：model 恒为请求里的抽象模型名——上游响应里的真实模型名
	// （deepseek-chat 等）在出站前覆盖（防泄露池结构）。
	resp.Model = pr.req.Model
	out, err := pr.inbound.EncodeResponse(resp)
	if err != nil {
		// 入站方言编码失败（如 Anthropic 响应含 openai 方言没有的字段）——
		// 这是本请求不可恢复的冲突，向上抛给调用方终止。
		return false
	}
	// 终态结算的可观测产物：成功候选才写 Usage 与 RealModelID
	// （换过候选时，失败的尝试不该把它们的 usage/行 id 带进账）。
	if obs != nil {
		u := resp.Usage // 值类型取地址：调用方可能要区分「有 usage」与「没报」
		obs.Usage = &u
		obs.RealModelID = cand.ID
		obs.TotalMs = time.Since(start).Milliseconds()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return true
}

// streamOnce 对单个候选执行一次流式代理。
// 返回 true 表示已经开始向客户端写字节（此后不可再切候选——C7）；
// 返回 false 表示失败且未吐任何可见内容（可安全换候选）。
// 为什么带 obs：流式的终态 usage 只在事件里出现一次（I4），
// 不在循环里留就会丢；TTFT 也只有这里能测准（首段可见内容时刻）。
func (s *Server) streamOnce(ctx context.Context, w http.ResponseWriter, pr proxyReq, cand Candidate, reqID string, obs *attemptObs) bool {
	// 起点用于 TTFT 计算：必须在编码/上游握手之前取，否则首包延迟被低估。
	start := time.Now()
	upOpt := ir.EncodeOptions{
		UpstreamModel: cand.UpstreamModelID,
		RequestID:     reqID,
	}
	body, warns, err := cand.Adapter.EncodeRequest(&pr.req, upOpt)
	if err != nil {
		return false
	}
	_ = body // 上游调用方拿的是 body 字节；此处保留以防误删后误取 nil
	for _, warn := range warns {
		s.log.Warn("v1 upstream encode warning", "request_id", reqID, "param", warn.Param, "message", warn.Message)
	}
	call, err := s.v1Upstream().Call(ctx, cand, pr.req, true)
	if err != nil || call == nil {
		return false
	}
	defer call.Body.Close()
	if call.Err != nil {
		return false
	}

	// 上游事件流（候选方言）→ IR 事件 → 出站方言 StreamWriter。
	reader := cand.Adapter.NewStreamReader(call.Body, ir.DecodeOptions{RequestID: reqID})
	writer := pr.inbound.NewStreamWriter(w, ir.EncodeOptions{
		UpstreamModel: pr.req.Model, // 抽象模型名出站（§4.1）
		RequestID:     reqID,
	})

	ttft := time.Now() // 首帧时刻（TTFT 上报用，写入请求日志）
	first := true
	var streamErr error
	for {
		ev, err := reader.Next()
		if err != nil {
			streamErr = err
			break
		}
		if ev.Type == ir.EvDone {
			_ = writer.Write(ev)
			break
		}
		// I4/I5 收敛点：EncoderState.Accept 决定丢不丢（重复 usage 等）。
		if !writer.State().Accept(ev) {
			continue
		}
		// 终态 usage 只在 Accept 放行的那一份上留：I4 保证至多一次，
		// 重复被 Accept 丢弃时不应覆盖第一份（与 EncoderState 同口径）。
		if ev.Type == ir.EvUsage && ev.Usage != nil && obs != nil {
			obs.Usage = ev.Usage
		}
		if err := writer.Write(ev); err != nil {
			// 客户端断开：整场结束（写失败没有恢复语义）。
			streamErr = err
			break
		}
		if first && writer.State().Visible() {
			// TTFT 记录的是「第一段可见内容」而非首帧字节——start 壳帧
			// 不算（02-ir.md §5.3 同理：可见内容才是客户端感知的开始）。
			ttft = time.Now()
			// 上报的是**耗时**（相对函数入口的毫秒数），不是 Unix 绝对毫秒——
			// 旧实现直接写 ttft.UnixMilli()，得到 1.7e12 量级的绝对时间戳，
			// 看板上会误判成“首包延迟十几亿毫秒”。
			s.log.Info("v1 stream ttft", "request_id", reqID, "ttft_ms", ttft.Sub(start).Milliseconds())
			first = false
		}
	}
	// 用 errors.Is 而非字符串比较判断流结束：reader 一旦用 %w 包裹 EOF，
	// 字符串"EOF"不再匹配，会把正常收尾误报为流中失败并给客户端多吐一个错误帧。
	if streamErr != nil && !errors.Is(streamErr, io.EOF) {
		// 流中失败：已吐内容后不能改状态码（§4.2），只能写错误事件帧。
		// 出站方言的错误帧形状由各 StreamWriter 自理（openai=data:{"error":…}，
		// anthropic=event: error）。
		payload := classifyErr(streamErr)
		errEv := ir.Event{Type: ir.EvError, Err: &ir.Error{Code: payload.code, Message: payload.message}}
		if writer.State().Accept(errEv) {
			_ = writer.Write(errEv)
		}
	}
	// 流式尝试的可观测产物：TTFT 恒为「首段可见内容」；从未 Visible
	// （整场只有壳帧/错误帧）时保持 -1——缺样本不编造（与 routing 同口径）。
	if obs != nil {
		obs.TotalMs = time.Since(start).Milliseconds()
		obs.RealModelID = cand.ID
		if !first {
			obs.TTFTMs = ttft.Sub(start).Milliseconds()
		}
	}
	_ = writer.Close() // 幂等：补收尾帧（[DONE] / message_stop）
	return true
}

// v1SwitchBudgetMs 是候选切换的总时长预算（M6 接线）。
//
// 为什么 5s：与 routing.DefaultMaxSwitches=3 同属「一批候选同时坏」的
// 防御——次数上限限制打击面，时长上限避免用户干等（客户端重试比我们
// 内部慢慢换路体验好）。数字写在装配层（本包）：routing 明确把
// MaxDurationMs 定为装配参数（strategy.go 注释），协议语义层不养第二个常量。
const v1SwitchBudgetMs = 5000

// proxyV1 是三端点共享的编排入口。
// 失败切换：按 CandidateSource 顺序逐候选尝试；流式路径一旦
// streamOnce 返回 true（已吐可见内容）即不可回退（C7）。
//
// M6 接线三件事：
//   - 候选源返回 ErrModelNotFound → 404 model_not_found（抽象模型
//     不可见/不存在/真实模型名防泄露，02-abstract §6.2）；
//   - 切换预算走 routing.SwitchBudget.CanSwitch（次数+时长双上限；
//     传 visibleWritten=false 是对事实的忠实描述——本循环只在
//     「未吐可见内容」的窗口里换路）；
//   - 候选失败回传 CandidateFailureNotifier（冷却账本），trace 经
//     ctx sink 落到 X-FR-Trace 响应头。
//
// upstream/candidates 为 nil 时直接 no_candidate（未接线是部署态）。
func (s *Server) proxyV1(ctx context.Context, w http.ResponseWriter, pr proxyReq, requestID string) {
	// 终态结算的唯一入口：一次请求恰一个 defer（自围栏在 attempt 内会
	// 因换候选写多行——见 settle.go 文件头）。st 是单 goroutine 写读，
	// 无需加锁；TTFTMs 初值 -1 = 无样本。
	st := &settleState{obs: attemptObs{TTFTMs: -1}}
	defer s.settleRequest(ctx, pr, requestID, st)

	if pr.traceRequested {
		// sink 必须在调候选源之前挂上：trace 由候选源在排序完成后
		// 经 ctx 回填（routing.BuildTrace 的产物）。
		ctx = WithV1Trace(ctx)
	}
	cs := s.v1Candidates()
	var cands []Candidate
	if cs != nil {
		var err error
		cands, err = cs.Candidates(ctx, pr.req.Model, pr.req)
		switch {
		case errors.Is(err, ErrModelNotFound):
			// 模型不可见 → 404（客户端动作是「换模型」，与 503 的
			// 「稍后重试」必须分开——03-errors.md §3.1）。
			st.status, st.httpStatus, st.errorCode = "error", 404, "model_not_found"
			if pr.anthropicShape {
				s.writeV1AnthropicError(w, "model_not_found", "模型不存在", requestID)
			} else {
				s.writeV1Error(w, "model_not_found", "模型不存在", requestID)
			}
			writeV1Trace(w, ctx)
			return
		case err != nil:
			s.log.Warn("v1 candidate source error", "request_id", requestID, "error", err)
		}
	}
	if pr.traceRequested {
		// 必须在任何 WriteHeader 之前落头：流式路径在 streamOnce 的
		// setSSEHeaders 里就写了响应头，晚了就丢。
		writeV1Trace(w, ctx)
	}
	if len(cands) == 0 {
		// 无候选 → 503 no_candidate（03-errors.md §3.1；客户端动作是
		// 「稍后重试」而非改代码——与 404 model_not_found 区分）。
		st.status, st.httpStatus, st.errorCode = "error", 503, "no_candidate"
		if pr.anthropicShape {
			s.writeV1AnthropicError(w, "no_candidate", "当前无可用上游候选", requestID)
		} else {
			s.writeV1Error(w, "no_candidate", "当前无可用上游候选", requestID)
		}
		return
	}

	// 候选循环骨架与 responses 端点共享（预算/冷却回传单点实现）。
	// 返回 true = 已写出响应（成功或已吐内容），不再写错误体。
	// 候选循环的终局响应统一由骨架写（成功走 attempt、失败走 onExhausted）——
	// 调用点是无条件顺序调用，见 runCandidates 的注释。
	s.runCandidates(ctx, w, requestID, cs, cands, func(cand Candidate) bool {
		st.attempts++
		if pr.req.Stream {
			if s.streamOnce(ctx, w, pr, cand, requestID, &st.obs) {
				st.status, st.httpStatus, st.errorCode = "ok", 200, ""
				return true // 已开流：成功或已吐内容，不切
			}
			// 未吐可见内容：换下一个候选（C7 唯一切换窗口）
			s.log.Info("v1 stream candidate failed, switching", "request_id", requestID, "candidate", cand.Display)
			return false
		}
		if s.proxyOnce(ctx, w, pr, cand, requestID, &st.obs) {
			st.status, st.httpStatus, st.errorCode = "ok", 200, ""
			return true
		}
		s.log.Info("v1 candidate failed, switching", "request_id", requestID, "candidate", cand.Display)
		return false
	}, func() {
		// 全部候选失败（或预算耗尽）且未吐内容：对外 502 upstream_unavailable
		//（03-errors.md：上游都不可用；客户端可重试）。
		st.status, st.httpStatus, st.errorCode = "error", 502, "upstream_unavailable"
		if pr.anthropicShape {
			s.writeV1AnthropicError(w, "upstream_unavailable", "所有上游候选均失败", requestID)
			return
		}
		s.writeV1Error(w, "upstream_unavailable", "所有上游候选均失败", requestID)
	})
}

// writeV1Trace 把候选源回填的路由诊断落到 X-FR-Trace 响应头。
//
// 未要 trace（无 sink）或候选源未回填（M5 直连源）时静默跳过——
// trace 是诊断功能不是契约，缺了不该影响请求结果。
func writeV1Trace(w http.ResponseWriter, ctx context.Context) {
	sink := traceSinkFrom(ctx)
	if sink == nil || sink.trace == nil {
		return
	}
	v, err := sink.trace.HeaderValue()
	if err != nil {
		// 编码失败是内部缺陷：trace 缺失不影响请求，记日志即可。
		return
	}
	w.Header().Set("X-FR-Trace", v)
}

// adapters 全局缓存：Adapter() 零值可用，包级单例避免每请求分配。
var adapters = map[ir.Protocol]ir.Adapter{
	ir.ProtocolOpenAIChat: openai.Adapter(),
	ir.ProtocolAnthropic:  anthropic.Adapter(),
}

// adapterFor 按协议取适配器；responses 方言在 v1responses.go 里自解码。
func adapterFor(p ir.Protocol) ir.Adapter {
	return adapters[p]
}

// ensureHeaders 给成功响应补 v1 契约头。
func ensureHeaders(w http.ResponseWriter, requestID string) {
	// 01-inbound.md §7.2：请求 ID 必须回写客户端（对账依赖）。
	// mwRequestID 已写 X-Request-ID；这里补 X-Free-Router-Request-Id
	//（middleware.go 铁律不改，handler 自行补齐）。
	w.Header().Set("X-Free-Router-Request-Id", requestID)
}

// v1Deps 是面板依赖的进程级注册表：server.go 不能动，就用包级 registry
// 把 v1Panel 挂在 Server 指针上（指针键——每个 Server 实例一份，测试可
// 并行互不污染）。为什么不用全局单例：newTestServer 每个测试各建 Server，
// 全局单例会让测试互相覆盖。
var v1Registry = map[*Server]*v1Panel{}

// AttachV1 把上游与候选源接到 Server（Lead 装配路由时调用；测试注入
// fake 也走这里）。
func (s *Server) AttachV1(up Upstream, cs CandidateSource) {
	v1Registry[s] = &v1Panel{upstream: up, candidates: cs}
}

// v1Upstream 返回面板的上游实现；未接线时返回 nil（调用方判空走
// no_candidate 路径——比 panic 好：未接线是部署态不是程序错误）。
func (s *Server) v1Upstream() Upstream {
	if p, ok := v1Registry[s]; ok {
		return p.upstream
	}
	return nil
}

func (s *Server) v1Candidates() CandidateSource {
	if p, ok := v1Registry[s]; ok {
		return p.candidates
	}
	return nil
}

// v1Panel 是 v1 面板的运行时依赖（上游调用 + 候选来源）。
type v1Panel struct {
	upstream   Upstream
	candidates CandidateSource
	catalog    ModelCatalog
}

// decodeInboundBody 统一读入站请求体（mwBodyLimit 已做 413——这里只是
// io 层兜底；33 MiB 上限与 §7.1 对齐防极端绕过中间件层的直调路径）。
func decodeInboundBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 33<<20))
	if err != nil {
		writeV1ErrorSafe(w, "invalid_request", "读取请求体失败", "")
		return nil, false
	}
	return body, true
}

// writeV1ErrorSafe 在无 requestID 语境下的最小错误写出（解码失败早期路径）。
func writeV1ErrorSafe(w http.ResponseWriter, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(errStatus(code))
	_ = json.NewEncoder(w).Encode(v1Error{
		Error:     v1ErrorBody{Message: message, Type: ir.CodeToErrorType(code), Param: nil, Code: code},
		RequestID: requestID,
	})
}

// runCandidates 是候选循环的共享骨架（proxyV1 与 responses 端点复用）。
//
// 预算与冷却回传只在这里实现一次——两份循环会漂移，而「切换上限」
// 是 C7 红线的一部分（换路次数失控会把一批候选同时坏放大成对上游的
// 小规模打击，03-strategy.md §4.5）。
//
// **为什么无返回值**（code-review 建议（b），2026-10-22）：上一版返回
// bool（true = 响应已写完、调用方必须立即 return）把这层语义推给了调用方，
// 而「true 就 return」与 Go 惯用法（err==nil 继续）相反——本人就反判过一次，
// 症状是成功响应后再追加一段 502 JSON，只有契约测试能发现。现改为骨架
// 负责写完全部终局响应（成功由 attempt 写、失败由骨架写 502），调用点是
// 无条件顺序调用，反判在形状上不再可能。
//
// attempt 返回 true 表示已写出响应（成功，或已吐可见内容后失败），
// 骨架据此终止循环。cands 为空不写任何东西——空候选由调用方写
// no_candidate（它需要自己的错误形状：anthropic 形态与 OpenAI 形态不同）。
func (s *Server) runCandidates(ctx context.Context, w http.ResponseWriter, requestID string,
	cs CandidateSource, cands []Candidate, attempt func(Candidate) bool, onExhausted func()) {
	start := time.Now()
	var notifier CandidateFailureNotifier
	if n, ok := cs.(CandidateFailureNotifier); ok {
		notifier = n
	}
	budget := routing.SwitchBudget{MaxDurationMs: v1SwitchBudgetMs}
	for i, cand := range cands {
		if i > 0 {
			budget.ElapsedMs = time.Since(start).Milliseconds()
			if !budget.CanSwitch(false, i) {
				s.log.Warn("v1 switching budget exhausted",
					"request_id", requestID, "attempted", i, "elapsed_ms", budget.ElapsedMs)
				onExhausted()
				return
			}
		}
		if attempt(cand) {
			return
		}
		if notifier != nil {
			notifier.NotifyCandidateFailure(cand)
		}
	}
	onExhausted()
}
