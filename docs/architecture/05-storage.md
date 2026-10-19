> 分册：架构 · 依赖：[01-overview.md](01-overview.md)、[03-upstream.md](03-upstream.md)、[06-runtime.md](06-runtime.md) · 对应总纲：[PLAN.md](../PLAN.md) 第 10 章

# 存储层

本册给出完整的表结构、索引策略、迁移机制、WAL 与连接配置、事务边界，以及备份与恢复。
所有表结构逐字对齐 [PLAN.md](../PLAN.md) 第 10 章的字段清单；本册只做细化。

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
| 主键 | `INTEGER PRIMARY KEY`（rowid 别名） | 写入快、索引小；UUID 主键会造成索引膨胀与随机写。对外**不**直接暴露自增 id（见 `../security/04-authz.md` 的越权枚举风险） |
| 对外不透明标识 | `TEXT`（`sessions.id` / `api_keys.prefix`） | 需要被外部持有且不可猜测 |
| 纯联结表 | `WITHOUT ROWID` | 复合主键即数据本身，省一层索引 |
| 外键 | 全部显式声明 + 连接级 `PRAGMA foreign_keys=ON` | SQLite 默认**关闭**外键；忘记打开会静默允许悬空引用 |
| 布尔 | `INTEGER 0/1` | SQLite 无布尔类型 |

---

## 3. 完整 DDL

### 3.1 账户与权限类

```sql
CREATE TABLE accounts (
    id                 INTEGER PRIMARY KEY,
    display_name       TEXT    NOT NULL,
    username           TEXT    NOT NULL,
    password_hash      TEXT    NOT NULL,       -- argon2id，含参数与盐的自描述串
    status             TEXT    NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','active','disabled','rejected','merged')),
    recovery_code_hash TEXT,                   -- 可空
    created_at         INTEGER NOT NULL,
    last_login_at      INTEGER,
    note               TEXT
);
CREATE UNIQUE INDEX ux_accounts_username ON accounts(username);
CREATE INDEX ix_accounts_status ON accounts(status);

CREATE TABLE account_classes (
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    class      TEXT    NOT NULL CHECK (class IN ('admin','tenant','contributor')),
    PRIMARY KEY (account_id, class)
) WITHOUT ROWID;
CREATE INDEX ix_account_classes_class ON account_classes(class);

CREATE TABLE account_merges (
    id               INTEGER PRIMARY KEY,
    source_id        INTEGER NOT NULL REFERENCES accounts(id),
    target_id        INTEGER NOT NULL REFERENCES accounts(id),
    merged_at        INTEGER NOT NULL,
    reversible_until INTEGER NOT NULL,
    CHECK (source_id <> target_id)
);
CREATE INDEX ix_account_merges_source ON account_merges(source_id);
CREATE INDEX ix_account_merges_target ON account_merges(target_id);
```

**为什么 `account_classes` 是独立表而不是 `accounts.role`**：一个账户可同时拥有多个权限类
（README 术语表）；单列存不了集合。另外 `account_classes` 上的 `CHECK` 是**权限类的唯一权威
枚举**——多一个类就必须改这里，无法悄悄多出来。

**为什么 `account_merges` 不级联删除**：合并是**可逆**的（`reversible_until`）。源账户被删除后
追溯链断裂，"可逆"就无从执行。外键默认 `RESTRICT` 正是我们要的行为。

### 3.2 会话

```sql
CREATE TABLE sessions (
    id             TEXT    PRIMARY KEY,        -- 不透明随机串（128bit base64url）
    account_id     INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash     BLOB    NOT NULL,           -- SHA-256(token)，32 字节
    created_at     INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,
    elevated_until INTEGER,                    -- 提权窗口（15 分钟，README 术语表）
    ip             TEXT,
    ua             TEXT
) WITHOUT ROWID;
CREATE UNIQUE INDEX ux_sessions_token_hash ON sessions(token_hash);
CREATE INDEX ix_sessions_account ON sessions(account_id);
CREATE INDEX ix_sessions_expires ON sessions(expires_at);
```

**为什么 `token_hash` 是 `BLOB` 而不是十六进制 `TEXT`**：32 字节 vs 64 字符，索引体积减半，
且比较无需大小写归一。**为什么它单独建唯一索引**：会话校验是**每个管理请求**都要做的热路径查询，
必须走唯一索引（一次 B 树探测），不能全表扫。

### 3.3 组与授权

