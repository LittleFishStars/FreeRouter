package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/logging"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// 本文件是 task-10 第 2 点：relay 传输——经 dshprovider.Client 的 sidecar IPC。
//
// 为什么 relay 不自己发 HTTP（03-upstream.md §3.1 表）：模型目录/重试策略/
// 鉴权签名/流式归一的知识全在 lib/*-adapter.js 里，且与 providers-hub 同版本
// 发布；Go 重写等于把 14 个 adapter 的矩阵复制一遍并立刻漂移。
// relay 的代价必须写明：sidecar 挂了这一整类不可用（/readyz 两类分开算的原因）。

// RelayTransport 是 relay 类上游的传输实现。
//
// 方法面：dshprovider.Client.Call(ctx, "infer.stream", params)（04-sidecar.md
// 2.2 方法集之外的推理方法，sidecar 以 NDJSON 帧流式应答）。
// 帧 → ir.Event 的转换在 relay_stream.go：sidecar 侧 hub 未就绪时仍会返回
// E_NOT_IMPLEMENTED（归 ErrProtocol），但 Go 侧的调用形状、帧映射与错误归一
// 已定型——sidecar 一就绪即可用，不需要再改适配层。
type RelayTransport struct {
	client *dshprovider.Client
}

// NewRelayTransport 建传输。client 允许为 nil——sidecar 不可用时装配层
// 仍可持有（每次 Call 返回 E_SIDECAR_DOWN 形态的结构化错误，不 panic）。
func NewRelayTransport(client *dshprovider.Client) *RelayTransport {
	return &RelayTransport{client: client}
}

// inferParams 是 infer.stream 的参数（04-sidecar.md 5.x 的推理帧）。
type inferParams struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Body 是候选方言编码后的请求体原文（JSON）——adapter 说什么方言就传什么，
	// Go 侧不翻译（协议知识在 adapter，重写即双重维护）。
	Body   json.RawMessage `json:"body"`
	Stream bool            `json:"stream"`
}

// InferStream 发起一次 relay 推理调用。
//
// 返回 rc 是 sidecar 流式应答的载荷（SSE/NDJSON 事件流，由候选方言的
// StreamReader 消费）；err 归一到本包 Error（sidecar 错误码 → ErrClass 映射）。
func (t *RelayTransport) InferStream(ctx context.Context, tgt Target, body []byte, stream bool) (io.ReadCloser, error) {
	raw, err := t.inferPayload(ctx, tgt, body, stream)
	if err != nil {
		return nil, err
	}
	// 必须解一层信封：M8 起 sidecar 把 adapter 的 StreamChunk 还原成 SSE 文本，
	// 装进 {"sse": "..."} 返回；直接把信封交给 adapter 的 StreamReader，
	// 会得到「200 + 空回答」（信封不是 SSE，reader 只看到 EOF）——比 502 更难发现。
	return unwrapInferPayload(raw)
}

// inferPayload 是 relay 调用的公共前半段：参数编码、IPC 调用、错误归一。
//
// 为什么独立出来：InferStream 服务「字节流消费面」（Caller→httpapi），Open
// （relay_stream.go）服务「IR 事件消费面」；把 IPC 细节抄两遍，两边的错误分类
// 与空载荷判定就会开始漂移。
func (t *RelayTransport) inferPayload(ctx context.Context, tgt Target, body []byte, stream bool) ([]byte, error) {
	if err := ValidateTarget(tgt); err != nil {
		return nil, &Error{Class: ErrBadRequest, Message: err.Error()}
	}
	if t == nil || t.client == nil {
		// sidecar 不可用：relay 整类降级（vendor/selfhosted 不受影响）。
		return nil, &Error{Class: ErrConnect, Message: "sidecar 客户端不可用（relay 整类降级，转发不受影响）"}
	}
	if tgt.Kind != KindRelay {
		return nil, &Error{Class: ErrBadRequest, Message: "非 relay 目标不得走 RelayTransport"}
	}
	// provider 从 Target 提取：relay 的 RealModel 形如 "buddy/deepseek-v4.1"，
	// 前缀即 provider 名（03-upstream.md §2 Target 注释的同一约定）。
	provider, model := splitProviderModel(tgt.RealModel)
	if provider == "" {
		return nil, &Error{Class: ErrBadRequest, Message: "relay RealModel 缺 provider 前缀: " + tgt.RealModel}
	}
	params := inferParams{Provider: provider, Model: model, Body: json.RawMessage(body), Stream: stream}

	// dshprovider.Call 是「一问一答」语义（串行锁内单帧应答）；流式推理的多帧应答
	// 由 sidecar 在 result 内以帧流/帧数组返回，由 relay_stream.go 逐帧解成 ir.Event。
	raw, err := t.client.Call(ctx, "infer.stream", params)
	if err != nil {
		return nil, relayErr(err)
	}
	// 这里返回**原始载荷**：解信封是消费面的责任（InferStream 解成字节流，
	// Open 逐帧解成 ir.Event）——在公共前半段就解会逼着两条消费面都接受 io.ReadCloser。
	return raw, nil
}

