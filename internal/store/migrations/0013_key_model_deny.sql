-- 0013: Key 的模型过滤从「白名单」改为「黑名单」（2026-11-01 用户定案）。
--
-- 动机：白名单是默认拒绝——每把新 Key 都要先勾模型才能用，日常把 Key 当
-- 「全池入口」（auto / 组授权全集）时是纯摩擦。黑名单反转方向：空 = 不收窄
-- （组授权全集直接可用），列出的模型才对这把 Key 隐藏。组授权仍然是天花板
-- （三层收敛的另外两层不变）。
--
-- 表改名 key_model_allow → key_model_deny；**清空存量行**：白名单行在黑名单
-- 语义下会反转成「这些模型被拒」，与升级前「这些模型可用」恰好相反——带上
-- 旧数据等于把每把 Key 的可用集合打反。清空后所有 Key 从「不收窄」起步，
-- 需要按 Key 拒绝某些模型的场景重新配置（显式动作，符合新语义）。
ALTER TABLE key_model_allow RENAME TO key_model_deny;
DROP INDEX IF EXISTS ix_key_model_allow_model;
CREATE INDEX IF NOT EXISTS ix_key_model_deny_model ON key_model_deny(abstract_model_id);
DELETE FROM key_model_deny;