```sql
CREATE TABLE groups (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    description TEXT,
    is_default  INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
    created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_groups_name ON groups(name);
CREATE UNIQUE INDEX ux_groups_default ON groups(is_default) WHERE is_default = 1;

CREATE TABLE group_members (
    group_id   INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, account_id)
) WITHOUT ROWID;
CREATE INDEX ix_group_members_account ON group_members(account_id);

CREATE TABLE group_models (
    group_id          INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    abstract_model_id INTEGER NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, abstract_model_id)
) WITHOUT ROWID;
CREATE INDEX ix_group_models_model ON group_models(abstract_model_id);

CREATE TABLE group_policies (
    group_id     INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    policy_key   TEXT    NOT NULL,
    policy_value TEXT    NOT NULL,       -- JSON
    PRIMARY KEY (group_id, policy_key)
) WITHOUT ROWID;
```

**为什么 `ux_groups_default` 是部分唯一索引**：默认组**有且只有一个**（新账户的兜底授权）。
用 `WHERE is_default = 1` 的部分索引让"唯一"只约束默认组那一行，其它组不受影响。

**为什么 `group_policies` 用 `policy_key`/`policy_value` 的键值形状**：策略集合会随功能增长
（限速覆盖、额度覆盖、能力开关……）。每加一个策略就改表结构，会带来"每加一个功能一次迁移"，
而迁移是线上操作中风险最高的一类。**代价**：值无法由 DB 约束校验形状，必须在应用层用
schema 校验——这个代价我们接受，因为策略值本来就是 JSON 文档。

### 3.4 抽象模型与目录

```sql
CREATE TABLE abstract_models (
    id                   INTEGER PRIMARY KEY,
    name                 TEXT    NOT NULL,     -- 对外模型名（fast / balanced / …）
    display_name         TEXT    NOT NULL,
    strategy             TEXT    NOT NULL DEFAULT 'expiry-first',
    capability_mode      TEXT    NOT NULL DEFAULT 'strict'
                         CHECK (capability_mode IN ('strict','filter')),
    status               TEXT    NOT NULL DEFAULT 'active'
                         CHECK (status IN ('active','deprecated','disabled')),
    replacement_model_id INTEGER REFERENCES abstract_models(id),
    deprecated_at        INTEGER,
    sunset_at            INTEGER,
    note                 TEXT,
    CHECK (replacement_model_id IS NULL OR replacement_model_id <> id)
);
CREATE UNIQUE INDEX ux_abstract_models_name ON abstract_models(name);
CREATE INDEX ix_abstract_models_status ON abstract_models(status);

CREATE TABLE abstract_candidates (
    abstract_model_id INTEGER NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    real_model_id     INTEGER NOT NULL REFERENCES real_models(id)     ON DELETE CASCADE,
    constraint_json   TEXT,                    -- 候选级约束（渠道范围等）
    weight            INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (abstract_model_id, real_model_id)
) WITHOUT ROWID;
CREATE INDEX ix_abstract_candidates_real ON abstract_candidates(real_model_id);

CREATE TABLE real_models (
    id                      INTEGER PRIMARY KEY,
    upstream_id             INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    upstream_model_id       TEXT    NOT NULL,
    display_name            TEXT    NOT NULL,
    group_name              TEXT,              -- 上游给的分组/前缀（20 个前缀）
    context_window          INTEGER,
    modalities              TEXT,              -- JSON 数组
    efforts                 TEXT,              -- JSON 数组（off/low/high/max）
    status                  TEXT    NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active','missing','disabled')),
    hidden                  INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0,1)),
    excluded_from_candidates INTEGER NOT NULL DEFAULT 0 CHECK (excluded_from_candidates IN (0,1)),
    sort_order              INTEGER NOT NULL DEFAULT 0,
    source                  TEXT    NOT NULL DEFAULT 'auto' CHECK (source IN ('auto','override')),
    last_seen_at            INTEGER,
    missing_since           INTEGER
);
CREATE UNIQUE INDEX ux_real_models_upstream_model ON real_models(upstream_id, upstream_model_id);
CREATE INDEX ix_real_models_upstream ON real_models(upstream_id);
CREATE INDEX ix_real_models_status ON real_models(status);
```

**`CHECK (replacement_model_id <> id)` 只能防自环**。多跳成环（A→B→C→A）需要在写入时做
5 跳游走校验（替代链上限 5 跳，README 术语表），由应用层在同一个事务里完成。
**为什么不在 DB 里用递归 CTE 触发器做**：SQLite 触发器里的递归查询代价高、调试难，
而且"成环"是一个**业务判定**（要给出"哪一跳成环"的错误信息），DB 层给不出这个体验。

