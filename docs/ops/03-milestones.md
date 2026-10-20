> 分册：运维 · 依赖：[01-security.md](01-security.md)、[02-deploy.md](02-deploy.md) · 对应总纲：[PLAN.md](../PLAN.md) 第 9、12 章

# 里程碑与验收

把总纲第 9 章的 M0–M10 拆到**可执行任务粒度**：每项给出产出物、
依赖、以及**可以直接敲的验收命令**。

**为什么验收要写成命令**：文档里的「已实现」是主观判断，命令的输出是客观的。
每个里程碑结束时把这些命令跑一遍，把输出贴进 PR 描述——这样「完成」
有可复核的证据，而不是靠记忆。

---

## 1. 总览与依赖

```
M0 骨架与契约锁定
 └─ M1 上游接入与配额探测
     ├─ M2 模型目录与鉴定
     ├─ M3 账户与鉴权 ── M4 额度与限速 ── M5 API 出口
     │                                      └─ M6 抽象模型与策略路由
     │                                          └─ M7 组与授权
     └─ M8 贡献与 sidecar
                └─ M9 界面完善 ── M10 加固与上线
```

**关键路径**：M0 → M1 → M3 → M4 → M5 → M6 → M10。
M2 与 M8 可与主线并行；M7 必须在 M6 之后（组的授权对象是抽象模型）。

**工日估算**（单人全职，含测试与文档）：

| 里程碑 | 工日 | 可并行 |
|---|---|---|
| M0 | 1 | — |
| M1 | 1.5 | — |
| M2 | 1.5 | 与 M3 并行 |
| M3 | 1.5 | 与 M2 并行 |
| M4 | 2.0 | — |
| M5 | 2 | — |
| M6 | 2.0 | — |
| M7 | 1.5 | — |
| M8 | 2.0 | 与 M5–M7 并行 |
| M9 | 3.0 | — |
| M10 | 1.5 | — |
| **合计** | **19.5** | 关键路径约 **14 天** |

**M8 为何由 2.5 下调到 2.0**：贡献者无偿（PLAN 4.5）后取消了"回报"这一整块——没有额度折算、没有优先使用权的发放与对账、没有升级凭据的判定，只剩一个只读的贡献统计视图。按原估算里收益模块约占 0.5 工日扣减；M8 不在关键路径上，所以 14 天不变。

**2026-10-20 对齐后的工时变化（合计 19.0 → 19.5）**：

| 里程碑 | 旧 | 新 | 为什么 |
|---|---|---|---|
| M4 | 1.5 | 2.0 | 新增价格乘数表（内置 + 管理员可调 + 改入审计），它是 `credit` 维度与 `price` 排序策略的共同数据源；同时额度从 Key 级移到账户级并支持三种计量单位，判定逻辑复杂度上升 |
| M6 | 2.5 | 2.0 | 排序策略从七种减到四种（“价格”与 `price` 合并、“延迟”与 `speed` 合并，权重 / 质量分层 / 额度感知三种彻底删除），弃用链删掉跳数上限、深度告警、超限降级三套分支——少写的是分支，不是能力 |
| M9 | 2.5 | 3.0 | 新增待处理事项页（`/admin/inbox` + `admin_inbox` 表 + 目录刷新任务写入点 + 未读计数 + 轮询），大约半个工日 |

M3 **保持 1.5 不变**：取消类级提权拿掉了「进管理面再验 + 四项危险操作逐次重验 + 会话表里的再验时间戳」这套机制（约 −0.5），
但新增的「登录强度三项定值」（argon2id 参数、退避曲线、锁定阈值都要调参并逐项验证）约 +0.5，两者基本抵消。

**关键路径仍约 14 天**：在关键路径上的只有 M4（+0.5）与 M6（−0.5）两处且互相抵消；
M9 新增的待处理事项页与 M7 的组管理页可并行，不拉长主线。

---

