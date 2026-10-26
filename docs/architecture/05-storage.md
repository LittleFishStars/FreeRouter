> 分册：架构 · 依赖：[01-overview.md](01-overview.md)、[03-upstream.md](03-upstream.md)、[06-runtime.md](06-runtime.md) · 对应总纲：[PLAN.md](../PLAN.md) 第 10 章

# 存储层

本册给出完整的表结构、索引策略、迁移机制、WAL 与连接配置、事务边界，以及备份与恢复。
表结构的**权威定义是迁移链的最终叠加结果**（§3 说明这条关系在 2026-10-26 如何变化）；本册给约定、理由与索引策略。

---

## 1. 为什么是 SQLite

选型已在 PLAN 7 定下（`modernc.org/sqlite` 纯 Go、CGO-free + WAL）。这里补的是**边界**：

| 数据 | 进 DB | 理由 |
|---|---|---|
| 账户、权限类、组、策略 | ✅ | 需要事务与一致性约束 |
| 抽象模型、候选、真实模型目录 | ✅ | 需要跨请求共享读缓存与并发写 |
| 上游、凭据密文、Key（hash） | ✅ | 需要持久与原子更新 |
| 请求记录、日汇总、审计 | ✅ | 对账的**唯一权威来源** |
| 主密钥 | ❌ **永不入库** | PLAN 9：落到同一个库里，等于把锁和钥匙放进同一个抽屉 |
| 凭据明文 | ❌ | 只存 AES-GCM 密文（`upstream_secrets`） |
| 请求正文 / 提示词 | ❌ | PLAN 9：默认不记录 |
| 目录缓存（`GET /v1/models` 的原始响应） | ❌（进程内存） | 可再生、体积大、无对账价值 |
| 上游健康计数（滑动窗口） | ❌（进程内存） | 高频写、进程重启即可丢弃 |

**为什么健康计数不进 DB**：它是**秒级**更新的滑动窗口，写库会让 SQLite 的写通道成为
转发路径上的一个真正的瓶颈（见第 7 节的吞吐估算）。它也不需要持久——重启后从 `healthy`
重新开始是正确的语义。

---

## 2. 全局约定

| 约定 | 取值 | 为什么 |
|---|---|---|
| 时间列 | `INTEGER`，UTC unix 秒 | SQLite 没有原生时间类型；整数比较与索引比字符串高效；避免时区歧义。**所有时间列都是 UTC**，展示层负责转换 |
| 日期列 | `TEXT "YYYY-MM-DD"`（仅 `usage_daily.day`） | 按天聚合的键，文本排序即时间序；见第 9 节的待定项 |
| 主键 | `TEXT NOT NULL PRIMARY KEY`（UUID v4） | 2026-10-26 改造（迁移 0009）。**为什么不再用 `INTEGER PRIMARY KEY`**：旧方案是「库内自增整型 + 对外 `<前缀>_<十进制>` 字符串」两层，接缝由 `internal/idcode` 维护；换 UUID 后对外 id 与库内主键是同一串，那层编解码（桥层 35 处 encode / 54 处 decode、`secret_ref` 的 `CAST(... AS INTEGER)` join）整体消失。代价如实记下：主键索引从顺序写入变随机写入（B-tree 页分裂）+ 索引体积变大，在本地 SQLite 的万级行规模下测不出差异。**为什么必须写 `NOT NULL`**：SQLite 的 `TEXT PRIMARY KEY` 不强制非空，且多行 NULL 在唯一约束里互不冲突——漏设 id 的插入会静默造出永远查不到的幽灵行（`INTEGER PRIMARY KEY` 时代由自增兜住，换 TEXT 后没有这层保护）。id 由 store 在插入前生成（`internal/idgen`），不让调用方生成 |
| 合计哨兵 | `''`（空串） | `account_quotas` / `group_quota_pools` 的 `abstract_model_id` 用空串表示「全模型合计」（旧方案是整型 0）。为什么不用 NULL：这两列是 WITHOUT ROWID 复合主键的成员，`NULL = NULL` 在 SQL 里是 UNKNOWN，会让合计行的 upsert 冲突判定失效、池子被拆成多行；空串是值域内的「合法非值」。Go 侧对位常量 `store.TotalScope` |
| 对外不透明标识 | `TEXT`（`sessions.id` / `api_keys.prefix`） | 需要被外部持有且不可猜测 |
| 纯联结表 | `WITHOUT ROWID` | 复合主键即数据本身，省一层索引 |
| 外键 | 全部显式声明 + 连接级 `PRAGMA foreign_keys=ON` | SQLite 默认**关闭**外键；忘记打开会静默允许悬空引用 |
| 布尔 | `INTEGER 0/1` | SQLite 无布尔类型 |

