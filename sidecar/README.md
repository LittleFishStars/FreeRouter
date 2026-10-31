# sidecar —— providers-hub 的 Node 侧适配器

由 FreeRouter 主进程（Go）拉起的独立 Node 进程，经 **stdio + NDJSON** 一问一答，
复用 dsh-providers-hub 的登录流 / 凭据只读查询 / 余额与目录。
契约权威：[`docs/architecture/04-sidecar.md`](../docs/architecture/04-sidecar.md)（本目录一切行为以它为准）。

## 运行要求

- Node ≥ 22.19（providers-hub 的 engines；实测 v26.5.0 通过全部用例）。
- 主进程侧经 `internal/upstream/dshprovider.Start` 拉起，参数来自 `config.yaml` 的 `sidecar.*`
  （`entry` / `ipc: stdio` / `hub_path` / `node_path`）。**本进程不监听任何 TCP 端口**（红线③）。

## 文件

| 文件 | 职责 |
|---|---|
| `index.mjs` | IPC 主循环、方法映射表、红线守卫、stdout 守护、登录会话簿记 |
| `deps.mjs` | **B 层依赖装载**：定位 DSH 的 `@deepseek-ai/*` 依赖根 → 建符号链接 shim → 安装 ESM 解析钩子 |
| `resolve-hook.mjs` | 解析钩子的异步回落（Node < 22.15 时 `module.register` 用） |
| `hub.mjs` | 深路径 adapter 装载、能力表、`models.list` / `infer.stream` 实现 |
| `credentials.mjs` | `$DSH_HOME/.credentials.yaml` 的**只读**解析（`credentials.resolve/describe`） |
| `redact.mjs` | 外发文本的敏感遮蔽（与 Go 侧 `redactLine` 同组模式） |
| `selftest.mjs` | 不依赖 Go / 不依赖真凭据的自测：`node sidecar/selftest.mjs` |

## hub 装载实测（2026-10-22，hub 0.2.1-rc.2，node v26.5.0）

实测对象：`/home/ylxc/.local/share/dsh/profiles/web/node_modules/dsh-providers-hub`。
可达方式与结论（复跑命令见本节末尾）：

| 入口 | 结论 | 失败/成功的原始证据 |
|---|---|---|
| `import 'dsh-providers-hub'`（`.` 导出 → `lib/index.js`） | **可 import，但不可用** | 导出键 `{Config, apply, inject, makeReadImage, makeReadImageRequest, name}`——是一个 cordis 插件。`apply(ctx)` 需要完整 Cordis `Context`（credentials / llm / settings / store 都在 ctx 上），在 DSH 之外没有这个东西 |
| `import 'dsh-providers-hub/client'`（`./client` → `lib/client/jet-hub.js`） | **失败** | `ReferenceError: window is not defined`——文件首行是 `window.__ModuleLoader__.load({...})`，这是浏览器模块加载格式 |
| `import '.../lib/xxx-adapter.js'`（深路径） | **首次失败 → 加解析钩子后成功** | 裸 import 报 `ERR_MODULE_NOT_FOUND: Cannot find package '@deepseek-ai/dsh-llm' imported from .../lib/modelscope-adapter.js`（该 adapter 已随 provider 下线，原始错误文本保留不动）。**这不是 hub 的问题，是 B 层依赖不在 profile 里**：305 个 `@deepseek-ai/*` 包全在 DSH 安装目录 `/usr/lib/node_modules/@deepseek-ai/dsh/node_modules/@deepseek-ai/`，而 `hub/node_modules/@deepseek-ai` 是个**空目录** |
| `NODE_PATH=<DSH 依赖根>` + 裸 import | **无效** | ESM 不读 `NODE_PATH`（该变量只影响 CJS 的 legacy 查找），报错与上一行完全相同 |
| 在 `hub/node_modules/@deepseek-ai/` 里建软链 | **可行但违反纪律** | hub 目录是**只读参考**（PRD 要求），且升级 hub 会把链接冲掉 |
| `createRequire(hub/package.json).resolve('@deepseek-ai/dsh-credentials')` | **假阴性** | 即使依赖就在依赖根里也报 `Cannot find module`——`createRequire` 从 hub 的 `package.json` 起解析时**不受** `module.registerHooks` 影响（只有 ESM import 走钩子）。所以 health 的逐包判定改成「依赖根里有没有这个包目录」，不再用这个 API |
| 自建解析钩子 + shim 目录（**采用**） | **成功** | `module.registerHooks({resolve})` 把 `@deepseek-ai/*` 的解析起点改到 `/tmp/freerouter-sidecar-<uid>/deps-shim/__entry.mjs`（同级 `node_modules/@deepseek-ai/<pkg>` 是指向 DSH 依赖根的符号链接），Node 随后走**原生 exports 解析**。实测 18 个 adapter 模块全部 import 成功（其中 2 个已随 modelscope / opencode 下线，现表内 16 个） |

