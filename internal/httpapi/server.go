// Package httpapi 是 L1 层：入站 HTTP 服务器、固定顺序的中间件链与路由注册。
//
// 设计依据：docs/architecture/01-overview.md（L1 中间件 11 步顺序、C1/C4/C6）、
// docs/api/01-inbound.md、docs/api/03-errors.md。
//
// 本包只负责「把请求正确地送进 L2 并把响应编码出去」，
// 不做模型选择、不做上游调用、不碰凭据——那些属于 L2。
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/logging"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
	"github.com/LittleFishStars/FreeRouter/internal/version"
)

// Deps 是服务器运行期需要的依赖。
//
// 为什么用结构体而不是一堆参数：M0 之后每个里程碑都会往这里加一项
// （store、抽象模型解析器、额度管理器……），参数列表会失控。
type Deps struct {
	Config *config.Config
	Logger *slog.Logger
	// Ready 由 main 注入：返回 nil 表示可以接收流量。
	// 启动自举（数据库迁移、sidecar 拉起）任一失败都必须让它返回错误，
	// 否则 readyz 会在半残状态下报 200，把流量引到一个跑不动的进程上。
	Ready func(ctx context.Context) error
	// ShuttingDown 在优雅关闭第一步置位：readyz 立刻变红，把新流量从 LB 摘掉。
	ShuttingDown func() bool
}

// Server 持有路由器与依赖。
type Server struct {
	deps Deps
	cfg  *config.Config
	log  *slog.Logger

	// authChain 是鉴权→限速→配额三中间件（SetAuthChain 注入；
	// 未注入时 Handler() 只挂前四步且不挂 v1 面板）。
	authChain *authChainDeps
	// consoleRoutes 是控制台面路由注册回调（SetConsoleRoutes 注入）。
	consoleRoutes func(r chi.Router)

	// webUI 是控制台前端的静态处理器（SetWebUI 注入 internal/webui.Handler()）。
	//
	// 为什么要注入而不是在 httpapi 里 import webui：httpapi 不该知道「前端产物
	// 有没有被构建」这件事——那是构建形态（go:build webui_dist）的职责，注入点
	// 在装配层。httpapi 只负责把非 API 路径交给它。
	webUI http.Handler
	// webUIBuilt 报告注入的是真实前端产物还是「未构建」占位页。
	// 运维要能一眼分辨「忘了 pnpm build 就重新编译」——那是本项目
	// 最容易出现的部署态错误，而两种情况页面都能打开。
	webUIBuilt bool

	// settler 是 v1 请求终态结算出口（SetSettler 注入）。
	// nil = 未装配：请求照常服务但不写 requests/usage_daily（测试与降级态）。
	settler Settler

	// latencySink 是每请求延迟采样出口（SetLatencySink 装配）。
	// nil = 未装配：延迟样本丢弃（路由 speed 排序退回无样本垫底）。
	latencySink LatencySink
}

// New 构造服务器。deps.Logger 为 nil 时使用默认 logger。
func New(deps Deps) *Server {
	cfg := deps.Config
	if cfg == nil {
		cfg = config.Default()
	}
	log := deps.Logger
	if log == nil {
		log = logging.FromContext(context.Background())
	}
	return &Server{deps: deps, cfg: cfg, log: log}
}

// Handler 返回装配好的 http.Handler。
//
// 中间件顺序是**契约**（docs/architecture/01-overview.md L1）：
//
//	请求ID → 安全头 → 可信代理 → body上限 → 鉴权 → 限速 → 配额
//	→ 抽象模型解析 → 协议解析IR → L2 → 响应编码
//
// 顺序不可交换的原因写在 middleware.go 各函数注释里。
// 鉴权/限速/配额三步由 SetAuthChain 注入（装配层 internal/app 调用）——
// 未注入时 v1 面板路由不挂：半套链子挂出比不挂更危险（有鉴权无限速
// 等于把计数器裸奔给攻击者）。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	r.Use(s.mwRequestID)
	r.Use(s.mwSecurityHeaders)
	r.Use(s.mwTrustedProxy)
	r.Use(s.mwBodyLimit)
	if s.authChain != nil {
		r.Use(s.authChain.keyAuth)
		r.Use(s.authChain.rate)
		r.Use(s.authChain.quota)
	}

	// 运维探针：不进鉴权、不写访问日志噪音。
	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	// 公开信息：版本与能力，供客户端探测（docs/api/01-inbound.md）。
	r.Get("/api/version", s.handleVersion)

	// 初始化向导与控制台鉴权面：由装配层 SetConsoleRoutes 注入
	//（M3 交付：handleAuthLogin/handleSetupStatusV2 等 handler 是
	// 带依赖闭包的方法，装配层才知道 AuthDeps/SetupDeps 怎么组）。
	if s.consoleRoutes != nil {
		s.consoleRoutes(r)
	}

	// v1 协出面：仅在鉴权链就位时挂载（见上注）。
	if s.authChain != nil {
		r.Post("/v1/chat/completions", s.HandleV1Chat)
		r.Post("/v1/messages", s.HandleV1Messages)
		r.Post("/v1/responses", s.HandleV1Responses)
		r.Get("/v1/models", s.HandleV1Models)
	}

	// 兜底路由：注入了前端时，非 API 路径交给 SPA（HashRouter 也受益——
	// 前端产物里的 index.html 必须在任意入口 URL 上可达）；/api/* 与 /v1/*
	// 仍然回 JSON 404，理由见 SetWebUI 注释。
	if s.webUI != nil {
		r.NotFound(s.handleWebUIOrNotFound)
	} else {
		r.NotFound(s.handleNotFound)
	}
	r.MethodNotAllowed(s.handleMethodNotAllowed)

	return r
}

