# 变更日志

本文件格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。
项目尚未发布，版本号自 `v0.0.x` 起；**里程碑**（M0–M10，见 docs/ops/03-milestones.md）
作为开发期的分组单位。

约定：凡属「运维会感知为行为变化」的改动（默认值语义、错误体形状、存储迁移），
必须记入本文件的 Unreleased → 变更 一节，注明旧行为、新行为与原因。

---

## [Unreleased]

### 新增

- **租户面/注册/贡献者面端点落地**（2026-10-23，界面此前按契约先行）：
  - `GET /api/usage/overview`（用量总览：组、Key 统计、额度维度与 reset_at、按模型限额）、
    `GET /api/usage/logs`（调用日志筛选分页）、`GET /api/usage/models`（组授权 ∩ Key 白名单）、
    `GET /api/account/sessions` + `POST /api/account/sessions/{id}/revoke`（我的会话）；
  - Key 面补齐：`GET/PATCH /{id}`、`enable/disable`、创建支持 `expires_at` 与 `allowlist`，
    响应形状对齐前端 `ApiKey/ApiKeyCreated`（前端路径改为 `/api/account/keys`）；
  - 修复 Key 白名单链路：auth 层语言是模型名，桥此前把名字当数字 id 解析导致
    白名单恒为空（设了等于没设）；现桥做名字↔id 显式翻译；
  - `POST /api/register`（防枚举：占用与成功同形 201 pending）与
    `GET /api/account/status`；登录/会话响应新增 `status` 字段（pending 让路生效）；
  - 贡献者面：`providers`、`POST /api/contributions`（未登录自动建 contributor 账户 +
    一次性恢复码 `frv-<id>-<secret>`）、`recovery` 兑换、我的凭据/详情/统计、
    `recovery-code` 状态与 regenerate（每次生成覆盖旧码）；
  - pending/contributor 账户不进默认组（组成员要求 tenant 类，03-groups §9）；
    组的落位发生在审批通过时。
- **供应商 OAuth 贡献（CodeArts）落地**（2026-10-23）：按 PLAN §5.5「登录与凭据
  续期必须复用 providers-hub 实现」的定案，从 `deepseek-harness-codearts`
  （Apache-2.0，gitee.com/iJetLi/deepseek-harness-codearts）提取 OAuth 组件移植进
  `sidecar/providers/codearts/`（PKCE + DPoP + 华为 STS 换取 + SDK-HMAC-SHA256 签名，
  **不引入 jose**，用 node:crypto 实现同一套 ES256/DPoP）。
  - sidecar 新方法：`oauth.authorize` / `oauth.exchange` / `oauth.refresh`；
    换取失败给 `E_OAUTH_EXCHANGE_FAILED`（原文上抛，界面能说清"授权码过期"这类原因）。
  - 主进程：`POST /api/contributions/oauth/{provider}`（公共区，返回 authorize_url）与
    `GET /oauth/callback`（供应商按「端口 + 固定路径」构造，必须挂根路径；成功后
    302 到 `/#/contribute/callback?accepted=…`）。PKCE verifier 与 DPoP 私钥放在
    进程内一次性表（10 分钟 TTL），不进 Cookie、不落库。
  - 凭据落库走既有链路（上游行 enabled=false + AES-GCM 密文 + 凭据行），
    OAuth 路径先停在 pending/unverified（未探测前不进候选池，01-flow 5.2）；
    未登录贡献自动建 contributor 账户并发放一次性恢复码。
  - `/api/contributions/providers` 改为真实列表（已移植供应商 + API Key 型），
    不可用时带 `unavailable_reason`；未移植的 provider 发起授权返回 404。

- M0 骨架：配置（YAML + `FR_` 环境变量覆盖）、L1 中间件链（请求 ID / 安全头 / 可信代理 / body 上限）、
  `/healthz` `/readyz` `/api/version`、优雅关闭；`make contract` 契约测试门禁。
