> 分册：架构 · 依赖：[01-inbound.md](01-inbound.md)、[03-errors.md](03-errors.md)、[../ui/03-admin.md](../ui/03-admin.md) · 对应总纲：PLAN.md 第 2、3、4、8 章

# 管理 API

## 1. 管理 API 的定位
管理 API 是控制台（[../ui/03-admin.md](../ui/03-admin.md)）的数据面， 不对外公开、不签发长期令牌、不提供跨账户的服务间调用。
三条定位：
| 定位 | 说明 |
|---|---|
| 单一消费方 | 控制台前端。**不承诺第三方兼容性** |
| 会话承载 | 认证走 HttpOnly 会话 Cookie，不签 API Token |
| 危险写敏感 | 每个写端点声明它是否属危险写（需要界面二次确认），接口本身不做第二次身份验证 |

**为什么管理 API 不签发 API Token**（2026-10-20 改定理由）：旧理由是"长期 Token 会绕过有效期很短的二次验证窗口"，那个窗口已经取消。现在保留"不签发 Token"是因为另两条：① 管理 API 的唯一消费方是同源控制台，它用 Cookie 已经够，签发 Token 只是多一条必须被保护、被轮换、被吊销的凭据；② 一个能改全站策略的长期凭据一旦泄漏就等于全站失守，而它的存在本身没有任何需求驱动。**注意这条不是安全机制的全部**：管理面的抵抗力落在登录本身（argon2id + 退避锁定 + 会话 24h 过期）。

**为什么不承诺第三方兼容**：一旦承诺，抽象模型与候选集的内部结构 就会变成对外契约，后续的模型体系演进会被冻结。

## 2. 认证与会话

### 2.1 会话

| 项 | 规则 | 来源 |
|---|---|---|
| Cookie 名 | `fr_session` | —— |
| 属性 | `HttpOnly`、`Secure`、`SameSite=Lax` | 总纲 9.2 |
| 时效 | 默认 **24 小时绝对过期**（`session_ttl` 可配），不做滑动续期 | [../PLAN.md](../PLAN.md) 第 2.5 节 |
| 存储 | 服务端只存 `token_hash` | `sessions.token_hash` |

**为什么 `SameSite=Lax` 而不是 `Strict`**：OAuth 贡献回调与 OAuth 登录 都是从外部站点跳回本站的。`Strict` 会让这些回调落地时**不带 Cookie**， 用户看到的是"已登录状态下被要求重新登录"的循环。

### 2.2 权限检查

管理端点只过一层：

| 层 | 检查 | 失败 |
|---|---|---|
| 唯一一层 | 会话所属账户的 `classes` 含 `admin` | **404** + `not_found` |

**为什么"没有 admin 类"返回 404 而不是 403**：403 等于确认"这个路径存在，只是你没权限"。管理面的路由结构本身就是信息（存在哪些页面、id 是什么形态），对无 `admin` 类的账户统一返回 404，不确认资源是否存在。**为什么只有这一层**：一次登录即该账户全部权限类的全部权限，不存在"已登录但还差一步"的中间态，因此管理面没有第二道判定的位置。这条与 [../ui/01-ia.md](../ui/01-ia.md) 第 7 节一致。

### 2.3 危险写与确认（不再是服务端环节）

| 端点类别 | 需要 `admin` 类 | 是否需要额外请求头 | 界面是否二次确认 |
|---|---|---|---|
| 只读查询 | ✅ | ➖ | ➖ |
| 普通写（改名、建组、启停凭据） | ✅ | ➖ | ➖ |
| 危险写（删/禁账户、改组、改抽象模型、改弃用、封禁凭据、人工改判、危险设置） | ✅ | ➖ | ✅（`🔒`） |

**为什么危险写在 API 层没有任何特殊标记**：服务端对每个写请求做的是同一套检查（会话 → 账户状态 → `classes` → 资源）。二次确认是**界面行为**，拦的是误点，不是身份；把它做成"某些端点需要额外请求头"只会让绕过 UI 直接调 API 的人不受影响、而正常用户多一步。**为什么还要在本文档里列出"哪些算危险写"**：它是 [../ui/03-admin.md](../ui/03-admin.md) 1.3 确认框与路由 `🔒` 图例的共同判据，属于契约的一部分（定了就不好改）。

**危险写清单**：

