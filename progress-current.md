# FreeRouter 实现进度快照（2026-10-22 22:5x 恢复用）

> 临时进度锚点，会话恢复时快速对账。内容以仓库 git 状态为准；M10 收尾后由 Lead 删除。

## main 已合入（HEAD=1f40ff3，13 包 build/test 绿，379 条 TestContract）

M0–M9 主干全部落地：

| 里程碑 | 状态 | 关键点 |
|---|---|---|
| M0 骨架 | ✅ | config/FR_ 覆盖、L1 中间件链、契约测试门禁 |
| M1 上游与配额 | ✅ | relay 流式帧→IR（I1/I4/I5/I6 兜底）、配额到期提取+内存快照、连接池/健康 |
| M2 目录与鉴定 | ✅ | `SetRealModelVerification`（三列）+ `VerifySourceOf` 唯一映射（builtin_vN） |
| M3 账户与鉴权 | ✅ | authz 表驱动判定链、fail-closed、可见性闸、会话/Key/锁定 |
| M4 额度与限速 | ✅ | 账户级额度行化、两层闸（429 账户 / 503 overloaded 全局）、`quota{unit,period,model}` |
| M5 API 出口 | ✅ | v1 三端点+SSE+失败切换+错误映射（`ir.CodeToErrorType` 唯一源）+ **终态结算** |
| M6 路由策略 | ✅ | 三段式/弃用链/冷却/trace；Rank 副本与 speed 键修正 |
| M7 管理面 | ✅ | 45 端点（internal/app/console_routes.go） |
| M8 贡献与 sidecar | ✅ | hub 深路径装载实测、贡献四态判定、撤销墓碑、stderr 遮蔽 |
| M9 界面 | ✅ | 46 路由（IA 4.1–4.5）、四态、二次确认、权限派生导航、46/46 真机 smoke |
| M10 加固 | ✅ 实跑通过 | 安全基线 C1–C12 全 PASS（S2/S8/S11 按设计 SKIP）；在线 VACUUM INTO 备份 + dry-run 三重断言 + 真恢复演练（删库→恢复→200→迁移4条）；`--verify-store` 自检；空库→setup init→登录→会话恢复全流程；umask 0077 根治库文件 0644 |

## 在途 / 待办

| 事项 | owner | 状态 |
|---|---|---|
| code-review 复验（verification_writer、settle_bridge、routing、M9） | code-review | 排队 |
| 贡献测试 WAL 断言强化 + 遮蔽正则左边界 | contrib-sidecar | 返工中 |
| M10 实跑 | Lead | ✅ 完成（压测与 govulncheck 留 CI） |
| 结算 `ResolvedCredentialID` 留空（待 M8 凭据回传） | Lead 集成 | 登记 |
| `config.sidecar.deps_root` → sidecar 子进程 env 接线 | Lead | 登记（当前自动枚举可用） |
| 租户/贡献者/注册端点 docs/api 分册定稿 | 文档待办 | 登记 |

## 用户裁决（勿再复查）

1. 额度/限速账户级（daily=0 不可用、tpm=-1 不限、global=0 不熔断）2026-10-20
2. 全局闸排队超时 503 `overloaded`；账户闸 429 `rate_limited` 2026-10-21
3. 登录锁定 5 次/15m；IP 50 2026-10-21
4. 取消类级提权；账户 status 字段而定 2026-10-21
5. `verify_source` 保持可空（NULL=从未鉴定），不加 DEFAULT 2026-10-22
6. 结算两半非同事务，对账线索随告警输出 2026-10-22

## 合并门禁（每次合并后跑）

```bash
export GOMODCACHE=$PWD/.cache/gomod GOCACHE=$PWD/.cache/gobuild GOPATH=$PWD/.cache/gopath
go build ./... && go test -count=1 ./... && make contract
python3 scripts/docs_fingerprint.py check --brief
```