---

## 3. 完整 DDL

**事实源关系（2026-10-26 起）**：本节不再逐字内联建表语句。UUID 改造需要
「拆掉整型 schema 重建为 TEXT 主键」，而迁移文件一经应用 checksum 就不可改
（§5），于是最终 schema 只能由 `0001 → 0009` 的**叠加结果**定义，一份内联拷贝
必然漂移成假事实。（0001 文件头仍写着"本文件与该节逐字对齐"——那句话已不成立，
但 0001 的字节不能改，所以它留在原地；以本节为准。）

权威定义：**`internal/store/migrations/0009_uuid_schema.sql` 的 CREATE 段**。
它包含 0001~0008 累积后的全部列（`real_models` 的 channel 与 verify_* 三列、
`api_keys` 的 revoked 状态、`accounts` 的 must_change_password、
`abstract_candidates` 的 sort_order、`admin_inbox` 的部分唯一索引
`ux_admin_inbox_open` 都在这里一次建全）。下面按原节号列出对象与只在这里
讲的形状要点；每张表为什么这么设计的理由写在该迁移的注释里。

三处全局形状（其余见 §2 与 0009 文件头）：

- 主键一律 `TEXT NOT NULL PRIMARY KEY`（UUID 文本），id 由 store 生成。
- 合计哨兵是空串：`account_quotas.abstract_model_id` / `group_quota_pools.abstract_model_id`
  （Go 侧 `store.TotalScope`）。
- `upstream_credentials.secret_ref` 存 `upstream_secrets.id` 本身（UUID 文本），
  聚合 join 是纯字符串相等——旧方案的 `CAST(... AS INTEGER)` 与其容错一并消失。

### 3.1 账户与权限类

`accounts` · `account_classes` · `account_merges` · `account_quotas`

`account_classes` 独立成表是因为一个账户可同时持多个权限类；`account_merges`
刻意不级联删除（合并可逆，源账户被删后追溯链断裂就没法撤销）；三份
`moved_*_ids` 存 JSON 数组文本，只在回滚那一刻整体读一次。

### 3.2 会话

`sessions`

`sessions.id` 本来就是 TEXT（不透明随机串），本次只有 `account_id` 换了类型。
`token_hash` 用 BLOB 而非十六进制 TEXT：32 字节 vs 64 字符，索引体积减半。

### 3.3 组与授权

`groups` · `group_members` · `group_models` · `group_policies` ·
`group_quota_pools` · `group_model_quota_caps`

策略用 `policy_key/policy_value` 键值形状（每加一个策略就改表结构 = 每加一个
功能一次迁移，而迁移是风险最高的操作）；共享池刻意不塞进 `group_policies`——
它是状态不是策略，写频高几个数量级，且需要单条条件 `UPDATE`（JSON 列里的数字
不能参与计算）。「不设池 = 没有这一行」由 `pool_limit > 0` 的 CHECK 承载。

### 3.3.1 按模型的每人上限（组侧声明）

`group_model_quota_caps` 与池不同它**不带计数器**：cap 是策略（管理员设了什么，
全组共享一行），消耗必须按人记账（记在 `account_quotas`）。把计数器挂组行上
会把「每人限额」错实现成「共享池再加一维」，两者是完全不同的闸。

### 3.4 抽象模型与目录

`abstract_models` · `real_models` · `abstract_candidates`

`abstract_models.replacement_model_id` 的自环由 CHECK 拦，多跳成环由应用层
在同一事务里做 visited 游走兜底。`abstract_candidates` 是 WITHOUT ROWID 的
声明表：`weight` 已退役（恒 0），声明序看 `sort_order`（§5 的 0008）。

### 3.5 上游与凭据

`upstreams` · `upstream_secrets` · `upstream_credentials`

