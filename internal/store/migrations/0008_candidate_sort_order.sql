-- 0008_candidate_sort_order.sql —— abstract_candidates 增加「声明序」列。
--
-- 为什么需要这一列（2026-10-26 用户决定 + 03-strategy.md §3.3.2）：
--   候选集不设权重了。排序条件（expiry-first / balance / speed / price）
--   次序相同时，按**管理员在候选集编辑器里排列的顺序**决定先后——这是
--   「声明式候选」的必然含义：声明的次序本身就是声明的一部分。
--
-- 为什么不能靠现有列凑：
--   - `weight` 是历史死字段（03-strategy.md §3.3.2：内置四种策略全不读它，
--     界面上的输入框也已删除）。用它排序等于让一个已退役的输入决定线上
--     次序——而且它平时恒为 0，等于让所有候选挤在同一个值上。
--   - `real_model_id` 决胜是行 id 序，与管理员保存的顺序无关。
--   - 本表是 `WITHOUT ROWID`（主键即 (abstract_model_id, real_model_id)），
--     **没有天然插入序**：同一主键前缀的行在 B-tree 里按主键排，插入先后
--     不可见。要让「声明序」可复现，必须显式落一列。
--
-- 回填口径：按旧读序（ORDER BY weight DESC, real_model_id）赋 0,1,2,…，
-- 这样升级那一刻线上次序不变——存量库的行为不因这次迁移而跳变。
--
-- 为什么不加索引：主键 (abstract_model_id, real_model_id) 已覆盖按前导列
-- 过滤的查询（一次 ListCandidates 只取一个模型的候选，量级是几十条），
-- sort_order 的排序在这么小的结果集上全扫即可；为一个几十行的排序建索引
-- 只会让写入多付一次 B-tree 维护。
--
-- 为什么是 NOT NULL DEFAULT 0 而不是可空：可空列让「没排过序」与「排在
-- 第一位」同形，读取方每次都得判一次 NULL，而 NULL 序在 SQLite 里恒排
-- 最前——那正是「没排过序」最不该有的位置。
--
-- 加列类迁移照 0002 的纪律办理：SQLite 的 ADD COLUMN 不支持 IF NOT EXISTS，
-- DDL 与版本行写在同一事务里（migrate.go 的 applyPending），中途崩溃整体
-- 回滚，不存在「列已在、版本行没写」的中间态。不要试图用 pragma_table_info
-- 之类的条件 DDL 绕过去。

ALTER TABLE abstract_candidates ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;

-- 回填：按旧读序给存量行编号。SQLite 的 UPDATE ... FROM（3.33+）在本机
-- SQLite 版本可用；为兼容更老的 SQLite，这里用「相关子查询 + ROW_NUMBER
-- 的等价写法」：先按旧序给每行算出名次，再写回。
--
-- 为什么不用窗口函数 ROW_NUMBER()：迁移文件要在任何 SQLite 3.25+ 上都能跑，
-- 而窗口函数恰好是 3.25 引入的（SQLite 3.25 2018-09）。本项目的最低版本
-- 要求见 docs/architecture/05-storage.md；用「自连接计数」这种更老也能跑
-- 的写法把兼容面压到最宽。
--
-- 计数写法：某行的名次 = 「旧序排在它前面且不是它自己的行数」。
-- 旧序 = (weight DESC, real_model_id ASC)，所以「排在前面」= 权重更大，
-- 或权重相同而 real_model_id 更小。
UPDATE abstract_candidates AS c
   SET sort_order = (
       SELECT COUNT(*) FROM abstract_candidates AS o
        WHERE o.abstract_model_id = c.abstract_model_id
          AND (o.weight > c.weight
               OR (o.weight = c.weight AND o.real_model_id < c.real_model_id))
   )
 WHERE c.abstract_model_id IS NOT NULL;
