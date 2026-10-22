-- 0005_api_key_revoked.sql —— api_keys.status 增加 'revoked'（单向吊销）。
--
-- 为什么必须迁移而不是在 Go 层映射成 disabled：
--   auth 的 KeyManager 用「读回来是不是 revoked」守单向性（Revoke 后
--   Enable/Disable 都拒绝）。若把 revoked 落成 disabled，读回来变 disabled，
--   Enable 就会被放行——一条被吊销的 Key 能被恢复，吊销语义直接失效。
--   文档 05-keys §4 的 revoked 与 disabled 分开，就是为了这个。
--
-- 为什么重建表而不是 ALTER：SQLite 不支持修改 CHECK 约束，只能
-- 「建新表 → 拷数据 → 删旧表 → 改名 → 重建索引」。表很小（每行几十字节），
-- 重建的锁窗可忽略；migrate.go 在事务外逐条执行 DDL（SQLite 的 DDL 也是事务性的）。

CREATE TABLE api_keys_new (
    id              INTEGER PRIMARY KEY,
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    key_hash        BLOB    NOT NULL,
    prefix          TEXT    NOT NULL,
    name            TEXT,
    status          TEXT    NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','disabled','expired','revoked')),
    expires_at      INTEGER
);

INSERT INTO api_keys_new(id, account_id, key_hash, prefix, name, status, expires_at)
SELECT id, account_id, key_hash, prefix, name, status, expires_at FROM api_keys;

DROP TABLE api_keys;

ALTER TABLE api_keys_new RENAME TO api_keys;

CREATE UNIQUE INDEX IF NOT EXISTS ux_api_keys_key_hash ON api_keys(key_hash);
CREATE INDEX IF NOT EXISTS ix_api_keys_account ON api_keys(account_id);
CREATE INDEX IF NOT EXISTS ix_api_keys_prefix ON api_keys(prefix);
