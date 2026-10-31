> 分册：架构 · 依赖：[01-overview.md](01-overview.md)、[03-upstream.md](03-upstream.md)、[06-runtime.md](06-runtime.md) · 对应总纲：[PLAN.md](../PLAN.md) 第 5.5 节

# Node sidecar

sidecar 是一个由 FreeRouter 主进程拉起的**独立 Node 进程**，用来复用 providers-hub 里那些
"登录 / 续期 / 余额 / 目录"的实现。本册定义它与主进程之间的 IPC 契约、生命周期、三条红线，
以及 17 家 provider 的登录流分类。

---

## 1. 为什么是进程级 sidecar

### 1.1 被否掉的方案

| 方案 | 否掉的理由 |
|---|---|
| 在 Go 里重写所有渠道登录流 | 17 家 × 各自的 OAuth/PKCE/扫码/短信方言，工作量与后续维护成本都不可接受；且这些流程会随上游改版而失效，重写等于永久背上维护负担 |
| 用 Go 直接 import providers-hub 的 TS | 跨语言，不可行 |
| 把 sidecar 代码作为库 `import` 进 DSH 进程 | providers-hub 的 `package.json` 只导出 `.` 与 `./client`，深路径 import 会随版本漂移（PLAN 5.5）；更严重的是会与 DSH 进程内的模块状态冲突（同一个 `jet-hub-store` 被加载两次，缓存与锁各说各话） |
| 复用 DSH 的插件机制（写一个 cordis 插件） | 那等于让 FreeRouter 的代码跑在 DSH 的生命周期里。DSH 重启、升级、profile 切换都会影响我们；且我们要用 Go 写主体，无法只把一半逻辑塞进 TS 插件 |

**选定：独立 Node 进程 + 本机 IPC。** 进程边界带来三件必须自己承担的事（本册其余部分就是在处理它们）：
谁拉起来、怎么说话、边界怎么守。

### 1.2 代价与对策

| 代价 | 对策 |
|---|---|
| 多一个进程要管生命周期 | 第 5 节的启动顺序与退避重启 |
| 跨进程调用会失败、会超时 | 第 2 节的显式错误码 + [06-runtime.md](06-runtime.md) 的 context 传播 |
| 边界可能被越写越松（"顺手让它写回凭据吧"） | 第 4 节的红线 + 两个专用错误码做守卫 |

---

## 2. IPC 契约

### 2.1 传输

**默认：stdio + NDJSON（每行一个 JSON 对象）。**

```
→ {"id":1,"method":"credentials.resolve","params":{"credentialRef":"dsh:buddy:0"}}
← {"id":1,"ok":true,"result":{"value":"***","source":"keychain"}}
← {"id":2,"ok":false,"error":{"code":"E_UPSTREAM_TIMEOUT","message":"...","retryable":true}}
```

| 决策 | 理由 |
|---|---|
| **stdio 而非 Unix socket** | 进程边界与通道边界重合：sidecar 一死，管道立刻 EOF，主进程第一时间知道，不需要额外的存活探测。Unix socket 会留下**孤儿监听**——sidecar 崩溃后 socket 文件还在，重连会连到一个不存在的进程或一个僵尸无人应答的 fd |
| **不用端口** | 端口会与"唯一出口原则"的直觉打架（第 4 节红线③），也会带来被本机其它进程连上的风险 |
| 字段命名借用 JSON-RPC 2.0 的 `id` / `method` / `params` / `result` / `error` | 降低两端实现成本；但**不实现完整的 JSON-RPC 2.0**（无 batch、无 notification），因为我们没有这两类需求，实现它只会带来没被测试覆盖的分支 |
| `id` 单调递增，由主进程分配 | 单 sidecar 进程下最简单；sidecar 只做回填，不主动发消息（**sidecar 是无状态一问一答的**） |
| 一行一帧，UTF-8，禁止内嵌换行 | 流式解析必须是"读一行 = 一个完整对象"；`JSON.stringify` 天然保证不产生裸换行 |

**为什么 sidecar 不主动推送**：一旦允许服务端主动发消息，就要处理"推送到达时主进程正在等另一个
应答"的并发问题，还要定义订阅与退订。我们的所有需求（登录状态、余额、目录）都是**轮询**
语义（`login.poll`），不需要推送。

> **待定**：是否需要在多实例部署（P2）下让多个主进程共用一个 sidecar（需要改为 Unix socket +
> 0600 权限 + 并发连接管理）。当前按"每个主进程一个 sidecar"设计。

### 2.2 方法清单

所有方法**同步一问一答**，参数与返回都是 JSON 对象。