环形外键（`upstreams.secret_id` ↔ `upstream_secrets.upstream_id`）在 SQLite 里
延迟解析，两个方向都能建表成功。凭据↔上游的唯一一条边是
`secret_ref → upstream_secrets.id → upstream_id`；反方向的 `upstreams.secret_id`
只对「上游自己那把 key」成立，拿它当连接边会把贡献凭据算到别的上游头上。

### 3.6 API Key

`api_keys` · `key_model_allow`

Key 不持有任何额度/速率字段（每把 Key 一份额度等于账户级上限形同虚设）。
只存 `key_hash`（BLOB），明文只在创建时返回一次。

### 3.7 请求记录与计量

`requests` · `usage_daily`

刻意没有 cost 列（不做计价，金额由对账与上游账本比对得出）。`in_tokens` /
`out_tokens` 可空：NULL（上游没报）与 0（上游说零）在额度护栏与对账上语义不同。
`usage_daily` 按 account_id 聚合（额度判定目标是账户）；requests 按 90 天清理后
它是唯一的长期事实。

### 3.8 审计

`audit_log` + `trg_audit_log_no_update` / `trg_audit_log_no_delete`

`account_id` 可空：登录失败这类事件发生在身份确定之前，记成某账户的审计是伪造
归因。`acting_class` 必记：一个账户可能同时是 admin 与 tenant。append-only 由
触发器保障而非靠自觉；保留期清理走 `store.PruneAudit` 的受控通道（导出 → 回读
校验 → 摘触发器 → 单事务删除 → 建回触发器 → 写审计），UUID 改造后该通道的
首尾 id 校验也跟着改成文本比较（`returning`/`ORDER BY ts, id`）。

### 3.9 系统表

`admin_inbox` · `model_multipliers` · `settings`

`related_id` 是多态列（按 `kind` 解释指向哪张表），不加外键。`settings` 与业务
表分开：它是运维可改的运行时开关，混在一起就没法把「备份业务数据」与「备份配置」
分开。

### 3.10 贡献统计（不建表）

贡献者侧没有任何回报，所以不存在回报表/结算流水。要展示的只有贡献统计，而统计是
纯读取——视图 `v_contributor_stats` 的答案永远与事实表同源；落成汇总表就必须
在请求落库的同一事务里维护它，任何一处漏更新都会让贡献者看到的数字与实际请求
永久分叉。聚合走 `ix_requests_credential_ts` 的前导列。

## 4. 索引策略

| 索引 | 服务的查询 | 为什么需要 |
|---|---|---|
| `ux_sessions_token_hash` | 每次管理请求的会话校验 | 热路径，必须唯一索引 |
| `ux_api_keys_key_hash` | 每次模型请求的 Key 校验 | 转发热路径第一跳 |
| `ux_real_models_upstream_model` | 目录同步 upsert | 同步按 `(upstream, model_id)` 匹配 |
| `ix_requests_key_ts` 等 | 用量查询、对账、排障 | 都带时间范围，故索引前导列是**实体 id + ts** |
| `ix_usage_daily_*` | 日报表 | 主键是 `(day, …)`，按 day 查走主键；按账户查走 `ix_usage_daily_account`（按 Key 的明细在 `requests`，不在此表） |
| `ux_upstream_credentials_external` | 去重 | 硬约束，不是优化 |
| `group_quota_pools` 的**隐式**主键索引 | 每次结算的原子扣减、组详情页的 "还剩多少" | 扣减的 `WHERE` 要命中 `(group_id, abstract_model_id, unit, period)` 全列；没有额外索引，因为主键已经正好是这个形状 |

**通用原则：时间序列类索引的末列是 `ts`，不是首列。** 因为所有真实查询都是
"某个实体在某段时间内的记录"。反过来（`ts` 在前）会导致范围扫描在时间维度展开，
再逐个过滤实体——写入当前热区、但查任何单个实体的历史都要扫全表。

**为什么不全表 `ANALYZE`/写一堆覆盖索引**：SQLite 在 WAL 下每次写都要同步更新所有索引。
在请求路径上的表（`requests`/`usage_daily`）多加一个索引就是给每次请求多加一份写放大。
当前索引集合的判据是"**每个索引都对应一个真实存在的查询**"。

---

## 5. 迁移机制

### 5.1 形态

