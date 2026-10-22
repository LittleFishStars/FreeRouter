-- 0006_account_must_change_password.sql —— accounts 增加「首次登录必须改密」标志。
--
-- 为什么需要这一列（2026-10-23 首启随机口令定案）：
--   实例首次启动时不再让用户先填向导，而是由进程随机生成一次性口令、创建
--   首个 admin 并打印到终端。这个口令是**系统给的**，不是用户自己选的——
--   登录后必须换成自己的口令（并补全显示名），否则「随机口令」会长期留在
--   终端滚动缓冲与 initial-password.txt 里，成为一条长期有效的弱凭据。
--
-- 为什么是账户级布尔而不是会话级：改密要求属于「凭据生命周期」的账户事实，
--   同一账户在任何设备上登录都要满足它；只挂在一次会话上会被「换个浏览器
--   登录」绕过。
--
-- 为什么 NOT NULL DEFAULT 0：存量账户与向导自建账户都不需要强制改密，
--   默认 0 让迁移可安全追加；NULL 会让「未设置」与「不需要」在判定处分叉。
--
-- 为什么加 CHECK：该列只允许 0/1（SQLite 无布尔类型），把值域收在 DDL 里
--   与 accounts.status 的 CHECK 同一条纪律——写错值应当在写入时失败。
ALTER TABLE accounts ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0
    CHECK (must_change_password IN (0, 1));
