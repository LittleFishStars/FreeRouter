package upstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 本文件是 task-10 第 1 点：vendor / selfhosted 的 HTTPS 传输实现。
//
// 出站路径：Target.BaseURL → Pool 复用的 http.Transport → POST → 响应体。
// 编解码走 ir.Adapter（EncodeRequest→POST→DecodeResponse / NewStreamReader 流式），
// 错误归一走本包 Classify/ClassifyStatus（task-6 已落的分类表）。

// HTTPTransport 是 vendor/selfhosted 上游的传输实现。
//
// 为什么字段只有 pool 与 secret 源：Transport 按 (kind,host) 复用（§5.3），
// 凭据在请求时才解（明文只在内存里存在一次请求的时间，04-upstream.md §4 表）。
type HTTPTransport struct {
	pool *Pool
	// SecretSource 解 SecretRef → Bearer 明文。nil 时仅支持无鉴权 selfhosted。
	// 为什么是接口：AES-GCM 解密在 store 侧，本包不 import internal/store
	//（写域铁律），装配层把闭包传进来。
	secrets SecretSource
}

// SecretSource 按凭据引用取 Bearer。
//
// relay 恒空引用（账号池在 sidecar）；vendor/selfhosted 的 ref 是 upstream_secrets.id
// 的字符串形态。返回 ("", nil) 表示「该上游无鉴权」——selfhosted 本机端点常见。
type SecretSource func(ctx context.Context, secretRef string) (string, error)

// NewHTTPTransport 建传输。pool 为 nil 时用默认配置建一个（测试便利），
// 装配层应传入共享 pool（单例——连接池的价值就在复用）。
func NewHTTPTransport(pool *Pool, secrets SecretSource) *HTTPTransport {
	if pool == nil {
		pool = NewPool(PoolConfig{})
	}
	return &HTTPTransport{pool: pool, secrets: secrets}
}

// httpCall 是一次出站调用的载荷：httpapi.UpstreamCall 的 Body 是 SSE 或 JSON，
// 这里的 Adapter 由 Candidate 携带（同方言编解码闭环）。
type httpCallOptions struct {
	adapter ir.Adapter
	stream  bool
}

