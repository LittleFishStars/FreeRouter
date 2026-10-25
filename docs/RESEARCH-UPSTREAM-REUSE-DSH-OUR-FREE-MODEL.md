# dsh-our-free-model 可复用性查证（模型渠道）

> 目的：判断 `Ebony-Vinyl/dsh-our-free-model`（dsh 插件，v2.7.1，MIT）对 FreeRouter 的
> **模型渠道/供应商体系**有何可参考、可融合之处。
> 结论先行：**channel/供应商的「静态注册表 + 多协议多方言」数据模型值得照搬**；
> 账号池/协付/OAuth 渠道则是 FreeRouter 明确 range 之外的形态，只能做思想参考。

## 0. 仓库定位（先搞清楚它是什么）

这是一个 **dsh（DeepSeek Harness）插件**，作用是在 dsh 里免登录、免 API Key 用上
DeepSeek V4.1 / Kimi K3 等前沿模型。它自身是**前端 JS**（跑在宿主进程里），
不是网关、不是服务端。

它持有一套多层的「模型渠道」：

| 层 | 形态 | FreeRouter 对应物 |
|---|---|---|
| 匿名免费车道 | 内置（OpenCode Zen 网关直连） | 无直连对应 |
| EAC 协付 | 桌面端专属、GitHub 授权放行 | 无 |
| Kilo 免费池 | 免密免费池 + 实时 isFree 清单 | 无 |
| **聚合渠道（自带 Key）** | **注册表 76 家 provider，用户填 key** | **≈「API 供应商贡献」** |
| **账号渠道（白嫖）** | 10 家 OAuth 账号（有道/阿里/字节/讯飞/商汤/智谱/Google 等）| 无 |
| 本地 OpenAI 网关 + 局域网中继 | /v1/chat + /v1/responses + /v1/messages | ≈ webui 的网关 |

FreeRouter 的「贡献供应商」机制，形态上强对应这个聚合渠道，也最值得看。

## 1. 最值得融合：`src/providers/registry.json` 的静态注册表数据模型

这是它的核心资产：**76 家 provider 的纯数据声明**，一个可 diff 的 JSON，含：

```
{
  "id": "deepseek",
  "name": "DeepSeek",
  "category": "apikey",            // apikey | free-tier | local | oauth
  "auth": {
    "kind": "apiKey",              // apiKey | oauth | adc | none
    "env": "DEEPSEEK_API_KEY",     // 环境变量名约定
    "url": "https://platform.deepseek.com/api_keys"  // key 申请地址
  },
  "baseUrl": "https://api.deepseek.com",
  "baseUrlOverrideEnv": "DEEPSEEK_BASE_URL",   // 允许用户覆盖 base
  "format": "openai-chat",         // openai-chat | openai-responses | anthropic-messages
  "modelsUrl": "https://...",      // 模型目录接口（可为空）
  "headers": {},                   // 供应商声明的固定头
  "models": [ { "id", "name", "contextWindow", "vision", "reasoning" } ],
  "notes": "…", "website": "…", "source": "fcc|9router|自制", "local": false
}
```

**对 FreeRouter 的四点可迁移设计：**

1. **`format` 三协议声明**（openai-chat / openai-responses / anthropic-messages）。
   FreeRouter 已有 IR 层，但贡献上传时只约定「OpenAI 兼容端点」。若把贡献
   `Submission` 加一个 `format` 字段（默认 openai-chat），从源头就支持
   Anthropic-Messages 原生端点（如 Z.ai、MiniMax-CN、Azure OpenAI 直连 Anthropic 协议）
   ，不必都转成 OpenAI。
2. **`category` 分层**（apikey / free-tier / local / oauth）+ **`auth.kind` 认证方式**分离。
   目前 FreeRouter 贡献不区分 free 供应商；若想给「免费/白嫖来源」打标（像 anonymous
   车道），这套 category 口径能直接扩展 `upstream` 的 kind 语义。
