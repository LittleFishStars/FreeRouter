> 分册：架构 · 依赖：[01-overview.md](01-overview.md)、[02-ir.md](02-ir.md)、[06-runtime.md](06-runtime.md) · 对应总纲：[PLAN.md](../PLAN.md) 第 5、6 章

# 上游适配层

本册定义 L3 的对外接口、三类上游（`dsh_provider` / `contributed` / `compatible`）的实现约束、
连接池与重试、健康探测、超时矩阵，以及上游侧的硬约束（请求体上限、keep-alive、出站边界）。

---

## 1. 三类上游

| kind | 表 `upstreams.kind` | 目标 | 凭据 | 供给来源 |
|---|---|---|---|---|
| `dsh_provider` | `"dsh_provider"` | providers-hub 的 **provider adapter**（跑在 Node sidecar 内，`lib/*-adapter.js`），**不经过任何 HTTP 网关** | providers-hub 账号池，由 sidecar 持有；FreeRouter 不落库 | 平台自有（`owner_account_id = NULL`） |
| `contributed` | `"contributed"` | 凭据自带的第三方端点（只允许 https） | `upstream_secrets.ciphertext`（AES-GCM） | **贡献者**凭据 |
| `compatible` | `"compatible"` | 管理员填写的 OpenAI 兼容 `base_url`（本机或自建） | `upstream_secrets.ciphertext`；平台自有行 `owner_account_id = NULL` | 平台自有 / 管理员配置 |

**为什么 `dsh_provider` 不再经过 `127.0.0.1:8326` 的 OpenAI 兼容网关**（ALIGN-SPEC H 推翻旧设计）：
那个网关是 providers-hub 为了让「本机一个客户端拿一个 Bearer 用全部模型」而做的**收口层**，
它顺带抹掉了三件我们需要的东西——① 到底是哪个 provider 账号出的这次请求（网关只认一个 Bearer）；
② provider 侧的原始错误与限流语义（网关归一成 OpenAI 形态）；③ adapter 才有的能力
（provider 私有重试策略、图像请求定价、思考档位）。provider adapter 才是这些信息的源头，
而 adapter 只能跑在 Node 进程里（它继承 `LlmAdapter`、依赖 `@deepseek-ai/dsh-llm`）。
所以链路变成 **Go → sidecar IPC → provider adapter → 官方端点**，不再有网关这一跳。

**为什么三类上游是同一个接口而不是三条流程**：从 `call` 模块看，它们都是「拿一个目标和一个
凭据做一次生成请求」。差异只有四处——目标从哪来、凭据从哪来、协议方言是什么、**请求经哪条
物理路径出去**（`dsh_provider` 走 IPC，另两类走 HTTP）。前三项收在 `Target` 里、第四项收在
`Open` 的实现里，重试/切换/超时/计量的代码就只需一份。

**`owner_account_id = NULL` 表示平台自有**（PLAN 4.2）：平台自有的两条来源（`dsh_provider` 与
管理员配置的 `compatible`）与贡献凭据**同构但来源不同**，
这个区分必须保留在数据层，因为：
- 归因时不能把平台额度算成某个贡献者的贡献量；
- 审计时"我们自己的 key 被谁用了"与"贡献者的 key 被用了几次"是两个问题。

---

## 2. 统一接口

```go
type Kind string

const (
    KindDSHProvider Kind = "dsh_provider"
    KindContributed Kind = "contributed"
    KindCompatible  Kind = "compatible"
)

// Dialect 是上游说哪种协议。两类 HTTP 上游用前两个；dsh_provider 由 adapter 决定线上格式。
type Dialect string

const (
    DialectOpenAIChat      Dialect = "openai-chat"
    DialectOpenAIResponses Dialect = "openai-responses"
    DialectAdapter         Dialect = "adapter" // dsh_provider：线上格式由 adapter 决定，Go 侧只见到 IR 事件
)

// Target 是"一次尝试"的全部输入：上游 + 凭据实例 + 真实模型 + 协议方言。
type Target struct {
    UpstreamID   int64
    Kind         Kind
    BaseURL      string
    CredentialID int64  // 0 = 库里没有凭据（dsh_provider 走 sidecar 侧的账号池）
    SecretRef    string // dsh_provider：空；其余：upstream_secrets.id
    RealModel    string // 上游模型 id（provider 前缀 + 模型名，如 codearts/deepseek-v4.1-flash）
    Dialect      Dialect
}

type Upstream interface {
    // Probe 探测可达性与鉴权是否有效。必须廉价。
    Probe(ctx context.Context) ProbeResult

    // ListModels 拉取上游模型目录。dsh_provider 走 adapter 的 listModels，其余打 /v1/models。
    ListModels(ctx context.Context) ([]ModelInfo, error)

    // Open 发起一次生成请求。返回时已确认拿到响应头（200），或返回错误。
    // 错误必须分类（见第 6 节的 ErrClass），供 call 模块决定是否重试。
    Open(ctx context.Context, t Target, req *ir.Request) (Stream, error)
}

type Stream interface {
    // Recv 返回下一个事件。io.EOF 表示上游正常结束。
    // 返回 error 时，若 Stats().VisibleWritten 为 false，调用方可以换候选。
    Recv() (ir.Event, error)

    // Close 幂等，关闭连接与底层 body。
    Close() error

    // Stats 供 meter 与排障使用。
    Stats() StreamStats
}

type StreamStats struct {
    UpstreamStatus  int       // 上游 HTTP 状态码；http=000 类失败记为 0
    TTFT            time.Duration
    BytesIn         int64
    EventsOut       int64
    VisibleWritten  bool      // 是否已产出过"可见内容"（见 02-ir.md 第 5.3 节）
    RetryCount      int       // 本候选消耗的重试次数
}
```

