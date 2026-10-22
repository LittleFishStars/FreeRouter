// Package dshprovider 是 DSH providers-hub 的 Node sidecar 的 **Go 侧客户端**。
//
// 为什么它必须是一个独立包，而不是塞进 upstream/relay：
// 本包只负责「怎么跟一个子进程说话」（帧格式、id、超时、进程死亡），
// 而 relay 上游负责「用说话的结果干什么」（选择候选、重试、计量）。
// 混在一起后，契约测试就得连路由逻辑一起搭台，故障定位也会分不清是通道坏了还是上游坏了。
//
// 三条与本包直接相关的红线（docs/architecture/04-sidecar.md 第 4 节）：
//
//	① 只读凭据：本包**不提供** credentials.set / unset / refresh。
//	   凭据续期只能由 DSH 完成，我们只读最新值。Reflectively 也查不到这些方法，
//	   contract_test.go 里有一条用例把这条规则做成可执行的断言。
//	② 不碰 state.json：本包没有任何读写 DSH 运行状态的方法。
//	③ 不经本机 HTTP 网关：本包只讲 stdio + NDJSON，不打开任何监听端口。
//
// 关于「不可用」的 API 形状（这是本包最重要的设计约束）：
// relay 是主供给，sidecar 只影响**贡献/登录**这条支线（04-sidecar.md 5.1）。
// 所以本包把所有失败都表达成「贡献功能不可用」，并且：
//   - Client 的每个方法都对 **nil 接收者安全**，返回结构化错误而不是 panic；
//   - Start 在 sidecar 起不来时**仍然返回一个可用的 Client**（同时带回 error 供打横幅）；
//   - Status() 给出可直接展示的中文原因，供 /healthz 明细与横幅使用。
//
// 转发链路**不调用本包**，因此 sidecar 崩溃在设计上无法影响转发。
package dshprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 默认值与上限。取值依据 docs/architecture/04-sidecar.md：
//   - 启动 health 超时 10s（5.1：Node 冷启动通常 < 3s，10s 足够区分「慢」与「卡住」）；
//   - 单次调用默认 15s（5.3 的超时矩阵里最长的那一档）；
//   - 连续 3 次协议违规视为 sidecar 失能（5.2），故一次调用内最多容忍 3 个错位帧。
const (
	defaultNodePath       = "node"
	defaultStartupTimeout = 10 * time.Second
	defaultCallTimeout    = 15 * time.Second
	defaultStderrLines    = 200
	maxStrayFramesPerCall = 3
	// readBufSize：models.list 这类应答可能上百 KB，单行缓冲给到 1 MiB 免得被切成两半。
	readBufSize = 1 << 20
)

// Options 是启动 sidecar 的全部参数。字段与 config.Sidecar 一一对应，
// 但本包不 import internal/config：配置结构属于装配层，客户端只吃它需要的东西，
// 这样契约测试不必为造一个合法 Config 而写一大堆无关字段。
type Options struct {
	// NodePath 是 node 可执行文件路径，留空则用 PATH 里的 "node"。
	NodePath string
	// Entry 是 sidecar 入口脚本（sidecar/index.mjs）的路径。
	Entry string
	// HubPath 是 dsh-providers-hub 包目录，透传给 sidecar 供 import() 使用。
	HubPath string
	// DSHHome 覆盖 sidecar 读取凭据的根目录（默认 $DSH_HOME 或 ~/.dsh）。
	DSHHome string
	// Env 追加到子进程环境（在继承 os.Environ() 之后追加，故可覆盖）。
	Env []string
	// StartupTimeout 是等待 health 应答的上限。
	StartupTimeout time.Duration
	// Timeout 是未在 Timeouts 里单列的方法的默认超时。
	Timeout time.Duration
	// Timeouts 按方法覆盖超时（值是 04-sidecar.md 5.3 那张表）。
	Timeouts map[string]time.Duration
	// Logger 用于记录 stderr 与协议告警；为 nil 时用 slog.Default()。
	Logger *slog.Logger
	// StderrLines 是 stderr 环形缓冲保留的行数，供排障与经济地断言测试使用。
	StderrLines int
	// Dir 是子进程工作目录，留空则继承。
	Dir string
}

