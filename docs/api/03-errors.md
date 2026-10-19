> 分册：架构 · 依赖：[01-inbound.md](01-inbound.md)、[02-admin.md](02-admin.md)、[../ui/06-design.md](../ui/06-design.md) · 对应总纲：PLAN.md 第 3、5、8、9 章

# 错误与状态码

## 1. 本文的适用范围
FreeRouter 有三个各自的错误面，它们共用**同一套错误码**与**同一个错误体**：
| 错误面 | 消费者 | 载体 |
|---|---|---|
| 入站面 | 第三方客户端（OpenAI / Anthropic SDK） | `/v1/*` 的 HTTP 响应 |
| 控制台面 | 本站前端 | `/api/admin/*`、账户自助端点的 HTTP 响应 |
| 页面面 | 浏览器 | 错误页（见 [../ui/02-public.md](../ui/02-public.md) 第 8 节） |

**为什么三个面共用同一套码**：同一个故障（例如"没有可用候选"） 在三个面上的成因完全相同。如果各有各的编码体系， 排障时就需要一张翻译表，而翻译表会随时间漂移。

**为什么页面面不出码只出文案**：错误页面向的是人。 但错误页必须给**请求 ID**（见 [../ui/06-design.md](../ui/06-design.md) 4.1）， 管理员据此能反查到错误码。

**一条稳定性契约**：

> **错误码（`code`）是契约，错误消息（`message`）不是。** 修改 `message` 的文案不构成不兼容；修改或删除 `code` 构成不兼容。

**为什么要写这条**：客户端会按 `code` 分支，但不会按文案分支。 把它写下来才能阻止"把文案也当契约、于是两种改动都被冻结"。

## 2. 错误体形状

### 2.1 OpenAI 兼容形状（默认）

```json
{
  "error": {
    "message": "当前没有可用资源，请稍后重试", "type": "service_unavailable_error", "param": null,
    "code": "no_candidate"
  }
}
```

| 字段 | 说明 |
|---|---|
| `message` | 面向人的说明，**不承诺稳定** |
| `type` | 粗粒度类型枚举，见 2.4 |
| `param` | 出错字段名（如 `max_tokens`），无则 `null` |
| `code` | 细粒度错误码，见第 3 节 |

### 2.2 Anthropic 形状（仅 `/v1/messages`）

```json
{
  "type": "error",
  "error": {
    "type": "service_unavailable_error", "message": "当前没有可用资源，请稍后重试", "code": "no_candidate"
  }
}
```

**为什么 Anthropic 路径要换形状**：Anthropic SDK 按 `{"type":"error"}` 解析错误。返回 OpenAI 形状会让它把响应当成畸形成功响应。

**为什么仍带 `code`**：`code` 是我们自己的细粒度标识（Anthropic 原生协议没有）， 带上它才能让两边共用同一套排障语言与同一个日志查询入口。

### 2.3 控制台面

控制台面**沿用 2.1 的 OpenAI 兼容形状**。

**为什么控制台不另立形状**：前端与第三方客户端可以共用同一段 错误解析代码，`code` 也能直接用于界面分支（见 [../ui/06-design.md](../ui/06-design.md) 4.1 的错误分级）。

### 2.4 `type` 枚举

| `type` | 用在哪类 `code` |
|---|---|
| `invalid_request_error` | 请求本身有问题（参数、体积、能力不匹配） |
| `authentication_error` | 身份不可接受 |
| `permission_error` | 身份可接受但权限不足 |
| `not_found_error` | 目标不存在或对当前身份不可见 |
| `conflict_error` | 与现有状态冲突 |
| `rate_limit_error` | 速率或额度受限 |
| `service_unavailable_error` | 服务侧暂时不可用 |
| `internal_error` | 未预期的服务端故障 |

**为什么保留一层粗粒度 `type`**：现有的 OpenAI / Anthropic SDK 在部分路径上只看 `type`。只给 `code` 会让这些 SDK 落到兜底分支， 用户看到的是 SDK 的通用报错而不是我们的说明。

## 3. 错误码表

### 3.1 入站面

