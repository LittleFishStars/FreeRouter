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

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/logging"
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
// 顺序不可交换的原因写在 middleware.go 各函数注释里。M0 只落地前四步，
// 其余步骤由 M3（鉴权）、M4（限速/配额）、M5（协议解析）、M6（抽象模型）依次插入——
// **插入位置由这条链固定，不由实现者决定**。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	r.Use(s.mwRequestID)
	r.Use(s.mwSecurityHeaders)
	r.Use(s.mwTrustedProxy)
	r.Use(s.mwBodyLimit)

	// 运维探针：不进鉴权、不写访问日志噪音。
	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	// 公开信息：版本与能力，供客户端探测（docs/api/01-inbound.md）。
	r.Get("/api/version", s.handleVersion)

	// 初始化向导：尚未创建管理员账户时唯一对外可用的写入口。
	// M0 只暴露状态查询，创建逻辑在 M3 落地。
	r.Get("/api/setup/status", s.handleSetupStatus)

	r.NotFound(s.handleNotFound)
	r.MethodNotAllowed(s.handleMethodNotAllowed)

	return r
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