## 2. M0 — 骨架与契约锁定

**目标**：仓库可跑、配置可读、对上游的行为有测试锁住。

### 任务

- [ ] `go mod init`，依赖：`chi`、`modernc.org/sqlite`、`golang.org/x/crypto`、`slog`
- [ ] 目录骨架按 [../architecture/01-overview.md](../architecture/01-overview.md)
- [ ] `cmd/freerouter/main.go`：配置加载 + 路由注册 + 优雅关闭
- [ ] `internal/config`：结构体 + YAML + 环境变量覆盖（`FR_` 前缀）
- [ ] `internal/upstream/dshprovider` 契约测试（锁主进程 ↔ sidecar 的 IPC 契约）：
  - [ ] `listModels` → 返回带 `id` / `display_name` / `provider_id` 的条目
  - [ ] `invoke` 非流式 → 响应形状（含 `usage`）
  - [ ] `invoke` 流式 → 事件帧形状（`start` / `delta` / `usage` / `done`）
  - [ ] 未知方法名 → 结构化错误，不是进程崩溃
  - [ ] sidecar 中途死掉 → 主进程返回 5xx 且自动重拉
- [ ] 不再有「从 `$DSH_HOME/openai-gateway/api-key` 自举读 key」这一步：
  `relay` 用贡献者自己的供应商登录态，没有网关层 key 可读（ALIGN-SPEC H）
- [ ] `Makefile`：`build` / `test` / `lint` / `contract`

### 产出物

| 路径 | 内容 |
|---|---|
| `cmd/freerouter/main.go` | 入口 |
| `internal/config/*.go` | 配置 |
| `internal/upstream/dshprovider/contract_test.go` | **契约测试（最重要的产出）** |
| `Makefile` | 构建入口 |

### 验收

```bash
make build && ./bin/freerouter --version
make contract        # 必须全绿，且能检测到上游偶发连接失败
./bin/freerouter --config testdata/config.yaml --print-config
```

**契约测试必须记录实测数据**：跑一次把「模型总数、上游前缀数」写进测试注释，
上游升级后数字突变即说明目录变了。

> **注意**：契约测试依赖本机 DSH 正在运行。CI 中应标记 `-short` 跳过或使用
> 录制的 fixture；本地开发必须能联通真实上游。

---

## 3. M1 — 上游接入与配额探测

**目标**：能列出上游模型、能发起一次真实的对话请求、能读出配额。

### 任务

- [ ] `internal/upstream`：`Upstream` / `Credential` / `Quota` 抽象（见 [../architecture/03-upstream.md](../architecture/03-upstream.md)）
- [ ] 连接池：`idle_timeout ≤ 120s`、连接数上限、健康标记
- [ ] **重试**：仅对未吐内容的请求；指数退避；记录 `http=000` 类连接失败
- [ ] 经 sidecar 的 `listModels` 拉模型目录并落 `real_models`（含 `last_seen_at`）
      —— 不再直接打 `/v1/models`（ALIGN-SPEC H）
- [ ] 探测上游配额（余额 / 到期时间，能拿到多少拿多少）
- [ ] `GET /admin/api/upstreams/{id}/health` 返回健康状态与延迟
- [ ] 结构化日志：请求 ID、上游、耗时、状态码

### 验收

```bash
# 上游健康
curl -s localhost:8080/admin/api/upstreams -H "Cookie: $ADMIN_COOKIE" | jq
# 模型目录已落库
sqlite3 data/freerouter.db "SELECT count(*) FROM real_models;"
# 重试生效：停掉 sidecar 后请求应重试并记录失败原因
```

**必测的边界**：把 sidecar 杀再发请求 → 应在 `connect_timeout` 内失败并给出
可读错误，而不是挂住 300 秒；同时主进程不能跟着死。

---

## 4. M2 — 模型目录与鉴定

### 任务