### 2.1 关键取舍

| 决策 | 理由 |
|---|---|
| `Open` + `Recv`，不用 `chan ir.Event` | chan 会同时模糊三件事：谁负责关闭、错误放在哪、取消如何表达。HTTP 响应体本来就是"拉取"语义，`Recv` 是直接映射；chan 还会强制引入一个常驻 goroutine 与缓冲策略（见 [06-runtime.md](06-runtime.md) 的 goroutine 账） |
| `Stats()` 暴露 `UpstreamStatus` | 诊断与 `requests.http_status` 落库都需要；且"http=000"这类失败必须能被计数（PLAN 13 风险表要求观察其频率） |
| `Stats()` 暴露 `VisibleWritten` | 它是 [06-runtime.md](06-runtime.md) 切换状态机的唯一输入。放在 Stream 上是为了不重复实现一遍判定 |
| **接口上没有 Retry** | 重试是**跨候选**的决策（先同候选重试还是换候选、退避多少、总预算剩多少），属于 `call`。若让每个上游实现自己重试，就会与"未吐可见内容前可切换候选"的判定打架：上游重试成功了我们不知道，上游重试失败了 call 又重试一次，同一个候选被打了 6 次 |
| `Target` 里有 `Dialect` | 出站编码按**上游**协议而非入站协议选择。`contributed` / `compatible` 的端点可能只支持 chat 或两者都支持，选哪个由候选声明决定；`dsh_provider` 的线上格式由 adapter 决定（`DialectAdapter`） |

### 2.2 错误分类

```go
type ErrClass string

const (
    ErrConnReset    ErrClass = "conn_reset"    // 连接被重置 / 半开连接
    ErrConnect      ErrClass = "connect"        // 连不上（拒绝 / 超时）
    ErrTLS          ErrClass = "tls"
    ErrTimeout      ErrClass = "timeout"        // 阶段超时，细分见第 8 节
    ErrUpstream5xx  ErrClass = "upstream_5xx"
    ErrRateLimited  ErrClass = "rate_limited"   // 429
    ErrAuth         ErrClass = "upstream_auth"  // 401/403，凭据坏了
    ErrBadRequest   ErrClass = "bad_request"    // 4xx，请求本身的问题
    ErrQuota        ErrClass = "upstream_quota" // 额度耗尽类错误
    ErrProtocol     ErrClass = "protocol"       // 上游返回了不可解析的流
)
```

**为什么要分类而不是看 HTTP 状态码**：`http=000` 根本没有状态码；429 与"额度耗尽"可能都是 429，
但前者等一会儿能好、后者要换凭据。分类值直接映射到第 6 节的重试表与第 7 节的健康状态。

### 2.3 两条物理路径

同一个 `Upstream` 接口下面有两条完全不同的物理路径，它们共享错误分类、重试决策与计量，
但**不共享传输实现**：

| | `dsh_provider` | `contributed` / `compatible` |
|---|---|---|
| 出口 | Go → sidecar IPC（`infer.stream`）→ provider adapter → 官方端点 | Go → HTTPS → 端点 |
| 谁实现协议 | provider adapter（Node 侧） | 本册第 4 节 + [02-ir.md](02-ir.md) 的出站映射 |
| 谁持有凭据 | providers-hub 账号池（sidecar 进程内） | `upstream_secrets`（AES-GCM），我们保管 |
| 错误从哪来 | adapter 抛 `LlmError` → sidecar 映射成本册 2.2 的错误类 | 直接来自 `net/http` 与状态码 |
| 连接池在哪 | sidecar 进程内（不在 Go 的 `Transport` 里） | Go 的 `Transport`（第 5 节） |