func (o Options) nodePath() string {
	if strings.TrimSpace(o.NodePath) == "" {
		return defaultNodePath
	}
	return o.NodePath
}

func (o Options) startupTimeout() time.Duration {
	if o.StartupTimeout <= 0 {
		return defaultStartupTimeout
	}
	return o.StartupTimeout
}

func (o Options) logger() *slog.Logger {
	if o.Logger == nil {
		return slog.Default()
	}
	return o.Logger
}

func (o Options) stderrLines() int {
	if o.StderrLines <= 0 {
		return defaultStderrLines
	}
	return o.StderrLines
}

// methodTimeout 按方法取超时。写在客户端而不是调用方：
// 超时是**通道语义**的一部分（登录轮询必须比 models.list 更快失败），
// 让每个调用点自己传一个数字，第一个忘记传的人就会把整条链路挂住。
func (o Options) methodTimeout(method string) time.Duration {
	if d, ok := o.Timeouts[method]; ok && d > 0 {
		return d
	}
	base := o.Timeout
	if base <= 0 {
		base = defaultCallTimeout
	}
	return base
}

// 帧形状。字段命名借用 JSON-RPC 2.0，但**不实现**完整规范（无 batch、无通知）：
// 我们没有这两类需求，实现只会带来没被测试覆盖的分支（04-sidecar.md 2.1）。
type request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	// ID 用指针：缺 id 的帧是协议违规，不能被当成 id=0 的正常应答。
	ID     *uint64         `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

type wireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// inbound 是读循环交给调用方的一帧。
type inbound struct {
	resp response
	// err 非 nil 表示这一行根本不是合法 JSON（第三方库往 stdout 吐了日志之类）。
	// 这类帧不致命：丢掉它、记一次告警，继续等真正的应答。
	err error
	raw string
}

// Client 是一个 sidecar 子进程的句柄。零值不可用，但**对 nil 与零值都安全**：
// 方法返回「不可用」错误而不是 panic。这条是刻意的 API 形状——
// 转发路径不判空，就不会因为有人顺手把 header 接错而崩掉。
type Client struct {
	opts Options
	log  *slog.Logger

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	waitCh chan struct{}
	// waitErr 由 Wait goroutine 写、由 Call 读，故必须加锁：
	// 二者之间只有 waitCh 的关闭建立 happens-before，而 frames 的关闭更早，
	// 在「stdout 先 EOF」这条路径上直接读 waitErr 就是一次数据竞争（已实测）。
	waitMu  sync.Mutex
	waitErr error

	frames chan inbound
	// callMu 串行化「一问一答」：同一时刻只允许一个未完成请求。
	// 用 mutex 而不是队列：sidecar 契约本身就不支持并发，排队只会把并发错误变成延迟。
	callMu sync.Mutex
	nextID uint64 // 只在持有 callMu 时读写，故不必原子

	stderr *stderrRing

	stateMu  sync.Mutex
	state    statusState
	reason   string
	version  string
	provide  []string
	deps     []Dependency
	readonly bool
	since    time.Time
}

type statusState int

const (
	stateUnknown statusState = iota
	stateReady
	stateDown
)

// Dependency 是 sidecar 自报的 B 层依赖（@deepseek-ai/* 是否可解析）。
type Dependency struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Status 描述「贡献功能」的可用性。它**不是**就绪探针：
// /readyz 只看 relay，本结构只喂 /healthz 的明细与控制台横幅（04-sidecar.md 5.1）。
type Status struct {
	Available    bool
	Reason       string // 面向用户的中文原因，可直接进横幅
	Version      string
	Providers    []string
	Dependencies []Dependency
	// StateReadonly 永远是 true——sidecar 自己也只能只读（红线②）。
	StateReadonly bool
	Since         time.Time
}

// Start 拉起 sidecar 进程并做一次 health 握手。
//
// 返回值语义（这条必须照做，否则一次 sidecar 故障就能让整站起不来）：
//   - 返回的 *Client **永远非 nil**，即使 sidecar 完全起不来；
//   - 返回的 error 只表示「贡献功能不可用」，调用方应当**记录并继续启动**，
//     而不是把它当作启动失败上抛。
//
// 文档依据：04-sidecar.md 5.1 步骤 4——health 失败挂横幅、继续、vendor 全部视为不可用。
func Start(ctx context.Context, opts Options) (*Client, error) {
	c := &Client{
		opts:   opts,
		log:    opts.logger(),
		frames: make(chan inbound, 8),
		stderr: newStderrRing(opts.stderrLines()),
		nextID: 0,
	}
	if strings.TrimSpace(opts.Entry) == "" {
		c.markDown("未配置 sidecar 入口脚本（sidecar.entry），贡献功能不可用")
		return c, c.unavailableErr(methodHealth, "未配置 sidecar 入口脚本")
	}

	if err := c.spawn(); err != nil {
		reason := fmt.Sprintf("sidecar 无法启动（%s %s）：%v", opts.nodePath(), opts.Entry, err)
		c.markDown(reason)
		return c, c.unavailableErr(methodHealth, reason)
	}

	// 握手：只有 health 成功才把状态置为 ready。失败也不杀进程——
	// 一个能启动但不健康的 sidecar 仍然值得留在身边重试（它可能是慢，不是死）。
	h, err := c.Health(ctx)
	if err != nil {
		reason := "sidecar health 握手失败：" + err.Error()
		c.markDown(reason)
		return c, c.unavailableErr(methodHealth, reason)
	}
	c.markReady(h)
	return c, nil
}

func (c *Client) spawn() error {
	cmd := exec.Command(c.opts.nodePath(), c.opts.Entry)
	cmd.Dir = c.opts.Dir
	env := append([]string{}, os.Environ()...)
	env = append(env, c.opts.Env...)
	if c.opts.HubPath != "" {
		env = append(env, "FR_HUB_PATH="+c.opts.HubPath)
	}
	if c.opts.DSHHome != "" {
		env = append(env, "DSH_HOME="+c.opts.DSHHome)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("取 stdin 管道失败：%w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("取 stdout 管道失败：%w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("取 stderr 管道失败：%w", err)
	}
	// 子进程退出后不要把整个进程组一起带走：sidecar 只是支线，主进程必须能继续跑。
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec 失败：%w", err)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.waitCh = make(chan struct{})
	go func() {
		// Wait 必须有人调用：否则子进程退化成僵尸。
		err := cmd.Wait()
		c.waitMu.Lock()
		c.waitErr = err
		c.waitMu.Unlock()
		close(c.waitCh)
	}()

	go c.readLoop(stdout)
	go c.drainStderr(stderr)
	return nil
}

// readLoop 是唯一的 stdout 读者。把行读成帧后投给 frames；
// EOF 时关掉 frames —— 这正是「stdio 而非 socket」的值：
// sidecar 一死，读循环立刻结束，调用方**不需要额外探活**就知道它没了。
func (c *Client) readLoop(stdout io.Reader) {
	defer close(c.frames)
	br := bufio.NewReaderSize(stdout, readBufSize)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if strings.TrimSpace(trimmed) != "" {
				var resp response
				if jerr := json.Unmarshal([]byte(trimmed), &resp); jerr != nil {
					c.frames <- inbound{err: jerr, raw: trimmed}
				} else {
					c.frames <- inbound{resp: resp, raw: trimmed}
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.log.Warn("sidecar stdout 读取错误", "err", err)
			}
			return
		}
	}
}

// stderrLogMaxBytes 是进结构化日志的 sidecar stderr 单行上限。
//
// 为什么必须有上限：sidecar 的 stdout-guard 会把「任何绕过 console 直写 stdout 的内容」
// 改道 stderr（典型：第三方库 banner、hub adapter 的调试输出）。这些行可能很长，
// 也可能带整段 HTML/JSON；整行落进日志既淹没有用信息，也放大泄露面。
// 512 字节足够保留「哪一类输出、在哪一步」，又不会把日志变成大文件。
const stderrLogMaxBytes = 512

// drainStderr 把 sidecar 的日志并进主进程日志流。
// sidecar 的 stdout 只准是 NDJSON，任何人类可读内容都必须从这里出来。
//
// 两道处理（都已实测过泄露路径，见 docs/architecture/04-sidecar.md §8）：
//  1. **截断**到 stderrLogMaxBytes；
//  2. **遮蔽** Bearer/授权码/token/secret/sk- 形态——sidecar 的 stdout-guard 会把
//     第三方库的任意输出改道到这里，OAuth 回调 URL 里的 `code=` 完全可能混进来。
//
// 环形缓冲里保留**原文**：StderrTail 是排障用的（要看完整错误），且它只在本进程内存里、
// 不会进日志系统；进结构化日志的那一份才是需要遮蔽的。
func (c *Client) drainStderr(stderr io.Reader) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64<<10), readBufSize)
	for sc.Scan() {
		line := sc.Text()
		c.stderr.push(line)
		c.log.Debug("sidecar stderr", "line", redactLogLine(line), "raw_bytes", len(line))
	}
}

// —— 敏感文本遮蔽（Go 侧的**唯一实现**）——
//
// ⚠️ 三处同源，改一处必须同步其余两处：
//   1. 本文件（sidecar stderr 进结构化日志前）；
//   2. internal/upstream/contribution.go 的 redactText —— 直接转调 RedactLine（不复制模式）；
//   3. sidecar/redact.mjs —— sidecar 自己外发的错误/stderr 文本。
// Go 侧只有这一份模式（第 2 处是转调）；第 3 处是跨语言副本，
// 由 sidecar/selftest.mjs 的同一组表驱动用例把两边钉住。
//
// 两条设计约束（改正则前必须先读，否则会踩已经踩过的坑）：
//   - **键名要左边界**：裸 `code`/`token` 会命中 `error_code`/`status_code` 的子串，
//     把排障字段（正是 sidecar 错误帧 `{code:"E_..."}` 的形态）一并抹掉。
//   - **RE2 不支持反向引用/环视**：带引号的值不能靠 `\1` 配对，必须分组 + 手工拼装，
//     否则引号被吃掉，日志里的 JSON 不再可解析。
//
// 值什么时候**不遮**：snake_case 标识符（E_DEPENDENCY_MISSING、no_candidate、
// model_not_found）是错误码而不是凭据。这条优先级明确——宁可漏遮「长成这样」的真凭据
// （极少见），也不抹掉「为什么失败」，否则 M8 联调时日志会变成一堆 ***。

var (
	// 鉴权头：Bearer <tok> / Basic <base64(user:pass)>。
	// Basic 以前漏遮——base64 里就是明文的 user:pass。
	redactBearerRE = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	redactBasicRE  = regexp.MustCompile(`(?i)\bBasic\s+[A-Za-z0-9+/=]{8,}`)
	// 裸密钥前缀（OpenAI 风格）：即使没有键名也要遮。
	//
	// 左边界是 `(^|[^A-Za-z0-9_])` 而不是 `\b`：`\b` 要求前一个是「词/非词交界」，
	// 而 `sk-` 前面常是另一个词字符（`token:sk-...` 里是冒号，没问题；但拼接日志里
	// 可能直接粘在字母后面）——`\b` 会在那里静默不匹配。用非词字符做左边界既挡住
	// `task-management` 这类误伤，也不会漏掉粘在字母后的真 key。
	redactSKRE = regexp.MustCompile(`(^|[^A-Za-z0-9_])sk-[A-Za-z0-9_-]{8,}`)
	// 「键 = 值 / 键: 值」。group1=左边界（同时保证 `error_code` 不会被当成 `code`），
	// group2=键与分隔符，group3=值（带引号形态由 `"[^"]*"` 分支整体接住并保留配对引号）。
	//
	// 值字符集排除 `"` `'` 与空白/分隔符，避免把 JSON 结构字符吞进来（那样会破坏可解析性）。
	redactPairRE = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])((?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?code|oauth[_-]?code|authorization[_-]?code|client[_-]?secret|secret|token|code|key|password)["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&)}"']{8,})`)
	// 标识符形态（至少一个下划线）——判定「这是错误码/状态名，不是凭据」。
	redactIdentifierRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:_[A-Za-z0-9]+)+$`)
)

// RedactLine 返回可安全进日志/证据的一行。
//
// 导出给 internal/upstream/contribution.go 转调：「哪一行算敏感」不该有两套规则。
// 包方向是既有的（relay.go 已 import 本包）。
//
// 幂等：对已遮蔽的文本再跑一次不变（同一行可能被多层包装：adapter → sidecar → 主进程日志）。
func RedactLine(line string) string {
	out := line
	// 先截断再遮蔽：反过来会把 `***` 截掉一半，留下半截密钥前缀——那比不遮更糟。
	if len(out) > stderrLogMaxBytes {
		out = out[:stderrLogMaxBytes] + "…"
	}
	out = redactBearerRE.ReplaceAllString(out, "Bearer ***")
	out = redactBasicRE.ReplaceAllString(out, "Basic ***")
	out = redactSKRE.ReplaceAllString(out, "${1}sk-***")
	return redactPairs(out)
}

// redactLogLine 是 drainStderr 的调用点（保留短名，导出名给跨包用）。
func redactLogLine(line string) string { return RedactLine(line) }

// redactPairs 处理「键=值」形态：纯正则可以命中，但「遮不遮」要看值的形态
// （标识符保留、其余替换），所以用一次扫描 + 手工拼装，而不是 ReplaceAllString。
func redactPairs(s string) string {
	matches := redactPairRE.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, m := range matches {
		keyEnd := m[5]   // group2 结束：写到这里为止（键与前导分隔原样保留）
		valStart := m[6] // group3 开始
		valEnd := m[7]
		value := s[valStart:valEnd]
		if looksLikeIdentifier(value) {
			continue // 错误码/状态名：原样保留（`last` 不动，后面的拼接会带上它）
		}
		b.WriteString(s[last:keyEnd])
		// 保留配对引号，保证 JSON 还能解析（`{"access_token":"***"}`）。
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			b.WriteByte(value[0])
			b.WriteString("***")
			b.WriteByte(value[0])
		} else {
			b.WriteString("***")
		}
		last = valEnd
	}
	b.WriteString(s[last:])
	return b.String()
}

// looksLikeIdentifier 判断值是否是被抹掉会伤排障的标识符（错误码/状态名）。
//
// 规则刻意保守：必须含下划线（`E_X`、`no_candidate`、`model_not_found`），
// 长度 8..64。纯小写随机串（`abcd1234efghijkl`）没有下划线 → 仍然遮蔽。
func looksLikeIdentifier(v string) bool {
	v = strings.Trim(v, `"'`)
	if len(v) < 8 || len(v) > 64 {
		return false
	}
	return redactIdentifierRE.MatchString(v)
}

