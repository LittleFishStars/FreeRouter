// Package app 是进程装配层：把 store、sidecar、meter、auth、upstream、
// httpapi 六个内聚包组装成一个可运行的 FreeRouter 进程。
//
// 为什么需要这一层：各功能包都刻意只依赖窄接口（KeyAuthStore、
// QuotaStore、CatalogSource……），谁也不 import 谁——依赖方向干净了，
// 但总要有一个地方把这些接口「钉」到真实实现上。这里就是那个地方，
// 且是唯一允许同时 import store/meter/auth/upstream/httpapi 的包
// （docs/architecture/01-overview.md 的装配边界，06-runtime.md §9 的接入点表）。
//
// 装配失败的三种处理（06-runtime.md 启动序列）：
//  1. 库打不开/迁移失败 → 进程不启动（存储是一切的前提）；
//  2. sidecar 拉不起 → 警告但不阻塞（上游自举失败不挡启动）；
//  3. relay 无上游行 → 目录为空、v1 面板对一切请求回 no_candidate（可运行）。
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// App 是组装完成后的进程级对象。
type App struct {
	cfg     *config.Config
	log     *slog.Logger
	store   *store.Store
	sidecar *dshprovider.Client

	// 鉴权/限速/配额三中间件的运行时依赖（buildServer 构造）。
	// 为什么放 App 上：优雅关闭时要先停限速器的排队（背景任务归 bgCancel），
	// 测试探针也要能拿到这三个。
	keyAuthn *auth.KeyAuthenticator
	limiter  *meter.Limiter
	debiter  httpapi.QuotaDebiter

	// 后台任务（模型目录刷新等）的取消函数，Stop 时统一收回。
	bgCancel context.CancelFunc

	// HTTP 服务器（装配好全部中间件与路由的）。
	server *httpapi.Server
}

// Options 控制装配行为（测试用得上的注入点）。
type Options struct {
	// Config 是已 Load+Validate 过的配置。nil 时用 Default()。
	Config *config.Config
	// Logger 为 nil 时用 slog.Default()。
	Logger *slog.Logger
	// SidecarStarter 覆盖 sidecar 拉起（测试注入 fake；nil 用 dshprovider.Start）。
	SidecarStarter func(ctx context.Context, opts dshprovider.Options) (*dshprovider.Client, error)
}

// Setup 按启动序列装配整个进程（06-runtime.md：配置→迁移→初始化向导判定
// →上游自举→拉 sidecar→HTTP）。返回的 App 调 Serve 前不会监听端口。
//
// sidecar 失败的处理：记 Warn 后继续——C1 口径的「上游自举失败不阻塞启动」。
// 真正推理时 sidecar 不可用会在调用点返回错误，v1 面板表现为换候选/502。
func Setup(ctx context.Context, opts Options) (*App, error) {
	cfg := opts.Config
	if cfg == nil {
		cfg = config.Default()
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	// 第 2 步：存储（含迁移）。失败即终：没有库的进程没有意义。
	st, err := store.Open(ctx, store.Options{
		Path:                 cfg.Store.Path,
		MasterKeyFile:        cfg.Store.MasterKeyFile,
		BusyTimeout:          cfg.Store.BusyTimeout.Std(),
		BackupDir:            cfg.Store.BackupDir,
		AuditRetainDays:      cfg.Store.AuditRetainDays,
		RequestLogRetainDays: cfg.Store.RequestLogRetainDays,
	})
	if err != nil {
		return nil, fmt.Errorf("app: 打开存储失败: %w", err)
	}

	a := &App{cfg: cfg, log: log, store: st}

	// 第 5 步：sidecar。失败降级（不阻塞）。
	if cfg.Sidecar.Enabled {
		starter := opts.SidecarStarter
		if starter == nil {
			starter = dshprovider.Start
		}
		sc, err := starter(ctx, a.sidecarOptions())
		if err != nil {
			// 上游自举失败不阻塞启动（03-upstream.md §3.2 的同一条纪律）。
			log.Warn("app: sidecar 拉起失败，relay 类上游暂不可用（不阻塞启动）", "error", err)
		} else {
			a.sidecar = sc
		}
	}

	// 第 4 步：上游自举 + 模型目录刷新（后台周期任务，06-runtime §2.1）。
	bgCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.bgCancel = cancel
	a.startModelRefresh(bgCtx)

	// 第 6 步：HTTP（装配中间件链 + 路由 + v1 面板注入）。
	a.server = a.buildServer()
	return a, nil
}

// sidecarOptions 把 config 翻译成 dshprovider.Options。
// Timeout 用文档表（04-sidecar.md §5.3）的默认值；config 只给了
// startup_timeout 一个键，方法级超时留给 client 内建表。
func (a *App) sidecarOptions() dshprovider.Options {
	return dshprovider.Options{
		NodePath:       a.cfg.Sidecar.NodePath,
		Entry:          a.cfg.Sidecar.Entry,
		HubPath:        a.cfg.Sidecar.HubPath,
		StartupTimeout: a.cfg.Sidecar.StartupTimeout.Std(),
		Logger:         a.log,
	}
}

// startModelRefresh 启动模型目录后台刷新（task-7 的 ModelRefresher）。
// sidecar 不可用时不启动——刷新失败本来就不阻塞，但没有 sidecar 时
// 每 10 分钟空转一轮只是浪费日志。
func (a *App) startModelRefresh(ctx context.Context) {
	if a.sidecar == nil || !a.cfg.Upstream.Relay.Enabled {
		return
	}
	// 装配点：CatalogSource 由 dshprovider.Client 天然满足（task-7 设计），
	// 存储侧的 upsert/missing 标记经窄接口注入。
	// 装配点：CatalogSource 由 dshprovider.Client 天然满足（task-7 设计）；
	// refresh.go 直接要 *store.Store（UpsertRealModel/MarkMissingRealModels 都在
	// Store 聚合上）——这里直传，不另包一层。
	refresher := upstream.NewModelRefresher(
		a.store, a.sidecar,
		a.cfg.Upstream.Relay.ModelRefreshInterval.Std(), a.log)
	refresher.Start(ctx)
}

// Handler 返回装配好的 http.Handler（供 main 塞进 http.Server）。

// Serve 装配 HTTP handler（供 main 塞进 http.Server）。
func (a *App) Handler() http.Handler { return a.server.Handler() }

// Serve 由 main 调用；保留接口位（实际监听逻辑在 cmd/freerouter）。
func (a *App) Server() *httpapi.Server { return a.server }

// Stop 收回后台任务并关 sidecar（优雅关闭的第 4 步）。
// 存储的 WAL checkpoint 在 main 的最后一步做——这里不抢。
func (a *App) Stop(ctx context.Context) {
	if a.bgCancel != nil {
		a.bgCancel()
	}
	if a.sidecar != nil {
		if err := a.sidecar.Close(ctx); err != nil {
			a.log.Warn("app: 关闭 sidecar 出错", "error", err)
		}
	}
}

// Close 关闭存储（进程退出最后一步，WAL checkpoint 由 store.Close 内做）。
func (a *App) Close() error {
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}