**为什么两条路径共用 `Open`/`Recv` 而只在内部换传输**：`call` 模块需要判断的全部信息
（能不能重试、有没有吐过可见内容、用了多少 token）在两条路径上完全一样。若为前者单独做一套
`Stream`，[06-runtime.md](06-runtime.md) 的切换状态机就要写两遍，而两遍必然漂移。

---

## 3. `dsh_provider` 适配

### 3.1 为何是 sidecar 直调 adapter

`dsh_provider` 的目标不是一个 HTTP 地址，而是 **sidecar 进程里的一个 provider adapter
实例**。Go 侧看到的仍然是一个 `Upstream` 实现，但这个实现的 `Open` 不建 HTTP 连接，
而是走 [04-sidecar.md](04-sidecar.md) 的 `infer.stream`。

| 能力 | 从哪来 | 为什么必须走 adapter 而不能自己发 HTTP |
|---|---|---|
| 模型目录 | `adapter.listModels(provider)` | 每个 provider 的目录形态不同（有的 `/v1/models`，有的嵌在账号信息里，`aggregate-*` 还要展开） |
| 模型元信息 | `adapter.resolveModel(provider, model)` | 上下文窗口、思考档位、模态是 provider 私有知识 |
| 重试策略 | `adapter.providerRetryPolicy(provider)` | 每家对 429 / 额度的语义不同，写死一套必错 |
| 鉴权与签名 | adapter 内部（含 HW 签名等） | 签名算法在 adapter 里，Go 重写一遍就是双重维护 |
| 流式生成 | `adapter.stream(options)` | 唯一必需方法；把各家 SSE 归一成 `StreamChunk` |

**为什么不让 Go 自己直连官方端点**：上面五行的知识全在 `lib/*-adapter.js` 里，且与
providers-hub 同版本发布。在 Go 里重写等于把一个 14 个 adapter 的矩阵复制一遍，
而且会立刻滞后于上游改动（PLAN 12 的风险表把这种漂移列为持续风险）。

**代价必须写明**：模型推理现在依赖 sidecar 活着。sidecar 挂了 → `dsh_provider` 这一整类
不可用；`contributed` / `compatible` 仍可用。这就是 `/readyz` 必须把两类分开算的原因（7.3）。

### 3.2 自举与目录同步

| 步骤 | 动作 | 失败的后果 |
|---|---|---|
| 1 | 启动 sidecar，等 `health` 就绪（[04-sidecar.md](04-sidecar.md) 第 5 节） | sidecar 未就绪 → `dsh_provider` 标不可用；**不阻塞主进程启动** |
| 2 | 写 `upstreams` 一行（`kind = 'dsh_provider'`, `owner_account_id = NULL`）；**不写** `upstream_secrets` | 写库失败 → 记错误，下次启动重试 |
| 3 | `providers.list` → `models.list` 拉目录 → 写 `real_models`（`source = auto`） | 失败 → 退避重试（30s 起，指数到 15m） |
| 4 | 缓存目录（TTL 10 分钟），供能力匹配使用 | 缓存过期时先用旧值，避免目录抖动影响转发 |

**为什么 `dsh_provider` 没有 `upstream_secrets` 行**：凭据在 providers-hub 的账号池里，
由 sidecar 持有并刷新。FreeRouter 不复制一份——复制就必然过期，而过期的凭据落在我们库里
只会把“上游挂了”伪装成“我们配错了”。

**为什么目录要有 TTL 与旧值兜底**：目录决定能力过滤，直接影响可用性。若每次请求都实时拉目录，
上游抖动会被放大成“全站 503”；TTL + 旧值让抖动只影响“新模型的可见性”这种可延迟的事情。

**为什么 `source = auto` 的条目在管理员改过之后记 `override` 且刷新不覆盖**（PLAN 11.1）：
管理员的重命名 / 分组 / 隐藏是人工判断的结果，被自动刷新覆盖等于让人白干。

### 3.3 目录变动与故障 → `admin_inbox`

后台目录刷新与健康检查都是**唯一的发现点**，所以通知也写在这里（ALIGN-SPEC G）：

| 事件 | `kind` | `severity` | 为什么算需要管理员处理 |
|---|---|---|---|
| 目录新增模型 | `upstream_model_added` | `info` | 新模型默认不进任何抽象模型，不告知就等于白发现 |
| 目录消失模型 | `upstream_model_missing` | `warn` | PLAN 3.6：连续 N 天未出现才标 `missing`，但一旦标上，依赖它的候选就永久失效 |
| 模型属性变化（上下文 / 模态 / 思考档位） | `upstream_model_changed` | `warn` | 能力守恒是 PLAN 3.4 的下界来源，变了就可能让已有抽象模型的声明失真 |
| `dsh_provider` 连续失败 | `upstream_failing` | `error` | 它一整类都挂了，而管理员看不到 sidecar 内部 |
| `compatible` 端点连不上 / 鉴权失败 | `upstream_failing` | `error` | 管理员自己填的地址，没人会替他发现 |

