-- 0007_merge_moved_ids.sql —— account_merges 增加「被改挂资源清单」三列。
--
-- 为什么需要（01-accounts §4 回滚范围定案）：文档要求回滚时
-- 「Key / 凭据 / 组关系 改挂回 source」。如果不记录**具体是哪几行**，
-- 回滚就只能靠猜（按时间戳、按名字后缀），而合并后 target 可能又新增了
-- 自己的 Key 与凭据——猜错会凭空造出归属错误的资产，比回滚不全更糟
-- （回滚不全是管理员看得见的，猜错是看不见的）。
--
-- 存 JSON 数组文本而不是建关联表：这三份清单只在「回滚」这一个时刻被整体
-- 读取，没有任何按 id 反查「它属于哪次合并」的需求；关联表要为一次读操作
-- 付出三张表的建表、索引与级联成本。
--
-- NOT NULL DEFAULT '[]'：空数组是合法值（源账户本来没有 Key），
-- 而 NULL 会让回滚代码多一个「读不到」分支去猜。

ALTER TABLE account_merges ADD COLUMN moved_key_ids TEXT NOT NULL DEFAULT '[]';
ALTER TABLE account_merges ADD COLUMN moved_credential_ids TEXT NOT NULL DEFAULT '[]';
ALTER TABLE account_merges ADD COLUMN moved_group_ids TEXT NOT NULL DEFAULT '[]';