| 项 | 包含的动作 | 端点 |
|---|---|---|
| 账户面 | 禁用、合并（含回滚）、增删权限类、重置密码 | `PATCH /accounts/{id}`、`POST /accounts/{id}/merge`、`POST /accounts/{id}/merge/rollback`、`POST /accounts/{id}/password` |
| 组面 | 改模型授权、改策略、增/移成员、设默认组、删组、重置池已用 | `PUT /groups/{id}/models`、`PUT /groups/{id}/policies`、成员增删、`PATCH /groups/{id}`、`DELETE /groups/{id}`、`POST /groups/{id}/pool/reset` |
| 抽象模型面 | 改显示名/能力模式/能力声明/排序链、替换候选集、设 `disabled`、保存弃用配置 | `PATCH /abstract-models/{id}`、`PUT /abstract-models/{id}/candidates`、`PUT /abstract-models/{id}/deprecation` |
| 上游与凭据 | 禁用上游、封禁凭据 | `PATCH /upstreams/{id}`、`POST /credentials/{id}/ban` |
| 鉴定 | 人工改判 | `POST /verifications/{id}/override` |
| 设置 | 关闭注册、全局熔断阈值、`TRUSTED_PROXIES`、开启正文记录、登录退避与锁定阈值 | `PATCH /settings` 的对应字段 |

**为什么不给管理 API 加「批量危险操作」端点**：批量会让一次误点放大成 N 个不可逆动作。界面上的批量批准（[../ui/03-admin.md](../ui/03-admin.md) 第 3 节）只作用于可逆的、且逐条审计的准入动作，不在此列。
## 3. 通用约定

### 3.1 请求与响应

| 项 | 约定 |
|---|---|
| 基础路径 | `/api/admin` |
| 方法 | `GET` 查询、`POST` 创建、`PATCH` 局部更新、`DELETE` 仅用于可逆的移除（如移出组） |
| 更新语义 | `PATCH` 是**局部**更新；未提供的字段保持原值 |
| 请求体 | JSON；`Content-Type: application/json` |
| 响应体 | JSON；错误体见 [03-errors.md](03-errors.md) 第 4 节 |
| 列表分页 | `?limit=&cursor=`，返回 `{"data":[...],"next_cursor":null}` |
| 列表筛选 | 显式查询参数，不做通用的过滤器表达式语言 |

**为什么不做过滤器表达式语言**：通用查询语言会让管理 API 变成一个有无限表达力的数据面，其权限边界难以审计。显式筛选参数 让每个端点的可查字段一目了然。

**为什么 `DELETE` 只用于可逆移除**：总纲 1.3 与 [../ui/03-admin.md](../ui/03-admin.md) 1.1 都定了"不可逆一律状态迁移"。 把 `DELETE` 限定在"移出组""移出白名单"这类可逆关系上， 可以让"看到 DELETE 就知道它是可逆的"成为一个可用直觉。

### 3.2 写端点的共同返回

```json
{
  "data": {"id": "grp_01J8..."},
  "audit_id": "aud_01J8..."
}
```

**为什么每个写响应都带 `audit_id`**：总纲 9.5 的审计是 append-only 的， 但它必须能被界面回指。带 `audit_id` 让"我刚做的这次修改"在审计页 是可定位的，从根上避免"改完不知道有没有生效"的疑问。

**为什么不再带"当前验证状态"这类字段**：类级二次验证取消后，一次登录就是一次完整的身份确立，没有"在哪种验证状态下完成"这回事。留着会让前端与审计都保留一个恒为真的布尔值。

## 4. 资源分组总览

| 分组 | 基础路径 | 对应界面 |
|---|---|---|
| 账户 | `/api/admin/accounts` | [../ui/03-admin.md](../ui/03-admin.md) 第 4 节 |
| 组 | `/api/admin/groups` | 同第 5 节 |
| 真实模型 | `/api/admin/real-models` | 同第 6 节 |
| 抽象模型 | `/api/admin/abstract-models` | 同第 7 节 |
| 上游 | `/api/admin/upstreams` | 同第 8.1 节 |
| 凭据 | `/api/admin/credentials` | 同第 8.2 节 |
| 鉴定 | `/api/admin/verifications` | 同第 9 节 |
| 审计 | `/api/admin/audit` | 同第 10 节 |
| 设置 | `/api/admin/settings` | 同第 11 节 |
| 待处理事项 | `/api/admin/inbox` | [../ui/03-admin.md](../ui/03-admin.md) 第 12 节 |
| 初始化 | `/api/setup` | 同第 2 节（**不在 `/api/admin` 下**） |

**为什么初始化接口不在 `/api/admin` 下**：它在"库中还没有任何账户"时 就必须可用，而 `/api/admin` 的检查（账户的 `classes` 含 `admin`） 在那个时刻没有任何账户可以满足。放进 `/api/admin` 会导致悖论： 没有账户就不能创建第一个账户。

## 5. 账户组

