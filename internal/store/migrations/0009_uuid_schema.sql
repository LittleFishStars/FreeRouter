-- 0009_uuid_schema.sql —— 主键全面换成 TEXT UUID（2026-10-26 用户决定）。
--
-- 这一条不是一次「加列」迁移，而是**重建 schema**：SQLite 不能 ALTER 主键列的
-- 类型，也没有「把 INTEGER PRIMARY KEY 改成 TEXT PRIMARY KEY」的任何局部手段。
-- 所以本文件的做法是：按依赖逆序 DROP 全部业务表，再按 UUID 形状重建。
--
-- 为什么允许这么做（而不是写一份保数据的搬迁）：用户明确决定不做旧库迁移。
-- 配套的守卫在 migrate.go 的 rejectLegacyIntegerDB：库里任何业务表有行时，
-- 本迁移根本不会被应用——启动直接报错并提示「备份后删除 freerouter.db 重建」。
-- 于是本文件只会在**空库**上跑（全新库先跑 0001~0008 建出空的整型表，再被
-- 0009 拆掉重建），DROP 的是零行的表，没有数据风险。
--
-- 为什么不改 0001~0008 的字节：迁移器对已应用的迁移做 checksum 校验
-- （migrate.go），改一个字符就会让所有存量库启动失败。「历史不可变」这条纪律
-- 优先于「少一次重建」的便利——代价是全新库多跑七个空迁移加一次拆建，几十毫秒。
--
-- 累积列：本文件写的是 0001~0008 全部叠加后的**最终形状**（real_models 的
-- channel 与 verify_* 三列、api_keys 的 revoked 状态、accounts 的
-- must_change_password、abstract_candidates 的 sort_order、admin_inbox 的
-- 部分唯一索引 ux_admin_inbox_open 都在这里一次建全）。重建后 0001~0008 的
-- 效果被完整覆盖，不会丢任何列。
--
-- id 形状的两处语义变化（不只是类型换个写法）：
--   1. **合计哨兵 0 → 空串**：account_quotas / group_quota_pools 的
--      abstract_model_id 原来用 INTEGER 0 表示「全模型合计」（这两列无外键，
--      0 不是任何行的 id）。TEXT 之后 0 不是合法 UUID，哨兵改成 ''——
--      为什么不用 NULL：这两列是 WITHOUT ROWID 复合主键的成员，而
--      `NULL = NULL` 在 SQL 里是 UNKNOWN，会让「合计行」的 upsert 冲突判定
--      失效（同一组合反复插入不冲突，池子被拆成多行）。空串是值域内的
--      「合法非值」，比较行为正常。Go 侧对位常量是 store.TotalScope。
--   2. **secret_ref 的 join 去掉了 CAST**：upstream_credentials.secret_ref 存
--      upstream_secrets.id。以前那是十进制字符串，聚合 SQL 要
--      `CAST(c.secret_ref AS INTEGER)`，并靠「非数字 CAST 得 0 → join 落空」
--      容错。现在两边都是 UUID 文本，join 是纯字符串相等；落空的原因变成
--      「引用了一行不存在的密文」——语义等价，但注释与测试都得按新成因写。

-- ---------------------------------------------------------------- 拆除
-- 视图先走：它引用 upstream_credentials 与 requests，留在最后会让 DROP 顺序纠结。
DROP VIEW IF EXISTS v_contributor_stats;

-- append-only 触发器随表消失，但显式先 DROP 再 DROP TABLE 让顺序在文件里可读；
-- 也避免"触发器还在、表已没了"这种中间态被并发的旧连接观察到。
DROP TRIGGER IF EXISTS trg_audit_log_no_update;
DROP TRIGGER IF EXISTS trg_audit_log_no_delete;

-- 依赖逆序（子表先走）。SQLite 的 DROP TABLE 不校验"是否被引用"，但按逆序写
-- 让这份依赖图在文件里是可见的——正序读一遍就是建表顺序的镜像。
DROP TABLE IF EXISTS model_multipliers;
DROP TABLE IF EXISTS usage_daily;
DROP TABLE IF EXISTS requests;
DROP TABLE IF EXISTS key_model_allow;
DROP TABLE IF EXISTS abstract_candidates;
DROP TABLE IF EXISTS upstream_credentials;
DROP TABLE IF EXISTS upstream_secrets;
DROP TABLE IF EXISTS upstreams;
DROP TABLE IF EXISTS real_models;
DROP TABLE IF EXISTS abstract_models;
DROP TABLE IF EXISTS group_model_quota_caps;
DROP TABLE IF EXISTS group_quota_pools;
DROP TABLE IF EXISTS group_policies;
DROP TABLE IF EXISTS group_models;
DROP TABLE IF EXISTS group_members;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS account_quotas;
DROP TABLE IF EXISTS account_merges;
DROP TABLE IF EXISTS account_classes;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS admin_inbox;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS accounts;
-- settings（key→value，无 id 列）与 schema_migrations（迁移器自身）不碰。

