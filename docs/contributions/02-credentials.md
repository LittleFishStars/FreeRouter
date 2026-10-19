> 分册：贡献与凭据 · 依赖：[01-flow.md](01-flow.md)、[../models/05-verification.md](../models/05-verification.md)、[../security/05-keys.md](../security/05-keys.md)、[../security/01-accounts.md](../security/01-accounts.md) · 对应总纲：PLAN.md 4.2 / 4.5 / 9.4 / 10

# 凭据归属、加密与生命周期

## 1. 凭据是什么

一条"凭据"= **一个有归属的、可用于出站调用的上游访问凭据**。它可能是某供应商账号下的一个 OAuth 授权，也可能是一个 OpenAI 兼容端点的 API Key。

总纲 4.2 的表结构：

```sql
upstream_credentials(
  id, owner_account_id,        -- 归属账户（要求该账户有 contributor 类）
  provider, external_id, external_label,
  secret_ref,                  -- 加密引用，不明文
  status, login_enabled,
  created_at, last_used_at,
  verify_status, verify_detail,
  UNIQUE(provider, external_id)  -- 一个上游账号只能绑一个站点账户
)
```

### 1.1 逐字段的为什么

| 字段 | 为什么需要它 |
|---|---|
| `owner_account_id` | 没有归属就没有"我的贡献"这一概念，也就无法做回报与撤销。**归属是本表存在的理由** |
| `provider` + `external_id` | 这两者合起来是**外部身份的坐标**。有它才能做 OAuth 登录反查、才能做撞身份检测 |
| `external_label` | 给贡献者看的可读标识（如账号名）。明文 key 永不可见，标签是贡献者唯一能"认出自己那条"的东西 |
| `secret_ref` | **加密引用**而不是密文本身。密钥材料统一落 `upstream_secrets`，本表只存引用——这样"谁能取到密钥"由一个地方决定，而不是散落多处 |
| `status` | 生命周期（第 5 节） |
| `login_enabled` | 该凭据能否作为登录身份（第 8 节） |
| `created_at` / `last_used_at` | 审计与回收；`last_used_at` 也是"这条凭据还有没有价值"的唯一客观信号 |
| `verify_status` / `verify_detail` | 鉴定的判定与证据（[../models/05-verification.md](../models/05-verification.md)） |

**为什么 `provider` / `external_id` 不拆到独立的「外部身份」表里**：撞身份判定（`UNIQUE`）与 OAuth 反查都发生在**同一行**上，拆表会让这两个高频操作都要 join，且无法用数据库的唯一约束来保证不变量。

## 2. 四张表的关系

```
upstreams            可调用的通道：base_url + kind(dshgateway|contributed) + owner_account_id
   │ 1
   │ n
upstream_secrets     密钥材料：ciphertext + nonce（AES-GCM），主密钥不入库
   │
   │ 引用
upstream_credentials 归属与身份：谁贡献的、外部身份是什么、状态如何、鉴定结论
   │
   │ 提供
real_models          真实模型目录（source=auto|override，status 含 missing）
   │
   │ 被声明为候选
abstract_candidates  抽象模型的候选集（声明式，不绑凭据）
```

### 2.1 为什么"归属"在 `upstream_credentials` 而不在 `upstreams`

`upstreams` 表也有 `owner_account_id`（用于平台自有凭据的 `NULL` 表达）。那么归属到底看谁？

- `upstreams.owner_account_id = NULL` 表达的是**平台自有**——这是一个**来源属性**；
- `upstream_credentials.owner_account_id` 表达的是**贡献归属**——这是一个**人属性**。

两者不重复：前者回答"这条通道是不是我们自己配的"，后者回答"这条通道是谁贡献的、回报该给谁"。混成一个字段会导致平台自有凭据也必须填一个假账户。

> **待定**：一条 `upstream_credentials` 与一条 `upstreams` 是否严格 1:1。同一凭据可服务多个 base_url（例如同账号的不同区域端点）时，是否拆成多条 `upstreams`。

