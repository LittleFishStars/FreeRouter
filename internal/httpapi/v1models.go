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
	"strings"
	"time"
)

// ModelEntry 是 /v1/models 列表条目（01-inbound §5 形状）。
type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	// 能力面（可选，零值省略）。为什么对外报能力而不报候选：
	// 客户端要的是「这个模型能不能看图、支持哪些思考档位」才能正确发请求，
	// 而它按红线 C2 永远不该知道背后有几个上游。能力是**聚合结论**，
	// 候选是**池结构**——前者是契约，后者是实现。
	//
	// 字段名同时给 snake 与 camel 两种拼写：OpenAI 生态读 snake_case，
	// 而 DSH 的 readListing 两种都认（`context_window`/`contextWindow`、
	// `max_output_tokens`/`maxTokens`）。多写一份的代价是几十字节响应，
	// 少写一份的代价是客户端读不到能力。
	ContextWindow     int64    `json:"context_window,omitempty"`
	ContextWindowC    int64    `json:"contextWindow,omitempty"`
	MaxOutputTokens   int64    `json:"max_output_tokens,omitempty"`
	MaxOutputTokensC  int64    `json:"maxOutputTokens,omitempty"`
	ReasoningEfforts  []string `json:"reasoning_efforts,omitempty"`
	ReasoningEffortsC []string `json:"reasoningEfforts,omitempty"`
	Modalities        []string `json:"modalities,omitempty"`
}

// ModelCatalog 是模型目录的数据源接口：列**抽象模型**名（不是真实模型）。
// 为什么是接口：目录来源（store 查询/sidecar 拉取的镜像）是部署态，
// handler 只消费「当前对外可见的模型 id」这一抽象面。
type ModelCatalog interface {
	// ListModelIDs 返回对外可见的模型 id 列表。
	ListModelIDs(ctx context.Context) ([]string, error)
}

// ModelCapabilitySource 是目录的**可选**能力面（判型取用，不强制实现）。
//
// 为什么做成可选接口而不是给 ModelCatalog 加方法：既有实现与测试 fake 只需
// 提供 id 列表就能工作，强制加方法会逼所有实现方一起改；判型是本项目对
// "可选能力"的既定做法（同 CandidateOutcomeNotifier）。
type ModelCapabilitySource interface {
	// ListModelCapabilities 返回「抽象模型名 → 聚合能力」。
	// 读不到能力的模型可以缺席（handler 按"未声明"处理，不编造）。
	ListModelCapabilities(ctx context.Context) (map[string]ModelCapability, error)
}

