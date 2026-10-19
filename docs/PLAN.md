# FreeRouter 方案（v4）

> AI 中转站，对标 [Tingly Box](https://github.com/tingly-dev/tingly-box)，
> 上游复用 [iJetLi/dsh-providers-hub](https://github.com/iJetLi/dsh-providers-hub)
> 的订阅额度池，并向任何人开放**众包贡献**上游凭据。
>
> **已定决策**：账户制（权限类可组合 · 注册需审核 / 管理员导入）·
> 不做智能应用 / 团队 / 远程 · 只对外暴露**抽象模型**
>
> 状态：待你过目，未开工。
>
> v4 相对 v3 的变化：**账户模型重做**（由"角色二选一"改为"权限类集合"）、
> 新增**组**、**抽象模型与策略路由**、**弃用与替代链**、**贡献者与凭据归属**、
> **模型鉴定**；并**推翻 v3 的"路由退一层"**（见 1.4）。

---

## 0. 定位

FreeRouter 是一个**面向多用户的 AI 中转站**：把 dsh-providers-hub 的自有订阅额度，
加上**众包贡献**的上游凭据，变成一个统一的对外出口。

三种权限类**彻底分立**，一件事只有一类能力能做：

```
        供给                管控                     使用
┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐
│ providers-hub    │  │   FreeRouter     │  │   API Key 持有者  │
│ 自有账号池        │─▶│  协议转换         │─▶│   （租户）        │
│ + 贡献者众包凭据   │  │  访问控制/计量    │  │                  │
└──────────────────┘  └──────────────────┘  └──────────────────┘
```

**只做三件事**：协议转换、访问控制、可观测。
**不做**：客户端接入管理、团队协作、聊天机器人。

**一句话**：providers-hub 管「额度从哪来」，FreeRouter 管「额度怎么发出去」。

---

## 1. 范围边界：明确不做

### 1.1 砍掉 Tingly Box 的三块

| 模块 | 它实际是什么（证据） | 处置 |
|---|---|---|
| **智能应用** | `frontend/src/i18n/locales/zh/agent.ts` 的 `"title": "智能应用"`——**面向 agent 客户端的接入向导 + 逐客户端监控台**（把 Claude Code / Codex / OpenCode 逐个登记，显示已接入 / 最近请求 / 错误率 / 用量 / 日志） | **不做**。我们的观测维度是 **API Key** 与**抽象模型**，不是"已接入的客户端" |
| **团队** | README `Team - Shared Model Deployment`：共享端点 + 集中管控 + *"Each member gets a dedicated Team Key"* + `Dashboard → Team usage` | **不做**。用**组**替代（组的语义完全不同，见 2.6） |
| **远程** | `remote.ts`：IM Bot 配置（TG / 微信 / 企业微信 / 飞书 / Lark / 钉钉），字段含 `bashAllowlist`（允许 `/bash` 执行 `cd/ls/pwd`）、`chatIdLock`、`proxyUrl` | **不做**，整个模块从路线图删除（v1 曾列在 P2，现在连 P2 都不留） |

### 1.2 「团队」不做，但要做组

v3 曾把治理层整个砍掉（"只有一堆 Key，没有人的概念"），v4 由需求方重新定义：
**要组，不要团队**。两者不是一回事：

| | 不做：团队（Tingly Box 的 Team） | 做：组 |
|---|---|---|
| 性质 | 组织实体，成员归属团队 | **管理员的批量控制单元** |
| 端点 | 每团队一个共享端点 | 全站一个端点，按 Key 区分 |
| 可见性 | 成员知道同团队 | 成员**不知道**彼此同组 |
| 凭据 | 团队级共享 | 组**不持有**任何 Key 或凭据 |
| 汇总 | Team usage（团队维度） | 管理员视角的组用量汇总（运营用） |
| 邀请 | 有 | 无 |

组本质是**一组策略 + 一批成员**，是 v3「权限组」的自然扩展——v3 的 `perm_groups`
不再单独存在，合并进 `groups`。

### 1.3 顺带砍掉的复杂度

| 砍掉 | 曾经的设计 | 现在 |
|---|---|---|
| 团队 / 租户 | `tenants` 表、成员归属、团队级汇总 | 删表。改为扁平的 `accounts`（**无上级归属、无邀请**） |
| 计费 | `cost` 字段（金额） | 删。**订阅额度没有单价**，算不出真钱；只记 token 消耗量 |
| 前端重量 | Vite + React + embed | 已定案（第 7 章）：与 Tingly Box 的 Web UI 同级，不额外引入部署单元 |

### 1.4 v3 的「路由退一层」作废

v3 的判断是：上游已有 `aggregate/*` 共 22 个模型做跨渠道聚合，我们这层只做
可见性 / 覆盖 / 兜底，不做权重、不做多策略。

**v4 推翻。** 理由是两者粒度根本不同：

- 上游 `aggregate/*`：把**同一个真实模型**跨渠道归并——粒度是"一个真实模型"
- 我们要的**抽象模型**：管理员自定义的虚拟模型，可以跨**不同真实模型**，
  按成本 / 速度 / 额度策略排序——粒度是"一类需求"

两者不冲突，各自成立。但有一条必须钉死：

> **上游的 `aggregate/*` 不作为抽象模型的候选来源**（默认排除）。
> 否则「我们的策略」与「上游的策略」叠在一起，出问题无法归因。

聚合在我们这层做，上游只当**真实的、单一渠道的模型**用。

---

## 2. 账户、权限类与组

### 2.1 账户模型

```
account {
  id,                                         -- 账户 ID（内部唯一）
  display_name,                               -- 账户显示名
  classes: { admin, tenant, contributor },    -- 权限类集合，可任意组合
  status, password_hash, ...
}
```

**一个账户 ID 可以同时挂三个权限类。** 这是 v4 的核心修正——权限类的**能力**彼此隔离，
但**同一个人**不必为此开三个账户。

三处此前的表述在模型下的准确含义：

| 需求原话 | 真正含义 |
|---|---|
| 「管理员附带一个同名租户账户」 | 不是两个账户——**同一个账户**同时有 `admin` 与 `tenant` 类，所以同名 |
| 「租户也不能持有上游凭据」 | 不是租户这个人不能有凭据——是 **`tenant` 类不自带凭据能力**，要持凭据得有 `contributor` 类 |
| 「贡献者账户和租户账户可以合并」 | 合并 = 两个账户并成一个、**类取并集**，凭据留在同一账户里，隔离一点没破 |

### 2.2 权限矩阵

| 权限类 | 管辖范围 | 需要它才能… |
|---|---|---|
| `admin` | 全局配置、账户审批、组、抽象模型与弃用、上游目录与鉴定、审计 | 进管理后台 |
| `tenant` | 自己名下的 API Key、额度、可访问模型、调用日志 | **调模型**、建 Key |
| `contributor` | 自己名下的上游凭据、贡献记录、鉴定结果 | 贡献账号 / Key、查看贡献统计 |

- **只有 `tenant` 类能调用模型**——管理员的 `admin` 类不提供任何调用能力。
- **管理员账户默认 `{admin, tenant}`**：所以他既能管理、也能用模型，
  但两件事**走两套账、两套额度**，管理权限不会渗到调用侧。
- **`contributor` 类不提供调用能力**，贡献者想用模型必须另加 `tenant` 类。
- 新账户注册默认只给 `tenant` 类。

### 2.3 「隔离」的准确含义

隔离发生在**权限类之间**，不在人与账户之间：

1. **能力不互授**：有 `admin` 不会让 `tenant` 侧获得管理特权；有 `tenant`
   不会让 `contributor` 侧的凭据获得调用权。
2. **资源分表归属**：API Key 归账户但**要求 `tenant` 类**；上游凭据归账户但
   **要求 `contributor` 类**。两张表互不可见——租户页看不到凭据，贡献页看不到 Key。
3. **凭据不可自选**：贡献进池后由策略路由决定用谁，贡献者**不能指定自己的请求
   走自己的凭据**，也**看不到凭据明文**。
4. **审计分账**：每条记录带 `account_id + acting_class`，这样"alice 以管理员身份
   改配置"与"alice 以租户身份调模型"两条线永不混淆。

### 2.4 登录与界面

一个账户**登录一次**，界面**按 `classes` 渲染**：

- 有 `admin` → 出现管理入口
- 有 `tenant` → 出现「我的 Key / 用量 / 日志」
- 有 `contributor` → 出现「我的贡献」

不需要切换账户。OAuth 登录靠 `UNIQUE(provider, external_id)` 定位账户，
命中后直接看它有哪些类。

### 2.5 类级提权（step-up）

`admin` 类是整个系统最高权限，而管理面是**公网暴露**的。因此：

- 登录后会话默认停在**低权限上下文**；
- 要进管理面或改配置时**当场再验一次**（TOTP 或重输密码）；
- 提权有效期 15 分钟，空闲即失效；
- **危险操作**（删账户、改组、改抽象模型、改弃用）要求**每次**重新验证；
- 审计里带 `elevated=true`，与普通操作分得开。

> **已定案（2026-10-19）：采用方案 A。** 判断依据是管理员就是站长本人，
> 不需要为了委托管理权而做独立身份的物理隔离；方案 A 唯一的缺口——三类身份
> 的认证强度无法分档——由上面的类级提权补上。
>
> 若将来出现第二个管理员、或需要把管理权交给别人保管，再回头评估方案 B
> （主体 + 多账户，管理身份有完全独立的密码与会话）。

### 2.6 组

**一个组 = 一组策略 + 一批成员**（成员必须是带 `tenant` 类的账户）。

组上的策略：

| 策略 | 类型 |
|---|---|
| 可访问的抽象模型 | 放开类 |
| 能否建 Key、Key 数量上限 | 放开类 |
| Key 白名单能否放宽 | 放开类 |
| 每人日 / 月 token 上限 | 限制类 |
| 每人 RPM / TPM / 并发上限 | 限制类 |
| 能否使用某类抽象模型（如 `smart`） | 放开类 |

**合并语义（核心）——一个账户可属于多个组：**

> **放开类取并集，限制类取最严。**

- 模型可见性 = 各组成员资格之**并集**
- 额度 / 速率 = 各组的**最小值**
- 布尔开关 = **deny 优先**（有一组禁止就不允许）

最终可用模型：

```
(∪ 所属组的抽象模型)  ∩  Key 白名单（用户自己收窄）  =  实际可用
```

**其它规则**：

- **默认组**：新账户自动进默认组，保证有最基本可用性。
- **删组不删人**：成员退回默认组。
- 组**不持有** Key、不持有凭据、不提供端点。
- 组名对成员可见，**成员列表不可见**（避免形成组织感）。
- 组变更全部进审计。

**组级额度有两种，可单独用、也可同时用**（2026-10-19 定案）：

| 配置 | 生效后的效果 |
|---|---|
| 只设**每人上限** | 每个成员各自 N 次/天，互不影响 |
| 只设**组级共享池** | 全组共用 N 次/天，先到先得；有人不用，别人就能多用 |
| **两者都设** | 两闸都生效：个人额度用完**只停这个人**；共享池用完全组都停 |

**为什么“两者都设”是交集、而不是取最严**：这两个数是两个不同的资源，不是同一资源的两种表达。若按“限制取最严”合并，“组共 500 次 + 每人 100 次”会被压成“有效上限 100 次”——池子在 5 个人各用满 100 次后就再也不起作用了。交集语义下两个数各管各的，语义不会被吞掉。

### 2.7 准入：注册 / 审核 / 导入

- **注册**：全局开关，默认**开启但需审核**。注册即 `pending`，未审核不能登录。
  按 IP 限速；用户名 / 邮箱做**防枚举**（响应时间恒定、文案不区分）；
  密码 ≥12 字符，argon2id 存储。
- **审核**：管理端队列 → 批准（**选组 + 配额**，账号转 `active`）/ 拒绝（可填理由）。
- **管理员导入账密**：单个添加或 CSV（`用户名,密码[,组][,日额度]`），导入即 `active`；
  密码留空则生成随机密码，一次性展示；导入动作全进审计。
- **贡献即得 `contributor` 类**：无需审批（见第 4 章）。
- **`admin` 类只能由现有管理员授予**。

**初始化向导**：首次启动若库中无任何账户，只监听 `127.0.0.1` 并引导创建管理员
（无默认密码）；管理员创建完成前不开公网。

---

## 3. 模型体系

### 3.1 两层模型

| 层 | 是什么 | 谁看得到 |
|---|---|---|
| **真实模型** | 上游池子里的模型（`deepseek-official/deepseek-flash` 等） | **只有管理员** |
| **抽象模型** | 管理员定义的对外模型（`smart` / `fast` / 自定义） | **只有租户** |

**对外只暴露抽象模型**：

- `/v1/models` 只列抽象模型；
- 客户端直接请求真实模型名 → **404**（不泄露池结构）；
- 组的模型授权对象也是抽象模型；
- **上游 `aggregate/*` 不作为候选来源**（见 1.4）。

### 3.2 抽象模型

```
abstract_model {
  name,                    -- 对外名称
  capabilities,            -- 对外能力声明（见 3.4）
  strategy,                -- 策略链（见 3.3）
  candidates,              -- 候选集：真实模型 × 渠道范围 × 约束（声明式）
  status, replacement, ... -- 弃用相关（见 3.5）
}
```

**候选集是声明式的，不绑定具体凭据**。运行时才展开成"候选 × 可用凭据实例"再排序。

### 3.3 策略引擎（三段式）

```
① 过滤  能力匹配 · 上下文够长 · 额度充足 · 渠道未禁用 · 健康 · 不在冷却
   ↓
② 排序  expiry-first（默认）· cheapest · fastest · weighted · round-robin
        · quality-tier · quota-aware    ← 可组合成排序链
   ↓
③ 切换  候选失败换下一个 —— 仅在「还没吐出任何可见内容」时
```

- **`expiry-first` 是默认**：最快作废的额度优先用（复用 providers-hub 的到期逻辑）。
- **③ 的约束不可放松**：一旦已经向客户端吐出可见内容（文本 / 工具调用），
  就**不能**再换候选——流式串话是 providers-hub 已经踩过的坑。

### 3.4 能力声明：取保守下界

抽象模型对外的能力声明**默认取候选的最小值**：

| 能力 | 默认（严格模式） |
|---|---|
| 上下文窗口 | 候选的 **min** |
| 最大输出 | 候选的 **min** |
| 视觉 / 工具调用 / JSON mode | 候选的**交集** |

管理员可以对单个抽象模型切换到**过滤模式**：能力取并集，路由前把不支持的候选筛掉。

理由：宁可保守声明（客户端少用一点能力），也不要声称支持然后中途失败。

### 3.5 弃用与替代链

**三层状态**：

| 状态 | 接受请求？ | 行为 |
|---|---|---|
| `active` | 是 | 正常路由 |
| `deprecated` | **是** | 正常返回，但内部解析到替代模型，并附弃用信号 |
| `disabled` | 否 | 404 / **410 Gone** |

> **弃用不是关闭。** 有标准背书：RFC 9745 明确规定 *"the act of deprecation does not
> change any behavior of the resource"*，真正的"到点不可用"由 `Sunset` 头表达，
> 且 Sunset 时间**不得早于** Deprecation 时间。

**响应头**（两个头日期格式不同，别写成一样）：

```
Deprecation: @1688169599                        ← RFC 9745，Structured Field Date
Sunset: Sun, 30 Jun 2026 23:59:59 UTC           ← RFC 8594，HTTP-date
Link: </docs/models/smart>; rel="deprecation"   ← 指向迁移文档
```

**替代是链式的**：

```
old → mid              （管理员只配一跳）
mid 后来也弃用 → mid → new
old → mid → new        （old 的配置一个字没改，自动延伸）
```

已有客户端不用改配置，`old` 自己会跟到底。

**两条硬约束**：

1. **环检测**：配置时即时校验（A→B 时若 B 的链上已有 A 则拒绝），运行时用
   visited 集合兜底。
2. **深度上限 5 跳**。

超限或检出环 → **降级为"无替代"**：照样接受请求、照样回显旧名，只是不跟随，
**绝不报错**。深度 >2 跳时告警管理员「迁移链过长，建议收敛」。

**对外暴露链的终点**：

- 响应 `model` 仍回显 `old`（不变）；
- `Link: rel="deprecation"` 必须指向**最终 active 目标**，不能指向中间节点
  （否则客户端迁到 `mid`，过两天还得再迁一次）；
- `Deprecation` / `Sunset` 给 `old` **自己**的时间。

**Sunset 可选**：不设 = 永久兼容壳一直跟随；设了 = 到期自动 `disabled` 返回 410。

**能力约束**：替代目标的**能力必须逐项 ≥ 原模型**（上下文 / 输出上限 / 工具调用 /
视觉四项），否则**禁止保存**这条弃用配置——因为我们对外承诺行为不变。

**实现**：`abstract_models.replacement_model_id` 自引用，纯图遍历；**链不压缩**
（保留"第几手替代"的追溯），运行时缓存解析结果、链变更时失效。

管理面：编辑弃用配置时**实时预览整条链**与最终落点；自环 / 环 / 超深当场报错。

### 3.6 真实模型目录（池内管理）

真实模型的增减**不动对外契约**，属于目录管理：

- 上游 `/v1/models` 连续 N 天未出现 → 自动标 `missing`，提示管理员
  「建议弃用 / 已下线」，管理员一键确认或忽略；
- 已产生的用量与日志**不追溯**，历史里保留旧引用。

### 3.7 对外契约

- 响应 `model` 回显**抽象模型名**；
- 管理端 / 特定组可开调试响应头（如 `x-fr-trace`）看到真实落点；
- **无候选可用返回 503 + 专用错误码**（与"模型不存在"的 404 区分）；
- 用户日志只出现抽象模型名，**真实目标与贡献者归因仅管理端可见**；
- （P1）兼容别名：把 `gpt-4o` 等真实名映射到抽象模型。

**不预置任何抽象模型**（2026-10-19 定案）：系统不会自动建出 `fast` / `smart` 这类名字，全部由管理员手工创建。

**为什么不做预置**：预置的名字会附带上**我们替他决定的语义**（“fast 应该快”、“smart 应该强”），但候选集取决于他自己贡献进来的是哪些渠道——两者对不上时，管理员得先把默认值擦干净才能用。预置只省掉“起个名”这一步，代价是把一组可能误导人的默认值写进产品。

---

## 4. 贡献与凭据

### 4.1 贡献者

**任何人都可以贡献**：

| 贡献物 | 说明 |
|---|---|
| dsh-providers-hub 的**供应商账号** | 走该供应商 OAuth 授权（17 家已由 providers-hub 实现） |
| **API Key** | 任意 OpenAI 兼容端点的 key |

**贡献即生效，无需审核**（账号审核只用于 `tenant` 类）。

- **已登录用户**贡献 → 给其账户加 `contributor` 类；
- **未登录用户**贡献 → 创建账户（仅 `contributor` 类），并**当场给恢复路径**：
  - OAuth 型 → 提示"用任意用过的账号登录即可回来"
  - 纯 Key 型（没有可识别的账号身份）→ 一次性展示**恢复码**

### 4.2 凭据归属与隔离

```sql
upstream_credentials(
  id, owner_account_id,        -- 归属账户（要求该账户有 contributor 类）
  provider, external_id, external_label,
  secret_ref,                  -- 加密引用，不明文
  status, login_enabled,
  created_at, last_used_at,
  UNIQUE(provider, external_id)  -- 一个上游账号只能绑一个站点账户
)
```

- **`UNIQUE(provider, external_id)` 是硬约束**：撞身份必须走合并或拒绝，
  **不得静默覆盖**。
- 凭据**只归属 `contributor` 类**的账户；`tenant` 类账户名下一律没有凭据。
- **平台自有凭据**（我们自己配的官方 key）用 `owner_account_id = NULL` 表达，
  与贡献凭据同构但来源不同。
- 凭据明文**永不回显**，贡献者只能看到标签与状态。

### 4.3 登录身份

| 账户的类 | 密码登录 | 上游 OAuth 登录 |
|---|---|---|
| 有 `admin` | ✅ | ❌ |
| 有 `tenant` | ✅ | ❌ |
| 有 `contributor` | 无密码（只有恢复码） | ✅ 用自己贡献过的任一账号 |

**一个账户有多个类时，取并集**——例如同时有 `tenant` 与 `contributor` 的账户，
既能密码登录，也能用自己贡献过的账号 OAuth 登录。

**安全红线**：上游账号当密码，等于把站内账户的安全性绑在第三方共享号的下限上。
因此**用 OAuth 登录的会话默认只给低权限上下文**，要进管理面必须另外提权。

### 4.4 模型鉴定（防假模型）

> **需求**：检验用户提交的 key 对应的模型与模型名是否匹配（避免假模型），
> 可多种方案共同使用；**对于无鉴定方案的冷门模型默认放行**。

**目标收窄**：不是"这是哪个模型"，而是"**这个 key 提供的模型是否与声称的名字相符**"。

**调研结论**（三个现成方案 + 三条硬教训）：

| 方案 | 做法 | 局限 |
|---|---|---|
| [zing-audit](https://pypi.org/project/zing-audit/) | 九维度黑盒审计，自带 85 个模型档案 / 7 平台 YAML 知识库 | 也有非 chat 面（embed / rerank / image / audio） |
| [lm-detector / lmfpd](https://github.com/Ikaleio/lm-detector/) | 让模型凭第一反应写约 300 个随机整数，比对**数值偏好分布**（与语义无关） | 结果是"参考库内的**封闭集合**排序"，库外模型也会得到"最像"的候选 |
| [which-llm](https://arxiv.org/html/2608.29930v1) | 30 个受控文本比对 `usage.prompt_tokens` | 留出集只 6/12 对有效，**token 计数一致只证明"共享分词栈"，不能证明模型血缘** |

**三条硬教训**：

1. token 计数不足以证明模型血缘（同家族不同版本会落到阈值下）；
2. 参考库匹配只是封闭集合排序，**排名分数不是身份证明**；
3. 元数据（`usage`）可以一行伪造 → 权重必须按**伪造抵抗性**分配。

**信号按伪造抵抗性分级**：

| 级别 | 信号 |
|---|---|
| **强** | 数值随机指纹（lmfpd）、能力边界（上下文实测 / 视觉 / 工具调用）、logprobs 分布、已知答案金丝雀 |
| **中** | 知识边界 KBF、计费一致性（独立 tokenizer 估算 vs 上报用量） |
| **弱** | usage token 差值、自报身份、回显 `model` 字段、响应头、错误格式 |

**三级流水线**：

```
L0 形式校验   /v1/models 是否含声称 id · 端点黑白名单 · 响应形状
   ↓        （几秒，全部贡献必经）
L1 标准鉴定   数值指纹 + 自报 + 知识截止 + 能力探针 + 计费一致性 + 金丝雀
   ↓        （~1 分钟，决定入池与否）
L2 深度鉴定   上下文二分 + 视觉 + logprobs + 流式真实性 + 隐藏 system prompt
            （抽检 / 可疑时跑）
```

**判定与放行**：

| 情况 | 处置 |
|---|---|
| 库中有参考指纹且达标 | `verified` → 入池 |
| 有指纹但不达标 | `suspect` → **拒绝入池**并给证据 |
| **库中无指纹（冷门模型）** | `unverified` → **默认放行**入池，标「未鉴定」，进复检队列 |
| 官方域名 | `trusted` → 免检 |

**指纹库来源**：① lmfpd 可导出的参考库；② zing 的 85 模型 YAML；
③ **用自己池里的官方渠道采样自建**——**参考样本绝不能取自待检的可疑渠道**。

**防"今天真、下周转假"**：移植 `zing watch` 的**定时复检 + 回归告警**
（得分下滑也告警）。

### 4.5 贡献者无偿

**贡献是无偿的**：不折算额度、不给优先使用权、不给升级凭据，也不拿它换 `tenant` 类的优先审批。

贡献页上展示给用户的**规范句**（全库逐字统一，见 [README.md](README.md) 术语表的「贡献者无偿」）：

> **「贡献是无偿的，我们不提供额度、优先使用权或升级凭据作为回报。」**

**为什么这样定**：`contributor` 类没有调用能力，任何形式的「回报」都必须在别处
再开一个口子——一笔额度账目、一条优先队列、一项审批特权。而这个口子恰恰是最
不该开的：它是唯一能把「贡献」与「使用权」耦合起来的东西，一旦存在，围绕它的
刷量、套利与分配争议就会全部回来。把贡献定位成**互助**之后，整条链路只剩三件事：
凭据进来、被调用、被计量。

**贡献者仍然能看到统计**（纯展示，不是回报）：

- 贡献了几条凭据，每条的健康度与鉴定状态
- 被调用次数、实际消耗的 token
- 最近调用记录——用来自证「我的贡献确实在服务」

**统计怎么来**：不建新表。`requests` 表已经记了 `resolved_credential_id`
（3.7 节的归因链），按凭据聚合即可。理由是统计是纯读取，日均请求量下聚合成本
可接受，而且避免了统计表与事实表不一致的风险。

**必须在贡献页显式声明无偿**，并且是**阻断式确认**（提交前必须看到并确认），
不能只在页脚用小字说明——让贡献者在信息完整的前提下做决定。

---

## 5. 上游：dsh-providers-hub 实测现状

TypeScript，MIT，DSH 插件，本机 `0.2.1-rc.2`（profile `web`，link 安装）。

### 5.1 它提供（全部继承，不重写）

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
4. **聚合路由** `aggregate/*`（我们不作为候选来源，见 1.4）。

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

6. **记账**：`$DSH_HOME/jet-hub/token-ledger.json`，模块级 Symbol
   `GATEWAY_CHANNEL_MARK` 区分 `direct` / `gateway` 双路流量。

### 5.2 它不提供（FreeRouter 的生存空间）

| 缺口 | 说明 |
|---|---|
| 只有 OpenAI 协议 | Anthropic `messages` / Gemini `generateContent` 入站全无 |
| 没有访问控制 | 单一 API Key，无 key 管理、配额、限速、分账 |
| 没有独立控制面 | 配置散在 DSH 的 Jet Hub 面板里 |
| 只绑 `127.0.0.1`，**拒绝** `0.0.0.0` | 设计上就不打算对外 |
| 没有请求日志 / 用量看板 | 只有内部 token-ledger |
| 不能与 DSH 解耦 | 登录、续期、模型目录都挂在 DSH 进程里 |
| **不能多人用** | 没有账户概念 |

### 5.3 上游硬约束（写进适配层）

- 拒绝通配地址 → **唯一出口原则**：上游永走 `127.0.0.1`，对外只在 FreeRouter。
- 请求体上限 **300 MiB**（超限 413 `request_too_large`）；入站设更小上限
  （默认 32 MiB）提前拒绝。
- 空闲 keep-alive **120 秒**后服务端发 FIN → 自管连接池，不复用死连接。
- **实测存在偶发连接失败**：首次带 Bearer 的请求拿到 `http=000`，重试即通。
  → 上游客户端**必须有重试与连接池**，不能裸 `http.Get`。

### 5.4 Tingly Box 的定位

它不是纯"对标对象"：**它已经作为上游渠道接进池子了**（`tingly-box` 前缀 4 个模型）。

- 它在**出站侧**是我们池子里的一个渠道
- 我们在**入站侧**是它同类的出口
- 两者可共存；只借它 `internal/protocol` 的设计思路，**不引代码**（MPL-2.0）

### 5.5 不做代码级复用，改用 Node sidecar

providers-hub 的 `package.json` 只导出 `.` 与 `./client`，深路径 import 会随版本漂。

**只把 4 个 HTTP 端点当契约**（写契约测试锁字段）。但**登录与凭据续期**这件事
必须复用它的实现——17 家供应商的 OAuth 与静默续期不可能重写。

**方案：进程级 Node sidecar**（不是 import）：

- 独立 Node 子进程加载 providers-hub 的 auth / adapter 模块；
- FreeRouter 通过**子进程的 stdin/stdout（stdio + NDJSON）**调用它；
- 三条红线：**① 只读凭据**（不改 Jet Hub 状态）；**② 不碰**
  `$DSH_HOME/jet-hub/state.json`；**③ 上游永走 `127.0.0.1`**。

> 直接 import 会与 DSH 进程内的模块状态冲突，故走子进程隔离。

---

## 6. 架构

```
   客户端 ──▶ ┌────────────────────────────────────┐
  Claude Code │  L1 入站（公网 · TLS 可选）          │
  Cherry Studio│  /v1/chat/completions             │
  Cursor/Cline│  /v1/messages        (Anthropic)    │
  Gemini CLI  │  /v1/responses       (OpenAI)       │
              │  /v1beta/models/…:generateContent   │
              │  ───────────────────────────────    │
              │  鉴权 → 限速 → 配额 → 解析成 IR      │
              └─────────────────┬──────────────────┘
                                ▼
              ┌────────────────────────────────────┐
              │  L2 内核                            │
              │  abstract  抽象模型：解析替代链 →    │
              │            过滤 → 排序 → 切换        │
              │  call      重试 · 超时 · 取消传播    │
              │  meter     token / TTFT / 归因打标   │
              │  store     SQLite(WAL)              │
              └─────────────────┬──────────────────┘
                                ▼
              ┌────────────────────────────────────┐
              │  L3 上游                            │
              │  dshgateway → 127.0.0.1:8326        │
              │  contributed → 贡献者凭据直连        │
              │  连接池 · 重试 · 健康探测            │
              └───────┬────────────────────┬───────┘
                      ▼                    ▼
        ┌──────────────────────┐  ┌──────────────────────┐
        │  dsh-providers-hub   │  │  Node sidecar        │
        │  :8326               │  │  供应商 OAuth / 续期  │
        │  20 前缀 · 116 模型   │  │  （stdio + NDJSON）  │
        │  账号池 · 积分 · 续期  │  └──────────────────────┘
        └──────────────────────┘
                      ▼
            CodeBuddy / CodeArts / TRAE / minimax / 第三方 key …
```

### 6.1 IR 设计要点

- **消息**：`role` + `parts[]`（`text` / `image` / `tool_call` / `tool_result` /
  `thinking`），对齐 Anthropic content block 粒度——它最能表达富内容，
  向下映射到 OpenAI 最简单。
- **流式事件**：`start` / `thinking_delta` / `text_delta` / `tool_call_start|delta|end` /
  `usage` / `error` / `done`。
- **不静默降级**：缺失能力明确 400，绝不悄悄丢图片 / 丢 `tool_use`。
- **ID 稳定**：tool_call 的 `id` 按 SSE `index` 缓存重组，不依赖分片到达顺序。

---

## 7. 技术选型

| 层 | 选择 | 理由 |
|---|---|---|
| 语言 | Go 1.27（本机 `go1.27.2 linux/amd64`） | 单二进制、低内存 |
| 路由 | `github.com/go-chi/chi/v5` | handler 就是标准 `http.Handler`；SSE 直接写 `ResponseWriter` 并 Flush |
| 存储 | `modernc.org/sqlite`（纯 Go，CGO-free）+ WAL | 交叉编译无忧 |
| 迁移 | 自写（`embed` SQL + `schema_migrations`） | 30 行够用 |
| 日志 | 标准库 `log/slog` | JSON 结构化，零依赖 |
| 限速 | `golang.org/x/time/rate` | 需要**三维**（全局 / 按 Key / 按 IP）令牌桶，且要能在 Key 配置变更后重建桶；`rate.Limiter` 可 `SetLimit` 原地调参，不必丢弃历史令牌计数；自写桶的并发正确性收益不抵风险 |
| 密码哈希 | `golang.org/x/crypto/argon2` | 唯一同时满足**可调内存硬度**（argon2id 的 `memory` 参数）与**无 CGO** 的内存硬密码哈希；bcrypt 的 cost 只能调迭代不能调内存，防 GPU 能力弱 |
| 加密 | `crypto/aes` GCM | 上游 secret 落库需要**认证加密**（改一个比特就要解不开，而不是解出垃圾）；GCM 是标准库自带、无需第三方依赖的 AEAD |
| TLS | 内置可选：标准库 `crypto/tls` 加载 cert/key（不依赖反代） | 默认关闭；由于暂无域名，不引入 `autocert` |
| 前端 | **Vite + React 19 + TypeScript + Tailwind + shadcn/ui**，产物用 `go:embed` 打进二进制 | 见 7.1 |
| 前端数据层 | TanStack Query（缓存与失效）+ React Router | 管理台表单密集，手写 fetch 会重复实现缓存 |
| 依赖代理 | **必须设 `GOPROXY=https://goproxy.cn,direct`** | 本机实测 `proxy.golang.org` 不通 |

> **前端选型（2026-10-19 定案：Vite + React，`go:embed` 单二进制）**
>
> 四条候选路线：A 纯 Go 模板 + htmx / **B Vite + React + embed（选它）** / C 前后端分离 / D Next.js 静态导出。
>
> **为什么选 B**：
> 1. **管理台的真实复杂度不支持极简前端**。抽象模型候选集编辑（多行、排序、权重）、
>    策略编排、弃用替代链预览、审计检索分页、鉴定队列逐条展开——这不是“十几个简单表单”。
>    轻量方案省下的构建成本会在实现阶段加倍还回来。
> 2. **部署形态与 A 完全一致**：一个二进制 + 一个配置 + 一个数据目录。
> 3. **A 想省掉的 Node 在生产环境根本省不掉**：sidecar 必须跑 Node（17 家供应商的
>    OAuth 与续期只有 `dsh-providers-hub` 的实现能复用，用 Go 重写是另一个量级的工程）。
>    A 的价值只剩“省掉构建期 Node”这一半。
>
> **为什么不是 C（前后端分离）**：部署单元从 1 个变 2 个，与“先本地跑、直接 IP+端口”
> 的决策正面冲突；Cookie 认证跨域要配 `SameSite=None; Secure`，而当前默认是明文 HTTP，
> 这套组合在明文下走不通；且它强制先解决域名（当前未提供）。
>
> **为什么不是 D（Next.js 静态导出）**：`output: 'export'` 下 `_next/` 资源路径、
> `basePath`/`assetPrefix`、`public/` 与字体都要走 embed 映射，动态路由受限；
> 而它换来的 SSR 能力对一个内网管理台用不上。
>
> **为什么不是 A（纯 Go 模板 + htmx）**：见上面第 1 条。如果将来管理台确实简化到
> 只剩简单表单，可重新评估。
>
> **连带约定**：Vite 产物（`dist/`）用 `go:embed` 嵌入，SPA fallback 由 Go 侧处理
> （非 `/api` 与 `/v1` 前缀的 404 回退到 `index.html`）；开发期用 Vite dev server
> 反代到 Go，或 Go 侧代理到 Vite（二者任选，见 `ops/02-deploy.md`）。
### 7.1 目录结构

```
FreeRouter/
├── cmd/freerouter/main.go        # 配置 → 装配 → 启 HTTP
├── internal/
│   ├── config/                   # YAML + 环境变量覆盖 + 校验
│   ├── ir/                       # 消息与流式事件类型
│   ├── protocol/{openaichat,openairesponses,anthropic,gemini}/
│   ├── abstract/                 # 抽象模型：替代链、过滤、排序、切换
│   ├── catalog/                  # 真实模型目录与健康
│   ├── meter/                    # token / 延迟 / 归因打标
│   ├── store/                    # SQLite + 迁移 + DAO
│   ├── upstream/dshgateway/      # 8326 客户端（连接池 / 重试 / 契约测试）
│   ├── upstream/contributed/     # 贡献凭据直连
│   ├── sidecar/                  # Node sidecar 客户端（IPC）
│   ├── verify/                   # 模型鉴定流水线
│   ├── httpapi/                  # 入站：鉴权、限速、配额、SSE
│   ├── admin/                    # 管理面 API
│   ├── webui/                    # SPA fallback + 静态资源缓存头（embed 由 web 包提供）
│   └── security/                 # 密钥、限速器、审计、安全头、提权
├── sidecar/                      # Node 子进程（读 providers-hub auth）
├── web/                          # 控制台源码（Vite + React + TS），pnpm build 产出 dist/
│   ├── dist/                     # 构建产物，由同目录 embed.go 的 go:embed 引用
│   └── embed.go                  # package web：//go:embed dist（go:embed 不能跨目录）
├── migrations/
├── deploy/                       # Dockerfile / compose / Caddyfile / systemd
└── docs/PLAN.md
```

---

## 8. 功能清单

### MVP

**账户与权限**
- [ ] 初始化向导（无账户时只监听 `127.0.0.1`）
- [ ] 账户 = 显示名 + ID + 权限类集合（`admin` / `tenant` / `contributor`）
- [ ] 注册（`pending`）→ 审核（选组 + 配额）→ 登录；管理员导入账密（单个 / CSV）
- [ ] 会话：HttpOnly、登录限速、登出、自助改密、管理员重置、改密/禁用即吊销
- [ ] **类级提权**：进管理面当场再验，15 分钟有效
- [ ] 组：成员管理、模型授权、限额（**每人上限 + 组级共享池，可单用可并用**）、合并语义（并集 / 最严 / deny 优先）、默认组

**模型体系**
- [ ] 抽象模型：CRUD、候选集声明、能力声明（保守下界）
- [ ] 策略引擎三段式：过滤 → 排序（`expiry-first` 默认）→ 切换（未吐内容前）
- [ ] 弃用：三态、替代链、环检测、深度上限、RFC 9745/8594 响应头、管理面链预览
- [ ] 真实模型目录：自动发现、分组、置顶、改名、隐藏、`missing` 巡检
- [ ] `/v1/models` 按 Key 可见性过滤（只出抽象模型）

**协议与上游**
- [ ] 入站 `POST /v1/chat/completions`（流式 + 非流式 + tool_calls + 图片）
- [ ] 入站 `POST /v1/messages`（Anthropic，含 `tool_use` / `thinking` / SSE 事件名对齐）
- [ ] 上游 `dshgateway`：key 自举、目录发现与缓存、连接池、重试、离线降级

**贡献**
- [ ] 贡献页：供应商 OAuth 贡献 + API Key 贡献（未登录也可）
- [ ] `contributor` 类授予、恢复码 / OAuth 找回
- [ ] **模型鉴定 L0 + L1**（`verified` / `suspect` / `unverified` / `trusted` **四态**，冷门默认放行）

**Key 与观测**
- [ ] API Key：归属账户（需 `tenant` 类），创建 / 禁用 / 过期 / 额度 / RPM·TPM /
     模型白名单；数量与额度受组约束
- [ ] 请求日志：端点、抽象模型、**真实落点（仅管理端）**、状态、TTFT、耗时、token、错误码
- [ ] 控制台：公共页（登录 / 注册 / 待审核 / 贡献）+ 管理端 + 租户端 + 贡献者端
- [ ] 安全基线（第 9 章）——**默认回环监听下，公网专属项可延到 M10，但不得删**；
      具体哪些项可延、哪些无论如何必须有，见 [ops/01-security.md](ops/01-security.md) 第 8 节
- [ ] 部署：本地直连（默认）+ systemd unit + Docker Compose（反代模板可选）

### P1

- [ ] 入站 `POST /v1/responses`、`POST /v1/generateContent`
- [ ] **模型鉴定 L2**（上下文二分 / 视觉 / logprobs / 流式真实性）+ 定时复检与回归告警
- [ ] 兼容别名（`gpt-4o` → 抽象模型）
- [ ] 配额：日 / 月 token 上限、全局预算熔断、异常用量告警
- [ ] 上游健康：主动探活、失败冷却、半开恢复
- [ ] 贡献统计（见 4.5；纯展示，无回报）
- [ ] 邮箱验证与找回密码（需 SMTP）、注册开关 / 邀请码、审核通知
- [ ] 上下文压缩，可开关

### P2

- [ ] 多节点：SQLite → Postgres + Redis 共享限速与配额
- [ ] 语义缓存（按抽象模型维度开关）

> **已从路线图移除**：智能应用、团队协作、IM 远程控制。

---

## 9. 公网安全设计

> 公网形态下这一章不是可选项，是 MVP 的一部分。

### 9.1 威胁模型（最要紧的一条）

**上游额度是真实的账号。** 谁拿到 Key 谁就白嫖，账记在账号主头上。
原则：**默认拒绝、显式授权、额度护栏先行**。

1. **账户默认不激活**（注册即 `pending`）；新建 Key **默认额度 0、模型白名单为空**。
2. 每个 Key 强制日 / 月 token 上限与并发上限，可设全局默认兜底。
3. 全局预算熔断：全站当日超标自动拒绝并告警。
4. 异常用量检测：单 Key 单位时间用量突增 → 告警（可选自动暂停）。

### 9.2 传输与边界

| 项 | 措施 |
|---|---|
| TLS | **默认直连 IP + 端口（HTTP）**，反代可选、由部署者自行配置；内置可选 TLS（配 cert/key 即启用）。非回环监听且未启用 TLS 时**打印醒目启动警告**，并提供开关升级为拒绝启动 |
| 端口 | 默认只监听 `127.0.0.1`；要暴露公网必须**显式**改绑定地址（不做自动探测） |
| 可信代理 | `TRUSTED_PROXIES` 显式配置。**不配则忽略 `X-Forwarded-For`**，否则 IP 限速被伪造头绕过 |
| 安全头 | HSTS、CSP、`X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options: DENY` |
| CORS | 默认关闭（同源）；跨域需显式白名单，**禁止 `*` + 凭据** |
| 请求体 | 入站默认 32 MiB → 超限 413，提前于上游的 300 MiB 拒绝 |

### 9.3 账户、注册与管理面

- **密码策略** ≥12 字符，argon2id。
- **防枚举**：注册与登录的"已存在 / 不存在"响应时间恒定、文案一致。
- **登录防爆破**：失败计数 + 指数退避 + 锁定，失败记录含 IP / UA 进审计。
- **类级提权**：见 2.5；危险操作每次重验。
- **禁用联动**：账户转 `disabled` → 名下所有 Key **立即** 401
  （鉴权层实时查账户状态，不依赖可能有延迟的缓存）。
- **不提供**管理面 IP 允许列表（2026-10-19 定案）。公网形态下管理员 IP 会变，
  白名单是最容易把自己锁在门外的配置；补偿手段是类级提权 + 登录退避 + 审计。
- **OAuth 登录的会话默认低权限**（见 4.3）。

### 9.4 密钥

- Key 格式 `fr-` + 32 字节 base62；**只存 SHA-256**，明文仅创建时返回一次。
- 列表只显示 `fr-xxxx…xxxx` 前缀；支持即时吊销。
- 上游凭据用 AES-GCM 落库，主密钥来自环境变量或 `0600` 文件，**永不入库**。
- 密钥比较用 `crypto/subtle.ConstantTimeCompare`。
- **凭据明文永不回显**（连贡献者本人也不行）。

### 9.5 限速与审计

- **三维令牌桶**：全局 / 按 Key / 按 IP。超限 `429` + `Retry-After`。
- 审计日志 append-only：管理操作、鉴权失败、额度熔断、上游错误、**提权事件**、
  **组的变更**、**凭据的增删**。
- 每条审计带 `account_id + acting_class (+ elevated)`。
- **默认不记录请求正文**；排查时开脱敏模式（保留结构、抹掉文本）并在 UI 明示。

### 9.6 运行态加固

- 容器非 root，`--read-only` 根文件系统 + 仅数据目录可写，`--cap-drop=ALL`。
- `/healthz`（进程）与 `/readyz`（含上游连通性）分离。
- `slog` JSON 输出 + 日志轮转。CI 跑 `govulncheck`。

---

## 10. 数据模型

SQLite（WAL），编号 SQL 迁移，启动时自动执行。

| 表 | 关键字段 | 说明 |
|---|---|---|
| `accounts` | id, display_name, username, password_hash, status, recovery_code_hash, created_at, last_login_at, note | status: `pending` / `active` / `disabled` / `rejected` / `merged`；**无上级归属** |
| `account_classes` | account_id, class | `admin` / `tenant` / `contributor`，一个账户可多行 |
| `account_merges` | source_id, target_id, merged_at, reversible_until, undone_at | 合并留痕（类取并集）；撤销窗口 **7 天**，撤销不删行、只写 `undone_at` |
| `sessions` | id, account_id, token_hash, created_at, expires_at, elevated_until, ip, ua | 改密 / 禁用即吊销；`elevated_until` 支撑提权 |
| `groups` | id, name, description, is_default, created_at | 组 = 一组策略 + 一批成员 |
| `group_members` | group_id, account_id | 多对多 |
| `group_models` | group_id, abstract_model_id | 组可访问的抽象模型 |
| `group_policies` | group_id, policy_key, policy_value | 放开类取并集 / 限制类取最严 |
| `group_quota_pool` | group_id, unit, limit, used, resets_at | 组级**共享额度池**的当前周期计数；全库**唯一带可变状态的组表** |
| `abstract_models` | id, name, display_name, strategy, capability_mode, status, replacement_model_id, deprecated_at, sunset_at, note | `status`: `active` / `deprecated` / `disabled`；自引用替代链 |
| `abstract_candidates` | abstract_model_id, real_model_id, constraint_json, weight | 候选集（声明式，不绑凭据） |
| `real_models` | id, upstream_id, upstream_model_id, display_name, group_name, context_window, modalities, efforts, status, hidden, sort_order, source, last_seen_at, missing_since | source: `auto` / `override`；status 含 `missing` |
| `upstreams` | id, kind, name, base_url, secret_id, owner_account_id, enabled, last_seen_at | kind: `dshgateway` / `contributed`；**`owner_account_id = NULL` 表示平台自有** |
| `upstream_secrets` | id, upstream_id, ciphertext, nonce | AES-GCM，主密钥不入库 |
| `upstream_credentials` | id, owner_account_id, provider, external_id, external_label, secret_ref, status, login_enabled, verify_status, verify_detail, created_at, last_used_at, **UNIQUE(provider, external_id)** | 归属 `contributor` 类账户 |
| `api_keys` | id, account_id, key_hash, prefix, name, status, expires_at, quota_limit, quota_used, rpm_limit, tpm_limit, daily_token_cap, max_concurrency | **必须归属有 `tenant` 类的账户**；只存 hash |
| `key_model_allow` | key_id, abstract_model_id | Key 白名单（在组之外再收窄） |
| `requests` | id, ts, key_id, account_id, endpoint, virtual_model_id, resolved_real_model_id, resolved_credential_id, status, http_status, error_code, ttft_ms, total_ms, in_tokens, out_tokens | **无 cost**；含抽象→真实的解析链，用于贡献者归因 |
| `usage_daily` | day, key_id, abstract_model_id, real_model_id, requests, in_tokens, out_tokens | 看板聚合 |
| `audit_log` | id, ts, account_id, acting_class, elevated, action, target, ip, ua, result, detail | append-only |
| `settings` | key, value | 全局配置 |

对比 v3：`perm_groups` / `perm_group_models` 并入 `groups` / `group_models`；
`accounts` 去掉 `role` 改为 `account_classes`；新增 `abstract_models` /
`abstract_candidates` / `upstream_credentials` / `account_merges`。
`tenants` 始终没有复活。

---

## 11. 与 dsh-providers-hub 的接线

### 11.1 启动自举

1. 读 `$DSH_HOME/openai-gateway/api-key` → AES-GCM 加密存库，标记 `auto`，
   `owner_account_id = NULL`（平台自有）。
2. `GET /v1/models` 拉目录 → 写 `real_models`（`source=auto`；
   管理员改过的记 `override`，刷新不覆盖）。
3. 端口不通**不阻塞启动**：控制台顶部挂「上游离线，检查 DSH 是否在跑」横幅，
   每 30s 重试。
4. `/healthz` 只反映进程，`/readyz` 反映上游连通性。

### 11.2 真实模型命名（仅管理端可见）

- **限定名**：`dsh:deepseek-official/deepseek-flash`（渠道 + 上游模型 id）
- 上游 `aggregate/*` 默认标记 `excluded_from_candidates = true`

### 11.3 116 个模型怎么摆

直铺等于把渠道构成全暴露。可见性**三层收敛**：

```
组的模型授权（管理员，并集）  ∩  Key 白名单（用户自己收窄）  =  实际可用
```

而租户看到的又**只有抽象模型**——真实模型的渠道构成他根本看不到。

### 11.4 打标与对账

不改 providers-hub 代码。用 `x-free-router-request-id` 头 + 本地 `requests` 表双向对账：
它记它的 `gateway` 通道，我们记 Key 与归因链，按时间窗 + 模型 + token 数对齐。

### 11.5 契约测试（防漂移）

对 8326 的 4 个端点各写一组契约测试，锁住：状态码、鉴权失败形态、`/v1/models`
条目字段、SSE 帧形状、413 行为。**外加一条**：连接偶发失败的复现（`http=000`）
与重试是否成功。providers-hub 升级后先跑契约测试，红了再适配。

---

## 12. 里程碑

| 阶段 | 内容 | 产出 | 工日 |
|---|---|---|---|
| M0 | 骨架与契约锁定：go.mod、chi 起服务、SQLite 迁移、配置、`/healthz`、**8326 契约测试** | `go run ./cmd/freerouter` 能起 | 1 |
| M1 | 上游接入与配额探测：连接池 / 重试、`/v1/models` 摄入、余额查询 | 116 个模型入目录 | 1.5 |
| M2 | 模型目录与鉴定 L0+L1 + 复检队列 | 假模型进不来 | 1.5 |
| M3 | **账户与权限类**：初始化向导、注册→审核、导入、会话、类级提权 | 能把账户发给人用 | 1.5 |
| M4 | 额度与限速：7 维配额、三维限速、额度护栏、请求日志 | 可发给人用不烧穿 | 1.5 |
| M5 | API 出口：`/v1/chat/completions`、`/v1/responses`、**`/v1/messages`**、`/v1/models`、SSE、失败切换 | 客户端接得进来 | 2 |
| M6 | **抽象模型 + 策略引擎 + 弃用替代链** | 对外只有抽象模型 | 2.5 |
| M7 | **组**与授权（含组级共享池） | 可发给一组人 | 1.5 |
| M8 | **贡献与凭据**：贡献页、`contributor` 类、Node sidecar、凭据入池 | 别人能贡献 | 2.0 |
| M9 | 界面完善（管理端 + 租户端 + 贡献者端） | 可运维 | 2.5 |
| M10 | 加固与上线：安全基线、健康探测、契约测试、压测 | 可长期跑 | 1.5 |

共 **19.0 工日**，关键路径（M0→M1→M3→M4→M5→M6→M10）约 **14 天**。
M1 结束即有实用价值；**M6 结束就能对外提供抽象模型**；
M2 与 M8 可并行；M7 必须在 M6 之后（组的授权对象是抽象模型）。

> 本章与 [ops/03-milestones.md](ops/03-milestones.md) 第 1 节**同一切分**（M0–M10、工日、
> 依赖图）。运维分册是权威执行清单，本节只给总纲视角；两者若再出现不一致，
> 以运维分册为准并回头修本节。

**M8 为何由 2.5 下调到 2.0**：贡献者无偿（4.5）后取消了「回报」这一整块——
没有额度折算、没有优先使用权的发放与对账、没有升级凭据的判定，只剩一个
只读的贡献统计视图（[architecture/05-storage.md](architecture/05-storage.md) 3.10）。
M8 不在关键路径上，所以 14 天不变。

**为什么不再写「M8 不可跳过」**：这句话在「先本地跑、默认只监听 `127.0.0.1`」
的定案下**前提不成立**（用户 2026-10-19 拍板）。安全基线仍需做，但它排在
M10 而非插在关键路径中间；真要暴露公网时 M10 必须完成。

---

## 13. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| **公网泄露 Key / 账户被盗 → 白嫖上游额度** | 额度损失 + 上游账号风控 | 账户默认待审核；Key 默认额度 0；日/月上限 + 并发上限；全局熔断；异常告警；一键禁用（联动吊销 Key 与会话） |
| **贡献者提交假模型 / 假 key** | 池子被污染，用户拿到烂模型 | 三级鉴定（L0/L1 必跑）；`suspect` 拒绝入池；冷门模型放行但标「未鉴定」并进复检队列 |
| **贡献的凭据被上游封禁** | 该候选失败 | 候选级熔断 + 冷却 + 自动降权；连续失败暂停并通知贡献者 |
| **贡献者凭据被盗用（我们泄露）** | 极其严重 | 凭据 AES-GCM 落库、明文永不回显、主密钥不入库、最小可见 |
| **抽象模型策略配错 → 全站不可用** | 服务中断 | 保存前**干跑**（用历史请求回放验证候选可达）；无候选时返回 503 + 明确错误码而非 500 |
| **弃用替代链配成环** | 请求死循环 | 配置时环检测 + 运行时 visited 兜底 + 深度上限 5；超限降级为"无替代" |
| **替代模型能力低于原模型** | 客户端中途失败 | 保存时逐项校验能力 ≥，不满足直接拒绝保存 |
| 上游偶发连接失败（实测 `http=000`） | 请求随机失败 | 连接池 + 幂等重试 + 健康探测；计入 `requests` 便于观察频率 |
| providers-hub 升级接口漂移 | 上游断 | 只依赖 4 个 HTTP 端点 + 契约测试锁字段 |
| Node sidecar 与 DSH 抢状态 | 两边都坏 | 子进程隔离 + 只读凭据 + 不碰 `state.json` |
| 上游拒绝通配 / 只绑回环 | 跨机不可用 | 唯一出口原则：上游永走 `127.0.0.1` |
| 300 MiB body / 120s keep-alive | 413、连接重置 | 入站设更小上限并给明确错误；自管连接池 |
| 各家思考档位 / 模态方言不一 | 静默降级或 400 | IR 保留「未知」第三态；缺能力明确报错 |
| SSE 分片乱序 / tool_call id 重组 | 工具调用错乱 | 按 `index` 缓存 id 重组 |
| 反代后 IP 被伪造 | 限速失效 | 反代是可选形态；配了才需 `TRUSTED_PROXIES`，未配则忽略 `X-Forwarded-For` |
| **管理面被盗** | 全站失守 | argon2id + 会话 + 类级提权（危险操作每次重验）+ 登录退避锁定。**不提供 IP 白名单**（管理面不限来源 IP，见 2.5） |
| **管理员账户同时是租户（`admin`+`tenant`）** | 日常账户被盗即失管理面 | 类级提权（危险操作每次重验）；已定案采用方案 A，如需彻底隔离再评估方案 B（2.5） |
| 开放注册被刷号 | 垃圾账户 | 默认需审核 + 注册限速；可选邀请码（P1） |
| 审核积压无人处理 | 用户等不到批准 | 管理端审核队列置顶 + 待办计数；通知（P1） |
| **贡献者无偿导致供给不足** | 没人愿意贡献 | 已定案为无偿（4.5）。缓解靠三件事：贡献页**阻断式声明**无回报（避免预期错位）、控制台给出可验证的贡献统计、平台自带渠道（5.5）保底供给 |
| 借额度自用的合规边界 | 上游账号风控 | README 写明「仅限本人已登录账号、自用」；不内置任何绕过、代理池、指纹伪装 |
| 单点进程 | 服务中断 | systemd `Restart=always` + 优雅关闭 drain；P2 多节点 |

---

## 14. 验收标准（MVP）

1. `curl` 用新 Key 打 `/v1/chat/completions`，`stream:true` 逐帧吐字，`[DONE]` 正常。
2. Claude Code 设 `ANTHROPIC_BASE_URL` 指过来 + 填 Key，完成一次带工具调用的编码任务。
3. 停掉 DSH（8326 关闭）：返回 `502 upstream_unavailable` 而非挂起，`/readyz` 变红。
4. Key 打满 RPM → `429` + 有值 `Retry-After`；打满日额度 → 拒绝且日志留痕。
5. **新建 Key 不改任何配置时无法发起请求**（默认拒绝）。
6. **注册**：新账户 `pending` 时登录被拒且提示待审核；管理员批准并选组后可登录。
7. **导入**：粘贴 3 行 `用户名,密码` → 3 个账户立即可登录，审计里能看到这次导入。
8. **禁用联动**：账户置 `disabled` → 名下所有 Key **立刻** 401。
9. **防枚举**：注册"已存在的名字"与"全新的名字"，响应时间与文案无差异。
10. **权限类隔离**：只有 `tenant` 类的账户**无法**创建 Key；
    只有 `contributor` 类的账户**看不到**「我的 Key」入口；
    `admin` 类账户在未提权时**进不去**管理页。
11. **抽象模型**：请求真实模型名 → 404；`/v1/models` 只出现抽象模型；
    请求抽象模型能在日志里看到管理端可见的真实落点。
12. **策略生效**：把某候选渠道禁用 → 请求自动落到下一个候选，
    且**在已有可见输出后不会切换**（构造一个中途断流的候选验证）。
13. **弃用链**：把 `old` 弃用指向 `mid`，`mid` 再弃用指向 `new`；
    请求 `old` → 响应 `model` 仍为 `old`，`Link: rel="deprecation"` 指向 `new`，
    头部含 `Deprecation` 与 `Sunset`。配成环时保存被拒。
14. **能力校验**：替代目标上下文小于原模型时，保存被拒。
15. **贡献**：未登录状态下 OAuth 贡献一个供应商账号 → 生成 `contributor` 账户
    并拿到恢复路径；该凭据出现在管理端的鉴定队列，鉴定通过后进入候选池。
16. **鉴定**：提交一个冒充模型名的 key → 判为 `suspect` 且**不入池**；
    提交一个库中没有参考的冷门模型 → 判为 `unverified` 但**入池**并带标记。
17. **组合并语义**：账户同属两个组（一个给 3 个模型、一个给 2 个）→ 可见 5 个；
    两组日额度分别为 100 / 200 → 生效 100。
18. **组级共享池**：组设“每人 100 次 + 全组 150 次”，3 个成员各用满 50 次 → 池子耗尽；
    下一个请求被拒，且报的是**共享池已用尽**而不是“你的个人额度已用尽”。
19. 未启用 TLS 且非回环监听时，启动会打印警告；把该策略配成“拒绝启动”后，同样配置下进程不启动；伪造 `X-Forwarded-For` 不能绕过 IP 限速。
20. `docker compose up -d` 后宿主端口等价于第 1 条。
21. `govulncheck ./...` 无高危；容器非 root 运行。

**反面验收（确认没做多）**：控制台没有"团队 / 共享端点 / 邀请成员"入口；
账户之间没有任何归属关系；没有"已接入客户端"引导；没有任何 IM Bot 配置项；
租户界面看不到任何真实模型名。

---

## 15. 待你拍板


按重要度排序。**第 1、2 项已于 2026-10-19 拍板**，但保留编号以免打断其它分册的交叉引用：

1. ~~账户模型选 A 还是 B~~ —— **已定案：采用方案 A**（见 2.5）。M3 已解锁。
2. ~~贡献者回报机制~~ —— **已定案：贡献者无偿**（见 4.5），只保留贡献统计。
3. ~~前端选型~~ —— **已定案：Vite + React + `go:embed` 单二进制**（见 7.1）。否掉纯 Go 模板 / 前后端分离 / Next 静态导出三条路线的理由在同一节。
4. ~~组级共享额度池~~ —— **已定案：两种都做**（每人上限 + 组级共享池，可单独用也可同时用，见 2.6）。
5. ~~是否预置抽象模型名~~ —— **已定案：不预置**，全部由管理员手建（见 3.7）。
6. ~~平台自有凭据~~ —— **已定案：用 `owner_account_id = NULL` 表达**（已在 4、10、11 章作为既定事实使用）。
7. ~~部署位置~~ **先本地跑**；~~反代方案~~ **不用反代，默认 IP + 端口，需要时自行配置**；~~管理面限来源 IP~~ **不限**。**域名仍未定**。

**本章现状**：七项中六项已定案，只剩「域名」一个未定项（不阻塞 M0–M10，
默认形态用 `http://<ip>:<port>`）。原本列在本章的「待拍板」实质已清空，
因此标题仍用「待你拍板」只为保持各分册穿插引用不断裂。

> **还有一项需要你复核**：全库中**由写入者自行拍板、你从未明确表态**的设计选择
> 已全部归集到 [DECISIONS-REVIEW.md](DECISIONS-REVIEW.md)（约 500 条，按影响面
> 分 10 章，每条带 `文件:行号` 与理由）。你只需指出**第几节 / 哪几条不接受**。