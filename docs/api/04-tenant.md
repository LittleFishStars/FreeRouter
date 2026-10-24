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

**Key 明文只返回一次**（创建与 rotate 的响应里）。之后任何读取端点只给 `prefix`（如 `fr-J0Sl…06`），因为库中只存 SHA-256 哈希。理由与恢复码只展示一次相同，见 [../security/05-keys.md](../security/05-keys.md)。

**创建受组策略约束**：所属组的 `can_create_key=false` → 403 `forbidden`；达到 `max_keys` → 409 `conflict`。两者具体见 [../security/03-groups.md](../security/03-groups.md)。

---

## 4. 用量查询

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/usage/overview` | 额度与用量总览 |
| `GET` | `/api/usage/logs` | 请求日志（游标分页） |
| `GET` | `/api/usage/models` | 我有权使用的模型 |

**为什么租户只能看自己的**：这三条路径的账户过滤在 handler 里硬编码为会话账户，**不接受 `account_id` 参数**。管理面才有按账户筛选的能力（见 02-admin）。

---

## 5. 贡献面

贡献分两条路径（详见 [../contributions/01-flow.md](../contributions/01-flow.md)）：

| 形态 | 入口 | 适用 |
|---|---|---|
| API Key 型 | `POST /api/contributions` | 任何 OpenAI 兼容端点 |
| 供应商 OAuth 型 | `POST /api/contributions/oauth/{provider}` | 16 家（13 家 OAuth + 3 家 API Key 型） |

### 5.1 端点总表

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/contributions/providers` | 供应商清单（含可用性与交互形态） |
| `POST` | `/api/contributions` | 提交 API Key 型贡献 |
| `POST` | `/api/contributions/oauth/{provider}` | 发起 OAuth（`?phone=` 供短信形态） |
| `POST` | `/api/contributions/oauth/{provider}/poll` | 轮询授权进度 |
| `POST` | `/api/contributions/oauth/{provider}/cancel` | 取消授权会话 |
| `GET` | `/api/contributions/oauth/callback` | OAuth 回调（**302 到前端**，非 JSON 端点） |
| `POST` | `/api/contributions/recovery` | 用恢复码兑换会话 |
| `GET` | `/api/contributions` | 我的贡献汇总 |
| `GET` | `/api/contributions/credentials` | 我贡献的凭据列表 |
| `GET` | `/api/contributions/credentials/{id}` | 单个凭据详情 |
| `GET` | `/api/contributions/stats` | 贡献统计 |
| `GET` | `/api/contributions/recovery-code` | 恢复码状态（是否已生成） |
| `POST` | `/api/contributions/recovery-code/regenerate` | 重新生成恢复码（旧码作废） |

### 5.2 `POST /api/contributions`（API Key 型）

```json
{
  "endpoint": "https://api.example.com/v1",
  "api_key": "sk-……",
  "label": "我的备用 Key",
  "claimed_models": ["some-model-id"]
}
```

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

---

## 6. 待定项

> **待定**：`GET /api/usage/logs` 的保留期与分页上界（与管理面审计保留期同一批问题）。

> **待定**：恢复码是否应该支持"多码并存"（当前重生成即作废旧码）。