**`excluded_from_candidates`**：来自 PLAN 11.2——上游 `aggregate/*` 默认排除在候选来源之外。
它必须是一个**独立于 `status` 的列**：`status = missing/disabled` 表达"这个模型不可用"，
而 `excluded_from_candidates` 表达"它可用，但我们不拿它当候选"。两者的运维含义完全不同
（前者要排障、后者是策略）。

### 3.5 上游与凭据

```sql
CREATE TABLE upstreams (
    id               INTEGER PRIMARY KEY,
    kind             TEXT    NOT NULL CHECK (kind IN ('dshgateway','contributed')),
    name             TEXT    NOT NULL,
    base_url         TEXT    NOT NULL,
    secret_id        INTEGER REFERENCES upstream_secrets(id),
    owner_account_id INTEGER REFERENCES accounts(id),   -- NULL = 平台自有
    enabled          INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    last_seen_at     INTEGER
);
CREATE UNIQUE INDEX ux_upstreams_kind_name ON upstreams(kind, name);
CREATE INDEX ix_upstreams_owner ON upstreams(owner_account_id);

CREATE TABLE upstream_secrets (
    id         INTEGER PRIMARY KEY,
    upstream_id INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    ciphertext BLOB    NOT NULL,     -- AES-GCM 密文
    nonce      BLOB    NOT NULL      -- AES-GCM nonce（每次加密唯一）
);
CREATE UNIQUE INDEX ux_upstream_secrets_upstream ON upstream_secrets(upstream_id);

CREATE TABLE upstream_credentials (
    id               INTEGER PRIMARY KEY,
    owner_account_id INTEGER NOT NULL REFERENCES accounts(id),
    provider         TEXT    NOT NULL,
    external_id      TEXT    NOT NULL,
    external_label   TEXT,
    secret_ref       TEXT    NOT NULL,     -- 指向本地安全存储的引用，不含明文
    status           TEXT    NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','active','invalid','expired','disabled')),
    login_enabled    INTEGER NOT NULL DEFAULT 1 CHECK (login_enabled IN (0,1)),
    verify_status    TEXT    NOT NULL DEFAULT 'unverified'
                     CHECK (verify_status IN ('verified','suspect','unverified','trusted')),
    verify_detail    TEXT,                 -- JSON：鉴定证据（L0/L1）
    created_at       INTEGER NOT NULL,
    last_used_at     INTEGER
);
CREATE UNIQUE INDEX ux_upstream_credentials_external
    ON upstream_credentials(provider, external_id);
CREATE INDEX ix_upstream_credentials_owner ON upstream_credentials(owner_account_id);
CREATE INDEX ix_upstream_credentials_status ON upstream_credentials(status, verify_status);
```

**`UNIQUE(provider, external_id)` 是防重放的硬约束**（PLAN 10 明确要求）：同一个上游账号
被两个贡献者重复提交时，必须被 DB 拒绝而不是变成两条候选。**为什么 `external_id` 可以是
上游账号 id、也可以是其哈希**：不同 provider 暴露的标识不同，我们只需它**稳定且唯一**。

**为什么 `verify_detail` 用 JSON 而不是拆列**：鉴定证据（PLAN 第 4.4 节的 L0/L1 信号）会演化，
且不同 provider 的信号集合不同。JSON 让我们能在不迁移的情况下补充信号。

> **待定**：是否需要独立的 `verifications` 表来记录**历史**鉴定过程（当前只存最新结果）。
> 影响：无法回答"这个凭据是什么时候从 `verified` 变成 `suspect` 的"。

### 3.6 API Key

```sql
CREATE TABLE api_keys (
    id              INTEGER PRIMARY KEY,
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    key_hash        BLOB    NOT NULL,     -- SHA-256(明文 key)
    prefix          TEXT    NOT NULL,     -- 展示用前缀（fr-xxxx…）
    name            TEXT,
    status          TEXT    NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','disabled','expired')),
    expires_at      INTEGER,
    quota_limit     INTEGER,              -- NULL = 不限
    quota_used      INTEGER NOT NULL DEFAULT 0,
    rpm_limit       INTEGER,
    tpm_limit       INTEGER,
    daily_token_cap INTEGER,
    max_concurrency INTEGER
);
CREATE UNIQUE INDEX ux_api_keys_key_hash ON api_keys(key_hash);
CREATE INDEX ix_api_keys_account ON api_keys(account_id);
CREATE INDEX ix_api_keys_prefix ON api_keys(prefix);

CREATE TABLE key_model_allow (
    key_id            INTEGER NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    abstract_model_id INTEGER NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    PRIMARY KEY (key_id, abstract_model_id)
) WITHOUT ROWID;
CREATE INDEX ix_key_model_allow_model ON key_model_allow(abstract_model_id);
```

