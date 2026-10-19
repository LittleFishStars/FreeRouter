# FreeRouter 方案（v0）

> AI 中转站，对标 [Tingly Box](https://github.com/tingly-dev/tingly-box)，上游复用
> [iJetLi/dsh-providers-hub](https://github.com/iJetLi/dsh-providers-hub) 的订阅账号池。
>
> 状态：**方案待确认**，未开始编码。

---

## 0. 一句话结论

FreeRouter 把 dsh-providers-hub 已经具备的「本机订阅额度池 + 本机 OpenAI 网关」，
从**只服务本机 DSH 会话**，升级成一个**多协议、多租户、带策略路由和 Web 控制面**的
AI 中转站：入站收 OpenAI / Anthropic / Gemini 三家协议，出站打向
`127.0.0.1:8326`（默认）或直连 DSH 的 `ctx.llm`。

**核心差异化**：上游不是买来的 API Key，而是**你自己已登录的订阅账号池**（17 个渠道、
多账号、临期积分自动优先烧）。这是 FreeRouter 与 One API / New API / Tingly Box
最本质的区别——它们解决「多 Key 聚合」，FreeRouter 解决「免费/订阅额度的统一出口」。

---

## 1. 两个参照物拆解

### 1.1 Tingly Box：抄什么

Go 单体，MPL-2.0，`npx tingly-box` 起在 `:12580`，自带 Web UI 与 wails3 桌面壳。

| 能力 | 它的做法 | 我们的取舍 |
|---|---|---|
| 统一端点 + 协议互转 | OpenAI / Anthropic / Google 三家互相翻译，`internal/protocol/{request,stream,nonstream,token,transformer}` | **照抄**，这是中转站的地基 |
| 智能路由 | 按成本 / 速度 / 自定义策略跨模型跨 token 选路（`internal/smart_routing`） | **照抄**，但候选集换成「渠道 × 账号」 |
| 复用已有订阅额度 | OAuth 接 Claude Code / Gemini，把订阅额度当 API 用 | **这正是 dsh-providers-hub 已经在做的事**，我们直接继承 |
| Web 控制面 | providers / routes / aliases / models 可视化管理 | **照抄**，做成 Keys / Routes / Logs / Playground |
| 客户端用量统计 | 逐请求 token、延迟、成本估算 | **照抄**，且能与 providers-hub 的 token-ledger 对账 |
| 上下文压缩 | `internal/smart_compact`（对话蒸馏） | P1 再说，先不背这个复杂度 |
| IM 远程控制 | Telegram / 钉钉 / 飞书 / Slack / Discord | P2 |
| 部署 | npm / 源码 / Docker（数据卷 `~/.tingly-box`） | **照抄**三形态 |

它刻意**没有**的东西（我们也不需要）：不内置账号池登录、不管额度到期、
不做积分领取。这三块正是 providers-hub 的地盘。

### 1.2 dsh-providers-hub：结合什么

TypeScript，MIT，DSH 插件，当前本机装的是 `0.2.1-rc.2`（profile `web`，link 安装）。

**它已经提供了什么（我们全部继承，不重写）：**

1. **17 + 1 个 provider 路由**，每家一套 auth / adapter / credits / oauth 实现：
   `codearts` `buddy` `workbuddy` `lobsterai` `qoder` `qodercn` `trae` `cline` `loomy`
   `raccoon` `minimax` `minimaxintl` `zcode` `opencode` `gemini` `officeace` `accio`。
2. **账号池**（`AccountPool`）：同渠道多账号、拖拽排序即优先级、限流切换、
   策略拦截冷却、停用不影响续期。
3. **凭据静默续期**：到期前自动刷新，无需再开浏览器。
4. **积分体系**：余额查询、每日签到 / 领取、锁定永久积分、按临期排序。
5. **聚合路由**（`aggregate`）：同一真实模型跨渠道归并，按「最快作废的额度优先」排序，
   候选级失败切换（仅在还没吐出可见内容时切换），`aggregate/auto` 全池兜底。
6. **本机 OpenAI 兼容网关**（`:8326`，实测已在本机运行）：
   ```
   GET  /v1/models
   GET  /v1/reasoning-efforts
   POST /v1/chat/completions     ← OpenAI Chat Completions
   POST /v1/responses            ← OpenAI Responses API
   ```
   Bearer 鉴权，key 在 `$DSH_HOME/openai-gateway/api-key`（实测 `无 Bearer → 401`）。
7. **记账**：`$DSH_HOME/jet-hub/token-ledger.json`，配合 `GATEWAY_CHANNEL_MARK`
   （模块级 Symbol）区分 `direct` / `gateway` 双路流量。

**它明确不提供（这即是 FreeRouter 的生存空间）：**

| 缺口 | 说明 |
|---|---|
| 只有 OpenAI 协议 | Anthropic `messages` / Gemini `generateContent` 入站全无 |
| 没有多租户 | 单一 API Key，没有 key 管理、配额、限速、分账 |
| 没有策略路由 | 只有「临期优先」一种排序，不能按成本 / 速度 / 权重 / 自定义 |
| 没有独立控制面 | 配置散在 DSH 的 Jet Hub 面板里，不是中转站管理台 |
| 默认只绑 `127.0.0.1` 且**拒绝** `0.0.0.0` | 设计上就不打算对外；跨机复用要手改 host 填具体网卡 IP |
| 没有请求日志 / 用量看板 | 只有内部 token-ledger，不是面向运维的日志与统计 |
| 不能与 DSH 解耦 | 登录、续期、模型目录都挂在 DSH 进程里 |

> ⚠️ 关键约束（读它的文档得来，必须写进我们的适配层）：
> 网关拒绝通配地址；请求体上限 **300 MiB**（超限回 413
> `request_too_large`）；空闲 keep-alive **120 秒**后服务端发 FIN；
> 每轮全量回传历史的客户端（Codex 一类）可能撞 413。

### 1.3 复用的技术锚点（精确到接口）

**A. HTTP 上游（最省事，零侵入）**

```http
POST http://127.0.0.1:8326/v1/chat/completions
Authorization: Bearer <$DSH_HOME/openai-gateway/api-key>
Content-Type: application/json
```

`/v1/models` 实测返回的 `id` 形如 `deepseek-official/deepseek-flash`、
`deepseek-account/deepseek-flash`、`aggregate/...`，每个条目带
`name` / `input:["text","image"]` / `reasoning.efforts[]` / `openai_efforts[]`。
**这份目录就是我们的上游模型清单来源。**

**B. DSH 插件形态（深度结合，直连运行时）**

网关内部只依赖一个极窄的运行时面（`lib/openai-gateway/server.d.ts`）：

```ts
interface LlmRuntimeLike {
  listProviders(): readonly { id: string }[];
  listModels(provider: string): Promise<readonly LlmModelInfo[]>;
  resolveModelInfo(provider: string, model: string, signal?: AbortSignal): Promise<unknown>;
  stream(options: GenerateOptions): AsyncIterable<StreamChunk>;
}
```

`ctx.llm` 满足它。只要能拿到 `ctx`，就能绕开 HTTP、直接吃 providers-hub
注册进 DSH 的 18 条路由——**包括账号池选号、限流切换、临期排序全部生效**，
且能拿到 `LlmModelInfo` 的完整元数据（上下文窗口、模态、思考档位）。

**C. 代码级复用（可选，收益递减）**

`dsh-providers-hub` 的 `package.json` 只导出 `.` 与 `./client`，内部模块需按
`lib/xxx.js` 深路径 import（版本升级会漂）。值得借的只有两处：

- `lib/openai-gateway/{messages,responses,stream,images}.js` —— OpenAI 双向序列化与 SSE 解析
- `lib/openai-gateway/channel.js` —— `markGatewayChannel()` 打标思路（我们打自己的标）

**结论：不做代码级硬依赖**，只用「HTTP 上游 + 可选插件直连」两种接线，
把 providers-hub 当成**语义稳定的外部上游**，升级不伤我们。

---

## 2. FreeRouter 的位置

```
                       ┌─────────────────────────────────────────┐
   Claude Code ────────▶│  Anthropic Messages  /v1/messages      │
   Cherry Studio ──────▶│  OpenAI Chat        /v1/chat/...       │──┐
   Cursor / Cline ─────▶│  OpenAI Responses   /v1/responses      │  │
   Gemini CLI ─────────▶│  Gemini generateContent /v1beta/...    │  │
                       └─────────────────────────────────────────┘  │
                                    ▲                               │
                          ┌─────────┴──────────┐                    │
                          │  FreeRouter 内核    │                    │
                          │  IR · 路由 · 策略   │                    │
                          │  记账 · 多租户      │                    │
                          └─────────┬──────────┘                    │
                                    │ 出站                          │
              ┌─────────────────────┴──────────────────┐            │
              │                                        │            │
   ① HTTP 上游（默认）                      ② 插件直连（可选）        │
   127.0.0.1:8326                           ctx.llm                  │
   Bearer api-key                           LlmRuntimeLike           │
              │                                        │            │
              └────────────────┬───────────────────────┘            │
                               ▼                                     │
                  ┌────────────────────────────┐                     │
                  │  dsh-providers-hub          │                     │
                  │  18 路由 · 账号池 · 续期    │◀────────────────────┘
                  │  积分 · 临期排序 · 聚合     │      （同一份额度池，
                  └────────────────────────────┘        不同出口）
                               │
              ┌────────────────┼────────────────┐
              ▼                ▼                ▼
          CodeBuddy        CodeArts          TRAE … 17 家
```

**一句话定位**：providers-hub 管「额度从哪来」，FreeRouter 管「额度怎么发出去」。

---

## 3. 总体架构

三层，中间用**统一 IR** 隔开。IR 是全部复杂度的收敛点：N 种入站 × M 种出站，
不收敛就是 N×M 份转换代码。

```
┌── L1 入站协议层 ────────────────────────────────────────────┐
│  openai-chat · openai-responses · anthropic-messages         │
│  · gemini-generateContent                                    │
│  职责：HTTP/SSE 解码 → 鉴权 → 配额/限速 → 解析成 IR 请求      │
└──────────────────────────┬──────────────────────────────────┘
                           ▼
┌── L2 内核 ──────────────────────────────────────────────────┐
│  router：模型别名 → 路由规则 → 候选目标（渠道/账号/模型）     │
│  policy：临期优先 / 成本优先 / 速度优先 / 权重 / 故障转移     │
│  call：候选级重试（未输出前可切）、冷却、超时、取消传播        │
│  meter：token / 延迟 / 成本 / 渠道打标 → 记账                  │
│  store：SQLite（keys·routes·requests·usage）                  │
└──────────────────────────┬──────────────────────────────────┘
                           ▼
┌── L3 上游适配层 ────────────────────────────────────────────┐
│  upstream/dsh-gateway   → HTTP 8326（默认，零侵入）           │
│  upstream/dsh-runtime   → ctx.llm（插件形态，直连）           │
│  upstream/openai-compat → 任意标准 OpenAI 兼容端点（扩展位）   │
│  职责：IR → 上游协议 → SSE 流 → IR 事件                       │
└─────────────────────────────────────────────────────────────┘
```

### 3.1 IR 设计要点

- **消息**：`role` + `parts[]`（`text` / `image` / `tool_call` / `tool_result` / `thinking`），
  对齐 Anthropic 的 content block 粒度——它最能表达富内容，向下映射到 OpenAI 最简单。
- **流式事件**：`start` / `thinking_delta` / `text_delta` / `tool_call_start|delta|end` /
  `usage` / `error` / `done`。每个入站协议各写一个「事件 → 自己的 SSE 帧」渲染器。
- **不做「把 OpenAI 转成 Anthropic 文本」**这类有损降级；缺失能力明确报错，
  宁可 400 也不要静默丢图片 / 丢 tool_use（providers-hub 踩过这类坑：
  静默丢图会让模型幻觉出答案）。

---

## 4. 功能清单

### MVP（先能用）

- [ ] 入站 `POST /v1/chat/completions`（流式 + 非流式 + tool_calls + 图片）
- [ ] 入站 `POST /v1/messages`（Anthropic，含 `tool_use` / `thinking` / SSE 事件名对齐）
- [ ] 入站 `GET /v1/models`（按租户可见性过滤，`owned_by` 映射成路由名）
- [ ] 多租户 API Key：创建 / 禁用 / 过期 / 额度 / RPM·TPM 限速 / 模型白名单
- [ ] 上游：`dsh-gateway`（HTTP 8326），自动发现模型目录并缓存
- [ ] 路由：`direct`（指定 `provider/model`）、`alias`（别名 → 多个目标）
- [ ] 策略：`expiry-first`（临期优先，默认）、`weighted`、`failover`
- [ ] 请求日志：路径、模型、上游、状态、首 token 延迟、总耗时、token 用量、错误码
- [ ] Web 控制台：Overview / Keys / Upstreams / Routes / Models / Logs / Playground
- [ ] 部署：`docker compose up` 一条命令；非回环绑定时强制要求 master key

### P1

- [ ] 入站 `POST /v1/responses`（OpenAI Responses API，对齐 providers-hub 已实现的语义）
- [ ] 入站 `POST /v1beta/models/{m}:generateContent`（Gemini）
- [ ] 策略：`cost-first`（按每 provider 的倍率/价格表选最便宜）、`latency-first`（滑窗 p50）
- [ ] 额度计费：自定义价格表 + 按 key 累计消费 + 日/月配额
- [ ] 上游健康：主动探活、失败冷却、半开恢复
- [ ] 可选插件形态：`upstream/dsh-runtime` 直连 `ctx.llm`
- [ ] 上下文压缩（对标 `smart_compact`）：长对话蒸馏，可开关

### P2

- [ ] IM 机器人远程控制（Telegram / 钉钉 / 飞书）
- [ ] 多节点：共享 SQLite→Postgres + Redis 限速与配额
- [ ] 管理面 RBAC、审计日志导出
- [ ] 语义缓存（命中即回放，按模型维度开关）

---

## 5. 数据模型

SQLite（WAL），迁移用编号 SQL 文件，启动时自动执行。

| 表 | 关键字段 | 说明 |
|---|---|---|
| `tenants` | id, name, status, created_at | 租户（个人用可以先只有 default） |
| `api_keys` | id, tenant_id, key_hash, prefix, name, status, expires_at, quota_limit, quota_used, rpm_limit, tpm_limit, model_allow | 只存 hash，明文仅创建时返回一次 |
| `upstreams` | id, kind(`dsh-gateway`/`dsh-runtime`/`openai-compat`), name, base_url, secret_ref, enabled | `secret_ref` 指向本地 secret 表或 env |
| `upstream_secrets` | id, upstream_id, ciphertext, nonce | 用本机 key 加密，避免明文落库 |
| `exposed_models` | id, upstream_id, upstream_model_id, display_name, context_window, modalities, efforts, enabled | 上游目录快照 + 人工覆盖 |
| `model_aliases` | id, alias, tenant_id(null=全局), enabled | 客户端只认别名 |
| `routes` | id, alias_id, strategy, sticky_key, enabled | 一条别名一套策略 |
| `route_targets` | id, route_id, upstream_id, upstream_model_id, weight, priority, enabled | 候选 |
| `requests` | id, ts, tenant_id, key_id, endpoint, alias, upstream_id, upstream_model_id, status, http_status, error_code, ttft_ms, total_ms, in_tokens, out_tokens, cost | 明细，按天分区/归档 |
| `usage_daily` | day, tenant_id, key_id, upstream_id, model, requests, in_tokens, out_tokens, cost | 看板聚合 |
| `settings` | key, value | 全局配置 |

---

## 6. 目录结构

```
FreeRouter/
├── package.json                 # pnpm workspace 根
├── pnpm-workspace.yaml
├── docker-compose.yml
├── docs/PLAN.md                 # 本文件
├── packages/
│   ├── core/                    # L2 内核（纯 TS，无 HTTP 依赖，可单测）
│   │   ├── ir/                  # 消息与流式事件类型
│   │   ├── router/              # 别名解析 + 策略 + 冷却
│   │   ├── meter/               # token 记账、渠道打标
│   │   └── store/               # SQLite + 迁移
│   ├── protocols/               # L1/L3 协议转换
│   │   ├── openai-chat/         # 入站 + 出站
│   │   ├── openai-responses/
│   │   ├── anthropic/
│   │   └── gemini/
│   ├── upstreams/
│   │   ├── dsh-gateway/         # HTTP 8326
│   │   ├── dsh-runtime/         # ctx.llm（插件形态）
│   │   └── openai-compat/
│   ├── server/                  # Fastify 装配、鉴权、限速、路由注册
│   └── web/                     # Next.js 控制台
└── scripts/                     # 起服务、探活、备份
```

---

## 7. 技术选型

| 层 | 选择 | 理由 |
|---|---|---|
| 后端 | TypeScript + Node 22 + Fastify | 与 dsh 生态同构，`upstream/dsh-runtime` 必须能 import `@deepseek-ai/dsh-llm`；代理是 IO 密集，Node 足够 |
| 前端 | Next.js + Tailwind + shadcn/ui | 与既有习惯一致 |
| 存储 | SQLite（better-sqlite3，WAL）| 单机零运维；表结构留好迁移到 Postgres 的余地 |
| 流式 | 原生 `fetch` + `ReadableStream`，手写 SSE 解析 | 各家 SSE 方言差异大，统一收口在 IR 层 |
| 包管理 | pnpm workspace | 与 dsh 插件生态一致 |
| 部署 | Docker Compose（可选 Caddy 反代） | 对标 Tingly Box 的一键起 |

### ⚠️ 需要你决定的取舍：后端语言

**推荐 TS**。理由：`upstream/dsh-runtime` 这条深度结合的路只能在 Node 进程里走
（要 `ctx.llm`、要 `@deepseek-ai/dsh-llm` 的类型）；选 Go 就只剩 HTTP 上游一条路，
协议转换器要全部重写一遍（Tingly Box 的 `internal/protocol` 就是 Go 写的，
可以照着抄思路但代码不能直接借）。

**若你更想要 Go**（GoLand 习惯、并发更强），代价明确：
- 只保留 `dsh-gateway` HTTP 上游（其实 MVP 够用）
- 协议层用 Go 重写（自带 SSE + `httputil.ReverseProxy` 改造成本不高）
- 好处：单二进制分发、内存占用低、长期跑更稳
- 折中：**核心 Go + 一个薄 Node 侧车**只在需要插件直连时启用

请在第 12 节给个明确选择。

---

## 8. 里程碑

| 阶段 | 内容 | 产出 | 估时 |
|---|---|---|---|
| M0 | 骨架：workspace、Fastify 起服务、SQLite 迁移、登录页 | `pnpm dev` 能开控制台 | 0.5 天 |
| M1 | OpenAI Chat 入站 + dsh-gateway 上游 + 单 key 鉴权 | Cherry Studio 能跑通一轮对话 | 1 天 |
| M2 | Anthropic Messages 入站（含 tool_use / thinking） | Claude Code 能接进来 | 1 天 |
| M3 | 多租户 key + 限速 + 配额 + 请求日志 | 可对外发给别人用 | 1 天 |
| M4 | 控制台七页 + 模型目录 + 路由策略编辑 | 可运维 | 1.5 天 |
| M5 | 故障转移 / 冷却 / 探活 + Docker 打包 | 可长期跑 | 1 天 |

M1 结束时就有可用价值；M2 结束即覆盖最常见的两个客户端。
总计约 6 天有效工作量（不含 P1/P2）。

---

## 9. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| providers-hub 升级导致接口漂移 | 上游断 | 全部隔离在 `upstreams/dsh-gateway` 一个包里，只依赖 4 个 HTTP 端点；写契约测试锁字段 |
| 上游只绑回环 / 拒绝通配 | 跨机不可用 | 我们自己监听对外端口；上游永远走 `127.0.0.1`；跨机场景由 FreeRouter 做唯一出口 |
| 300 MiB body / 120s keep-alive | 大图 + 长会话 413、连接重置 | 入站也设同类上限并给明确错误；对客户端做 body 透传时不额外缓存；上游连接池自己管理 |
| 各家思考档位 / 模态方言不一 | 静默降级或 400 | IR 里保留「未知」第三态；缺能力明确报错，不静默丢 |
| SSE 分片 id 重组（tool_calls） | 工具调用错乱 | 按 `index` 缓存 id，与 providers-hub 的 `consumeSse` 同思路 |
| 借用订阅额度的使用边界 | 账号风险 | **默认只绑 `127.0.0.1`**，开对外必须显式配置 master key；README 写清「仅限本人已登录账号、自用」；不内置任何绕过或代理池 |
| 密钥落库 | 泄露 | key 只存 hash；上游 secret 用本机 key 派生的 AES-GCM 加密 |

---

## 10. 与 dsh-providers-hub 的具体接线（实施细节）

### 10.1 启动即自举

1. 读 `$DSH_HOME/openai-gateway/api-key`（`DSH_HOME` 默认 `~/.local/share/dsh`，
   兼容 `~/.dsh`）→ 存进 `upstream_secrets`，标记来源为 `auto`。
2. `GET /v1/models` 拉目录 → 写 `exposed_models`（`enabled` 默认跟随上游，
   用户改过的记 `override`）。
3. 若端口不通：不阻塞启动，控制台顶部挂一条「上游离线，检查 DSH 是否在跑」的横幅，
   并每 30s 重试。

### 10.2 模型命名

对外暴露两级命名，二者都可用：

- **裸名**：`deepseek-flash`（需要配置别名，指向多个候选才有意义）
- **限定名**：`<upstream_id>:<upstream_model_id>`，如 `dsh:deepseek-account/deepseek-flash`

`/v1/models` 里 `owned_by` 填 `<upstream_id>`，`name` 填可读名 + 渠道后缀，
与 providers-hub 的展示习惯保持一致（它用 `· <渠道>` 后缀标首选渠道）。

### 10.3 打标与对账

我们自己的流量要在 providers-hub 的账本里可区分。**不改它的代码**，用
`x-free-router-request-id` 头 + 本地 `requests` 表双向对账即可：
它记它的 `gateway` 通道，我们记我们的租户与 key，通过时间窗 + 模型 + token 数对齐。

### 10.4 插件形态（P1）

做成 DSH 插件时，形状与 providers-hub 一致：`cordis.patch.yml` 里挂一行，
`apply(ctx)` 里取 `ctx.llm`，把 `upstream/dsh-runtime` 指向它，再 `ctx.server`-level
监听自己的端口（或复用 DSH 的 HTTP 面挂子路径）。**前提是 DSH 允许插件注册
非 LLM 的 HTTP 路由**——这一点在 P1 开工前要先验证，验证失败就退回只做 HTTP 上游。

---

## 11. 验收标准（MVP）

1. `curl` 用新 key 打 `/v1/chat/completions`，`stream:true` 能逐帧吐字，`[DONE]` 正常。
2. Claude Code 把 `ANTHROPIC_BASE_URL` 指过来、填 key，能完成一次带工具调用的编码任务。
3. 断掉 DSH（8326 关闭）时，FreeRouter 返回 `502 upstream_unavailable` 而不是挂起，
   控制台显示离线。
4. 一条 key 打满 RPM 后返回 `429`，`Retry-After` 有值。
5. 日志页能看到该请求的：端点、别名、实际上游、TTFT、总耗时、token。
6. `docker compose up -d` 后，`curl` 宿主端口等价于第 1 条。

---

## 12. 待确认的三个决策点

1. **后端语言**：TypeScript（推荐，能深连 DSH）还是 Go（单二进制、性能）？
2. **形态**：独立服务（推荐先做）/ DSH 插件 / 双形态？
3. **暴露范围**：仅本机（推荐默认）/ 局域网 / 公网带鉴权？

确认后我按 M0→M5 开工。
