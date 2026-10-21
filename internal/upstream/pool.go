package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/logging"
)

// errNoHost：base_url 缺少 scheme 或 host（transport 缓存键无法建立）。
var errNoHost = errors.New("base_url 缺少 scheme 或 host")

// 本文件是 03-upstream.md §5 连接池 + §7.1 健康状态的 M1 落地。
//
// 这张参数表只管 vendor 与 selfhosted——relay 不发 HTTP，它的连接池在 sidecar 进程内
// 由 provider adapter 自己管，Go 侧对它只有一条 IPC 管道（§5.1 末段）。

// PoolConfig 是连接池的参数。零值字段回落 DefaultPoolConfig。
type PoolConfig struct {
	// ServerIdleTimeout 是 server.idle_timeout（config.go 默认 120s）。
	// 本池的 IdleConnTimeout 必须不大于它——任务红线「idle_timeout ≤ server.idle_timeout」：
	// 若连接池保留空闲连接的时间超过 server 侧，server 的 keep-alive 淘汰会把
	// 还挂在池里的连接关掉，下一次复用就变成偶发 http=000。
	ServerIdleTimeout time.Duration
	// MaxConnsPerUpstream 是单上游并发上限（信号量粒度）。文档 §5.1 的
	// MaxConnsPerHost=128 在 host 级兜底；这里再按「上游实体」加一层，
	// 给「单个坏上游吃光 fd」设上限（待定项 §5.3 认可 host 级兜底 + 实测后收紧）。
	MaxConnsPerUpstream int
	// ConnectTimeout 对应 §5.1 DialContext 5s：公网正常 <200ms，5s 足够区分「慢」与「死」。
	ConnectTimeout time.Duration
	// ResponseHeaderTimeout 对应 §5.1 60s：即 TTFT 上限（§8）。
	ResponseHeaderTimeout time.Duration
	// TLSHandshakeTimeout 对应 §5.1 10s：公网 vendor 的握手可能慢。
	TLSHandshakeTimeout time.Duration
}

// DefaultPoolConfig 返回 §5.1 参数表的默认值。
//
// 为什么 ServerIdleTimeout 默认取 120s：internal/config 的 server.idle_timeout 默认
// 就是 120s（config.go:165）；装配层接真实配置后传入即可覆盖。
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		ServerIdleTimeout:     120 * time.Second,
		MaxConnsPerUpstream:   16,
		ConnectTimeout:        5 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	}
}

// effectiveIdleTimeout 计算 IdleConnTimeout（§5.2：客户端先淘汰连接）。
//
// 为什么是 90s 而不是贴近 120s：对端（CDN/反代/网关）淘汰 keep-alive 的实测区间
// 多在 60–120s 且各家不同，90s 落在多数实现的保守侧，再留调度抖动余量；
// 复用一条已被对端 FIN 的连接的成本（用户可见失败）远高于重建连接。
// 任务红线再压一道：若 server.idle_timeout 配得更小，跟着更小（min 语义）。
func (c PoolConfig) effectiveIdleTimeout() time.Duration {
	const defaultIdle = 90 * time.Second
	if c.ServerIdleTimeout <= 0 {
		return defaultIdle
	}
	if defaultIdle < c.ServerIdleTimeout {
		return defaultIdle
	}
	return c.ServerIdleTimeout
}

// Pool 按 (kind, host) 复用 *http.Transport（§5.3 选定方案）。
//
// 为什么不按凭据/按 upstream id 拆 Transport：按凭据拆会让连接数随贡献者数量线性
// 增长、每个新凭据都要冷启动 TLS；按 upstream id 拆会让同一 host 的多账号无法
// 共享 TLS 会话。(kind, host) 复用效率最高；「一个凭据的限流连坐同 host 其它凭据」
// 不成立——限流是上游按账号判定的，与连接复用无关。
type Pool struct {
	mu         sync.Mutex
	cfg        PoolConfig
	transports map[string]*http.Transport // key: kind|scheme|host
	limits     map[int64]chan struct{}    // 单上游并发信号量，key: upstream id
	healths    map[int64]*Health
}

func NewPool(cfg PoolConfig) *Pool {
	d := DefaultPoolConfig()
	if cfg.ServerIdleTimeout > 0 {
		d.ServerIdleTimeout = cfg.ServerIdleTimeout
	}
	if cfg.MaxConnsPerUpstream > 0 {
		d.MaxConnsPerUpstream = cfg.MaxConnsPerUpstream
	}
	if cfg.ConnectTimeout > 0 {
		d.ConnectTimeout = cfg.ConnectTimeout
	}
	if cfg.ResponseHeaderTimeout > 0 {
		d.ResponseHeaderTimeout = cfg.ResponseHeaderTimeout
	}
	if cfg.TLSHandshakeTimeout > 0 {
		d.TLSHandshakeTimeout = cfg.TLSHandshakeTimeout
	}
	return &Pool{
		cfg:        d,
		transports: make(map[string]*http.Transport),
		limits:     make(map[int64]chan struct{}),
		healths:    make(map[int64]*Health),
	}
}