### 5.1 端点

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/accounts` | 列表（筛选 `class` / `status` / `group_id`） | ➖ |
| `GET` | `/api/admin/accounts/{id}` | 详情 | ➖ |
| `POST` | `/api/admin/accounts` | 单个添加（同 CSV 导入的字段） | ➖ |
| `POST` | `/api/admin/accounts/import` | CSV 导入 | ➖ |
| `PATCH` | `/api/admin/accounts/{id}` | 改权限类、状态、所属组、备注、显示名 | 🔒（账户面） |
| `POST` | `/api/admin/accounts/{id}/password` | 重置密码 | 🔒（二次确认） |
| `POST` | `/api/admin/accounts/{id}/sessions/revoke` | 吊销全部会话 | ➖ |
| `POST` | `/api/admin/accounts/{id}/merge` | 合并到目标账户 | 🔒（账户面） |
| `POST` | `/api/admin/accounts/{id}/merge/rollback` | `reversible_until` 内回滚 | 🔒（账户面） |
| `GET` | `/api/admin/accounts/pending` | 审核队列 | ➖ |
| `POST` | `/api/admin/accounts/pending/{id}/approve` | 批准（**必填组 + 配额**） | ➖ |
| `POST` | `/api/admin/accounts/pending/{id}/reject` | 拒绝（可填理由） | 🔒（二次确认） |

**没有 `DELETE /api/admin/accounts/{id}`**。总纲的数据模型里 `requests` / `audit_log` 引用 `account_id`，物理删除会让历史悬空。

### 5.2 关键字段

```json
{
  "id": "acc_01J8...", "display_name": "alice", "username": "alice",
  "classes": ["admin", "tenant"],
  "status": "active",
  "groups": ["grp_default", "grp_power"],
  "created_at": "2026-01-01T00:00:00Z", "last_login_at": "2026-01-02T00:00:00Z", "key_count": 3,
  "credential_count": 0, "note": "内部备注",
  "quota": {"unit": "token", "limit": 200000, "used": 34000, "period": "day"},
  "rpm_limit": 60, "tpm_limit": 100000, "max_concurrency": 4
}
```

| 字段 | 可写 | 说明 |
|---|---|---|
| `classes` | ✅ | 三个值的集合；**增删都要 🔒 二次确认**（变更能力面，本文件里最需要慢一点的动作） |
| `status` | ✅ | `active` / `disabled` / `rejected` |
| `groups` | ✅ | 组 ID 数组 |
| `key_count` | ➖ | **只给数量，不给 Key 列表**（见下） |
| `credential_count` | ➖ | 同上 |
| `quota` | ✅ | **账户级**额度：`unit` 取 `request` / `token` / `credit`，`limit` 为上限，`used` 只读、由结算写入，`period` 取 `day` / `month` |
| `rpm_limit` / `tpm_limit` / `max_concurrency` | ✅ | **账户级**速率；不随 Key 变化 |

**为什么额度与速率是账户字段而不是 Key 字段**（2026-10-20 改定）：否则"同一账户多开 10 把 Key" 就等于额度翻 10 倍，账户级额度形同虚设。把闸门挂在账户上之后，Key 只剩三件事——认证、模型白名单、生命周期——这三件事都是"这把 Key 能做什么"，而"这个人能用多少"不再由它表达。**为什么 `used` 在同一个对象里而不是单独端点**：管理端在改 `limit` 时必须看到"已经用了多少"，否则改完才发现设得比已用量还低，等于立刻把一个账户拦死。

**为什么 `quota` 是单个对象而不是数组**：一个人在同一时刻只受**一条**个人额度约束（同时设多维度时数据层是多行，但那是 `group_policies` 里的池；账户自身的主额度保持单条，避免"到底哪条在生效"的歧义）。需要多维度时按 `unit` 各建一条——这一点在实现上由 `accounts` 表的列布局决定，不在本 API 的表达范围里。

**为什么详情里只给 `key_count` 与 `credential_count` 而不给列表**： 总纲 2.4 的隔离四条包含"资源分表归属"。`api_keys` 需要 `tenant` 类， `upstream_credentials` 需要 `contributor` 类。管理端账户**可以** 逐条查看它们（管理端本身就是跨类的运维面），但列表不在账户端点里返回， 是为了让这个端点不具备"一次拿到某人的全部资源"的能力。 需要时走各自的分组端点。

**为什么批准必须带组与配额**：总纲 2.7。缺任一项都返回 400， 错误码 `invalid_request`，且消息指明缺的是哪一项。

### 5.3 CSV 导入格式

```
用户名,密码[,组][,额度单位][,额度上限]
alice,S3cret-passphrase-2026,grp_power,token,200000
bob,,grp_default,request,1000
```

| 列 | 说明 |
|---|---|
| 用户名 | 唯一 |
| 密码 | 留空则生成随机密码，**一次性展示** |
| 组 | 可选，默认进默认组 |
| 额度单位 | 可选，`request` / `token` / `credit`；留空则不单独设额度，取全局默认值 |
| 额度上限 | 可选，与该单位配套 |

导入即 `active`（总纲 2.7），全进审计。

**为什么留空密码要生成随机值而不是拒绝**：批量导入的常见场景是 管理员只准备了一份名单。拒绝会让整个导入失败，而生成一次性密码 只是多一步"把结果导出给用户"的操作。

**为什么导入结果必须列出一次性密码**：CSV 导入是唯一一次 能看到这批密码的时刻。不返回就等于这批账户全部不可登录。

## 6. 组组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/groups` | 列表 | ➖ |
| `GET` | `/api/admin/groups/{id}` | 详情（成员、模型授权、策略） | ➖ |
| `POST` | `/api/admin/groups` | 新建 | ➖ |
| `PATCH` | `/api/admin/groups/{id}` | 改基本信息（名称、描述） | ➖（但 `is_default` 字段变更属「改组」🔒） |
| `PUT` | `/api/admin/groups/{id}/models` | **替换**模型授权集合 | 🔒（改组） |
| `PUT` | `/api/admin/groups/{id}/policies` | **替换**策略集合 | 🔒（改组） |
| `POST` | `/api/admin/groups/{id}/members` | 增成员 | 🔒（改组） |
| `DELETE` | `/api/admin/groups/{id}/members/{account_id}` | 移出成员 | 🔒（改组） |
| `DELETE` | `/api/admin/groups/{id}` | 删组（成员退回默认组） | 🔒（改组） |
| `POST` | `/api/admin/groups/{id}/duplicate` | 复制组 | ➖ |

