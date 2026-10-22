package httpapi

// v1models.go —— GET /v1/models（task-8 范围④）。
//
// 入站契约（docs/api/01-inbound.md §5）：
// - 形状 {"object":"list","data":[{"id","object":"model","created",...
//   "owned_by":"free-router"}]}——owned_by 固定值防泄露渠道。
// - **只列抽象模型**（红线 C2，02-abstract.md §6.2）：真实模型名对外
//   一律 404，列表里出现它们等于把池结构念给租户听。装配层
//   （app.modelCatalog）负责只读 active/deprecated。
// - 鉴权必须；组可见性交集过滤是 M7 的事（现在已鉴权即全可见）。
// - M5 不做弃用头（Deprecation/Sunset）。

import (
	"context"
	"net/http"
	"time"
)

// ModelEntry 是 /v1/models 列表条目（01-inbound §5 形状）。
type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelCatalog 是模型目录的数据源接口：列**抽象模型**名（不是真实模型）。
// 为什么是接口：目录来源（store 查询/sidecar 拉取的镜像）是部署态，
// handler 只消费「当前对外可见的模型 id」这一抽象面。
type ModelCatalog interface {
	// ListModelIDs 返回对外可见的模型 id 列表。
	ListModelIDs(ctx context.Context) ([]string, error)
}

// AttachV1Models 挂模型目录（Lead 装配；测试注入 fake）。
func (s *Server) AttachV1Models(mc ModelCatalog) {
	if p, ok := v1Registry[s]; ok {
		p.catalog = mc
	} else {
		v1Registry[s] = &v1Panel{catalog: mc}
	}
}

func (s *Server) v1Catalog() ModelCatalog {
	if p, ok := v1Registry[s]; ok {
		return p.catalog
	}
	return nil
}

// HandleV1Models 处理 GET /v1/models。
//
// 组可见性交集过滤（M7）在装配层做：handler 只见「一个 id 列表」这一
// 窄面，把「哪些组能看到哪些模型」的策略藏进 catalog 实现比埋在
// handler 里更可测（01-inbound §5）。
func (s *Server) HandleV1Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeV1Error(w, "invalid_request", "方法不允许", requestIDOf(r))
		return
	}
	requestID := requestIDOf(r)
	ensureHeaders(w, requestID)

	if !hasAuthedAccount(r) {
		s.writeV1Error(w, "invalid_api_key", "缺少有效的 API Key", requestID)
		return
	}

	cat := s.v1Catalog()
	if cat == nil {
		// 目录未接线：空列表（比 500 好——面板落地期部署态，客户端
		// 拿到合法空响应可继续；真实接线后立即有数据）。
		writeJSON(w, http.StatusOK, modelsList{Object: "list", Data: []ModelEntry{}})
		return
	}

	ids, err := cat.ListModelIDs(r.Context())
	if err != nil {
		s.log.Warn("v1 models catalog error", "request_id", requestID, "error", err)
		s.writeV1Error(w, "internal_error", "模型目录不可用", requestID)
		return
	}

	// created 用目录构建时刻：列表本身的语义是「目录快照」，
	// 不是每个模型的发布时刻（真值要等目录入库带发布时间才接）。
	now := time.Now().Unix()
	entries := make([]ModelEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, ModelEntry{
			ID:      id,
			Object:  "model",
			Created: now,
			// owned_by 固定值（§5：防泄露渠道归属——真实落点是池结构）。
			OwnedBy: "free-router",
		})
	}
	writeJSON(w, http.StatusOK, modelsList{Object: "list", Data: entries})
}

type modelsList struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}