// authChainDeps 是鉴权三中间件（注入后 v1 面板同时激活）。
type authChainDeps struct {
	keyAuth func(http.Handler) http.Handler
	rate    func(http.Handler) http.Handler
	quota   func(http.Handler) http.Handler
}

// SetAuthChain 注入鉴权→限速→配额三中间件（装配层 internal/app 调用）。
// 三件必须一起给：链子的任何一段缺失都是装配错误，宁可全不挂。
func (s *Server) SetAuthChain(keyAuth, rate, quota func(http.Handler) http.Handler) {
	s.authChain = &authChainDeps{keyAuth: keyAuth, rate: rate, quota: quota}
}

// SetConsoleRoutes 注入控制台面路由（登录/登出/setup；M3 交付）。
// fn 收到路由器自行注册——为什么是回调而不是逐条方法：控制台路由
// 会随管理面膨胀（用户/组/审计……），回调让装配层一次组完闭包依赖。
func (s *Server) SetConsoleRoutes(fn func(r chi.Router)) {
	s.consoleRoutes = fn
}

// SetWebUI 注入控制台前端处理器（装配层 internal/app 调用）。
//
// 只在兜底路径上生效（NotFound 分流）；/api/* 与 /v1/* 下的 404 必须是
// JSON——否则调用方拿到 index.html 会误以为端点存在、只是响应坏了。
func (s *Server) SetWebUI(h http.Handler, built bool) {
	s.webUI = h
	s.webUIBuilt = built
}

// handleWebUIOrNotFound 按路径前缀分流：API 面走 JSON 404，其余交给前端。
func (s *Server) handleWebUIOrNotFound(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/v1" || strings.HasPrefix(p, "/v1/") {
		s.handleNotFound(w, r)
		return
	}
	s.webUI.ServeHTTP(w, r)
}

// MiddlewareKeyAuth/Rate/Quota 是三中间件的导出包装——装配层
// （internal/app）用它们拿闭包再经 SetAuthChain 注回本 Server。
// 不直接导出 mwKeyAuth 是为了保持「同包才能挂载」的默认安全边界。
func (s *Server) MiddlewareKeyAuth(authn *auth.KeyAuthenticator) func(http.Handler) http.Handler {
	return s.mwKeyAuth(authn)
}

func (s *Server) MiddlewareRate(l *meter.Limiter, limitsOf RateLimitsSource, tokenCost int64) func(http.Handler) http.Handler {
	return s.mwRate(l, limitsOf, tokenCost)
}

func (s *Server) MiddlewareQuota(debiter QuotaDebiter, modelIDOf ModelIDSource, groupIDsOf GroupIDsSource, tokenBudget int64) func(http.Handler) http.Handler {
	return s.mwQuota(debiter, modelIDOf, groupIDsOf, tokenBudget)
}
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.deps.ShuttingDown != nil && s.deps.ShuttingDown() {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "进程正在关闭，不再接收新请求", nil)
		return
	}
	if s.deps.Ready != nil {
		if err := s.deps.Ready(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", err.Error(), nil)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "freerouter",
		"version": version.Version,
		"commit":  version.Commit,
		"built":   version.BuildTime,
		// ui_built=false 表示二进制里没有前端产物（看到的是占位页）。
		"ui_built": s.webUIBuilt,
	})
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	// M0 阶段恒为「需要初始化」：真正的判定要读库（是否有 admin 账户），
	// 在 M3 引入 store 后替换。这里刻意不返回「已完成」，
	// 避免一个还没接数据库的骨架对外谎报初始化状态。
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required":       true,
		"registration_enabled": s.cfg.Security.RegistrationEnabled,
		"requires_approval":    s.cfg.Security.RegistrationRequiresApproval,
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
}

func (s *Server) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	// 405 复用 invalid_request，把方法名填进 param（docs/api/03-errors.md §4）。
	writeErrorParam(w, http.StatusMethodNotAllowed, "invalid_request", r.Method, "该路径不支持此 HTTP 方法")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
