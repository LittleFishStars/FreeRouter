> 分册：运维 · 依赖：[01-security.md](01-security.md)、[02-deploy.md](02-deploy.md) · 对应总纲：PLAN.md 第 9 章

# 静态分析与扫描基线

本分册记录项目使用的静态检查工具、当前基线，以及**每类剩余告警的评估结论**。

**为什么需要这份文档**：静态扫描的价值在于「零告警」这个状态本身——一旦告警
变成长期背景噪音，新引入的真问题就会被淹没。而要做到零告警，有两种错法：
一种是关掉规则（真问题也一起消失），另一种是给代码塞满 `#nosec`（噪音换成了
注释噪音）。本文件的做法是：**能修的修掉、确认误报的写明理由**，让每条剩余
告警都可追溯到一次判断。

---

## 1. 工具与基线

| 工具 | 用途 | 当前基线 | 怎么跑 |
|---|---|---|---|
| `go build` / `go vet` | 编译与语言级检查 | 0 问题 | `make vet` |
| `gofmt` | 格式 | 0 未格式化 | `make fmt` |
| `staticcheck` | 深度语义检查（未使用代码、无效比较等） | **0 问题** | 见下 |
| `gosec` | 安全模式扫描 | 42 项（全部经评估，见第 3 节） | 见下 |
| `govulncheck` | 依赖漏洞（可达性分析） | 代码 0 漏洞 | 见下 |
| `dupl` | 重复代码（80 token 以上） | 6 组（均为 REST 列表/详情样板，判定保留，见 3.8） | `.cache/tools/dupl -t 80 cmd internal` |
| `go mod tidy` | 依赖整洁 | 无变化 | `make tidy` |

工具装在仓库内（`GOMODCACHE` 指向 `.cache/gomod`，避免污染用户级模块缓存）：

```bash
export GOCACHE=$PWD/.cache/gobuild GOMODCACHE=$PWD/.cache/gomod \
       GOBIN=$PWD/.cache/bin XDG_CACHE_HOME=$PWD/.cache GOSUMDB=off
go install honnef.co/go/tools/cmd/staticcheck@latest
go install github.com/securego/gosec/v2/cmd/gosec@latest
go install golang.org/x/vuln/cmd/govulncheck@latest
go install github.com/mibk/dupl@latest

.cache/bin/staticcheck ./...
.cache/bin/gosec -quiet -exclude-dir=.cache ./...
.cache/bin/govulncheck ./...
```

> **为什么 `GOMODCACHE` 要改**：本机 `~/.local/share/go/pkg/mod` 只读，
> 不改会因 `mkdir ... read-only file system` 失败。`GOSUMDB=off` 同理
> （sumdb 缓存目录不可写）。CI 环境不需要这两项。

---

## 2. 已修复的问题（2026-10-24 一轮）

这一轮扫描修掉的都不是"风格问题"，而是会真实影响运行的点：

| 问题 | 后果 | 处置 |
|---|---|---|
| `strconv.FormatInt(x, 0)` × 4（`internal/app/policy_bridge.go`） | base=0 非法，**直接 panic**：`illegal AppendInt/FormatInt base`。触发路径是「组配了按模型上限/共享池」或「Key 鉴权走 keyRowToAuth」 | 改 base=10，补两条回归测试；已验证改回 0 时测试能捕获 |
| 关闭时 `context.WithTimeout` 的 cancel 未调用（`cmd/freerouter/main.go`） | timer 要等超时才回收；`go vet` 的 lostcancel 报 | `defer stopCancel()` |
| `math/rand` 用于退避 jitter 但注释写成 `//nolint:gosec` | 注释是 dead code（gosec 认的是 `#nosec`），规则实际没被抑制 | 改为 `#nosec G404` 并写明「只用于错开重试时刻」 |
| `uint32(len(hash))` 无边界检查（`internal/auth/password.go`） | 超长哈希会截断，`KeyLen` 与真实字节数不一致 → `Verify` 静默失败而非报错 | 加 `len(hash) > math.MaxUint32` 检查 + `#nosec G115` 说明 |

