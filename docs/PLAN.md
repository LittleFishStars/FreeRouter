# FreeRouter 方案（v1）

> AI 中转站，对标 [Tingly Box](https://github.com/tingly-dev/tingly-box)，上游复用
> [iJetLi/dsh-providers-hub](https://github.com/iJetLi/dsh-providers-hub) 的订阅额度池。
>
> **已定决策**：Go + chi · 独立服务 · 公网暴露 + 完整鉴权
>
> 状态：方案已定稿，待开工 M0。v0（TS 版草案）已废弃，见 git 历史。

---

## 0. 定位

FreeRouter 把 dsh-providers-hub 已经具备的「本机订阅额度池 + 本机 OpenAI 网关」，
升级成一个**多协议、多租户、带策略路由和安全边界的 AI 中转站**。

**核心差异化**：上游不是买来的 API Key，而是**你自己已登录的订阅账号池**
（17 家渠道、多账号、临期积分自动优先烧）。One API / New API / Tingly Box
解决「多 Key 聚合」，FreeRouter 解决「订阅额度的统一出口」。

**一句话**：providers-hub 管「额度从哪来」，FreeRouter 管「额度怎么发出去」。

---

## 1. 两个参照物

### 1.1 Tingly Box：抄什么

Go 单体，MPL-2.0，`npx tingly-box` 起在 `:12580`，自带 Web UI 与 wails3 桌面壳。

| 能力 | 它的做法（`internal/…`） | 我们的取舍 |
|---|---|---|
| 统一端点 + 协议互转 | OpenAI / Anthropic / Google 互译，`protocol/{request,stream,nonstream,token,transformer}` | **照抄**，中转站的地基 |
| 智能路由 | 按成本 / 速度 / 自定义选路，`smart_routing` | **照抄**，候选集换成「渠道 × 账号」 |
| 虚拟模型 | `virtualmodel` | **照抄**，即我们的「别名 → 多候选」 |
| 复用订阅额度 | OAuth 接 Claude Code / Gemini | **providers-hub 已在做**，直接继承 |
| Web 控制面 | providers / routes / aliases / models | **照抄**，扩展成 Keys / Routes / Logs |
| 用量统计 | 逐请求 token、延迟、成本 | **照抄**，与 providers-hub 账本对账 |
| 上下文压缩 | `smart_compact` 对话蒸馏 | P1 |
| IM 远程控制 | `remote_control`，Telegram / 钉钉 / 飞书 / Slack / Discord | P2 |
| 部署 | npm / 源码 / Docker（`~/.tingly-box`） | **照抄**三形态 |

它刻意**没有**：不内置账号池登录、不管额度到期、不做积分领取——这三块是 providers-hub 的地盘。

### 1.2 dsh-providers-hub：结合什么

TypeScript，MIT，DSH 插件。本机装的是 `0.2.1-rc.2`（profile `web`，link 安装）。

**它已经提供（我们全部继承，不重写）：**

1. **17 家 provider 路由**，每家一套 auth / adapter / credits / oauth：
   `codearts` `buddy` `workbuddy` `lobsterai` `qoder` `qodercn` `trae` `cline` `loomy`
   `raccoon` `minimax` `minimaxintl` `zcode` `opencode` `gemini` `officeace` `accio`。
2. **账号池**：同渠道多账号、拖拽排序即优先级、限流切换、策略拦截冷却、停用不影响续期。
3. **凭据静默续期**：到期前自动刷新，无需再开浏览器。
4. **积分体系**：余额查询、每日签到 / 领取、锁定永久积分、按临期排序。
5. **聚合路由**：同一真实模型跨渠道归并，按「最快作废的额度优先」排序，
   候选级失败切换（仅在还没吐出可见内容时切换），`aggregate/auto` 全池兜底。
6. **本机 OpenAI 兼容网关**（`127.0.0.1:8326`，实测正在运行）：

   | 端点 | 说明 |
   |---|---|
   | `GET /v1/models` | 模型目录 |
   | `GET /v1/reasoning-efforts` | 思考档位 |
   | `POST /v1/chat/completions` | OpenAI Chat Completions |
   | `POST /v1/responses` | OpenAI Responses API |

   Bearer 鉴权，key 在 `$DSH_HOME/openai-gateway/api-key`（实测：无 Bearer → 401）。
7. **记账**：`$DSH_HOME/jet-hub/token-ledger.json`，用模块级 Symbol
   `GATEWAY_CHANNEL_MARK` 区分 `direct` / `gateway` 双路流量。

**它明确不提供（这正是 FreeRouter 的生存空间）：**

| 缺口 | 说明 |
|---|---|
| 只有 OpenAI 协议 | Anthropic `messages` / Gemini `generateContent` 入站全无 |
| 没有多租户 | 单一 API Key，无 key 管理、配额、限速、分账 |
| 没有策略路由 | 只有「临期优先」一种排序 |
| 没有独立控制面 | 配置散在 DSH 的 Jet Hub 面板里 |
| 默认只绑 `127.0.0.1`，**拒绝** `0.0.0.0` | 设计上就不打算对外 |
| 没有请求日志 / 用量看板 | 只有内部 token-ledger |
| 不能与 DSH 解耦 | 登录、续期、模型目录都挂在 DSH 进程里 |

> ⚠️ 上游硬约束（必须写进适配层）：
> 网关拒绝通配地址；请求体上限 **300 MiB**（超限 413 `request_too_large`）；
> 空闲 keep-alive **120 秒**后服务端发 FIN；
> 每轮全量回传历史的客户端（Codex 一类）可能撞 413。

### 1.3 复用锚点（精确到接口）

**A. HTTP 上游 —— Go 版唯一路径**

```http
POST http://127.0.0.1:8326/v1/chat/completions
Authorization: Bearer <$DSH_HOME/openai-gateway/api-key>
Content-Type: application/json
```

`/v1/models` 返回的 `id` 形如 `deepseek-official/deepseek-flash`、
`deepseek-account/deepseek-flash`、`aggregate/...`，每条带
`name` / `input:["text","image"]` / `reasoning.efforts[]` / `openai_efforts[]`。
**这份目录就是上游模型清单来源。**

**B. 直连运行时 —— 已放弃（选 Go 的直接代价）**

网关内部只依赖一个极窄的运行时面，`ctx.llm` 满足它：

```ts
// lib/openai-gateway/server.d.ts
interface LlmRuntimeLike {
  listProviders(): readonly { id: string }[];
  listModels(provider: string): Promise<readonly LlmModelInfo[]>;
  resolveModelInfo(provider: string, model: string, signal?: AbortSignal): Promise<unknown>;
  stream(options: GenerateOptions): AsyncIterable<StreamChunk>;
}
```

`ctx.llm` 是 **DSH 的 Node 进程内对象**，Go 进程跨不过去。因此：

- ❌ 放弃：直连账号池选号、`LlmModelInfo` 完整元数据
- ✅ 保留：8326 后面的账号池 / 限流切换 / 临期排序**依然全部生效**——
  因为那些逻辑跑在 providers-hub 进程内，我们只是换了个入口进去

> 代价诚实说清：v0 方案里「P1 做 DSH 插件直连 `ctx.llm`」这条路作废。
> 如果将来真需要，只能再起一个薄 Node 侧车暴露 HTTP，等于绕一圈回来。
> **MVP 及 P1 均无此需求**，8326 的 4 个端点足够覆盖。

**C. 不做代码级复用**

它的 `package.json` 只导出 `.` 与 `./client`，内部模块需深路径 import
（版本升级会漂）。值得借的只有思路，不引代码：
- `lib/openai-gateway/{messages,responses,stream,images}.js` —— OpenAI 序列化与 SSE 解析
- `lib/openai-gateway/channel.js` —— 渠道打标思路

**结论**：把 providers-hub 当成**语义稳定的外部上游**，只依赖 4 个 HTTP 端点，
写契约测试锁字段，它升级不伤我们。

---

## 2. 架构

```
   Claude Code ─────┐
   Cherry Studio ───┤   ┌──────────────────────────────────────┐
   Cursor / Cline ──┼──▶│  L1 入站协议层（公网，TLS 终止）      │
   Gemini CLI ──────┘   │  /v1/chat/completions                │
                        │  /v1/messages        (Anthropic)     │
                        │  /v1/responses       (OpenAI)        │
                        │  /v1beta/models/{m}:generateContent  │
                        │  ────────────────────────────────    │
                        │  鉴权 → 限速 → 配额 → 解析成 IR       │
                        └──────────────────┬───────────────────┘
                                           ▼
                        ┌──────────────────────────────────────┐
                        │  L2 内核                             │
                        │  router  别名 → 规则 → 候选           │
                        │  policy  临期 / 加权 / 成本 / 故障转移 │
                        │  call    候选级重试、冷却、超时、取消  │
                        │  meter   token / 延迟 / 成本 / 打标    │
                        │  store   SQLite(WAL)                 │
                        └──────────────────┬───────────────────┘
                                           ▼
                        ┌──────────────────────────────────────┐
                        │  L3 上游适配层                        │
                        │  upstream/dshgateway → 127.0.0.1:8326│
                        │  upstream/openaicompat → 扩展位       │
                        │  IR → 上游协议 → SSE → IR 事件        │
                        └──────────────────┬───────────────────┘
                                           ▼
                        ┌──────────────────────────────────────┐
                        │  dsh-providers-hub  :8326            │
                        │  18 路由 · 账号池 · 续期 · 临期排序   │
                        └──────────────────┬───────────────────┘
                                           ▼
                     CodeBuddy  CodeArts  TRAE  …  共 17 家
```

**唯一出口原则**：上游永远走 `127.0.0.1:8326`，永不暴露；FreeRouter 是对外唯一入口。
（这条同时解决了 providers-hub「拒绝通配地址」带来的跨机不可用问题。）

### 2.1 IR 设计要点

- **消息**：`role` + `parts[]`（`text` / `image` / `tool_call` / `tool_result` / `thinking`），
  对齐 Anthropic content block 粒度——它最能表达富内容，向下映射到 OpenAI 最简单。
- **流式事件**：`start` / `thinking_delta` / `text_delta` / `tool_call_start|delta|end` /
  `usage` / `error` / `done`。每个入站协议各写一个「事件 → 自己的 SSE 帧」渲染器。
- **不静默降级**：缺失能力明确 400，绝不悄悄丢图片 / 丢 `tool_use`。
  （providers-hub 踩过这类坑：静默丢图会让模型把答案幻觉出来。）
- **ID 稳定性**：tool_call 的 `id` 按 SSE `index` 缓存重组，不依赖分片到达顺序。

---

## 3. 技术选型（Go）

| 层 | 选择 | 理由 |
|---|---|---|
| 语言 | Go 1.27（本机 `go1.27.2 linux/amd64`） | 单二进制、低内存、长期跑稳 |
| 路由 | `github.com/go-chi/chi/v5` | 标准 `net/http` 兼容、中间件链清晰、比 gin 更贴标准库 |
| 存储 | `modernc.org/sqlite`（纯 Go，CGO-free）+ WAL | 交叉编译无忧，符合单二进制目标 |
| 迁移 | 自写（`embed` SQL + `schema_migrations` 表） | 30 行够用，少一个依赖 |
| 日志 | 标准库 `log/slog` | JSON 结构化，零依赖 |
| 限速 | `golang.org/x/time/rate` | 令牌桶，三维限速 |
| 密码哈希 | `golang.org/x/crypto/argon2`（argon2id） | 管理面密码 |
| 密钥哈希 | 标准库 `crypto/sha256` | API key 本身高熵，无需慢哈希 |
| 加密 | 标准库 `crypto/aes` GCM | 上游 secret 落库加密 |
| TLS | `golang.org/x/crypto/acme/autocert`，或反代终止 | 公网必备 |
| 前端 | Next.js + Tailwind + shadcn/ui，构建产物 `embed.FS` 打进二进制 | 开发期分离，发布期单文件 |
| 包管理 | Go modules（**GOPROXY 必须设 `https://goproxy.cn,direct`**——本机实测 `proxy.golang.org` 不通，`goproxy.cn` 通） | 网络环境约束 |

### 3.1 目录结构

```
FreeRouter/
├── go.mod
├── cmd/freerouter/main.go        # 入口：配置 → 依赖装配 → 启 HTTP
├── internal/
│   ├── config/                   # YAML + 环境变量覆盖 + 校验
│   ├── ir/                       # 消息与流式事件类型（核心抽象）
│   ├── protocol/
│   │   ├── openaichat/           # 入站 + 出站
│   │   ├── openairesponses/
│   │   ├── anthropic/
│   │   └── gemini/
│   ├── router/                   # 别名解析 + 策略 + 冷却 + 健康
│   ├── meter/                    # token 记账、渠道打标
│   ├── store/                    # SQLite + 迁移 + 各表 DAO
│   ├── upstream/
│   │   ├── dshgateway/           # HTTP 8326（契约测试锁字段）
│   │   └── openaicompat/         # 扩展位
│   ├── httpapi/                  # 入站 HTTP：鉴权、限速、配额、SSE
│   ├── admin/                    # 管理面 API + 管理端鉴权
│   └── security/                 # 密钥、限速器、审计、安全头
├── web/                          # Next.js 控制台
├── migrations/                   # 编号 SQL
├── deploy/                       # Dockerfile / compose / Caddyfile / systemd
└── docs/PLAN.md                  # 本文件
```

### 3.2 为什么是 chi 而不是 gin

chi 的 handler 就是标准 `http.Handler`，中间件是标准 `func(http.Handler) http.Handler`。
代理类服务大量需要「拿到 `http.ResponseWriter` 直接写 SSE 并 Flush」，
标准接口比框架包装更省事，也更容易塞入自定义中间件（三条限速 + 审计 + 恢复）。

---

## 4. 功能清单

### MVP

- [ ] 入站 `POST /v1/chat/completions`（流式 + 非流式 + tool_calls + 图片）
- [ ] 入站 `POST /v1/messages`（Anthropic，含 `tool_use` / `thinking` / SSE 事件名对齐）
- [ ] 入站 `GET /v1/models`（按租户可见性过滤，`owned_by` 映射成路由名）
- [ ] 上游 `dshgateway`：key 自举、模型目录发现与缓存、离线降级
- [ ] 路由：`direct`（`provider/model`）+ `alias`（别名 → 多候选）
- [ ] 策略：`expiry-first`（默认）/ `weighted` / `failover`
- [ ] 多租户 Key：创建 / 禁用 / 过期 / 额度 / RPM·TPM / 模型白名单
- [ ] 请求日志：端点、别名、实际上游、状态、TTFT、总耗时、token、错误码
- [ ] 控制台：Overview / Keys / Upstreams / Routes / Models / Logs / Playground
- [ ] **安全基线**（见第 5 节全部条目，公网形态下不可裁剪）
- [ ] 部署：Docker Compose + Caddy 反代模板；systemd unit

### P1

- [ ] 入站 `POST /v1/responses`、`POST /v1beta/models/{m}:generateContent`
- [ ] 策略 `cost-first`（价格表）/ `latency-first`（滑窗 p50）
- [ ] 计费：自定义价格表、按 key 累计消费、日 / 月配额
- [ ] 上游健康：主动探活、失败冷却、半开恢复
- [ ] 上下文压缩（对标 `smart_compact`），可开关
- [ ] 管理面 TOTP 二次验证

### P2

- [ ] IM 远程控制（Telegram / 钉钉 / 飞书）
- [ ] 多节点：SQLite → Postgres + Redis 共享限速与配额
- [ ] RBAC、审计导出、OIDC 登录
- [ ] 语义缓存（按模型维度开关）

---

## 5. 公网安全设计

> 用户决策为**公网暴露**。这一节不是可选项，是 MVP 的一部分。

### 5.1 威胁模型（最要紧的一条）

**上游额度是你自己的账号**。公网暴露 + 任意人拿到 key = 别人白嫖你的订阅额度，
且行为记在你账号上。因此设计原则是**默认拒绝、显式授权、额度护栏先行**：

1. 新建 key **默认额度 0、模型白名单为空**——必须管理员显式配置才能用，
   杜绝「先open了再说」。
2. 每个 key 强制日 / 月 token 上限与并发上限，可设全局默认值兜底。
3. 全局预算熔断：全站当日 token 超阈值自动拒绝新请求并告警。
4. 用量异常检测：单 key 单位时间用量突增 → 告警（可选自动暂停）。

### 5.2 传输与边界

| 项 | 措施 |
|---|---|
| TLS | 强制 HTTPS。二选一：内置 `autocert`（域名 + 80/443）或反代（Caddy / nginx）终止。**不允许明文对外** |
| 端口 | 只暴露 443（+80 用于 ACME）；管理面不单独开端口 |
| 可信代理 | `TRUSTED_PROXIES` 显式配置。**不配则忽略 `X-Forwarded-For`**，否则 IP 限速可被伪造头绕过 |
| 安全头 | HSTS、CSP、`X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options: DENY` |
| CORS | 默认关闭（同源）。需要跨域时配显式白名单，**禁止 `*` + 凭据** |
| 请求体 | 入站上限（默认 32 MiB，可调）→ 超限 413；对齐上游 300 MiB 约束，提前拒绝并给明确错误 |

### 5.3 管理面

- 路径与 API 面分离（`/admin`），独立鉴权：argon2id 密码 + HttpOnly/Secure/SameSite=Strict session cookie。
- 首次启动强制设置管理员密码（未设置时只监听 `127.0.0.1`，设置后才开公网）。
- 登录防爆破：失败计数 + 指数退避 + 锁定；失败记录含 IP / UA 进审计。
- 可选 IP 允许列表（`ADMIN_IP_ALLOW`）。
- P1 加 TOTP。

### 5.4 API 鉴权与密钥

- Key 格式 `fr-` + 32 字节 base62；**只存 SHA-256 哈希**，明文仅在创建时返回一次。
- 列表页只显示 `fr-xxxx…xxxx` 前缀 + 名称，支持即时吊销。
- 上游 secret：`crypto/aes` + GCM 加密后落库，主密钥来自环境变量或 `0600` 文件，**永不入库**。
- 所有密钥比较用 `crypto/subtle.ConstantTimeCompare`，防时序侧信道。

### 5.5 限速与审计

- **三维令牌桶**：全局 / 按 key / 按 IP。超限返回 `429` + `Retry-After`。
- 审计日志（append-only）：管理操作、鉴权失败、额度熔断、上游错误。
- **默认不记录请求正文**。确需排查时开启脱敏模式（保留结构、抹掉文本内容），并在 UI 明示。

### 5.6 运行态加固

- 容器以非 root 运行，`--read-only` 根文件系统 + 仅数据目录可写；`--cap-drop=ALL`。
- 健康检查 `/healthz`（进程）与 `/readyz`（含上游连通性）分离。
- 日志轮转；`slog` 输出 JSON 便于采集。
- 依赖漏洞扫描进 CI（`govulncheck`）。

---

## 6. 数据模型

SQLite（WAL），编号 SQL 迁移，启动时自动执行。

| 表 | 关键字段 | 说明 |
|---|---|---|
| `tenants` | id, name, status, created_at | 租户（个人用可只有 default） |
| `api_keys` | id, tenant_id, key_hash, prefix, name, status, expires_at, quota_limit, quota_used, rpm_limit, tpm_limit, daily_token_cap, max_concurrency, model_allow | 只存 hash |
| `upstreams` | id, kind, name, base_url, secret_id, enabled, last_seen_at | kind: `dshgateway` / `openaicompat` |
| `upstream_secrets` | id, upstream_id, ciphertext, nonce | AES-GCM，主密钥不入库 |
| `exposed_models` | id, upstream_id, upstream_model_id, display_name, context_window, modalities, efforts, enabled, source | source: `auto` / `override` |
| `model_aliases` | id, alias, tenant_id(null=全局), enabled | 客户端只认别名 |
| `routes` | id, alias_id, strategy, sticky_key, enabled | 一条别名一套策略 |
| `route_targets` | id, route_id, upstream_id, upstream_model_id, weight, priority, enabled | 候选 |
| `requests` | id, ts, tenant_id, key_id, endpoint, alias, upstream_id, upstream_model_id, status, http_status, error_code, ttft_ms, total_ms, in_tokens, out_tokens, cost | 明细，按天归档 |
| `usage_daily` | day, tenant_id, key_id, upstream_id, model, requests, in_tokens, out_tokens, cost | 看板聚合 |
| `audit_log` | id, ts, actor, action, target, ip, ua, result, detail | append-only |
| `settings` | key, value | 全局配置 |

---

## 7. 与 dsh-providers-hub 的接线

### 7.1 启动自举

1. 读 `$DSH_HOME/openai-gateway/api-key`（`DSH_HOME` 默认 `~/.local/share/dsh`，
   兼容 `~/.dsh`）→ 加密存进 `upstream_secrets`，标记 `auto`。
2. `GET /v1/models` 拉目录 → 写 `exposed_models`（`source=auto`；用户改过的记 `override`，
   刷新时不覆盖）。
3. 端口不通不阻塞启动：控制台顶部挂「上游离线，检查 DSH 是否在跑」横幅，每 30s 重试。
4. `readyz` 反映上游连通性，`healthz` 只反映进程。

### 7.2 模型命名

对外两级命名，都可用：
- **裸名**：`deepseek-flash`（需配别名，指向多个候选才有意义）
- **限定名**：`<upstream_id>:<upstream_model_id>`，如 `dsh:deepseek-account/deepseek-flash`

`/v1/models` 的 `owned_by` 填 `<upstream_id>`，`name` 填可读名 + 渠道后缀，
与 providers-hub 的展示习惯一致（它用 `· <渠道>` 标首选渠道）。

### 7.3 打标与对账

不改 providers-hub 的代码，用 `x-free-router-request-id` 请求头 + 本地 `requests` 表
双向对账：它记它的 `gateway` 通道，我们记租户与 key，按时间窗 + 模型 + token 数对齐。

### 7.4 契约测试（防漂移）

对 8326 的 4 个端点各写一组契约测试，锁住：状态码、鉴权失败形态、
`/v1/models` 条目字段、SSE 帧形状、413 行为。providers-hub 升级后先跑契约测试，
红了再适配——**不靠人肉回归**。

---

## 8. 里程碑

| 阶段 | 内容 | 产出 | 估时 |
|---|---|---|---|
| M0 | 骨架：go.mod、chi 起服务、SQLite 迁移、配置、`/healthz` | `go run ./cmd/freerouter` 能起 | 0.5 天 |
| M1 | OpenAI Chat 入站 + dshgateway 上游 + 单 key 鉴权 + SSE | Cherry Studio 跑通一轮对话 | 1 天 |
| M2 | Anthropic Messages 入站（含 tool_use / thinking） | Claude Code 接得进来 | 1 天 |
| M3 | 多租户 key + 三维限速 + 额度护栏 + 请求日志 | 可对外发 key | 1 天 |
| M4 | 公网安全基线（TLS、管理面鉴权、审计、安全头）+ Caddy/Docker 部署 | 可安全暴露公网 | 1 天 |
| M5 | 控制台七页 + 模型目录 + 路由策略编辑 | 可运维 | 1.5 天 |
| M6 | 故障转移 / 冷却 / 探活 + 契约测试 + 压测 | 可长期跑 | 1 天 |

约 7 天有效工作量。M1 结束即有实用价值，M2 结束覆盖两大主流客户端。
**M4 不可跳过**——公网形态下，没有安全基线的服务等于把账号送出去。

---

## 9. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| **公网泄露 key → 白嫖本人账号额度** | 额度损失 + 账号风险 | 默认额度 0；日/月上限 + 并发上限；全局熔断；异常告警；一键吊销 |
| providers-hub 升级接口漂移 | 上游断 | 隔离在 `upstream/dshgateway` 一个包，只依赖 4 端点 + 契约测试 |
| 上游拒绝通配 / 只绑回环 | 跨机不可用 | 唯一出口原则：上游永走 `127.0.0.1`，对外只在 FreeRouter |
| 300 MiB body / 120s keep-alive | 413、连接重置 | 入站设更小上限并给明确错误；自管上游连接池；长会话提示客户端 |
| 各家思考档位 / 模态方言不一 | 静默降级或 400 | IR 保留「未知」第三态；缺能力明确报错 |
| SSE 分片乱序 / tool_call id 重组 | 工具调用错乱 | 按 `index` 缓存 id 重组 |
| 反代后 IP 被伪造 | 限速失效 | `TRUSTED_PROXIES` 未配则忽略 `X-Forwarded-For` |
| 管理面被盗 | 全站失守 | argon2id + session + 退避锁定 + 可选 IP 白名单 + (P1) TOTP |
| 单点进程 | 服务中断 | systemd `Restart=always` + 优雅关闭（在途请求 drain）；P2 多节点 |
| 借额度自用的合规边界 | 账号风控 | README 写清「仅限本人已登录账号、自用」；不内置任何绕过、代理池、指纹伪装 |

---

## 10. 验收标准（MVP）

1. `curl` 用新 key 打 `/v1/chat/completions`，`stream:true` 逐帧吐字，`[DONE]` 正常。
2. Claude Code 设 `ANTHROPIC_BASE_URL` 指过来 + 填 key，完成一次带工具调用的编码任务。
3. 停掉 DSH（8326 关闭）：返回 `502 upstream_unavailable` 而非挂起，`readyz` 变红，控制台显示离线。
4. key 打满 RPM → `429` + 有值 `Retry-After`；打满日额度 → `402`/`429` 且日志留痕。
5. 新建 key 不改任何配置时**无法发起请求**（默认拒绝）。
6. 日志页可见：端点、别名、实际上游、TTFT、总耗时、token。
7. 公网访问无 HTTPS 时被拒；伪造 `X-Forwarded-For` 不能绕过 IP 限速。
8. `docker compose up -d` 后宿主 443 等价于第 1 条。
9. `govulncheck ./...` 无高危；容器非 root 运行。

---

## 11. 开工前需你提供

公网形态下这几项缺一不可，先列出来免得 M4 卡住：

1. **域名**（用于 ACME 签发证书，或确认走反代终止）
2. **部署位置**：本机直连公网 / 有公网 IP 的服务器 / 内网 + 隧道（Cloudflare Tunnel 等）
3. **反代方案**：Caddy（推荐，自动证书）/ nginx / 不用反代由 Go 直接终止 TLS
4. **管理端地址**：是否限制来源 IP

这些不阻塞 M0–M3（先在本机 `127.0.0.1` 跑起来），但 M4 之前要有答案。
