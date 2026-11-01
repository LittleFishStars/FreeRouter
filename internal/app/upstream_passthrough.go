package app

// upstream_passthrough.go —— 透传执行面（httpapi.PassthroughUpstream 的实现）。
//
// 职责只有三件（口径见 internal/httpapi/v1passthrough.go 文件头）：
//   1. 选一个可用上游（按账号归属 + 启用状态 + 可达性）；
//   2. 把客户端原始请求原样交给 upstream.HTTPTransport.Passthrough；
//   3. 把上游原始响应原样交回 httpapi。
//
// 为什么落在装配层：它是唯一同时能看见 store（上游行/凭据）与 upstream
//（HTTP 传输）的层（app.go 包注释的依赖方向约定）。httpapi 不该认识这两者。
//
// 为什么不像推理那样走候选循环：文件上传的 body 是流（不可重放），
// 换一个上游等于再发一次同一个文件——那既浪费带宽也可能让上游产生两份
// 副本。所以只挑一个，失败就如实返回。

import (
	"context"
	"fmt"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// passthroughBridge 是 httpapi.PassthroughUpstream 的实现。
type passthroughBridge struct {
	app  *App
	http *upstream.HTTPTransport
}

// newPassthrough 构造透传执行面（server_build 里注入）。
func (a *App) newPassthrough() httpapi.PassthroughUpstream {
	return &passthroughBridge{
		app: a,
		// 与推理链路共用同一个 Transport 实例（连接池复用是它的价值所在）。
		http: a.upstreamHTTP,
	}
}

// Passthrough 选一个可用上游并原样转发。
func (b *passthroughBridge) Passthrough(ctx context.Context, in httpapi.PassthroughInput) (*httpapi.PassthroughResponse, error) {
	if b.http == nil {
		return nil, fmt.Errorf("app: 透传未装配 HTTP 传输")
	}

	tgt, err := b.pickTarget(ctx, in.AccountID)
	if err != nil {
		return nil, err
	}

	res, err := b.http.Passthrough(ctx, tgt, upstream.PassthroughRequest{
		Method:        in.Method,
		Path:          in.Endpoint,
		RawQuery:      in.RawQuery,
		Header:        in.Header,
		Body:          in.Body,
		ContentLength: in.ContentLength,
	})
	if err != nil {
		return nil, err
	}

	b.app.log.Info("v1 passthrough",
		"request_id", in.RequestID,
		"endpoint", in.Endpoint,
		"method", in.Method,
		"upstream_id", tgt.UpstreamID,
		"status", res.Status)

	return &httpapi.PassthroughResponse{
		Status:     res.Status,
		Header:     res.Header,
		Body:       res.Body,
		UpstreamID: tgt.UpstreamID,
	}, nil
}

// pickTarget 选一个可用于透传的上游。
//
// 选择规则（按优先级）：
//  1. 该账号**自有**的启用上游（贡献者上传的文件属于他自己的供应商账号，
//     这是唯一"文件归属正确"的选择）；
//  2. 没有自有上游时退到平台自有的启用上游。
//
// 为什么优先账号自有：Files 是**有状态资源**——文件存在某个上游的账号下，
// 后续 chat 请求要靠 file_id 引用它。如果上传时用了 A 的凭据、后续推理落到 B，
// file_id 在 B 那边不存在，模型会收到一个无效引用。所以透传必须尽量与
// 「该账号推理时会用的上游」一致；账号自有优先是这条一致性的最直接落地。
//
// relay 类上游被排除：它没有可直连地址（Target.BaseURL 恒为空），
// 文件与搜索都经 sidecar adapter，而 sidecar 的 adapter 没有透传能力。
func (b *passthroughBridge) pickTarget(ctx context.Context, accountID string) (upstream.Target, error) {
	ups, err := b.app.store.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		return upstream.Target{}, fmt.Errorf("app: 透传查询上游失败: %w", err)
	}

	// 先按归属分组，再按优先级挑：自有在前，平台自有在后。
	var own, platform []store.Upstream
	for _, u := range ups {
		if !u.Enabled || u.RemovedAt != nil {
			continue
		}
		// relay 没有可直连地址（见函数注释末段）。
		if strings.TrimSpace(u.BaseURL) == "" || upstream.IsRelayBaseURL(u.BaseURL) {
			continue
		}
		if u.OwnerAccountID != nil && accountID != "" && *u.OwnerAccountID == accountID {
			own = append(own, u)
			continue
		}
		platform = append(platform, u)
	}

	candidates := append(own, platform...)
	if len(candidates) == 0 {
		// 没有可用上游 → 与推理的 no_candidate 同口径（503，客户端稍后重试）。
		return upstream.Target{}, &upstream.Error{
			Class:   upstream.ErrBadRequest,
			Message: "当前没有可用于透传的上游（Files/Search 需要非 relay 的启用上游）",
		}
	}

	u := candidates[0]
	tgt := upstream.Target{
		UpstreamID:   u.ID,
		Kind:         upstream.Kind(u.Kind),
		BaseURL:      u.BaseURL,
		CredentialID: u.ID,
		RealModel:    "", // 透传不涉及模型
	}
	if u.SecretID != nil {
		tgt.SecretRef = *u.SecretID
	}
	if err := upstream.ValidateTarget(tgt); err != nil {
		return upstream.Target{}, fmt.Errorf("app: 透传上游 %s 不可用: %w", u.ID, err)
	}
	return tgt, nil
}
