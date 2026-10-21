# 并行开发与协作约定

> 本文档记录 FreeRouter 在**多分支并行开发**阶段的工程约定。
> 它不是设计文档（不描述系统行为），而是「怎么一起改代码」的规则。
> 相关：[里程碑拆解](03-milestones.md)、[PLAN.md](../PLAN.md)

---

## 1. 为什么用 worktree 而不是「切分支」

多人（多智能体）同时改同一个仓库时，`git checkout` 是**全局状态**：
A 切到 `feat/storage`，B 正在 `feat/ir` 上编译的那棵树就变了。表现是
「刚才还通过，现在莫名失败」，而真正的原因是别人换了分支。

`git worktree add` 给每个分支一份**独立的工作目录**，共享同一个 `.git`：

```
FreeRouter/                     ← main，集成用（Lead 独占）
├── .worktrees/
│   ├── ir-protocol/   feat/ir-protocol
│   ├── storage/       feat/storage
│   ├── sidecar/       feat/sidecar
│   ├── web/           feat/web
│   └── authn/         feat/authn
└── .cache/{gomod,gobuild,gopath}   ← 所有 worktree 共享的 Go 缓存
```

`.worktrees/` 在 `.gitignore` 里：它是**本地开发布局**，不是仓库内容。
每个 worktree 的未提交改动不会出现在 `git status`（主库）里——这正是我们要的隔离。

**代价与对策**：同一个分支不能在两个 worktree 里同时检出（git 会拒绝）；
所以「一个分支 = 一个 owner = 一个目录」，交接时先提交再换手。

---

## 2. 分支命名与归属

| 分支 | 作用域（写权限边界） | 内容 |
|---|---|---|
| `main` | 只有 Lead 写 | 集成、发布、文档 |
| `feat/ir-protocol` | `internal/ir/` | IR 与四协议编解码 |
| `feat/storage` | `internal/store/`、`go.mod`/`go.sum` | SQLite、迁移、仓储 |
| `feat/sidecar` | `internal/upstream/dshprovider/`、`sidecar/` | IPC 契约与 Node sidecar |
| `feat/web` | `web/`、`internal/webui/` | 前端骨架与嵌入 |
| `feat/authn` | `internal/auth/`、`internal/meter/`、`go.mod`/`go.sum` | 鉴权与计量 |

**作用域是硬约束**：一个分支只写自己那一格。跨格改动意味着要么走 `main`
（Lead 做），要么先发消息协商——不能「顺手改一下」。

**为什么作用域要这么切**：切分的依据是**变更频率**与**依赖方向**，不是目录好不好看。
`internal/config` 与 `internal/httpapi` 被所有人依赖，所以谁都不许改——
它们只在 `main` 上由 Lead 演进，各分支靠已发布的接口工作。

---

## 3. 提交约定

1. **中文提交信息**，首行 ≤ 50 字，说清「改了什么契约」，不是「改了哪些文件」。
2. **小步提交**：一个能编译、能跑测试的单元就提交一次。理由是并行开发时，
   merge 的冲突粒度取决于提交粒度——一个大提交会把五个人的改动搅在一次冲突里。
3. **每条提交都要能 `go build ./...`**。断掉的中间状态留在工作区，不进历史。
4. **不推送分支**：五个分支统一由 Lead 推送与合并，避免并发推送时的非快进冲突。
5. 测试方法名一律 `TestContract` 前缀——`make contract` 只跑这些，
   它们是**对外行为**的可执行副本（状态码、错误码、接口形状），不是实现细节的镜像。

---

## 4. 沙箱与缓存

本机（及 CI 容器）里 `$HOME/go` 可能只读，`go get` 会报
`mkdir .../pkg/mod/...: read-only file system`。约定：

```bash
export GOMODCACHE=$PWD/.cache/gomod
export GOCACHE=$PWD/.cache/gobuild
export GOPATH=$PWD/.cache/gopath
```

主工作区与 `Makefile` 已经这样设（`?=`，可被环境变量覆盖）。
worktree 里的 `$PWD` 是各自的 worktree，因此**要么导出绝对路径指向主工作区的
`.cache/`，要么接受每个 worktree 一份缓存**（会重复下载几百 MB）。
约定用前者：