```ts
// —— 探测 ——
health() -> {
  version: string,            // sidecar 自身版本
  providers: string[],        // 已装载的 provider id
  deps: { name: string, ok: boolean }[],  // B 层依赖是否可解析
  stateReadonly: true
}

// —— 目录 ——
providers.list() -> {
  provider: string,
  displayName: string,
  loginKind: "browser_callback" | "device_code" | "qr_scan" | "sms" | "plain_credential",
  loginImplemented: boolean,   // false 时 login.start 返回 E_NOT_IMPLEMENTED
  hasAccount: boolean          // 仅表示"存在元数据"，不含凭据值
}[]

models.list({ provider: string }) -> {
  upstreamModelId: string,
  displayName: string,
  contextWindow: number | null,
  modalities: string[],
  efforts: string[]
}[]

// —— 凭据（只读） ——
credentials.resolve({ credentialRef: string }) -> 
  { value: string, source: string } | null   // null = 不存在（对齐 DSH：空值视为不存在）

credentials.describe({ credentialRef: string }) -> {
  provider: string,
  externalLabel: string | null,
  expiresAt: string | null,     // RFC3339
  refreshable: boolean
}

// —— 登录流 ——
login.start({ provider: string }) -> {
  sessionId: string,
  kind: "browser_callback" | "device_code" | "qr_scan" | "sms",
  instructions: string,        // 给用户看的一句话（中文）
  verificationUrl: string | null,  // 设备码：用户要去访问的地址
  userCode: string | null,         // 设备码：用户要输入的码
  qrData: string | null,           // 扫码：二维码内容（由前端渲染）
  callbackPort: number | null,     // 浏览器回调：本地监听端口
  expiresAt: string
}

login.poll({ sessionId: string }) -> {
  status: "pending" | "awaiting_input" | "ok" | "failed" | "expired",
  credentialRef: string | null,   // status=ok 时给出
  secretRef: string | null,       // status=ok 且 kind=sms 等需要额外口令时
  detail: string | null           // 失败原因（面向用户的中文）
}

login.submit({ sessionId: string, input: string }) -> { status: string }  // 短信/验证码类
login.cancel({ sessionId: string }) -> {}

// —— 余额 ——
credits.query({ provider: string, credentialRef: string }) -> {
  balance?: number,
  quotaUsed?: number,
  quotaTotal?: number,
  unit?: string,
  raw?: unknown     // 上游原始响应的子集，仅供排障
}

// —— 推理（2026-10-31 起对「贡献凭据 + 专有协议」渠道可用） ——
// credential 是主进程解密后的凭据原文（完整 JSON 对象，单帧传入、只读使用、
// 不落盘——红线①的推理侧镜像）；带它时 oauth 形状 adapter 的
// resolveCredential/refresh 钩子接到这份凭据上（「单凭据账号池替身」）。
// stream=false 时 sidecar 把 chunk 流攒回完整 chat.completion（relay 的
// 非流式消费面要 JSON 体）。
//
// **覆盖面（2026-10-31 批量移植；11-01 补 buddy 系）**：凡 hub 有 adapter 且
// sidecar 清单声明 `kind: relay` 的渠道（qoder / qodercn / minimax /
// minimaxintl / trae / zcode / autoclaw / gemini / accio / buddy /
// workbuddy），贡献凭据的推理都走本端点——落库行
// 即为 relay 形态（sidecar://<provider>），不再 noOpenAI 停用。续期钩子按
// 渠道接了实测原语（qoder 系 wasm 链路 / minimax 的 refresh_token 换新 /
// autoclaw 的签名续期 / gemini、accio 走 registry 登记），未登记的降级为
// no-op（主动续期窗口沿用当前凭据，真过期走重新授权）。账号池替身必须
// 返回 null 而不是 undefined——adapter 的换号判据是 `next === null`，返回
// undefined 会在换号路径上 TypeError（qoder 实测）。已落库的 vendor 老行由
// 启动时的 OAuth 行对齐（app.reconcileOAuthUpstreams）原地翻转，enabled
// （属主的停止贡献开关）不动。
infer.stream({ provider, model, body, stream, credential? }) -> {
  sse?: string,          // stream=true：OpenAI chat.completion.chunk 的 SSE 全文
  completion?: object    // stream=false：合成的完整 chat.completion
}

// —— 签到（2026-10-31；上游业务写，不写凭据存储——红线①不变） ——
// credential 是已解密明文（与 credits.query 的 Go 侧二分一致：反代型 JSON
// 对象 / API 型裸 key——后者直接回 unsupported，没有签到活动）。
credits.claim({ provider: string, credential: unknown }) -> {
  ok: true,
  outcome: {
    kind: "claimed" | "already-claimed" | "inactive" | "failed",
    credit?: number,      // 仅 claimed
    unit?: string,        // "credit" | "token"，缺省按积分
    message?: string,     // already-claimed / inactive / failed 的原因
    code?: number,        // 仅 failed（-1 = 本地归一）
    covers_today?: boolean // false = 渠道自标「看到的是上一轮痕迹」，不得记「今天已处理」
  },
  unsupported?: boolean,  // 渠道没有签到能力（不发上游请求就判定）
  refreshed_credential?: object  // 领取轮换出的新凭据；写回密文是 Go 桥的责任
}
```

