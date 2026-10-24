> 分册：API 契约 · 依赖：[01-inbound.md](01-inbound.md)、[02-admin.md](02-admin.md)、[03-errors.md](03-errors.md) · 对应总纲：PLAN.md 第 2、4 章

# 租户与贡献者 API

本分册覆盖**非管理面、非推理面**的两组用户接口：

| 面 | 前缀 | 谁用 | 鉴权 |
|---|---|---|---|
| 租户面 | `/api/account/*`、`/api/usage/*` | 已登录的普通账户（`tenant` 类） | 会话 Cookie `fr_session` |
| 贡献面 | `/api/contributions/*`、`/api/register` | 任何人（含未登录的匿名贡献者） | 会话 Cookie（可选） |

**为什么单独成册而不并入 02-admin**：管理 API 的每一条都是"管理员对别人的资源"做操作，危险等级默认高；本册的端点大多数是"用户对自己的东西"操作，鉴权与错误语义都不同（例如管理面的 404 是"不确认能力存在"，租户面的 404 是"这条不是你名下的"）。混在一起会让读者误判危险等级。

通用约定（响应信封、错误体、游标、`X-Request-ID`）与 [02-admin.md](02-admin.md) 完全一致，本册不重复。

---

## 1. 注册与审核状态

| 方法 | 路径 | 说明 | 鉴权 |
|---|---|---|---|
| `POST` | `/api/register` | 自助注册（→ `pending`） | 无 |
| `GET` | `/api/account/status` | 查询自己的审核状态 | 会话 |

### 1.1 `POST /api/register`

```json
{"username": "alice", "display_name": "Alice", "password": "……"}
```

成功返回 **201**：

```json
{"data": {"status": "pending"}}
```

**防枚举**：用户名已存在时**仍返回 201 + `pending`**，不报冲突。理由与登录失败不区分原因同源（[../security/02-authn.md](../security/02-authn.md) 第 3 节）——注册接口若能区分"用户名占用"，它就是一个用户名枚举器。

**新账户进 `pending`**，不具备调用能力（不进默认组、无 `tenant` 之外的能力）。审核流程见 [02-admin.md](02-admin.md) 第 5 节。

### 1.2 `GET /api/account/status`

```json
{"data": {"status": "pending", "classes": ["tenant"]}}
```

`status` 取值 `pending` / `active` / `disabled` / `rejected` / `merged`，语义见 [../security/01-accounts.md](../security/01-accounts.md) 第 5 节。

**为什么这个端点放在租户面而不是管理面**：`pending` 账户唯一能做的事就是"看自己过没过"，它连 `tenant` 能力都还没有。放在管理面会让前端在登录后必须先有管理员权限才能显示状态。

---

