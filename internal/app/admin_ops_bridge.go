package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// admin_ops_bridge.go —— 管理面的「触发型」依赖（M10 补齐）。
//
// 两个能力都不属于 httpapi：刷新归 upstream 域（sidecar models.list + upsert），
// 上游连通性测试要用真实的传输层；httpapi 只负责"人点了一下"。
// 同一条纪律：桥只翻译与调度，不重新实现探测逻辑。

// refreshBridge 实现 httpapi.AdminModelRefresher。
type refreshBridge struct {
	refresher *upstream.ModelRefresher
}

// RefreshModels 同步跑一轮目录刷新（不启动后台循环）。
//
// 为什么用 RefreshOnce 而不是 Start：管理面点"刷新"要的是"现在这一轮的结果"，
// 后台循环会返回一个没有结果的空响应；而且并发点两次只应串行跑两次。
func (r *refreshBridge) RefreshModels(ctx context.Context) (map[string]any, error) {
	if r == nil || r.refresher == nil {
		return nil, httpapi.ErrAdminNotWired
	}
	res, err := r.refresher.RefreshOnce(ctx)
	if err != nil {
		return nil, err
	}
	failures := make([]map[string]any, 0, len(res.Failures))
	for _, f := range res.Failures {
		// Failure 里的 Upstream 是内部标识（名字或 id），原样透出即可：
		// 管理面本来就能看到全部上游。
		failures = append(failures, map[string]any{
			"upstream": f.Upstream,
			"reason":   f.Error(),
		})
	}
	return map[string]any{
		"upstreams":      res.Upstreams,
		"models":         res.Models,
		"marked_missing": res.MarkedMissing,
		"failures":       failures,
	}, nil
}

// upstreamTesterBridge 实现 httpapi.AdminUpstreamTester。
type upstreamTesterBridge struct {
	store   *store.Store
	sidecar *dshprovider.Client
	cfg     *config.Config
	httpc   *http.Client
}

// TestUpstream 做一次轻量连通性测试：relay 问 sidecar 健康与目录；
// vendor/selfhosted 打 {base}/models（带 3s 超时，不重试）。
//
// 为什么按 kind 分派而不是统一发一个请求：relay 的出口是 sidecar 的
// provider adapter（没有 base_url 可打），vendor/selfhosted 才是 HTTP 端点。
// 统一发请求要么编造 URL、要么把 relay 误判为不可达。
func (t *upstreamTesterBridge) TestUpstream(ctx context.Context, id string) (map[string]any, error) {
	if t == nil || t.store == nil {
		return nil, httpapi.ErrAdminNotWired
	}
	uid, err := decodeID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: 上游 id 形状非法", httpapi.ErrAdminNotFound)
	}
	u, err := t.store.Repos().Models.GetUpstreamByID(ctx, uid)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"upstream_id": id,
		"kind":        u.Kind,
		"enabled":     u.Enabled,
	}
	switch u.Kind {
	case "relay":
		if t.sidecar == nil {
			out["ok"] = false
			out["reason"] = "sidecar 未运行：relay 类上游不可用（不影响 vendor/selfhosted 转发）"
			return out, nil
		}
		h, err := t.sidecar.Health(ctx)
		if err != nil {
			out["ok"] = false
			out["reason"] = "sidecar 健康检查失败: " + err.Error()
			return out, nil
		}
		out["ok"] = true
		out["sidecar_version"] = h.Version
		out["providers"] = h.Providers
		out["reason"] = "sidecar 在线（具体渠道的额度与登录态由贡献页展示）"
		return out, nil
	default:
		base := strings.TrimRight(u.BaseURL, "/")
		if base == "" {
			out["ok"] = false
			out["reason"] = "该上游未配置 base_url，无法测试"
			return out, nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
		if err != nil {
			return nil, err
		}
		start := time.Now()
		resp, err := t.httpc.Do(req)
		if err != nil {
			out["ok"] = false
			out["reason"] = "连接失败: " + err.Error()
			return out, nil
		}
		defer func() { _ = resp.Body.Close() }()
		out["http_status"] = resp.StatusCode
		out["latency_ms"] = time.Since(start).Milliseconds()
		out["ok"] = resp.StatusCode >= 200 && resp.StatusCode < 500
		if out["ok"].(bool) {
			out["reason"] = "端点可达（4xx 也视为可达：说明鉴权或路径问题，不是网络问题）"
		} else {
			out["reason"] = fmt.Sprintf("端点返回 %d", resp.StatusCode)
		}
		return out, nil
	}
}

// buildRefreshBridge 取 App 持有的刷新器；未启动（sidecar 关闭）时返回 nil，
// 管理面会以「未接线」响应而不是 500。
func (a *App) buildRefreshBridge() httpapi.AdminModelRefresher {
	if a.modelRefresher == nil {
		return nil
	}
	return &refreshBridge{refresher: a.modelRefresher}
}

var _ = json.Marshal // 保留 json 导入供后续扩展（响应里可能出现嵌套结构）