// Transport 返回 (kind, host) 对应的复用 Transport。
//
// 为什么 relay 不允许出现在这里：它不走 Go 的 Transport（§5.1 末段），传进来
// 只会让人误以为 relay 也掷进这 128 个 fd 里。
func (p *Pool) Transport(kind Kind, rawURL string) (*http.Transport, error) {
	if kind == KindRelay {
		return nil, &Error{Class: ErrBadRequest, Message: "relay 不使用 Go 侧 HTTP 连接池（adapter 自管）"}
	}
	u, err := parseHostOnly(rawURL)
	if err != nil {
		return nil, &Error{Class: ErrBadRequest, Message: "base_url 不可解析: " + err.Error()}
	}
	key := string(kind) + "|" + u
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.transports[key]; ok {
		return t, nil
	}
	cfg := p.cfg
	t := &http.Transport{
		// §5.1 参数表：覆盖众多 vendor/selfhosted 端点的并发空闲连接。
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   64, // 默认值 2 会让同一 host 的连接反复建拆，放大半开连接问题
		MaxConnsPerHost:       128,
		IdleConnTimeout:       cfg.effectiveIdleTimeout(),
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true, // 上游支持则用；GOAWAY 按重试表兜底
		DisableCompression:    true, // 流式响应被 gzip 会让 Flush 延迟到解压缓冲满，破坏 SSE 实时性
		DialContext: (&net.Dialer{
			Timeout:   cfg.ConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	p.transports[key] = t
	return t, nil
}

// parseHostOnly 抽出 transport 缓存键（scheme|host）。
func parseHostOnly(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errNoHost
	}
	return u.Scheme + "|" + u.Host, nil
}

// Acquire 占用该上游的一个并发名额；返回的 release 必须调用（通常 defer）。
//
// ctx 取消时返回 false——连接数上限是硬限，排队等名额不该比请求本身活得久。
// 为什么按「上游实体」而不是 host：熔断与健康标记也挂在上游实体上（§7.1 粒度），
// 两个口径一致才能把「一个坏上游吃光 fd」和「一个坏上游吃光重试预算」一起治。
func (p *Pool) Acquire(ctx context.Context, upstreamID int64) (release func(), ok bool) {
	p.mu.Lock()
	sem, exists := p.limits[upstreamID]
	if !exists {
		sem = make(chan struct{}, p.cfg.MaxConnsPerUpstream)
		p.limits[upstreamID] = sem
	}
	p.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	case <-ctx.Done():
		return func() {}, false
	}
}

// Health 返回该上游实体的健康计数器（不存在则创建）。
// 粒度挂 upstreams.id（§7.1）：relay 整类一起熔断（共享同一个 sidecar 与账号池）、
// vendor 每凭据一实体、selfhosted 每地址一实体。
func (p *Pool) Health(upstreamID int64) *Health {
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok := p.healths[upstreamID]
	if !ok {
		h = NewHealth(defaultFailThreshold)
		p.healths[upstreamID] = h
	}
	return h
}

// CloseIdleConnections 关掉全部池内连接。优雅关闭时调用（06-runtime drain 的上游侧）。
func (p *Pool) CloseIdleConnections() {
	p.mu.Lock()
	ts := make([]*http.Transport, 0, len(p.transports))
	for _, t := range p.transports {
		ts = append(ts, t)
	}
	p.mu.Unlock()
	for _, t := range ts {
		t.CloseIdleConnections()
	}
}

// —— §7.1 健康状态（M1：计数驱动的状态机简化版） ——

// HealthState 是单上游实体的健康状态。
type HealthState string

const (
	HealthHealthy  HealthState = "healthy"  // 默认，排序正常
	HealthDegraded HealthState = "degraded" // 仍参与候选但降权（连续 5xx/429）
	HealthCooling  HealthState = "cooling"  // 冷却期不参与候选展开
)

// defaultFailThreshold：连续失败进入冷却/降级的阈值。
// §6.2 给 ErrUpstream5xx 的冷却阈值是「连续 3 次」，沿用为通用阈值。
const defaultFailThreshold = 3

// Health 是单上游实体的连续失败计数与状态。
//
// 为什么用「连续」而不是窗口累计：上游抖动在窗口累计口径下会被多次叠加成永久熔断；
// 连续口径意味着任何一次成功都复位——这与 §7.1「任一次成功即复位到 healthy」一致。
type Health struct {
	mu               sync.Mutex
	state            HealthState
	consecutiveFails int
	threshold        int
}

func NewHealth(threshold int) *Health {
	if threshold <= 0 {
		threshold = defaultFailThreshold
	}
	return &Health{state: HealthHealthy, threshold: threshold}
}

// RecordFailure 记一次失败并推进状态机。
//
// 状态转换（§7.1）：连接类失败（conn_reset/connect/tls/timeout）达到阈值 → cooling
// （不参与候选，避免把失败打到用户脸上）；5xx/429 达到阈值 → degraded（降权但保留
// ——候选池可能本来就只有一两个来源，摘除等于 503）。
// 429/auth/quota 类**单次**不动状态：它们的根因在凭据而不是连接，处置走重试表
// （换候选/标凭据），健康计数只在「连续」时才说话。
func (h *Health) RecordFailure(class ErrClass) {
	if h == nil || class == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consecutiveFails++
	if h.consecutiveFails < h.threshold {
		return
	}
	switch class {
	case ErrUpstream5xx, ErrRateLimited:
		h.state = HealthDegraded
	default:
		// 连接类与其余（protocol 等持续失败也按连接类处置：上游已不可用）。
		h.state = HealthCooling
	}
}

// RecordSuccess 记一次成功：任一次成功即复位 healthy（§7.1）。
// 为什么复位不经过 degraded：degraded 的语义是「还在但优先用别人」，一次成功
// 说明上游已恢复，保留 degraded 只会让它被无谓压后。
func (h *Health) RecordSuccess() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = HealthHealthy
	h.consecutiveFails = 0
}