## 3. 归属规则

总纲 4.2 的硬规则：

- 凭据**只归属 `contributor` 类**的账户；
- `tenant` 类账户名下一律**没有**凭据；
- **平台自有凭据**（我们自己配的官方 key）用 `owner_account_id = NULL` 表达，与贡献凭据同构但来源不同；
- 凭据明文**永不回显**，贡献者只能看到标签与状态。

### 3.1 为什么凭据不能归 `tenant`

这是总纲 2.3"隔离"的准确含义在凭据维度上的投影。如果凭据可以归 `tenant` 类账户，会立刻出现三个问题：

| 问题 | 后果 |
|---|---|
| 归属与能力绑死 | 一个能创建 Key、能调用模型的身份同时拥有"上游供给"。他可以用自己的 Key 优先打自己的凭据，把平台做成自己的私人中转 |
| 回报无法结算 | 回报（第 7 节）面向的是贡献者；若贡献者同时是租户，"贡献换额度"就从"激励机制"变成了"自产自销"，统计口径失去意义 |
| 隔离失去边界 | 总纲 2.7 要求账户之间没有归属关系，且租户看不到任何真实模型名。凭据是**真实模型的来源**——让租户持有凭据，等于给隔离开了一道侧门 |

**反过来，`contributor` 持有凭据没有任何风险**：它没有调用能力、不能创建 Key、看不到真实模型名（[01-flow.md](01-flow.md) 第 6 节）。它对凭据能做的最多只是"登记"与"撤销"。

### 3.2 为什么"贡献者只能看标签与状态"

明文不可回显是总纲 9.4 的结论（"凭据明文永不回显，连贡献者本人也不行"）。理由是本系统的一条基本不对称：

- 密钥在库里是**加密的**，但它每次被调用时都要解密使用。若允许回显，我们就必须实现一条"解密后展示"的路径——这条路径一旦存在，**任何一次前端漏洞、任何一次运维误操作都会直接泄露明文**；
- 而"不能回显"的代价很小：贡献者本来就知道自己提交的是什么（那是他的 key）。他需要认出的只是"哪一条"，标签足够。

**推论**：凭据标签不可编辑成包含明文的形式时也要注意——标签是自由文本，用户可能把 key 粘进去。UI 与入库校验都要拦住明显的密钥形态。

## 4. `UNIQUE(provider, external_id)` 硬约束

总纲 4.2：**撞身份必须走合并或拒绝，不得静默覆盖。**

### 4.1 为什么必须唯一

`(provider, external_id)` 是"同一个上游账号"的唯一坐标。如果允许重复行：

| 后果 | 说明 |
|---|---|
| OAuth 登录歧义 | 反查"这个外部身份属于哪个账户"会返回多行，登录逻辑必须瞎选一个——等于**谁都可以登录别人的账户** |
| 回报归错人 | 同一个账号的调用量被拆到两条凭据上，贡献统计与回报都会算少 |
| 鉴定结论分裂 | 同一个账号被鉴定两次，可能得到不同的 `verify_status` |

### 4.2 三种处置

| 情形 | 处置 | 为什么 |
|---|---|---|
| 同一账户重复提交同一身份 | **幂等接受**：更新标签、刷新密钥材料，不新建行 | 这是最常见的情况（用户换了个 key 再提交一次），拒绝会让他困惑 |
| 不同账户提交同一身份 | **合并或拒绝**，由规则决定，且必须在 UI 上说清 | 账号合并涉及"两个站内账户变一个"，是不可逆操作（虽有 `account_merges.reversible_until`，但用户感知上不可逆） |
| 身份缺失（纯 Key 型） | 不参与本约束 | 纯 Key 型没有 `external_id`，无法撞身份——这也是它必须靠恢复码的原因 |

**为什么"不得静默覆盖"**：静默覆盖意味着**直接把别人的凭据改写成了你的**。这既是数据丢失，也是一条提权路径（我提交一个别人的 `external_id`，就把那条凭据抢过来了）。