**定时刷新全部余额（2026-11-01）**：Go 侧 `balance_sync.go` 的调度循环按周期
（默认 60 分钟，管理面可改 5 分钟–24 小时）遍历**全部在册凭据**，逐条调用同一个
`credits.query`（与凭据池页按行「刷新余额」是同一份实现，落库口径不会分叉）。
串行执行——`credits.query` 走 sidecar 的单条 stdio 管道，并发会让各家风控看到
突发请求；单条上限 30s，一轮最坏几分钟，下一 tick 因 busy 跳过。开关与周期存
settings（`balance_sync_enabled` / `balance_sync_interval_minutes`），最近一轮
摘要存 `balance_sync_last_run`。端点：`/api/admin/balance-sync/{run,status,enabled,interval}`。

### 2.3 方法集合的边界（重要）

| **不存在**的方法 | 为什么不存在 |
|---|---|
| `credentials.set` / `credentials.unset` | **红线①**（第 4 节）。一旦存在，某天就会有人用它"顺手把刷新后的 token 写回去"，而那次写入会与 DSH 自己的续期竞争 |
| `credentials.refresh` | 续期必然要写回凭据存储，同红线①。**后果**：凭据的续期只能由 DSH 完成，我们只能读最新值。读到 `expiresAt` 已过 → 标 `status=expired` 并从候选中剔除，同时提示贡献者去 DSH 里重新登录 |
| `state.read` / `state.write` | **红线②**（第 4 节）。`state.json` 是 DSH 的运行状态（账号列表、`disabledModels`、`aggregateOrder` 等），与它共享状态就是承认耦合 |
| 任何"打开一个 HTTP 端口让 Go 侧代理推理"的方法 | **不进过本机 HTTP 网关**（ALIGN-SPEC H / 红线③）。sidecar **确实参与推理链路**（它就在链路上：Go → IPC → adapter → 官方端点），但那条链路上没有 HTTP 监听端口——推理走进程内 `import()`，数据经 stdio 管道搬运。若提供一个 `:8326` 那样的收口层，就等于把推理又变成"一个可被别人直接敲的 OpenAI 端点"，且会抹掉 provider 账号归因（网关只认一个 Bearer） |

**登录流拿到凭据之后怎么办**：`login.poll` 返回 `credentialRef` 与取值入口，**由贡献者在控制台
确认后，凭据存进 FreeRouter 自己的 `upstream_secrets`**（AES-GCM），不回写 DSH。
这样红线①②同时成立：我们借用它的登录实现，但不介入它的状态。

> **待定**：`login.start` 返回的凭据如何"交付"给贡献者最安全。当前设计是控制台内一次性展示 +
> 直接写入 `upstream_secrets`，用户看不到明文；但若用户需要把同一个凭据也用于自己的 DSH，
> 就需要一次显式的一次性展示。安全面与可用性的取舍未定。

### 2.4 错误码

| code | 含义 | retryable | 处理 |
|---|---|---|---|
| `E_BAD_REQUEST` | 参数不合法 | 否 | 报 400 到客户端 |
| `E_UNKNOWN_METHOD` | 方法不存在（版本不匹配） | 否 | 记错误；提示 sidecar 版本落后 |
| `E_BUSY` | sidecar 内部并发槽已满 | 是（短退避） | 重试一次 |
| `E_NOT_LOGGED_IN` | provider 尚无账号 | 否 | 引导用户走 `login.start` |
| `E_LOGIN_IN_PROGRESS` | 该 provider 已有登录会话 | 否 | 先 `login.cancel` |
| `E_LOGIN_FAILED` | 登录被拒绝 | 否 | 展示 `detail` |
| `E_LOGIN_TIMEOUT` | 会话过期 | 是（新会话） | 重新 `login.start` |
| `E_SUBMIT_REJECTED` | 短信/验证码校验失败 | 是（限次） | 让用户重输 |
| `E_NO_BROWSER` | 无图形环境或无法打开浏览器 | 否 | 提示改用设备码类 provider |
| `E_CREDENTIAL_MISSING` | `credentialRef` 不存在 | 否 | 凭据标 `invalid` |
| `E_CREDENTIAL_EXPIRED` | 已过期且需 DSH 续期 | 否 | 标 `expired`，通知贡献者 |
| `E_UPSTREAM_HTTP` | 上游非 2xx（附 status） | 视状态而定 | 按 [03-upstream.md](03-upstream.md) 第 6 节分类 |
| `E_UPSTREAM_TIMEOUT` | 上游超时 | 是 | 重试一次 |
| `E_RATE_LIMITED` | 上游 429 | 是（读 `Retry-After`） | 冷却 |
| `E_DEPENDENCY_MISSING` | B 层依赖缺失（`@deepseek-ai/*` 未装） | 否 | `/healthz` 里报为 degraded；需运维处理 |
| `E_NOT_IMPLEMENTED` | 该 provider 的此能力未实现 | 否 | 控制台隐藏入口 |
| **`E_CREDENTIAL_READONLY`** | **请求试图写凭据** | 否 | **红线告警**（见下） |
| **`E_STATE_FORBIDDEN`** | **请求试图访问 `state.json`** | 否 | **红线告警** |
| `E_PROTOCOL` | 帧解析失败 / id 不匹配 | 否 | 记错误；计入协议违规 |
| `E_INTERNAL` | 未分类 | 视情况 | 记错误 |

