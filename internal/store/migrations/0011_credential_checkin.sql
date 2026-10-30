-- 0011: 凭据签到列（定时签到领积分的落库位，2026-10-31）。
--
-- 参照 0010 的口径：签到状态是凭据的**属性**（一条凭据一份，随凭据删除而
-- 消失），直接加列不建表。调度语义参考 dsh-providers-hub 的 auto-checkin：
--   - checkin_text：最近一次尝试的展示串（如 "+200 积分" / "今天已签到" /
--     "签到失败：…"），由 Go 侧把应答格式化；NULL = 从未尝试过。
--   - checkin_date：最近一次**能证明今天已处理**的 UTC+8 日期（YYYY-MM-DD）。
--     ⚠️ 判据不是「跑过」，而是结果能覆盖今天（claimed / already-claimed）：
--     照抄 hub 的 coversToday 口径——失败/活动未开启留空，调度器按退避重试，
--     「上午那轮看到的是昨天的额度」这类渠道差异由渠道自己用 covers_today 标。
--   - checkin_at：最近一次尝试时刻（Unix 秒），失败退避的计时基准；0 = 从未。
--
-- 外键与触发器沿用 0010 的口径：签到落库是调度器的状态记录，不是凭据状态
-- 迁移，不产生审计事件。

ALTER TABLE upstream_credentials ADD COLUMN checkin_text TEXT;
ALTER TABLE upstream_credentials ADD COLUMN checkin_date TEXT;
ALTER TABLE upstream_credentials ADD COLUMN checkin_at INTEGER;
