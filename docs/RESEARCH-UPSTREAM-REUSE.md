# dsh-providers-hub 可复用性查证

> 目的：判断 FreeRouter 的「众包上游账号」能力能否复用 `dsh-providers-hub` 已有的
> 17 家 provider 实现，而不是用 Go 重写一遍。
> 结论先行：**A 层可直接复用，B 层装齐依赖后可用，只有协议适配层需要自研。**

## 1. 方法

在一台已安装 DSH 的机器上，用纯净 Node 进程（`node v26.5.0`，无 DSH 运行时）对
`~/.dsh/profiles/web/node_modules/dsh-providers-hub/lib/*.js` 逐个 `import()`，
记录成功与否及失败原因。

```js
const m = await import(pathToFileURL(base + f).href)   // f 为各模块文件名
```

## 2. 结果

| 模块 | 独立加载 | 导出数 | 说明 |
|---|---|---|---|
| `buddy-oauth.js` | ✅ | 10 | `runBuddyLoginFlow` / `refreshToken` / `fetchModels` |
| `gemini-oauth.js` | ✅ | 24 | `startGeminiOAuthFlow` / `refreshGeminiCredential` / `revokeGeminiToken` |
| `trae-oauth.js` | ✅ | 9 | `startTraeLoginFlow` / `runTraeLoginFlow` / `exchangeTraeCallback` |
| `qoder-oauth.js` | ✅ | 3 | `startQoderLoginFlow` / `runQoderLoginFlow` / `pollQoderDeviceToken` |
| `qoder-credits.js` | ✅ | 12 | `fetchQoderCreditBalance` / `claimQoderDailyCheckin` |
| `codearts-credits.js` | ✅ | 13 | `fetchCodeArtsAccountInfo` / `claimCodeArtsDailyCheckin` |
| `aggregate-core.js` | ✅ | 6 | 聚合路由纯逻辑 |
| `jet-hub-store.js` | ✅ | 21 | 本地状态读写 |
| `account-pool.js` | ⚠️ | — | 缺 `@deepseek-ai/dsh-credentials` |
| `aggregate-expiry.js` | ⚠️ | — | 同上 |
| `index.js` | ⚠️ | — | 同上 |
| `provider-credits.js` | ⚠️ | — | 缺 `@deepseek-ai/dsh-llm` |

其余 `*-oauth.js` / `*-credits.js` 同样为纯 `node:*` 依赖（`node:crypto`、`node:http`、
`node:https`、`node:net`、`node:child_process`、`node:fs`、`node:path`、`node:os`），
不需要 DSH 运行时。

## 3. 结论：三层复用边界

| 层 | 内容 | 复用方式 |
|---|---|---|
| **A 可直接复用** | 各渠道登录流、凭据续期、余额查询、每日签到、聚合目录、本地状态 | 纯净 Node 进程直接 `import` 调用 |
| **B 装依赖后可用** | 账号池编排、插件入口、聚合过期排序、credits 工厂 | 需安装 `@deepseek-ai/cordis`、`@deepseek-ai/dsh-credentials`、`@deepseek-ai/dsh-llm` |
| **C 需自研** | 协议适配（`*-adapter.js`） | 这些依赖 `dsh-llm` 的 adapter 注册接口；FreeRouter 自己做请求/响应转换更直接 |

B 层的失败原因是「找不到包」，不是代码不兼容。`@deepseek-ai/*` 系列在 npm 上公开：

```
GET https://registry.npmjs.org/@deepseek-ai%2fdsh-credentials → 200
```

## 4. DSH 侧契约（供写 shim 时参考）

`ctx.credentials` 的调用面极小，只有 4 个方法：

```
credentials.resolve(   ×101
credentials.set(       ×69
credentials.unset(     ×11
credentials.describe(  ×7
```

行为要点（摘自 `@deepseek-ai/dsh-credentials` 说明）：

- `resolve(ref)` → `{ value, source } | undefined`
- `describe()` 报告「是否已设置 / 来自哪里 / 能否写入」，**不暴露值**
- 空密钥值视为不存在，空记录仍表示有意存储的凭据

`ctx.llm` 的调用面（仅 adapter 需要，FreeRouter 可跳过）：

```
llm.listModels / llm.stream / llm.listProviders /
llm.resolveModelInfo / llm.registerConfigurableProviders / llm.registerAdapter
```

## 5. 账号元数据结构（来自本机 `$DSH_HOME/jet-hub/state.json` 的键名）

```
{ id, provider, nickname, enabled, credentialRef, refreshable,
  createdAt, expiresAt?, modelRateLimits? }
```

关键点：**凭据本体与元数据分离**——元数据只存 `credentialRef` 引用，值存在
`dsh-credentials` 的存储里。FreeRouter 设计贡献流程时要同时处理这两者。

`state.json` 顶层另有：`accounts`（17 个渠道各一组）、`disabledModels`（按渠道）、
`aggregateRejected` / `aggregateAllowed` / `aggregateOrder`、`gatewayEnabled`。

## 6. 环境坑

- 沙箱内 `npm view` 报 `EROFS: read-only file system, open '~/.cache/npm/_cacache/tmp/...'`，
  原因是 cache 目录只读，**不是网络问题**（registry 直连返回 200）。
  跑 npm / pnpm 时把 cache 指到项目内：`npm_config_cache=$PWD/.npm-cache`。
- `codearts` 在 `lib/` 下只有 `codearts-credits.js`，其登录实现不在独立 oauth 文件里，
  需在接入时单独确认（可能在主插件入口内联）。

## 7. 对本项目的含义

```
FreeRouter 主服务（Go）          租户 / Key / 配额 / 鉴权 / 协议转换 / 路由 / 日志
        │ stdio + NDJSON（一行一帧）
Node sidecar                    渠道登录流 / 凭据加密托管 / 定时续期 / 余额签到 / 模型目录
        │ HTTPS
各供应商官方端点
```

## 8. 尚未验证

- 各渠道 login flow 在无图形界面环境下的完整闭环（回调、扫码、短信）
- 续期并发与 refresh token rotation：两侧同时刷新是否会互相踢掉
- `codearts` 登录实现的具体位置与形态
