-- 0004_real_model_verify.sql —— real_models 加目录侧鉴定三列（task-24，M2 增量）。
--
-- 依据（lead 裁决，sidecar 三条证据性意见全部成立）：
--   1. 目录侧鉴定的落库面此前只有 upstream_credentials（repo_verification.go 的
--      MarkVerified 按凭据 id 定位）。把 real_model 的鉴定写进凭据行会产生
--      N×M 互相覆盖——一条凭据服务多个模型，一次目录刷新对同一条凭据写 N 个
--      互相覆盖的结论，后写的赢，前一个模型的 suspect 证据被冲掉。
--      这不是语义别扭，是数据丢失。
--   2. 两层鉴定回答不同的问题：凭据行的 verify_status 是 L1（这把 key 是不是
--      它声称的模型，贡献流程）；本列是目录侧元数据指纹（目录条目与指纹表
--      符不符）。后者挂 real_models 与 01-catalog §2.3 的字段映射同构。
--   3. 05-verification §6.2：判定可追溯必须记指纹库版本——列存才能被 SQL
--      直查「哪些行是 v1 判的」，指纹库升级后也才能圈出重判范围。
--
-- verify_status 与凭据行**同形四态**（同一套 CHECK 枚举，Go 侧共用
-- repo_verification.go 的 VerifyStatus 类型与 valid()，不另造枚举）。
--
-- verify_detail 存 evidence 原文（sidecar 的 evidence 是纯字符串，不强制 JSON；
-- 与凭据侧 verify_detail 的「JSON 而不拆列」理由并存——那边证据结构会演化，
-- 这边的 evidence 是给人读的判定说明）。
--
-- verify_source 存指纹库版本标识（如 builtin_v1）。
-- **NULL 与 'unverified' 是两回事，这条区分是本迁移的核心语义**：
--   NULL          = 从未鉴定过（连参考指纹都没有——冷门模型在指纹表扩充前）；
--   'unverified'  = 跑过判定但无参考指纹（已进复检队列，指纹库升级后要重判）。
--   两者的运维动作不同：NULL 没东西可重判，unverified 有。混成一个值会让
--   复检队列把从未鉴定的行也拉进来白跑，或反过来漏掉该重判的行。
--
-- 为什么 NOT NULL DEFAULT 'unverified' 而不是 NULL 默认：0001 已发布的表里
-- 已有行是「目录刷新落下来的」，它们确实跑过判定吗？没有——但 sidecar 的
-- 判定函数对无指纹返回 unverified（默认放行语义），所以存量行的真实状态
-- 就是「按无指纹规则处理过」。把 DEFAULT 设成 unverified 而非 NULL，等于
-- 声明「凡在池中的行都至少过过一遍判定」——但必须注意：这条语义只有在**存在写入方**
-- 时才成立。0004 落地时刷新联动尚无写回实现，存量与新增行会停在 DEFAULT 上；
-- task-3 已补 upstream.StoreVerificationWriter（→ ModelRepo.SetRealModelVerification）
-- 作为唯一写入路径，巡检「三列无写入方」的阻塞项由此消解。
--
-- **NULL 与 'unverified' 的区分落在 verify_source/verify_detail 上**（verify_status
-- 因 NOT NULL DEFAULT 永远有值）：
--   verify_source IS NULL       = 从未鉴定（连判定入口都没走到）；
--   verify_source = 'builtin_vN' 且状态为 unverified = 跑过判定但无参考指纹（进复检队列）。
-- 真正的 NULL 窗口只剩「装配了 verification 但尚未跑第一轮」或人工直插的行。

ALTER TABLE real_models ADD COLUMN verify_status TEXT NOT NULL DEFAULT 'unverified'
    CHECK (verify_status IN ('verified','suspect','unverified','trusted'));
ALTER TABLE real_models ADD COLUMN verify_detail TEXT;
ALTER TABLE real_models ADD COLUMN verify_source TEXT;
-- verify_source 的文本形态由 Go 侧**唯一映射点** upstream.VerifySourceOf(version)
-- 生成（builtin_v1 / builtin_v2…）。列类型是 TEXT 而不是 INTEGER：版本标识要能与
-- 未来的其他来源（external_yaml/v2、人工 trusted）区分。改映射格式时同步该函数
-- 与本注释，不要在装配层另写 fmt.Sprintf。

-- 按指纹库版本圈重判范围（05-verification §6.2）：指纹库升级后
-- 「哪些行是旧版判的」要一条 SQL 就能回答；不建索引——这是低频运维查询，
-- 而 real_models 是每轮刷新都写的表。
CREATE INDEX IF NOT EXISTS ix_real_models_verify_source ON real_models(verify_source);
