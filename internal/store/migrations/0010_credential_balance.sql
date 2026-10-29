-- 0010: 凭据余额列（凭据池「刷新余额」功能的落库位）。
--
-- 为什么加在凭据行而不是单独的余额表：余额是凭据的**属性**（一条凭据一份
-- 余额，随凭据删除而消失），一比一归属不需要独立表；查询路径也与凭据列表
-- 同源（列表页直取，不用二次 JOIN）。
--
-- 为什么只存「文本 + 时间戳」而不是结构化分项：各家上游的余额单位/分项形状
-- 互不相同（积分/credit/USD，多池/单池），结构化建模需要每家一张子表——
-- 显示层只需要一句话（"1,234 积分"），分项详情查上游实时接口（刷新动作）。
-- balance_text 由 Go 侧把上游应答格式化成展示串；NULL = 从未查询过。
--
-- 外键与触发器沿用 0009 的口径：upstream_credentials 的追加式审计触发器
-- 只覆盖 status/verify_status 变更列，余额列不在其上（余额刷新是只读查询
-- 的落库，不是状态迁移，不产生审计事件）。

ALTER TABLE upstream_credentials ADD COLUMN balance_text TEXT;
ALTER TABLE upstream_credentials ADD COLUMN balance_checked_at INTEGER;