- [ ] `internal/verify`：三级流水线（L0 形式 / L1 标准 / L2 深度）
- [ ] L0：`/v1/models` 含声称 id、端点黑白名单、响应形状与错误 schema
- [ ] L1：数值随机指纹（约 300 个整数）、自报身份、知识截止、
      能力探针（工具调用 / JSON mode）、计费一致性（独立分词器估算）、
      已知答案金丝雀
- [ ] L2：上下文窗口二分、视觉已知答案图、logprobs 分布、流式真实性、
      隐藏 system prompt 开销、多轮重复
- [ ] 指纹库：从 lmfpd 参考库 + zing 的 YAML + 自有官方渠道采样导入
- [ ] 判定：`verified` / `suspect` / `unverified` / `trusted`
- [ ] `--max-context-tokens` 等参数可配
- [ ] **定时复检 + 回归告警**（得分相比上次下滑也告警）

### 验收

```bash
# 对一个已知真货跑鉴定
freerouter verify --upstream relay --model deepseek-official/deepseek-flash --level L1
# 应输出各维度得分与最终判定 verified

# 冷门模型应放行但标记
freerouter verify --upstream X --model 某冷门模型 --level L1
# 期望 unverified + 进复检队列，而不是 suspect
```

**关键验收点**：**没有参考指纹的模型必须放行**（用户明确要求）。
用一个库里没有的模型跑一遍，确认判定是 `unverified` 而非拒绝。

---

## 5. M3 — 账户与鉴权

### 任务

- [ ] `accounts` 表 + `account_classes` 多行（权限类集合）
- [ ] 初始化向导：无账户时只监听回环，创建管理员（`{admin, tenant}`）
- [ ] 注册 → `pending`；审核队列（批准选组 / 拒绝填理由）
- [ ] `admin_inbox` 表 + **新注册待审核**的写入点（ALIGN-SPEC G）：注册发生时就写一条，
      管理员登录能看到未读计数；页面本身在 M9 做，建表放在这里是因为
      注册流程在本里程碑就产生数据，等到 M9 才建表会把已存在的待审核申请丢掉
- [ ] 管理员 CSV 导入账密（`用户名,密码[,组][,日额度]`；留空则生成随机密码一次性展示）
- [ ] 登录：argon2id + 会话（HttpOnly/Secure/SameSite=Lax）+ 防枚举 + 防爆破
- [ ] **登录强度三项定值**（本版没有类级提权，登录是唯一防线）：argon2id 参数（内存 / 迭代 / 并行度写具体数值，不随硬件自适应）、退避曲线（起点 / 倍数 / 上限）、锁定阈值（次数 / 时长 / 解除方式）
- [ ] **禁用联动**：账户 `disabled` → 名下 Key 立即 401、会话立即吊销
- [ ] 审计：`account_id + acting_class + ip + ua + result`（已删 `elevated`，ALIGN-SPEC A）
- [ ] 自助改密、管理员重置、改密即吊销会话

### 验收

```bash
# 防枚举：两种用户名的响应时间与文案一致
for u in exists_user brand_new_user; do
  curl -s -o /dev/null -w "%{time_total} %{http_code}\n" \
    -X POST localhost:8080/api/auth/login -d "{\"username\":\"$u\",\"password\":\"x\"}"
done

# 禁用联动
sqlite3 data/freerouter.db "UPDATE accounts SET status='disabled' WHERE id=3;"
curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY_OF_ACCOUNT_3"   # 期望 401，且立刻

# 登录强度三项按定值生效，会话 24h 过期
curl -s localhost:8080/admin/api/settings -H "Cookie: $ADMIN_COOKIE" | grep -A3 argon2
# 手工验证：错密码重试至第 6 次开始退避，超过锁定阈值后临时锁定
# session_ttl 回显为 24h；改密后旧会话立即 401
```

**关键验收点**：禁用后**不等待**任何缓存过期，下一次请求就必须 401。

---