### 4.3 合并语义

总纲 2.6 / 第 10 章的 `account_merges`：

```sql
account_merges(source_id, target_id, merged_at, reversible_until)
-- 类取并集；accounts.status 可置 merged
```

| 项 | 说明 |
|---|---|
| 类取并集 | 源账户的 `admin` / `tenant` / `contributor` 全部并入目标账户 |
| 凭据迁移 | 源账户名下的 `upstream_credentials.owner_account_id` 改指目标账户 |
| 留痕 | `account_merges` 记录 from/to 与时间；`reversible_until` 之前可回滚 |
| 审计 | 合并是管理操作，必须进 `audit_log`（总纲 9.5 的"管理操作"） |

**为什么合并要留可回滚窗口**：合并是**由人做出的、关于身份的判断**，而人的判断会错。回滚窗口把"错判"从"灾难"降级为"一次修复"。

## 5. 生命周期与撤销

### 5.1 `status` 与 `verify_status` 正交

两个维度**不能混用**（[../models/05-verification.md](../models/05-verification.md) 5.4）：

| 字段 | 回答的问题 | 取值 |
|---|---|---|
| `status` | 这条凭据现在**能不能被调用** | 见 5.2 |
| `verify_status` | 它声称的模型名**是否相符** | `verified` / `suspect` / `unverified` / `trusted` |

### 5.2 建议的 `status` 取值

| 取值 | 含义 | 可否进候选 |
|---|---|---|
| `pending` | 已提交，L0/L1 未走完 | 否 |
| `active` | 流程通过，可被调用 | 是 |
| `degraded` | 可用但健康差（连续失败 / 复检降权） | 是，但排序靠后 |
| `revoked` | 已撤销（贡献者主动 / 管理员 / 上游封禁） | 否 |

**为什么用状态机而不是布尔 `enabled`**：`pending` 与 `revoked` 都需要"不可调用"，但它们的**原因与恢复方式完全不同**。布尔字段会强迫所有展示与运维逻辑去猜"为什么它是 false"，最终会以某种隐式的第二字段补回来。

**为什么撤销用 `revoked` 而不是删除**：删除会破坏 `requests.resolved_credential_id` 的归因链（总纲 10 章：`requests` 含"抽象→真实的解析链，用于贡献者归因"）。历史必须可解释：一年后有人问"那天的流量走了谁的上游"，答案不能是一条悬空外键。

### 5.3 撤销的三种来源

| 来源 | 触发 | 语义 |
|---|---|---|
| 贡献者本人 | 在贡献者端撤销 | "我不想再贡献了"——凭据立即移出候选 |
| 管理员 | 违规 / 滥用 / 上游风险 | 需要记录理由（`audit_log.detail`） |
| 上游 | 封禁 / key 失效 / 401 持续 | 由健康状态自动推进，**不可逆**（除非贡献者重新提交密钥） |

**为什么"贡献者主动撤销"也必须立即生效**：这是对贡献者的一项承诺——他给出的是自己的账号，随时可以收回。承诺不成立，就不该指望有人贡献。

> **待定**：`revoked` 是否可逆（贡献者重新提交同一身份时是新建行还是复活原行）。复活保留统计连续性与历史，但会让"已撤销"这个状态变得不那么确定。
> **待定**：撤销后 `login_enabled` 是否一并置 false（撤销即断登录路径），以及这是否会锁死"只剩这一条贡献"的匿名账户。

## 6. 加密与主密钥

总纲 9.4：

| 项 | 规则 |
|---|---|
| 上游凭据落库 | **AES-GCM**（`upstream_secrets.ciphertext` + `nonce`） |
| 主密钥 | 来自**环境变量**或 `0600` 文件，**永不入库** |
| 比较 | `crypto/subtle.ConstantTimeCompare` |
| 明文回显 | **永不**（连贡献者本人也不行） |

### 6.1 为什么是 AES-GCM

