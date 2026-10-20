-- 0001_init.sql —— FreeRouter 初始表结构。
--
-- 唯一事实源：docs/architecture/05-storage.md 第 3 节「完整 DDL」。
-- 本文件与该节逐字对齐；任何偏离都必须先改文档再改这里。
--
-- 全局约定（05-storage.md 第 2 节）：
--   * 时间列一律 INTEGER，UTC unix 秒——SQLite 没有原生时间类型，整数比较与索引
--     比字符串高效，且不存在时区歧义。
--   * 日期列 TEXT "YYYY-MM-DD"（仅 usage_daily.day）：按天聚合的键，文本序即时间序。
--   * 主键是 INTEGER PRIMARY KEY（rowid 别名）：写入快、索引小；不用 UUID 主键，
--     否则索引膨胀 + 随机写。
--   * 纯联结表用 WITHOUT ROWID：复合主键本身就是数据，省一层索引。
--   * 布尔用 INTEGER 0/1：SQLite 没有布尔类型。
--   * 全部外键显式声明，并由连接级 PRAGMA foreign_keys=ON 打开（见 store.go 的 DSN）。
--
-- 为什么每个对象都写 IF NOT EXISTS：迁移必须幂等。schema_migrations 已经保证
-- "已应用的不会重放"，但幂等是一条防御性冗余——当一次迁移在版本号写入前崩溃、
-- 或有人手工执行过半个文件时，IF NOT EXISTS 让重放变成无操作而不是启动失败。

-- ---------------------------------------------------------------- 3.1 账户与权限类

