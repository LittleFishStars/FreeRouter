> 分册：架构 · 依赖：[01-overview.md](01-overview.md) · 对应总纲：[PLAN.md](../PLAN.md) 第 6 章

# 架构图（模型调用层级 · 权限架构）

本册只有两张图，是全库的**图形总索引**。文字定义分别在
[01-overview.md](01-overview.md)（分层与进程）、[../security/01-accounts.md](../security/01-accounts.md)
（账户与权限类）、[../security/03-groups.md](../security/03-groups.md)（组）、
[../models/02-abstract.md](../models/02-abstract.md)（抽象模型）、
[03-upstream.md](03-upstream.md)（上游）。**图与文字冲突时以文字为准**，图只负责建立整体印象。

---

## 1. 模型调用层级图

这张图回答一个问题：**一次请求从进来打到上游，经过哪几层，每层各决定什么。**

```mermaid
flowchart TB
    classDef keyfill fill:#1e3a8a,stroke:#93c5fd,color:#eff6ff
    classDef accfill fill:#14532d,stroke:#86efac,color:#f0fdf4
    classDef modfill fill:#7c2d12,stroke:#fdba74,color:#fff7ed
    classDef upfill fill:#3b0764,stroke:#d8b4fe,color:#faf5ff
    classDef note fill:#111827,stroke:#6b7280,color:#9ca3af,stroke-dasharray:4 3

    subgraph K["① API Key 层 —— 只是凭证"]
        direction LR
        K1["key 1<br/>模型白名单（只能收窄）"]:::keyfill
        K2["key 2"]:::keyfill
        K3["key 3"]:::keyfill
        K4["key N"]:::keyfill
    end

    subgraph A["② 账户层 —— 身份 · 权限类 · 全部额度"]
        direction LR
        A1["account A<br/>classes: admin + tenant<br/>额度 / 速率 / 用量"]:::accfill
        A2["account B<br/>classes: tenant<br/>额度 / 速率 / 用量"]:::accfill
        A3["account C<br/>classes: contributor<br/>不持有任何额度"]:::accfill
    end

    subgraph M["③ 抽象模型层 —— 对外唯一面孔"]
        direction LR
        M1["抽象模型 smart"]:::modfill
        M2["抽象模型 fast"]:::modfill
        M3["抽象模型 vision"]:::modfill
    end

    subgraph S["④ 上游层 —— 三类供给，地位等价"]
        direction LR
        S1["直连 API key<br/>贡献者凭据 / 平台自有"]:::upfill
        S2["dsh 账号池<br/>providers-hub adapter 直连"]:::upfill
        S3["本地 / 自建端点"]:::upfill
    end

    K1 --> A1
    K2 --> A1
    K3 --> A2
    K4 --> A2

    A1 -->|"组策略：可见性取并集 · 额度取最严 · 组池可叠加"| M1
    A2 --> M2
    A3 -. "contributor 类不调用模型" .-> M3

    M1 -->|"策略引擎：过滤 → 排序 → 切换"| S1
    M1 --> S2
    M2 --> S2
    M3 --> S3

    S1 -. "凭据归属贡献者账户" .-> A3
    S2 -. "凭据归属贡献者账户" .-> A3
```

![模型调用层级图：API Key → 账户 → 抽象模型 → 上游](images/model-call-layers.png)

### 1.1 读图要点