**为什么把 `E_CREDENTIAL_READONLY` / `E_STATE_FORBIDDEN` 做成真实存在的错误码，而不是"根本不会
发生所以不用写"**：这两个码是**守卫**。红线靠"大家都记得不要写"是守不住的，靠"违规调用会拿到
一个专用错误码、并触发审计与告警"才守得住。它们出现在日志里就意味着代码里多了一个违规调用点，
这正是我们想在最早期知道的事。

---

## 3. 契约的稳定性

| 规则 | 理由 |
|---|---|
| 方法**只加不改** | sidecar 可以独立于主进程升级（providers-hub 走 npm 版本），旧 sidecar 配新主进程必须能工作 |
| `health` 必须报告 `version` 与 `providers` | 主进程据此判断"能不能用"，而不是靠试调用 |
| 未知返回字段必须被主进程忽略 | 让 sidecar 能先行增加信息（如 `credits.query` 新增字段） |
| 未知 `error.code` 一律按 `E_INTERNAL` 处理并记原文 | 避免因新增错误码导致主进程崩溃 |
| sidecar 不感知"FreeRouter 的业务概念" | 参数里不出现 `abstract_model` / `api_key` / `account_id`，只有 provider 与 credentialRef |

**最后一条尤其重要**：一旦 IPC 里出现业务概念，sidecar 就从"上游能力的适配器"变成了"业务的
一半实现"，而它跑在 Node 里、不受 Go 侧的类型与测试保护。

---

## 4. 三条红线

### 4.1 定义与实现手段

| # | 红线 | 实现手段 | 违反的检测 |
|---|---|---|---|
| ① | **只读凭据** | 不提供 `set/unset/refresh` 方法；sidecar 内部只调用 `ctx.credentials.resolve` / `describe`；FreeRouter 侧不写 DSH 的凭据存储 | `E_CREDENTIAL_READONLY` + 审计告警 |
| ② | **不碰 `$DSH_HOME/jet-hub/state.json`** | 不提供任何读写方法；sidecar 以只读方式装载 providers-hub 模块，且不调用会触及该文件的路径 | `E_STATE_FORBIDDEN` + 文件 mtime 监控（启动时记录 mtime，运行期变化即告警） |
| ③ | **推理经 sidecar 直连 adapter，不经任何本机 HTTP 网关** | sidecar 进程内 `import()` providers-hub 的 `lib/*-adapter.js`，由它向官方端点发起推理；FreeRouter 的 Go 侧不直接连接任何 LLM 端点 | 代码审查 + 契约中只有 `chunk` 流式帧、无 HTTP 代理方法 |

### 4.2 红线③的精确边界

红线说的是**推理链路不经过任何本机 HTTP 网关**（没有 8326 那一跳，也没有我们自己的代理层），
不是"sidecar 不许出公网"。事实上 sidecar 必须出公网：既要访问 OAuth 授权端点（`github.com` /
`*.openai.com` / 各家自己的域名）、设备码端点、余额端点，也要承载模型推理本身。

| 出站类型 | 由谁发起 | 允许 | 理由 |
|---|---|---|---|
| OAuth / 设备码 / 余额 / 目录端点（公网） | sidecar | 允许 | 登录与配额探测本身 |
| 模型推理（公网，官方端点） | **sidecar 内的 adapter** | 允许 | adapter 是唯一知道各家请求形状的代码；搬到 Go 侧就等于把它重写一遍 |
| 本地回调监听 `127.0.0.1:<port>` | sidecar | 允许（仅回环） | OAuth 回调标准做法；监听非回环地址被配置校验拒绝 |
| 本机 HTTP 网关（如 8326） | —— | **禁止** | 多一层网关就多一份凭据副本与一个故障点；adapter 已经能直达官方端点 |

