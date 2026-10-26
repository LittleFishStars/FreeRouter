package upstream

import (
	"context"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 本文件是 03-upstream.md §2「统一接口」的逐字落地。
//
// 为什么三类上游是同一个接口而不是三条流程：从 call 模块看，它们都是「拿一个目标
// 和一个凭据做一次生成请求」。差异只有四处——目标从哪来、凭据从哪来、协议方言、
// 请求经哪条物理路径出去。前三项收在 Target 里、第四项收在 Open 的实现里，
// 重试/切换/超时/计量的代码就只需一份。

// Kind 是上游的三类（§1）：反代 relay、上游 vendor、自建 selfhosted。
//
// 为什么用英文标识符而叙述用中文名：upstreams.kind = 'upstream' 这种自指读起来像 bug，
// 且 upstream 同时是层名（upstream_credentials 等表都用了它），一个词指「层」和
// 「层里的一类」会让日志与 SQL 无法一眼判断在说谁。
type Kind string

const (
	KindRelay      Kind = "relay"
	KindVendor     Kind = "vendor"
	KindSelfHosted Kind = "selfhosted"
)

// Dialect 是上游说的协议。两类 HTTP 上游用前两个；relay 由 adapter 决定线上格式。
type Dialect string

const (
	DialectOpenAIChat      Dialect = "openai-chat"
	DialectOpenAIResponses Dialect = "openai-responses"
	// DialectAdapter：relay 的线上格式由 provider adapter 决定，Go 侧只见到 IR 事件。
	DialectAdapter Dialect = "adapter"
)

// Target 是「一次尝试」的全部输入：上游 + 凭据实例 + 真实模型 + 协议方言。
//
// 为什么 relay 的 BaseURL 恒为空（红线，validate.go 守卫）：relay 的目标不是
// 一个 HTTP 地址，而是 sidecar 进程里的一个 provider adapter 实例——写进地址
// 就等于把「不经任何 HTTP 网关」的架构决定（ALIGN-SPEC H）悄悄推翻。
type Target struct {
	UpstreamID   string // 上游行 UUID
	Kind         Kind
	BaseURL      string // relay：恒为空（见 ValidateTarget）；vendor/selfhosted：https 地址
	CredentialID string // 空串 = 库里没有凭据（relay 走 sidecar 侧的账号池）
	SecretRef    string // relay：空；其余：upstream_secrets.id
	RealModel    string // 上游模型 id（provider 前缀 + 模型名，如 codearts/deepseek-v4.1-flash）
	Dialect      Dialect
}

// ProbeResult 是一次探测的结果。必须廉价——探测无额度成本才有资格做健康探测。
type ProbeResult struct {
	OK      bool
	Latency time.Duration
	// Detail 是失败时的原因（错误类或文案）。为什么不是 error：探测失败是「一种状态」
	// 而不是异常，调用方要拿它更新健康计数而不是中断启动。
	Detail string
}

// ModelInfo 是上游模型目录的一个条目（§10 K1 断言的形状：id/name/input/reasoning.efforts）。
type ModelInfo struct {
	ID          string // 上游模型 id
	DisplayName string
	// ContextWindow 为 0 表示未知——「未知」与「确定是 0」在上游语义不同，
	// 能力匹配只能把前者当「不设限」处理。
	ContextWindow int
	// Modalities 是输入模态（"text"/"image"…）。nil = 未知，不等于「只支持文本」。
	Modalities []string
	// ReasoningEfforts 是支持的思考档位（ir.Effort 的字符串形态）。nil = 未知。
	ReasoningEfforts []string
}

// Upstream 是三类上游的统一接口。
//
// 接口上没有 Retry——重试是跨候选的决策（先同候选重试还是换候选、退避多少、
// 总预算剩多少），属于 call 模块（retry.go 提供决策表）。若让每个上游自己重试，
// 就会与「未吐可见内容前可切换候选」的判定打架：上游重试成功了我们不知道，
// 上游重试失败了 call 又重试一次，同一个候选被打了 6 次。
type Upstream interface {
	// Probe 探测可达性与鉴权是否有效。必须廉价。
	Probe(ctx context.Context) ProbeResult

	// ListModels 拉取上游模型目录。relay 走 adapter 的 listModels，其余打 /v1/models。
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Open 发起一次生成请求。返回时已确认拿到响应头（200），或返回错误。
	// 错误必须分类（errors.go 的 ErrClass），供 call 模块决定是否重试。
	Open(ctx context.Context, t Target, req *ir.Request) (Stream, error)
}

// Stream 是一次生成请求的流。
//
// 为什么 Open + Recv 而不用 chan ir.Event：chan 会同时模糊三件事——谁负责关闭、
// 错误放在哪、取消如何表达。HTTP 响应体本来就是「拉取」语义，Recv 是直接映射；
// chan 还会强制引入一个常驻 goroutine 与缓冲策略。
type Stream interface {
	// Recv 返回下一个事件。io.EOF 表示上游正常结束。
	// 返回 error 时，若 Stats().VisibleWritten 为 false，调用方可以换候选。
	Recv() (ir.Event, error)

	// Close 幂等，关闭连接与底层 body。
	Close() error

	// Stats 供 meter 与排障使用。
	Stats() StreamStats
}

// StreamStats 是流的可观测切片。
type StreamStats struct {
	// UpstreamStatus 是上游 HTTP 状态码；http=000 类失败记为 0。
	UpstreamStatus int
	TTFT           time.Duration
	BytesIn        int64
	EventsOut      int64
	// VisibleWritten 是否已产出过「可见内容」（02-ir.md §5.3）。
	// 它是 06-runtime.md 切换状态机的唯一输入；放在 Stream 上是为了不重复实现一遍判定。
	VisibleWritten bool
	// RetryCount 是本候选消耗的重试次数。
	RetryCount int
}
