> 分册：架构 · 依赖：[02-admin.md](02-admin.md)、[03-errors.md](03-errors.md)、[../ui/04-tenant.md](../ui/04-tenant.md) · 对应总纲：PLAN.md 第 3、5、6、8 章

# 入站 API

## 1. 入站层的定位
入站层是 L1（总纲第 6 章），只做四件事：
1. TLS 终止与公网暴露
2. 鉴权（`fr-` 开头的 API Key）→ 限速 → 配额
3. 协议解析成 IR（总纲第 6 章的 `ir` 包）
4. 按协议把 IR 结果序列化回客户端

**它不知道真实模型是谁**。真实落点由 L2 内核在路由时决定， 入站层只拿到抽象模型名。

**为什么把"不知道"写成定位而不是实现细节**：总纲 3.1 的硬约束 "对外只暴露抽象模型"只有在入站层结构上无法表达真实模型时才稳固。 一旦入站层能拿到真实模型名，早晚会有人把它写进响应。

### 1.1 MVP 范围内的端点

| 方法 | 路径 | 协议 | 状态 |
|---|---|---|---|
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions | MVP |
| `POST` | `/v1/messages` | Anthropic Messages | MVP |
| `GET` | `/v1/models` | OpenAI 模型列表 | MVP |
| `POST` | `/v1/responses` | OpenAI Responses | P1 |
| `POST` | `/v1beta/models/{model}:generateContent` | Gemini | P1 |

**为什么 MVP 只做前三个**：总纲第 8 章把入站三个 API 列在 MVP， 把 Responses 与 generateContent 列在 P1。验收标准第 1、2 条也只要 `chat/completions` 与 Claude Code（即 `messages`）。

## 2. 鉴权

### 2.1 请求头

| 端点 | 接受的鉴权头 | 说明 |
|---|---|---|
| `/v1/chat/completions` | `Authorization: Bearer fr-xxxxxxxx…` | OpenAI 客户端默认形态 |
| `/v1/messages` | `x-api-key: fr-xxxxxxxx…` | Anthropic 客户端默认形态 |
| `/v1/messages` | `Authorization: Bearer fr-xxxxxxxx…` | 兼容形态，二者取其一 |
| `/v1/models` | 同上游客户端形态 | 需要知道是哪个 Key，见 5.2 |

`/v1/messages` 还接受 `anthropic-version` 头并回显它（见 3.3）。

**为什么 `/v1/messages` 同时接受两种鉴权头**：总纲验收第 2 条要求 "Claude Code 设 `ANTHROPIC_BASE_URL` 后能完成带工具调用的任务"， 而 Claude Code 发的是 `x-api-key`。只支持 `Authorization` 会让验收第 2 条 在客户端侧直接失败，与我们的服务无关。

**为什么 Key 明文的形态是 `fr-` + 32 字节 base62**：总纲 9.4。 前缀的作用是让运维在日志与聊天记录里能一眼认出一个 Key， 从而判断"这段内容能不能公开贴出来"。

### 2.2 鉴权失败

鉴权在入站层最前端完成，失败立即返回，不进入限速与配额。

| 情况 | 状态码 | 错误码 |
|---|---|---|
| 缺鉴权头 | 401 | `invalid_api_key` |
| Key 不存在或已被吊销 | 401 | `invalid_api_key` |
| Key 已过期 | 401 | `invalid_api_key` |
| Key 被禁用 | 401 | `invalid_api_key` |
| 所属账户被禁用 | 401 | `invalid_api_key` |

**为什么五种情况共用同一个错误码**：区分它们等于告诉攻击者 "这个 Key 曾经存在"。总纲 9.3 的"禁用联动（账户 disabled → 名下所有 Key 立即 401，鉴权层实时查状态）"要求的是**行为一致**，不是**文案可区分**。

**为什么"账户被禁用"要在鉴权层实时查**：如果只在登录时检查， 一个刚被禁用的账户名下所有 Key 会继续可用到管理员下次想起为止。 实时查状态的代价是一次索引查询，收益是禁用动作立即生效（验收第 8 条）。

