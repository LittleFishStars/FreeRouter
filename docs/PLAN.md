# FreeRouter 方案（v2）

> AI 中转站，对标 [Tingly Box](https://github.com/tingly-dev/tingly-box)，上游复用
> [iJetLi/dsh-providers-hub](https://github.com/iJetLi/dsh-providers-hub) 的订阅额度池。
>
> **已定决策**：Go + chi · 独立服务 · 公网暴露 + 完整鉴权 ·
> **账号制**（注册需审核 / 管理员导入账密）· **不做智能应用 / 团队 / 远程**
>
> 状态：待你过目，未开工。v2「不做组织层」的判断已被 v3 的账号制取代（见 1.2）。

---

## 0. 定位

FreeRouter 是一个**面向多用户的账号制中转站**：把 dsh-providers-hub 已有的订阅额度池 +
本机 OpenAI 网关，变成一个有入站协议、有访问控制、有日志的对外出口。
账号由注册（需管理员审核）或管理员导入账密产生，每个账号持有自己的 API Key。

**只做三件事**：协议转换、访问控制、可观测。
**不做**：客户端接入管理、团队协作、聊天机器人。

**一句话**：providers-hub 管「额度从哪来」，FreeRouter 管「额度怎么发出去」。

---

## 1. 范围边界：明确不做

### 1.1 砍掉 Tingly Box 的三块

| 模块 | 它实际是什么（证据） | 我们的处置 |
|---|---|---|
| **智能应用** | 中文 UI 里 Agent 模块的标题（`frontend/src/i18n/locales/zh/agent.ts` 的 `"title": "智能应用"`）。它是**面向 agent 客户端的接入向导 + 逐客户端监控台**：把 Claude Code / Codex / OpenCode 逐个登记，显示「已接入」「最近请求」「错误率」「用量」「日志」，带"开始使用这个 Profile / 连接 AI"引导 | **不做**。我们的观测维度是 **API Key**，不是"已接入的客户端"。谁在用不重要，用了多少才重要 |
| **团队** | README `Team - Shared Model Deployment`：共享端点 + 集中管控模型规则 + *"Each member gets a dedicated Team Key, and usage is tracked per user"* + `Dashboard → Team usage` 面板 | **不做**。用扁平的账号制替代它，见 1.2 |
| **远程** | `remote.ts`：IM Bot 配置（TG / 微信 / 企业微信 / 飞书 / Lark / 钉钉），字段含 `bashAllowlist`（允许 `/bash` 执行 `cd/ls/pwd`）、`chatIdLock`、`proxyUrl` | **不做**，整个模块从路线图删除。v1 曾把它列在 P2，现在连 P2 都不留 |

### 1.2 「团队」不做，但要做账号制

v2 曾把用户管理整体砍掉（"只有一堆 Key，没有人的概念"）。v3 修正：**要账号，不要团队**。
两者不是一回事：

| | 不做：团队（Tingly Box 的 Team） | 做：账号制 |
|---|---|---|
| 组织形态 | 一个团队共享一个端点，成员归属团队 | 每个用户独立账号，互不相干 |
| 准入 | 成员由团队创建 / 邀请 | **注册 + 管理员审核**，或管理员直接导入账密 |
| 归属 | Team Key 属于某成员，用量汇总到团队 | API Key 属于某账号，但账号之间无归属关系 |
| 视角 | Team usage 面板（团队维度汇总） | 「我的用量」只看自己；管理员看全部 |
| 端点 | 每团队一个共享端点 | 全站一个端点，按 Key 区分 |

差别在于**没有"团队"这个中间层**：账号是扁平的、彼此独立的，不存在 A 属于 B，
也没有共享端点、邀请、团队级汇总。管理员看到的是一张账号列表，不是一棵组织树。

**权限模型**（两类角色，视图完全不同）：

| 能力 | 管理员 `admin` | 用户 `user` |
|---|---|---|
| 账号 | 审核 / 批准 / 拒绝 / 禁用 / 导入 / 重置密码 / 改配额 | 改自己的密码 |
| 模型 | 全量可见，定义权限组 | 只见到被授权的组 |
| Key | 全站可查可禁 | 只管自己的（受账号上限约束） |
| 日志 | 全站 | 只看自己的 |
| 上游 | 配置 / 健康 / 密钥 | 不可见 |

**默认最小权限**：审核通过时必须挂一个**权限组**（管理员预先定义，如"基础组 =
aggregate 推荐模型 + deepseek-official"）。新账号不继承任何模型权限。

### 1.3 顺带砍掉的复杂度

| 砍掉 | v1 的设计 | 现在 |
|---|---|---|
| 团队 / 租户 | `tenants` 表、成员归属、团队级汇总 | 删表。改为扁平的 `accounts`（个人账号）：无上级归属、无邀请、无团队汇总 |
| 三层路由 | `model_aliases` + `routes` + `route_targets` | 见 1.4 |
| 计费 | `cost` 字段（金额） | 删。订阅额度没有单价，算不出真钱。只记 token 消耗量 |

### 1.4 路由层：两层聚合会打架，我们退一层

实测发现上游池子里已经有 **`aggregate/*` 共 22 个模型**——providers-hub 自己就做了跨渠道聚合
（同一真实模型跨渠道归并、按额度最快作废优先、候选级失败切换）。

v1 又在上面套了一层 alias → routes → route_targets，等于**两层别名套娃**：
同名模型谁说了算、失败切换在哪层做，都是有歧义的。

**处置：让上层不碰聚合。**

```
aggregate/deepseek-flash              ← 上游自己跨渠道聚合，我们原样透传
dsh:deepseek-official/deepseek-flash  ← 钉死单一渠道，用于调试/对账
```

我们这层只做三件事：
1. **可见性**：哪些模型对哪些 Key 开放
2. **覆盖**：改名、置顶、隐藏、分组
3. **兜底**：上游整个不可达时的降级与错误映射

不做权重、不做多策略、不做 alias 到多目标的自动选路——那是上游的活。

---

## 2. 上游：dsh-providers-hub 实测现状

TypeScript，MIT，DSH 插件，本机 `0.2.1-rc.2`（profile `web`，link 安装）。

### 2.1 它提供（全部继承，不重写）

1. **20 个上游前缀 / 116 个模型**（实测 `GET /v1/models` → 200 / 48787 B）：

   | 前缀 | 数 | 前缀 | 数 | 前缀 | 数 |
   |---|---:|---|---:|---|---:|
   | `aggregate` | 22 | `buddy` | 6 | `loomy` | 4 |
   | `cline` | 13 | `qodercn` | 6 | `raccoon` | 4 |
   | `workbuddy` | 9 | `accio` | 6 | `autoclaw` | 4 |
   | `lobsterai` | 8 | `trae` | 7 | `modelscope` | 4 |
   | `qoder` | 7 | `tingly-box` | 4 | `codearts` | 3 |
   | `deepseek-official` | 2 | `deepseek-account` | 2 | `minimax` / `zcode` | 各 2 |
   | `jet-hub-auto` | 1 | | | | |

2. **账号池**：同渠道多账号、拖拽排序即优先级、限流切换、策略拦截冷却、停用不影响续期。
3. **凭据静默续期**、**积分体系**（余额 / 签到 / 领取 / 临期排序）。
4. **聚合路由** `aggregate/*`（见 1.4）。
5. **本机 OpenAI 兼容网关** `127.0.0.1:8326`：

   | 端点 | 说明 |
   |---|---|
   | `GET /v1/models` | 模型目录（含 `name` / `input` / `reasoning.efforts` / `openai_efforts`） |
   | `GET /v1/reasoning-efforts` | 思考档位 |
   | `POST /v1/chat/completions` | OpenAI Chat Completions |
   | `POST /v1/responses` | OpenAI Responses API |

   Bearer 鉴权，key 在 `$DSH_HOME/openai-gateway/api-key`（43 字符）。
   `~/.dsh` 只是 `~/.local/share/dsh` 的软链，**不是两个路径**。

   样例条目：
   ```json
   {"id":"deepseek-official/deepseek-flash","owned_by":"deepseek-official",
    "name":"DeepSeek-V4.1-Flash","input":["text","image"],
    "reasoning":{"efforts":[{"id":"off","canonical":"none"},{"id":"low"},
                            {"id":"high"},{"id":"max"}]}}
   ```

6. **记账**：`$DSH_HOME/jet-hub/token-ledger.json`，模块级 Symbol `GATEWAY_CHANNEL_MARK` 区分双路流量。

### 2.2 它不提供（FreeRouter 的生存空间）

| 缺口 | 说明 |
|---|---|
| 只有 OpenAI 协议 | Anthropic `messages` / Gemini `generateContent` 入站全无 |
| 没有访问控制 | 单一 API Key，无 key 管理、配额、限速 |
| 没有独立控制面 | 配置散在 DSH 的 Jet Hub 面板里 |
| 只绑 `127.0.0.1`，**拒绝** `0.0.0.0` | 设计上就不打算对外 |
| 没有请求日志 / 用量看板 | 只有内部 token-ledger |
| 不能与 DSH 解耦 | 登录、续期、模型目录都挂在 DSH 进程里 |

### 2.3 上游硬约束（写进适配层）

- 拒绝通配地址 → 我们**唯一出口原则**：上游永走 `127.0.0.1`，对外只在 FreeRouter。
- 请求体上限 **300 MiB**（超限 413 `request_too_large`）；入站设更小上限（默认 32 MiB）提前拒绝。
- 空闲 keep-alive **120 秒**后服务端发 FIN → 自管连接池，不复用死连接。
- **实测存在偶发连接失败**：首次带 Bearer 的请求拿到 `http=000`，重试即通。
  → 上游客户端**必须有重试与连接池**，不能裸 `http.Get`。

### 2.4 关于 Tingly Box 的定位修正

v1 把它写成纯"对标对象"，不准确：**它已经作为上游渠道接进池子了**
（`tingly-box` 前缀 4 个模型）。所以关系是——

- 它在**出站侧**是我们池子里的一个渠道
- 我们在**入站侧**是它同类的出口
- 两者可共存；只借它 `internal/protocol` 的设计思路，不引代码（MPL-2.0）

### 2.5 不做代码级复用

providers-hub `package.json` 只导出 `.` 与 `./client`，内部模块深路径 import 会随版本漂。
**只把 4 个 HTTP 端点当契约**，写契约测试锁字段。

> 补充：v1「P1 做 DSH 插件直连 `ctx.llm`」已作废——`ctx.llm` 是 DSH 的 **Node 进程内对象**，
> Go 进程跨不过去。代价是拿不到 `LlmModelInfo` 完整元数据；但账号池选号、限流切换、
> 临期排序**依然全部生效**，因为那些逻辑跑在 providers-hub 进程内。

---

## 3. 架构

```
   客户端 ──▶ ┌────────────────────────────────────┐
  Claude Code │  L1 入站（公网，TLS 终止）          │
  Cherry Studio│  /v1/chat/completions             │
  Cursor/Cline│  /v1/messages        (Anthropic)    │
  Gemini CLI  │  /v1/responses       (OpenAI)       │
              │  /v1beta/models/…:generateContent   │
              │  ───────────────────────────────    │
              │  鉴权 → 限速 → 配额 → 解析成 IR      │
              └─────────────────┬──────────────────┘
                                ▼
              ┌────────────────────────────────────┐
              │  L2 内核（刻意保持薄）              │
              │  route  可见性 → 覆盖 → 透传        │
              │  call   重试 · 超时 · 取消传播      │
              │  meter  token / TTFT / 打标         │
              │  store  SQLite(WAL)                 │
              └─────────────────┬──────────────────┘
                                ▼
              ┌────────────────────────────────────┐
              │  L3 上游                            │
              │  dshgateway → 127.0.0.1:8326        │
              │  openaicompat → 扩展位              │
              │  连接池 · 重试 · 健康探测            │
              └─────────────────┬──────────────────┘
                                ▼
              ┌────────────────────────────────────┐
              │  dsh-providers-hub  :8326           │
              │  20 前缀 · 116 模型 · 账号池 · 聚合  │
              └─────────────────┬──────────────────┘
                                ▼
               CodeBuddy  CodeArts  TRAE  … 共 20 个前缀
```

### 3.1 IR 设计要点

- **消息**：`role` + `parts[]`（`text` / `image` / `tool_call` / `tool_result` / `thinking`），
  对齐 Anthropic content block 粒度——它最能表达富内容，向下映射到 OpenAI 最简单。
- **流式事件**：`start` / `thinking_delta` / `text_delta` / `tool_call_start|delta|end` /
  `usage` / `error` / `done`。
- **不静默降级**：缺失能力明确 400，绝不悄悄丢图片 / 丢 `tool_use`。
- **ID 稳定**：tool_call 的 `id` 按 SSE `index` 缓存重组，不依赖分片到达顺序。

---

## 4. 技术选型

| 层 | 选择 | 理由 |
|---|---|---|
| 语言 | Go 1.27（本机 `go1.27.2 linux/amd64`） | 单二进制、低内存 |
| 路由 | `github.com/go-chi/chi/v5` | handler 就是标准 `http.Handler`；SSE 直接拿 `ResponseWriter` 写并 Flush |
| 存储 | `modernc.org/sqlite`（纯 Go，CGO-free）+ WAL | 交叉编译无忧 |
| 迁移 | 自写（`embed` SQL + `schema_migrations`） | 30 行够用 |
| 日志 | 标准库 `log/slog` | JSON 结构化，零依赖 |
| 限速 | `golang.org/x/time/rate` | 令牌桶 |
| 密码哈希 | `golang.org/x/crypto/argon2` | 管理面 |
| 加密 | `crypto/aes` GCM | 上游 secret 落库 |
| TLS | `golang.org/x/crypto/acme/autocert` 或反代终止 | 公网必备 |
| 前端 | Next.js + Tailwind + shadcn/ui，产物 `embed.FS` 打进二进制 | 开发期分离，发布期单文件 |
| 代理 | **必须设 `GOPROXY=https://goproxy.cn,direct`** | 本机实测 `proxy.golang.org` 不通 |

### 4.1 目录结构

```
FreeRouter/
├── cmd/freerouter/main.go        # 配置 → 装配 → 启 HTTP
├── internal/
│   ├── config/                   # YAML + 环境变量覆盖 + 校验
│   ├── ir/                       # 消息与流式事件类型
│   ├── protocol/{openaichat,openairesponses,anthropic,gemini}/
│   ├── route/                    # 可见性 + 覆盖 + 透传（薄）
│   ├── meter/                    # token / 延迟 / 打标
│   ├── store/                    # SQLite + 迁移 + DAO
│   ├── upstream/dshgateway/      # 8326 客户端（连接池 / 重试 / 契约测试）
│   ├── upstream/openaicompat/    # 扩展位
│   ├── httpapi/                  # 入站：鉴权、限速、配额、SSE
│   ├── admin/                    # 管理面 API + 管理端鉴权
│   └── security/                 # 密钥、限速器、审计、安全头
├── web/                          # Next.js 控制台
├── migrations/
├── deploy/                       # Dockerfile / compose / Caddyfile / systemd
└── docs/PLAN.md
```

---

## 5. 功能清单

### MVP

- [ ] 账号制：注册（`pending`）→ 管理员批准 / 拒绝 → 登录；管理员直接导入账密（单个 / 批量 CSV）
- [ ] 角色与权限组：`admin` / `user`，审核时分配模型权限组
- [ ] 会话：HttpOnly session、登录限速、登出、自助改密、管理员重置
- [ ] 入站 `POST /v1/chat/completions`（流式 + 非流式 + tool_calls + 图片）
- [ ] 入站 `POST /v1/messages`（Anthropic，含 `tool_use` / `thinking` / SSE 事件名对齐）
- [ ] 入站 `GET /v1/models`（按 Key 可见性过滤）
- [ ] 上游 `dshgateway`：key 自举、目录发现与缓存、连接池、重试、离线降级
- [ ] 模型管理：分组 / 置顶 / 改名 / 隐藏（116 个直铺是灾难）
- [ ] API Key：归属账号，可创建 / 禁用 / 过期 / 额度 / RPM·TPM / 模型白名单；数量与额度受账号上限约束
- [ ] 请求日志：端点、模型、实际上游、状态、TTFT、总耗时、token、错误码
- [ ] 控制台：公共页（登录 / 注册 / 待审核）+ 管理员八页（Overview / 账号审核 / Keys / Upstreams / Models / Logs / Playground / 设置）+ 用户四页（我的用量 / 我的 Key / 我的日志 / Playground）
- [ ] 安全基线（第 6 章全部，公网形态不可裁剪）
- [ ] 部署：Docker Compose + Caddy 模板 + systemd unit

### P1

- [ ] 入站 `POST /v1/responses`、`POST /v1beta/models/{m}:generateContent`
- [ ] 配额：日 / 月 token 上限、全局预算熔断、异常用量告警
- [ ] 上游健康：主动探活、失败冷却、半开恢复
- [ ] 管理面 TOTP
- [ ] 上下文压缩，可开关
- [ ] 账号体验：邮箱验证与找回密码（需 SMTP）、注册开关 / 邀请码、审核结果通知
- [ ] 账号配额与权限组的自助申请（用户提交，管理员批准）

### P2

- [ ] 多节点：SQLite → Postgres + Redis 共享限速与配额
- [ ] 语义缓存（按模型维度开关）

> **已从路线图移除**：智能应用、团队协作、IM 远程控制（v1 曾列在 P2）。

---

## 6. 公网安全设计

> 公网形态下这一章不是可选项，是 MVP 的一部分。

### 6.1 威胁模型（最要紧的一条）

**上游额度是你自己的账号。** 谁拿到 Key 谁就白嫖你的额度，账记在你头上。原则：
**默认拒绝、显式授权、额度护栏先行**。

1. **账号默认不激活**（注册即 `pending`），激活时必须挂权限组；新建 Key **默认额度 0、模型白名单为空**——一律显式授权。
2. 每个 Key 强制日 / 月 token 上限与并发上限，可设全局默认兜底。
3. 全局预算熔断：全站当日超标自动拒绝并告警。
4. 异常用量检测：单 Key 单位时间用量突增 → 告警（可选自动暂停）。

### 6.2 传输与边界

| 项 | 措施 |
|---|---|
| TLS | 强制 HTTPS（内置 autocert 或反代终止）。**不允许明文对外** |
| 端口 | 只暴露 443（+80 供 ACME）；管理面不单开端口 |
| 可信代理 | `TRUSTED_PROXIES` 显式配置。**不配则忽略 `X-Forwarded-For`**，否则 IP 限速被伪造头绕过 |
| 安全头 | HSTS、CSP、`X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options: DENY` |
| CORS | 默认关闭（同源）；跨域需显式白名单，**禁止 `*` + 凭据** |
| 请求体 | 入站默认 32 MiB → 超限 413，提前于上游的 300 MiB 拒绝 |

### 6.3 账号、注册与管理面

**初始化向导**：首次启动若库中无任何账号，只监听 `127.0.0.1` 并引导创建管理员账号
（无默认密码）；管理员创建完成前不开公网。

**注册**（`POST /register`）：
- 全局开关，默认**开启但需审核**；可选邀请码（P1）。
- 注册即 `pending`，**未审核不能登录**（登录返回"账号待管理员审核"）。
- 按 IP 限速；用户名 / 邮箱做**防枚举**：响应时间恒定，错误信息不区分"已存在 / 不存在"。
- 密码策略 ≥12 字符，argon2id 存储。

**审核**：管理端审核队列 → 批准（选权限组 + 配额，账号转 `active`）/ 拒绝（可填理由）。

**管理员导入账密**：单个添加，或批量粘贴 / CSV（每行 `用户名,密码[,权限组][,日额度]`）。
导入即 `active`，跳过审核；密码留空则生成随机密码，仅在结果里一次性展示。
导入动作（谁、何时、几个）全部进审计。

**会话**：HttpOnly + Secure + SameSite=Strict，登录时轮换 session id，默认 12 小时过期；
**改密或禁用会吊销该账号全部会话**。

**禁用联动**：账号转 `disabled` → 名下所有 API Key 立即 401（鉴权层实时查账号状态，
不依赖可能有延迟的缓存）。

**登录防爆破**：失败计数 + 指数退避 + 锁定，失败记录含 IP / UA 进审计。
可选 IP 允许列表（仅管理端）。P1 加 TOTP。

### 6.4 密钥

- Key 格式 `fr-` + 32 字节 base62；**只存 SHA-256**，明文仅创建时返回一次。
- 列表只显示 `fr-xxxx…xxxx` 前缀；支持即时吊销。
- 上游 secret 用 AES-GCM 落库，主密钥来自环境变量或 `0600` 文件，**永不入库**。
- 密钥比较用 `crypto/subtle.ConstantTimeCompare`。

### 6.5 限速与审计

- **三维令牌桶**：全局 / 按 Key / 按 IP。超限 `429` + `Retry-After`。
- 审计日志 append-only：管理操作、鉴权失败、额度熔断、上游错误。
- **默认不记录请求正文**；排查时开脱敏模式（保留结构、抹掉文本）并在 UI 明示。

### 6.6 运行态加固

- 容器非 root，`--read-only` 根文件系统 + 仅数据目录可写，`--cap-drop=ALL`。
- `/healthz`（进程）与 `/readyz`（含上游连通性）分离。
- `slog` JSON 输出 + 日志轮转。CI 跑 `govulncheck`。

---

## 7. 数据模型

SQLite（WAL），编号 SQL 迁移，启动时自动执行。

| 表 | 关键字段 | 说明 |
|---|---|---|
| `accounts` | id, username, email, password_hash, role, status, perm_group_id, quota_daily_tokens, quota_monthly_tokens, key_limit, created_at, approved_at, approved_by, last_login_at, note | status: `pending` / `active` / `disabled` / `rejected`；role: `admin` / `user`；**无上级归属** |
| `sessions` | id, account_id, token_hash, created_at, expires_at, ip, ua | 改密 / 禁用即吊销 |
| `perm_groups` | id, name, description, created_at | 权限组：一组模型的命名封装，审核时挂给账号 |
| `perm_group_models` | group_id, model_id | 组 ↔ 模型 |
| `api_keys` | id, account_id, key_hash, prefix, name, status, expires_at, quota_limit, quota_used, rpm_limit, tpm_limit, daily_token_cap, max_concurrency | **必须归属账号**；只存 hash |
| `upstreams` | id, kind, name, base_url, secret_id, enabled, last_seen_at | kind: `dshgateway` / `openaicompat` |
| `upstream_secrets` | id, upstream_id, ciphertext, nonce | AES-GCM，主密钥不入库 |
| `models` | id, upstream_id, upstream_model_id, display_name, group_name, context_window, modalities, efforts, enabled, hidden, sort_order, source | source: `auto` / `override` |
| `key_model_allow` | key_id, model_id | Key ↔ 模型可见性（多对多） |
| `requests` | id, ts, key_id, endpoint, upstream_id, upstream_model_id, status, http_status, error_code, ttft_ms, total_ms, in_tokens, out_tokens | **无 cost**；按天归档 |
| `usage_daily` | day, key_id, upstream_id, model, requests, in_tokens, out_tokens | 看板聚合 |
| `audit_log` | id, ts, actor, action, target, ip, ua, result, detail | append-only |
| `settings` | key, value | 全局配置 |

对比 v1：`model_aliases`、`routes`、`route_targets` 三张删除；
`tenants` 不复活——代之以扁平的 `accounts`，没有归属关系。

---

## 8. 与 dsh-providers-hub 的接线

### 8.1 启动自举

1. 读 `$DSH_HOME/openai-gateway/api-key` → AES-GCM 加密存库，标记 `auto`。
2. `GET /v1/models` 拉目录 → 写 `models`（`source=auto`；用户改过的记 `override`，刷新不覆盖）。
3. 端口不通**不阻塞启动**：控制台顶部挂「上游离线，检查 DSH 是否在跑」横幅，每 30s 重试。
4. `/healthz` 只反映进程，`/readyz` 反映上游连通性。

### 8.2 模型命名

- **裸名**：`deepseek-flash` —— 需在控制台配映射，指向某个上游模型
- **限定名**：`<upstream_id>:<upstream_model_id>`，如 `dsh:aggregate/deepseek-flash`

`/v1/models` 的 `owned_by` 填 `<upstream_id>`，`name` 为可读名 + 渠道后缀
（与 providers-hub 的 `· <渠道>` 习惯一致）。

### 8.3 116 个模型怎么摆

直铺等于把渠道构成全暴露给每个用户。可见性**两层收敛**：
**账号权限组（管理员分配）∩ Key 白名单（用户自己收窄）= 实际可用模型**

- 新账号默认**无任何模型权限**，审核时必须挂权限组（见 6.3）。
- 分组：`aggregate/*` 归「推荐」，其余按渠道前缀分组。
- 置顶：管理员标记常用项；隐藏：`hidden=1` 但保留（不删，便于恢复）。
- 对外的 `/v1/models` 只返回"该 Key 实际可用"的项。

### 8.4 打标与对账

不改 providers-hub 代码。用 `x-free-router-request-id` 头 + 本地 `requests` 表双向对账：
它记它的 `gateway` 通道，我们记 Key，按时间窗 + 模型 + token 数对齐。

### 8.5 契约测试（防漂移）

对 8326 的 4 个端点各写一组契约测试，锁住：状态码、鉴权失败形态、`/v1/models` 条目字段、
SSE 帧形状、413 行为。**外加一条**：连接偶发失败的复现（`http=000`）与重试是否成功。
providers-hub 升级后先跑契约测试，红了再适配。

---

## 9. 里程碑

| 阶段 | 内容 | 产出 | 估时 |
|---|---|---|---|
| M0 | 骨架：go.mod、chi 起服务、SQLite 迁移、配置、`/healthz` | `go run ./cmd/freerouter` 能起 | 0.5 天 |
| M1 | OpenAI Chat 入站 + dshgateway 上游（连接池 / 重试）+ 单 Key 鉴权 + SSE | Cherry Studio 跑通一轮对话 | 1 天 |
| M2 | Anthropic Messages 入站（含 tool_use / thinking） | Claude Code 接得进来 | 1 天 |
| M3 | **账号制**：初始化向导、注册→审核、管理员导入、登录会话、角色与权限组 | 能把账号发给人用 | 1.5 天 |
| M4 | API Key（归属账号）+ 三维限速 + 额度护栏 + 请求日志 | 用户可自助取 Key | 1 天 |
| M5 | 公网安全基线（TLS / 禁用联动 / 审计 / 安全头）+ 部署产物 | 可安全暴露公网 | 1 天 |
| M6 | 控制台：管理员八页 + 用户四页 | 可运维 | 2 天 |
| M7 | 健康探测 + 契约测试 + 压测 | 可长期跑 | 1 天 |

约 9 天。M1 结束即有实用价值；**M3 结束就能把账号发给别人用**。
**M5 不可跳过**——公网形态下没有安全基线等于把账号送出去。

---

## 10. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| **公网泄露 Key / 账号被盗 → 白嫖本人账号额度** | 额度损失 + 账号风险 | 账号默认待审核；Key 默认额度 0；日/月上限 + 并发上限；全局熔断；异常告警；一键禁用账号（联动吊销全部 Key 与会话） |
| 上游偶发连接失败（实测 `http=000`） | 请求随机失败 | 连接池 + 幂等重试 + 健康探测；失败计入 `requests` 便于观察频率 |
| providers-hub 升级接口漂移 | 上游断 | 只依赖 4 个 HTTP 端点 + 契约测试锁字段 |
| 上游拒绝通配 / 只绑回环 | 跨机不可用 | 唯一出口原则：上游永走 `127.0.0.1` |
| 300 MiB body / 120s keep-alive | 413、连接重置 | 入站设更小上限并给明确错误；自管连接池 |
| 各家思考档位 / 模态方言不一 | 静默降级或 400 | IR 保留「未知」第三态；缺能力明确报错 |
| SSE 分片乱序 / tool_call id 重组 | 工具调用错乱 | 按 `index` 缓存 id 重组 |
| 反代后 IP 被伪造 | 限速失效 | `TRUSTED_PROXIES` 未配则忽略 `X-Forwarded-For` |
| 管理面被盗 | 全站失守 | argon2id + session + 退避锁定 + IP 白名单 + (P1) TOTP |
| 单点进程 | 服务中断 | systemd `Restart=always` + 优雅关闭 drain；P2 多节点 |
| 116 模型对每个 Key 全暴露 | 渠道构成泄露 + 误调用 | 默认全隐藏，按 Key 白名单放行（8.3） |
| 借额度自用的合规边界 | 账号风控 | README 写明「仅限本人已登录账号、自用」；不内置任何绕过、代理池、指纹伪装 |
| 开放注册被刷号 | 垃圾账号、额度被薅 | 默认需审核（不自动激活）+ 注册限速；可选邀请码（P1） |
| 审核积压无人处理 | 用户等不到批准 | 管理端审核队列置顶 + 待办计数；可选通知（P1） |

---

## 11. 验收标准（MVP）

1. `curl` 用新 Key 打 `/v1/chat/completions`，`stream:true` 逐帧吐字，`[DONE]` 正常。
2. Claude Code 设 `ANTHROPIC_BASE_URL` 指过来 + 填 Key，完成一次带工具调用的编码任务。
3. 停掉 DSH（8326 关闭）：返回 `502 upstream_unavailable` 而非挂起，`/readyz` 变红，控制台显示离线。
4. Key 打满 RPM → `429` + 有值 `Retry-After`；打满日额度 → 拒绝且日志留痕。
5. **新建 Key 不改任何配置时无法发起请求**（默认拒绝）。
6. `/v1/models` 对某 Key 只返回其白名单内的模型。
7. 日志页可见：端点、模型、实际上游、TTFT、总耗时、token。
8. 公网无 HTTPS 时被拒；伪造 `X-Forwarded-For` 不能绕过 IP 限速。
9. `docker compose up -d` 后宿主 443 等价于第 1 条。
10. `govulncheck ./...` 无高危；容器非 root 运行。
11. **注册**：新账号 `pending` 时登录被拒且提示待审核；管理员批准并选权限组后可登录。
12. **导入**：粘贴 3 行 `用户名,密码` → 3 个账号立即可登录，审计里能看到这次导入。
13. **禁用联动**：账号置 `disabled` → 名下所有 Key **立刻** 401，不等缓存过期。
14. **权限组**：账号未获批模型组时 `/v1/models` 为空、调用返回 403。
15. **防枚举**：注册"已存在的用户名"与"全新用户名"，响应时间与文案无差异。

**反面验收（确认没做多）**：控制台没有"团队 / 共享端点 / 邀请成员"入口，
账号之间没有任何归属关系；没有"已接入客户端"引导；没有任何 IM Bot 配置项。

---

## 12. 开工前需你提供（不阻塞 M0–M3）

1. **域名**（ACME 签发，或确认走反代终止）
2. **部署位置**：本机直连公网 / 有公网 IP 的服务器 / 内网 + 隧道（Cloudflare Tunnel 等）
3. **反代方案**：Caddy（推荐，自动证书）/ nginx / Go 自己终止 TLS
4. **管理端是否限来源 IP**
