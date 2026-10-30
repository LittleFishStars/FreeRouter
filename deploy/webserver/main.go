// freerouter-web —— FreeRouter 前后端分离形态的前端边缘服务。
//
// 职责（且仅限这两条，见 docs/ops/02-deploy.md「形态 D：前后端分离」）：
//  1. 静态托管控制台构建产物（SPA；未知路径回落 index.html，深链可达）；
//  2. 把 /api/* 反代到本机回环的后端 API。
//
// 它不是通用反代：除 /api/ 前缀外不转发任何路径；后端只监听回环，
// 外部只能通过本服务触达 API——两个服务合起来对浏览器呈现一个同源站点，
// 因此不需要任何 CORS 改动（Go 后端与 React 前端代码零修改）。
//
// 流式红线：模型流式推理走 SSE，ReverseProxy 必须设 FlushInterval=-1，
// 否则响应在代理层被缓冲，客户端看到的是「转完才吐」。见 hub 透传链路
// （docs/architecture/03-upstream.md）。
//
// 用法：
//
//	freerouter-web [-addr :8081] [-root /opt/freerouter/web/dist] [-upstream http://127.0.0.1:8080]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	flagAddr     = flag.String("addr", ":8081", "监听地址（前端唯一对外入口）")
	flagRoot     = flag.String("root", "/opt/freerouter/web/dist", "控制台构建产物目录（vite 产物）")
	flagUpstream = flag.String("upstream", "http://127.0.0.1:8080", "后端 API 基址（必须回环）")
)

var logger = log.New(os.Stderr, "freerouter-web ", log.LstdFlags|log.LUTC)

func main() {
	flag.Parse()

	st, err := os.Stat(*flagRoot)
	if err != nil || !st.IsDir() {
		logger.Fatalf("前端产物目录不可用 %s: %v（部署未完成或路径配错）", *flagRoot, err)
	}
	up, err := url.Parse(*flagUpstream)
	if err != nil {
		logger.Fatalf("upstream 解析失败: %v", err)
	}
	if host := up.Hostname(); host != "127.0.0.1" && host != "::1" && host != "localhost" {
		logger.Fatalf("upstream 必须是回环地址（当前 %s）：后端绝不直接暴露公网", *flagUpstream)
	}

	proxy := newAPIProxy(up)
	spa := newSPAServer(*flagRoot)

	mux := http.NewServeMux()
	// API 域与后端口径完全一致（internal/httpapi/server.go 的 handleWebUIOrNotFound）：
	//   /api/* —— 控制台/管理 API；/v1/* —— OpenAI(chat/responses/models) 与 Anthropic(messages)。
	// 漏掉 /v1 会让模型调用拿到 SPA 的 index.html（2026-10-30 实测踩坑）。
	mux.Handle("/api/", proxy)
	mux.Handle("/v1", proxy)
	mux.Handle("/v1/", proxy)
	mux.HandleFunc("/healthz", handleHealthz(up))
	mux.Handle("/", spa)

	srv := &http.Server{
		Addr:              *flagAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Printf("listening on %s  root=%s  upstream=%s", *flagAddr, *flagRoot, *flagUpstream)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	logger.Printf("收到停止信号，优雅关闭（在飞请求 drain 10s）")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// newAPIProxy 构造 /api/* 反代。FlushInterval=-1 是流式生命线（见文件头）。
func newAPIProxy(up *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		// Rewrite（而非 Director）：新式 API，SetXForwarded 补全代理头，
		// 后端在 behind_proxy=false 时不会采信，但链路信息保留在头里便于排障。
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(up)
			pr.SetXForwarded()
			// Host 保留原始值：后端按请求 Host 推断 public_url 的路径依赖它。
			pr.Out.Host = pr.In.Host
		},
		// -1 = 每次写后立即 flush，SSE 逐 token 可见。
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Printf("proxy %s %s: %v", r.Method, r.URL.Path, err)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"upstream_unavailable","message":"后端 API 暂不可达"}}`))
		},
	}
}

// spaServer：静态产物 + SPA 回落。 dist 里 hash 文件名不变的内容给一年
// immutable 缓存；index.html（含所有回落命中）no-cache，保证发版后浏览器拿到新入口。
type spaServer struct {
	root string
	fs   http.Handler
}

func newSPAServer(root string) *spaServer {
	return &spaServer{
		root: root,
		fs:   http.FileServerFS(os.DirFS(root)),
	}
}

func (s *spaServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// path.Clean 以 URL 语义锚定前导 /，吃掉越界 ..；再 Join 到 root 下不穿越。
	p := path.Clean("/" + r.URL.Path)
	full := filepath.Join(s.root, filepath.FromSlash(p))

	st, err := os.Stat(full)
	fallback := err != nil || st.IsDir() && r.URL.Path != "/"
	if fallback {
		r.URL.Path = "/"
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(r.URL.Path, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	s.fs.ServeHTTP(w, r)
}

// handleHealthz：本服务存活 + 后端可达性（供 systemd 与人肉排障）。
func handleHealthz(up *url.URL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamOK := false
		c, err := net.DialTimeout("tcp", up.Host, time.Second)
		if err == nil {
			_ = c.Close()
			upstreamOK = true
		}
		w.Header().Set("Content-Type", "application/json")
		if !upstreamOK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"upstream": map[string]bool{"reachable": upstreamOK},
			"time":     time.Now().UTC().Format(time.RFC3339),
		})
	}
}
