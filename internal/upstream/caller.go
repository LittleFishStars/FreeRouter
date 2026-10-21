package upstream

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 本文件是 task-10 第 4 点：满足 httpapi.Upstream 接口（Call(ctx, cand, req,
// stream) → *UpstreamCall{Body, Err}）的适配器。
//
// 铁律：不改 internal/httpapi 任何文件——httpapi.Upstream 是它自己定义的消费面，
// 本包提供「同时满足它」的实现（鸭子类型：方法集一致即可赋值）。
// httpapi.Candidate / httpapi.UpstreamCall 的字段在装配层（Lead 的 internal/app）
// 完成搬运，本文件不 import httpapi；这里定义**同构**的本地类型并导出适配函数，
// Lead 装配时用一个薄壳把两边接起来（见文件尾的装配提示注释）。

// CallCandidate 是 httpapi.Candidate 的同构形状（UpstreamModelID/Display/Adapter）。
//
// 为什么本地再定义一份而不是 import：import httpapi 会把 upstream→httpapi 的
// 依赖方向倒过来（httpapi 已经 import upstream 的兄弟包 dshprovider 经由面板），
// 分层上 L3（upstream）不该认识 L1 出口层。
type CallCandidate struct {
	// UpstreamModelID 发给上游的真实模型 id。
	UpstreamModelID string
	// Display 内部日志用的落点描述（不外发）。
	Display string
	// Adapter 候选方言的 IR 适配器（EncodeRequest 目标方言）。
	Adapter ir.Adapter
}

// CallResult 是一次调用的结果（httpapi.UpstreamCall 的同构形状）。
type CallResult struct {
	Body io.ReadCloser
	Err  error
}

// Caller 满足 httpapi.Upstream 的适配器核心。
//
// Call(ctx, cand, req, stream)：
//   - 编码：cand.Adapter.EncodeRequest（RealModel=cand.UpstreamModelID）；
//   - 派发：Target.Kind==relay → RelayTransport；vendor/selfhosted → HTTPTransport；
//   - 应答：rc 包成 CallResult.Body；错误归一 *Error（ErrClass）放 CallResult.Err。
//
// 为什么 Err 放在结果里而不是 Go error 返回值：httpapi.UpstreamCall 的契约
// 区分「连接层失败（换候选）」与「协议违规」——Body/Err 的组合形状由它定义，
// 适配器照做。本方法 Go error 仅用于编程错误（nil 候选等）。
type Caller struct {
	relay *RelayTransport
	http  *HTTPTransport
	// TargetOf 把候选映射成 Target。为什么是函数字段：CandidateInfo→Target 的
	// 数据来源（upstreams 行/real_models 行）在装配层，本包只提供 TargetOf 纯函数；
	// 装配层包一个闭包查行→构造 CandidateInfo→TargetOf。
	targetOf func(cand CallCandidate) (Target, error)
}

// NewCaller 建适配器。relay/http 任一为 nil 时对应类候选返回结构化错误
// （E 不可用），不是 panic——装配是渐进的（先 vendor 后 relay 也合法）。
func NewCaller(relay *RelayTransport, http *HTTPTransport, targetOf func(cand CallCandidate) (Target, error)) *Caller {
	return &Caller{relay: relay, http: http, targetOf: targetOf}
}

// Call 执行一次上游调用（httpapi.Upstream.Call 的同构方法）。
func (c *Caller) Call(ctx context.Context, cand CallCandidate, req ir.Request, stream bool) (*CallResult, error) {
	if c == nil || c.targetOf == nil {
		return nil, fmt.Errorf("upstream: Caller 未装配 targetOf")
	}
	tgt, err := c.targetOf(cand)
	if err != nil {
		// 候选数据错位（如 relay 前缀不一致）：结构化失败，Body=nil。
		return &CallResult{Err: &Error{Class: ErrBadRequest, Message: err.Error()}}, nil
	}
	if err := ValidateTarget(tgt); err != nil {
		return &CallResult{Err: &Error{Class: ErrBadRequest, Message: err.Error()}}, nil
	}

	// 编码（两条路径共享的前半段，§2.3）。
	body, encErr := encodeFor(cand.Adapter, &req, cand.UpstreamModelID)
	if encErr != nil {
		if ue, ok := encErr.(*ir.UnsupportedError); ok {
			// C6 能力缺失：原样透传（httpapi.classifyErr 认它）。
			return &CallResult{Err: ue}, nil
		}
		return &CallResult{Err: &Error{Class: ErrBadRequest, Message: encErr.Error(), Cause: encErr}}, nil
	}

	// 派发（两条物理路径的分叉点）。
	var rc io.ReadCloser
	var callErr error
	started := time.Now()
	switch tgt.Kind {
	case KindRelay:
		if c.relay == nil {
			return &CallResult{Err: &Error{Class: ErrConnect, Message: "relay 传输未装配"}}, nil
		}
		rc, callErr = c.relay.InferStream(ctx, tgt, body, stream)
	default:
		if c.http == nil {
			return &CallResult{Err: &Error{Class: ErrConnect, Message: "HTTP 传输未装配"}}, nil
		}
		var status int
		rc, status, callErr = c.http.Do(ctx, tgt, body, httpCallOptions{adapter: cand.Adapter, stream: stream})
		_ = status
	}
	elapsed := time.Since(started)
	LogUpstreamFailureIfErr(ctx, tgt, callErr, elapsed)
	if callErr != nil {
		return &CallResult{Err: callErr}, nil
	}
	if rc == nil {
		return &CallResult{Err: &Error{Class: ErrProtocol, Message: "传输层返回空应答体"}}, nil
	}
	// 成功：结构化日志（任务第 5 点）+ 流式包装计数器。
	LogUpstreamSuccess(ctx, tgt, 200, elapsed, 0)
	if stream {
		stats := &StreamStats{UpstreamStatus: 200}
		tracked := &statsTrackingReader{r: rc, stats: stats}
		return &CallResult{Body: readCloserOf(tracked)}, nil
	}
	return &CallResult{Body: rc}, nil
}

// LogUpstreamFailureIfErr 失败时的日志包装（成功路径直接走 LogUpstreamSuccess）。
func LogUpstreamFailureIfErr(ctx context.Context, tgt Target, err error, elapsed time.Duration) {
	if err == nil {
		return
	}
	class := ErrProtocol
	status := 0
	if ue, ok := err.(*Error); ok {
		class = ue.Class
		status = ue.Status
	}
	LogUpstreamFailure(ctx, tgt, 1, class, status, elapsed)
}

// readCloserOf 组合 statsTrackingReader 与 Close。
func readCloserOf(r *statsTrackingReader) io.ReadCloser {
	return &trackedReadCloser{r: r}
}

type trackedReadCloser struct {
	r      *statsTrackingReader
	closed bool
}

func (t *trackedReadCloser) Read(p []byte) (int, error) { return t.r.Read(p) }
func (t *trackedReadCloser) Close() error               { t.closed = true; return nil }

// 装配提示（Lead 的 internal/app 薄壳，约 20 行）：
//
//	type upstreamShim struct{ c *upstream.Caller }
//	func (s upstreamShim) Call(ctx context.Context, cand httpapi.Candidate,
//	    req ir.Request, stream bool) (*httpapi.UpstreamCall, error) {
//	    res, err := s.c.Call(ctx, upstream.CallCandidate{
//	        UpstreamModelID: cand.UpstreamModelID,
//	        Display:         cand.Display,
//	        Adapter:         cand.Adapter,
//	    }, req, stream)
//	    if err != nil { return nil, err }
//	    if res == nil { return nil, nil }
//	    return &httpapi.UpstreamCall{Body: res.Body, Err: res.Err}, nil
//	}
//
// targetOf 闭包：查 real_models/upstreams 行 → upstream.CandidateInfo →
// upstream.TargetOf（注意 relay 行 base_url 的 sidecar:// 前缀在那里被剥掉）。
