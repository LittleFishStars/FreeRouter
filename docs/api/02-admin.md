> 分册：架构 · 依赖：[01-inbound.md](01-inbound.md)、[03-errors.md](03-errors.md)、[../ui/03-admin.md](../ui/03-admin.md) · 对应总纲：PLAN.md 第 2、3、4、8 章

# 管理 API

## 1. 管理 API 的定位
管理 API 是控制台（[../ui/03-admin.md](../ui/03-admin.md)）的数据面， 不对外公开、不签发长期令牌、不提供跨账户的服务间调用。
三条定位：
| 定位 | 说明 |
|---|---|
| 单一消费方 | 控制台前端。**不承诺第三方兼容性** |
| 会话承载 | 认证走 HttpOnly 会话 Cookie，不签 API Token |
| 提权敏感 | 每个写端点声明是否需要提权（`elevated`）与是否每次重验 |

**为什么管理 API 不签发 API Token**：总纲 2.5 的提权模型是 "进管理面当场再验一次，有效期 15 分钟"。一个长期 Token 天然绕过这个模型—— 它一旦签发，15 分钟就变成了"永不过期"。不签发是从结构上消除这个漏洞。

**为什么不承诺第三方兼容**：一旦承诺，抽象模型与候选集的内部结构 就会变成对外契约，后续的模型体系演进会被冻结。

## 2. 认证与提权

### 2.1 会话

| 项 | 规则 | 来源 |
|---|---|---|
| Cookie 名 | `fr_session` | —— |
| 属性 | `HttpOnly`、`Secure`、`SameSite=Lax` | 总纲 9.2 |
| 时效 | 由全局设置决定，空闲失效 | 总纲 2.5 |
| 存储 | 服务端只存 `token_hash` | `sessions.token_hash` |

**为什么 `SameSite=Lax` 而不是 `Strict`**：OAuth 贡献回调与 OAuth 登录 都是从外部站点跳回本站的。`Strict` 会让这些回调落地时**不带 Cookie**， 用户看到的是"已登录状态下被要求重新登录"的循环。

### 2.2 两层权限检查

每个管理端点必须先过两层：

| 层 | 检查 | 失败 |
|---|---|---|
| 第一层 | 会话所属账户的 `classes` 含 `admin` | **404** |
| 第二层 | 会话 `elevated_until > now` | **403** + `elevation_required` |

**为什么"没有 admin 类"返回 404 而不是 403**：403 等于确认 "这个路径存在，只是你没权限"。对未提权的 admin 账户返回 403 是必要的 （他确实有这个能力，只是需要提权），但对没有 `admin` 类的账户， 连路径的存在都不该被确认。这条与 [../ui/01-ia.md](../ui/01-ia.md) 第 7 节一致。

### 2.3 提权头与要求

```
Cookie: fr_session=...
X-FR-StepUp: <本次操作的短时凭据>
```

| 端点类别 | 需要 `admin` 类 | 需要提权 | 危险操作需每次重验 |
|---|---|---|---|
| 只读查询 | ✅ | ✅ | ➖ |
| 普通写（增删成员、启停凭据） | ✅ | ✅ | ➖ |
| 危险写（见下表） | ✅ | ✅ | ✅ |

危险写清单（总纲 2.5 的"删账户、改组、改抽象模型、改弃用" + [../ui/06-design.md](../ui/06-design.md) 第 5 节的判据）：

| 类别 | 端点 |
|---|---|
| 账户 | 改权限类、授予 `admin`、禁用、重置密码、合并 |
| 组 | 改模型授权、改策略、删除组 |
| 抽象模型 | 改能力模式、改策略链、增删候选、保存弃用配置、设 `disabled` |
| 上游 / 凭据 | 禁用上游、封禁凭据 |
| 鉴定 | 人工改判 |
| 设置 | 见第 11 节的五类危险配置 |

**为什么把"危险写清单"显式列出而不是让实现判断**：清单是 [../ui/03-admin.md](../ui/03-admin.md) 1.3 的确认框与审计的 `elevated` 标志 共同依赖的判据。留一个"其他类似操作自行判断"的口子， 必然出现某些危险操作被漏掉确认。