- sidecar 契约层：Go 客户端（stdio+NDJSON 一问一答、超时杀进程、崩溃降级不影响转发）
  与 Node sidecar；红线 `credentials.set/unset/refresh` 不存在且契约测试可执行断言。
- 存储层：SQLite(WAL) 迁移机制、主密钥 0600 校验、八类仓储、账户额度/组池的
  条件 UPDATE 扣减与惰性重置（`ErrQuotaExhausted` 携带打满行标识与 `ResetAt()`）。
- 前端骨架：Vite+React 表驱动路由 + `go:embed` 双形态（未构建时裸 `go build` 通过，
  发布二进制带 `-tags webui_dist`）。

### 变更（运维需感知）

- **首次启动改为进程自动生成随机口令并创建管理员**（2026-10-23 定案）：
  旧行为是浏览器打开 `/setup` 由用户填显示名与口令；新行为是启动时生成 20 字符
  随机口令、创建 `admin`（`{admin, tenant}`），把口令打印在终端 banner 并写入
  数据目录 `initial-password.txt`（0600），默认（且仅回环监听时）自动打开控制台。
  口令是**一次性**的：登录后除 `/api/auth/session`、`/api/auth/logout`、
  `/api/account/password`、`/api/account/profile` 外的控制台接口一律
  `403 password_change_required`，改密成功后口令文件自动删除并吊销全部会话。
  管理员重置他人口令同样会进入强制改密窗口。`/setup` 向导保留作为兜底
  （例如账户表被手工清空）。
  新增配置 `server.auto_open_browser`（默认 `true`）；迁移 `0006` 新增
  `accounts.must_change_password`（`NOT NULL DEFAULT 0`，存量账户不受影响）。
- 控制台静态页与 SPA 资源不再经过 Key 鉴权链（旧行为下浏览器打开 `/` 直接
  `401 invalid_api_key`，前端根本加载不了）；`/v1/*` 与 `/api/*` 不变。
- **供应商 OAuth 全量移植（15 家）**（2026-10-23）：按上游
  `deepseek-harness-codearts` 的 `tsc` 产物做依赖闭包整体提取（83 个文件），
  不手抄——这些文件里全是逆向出的常量与请求形状，手抄会引入静默偏差。
  - 已接入 13 家：codearts / gemini / accio / minimax / cline / qoder / loomy /
    raccoon / lobsterai / trae / autoclaw / officeace / zcode；
  - modelscope / astudio / opencode 走「API Key 型贡献」并给出具体原因
    （无登录面 / 需要本机 HTTP 服务 / 登录在 adapter 层）；
  - sidecar 把 15 家的差异收敛成三种交互形态（redirect / device_code / sms·qr），
    新接口 `oauth.list|authorize|exchange|poll|cancel|refresh`；
  - **轮询放后台**：上游 `pollXxx` 是阻塞 while 循环，直接占用 10 秒超时的 RPC
    通道会卡死，所以改为 `sessions.mjs` 的后台会话 + `oauth.poll` 查状态；
  - 主进程新增 `POST /api/contributions/oauth/{provider}/poll` 与 `/cancel`；
    前端按 `flow` 渲染三种形态（跳转 / 设备码+轮询 / 二维码或短信回填）。
  - **不内置第三方凭据**：上游产物里硬编码了 Gemini 的 OAuth client secret 与
    Loomy 的讯飞 AccessKey，那是别人应用的凭据，内置等于把配额与风控算到
    对方头上（也会被密钥扫描拦下）。现改为环境变量提供
    （`CMDC_PAK_GOOGLE_CLIENT_SECRET`、`LOOMY_ACCESS_KEY_ID`/`LOOMY_ACCESS_KEY_SECRET`）；
    未配置时该 provider 在清单里**列出但标不可用并给出配置方法**，
    直接发起返回 `E_NOT_CONFIGURED`——而不是发一个必然失败的请求。
