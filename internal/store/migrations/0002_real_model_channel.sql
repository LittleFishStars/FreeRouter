-- 0002_real_model_channel.sql —— real_models 增加渠道列并回填。
--
-- 为什么需要这一列（01-catalog.md §2.3 映射表）：
--   上游 `owned_by` → 派生「渠道」，限定名里的渠道段取自这里。渠道是**模型级**
--   属性（实测 20 个：deepseek-official / aggregate / cline …），与上游级标识
--   （relay 的 provider 名、vendor 的行名）不是一回事：同一个 deepseek 模型在
--   relay 与 vendor 两个上游下是两条 real_models 行，渠道相同但上游不同。
--   路由层此前用 channelOf(upstream) 算渠道，得到的是上游级值，与文档口径不符。
--
-- 为什么不能用 group_name 顶替：01-catalog.md:167 定义 group_name 是管理员自由
-- 填的「目录分组」（人工展示字段，改过即冻结、刷新不覆盖）。它是人的意志，
-- 渠道是上游事实，两者混用会让"按渠道分组"在人工改过标签后静默失真。
--
-- 为什么允许 NULL：vendor/selfhosted 的目录条目不保证带 `渠道/模型` 形态，
-- 派生不出来就是 NULL（表达"未知"），而不是回退到上游级值——回退会让同一列
-- 在不同行里混着两种语义。消费侧（routing_bridge）的兜底由调用方决定。
--
-- 为什么允许 NULL：vendor/selfhosted 的目录条目不保证带 `渠道/模型` 形态，
-- 派生不出来就是 NULL（表达"未知"），而不是回退到上游级值——回退会让同一列
-- 在不同行里混着两种语义。消费侧（routing_bridge）的兜底由调用方决定。
--
-- 为什么这条迁移没有 IF NOT EXISTS（SQLite 的 ADD COLUMN 不支持）：0001 里的
-- "全部 IF NOT EXISTS" 冗余防的是"崩溃在版本行写入之前"——但 DDL 与版本行写在
-- **同一个事务**里（migrate.go 的 applyPending），中途崩溃整体回滚，不存在
-- "列已在、版本行没写"的中间态。真正会重放的只有人为删版本行或手工执行半个文件，
-- 那属于操作事故，不该靠把 DDL 写成合法但不可靠的形式来掩盖。
--
-- 加列类迁移请照此办理，不要试图用 `SELECT count(*) FROM pragma_table_info` 之类
-- 的条件 DDL（SQLite 不支持，绕过去只会得到更难排查的语法/语义分叉）。
--
-- 回填为什么与加列同一条迁移：迁移纪律要求"已应用的不可改、重开只能追加"，
-- 但同一次迁移内部可以既 DDL 又 DML。若拆成 0002/0003，0003 回填时会读到
-- 已被 0002 加列的库，语义相同却多一次版本往返。

ALTER TABLE real_models ADD COLUMN channel TEXT;

-- 回填存量：只对带 `渠道/模型` 形态的 id 派生，且渠道段必须非空。
-- 精确取第一个 `/` 之前的部分（SQLite 的 instr + substr，不引 rtrim/instr 三参
-- 变体以免与其他方言混淆）。vendor/selfhosted 的裸 id 留 NULL。
UPDATE real_models
   SET channel = substr(upstream_model_id, 1, instr(upstream_model_id, '/') - 1)
 WHERE channel IS NULL
   AND instr(upstream_model_id, '/') > 1;

-- 渠道列要支撑管理端"按渠道分组"与路由侧过滤，索引加在 (channel, status)：
-- 两个都是常驻过滤条件（渠道看分组、status 剔 missing/disabled）。
CREATE INDEX IF NOT EXISTS ix_real_models_channel_status ON real_models(channel, status);