```
migrations/
  0001_init.sql
  0002_add_xxx.sql
  ...
```

- SQL 文件用 `go:embed` 打进二进制（PLAN 7）；
- 文件名 `NNNN_description.sql`，编号连续；
- **只有前滚（forward-only），没有 down 脚本**；
- 启动时在**单个事务**里执行所有未应用的迁移；
- 每条记录写进 `schema_migrations`（含 `checksum`）。

### 5.2 为什么这么设计

| 决策 | 理由 |
|---|---|
| **自写迁移，不用 goose/golang-migrate** | 我们的迁移场景极窄（单库、单进程、单文件、前滚），第三方库带来的是 CLI、方言适配、锁表等我们用不到的东西；而依赖越少，`govulncheck` 干净（PLAN 14 验收项）越容易达到 |
| **前滚，无 down** | down 脚本在生产几乎从不被执行，也就几乎从不被测试——一个"没测过的回滚路径"比没有回滚路径更危险（它让人以为可以回滚）。回滚的正确手段是**备份恢复 + 前滚修复** |
| **`checksum` 校验** | 防止已应用的迁移文件被事后修改。否则不同环境的表结构会因为"谁改了 0003"而不同，而版本号看起来一模一样 |
| **编号连续 + 启动时校验** | 缺号或重号时拒绝启动。两个分支各自新增 `0007` 是并行开发的必然事件，宁可启动失败也不要静默跳过 |
| **单事务** | SQLite 的 DDL 是事务性的。迁移中途失败能整体回滚，不会留下"半升级"的库 |
| **启动时执行** | 避免"忘记跑迁移"这种部署事故；代价是迁移必须在秒级完成（见下） |

**0009 是一个刻意的例外：它重建 schema 而不是增量改表**。UUID 主键改造没法用
`ALTER TABLE` 表达（SQLite 不能改主键列类型），所以 0009 按依赖逆序 DROP 全部
业务表再按 UUID 形状重建——它**不搬数据**。这不违反"前滚 + 保数据"的直觉，因为
配套的 Go 侧守卫 `rejectLegacyIntegerDB` 会在应用 0009 前检查业务表有没有行，
有数据就直接拒绝启动并提示"备份后删库重建"。判据是**有没有行**而不是"id 列是不是
INTEGER"：全新库也会先跑 0001~0008 建出空的整型表再被 0009 拆掉，按列类型检测会
把全新库一起拦死。这条决策的来龙去脉（用户 2026-10-26 明确不做旧库数据迁移）见
仓库内 `tmp/uuid-migration-contract.md`。

**为什么迁移必须在秒级完成**：它在启动路径上。SQLite 的 `ALTER TABLE` 能力有限
（不能改列类型、不能加非空无默认值的列），大表加列在旧版本需要重建表。因此
**加列必须带默认值**，且对 `requests` 这类会变大的表要预先规划（P2 可能需要"离线迁移 +
在线双写"）。

> **待定**：P2 多节点下的迁移协调（谁执行、其它节点怎么等）。当前设计是单进程，
> 若检测到库正在被迁移则拒绝启动。

---

## 6. WAL 与连接配置

| PRAGMA | 值 | 为什么 |
|---|---|---|
| `journal_mode` | `WAL` | 读不阻塞写、写不阻塞读。这是"转发热路径写 `requests` 的同时控制台在读报表"能成立的前提 |
| `synchronous` | `NORMAL` | WAL + `FULL` 会让每次 commit 都 fsync，转发热路径的尾延迟变成磁盘延迟。`NORMAL` 只在 checkpoint 前有极小丢失窗口——而我们的计量数据可以**从上游账本对账补齐**，不值得为此付 fsync 的代价 |
| `foreign_keys` | `ON` | 默认关闭，必须**每连接**打开 |
| `busy_timeout` | `5000` | SQLite 是单写者。没有超时会立刻返回 `SQLITE_BUSY`；5s 让写冲突表现为等待 |
| `wal_autocheckpoint` | `1000`（页） | 约 4 MiB 触发 checkpoint，避免 `-wal` 无限膨胀 |
| `cache_size` | `-65536`（64 MiB） | 负值表示 KiB。目录与策略表不大，热数据可以常驻 |
| `temp_store` | `MEMORY` | 排序/临时表不落盘 |
| `mmap_size` | `268435456`（256 MiB） | 只读连接受益；纯 Go 驱动下收益有限，保守取值 |