**为什么不给管理 API 加"批量危险操作"端点**：批量会把"每次重验" 变成"一次重验执行 N 个危险操作"。UI 上的批量批准（ [../ui/03-admin.md](../ui/03-admin.md) 第 3 节）是非危险的，不在此列。

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
  "audit_id": "aud_01J8...", "elevated": true
}
```

**为什么每个写响应都带 `audit_id`**：总纲 9.5 的审计是 append-only 的， 但它必须能被界面回指。带 `audit_id` 让"我刚做的这次修改"在审计页 是可定位的，从根上避免"改完不知道有没有生效"的疑问。

**为什么带 `elevated`**：让前端能确认这次操作确实在提权状态下完成了， 而不是因为某个环节漏检。

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
| 初始化 | `/api/setup` | 同第 2 节（**不在 `/api/admin` 下**） |

**为什么初始化接口不在 `/api/admin` 下**：它在"库中还没有任何账户"时 就必须可用，而 `/api/admin` 的两层检查（`admin` 类 + 提权） 在那个时刻没有任何账户可以满足。放进 `/api/admin` 会导致悖论： 没有账户就不能创建第一个账户。

## 5. 账户组

### 5.1 端点

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/accounts` | 列表（筛选 `class` / `status` / `group_id`） | ➖ |
| `GET` | `/api/admin/accounts/{id}` | 详情 | ➖ |
| `POST` | `/api/admin/accounts` | 单个添加（同 CSV 导入的字段） | ➖ |
| `POST` | `/api/admin/accounts/import` | CSV 导入 | ➖ |
| `PATCH` | `/api/admin/accounts/{id}` | 改权限类、状态、所属组、备注、显示名 | ⚠️ |
| `POST` | `/api/admin/accounts/{id}/password` | 重置密码 | ⚠️ 每次重验 |
| `POST` | `/api/admin/accounts/{id}/sessions/revoke` | 吊销全部会话 | ➖ |
| `POST` | `/api/admin/accounts/{id}/merge` | 合并到目标账户 | ⚠️ 每次重验 |
| `POST` | `/api/admin/accounts/{id}/merge/rollback` | `reversible_until` 内回滚 | ⚠️ |
| `GET` | `/api/admin/accounts/pending` | 审核队列 | ➖ |
| `POST` | `/api/admin/accounts/pending/{id}/approve` | 批准（**必填组 + 配额**） | ➖ |
| `POST` | `/api/admin/accounts/pending/{id}/reject` | 拒绝（可填理由） | ⚠️ |

**没有 `DELETE /api/admin/accounts/{id}`**。总纲的数据模型里 `requests` / `audit_log` 引用 `account_id`，物理删除会让历史悬空。

### 5.2 关键字段

```json
{
  "id": "acc_01J8...", "display_name": "alice", "username": "alice",
  "classes": ["admin", "tenant"],
  "status": "active",
  "groups": ["grp_default", "grp_power"],
  "created_at": "2026-01-01T00:00:00Z", "last_login_at": "2026-01-02T00:00:00Z", "key_count": 3,
  "credential_count": 0, "note": "内部备注"
}
```

| 字段 | 可写 | 说明 |
|---|---|---|
| `classes` | ✅ | 三个值的集合；**增删都要 ⚠️** |
| `status` | ✅ | `active` / `disabled` / `rejected` |
| `groups` | ✅ | 组 ID 数组 |
| `key_count` | ➖ | **只给数量，不给 Key 列表**（见下） |
| `credential_count` | ➖ | 同上 |

**为什么详情里只给 `key_count` 与 `credential_count` 而不给列表**： 总纲 2.4 的隔离四条包含"资源分表归属"。`api_keys` 需要 `tenant` 类， `upstream_credentials` 需要 `contributor` 类。管理端账户**可以** 逐条查看它们（管理端本身就是跨类的运维面），但列表不在账户端点里返回， 是为了让这个端点不具备"一次拿到某人的全部资源"的能力。 需要时走各自的分组端点。

**为什么批准必须带组与配额**：总纲 2.7。缺任一项都返回 400， 错误码 `invalid_request`，且消息指明缺的是哪一项。

### 5.3 CSV 导入格式

```
用户名,密码[,组][,日额度]
alice,S3cret-passphrase-2026,grp_power,200000
bob,,grp_default,
```

| 列 | 说明 |
|---|---|
| 用户名 | 唯一 |
| 密码 | 留空则生成随机密码，**一次性展示** |
| 组 | 可选，默认进默认组 |
| 日额度 | 可选，默认取全局默认值 |

导入即 `active`（总纲 2.7），全进审计。

**为什么留空密码要生成随机值而不是拒绝**：批量导入的常见场景是 管理员只准备了一份名单。拒绝会让整个导入失败，而生成一次性密码 只是多一步"把结果导出给用户"的操作。

**为什么导入结果必须列出一次性密码**：CSV 导入是唯一一次 能看到这批密码的时刻。不返回就等于这批账户全部不可登录。