### 4.3 为什么红线是"三条"而不是"零信任"

如果目标是"完全不让 sidecar 碰敏感面"，唯一彻底的做法是不做 sidecar。三条红线是**可执行**的
边界，每条都对应一个具体的失败模式：

- 违反① → 我们与 DSH 的续期竞争写同一个凭据，可能把对方刚刷新的 token 覆盖成旧的（refresh
  token rotation 互踢，RESEARCH 已列为未验证风险）；
- 违反② → 两个进程对同一个 JSON 文件做读改写，DSH 的账号列表可能被我们的旧快照覆盖；
- 违反③ → providers-hub 只绑回环、拒绝通配地址，代理到别处必然失败，且会把"上游必须在本机"
  这条信任边界打破。

---

## 5. 生命周期

### 5.1 启动顺序（Go 侧）

| # | 步骤 | 失败时的行为 |
|---|---|---|
| 1 | 加载配置，校验唯一出口（[03-upstream.md](03-upstream.md) 9.3） | **拒绝启动** |
| 2 | 打开 SQLite，执行迁移 | 拒绝启动 |
| 3 | 校验 `sidecar.hub_path`（随包副本 `hub_path`）可读、Node 版本满足 | 挂横幅，继续（不阻塞） |
| 4 | **拉起 sidecar 进程**，等待 `health` 应答（超时 **10s**） | 挂横幅，继续；vendor 全部视为不可用 |
| 5 | 异步拉 `GET /v1/models` 写 `real_models` | 每 30s 重试 |
| 6 | 启动健康探测循环（[03-upstream.md](03-upstream.md) 第 7 节） | —— |
| 7 | 开始监听，`/readyz` 开始返回 | —— |

**为什么步骤 4 失败**不阻塞启动**：`relay` 是主供给（providers-hub 账号池 + 上百个模型的额度），
它不依赖 sidecar。sidecar 只影响 `vendor` 上游与"贡献/登录"这条支线。为了一个支线
让整站起不来，是把 SLA 绑在了最不重要的一环上。

**为什么 `health` 超时是 10s 而不是 60s**：Node 冷启动 + 加载 providers-hub 模块通常 < 3s。
10s 已经足够区分"慢"与"卡住"，而更长的等待会让启动看起来很卡（用户会以为进程挂了）。

**为什么不把 sidecar 的 `health` 接进 `/readyz`**：见 [03-upstream.md](03-upstream.md) 7.3——
`/readyz` 的语义是"能不能服务某个请求"，`relay` 可用就够了。sidecar 的健康状况通过
`/healthz` 的明细与横幅暴露。

### 5.2 运行期

| 事件 | 处理 |
|---|---|
| sidecar 退出（任意信号） | 主进程按退避重启：1s → 2s → 5s → 15s → 30s → 60s（上限） |
| 连续 **5 次**启动失败 | 进入 `sidecar_degraded`：停止重启，横幅提示，vendor 全部冷却；**等管理员干预** |
| 主进程有在途 IPC 调用时 sidecar 死 | 所有在途调用立即以 `E_UPSTREAM_…`（或 `E_PROTOCOL`）失败。若该请求尚未产出可见内容，按 [06-runtime.md](06-runtime.md) 的切换状态机换候选（通常是换到 `relay` 或 `selfhosted` 的候选） |
| 单次调用超时 | 见 5.3；超时不代表 sidecar 死了，只记 `warn` |
| 协议违规（`E_PROTOCOL`） | 记错误计数；**连续 3 次**视为 sidecar 失能，主动重启它 |

**为什么要有"连续 5 次失败就停"**：Node 不在、依赖缺失、provider 模块加载抛异常——这些是
**确定性失败**，重启一万次也不会好。无限重启的代价是日志被刷爆、CPU 被吃满，而且掩盖了真正的
错误信息（人会先去翻最后那一屏）。

**为什么协议违规要主动重启**：`E_PROTOCOL` 意味着帧解析失败或 `id` 对不上，这通常是 sidecar
内部状态已经乱了（例如某个登录流卡在半途）。重启是唯一能保证它回到干净状态的手段。

### 5.3 调用超时

| 方法类 | 超时 | 理由 |
|---|---|---|
| `health` / `providers.list` | 3s | 纯内存操作 |
| `credentials.resolve` / `describe` | 5s | 可能触发底层安全存储读取 |
| `models.list` / `credits.query` | 15s | 可能走公网 |
| `credits.claim` | 60s | 预检 + 领取两趟公网请求（内部还可能重试） |
| `login.start` / `login.poll` / `login.submit` | 10s | 轮询会被高频调用，必须快；慢的一方是用户而不是我们 |
| `login.cancel` | 3s | 只是清理本地会话 |