## 2. 账户自助

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/auth/session` | 当前会话（含 `must_change_password`） |
| `POST` | `/api/auth/login` | 登录（`{username, password}`） |
| `POST` | `/api/auth/logout` | 登出 |
| `POST` | `/api/account/password` | 改密（**首次登录强制**时唯一放行的写操作之一） |
| `PATCH` | `/api/account/profile` | 改显示名 |
| `GET` | `/api/account/sessions` | 我的会话列表 |
| `POST` | `/api/account/sessions/{id}/revoke` | 吊销某个会话 |

### 2.1 强制改密窗口

账户带 `must_change_password=true`（首次启动生成的管理员、管理员重置口令后、CSV 导入生成的账户）时，**除下列四个端点外的所有请求返回 403 `password_change_required`**：

- `GET /api/auth/session`
- `POST /api/auth/logout`
- `POST /api/account/password`
- `PATCH /api/account/profile`

这样设计的理由见 [../security/02-authn.md](../security/02-authn.md) 的「强制改密窗口」一节。

### 2.2 `POST /api/account/password`

```json
{"current_password": "旧口令", "new_password": "新口令", "display_name": "可选"}
```

改密成功会**吊销全部会话**并清除 Cookie（含当前这条），客户端须重新登录。`display_name` 可选：首次登录时一并补全账户信息，省一次往返。

响应是**裸对象**、不包 `data` 信封（控制台面的少量端点如此，与 `/api/admin/*` 的
`{data,audit_id}` 不同）：

```json
{"ok": true, "message": "口令已更新，请用新口令重新登录", "display_name_updated": true}
```

同理不带 `data` 的还有 `POST /api/account/sessions/{id}/revoke` 与
`POST /api/contributions/oauth/{provider}/cancel`（均为 `{"ok": true}`）。
前端用 `postPlain` 而不是 `postWrite` 声明这三个端点，否则类型会把 `data` 说成存在。

---

## 3. API Key 自助

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/account/keys` | 我的 Key 列表 |
| `GET` | `/api/account/keys/{id}` | 单个 Key 详情 |
| `POST` | `/api/account/keys` | 创建（**明文只返回一次**） |
| `PATCH` | `/api/account/keys/{id}` | 改名 / 改白名单 / 改有效期 |
| `POST` | `/api/account/keys/{id}/enable` | 启用 |
| `POST` | `/api/account/keys/{id}/disable` | 停用 |
| `POST` | `/api/account/keys/{id}/revoke` | 吊销（不可恢复） |
| `POST` | `/api/account/keys/{id}/rotate` | 换新 secret（旧 Key 立即失效） |

**`PATCH /api/account/keys/{id}` 的三值语义**（以 `expires_at` 为例）：字段**缺省** = 不变、
`null` = 清空、字符串 = 设定；空串 `""` 仍按清空处理（兼容只发空串的旧表单）。
**为什么要区分「缺省」与 `null`**：两者在普通的 `*string` 里都是 nil，于是
「想清空过期时间」与「这次不改过期时间」变成同一个请求——前端发 null 永远清不掉。
后端用一个显式三态包装（`Set` + `Value`）在解码层把两者分开；非法时间戳仍是
`400 invalid_request`（`param=expires_at`）。归属检查在写入**之前**
（拿别人的 Key id 不会改到任何一行，返回 404）。

**Key 明文只返回一次**（创建与 rotate 的响应里）。之后任何读取端点只给 `prefix`（如 `fr-J0Sl…06`），因为库中只存 SHA-256 哈希。理由与恢复码只展示一次相同，见 [../security/05-keys.md](../security/05-keys.md)。

**创建受组策略约束**：所属组的 `can_create_key=false` → 403 `forbidden`；达到 `max_keys` → 409 `conflict`。两者具体见 [../security/03-groups.md](../security/03-groups.md)。

---

## 4. 用量查询

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/usage/overview` | 额度与用量总览 |
| `GET` | `/api/usage/logs` | 请求日志（`limit`/`offset` 分页） |
| `GET` | `/api/usage/models` | 我有权使用的模型 |

**`/api/usage/logs` 的参数**：`limit`（≤200，默认 50）、`offset`、`from`/`to`、
`key_id`、`model`、`status`（`ok`/`failed`）、`error_code`（**精确匹配**，与 `status` 正交：
`status=failed&error_code=rate_limited` 是「失败且因限流」）。
**为什么只有 offset 而没有 cursor**：后端就只认 offset；前端曾发 cursor，
那条参数从来没人读——属于「发了但没人读」的静默失效，已在 2026-10-24 修掉。

**`/api/usage/models` 的响应没有 `capabilities`**：租户侧拿不到可靠的能力数据
（存储层没有这一列），旧实现发一个恒空对象，界面因此永远渲染空徽标。
不存在的字段不能靠「永远是空」来兼容——已删除，前端同步不再渲染。

**为什么租户只能看自己的**：这三条路径的账户过滤在 handler 里硬编码为会话账户，**不接受 `account_id` 参数**。管理面才有按账户筛选的能力（见 02-admin）。

---

## 5. 贡献面

贡献分**三条入口**（详见 [../contributions/01-flow.md](../contributions/01-flow.md)）：

| 入口 | 端点 | 适用 |
|---|---|---|
| 反代供应商 | `POST /api/contributions/oauth/{provider}` | 用供应商账号登录，由 sidecar 换出凭据（13 家） |
| API 供应商 | `POST /api/contributions`（`kind=api`） | 内置端点 + 粘贴 Key（目录见 5.1.2） |
| 自定义供应商 | `POST /api/contributions`（`kind=custom`） | 任意 OpenAI 兼容端点（自建网关、局域网服务） |

**为什么分成三类**：三者的「用户要做什么」完全不同——反代是登录一个账号，
API 供应商是去别处抄一个 Key，自定义是填一个别人没见过的地址。混在一页里，
用户既不知道该点哪个，也看不懂为什么有的供应商标着「不可用」。

### 5.1 端点总表

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/contributions/providers` | 三类入口清单（见 5.1.1） |
| `POST` | `/api/contributions` | 提交贡献（`kind=api|custom`，见 5.2） |
| `POST` | `/api/contributions/oauth/{provider}` | 发起 OAuth（`?phone=` 供短信形态） |
| `POST` | `/api/contributions/oauth/{provider}/poll` | 轮询授权进度 |
| `POST` | `/api/contributions/oauth/{provider}/cancel` | 取消授权会话 |
| `GET` | `/api/contributions/oauth/callback` | OAuth 回调（**302 到前端**，非 JSON 端点） |
| `POST` | `/api/contributions/credentials/{id}/pause` | 暂停我的一条贡献凭据（出池） |
| `POST` | `/api/contributions/credentials/{id}/resume` | 恢复我的一条贡献凭据（回池） |
| `POST` | `/api/contributions/recovery` | 用恢复码兑换会话 |
| `GET` | `/api/contributions` | 我的贡献汇总 |
| `GET` | `/api/contributions/credentials` | 我贡献的凭据列表 |
| `GET` | `/api/contributions/credentials/{id}` | 单个凭据详情 |
| `GET` | `/api/contributions/stats` | 贡献统计（`?range=day|month`，见 5.5） |
| `GET` | `/api/contributions/recovery-code` | 恢复码状态（是否已生成） |
| `POST` | `/api/contributions/recovery-code/regenerate` | 重新生成恢复码（旧码作废） |

#### 5.1.1 `GET /api/contributions/providers` 的三分类

响应把入口按类别分组，**不再是一条平铺清单**：

```json
{"data": {"categories": [
  {"id": "reverse_proxy", "name": "反代供应商", "description": "…",
   "items": [
     {"id": "trae", "name": "Trae", "flow": "browser", "needs": [], "available": true},
     {"id": "gemini", "name": "Google Gemini", "flow": "browser", "available": false,
      "unavailable_reason": "未配置 OAuth 客户端：请设 CMDC_PAK_GOOGLE_CLIENT_SECRET",
      "unavailable_kind": "config"}
   ]},
  {"id": "api", "name": "API 供应商", "description": "内置端点，只需粘贴 Key",
   "items": [
     {"id": "deepseek-com", "name": "Deepseek", "base_url": "https://api.deepseek.com/v1",
      "protocol": "openai", "vendor_family": "deepseek", "region": "global",
      "website": "https://deepseek.com", "api_doc": "https://api-docs.deepseek.com/",
      "models": [{"id": "deepseek-flash"}]}
   ]},
  {"id": "custom", "name": "自定义供应商", "description": "任意 OpenAI 兼容端点", "items": []}
]}}
```

- `reverse_proxy` 的 items 带可用性与交互形态。**不可用分两种性质**（`unavailable_kind`）：
  `config`＝本站缺配置（补上就能用）、`unsupported`＝上游没有可登录面（补配置也不会变）。
  前端必须给不同措辞，否则运维会去翻一个本来就不该有的 OAuth 配置。
- `api` 的 items 来自内置目录 `internal/apidir`（`protocol` 目前恒 `openai`），
  **不依赖 sidecar**，因此没有 `available` 字段。
- `custom` 恒为空数组——它的入口是表单而不是清单。

#### 5.1.2 内置 API 目录

目录是静态事实表（`internal/apidir/catalog.json`，2026-10-24 从 Tingly Box 的
provider catalog 提取的事实字段：端点与模型 id，**不含其代码**）。收录规则：

| 规则 | 原因 |
|---|---|
| 只收有 OpenAI 兼容端点的条目 | 本站出口链路是 OpenAI/Anthropic 协议；只有 anthropic 端点的家（如 `anthropic-com`）留给「自定义供应商」手填 |
| 排除 `oauth` / `gcp_sa` / `aws_sigv4` / `azure_key` | 它们不是「贴一个 Key」的形态 |
| 排除 `localhost` 自建（ollama / lm-studio / vLLM …） | 需要用户自己起服务，属于「自定义供应商」 |
| 排除带 `{account_id}` 之类占位符的模板端点 | 内置 URL 不完整，贴进去必失败 |
| **保留** ModelScope / OpenCode 的 key 型条目 | 2026-10-25 用户澄清：这两家移除的只是**反代（OAuth）形态**（上游没有可登录面）；它们的 OpenAI 兼容端点（`modelscope-cn` / `opencode-ai` / `opencode-go`）贴 Key 即用，正是本目录服务的交互 |

目录条目的 `models` 只是**建议清单**（供界面点选），不是白名单——用户仍可手填。

### 5.2 `POST /api/contributions`（api / custom 两类）

请求体的 `kind` 决定「端点从哪来」：

```json
{"kind": "api", "provider": "deepseek-com", "api_key": "sk-……",
 "label": "我的备用 Key", "claimed_models": ["deepseek-flash", "deepseek-reasoner"]}

{"kind": "custom", "endpoint": "https://api.example.com/v1", "api_key": "sk-……",
 "label": "自建网关", "claimed_models": ["some-model-id"]}
```

- `kind` 缺省为 `custom`（旧客户端不带该字段仍可用）。
- `kind=api` 时 **不要传 endpoint**：端点由服务端从内置目录取（传了也忽略）。
  目录里没有这个 `provider` → `400 invalid_request`（message 写明未知 id）——
  不允许客户端自报一个任意端点再冒名某个供应商。
- `kind=custom` 时 `endpoint` 必填，校验同上游 `ValidateTarget`（回环/内网地址归
  `selfhosted`，其余归 `vendor`）。

**`claimed_models` 可多选**（2026-10-25 起；此前只取第一个）：一把 key 往往同时提供
好几个模型，只让声明一个会迫使贡献者隐去其余事实。提交后的 L1 目录比对按条判定：

| 声称 vs 目录 | status | verify_status |
|---|---|---|
| 全部命中 | active | `verified` |
| 部分命中（≥1 且 <N） | active | `unverified`（入池 + 复检队列，证据列命中/未命中明细） |
| 目录非空但全不命中 | disabled | `suspect`（冒充的最强信号） |
| 端点无目录 | active | `unverified`（放行 + 复检） |
| 未声明任何模型 | active | `unverified`（不判定「相符」） |

**为什么「部分命中」不盖章也不误杀**：命中一半说明 key 是真的、但声称清单掺了水——
给 `verified` 等于替贡献者背书全部声称；给 `suspect` 又过了（「三个里中了一个」恰恰
说明不是冒充）。`unverified` 是两个误判代价里较小的一个。

落库的 `upstreams.provider`：反代型为 sidecar 的 provider 名，api 型为
`api:<catalog id>`，自定义为 `custom`。

**提交即探测**：handler 在落库前后跑 L0/L1 判定（端点可达性、鉴权、目录比对），失败也保留记录（状态落 `pending/unverified`）。判定流水线见 [../models/05-verification.md](../models/05-verification.md)。

**贡献者无偿**：不折算额度、不给优先使用权、不给升级凭据。这条是定案，见 [../contributions/02-credentials.md](../contributions/02-credentials.md) 第 7 节。

### 5.3 OAuth 三件套

#### 发起：`POST /api/contributions/oauth/{provider}`

可选查询参数 `phone`（仅 `sms` 形态需要，如 Loomy）。

响应 `data` 形状随 **交互形态** 变化：

```json
{"data": {
  "mode": "redirect",              // redirect | session
  "authorize_url": "https://...",  // redirect 形态：整页跳转
  "session_id": "……",              // session 形态：后续 poll 用
  "flow": "device_code",           // browser | device_code | sms | qr
  "user_code": "NPUZ-BN9H",        // device_code 形态
  "verification_uri": "https://...",
  "qr_image_url": "https://...",   // qr 形态
  "interval": 3,                   // poll 建议间隔（秒）
  "message": "验证码已发送，请回填"
}}
```

**为什么有两种 mode**：`redirect` 是让浏览器整页跳去供应商、回调落在 `/api/contributions/oauth/callback`；`session` 是上游把结果投递到它自己的端点，我们只能轮询（例如华为云 officeace、bigmodel zcode）。前端按 `mode` 分叉渲染，见 [../ui/05-contributor.md](../ui/05-contributor.md)。

#### 轮询：`POST /api/contributions/oauth/{provider}/poll`

```json
{"session_id": "……", "code": "123456"}
```

`code` 只在 `sms` 形态回填验证码时给。

```json
{"data": {
  "status": "pending",          // pending | ok | failed | expired
  "message": "……",
  "outcome": {"…": "ok 时的凭据与恢复码"},
  "interval": 3
}}
```

**为什么要有独立的 poll 端点**：上游的 `pollXxx` 是**阻塞 while 循环**（等到用户授权完成才返回），而 sidecar 的一问一答通道有 10 秒超时。直接把它挂在 `authorize` 上会把通道卡死，所以轮询在 sidecar 侧跑成后台会话，HTTP 侧只查状态。会话 TTL 默认 600 秒（`FR_OAUTH_SESSION_TTL_SEC` 可调）。

#### 取消：`POST /api/contributions/oauth/{provider}/cancel`

请求体同上（只需 `session_id`）。取消后该会话状态变 `expired`，再 poll 不会复活。

#### 回调：`GET /api/contributions/oauth/callback`

**这不是 JSON 端点**：无论成败都 **302** 到前端 `/#/contribute/callback?accepted=0|1&message=…`。失败原因的原文（上游返回什么就带什么）放在 `message` 里，便于排障。

### 5.4 恢复码

匿名贡献者拿不到账号身份，**恢复码是唯一锚点**（格式 `frv-<accountID>-<secret>`，只展示一次，库中只存 argon2id 哈希）。

| 端点 | 说明 |
|---|---|
| `POST /api/contributions/recovery` | `{"code": "frv-…"}` → 建立会话 |
| `GET /api/contributions/recovery-code` | 是否已生成（**不返回明文**） |
| `POST /api/contributions/recovery-code/regenerate` | 生成新码并作废旧码，明文只返回一次 |

**为什么 OAuth 型的贡献者也可能需要恢复码**：OAuth 型理论上能靠"用同一上游账号再登录一次"找回身份，但那依赖 `LoginByUpstreamOAuth` 通道——**它尚未实现**。所以当前实现给所有匿名贡献者都发恢复码；等该通道落地后可以停发（保留字段与兑换入口，避免历史账户失联）。这处偏差在 [../contributions/01-flow.md](../contributions/01-flow.md) 第 3.1 节有记录。

### 5.5 暂停 / 恢复与统计窗口

**暂停**（`POST /api/contributions/credentials/{id}/pause`）把该凭据从候选池摘下：
底层是把对应上游行 `enabled=false`——与「凭据被禁用」走同一条路径，
不做第二套状态机，否则「凭据状态」会在三处各有一套语义。
**恢复**（`.../resume`）置回 `enabled=true`。两者都幂等、都要求属主
（非属主 404 `not_found`，与详情端点同一口径）。

**统计窗口**：`GET /api/contributions/stats?range=day|month`：

| 取值 | 起点 |
|---|---|
| `day`（缺省） | 今天 0 点（UTC 日界，与 `usage_daily.day` 同一套时间口径） |
| `month` | 本月 1 日 0 点（UTC） |
| 其它 | `400 invalid_request`，`param=range` |

**为什么用 UTC 日界而不是本地时区**：用量表的主键是 UTC 日期字符串，
统计若按本地时区切，会与「按天聚合」的既有数据错位一格。

---

## 6. 待定项

> **待定**：`GET /api/usage/logs` 的保留期与分页上界（与管理面审计保留期同一批问题）。

> **待定**：恢复码是否应该支持"多码并存"（当前重生成即作废旧码）。
