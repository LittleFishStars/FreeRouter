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

// CredentialWriteBack 是凭据轮换的写回钩子类型（SecretRef → 新密文）。
// 单独命名：装配层（app）要声明返回它的方法，匿名函数签名不可引用。
type CredentialWriteBack func(ctx context.Context, secretRef string, fresh json.RawMessage) error

// RawCredentialSource 按 SecretRef 取**凭据明文原文**（不提取 token）。
// 与 SecretSource（提取 Bearer）的差异：relay 推理的凭据是 JSON 对象
// （oauth.exchange 产出，含 access_token/machine_id/uid），sidecar 的
// adapter 要吃完整对象——单帧 stdio 传入，只读使用不落盘（红线①）。
type RawCredentialSource func(ctx context.Context, secretRef string) (json.RawMessage, error)

// RelayTransport 是 relay 类上游的传输实现。
//
// 方法面：dshprovider.Client.Call(ctx, "infer.stream", params)（04-sidecar.md
// 2.2 方法集之外的推理方法，sidecar 以 NDJSON 帧流式应答）。
// 帧 → ir.Event 的转换在 relay_stream.go：sidecar 侧 hub 未就绪时仍会返回
// E_NOT_IMPLEMENTED（归 ErrProtocol），但 Go 侧的调用形状、帧映射与错误归一
// 已定型——sidecar 一就绪即可用，不需要再改适配层。
//
// credentials（2026-10-31 qodercn 移植）：上游行带 SecretRef 时（贡献凭据），
// 推理前解密并单帧传给 sidecar 的 adapter——这是「DSH 账号池」之外的第二种
// 凭据来源，让 relay 类上游不再依赖 DSH 池。
type RelayTransport struct {
	client      *dshprovider.Client
	credentials RawCredentialSource
	// onCredentialRefreshed 是凭据轮换的写回钩子（2026-10-31）：sidecar 在
	// 105（auth_error）时用 refresh_token 续期并重试，新凭据随应答信封上行；
	// 不写回 = 每次请求都续期一次（旧 token 作废 → 每请求一跳）。
	// nil 时只跳过写回，不影响本次推理结果。返回 error 供日志归因。
	onCredentialRefreshed CredentialWriteBack
}

// NewRelayTransport 建传输。client 允许为 nil——sidecar 不可用时装配层
// 仍可持有（每次 Call 返回 E_SIDECAR_DOWN 形态的结构化错误，不 panic）。
// credentials 可为 nil——nil 时 relay 候选只在「凭据在 DSH 池」的场景可用。
func NewRelayTransport(client *dshprovider.Client, credentials RawCredentialSource, onCredentialRefreshed CredentialWriteBack) *RelayTransport {
	return &RelayTransport{client: client, credentials: credentials, onCredentialRefreshed: onCredentialRefreshed}
}

// inferParams 是 infer.stream 的参数（04-sidecar.md 5.x 的推理帧）。
type inferParams struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Body 是候选方言编码后的请求体原文（JSON）——adapter 说什么方言就传什么，
	// Go 侧不翻译（协议知识在 adapter，重写即双重维护）。
	Body   json.RawMessage `json:"body"`
	Stream bool            `json:"stream"`
	// Credential 是贡献凭据的解密原文（完整 JSON 对象，2026-10-31）。
	// omitempty：凭据在 DSH 池的 relay（红线①场景）不带它，sidecar 走
	// credentialRef/账号池的老路。
	Credential json.RawMessage `json:"credential,omitempty"`
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
	// 必须解一层信封（形态随消费面走，2026-10-31 起分两种）：
	//   流式 → {"sse": "文本", "refreshed_credential"?}，消费面是 adapter 的
	//   StreamReader（SSE 原文）；
	//   非流式 → {"completion": {...}, "refreshed_credential"?}，消费面是
	//   proxyOnce 的 DecodeResponse（要完整 chat.completion JSON——sidecar 已把
	//   chunk 流攒回整响应）。
	// 直接把信封交给消费面，前者得到「200 + 空回答」、后者得到「协议不符」
	// ——都比 502 更难发现。
	rc, refreshed, uerr := unwrapInferPayload(raw, stream)
	if uerr != nil {
		return nil, uerr
	}
	if len(refreshed) > 0 && t.onCredentialRefreshed != nil && tgt.SecretRef != "" {
		// 凭据轮换写回（与余额刷新的静默续期同一条通道）：失败只记日志，
		// 不影响本次推理结果（新凭据已丢，下次续期再轮换）。
		if err := t.onCredentialRefreshed(ctx, tgt.SecretRef, refreshed); err != nil {
			LogUpstreamFailureIfErr(ctx, tgt, &Error{Class: ErrInternalBug, Message: "续期凭据写回失败: " + err.Error()}, 0)
		}
	}
	return rc, nil
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

	// 贡献凭据（2026-10-31）：上游行带 SecretRef 时解密原文传给 sidecar——
	// adapter 的 resolveCredential 钩子吃完整凭据对象（access_token/machine_id/uid）。
	// 解密失败归 ErrAuth（与 vendor 路径的解密失败同类）。
	if tgt.SecretRef != "" && t.credentials != nil {
		cred, cerr := t.credentials(ctx, tgt.SecretRef)
		if cerr != nil {
			return nil, &Error{Class: ErrAuth, Message: "解密 relay 凭据失败: " + cerr.Error()}
		}
		params.Credential = cred
	}

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
func unwrapInferPayload(raw []byte, stream bool) (io.ReadCloser, json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil, &Error{Class: ErrProtocol, Message: "sidecar infer.stream 返回空载荷"}
	}
	// refreshed_credential 是 sidecar 在 105 续期后带回的新凭据（可选字段，
	// 两种形态都可能出现）——提取后由调用方写回密文。
	var refreshed struct {
		Fresh json.RawMessage `json:"refreshed_credential"`
	}
	_ = json.Unmarshal(raw, &refreshed)
	// 非流式：拆 {"completion": {...}} 信封，交出完整 chat.completion JSON。
	if !stream {
		var nonStream struct {
			Completion json.RawMessage `json:"completion"`
		}
		if err := json.Unmarshal(raw, &nonStream); err == nil && len(nonStream.Completion) > 0 {
			return io.NopCloser(bytesReader(nonStream.Completion)), refreshed.Fresh, nil
		}
		// 旧版 sidecar 非流式也回 {"sse":...}：交出去会在 DecodeResponse 报
		// 「协议不符」——如实暴露版本错配，不做静默转换。
	}
	// 两种形态都接受：M8 起 sidecar 把 adapter 的 StreamChunk 还原成 OpenAI SSE 文本，
	// 装进 {"sse": "..."}；旧版 sidecar（或未来直通实现）直接给载荷原文。
	// 判定只看 sse 字段：非空就拆信封，否则当裸载荷——不能按 JSON 解析失败就报错，
	// 因为 SSE 原文本身就不是合法 JSON（否则会把正常流误判成协议错）。
	var wrapped struct {
		SSE string `json:"sse"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.SSE != "" {
		return io.NopCloser(bytesReader([]byte(wrapped.SSE))), refreshed.Fresh, nil
	}
	return io.NopCloser(bytesReader(raw)), refreshed.Fresh, nil
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