写入时机是**检测到就立刻写**，不等请求路径上报。理由：`dsh_provider` 可能整类零流量
（管理员还没把任何抽象模型指过去），这时永远等不到“请求失败”，但“目录拉不下来”
已经是确定的事实，不写就是永久静默。

去重规则：同一 `(kind, related_id, title)` 在 `read_at IS NULL` 状态下不重复插入，
只更新 `created_at` 与 `detail_json`。理由：目录抖动会在一个小时内反复触发，
不去重会把待处理页刷成噪声，而噪声等于没有通知。

写入失败（库锁、磁盘满）必须落 `slog` 且**不得影响目录刷新本身**：通知是旁路，
不能让“发不出通知”拖垮“模型目录已经拉到了”这个主结果。

---

## 4. contributed 适配

| 方面 | 约定 |
|---|---|
| 协议 | 默认 OpenAI 兼容（chat 为主）；responses 视凭据能力声明 |
| 地址 | 由凭据贡献时提交的 base_url；**只允许 https**（本地回环测试可显式放行 http） |
| 凭据 | `upstream_secrets.ciphertext` 解密后作 Bearer；**明文只在内存里存在一次请求的时间** |
| 模型目录 | 优先用贡献者声明的清单；若无则调 `/v1/models`（受 L0 形式校验） |
| 归属 | `upstream_credentials.owner_account_id`，必须指向带 `contributor` 类的账户 |

**为什么只允许 https**：贡献凭据是第三方端点。明文 http 会让贡献者的 key 在公网上裸奔，
而这个泄露发生在**我们**的出口上（"贡献凭据被我们泄露"在 PLAN 13 被列为极严重风险）。
唯一的例外是回环地址，用于本地联调。

> **待定**：`contributed` 上游是否允许非 OpenAI 兼容协议（原生 Anthropic / Gemini 端点）。
> 若允许，[02-ir.md](02-ir.md) 的出站映射要按上游协议选择，`Dialect` 需要扩充，
> 且契约测试要覆盖新方言。当前按"只支持 OpenAI 兼容"实现。

---

## 5. 连接池

### 5.1 Transport 参数

| 参数 | 值 | 理由 |
|---|---|---|
| `MaxIdleConns` | 128 | 覆盖众多 `contributed` / `compatible` 端点的并发空闲连接 |
| `MaxIdleConnsPerHost` | 64 | 默认值 2 会让同一 host 的连接反复建拆，放大半开连接问题 |
| `MaxConnsPerHost` | 128 | 给“单上游被打爆”设上限，避免一个坏上游吃光 fd |
| `IdleConnTimeout` | **90s** | 让客户端先淘汰连接，见 5.2 |
| `TLSHandshakeTimeout` | 10s | 公网 contributed 的握手可能慢 |
| `ResponseHeaderTimeout` | 60s | 即 TTFT 上限，见第 8 节 |
| `DialContext` 超时 | 5s | 公网正常 <200ms；5s 已足够区分“慢”与“死” |
| `ExpectContinueTimeout` | 1s | 大 body 时避免长时间等待 |
| `ForceAttemptHTTP2` | true | 上游支持则用；GOAWAY 按第 6 节重试 |
| `DisableCompression` | **true** | 流式响应被 gzip 会让 Flush 延迟到解压缓冲满，破坏 SSE 的实时性 |

**这张表只管 `contributed` 与 `compatible`**。`dsh_provider` 不发 HTTP：它的连接池在 sidecar 进程内，
由 provider adapter 自己管；Go 侧对它只有一条 IPC 管道。把两者写进同一个表会让人误以为
`dsh_provider` 也会掷到这 128 个 fd 里。

### 5.2 为什么 IdleConnTimeout 是 90s

第三方端点（尤其是带 CDN / 反代 / API 网关的）会主动淘汰空闲 keep-alive 连接，实测区间多在
**60–120 秒**。若客户端保留连接的时间长于对端，就会出现：

```
t=0    建立连接，请求成功
t=125  客户端认为连接还活着（自己没设空闲上限或设得更长）
       → 复用该连接 → 对端已 FIN → 写请求时报 EOF / connection reset
       → 表现为“偶发 http=000”
```

把 `IdleConnTimeout` 设为 90s，让**客户端先**淘汰连接。
**为什么是 90s 而不是更贴近 120s**：对端的淘汰时点不可知且各家不同（60s 的也不少），
90s 落在多数实现的保守侧；再留出调度抖动与 GC 停顿的余量。连接池重建一份连接的成本
远低于一次用户可见的失败。`Transport` 的淘汰是惰性的（在取用时判断），90s 已经能保证绝大多数
“取用旧连接”的场景落在安全区内。