```bash
export GOMODCACHE=/home/ylxc/Projects/app/FreeRouter/.cache/gomod
```

---

## 5. 合并顺序与冲突处理

合并顺序由**依赖方向**决定，与完成先后无关：

```
ir-protocol ─┐
storage ─────┼─→ main ─→ authn（需要 storage 的接口）
sidecar ─────┤           web（独立，随时可合）
web ─────────┘
```

- `ir-protocol` / `sidecar` / `web` 相互独立，可任意顺序合。
- `authn` 在自己的分支里用**窄接口**（`internal/auth/storeiface.go`）隔离对 store 的依赖；
  合并后由 Lead 把它对齐到 `internal/store` 的真实仓储方法。
- `go.mod` / `go.sum` 是唯一的多方共享可写文件：`storage` 与 `authn` 都会加依赖。
  **合并后必须 `go mod tidy` 再提交一次**，否则会出现「本地能编、干净检出不能编」。

每次合并后 Lead 跑的门禁：

```bash
make build && make test && make contract
./bin/freerouter --config testdata/config.yaml --check-config
```

---

## 5.1 文档会变：变更检测与响应

**前提：设计文档在实现过程中持续变化。** 用户会新增、修改、删除功能。
代码分支落后于文档是本项目最主要的返工来源，所以把「发现文档变了」做成工具，不靠人留意。

```bash
python3 scripts/docs_fingerprint.py baseline   # 记录当前基线（改动过文档后重记）
python3 scripts/docs_fingerprint.py check      # 对比，退出码 1 = 有变化
python3 scripts/docs_fingerprint.py check --brief   # 可直接贴给队友的提醒
```

它记录每个 `docs/**.md` 的内容哈希与**标题结构**，因此能区分「只改措辞」与
「新增/删除了一个章节」——后者才是功能变更的信号。差异会被映射到受影响的
分支与代码目录（映射表 `DOC_OWNERS` 在脚本里，文档大改时同步更新）。

**响应规则**（每个分支都要遵守）：

| 场景 | 动作 |
|---|---|
| 文档新要求 → 该功能**未实现** | 直接按新文档实现，不实现旧版本 |
| 文档新要求 → 该功能**已实现** | 重构/改实现对齐文档；旧行为不得保留为「兼容分支」 |
| 文档**删除了**某功能 | 已实现的部分要删掉，不是留着不用 |
| 文档自相矛盾 | 按事实源优先级裁决：用户最新指令 > `PLAN.md` > 分册；把冲突点报给 Lead |
| 变更影响了**正在写的代码** | 停下改方向，不要先写完再改 |

**合并前必须重跑一次 `check`**：分支开工时的文档可能已经不是合并时的文档。
这条不是仪式——它正是「返工发生在合并阶段」这个常见失败的堵点。

> 基线存在 `.cache/docs-fingerprint.json`，是本地状态，不入库：
> 每个工作副本（含 worktree）各自记录，避免把「谁看过什么」写进仓库历史。
---

## 6. 集成点由 Lead 独占

`internal/httpapi` 是 L1 中间件链的**唯一装配处**。中间件顺序是契约
（`docs/architecture/01-overview.md` L1），所以插入位置不能由各分支自己决定：

| 里程碑 | 插入的中间件 | 来源分支 |
|---|---|---|
| M3 | 鉴权（Key → account + acting_class + key_id） | `feat/authn` |
| M4 | 限速 + 配额 | `feat/authn`（`internal/meter`） |
| M5 | 协议解析 → IR | `feat/ir-protocol` |
| M6 | 抽象模型解析（含替代链） | 后续里程碑 |
| M9 | 静态资源 + SPA fallback | `feat/web`（`internal/webui`） |

各分支只提供**纯接口**，装配在 `main` 上做。这样「这条链的顺序」始终只有一处定义。

---

## 7. 收尾检查清单

合并完成后、宣布一个里程碑结束前：

- [ ] `git worktree list` 里没有遗留的临时 worktree
- [ ] `git status` 干净（`.cache/`、`.worktrees/`、`bin/`、`data/` 都已被忽略）
- [ ] 仓库里没有 `*.db`、`*.log`、`node_modules/`、`dist/` 残留
- [ ] `make build && make test && make contract` 全绿
- [ ] 相关分册已同步（里程碑完成标准第 4 条）
