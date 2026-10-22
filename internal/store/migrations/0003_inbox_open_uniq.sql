-- 0003_inbox_open_uniq.sql —— admin_inbox 未读期幂等的部分唯一索引（task-19）。
--
-- 依据：docs/models/01-catalog.md §3.5「幂等要求：同一个 (kind, related_id) 在
-- 未读期间不重复写，否则每轮刷新都会新增一条，一天就是上百条；读到同一变动
-- 第二次时只更新 detail_json 里的最新值，不新建行」。裸 INSERT 直接违反这条，
-- 而收件箱被刷屏后"有限注意力该先放哪"就失效了（01-catalog §151）。
--
-- 为什么是**部分**索引（WHERE read_at IS NULL）：幂等窗口只在未读期间——
-- 管理员标已读之后，同一变动再次发生必须能再开一条新目（那是"又发生了"，
-- 不是重复）。部分索引正好把这个时间窗写进约束本身。
--
-- 为什么 related_id 也要求非空：部分条目没有关联对象（related_id IS NULL），
-- 它们之间不构成重复；SQLite 的唯一索引本来就允许多个 NULL，这里显式写出
-- 是为了让"NULL 不参与去重"成为索引定义的一部分，而不是依赖实现细节。
--
-- 为什么这条迁移**不锁 kind/severity 词表**（不加 CHECK）：四份文档的词表
-- 互斥（02-admin §12 / 06-audit §3.1 / 01-catalog §3.5 / 03-upstream §8），
-- lead 尚未裁决；已发布的迁移改不动，裁决前加 CHECK 就是返工点。索引只按
-- (kind, related_id) 去重，与取值拼写无关——任何裁决结果都兼容，不需要
-- 因为词表变化重建索引。

CREATE UNIQUE INDEX IF NOT EXISTS ux_admin_inbox_open
    ON admin_inbox(kind, related_id)
 WHERE read_at IS NULL AND related_id IS NOT NULL;