**为什么模型授权与策略用 `PUT` 整体替换而不是 `PATCH` 局部改**： 总纲 2.6 的合并语义（放开取并集、限制取最严、布尔 deny 优先） 是对**整个集合**求值的。允许在集合上做局部增删， 会让"当前生效值"依赖一段难以复盘的历史。整体替换让每次变更 都是"从集合 A 到集合 B"的完整记录。

**为什么删组要回到默认组而不是变成无组**：总纲 2.6"删组不删人"。 无组账户的可见模型集合为空，等价于被静默停用—— 这不该是删组的一个副作用。

### 6.1 关键字段

```json
{
  "id": "grp_power", "name": "高额度组", "description": "内部试点", "is_default": false,
  "member_count": 12,
  "models": ["smart", "fast"],
  "policies": {
    "allow_create_key": true, "max_keys": 5, "allow_widen_allowlist": false,
    "account_quota": {"unit": "token", "limit": 200000, "period": "day"},
    "account_rpm_limit": 60, "account_tpm_limit": 100000, "account_max_concurrency": 4,
    "pool": [
      {"unit": "request", "period": "day", "limit": 500},
      {"unit": "credit", "period": "month", "limit": 2000000}
    ]
  }
}
```

**为什么额度字段改成 `account_quota` + `pool` 两个结构**（2026-10-20 改定）：① 额度不再挂在 Key 上，所以组策略里的一切额度都是**账户级**的，字段名必须体现这一点（`account_quota` 而不是裸的 `daily_token_cap`），否则读文档的人会以为它还是逐 Key 生效；② 计量维度从两种（requests / tokens）扩到三种（`request` / `token` / `credit`），单一标量字段放不下"维度 + 周期 + 上限"三元组，因此个人额度用一个对象、共享池用一个数组；③ 共享池是**可多行**的（三个维度各自独立判定、哪个先到算哪个），数组形态与数据层"多行 or 无行"一一对应。

**为什么组详情里没有端点、密钥、邀请这三类字段**：总纲 2.6 与 [../ui/03-admin.md](../ui/03-admin.md) 5.2 都明确它们不该存在。 API 层不定义它们，界面就无法渲染它们。

## 7. 真实模型组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/real-models` | 列表（含 `status` / `hidden` / `source`） | ➖ |
| `GET` | `/api/admin/real-models/{id}` | 详情 | ➖ |
| `POST` | `/api/admin/real-models/refresh` | 重新拉上游 `GET /v1/models` | ➖ |
| `PATCH` | `/api/admin/real-models/{id}` | 改显示名、分组、能力元数据、隐藏、排序 | ➖ |
| `POST` | `/api/admin/real-models/{id}/exclude` | 排除出候选（`aggregate/*` 默认已排除） | ➖ |

### 7.1 关键字段

```json
{
  "id": "rm_01J8...", "display_name": "DeepSeek-V4.1-Flash",
  "qualified_name": "dsh:deepseek-official/deepseek-flash", "upstream_id": "up_provider_a",
  "upstream_model_id": "deepseek-official/deepseek-flash", "group_name": "deepseek",
  "context_window": 128000,
  "modalities": ["text", "image"],
  "efforts": ["off", "low", "high", "max"],
  "status": "active", "hidden": false, "sort_order": 10, "source": "auto",
  "credit_multiplier": {"in": 1.0, "out": 1.0},
  "last_seen_at": "2026-01-02T00:00:00Z", "missing_since": null,
  "excluded_from_candidates": false
}
```