上游凭据是**双向需要**的机密：我们不能只存 hash（那样就用不了它去调上游）。所以必须可解密，问题就变成"怎么存可解密的秘密"。

AES-GCM 是认证加密（AEAD）：它同时给机密性与完整性。**完整性在这里有实际意义**——如果攻击者能改写密文，把我们的流量引到一个他控制的 endpoint 上去，机密性再好也没用。GCM 让篡改在解密时直接失败，而不是解出一段垃圾再被送进路由器。

### 6.2 为什么主密钥不入库

数据库是**最容易被整体拿走的东西**：一次备份泄露、一次 SQL 注入、一次误传的 `app.db`。如果主密钥也在库里，加密就退化成了"把明文换个写法"，唯一的防线变成"攻击者不会同时读到两列"——这不是设计，是运气。

把主密钥放在**环境变量或 `0600` 文件**里，就把"拿到数据库"和"拿到密钥"拆成了两个独立的攻击面。

### 6.3 为什么用 `ConstantTimeCompare`

密钥比较若用普通 `==`，比较耗时与"前多少个字符相同"相关，攻击者可以逐字节爆破。这对 **API Key 鉴权**（入站）是致命的；对**上游凭据**，我们通常不比较它（它是拿去用的，不是拿来验的），但一旦出现任何"用字符串比对来判断凭据"的代码路径，就必须走常量时间比较。

**一条工程约束**：本系统里凡是"用户提供的秘密与服务端的秘密比较"的地方，一律用 `ConstantTimeCompare`，没有例外——因为例外需要论证"这里不会泄露时序"，而那种论证通常是错的。

### 6.4 解密的使用边界

```
需要调用上游 → 取 upstream_secrets.ciphertext → 主密钥解密 → 用于 HTTP 请求 → 不留副本
```

- 解密后的明文**只在内存中存活于一次调用期间**，不进日志、不进 `requests.detail`、不进错误信息；
- **错误信息要脱敏**：上游返回的 401 正文可能回显 key 前缀，进日志前必须过滤（总纲 9.5：默认不记录请求正文）。

> **待定**：主密钥轮换（re-key）流程。轮换需要"用旧密钥解、用新密钥加"的全量重写，且必须在线完成；是否进 MVP 未定。
> **待定**：`upstream_secrets` 与 `upstream_credentials` 的引用形式（外键 id / `secret_ref` 字符串）。

## 7. 贡献者回报机制（未定）

总纲 4.5：**这是一个真问题**——`contributor` 类**没有调用能力**，所以贡献换回来的不是"自己能用的额度"。

总纲给出的三个候选方案：

| # | 方案 | 说明 |
|---|---|---|
| 1 | **折算成额度挂着** | 但自己用不了，需另加 `tenant` 类才生效 |
| 2 | **只有统计** | 贡献量、被调用次数、消耗 token，无实物回报 |
| 3 | **作为升级凭据** | 贡献到一定量可换取 `tenant` 类的优先审批 |

> **待定**：选哪一种，或者组合。总纲原话："这一条不定，贡献侧的产品逻辑就是不完整的。"

### 7.1 三个方案各自的代价（不定结论，只列清取舍）

| 方案 | 优势 | 代价 |
|---|---|---|
| 1 折算额度 | 回报**可量化**，最接近"贡献得到东西"的直觉 | 需要引入"贡献额度"这一新账目；且它与 `tenant` 的额度体系如何打通、能否叠加，都要另行定义——**最容易催生新概念** |
| 2 只有统计 | 实现最轻，且统计是方案 1、3 的共同底座（`requests` 表已经记了解析链） | 回报是"名誉性"的，对只想换额度的贡献者没有吸引力 |
| 3 升级凭据 | 与现有权限类模型天然契合，不引入新账目 | 需要确定"多少贡献算够"，且 `tenant` 类目前要审核（总纲 2.7）——"优先审批"意味着对 `contributor` 打开了审核的快速通道 |

