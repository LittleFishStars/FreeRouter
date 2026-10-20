# FreeRouter 设计文档

> 本目录是 FreeRouter 的设计文档集。**[PLAN.md](PLAN.md) 是总纲**，
> 其余按主题分册。分册只做**细化**，不改变总纲里已定的决策。
>
> **冲突时的优先级**：用户最新指令 > `PLAN.md` > 分册。

---

## 文档地图

| 分册 | 文件 | 内容 | 状态 |
|---|---|---|---|
| **总纲** | [PLAN.md](PLAN.md) | 定位、范围边界、需求映射、里程碑、验收 | v4 已定（1127 行） |
| **决策复核** | [DECISIONS-REVIEW.md](DECISIONS-REVIEW.md) | 写入者自行拍板、用户未表态的全部设计选择，交用户逐条复核 | 471 行 |
| **待定汇总** | [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) | 按 A/B/C 分级的未决项清单 | 340 行 |
| **上游查证** | [RESEARCH-UPSTREAM-REUSE.md](RESEARCH-UPSTREAM-REUSE.md) | `dsh-providers-hub` 三层复用边界的实测记录 | 111 行 |
| | [architecture/00-diagrams.md](architecture/00-diagrams.md) | 两张总图：模型调用层级图、权限架构图（mermaid） | ✅ 200 行 |
| | [architecture/01-overview.md](architecture/01-overview.md) | 分层、进程模型、部署形态 | ✅ 359 行 |
| | [architecture/02-ir.md](architecture/02-ir.md) | IR 与四协议转换 | ✅ 554 行 |
| | [architecture/03-upstream.md](architecture/03-upstream.md) | 上游适配：relay / vendor / selfhosted | ✅ 558 行 |
| | [architecture/04-sidecar.md](architecture/04-sidecar.md) | Node sidecar 的 IPC 契约、重写为 Go 的成本评估 | ✅ 469 行 |
| | [architecture/05-storage.md](architecture/05-storage.md) | 数据模型、迁移、索引 | ✅ 809 行 |
| | [architecture/06-runtime.md](architecture/06-runtime.md) | 并发、连接池、流式、超时、错误处理 | ✅ 405 行 |
| **权限与安全** | [security/01-accounts.md](security/01-accounts.md) | 账户与权限类模型 | ✅ 355 行 |
| | [security/02-authn.md](security/02-authn.md) | 登录、会话、OAuth 登录 | ✅ 418 行 |
| | [security/03-groups.md](security/03-groups.md) | 组、合并语义、组级共享池 | ✅ 484 行 |
| | [security/04-authz.md](security/04-authz.md) | 授权判定链（决策表） | ✅ 259 行 |
| | [security/05-keys.md](security/05-keys.md) | API Key 与密钥管理 | ✅ 251 行 |
| | [security/06-audit.md](security/06-audit.md) | 审计与可观测 | ✅ 405 行 |
| **模型体系** | [models/01-catalog.md](models/01-catalog.md) | 真实模型目录与健康 | ✅ 319 行 |
| | [models/02-abstract.md](models/02-abstract.md) | 抽象模型定义与能力声明 | ✅ 289 行 |
| | [models/03-strategy.md](models/03-strategy.md) | 策略引擎：过滤 / 排序 / 切换 | ✅ 417 行 |
| | [models/04-deprecation.md](models/04-deprecation.md) | 弃用与替代链 | ✅ 293 行 |
| | [models/05-verification.md](models/05-verification.md) | 模型鉴定流水线 | ✅ 266 行 |
| **贡献与凭据** | [contributions/01-flow.md](contributions/01-flow.md) | 贡献流程（匿名 / 已登录） | ✅ 193 行 |
| | [contributions/02-credentials.md](contributions/02-credentials.md) | 凭据归属、加密、生命周期、无偿与贡献统计 | ✅ 344 行 |
| **界面规划** | [ui/01-ia.md](ui/01-ia.md) | 信息架构与导航 | ✅ 276 行 |
| | [ui/02-public.md](ui/02-public.md) | 公共页：登录 / 注册 / 待审 / 贡献 | ✅ 275 行 |
| | [ui/03-admin.md](ui/03-admin.md) | 管理端页面 | ✅ 595 行 |
| | [ui/04-tenant.md](ui/04-tenant.md) | 租户端页面 | ✅ 237 行 |
| | [ui/05-contributor.md](ui/05-contributor.md) | 贡献者端页面 | ✅ 184 行 |
| | [ui/06-design.md](ui/06-design.md) | 交互约定与组件 | ✅ 262 行 |
| **API 契约** | [api/01-inbound.md](api/01-inbound.md) | 入站 API（OpenAI / Anthropic） | ✅ 445 行 |
| | [api/02-admin.md](api/02-admin.md) | 管理 API | ✅ 539 行 |
| | [api/03-errors.md](api/03-errors.md) | 错误码与响应头 | ✅ 326 行 |
| **运维** | [ops/01-security.md](ops/01-security.md) | 公网安全基线清单 | ✅ 314 行 |
| | [ops/02-deploy.md](ops/02-deploy.md) | 部署、配置项、备份 | ✅ 520 行 |
| | [ops/03-milestones.md](ops/03-milestones.md) | 里程碑拆解与验收清单 | ✅ 507 行 |