## 6. 组组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/groups` | 列表 | ➖ |
| `GET` | `/api/admin/groups/{id}` | 详情（成员、模型授权、策略） | ➖ |
| `POST` | `/api/admin/groups` | 新建 | ➖ |
| `PATCH` | `/api/admin/groups/{id}` | 改基本信息（名称、描述） | ➖ |
| `PUT` | `/api/admin/groups/{id}/models` | **替换**模型授权集合 | ⚠️ 每次重验 |
| `PUT` | `/api/admin/groups/{id}/policies` | **替换**策略集合 | ⚠️ 每次重验 |
| `POST` | `/api/admin/groups/{id}/members` | 增成员 | ➖ |
| `DELETE` | `/api/admin/groups/{id}/members/{account_id}` | 移出成员 | ➖ |
| `DELETE` | `/api/admin/groups/{id}` | 删组（成员退回默认组） | ⚠️ 每次重验 |
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
    "daily_token_cap": 200000, "monthly_token_cap": 4000000, "rpm_limit": 60,
    "tpm_limit": 100000, "max_concurrency": 4
  }
}
```

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
  "qualified_name": "dsh:deepseek-official/deepseek-flash", "upstream_id": "up_dshgateway",
  "upstream_model_id": "deepseek-official/deepseek-flash", "group_name": "deepseek",
  "context_window": 128000,
  "modalities": ["text", "image"],
  "efforts": ["off", "low", "high", "max"],
  "status": "active", "hidden": false, "sort_order": 10, "source": "auto",
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

**为什么 `missing` 的处理是跳转到抽象模型的弃用端点**： 弃用是 `abstract_models` 的动作，且需要过环检测与深度校验 （见 [../ui/03-admin.md](../ui/03-admin.md) 第 6 节）。 在真实模型端点里就地改会绕过校验。

## 8. 抽象模型组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/abstract-models` | 列表（含候选数与可用候选数） | ➖ |
| `GET` | `/api/admin/abstract-models/{id}` | 详情 | ➖ |
| `POST` | `/api/admin/abstract-models` | 新建 | ➖ |
| `PATCH` | `/api/admin/abstract-models/{id}` | 改显示名、能力模式、能力声明、策略链 | ⚠️ 每次重验 |
| `PUT` | `/api/admin/abstract-models/{id}/candidates` | **替换**候选集 | ⚠️ 每次重验 |
| `POST` | `/api/admin/abstract-models/{id}/dry-run` | 保存前干跑 | ➖ |
| `GET` | `/api/admin/abstract-models/{id}/chain` | 替代链预览 | ➖ |
| `PUT` | `/api/admin/abstract-models/{id}/deprecation` | 保存弃用配置 | ⚠️ 每次重验 |
| `GET` | `/api/admin/abstract-models/{id}/references` | 被哪些组引用 | ➖ |

### 8.1 关键字段

```json
{
  "id": "am_smart", "name": "smart", "display_name": "通用智能", "capability_mode": "strict",
  "capabilities": {
    "context_window": 128000, "max_output": 8192, "vision": true, "tool_calling": true,
    "json_mode": true
  },
  "strategy": ["expiry_first", "cheapest"],
  "status": "active", "replacement": null, "deprecated_at": null, "sunset_at": null,
  "candidate_count": 4, "available_candidate_count": 2, "chain": null
}
```

| 字段 | 可写 | 说明 |
|---|---|---|
| `name` | ➖ | 对外名，**创建后不可改** |
| `capability_mode` | ✅ | `strict`（默认，取候选最小值）/ `filter`（取并集 + 路由前筛） |
| `strategy` | ✅ | 有序数组，见 [01-inbound.md](01-inbound.md) 与总纲 3.3 |
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
{"strategy": ["expiry_first", "cheapest"], "sample_size": 200}
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
    "chain": ["fast", "balanced", "smart"],
    "hops": 2, "final_active": "smart", "warning": "迁移链过长，建议收敛",
    "capability_check": {"ok": true, "checks": {"context_window": true, "max_output": true, "tool_calling": true, "vision": true}}
  }
}
```

三条硬校验（总纲 3.5），保存时即时执行：

| 校验 | 失败行为 |
|---|---|
| 环检测 | 拒绝保存，返回 `invalid_request` + 环路径 |
| 深度 ≤ 5 跳 | 拒绝保存 |
| 替代目标能力逐项 ≥ 原模型 | 拒绝保存，返回哪一项不合格 |

**为什么校验必须在保存时而不是运行时**：运行时才发现配错 意味着线上已经有一批客户端在跑了。总纲定的两条硬约束 （环检测 + 深度上限）里，配置时即时校验是主防线， 运行时的 `visited` 是兜底。

**为什么 `capability_check` 是四项**：上下文窗口、最大输出、 工具调用、视觉——总纲 3.5 明确这四项逐项比较。

## 9. 上游与凭据组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/upstreams` | 列表 | ➖ |
| `POST` | `/api/admin/upstreams` | 新建 | ➖ |
| `PATCH` | `/api/admin/upstreams/{id}` | 改名称、base_url、启用 | ⚠️（禁用时） |
| `POST` | `/api/admin/upstreams/{id}/test` | 连通性测试 | ➖ |
| `GET` | `/api/admin/credentials` | 列表（筛选 `owner` / `provider` / `verify_status` / `status`） | ➖ |
| `GET` | `/api/admin/credentials/{id}` | 详情（**无明文**） | ➖ |
| `POST` | `/api/admin/credentials/{id}/suspend` | 暂停 | ➖ |
| `POST` | `/api/admin/credentials/{id}/ban` | 封禁 | ⚠️ 每次重验 |
| `POST` | `/api/admin/credentials/{id}/notify` | 通知贡献者 | ➖ |

