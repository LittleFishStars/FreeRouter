-- 0014: 抽象模型手动排序定案 + 不可用模型台账（2026-11-01 用户定案）。
--
-- 两件事，一次迁移：
--
-- 1) 退役 abstract_models.strategy 列。
--    四种自动排序策略（expiry-first / balance / speed / price + round-robin
--    平局决胜）整体退役：候选的使用顺序改为**管理员手动排序**——候选集
--    编辑器里排列的声明序（abstract_candidates.sort_order）就是使用序，
--    仅当前面的候选不可用时才使用下一个。排序链不再有任何运行时语义，
--    列留着只会让「删了功能却还在表里」变成下一次排查的陷阱。
--    SQLite 3.35+ 支持 DROP COLUMN（本项目用 modernc.org/sqlite 内嵌
--    SQLite 3.53，版本由 Go 依赖固定，不随系统变动）；该列无索引、无
--    触发器、无视图引用，可以安全摘除。
--
-- 2) 新增不可用模型台账 model_unavailability。
--    流量中判定为上游侧不可用的候选落库记账（原因 / 诊断 / 连续失败次数 /
--    下次检测时刻），后台按指数退避（1m→2m→4m…封顶 1h，到顶后每 1h 一次）
--    用真实推理 ping 检测是否恢复，恢复即删行。为什么落库而不是进程内：
--    退避排期是小时级，重启不应把排期清零重新从 1m 开始探测——那等于把
--    「上游还没好」的事实丢掉，让每个进程重启都白打一轮探测。
--
--    为什么主键是 real_model_id：不可用的作用域是「这条上游路径」（与
--    冷却账本同口径，见 internal/routing/cooldown.go）——同名模型可能挂
--    在多个上游下，一条路径坏了不牵连其它路径。
--
--    为什么不设 status 列：台账行只有「在册 = 不可用」一种状态，恢复即
--    删行；用行存不存表达状态，与「不设池 = 没有这一行」的存储哲学一致
--    （05-storage.md §3.3）。reason 只记最近一次的原因（诊断价值），不
--    做历史——失败历史在请求日志里。

-- ① 退役排序链列。
ALTER TABLE abstract_models DROP COLUMN strategy;

-- ② 不可用台账。
CREATE TABLE IF NOT EXISTS model_unavailability (
    real_model_id TEXT    NOT NULL PRIMARY KEY
                  REFERENCES real_models(id) ON DELETE CASCADE,
    reason        TEXT    NOT NULL,        -- 稳定原因码（上游错误类）
    detail        TEXT,                    -- 人读诊断（脱敏后，仅供管理端）
    failed_at     INTEGER NOT NULL,        -- 最近一次判定不可用时刻（Unix 秒）
    attempts      INTEGER NOT NULL DEFAULT 1, -- 连续判定不可用次数（退避输入）
    next_check_at INTEGER NOT NULL         -- 下次探测时刻（Unix 秒）
);
-- 探测循环每 tick 按到点扫描：next_check_at 上的索引让扫描退化为一次
-- 范围查询。台账行数 = 不可用路径数（通常个位数），索引更多是纪律。
CREATE INDEX IF NOT EXISTS ix_model_unavailability_next
    ON model_unavailability(next_check_at);