| `code` | HTTP | `type` | 触发条件 | 客户端应如何处理 |
|---|---|---|---|---|
| `invalid_api_key` | 401 | `authentication_error` | 缺鉴权头 / Key 不存在、吊销、过期、禁用 / 所属账户被禁用 | 不重试，提示用户检查 Key |
| `invalid_request` | 400 | `invalid_request_error` | 缺必填字段、字段类型错、参数越界 | 不重试，修正请求 |
| `capability_unsupported` | 400 | `invalid_request_error` | 请求的图片 / 工具调用 / JSON mode / 思考档位超出候选能力 | 不重试；改用支持该能力的抽象模型 |
| `request_too_large` | 413 | `invalid_request_error` | 请求体超过入站上限（默认 32 MiB） | 不重试，缩减请求 |
| `model_not_found` | 404 | `not_found_error` | 抽象模型不存在 / 请求的是真实模型名 / 对当前 Key 不可见 | 不重试；先 `GET /v1/models` 对照 |
| `model_retired` | 410 | `not_found_error` | 模型 `Sunset` 到期后已 `disabled` | 不重试；按迁移文档换名 |
| `quota_exceeded` | 429 | `rate_limit_error` | 日 / 月 token 上限或全局预算熔断 | 按 `Retry-After`（若有）等待，或等额度重置 |
| `rate_limited` | 429 | `rate_limit_error` | 超出 RPM / TPM / 并发上限 | **必须**按 `Retry-After` 退避重试 |
| `upstream_unavailable` | 502 | `service_unavailable_error` | 上游网关不可达（如 DSH 未运行） | 可重试；退避 |
| `upstream_timeout` | 504 | `service_unavailable_error` | 上游未在超时内响应 | 可重试；退避 |
| `upstream_error` | 502 | `service_unavailable_error` | 上游返回了无法映射为上述码的失败 | 可重试；退避 |
| `no_candidate` | 503 | `service_unavailable_error` | 策略引擎过滤后无候选可用 | 可重试；退避 |
| `internal_error` | 500 | `internal_error` | 未预期故障 | 可重试一次；持续失败报请求 ID |

**为什么 `model_not_found` 同时覆盖"不存在"与"请求了真实模型名"**： 总纲 3.1 明确"客户端直接请求真实模型名 → 404（不泄露池结构）"。 如果真实模型名返回一个不同的码（例如 `real_model_not_exposed`）， 这个码本身就确认了"这个名字存在，只是不给你"。同一个码让两种情况不可区分。

**为什么新增 `model_retired` 而不是复用 `model_not_found`**： 总纲 3.5 定"`Sunset` 到期自动 `disabled` 返回 410"。 410 Gone 与 404 的语义差别正是"有过、现在没了"与"从来没有"—— 这个差别对客户端有实际价值（前者应引导用户迁移，后者应引导用户查文档）， 而它**不泄露池结构**，所以不违反上一条的理由。

**为什么 `quota_exceeded` 与 `rate_limited` 都用 429**：两者都是 "你撞到了使用上限"，客户端的正确反应都是"等一下再来"。 区分它们的信息在 `code` 与 `Retry-After` 的有无里，不在状态码里。

**为什么 `no_candidate` 用 503 而不是 404**：总纲 3.6 明确 "无候选可用返回 503 + 专用错误码（与「模型不存在」404 区分）"。 从客户端视角，"这个模型现在没资源"与"没有这个模型"会导致完全不同的动作： 前者重试，后者改代码。

### 3.2 控制台面与账户自助

| `code` | HTTP | `type` | 触发条件 |
|---|---|---|---|
| `invalid_credentials` | 401 | `authentication_error` | 登录时用户名或密码错误 |
| `session_expired` | 401 | `authentication_error` | 会话不存在或已过期 |
| `elevation_required` | 403 | `permission_error` | 有 `admin` 类但未提权，或提权已过期 |
| `forbidden` | 403 | `permission_error` | 身份可接受但缺少对应权限类 |
| `not_found` | 404 | `not_found_error` | 资源不存在 |
| `conflict` | 409 | `conflict_error` | 唯一性冲突（如用户名已存在、`UNIQUE(provider, external_id)` 撞身份） |
| `invalid_request` | 400 | `invalid_request_error` | 字段校验失败，`param` 指出字段 |
| `too_many_attempts` | 429 | `rate_limit_error` | 登录 / 提交类端点的防爆破退避生效 |
| `internal_error` | 500 | `internal_error` | 未预期故障 |

**为什么 `elevation_required` 必须是独立码而不是 `forbidden`**： 界面需要据此**引导用户去提权**，而不是显示"你没有权限"。 两者在 UI 上是完全不同的路径（见 [../ui/01-ia.md](../ui/01-ia.md) 第 7 节）。 把它合并进 `forbidden` 会让界面失去这个区分能力。