**为什么最终选「深路径 adapter」而不是「加载 cordis 插件」**：

1. 根导出的插件在 DSH 之外**没有宿主**——要在 sidecar 里伪造一个 Cordis Context，等于把
   DSH 的加载器重写一遍，且这个伪造会随 DSH 版本漂移（正是 04-sidecar 1.1 否掉的方案）；
2. adapter 的构造选项**全是调用方注入的**（`slots()` / `identitySlots()` / `accountPool` /
   `fetchImpl`），这天然就是「Go 给凭据、Node 跑协议」的接缝；
3. 深路径 import 的漂移风险是**可观测的**：`hub.mjs` 的能力表登记了模块文件与导出类名，
   漂移时表现为 `/health` 的 `hubAdapters[].detail`（实测已经抓到一次：
   `officeace-adapter.js` 导出的是 `OfficeAceAdapter`，不是按 provider 名猜的 `OfficeaceAdapter`）。

### 能跑到什么程度（能力矩阵）

| 能力 | 状态 | 依据 |
|---|---|---|
| `health` / `providers.list` / `login.*` 会话簿记 / 红线守卫 | **可用** | 不依赖 hub，纯内存/session |
| `credentials.resolve` / `credentials.describe` | **可用（只读）** | 读 `$DSH_HOME/.credentials.yaml` 的 `refs:` 段；22 条真实引用解析通过，且调用后文件 mtime/内容不变 |
| `infer.stream` | **qoder/qodercn 可用（2026-10-31 移植）** | 贡献凭据由主进程解密后单帧传入（`credential` 参数），hub QoderAdapter 的 wasm 加密链路在 sidecar 内跑通；stream=false 时合成完整 chat.completion。其余 oauth 形状渠道仍 E_NOT_IMPLEMENTED（同一接缝，逐家点亮即可） | 这些 adapter 从 `options.accountPool` 取 `credential.access_token`，账号池是 DSH 的运行态；sidecar 没有池替身，硬跑会把「没账号」伪装成上游故障 |

> **2026-10-24 变更**：原先唯一「可用」的那一类是 modelscope / opencode 两个「粘贴 API Key」
> 的 adapter（`slots()` / `identitySlots()` 由我们注入，纯 Node 可驱动）。两家已按
> `tmp/CONTRACT-contribution-v2.md` §7 从 sidecar 与注册表里删除——那种凭据形态改由主进程的
> **内置 API 目录 + 直连出站**承担（少一层进程，也少一处明文 key 落点）。因此 sidecar 现在
> 不再提供任何「粘贴 Key 就能跑」的能力，`models.list` / `infer.stream` 两条路都如实报码。
| `credits.query` | **可用（2026-10-28 起）** | 直接复用 hub `provider-credits.js` 的 17 家分发器（deps shim 装 hub lib 实体）；凭据明文由主进程解密后经 stdio 传入，只读查询 |
| `credits.claim` | **可用（2026-10-31 起）** | 每日签到/领取积分：走 `createProviderCredits` 的同一份分发表（`claim`/`claimStatus`/`precheckStatus`/`claimUnavailableReason` 全在条目上）；**qoder/qodercn 例外直调** `qoder-credits`（hub 刻意不让它们进表，jet-hub-rpc 的 claimAll 同样保留特例分支）；trae 附带 1024 打卡抽奖（afterClaimNote）；minimax/raccoon 401 自愈换新重试；CodeBuddy 成长中心刻意不接（90~270s 会阻塞串行管道）；zcode 要 captcha + headful 浏览器，不支持。**上游业务写，不写凭据存储**——轮换出的新凭据放 `refreshed_credential` 随应答上行，写回密文是 Go 桥的责任 |
| `login.start` 的真实登录流 | **E_NOT_IMPLEMENTED**（保持原状） | 登录流是 cordis 插件 `apply(ctx)` 里注册的，走的是第 1.1 节被否掉的那条路；当前只提供会话簿记与 `expiresAt` 自清理 |