## 6. M4 — 额度与限速

### 任务

- [ ] 三维令牌桶：全局 / **按账户** / 按 IP
- [ ] **账户级额度（三维度 `request` / `token` / `credit`）**：日 / 月额度、并发上限、RPM / TPM
      —— Key 没有自己的额度，一个账户的所有 Key 共享同一份（ALIGN-SPEC B）
- [ ] 组级策略合并（放开取并集、限制取最严）
- [ ] 超限 → `429` + `Retry-After`
- [ ] 全局预算熔断 + 告警
- [ ] 用量记账：`requests`（明细，按 `account_id` / `api_key_id` / `abstract_model_id` / `ts`）+ `usage_daily`（日聚合看板）
- [ ] 异常用量检测（突增告警）
- [ ] 价格乘数表：内置常见模型（基准 `deepseek-flash` = 1，表外默认 1）+ 管理员可调（改动进审计）

### 验收

```bash
# 打满 RPM
for i in $(seq 1 100); do curl -s -o /dev/null -w "%{http_code} " \
  localhost:8080/v1/chat/completions -H "Authorization: Bearer $KEY" \
  --data @small.json; done; echo
# 期望：前 N 个 200，之后 429 且带 Retry-After
# 注意 N 是**账户级**额度：换同一账户下的另一把 Key 再打，仍然 429

# 并发上限
xargs -P 10 -I{} curl -s -o /dev/null -w "%{http_code}\n" ... <<< "1 2 3 4 5 6 7 8 9 10"
# 期望：超过 max_concurrency 的返回 429

# 扣减时机（ALIGN-SPEC D）：请求结束立即同事务扣减
# 发一个正常请求后查 usage_daily，应已 +1 次、token 数对得上
sqlite3 ... "SELECT requests, tokens FROM usage_daily ORDER BY day DESC LIMIT 1;"
# 本地拒绝（如额度打满）不应扣减；打到上游但上游 500 仍按实 usage 扣

# 全局熔断
sqlite3 ... "UPDATE settings SET value='100' WHERE key='limits.global_daily_tokens';"
# 期望：全站拒绝 + 审计中一条熔断事件（验完改回 0 = 不熔断）
```

---

## 7. M5 — API 出口

**目标**：对外可用，且**只暴露抽象模型**（M6 之前可先用真实模型打通链路）。

### 任务

- [ ] `POST /v1/chat/completions`（流式 + 非流式）
- [ ] `POST /v1/responses`（OpenAI Responses API）
- [ ] **`POST /v1/messages`（Anthropic Messages 入站）**：内容块映射、`tool_use` / `tool_result`、扩展思考（`thinking`）、具名事件 SSE（`message_start` / `content_block_delta` / `message_stop`）、`event: error` 错误帧——验收命令见下
- [ ] `GET /v1/models`（只列**抽象模型**）
- [ ] Key 鉴权中间件（SHA-256 查表 + 常量时间比较 + 账户状态实时校验）
- [ ] SSE 透传（**不在中间缓冲**）+ TTFT 记录
- [ ] 失败切换：**仅在未吐出任何可见内容时**换候选
- [ ] 错误映射：上游错误 → 对外错误码；无候选 → **503 + 专用错误码**
- [ ] 请求日志（默认不记正文）

### 验收

```bash
# 流式首字延迟（不能等到全部生成完才返回）
curl -N -s localhost:8080/v1/chat/completions -H "Authorization: Bearer $KEY" \
  --data '{"model":"<抽象模型>","messages":[{"role":"user","content":"数到 100"}],"stream":true}' \
  | head -c 200
# 期望：立即看到分片，而不是停顿后一次性吐出

# 真实模型名必须 404（不泄露池结构）
curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  --data '{"model":"deepseek-official/deepseek-flash","messages":[]}'   # 期望 404

# 无候选 → 503（与「模型不存在」的 404 区分）
curl -s -o /dev/null -w "%{http_code}\n" ... --data '{"model":"全渠道耗尽"}'

# Anthropic Messages 入站：期待具名事件流（GPT 客户端不适用，用 Claude 格式验）
curl -N -s localhost:8080/v1/messages -H "x-api-key: $KEY" -H "anthropic-version: 2023-06-01" \
  --data '{"model":"<抽象模型>","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}' \
  | head -5   # 期望：event: message_start / content_block_delta / message_stop 具名事件
```