| 字段 | 可写 | 说明 |
|---|---|---|
| `display_name` / `group_name` / `hidden` / `sort_order` | ✅ | 写后 `source` 变 `override` |
| `context_window` / `modalities` / `efforts` | ✅ | 上游自动发现，管理员可覆盖 |
| `source` | ➖ | `auto` / `override`；`override` 后刷新不覆盖 |
| `status` / `missing_since` | ➖ | 由 `refresh` 与巡检维护 |
| `excluded_from_candidates` | ✅ | `aggregate/*` 为默认 `true`（总纲 1.4） |

**为什么 `qualified_name` 不可写**：总纲 11.2 定命名是 `dsh:渠道/上游模型 id`，它由上游目录决定。允许改写会让 "上游到底给了什么"变得不可追溯，而这正是 missing 巡检的判据。

**为什么 `aggregate/*` 的排除是数据而不是界面约定**： 总纲 1.4 把它定为硬约束。放在数据层意味着即便将来有别的 UI 或导入路径，这条约束也不会被绕过。

**为什么 `missing` 的处理是跳转到抽象模型的弃用端点**： 弃用是 `abstract_models` 的动作，且需要过两条硬校验（目标必须 `active`、弃用不可逆，见 [../ui/03-admin.md](../ui/03-admin.md) 第 6 节）。 在真实模型端点里就地改会绕过校验。

**为什么价格乘数 `credit_multiplier` 在真实模型上而不在组或设置里**：它是**逐模型**的属性，且被两处共用——`credit` 维度的扣减与 `price` 排序策略。挂在真实模型上保证两边读的是同一个数；放进设置页会变成一张与目录脱钩的映射表，两边一旦不一致就没人知道以谁为准。内置价格表覆盖不到的模型默认 `{in:1, out:1}`，避免新模型被算成免费或天价。

## 8. 抽象模型组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/abstract-models` | 列表（含候选数与可用候选数） | ➖ |
| `GET` | `/api/admin/abstract-models/{id}` | 详情 | ➖ |
| `POST` | `/api/admin/abstract-models` | 新建 | ➖ |
| `PATCH` | `/api/admin/abstract-models/{id}` | 改显示名、能力模式、能力声明、排序链 | 🔒（改抽象模型） |
| `PUT` | `/api/admin/abstract-models/{id}/candidates` | **替换**候选集 | 🔒（改抽象模型） |
| `POST` | `/api/admin/abstract-models/{id}/dry-run` | 保存前干跑 | ➖ |
| `GET` | `/api/admin/abstract-models/{id}/chain` | 替代链预览 | ➖ |
| `PUT` | `/api/admin/abstract-models/{id}/deprecation` | 保存弃用配置 | 🔒（改弃用） |
| `GET` | `/api/admin/abstract-models/{id}/references` | 被哪些组引用 | ➖ |

### 8.1 关键字段

```json
{
  "id": "am_smart", "name": "smart", "display_name": "通用智能", "capability_mode": "strict",
  "capabilities": {
    "context_window": 128000, "max_output": 8192, "vision": true, "tool_calling": true,
    "json_mode": true
  },
  "strategy": ["expiry-first", "balance"],
  "status": "active", "replacement": null, "deprecated_at": null, "sunset_at": null,
  "candidate_count": 4, "available_candidate_count": 2, "chain": null
}
```

| 字段 | 可写 | 说明 |
|---|---|---|
| `name` | ➖ | 对外名，**创建后不可改** |
| `capability_mode` | ✅ | `strict`（默认，取候选最小值）/ `filter`（取并集 + 路由前筛） |
| `strategy` | ✅ | 有序数组，元素取自 `expiry-first` / `balance` / `speed` / `price`，可选把 `round-robin` 追加在末尾作平局决胜；默认 `["expiry-first","balance"]` |
| `candidate_count` / `available_candidate_count` | ➖ | 后者是"声明式候选 → 可用凭据实例"的展开结果 |

**为什么 `name` 创建后不可改**：契约的稳定性来自名字。 改名的正确手段是弃用 + 替代链（总纲 3.5），而不是原地改名。

**为什么要有 `available_candidate_count`**：总纲 3.2 定候选集是 声明式的、运行时才展开成"候选 × 可用凭据实例"。 只给`candidate_count` 会让"配了但永远选不中"成为静默故障。

### 8.2 候选集

```json
{
  "candidates": [
    {
      "real_model_id": "rm_01J8...",
      "constraint": {"channel_scope": "all", "require": {"vision": true}},
      "weight": 100, "available_instances": 2
    }
  ]
}
```

**为什么 `available_instances` 是响应字段而不是请求字段**： 它由运行时状态决定，不可写。放在响应里是为了让保存前的 预览能显示"这条候选实际上有没有凭据支撑"。

### 8.3 干跑