| 观察 | 含义 |
|---|---|
| **Key 没有自己的额度** | Key 只是「凭证 + 可收窄的模型白名单」。日/月 token、Credits、并发全都挂在**账户**上，这个账户的所有 Key 共享同一份额度。这样"多把 Key 等于偷偷放大额度"这条路根本不存在 |
| **Key 层与模型层不嵌套** | 图中 `① → ②` 是身份汇聚，真正的路由是 `② → ③ → ④`。Key 的两个产物（`account_id`、白名单）只是**过滤器**进入策略引擎 |
| **账户层是分叉点** | 同一账户既可能"调模型"（`tenant` 类）、又可能"只供凭据"（`contributor` 类）、还可能"只管理"（`admin` 类）。一条账户可挂多个类，所以图上同一节点向右有多条去向 |
| **额度只跟 tenant 类走** | `contributor` 不调模型，所以不持有额度；`admin` 只管理，也不消耗额度（管理员要调模型靠同一个账户挂的 `tenant` 类） |
| **模型层是唯一对外面孔** | 客户端只会看到 `smart` / `fast` 这类抽象模型名；真实模型名与渠道不出现在任何客户端可见字段 |
| **上游三类地位等价** | 直连 API key、dsh 账号池、本地端点都实现同一个 `Upstream` 接口，差异只在"凭据从哪来"与"执行体是什么"。路由与协议转换**不区分**来源 |
| **虚线是归属关系不是数据流** | `S1/S2 -.-> A3` 表示凭据所有权，不是请求路径。请求走 `② → ③ → ④`，凭据**不可被调用方指定** |

### 1.2 四个问题的归属（谁决定什么）

| 问题 | 由谁决定 | 不由谁决定 |
|---|---|---|
| 这个请求能不能进来 | L1 鉴权 + 限速 + 账户额度 | 策略引擎 |
| 用哪个抽象模型 | 入站请求里的 `model` 字段 + 弃用链解析 | Key、上游 |
| 抽象模型怎么落到真实模型 | 策略引擎（过滤 → 排序） | L1、上游 |
| 具体用哪条凭据 | 运行时展开候选时选择 | 调用方、贡献者本人 |
| 中途失败了换不换 | `call` 状态机（**已吐可见内容则不换**） | 策略引擎 |

### 1.3 排序策略（内置四种）

| 策略 | 按什么排 | 数据来源 |
|---|---|---|
| `expiry-first` | 最快作废的额度先用 | 各渠道的到期时间探测 |
| `balance` | 余额最少的先用 | 余额查询 |
| `speed` | 最近延迟最低的先用 | 滚动统计 |
| `price` | Credits 折算最便宜的先用 | 内置价格表 × 模型 |

默认链 `[expiry-first, balance]`：先用快过期的，同到期时间再比余额。理由——额度到期作废是真实损失，
余额少说明这个渠道本来就快用完了，两者都指向"先把它用掉"。`speed` / `price` 是可选的首位策略，
管理员按渠道特性自己换。

---

## 2. 权限架构图

这张图回答一个问题：**谁能做什么、看不到什么、隔离发生在哪里。**

```mermaid
flowchart LR
    classDef adm fill:#7f1d1d,stroke:#fca5a5,color:#fef2f2
    classDef ten fill:#14532d,stroke:#86efac,color:#f0fdf4
    classDef con fill:#1e3a8a,stroke:#93c5fd,color:#eff6ff
    classDef res fill:#1f2937,stroke:#9ca3af,color:#e5e7eb
    classDef note fill:#111827,stroke:#6b7280,color:#9ca3af,stroke-dasharray:4 3

    ACC["账户 account<br/>id + display_name + status<br/>登录一次 = 该类全部权限"]:::res

    subgraph CLS["权限类 classes（同一账户可并挂多个）"]
        direction TB
        ADM["admin<br/>只管理，不调用模型"]:::adm
        TEN["tenant<br/>只调用模型，不持凭据"]:::ten
        CON["contributor<br/>只持凭据，不调用模型"]:::con
    end

    ACC --> CLS

    GRP["组 group<br/>管理员批量控制单元"]:::res
    GRP -. "放开类取并集 / 限制类取最严" .-> TEN

    subgraph RES["资源（按类分表归属，互不可见）"]
        direction TB
        R1["api_keys<br/>需 tenant 类"]:::res
        R2["upstream_credentials + secrets<br/>需 contributor 类"]:::res
        R3["sessions / audit_log<br/>按 account_id + acting_class 分账"]:::res
    end

    ADM -->|"管理面 /admin<br/>登录即可（会话 24h）"| AMIN["配置：账户 / 组 / 抽象模型 / 上游 / 待处理事项"]:::adm
    TEN --> R1
    R1 --> CALL["出站调用<br/>/v1/chat/completions · /v1/responses · /v1/messages · generateContent"]:::ten
    CON --> R2
    R2 --> POOL["上游凭据池<br/>贡献者看不到明文、也不能指定自己的凭据服务谁"]:::con

    R3 --- AUDIT["审计：谁以哪个类做了什么"]:::note
```