**为什么只存 `key_hash` 且用 `BLOB`**：PLAN 9——明文只在创建时返回一次。Hash 用 BLOB 的理由
同 `sessions.token_hash`。**为什么校验必须走唯一索引**：Key 校验是转发热路径上的第一跳。

**为什么额度上限列可空**：`NULL` 与 `0` 语义不同——`NULL` 是"不限"，`0` 是"额度为零（全部拒绝）"。
把两者混淆会让"新建 Key 默认无法请求"（PLAN 14 验收项）与"不限额度"变得无法区分。

### 3.7 请求记录与计量

```sql
CREATE TABLE requests (
    id                     INTEGER PRIMARY KEY,
    ts                     INTEGER NOT NULL,
    key_id                 INTEGER REFERENCES api_keys(id),
    account_id             INTEGER REFERENCES accounts(id),
    endpoint               TEXT    NOT NULL
                           CHECK (endpoint IN ('openai-chat','openai-responses',
                                               'anthropic-messages','gemini-generate')),
    virtual_model_id       INTEGER REFERENCES abstract_models(id),
    resolved_real_model_id INTEGER REFERENCES real_models(id),
    resolved_credential_id INTEGER REFERENCES upstream_credentials(id),
    status                 TEXT    NOT NULL,   -- ok | error | cancelled | timeout
    http_status            INTEGER,
    error_code             TEXT,
    retry_count            INTEGER NOT NULL DEFAULT 0,
    ttft_ms                INTEGER,
    total_ms               INTEGER,
    in_tokens              INTEGER,            -- NULL = 上游未上报（≠ 0）
    out_tokens             INTEGER
);
CREATE INDEX ix_requests_ts ON requests(ts);
CREATE INDEX ix_requests_key_ts ON requests(key_id, ts);
CREATE INDEX ix_requests_account_ts ON requests(account_id, ts);
CREATE INDEX ix_requests_credential_ts ON requests(resolved_credential_id, ts);
CREATE INDEX ix_requests_error ON requests(error_code, ts);

CREATE TABLE usage_daily (
    day               TEXT    NOT NULL,        -- 'YYYY-MM-DD'（UTC）
    key_id            INTEGER NOT NULL,
    abstract_model_id INTEGER NOT NULL,
    real_model_id     INTEGER NOT NULL,
    requests          INTEGER NOT NULL DEFAULT 0,
    in_tokens         INTEGER NOT NULL DEFAULT 0,
    out_tokens        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, key_id, abstract_model_id, real_model_id)
) WITHOUT ROWID;
CREATE INDEX ix_usage_daily_day ON usage_daily(day);
CREATE INDEX ix_usage_daily_key ON usage_daily(key_id, day);
```

**`requests` 里没有 `cost` 列，这是刻意的**（PLAN 10 明确）：我们**不做计价**。理由：每个上游
的计价方式不同且会变（订阅额度池、按量、赠送额度），把价格写进我们的表意味着要么维护一张
永远滞后的价目表，要么给用户一个不可信的金额。我们只记录 token 与归因字段，
金额由**对账**环节与上游账本（`token-ledger.json`）比对后得出。

**`retry_count` 是对第 10 章字段表的细化**：PLAN 11.5 要求契约测试覆盖"偶发连接失败与重试
是否成功"，PLAN 13 把"上游偶发连接失败"列为风险。没有这个列就只能看日志，无法统计。
它**不是**新概念，只是同一件事的计数字段。

**为什么 `in_tokens` 可空**：见 [02-ir.md](02-ir.md) 第 9 节——`NULL`（上游没报）与 `0`
（上游说零）在配额护栏与对账上语义完全不同。`usage_daily` 上的 `NOT NULL DEFAULT 0` 是
**聚合值**：聚合时把 `NULL` 记为 0 并把"有未知"通过 `requests` 表可回溯，这是唯一可接受的
折中（聚合键必须非空）。

### 3.8 审计

```sql
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,
    account_id  INTEGER REFERENCES accounts(id),
    acting_class TEXT,                    -- 以哪个权限类身份操作
    elevated    INTEGER NOT NULL DEFAULT 0 CHECK (elevated IN (0,1)),
    action      TEXT    NOT NULL,
    target      TEXT,
    ip          TEXT,
    ua          TEXT,
    result      TEXT    NOT NULL,
    detail      TEXT                       -- JSON
);
CREATE INDEX ix_audit_log_ts ON audit_log(ts);
CREATE INDEX ix_audit_log_account_ts ON audit_log(account_id, ts);
CREATE INDEX ix_audit_log_action_ts ON audit_log(action, ts);

-- append-only 由机制保障，而不是靠"我们不会写 UPDATE"
CREATE TRIGGER trg_audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER trg_audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
```