另有 12 处生产死代码与 6 处测试死变量删除（名单见 CHANGELOG）。

---

## 3. 剩余告警的评估结论

gosec 42 项，按规则分组如下。**下表每一行都是"看过代码后判定可接受"**，
不是"先放着"。

### 3.1 G104 · 未检查的错误（20 项）

**判定：可接受，不加忽略注释。**

绝大多数是 `defer f.Close()` / `_ = w.Write(...)` / `rows.Close()` 这类
「失败也无补救动作」的调用。对它们的正确处理是显式 `_ =`（本项目已在用）
或记日志，而不是把错误往上抛——关闭一个已经完成的只读文件失败，没有
上层能做的事。

> 例外检查：如果哪条 `_ =` 出现在**写入类**操作上（例如 `_ = tx.Commit()`），
> 那是真问题。本轮逐条看过，20 项里没有这种。

### 3.2 G101 · 疑似硬编码凭据（11 项）

**判定：全部误报。**

命中的是错误码常量（`CodeCredentialExpired = "E_CREDENTIAL_EXPIRED"`）、
状态枚举（`InboxChannelFailing = "channel_failing"`）、策略键
（`PolicyQuotaTokenDay = "quota_token_day"`）、操作名
（`OpDeleteOwnCredential = "delete_own_credential"`）与 SQL 列名常量。

gosec 的 G101 靠「变量名含 credential/secret/password + 字符串字面量」判定，
把命名里的 `Credential` 当成了凭据。这些值会出现在错误体与日志里，
本来就是公开的。

> **真正的凭据在库里且是密文**：上游密钥用 AES-GCM 存储，主密钥来自
> `master.key`（0600）。代码里没有任何真实凭据字面量——这一点在
> 2026-10-23 的 push protection 事件里被 GitHub 独立验证过（当时命中的
> 是 vendored 上游代码，不是本项目）。

### 3.3 G304 · 变量文件路径（5 项）

**判定：可接受。**

命中：配置文件加载（`config.go`）、主密钥文件（`masterkey.go` ×2）、
审计导出（`repo_log.go` ×2）。

这些路径全部来自**管理员配置或服务端派生**，不接受 HTTP 输入。文件包含
类漏洞的前提是「攻击者能控制路径」，这里不成立。

### 3.4 G204 · 子进程（2 项）

**判定：可接受。**

命中：`openBrowser`（打开控制台 URL）与 sidecar 启动。

命令本身由我们自己构造（浏览器名来自 `runtime.GOOS` 分支，sidecar 入口来自
配置）。参数里没有拼接请求内容。**注意**：`openBrowser` 的 URL 来自
`consoleURL(cfg, ln)`，是本地监听地址，不含用户输入。

### 3.5 G201 / G202 · SQL 拼接（2 项）

**判定：可接受，且注释已说明原因。**

- `verify.go:49`：`SELECT count(*) FROM %s` 的表名来自**编译期常量表**
  `keyTables`，不是外部输入。参数化不能用于表名，这里用常量白名单是正确做法。
- `migrate.go:267`：`VACUUM INTO '...'` **不接受占位参数**（SQLite 语法限制），
  目标路径经 `quoteLiteral` 转义单引号。目标路径来自备份配置，非 HTTP 输入。

### 3.6 G705 · 写未转义内容（2 项）

**判定：误报。**

命中 `w.Write(out)` 与 `w.Write(rows)`。`out` 是 `json.Marshal` 的结果、
`rows` 是 JSONL 导出的字节流，Content-Type 均为 `application/json` /
`application/x-ndjson`，不是 `text/html`。G705 针对的是 HTML 上下文。

### 3.7 已消除的规则

`G115`（整数截断）、`G118`（context 泄漏）、`G404`（弱随机）已在前一节
修复或显式标注，不再出现在基线里。