### 6.1 为什么 PRAGMA 必须绑到连接上

PRAGMA 大部分是**连接级**的。`database/sql` 的连接池会新建连接，池里新连接不会继承你
在另一条连接上设的 PRAGMA。两个做法：

1. 在 DSN 里带 `_pragma=`（`modernc.org/sqlite` 支持），保证**每条**新连接都带上；
2. 把连接池上限设为 1。

**选定做法 1 + 分池**：

| 池 | 上限 | 用途 | 说明 |
|---|---|---|---|
| `writeDB` | **1** | 所有写 | 单写连接，消除写-写竞争与锁升级抖动 |
| `readDB` | 4（可配） | 只读查询（报表、控制台列表） | `?mode=ro`，见下 |
| 初始化连接 | 1（临时） | 建表 / 迁移 / 备份 | 迁移期间独占 |

**为什么写池上限是 1**：SQLite 在任意时刻只有一个写者。允许 N 条并发写连接不会提升吞吐，
只会把"排队"从应用层搬到 `busy_timeout`，并增加 `SQLITE_BUSY` 的概率。**显式串行化
比让 DB 层随机报错好**——排队是可观测的（队列长度、等待时间），`BUSY` 不是。

**为什么读池用只读模式（`mode=ro`）**：只读连接无法升级为写事务，因此不会意外阻塞
checkpoint。这是"长报表查询把 `-wal` 撑大"这类问题的根治手段。

---

## 7. 并发与事务边界

### 7.1 吞吐估算（为什么 SQLite 够用）

| 事实 | 数值 |
|---|---|
| 单条 SQLite insert（WAL、无 fsync 等待） | 约 0.1–1 ms |
| 单写连接理论上限 | 约 1 000–10 000 写入/秒 |
| 一次模型请求需要的事务数 | **1**（见 7.2） |
| 目标并发 | 数十到数百 QPS |

**结论**：写通道有 10–100 倍余量。**真正的瓶颈不会是这个数据库**——所以本层的设计目标
不是"极致吞吐"，而是"**不把写通道变成转发的同步阻塞点**"。

### 7.2 事务边界