```json
POST /api/admin/abstract-models/am_smart/dry-run
{"strategy": ["expiry-first", "balance"], "sample_size": 200}
```

```json
{
  "data": {
    "reachable": 198, "no_candidate": 2, "fallback_used": 0,
    "failures": [{"reason": "capability_unsupported", "count": 2}]
  }
}
```

**为什么干跑是保存路径上的强制步骤**：总纲第 13 章把 "抽象模型策略配错 → 全站不可用"列为风险，干跑是它的对策。 一个"记得点"的按钮不足以防住它，保存端点必须要求 一个新鲜的干跑结果（带时效）。

### 8.4 弃用与替代链

```json
PUT /api/admin/abstract-models/am_fast/deprecation
{
  "status": "deprecated",
  "replacement_model_id": "am_smart",
  "deprecated_at": "@1688169599",
  "sunset_at": "Sun, 30 Jun 2026 23:59:59 UTC",
  "migration_doc": "/docs/models/fast"
}
```

```json
GET /api/admin/abstract-models/am_fast/chain
```

```json
{
  "data": {
    "chain": ["<抽象模型名 A>", "<抽象模型名 B>", "<抽象模型名 C>"],
    "hops": 2, "final_active": "<抽象模型名 C>", "warning": null,
    "capability_check": {"ok": true, "checks": {"context_window": true, "max_output": true, "tool_calling": true, "vision": true}}
  }
}
```

**为什么 `warning` 字段仍然保留但正常情况恒为 `null`**：它的唯一用途是运行时兜底命中（visited 检出环，即配置被绕过直接写库）时的告警，不是"链太长"的提示。跳数上限取消后，"长链"不再是需要提示的状态——它只是一串迟早会走完的迁移（[../ui/03-admin.md](../ui/03-admin.md) 第 7.4 节）。

三条硬校验，保存时即时执行：

| 校验 | 失败行为 |
|---|---|
| 替代目标存在且状态为 `active` | 拒绝保存，返回 `invalid_request` + 具体原因（目标是 `deprecated` / `disabled` / 不存在） |
| 替代目标能力逐项 ≥ 原模型 | 拒绝保存，返回哪一项不合格 |
| 环检测（防御性） | 拒绝保存，返回环路径 |

**为什么校验必须在保存时而不是运行时**：运行时才发现配错 意味着线上已经有一批客户端在跑了。配置时即时校验是主防线，运行时的 `visited` 集合是兜底。**为什么校验项是这三条**（[../ui/03-admin.md](../ui/03-admin.md) 第 7.4 节）：两条结构规则（目标只能 `active`、`deprecated` 单向）让环在结构上不可能出现，因此环检测从"主防线"降为"防御性兜底"——留它是因为配置路径可能被绕过（直接写库、导数据），而不是因为正常配置会造出环。**为什么目标必须 active 这条不能省**：它是唯一能阻止"死路"（弃用指向另一个弃用，客户端跟着跟着落到一个同样要被弃用的名字上）的规则。

**为什么 `capability_check` 是四项**：上下文窗口、最大输出、 工具调用、视觉——总纲 3.5 明确这四项逐项比较。

## 9. 上游与凭据组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/upstreams` | 列表 | ➖ |
| `POST` | `/api/admin/upstreams` | 新建 | ➖ |
| `PATCH` | `/api/admin/upstreams/{id}` | 改名称、base_url、启用 | ➖（禁用时 🔒 二次确认） |
| `POST` | `/api/admin/upstreams/{id}/kind` | 变更 `kind`（`relay` / `vendor` / `selfhosted`） | 🔒（改上游形态） |
| `POST` | `/api/admin/upstreams/{id}/test` | 连通性测试 | ➖ |
| `GET` | `/api/admin/credentials` | 列表（筛选 `owner` / `provider` / `verify_status` / `status`） | ➖ |
| `GET` | `/api/admin/credentials/{id}` | 详情（**无明文**） | ➖ |
| `POST` | `/api/admin/credentials/{id}/suspend` | 暂停 | ➖ |
| `POST` | `/api/admin/credentials/{id}/ban` | 封禁 | 🔒（二次确认） |
| `POST` | `/api/admin/credentials/{id}/notify` | 通知贡献者 | ➖ |

**没有任何端点返回凭据明文**。总纲 9.4 定"凭据明文永不回显"。 管理端详情返回的是 `provider` / `external_label` / `verify_detail` 等元数据。

**为什么 `relay` 类型不接受 `base_url`**（2026-10-20 改定）：这一类的调用路径是 **Node sidecar 直接调用 providers-hub 的 provider adapter**（`lib/*-adapter.js`），全程不经过 HTTP 网关。构造 `kind=relay` 时如果传了地址，端点在保存前就拒绝——不是"忽略它"，而是明确报错。**为什么报错而不是忽略**：忽略会让管理员以为自己配的地址生效了，等排查时才发现根本没被使用；报错把这件事在保存那一刻讲清楚。