**为什么"没有 `admin` 类"的请求连错误体都不返回**（[02-admin.md](02-admin.md) 2.2 的 404）： 这里要区分两个层次——**没有 `admin` 类**时返回 404 但**用 `not_found` 码**， 即不承认路径存在；**有 `admin` 类但未提权**时返回 403 + `elevation_required`。 前者不泄露路径存在性，后者必须明说，因为用户确实有这个能力。

**为什么登录失败用 `invalid_credentials` 而不复用 `invalid_api_key`**： `invalid_api_key` 属于入站面，其契约是"Key 的问题"。 登录是密码的问题，两者的排障动作不同（查 Key 列表 vs 重置密码）。

### 3.3 码的边界原则

三条分界，用于将来新增错误码时的判断：

| 分界 | 原则 | 为什么 |
|---|---|---|
| 客户端动作不同 | 拆成两个码 | 客户端按码分支，动作不同就必须可区分 |
| 泄露面不同 | 合并成一个码 | 区分本身就是信息披露（见 `model_not_found`） |
| 仅文案不同 | 不新增码 | 文案不是契约，无须为措辞引入枚举值 |

## 4. HTTP 状态码映射总表

| HTTP | 语义 | 本服务的码 |
|---|---|---|
| 400 | 请求格式或参数不对 | `invalid_request`、`capability_unsupported` |
| 401 | 身份不可接受 | `invalid_api_key`、`invalid_credentials`、`session_expired` |
| 403 | 权限不足 | `elevation_required`、`forbidden` |
| 404 | 目标不存在或不可见 | `model_not_found`、`not_found` |
| 405 | 方法不允许 | `invalid_request`（`param` 为方法名） |
| 409 | 状态冲突 | `conflict` |
| 410 | 曾经存在，现已不可用 | `model_retired` |
| 413 | 请求体过大 | `request_too_large` |
| 415 | `Content-Type` 不支持 | `invalid_request` |
| 429 | 速率或额度受限 | `rate_limited`、`quota_exceeded`、`too_many_attempts` |
| 500 | 服务端未预期故障 | `internal_error` |
| 502 | 上游不可用或返回了坏响应 | `upstream_unavailable`、`upstream_error` |
| 503 | 服务侧暂时无资源 | `no_candidate` |
| 504 | 上游超时 | `upstream_timeout` |

**为什么 405 与 415 复用 `invalid_request` 而不新增码**：客户端 对这两者的动作与 400 完全相同（改请求）。为它们新增码违反了 3.3 的第一条分界——动作相同时不该拆。

**为什么没有 402**：总纲 1.2（MVP 不做计费）与 [../ui/06-design.md](../ui/06-design.md) 6.2 的禁用词表都把 "费用金额"排除在界面之外。没有付费概念就没有 Payment Required。

**为什么没有 418 / 451 之类的扩展码**：错误面越小越好。 每一个额外的状态码都需要客户端专门处理，否则会落到"未知错误"分支， 那比一个语义稍宽的 400 更糟。

## 5. 响应头清单

### 5.1 所有响应

| 头 | 值 | 说明 |
|---|---|---|
| `X-Free-Router-Request-Id` | 如 `req_01J8...` | **每个响应都有**，含错误与流式 |
| `Content-Type` | `application/json` / `text/event-stream` | 按端点 |
| `Strict-Transport-Security` | `max-age=…; includeSubDomains` | 总纲 9.2 |
| `X-Content-Type-Options` | `nosniff` | 总纲 9.2 |
| `Referrer-Policy` | 如 `strict-origin-when-cross-origin` | 总纲 9.2（控制台页面） |
| `X-Frame-Options` | `DENY` | 总纲 9.2（控制台页面） |
| `Content-Security-Policy` | 按全局设置 | 总纲 9.2 |
| `Access-Control-Allow-*` | **默认不发送** | 总纲 9.2：CORS 默认关闭，禁止 `*` + 凭据 |

**为什么 `X-Free-Router-Request-Id` 在错误响应上也必须有**：错误是 最需要报障的场景。只在成功响应上带 ID 等于在最需要它的时刻把它拿掉。

