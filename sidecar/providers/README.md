# sidecar/providers —— 供应商 OAuth 组件（提取自上游仓库）

PLAN §5.5 定案：17 家供应商的 OAuth 与静默续期「不可能重写，必须复用
providers-hub 的实现」。此目录就是那条定案的落点——按上游仓库的提取计划，
把**登录/续期所需的最小实现**移植成 sidecar 可直接 import 的 ESM 模块。

| 子目录 | 来源 | 内容 |
|---|---|---|
| `codearts/` | [deepseek-harness-codearts](https://gitee.com/iJetLi/deepseek-harness-codearts)（Apache-2.0） | `oauth.mjs` PKCE/DPoP/STS 换取；`sign.mjs` 华为 SDK-HMAC-SHA256；`codearts.mjs` sidecar 方法面 |

## 为什么是「提取」而不是「依赖」

上游包的 `exports` 只导出 `.` 与 `./client`，深路径 import 会随版本漂——而 sidecar
需要的正是深路径里的 auth 模块。按其提取计划把这几百行拿过来，得到的是一份
**版本可控、无外部依赖**（不引 jose / jose 被 node:crypto 等价实现替代）的实现。

## 移植纪律

1. **常量逐字对齐**：`CLIENT_ID`、`PORTAL_AUTHORIZE_BASE`、`plugin-name/version`、
   `code_challenge_method=SHA-256`（非 RFC 缩写 S256）都是逆向得到的真实插件值，
   改动会让 portal 回退到不支持的旧 ticket 流程。
2. **错误语义照抄**：`invalid_grant` / `ExpiredRefreshToken` 才算 refresh 终态；
   `InvalidDPoPHeader` 是请求级拒绝，不能把账号标死（上游缺陷史，见其
   `docs/agent-notes/providers-codearts.md`）。
3. **不加状态**：模块全为纯函数；跨请求的状态（PKCE verifier / DPoP 私钥）
   由主进程的内存表保管，sidecar 不落盘、不读 DSH 状态（三条红线不变）。

## 新增一家供应商的步骤

1. 在上游仓库找到 `<provider>-oauth.ts` / `<provider>-auth.ts`；
2. 按同样的「去类型 + 去状态」改写进 `providers/<provider>/`；
3. 在 `sidecar/index.mjs` 的 `oauth.*` 方法里加分支（未移植分支报 `E_NOT_IMPLEMENTED`）；
4. 主进程 `internal/app/oauth_codearts.go` 的 `Providers()` 增一行，并把 provider
   特化的落库参数（上游 baseURL / kind）补进 `persist()`。
