-- 0016_request_endpoint_completion.sql —— requests.endpoint 词表加入文本补全端点。
--
-- 需求（2026-11-02 用户提出「至少需要支持 DeepSeek 官方支持的所有 api」）：
-- 新增 POST /v1/completions（官方 FIM Completion API，Beta）。它记录的
-- endpoint 值是 'openai-completion'。
--
-- 为什么必须重建表：SQLite 的 ALTER TABLE 改不了 CHECK 约束。旧 CHECK 只允许
-- ('openai-chat','openai-responses','anthropic-messages','gemini-generate')，
-- 不重建表的话每一条补全请求的落库 INSERT 都会被约束拒绝——请求成功、
-- 记账失败，是最难排查的一类故障（对账侧会显示"少了一批请求"）。
--
-- 搬数据规则：requests 的列与旧表完全一致（0009 之后的定义），逐列照搬。
-- 索引在重建后重新创建（DROP TABLE 会一并丢弃旧索引）。
-- 视图 dependency：本表被 0009 定义的视图引用，但那些视图只读 requests 的
-- 既有列，列集合不变，故不受影响；DROP 顺序按 0009 的约定（先视图后表）——
-- 这里为最小改动起见先删视图依赖不涉及的索引即可，实际执行顺序见下。

-- 视图必须先删：SQLite 在重建被视图引用的表时会校验依赖，表和视图并存时
-- DROP TABLE 会报 "error in view ...: no such table"（0009 的经验：
-- 视图与表的 DROP 顺序是硬约束，不是风格问题）。
DROP VIEW IF EXISTS v_contributor_stats;

DROP INDEX IF EXISTS ix_requests_ts;
DROP INDEX IF EXISTS ix_requests_key_ts;
DROP INDEX IF EXISTS ix_requests_account_ts;
DROP INDEX IF EXISTS ix_requests_credential_ts;
DROP INDEX IF EXISTS ix_requests_error;

CREATE TABLE requests_new (
    id                     TEXT NOT NULL PRIMARY KEY,
    ts                     INTEGER NOT NULL,
    key_id                 TEXT    REFERENCES api_keys(id),
    account_id             TEXT    REFERENCES accounts(id),
    endpoint               TEXT    NOT NULL
                           CHECK (endpoint IN ('openai-chat','openai-responses',
                                               'openai-completion',
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

INSERT INTO requests_new (id, ts, key_id, account_id, endpoint, virtual_model_id,
                          resolved_real_model_id, resolved_credential_id, status,
                          http_status, error_code, retry_count, ttft_ms, total_ms,
                          in_tokens, out_tokens)
SELECT id, ts, key_id, account_id, endpoint, virtual_model_id,
       resolved_real_model_id, resolved_credential_id, status,
       http_status, error_code, retry_count, ttft_ms, total_ms,
       in_tokens, out_tokens
FROM requests;

DROP TABLE requests;
ALTER TABLE requests_new RENAME TO requests;

CREATE INDEX IF NOT EXISTS ix_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS ix_requests_key_ts ON requests(key_id, ts);
CREATE INDEX IF NOT EXISTS ix_requests_account_ts ON requests(account_id, ts);
CREATE INDEX IF NOT EXISTS ix_requests_credential_ts ON requests(resolved_credential_id, ts);
CREATE INDEX IF NOT EXISTS ix_requests_error ON requests(error_code, ts);

-- 视图重建：定义与 0009 逐字一致（只读 requests 的既有列，列集合未变）。
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
