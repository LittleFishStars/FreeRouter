# sidecar —— providers-hub 的 Node 侧适配器

由 FreeRouter 主进程（Go）拉起的独立 Node 进程，经 **stdio + NDJSON** 一问一答，
复用 dsh-providers-hub 的登录流 / 凭据只读查询 / 余额与目录。
契约权威：[`docs/architecture/04-sidecar.md`](../docs/architecture/04-sidecar.md)（本目录一切行为以它为准）。

## 运行要求

- Node ≥ 22.19（providers-hub 的 engines；实测 v26.5.0 通过全部契约测试）。
- 主进程侧经 `internal/upstream/dshprovider.Start` 拉起，参数来自 `config.yaml` 的 `sidecar.*`
  （`entry` / `ipc: stdio` / `hub_path` / `node_path`）。**本进程不监听任何 TCP 端口**（红线③）。

## 冒烟示例（不依赖 Go，手工验协议）

在仓库根目录执行；每行一个请求帧，应答逐行返回：

```console
$ printf '%s\n' \
    '{"id":1,"method":"health"}' \
    '{"id":2,"method":"providers.list"}' \
    '{"id":3,"method":"credentials.resolve","params":{"credentialRef":"dsh:buddy:0"}}' \
    '{"id":4,"method":"credentials.set","params":{"credentialRef":"dsh:buddy:0"}}' \
    '{"id":5,"method":"shutdown"}' \
  | node sidecar/index.mjs 2>/tmp/sidecar.log

{"id":1,"ok":true,"result":{"version":"0.1.0","providers":["accio","autoclaw",…17 家…],"deps":[{"name":"@deepseek-ai/dsh-credentials","ok":false,"detail":"Cannot find module …"}],"stateReadonly":true,"nodeVersion":"v26.5.0"}}
{"id":2,"ok":true,"result":[{"provider":"accio","displayName":"Accio","loginKind":"browser_callback","loginImplemented":true,"hasAccount":false}, … ]}
{"id":3,"ok":false,"error":{"code":"E_DEPENDENCY_MISSING","message":"credentials.resolve 需要 dsh-providers-hub，装载失败：…","retryable":false}}
{"id":4,"ok":false,"error":{"code":"E_CREDENTIAL_READONLY","message":"该方法违反红线，永远不存在：credentials.set","retryable":false}}
{"id":5,"ok":true,"result":{}}
$ echo $?
0
```

要点（`/tmp/sidecar.log` 里能看到对应行）：

- **缺 hub 不崩**：第 3 帧返回结构化错误而不是崩溃或挂死——主进程的「挂横幅继续」（04-sidecar.md 5.1）
  依赖的正是这个行为。当前环境的 hub 根导出是 cordis 插件（需 `@deepseek-ai/cordis` 等 B 层包）、
  `./client` 导出是浏览器模块格式，纯 Node 下都装载不了，所以依赖 hub 的方法一律
  `E_DEPENDENCY_MISSING` / `E_NOT_IMPLEMENTED`；health、providers.list、login 会话簿记、
  红线守卫不依赖 hub，真实可用。
- **第 4 帧是红线守卫的活体演示**：`credentials.set` 不存在于映射表 METHODS，且即便收到也返回
  专用码 `E_CREDENTIAL_READONLY` 并打 `[REDLINE]` 审计行（Go 侧契约测试
  `TestContractRedlineNoCredentialWriteMethods` 有同款断言）。
- **stdout 只有 NDJSON**：第三方库的 `console.*` 与直写 stdout 全被进程级守护改道 stderr；
  人类可读日志一律看 stderr。
- 最后一帧 `shutdown` 后进程自行退出（04-sidecar.md 5.4 步骤 4）。

## 方法在哪加

`index.mjs` 的分派是**映射表 METHODS**（方法名 → handler）。契约方法集随文档增删时：
改 `docs/architecture/04-sidecar.md` 2.2 → 加/删 METHODS 的一个条目 → Go 侧同步
`methods.go` 的常量与类型化封装。主循环、错误归一、红线守卫都不动。
**新条目的名字过不了红线守卫表（REDLINE_GUARDS）就不该存在。**

## 测试

```console
make contract                  # Go 侧 12 条契约测试（本目录被真 sidecar 冒烟用例引用）
# node 不在 PATH 时自动 Skip，不 Fail
```