// Call 发一个方法调用并等应答。params 为 nil 时字段整个省略。
//
// 为什么超时就杀进程：一问一答契约下，一旦放弃等待，接下来到达的那一帧属于
// 一个已经没人认领的 id。留着一个「有一帧在途」的进程，后面每次调用都要先猜
// 这一帧是谁的——那比重启贵得多。所以任何中途放弃（超时、ctx 取消）一律杀进程。
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c == nil {
		return nil, errDown(method, "sidecar 客户端未初始化")
	}
	c.callMu.Lock()
	defer c.callMu.Unlock()

	// 门禁只拦「明确不可用」：stateUnknown 必须放行——否则 Start 的 health 握手
	// 自己就被自己拦死（握手时还没有任何成功记录，状态永远是 unknown）。
	if st := c.availability(); !st.Available && c.stateIsDown() {
		return nil, errDown(method, st.Reason)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, &Error{Code: CodeBadRequest, Method: method,
				Message: "参数无法序列化：" + err.Error()}
		}
		raw = b
	}

	c.nextID++
	id := c.nextID
	line, err := json.Marshal(request{ID: id, Method: method, Params: raw})
	if err != nil {
		return nil, &Error{Code: CodeInternal, Method: method, Message: err.Error()}
	}
	line = append(line, '\n')

	// 写失败 = 管道已断 = 子进程已死。这条路径必须比读超时更快地给出答案。
	if _, err := c.stdin.Write(line); err != nil {
		c.markDown(fmt.Sprintf("sidecar stdin 写入失败（进程已退出）：%v", err))
		return nil, &Error{Code: CodeProtocol, Method: method, Retryable: true,
			Message: "sidecar 进程已退出：" + err.Error()}
	}

	callCtx, cancel := c.deadline(ctx, method)
	defer cancel()

	strays := 0
	for {
		select {
		case frame, ok := <-c.frames:
			if !ok {
				reason := "sidecar 在应答前退出（stdout EOF）"
				if werr := c.waitError(); werr != nil {
					reason += "：" + werr.Error()
				}
				c.markDown(reason)
				return nil, &Error{Code: CodeProtocol, Method: method, Retryable: true, Message: reason}
			}

			if frame.err != nil {
				// 非 JSON 行：容错。第三方库往 stdout 写日志是可预期的污染源
				// （04-sidecar.md §8 的待定项正是它），丢掉并告警，不因此判死。
				strays++
				c.log.Warn("sidecar stdout 混入非 JSON 行，已忽略",
					"method", method, "line", truncate(frame.raw, 200), "err", frame.err)
				if strays > maxStrayFramesPerCall {
					return nil, c.protocolBail(method, "stdout 连续出现非协议内容")
				}
				continue
			}
			if frame.resp.ID == nil || *frame.resp.ID != id {
				got := "null"
				if frame.resp.ID != nil {
					got = fmt.Sprintf("%d", *frame.resp.ID)
				}
				strays++
				c.log.Warn("sidecar 应答 id 不匹配，已忽略",
					"method", method, "want", id, "got", got)
				if strays > maxStrayFramesPerCall {
					return nil, c.protocolBail(method, "应答 id 连续不匹配")
				}
				continue
			}
			return c.finish(method, frame.resp)

		case <-callCtx.Done():
			if errors.Is(callCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				timeout := c.opts.methodTimeout(method)
				c.kill()
				c.markDown("sidecar 调用超时后被终止（" + method + "）")
				return nil, &Error{Code: CodeTimeout, Method: method, Retryable: true,
					Message: fmt.Sprintf("等待 sidecar 应答超过 %s（%s）", timeout, method)}
			}
			c.kill()
			c.markDown("sidecar 调用被取消后被终止（" + method + "）")
			return nil, &Error{Code: CodeProtocol, Method: method,
				Message: "调用被取消：" + ctx.Err().Error()}
		}
	}
}