**为什么 `account_id` 可空**：登录失败、Key 尝试等事件发生在**身份确定之前**。
把它们记成某个账户的审计是伪造归因；记 NULL 才是事实。

**为什么 `acting_class` 与请求时的权限类都要记**（PLAN 9）：一个账户可能同时是 `admin`
与 `tenant`。"以管理员身份改了这个组"和"以租户身份建了这个 Key"是不同性质的操作，
隔离与分账都依赖这个字段。

**append-only 触发器的代价**：保留期清理不能再用 `DELETE`。见第 9 节待定项。

### 3.9 系统表

```sql
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL          -- JSON
) WITHOUT ROWID;

CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
);
```

**为什么 `settings` 与业务表分开**：它是运维可改的运行时开关（横幅文案、探测间隔等），
与业务数据生命周期不同，混在一起会让"备份业务数据"与"备份配置"无法分开。

---

## 4. 索引策略

| 索引 | 服务的查询 | 为什么需要 |
|---|---|---|
| `ux_sessions_token_hash` | 每次管理请求的会话校验 | 热路径，必须唯一索引 |
| `ux_api_keys_key_hash` | 每次模型请求的 Key 校验 | 转发热路径第一跳 |
| `ux_real_models_upstream_model` | 目录同步 upsert | 同步按 `(upstream, model_id)` 匹配 |
| `ix_requests_key_ts` 等 | 用量查询、对账、排障 | 都带时间范围，故索引前导列是**实体 id + ts** |
| `ix_usage_daily_*` | 日报表 | 主键是 `(day, …)`，按 day 查走主键；按 key 查需要额外索引 |
| `ux_upstream_credentials_external` | 去重 | 硬约束，不是优化 |

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
| 一次模型请求的落库 | 流结束后**一个**事务：`INSERT requests` + `usage_daily` upsert（+ 额度递增） | 三段必须原子：否则会出现"有请求记录但日汇总少一笔"，对账时无法解释 |
| `usage_daily` 累加 | `INSERT … ON CONFLICT(day,key_id,abstract_model_id,real_model_id) DO UPDATE SET requests=requests+?` | 读-改-写会在并发下丢更新；upsert 由 DB 保证原子 |
| `api_keys.quota_used` 递增 | `UPDATE api_keys SET quota_used = quota_used + ? WHERE id = ?` | 同上 |
| 抽象模型/候选/组策略变更 | 单事务（含替代链成环校验） | 校验与写入必须同事务，否则并发下会出现"校验时没环、写下去有环" |
| 账户合并 | 单事务（多个表） | 合并是多表改写的；中途失败留下半个身份是灾难 |
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
| `$DSH_HOME/openai-gateway/api-key` | 本机 DSH | `dshgateway` 不可用，可重新引导 |

**为什么值得接受"恢复更麻烦"**：反过来（主密钥随备份）意味着**任何拿到备份的人拿到全部
凭据**，而备份会被复制到更多地方（异地、对象存储、开发机）。把两样东西分开放，
是让"一处泄漏不等于全线失守"的唯一手段。

### 8.3 保留策略

| 数据 | 建议保留 | 依据 |
|---|---|---|
| `requests` | 90 天明细 | 排障窗口；更久靠 `usage_daily` |
| `usage_daily` | 永久 | 体积极小（每天 × key × 模型 的行数），是对账的长尾依据 |
| `audit_log` | ≥ 1 年，理想永久 | 安全审计的追溯要求 |
| `sessions` | 过期即清（后台任务） | 纯垃圾 |
| `upstream_credentials`（`disabled`） | 保留 | 归因历史需要 |

> **待定**：`audit_log` 的 append-only 触发器与保留期清理冲突（不能 `DELETE`）。
> 可选方案：① 导出到归档后**整库重建**该表；② 用按月分表 + 丢整表；③ 放宽为"允许
> 保留期外的删除，但记一条删除审计"。三者都改变了 append-only 的强度，需要拍板。

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

> **待定**：是否需要给"贡献者回报"相关的数据单独建表（PLAN 15 把贡献者回报列为待拍板项）。
> 当前设计里没有回报率的持久化位置，只有 `requests.resolved_credential_id` 可供事后统计。
