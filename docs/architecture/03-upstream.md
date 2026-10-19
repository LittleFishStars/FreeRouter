> 分册：架构 · 依赖：[01-overview.md](01-overview.md)、[02-ir.md](02-ir.md)、[06-runtime.md](06-runtime.md) · 对应总纲：[PLAN.md](../PLAN.md) 第 5、6 章

# 上游适配层

本册定义 L3 的对外接口、两类上游（`dshgateway` / `contributed`）的实现约束、连接池与重试、
健康探测、超时矩阵，以及上游侧硬约束（300 MiB body、120s keep-alive、唯一出口）。

---

## 1. 两类上游

| kind | 表 `upstreams.kind` | 目标地址 | 凭据 | 供给来源 |
|---|---|---|---|---|
| `dshgateway` | `"dshgateway"` | `127.0.0.1:8326` | `$DSH_HOME/openai-gateway/api-key`（43 字符），`owner_account_id = NULL` | 平台自有（providers-hub 账号池） |
| `contributed` | `"contributed"` | 凭据自带的第三方端点 | `upstream_secrets.ciphertext`（AES-GCM） | **贡献者**凭据 |

**为什么两者在架构上是同一层而不是两套流程**：从 `call` 模块看，它们都是"拿着一个地址和一个
凭据去做一次生成请求"。差异只有三处——地址从哪来、凭据从哪来、协议方言是什么。把差异收在
`Target` 结构里，重试/切换/超时/计量的代码就只需一份。

**`owner_account_id = NULL` 表示平台自有**（PLAN 4.2）：平台自有凭据与贡献凭据**同构但来源不同**，
这个区分必须保留在数据层，因为：
- 归因时不能把平台额度算成某个贡献者的贡献量；
- 审计时"我们自己的 key 被谁用了"与"贡献者的 key 被用了几次"是两个问题。

---

## 2. 统一接口

```go
type Kind string

const (
    KindDSHGateway  Kind = "dshgateway"
    KindContributed Kind = "contributed"
)

// Dialect 是上游说哪种协议。目前只有 OpenAI 兼容的两种形态。
type Dialect string

const (
    DialectOpenAIChat      Dialect = "openai-chat"
    DialectOpenAIResponses Dialect = "openai-responses"
)

// Target 是"一次尝试"的全部输入：上游 + 凭据实例 + 真实模型 + 协议方言。
type Target struct {
    UpstreamID   int64
    Kind         Kind
    BaseURL      string
    CredentialID int64  // 0 = 平台自有凭据
    SecretRef    string // 平台自有：api-key 文件路径；贡献：upstream_secrets.id
    RealModel    string // 上游模型 id（如 deepseek-official/deepseek-flash）
    Dialect      Dialect
}

type Upstream interface {
    // Probe 探测可达性与鉴权是否有效。必须廉价。
    Probe(ctx context.Context) ProbeResult

    // ListModels 拉取上游模型目录。仅 dshgateway 与支持该端点的 contributed 上游实现。
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
| `Target` 里有 `Dialect` | 出站编码按**上游**协议而非入站协议选择。`dshgateway` 同时提供 chat 与 responses 两个端点，选哪个由候选声明决定 |

### 2.2 错误分类

```go
type ErrClass string