## 3. `POST /v1/chat/completions`

### 3.1 请求结构

```json
{
  "model": "smart",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "北京今天天气如何？"}
  ],
  "stream": true,
  "stream_options": {"include_usage": true},
  "max_tokens": 1024, "temperature": 0.7,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather", "description": "查询指定城市的天气",
        "parameters": {
          "type": "object",
          "properties": {"city": {"type": "string"}},
          "required": ["city"]
        }
      }
    }
  ],
  "tool_choice": "auto", "reasoning_effort": "high",
  "response_format": {"type": "json_object"}
}
```

**必填字段**：`model`、`messages`。

**多模态消息**（图片，对应 IR 的 `parts[].image`）：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "这张图里有什么？"},
    {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo..."}}
  ]
}
```

### 3.2 IR 映射

| 入站字段 | IR |
|---|---|
| `messages[].role` | `message.role` |
| 字符串 `content` | `message.parts = [{type:"text"}]` |
| `content[].type=text` | `parts[].text` |
| `content[].type=image_url` | `parts[].image`（返回时为 base64 或 URL 二选一，见下） |
| `tools[]` | IR 的工具声明集合 |
| `tool_choice` | IR 的工具选择策略 |
| 拒绝候选中缺失的 `reasoning_effort` | 入站层不拦截，交给 L2 按候选能力处理 |

**为什么图片在 IR 里只保留两种形态**：IR 是协议无关的中间表示， 它必须以"能表达"为界，而不是以"某个协议支持什么"为界。 base64 与 URL 是上游能接受的两种最小形态。

### 3.3 工具调用结果回传

```json
{
  "role": "assistant", "content": null,
  "tool_calls": [
    {
      "id": "call_abc123", "type": "function",
      "function": {"name": "get_weather", "arguments": "{\"city\":\"北京\"}"}
    }
  ]
}
```

```json
{"role": "tool", "tool_call_id": "call_abc123", "content": "{\"temp\": 18}"}
```

## 4. SSE：流式响应

### 4.1 完整帧序列（`stream: true`，带工具调用）

**为什么给一个"带工具调用"的完整序列**：总纲第 6 章明确 "`tool_call` 的 id 按 SSE `index` 缓存重组"。只给纯文本的示例 无法表达 `index` 的用法，而 `index` 正是多工具调用并行时的唯一区分键。

```
HTTP/1.1 200 OK
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Free-Router-Request-Id: req_01J8...

data: {"id":"chatcmpl-req_01J8","object":"chat.completion.chunk","created":1748000000,"model":"smart","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-req_01J8","object":"chat.completion.chunk","created":1748000000,"model":"smart","choices":[{"index":0,"delta":{"content":"北京"},"finish_reason":null}]}

data: {"id":"chatcmpl-req_01J8","object":"chat.completion.chunk","created":1748000000,"model":"smart","choices":[{"index":0,"delta":{"content":"今天"},"finish_reason":null}]}