// unwrapInferPayload 把 sidecar 的 infer.stream 应答换成 Go 侧要消费的字节流。
//
// 为什么需要解一层：sidecar（M8 起）真跑 hub adapter，adapter 吐的是 StreamChunk 事件，
// 由 sidecar 还原成 OpenAI chat.completion.chunk 的 SSE **文本**，装进 `{"sse": "..."}`。
// 而 relay 的消费面是「SSE 原文」，所以这一步必须把信封拆掉。
//
// 为什么要兼容裸载荷：M8 之前的契约假设是「adapter 吐的原文直接当载荷」，同一段代码
// 也会被旧版 sidecar（或未来的直通实现）打到。两种形态都接受，判定只看 `sse` 字段。
func unwrapInferPayload(raw []byte) (io.ReadCloser, error) {
	if len(raw) == 0 {
		return nil, &Error{Class: ErrProtocol, Message: "sidecar infer.stream 返回空载荷"}
	}
	// 两种形态都接受：M8 起 sidecar 把 adapter 的 StreamChunk 还原成 OpenAI SSE 文本，
	// 装进 {"sse": "..."}；旧版 sidecar（或未来直通实现）直接给载荷原文。
	// 判定只看 sse 字段：非空就拆信封，否则当裸载荷——不能按 JSON 解析失败就报错，
	// 因为 SSE 原文本身就不是合法 JSON（否则会把正常流误判成协议错）。
	var wrapped struct {
		SSE string `json:"sse"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.SSE != "" {
		return io.NopCloser(bytesReader([]byte(wrapped.SSE))), nil
	}
	return io.NopCloser(bytesReader(raw)), nil
}

// relayErr 把 dshprovider 错误映射到本包 ErrClass（03-upstream.md 2.3 表：
// 「错误从哪来——adapter 抛 LlmError → sidecar 映射成本册 2.2 的错误类」）。
func relayErr(err error) error {
	if err == nil {
		return nil
	}
	var de *dshprovider.Error
	if ok := asDshError(err, &de); ok {
		return &Error{
			Class:   relayClass(de.Code),
			Status:  0,
			Message: fmt.Sprintf("sidecar %s(%s): %s", "infer.stream", de.Code, de.Message),
			Cause:   err,
		}
	}
	return &Error{Class: Classify(err), Message: err.Error(), Cause: err}
}

// relayClass：sidecar 错误码（04-sidecar.md 2.4）→ 本包 ErrClass。
//
// 映射原则：同一语义用同一类，让 vendor/relay 两条路径的重试决策表完全共享。
func relayClass(code string) ErrClass {
	switch code {
	case "E_RATE_LIMITED":
		return ErrRateLimited
	case "E_UPSTREAM_TIMEOUT":
		return ErrTimeout
	case "E_UPSTREAM_HTTP":
		return ErrUpstream5xx
	case "E_CREDENTIAL_MISSING":
		return ErrAuth
	case "E_CREDENTIAL_EXPIRED":
		return ErrQuota // 过期凭据的处置与额度耗尽同：换凭据，不重试
	case "E_CREDENTIAL_READONLY", "E_STATE_FORBIDDEN":
		// 红线守卫码出现在运行时 = 有代码在违规调用（04-sidecar.md 2.4 设计说明）。
		// 归 internal_bug：让告警归到「修代码」而不是「换上游」。
		return ErrInternalBug
	case "E_SIDECAR_DOWN", "E_DEPENDENCY_MISSING":
		return ErrConnect
	case "E_NOT_IMPLEMENTED", "E_UNKNOWN_METHOD":
		return ErrProtocol
	case "E_BAD_REQUEST":
		return ErrBadRequest
	}
	return ErrProtocol
}

// asDshError 提取 dshprovider.Error（避免 import errors 只为一个 As）。
func asDshError(err error, target **dshprovider.Error) bool {
	for err != nil {
		if de, ok := err.(*dshprovider.Error); ok {
			*target = de
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// splitProviderModel 拆 "provider/model" → (provider, model)。
//
// 为什么用第一个斜杠：provider 名不含斜杠（sidecar 目录表里的名字全部如此），
// 模型名可能含（vendor 侧有 "org/model" 形态）——第一个斜杠前是 provider。
func splitProviderModel(realModel string) (string, string) {
	for i := 0; i < len(realModel); i++ {
		if realModel[i] == '/' {
			if i == 0 {
				return "", realModel
			}
			return realModel[:i], realModel[i+1:]
		}
	}
	return "", realModel
}

// bytesReader 避免为 bytes 包单开 import（仅此一处使用）。
func bytesReader(b []byte) io.Reader {
	return &byteSliceReader{b: b}
}

type byteSliceReader struct {
	b []byte
	i int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// LogRelayCall 记一次 relay 调用的结构化日志（§6.4 归因日志）。
func LogRelayCall(ctx context.Context, tgt Target, stream bool, elapsed time.Duration, err error) {
	attrs := []any{
		"upstream_id", tgt.UpstreamID,
		"kind", string(tgt.Kind),
		"real_model", tgt.RealModel,
		"stream", stream,
		"elapsed_ms", elapsed.Milliseconds(),
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
		logging.FromContext(ctx).Warn("relay 调用失败", attrs...)
		return
	}
	logging.FromContext(ctx).Info("relay 调用完成", attrs...)
}

// 编码复用：relay 的上游方向编码与 vendor 完全同构（encodeFor），
// 这里只补 IR 请求侧的空请求防呆（relay 上游方言由 adapter 决定）。
func relayEncode(adapter ir.Adapter, req *ir.Request, realModel string) ([]byte, error) {
	if req == nil {
		return nil, &Error{Class: ErrBadRequest, Message: "relay 编码收到空请求"}
	}
	return encodeFor(adapter, req, realModel)
}
