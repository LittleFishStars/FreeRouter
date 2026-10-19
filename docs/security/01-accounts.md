> 分册：权限与安全 · 依赖：[02-authn.md](02-authn.md)、[03-groups.md](03-groups.md)、[04-authz.md](04-authz.md) · 对应总纲：PLAN.md 第 2 章、第 10 章

# 账户与权限类

## 1. 账户模型
**账户 = 显示名 + 账户 ID + 权限类集合**。三者各司其职，不能互相替代：
| 组成 | 字段 | 承担什么 | 为什么必须有 |
|---|---|---|---|
| 显示名 | `display_name` | 人读的称呼 | 审核、审计、管理端点名要有可读对象；**不承担唯一性**，所以重名不是身份冲突 |
| 账户 ID | `id` | 内部唯一标识 | 显示名可改、可重名，外键、审计、会话必须挂在一个永不变的值上 |
| 权限类集合 | `account_classes.account_id → class` | 全部能力判定 | 能力只从"类"推导，不从"这个人是谁"推导——这是隔离成立的前提 |

数据形状（细化 [PLAN.md](../PLAN.md) 第 10 章的 `accounts` / `account_classes`）：

```
account {
  id,                 -- 账户 ID，内部唯一
  display_name,       -- 显示名，非唯一
  username,           -- 登录名（密码登录用）
  password_hash,      -- argon2id；只走 OAuth / 恢复码路径的账户为 NULL
  status,             -- pending / active / disabled / rejected / merged
  recovery_code_hash, -- contributor 无密码时的找回凭据
  created_at, last_login_at, note
}

account_class {
  account_id, class   -- class ∈ { admin, tenant, contributor }
}                     -- 一个账户可有多行
```

**为什么把类拆成独立表，而不是账户上的一个字段**：一个账户可同时挂多个类，单值字段只能表达"其中一个"；而且类的授予与撤销本身要单独进审计，独立行粒度最自然，也让"取并集"（见第 4 节）变成集合运算而不是字符串拼接。

**不变式**（写入时校验）：

1. `classes` **非空**。推导依据是总纲两条创建路径都至少给一类：注册默认给 `tenant`（PLAN 2.7），未登录贡献创建账户只给 `contributor`（PLAN 4.1）。零类账户没有意义，只会在判定链上留下空洞。
2. `admin` **只能由已有 `admin` 类的账户授予**（PLAN 2.7）：系统不自举、管理员导入不隐式产生。
3. 账户**无上级归属、无邀请关系**（PLAN 1.3）：不存在"某个账户属于另一个账户"这条边。

**账户模型选型（2026-10-19 定案：选方案 A）**

| 方案 | 形态 | 结论 |
|---|---|---|
| **A** | 单账户 + 权限类集合 + 类级提权 | **采用** |
| B | 主体 + 多账户（按类各开一个账户，管理身份有完全独立的密码与会话） | 仅在出现第二个管理员时重评 |

- **为什么选 A**：管理员就是**站长本人**，本站不存在"把管理权委托给第三方"的场景，因此不必用物理隔离（两个账户、两套密码）来约束管理员自己；一套身份、一条审计线、一次登录，实现面也更小。
- **A 的唯一缺口与补法**：A 无法把三类的**认证强度**分档——同一套密码与会话同时守着管理面和调用面。这一缺口由**类级提权**补上：进管理面当场再验一次、有效期 15 分钟、危险操作每次重验（PLAN 2.5，见 [02-authn.md](02-authn.md)）。认证强度不再由"分开的账户"提供，而由"分开的验证动作"提供。
- **何时重评 B**：若将来出现**第二个管理员**，或需要把管理权交出、交给别人保管，"管理员账户同时是租户"这个前提就不再成立，届时重评方案 B；重评时第 2 节推论 2（"`admin` 与 `tenant` 是两套账"）与第 3 节的隔离分析需要重做。

## 2. 三个权限类

### 能力矩阵

| 权限类 | 管辖范围 | 需要它才能… | 它**不**提供什么 |
|---|---|---|---|
| `admin` | 全局配置、账户审批、组、抽象模型与弃用、上游目录与鉴定、审计 | 进管理后台（且需提权） | **不提供任何模型调用能力** |
| `tenant` | 自己名下的 API Key、额度、可访问模型、调用日志 | 调模型、建 Key | **不提供凭据能力** |
| `contributor` | 自己名下的上游凭据、贡献记录、鉴定结果 | 贡献账号 / Key、看贡献与鉴定结果 | **不提供模型调用能力** |