超时后主进程**不**取消 sidecar 侧的操作（我们无法真正中断 Node 里的第三方登录流），
只是不再等待。因此 `login.start` 的会话必须在 sidecar 里有自己的过期时间（`expiresAt`），
由 sidecar 自己清理，否则会泄漏会话。

### 5.4 关闭顺序

| # | 步骤 | 超时 |
|---|---|---|
| 1 | 停止接受新连接，`/readyz` 开始返回 503 | 立即 |
| 2 | drain 在途请求（[06-runtime.md](06-runtime.md)） | 30s |
| 3 | 拒绝新的 IPC 调用；等在途 IPC 调用返回 | 5s |
| 4 | 向 sidecar 发 `shutdown`（一条特殊帧），等它自行退出 | 3s |
| 5 | `SIGTERM` | 2s |
| 6 | `SIGKILL` | —— |

**为什么需要步骤 4**：`login.*` 类方法可能在 Node 侧持有定时器与监听端口（浏览器回调监听）。
直接 `SIGTERM` 会让 Node 有机会跑 `exit` 钩子但不是所有流程都注册了；显式 `shutdown` 让
"进程还在但我们不要它了"这个状态可以被 sidecar 感知并主动清理（关监听端口、清会话）。

**为什么最后还是留 `SIGKILL`**：不能因为 sidecar 不配合就让主进程永远退不出去。退出顺序必须
有终点，否则 systemd 的 `TimeoutStopSec` 会来杀我们（PLAN 13 的单点进程风险对策依赖
`Restart=always` + 优雅 drain，前提是"能退出"）。

---

## 6. 17 家 provider 的登录流分类

### 6.1 分类的意义

登录流分四类（PLAN 要求覆盖：OAuth 回调 / 设备码 / 扫码 / 短信），另加一类"无交互登录"。
分类决定：

| 影响面 | 说明 |
|---|---|
| 控制台 UI | 浏览器回调只要一个链接；设备码要显示 URL + 用户码；扫码要渲染二维码；短信要输入框 |
| 运行环境 | 浏览器回调与扫码需要用户在**有图形环境**的机器上操作（或至少能手动打开链接）；设备码与短信纯文本即可 |
| 超时与取消 | 浏览器回调依赖本地监听端口（可能冲突）；设备码有上游给的过期时间；短信有次数限制（`E_SUBMIT_REJECTED`） |
| 是否可实现 | 登录实现在 providers-hub 中不存在时只能标 `E_NOT_IMPLEMENTED`，UI 要隐藏入口而不是给用户一个必失败的按钮 |

### 6.2 分类表

依据：`~/.dsh/profiles/web/node_modules/dsh-providers-hub/lib` 下各模块的实测标记
（`code_challenge` / `device_code` / `openBrowser` / `qrcode` / `smsCode` / `redirect_uri` 等）。

| provider | 分类 | 依据模块 | 需图形环境 | 备注 |
|---|---|---|---|---|
| `accio` | OAuth 回调（PKCE） | `accio-oauth.js` | 是（`xdg-open`/`openBrowser`） | `code_challenge` 密集 |
| `autoclaw` | OAuth 回调 | `autoclaw-oauth.js` | 是 | 本地 `127.0.0.1` 回调 |
| `buddy` | OAuth 回调 | `buddy-oauth.js` | 是 | token/refresh 密集，可续期 |
| `workbuddy` | OAuth 回调 | `buddy-auth.js`（无独立模块） | 是 | 与 `buddy` 同源，实现上应复用同一流程 |
| `cline` | **设备码** | `cline-oauth.js` | 否 | `device_code` / `deviceCode` |
| `codearts` | **未知** | 仅 `codearts-credits.js` | ？ | 登录实现位置未确认 → `loginImplemented=false` |
| `lobsterai` | OAuth 回调 | `lobsterai-oauth.js` | 是 | `redirect_uri` + 本地回调 |
| `loomy` | **扫码**（微信） | `loomy-wechat-login.js` | 是（展示二维码） | 涉及 `loomyPermanentLocked` |
| `minimax` | **设备码 + PKCE** | `minimax-oauth.js` | 否 | `device_code` + `code_challenge` |
| `minimaxintl` | **设备码 + PKCE** | `minimax-oauth.js` | 否 | 与 `minimax` 同源 |
| ~~`modelscope`~~ | —（2026-10-24 已移出反代支持） | — | — | 见下 |
| ~~`opencode`~~ | —（2026-10-24 已移出反代支持） | — | — | 见下 |
| `qoder` | OAuth 回调 + 设备轮询 | `qoder-oauth.js` | 是 | 另有 `pollQoderDeviceToken` |
| `qodercn` | OAuth 回调 | `qoder-oauth.js`（地区别名） | 是 | 无独立模块 |
| `raccoon` | **扫码 / 短信** | `raccoon-oauth.js`、`raccoon-login-page.js` | 是 | `qrcode` + `smsCode` 两种入口 |
| `trae` | OAuth 回调（**固定端口 18080**） | `trae-oauth.js` | 是 | 端口可能被占用，需失败提示 |
| `zcode` | OAuth 回调（登录页） | `zcode-login.js` | 是 | `redirect_uri` |

