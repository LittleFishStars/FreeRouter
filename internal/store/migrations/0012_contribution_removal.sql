-- 0012: 移除（撤销）标记列（2026-10-31）。
--
-- 「移除供应商」的落库语义是**留行不留钥**：上游行/凭据行必须保留——
--   - requests.resolved_credential_id / resolved_real_model_id 的归因链靠行，
--     外键（RESTRICT）也不允许删被引用过的行；
--   - 移除销毁的是**密钥材料**（墓碑覆写密文）与**候选池资格**（enabled=0 +
--     removed_at 把行从各列表/来源聚合里滤掉）。
--
-- 两列都是可空时间戳：NULL = 在册；非 NULL = 已移除（不可逆——移除不是停用，
-- 恢复的唯一途径是重新提交/重新授权，那会走完整鉴定流）。
--
--   - upstreams.removed_at：管理端「移除供应商」与贡献者「移除贡献」都置位；
--     凭据池来源聚合、可见真实模型、OAuth 行对齐都以它过滤。
--   - upstream_credentials.removed_at：贡献者列表以它过滤（移除后自己的
--     贡献清单里不再出现）；管理端凭据列表不过滤（审计面，看到 disabled）。
ALTER TABLE upstreams ADD COLUMN removed_at INTEGER;
ALTER TABLE upstream_credentials ADD COLUMN removed_at INTEGER;
