# sidecar/providers —— 供应商 OAuth 组件（提取自上游仓库）

PLAN §5.5 定案：17 家供应商的 OAuth 与静默续期「不可能重写，必须复用
providers-hub 的实现」。此目录就是那条定案的落点。

## 来源与许可

| 来源 | 内容 |
|---|---|
| [deepseek-harness-codearts](https://gitee.com/iJetLi/deepseek-harness-codearts)（Apache-2.0） | 全部 `*-oauth.js` / `*-auth.js` / `*-product.js` 及其依赖闭包 |

文件是**上游 `tsc` 的编译产物**（`lib/*.js`）按「OAuth 入口的依赖闭包」整体拷过来
的，不是手抄重写。这样做的理由：这些文件里全是逐个接口逆向出来的常量与请求形状
（签名算法、PKCE 变体、设备码节奏、脱敏规则），手抄必然引入静默偏差，而偏差的
表现是「登录成功但推理 401」这类难查故障。

`codearts/` 是唯一手写移植的一家（`oauth.mjs` / `sign.mjs` / `codearts.mjs`）：
上游用 `jose` 做 ES256，本机部署环境里不保证有这个包，所以改用 `node:crypto`
实现同一套 DPoP JWS（`dsaEncoding: 'ieee-p1363'`，已用自身公钥验签通过）。

## 目录

```
providers/
├── registry.mjs        14 家的统一注册表：把差异收敛成 3 种交互形态
├── sessions.mjs        授权会话簿记（后台轮询 + TTL）
├── _shims/             依赖替身（cordis / dsh-credentials / 内存 ctx）
├── codearts/           手写移植（PKCE + DPoP + STS 换取 + SDK-HMAC 签名）
├── accio/ cline/ gemini/ lobsterai/ loomy/ minimax/ qoder/ raccoon/ trae/
│                     上游闭包（每家一个目录，文件名保持上游原样）
├── autoclaw/ astudio/ officeace/ zcode/
└── ...
```

## 三种交互形态

注册表把这些差异收敛成三种，**主进程只认这三种**：

| flow | 交互 | 上游例子 |
|---|---|---|
| `browser` (redirect) | 给 `authorize_url`，用户浏览器登录，code 回传到 `/oauth/callback` | codearts / gemini / accio / lobsterai / trae |
| `browser` (session) | 给 URL，但**登录结果由上游自己的端点轮询**（不是我们的回调） | officeace（云侧 code）/ zcode（bigmodel） |
| `device_code` | 给 `user_code` + `verification_uri`，后台轮询 | minimax / cline / qoder |
| `sms` / `qr` | 发短信或出二维码，用户回填/扫码后后台轮询 | loomy / autoclaw / raccoon |

**关键设计**：上游的 `pollXxx` 是**阻塞 while 循环**（等到用户授权完成才返回）。
直接挂在 sidecar 的一问一答通道上会卡死（`Call` 超时只有 10 秒）。所以轮询一律
进 `sessions.mjs` 的后台任务，`oauth.poll` 只查状态、毫秒级返回。

## 当前接入状态

已接入 14 家：codearts、gemini、accio、minimax、cline、qoder、loomy、raccoon、
lobsterai、trae、autoclaw、astudio、officeace、zcode。

`API_KEY_ONLY` **为空**（2026-10-24 起）：原先列在这里的 modelscope / opencode 已按
`tmp/CONTRACT-contribution-v2.md` §7 从本目录删除——它们「控制台生成、粘贴使用」的凭据
形态与新增的「API 供应商」入口完全一致，由主进程的 `internal/apidir`（52 家，编译进
二进制的内置目录）承载，不需要 sidecar 再列一遍、也不需要多一层进程。

> 顺带纠正一条旧判断：astudio 曾因「登录流自己起本机 HTTP 服务」被列在这里，实际它还有
> 一条纯 HTTP 的微信扫码三件套（`createAstudioWechatQrCode` / `pollAstudioWechatScan` /
> `bindAstudioWechatSession`），不需要本机监听，现已作为 `flow: 'qr'` 正常接入。

## 移植纪律

1. **不改协议细节**：常量、URL 参数、签名算法、轮询节奏全部保持上游原样。
   这些是逆向得到的事实，改动会静默失效。
2. **不加状态**：模块本身无状态；跨请求状态（PKCE verifier / DPoP 私钥 / 设备码）
   由主进程的内存表或 sidecar 的会话表保管。
3. **sidecar 不落盘、不监听端口**（红线①③）：需要本机 HTTP 服务的流程
   （astudio/trae 的原始实现）改为「自己拼 URL + 用主进程的回调路由」。
4. **错误语义照抄**：`invalid_grant` / `ExpiredRefreshToken` 才算 refresh 终态；
   `InvalidDPoPHeader` 是请求级拒绝，不能把账号标死。

## 新增一家 provider 的步骤

1. 在上游仓库找到 `<provider>-oauth.ts` / `<provider>-auth.ts`；
2. 用上游 `tsc` 编译，按依赖闭包把 `lib/*.js` 拷进 `providers/<provider>/`；
3. 把 `@deepseek-ai/cordis` / `@deepseek-ai/dsh-credentials` 改指向 `_shims/`；
4. 在 `registry.mjs` 加一个 `register('<id>', {...})` 条目（选三种形态之一）；
5. 需要 product 对象的，把 `<provider>-product.js` 一并拷来；
6. 在 `sidecar/index.mjs` 无需改动（`oauth.*` 已按注册表分派）。