![权限架构图：账户与三个权限类、组、资源分表归属](images/permission-architecture.png)

### 2.1 读图要点

| 观察 | 含义 |
|---|---|
| **账户是唯一主体，权限类是挂件** | 没有"租户账户""贡献者账户"三种实体，只有一个 `accounts` 表；一个人的身份是他挂了哪些类 |
| **三类能力互不授予** | `admin` 不因为能改配置就获得调模型的权力；`tenant` 不因为能调模型就获得持凭据的权力。这是"彻底隔离"的准确含义——隔离发生在**类之间**，不是"人不能有多种身份" |
| **没有类级提权** | 登录一次即获得该账户所有类的全部权限，会话有效期默认 **24 小时**。管理面与调用面共用一次登录，不存在"再验一次"的中间态 |
| **两张资源表不互通** | `api_keys` 与 `upstream_credentials` 归属不同的类，任何一类都读不到另一类的表。管理员在管理面上能看到元数据（谁贡献了什么渠道、健康度），但**没有接口返回凭据明文** |
| **组只作用于 tenant** | 组的策略（模型可见性、每人额度、组共享池、Key 上限）全都约束"调模型"这件事；`contributor` 不调模型，所以组对它无意义 |
| **审计按 `account_id + acting_class` 分账** | 同一账户用 `tenant` 类发起的调用与用 `admin` 类做的配置变更，在审计里是两条不同的记录 |

### 2.2 合并后的权限继承

```mermaid
flowchart LR
    classDef cf fill:#1e3a8a,stroke:#93c5fd,color:#eff6ff
    classDef tf fill:#14532d,stroke:#86efac,color:#f0fdf4
    classDef nf fill:#111827,stroke:#6b7280,color:#9ca3af,stroke-dasharray:4 3

    S["source 账户<br/>classes: contributor"]:::cf -->|"合并（7 天内可撤销）"| T["target 账户<br/>classes: tenant"]:::tf
    T --> R["合并后 classes = 并集<br/>{tenant, contributor}"]:::tf
    R --> X1["凭据迁移到 target<br/>（同 provider 撞身份则拒绝整次合并）"]:::nf
    R --> X2["Key / 组关系迁移<br/>（同名 Key 自动改名）"]:::nf
    R --> X3["额度与用量归属 target<br/>（用量与审计不回滚）"]:::nf
```

![账户合并后的权限继承](images/account-merge.png)

**为什么类取并集而不是取其一**：合并的语义是"这两个身份是同一个人"，而不是"其中一个身份作废"。
作废其中一类会让被合并方的资源变成孤儿（凭据没有归属、Key 没有主人）。
撤销（7 天内）只拆回类归属与资源归属，不动已发生的用量与审计。

---

## 3. 与其它分册的关系

| 想看什么 | 去哪 |
|---|---|
| 分层职责、进程模型、部署形态 | [01-overview.md](01-overview.md) |
| IR 与四协议映射 | [02-ir.md](02-ir.md) |
| 上游三类怎么实现、健康与重试 | [03-upstream.md](03-upstream.md) |
| Node sidecar 的 IPC 契约 | [04-sidecar.md](04-sidecar.md) |
| 账户、权限类、合并 | [../security/01-accounts.md](../security/01-accounts.md)、[../security/02-authn.md](../security/02-authn.md) |
| 组的策略与合并语义 | [../security/03-groups.md](../security/03-groups.md) |
| 授权判定的完整规则 | [../security/04-authz.md](../security/04-authz.md) |
| 抽象模型、策略引擎、弃用链 | [../models/02-abstract.md](../models/02-abstract.md)、[../models/03-strategy.md](../models/03-strategy.md)、[../models/04-deprecation.md](../models/04-deprecation.md) |