**没有任何端点返回凭据明文**。总纲 9.4 定"凭据明文永不回显"。 管理端详情返回的是 `provider` / `external_label` / `verify_detail` 等元数据。

**为什么 `dshgateway` 类型的 `base_url` 只能是 `127.0.0.1:8326`**： 总纲 5.3 的唯一出口原则（上游拒绝通配地址）。 构造 `kind=dshgateway` 时如果传了别的地址，端点在保存前就拒绝。

## 10. 鉴定组

| 方法 | 路径 | 说明 | 危险 |
|---|---|---|---|
| `GET` | `/api/admin/verifications` | 队列（筛选 `verdict` / `provider` / `source`） | ➖ |
| `GET` | `/api/admin/verifications/{id}` | 详情 + 证据 + 复检历史 | ➖ |
| `POST` | `/api/admin/verifications/{id}/l2` | 触发 L2 深度鉴定 | ➖ |
| `POST` | `/api/admin/verifications/{id}/override` | 人工改判（**必填理由**） | ⚠️ 每次重验 |
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
| `GET` | `/api/admin/audit` | 查询（`from` / `to` / `account_id` / `acting_class` / `action` / `result` / `elevated`） | ➖ |
| `GET` | `/api/admin/audit/{id}` | 单条详情 | ➖ |
| `GET` | `/api/admin/audit/export` | 导出 | ➖ |
| `GET` | `/api/admin/settings` | 全部设置（按分组） | ➖ |
| `PATCH` | `/api/admin/settings` | 批量改设置 | ⚠️（见下表） |
| `POST` | `/api/admin/settings/ip-check` | 用当前源 IP 自检管理面允许列表 | ➖ |

**没有 `DELETE /api/admin/audit`**。总纲 9.5 的 append-only 是绝对约束。

五类需要 ⚠️ 每次重验的设置（与 [../ui/03-admin.md](../ui/03-admin.md) 第 11 节一致）：

| 设置键 | 为什么 |
|---|---|
| `registration_enabled` | 关闭立即切断新增账户 |
| `global_budget_cap` | 配错导致全站拒绝 |
| `trusted_proxies` | 配错会让 IP 限速失效或误封 |
| `record_request_body` | 直接把用户内容纳入存储 |
| `admin_ip_allowlist` | 配错会把管理员锁在外面 |

**为什么 `ip-check` 是一个独立端点**：它是唯一一个能让管理员 自锁的配置。[../ui/03-admin.md](../ui/03-admin.md) 第 11 节要求 保存前用当前源 IP 自检，独立端点让这个自检与保存解耦， 前端可以先自检再提交。

**为什么脱敏模式是设置而不是请求参数**：总纲 9.5 定 "默认不记录请求正文，排查时开脱敏模式并在 UI 明示"。 把它做成会话级参数会让"当前是否在记录正文"变得不可审计。

## 12. 待定项

> **待定**：管理 API 是否需要为控制台提供一个 "聚合仪表盘"端点（一次返回各分组的关键计数）？ 当前每个分组各自查询，首页会发出多次请求。

> **待定**：`X-FR-StepUp` 凭据的具体形态（一次性口令 / TOTP 验证后的短时令牌）。 总纲 2.5 只说"当场再验（TOTP 或重输密码）"，未定提权后如何承载。

> **待定**：审计导出的格式（CSV / JSONL）与保留期。 与 [../ui/03-admin.md](../ui/03-admin.md) 第 12 节的对应待定项相关。

> **待定**：鉴定证据在 API 层对管理端返回的字段粒度。 [../ui/03-admin.md](../ui/03-admin.md) 第 12 节已列"披露粒度"待定， 这里需要同步决定是否只返回 `evidence[]` 的摘要。