// finish 把一帧应答翻译成结果或结构化错误。
func (c *Client) finish(method string, resp response) (json.RawMessage, error) {
	if resp.OK {
		if len(resp.Result) == 0 {
			return json.RawMessage("null"), nil
		}
		return resp.Result, nil
	}
	if resp.Error == nil {
		// ok=false 但没给 error：协议违规，但**不能**当成功。
		return nil, &Error{Code: CodeProtocol, Method: method,
			Message: "sidecar 返回 ok=false 但缺少 error 字段"}
	}
	code := resp.Error.Code
	if !knownCode(code) {
		// 04-sidecar.md §3：未知 error.code 一律按 E_INTERNAL 处理并记原文，
		// 否则新版本 sidecar 新增一个错误码就能把老主进程搞崩。
		c.log.Warn("sidecar 返回了未知错误码，按 E_INTERNAL 处理",
			"method", method, "code", code, "message", resp.Error.Message)
		code = CodeInternal
	}
	return nil, &Error{Code: code, Method: method, Retryable: resp.Error.Retryable,
		Message: resp.Error.Message}
}

// protocolBail 在协议违规超限时把客户端标记为失能。
// 依据 04-sidecar.md 5.2：连续 3 次协议违规视为 sidecar 失能，必须重启。
func (c *Client) protocolBail(method, why string) error {
	c.kill()
	c.markDown("sidecar 协议违规：" + why)
	return &Error{Code: CodeProtocol, Method: method, Retryable: true, Message: why}
}