**三类 `kind` 的字段要求**：

| `kind` | `base_url` | 调用路径 |
|---|---|---|
| `relay` | **禁止提供** | Node sidecar → providers-hub provider adapter |
| `vendor` | 必填（该供应商官方地址） | Go 直连上游官方端点，凭据来自贡献 |
| `selfhosted` | 必填（自建 / 本地兼容端点） | Go 直连该端点 |

**为什么 `kind` 的变更要单独一个端点并标危险**：它改变的是这条上游**怎么被调用**，不是它的名字或开关。就地 `PATCH` 会让"我改了 base_url"和"我改了调用方式"看起来是同一类操作，而后者会让所有依赖该上游的候选在下一次请求时走一条完全不同的代码路径。

## 10. 鉴定组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/verifications` | 队列（筛选 `verdict` / `provider` / `source`） | ➖ |
| `GET` | `/api/admin/verifications/{id}` | 详情 + 证据 + 复检历史 | ➖ |
| `POST` | `/api/admin/verifications/{id}/l2` | 触发 L2 深度鉴定 | ➖ |
| `POST` | `/api/admin/verifications/{id}/override` | 人工改判（**必填理由**） | 🔒（二次确认） |
| `POST` | `/api/admin/verifications/{id}/recheck` | 加入 / 移出复检队列 | ➖ |

### 10.1 关键字段

```json
{
  "id": "ver_01J8...", "credential_id": "cred_01J8...", "provider": "example-provider",
  "claimed_model": "some-model-id", "source": "new_contribution", "stage": "L1",
  "verdict": "unverified",
  "stage_results": {
    "L0": {"ok": true, "checked_at": "..."},
    "L1": {"ok": true, "score": null, "reason": "no_reference_fingerprint"},
    "L2": null
  },
  "evidence": [{"signal": "kbf", "strength": "medium", "result": "skipped"}],
  "override": null,
  "recheck": {"enabled": true, "last_at": "..."}
}
```

| 字段 | 说明 |
|---|---|
| `stage` | `L0` / `L1` / `L2`，当前进行到哪一步 |
| `verdict` | `verified` / `suspect` / `unverified` / `trusted` |
| `evidence[].strength` | `strong` / `medium` / `weak`（总纲 4.3 的分级） |
| `override` | 人工改判记录，含理由与操作人 |

**为什么 `stage` 与 `verdict` 是两个字段**：`stage` 是进度， `verdict` 是结论。三级流水线里 L0 只需几秒而 L1 约一分钟， 合并成一个字段会让贡献者与管理员都无法判断"到底卡在哪"。

**为什么 `verdict` 的 `unverified` 不进 `suspect` 的处置路径**： 总纲 4.4 定 `unverified` 默认放行（库中无参考指纹）， `suspect` 拒绝并给证据。API 层保持它们是两个独立的枚举值， 避免下游把"放行"误实现成"拒绝"。

## 11. 审计与设置组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/audit` | 查询（`from` / `to` / `account_id` / `acting_class` / `action` / `result`） | ➖ |
| `GET` | `/api/admin/audit/{id}` | 单条详情 | ➖ |
| `GET` | `/api/admin/audit/export` | 导出 | ➖ |
| `GET` | `/api/admin/settings` | 全部设置（按分组） | ➖ |
| `PATCH` | `/api/admin/settings` | 批量改设置 | 🔒（二次确认，见下表） |
| `POST` | `/api/admin/settings/ip-check` | 用当前源 IP 自检管理面允许列表 | ➖ |

**没有 `DELETE /api/admin/audit`**。总纲 9.5 的 append-only 是绝对约束。

**五类需要二次确认（🔒）的设置**（与 [../ui/03-admin.md](../ui/03-admin.md) 第 11 节一致）。二次确认发生在界面层，接口不做额外验证：

| 设置键 | 为什么 |
|---|---|
| `registration_enabled` | 关闭立即切断新增账户 |
| `global_budget_cap` | 配错导致全站拒绝 |
| `trusted_proxies` | 配错会让 IP 限速失效或误封 |
| `record_request_body` | 直接把用户内容纳入存储 |
| `login_backoff` / `lockout_threshold` | 调松即削弱全站唯一的身份防线 |
| ~~`admin_ip_allowlist`~~ | **不提供**（2026-10-19 定案）。公网下管理员 IP 会变，白名单是唯一能把自己锁在门外的配置 |

**为什么 `ip-check` 是一个独立端点**：它是唯一一个能让管理员 自锁的配置。[../ui/03-admin.md](../ui/03-admin.md) 第 11 节要求 保存前用当前源 IP 自检，独立端点让这个自检与保存解耦， 前端可以先自检再提交。