分类汇总：OAuth 回调 9 家（accio / autoclaw / buddy / workbuddy / lobsterai / qoder / qodercn /
trae / zcode）、设备码 2 家（cline / minimax 系，含 minimaxintl）、扫码 2 家（loomy / raccoon）、
短信 1 家（raccoon，与扫码同 provider）、未知 1 家（codearts）。

> 2026-10-24：`modelscope` 与 `opencode` 已从**反代（登录）支持**中移除（用户定案）。
> 它们在反代列表里恒不可用（一个只有刷新逻辑、一个是账号池工具），用不了也说不清。
> 同类需求由「API 供应商」目录承接：2026-10-25 用户澄清它们贴 Key 即用的端点
> （`modelscope-cn` / `opencode-ai` / `opencode-go`）保留在 `internal/apidir`——
> 移除的是登录形态，不是供应商本身。下表与统计仍保留它们的上游数据，只作为移植历史的记录。

**表中还出现了 3 个非 17 家的模块**（`gemini-oauth.js`、`officeace-auth.js`、`login.js`）：
它们存在于 lib 目录但当前 `state.json` 里没有对应账号。**这些模块不进入 MVP 的 provider 清单**，
理由：没有账号就无从验证登录流是否可用，而"接了一个没验证过的 provider"在 [03-upstream.md](03-upstream.md)
第 10 节的防漂移思路下是纯负担。

### 6.3 与三条红线的关系

| 登录类别 | 是否会写凭据 | 是否会碰 `state.json` | 是否出公网 |
|---|---|---|---|
| OAuth 回调 | 会（在 DSH 里），**我们只读结果** | 否 | 是（授权端点）——不违反红线③ |
| 设备码 | 同上 | 否 | 是 |
| 扫码 | 同上 | 否 | 是 |
| 短信 | 同上 | 否 | 是（短信网关） |
| 凭据粘贴 | 不涉及（用户直接给） | 否 | 否 |

**注意"会（在 DSH 里）"这一列**：登录过程本身在 DSH 的凭据存储里落地——那是 **DSH 的行为，
不是我们的**。我们的红线是"我们不去写"，不是"这件事不许发生"。这个区分必须写清楚，
否则实现者会误以为要在 sidecar 里禁掉 DSH 自己的写入路径（而这会直接让登录失败）。

---

## 7. 将来重写为 Go 的成本评估

> **预留决策**：sidecar 当前用 Node 实现。将来若重写为 Go，走**逐 provider 迁移**：
> 契约不变、Go 侧零改动，允许「混合 sidecar」（部分 provider 永久留在 Node）作为长期终态。
> 本节把实测数据留档，避免届时重新摸底。

### 7.1 为什么成本上界由契约决定

重写工作量不看 providers-hub 有多少行，看第 2 节那道契约有多宽。契约是 10 个方法 + 一条流事件，
且第 3 节强制 provider-agnostic（参数里只有 `provider` 与 `credentialRef`，没有业务概念）——
这是将来能低成本换实现的**唯一原因**，所以那条约束不能破。

### 7.2 代码量实测

对象：`dsh-providers-hub` 的 `lib/`（199 个文件，105,766 行）。

| 职责分块 | 文件 | 行数 | 占比 | 是否在范围内 |
| --- | ---: | ---: | ---: | --- |
| provider adapter（协议本体） | 18 | 24,888 | 23.5% | **是**（扣掉框架文件后 ≈19,341） |
| 账号 / 凭据 / 额度 / 刷新 | 33 | 19,731 | 18.7% | 部分（登录流要，账本不要） |
| OpenAI 兼容网关 | 2 | 1,785 | 1.7% | 否（红线③） |
| 浏览器 / 验证码 / 签到 / 增长 | 18 | 15,338 | 14.5% | 基本不要 |
| 其他（含 6,265 行 `jet-hub-rpc.js` 自有 RPC） | 128 | 44,024 | 41.6% | 否 |

「provider adapter」减掉三个框架文件（`llm-adapter.js` 2,372 行基类、`aggregate-adapter.js` 2,213、
`auto-adapter.js` 961）后，**15 家的真实协议逻辑 ≈19,341 行，平均每家 1,290 行**：
buddy 4,184 / trae 1,989 / zcode 1,840 / accio 1,792 / lobsterai 1,678 / qoder 1,295 /
cline 1,129 / minimax 941 / loomy 799 / raccoon 698 / gemini 677 / opencode 637 /
officeace 605 / modelscope 595 / autoclaw 482。

