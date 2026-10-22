# FreeRouter 实现进度快照（2026-10-22 22:2x 恢复用）

> 临时进度锚点，会话恢复时快速对账。内容以仓库 git 状态为准；全部合并完由 Lead 删除。

## main 已合入（HEAD 见 git log，全仓 build/test 绿，338 条 TestContract）

M0–M7 骨架 + M10 脚本：
- 配置/日志/version、L1 中间件链 + v1 三端点/SSE/失败切换/错误映射（ir.CodeToErrorType 唯一源）
- internal/ir（OpenAI/Anthropic 双向编解码）、internal/routing（策略三段式/弃用链/冷却/trace）
- internal/upstream（传输/sidecar 客户端/配额骨架）、internal/store（全仓储+迁移）、internal/auth（token/会话/组策略 fail-closed）
- M7 管理面 45 端点（internal/app/console_routes.go）
- M10 脚本（feat/ops 已合）：security-checklist.sh / backup.sh / restore.sh / smoke.sh + deploy/docker + systemd
- feat/authn 已合：组策略布尔合并 fail-closed + 判定链第 5 步可见性闸接线

## 本轮在途（三个队友并行，分支/worktree 一一对应）

| worktree | 分支 | 任务 | owner |
|---|---|---|---|
| .worktrees/web | feat/web | M9 全量页面（公共/管理/租户/贡献者 + 空态错误态 + 二次确认） | ui-web |
| .worktrees/sidecar | feat/sidecar | M8 hub 真实装载方案 + 贡献流程接凭据加密 | contrib-sidecar |
| .worktrees/storage | feat/storage | M1 relay infer.stream + 配额探测 + store 补方法（admin-api 断点） | upstream-core |

历史 WIP 挂起：.worktrees/admin-api 的 internal/app/admin_bridge_*.go（5 文件，断点在 store 签名：UpsertUpstream 2 vs 3 值、getUpstreamByIDPub 缺失——upstream-core 在补 store 侧）。

## 用户裁决（勿再复查）

1. 额度/限速账户级（daily=0 不可用、tpm=-1 不限、global=0 不熔断）2026-10-20
2. 全局闸排队超时 503 overloaded；账户闸 429 rate_limited 2026-10-21
3. 登录锁定 5 次/15m；IP 50 2026-10-21
4. 取消类级提权；账户 status 字段而定 2026-10-21
5. 02-ir.md 文档债已回写（用户批）；ir-protocol 复核通过（一处 golden 引用已修）
6. admin_inbox 表 M7 用；主密钥 0600 不入库

## 合并门禁（每次合并后跑）

```bash
export GOMODCACHE=$PWD/.cache/gomod GOCACHE=$PWD/.cache/gobuild GOPATH=$PWD/.cache/gopath
go build ./... && go test -count=1 ./... && make contract
python3 scripts/docs_fingerprint.py check --brief
```