**为什么 `dsh_provider` 不适用这一条**：它没有 Go 侧的长连接可复用。sidecar 与官方端点之间的
keep-alive 由 adapter 管，我们既看不到也不能配——这也是 3.1 写明的代价的另一面。

### 5.3 Transport 的复用粒度（HTTP 类上游）

**按 (kind, host) 复用同一个 `*http.Transport`，不同凭据只换请求头。**
| 方案 | 后果 |
|---|---|
| 按凭据拆 Transport | 连接数 = 凭据数 × 每主机连接数；每个新凭据都要冷启动；fd 消耗随贡献者数量线性增长 |
| 按 upstream id 拆 | 同一 host 的不同凭据（同一供应商的多账号）无法共享已建立的 TLS 会话 |
| **按 (kind, host) 复用**（选定） | 连接池效率最高；风险是"一个凭据的限流影响同 host 的其它凭据"——但限流是**上游按账号**判定的，与连接复用无关 |

> **待定**：是否需要给单个凭据设连接数上限（防止一个贡献凭据被大量并发打爆）。
> 当前用 `MaxConnsPerHost` 在 host 级别兜底，逐凭据上限留到有真实滥用数据后再加。

---

## 6. 偶发 `http=000` 的重试策略

### 6.1 现象与归因

PLAN 5.3 记录过一个现象：首次请求偶发拿到 `http=000`（Go 侧表现为 `EOF`、`unexpected EOF`、
`connection reset by peer`、`http2: server sent GOAWAY`），**重试即通**。现象本身与具体上游无关，
凡是复用长连接的客户端都会遇到，所以本节的策略对 `contributed` / `compatible` 继续适用。
归因有四类，处置不同。

| 归因 | 典型表现 | 触发条件 |
|---|---|---|
| 半开连接（服务端已 FIN，客户端未察觉） | 写请求或读首字节时 `EOF` | 空闲超过 120s 的连接被复用 |
| 对端已重启 | `connect: connection refused` | 端点刚重启、端口还没监听 |
| 上游优雅重启 | `GOAWAY` / `stream closed` | 反代 / 网关热更新 |
| sidecar 未就绪（仅 `dsh_provider`） | `E_UNAVAILABLE` / IPC 超时 | sidecar 正在退避重启 |

### 6.2 重试决策表

| 错误类别 | 同候选重试 | 退避 | 之后 |
|---|---|---|---|
| `ErrConnReset` | ≤2 | 50ms → 150ms（+0~50% jitter） | 仍失败 → 换候选 |
| `ErrConnect` | ≤2 | 200ms → 500ms | 仍失败 → 换候选 + 该上游计失败 |
| `ErrTLS` | ≤1 | 200ms | 仍失败 → 换候选 |
| `ErrTimeout`（TTFT 前） | ≤1 | 无（换候选更优） | 换候选 + 计失败 |
| `ErrUpstream5xx` | ≤2 | 100ms → 300ms | 换候选；连续 3 次 → 冷却 |
| `ErrRateLimited`（429） | **0** | —— | 直接换候选 + 该凭据计入冷却（读 `Retry-After`） |
| `ErrQuota` | 0 | —— | 换候选 + 标记凭据额度耗尽 |
| `ErrAuth` | 0 | —— | 换候选 + 该凭据标 `invalid`，通知贡献者 |
| `ErrBadRequest`（4xx 其它） | 0 | —— | **直接返回给客户端**（我们写错了请求，重试无意义） |
| `ErrProtocol` | 0 | —— | 换候选 |
| **已吐可见内容后的任何错误** | **0** | —— | 终止并向客户端发 `error`（C7） |

### 6.3 三条不可放松的规则

1. **重试总预算 ≤ 2 次/候选、≤ 4 次/请求**。理由：我们的上游请求是"生成"语义而非幂等读，
   每次重试都在真实消耗额度；一次用户请求打 6 次上游，在额度成本上不可接受。

2. **只有在 `VisibleWritten == false` 时才允许重试或换候选**。理由：C7（流式串话）。
   判定完全依赖 `Stats().VisibleWritten`，不接受"我觉得还没吐出去"这类推断。

3. **重试与切换的优先级：先同候选重试一次连接类错误，再换候选**。理由：`http=000` 的
   根因是半开连接，同候选用**新建连接**重试的剥离率很高；而换候选会引入"随机换到更差的模型"
   这种语义变化（候选可能有不同的质量档位）。反之，429 / 额度 / 鉴权类错误**不重试**直接换候选，
   因为它们的根因在凭据而不是连接。

### 6.4 可观测