> 能力缺失一律**显式报码**，不做「看起来能用」的降级——这是 code-review 对 `secretSource`
> 那条静默降级的同一条纪律，在 sidecar 侧同样适用。

`infer.stream` 的应答形状是 `{"sse": "<OpenAI chat.completion.chunk 的 SSE 全文>"}`：
NDJSON 是一问一答（不能多帧推送），所以 adapter 的 `StreamChunk` 在 sidecar 内被还原成
线上格式再整段返回；Go 侧 `relay.go` 的 `unwrapInferPayload` 会拆掉这层信封，
并兼容「裸 SSE/JSON 载荷」的旧假设（`TestContractRelayUnwrapSSEPayload` 覆盖三种形态）。
真流式（多帧应答）需要契约演进，属于单独一次接口变更，不在本次范围。

### 复跑实测

```console
# 1) 完整自测（含凭据解析、遮蔽、hub 能力表、打桩 infer）
$ node sidecar/selftest.mjs
  ok   parseRefs 只解析 refs 段（records 里的 token 不外泄）
  ...
  13/13 通过（skip 0）

# 2) 三种入口的原始结论（失败证据）
$ node -e 'import("file:///home/ylxc/.local/share/dsh/profiles/web/node_modules/dsh-providers-hub/lib/client/jet-hub.js").catch(e=>console.log(e.message))'
window is not defined

# 3) 降级路径：hub 不在位时仍然「活着但会说 error」
$ printf '%s\n' '{"id":1,"method":"health"}' '{"id":2,"method":"models.list","params":{"provider":"trae","apiKey":"sk-x"}}' \
  | FR_HUB_PATH=/nonexistent/hub node sidecar/index.mjs 2>/dev/null
{"id":1,"ok":true,"result":{...,"deps":[{"name":"dsh-providers-hub","ok":false,...}],"hubAdapters":[]}}
{"id":2,"ok":false,"error":{"code":"E_DEPENDENCY_MISSING","message":"models.list 需要可装载的 dsh-providers-hub adapter（hub_path=/nonexistent/hub）…"}}
```

环境变量（都只在需要时设，默认值已在代码里）：

| 变量 | 作用 |
|---|---|
| `FR_HUB_PATH` | hub 包根（Go 侧 `Options.HubPath` 注入） |
| `FR_DSH_DEPS_ROOT` | 强制指定 B 层依赖根（默认从 `dsh` 可执行文件/PATH/系统安装位枚举） |
| `FR_DSH_CREDENTIALS_FILE` | 覆盖凭据文件路径（测试用；仍只读） |
| `FR_SIDECAR_SHIM_DIR` | 覆盖 shim 目录（默认 `/tmp/freerouter-sidecar-<uid>/deps-shim`） |

> **注意 shim 目录为什么在 /tmp**：实测本机 `~/.cache` 建目录直接 `ENOENT`（沙箱只读），
> 而 shim 只是符号链接，重建成本为零——为它挡启动不划算。

## 冒烟示例（不依赖 Go，手工验协议）

在仓库根目录执行；每行一个请求帧，应答逐行返回：