const (
    ErrConnReset    ErrClass = "conn_reset"    // 连接被重置 / 半开连接（http=000）
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

---

## 3. dshgateway 适配

### 3.1 引导与注入

| 步骤 | 动作 | 失败的后果 |
|---|---|---|
| 1 | 读 `$DSH_HOME/openai-gateway/api-key` | 文件缺失 → 控制台挂横幅，`dshgateway` 标记不可用；**不阻塞启动** |
| 2 | AES-GCM 加密后写 `upstream_secrets` + `upstreams`（`owner_account_id = NULL`） | 写库失败 → 记错误，下次启动重试 |
| 3 | `GET /v1/models` 拉目录 → 写 `real_models`（`source = auto`） | 失败 → 每 30s 重试 |
| 4 | 缓存目录（TTL 10 分钟），供能力匹配使用 | 缓存过期时先用旧值，避免目录抖动影响转发 |

**为什么目录要有 TTL 与旧值兜底**：目录决定能力过滤，直接影响可用性。若每次请求都实时拉目录，
上游抖动会被放大成"全站 503"；TTL + 旧值让抖动只影响"新模型的可见性"这种可延迟的事情。

**为什么 `source = auto` 的条目在管理员改过之后记 `override` 且刷新不覆盖**（PLAN 11.1）：
管理员的重命名 / 分组 / 隐藏是人工判断的结果，被自动刷新覆盖等于让人白干。

### 3.2 依赖的端点（契约面）

只依赖 4 个 HTTP 端点，不依赖 providers-hub 的模块导出（PLAN 5.5）：

| 端点 | 用途 | 契约锁什么 |
|---|---|---|
| `GET /v1/models` | 目录发现 | 状态码、条目字段（`id` / `name` / `input` / `reasoning.efforts`） |
| `GET /v1/reasoning-efforts` | 思考档位（可选增强） | 状态码与形状；**不可用时降级为不提供档位过滤** |
| `POST /v1/chat/completions` | 主路径 | SSE 帧形状、`usage` 位置、`[DONE]` |
| `POST /v1/responses` | Responses 入站的出站形态 | 事件名与结束语义 |

**为什么列 `reasoning-efforts` 却标注"可选增强"**：档位集合也能从目录条目里的
`reasoning.efforts` 拿到。单独端点的价值在于"目录没给全时补全"，所以它的失败**不能**
让请求失败——否则我们给自己加了一个非必需的上游依赖。

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
| `MaxIdleConns` | 128 | 覆盖 17+ 家 provider 的并发空闲连接 |
| `MaxIdleConnsPerHost` | 64 | 默认值 2 会导致 8326 的连接反复建拆，放大半开连接问题 |
| `MaxConnsPerHost` | 128 | 给"单上游被打爆"设上限，避免一个坏上游吃光 fd |
| `IdleConnTimeout` | **90s** | **必须 < 上游的 120s**，见 5.2 |
| `TLSHandshakeTimeout` | 10s | 公网 contributed 的握手可能慢 |
| `ResponseHeaderTimeout` | 60s | 即 TTFT 上限，见第 8 节 |
| `DialContext` 超时 | 5s | 回环上游正常 <10ms；5s 已足够区分"慢"与"死" |
| `ExpectContinueTimeout` | 1s | 大 body 时避免长时间等待 |
| `ForceAttemptHTTP2` | true | 上游支持则用；GOAWAY 按第 6 节重试 |
| `DisableCompression` | **true** | 流式响应被 gzip 会让 Flush 延迟到解压缓冲满，破坏 SSE 的实时性 |

### 5.2 为什么 IdleConnTimeout 必须小于 120s

providers-hub 的空闲 keep-alive 是 **120 秒**后服务端发 FIN（PLAN 5.3）。若客户端保留连接的
时间超过服务端，就会出现：

```
t=0    建立连接，请求成功
t=125  客户端认为连接还活着（自己没设空闲上限或设得更长）
       → 复用该连接 → 服务端已 FIN → 写请求时报 EOF / connection reset
       → 表现为"偶发 http=000"
```

把 `IdleConnTimeout` 设为 90s，让**客户端先**淘汰连接，比服务端快 30s。
**为什么是 90s 而不是 119s**：留出调度抖动与 GC 停顿的余量；连接池重建一份连接的成本远低于
一次用户可见的失败。`Transport` 的淘汰是惰性的（在取用时判断），90s 已经能保证绝大多数
"取用旧连接"的场景落在安全区内。

### 5.3 Transport 的复用粒度

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

实测：首次带 Bearer 的请求可能拿到 `http=000`（Go 侧表现为 `EOF`、`unexpected EOF`、
`connection reset by peer`、`http2: server sent GOAWAY`），**重试即通**（PLAN 5.3）。
归因有三类，处置不同：

| 归因 | 典型表现 | 触发条件 |
|---|---|---|
| 半开连接（服务端已 FIN，客户端未察觉） | 写请求或读首字节时 `EOF` | 空闲超过 120s 的连接被复用 |
| 上游进程尚未就绪 | `connect: connection refused` | DSH 刚重启、8326 还没监听 |
| 上游优雅重启 | `GOAWAY` / `stream closed` | providers-hub 热更新 |

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
- `dshgateway`：整条 8326 通道一起熔断（它就是一个进程）；
- `contributed`：每个凭据大概率对应用户自己的一条上游实体，逐个熔断。

**为什么用"降权"而不是"直接摘除"处理 degraded**：候选池可能本来就只有一两个来源，
摘除等于 503。降权保留可用性，同时让健康候选优先——这正是**策略引擎**排序链里
"健康"这一过滤/排序维度的实现位置。

### 7.2 探测方式

| 对象 | 方式 | 频率 | 为什么 |
|---|---|---|---|
| `dshgateway` | `GET /v1/models` | 健康时 30s；冷却期按退避 | 廉价、无额度成本，且顺带刷新目录 |
| `contributed` | 仅在冷却后半开时，用最小请求（`max_tokens=1`） | 退避驱动 | 有额度成本，不能定期打 |
| 全集 | 进程内被动统计 | 实时 | 被动数据是主要依据；主动探测只用于恢复判定 |

**冷却退避曲线**：30s → 1m → 5m → 15m（上限 15m），任一次成功即复位到 `healthy`。
上限 15m 的理由：再长就等于把该上游永久摘除，而管理员可能根本不知道它出了问题；
15m 保证"坏上游"最多每小时被打 4 次探测，成本可忽略。

### 7.3 `/readyz` 语义

```
/readyz = (dshgateway 的 Probe 成功) && (至少有 1 个 contributed 上游处于 healthy)
          || (dshgateway 成功)      // contributed 全部冷却不影响"还能服务"的事实
```

**为什么 `/readyz` 不能要求全部上游健康**：那会让"某个贡献者的 key 失效"导致整站被反代摘流量。
`/readyz` 的语义应当是"我现在能不能服务**某个**请求"，不是"我的供给是否完整"。

---

## 8. 超时矩阵

| 阶段 | 默认值 | 可配 | 理由 |
|---|---|---|---|
| 入站读 body（含上传图片） | 30s | 是 | 慢客户端防护；公网移动网络下 30s 足够 |
| 上游 TCP 连接 | 5s | 是 | 回环应 <10ms；公网 contributed 放宽到 10s |
| TLS 握手 | 10s | 是 | 公网 |
| 响应头（TTFT） | 60s | 是 | 大模型首字节可到十几秒；60s 是"明显不对"的界限 |
| 流式事件间隔 | 120s | 是 | 长思考模型可能长时间无输出；与上游 keep-alive 数值巧合但语义无关 |
| 单请求总时长 | 600s | 是 | 编码类 agent 任务可能跑几分钟 |
| 上游空闲连接淘汰 | 90s | 否 | 见 5.2，必须 < 120s |
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
| 上游 `dshgateway` | 300 MiB | 413 `request_too_large` | providers-hub 的硬上限（PLAN 5.3） |

**为什么入站必须显著小于上游**：上游给 300 MiB 是因为它面向"本机受信任客户端"，
而我们是公网多租户。等 body 传完再被上游拒绝，等于把带宽、内存、连接时长全部浪费掉，
并且让攻击者可以用一个大 body 拖住一个 goroutine。

**base64 膨胀的计算要对用户可见**：图片内联后体积是原图 4/3。文档与错误信息里要说清楚
"32 MiB 大约等于两张 10 MiB 的原图"，否则用户只会看到莫名其妙的 413。

### 9.2 keep-alive

- 上游 **120s** 空闲后 FIN（PLAN 5.3）；
- 我方 `IdleConnTimeout = 90s` + `http=000` 重试兜底（第 5、6 节）。

**两层防护都要有**，不能只靠一个：主动淘汰覆盖绝大多数场景，重试覆盖"恰好卡在边界"的场景
（例如连接空闲 118s 时被取用）。

### 9.3 唯一出口原则

| 规则 | 实现位置 | 违反时的行为 |
|---|---|---|
| `dshgateway.base_url` 的 host 必须 ∈ `{127.0.0.1, ::1, localhost}` | 配置校验（启动时） | **拒绝启动**，打印明确原因 |
| 拒绝 `0.0.0.0` / `::` / 公网 IP / 普通域名 | 配置校验 | 同上 |
| 运行时不接受任何"上游地址"来自请求参数 | 代码约束 + review | 视为安全缺陷 |

**为什么配置层面就拒绝而不是"连不上再说"**：providers-hub 只绑回环、拒绝通配地址（PLAN 5.2）。
若允许写一个公网地址，失败会推迟到第一次真实请求，表现为"上游不可用"——运维会去查网络、
查防火墙、查 DSH，而真正的原因是一个永远不可能连通的配置。**前置拒绝把错误提前到最便宜的位置。**

**`contributed` 不受此限**：它的目标本来就是第三方 HTTPS 端点，这是**出站**流量。
唯一出口原则约束的是 `dshgateway` 这条"上游必须在本机"的通道，不是"我们不许出站"。

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
> `dshgateway` 永在回环不需要代理、`contributed` 直连。若部署环境强制出网代理，
> 需要在 Transport 层统一注入，并保证代理不改变唯一出口原则。

> **待定**：多节点（P2）下每个节点各自连接 8326 是可接受的（同机多进程），
> 还是需要独立的网关侧连接复用服务。当前按"每节点各自连"设计。