**为什么脱敏模式是设置而不是请求参数**：总纲 9.5 定 "默认不记录请求正文，排查时开脱敏模式并在 UI 明示"。 把它做成会话级参数会让"当前是否在记录正文"变得不可审计。

## 12. 待处理事项组

**这是本次新增的能力**（不是在改旧端点）：后台任务把"需要管理员动手的事"写进 `admin_inbox`，控制台用这一组端点拉取与标记。

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/inbox` | 列表（筛选 `kind` / `severity` / `read` / 时间） | ➖ |
| `GET` | `/api/admin/inbox/count` | **只返回未读数**（导航角标专用，不拉列表） | ➖ |
| `GET` | `/api/admin/inbox/{id}` | 单条详情（含 `detail_json`） | ➖ |
| `PATCH` | `/api/admin/inbox/{id}` | 标记已读 / 未读 | ➖ |
| `POST` | `/api/admin/inbox/read` | 批量标记已读（body 带 id 数组，或带筛选条件） | ➖ |

**为什么单独给一个 `/inbox/count` 端点**：导航角标每 60 秒轮询一次，它只需要一个整数。用列表端点做这件事意味着每分钟拉一次完整列表（含 `detail_json`），在大表上这是可观的读放大；计数端点可以直接命中索引。**为什么不合并成 `?fields=count` 参数**：参数化的"只取数量"会让缓存与限流多一种变体，而分开一个端点只是多一行路由。

**为什么标记已读用 `PATCH` 而不是 `POST`**：它是幂等的字段更新（`read_at` / `read_by`），与第 3.1 节"`PATCH` 是局部更新"的约定一致；`POST` 在本文件里保留给"创建"与"触发动作"。

**没有任何端点能删除 `admin_inbox` 记录**。它是运维事件的留痕，与审计同类。清理策略见第 13 节待定项。

**入站字段（`admin_inbox` 表）**：

```json
{
  "id": "inb_01J8...", "kind": "model_change", "severity": "warning",
  "title": "渠道 A 下模型 X 已从上游消失",
  "detail_json": {"real_model_id": "rm_01J8...", "change": "disappeared"},
  "related_id": "rm_01J8...", "created_at": "2026-01-02T00:00:00Z",
  "read_at": null, "read_by": null
}
```

| 字段 | 说明 |
|---|---|
| `kind` | `registration` / `model_change` / `failure` / `pool_alert` / `breaker_alert` |
| `severity` | `error` / `warning` / `info`，列表**默认按此降序** |
| `related_id` | 指向关联对象（账户 / 真实模型 / 上游 / 组 / 凭据），控制台据此生成跳转 |
| `read_at` / `read_by` | 已读状态；`read_by` 记录是哪个管理员读的 |

**为什么 `related_id` 是一个不带头缀的裸 id 而不是外键**：关联对象横跨五张表（账户、真实模型、上游、组、凭据），单一外键列放不下。裸 id + `kind` 组合足以定位，代价是数据库层不能做级联约束——**这个代价可以接受**，因为收件箱条目本来就是"发生过的事"，关联对象被删掉之后条目仍应存在（用户点进去看到 404 即可，不该让整条记录消失）。

**为什么不给 `related_id` 加表名后缀**（如 `real_model:rm_01J8...`）：`kind` 已经隐含了目标表，再加一层会造出两个必须保持一致的字段。

**为什么不在这个接口里做"处理"**：注册审批要选组与配额、弃用要过两条硬校验、池上限要算已用量——把这些搬进来等于把各页面的校验逻辑复制一份，必然漂移。控制台只做"跳转"，处理仍在原页面（[../ui/03-admin.md](../ui/03-admin.md) 第 12.2 节）。

## 13. 待定项

> **待定**：管理 API 是否需要为控制台提供一个 "聚合仪表盘"端点（一次返回各分组的关键计数）？ 当前每个分组各自查询，首页会发出多次请求。

> **待定**：`POST /accounts/{id}/password` 返回的一次性密码，其有效期与"首次登录强制改密"是否强制。当前按"生成即长期有效，直到管理员再次重置"撰写。

> **待定**：审计导出的格式（CSV / JSONL）与保留期。 与 [../ui/03-admin.md](../ui/03-admin.md) 第 13 节的对应待定项相关。

> **待定**：鉴定证据在 API 层对管理端返回的字段粒度。 [../ui/03-admin.md](../ui/03-admin.md) 第 13 节已列"披露粒度"待定， 这里需要同步决定是否只返回 `evidence[]` 的摘要。

> **待定**：`admin_inbox` 的 `related_id` 在关联对象被删除（如凭据被合并、真实模型被下线）后的语义。 当前按"条目保留、点进去 404"撰写。

> **待定**：`admin_inbox` 的保留期与归档方式。它既是运维留痕也是可增长的表， 与审计保留期（上一条）是同一批问题，建议一并定。
