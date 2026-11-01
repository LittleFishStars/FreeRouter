-- 0015_abstract_nested_candidates.sql —— 抽象模型可以把其他抽象模型加为候选。
--
-- 需求（2026-11-01 用户提出）：抽象模型的候选集除了真实模型，还应能加入
-- 其他抽象模型——「先试 A 组里排第一的，不行再退到 B 组」这类分层回退，
-- 不必把 B 的候选手工抄进 A（抄副本会在 B 改配置后悄悄失真）。
--
-- 循环依赖的治理放在**两道闸**：
--   1. 保存时（管理桥 putCandidates）：把这条候选加进去之后还会成环 → 拒绝；
--   2. 选择时（routing expandCandidates）：visited 游走，会成环的抽象候选
--      直接隐藏（从展开结果里剔除，不报错）——与弃用链的降级哲学一致：
--      手工改库绕过保存路径的环不该变成用户可见的 500。
-- 管理端选择列表的「会成环的项隐藏」由 blocked_abstract_ids 提供（见
-- abstract-models 详情端点），前端据此置灰/隐藏。
--
-- 存储形状：abstract_candidates 复用原表，新增 candidate_kind 列：
--   real     = 候选是真实模型（real_model_id 存 real_models.id，原有语义）；
--   abstract = 候选是抽象模型（real_model_id 列存 abstract_models.id）。
-- 列名不改（改列名是第二次表重建，没有收益）：它是「候选指向的目标 id」，
-- 语义按 kind 分支解释。外键从 real_models 上摘除——同一列在两种 kind 下
-- 指向两张表，SQL 外键表达不了联合判据，存在性校验收归应用层（保存路径
-- 本来就逐条校验，见 docs/models/02-abstract.md §3.4）。
--
-- 重建表的原因：SQLite 的 ALTER TABLE 不能摘列（外键在列定义上），只能
-- 建新表搬数据。搬数据规则：
--   abstract_candidates.real_model_id 若在 abstract_models 有同 id 行 →
--   不可能有（2026-11-01 之前 real_model_id 全部是真实模型 id，两表主键
--   都是 UUIDv7，空间不相交）→ 存量行全部是 real，kind 恒 'real'。

CREATE TABLE abstract_candidates_new (
    abstract_model_id TEXT    NOT NULL REFERENCES abstract_models(id) ON DELETE CASCADE,
    -- 候选目标 id：kind=real 时是 real_models.id；kind=abstract 时是
    -- abstract_models.id。指向自身永远非法（应用层拒绝，DB 无法表达跨列判据）。
    real_model_id     TEXT    NOT NULL,
    candidate_kind    TEXT    NOT NULL DEFAULT 'real'
                      CHECK (candidate_kind IN ('real','abstract')),
    constraint_json   TEXT,                    -- 候选级约束（渠道范围等）
    weight            INTEGER NOT NULL DEFAULT 0,
    sort_order        INTEGER NOT NULL DEFAULT 0,   -- 声明序：平局决胜按它
    PRIMARY KEY (abstract_model_id, real_model_id, candidate_kind)
) WITHOUT ROWID;

-- 搬数据：历史行全是真实模型候选。
INSERT INTO abstract_candidates_new
       (abstract_model_id, real_model_id, candidate_kind, constraint_json, weight, sort_order)
SELECT abstract_model_id, real_model_id, 'real', constraint_json, weight, sort_order
  FROM abstract_candidates;

DROP TABLE abstract_candidates;
ALTER TABLE abstract_candidates_new RENAME TO abstract_candidates;
CREATE INDEX IF NOT EXISTS ix_abstract_candidates_real ON abstract_candidates(real_model_id);