三条必须写死的推论：

1. **只有 `tenant` 类能调用模型。** `admin` 是纯管理标签，它的存在不会让持有者多出一次模型调用；`contributor` 同理。
   - 为什么：调用模型消耗的是全站额度（上游是真实账号），调用能力必须只有一条入口，否则额度护栏的覆盖面就不再完整。
2. **`admin` 与 `tenant` 是两套账。** 管理员账户默认 `{admin, tenant}`（PLAN 2.2），他既能管理也能用模型，但两次行为分别记在 `admin` 与 `tenant` 两个 `acting_class` 上，走两套额度与两套日志。
   - 为什么：如果管理权限能渗到调用侧，"以管理员身份调模型"就能绕开租户侧的额度与白名单，护栏当场失效。
3. **`contributor` 想用模型必须另加 `tenant` 类。** 贡献能力不顺带带来调用能力。
   - 为什么：否则"贡献一个 key 换无限调用"成立，众包就变成刷额度通道。

### 类的授予与撤销

| 类 | 授予路径 | 撤销后的影响 |
|---|---|---|
| `tenant` | 注册默认（PLAN 2.7）；审核通过时确认；管理员导入；管理员单独授予 | 名下 API Key 立即不可用（见 [05-keys.md](05-keys.md)） |
| `contributor` | **贡献即得**，无需审核（PLAN 4.1）；未登录贡献时随账户创建 | 名下上游凭据不再满足"归属账户带 `contributor` 类"，必须处置（见 [../contributions/02-credentials.md](../contributions/02-credentials.md)） |
| `admin` | 只能由现有 `admin` 类账户授予 | 会话的提权状态立即失效，管理端点不再可达 |

**为什么 `contributor` 不需要审核而 `tenant` 需要**：`tenant` 是**取用**，消耗全站额度；`contributor` 是**供给**，供给越多池子越好。先收进来、再用鉴定与策略引擎筛（PLAN 4.4），比事前审核每一个贡献更划算，也避免审核队列成为供给瓶颈。

## 3. 隔离的准确含义

> **隔离发生在权限类之间，不在人与账户之间。** 一个账户同时拥有 `admin` 与 `contributor` 是合法且常见的，隔离一点没破。

**为什么必须这样定义**：若把隔离理解成"一个人不能有两个类"，那么"管理员附带租户能力"就必须开两个账户，于是同一自然人在系统里有两套身份、两套密码、两套审计线。这既不更安全（两个账户的密码很可能一样），还让"我贡献的东西在哪"变成人的负担。隔离要防的是**能力之间的相互放大**，而不是人的多重身份。

隔离由四个维度共同构成，缺一不可。

### 维度一：能力不互授

| 从 | 到 | 不会发生什么 |
|---|---|---|
| `admin` | `tenant` 侧 | 管理权限不会让租户侧的额度、限速、模型白名单被绕过 |
| `tenant` | `contributor` 侧 | 持有 Key 不会让名下的上游凭据变得可读或可用 |
| `contributor` | `tenant` 侧 | 凭据入池不会给持有者额外的模型调用能力 |

- 为什么：这是隔离的**逻辑层**。只要存在一条"有了 A 就等于有了 B"的路径，A 的失效半径就等于 B 的失效半径。
- 落地方式：判定只读 `acting_class` 对应的一类能力，不做任何跨类推导（见 [04-authz.md](04-authz.md)）。

### 维度二：资源分表归属

| 资源 | 表 | 归属要求 | 谁看得见 |
|---|---|---|---|
| API Key | `api_keys` | 账户**必须**带 `tenant` 类 | 租户端；管理端（审计与处置用） |
| 上游凭据 | `upstream_credentials` | 账户**必须**带 `contributor` 类 | 贡献者端（仅标签与状态）；管理端 |
| 用量 / 日志 | `requests` / `usage_daily` | 按 `account_id` 归属 | 自己的调用日志；管理端可见完整归因链 |