**无论选哪个，统计是必须先做的**：`requests` 表已经记录了 `resolved_credential_id`（总纲 10 章），按凭据聚合即可得到贡献量、被调用次数、消耗 token。也就是说**方案 2 是任何组合方案的必要底座**，可以先行落地。

## 8. `login_enabled`

`login_enabled` 决定该凭据能否作为**登录身份**（[01-flow.md](01-flow.md) 第 4 节）。

| 贡献物 | `login_enabled` | 为什么 |
|---|---|---|
| 供应商账号（OAuth 型） | `true` | 有可验证的外部身份，可反查账户 |
| 纯 Key 型 | `false` | 没有可识别的账号身份，无法据此定位"这是谁的账户" |
| 已撤销 | `false`（建议） | 撤销即断路径，见第 5.3 节待定 |

**为什么要有这个字段而不是靠推导**：`provider` 是否支持 OAuth、该凭据是否本人授权、是否已撤销——这些状态的组合无法从其它字段无歧义地推导出来。一个显式字段让"能否登录"变成一条**可审计的规则结果**，而不是一段隐式判断。

## 9. 审计与对账

总纲 9.5：审计日志 append-only，**凭据的增删**在列；每条审计带 `account_id + acting_class (+ elevated)`。

| 事件 | 是否审计 |
|---|---|
| 凭据新增 / 修改标签 | 是 |
| 凭据撤销（三种来源各自区分） | 是，含理由 |
| 合并 / 回滚合并 | 是 |
| 鉴定判定与复检 | 是（[../models/05-verification.md](../models/05-verification.md) 第 9 节） |
| 密钥解密失败 / 密文被篡改 | 是（属于安全事件） |

**对账**（总纲 11.4）：用 `x-free-router-request-id` 头 + 本地 `requests` 表双向对账，按时间窗 + 模型 + token 数与 providers-hub 的 `gateway` 通道记录对齐。**贡献者归因只走本地 `requests` 表**，不依赖上游记账——上游记的是它自己的通道，看不到"哪个站内 Key 用了哪条贡献凭据"。

## 10. 接口签名

```
-- 凭据
CreateCredibility(owner AccountID, provider, externalID, label string, secretRef SecretRef, loginEnabled bool) -> (Credibility, error)
    -- 校验：owner 必须有 contributor 类；UNIQUE(provider, external_id) 冲突 → 幂等/合并/拒绝
UpdateCredibilityLabel(id, label string) -> error
RefreshSecret(id, secretRef SecretRef) -> error        -- 同一身份换密钥材料

-- 生命周期
SetCredibilityStatus(id, status, reason string, actor Actor) -> error
RevokeCredibility(id, by RevokeSource, reason string) -> error
    -- 不删除行：requests.resolved_credential_id 的归因链必须保持可解析

-- 密钥材料
PutSecret(upstreamID, plaintext []byte) -> (SecretRef, error)   -- AES-GCM，主密钥来自进程环境
OpenSecret(secretRef SecretRef) -> (plaintext []byte, error)    -- 仅调用期内存活，禁止入日志

-- 归属查询
CredentialsOf(account AccountID) -> []Credibility         -- 贡献者端：只返回标签与状态
FindByExternalID(provider, externalID string) -> (AccountID, bool)   -- OAuth 登录反查
```

## 11. 待定项汇总

> **待定**：`upstream_credentials` 与 `upstreams` 是否 1:1，见第 2.1 节。

> **待定**：`revoked` 是否可逆（复活原行还是新建行），见第 5.3 节。

> **待定**：撤销是否一并置 `login_enabled = false`，见第 5.3 与第 8 节。

> **待定**：主密钥轮换流程是否进 MVP，见第 6.4 节。

> **待定**：`upstream_secrets` 的引用形式（外键 id / `secret_ref` 字符串），见第 6.4 节。

> **待定**：贡献者回报机制选哪一种或哪种组合（总纲 4.5 的三个候选），见第 7 节。