// State 返回当前状态快照。
func (h *Health) State() HealthState {
	if h == nil {
		return HealthHealthy
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// Available 报告该上游当前是否参与候选展开（cooling = false，degraded = true）。
// degraded 的降权发生在策略引擎的排序链里，不是这里的职责。
func (h *Health) Available() bool {
	return h.State() != HealthCooling
}

// ConsecutiveFails 返回连续失败计数快照（测试与 /readyz 明细用）。
func (h *Health) ConsecutiveFails() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.consecutiveFails
}

// —— 结构化日志辅助（§6.4：每次重试记 upstream_id/credential_id/attempt/class） ——

// LogUpstreamFailure 把一次上游失败记进 ctx 的请求级 logger。
//
// 为什么是包级函数而不是方法：调用方（call 模块、探测循环）手里有 ctx 没有 Pool；
// logging.FromContext 保证无 logger 时退回全局，绝不 panic。字段名与 requests
// 落库列对齐（upstream_id/credential_id/attempt/class/http_status），日志与 DB
// 能用同一套 grep。
func LogUpstreamFailure(ctx context.Context, t Target, attempt int, class ErrClass, status int, elapsed time.Duration) {
	logging.FromContext(ctx).Warn("上游尝试失败",
		"upstream_id", t.UpstreamID,
		"kind", string(t.Kind),
		"credential_id", t.CredentialID,
		"attempt", attempt,
		"class", string(class),
		// http=000：连接类失败无状态码，显式写 000 而不是省略字段——
		// 省略会让「这条日志有没有状态码」变成一个需要额外判断的问题。
		"http_status", statusOrZero(status),
		"elapsed_ms", elapsed.Milliseconds(),
		"request_id", requestIDFrom(ctx, t),
	)
}

// LogUpstreamSuccess 对应成功侧：请求 ID、上游、耗时、状态码（任务第 5 点）。
func LogUpstreamSuccess(ctx context.Context, t Target, status int, elapsed time.Duration, retryCount int) {
	logging.FromContext(ctx).Info("上游尝试成功",
		"upstream_id", t.UpstreamID,
		"kind", string(t.Kind),
		"credential_id", t.CredentialID,
		"http_status", statusOrZero(status),
		"elapsed_ms", elapsed.Milliseconds(),
		"retry_count", retryCount,
		"request_id", requestIDFrom(ctx, t),
	)
}

func statusOrZero(status int) int {
	if status < 0 {
		return 0
	}
	return status
}

// requestIDFrom 取 ctx 里请求级 logger 不可得时的兜底——M1 阶段请求 ID 由
// httpapi 写进 logger 属性（logging.WithLogger），上游层只透传，不自行编造。
func requestIDFrom(ctx context.Context, t Target) string {
	// 预留：ctx 里没有独立 request_id 值时返回空串，slog 会省略该属性。
	return ""
}