- 为什么：这是隔离的**数据层**。租户页查不到凭据表、贡献页查不到 Key 表，那么即使某一侧出现越权读取，读到的也不是另一侧的东西。
- 可验收表现：只有 `contributor` 类的账户**看不到**「我的 Key」入口；只有 `tenant` 类的账户**无法**创建 Key（PLAN 14 验收 10）。

### 维度三：凭据不可自选

- 贡献者**不能指定自己的请求走自己的凭据**，也**看不到凭据明文**（PLAN 2.3、9.4）。
- 凭据进池后，用哪一条由抽象模型的候选集展开 + 策略引擎（过滤 → 排序 → 切换）决定（PLAN 3.3、4.2）。
- 为什么：这是隔离的**执行层**。若贡献者可以自选凭据，就等于给了他一条"把别人的请求算到自己账上、或把自己的额度优先给自己"的通道；也会让"平台自有凭据与贡献凭据同构"这一简化（`owner_account_id = NULL`）失去意义。

### 维度四：审计分账

- 每条记录带 `account_id + acting_class (+ elevated)`（PLAN 9.5、10）。
- 于是 "alice 以 `admin` 身份改配置" 与 "alice 以 `tenant` 身份调模型" 是两条永不混淆的线。
- 为什么：这是隔离的**追责层**。隔离的价值只有在"出事时能说清是哪一重身份做的"时才成立；若审计只记 `account_id`，一个同时有 `admin` 与 `tenant` 的账户出问题，就无法判断是管理面失守还是 Key 泄露。

### 隔离**不是**什么（反面清单）

| 常见误读 | 正确表述 |
|---|---|
| 一个人不能同时有多个权限类 | 可以，且默认组合 `{admin, tenant}` 就是两个类 |
| `admin` 账户不能调模型 | 能调，走的是同一账户的 `tenant` 类，另记一套账与额度 |
| 贡献者不能看自己贡献的凭据 | 能看**标签与状态**，看不到明文，也不能让请求指定它 |
| 隔离靠"页面不显示入口"实现 | 入口隐藏只是表现；判定在 [04-authz.md](04-authz.md) 的链上，与界面无关 |
| 隔离 = 账户之间不能合并 | 合并是合法路径，合并后**类取并集**，四个维度一条不少 |

## 4. 账户合并

`UNIQUE(provider, external_id)` 是硬约束（PLAN 4.2）：同一个上游账号不能绑两个站点账户。撞身份时只有两条合法出路——**合并**或**拒绝**，**不得静默覆盖**。

| 触发场景 | 说明 |
|---|---|
| 未登录贡献后又注册 | 贡献时创建了只有 `contributor` 类的账户，之后注册得到带 `tenant` 类的账户，同一人两份 |
| 两个账户贡献了同一个上游账号 | 直接撞 `UNIQUE(provider, external_id)` |
| 管理员手工合并 | 审核时发现重复申请 |

**合并语义**：

```
source.classes ∪ target.classes              →  target.classes
source 名下资源（Key / 凭据 / 组关系）        →  改挂 target
source.status                                →  merged
```

| 项 | 处理 | 为什么 |
|---|---|---|
| 权限类 | **取并集** | 合并没有降权的理由：两边的能力都是本人已经获得的 |
| API Key | 改挂 target，继续可用 | Key 是已经发出去的凭据，合并是把人并起来、不是把工具扔掉 |
| 上游凭据 | 改挂 target | 归属校验点是"归属账户带 `contributor` 类"，并集后依然成立 |
| 组关系 | 两边组关系都保留，合并语义照常生效 | 组是策略载体，人的归属变了不等于策略归属变了 |
| 审计 | **不重写历史**，旧记录仍带 `source_id` | 审计 append-only（PLAN 9.5），改历史等于销毁证据 |
| 留痕 | `account_merges(source_id, target_id, merged_at, reversible_until)` | 为可能的撤销留出依据 |

**为什么合并后隔离仍然完整**：四个维度里，**能力不互授**看的是 `acting_class`，合并只是让 `target` 多了几个类；**资源分表归属**看的是"该类有没有该资源"，路径不变；**凭据不可自选**与合并无关；**审计分账**因为 `acting_class` 仍在，历史照旧可读。