```console
$ printf '%s\n' \
    '{"id":1,"method":"health"}' \
    '{"id":2,"method":"providers.list"}' \
    '{"id":3,"method":"credentials.resolve","params":{"credentialRef":"DEEPSEEK_API_KEY"}}' \
    '{"id":4,"method":"credentials.set","params":{"credentialRef":"DEEPSEEK_API_KEY"}}' \
    '{"id":5,"method":"shutdown"}' \
  | node sidecar/index.mjs 2>/tmp/sidecar.log

{"id":1,"ok":true,"result":{"version":"0.1.0","providers":[…15 家…],"deps":[{"name":"dsh-providers-hub","ok":true,"detail":"依赖根=/usr/lib/node_modules/@deepseek-ai/dsh/node_modules"}],"hubAdapters":[…16 条…],"stateReadonly":true,"nodeVersion":"v26.5.0"}}
{"id":2,"ok":true,"result":[{"provider":"accio","displayName":"Accio","loginKind":"browser_callback","loginImplemented":true,"hasAccount":true,"adapterAvailable":true,"adapterShape":"oauth"}, … ]}
{"id":3,"ok":true,"result":{"value":"<真值>","source":"dsh-credentials-yaml"}}
{"id":4,"ok":false,"error":{"code":"E_CREDENTIAL_READONLY","message":"该方法违反红线，永远不存在：credentials.set","retryable":false}}
{"id":5,"ok":true,"result":{}}
$ echo $?
0
```

要点（`/tmp/sidecar.log` 里能看到对应行）：

- **第 3 帧是只读凭据解析**：值来自 `$DSH_HOME/.credentials.yaml`，**只读**打开、不缓存
  （DSH 续期会原子替换该文件，缓存旧值等于拿过期 token 去调用）。空值与缺失都返回 `null`。
- **第 4 帧是红线守卫的活体演示**：`credentials.set` 不存在于映射表 METHODS，且即便收到也返回
  专用码 `E_CREDENTIAL_READONLY` 并打 `[REDLINE]` 审计行。
- **stdout 只有 NDJSON**：第三方库的 `console.*` 与直写 stdout 全被进程级守护改道 stderr；
  改道内容在 `toStderr` 里过一遍 `redactLine`（Bearer/Basic/code/token/sk- 形态遮蔽；
  错误码一类 snake_case 标识符**不遮**——它们回答「为什么失败」，与 Go 侧 `RedactLine` 同一组语义）。
- **缺 hub 不崩**：hub 不在位时 health 报 `deps.ok=false`，依赖它的方法回 `E_DEPENDENCY_MISSING`；
  主进程的「挂横幅继续」（04-sidecar 5.1）依赖的正是这个行为。
- 最后一帧 `shutdown` 后进程自行退出（04-sidecar 5.4 步骤 4）。

## 红线自检（每次改本目录前读一遍）

| 红线 | 本目录的实现 |
|---|---|
| ① 只读凭据 | `credentials.mjs` 只有读路径；METHODS 里没有 set/unset/refresh/write/delete；`REDLINE_GUARDS` 命中即回专用码 + 审计行。`selftest.mjs` 断言「调用前后文件内容与 mtime 不变」 |
| ② 不碰 `jet-hub/state.json` | 只在 `health` 里 `statSync` 取 mtime 做审计基线，**不读内容、不写** |
| ③ 推理不经本机 HTTP 网关 | 本进程不监听任何端口；`infer.stream` 由 adapter 直接打官方端点 |

## 方法在哪加

`index.mjs` 的分派是**映射表 METHODS**（方法名 → handler）。契约方法集随文档增删时：
改 `docs/architecture/04-sidecar.md` 2.2 → 加/删 METHODS 的一个条目 → Go 侧同步
`methods.go` 的常量与类型化封装。主循环、错误归一、红线守卫都不动。
**新条目的名字过不了红线守卫表（REDLINE_GUARDS）就不该存在。**

## 测试

```console
node sidecar/selftest.mjs       # Node 侧自测（13 条；hub 不在位时 hub 相关用例 Skip）
make contract                   # Go 侧契约测试（含真 sidecar 冒烟；node 不在 PATH 时自动 Skip）
```

`selftest.mjs` 与 Go 契约测试的分工：Go 侧验「IPC 帧形状与错误码」，Node 侧验
「帧里的内容对不对」（YAML 标量、钩子、SSE 拼装）——后者从 Go 只能间接观察。