**为什么不发 `WWW-Authenticate`**：发送它会让部分 SDK 进入 "尝试刷新令牌"的分支。我们没有可刷新的令牌（管理面走会话 Cookie， 入站面走静态 Key），这条分支必然失败，只是把一次明确失败变成一次多余往返。

### 5.2 受限时（429 / 503）

```
Retry-After: 30
```

| 情况 | `Retry-After` | 为什么 |
|---|---|---|
| `rate_limited` | **必有**，整数秒 | 验收第 4 条要求"429 + 有值 Retry-After" |
| `quota_exceeded`（日 / 月上限） | 有值，指向额度重置时刻；无法计算时省略 | 额度重置时刻是可计算的 |
| `quota_exceeded`（全局预算熔断） | 省略 | 熔断解除依赖人工，给一个数字是误导 |
| `no_candidate` | 可选 | 候选恢复时间不可预测 |
| `upstream_unavailable` / `upstream_timeout` | 可选 | 上游恢复时间不可预测 |

**为什么允许"有时没有 `Retry-After`"**：给一个编造的数字 会让守规矩的客户端在错误的时刻重试。宁可让它自行退避。

**为什么 `upstream_unavailable` 也允许带 `Retry-After`**：上游是 本机网关（`127.0.0.1:8326`），它的恢复时间在"进程被重启"这类 场景下是可估计的。给得出就给。

### 5.3 弃用信号（RFC 9745 与 RFC 8594）

请求到**被弃用**（`deprecated`）的抽象模型时，响应（含流式）带：

```
Deprecation: @1688169599
Sunset: Sun, 30 Jun 2026 23:59:59 UTC
Link: </docs/models/smart>; rel="deprecation"
```

| 头 | 规范 | 格式 | 语义 |
|---|---|---|---|
| `Deprecation` | RFC 9745 | Structured Field 的 Date，`@` + Unix 秒 | **何时**开始弃用 |
| `Sunset` | RFC 8594 | HTTP-date | **何时**完全不可用 |
| `Link` | RFC 8288 | `rel="deprecation"` | 迁移文档位置 |

四条规则（总纲 3.5）：

| 规则 | 内容 | 为什么 |
|---|---|---|
| 分工 | `Deprecation` 说时间、`Sunset` 说不可用 | RFC 9745 明确 deprecation 不改变资源行为，不可用由 `Sunset` 表达 |
| 顺序 | `Sunset` **不得早于** `Deprecation` | 违反时客户端无法构造合理的时间线 |
| `Sunset` 可选 | 不设 = 永久兼容壳 | 允许长期保留旧名 |
| `Link` 的落点 | **必须指向替代链的最终 active 目标**，不能指向中间节点 | 中间节点自己也会被弃用，指过去等于把用户导到一个新的死胡同 |

**为什么 `Deprecation` 是 `@1688169599` 而不是 `true`**： 早期草案用布尔值表达"已弃用"。RFC 9745 的正式形式是 Structured Field Date，携带时间信息。用正式形式的好处是客户端可以判断"弃用多久了"， 而布尔值只能判断"是否弃用"。

**为什么 `Sunset` 用 HTTP-date 而 `Deprecation` 用 Structured Field Date**： 两个头来自两份不同的规范。我们按各自规范输出，不做统一—— 统一成任一种都会违反另一份规范。

**为什么弃用信号出现在响应头而不是响应体**：总纲 3.5 定 "响应 `model` 仍回显 `old`"（即响应体的模型名保持不变）。 把它挪到头部才能在"契约不变"的前提下附加新信息。

### 5.4 调试头（默认关闭）

```
x-fr-trace: real=dsh:deepseek-official/deepseek-flash; cred=cred_01J8; attempt=1
```

**为什么只对管理端账户或特定组的 Key 开放**：这是全站唯一 会把真实模型名送出租户边界的机制（[01-inbound.md](01-inbound.md) 7.3）。

### 5.5 速率状态头（成功响应上）

```
x-ratelimit-limit-requests: 60
x-ratelimit-remaining-requests: 42
x-ratelimit-reset-requests: 30
x-ratelimit-limit-tokens: 100000
x-ratelimit-remaining-tokens: 81344
x-ratelimit-reset-tokens: 30
```

**为什么成功响应也要带速率信息**：只在 429 时告诉用户 "你超了"是一种追赶式交互。带在成功响应上，守规矩的客户端可以 自己提前减速，把 429 从常态变成异常。