**合并撤销窗口（定案：7 天）**：`reversible_until = merged_at + 7 天`。为什么是 7 天而不是无限：
撤销是给「误操作」准备的，而不是给「后悔」准备的。一个只用于误操作的回滚窗口应该
足够长以容纳“合并后第几天才发现不对劲”（一周足够），又足够短以让“撤销”在语义上
仍可解释——超过一周后，用量、审计、组关系已经以 target 为中心产生了新的历史，
再拆回去会把那些历史变成无法归因的孤儿。

**撤销的回滚范围**（只回滚归属与类，**不回滚用量**）：

| 项 | 回滚 | 为什么 |
|---|---|---|
| `classes` | ✅ 拆回各自的类集合 | 类归属是本次操作的直接产物 |
| Key / 凭据 / 组关系 | ✅ 改挂回 `source`（若已产生子资源则一并回挂） | 同上 |
| `source.status` | ✅ `merged` → `active` | |
| `account_merges` | ✅ 写一条 `undone_at`（**不删行**） | 撤销本身也是审计事件 |
| **用量与审计** | ❌ **不回滚** | 已发生的请求确实以 `target` 身份消费了额度、也真的执行了。历史就是历史；把用量搬走会让对账失去可信锚点 |
| `usage_daily` 已聚合的旧行 | ❌ 不改 | 理由同上；新的用量从撤销时刻起回到 `source` |

**改挂冲突的处理（定案：预检 + 分项选择，不一律拒绝整次合并）**：整次拒绝会让
「一把同名 Key」把带有几十个凭据的合并全部卡死，而合并的动机往往是“这个人失联了、
东西都在那边”。具体分层：

| 冲突 | 处理 | 为什么 |
|---|---|---|
| Key 同名 | **自动改名**（追加 `-from-<source_id 短码>`），不阻塞 | 名字不是身份，Key 的身份是它的 secret |
| 同 `provider` 的凭据（`UNIQUE(provider, external_id)` 真撞） | **拒绝整次合并**，报出具体是哪一条 | 这是身份重叠，静默合并会让两个自然人变成一个，不可逆 |
| 同名组关系 | **无冲突**（多对多，允许重复） | |
| 用户名同名 | 目标账户的用户名不变，源账户用户名变成别名或丢弃 | 用户名是登录因子，不能两个都有 |

管理员在合并确认页看到预检结果：🔴 阻塞项（同 provider 凭据）→ 必须先人工处理；
🟡 自动处理项（Key 改名）→ 明示将做什么。**只有🔴 为空时才能点确认**。

## 5. 账户状态机

状态取值来自 PLAN 第 10 章。

| 状态 | 能否登录 | 能否调用模型 | 进入方式 | 为什么需要这个状态 |
|---|---|---|---|---|
| `pending` | 否 | 否 | 自助注册（默认） | 公网注册必须默认不激活（PLAN 9.1），否则注册即白嫖 |
| `active` | 是 | 是（需 `tenant` 类且 Key 合规） | 审核批准（选组 + 配额）；管理员导入直接 `active` | 唯一"正常使用"态，其余状态都只是关卡 |
| `disabled` | 否 | 否 | 管理员禁用（违规 / 异常用量 / 主动申请） | 需要一条**可逆**的止血路径：禁用不是删除，恢复后类与资源都还在 |
| `rejected` | 否 | 否 | 审核拒绝（可填理由） | 与 `disabled` 区分：前者是"从未通过审核"，后者是"曾通过后被停" |
| `merged` | 否 | 否 | 被并入其它账户 | 保留行，避免外键悬挂与审计断裂 |

**迁移表**：