| 记录项 | 位置 | 用途 |
|---|---|---|
| `requests.error_code` | DB | `upstream_conn_reset` 等分类值 |
| `requests.retry_count` | DB | 观察"重试后成功"的比例 |
| 归因日志 | slog | 每次重试记 `upstream_id` / `credential_id` / `attempt` / `class` |
| 健康计数 | 内存 | 驱动第 7 节的冷却状态机 |

**为什么要把"重试后成功"也记下来**：只看最终失败率会低估 `http=000` 的频次。PLAN 11.5 明确
契约测试里要包含"连接偶发失败的复现与重试是否成功"，说明这个数字是持续要看的指标，
而不是上线前查一次就完。

---

## 7. 健康探测

### 7.1 状态机

```
healthy ──(连接类失败窗口超阈值)──▶ cooling ──(冷却到点，半开探测成功)──▶ healthy
   │                                   │
   │                                   └──(探测仍失败)──▶ 冷却时间翻倍，退回 cooling
   └──(连续 5xx / 429)──▶ degraded ──(成功一次)──▶ healthy
```

| 状态 | 是否参与候选展开 | 说明 |
|---|---|---|
| `healthy` | 是，排序正常 | 默认 |
| `degraded` | 是，但**降权**（排序时排后） | 还能用，但优先用别人 |
| `cooling` | **否** | 冷却期不参与，避免把失败打到用户脸上 |
| `disabled` | 否 | 管理员手动静止（PLAN 3.3 过滤条件之一） |

**粒度**：熔断挂在 `upstreams.id` 上，即**一个上游实体**。
- `dsh_provider`：整类一起熔断（它们共享同一个 sidecar 进程与同一份账号池）；
- `contributed`：每个凭据大概率对应用户自己的一条上游实体，逐个熔断；
- `compatible`：每条管理员配置的地址一个实体。

**为什么 `dsh_provider` 不能按 provider 拆分熔断**：拆分的价值是“某家挂了不连坐别家”。
但 `dsh_provider` 整类都经过 sidecar 这一个进程：sidecar 活着时各家能否用取决于账号池
（这一层的信息已经由 adapter 的 `providerRetryPolicy` 与错误分类反映了）；sidecar 死了则谁也
不能用。所以“拆”能区分的场景都已在错误分类里，再拆一层只会让健康状态表变成噪声。
**为什么用"降权"而不是"直接摘除"处理 degraded**：候选池可能本来就只有一两个来源，
摘除等于 503。降权保留可用性，同时让健康候选优先——这正是**策略引擎**排序链里
"健康"这一过滤/排序维度的实现位置。

### 7.2 探测方式

| 对象 | 方式 | 频率 | 为什么 |
|---|---|---|---|
| `dsh_provider` | sidecar 的 `health` + `providers.list` | 健康时 30s；冷却期按退避 | 廉价、无额度成本，且顺带刷新目录 |
| `contributed` / `compatible` | `GET /v1/models`；不支持时仅在冷却后半开用最小请求（`max_tokens=1`） | 目录：健康时 30s；半开：退避驱动 | 目录探测无额度成本；最小请求有成本，不能定期打 |
| 全集 | 进程内被动统计 | 实时 | 被动数据是主要依据；主动探测只用于恢复判定 |

**冷却退避曲线**：30s → 1m → 5m → 15m（上限 15m），任一次成功即复位到 `healthy`。
上限 15m 的理由：再长就等于把该上游永久摘除，而管理员可能根本不知道它出了问题；
15m 保证“坏上游”最多每小时被打 4 次探测，成本可忽略。

**连续失败与恢复都必须写 `admin_inbox`**（ALIGN-SPEC G）：进入 `cooling` 时写一条
`upstream_failing`（`severity = warn`），从 `cooling` 恢复 `healthy` 时把对应的未读项
自动标为已读（`read_at` 与 `read_by` 置为 NULL 表示系统已恢复，而非人工确认）。
**为什么恢复也要写**：只写坏不写好，管理员打开待处理页看到的全是历史噪声，
无法判断“现在到底还有几个坏的”。

### 7.3 `/readyz` 语义

```
/readyz = (sidecar 的 health 就绪) || (至少有 1 个贡献类/自建上游处于 healthy)
```

**为什么 `/readyz` 不能要求全部上游健康**：那会让"某个贡献者的 key 失效"导致整站被反代摘流量。
`/readyz` 的语义应当是“我现在能不能服务**某个**请求”，不是“我的供给是否完整”。

**为什么用「或」而不是「与」把两类拼起来**：`dsh_provider` 与 HTTP 类上游是**两条独立的
物理路径**（2.3），任一条能走就能接请求。若要求两者同时健康，就等于“sidecar 挂了全站下线”
或“没贡献者则全站下线”——前者把一个进程的可用性放大成全站可用性，后者让新部署根本无法启动。
---