| 操作 | 事务边界 | 为什么 |
|---|---|---|
| Key 校验 | 单条 `SELECT`，无显式事务 | 自动提交；且**必须实时查账户状态**（PLAN 9：账户转 `disabled` 后名下 Key 立即 401），不能靠缓存 |
| 一次模型请求的落库 | 流结束后**一个**事务：`INSERT requests` + `usage_daily` upsert（+ 额度递增 + 组池扣减） | 四段必须原子：否则会出现"有请求记录但日汇总少一笔"或"池子扣了但用量没记"，两种都对不上账 |
| `usage_daily` 累加 | `INSERT … ON CONFLICT(day,account_id,abstract_model_id,real_model_id) DO UPDATE SET requests=requests+?` | 读-改-写会在并发下丢更新；upsert 由 DB 保证原子 |
| 账户额度递增 | 对`account_quotas`的当期合计行（与命中模型的行）各跑单条条件 UPDATE（见 7.4）；不限 = 无行 = 跳过 | 同上；额度与用量同一事务，不会出现"池子扣了但用量没记" |
| 抽象模型/候选/组策略变更 | 单事务（含替代链成环校验） | 校验与写入必须同事务，否则并发下会出现"校验时没环、写下去有环" |
| 账户合并 | 单事务（多个表） | 合并是多表改写的；中途失败留下半个身份是灾难 |
| 组级共享池扣减 | **与请求落库同一个事务**，单条条件 `UPDATE`（见第 3.3 节与 [第 7.4 节](#74-组级共享池的并发扣减与惰性重置)） | 池扣减与用量必须同生同死；而条件 `UPDATE` 把"判定+递增"压进一条语句，避免并发超发 |
| 审计追加 | 与它所属的业务写操作**同事务** | 审计与业务动作必须同时成立或不成立（"动作成功但没记审计"是不可接受的） |

### 7.3 转发热路径上不能做的事

| 禁止 | 理由 |
|---|---|
| 在事务里跨越上游调用 | 持有写事务跨越秒级 IO，会把整个写通道锁死。**这是最容易犯的错，也是危害最大的一个** |
| 请求**开始**时写一行 `requests` | 会让每次请求产生两个事务、两倍写入。代价是进程崩溃时丢失在途请求的记录——而**在途请求没有对账价值**（token 尚未产生），可以接受 |
| 每条 SSE 事件写库 | 同上；事件级信息只在内存里汇总，结束时一次落库 |
| 同步 `VACUUM` / `checkpoint(TRUNCATE)` | 会长时间持锁；必须放到低峰或后台 |

**请求落库的时机（终态由 `defer` 保证）**：正常结束、上游报错、客户端断开、超时、drain——
**所有**路径都必须走到写库那一步。一个"客户端断开导致没记账"的漏洞，在计费产品里就是事故。

### 7.4 组级共享池的并发扣减与惰性重置

池是**全组共享、会被并发争用**的资源，它的正确写法只有一个：**判定与扣减是同一条带条件的 UPDATE**，
以"受影响行数"为成败。

```sql
-- 账户额度扣减（合计行 :mid = 0；按模型限额行 :mid > 0）：0 行 = 不足（拒绝）；1 行 = 成功
UPDATE account_quotas
   SET quota_used = quota_used + :cost, updated_at = :now
 WHERE account_id = :aid AND abstract_model_id = :mid AND unit = :unit AND period = :period
   AND period_anchor = :current_anchor      -- 先由重置语句保证锚点是当期
   AND quota_used + :cost <= quota_limit;

-- 池扣减（与上面同形，目标是组级共享池）：0 行 = 额度不足（拒绝）；1 行 = 成功（放行）
UPDATE group_quota_pools
   SET pool_used = pool_used + :cost, updated_at = :now
 WHERE group_id = :gid AND abstract_model_id = :mid AND unit = :unit AND period = :period
   AND period_anchor = :current_anchor      -- 先由下面的重置语句保证锚点是当期
   AND pool_used + :cost <= pool_limit;
```

**为什么不用"先 SELECT 判定、再 UPDATE 扣减"**：两步之间存在窗口，N 个并发请求会**都**读到
`pool_used = N < limit`，然后各自扣减成功——池子被超发，且**超发的量随并发度线性增长**。
更坑的是它在开发环境永远不出现（并发低），只在真实流量峰值时出现。
条件 UPDATE 把"判定 + 递增"压进一个语句，由 SQLite 的写锁串行化，窗口从根上消失。
这不是性能优化，而是**正确性要求**。

**扣减本身不会超发，但周期总额可能超出 `pool_limit`**——这是"终态一次结算"的必然结果，必须分清这两件事：

| 现象 | 是否可能 | 为什么 |
|---|---|---|
| 同一条 `UPDATE` 内的判定与递增之间超发 | **不可能** | 单写连接把写操作串行化，`WHERE` 与 `SET` 在同一语句里原子生效 |
| 本周期 `pool_used` 最终超过 `pool_limit` | **可能**，上界 = 峰值并发 × 单请求消耗 | 在途请求不占额度（见 [../security/03-groups.md](../security/03-groups.md) 第 4 节"消耗时机"），它们都是先通过判定、后结算 |

**为什么接受后者**：要消灭它只能预扣，而预扣必须为失败/取消设计返还；返还写错的方向是**多扣**
（无故停掉全组），而超额的方向是**少扣**（池子比预期宽松一点）。两害相权，选少扣。
同理，扣减写失败（进程崩溃）也表现为少扣——这与第 7.1 节所述"在途请求没有对账价值"是同一取舍。

**为什么不设一个"超发容忍比例"参数**：添一个可配比例会让实现者以为读-改-写是可接受的近似，
而它根本不是"近似"——在峰值并发下它会稳定地打穿池子。宁可把"超额的真实来源"（在途请求）写清楚。

**重置：惰性重置（lazy reset），不用定时任务。** 扣减前先跑一条幂等的"跨周期归零"语句：

```sql
UPDATE group_quota_pools
   SET pool_used = 0, period_anchor = :current_anchor, updated_at = :now
 WHERE group_id = :gid AND abstract_model_id = :mid AND unit = :unit AND period = :period
   AND period_anchor <> :current_anchor;
-- account_quotas 的惰性重置与池同形（account_id/:aid 替 group_id/:gid，pool_used/pool_limit 替
-- quota_used/quota_limit）；"不限" = 无行 = 根本不参与重置与扣减，无需额外分支

| 方案 | 取舍 |
|---|---|
| **惰性重置**（选定） | 归零发生在"下一次用到它"的瞬间，**幂等且与重启无关**；成本是每个活跃组每周期只多一条命中主键的 UPDATE（而且因为它自带 `WHERE period_anchor <> …`，非跨周期时受影响行数为 0） |
| 定时任务 | 需要一个调度器与"任务没跑成怎么办"的补偿逻辑；漏跑的后果是**全组被凭空卡死一整个周期**（池子明明该归零却没归零），而这类故障发现得晚、影响面大 |
| 读时判定（不落地归零） | 每次读都要判断"这是当期的数吗"，而 `pool_used` 是累加计数，无法用"读时加减"得到当期值 |

**为什么惰性重置必须先于扣减、且两者写在同一个事务里**：分开写会出现"已跨周期但尚未归零"的窗口，
此窗口内的请求会拿上期的 `pool_used` 去比本期的 `pool_limit`，结果要么误拒（上期用满了），
要么误放（上期用得少）。放在同一事务里，外部观察不到这个中间态。

**为什么重置与扣减不能合并成一条 `UPDATE`**：合并写法需要把 `CASE WHEN` 塞进 `SET`
（`SET pool_used = CASE WHEN period_anchor <> :now_anchor THEN :cost ELSE pool_used + :cost END`），
而 `WHERE` 里的额度比较就必须同时考虑两种情形（本期余量 vs 新周期满额），
一条语句里两个分支**无法都写成可走主键的条件**。拆成两条语句，两条都只命中一行，总成本仍然是一次事务。

**为什么不给池子建额外的"累计表"**：`pool_used` 是**闸门口的权威数字**，
而 `usage_daily` 是**报表口径**。两者都从同一次结算写入，但前者可被管理员手工调小
（应急关闸），后者不可。刻意保留这份"不一致"而不用事件溯源去协调它们——
因为我们的需求是"闸门准"而不是"两个数字相等"（见 [第 9 节](#9-待定项) 的待定项）。

---

## 8. 备份与恢复

### 8.1 快照

| 项 | 做法 | 为什么 |
|---|---|---|
| 方式 | `VACUUM INTO '<path>'` | 生成一致快照且**不需要停写**。`cp` 在 WAL 下会拿到不含 `-wal` 的旧数据（不一致快照） |
| 频率 | 每日 1 次（低峰） | 与我们体量的 RPO 匹配 |
| 保留 | 7 份滚动 | 够覆盖"几天后才发现写坏了" |
| 校验 | 对快照跑 `PRAGMA integrity_check` | 备份也要**被验证**，否则它只是"一个文件" |
| 演练 | 定期在临时目录还原 + 抽查询 | 未演练过的备份不算备份 |

### 8.2 主密钥不随备份走（关键约束）

`upstream_secrets` 里是 AES-GCM 密文，主密钥来自环境变量或 `0600` 文件、**永不入库**（PLAN 9）。

**因此：一份只有 DB 的备份恢复后，所有上游凭据都不可解密。** 这不是缺陷，是刻意的属性——
它保证"备份文件泄漏"不等于"凭据泄漏"。代价必须在运维文档里写死：

| 恢复需要的东西 | 从哪里来 | 缺失的后果 |
|---|---|---|
| DB 快照 | 备份目录 | —— |
| 主密钥 | 独立的密钥保管（不与备份同处） | 上游凭据全部作废，必须让贡献者重新提交 |
| `sidecar.hub_path`（随包副本 `hub_path`） | 部署包内 / npm 缓存 | `relay` 全部不可用，只能靠 `vendor` 与 `selfhosted` |

**为什么值得接受"恢复更麻烦"**：反过来（主密钥随备份）意味着**任何拿到备份的人拿到全部
凭据**，而备份会被复制到更多地方（异地、对象存储、开发机）。把两样东西分开放，
是让"一处泄漏不等于全线失守"的唯一手段。

### 8.3 保留策略

| 数据 | 建议保留 | 依据 |
|---|---|---|
| `requests` | 90 天明细 | 排障窗口；更久靠 `usage_daily` |
| `usage_daily` | 永久 | 体积极小（每天 × key × 模型 的行数），是对账的长尾依据 |
| `audit_log` | **默认永久**（`audit_retain_days = 0`）；若设定则 ≥ 1 年 | 安全审计的追溯要求；删除必须走归档通道，见下 |
| `sessions` | 过期即清（后台任务） | 纯垃圾 |
| `upstream_credentials`（`disabled`） | 保留 | 归因历史需要 |

**审计保留与 append-only 的冲突处理（已定案）**：两个触发器保留不动——`UPDATE` 永远禁止，
`DELETE` 也不开放给任何业务代码；保留期清理走一条**独立的受控通道**：

1. 默认 `store.audit_retain_days = 0`（**永久保留**）。append-only 的默认语义与安全审计的
   追溯需求一致，删数据应当是需要显式打开的动作，而不是出厂默认。
2. 管理员显式设定 `> 0` 时，清理任务必须按以下顺序执行，缺一步不得删除：
   **导出**目标区间的行到 NDJSON 归档文件 → **回读校验**行数与首尾 `id` 与源库一致 →
   `DROP TRIGGER trg_audit_log_no_delete` → 单事务内 `DELETE ... WHERE ts < :cutoff` →
   **重建**触发器 → 写一条 `system.audit_pruned` 审计（`account_id = NULL`，带归档文件名
   与删除行数）。
3. 任一步失败则回滚并保留原数据，任务下次重试。

**为什么不改成“允许删除但记一条审计”**：那条删除审计本身也会被下一次清理删掉，
“谁删了审计”这个问题最终无解。归档文件在数据库之外，才是不被同一条链路消灭的证据。

> 因此 `ops/02-deploy.md` 的出厂默认已同步改为 `0`，与本节一致。

---

## 9. 待定项

> **待定**：`usage_daily.day` 的时区。当前用 **UTC** 日界。但用户看"今日用量"时用的是
> 本地时间（`Asia/Shanghai`），UTC 日界会让"今天"从早上 8 点开始。改为本地日界会影响
> 历史数据的可比性，需要一次明确的迁移决策。

> **待定**：`requests` 与 `audit_log` 是否需要分区/归档表。SQLite 单表在十万到百万行级别
> 仍可用，但"保留 90 天明细"在大流量下会产生可观的表大小与 `VACUUM` 成本。

> **待定**：`verify_detail` 之外是否要独立的历史鉴定表（见 3.5）。

> **待定**：`group_policies` 的 `policy_key` 是否需要一张"策略定义"元数据表来约束取值形状。
> 当前靠应用层校验。

> **待定**：`pool_used` 与 `usage_daily` 是否需要一次定期的对账任务（前者是闸门口径，后者是报表口径，
> 两者因"管理员手工重置/调小上限"而**允许**不一致）。倾向**不做自动对账**，只做一条"偏差超过阈值就告警"的只读巡检——
> 自动回写会把管理员的人工干预抹掉，反而掩盖真实操作。需与 [../ops/](../ops/) 的巡检项一起拍板。

> **待定**：池的**并发上限**是否要随池一起配（当前池只管总量，瞬时并发由组级并发上限管，见
> [../security/03-groups.md](../security/03-groups.md)）。若实测发现"总额度超发上界 = 峰值并发 × 单请求消耗"过大，
> 可能需要给池加一个"在途预占计数"——但那会把预扣-返还的逻辑重新引进来（见第 7.4 节为何拒绝预扣）。

> **待定**：组级共享池进入 MVP 后，`usage_daily` 是否还需要一张**按组聚合**的视图/表来回答
> "这个组今天用了多少"。可行做法是现查（`usage_daily` 不带 `group_id`，需要 join `group_members`，
> 且用户换组后历史会跟着新组算），或另建 `usage_daily_by_group`（多一份写放大，但口径稳定）。
> 当前池子的权威数字是 `group_quota_pools.pool_used`，所以**报表口径**与**闸门口径**可以不一致，
> 需要 lead 拍板以哪个为准。

> **待定**：`group_quota_pools` 的惰性重置与 `usage_daily.day`（UTC）是否共用同一日界。
> 当前设计是共用，但两者都是"用户看到的今天"与"系统判定的今天"不一致的来源。