3. **`modelsUrl` + `baseUrlOverride` 的「覆盖不发明」规则**：供应商声明了 listing
   端点才去探测 `/models`；且只有当 listing 确实「住在」注册表 base 之下时才随 base
   覆盖改写，否则不动（`effectiveModelsUrl`）。**这正是 FreeRouter「探测 /models
   得出模型」这条契约要补的健壮性**——避免为静态清单源猜一个不存在的模型 URL。
4. **`models[].contextWindow / vision / reasoning` 三能力字段 + null 语义**：
   上游语义「未知」≠「0」也不等于「有限」；能力判定只能把 null 当「不设限」。
   与 FreeRouter `ModelInfo.ContextWindow=0 表示未知，不是确定是 0」**逐字一致**，
   说明两边对能力未知的处理收敛到了同一口径。

## 2. 可参考的探测器逻辑（`src/aggregate.js`，纯函数可直接对照）

- **`modelIdsFrom` 多方言容错解析**：同一个 `/models` 返回，兼容
  `{data:[{id}]}`（OpenAI）、`{models:[{name}]}`（Cohere/Ollama）、
  裸数组、`[{model_name}]`（DeepInfra）。**FreeRouter 探测若要求上游固定
  OpenAI 形状，可对照这里补容错**——不同供应商的 /models 方言不一。
- **`classifyUpstream` + `redact`**：探测失败时按 status+body 分类（凭据/配额/限流），
  且**先把 body 里 echo 出来的 key 抹掉再入库**（`[redacted]`）。FreeRouter
  的 verification evidence 应同样先脱敏再存，防止 401 body 回显 token。

## 3. 部分是 FreeRouter 已经踩过的共识（无需重复）

- `providers/index.js` 用 `node:fs` 手读 JSON 而非 `import ... assert/with`：
  「JSON 模块断言关键字在跨 Node 版本间不一致」。对 FreeRouter（Go）不相关。
- 注册中心 id 与模型 id 都是 `provider/model` 复合形式（模型 id 自身可能含 `/`）。
  FreeRouter 的 `real_model_id` 已经用 provider 前缀 + 模型名，口径相同，无需改。

## 4. 明确不融合的部分（range 之外）

- **十个账号渠道 + 账号池 + 每日签到 + 余额查询**：这是「白嫖他人 API」的
  灰色打法，且依赖浏览器登录口、账号池、限流绕过，FreeRouter 的架构数据源
  （vendor/selfhosted/无账号）无此位置。文档 `04-sidecar.md` 已把 sidecar
  的范围限定在「登录流 / 凭据加密 / 续期」上，但是否需要接入某一家账号
  体系由用户另定。
- **OAuth / ADC 认证渠道放在「可列出但不可驱动」**：`authUsable()` 把 oauth/adc
  退回 false，即使填了 key 也不发——因为它拿不到 sign-in 的 token。FreeRouter
  的贡献模型也不支持 OAuth 签名，二者行为一致，直接保留现状即可。
- **本地模型（Ollama / LM Studio / llama.cpp）**：FreeRouter 有「自建/本机」
  kind，但本地端点/模型清单需要「每条本机配置各自声明」，不适合塞进共享的
  众包供应商注册中心——本地模型对角色的作用由自再技术无关。

## 5. 归纳：FreeRouter 接下来的三项可落地改进（按收益排序）

1. **`Submission` / 模型来源加 `format` 字段（openai-chat 默认）**，探测时按
   `format` 走 `defaultHeaders` 的分支（anthropic 用 `x-api-key` + `anthropic-version`），
   让 Anthropic-Messages 类上游能原生贡献。
2. **`/models` 探测多形状容错**：按 §2 的 `modelIdsFrom` 做一组 fallback 解析，
   覆盖 `data` / `models` / 数组 / `model_name`，避免上游形状不符就报未识别。
3. **探测 evidence 脱敏**：把响应 body 中与提交 key 相同的子串在落库前替换为
   `[redacted]`，与 `ProbeResult.Detail` 一起入审计。

> 说明：本仓库注册表中「DeepSeek V4.1 Flash / GPT-6.0 / Gemini 3.8」等为
> 该仓库注册表中所涉模型命名以该仓库自身时间为准，FreeRouter 不据此断言
> 自身平台上实际可用的型号。本文仅借鉴其「渠道数据模型」结构与对接思路。