---

## 术语表

**以下定义是全文唯一口径。** 分册里不得另起炉灶使用同义词。

| 术语 | 英文 / 标识 | 定义 |
|---|---|---|
| **账户** | `account` | 一个人的登录身份。由**显示名 + 账户 ID + 权限类集合**组成 |
| **权限类** | `class` | 能力标签：`admin` / `tenant` / `contributor`。**一个账户可同时拥有多个** |
| **管理员类** | `admin` | 只能做管理。**不提供任何模型调用能力** |
| **租户类** | `tenant` | 只能调模型、建 Key、看自己的用量。**不提供凭据能力** |
| **贡献者类** | `contributor` | 只能贡献与查看自己的贡献。**不提供模型调用能力** |
| **隔离** | isolation | **权限类之间**的能力不互授、资源分表归属、审计分账。**不是**"人不能有多个类" |
| **组** | `group` | 管理员定义的**批量控制单元** = 一组策略 + 一批成员。**不是**组织实体 |
| **合并语义** | merge semantics | 一个账户属于多个组时：**放开类取并集，限制类取最严**，布尔开关 deny 优先 |
| **组级共享池** | group pool | 组级额度的一种：全组共用 N 次（**按周期**，日或月，由 `period` 指定）。与「每人上限」**可单用、也可并用**；并用时是**交集**（个人额度用完只停这个人，池子用完全组都停），**不是**取最严 |
| **真实模型** | `real_model` | 上游池子里的模型（`dsh:deepseek-official/deepseek-flash`）。**只有管理员可见** |
| **抽象模型** | `abstract_model` | 管理员定义的对外模型（`smart` / `fast`）。**只有租户可见** |
| **候选** | `candidate` | 抽象模型的一个候选来源：真实模型 × 渠道范围 × 约束。**声明式，不绑凭据** |
| **策略引擎** | strategy | 三段式：**过滤 → 排序 → 切换**。排序默认 `expiry-first` |
| **弃用** | `deprecated` | **仍然接受请求**，内部解析到替代模型并附弃用信号。**不是关闭** |
| **替代链** | replacement chain | `old → mid → new`，可多跳。**不设跳数上限**——靠「弃用只能指向 active 模型」+「弃用不可逆」两条规则杜绝环 |
| **禁用** | `disabled` | 不接受请求（404 / 410 Gone） |
| **凭据** | `credential` | 上游访问凭据（供应商账号 OAuth 或 API Key）。**只归属带 `contributor` 类的账户** |
| **平台自有凭据** | `owner_account_id = NULL` | 我们自己配的凭据，不属于任何账户，与贡献凭据同构但来源不同 |
| **API Key** | `api_key` | 租户调模型用的站点密钥。**只归属带 `tenant` 类的账户** |
| ~~**提权**~~ | ~~step-up~~ | **已废除（2026-10-20）**：登录一次即获得该账户所有类的全部权限，不再二次验证。会话有效期 24h。抵抗力改由登录强度承担（argon2id + 退避锁定 + 会话过期） |
| **账户级额度** | account quota | 额度与速率**全部挂在账户上**，一个账户的所有 Key 共享同一份。Key 只做认证 + 白名单收窄 + 生命周期 |
| **计量维度** | quota dimension | 三种：`request`（次数）/ `token` / `credit`（Credits，以 `deepseek-flash` 为基准按价格乘数折算） |
| **价格乘数** | price multiplier | Credits 的换算系数；内置常见模型价格表，**表里没有的默认 1**，管理员可手动调 |
| **待处理事项** | admin inbox | 需要管理员处理的事的汇总队列：注册申请 / 上游模型变动 / 故障通知 / 池与熔断告警。登录时显示未读计数 |
| **二次确认** | confirm dialog | 界面层再拦一道（影响面 + 不可逆程度 + 具体动作按钮）。**这不是认证**，只是防误点——取消提权后它是唯一的额外护栏 |
| **贡献统计** | contributor stats | 贡献者的只读观测面：贡献量 / 被调用次数 / token / 健康度。**不含任何回报**，也不参与分配 |
| **贡献者无偿** | no recompense | 贡献**不折算额度、不给优先使用权、不给升级凭据**，也不拿它换任何审批特权。贡献页必须**阻断式声明**这一点，见 [contributions/02-credentials.md](contributions/02-credentials.md) 第 7 节 |
| **鉴定** | verification | 检验"贡献的凭据声称的模型是否真的匹配"。判定见下 |