**为什么不带额度（日 / 月）的剩余量头**：额度是低频变化的配额， 它属于用量面板（[../ui/04-tenant.md](../ui/04-tenant.md) 第 4 节）， 不属于每个响应。塞进头部会让每个响应都多一次配额查询。

## 6. 流式响应中的错误

流已开始时（HTTP 状态码已定），错误只能走流内事件：

```
data: {"error":{"message":"当前没有可用资源，请稍后重试","type":"service_unavailable_error","param":null,"code":"no_candidate"},"request_id":"req_01J8..."}

data: [DONE]
```

| 规则 | 内容 |
|---|---|
| 帧形状 | `{"error":{<与 2.1 相同的对象>},"request_id":"…"}` |
| Anthropic 路径 | 改用 `event: error` 具名事件，体为 `{"type":"error","error":{…}}` |
| 结束 | 错误帧后**必须**仍发 `[DONE]`（Anthropic 路径为 `message_stop`） |
| ID | 请求 ID 在错误帧里给一次，头里已给过 |

**为什么流内错误的 `request_id` 与 `error` 是同级而不是嵌在 `error` 里**： 头可能因为代理被剥离，流内的 ID 是最后一道可追溯性。 放在同级让它不依赖对错误体形状的理解就能被脚本抓取。

**为什么 Anthropic 路径改变了帧形状**：Anthropic 的流式协议 用具名事件名区分帧类型，客户端按 `event:` 行分发。塞一个 OpenAI 形状的 `data:` 行会落进它的未知帧分支。

## 7. 重试语义

| 分类 | 码 | 客户端做法 |
|---|---|---|
| 可重试（带退避） | `rate_limited`、`quota_exceeded`、`no_candidate`、`upstream_*`、`internal_error` | 指数退避；有 `Retry-After` 时以它为准 |
| 不可重试 | `invalid_api_key`、`invalid_request`、`capability_unsupported`、`request_too_large`、`model_not_found`、`model_retired` | 修改请求或配置后重新发起 |
| 需人工介入 | `elevation_required`、`forbidden` | 去提权 / 申请权限类 |

**为什么把"可重试 / 不可重试"写进文档**：这是唯一能阻止用户 对着 `model_not_found` 循环重试的东西。总纲第 13 章把 "上游故障→全站雪崩"列为风险，而这个雪崩的常见成因正是 所有客户端同时无退避重试。

**为什么 `internal_error` 是"可重试一次"**：未预期故障可能是 瞬时状态。允许一次重试能救回大部分瞬时故障，同时限制它不至于变成 重试风暴。

## 8. 错误码与可观测性的连接

每个错误都进 `requests.error_code`（入站面）与 `audit_log` （控制台面的写操作），并在两个界面呈现：

| 界面 | 展示 | 不展示 |
|---|---|---|
| [../ui/04-tenant.md](../ui/04-tenant.md) 第 5 节调用日志 | 错误码、HTTP 状态、抽象模型名 | 真实落点、贡献者归因、上游渠道 |
| [../ui/03-admin.md](../ui/03-admin.md) 第 9、10 节 | 全部，含真实落点与贡献者归因 | 凭据明文 |

**为什么 `error_code` 是一个枚举字段而不是自由文本**： 按码聚合是排障的第一步（"今天 `no_candidate` 涨了多少"）。 自由文本会让聚合失效。

## 9. 待定项

> **待定**：`x-ratelimit-*` 头的具体字段名集合 （是否完全对齐 OpenAI 的命名，还是用更短的自有命名）？

> **待定**：`Link` 头的迁移文档路径在部署中是否可配置？ 当前示例用 `/docs/models/<name>`，但站点未必托管这些文档。

> **待定**：`quota_exceeded` 在"全局预算熔断"触发时， 是否需要与"个人额度耗尽"使用不同的码（两者当前共用一个码， 但对用户的可操作建议不同：前者只能等，后者可申请提额）。

> **待定**：错误消息是否需要 i18n？当前假设单一语言， 与 [../ui/06-design.md](../ui/06-design.md) 第 10 节的 i18n 待定项相关。

> **待定**：控制台面在"未提权"时返回 403 + `elevation_required`， 但页面面走 302 跳转到 `/elevate`（[../ui/01-ia.md](../ui/01-ia.md) 3.2）。 两种表达是否需要统一为"API 一律返回码、页面一律跳转"的显式规则？