### 3.8 `dupl` · 重复代码（6 组）

**判定：结构性样板，保留。** 逐组看过：

- 管理面列表 handler（`need → limit/offset → List → render → envelope`）与详情
  handler（`need → 取 id → Get → mapAdminError → writeJSON{data}`）——这些是 REST
  端点的**天然形状**。提到泛型 helper 能把 12 行压成 3 行，但每个 handler 里
  都有一段特有逻辑（账户详情补两个计数、组详情把 `policies/members/models` 的
  nil 归一成空对象、票据/收件箱多一步审计），闭包化之后要读两处才看得懂一条
  端点，与「每个 handler 可独立对照分册」的写法相悖。
- 测试里的同构断言（`v1_test.go`、`routing_bridge_test.go`、`settle_bridge_test.go`）
  ——测试的重复是「同一种失败要各自能独立读懂」，合并成表驱动反而要跳两屏。

**真正该合的已合**：仓储层的「读一列 int64」（7 处）与 id 参数校验（4 处）
已经收敛（见第 2 节），它们的重复有**漂移风险**（漏 `rows.Err()`、前缀与 msg 不同步），
和上面这些“没有第二份判断”的样板不是一个性质。

---

## 4. 测试覆盖基线

```
internal/idgen            100.0%   internal/apidir            87.0%
internal/upstream          82.2%   internal/routing           82.4%
internal/upstream/dshprovider 80.5%   internal/auth          67.9%
internal/ir                58.5%   internal/config           59.6%
internal/meter             55.5%   internal/store            52.7%
internal/httpapi           49.0%   internal/app              25.2%
internal/ir/openai          7.8%   internal/ir/anthropic      2.6%
```

**怎么读这张表**：

- `internal/idgen` 是 2026-10-26 UUID 改造新增的包，取代已删除的 `internal/idcode`
  （旧基线里的 idcode 100.0% 就是它的前身），New/Valid/Parse/MustParse 四条路径
  都有用例，含"非法装配期常量必崩"与"异形态输入必被拒"两侧。
- `internal/app` 偏低是**结构性的**——它绝大部分是跨类型搬运的装配代码
  （桥接函数），行为由被桥接的两端测试覆盖。真正有逻辑的部分
  （策略快照转换、延迟/余额采样）已有测试。
- `internal/ir/anthropic` / `internal/ir/openai` 的百分比低，是因为这两个包
  的文件多（各自 6–8 个），而**跨方言契约测试放在 `internal/ir` 根包**
  （`contract_test.go` 覆盖请求/响应/流式三条路径的编解码往返）。子包内的
  `mapping_test.go` 覆盖映射表，`responses_stream_test.go` 覆盖 Responses
  流式事件。若只按包看覆盖率会低估实际覆盖。
- `internal/config` 从 0% 提升到 59.6%（2026-10-24 补测），剩下的是
  `Load` 的文件系统路径与 `Marshal` 的往返，属低风险区。

---

## 5. 已知未做的检查

| 检查 | 为什么没做 | 什么时候该做 |
|---|---|---|
| 前端 ESLint | 当前靠 TypeScript `strict` + `tsc -b` 把关；引入 ESLint 需要额外一套规则维护 | 前端文件数明显增长，或出现多次类型检查抓不到的问题 |
| `golangci-lint` 聚合 | `staticcheck` + `gosec` + `govulncheck` 已覆盖其核心 linter 集，再多一层只是增加配置面 | 需要统一多工具输出格式时 |
| 覆盖率门禁（CI 阻断） | 项目尚未接入 CI | 有 PR 流程后，建议对 `internal/auth`、`internal/store`、`internal/routing` 设下限（这三处是「错了会静默出错」的区域） |
| 并发/竞态全量 | `go test -race` 已在若干包跑过，未作为全量基线 | 任何涉及共享状态的改动提交前 |