data: {"id":"chatcmpl-req_01J8","object":"chat.completion.chunk","created":1748000000,"model":"smart","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc123","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-req_01J8","object":"chat.completion.chunk","created":1748000000,"model":"smart","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-req_01J8","object":"chat.completion.chunk","created":1748000000,"model":"smart","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"北京\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-req_01J8","object":"chat.completion.chunk","created":1748000000,"model":"smart","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl-req_01J8","object":"chat.completion.chunk","created":1748000000,"model":"smart","choices":[],"usage":{"prompt_tokens":128,"completion_tokens":37,"total_tokens":165}}

data: [DONE]
```

四条要点：

| 要点 | 规则 | 为什么 |
|---|---|---|
| `model` 字段 | 恒为**请求里的抽象模型名** | 总纲 3.5：弃用后响应仍回显 `old` |
| `tool_calls[].index` | 同一轮里每个工具调用一个稳定的 `index` | 多工具并行时 `id` 可能分片到达，`index` 是唯一可靠的归并键 |
| 结尾 | 必须发 `data: [DONE]` | OpenAI 客户端的结束判据（验收第 1 条） |
| `usage` 帧 | `choices` 为空数组 | 与 OpenAI 的 `stream_options.include_usage` 行为一致；放在 `[DONE]` 之前 |

**为什么 `usage` 帧的 `choices` 是空数组而不是省略**：部分客户端会 无条件访问 `choices[0]`。给一个空数组会让它们走"没有 delta"的分支， 而不是崩溃。

### 4.2 流式与非流式的差异

| 维度 | `stream: true` | `stream: false`（默认） |
|---|---|---|
| 传输 | `text/event-stream` | `application/json` |
| 结束判据 | `data: [DONE]` | HTTP 响应体完整 |
| `usage` | 在末尾的独立帧（若 `include_usage`）或在 `finish_reason` 帧后 | 在响应体的 `usage` 字段 |
| 错误 | 已经吐过内容后**不能**再改状态码，只能用 `error` 事件帧 | 直接返回错误状态码 + 错误体 |
| 候选切换 | **仅在还没吐出任何可见内容时**允许 | 同左 |

**为什么流式的错误不能改状态码**：HTTP 状态码在第一个字节发出时就定了。 总纲 3.3 的策略引擎要求"一旦吐出可见内容就不能再换候选"， 对应的错误表达也就只能是流内事件。

### 4.3 流内的错误事件

```
data: {"error":{"message":"当前没有可用资源，请稍后重试","type":"service_unavailable_error","param":null,"code":"no_candidate"},"request_id":"req_01J8..."}

data: [DONE]
```

**为什么错误帧后还要发 `[DONE]`**：客户端在收到错误帧后会继续读流。 不发 `[DONE]` 会让它一直等到连接超时，把一次快速失败变成一次挂起。

## 5. `GET /v1/models`

### 5.1 响应

```json
{
  "object": "list",
  "data": [
    {"id": "smart", "object": "model", "created": 1748000000, "owned_by": "free-router"},
    {"id": "fast", "object": "model", "created": 1748000000, "owned_by": "free-router"}
  ]
}
```

**为什么 `owned_by` 是一个固定值而不是渠道名**：渠道名会泄露池结构 （总纲 3.1）。固定值同时满足 OpenAI 客户端对字段存在性的期待。

### 5.2 可见性过滤

返回集合 = **该 Key 的所属账户所属组的抽象模型授权** 减去 **该 Key 的黑名单**。

**为什么需要鉴权才能列出模型**：总纲 3.1 只说了"只列抽象模型"， 但同一实例上不同组的授权不同。没有 Key 就无法计算交集， 也就无法给出"这个 Key 实际能用什么"。因此 `/v1/models` 与另外两个端点 同级要求鉴权。

**为什么无鉴权时返回 401 而不是一个公共列表**：给公共列表等于 让未登录者拿到全站抽象模型清单，而这份清单本身是运营信息。

**为什么只能看到自己可用的**：这是总纲 11.3 的"三层收敛"在 目录端点上的自然推论。用户看到调不通的名字，只会得到 `model_not_found`。

### 5.3 弃用模型的表示

被弃用（`deprecated`）的抽象模型**仍然出现在列表里**，响应头带弃用信号：

```
HTTP/1.1 200 OK
Deprecation: @1688169599
Sunset: Sun, 30 Jun 2026 23:59:59 UTC
Link: </docs/models/smart>; rel="deprecation"
```

**为什么仍要列出**：总纲 3.5 定"弃用不是关闭，仍接受请求"。 从列表里移除会让自动化客户端认为"这个模型没了"，反而触发不必要的迁移。

**为什么头的语义分工是"`Deprecation` 说时间、`Sunset` 说不可用"**： RFC 9745 明确 deprecation 不改变资源行为，由 RFC 8594 的 `Sunset` 表达不可用；且 `Sunset` 不得早于 `Deprecation`。详见 [03-errors.md](03-errors.md) 第 5 节。

## 6. `POST /v1/messages`（Anthropic Messages）

### 6.1 请求结构

```json
{
  "model": "smart", "max_tokens": 1024, "system": "You are a helpful assistant.",
  "messages": [
    {"role": "user", "content": "北京今天天气如何？"}
  ],
  "tools": [
    {
      "name": "get_weather", "description": "查询指定城市的天气",
      "input_schema": {
        "type": "object",
        "properties": {"city": {"type": "string"}},
        "required": ["city"]
      }
    }
  ],
  "stream": true,
  "thinking": {"type": "enabled", "budget_tokens": 2048}
}
```

与 OpenAI 的三处结构性差异：

| 差异 | Anthropic | OpenAI |
|---|---|---|
| system | 顶层 `system` 字段（不是 messages 里的一条） | `messages[0].role = "system"` |
| 工具声明 | `name` / `description` / `input_schema` 平铺 | `type:"function"` + 嵌套 `function` |
| 最大输出 | `max_tokens` **必填** | 可选 |

**为什么 `max_tokens` 在 Anthropic 侧必填而在 OpenAI 侧可选**： 前者是协议规定，后者是协议允许省略。入站在 Anthropic 路径上 不做"帮你补一个默认值"的便利——补一个乐观的大值会让 `max_tokens` 超出候选上限从而触发一次本可避免的路由失败。

### 6.2 工具调用与思考块

助手消息里的 `tool_use` 与 `thinking` 块对应 IR 的 `parts[].tool_call` 与 `parts[].thinking`：

```json
{
  "role": "assistant",
  "content": [
    {"type": "thinking", "thinking": "用户想知道天气，需要调用工具。"},
    {"type": "text", "text": "我查一下。"},
    {"type": "tool_use", "id": "toolu_abc123", "name": "get_weather", "input": {"city": "北京"}}
  ]
}
```

工具结果作为**用户消息**里的 `tool_result` 块回传：

```json
{
  "role": "user",
  "content": [
    {"type": "tool_result", "tool_use_id": "toolu_abc123", "content": "{\"temp\": 18}"}
  ]
}
```

**为什么必须支持 `thinking` 块的回传**：总纲第 6 章把 `thinking` 列为 IR part 类型之一，验收第 2 条要求 Claude Code 能完成带工具调用的任务， 而 Claude Code 在开启思考时会回传 `thinking` 块。

### 6.3 SSE 帧序列

Anthropic 的流式用**具名事件**，帧序列与 OpenAI 不同：

```
HTTP/1.1 200 OK
Content-Type: text/event-stream
X-Free-Router-Request-Id: req_01J8...

event: message_start
data: {"type":"message_start","message":{"id":"msg_01J8","type":"message","role":"assistant","model":"smart","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":128,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"用户想知道天气"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"我查"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"一下。"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_abc123","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":":\"北京\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":37}}

event: message_stop
data: {"type":"message_stop"}
```

四条要点：

| 要点 | 规则 | 为什么 |
|---|---|---|
| `index` | `content_block` 级递增，与 OpenAI 的 `choices[].tool_calls[].index` 不是同一层 | 两套协议的 `index` 语义不同，映射必须显式 |
| 块生命周期 | 每个块必须成对出现 `content_block_start` / `content_block_stop` | Anthropic 客户端按此维护块状态 |
| 结束 | `message_stop`（**没有** `[DONE]`） | 协议规定 |
| 不使用具名 `ping` | MVP 不发 `ping` 事件 | 见下方待定项 |

**为什么 Anthropic 侧不发 `[DONE]`**：它的结束判据是 `message_stop`。 多发一个 OpenAI 风格的行不会让严格客户端更快结束，只会让它困惑。

### 6.4 非流式响应

```json
{
  "id": "msg_01J8", "type": "message", "role": "assistant", "model": "smart",
  "content": [
    {"type": "text", "text": "北京今天晴，18°C。"}
  ],
  "stop_reason": "end_turn", "stop_sequence": null,
  "usage": {"input_tokens": 128, "output_tokens": 37}
}
```

## 7. 入站层的横切规则

### 7.1 请求体上限

| 项 | 值 | 行为 |
|---|---|---|
| 入站上限 | 32 MiB（默认，可配置） | 413 + `request_too_large` |
| 上游上限 | 300 MiB | 由入站上限提前拦截，正常不会触达 |

**为什么入站上限远小于上游上限**：上游的 300 MiB 是它自己的限制， 不是我们的目标。在当前额度池的形态下，32 MiB 已经远超正常请求 （含 base64 图片），再放大只会把内存压力从上游搬到我们这里。

### 7.2 会话与连接

| 项 | 规则 | 为什么 |
|---|---|---|
| keep-alive | 空闲 120 秒后由上游发 FIN，客户端自管连接池 | 总纲第 5 章：上游行为，不能靠默认 `http.Client` |
| 首次请求偶发失败 | 上游客户端必须有重试 | 总纲第 5 章实测：首次带 Bearer 的请求偶发 `http=000` |
| 请求 ID | 每个入站请求生成，回写 `X-Free-Router-Request-Id` | 总纲第 11 章的双向对账 |
| 超时 | 首字节超时与总超时分别配置 | 流式响应的总长度不可预测 |

**为什么请求 ID 要发给客户端**：总纲第 11 章的打标对账依赖 `x-free-router-request-id` 头 + `requests` 表双向对账。客户端拿着这个 ID， 用户报障时管理员可以一次定位到具体请求。

### 7.3 调试响应头

`x-fr-trace` 可以把真实落点回显给**管理端账户**或**特定组的 Key**（总纲 3.6）。

```
x-fr-trace: real=dsh:deepseek-official/deepseek-flash; cred=cred_01J8; attempt=1
```

**为什么它必须是可选且默认关闭**：这是全站唯一一个会 把真实模型名送出租户边界的机制。默认关闭、按范围开放， 是把"泄露面"限制在一个显式枚举的集合里。

### 7.4 不静默降级

总纲第 6 章的硬规则：**缺失能力明确报错，绝不悄悄丢图片或 `tool_use`**。

| 情况 | 行为 |
|---|---|
| 请求含图片，但候选不支持视觉 | 400 + `capability_unsupported` |
| 请求含 `tools`，但候选不支持工具调用 | 400 + `capability_unsupported` |
| 请求含 `response_format`，但候选不支持 JSON mode | 400 + `capability_unsupported` |
| 请求 `reasoning_effort` 超出候选档位 | 400 + `capability_unsupported` |

**为什么这条不能妥协**：静默丢弃图片或工具调用会让客户端拿到一个 "看起来成功但语义错误"的结果。这类错误在用户的业务侧表现为 "模型好像没看见图片"，排查成本极高，而归因完全指向我们。

## 8. 与其它分册的接口

| 依赖 | 说明 |
|---|---|
| [03-errors.md](03-errors.md) | 错误码表、HTTP 状态码映射、错误体形状、响应头清单 |
| [02-admin.md](02-admin.md) | 抽象模型与候选集的配置面，决定本层的路由结果 |
| [../ui/04-tenant.md](../ui/04-tenant.md) | 日志页展示的字段即本层的可观测子集 |

## 9. 待定项

> **待定**：`/v1/messages` 是否需要发送 Anthropic 的具名 `ping` 事件 （长思考期间的保活）？当前 MVP 不发，但部分客户端可能据此判断连接存活。

> **待定**：`/v1/models` 在未鉴权时是否有必要提供一个 "仅名称、无能力细节"的公开列表？当前一律 401。

> **待定**：`reasoning_effort` 在 Anthropic 路径上如何映射？ Anthropic 用 `thinking.budget_tokens`，OpenAI 用枚举档位， 两者的换算关系总纲未定义。

> **待定**：`/v1/responses` 与 `generateContent`（P1）的 IR 映射细节， 是否需要在 MVP 的 IR 设计里预留（当前 IR 已含 `parts[]`， 但两个协议的输入结构差异较大）。