### 7.3 为什么不是「照着抄一遍」

| 卡点 | 说明 |
| --- | --- |
| 不是模板填空 | 15 家不是「OpenAI 兼容 + 换个 `base_url`」——那部分只有 `openai-compat.js` 1,408 行。每家有自己的请求整形、响应解析、错误与限流语义，有的连 SSE 都不标准 |
| 基类要重设计 | adapter 继承 2,372 行的 `LlmAdapter`，带 `providerRetryPolicy` / `imageRequestPricing` / `imageRequestTarget` / `resolveModelInfo` 等钩子；Go 里得当接口 + 共享 helper 重新设计，这是设计题不是翻译题 |
| 登录流是有状态的 | 9 家 OAuth 回调（要本地回调监听 + 拉起浏览器）、2 家设备码 + PKCE、2 家扫码（loomy 走微信）、1 家短信、1 家粘贴、1 家只刷新；各家 cookie / token 轮换与重试语义都不同 |
| zcode 无法「纯」Go | 需拉起无头 Chromium 产阿里云 captcha（`zcode-captcha.js` 989 行 + `chromium-session.js` 1,186 行，源码 133 处 captcha 引用）；Go 里要么内嵌 CDP、要么永远保留一个浏览器 |
| 隐性知识不在代码里 | 源码注释大量是逆向厂商行为换来的结论（如「captcha 是**按需**索要的，索要时**不能复用**」）。翻译不会把这些带过去，必须对着真实厂商端点重新踩——**这才是主要耗时** |

### 7.4 迁移难度分档

| 档次 | 家数 | provider | 单家估时 |
| --- | ---: | --- | ---: |
| 易（纯 HTTP，无浏览器） | 2 | minimax、cline（设备码） | 1–2 天 |
| 中（OAuth 回调 + 本地监听） | 9 | accio、autoclaw、buddy、workbuddy、lobsterai、qoder、qodercn、trae、codearts | 2–5 天 |
| 难（浏览器 / 扫码 / 短信） | 3 | loomy（微信扫码）、raccoon（扫码 + 短信）、zcode（captcha） | 5–15 天 |

### 7.5 估算

Go 重写 HTTP / JSON / SSE 类代码，行数通常是 JS 的 1.2–1.6 倍：

- 19K 行协议逻辑 → 约 23K–31K 行 Go
- 共享基础设施（SSE 解析、重试、token 存储、adapter 注册）→ 3K–5K 行
- 合计 **约 26K–36K 行 Go**，测试另算

时间（熟手）：

| 范围 | 工期 |
| --- | ---: |
| 全量对齐 15 家 | 2–4 个月 |
| 仅 4 家「易」档 | 约 3 周 |
| 不做 | 0（Node sidecar 是正当的长期方案） |

时间的主要构成不是编码，是**逐家对着活账号验证**。

### 7.6 三条结论

1. **现在不重写**。真正的资产是第 2 节那道契约，不是实现语言。
2. **要重写就逐家迁移**：Node 与 Go 双跑对比，一家一家切，随时可回退；Go 侧（路由、账户、额度、计量、管理 API）一行都不用改。
3. **允许混合 sidecar**：`zcode`（以及大概率的 `loomy` / `raccoon`）永久留在 Node 是合理终态，不必强行纯 Go。

---

## 8. 待定项

> **待定**：`codearts` 的登录实现位置。`lib/` 下只有 `codearts-credits.js`（余额查询），
> 没有 oauth/login 模块。可能实现位于 lib 之外或另一个包。在确认前该 provider 的
> `loginImplemented = false`，控制台不展示登录入口。


> **待定**：各登录流在**无图形环境**（纯 CLI / 容器）下的完整闭环未验证。设备码类理论上可行，
> 但需要实测 `openBrowser` 失败时是否仍有可用的降级路径（把 URL 打印给用户）。
> 这也是 [../RESEARCH-UPSTREAM-REUSE.md](../RESEARCH-UPSTREAM-REUSE.md) 里明确未验证的部分。

> **待定**：`trae` 的固定回调端口 18080 与其它服务冲突时的表现（是报错还是静默失败）。
> 这决定我们是否需要给它做端口检测与提示。

> **待定**：sidecar 是否需要独立的日志文件与轮转策略。当前设计是 sidecar 的 stderr 直接并入
> 主进程日志流（`slog` 统一输出），但第三方库可能往 stdout 写非协议内容——那会**污染 NDJSON 帧**。
> 需要确认"第三方库不写 stdout"这一假设；若不成立，就要改用 fd 3/4 或 Unix socket。
