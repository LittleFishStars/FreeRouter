# 变更日志

本文件格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。
项目尚未发布，版本号自 `v0.0.x` 起；**里程碑**（M0–M10，见 docs/ops/03-milestones.md）
作为开发期的分组单位。

约定：凡属「运维会感知为行为变化」的改动（默认值语义、错误体形状、存储迁移），
必须记入本文件的 Unreleased → 变更 一节，注明旧行为、新行为与原因。

---

## [Unreleased]

### 新增

- M0 骨架：配置（YAML + `FR_` 环境变量覆盖）、L1 中间件链（请求 ID / 安全头 / 可信代理 / body 上限）、
  `/healthz` `/readyz` `/api/version`、优雅关闭；`make contract` 契约测试门禁。
- sidecar 契约层：Go 客户端（stdio+NDJSON 一问一答、超时杀进程、崩溃降级不影响转发）
  与 Node sidecar；红线 `credentials.set/unset/refresh` 不存在且契约测试可执行断言。
- 存储层：SQLite(WAL) 迁移机制、主密钥 0600 校验、八类仓储、账户额度/组池的
  条件 UPDATE 扣减与惰性重置（`ErrQuotaExhausted` 携带打满行标识与 `ResetAt()`）。
- 前端骨架：Vite+React 表驱动路由 + `go:embed` 双形态（未构建时裸 `go build` 通过，
  发布二进制带 `-tags webui_dist`）。

### 变更（运维需感知）

- **`limits.default_tpm` 默认值 `0 → -1`，且「不限」的表示从 `0` 迁移到 `-1`**
  （2026-10-20 定案，与 `accounts` 速率列同一套哨兵：`-1` = 不限、`0` = 全拒）。
  升级后若你的旧配置写了 `default_tpm: 0`，行为会从「不限」变为「全拒」——
  请显式改写为 `-1`。这不是 bug 修复而是语义定案（旧「0=不限制」已废除）。
- 额度域全面行化（2026-10-20 两轮定案）：账户周期额度从 `accounts` 三列迁移到
  `account_quotas` 行（合计行 + 按模型限额行，「不限」= 无行）；`api_keys` 不再
  持有额度/速率列；`usage_daily` 从按 Key 聚合改为按账户聚合；组池与按模型上限
  新增 `credit` 维度与 `abstract_model_id` 维。首次启动将按新迁移建表。
- `quota_exceeded` 错误体新增机器可读 `quota{unit, period, model?}` 对象，
  `Retry-After` 指向命中行的重置时刻（锚点脏数据时宁缺勿错）。
- **全局并发闸排队超时改用 503 `overloaded`**（2026-10-21 定案）：旧行为归入
  429 `rate_limited`，与账户/Key 限速同码；新行为拆开——`rate_limited`（429）
  归因“你超了上限”，`overloaded`（503 + `Retry-After: <queue_timeout>`）归因
  “服务容量饱和，与你是谁无关”，避免按码运营的告警被容量问题污染。
  `ErrGloballyFused`（全局预算熔断）仍为 429 `quota_exceeded`（与分册 §3.1 一致）。
  `Retry-After` 指向命中行的重置时刻（锚点脏数据时宁缺勿错）。

### 废弃

- 类级提权（step-up）已于设计层废除（2026-10-20）：登录一次即账户全部权限，
  会话 24h 为唯一时效边界。
- `RelayTransport.Open` 与 `OpenFromPayload` 已删除（2026-10-22）：无生产调用方，
  且拆完信封拿到的是 OpenAI SSE 而 `relayPayloadToNDJSON` 期望 relay 帧——
  留着只会让下一个人得到「0 事件 + 协议错」。IR 原生流式面需要时走
  `Caller.InferStream` + 协议 `NewStreamReader`。

### 验证结论（M10 实跑，2026-10-22）

- 安全基线 C1–C12 全 PASS（脚本 `scripts/security-checklist.sh`）；S2（伪造 XFF）
  与 S8（429 Retry-After 压测）留 CI，S11 已本机跑 `govulncheck`：**0 可调用漏洞**。
- 在线 `VACUUM INTO` 备份 + dry-run 三重断言 + 删库恢复演练通过；`--verify-store`
  自检可用（库不存在时拒绝，避免把空库误判为可用）。
- 空库 → `POST /api/setup/init` → 登录 → 会话恢复 全流程通过。
- 轻量压测：16 并发 × 400 请求，**3851 rps / p95 7ms / p99 8.4ms**，RSS +4.4MB
  （17.8MB → 22.2MB）。
- TLS `files` 模式实跑：HTTPS 200、HSTS `max-age=31536000`、明文请求被拒（400）。