**关键验收点**：**切换只发生在未吐内容时**。构造一个「第一个候选返回 200 后
中途失败」的场景，确认不会出现两份内容拼在一起的串话。

---

## 8. M6 — 抽象模型与策略路由

### 任务

- [ ] `abstract_models` + `abstract_model_candidates`（声明式候选集，**不绑凭据**）
- [ ] **弃用链**：只能指向 `active` 目标，`deprecated` 单向不可恢复；
      不加跳数上限（不做 5 跳 / 深度告警 / 超限降级）；保留 visited 集兜底 + 检环降级为无替代
- [ ] 能力声明取保守下界（上下文/输出取 min，vision/工具/JSON 取交集）
- [ ] 策略引擎三段式：过滤 → 排序 → 切换
- [ ] 排序策略内置**四种**：`expiry-first` / `balance` / `speed` / `price`，可组合成链
- [ ] **默认链 `[expiry-first, balance]`**（先按到期时间，同到期再按余额）
- [ ] `round-robin` 保留为**可选**的平局决胜（放链尾，不属默认链）
- [ ] 冷却与健康：失败候选短期冷却，避免反复打同一失败目标
- [ ] 默认排除上游 `aggregate/*` 作为候选
- [ ] 调试响应头（`x-fr-trace`，仅管理端或特定组）
- [ ] 真实落点仅管理端可见

### 验收

```bash
# 同一个抽象模型，两次请求可能落到不同真实上游（轮换/临期优先生效）
for i in 1 2 3; do
  curl -s localhost:8080/v1/chat/completions -H "Authorization: Bearer $KEY" \
    -H "X-FR-Trace: 1" --data '{"model":"balanced",...}' -D- -o /dev/null \
    | grep -i x-fr-trace
done

# 候选全部不可用 → 503
# 把候选渠道全禁用
sqlite3 ... "UPDATE upstreams SET enabled=0;"
curl -s -o /dev/null -w "%{http_code}\n" ...   # 期望 503 + 专用错误码
```

**关键验收点**：`/v1/models` 只能看到抽象模型名，**看不到任何真实模型名**。

---

## 9. M7 — 组与授权

### 任务

- [ ] `groups` / `group_members` / `group_models` / `group_policies` / `group_quota_pools`
- [ ] 组的策略合并语义（放开取并集、限制取最严）
- [ ] 默认组（新账号自动加入；删组不删人，退回默认组）
- [ ] 管理面：组列表 / 组详情 / 租户批量加入移除
- [ ] 授权对象是**抽象模型**
- [ ] 组内用量汇总（管理员视角）
- [ ] 组变更进审计

### 验收

```bash
# 两组合并：模型取并集
# 组 A 给 {fast}，组 B 给 {smart}，成员同时在两组合 → 可用 {fast, smart}
curl -s localhost:8080/v1/models -H "Authorization: Bearer $KEY" | jq -r '.data[].id'

# 限制取最严：组 A 日额度 1000，组 B 日额度 100，成员在两组合 → 实际 100
sqlite3 ... "SELECT effective_daily FROM ...;"   # 期望 100

# 成员互不可见
curl -s localhost:8080/api/group/members -H "Cookie: $TENANT_COOKIE"  # 期望 403/404
```

**关键验收点**：**限制必须取最严**。故意让一个「宽」组和一个「严」组同时
包含同一成员，确认生效的是严的那个。

---