-- ---------------------------------------------------------------- 重建：3.1 账户与权限类
-- 主键为什么是 TEXT 而不是继续 INTEGER + 一张 uuid 映射表：映射表会把每一次
-- join 变成两次探测，而映射本身又是一个需要与主表同生命周期维护的事实源——
-- 那正是 0001~0008 时代 idcode 层的负担，重建的目的就是把它彻底去掉。
CREATE TABLE IF NOT EXISTS accounts (
    id                 TEXT NOT NULL PRIMARY KEY,     -- UUID v4（对外 id 与库内主键同值）
    display_name       TEXT    NOT NULL,
    username           TEXT    NOT NULL,
    password_hash      TEXT    NOT NULL,       -- argon2id，含参数与盐的自描述串
    status             TEXT    NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','active','disabled','rejected','merged')),
    recovery_code_hash TEXT,                   -- 可空
    created_at         INTEGER NOT NULL,
    last_login_at      INTEGER,
    note               TEXT,
    -- 账号级速率/并发限位：-1 = 不限，0 = 全拒；NOT NULL 禁止 NULL——
    -- NULL 参与比较得 UNKNOWN，会让限位检查在"没配置"时静默放行或静默拒绝。
    rpm_limit          INTEGER NOT NULL DEFAULT -1 CHECK (rpm_limit >= -1),
    tpm_limit          INTEGER NOT NULL DEFAULT -1 CHECK (tpm_limit >= -1),
    max_concurrency    INTEGER NOT NULL DEFAULT -1 CHECK (max_concurrency >= -1),
    must_change_password INTEGER NOT NULL DEFAULT 0
                       CHECK (must_change_password IN (0, 1))
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_accounts_username ON accounts(username);
CREATE INDEX IF NOT EXISTS ix_accounts_status ON accounts(status);

CREATE TABLE IF NOT EXISTS account_classes (
    account_id TEXT    NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    class      TEXT    NOT NULL CHECK (class IN ('admin','tenant','contributor')),
    PRIMARY KEY (account_id, class)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_account_classes_class ON account_classes(class);

-- 为什么不级联删除：合并是可逆的（reversible_until）。源账户被删后追溯链断裂。
CREATE TABLE IF NOT EXISTS account_merges (
    id                 TEXT NOT NULL PRIMARY KEY,
    source_id          TEXT    NOT NULL REFERENCES accounts(id),
    target_id          TEXT    NOT NULL REFERENCES accounts(id),
    merged_at          INTEGER NOT NULL,
    reversible_until   INTEGER NOT NULL,          -- merged_at + 7 天
    undone_at          INTEGER,                  -- NULL = 未撤销；撤销只写这里，不删行
    -- 三份「被改挂资源清单」存 JSON 数组文本（0007）：只在回滚那一刻整体读一次。
    moved_key_ids        TEXT NOT NULL DEFAULT '[]',
    moved_credential_ids TEXT NOT NULL DEFAULT '[]',
    moved_group_ids      TEXT NOT NULL DEFAULT '[]',
    CHECK (source_id <> target_id)
);
CREATE INDEX IF NOT EXISTS ix_account_merges_source ON account_merges(source_id);
CREATE INDEX IF NOT EXISTS ix_account_merges_target ON account_merges(target_id);

-- 账户级周期额度。abstract_model_id 的空串 = 全模型合计（见文件头第 1 条）。
-- 该列刻意不加外键：合计行不对应任何抽象模型。
CREATE TABLE IF NOT EXISTS account_quotas (
    account_id        TEXT    NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    abstract_model_id TEXT    NOT NULL DEFAULT '',  -- '' = 合计哨兵；否则是抽象模型 UUID
    unit              TEXT    NOT NULL CHECK (unit IN ('request', 'token', 'credit')),
    period            TEXT    NOT NULL CHECK (period IN ('day', 'month')),
    quota_limit       INTEGER NOT NULL CHECK (quota_limit >= 0),
    quota_used        INTEGER NOT NULL DEFAULT 0 CHECK (quota_used >= 0),
    period_anchor     TEXT    NOT NULL,             -- 'YYYY-MM-DD'
    updated_at        INTEGER NOT NULL,
    PRIMARY KEY (account_id, abstract_model_id, unit, period)
) WITHOUT ROWID;

-- ---------------------------------------------------------------- 3.2 会话
-- sessions.id 本来就是 TEXT（不透明随机串），只有 account_id 换类型。
CREATE TABLE IF NOT EXISTS sessions (
    id             TEXT NOT NULL PRIMARY KEY,         -- 不透明随机串（128bit base64url）
    account_id     TEXT    NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
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
    id          TEXT NOT NULL PRIMARY KEY,
    name        TEXT    NOT NULL,
    description TEXT,
    is_default  INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
    created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_groups_name ON groups(name);
CREATE UNIQUE INDEX IF NOT EXISTS ux_groups_default ON groups(is_default) WHERE is_default = 1;

CREATE TABLE IF NOT EXISTS group_members (
    group_id   TEXT    NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    account_id TEXT    NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, account_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_group_members_account ON group_members(account_id);

CREATE TABLE IF NOT EXISTS group_models (
    group_id          TEXT    NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    abstract_model_id TEXT    NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, abstract_model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_group_models_model ON group_models(abstract_model_id);

CREATE TABLE IF NOT EXISTS group_policies (
    group_id     TEXT    NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    policy_key   TEXT    NOT NULL,
    policy_value TEXT    NOT NULL,       -- JSON
    PRIMARY KEY (group_id, policy_key)
) WITHOUT ROWID;

-- 组级共享池。abstract_model_id 的空串 = 全模型合计（与 account_quotas 同形）。
-- pool_limit 的 CHECK (>0) 与「没设 = 没有这一行」的口径不变。
CREATE TABLE IF NOT EXISTS group_quota_pools (
    group_id          TEXT    NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    abstract_model_id TEXT    NOT NULL DEFAULT '',   -- '' = 合计哨兵
    unit              TEXT    NOT NULL CHECK (unit IN ('request', 'token', 'credit')),
    period            TEXT    NOT NULL CHECK (period IN ('day', 'month')),
    pool_limit        INTEGER NOT NULL CHECK (pool_limit > 0),
    pool_used         INTEGER NOT NULL DEFAULT 0 CHECK (pool_used >= 0),
    period_anchor     TEXT    NOT NULL,              -- 'YYYY-MM-DD'
    updated_at        INTEGER NOT NULL,
    PRIMARY KEY (group_id, abstract_model_id, unit, period)
) WITHOUT ROWID;

-- 每人按模型上限（策略，不带计数器）。
CREATE TABLE IF NOT EXISTS group_model_quota_caps (
    group_id          TEXT    NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    abstract_model_id TEXT    NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    unit              TEXT    NOT NULL CHECK (unit IN ('request', 'token', 'credit')),
    period            TEXT    NOT NULL CHECK (period IN ('day', 'month')),
    cap               INTEGER NOT NULL CHECK (cap > 0),
    PRIMARY KEY (group_id, abstract_model_id, unit, period)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_gmqc_model ON group_model_quota_caps(abstract_model_id);

-- ---------------------------------------------------------------- 3.4 抽象模型与目录
CREATE TABLE IF NOT EXISTS abstract_models (
    id                   TEXT NOT NULL PRIMARY KEY,
    name                 TEXT    NOT NULL,     -- 对外模型名（fast / balanced / …）
    display_name         TEXT    NOT NULL,
    strategy             TEXT    NOT NULL DEFAULT 'expiry-first',
    capability_mode      TEXT    NOT NULL DEFAULT 'strict'
                         CHECK (capability_mode IN ('strict','filter')),
    status               TEXT    NOT NULL DEFAULT 'active'
                         CHECK (status IN ('active','deprecated','disabled')),
    replacement_model_id TEXT    REFERENCES abstract_models(id),
    deprecated_at        INTEGER,
    sunset_at            INTEGER,
    note                 TEXT,
    CHECK (replacement_model_id IS NULL OR replacement_model_id <> id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_abstract_models_name ON abstract_models(name);
CREATE INDEX IF NOT EXISTS ix_abstract_models_status ON abstract_models(status);

-- 列集合含 0002 的 channel 与 0004 的 verify_* 三列（重建后一次建全）。
CREATE TABLE IF NOT EXISTS real_models (
    id                      TEXT NOT NULL PRIMARY KEY,
    upstream_id             TEXT    NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    upstream_model_id       TEXT    NOT NULL,
    display_name            TEXT    NOT NULL,
    group_name              TEXT,              -- 管理员自由填的目录分组（人工字段）
    channel                 TEXT,              -- 模型级渠道（供应商身份）；NULL = 不可知
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
    missing_since           INTEGER,
    verify_status           TEXT    NOT NULL DEFAULT 'unverified'
                            CHECK (verify_status IN ('verified','suspect','unverified','trusted')),
    verify_detail           TEXT,              -- JSON：目录侧鉴定证据
    verify_source           TEXT               -- 证据来源标记（sidecar/refresh/…）
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_real_models_upstream_model ON real_models(upstream_id, upstream_model_id);
CREATE INDEX IF NOT EXISTS ix_real_models_upstream ON real_models(upstream_id);
CREATE INDEX IF NOT EXISTS ix_real_models_status ON real_models(status);
CREATE INDEX IF NOT EXISTS ix_real_models_channel_status ON real_models(channel, status);
CREATE INDEX IF NOT EXISTS ix_real_models_verify_source ON real_models(verify_source);

-- 候选集是声明式的；weight 已退役（恒 0），声明序看 sort_order（0008 的语义）。
CREATE TABLE IF NOT EXISTS abstract_candidates (
    abstract_model_id TEXT    NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    real_model_id     TEXT    NOT NULL REFERENCES real_models(id)     ON DELETE CASCADE,
    constraint_json   TEXT,                    -- 候选级约束（渠道范围等）
    weight            INTEGER NOT NULL DEFAULT 0,
    sort_order        INTEGER NOT NULL DEFAULT 0,   -- 声明序：平局决胜按它
    PRIMARY KEY (abstract_model_id, real_model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_abstract_candidates_real ON abstract_candidates(real_model_id);

-- ---------------------------------------------------------------- 3.5 上游与凭据
-- 环形外键（upstreams.secret_id ↔ upstream_secrets.upstream_id）在 SQLite 里
-- 延迟解析，两个方向都能建表成功——与 0001 同一处理。
CREATE TABLE IF NOT EXISTS upstream_secrets (
    id          TEXT NOT NULL PRIMARY KEY,
    upstream_id TEXT    NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    ciphertext  BLOB    NOT NULL,
    nonce       BLOB    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_upstream_secrets_upstream ON upstream_secrets(upstream_id);

CREATE TABLE IF NOT EXISTS upstreams (
    id               TEXT NOT NULL PRIMARY KEY,
    kind             TEXT    NOT NULL CHECK (kind IN ('relay','vendor','selfhosted')),
    name             TEXT    NOT NULL,
    base_url         TEXT    NOT NULL,
    secret_id        TEXT    REFERENCES upstream_secrets(id),
    owner_account_id TEXT    REFERENCES accounts(id),   -- NULL = 平台自有
    enabled          INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    last_seen_at     INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_upstreams_kind_name ON upstreams(kind, name);
CREATE INDEX IF NOT EXISTS ix_upstreams_owner ON upstreams(owner_account_id);

-- secret_ref 现在直接存 upstream_secrets.id（UUID 文本）。
-- 旧口径「十进制字符串 + CAST join」作废，见文件头第 2 条。
CREATE TABLE IF NOT EXISTS upstream_credentials (
    id               TEXT NOT NULL PRIMARY KEY,
    owner_account_id TEXT    NOT NULL REFERENCES accounts(id),
    provider         TEXT    NOT NULL,
    external_id      TEXT    NOT NULL,
    external_label   TEXT,
    secret_ref       TEXT    NOT NULL,     -- upstream_secrets.id（UUID 文本），不含明文
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
-- 列集合含 0005 的 revoked 状态（那张表重建的最终形状）。
CREATE TABLE IF NOT EXISTS api_keys (
    id              TEXT NOT NULL PRIMARY KEY,
    account_id      TEXT    NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    key_hash        BLOB    NOT NULL,     -- SHA-256(明文 key)
    prefix          TEXT    NOT NULL,     -- 展示用前缀（fr-xxxx…）
    name            TEXT,
    status          TEXT    NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','disabled','expired','revoked')),
    expires_at      INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_api_keys_key_hash ON api_keys(key_hash);
CREATE INDEX IF NOT EXISTS ix_api_keys_account ON api_keys(account_id);
CREATE INDEX IF NOT EXISTS ix_api_keys_prefix ON api_keys(prefix);

CREATE TABLE IF NOT EXISTS key_model_allow (
    key_id            TEXT    NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    abstract_model_id TEXT    NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    PRIMARY KEY (key_id, abstract_model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_key_model_allow_model ON key_model_allow(abstract_model_id);

-- ---------------------------------------------------------------- 3.7 请求记录与计量
-- 刻意没有 cost 列（PLAN 10：不做计价）。
CREATE TABLE IF NOT EXISTS requests (
    id                     TEXT NOT NULL PRIMARY KEY,
    ts                     INTEGER NOT NULL,
    key_id                 TEXT    REFERENCES api_keys(id),
    account_id             TEXT    REFERENCES accounts(id),
    endpoint               TEXT    NOT NULL
                           CHECK (endpoint IN ('openai-chat','openai-responses',
                                               'anthropic-messages','gemini-generate')),
    virtual_model_id       TEXT    REFERENCES abstract_models(id),
    resolved_real_model_id TEXT    REFERENCES real_models(id),
    resolved_credential_id TEXT    REFERENCES upstream_credentials(id),
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

-- 聚合键必须非空（这里没有合计哨兵：三列都是真实 id）。
CREATE TABLE IF NOT EXISTS usage_daily (
    day               TEXT    NOT NULL,        -- 'YYYY-MM-DD'（UTC）
    account_id        TEXT    NOT NULL,
    abstract_model_id TEXT    NOT NULL,
    real_model_id     TEXT    NOT NULL,
    requests          INTEGER NOT NULL DEFAULT 0,
    in_tokens         INTEGER NOT NULL DEFAULT 0,
    out_tokens        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, account_id, abstract_model_id, real_model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_usage_daily_day ON usage_daily(day);
CREATE INDEX IF NOT EXISTS ix_usage_daily_account ON usage_daily(account_id, day);

-- ---------------------------------------------------------------- 3.8 审计
CREATE TABLE IF NOT EXISTS audit_log (
    id           TEXT NOT NULL PRIMARY KEY,
    ts           INTEGER NOT NULL,
    account_id   TEXT    REFERENCES accounts(id),
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

-- append-only 由机制保障，而不是靠"我们不会写 UPDATE"。保留期清理走
-- store.PruneAudit 的受控通道（它自己摘触发器、删完再建回来）。
CREATE TRIGGER IF NOT EXISTS trg_audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER IF NOT EXISTS trg_audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;

-- ---------------------------------------------------------------- 3.9 待办与价格乘数
-- related_id 是多态列（按 kind 解释指向哪张表），不加外键；存裸 UUID。
-- 「按 kind 给 id 加前缀」那套逻辑随前缀一起废弃。
CREATE TABLE IF NOT EXISTS admin_inbox (
    id          TEXT NOT NULL PRIMARY KEY,
    kind        TEXT    NOT NULL,     -- registration | model_change | outage | pool_alert
    severity    TEXT    NOT NULL,     -- info | warning | critical
    title       TEXT    NOT NULL,
    detail_json TEXT,                 -- JSON
    related_id  TEXT,                 -- 关联对象 id（按 kind 解释）
    created_at  INTEGER NOT NULL,
    read_at     INTEGER,
    read_by     TEXT    REFERENCES accounts(id)
);
CREATE INDEX IF NOT EXISTS ix_admin_inbox_unread ON admin_inbox(read_at, created_at);
CREATE INDEX IF NOT EXISTS ix_admin_inbox_kind ON admin_inbox(kind, created_at);
CREATE INDEX IF NOT EXISTS ix_admin_inbox_related ON admin_inbox(related_id);
-- 未读期幂等（0003 的部分唯一索引）：同一 (kind, related_id) 未读时只能有一行。
CREATE UNIQUE INDEX IF NOT EXISTS ux_admin_inbox_open
    ON admin_inbox(kind, related_id)
 WHERE read_at IS NULL AND related_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS model_multipliers (
    real_model_id  TEXT NOT NULL PRIMARY KEY REFERENCES real_models(id) ON DELETE CASCADE,
    in_multiplier  REAL    NOT NULL DEFAULT 1.0,
    out_multiplier REAL    NOT NULL DEFAULT 1.0,
    updated_by     TEXT    REFERENCES accounts(id),
    updated_at     INTEGER NOT NULL
);

-- settings 不重建：它是 key→value，没有 id 列，生命周期与业务数据不同。

-- ---------------------------------------------------------------- 3.10 贡献统计视图
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