// ModelCapability 是一个抽象模型对外可声明的能力聚合值。
type ModelCapability struct {
	// ContextWindow 是候选中的**最大**窗口：路由按「有候选装得下就能服务」
	// 过滤，所以对外报 max 与 FilterPaths 的语义一致（报 min 会低估能力，
	// 让客户端提前放弃一个其实能服务的模型）。
	ContextWindow int64
	// MaxOutputTokens 是候选中的最大输出上限（同上口径）。
	MaxOutputTokens int64
	// Efforts 是候选档位的**并集**：只要有一个候选支持该档，请求就可能被服务。
	// 拼写已归一到 IR 口径（off/low/high/max），与 reasoning_effort 的取值一致。
	Efforts []string
	// Modalities 是输入模态并集（"text"/"image"…）。
	Modalities []string
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

	// 能力面是可选的：目录实现方没提供时按"未声明"处理（省略字段），
	// 而不是回一份猜测的能力——客户端宁可按无能力保守发请求，
	// 也不该被一份编造的能力位误导成 400。
	var caps map[string]ModelCapability
	if cs, ok := cat.(ModelCapabilitySource); ok {
		caps, err = cs.ListModelCapabilities(r.Context())
		if err != nil {
			// 能力读失败不影响列表本身：降级成无能力输出并留痕。
			s.log.Warn("v1 models capability error", "request_id", requestID, "error", err)
			caps = nil
		}
	}

	// created 用目录构建时刻：列表本身的语义是「目录快照」，
	// 不是每个模型的发布时刻（真值要等目录入库带发布时间才接）。
	now := time.Now().Unix()
	entries := make([]ModelEntry, 0, len(ids))
	for _, id := range ids {
		e := ModelEntry{
			ID:      id,
			Object:  "model",
			Created: now,
			// owned_by 固定值（§5：防泄露渠道归属——真实落点是池结构）。
			OwnedBy: "free-router",
		}
		if c, has := caps[id]; has {
			e.applyCapability(c)
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, modelsList{Object: "list", Data: entries})
}

// applyCapability 把聚合能力写进列表条目（snake 与 camel 双拼写同时给）。
//
// 为什么两种拼写都写而不是选一个：不同客户端读不同拼写（OpenAI 生态读
// snake_case，DSH/pi-ai 的 readListing 两种都试）。只给一种的表现是
// "字段存在但客户端读成 undefined"——比字段缺失更难排查，因为它看起来是通的。
func (e *ModelEntry) applyCapability(c ModelCapability) {
	if c.ContextWindow > 0 {
		e.ContextWindow = c.ContextWindow
		e.ContextWindowC = c.ContextWindow
	}
	if c.MaxOutputTokens > 0 {
		e.MaxOutputTokens = c.MaxOutputTokens
		e.MaxOutputTokensC = c.MaxOutputTokens
	}
	if len(c.Efforts) > 0 {
		e.ReasoningEfforts = c.Efforts
		e.ReasoningEffortsC = c.Efforts
	}
	if len(c.Modalities) > 0 {
		e.Modalities = c.Modalities
	}
}

type modelsList struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}

// HandleV1ModelRetrieve 处理 GET /v1/models/{id}（OpenAI 的 retrieve model 形态）。
//
// 为什么值得单独提供：大量 SDK 在正式流式请求前会先 retrieve 一次模型确认存在
// （OpenAI 官方 SDK 的 models.retrieve），缺了这个端点它们会在第一步就 404，
// 而 `/v1/models` 列表本身是通的——那种"列表里有、单个查不到"的不一致最难解释。
//
// 红线同列表：只能查**抽象模型 id**。真实模型名必须 404（02-abstract §6.2：
// 对外暴露真实模型名等于把池结构念给租户听）。
func (s *Server) HandleV1ModelRetrieve(w http.ResponseWriter, r *http.Request) {
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

	id := strings.TrimPrefix(r.URL.Path, "/v1/models/")
	if id == "" || strings.Contains(id, "/") {
		// 空 id 或更深路径都不是合法资源：404（不引导客户端猜）。
		s.writeV1Error(w, "model_not_found", "模型不存在", requestID)
		return
	}

	cat := s.v1Catalog()
	if cat == nil {
		s.writeV1Error(w, "model_not_found", "模型不存在", requestID)
		return
	}
	ids, err := cat.ListModelIDs(r.Context())
	if err != nil {
		s.log.Warn("v1 model retrieve catalog error", "request_id", requestID, "error", err)
		s.writeV1Error(w, "internal_error", "模型目录不可用", requestID)
		return
	}
	for _, mid := range ids {
		if mid != id {
			continue
		}
		entry := ModelEntry{
			ID:      mid,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "free-router",
		}
		// 单查与列表的能力口径必须同源：两处各写一份聚合逻辑迟早漂移，
		// 而客户端正是靠"列表里有、单查也对得上"建立信任。
		if cs, ok := cat.(ModelCapabilitySource); ok {
			if caps, err := cs.ListModelCapabilities(r.Context()); err == nil {
				if c, has := caps[mid]; has {
					entry.applyCapability(c)
				}
			}
		}
		writeJSON(w, http.StatusOK, entry)
		return
	}
	// 不存在（含真实模型名）：404 model_not_found，文案与列表外一切未知 id 一致——
	// 不区分"是真实模型名"与"根本不存在"，否则等于确认了池结构。
	s.writeV1Error(w, "model_not_found", "模型不存在", requestID)
}