CREATE TABLE IF NOT EXISTS accounts (
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
CREATE UNIQUE INDEX IF NOT EXISTS ux_accounts_username ON accounts(username);
CREATE INDEX IF NOT EXISTS ix_accounts_status ON accounts(status);

-- 为什么权限类单独成表而不是 accounts.role：一个账户可同时拥有多个权限类，
-- 单列存不了集合；而这里的 CHECK 是权限类的唯一权威枚举，多一个类就必须改这里。
CREATE TABLE IF NOT EXISTS account_classes (
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    class      TEXT    NOT NULL CHECK (class IN ('admin','tenant','contributor')),
    PRIMARY KEY (account_id, class)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_account_classes_class ON account_classes(class);

-- 为什么不级联删除：合并是可逆的（reversible_until）。源账户被删后追溯链断裂，
-- "可逆"就无从执行。外键默认 RESTRICT 正是我们要的行为。
CREATE TABLE IF NOT EXISTS account_merges (
    id               INTEGER PRIMARY KEY,
    source_id        INTEGER NOT NULL REFERENCES accounts(id),
    target_id        INTEGER NOT NULL REFERENCES accounts(id),
    merged_at        INTEGER NOT NULL,
    reversible_until INTEGER NOT NULL,          -- merged_at + 7 天
    undone_at        INTEGER,                  -- NULL = 未撤销；撤销只写这里，不删行
    CHECK (source_id <> target_id)
);
CREATE INDEX IF NOT EXISTS ix_account_merges_source ON account_merges(source_id);
CREATE INDEX IF NOT EXISTS ix_account_merges_target ON account_merges(target_id);

-- ---------------------------------------------------------------- 3.2 会话

-- token_hash 用 BLOB 而不是十六进制 TEXT：32 字节 vs 64 字符，索引体积减半且比较
-- 无需大小写归一。单独建唯一索引是因为会话校验是每个管理请求的热路径查询，
-- 必须一次 B 树探测，不能全表扫。
CREATE TABLE IF NOT EXISTS sessions (
    id             TEXT    PRIMARY KEY,         -- 不透明随机串（128bit base64url）
    account_id     INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash     BLOB    NOT NULL,            -- SHA-256(token)，32 字节
    created_at     INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,
    ip             TEXT,
    ua             TEXT
) WITHOUT ROWID;
CREATE UNIQUE INDEX IF NOT EXISTS ux_sessions_token_hash ON sessions(token_hash);
CREATE INDEX IF NOT EXISTS ix_sessions_account ON sessions(account_id);
CREATE INDEX IF NOT EXISTS ix_sessions_expires ON sessions(expires_at);

-- ---------------------------------------------------------------- 3.3 组与授权

CREATE TABLE IF NOT EXISTS groups (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    description TEXT,
    is_default  INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
    created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_groups_name ON groups(name);
-- 部分唯一索引：默认组有且只有一个（新账户的兜底授权）。让"唯一"只约束默认组那一行，
-- 其它组不受影响。
CREATE UNIQUE INDEX IF NOT EXISTS ux_groups_default ON groups(is_default) WHERE is_default = 1;

CREATE TABLE IF NOT EXISTS group_members (
    group_id   INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, account_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_group_members_account ON group_members(account_id);

CREATE TABLE IF NOT EXISTS group_models (
    group_id          INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    abstract_model_id INTEGER NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, abstract_model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_group_models_model ON group_models(abstract_model_id);

-- 策略用 policy_key/policy_value 键值形状：策略集合会随功能增长，每加一个策略就改
-- 表结构会带来"每加一个功能一次迁移"，而迁移是线上风险最高的操作。代价是值无法由
-- DB 约束校验形状，必须在应用层用 schema 校验——这个代价我们接受，因为策略值本来
-- 就是 JSON 文档。
CREATE TABLE IF NOT EXISTS group_policies (
    group_id     INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    policy_key   TEXT    NOT NULL,
    policy_value TEXT    NOT NULL,       -- JSON
    PRIMARY KEY (group_id, policy_key)
) WITHOUT ROWID;

-- 组级共享池：有状态的消耗计数，刻意不塞进 group_policies。
--   1) 它是状态不是策略：pool_used 每次请求都变，写频比策略高几个数量级，
--      混在一张表里会让"改策略"与"扣额度"争同一个行的写锁。
--   2) 它需要单条条件 UPDATE：JSON 列里的数字无法在 WHERE 里参与计算与比较。
--   3) 主键形态不同：一个组可以同时有 requests/day 与 tokens/day 两行。
-- pool_limit 用 NOT NULL CHECK (pool_limit > 0)："没设"用"没有这一行"表达。
-- 为什么不允许 0：NULL 参与比较的结果是 UNKNOWN，而条件 UPDATE 的 WHERE 只接受"真"，
-- 于是"有行但 limit 为 NULL"会让每次扣减都静默失败——表现与"池子已耗尽"一模一样。
-- 不加 CHECK (pool_used <= pool_limit)：管理员把上限调小到低于已用量是合法且必要的
-- 应急关闸手段，那条 CHECK 会让该操作直接写入失败。
CREATE TABLE IF NOT EXISTS group_quota_pools (
    group_id      INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    dimension     TEXT    NOT NULL CHECK (dimension IN ('requests', 'tokens')),
    period        TEXT    NOT NULL CHECK (period IN ('day', 'month')),
    pool_limit    INTEGER NOT NULL CHECK (pool_limit > 0),
    pool_used     INTEGER NOT NULL DEFAULT 0 CHECK (pool_used >= 0),
    period_anchor TEXT    NOT NULL,              -- 'YYYY-MM-DD'：pool_used 属于哪个周期的起点
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (group_id, dimension, period)
) WITHOUT ROWID;

-- ---------------------------------------------------------------- 3.4 抽象模型与目录
-- 注意建表顺序：abstract_models 先于 group_models 的引用目标，real_models 先于
-- abstract_candidates。SQLite 的外键是延迟解析的，但按依赖顺序建表能让
-- "哪张表依赖哪张表"在文件里一眼可读。

CREATE TABLE IF NOT EXISTS abstract_models (
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
    -- 只能防自环；多跳成环由应用层在同一个事务里做 visited 游走兜底。
    -- 不用递归 CTE 触发器：SQLite 触发器里递归代价高、难调试，而且"哪里成环"
    -- 是一个业务判定，DB 层给不出这个错误信息。
    CHECK (replacement_model_id IS NULL OR replacement_model_id <> id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_abstract_models_name ON abstract_models(name);
CREATE INDEX IF NOT EXISTS ix_abstract_models_status ON abstract_models(status);

CREATE TABLE IF NOT EXISTS real_models (
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
    -- excluded_from_candidates 必须独立于 status：status 表达"不可用"，
    -- 它表达"可用但我们不拿它当候选"。运维含义完全不同（前者排障、后者是策略）。
    excluded_from_candidates INTEGER NOT NULL DEFAULT 0 CHECK (excluded_from_candidates IN (0,1)),
    sort_order              INTEGER NOT NULL DEFAULT 0,
    source                  TEXT    NOT NULL DEFAULT 'auto' CHECK (source IN ('auto','override')),
    last_seen_at            INTEGER,
    missing_since           INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_real_models_upstream_model ON real_models(upstream_id, upstream_model_id);
CREATE INDEX IF NOT EXISTS ix_real_models_upstream ON real_models(upstream_id);
CREATE INDEX IF NOT EXISTS ix_real_models_status ON real_models(status);

-- 候选集是声明式的，不绑定具体凭据：运行时才展开成「候选 × 可用凭据实例」再排序
-- （PLAN 3.2）。把凭据绑进来会让一条候选在凭据失效后变成脏数据，需要额外的清理任务。
CREATE TABLE IF NOT EXISTS abstract_candidates (
    abstract_model_id INTEGER NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    real_model_id     INTEGER NOT NULL REFERENCES real_models(id)     ON DELETE CASCADE,
    constraint_json   TEXT,                    -- 候选级约束（渠道范围等）
    weight            INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (abstract_model_id, real_model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_abstract_candidates_real ON abstract_candidates(real_model_id);

-- ---------------------------------------------------------------- 3.5 上游与凭据
-- upstream_secrets 先建：upstreams.secret_id 指向它，而它反向指回 upstreams。
-- 环形的外键在 SQLite 里是延迟解析的，两个方向都能建表成功。

-- ciphertext/nonce 是 AES-GCM 密文与每次加密唯一的 nonce。
-- 明文永不入库；主密钥永不入库（PLAN 9）。
CREATE TABLE IF NOT EXISTS upstream_secrets (
    id          INTEGER PRIMARY KEY,
    upstream_id INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_upstream_secrets_upstream ON upstream_secrets(upstream_id);

CREATE TABLE IF NOT EXISTS upstreams (
    id               INTEGER PRIMARY KEY,
    kind             TEXT    NOT NULL CHECK (kind IN ('relay','vendor','selfhosted')),
    name             TEXT    NOT NULL,
    base_url         TEXT    NOT NULL,
    secret_id        INTEGER REFERENCES upstream_secrets(id),
    owner_account_id INTEGER REFERENCES accounts(id),   -- NULL = 平台自有
    enabled          INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    last_seen_at     INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_upstreams_kind_name ON upstreams(kind, name);
CREATE INDEX IF NOT EXISTS ix_upstreams_owner ON upstreams(owner_account_id);

-- UNIQUE(provider, external_id) 是防重放的硬约束：同一个上游账号被两个贡献者
-- 重复提交时必须被 DB 拒绝，而不是变成两条候选。
-- external_id 可以是上游账号 id、也可以是它的哈希——不同 provider 暴露的标识不同，
-- 我们只需要它稳定且唯一。
-- verify_detail 用 JSON 而不是拆列：鉴定证据（L0/L1 信号）会演化，且不同 provider
-- 的信号集合不同，JSON 让我们能在不迁移的情况下补充信号。
CREATE TABLE IF NOT EXISTS upstream_credentials (
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
CREATE UNIQUE INDEX IF NOT EXISTS ux_upstream_credentials_external
    ON upstream_credentials(provider, external_id);
CREATE INDEX IF NOT EXISTS ix_upstream_credentials_owner ON upstream_credentials(owner_account_id);
CREATE INDEX IF NOT EXISTS ix_upstream_credentials_status ON upstream_credentials(status, verify_status);

-- ---------------------------------------------------------------- 3.6 API Key

-- 只存 key_hash（BLOB，理由同 sessions.token_hash）；明文只在创建时返回一次。
-- 上限列可空：NULL 是"不限"，0 是"额度为零（全部拒绝）"，两者混淆会让
-- "新建 Key 默认无法请求"与"不限额度"变得无法区分。
CREATE TABLE IF NOT EXISTS api_keys (
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
CREATE UNIQUE INDEX IF NOT EXISTS ux_api_keys_key_hash ON api_keys(key_hash);
CREATE INDEX IF NOT EXISTS ix_api_keys_account ON api_keys(account_id);
CREATE INDEX IF NOT EXISTS ix_api_keys_prefix ON api_keys(prefix);

CREATE TABLE IF NOT EXISTS key_model_allow (
    key_id            INTEGER NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    abstract_model_id INTEGER NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    PRIMARY KEY (key_id, abstract_model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_key_model_allow_model ON key_model_allow(abstract_model_id);

-- ---------------------------------------------------------------- 3.7 请求记录与计量

-- 刻意没有 cost 列（PLAN 10）：我们不做计价。每个上游的计价方式不同且会变，
-- 把价格写进我们的表意味着要么维护一张永远滞后的价目表，要么给用户一个不可信的
-- 金额。金额由对账环节与上游账本比对后得出。
-- retry_count 支撑"偶发连接失败与重试是否成功"的统计；没有它就只能看日志。
-- in_tokens/out_tokens 可空：NULL（上游没报）与 0（上游说零）在配额护栏与对账上
-- 语义完全不同。
CREATE TABLE IF NOT EXISTS requests (
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
    in_tokens              INTEGER,
    out_tokens             INTEGER
);
CREATE INDEX IF NOT EXISTS ix_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS ix_requests_key_ts ON requests(key_id, ts);
CREATE INDEX IF NOT EXISTS ix_requests_account_ts ON requests(account_id, ts);
CREATE INDEX IF NOT EXISTS ix_requests_credential_ts ON requests(resolved_credential_id, ts);
CREATE INDEX IF NOT EXISTS ix_requests_error ON requests(error_code, ts);

-- usage_daily 上的 NOT NULL DEFAULT 0 是聚合值：聚合时把 NULL 记为 0，并把"有未知"
-- 通过 requests 表可回溯。这是唯一可接受的折中——聚合键必须非空。
-- 它必须落表而不是现查：requests 按 90 天清理后，它就是唯一的长期事实。
CREATE TABLE IF NOT EXISTS usage_daily (
    day               TEXT    NOT NULL,        -- 'YYYY-MM-DD'（UTC）
    key_id            INTEGER NOT NULL,
    abstract_model_id INTEGER NOT NULL,
    real_model_id     INTEGER NOT NULL,
    requests          INTEGER NOT NULL DEFAULT 0,
    in_tokens         INTEGER NOT NULL DEFAULT 0,
    out_tokens        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, key_id, abstract_model_id, real_model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_usage_daily_day ON usage_daily(day);
CREATE INDEX IF NOT EXISTS ix_usage_daily_key ON usage_daily(key_id, day);

-- ---------------------------------------------------------------- 3.8 审计

-- account_id 可空：登录失败、Key 尝试等事件发生在身份确定之前，把它们记成某个
-- 账户的审计是伪造归因，记 NULL 才是事实。
-- acting_class 必记：一个账户可能同时是 admin 与 tenant，"以管理员身份改了这个组"
-- 与"以租户身份建了这个 Key"是不同性质的操作，隔离与分账都依赖它。
CREATE TABLE IF NOT EXISTS audit_log (
    id           INTEGER PRIMARY KEY,
    ts           INTEGER NOT NULL,
    account_id   INTEGER REFERENCES accounts(id),
    acting_class TEXT,                    -- 以哪个权限类身份操作
    elevated     INTEGER NOT NULL DEFAULT 0 CHECK (elevated IN (0,1)),
    action       TEXT    NOT NULL,
    target       TEXT,
    ip           TEXT,
    ua           TEXT,
    result       TEXT    NOT NULL,
    detail       TEXT                       -- JSON
);
CREATE INDEX IF NOT EXISTS ix_audit_log_ts ON audit_log(ts);
CREATE INDEX IF NOT EXISTS ix_audit_log_account_ts ON audit_log(account_id, ts);
CREATE INDEX IF NOT EXISTS ix_audit_log_action_ts ON audit_log(action, ts);

-- append-only 由机制保障，而不是靠"我们不会写 UPDATE"。
-- 保留期清理的 DELETE 也不开放给业务代码：必须走 store.PruneAudit 的受控通道
-- （导出 → 回读校验 → 摘触发器 → 单事务删除 → 重建触发器 → 写审计）。
CREATE TRIGGER IF NOT EXISTS trg_audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;

-- ---------------------------------------------------------------- 管理员待办与价格乘数
-- 这两张表不在 05-storage.md 第 3 节里，但 PLAN 第 10 章（优先级高于分册）明确要求：
--   * admin_inbox：目录变动（新增/消失/属性变化）、注册待审核、池与熔断告警都要落成
--     一条待办，否则管理员只能靠翻日志发现「模型少了三个」（PLAN 11.1 第 5 条）。
--   * model_multipliers：Credits 价格乘数，表里没有的行默认 1。
-- 两者都是纯追加，与 05 的既有表无冲突，因此放在 0001 而不是留到 0002——
-- 新库一次建全，比为一条 CREATE TABLE 多跑一次迁移简单。

CREATE TABLE IF NOT EXISTS admin_inbox (
    id          INTEGER PRIMARY KEY,
    kind        TEXT    NOT NULL,     -- registration | model_change | outage | pool_alert
    severity    TEXT    NOT NULL,     -- info | warning | critical
    title       TEXT    NOT NULL,
    detail_json TEXT,                 -- JSON
    related_id  INTEGER,              -- 关联对象 id（按 kind 解释），不加外键：种类太多
    created_at  INTEGER NOT NULL,
    read_at     INTEGER,
    read_by     INTEGER REFERENCES accounts(id)
);
-- 看板默认只看未读，所以索引带上 read_at：未读行是极少数，部分索引用不上时
-- 这条复合索引仍能覆盖「未读 + 按时间倒序」这个唯一查询形状。
CREATE INDEX IF NOT EXISTS ix_admin_inbox_unread ON admin_inbox(read_at, created_at);
CREATE INDEX IF NOT EXISTS ix_admin_inbox_kind ON admin_inbox(kind, created_at);
CREATE INDEX IF NOT EXISTS ix_admin_inbox_related ON admin_inbox(related_id);

-- 表里没有的模型默认乘数 1（PLAN 第 10 章），因此「缺行」是正常状态，不需要回填。
-- updated_by 不加外键约束到 NOT NULL：系统自举写入时没有操作者。
CREATE TABLE IF NOT EXISTS model_multipliers (
    real_model_id  INTEGER PRIMARY KEY REFERENCES real_models(id) ON DELETE CASCADE,
    in_multiplier  REAL    NOT NULL DEFAULT 1.0,
    out_multiplier REAL    NOT NULL DEFAULT 1.0,
    updated_by     INTEGER REFERENCES accounts(id),
    updated_at     INTEGER NOT NULL
);

-- settings 与业务表分开：它是运维可改的运行时开关，生命周期与业务数据不同，
-- 混在一起会让"备份业务数据"与"备份配置"无法分开。
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL          -- JSON
) WITHOUT ROWID;

-- schema_migrations 由迁移器在读取迁移集之前引导创建（见 migrate.go），
-- 不放在 0001 里：否则"迁移器要先读它才知道该不该跑 0001"就成了循环依赖。
-- 这里只声明形状，权威定义在 migrate.go 的 bootstrapSQL。

-- ---------------------------------------------------------------- 3.10 贡献统计视图

-- 贡献者侧没有任何回报，所以不存在回报表/结算流水。要展示的只有贡献统计，
-- 而统计是纯读取，读的是已落库的 requests 与 upstream_credentials。
-- 为什么不落汇总表：一旦落表就必须在请求落库的同一事务里维护它（热路径上多一次写
-- 就多一份代价），而任何一处漏更新都会让贡献者看到的数字与实际请求永久分叉。
-- 视图的答案永远与事实表同源，这个风险直接消失。
-- 成本可接受：ix_requests_credential_ts 的前导列正是 credential_id，聚合走索引。
CREATE VIEW IF NOT EXISTS v_contributor_stats AS
SELECT uc.owner_account_id            AS account_id,
       uc.id                          AS credential_id,
       uc.provider,
       uc.status,                     -- 凭据状态
       uc.verify_status,              -- 健康度（鉴定结果）
       COUNT(r.id)                    AS request_count,
       COALESCE(SUM(r.in_tokens), 0)  AS in_tokens,
       COALESCE(SUM(r.out_tokens), 0) AS out_tokens,
       MAX(r.ts)                      AS last_request_at
FROM upstream_credentials uc
LEFT JOIN requests r ON r.resolved_credential_id = uc.id
GROUP BY uc.id;