// Do 执行一次出站请求：编码→POST→按状态码分类。
//
// 返回的 rc 是上游响应体（200 时）；err 非空时 rc 为 nil。
// 4xx/5xx 不在这里重试——重试是跨候选决策（task-6 RetryController），
// 传输层只负责把失败**归类**（ErrClass）交给上层。
func (t *HTTPTransport) Do(ctx context.Context, tgt Target, body []byte, opts httpCallOptions) (io.ReadCloser, int, error) {
	if err := ValidateTarget(tgt); err != nil {
		return nil, 0, &Error{Class: ErrBadRequest, Message: err.Error()}
	}
	if tgt.Kind == KindRelay {
		// relay 不该走到这里：relay.go 走 sidecar IPC。拦下来是防御
		// 装配错配（BaseURL 恒空在这里连 URL 都拼不出，错误信息会更迷惑）。
		return nil, 0, &Error{Class: ErrBadRequest, Message: "relay 类上游不走 HTTPTransport（走 sidecar IPC）"}
	}
	if t.pool == nil {
		return nil, 0, &Error{Class: ErrInternalBug, Message: "连接池未初始化"}
	}
	tr, err := t.pool.Transport(tgt.Kind, tgt.BaseURL)
	if err != nil {
		return nil, 0, &Error{Class: ErrBadRequest, Message: err.Error()}
	}

	// 并发名额：单上游上限（§5.3 待定项——host 级兜底之外按上游实体再卡一道）。
	release, ok := t.pool.Acquire(ctx, tgt.UpstreamID)
	if !ok {
		return nil, 0, &Error{Class: ErrTimeout, Message: "等待上游并发名额超时（单上游连接上限）"}
	}
	defer release()

	// 拼 URL：BaseURL + /chat/completions（openai-chat 方言的既定路径）。
	// responses 方言在 M5 入站侧自解码；vendor 出站按 chat 走（03-upstream.md §4
	// 「默认 OpenAI 兼容（chat 为主）」）。
	endpoint := strings.TrimRight(tgt.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, &Error{Class: ErrBadRequest, Message: "构造出站请求失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	// 鉴权头：有 SecretSource 且 ref 非空才带 Bearer。
	// 无鉴权 selfhosted（本机 ollama 等）ref 为空——不带 Authorization，
	// 而不是带一个 "Bearer "（空 token 会被多数端点 401）。
	if t.secrets != nil && tgt.SecretRef != "" {
		token, err := t.secrets(ctx, tgt.SecretRef)
		if err != nil {
			return nil, 0, &Error{Class: ErrAuth, Message: "解密凭据失败: " + err.Error()}
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		// 连接层失败：http=000 语境。Classify 按 §2.2 文案归位。
		return nil, 0, &Error{Class: Classify(err), Message: err.Error(), Cause: err}
	}
	if resp.StatusCode != http.StatusOK {
		// 非 200：读一小段错误体给排障（错误体可能很大，只取 4KiB）。
		snippet := readSnippet(resp.Body, 4096)
		resp.Body.Close()
		return nil, resp.StatusCode, &Error{
			Class:  ClassifyStatus(resp.StatusCode),
			Status: resp.StatusCode,
			Message: fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, snippet),
		}
	}
	return resp.Body, resp.StatusCode, nil
}

// readSnippet 读错误体的前 n 字节（错误体不是契约内容，仅排障日志用）。
func readSnippet(r io.Reader, n int) string {
	if r == nil {
		return ""
	}
	b := make([]byte, n)
	m, _ := io.ReadFull(r, b)
	return strings.TrimSpace(string(b[:m]))
}

// ErrInternalBug 是程序错误（pool 未初始化等）的类。
// 为什么需要：它不该参与任何重试（重试烧额度），也不该计健康失败——
// 它是装配缺陷，正确的处置是告警 + 修代码，重试只会掩盖。
const ErrInternalBug ErrClass = "internal_bug"

// encodeFor 上游方向编码：把 IR 请求编成候选方言的请求体。
//
// 为什么独立函数：relay.go 也要用同一份（两条物理路径共享「编码→出站」的前半段，
// 差异只在 POST 还是 IPC——03-upstream.md §2.3 表的「不共享传输实现」边界）。
func encodeFor(adapter ir.Adapter, req *ir.Request, realModel string) ([]byte, error) {
	if adapter == nil {
		return nil, &Error{Class: ErrInternalBug, Message: "候选缺少 IR 适配器"}
	}
	body, _, err := adapter.EncodeRequest(req, ir.EncodeOptions{UpstreamModel: realModel})
	if err != nil {
		// C6：能力缺失是结构化错误，透传给 httpapi 的 classifyErr 归
		// capability_unsupported；这里不吞不换码。
		return nil, err
	}
	return body, nil
}

// nopCloser 把已读入内存的字节包成 ReadCloser（非流式应答的统一返回形状）。
type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

// statsTrackingReader 在读流时维护 StreamStats 的计数。
//
// 为什么需要：VisibleWritten 供 C7 判定（httpapi 侧已由 EncoderState 判定），
// BytesIn/EventsOut 供 meter 与排障；传输层是唯一能数「进来的字节」的位置。
type statsTrackingReader struct {
	r        io.Reader
	stats    *StreamStats
	ttftAt   time.Time
	firstEvt bool
}

func (s *statsTrackingReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.stats.BytesIn += int64(n)
	return n, err
}

// MarkEvent 由流式调用方在每个事件后调用（EventsOut 计数）。
// TTFT 在首个事件时刻记录（含协议壳帧——精确的「可见内容 TTFT」由
// httpapi 侧记录，这里的 TTFT 是传输层口径：首字节到首个完整事件）。
func (s *statsTrackingReader) MarkEvent() {
	s.stats.EventsOut++
	if !s.firstEvt {
		s.firstEvt = true
		if s.ttftAt.IsZero() {
			s.ttftAt = time.Now()
		}
	}
}

var _ io.ReadCloser = (*nopCloser)(nil)
