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
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/app"
	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/logging"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/version"
)

func main() {
	// 进程级 umask：服务创建的所有文件（库、-wal/-shm、备份、主密钥）默认仅属主
	// 可读写。为什么在 main 而不在 store.Open：umask 是进程级状态，写进包函数
	// 会让「谁改了 umask」变成隐式副作用；放在进程入口，一眼能看到全局约定。
	// 为什么必须做：SQLite 按 umask 创建文件，而 checkpoint 会删掉并重建 -wal，
	// 只在打开时 chmod 一次拦不住重建——安全基线 C7（同机其他用户可读库）
	// 就靠这一行长期成立。
	syscall.Umask(0o077)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	configPath  string
	printConfig bool
	checkConfig bool
	verifyStore bool
	showVersion bool
}

func parseFlags(args []string, stderr io.Writer) (*options, error) {
	opts := &options{}
	fs := flag.NewFlagSet("freerouter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.configPath, "config", "", "配置文件路径（YAML）；缺省只用默认值 + FR_ 环境变量")
	fs.BoolVar(&opts.printConfig, "print-config", false, "打印生效配置（含环境变量覆盖）后退出")
	fs.BoolVar(&opts.checkConfig, "check-config", false, "只校验配置与端口可用性，不启动服务")
	fs.BoolVar(&opts.verifyStore, "verify-store", false, "只做存储自检（库可开/迁移齐/关键表全/主密钥可用）后退出")
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
	// 首次启动会自动创建管理员并随机口令（app.bootstrapAdmin）：口令只出现在
	// 本机终端 banner 与数据目录的 initial-password.txt。监听非回环时必须提醒
	// 运维把首启口令当作一次性凭据保护——它尚未被改成个人口令。
	if !cfg.IsLoopbackListen() {
		log.Warn("监听地址非回环：首启管理员口令只出现在本机终端与数据目录口令文件中，"+
			"请确认该端口仅对可信网络开放，并在首次登录后立即改密",
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

	// 存储自检（M10 恢复演练验收项）：库可开、迁移齐、关键表全、主密钥可用。
	// 为什么单独一个开关而不是启动时总做：启动路径要快，而运维需要在「不提供服务」
	// 的前提下确认备份恢复出来的库真能用（restore.sh 也提示跑这一步）。
	if opts.verifyStore {
		if err := verifyStore(opts.configPath); err != nil {
			fmt.Fprintf(stderr, "存储校验失败：%v\n", err)
			return 1
		}
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
		// 必须调 cancel：WithTimeout 创建的 timer 要显式释放（Go 用法约定，
		// go vet 的 lostcancel 与 gosec G118 都报这个）。之前写成 `_ = stopCancel`，
		// 功能上 Stop 会阻塞到完成/超时，但 timer 要等到超时才回收。
		defer stopCancel()
		application.Stop(stopCtx)
		if err := application.Close(); err != nil {
			log.Warn("存储关闭出错", "err", err)
		}
	}()

	// 步骤 3（真判定）：初始化向导。已建管理员账户且监听非回环→提醒；
	// 未初始化且非回环→强警告（M0 只有警告，deny 语义在 config.Validate）。
	if !cfg.IsLoopbackListen() {
		log.Warn("监听地址非回环：首启口令是一串随机值，请确认访问者都是可信运维",
			"listen", cfg.Server.Listen)
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

	// 首启横幅：一次性口令只在这里与 initial-password.txt 出现，不进日志。
	// 必须在 Serve 之前打印（用户拿到口令时服务已可访问）；浏览器打开
	// 放在延迟启动里，避免在监听完成前请求到一个拒绝连接。
	if br := application.Bootstrap(); br != nil {
		br.ListenURL = consoleURL(cfg, ln)
		fmt.Fprint(stdout, application.BootstrapBanner())
		if cfg.Server.AutoOpenBrowser && cfg.IsLoopbackListen() {
			go openBrowser(br.ListenURL)
		}
	}

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

// verifyStore 做一次只读自检：库可开、迁移齐、关键表全、主密钥可用。
//
// 三项都通过才算 PASS——Open 本身会跑迁移并加载主密钥，所以「能开」已经
// 覆盖了「迁移可执行 + 主密钥可读」；Verify 再补上「关键表真的在」。
// 失败时报具体缺哪张表，而不是笼统的“库坏了”：恢复演练里运维要知道
// 是备份不完整还是主密钥不匹配。
func verifyStore(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("读配置: %w", err)
	}
	// 库不存在时直接报错：store.Open 会自动建库并跑迁移，若这里也“通过”，
	// 恢复演练就会把「备份没恢复成功、现场是个新空库」误判成通过。
	if _, err := os.Stat(cfg.Store.Path); err != nil {
		return fmt.Errorf("库文件不存在 %s（本开关用于校验已有/恢复后的库；首次启动会建库）: %w",
			cfg.Store.Path, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := store.Open(ctx, store.Options{
		Path:          cfg.Store.Path,
		MasterKeyFile: cfg.Store.MasterKeyFile,
		BusyTimeout:   cfg.Store.BusyTimeout.Std(),
		ReadConns:     2,
	})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	rep, err := st.Verify(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("存储校验通过：迁移 %d 条、关键表 %d 张、主密钥可用（%s）\n",
		rep.Migrations, len(rep.Tables), cfg.Store.Path)
	return nil
}

// consoleURL 推导控制台入口地址（首启 banner 与自动弹浏览器用）。
//
// public_url 优先（反代/域名场景）；否则从实际监听的地址取端口，
// 通配监听（0.0.0.0 / ::）换成本机回环——浏览器打开 0.0.0.0 不可用。
func consoleURL(cfg *config.Config, ln net.Listener) string {
	scheme := "http"
	if strings.EqualFold(cfg.TLS.Mode, "files") {
		scheme = "https"
	}
	if cfg.Server.PublicURL != "" {
		return strings.TrimRight(cfg.Server.PublicURL, "/") + "/"
	}
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return scheme + "://" + ln.Addr().String() + "/"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/"
}

// openBrowser 在首启时打开控制台页面（仅回环监听 + 配置开启时调用）。
//
// 失败静默：服务器/容器里没有 xdg-open 是常态，终端 banner 已经给了 URL，
// 一个“打不开浏览器”的错误不该在启动日志里制造噪音。
func openBrowser(url string) {
	time.Sleep(600 * time.Millisecond) // 让 Serve 先就位，避免连接被拒
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}