| 从 | 到 | 触发者 | 附加动作 |
|---|---|---|---|
| — | `pending` | 自助注册 | 按 IP 限速；给默认类 |
| — | `active` | **管理员导入** | 单个 / CSV；密码留空则生成随机密码一次性展示 |
| `pending` | `active` | 管理员批准 | **必须选组 + 配额**（PLAN 2.7） |
| `pending` | `rejected` | 管理员拒绝 | 可填理由；理由进审计 |
| `active` | `disabled` | 管理员 | 名下所有 Key **立即** 401（见 [04-authz.md](04-authz.md#5-禁用联动)）；吊销会话 |
| `disabled` | `active` | 管理员 | 不自动恢复提权状态；恢复动作本身进审计 |
| `rejected` | `pending` | 管理员（允许复申时） | 需要显式动作，注册流程不自动复活 |
| 任意（非 `merged`） | `merged` | 管理员 | 类取并集、资源改挂（见第 4 节） |

**为什么 `disabled` 不删除数据**：Key 与凭据都是**第三方可感知的资源**（客户端拿着 Key、贡献者的上游账号还挂在平台上）。删行会让它们悬空，或让历史请求误判为"从未存在"。

**为什么 `rejected` 与 `disabled` 不合并成一个状态**：审核队列、用户提示、复申路径、审计统计口径都不同——前者是"你的申请没通过"，后者是"你的账号被停了"。

## 6. 与其它实体的关系

| 实体 | 关系 | 约束来源 |
|---|---|---|
| `account_classes` | 一对多 | 一个账户可多类，集合非空 |
| `sessions` | 一对多 | `elevated_until` 支撑类级提权（见 [02-authn.md](02-authn.md)） |
| `groups` / `group_members` | 多对多 | 组是批量控制单元，不是归属（见 [03-groups.md](03-groups.md)） |
| `api_keys` | 一对多 | 归属账户必须带 `tenant` 类（见 [05-keys.md](05-keys.md)） |
| `upstream_credentials` | 一对多 | 归属账户必须带 `contributor` 类，且 `UNIQUE(provider, external_id)` |
| `requests` / `usage_daily` | 一对多 | 按 `account_id` 归属，含抽象 → 真实的解析链 |
| `audit_log` | 一对多 | `account_id + acting_class + elevated` |

**账户之间没有任何边**（PLAN 14 反面验收）：没有上级、没有邀请、没有"团队成员"。

## 7. 数据结构与接口签名

```go
// 权限类：闭集合。新增取值必须先进 README 术语表。
type Class string

const (
    ClassAdmin       Class = "admin"
    ClassTenant      Class = "tenant"
    ClassContributor Class = "contributor"
)

type AccountStatus string

const (
    StatusPending  AccountStatus = "pending"
    StatusActive   AccountStatus = "active"
    StatusDisabled AccountStatus = "disabled"
    StatusRejected AccountStatus = "rejected"
    StatusMerged   AccountStatus = "merged"
)

type Account struct {
    ID          string        // 账户 ID
    DisplayName string        // 显示名（非唯一）
    Username    string        // 登录名
    Classes     []Class       // 权限类集合，非空
    Status      AccountStatus
    CreatedAt   time.Time
    LastLoginAt *time.Time
    Note        string
}

// Subject 是判定链上的主体快照 = 账户 + 本次行为以哪个类进行 + 是否处于提权有效期。
// 它不是一个新概念，就是 PLAN 第 10 章里 account_id + acting_class + elevated 的复合。
type Subject struct {
    AccountID   string
    ActingClass Class
    Elevated    bool
}

type AccountService interface {
    // Create 建立账户；classes 非空；Admin 类只允许由持 admin 类的 actor 授予。
    Create(actor Subject, displayName, username string, classes []Class, status AccountStatus) (*Account, error)

    // GrantClass / RevokeClass 各自单独进审计；撤销 tenant 类必须联动吊销名下 Key。
    GrantClass(actor Subject, accountID string, c Class) error
    RevokeClass(actor Subject, accountID string, c Class) error

    // SetStatus 状态迁移；转 disabled 必须联动吊销会话与 Key。
    SetStatus(actor Subject, accountID string, st AccountStatus, reason string) error

    // Merge 类取并集、资源改挂；撞 UNIQUE(provider, external_id) 时拒绝而非覆盖。
    Merge(actor Subject, sourceID, targetID string) error

    Get(accountID string) (*Account, error)
    HasClass(accountID string, c Class) (bool, error)
}
```

## 8. 待定

> **已定（2026-10-19）**：合并撤销窗口 = `merged_at + 7 天`；撤销只回滚类与资源归属，
> **不回滚用量与审计**；改挂冲突预检分项处理（见第 4 节）。

> **待定**：`note` 字段的可见范围（仅管理端，还是账户本人可见）。

## 9. 相关分册

- 登录、会话、提权、OAuth 登录、恢复码 → [02-authn.md](02-authn.md)
- 组与合并语义 → [03-groups.md](03-groups.md)
- 判定链与决策表 → [04-authz.md](04-authz.md)
- Key 生命周期 → [05-keys.md](05-keys.md)
- 审计字段与留存 → [06-audit.md](06-audit.md)