## 10. M8 — 贡献与 sidecar

### 任务

- [ ] `freerouter-sidecar`（Node 子进程）：**stdio + NDJSON**（不提供 socket / TCP 选项）
- [ ] 复用 `dsh-providers-hub` 模块：`listProviders` / 各渠道 OAuth 登录流 / 额度查询；
      **provider adapter（`lib/*-adapter.js`）由 sidecar 进程内 `import()` 直调**，
      不再绕 `127.0.0.1:8326` 的 OpenAI 兼容网关（ALIGN-SPEC H）
- [ ] 贡献页：选渠道 → 一次性贡献会话 → OAuth/设备码/扫码/短信
- [ ] 凭据 AES-GCM 加密入库（主密钥独立于库）
- [ ] 自动探测（查余额 + 探模型目录）→ 入池或拒绝
- [ ] 贡献者可一键撤销
- [ ] **API key 型贡献走 M2 鉴定流水线**（冷门放行 + 标未鉴定）
- [ ] 贡献者账户：贡献即生效，给恢复码 / OAuth 找回
- [ ] 虚假凭据防护：连续失败自动禁用

### 验收

```bash
# sidecar 起不来不影响主流程
FR_SIDECAR_ENABLED=false ./bin/freerouter
# 期望：正常服务，贡献页显示「功能不可用」

# 凭据加密（库里搜不到明文）
strings data/freerouter.db | grep -i "sk-\|api_key\|Bearer"   # 期望：无命中

# 贡献者看不到自己的凭据明文
curl -s localhost:8080/api/contributions -H "Cookie: $CONTRIB_COOKIE" | jq
# 期望：只有 provider / 状态 / 用量，无 secret 字段
```

**关键验收点**：`strings` 检查必须**零命中**。这是加密是否真的生效的
最直接证据。

---

## 11. M9 — 界面完善

### 任务

- [ ] 公共页：登录 / 注册 / 待审核 / 初始化向导
- [ ] 管理员页：仪表盘、账户与审核、组、抽象模型与策略、模型目录与鉴定、
      上游与凭据、日志与审计、设置
- [ ] 租户页：总览、Key 管理、用量、文档
- [ ] 贡献者页：贡献管理、贡献统计、我的贡献
- [ ] 空状态 / 加载态 / 错误态 / 权限不足态（每页都要有）
- [ ] 顶部未读计数入口（登录后拉一次，点击进 `/admin/inbox`）
- [ ] 危险操作二次确认（删账户、删组、改抽象模型、改弃用）——**这不是认证，是防误点**（ALIGN-SPEC A）
- [ ] **待处理事项页 `/admin/inbox`**（ALIGN-SPEC G）：汇总新注册待审核 / 上游模型变动 /
      故障通知 / 组池与全站熔断告警；登录时顶部显示未读计数 + 页面轮询，**不做实时推送**
- [ ] `admin_inbox` 表与写入点：后台目录刷新任务检测到变动立即写一条记录
      （`id, kind, severity, title, detail_json, related_id, created_at, read_at, read_by`）
- [ ] 弃用配置的**链路预览**（`old → mid → new（active）`）
- [ ] 移动端基本可用

### 验收

见 [../ui/06-design.md](../ui/06-design.md) 的交互与视觉约定；
命令侧检查：

```bash
# 每页无 JS 报错（用 headless 跑一遍）
npx playwright test --project=chromium
# 权限不足必须有可见提示，而不是白屏
```

---

## 12. M10 — 加固与上线

### 任务

- [ ] 逐条过 [01-security.md](01-security.md) §8 的 12 项检查
- [ ] TLS（内置 ACME 或反代）+ HSTS + 安全响应头
- [ ] 容器非 root / read-only / cap-drop
- [ ] `govulncheck` 无高危
- [ ] 备份与恢复演练（**真的恢复一次**）
- [ ] 压测：并发流式请求下的内存与延迟
- [ ] 文档：部署、运维、故障排查
- [ ] 初始化演练：从空库到可用的完整流程