**鉴定判定四态**：

| 判定 | 含义 | 是否入池 |
|---|---|---|
| `verified` | 库中有参考指纹且达标 | ✅ |
| `suspect` | 有指纹但不达标 | ❌ 拒绝并给证据 |
| `unverified` | **库中无参考指纹**（冷门模型） | ✅ 放行，标「未鉴定」，进复检队列 |
| `trusted` | 官方域名 | ✅ 免检 |

**三类上游**（用户用中文命名，代码用英文标识符）：

| 中文名 | 标识符 | 含义 |
|---|---|---|
| **反代** | `relay` | 把 providers-hub 的账号池反代出去；凭据在它那里，推理走 sidecar 进程内的 provider adapter |
| **上游** | `vendor` | 贡献者交的、直接连厂商官方端点的 API 凭据 |
| **自建** | `selfhosted` | 管理员自己填的 OpenAI / Anthropic 兼容地址 |

> 不用中文直译当标识符：`upstreams.kind='upstream'` 这种自指像 bug，且 `upstream` 已是 L3 层名
> （`upstream_credentials` / `upstream_secrets`）。详见 [architecture/03-upstream.md](architecture/03-upstream.md) 第 1 节。

---

## 文档约定

1. **文件开头**写一行归属信息：
   `> 分册：架构 · 依赖：[architecture/02-ir.md](architecture/02-ir.md) · 对应总纲：PLAN.md 第 6 章`
2. **章节编号**从 `## 1.` 起，二级标题带编号，三级不带。
3. **待定项**统一写成引用块，不得自行拍板：
   > **待定**：<问题描述>
4. **交叉引用**用相对路径 + 锚点，如 `../security/04-authz.md#判定链`。
5. **每个设计决策必须写"为什么"**，不能只写"是什么"。只写结论的条目视为未完成。
6. **不引入新概念**。需要新概念时，先写进本文档的术语表，再在分册里用。
7. **表格优先于长段落**；流程用编号列表；数据结构用代码块。
8. **不写实现代码**（除数据结构与接口签名）。这是设计文档，不是源码。

---

## 阅读路径

| 你是 | 从哪开始 |
|---|---|
| 想了解这个项目 | [PLAN.md](PLAN.md) → [architecture/01-overview.md](architecture/01-overview.md) |
| 要写后端代码 | [architecture/](architecture/) 全部 → [security/](security/) 全部 → [api/](api/) |
| 要做界面 | [ui/01-ia.md](ui/01-ia.md) → [security/04-authz.md](security/04-authz.md)（决定谁看到什么） |
| 关心模型路由 | [models/](models/) 全部 |
| 关心贡献侧 | [contributions/](contributions/) |
| 要部署 | [ops/01-security.md](ops/01-security.md) → [ops/02-deploy.md](ops/02-deploy.md) |
| 只想看图 | [architecture/00-diagrams.md](architecture/00-diagrams.md) |

---

## 事实源优先级

1. **用户的最新指令** —— 最高
2. **[PLAN.md](PLAN.md)** —— 已定决策与需求映射，分册不得违背
3. **分册** —— 细化实现细节

发现分册与总纲冲突时：**以总纲为准**，并在分册里标注冲突点。