// deadline 把「方法超时」与「调用方 ctx」合并成一个。
// 取更早的那个：调用方已经没耐心了，多等没有意义。
func (c *Client) deadline(ctx context.Context, method string) (context.Context, context.CancelFunc) {
	d := c.opts.methodTimeout(method)
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain < d {
			d = remain
		}
	}
	return context.WithTimeout(ctx, d)
}

func (c *Client) kill() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
}

func (c *Client) markReady(h *Health) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.state = stateReady
	c.reason = ""
	c.version = h.Version
	c.provide = append([]string{}, h.Providers...)
	c.deps = append([]Dependency{}, h.Deps...)
	c.readonly = true
	c.since = time.Now()
}

func (c *Client) markDown(reason string) {
	if c == nil {
		return
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.state == stateDown && c.reason == reason {
		return
	}
	c.state = stateDown
	c.reason = reason
	c.since = time.Now()
	c.log.Warn("sidecar 不可用：贡献功能降级（不影响转发）", "reason", reason)
}

// availability 是唯一读状态的地方，所有公开方法都经过它。
func (c *Client) availability() Status {
	if c == nil {
		return Status{Available: false, Reason: "sidecar 客户端未初始化"}
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return Status{
		Available:     c.state == stateReady,
		Reason:        c.reason,
		Version:       c.version,
		Providers:     append([]string{}, c.provide...),
		Dependencies:  append([]Dependency{}, c.deps...),
		StateReadonly: c.readonly,
		Since:         c.since,
	}
}

// Status 返回「贡献功能」的可用性快照。
// 注意它**不影响**转发：转发路径根本不调本包，所以这里返回 false 时
// 正常推理仍然必须工作（04-sidecar.md 5.1）。
func (c *Client) Status() Status { return c.availability() }

// Available 是 Status().Available 的简写，供 /healthz 逐项汇总。
func (c *Client) Available() bool { return c.availability().Available }

// StderrTail 返回最近 n 行 sidecar stderr，供排障与契约测试断言。
// 为什么存在：sidecar 的日志不落在独立文件里（§8 待定项），
// 不留一个可读的尾巴就只能去翻主进程日志。
func (c *Client) StderrTail(n int) []string {
	if c == nil {
		return nil
	}
	return c.stderr.tail(n)
}

// Close 按 04-sidecar.md 5.4 的顺序关掉 sidecar：
// 先发 shutdown 让它自己清会话与监听端口，再关 stdin，再等，最后 kill。
// 为什么不能只 kill：login.* 可能在 Node 侧持有定时器和回环监听端口，
// 显式 shutdown 是唯一能让它主动清理的机会。
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.cmd == nil {
		return nil
	}
	c.callMu.Lock()
	if c.stateIsReady() {
		line, _ := json.Marshal(request{ID: c.nextID + 1, Method: methodShutdown})
		_, _ = c.stdin.Write(append(line, '\n'))
		c.nextID++
	}
	_ = c.stdin.Close()
	c.callMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-c.waitCh:
	case <-time.After(2 * time.Second):
		c.kill()
		<-c.waitCh
	case <-ctx.Done():
		c.kill()
		<-c.waitCh
	}
	c.markDown("sidecar 已关闭")
	return nil
}

func (c *Client) stateIsReady() bool {
	if c == nil {
		return false
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state == stateReady
}

// stateIsDown 区分「明确不可用」与「尚未握手（unknown）」：Call 的门禁只拦前者。
// 为什么必须区分：health 握手自己就是一个 Call，握手时状态还是 unknown——
// 若门禁把 unknown 也拦下，Start 永远无法把状态从 unknown 推到 ready（实测踩过）。
func (c *Client) stateIsDown() bool {
	if c == nil {
		return true
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state == stateDown
}

// waitError 安全地取子进程退出错误。
func (c *Client) waitError() error {
	if c == nil {
		return nil
	}
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	return c.waitErr
}

// unavailableErr 构造「贡献功能不可用」错误。
func (c *Client) unavailableErr(method, reason string) error {
	return &Error{Code: CodeSidecarDown, Method: method, Retryable: true, Message: reason}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