### 验收

```bash
# 安全基线 12 项
./scripts/security-checklist.sh    # 逐项输出 PASS/FAIL

# 非回环 + 无 TLS：默认只告警；把策略升级为 deny 后才拒绝启动
FR_SERVER_LISTEN=0.0.0.0:8080 FR_TLS_MODE=off ./bin/freerouter            # 期望：启动并打印醒目警告
FR_SERVER_LISTEN=0.0.0.0:8080 FR_TLS_MODE=off FR_SERVER_INSECURE_PUBLIC_LISTEN=deny ./bin/freerouter  # 期望：拒绝启动

# 恢复演练
sqlite3 ... "VACUUM INTO '/tmp/backup.db'"
rm data/freerouter.db && cp /tmp/backup.db data/freerouter.db
./bin/freerouter --verify-store    # 期望：库可打开 + 主密钥可解密测试凭据
```

---

## 13. 每个里程碑的通用完成标准

一个里程碑只有在**同时**满足以下四条时才算完成：

1. **代码**：`go vet ./...` 与 `golangci-lint run` 无新增告警
2. **测试**：新增逻辑的单测覆盖，关键路径有集成测试
3. **验收命令**：本册对应章节的命令全部跑通，输出贴进 PR
4. **文档**：相关分册同步更新（不是「以后再补」）

**为什么把文档算进完成标准**：方案文档是实现的一部分——没有文档的
里程碑在下一个里程碑开始时就会失真，而失真累积到 M10 就变成
「文档与代码两张皮」，运维时无法判断以哪个为准。

---

## 14. 阻塞项（开工前需要）

| 项 | 影响里程碑 | 现状 |
|---|---|---|
| 账户模型 A / B（总纲 15 章第 1 项） | M3 | **已定案 2026-10-19：选 A**（单账户 + 权限类集合）；**2026-10-20 补：取消类级提权**（登录一次 = 全部权限，会话 24h）→ **M3 已解锁，可开工** |
| 贡献者回报机制 | M8 的贡献者侧 | **已定案 2026-10-19：贡献者无偿**（不折算额度、不给优先使用权、不给升级凭据，只保留贡献统计）→ **M8 贡献侧已解锁**；取消"收益"模块，只有统计呈现，工时 2.5 → **2.0** |
| ~~前端选型（Next.js / Go 模板 + htmx）~~ | ~~M9~~ | **已定案 2026-10-19：Vite + React + `go:embed` 单二进制** → M9 可开工 |
| ~~组级共享额度池是否进 MVP~~ | ~~M7~~ | **已定案：进 MVP**（2026-10-19，每人上限与组级共享池可单用可并用） |
| ~~部署位置 / 反代方案~~ | ~~M10~~ | **已定案：先本地跑；不用反代，默认 IP + 端口**（2026-10-19）；**域名**仍待用户提供 |

**其它（不阻塞）**：平台自有凭据归属、Prometheus 指标。

**M0–M3 不依赖任何待定项**，可以立即开工——M0 的契约测试与
M1 的上游接入都与账户模型无关，M3 的账户模型也已定案（选 A）。

---

## 15. 相关分册

| 主题 | 详见 |
|---|---|
| 分层与进程模型 | [../architecture/01-overview.md](../architecture/01-overview.md) |
| IR 与协议映射 | [../architecture/02-ir.md](../architecture/02-ir.md) |
| 上游适配 | [../architecture/03-upstream.md](../architecture/03-upstream.md) |
| sidecar IPC | [../architecture/04-sidecar.md](../architecture/04-sidecar.md) |
| 安全基线清单 | [01-security.md](01-security.md) |
| 部署与配置 | [02-deploy.md](02-deploy.md) |
| UI 交互与视觉约定 | [../ui/06-design.md](../ui/06-design.md) |