## 8. 超时矩阵

| 阶段 | 默认值 | 可配 | 理由 |
|---|---|---|---|
| 入站读 body（含上传图片） | 30s | 是 | 慢客户端防护；公网移动网络下 30s 足够 |
| 上游 TCP 连接（仅 HTTP 类） | 5s | 是 | 公网 contributed 放宽到 10s |
| TLS 握手 | 10s | 是 | 公网 |
| 响应头（TTFT） | 60s | 是 | 大模型首字节可到十几秒；60s 是"明显不对"的界限 |
| 流式事件间隔 | 120s | 是 | 长思考模型可能长时间无输出；与上游 keep-alive 数值巧合但语义无关 |
| 单请求总时长 | 600s | 是 | 编码类 agent 任务可能跑几分钟 |
| 上游空闲连接淘汰（仅 HTTP 类） | 90s | 否 | 见 5.2 |
| 优雅关闭 drain | 30s | 是 | 见 [06-runtime.md](06-runtime.md) |

**为什么 TTFT 与"流式事件间隔"要分开**：TTFT 是"上游还没开始生成"，事件间隔是"上游生成了
但卡住了"。两者混用会造成两种错误：
- 用 TTFT 的 60s 限制事件间隔 → 长思考模型被误杀；
- 用 120s 限制 TWFT → 用户在"上游根本没响应"的情况下干等两分钟。

**为什么总时长限制要远大于上面各项**：它是兜底（防止 goroutine 泄漏），不是质量控制手段。
把总时长压到与时长相称会导致长任务全部失败，而长任务是这个产品的主要用途。

### 8.1 两套超时口径的对应关系（必须对齐）

本表与 [06-runtime.md](06-runtime.md) 第 5 节是**同一套参数的两种视角**，不是两套独立配置：

| 本表（阶段视角） | 06-runtime 第 5 节（层次视角） | 备注 |
|---|---|---|
| 入站读 body 30s | — | 属于 L1 入站，不在上游超时体系内 |
| 上游 TCP 连接 5s | `connect_timeout` 5s | 同一个参数 |
| TLS 握手 10s | 并入 `connect_timeout` 的预算 | `connect + TLS` 共用 5s；contributed 类为 10s |
| 响应头 TTFT 60s | `first_byte_timeout` 60s | 同一个参数 |
| 流式事件间隔 120s | — | **仅本表有**：它管的是“已开始生成后的卡顿”，不是首字节 |
| 单请求总时长 600s | `request_timeout` 600s | 同一个参数 |
| — | `attempt_timeout` 300s | **仅 06 有**：单个候选的尝试上限 |

**`attempt_timeout` 300s 与「≤2 次重试」的关系**：总预算 600s = 1 次首发（≤ 300s）+ 1 次重试（≤ 300s）刚好打满，**这是刻意的**。为什么不在 03 那边也写这行：`attempt_timeout` 不是上游协议属性，而是 `call` 模块的调度参数（“一个候选最多占多久，以便给下一个候选留出预算”），它只对调用方有意义。

**为什么流式事件间隔不在 06 的超时体系里**：06 是“请求生命周期”视角，事件间隔是“流**内部**的进展监控”，实现在 SSE 转发那一层（`VisibleWritten == true` 之后），不是一个独立的 context 超时。把它写成 context 超时会在流式响应中途取消请求，而正确的行为是断开流并附一个错误帧。

---

## 9. 上游硬约束

### 9.1 请求体上限

| 层 | 上限 | 超限行为 | 理由 |
|---|---|---|---|
| 入站 | **32 MiB**（默认，可配） | 413，**不读 body** | 公网 DoS 面；32 MiB ≈ 两张原图 base64 后的大小 |
| 出站（`contributed` / `compatible`） | 与入站同限 | 本来就是同一个 body，不再放宽 | 我们转发的就是入站 body 的重新编码，不存在“上游能收 300 MiB 所以我们也该收”的场景 |
| 出站（`dsh_provider`） | 无独立上限 | —— | 不过 HTTP，由 adapter 直接构造请求；体积约束由入站的 32 MiB 已经卡死 |

**为什么删掉了旧的 300 MiB 一栏**：那个数字属于 providers-hub 的 OpenAI 兼容网关（面向本机受信任客户端），
`dsh_provider` 不再经过网关后这个上限已经没有约束对象。保留它只会造成两种误读：
误以为我们该放大入站上限，或误以为 adapter 会拒 32–300 MiB 的请求。

**为什么入站上限是 32 MiB**：公网 DoS 面与真实需求之间的折中——32 MiB 能装下两张原图 base64
后的大小，而再大的请求在公网多租户环境里只会把带宽、内存、连接时长全部浪费掉，
并且让攻击者可以用一个大 body 拖住一个 goroutine。