- **`limits.default_tpm` 默认值 `0 → -1`，且「不限」的表示从 `0` 迁移到 `-1`**
  （2026-10-20 定案，与 `accounts` 速率列同一套哨兵：`-1` = 不限、`0` = 全拒）。
  升级后若你的旧配置写了 `default_tpm: 0`，行为会从「不限」变为「全拒」——
  请显式改写为 `-1`。这不是 bug 修复而是语义定案（旧「0=不限制」已废除）。
- 额度域全面行化（2026-10-20 两轮定案）：账户周期额度从 `accounts` 三列迁移到
  `account_quotas` 行（合计行 + 按模型限额行，「不限」= 无行）；`api_keys` 不再
  持有额度/速率列；`usage_daily` 从按 Key 聚合改为按账户聚合；组池与按模型上限
  新增 `credit` 维度与 `abstract_model_id` 维。首次启动将按新迁移建表。
- `quota_exceeded` 错误体新增机器可读 `quota{unit, period, model?}` 对象，
  `Retry-After` 指向命中行的重置时刻（锚点脏数据时宁缺勿错）。
- **全局并发闸排队超时改用 503 `overloaded`**（2026-10-21 定案）：旧行为归入
  429 `rate_limited`，与账户/Key 限速同码；新行为拆开——`rate_limited`（429）
  归因“你超了上限”，`overloaded`（503 + `Retry-After: <queue_timeout>`）归因
  “服务容量饱和，与你是谁无关”，避免按码运营的告警被容量问题污染。
  `ErrGloballyFused`（全局预算熔断）仍为 429 `quota_exceeded`（与分册 §3.1 一致）。
  `Retry-After` 指向命中行的重置时刻（锚点脏数据时宁缺勿错）。

### 修复

- **登录后页面不断闪烁、最后空白**（2026-10-23 定案）：根因是 Key 鉴权链
  （全局 `r.Use`）把前端尚未接入的 `/api/usage/overview` 等请求拦成
  `401 invalid_api_key`，而 client 把任何 401 当作「会话失效」跳登录页；
  登录页当时的「已有 session 就回跳」逻辑又把它送回去，形成首页 ↔ 登录页
  无限跳转。修复：① Key 鉴权只作用于 `/v1/*`，`/api/*` 未知端点返回
  `404 not_found`（前端显示「尚未接入」而不是踢人）；② 401 时同步清空内存
  会话，登录页不再因旧 session 回跳；③ 登录页拒绝 `from=/login` 的脏回跳
  目标；④ `index.html` 加 `Cache-Control: no-store`，防止旧产物清单引用
  已删除的 chunk 导致白屏。

### 废弃

- 类级提权（step-up）已于设计层废除（2026-10-20）：登录一次即账户全部权限，
  会话 24h 为唯一时效边界。
- `RelayTransport.Open` 与 `OpenFromPayload` 已删除（2026-10-22）：无生产调用方，
  且拆完信封拿到的是 OpenAI SSE 而 `relayPayloadToNDJSON` 期望 relay 帧——
  留着只会让下一个人得到「0 事件 + 协议错」。IR 原生流式面需要时走
  `Caller.InferStream` + 协议 `NewStreamReader`。

### 验证结论（M10 实跑，2026-10-22）

- 安全基线 C1–C12 全 PASS（脚本 `scripts/security-checklist.sh`）；S2（伪造 XFF）
  与 S8（429 Retry-After 压测）留 CI，S11 已本机跑 `govulncheck`：**0 可调用漏洞**。
- 在线 `VACUUM INTO` 备份 + dry-run 三重断言 + 删库恢复演练通过；`--verify-store`
  自检可用（库不存在时拒绝，避免把空库误判为可用）。
- 空库 → `POST /api/setup/init` → 登录 → 会话恢复 全流程通过。
- 轻量压测：16 并发 × 400 请求，**3851 rps / p95 7ms / p99 8.4ms**，RSS +4.4MB
  （17.8MB → 22.2MB）。
- TLS `files` 模式实跑：HTTPS 200、HSTS `max-age=31536000`、明文请求被拒（400）。
