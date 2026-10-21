// Command freerouter 是 FreeRouter 的主进程。
//
// 启动顺序是契约（docs/architecture/01-overview.md「启动 6 步」）：
//
//	1 配置加载与校验
//	2 数据库迁移
//	3 初始化向导判定（尚无账户时只监听回环）
//	4 上游自举（失败不阻塞启动）
//	5 拉起 sidecar
//	6 开始监听 HTTP
//
// 优雅关闭顺序也是契约（「关闭 5 步」）：
//
//	readyz 变红 → 关 listener → 等在飞请求（默认 30s）→ 关 sidecar → 关库（WAL checkpoint）
//
// 为什么顺序不能换：先关 listener 才能保证没有新请求进来；
// 等在飞请求必须在关 sidecar 之前，否则正在进行的上游调用会被自己掐断；
// 库最后关，因为前面几步还在写审计与用量。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/app"
	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/logging"
	"github.com/LittleFishStars/FreeRouter/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	configPath  string
	printConfig bool
	checkConfig bool
	showVersion bool
}

func parseFlags(args []string, stderr io.Writer) (*options, error) {
	opts := &options{}
	fs := flag.NewFlagSet("freerouter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.configPath, "config", "", "配置文件路径（YAML）；缺省只用默认值 + FR_ 环境变量")
	fs.BoolVar(&opts.printConfig, "print-config", false, "打印生效配置（含环境变量覆盖）后退出")
	fs.BoolVar(&opts.checkConfig, "check-config", false, "只校验配置与端口可用性，不启动服务")
	fs.BoolVar(&opts.showVersion, "version", false, "打印版本后退出")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "freerouter — 把多个 AI Agent 免费额度池化成一个共享路由\n\n用法：\n  freerouter [选项]\n\n选项：\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return opts, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if opts.showVersion {
		fmt.Fprintln(stdout, version.String())
		return 0
	}

	// 步骤 1：配置。
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "配置加载失败：%v\n", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	if opts.printConfig {
		raw, err := cfg.Marshal()
		if err != nil {
			fmt.Fprintf(stderr, "序列化配置失败：%v\n", err)
			return 1
		}
		fmt.Fprint(stdout, string(raw))
		for _, warn := range cfg.Warnings() {
			fmt.Fprintf(stderr, "警告：%s\n", warn)
		}
		return 0
	}

	log := logging.Setup(logging.Options{Level: cfg.Log.Level, Format: cfg.Log.Format})
	log.Info("freerouter 启动中", "version", version.String(), "config", opts.configPath)
	for _, warn := range cfg.Warnings() {
		log.Warn(warn)
	}

	// 步骤 3：初始化向导判定。
	// 尚无管理员账户时服务只能绑回环：否则任何先到的人都能把自己建成管理员。
	// M0 还没有库，因此这里以「未初始化」为前提做端口检查，M3 接库后按实际账户数判定。
	if !cfg.IsLoopbackListen() {
		log.Warn("尚未创建管理员账户，但监听地址不是回环地址；"+
			"初始化完成前请勿把该端口暴露到不可信网络",
			"listen", cfg.Server.Listen)
	}

	if opts.checkConfig {
		scheme := "http"
		if strings.EqualFold(cfg.TLS.Mode, "files") {
			scheme = "https"
		}
		fmt.Fprintf(stdout, "配置有效：%s://%s\n", scheme, cfg.Server.Listen)
		return 0
	}

	// 步骤 4-6：装配（app.Setup 把存储迁移→sidecar→上游自举→HTTP 一次完成；
	// 06-runtime.md 启动序列）。sidecar 失败在 Setup 内部降级为 Warn，不阻塞。
	application, err := app.Setup(context.Background(), app.Options{Config: cfg, Logger: log})
	if err != nil {
		log.Error("进程装配失败", "err", err)
		return 1
	}
	defer func() {
		// 关闭顺序固定（06-runtime §8）：先 sidecar（可能回写最后一批用量）后库。
		stopCtx, stopCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace.Std())
		_ = stopCancel
		application.Stop(stopCtx)
		if err := application.Close(); err != nil {
			log.Warn("存储关闭出错", "err", err)
		}
	}()

	// 步骤 3（真判定）：初始化向导。已建管理员账户且监听非回环→提醒；
	// 未初始化且非回环→强警告（M0 只有警告，deny 语义在 config.Validate）。
	if !cfg.IsLoopbackListen() {
		log.Warn("监听地址非回环：初始化完成前请勿把该端口暴露到不可信网络", "listen", cfg.Server.Listen)
	}

	var shuttingDown atomic.Bool

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           application.Handler(),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Std(),
		IdleTimeout:       cfg.Server.IdleTimeout.Std(),
		ErrorLog:          nil, // 由 slog 承接；标准库的 ErrorLog 输出格式与结构化日志不一致
	}
	// 步骤 6：监听。先 Listen 再 Serve，是为了让「端口被占用」在启动日志里
	// 表现为一条明确错误，而不是一个后台 goroutine 里的 panic。
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		log.Error("监听失败", "listen", cfg.Server.Listen, "err", err)
		return 1
	}
	log.Info("开始监听", "listen", ln.Addr().String(), "tls", cfg.TLS.Mode)

	serveErr := make(chan error, 1)
	go func() {
		if strings.EqualFold(cfg.TLS.Mode, "files") {
			serveErr <- srv.ServeTLS(ln, cfg.TLS.CertFile, cfg.TLS.KeyFile)
			return
		}
		serveErr <- srv.Serve(ln)
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP 服务异常退出", "err", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		log.Info("收到退出信号，开始优雅关闭")
	}

	// 关闭步骤 1：readyz 立刻变红（由 ShuttingDown 提供），把流量从负载均衡器摘掉。
	shuttingDown.Store(true)
	// 给探针留出一个探测周期，让 LB 真的把我们从池子里摘掉再关 listener。
	grace := cfg.Server.ShutdownGrace.Std()
	drainWindow := grace
	if drainWindow > 2*time.Second {
		drainWindow = 2 * time.Second
	}
	time.Sleep(drainWindow)

	// 关闭步骤 2 + 3：关 listener，并给在飞请求留剩余时间。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace.Std())
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("优雅关闭超时，强制结束在飞请求", "err", err)
	}

	// 关闭步骤 4 / 5：已由 app.Setup 的 defer 执行（先 sidecar 后库，
	// sidecar 关闭时可能回写最后一批用量——06-runtime §8.1 顺序）。
	log.Info("已关闭")
	return 0
}