**base64 膨胀的计算要对用户可见**：图片内联后体积是原图 4/3。文档与错误信息里要说清楚
"32 MiB 大约等于两张 10 MiB 的原图"，否则用户只会看到莫名其妙的 413。

### 9.2 keep-alive（仅 HTTP 类上游）

- 对端空闲后主动 FIN（各家区间 60–120s）；
- 我方 `IdleConnTimeout = 90s` + `http=000` 重试兜底（第 5、6 节）。

**两层防护都要有**，不能只靠一个：主动淘汰覆盖绝大多数场景，重试覆盖“恰好卡在边界”的场景
（例如连接空闲 118s 时被取用）。`dsh_provider` 不在此节范围内，理由同 5.2 末段。

### 9.3 出站边界原则

旧的“唯一出口原则”（平台自有上游必须在本机）在 `dsh_provider` 改走 sidecar 后**已不再适用于
平台自有上游**：它根本不是一个地址，而是一条进程内调用。但同一份担忧（我们不得成为任意地址的
代理）并未消失，只是换了守卫对象：

| 规则 | 实现位置 | 违反时的行为 |
|---|---|---|
| `dsh_provider` 不持有任何可配地址 | 数据结构：`Target.BaseURL` 对它恒为空 | 写入即视为缺陷（代码约束） |
| `contributed` / `compatible` 的 `base_url` 必须是 `https`，仅回环可显式放行 `http` | 配置校验（启动时 + 写入时） | **拒绝保存 / 拒绝启动**，打印明确原因 |
| 拒绝 `0.0.0.0` / `::` 作为目标主机 | 配置校验 | 同上 |
| 运行时不接受任何“上游地址”来自请求参数 | 代码约束 + review | 视为安全缺陷 |

**为什么把旧规则改名而不保留原名**：旧名让人以为约束对象是“127.0.0.1”，但实际上真正要防的
是**“我们被当成开放代理”**。改名后每一条都能对应到一个具体要防的东西，
而不是一个已经不存在的地址。

---

## 10. 契约测试（防漂移）

providers-hub 升级会漂移（PLAN 12 风险表）。契约测试是唯一能提前发现漂移的手段：

| # | 覆盖 | 断言 |
|---|---|---|
| K1 | `GET /v1/models` | 200；条目含 `id` / `name` / `input` / `reasoning.efforts`；条目数变化**不**作为失败 |
| K2 | 鉴权失败形态 | 无 Bearer / 错 Bearer → 401 且响应体形状稳定 |
| K3 | `POST /v1/chat/completions` 非流式 | 状态码、`choices[0].message` 形状、`usage` 字段 |
| K4 | `POST /v1/chat/completions` 流式 | 帧形状、`[DONE]`、`usage` 出现位置 |
| K5 | 413 行为 | 超限返回 413 且 code 为 `request_too_large` |
| K6 | **偶发连接失败** | 复现 `http=000` 并验证重试成功（PLAN 11.5 明确要求） |
| K7 | `POST /v1/responses` | 事件名与结束语义（P1 起必需） |

**为什么 K1 不断言条目数**：116 个模型是会变的（PLAN 11.1 的自动发现就是为变化设计的）。
把数量写进断言，会让每次上游上下架模型都变成"测试红了"，久而久之没人看测试结果。

---

## 11. 待定项

> **待定**：是否给单个**凭据**（而非 host）设置连接数上限。当前只有 host 级
> `MaxConnsPerHost`，无法阻止一个贡献凭据被大量并发请求打爆（对贡献者是实际损失）。

> **待定**：`contributed` 上游的模型目录以"贡献者声明"为准还是以 `/v1/models` 为准。
> 前者可能造假（正是**鉴定**要防的），后者可能不被支持。当前倾向"声明为主 + L0 形式校验"，
> 但需要与 [../contributions/02-credentials.md](../contributions/02-credentials.md) 对齐。

> **待定**：上游是否需要一个显式的"出口代理"配置（`HTTP_PROXY`）。当前假设
> `dsh_provider` 由 sidecar 内的 adapter 直连官方端点、`contributed` 与 `compatible` 同样直连。
> 若部署环境强制出网代理，需要在 adapter 的 `fetchImpl` 与 Go 侧 Transport 两处统一注入——
> **这是个已知的裂缝**：adapter 的请求不经 Go 的 Transport，所以一个配置管不到两条路。

> **待定**：多节点（P2）下每个节点各自拉起自己的 sidecar（各自持有 vendored hub 副本与
> 各自的 OAuth 回调端口）是否可接受，还是需要独立的 sidecar 服务。当前按"每节点各自拉"设计。
