# FreeRouter 设计决策复核清单

> **归谁读**：项目所有者。**用途**：把「写文档时自行拍板、你从未说过」的设计选择一次性摆出来，你逐条看有没有不认可的。
> **产生方式**：四个只读审计员分别扫描 `docs/architecture/**`、`docs/security/**`、`docs/models/**`、`docs/ui/**` + `docs/api/**` + `docs/contributions/**`、`docs/ops/**`，穷举「不在你的明确指令清单里」的设计选择。原始条目约 500 条，去重归并后**本文件共 365 条**：第 0 章精选 12 条（影响面最大）+ 第 1–7 章按域展开 353 条（架构 78 / 安全 53 / 模型 50 / 对外契约 50 / 界面 70 / 贡献 17 / 运维 35）。
> **标记**：`[无理由]` = 文档只给了结论没给论证（按 README 文档约定这是缺陷，但结论本身可能仍然正确）。
> **怎么用**：只需告诉我「第几节 / 哪几条不接受」，我改文档；没提到的默认保留。
> **时间点**：归集于 2026-10-20；第 8 章的 11 条内部矛盾已在同日全部修完（不影响你的判断）。

---

## 0. 一旦不同意就要大返工的 12 条（先看这节）

| # | 决策 | 位置 | 说明 |
|---|---|---|---|
| 0.1 | **抽象模型的能力声明取「保守下界」**：上下文与最大输出取候选的 min，视觉/工具调用/JSON mode 取**交集**；默认 `strict` 模式 | `models/02-abstract.md:142-148,186-195` | 另一条路是取并集（声明能力更强，但请求可能中途失败）。你如果选并集，`models/02`、`models/03`、`models/04` 三册连改 |
| 0.2 | **组共享池与个人额度「都设时取交集」**（各管各的），多组都设池时「逐个都要满足」 | `security/03-groups.md:141,176-185` | 另一种理解是「取最严」，那会把「组共 500 次 + 每人 100 次」压成每人 100 次、池子永远碰不到。我按交集定了 |
| 0.3 | **组共享池「终态一次结算」**（流结束后按实际用量扣），周期总消耗可略超上限，上界 = 峰值并发 × 单请求消耗 | `security/03-groups.md:157-163,221-229` | 另一条路是请求前预扣、结束返还；我否决了它（认为返还写错方向会变成「无故停全组」） |
| 0.4 | **类级提权窗口 15 分钟**，危险操作**恰好四项**（删账户/改组/改抽象模型/改弃用）且**每次重验**；清单硬编码、不允许扩展 | `security/02-authn.md:171,175-211` | **已确认（2026-10-20 用户拍板）**：15 分钟 / 恰好四项 / 每次重验 / 硬编码不外扩，四项都已由用户确认，不再是「我定的」。改时长仍要动 security 02/03/04/06 + ops 会话表；改清单要动安全 02/03/04 + api 02 + ui 01/03/06 + Q126/Q152/Q153 |
| 0.5 | **弃用替代链深度上限 5 跳**（跨册硬常量），配置时 + 运行时**双处环检测** | `security/04-authz.md:115`、`models/04-deprecation.md:52,81-82` | 超限或成环时**降级为「无替代」而不报错**（照样接受请求、回显旧名） |
| 0.6 | **Key 默认额度 0、白名单默认空**，且每个 Key **强制**有日/月 token 上限与并发上限（未设时由全局默认兜底，不得理解为无限制） | `security/05-keys.md:35,82-83,98` | 默认值取「拒绝」。若你想让新建 Key 立刻可用，要改这里 + 建 Key 流程 |
| 0.7 | **限定名格式 `dsh:<渠道>/<上游模型 id>`** | `models/01-catalog.md:24,29-31` | 一旦定了要改命名，目录/抽象/弃用/文档全要迁移 |
| 0.8 | **默认排序链 `[expiry-first, round-robin]`** | `models/03-strategy.md:112,121` | 直接决定哪个下游账号先被限流。`expiry-first` 是你说的「临期优先」的延续；`round-robin` 是我加的平局决胜（否则同渠道多账号会总打同一个） |
| 0.9 | **对外错误码新造 4 个**：`model_retired`→410、`upstream_error`→502、`upstream_timeout`→504、`pool_exhausted`→429 | `api/03-errors.md:90-96` | 总纲只提到 `upstream_unavailable` 与「无候选 503」。这些名字一旦被客户端 SDK 硬编码就不好改 |
| 0.10 | **自定 6 个 `x-ratelimit-*` 响应头**（`-limit-requests` / `-remaining-requests` / `-reset-requests` / `-limit-tokens` / `-remaining-tokens` / `-reset-tokens`） | `api/03-errors.md:243-248` | 直接照抄 OpenAI 命名，但字段集合是我定的 |
| 0.11 | **管理端路由 19 条 + 租户前缀 `/usage` + `/contribute` 与 `/contributions` 拆两个路由** | `ui/01-ia.md:142-194` | 路由名是书签/文档/SEO 的同义词，改名成本高 |
| 0.12 | **里程碑 M0–M10 与工日估算**：两处已合并为同一套（M0–M10 同名同内容），合计 **19.0 工日**、关键路径 **14 天** | `ops/03-milestones.md:16-45` 与 `PLAN.md:930-942` | 19.0/14 天是单人全职估时，含测试与文档。若你觉得估算太乐观或太保守，改 `ops/03-milestones.md` 第 1 节即可（它是权威） |

---

## 1. 架构与运行时（`docs/architecture/**`）

| # | 决策 | 位置 |
|---|---|---|
| 1.1 | 分 **L1 入站 / L2 内核 / L3 上游** 三层：L1 不认识真实模型，L2 不认识 HTTP 协议 | `01-overview.md:32-77` |
| 1.2 | 层间只传 `ir.Request` 与 `ir.Event` 流，禁传 `map[string]any` / 原始字节 / 协议结构体 | `01-overview.md:67-77` |
| 1.3 | **两进程模型**：Go 主进程 + Node sidecar；不 import、不写 cordis 插件 | `01-overview.md:171-196` |
| 1.4 | **IPC 用 stdio + NDJSON**（一行一帧），借用 JSON-RPC 2.0 字段名但不实现 batch/notification | `04-sidecar.md:37-53`（已全库统一，见第 8.1 条） |
| 1.5 | sidecar **无状态、一问一答、不主动推送**，`id` 由主进程分配 | `04-sidecar.md:52-57` |
| 1.6 | sidecar 三条红线（只读凭据 / 不碰 `state.json` / 推理不经本机 HTTP 网关），并做成真实错误码 `E_CREDENTIAL_READONLY`、`E_STATE_FORBIDDEN` 当守卫 | `04-sidecar.md:199-231` |
| 1.7 | sidecar **不感知业务概念**（IPC 参数里不出现 `abstract_model`/`api_key`/`account_id`） | `04-sidecar.md:192-195` |
| 1.8 | 控制面与数据面**同进程** | `01-overview.md:311` |
| 1.9 | 中间件顺序**固定 11 步**：恢复/请求 ID → 安全头 → 可信代理由 → body 上限 → 鉴权 → 限速 → 配额 → 抽象模型解析 → 协议解析 → L2 → 响应编码 | `01-overview.md:80-104` |
| 1.10 | 入站请求体上限 **32 MiB**（可配），超限 **413 且不读 body** | `01-overview.md:88`、`03-upstream.md:363-373` `[无理由]`（为何是 32） |
| 1.11 | 上游请求体上限 **300 MiB** → 413 `request_too_large`（反代类尤其要注意：provider adapter 自己也限额） | `03-upstream.md:366` |
| 1.12 | 限速三维令牌桶：**全局 / 按 Key / 按 IP** | `01-overview.md:90` `[无理由]` |
| 1.13 | MVP 只做 `/v1/chat/completions` 与 `/v1/messages`；`/v1/responses` 与 Gemini 入站列 P1 | `01-overview.md:105-118` `[无理由]` |
| 1.14 | IR 只有**三种 role**（system/user/assistant），不设 `RoleTool`；系统提示统一提升为一条 system 消息 | `02-ir.md:68-85` |
| 1.15 | IR 以 **Anthropic content block 为粒度**；part 类型 5 种；`index` **由我们统一计算** | `01-overview.md:305`、`02-ir.md:87-94` |
| 1.16 | image part 内部存**原始字节**不存 base64；`Detail` 保留原样字符串不枚举 | `02-ir.md:112-129` |
| 1.17 | `tool_call.Arguments` **不做 JSON 解析**直接透传 | `02-ir.md:143-145` |
| 1.18 | thinking 独立成 part（带 `Signature`、`Redacted` 三态） | `02-ir.md:164-180` |
| 1.19 | 流事件类型固定 **9 种 + 7 条不变式 I1–I7** | `02-ir.md:255-296` |
| 1.20 | **「可见内容」定义**为除 `start`/`usage`/`error`/`done` 之外的一切（`start` 不算） | `02-ir.md:298-310` |
| 1.21 | `tool_call` id 按 index 重组；拿到 id 前**推迟发 start**；超 **8 KiB 或 2s** 放弃等待并用合成 id `call_{request_id}_{index}` | `02-ir.md:343-378` `[无理由]`（8 KiB/2s 是初值） |
| 1.22 | 能力缺失**只有两种合法处置**：过滤候选 或 400 指出哪个 part；**绝不丢弃继续跑**（`TopK` 是唯一例外） | `02-ir.md:433-474` |
| 1.23 | 可忽略字段白名单**恰好 5 条**；`store:true` **必须 400** | `02-ir.md:476-490` |
| 1.24 | usage **三态**（`nil`=上游没报 / `0`=明确是 0 / `Source=estimated`），估算与上报**不共用字段** | `02-ir.md:494-523` |
| 1.25 | IR 演进规则：**只加不改**；未知 part type → **400 不忽略** | `02-ir.md:527-535` |
| 1.26 | 上游接口用 `Open`+`Recv` 拉取模型（不用 `chan`），**接口上没有 Retry**（重试是跨候选项的决策） | `03-upstream.md:93-99` |
| 1.27 | 错误分类 **10 种 `ErrClass`**（不直接看 HTTP 码） | `03-upstream.md:101-121` |
| 1.28 | `Transport` 参数：`MaxIdleConns`**128** / `MaxIdleConnsPerHost`**64** / `MaxConnsPerHost`**128** / `IdleConnTimeout`**90s** / `TLSHandshakeTimeout`**10s** / `ResponseHeaderTimeout`**60s** / `DialContext`**5s** / `ExpectContinueTimeout`**1s** / `ForceAttemptHTTP2`**true** / `DisableCompression`**true** | `03-upstream.md:181-194` |
| 1.29 | `IdleConnTimeout` 取 **90s**（< 上游 120s 空闲 FIN） | `03-upstream.md:196-211` |
| 1.30 | 重试退避表：连接重置 **50→150ms** ≤2 次、连接失败 **200→500ms** ≤2、TLS **200ms** ≤1、超时 ≤1、5xx **100→300ms** ≤2；**429/额度/鉴权/bad_request/protocol 一律 0 次重试** | `03-upstream.md:242-256` |
| 1.31 | 重试总预算 **≤2 次/候选、≤4 次/请求** | `03-upstream.md:260` |
| 1.32 | 健康状态机 4 态；`degraded` **降权不摘除**；冷却退避 **30s→1m→5m→15m**（上限 15m） | `03-upstream.md:286-322` |
| 1.33 | 熔断粒度挂在 `upstreams.id`；反代类用 sidecar 的 `models.list` 探活、健康时 **30s** 一次 | `03-upstream.md:304-318` |
| 1.34 | `/readyz` = sidecar 可用 **且** 至少 1 条反代/上游健康 | `03-upstream.md:324-332` |
| 1.35 | 超时矩阵 8 项：入站读 body **30s** / TCP **5s**（公网贡献凭据放宽 10s）/ TLS **10s** / 首字节 **60s** / **流式事件间隔 120s** / 单请求总时长 **600s** / 空闲淘汰 **90s** / drain **30s** | `03-upstream.md:336-355` |
| 1.36 | 契约测试 **K1–K7**（含复现 `http=000` 并验证重试）；K1 **不断言模型条目数** | `03-upstream.md:400-415` |
| 1.37 | 启动顺序 7 步，**初始化向导先于开公网**；反代类自举失败**不阻塞启动**、指数退避到 30s 重试 | `01-overview.md:197-213`、`04-sidecar.md:236-257` |
| 1.38 | sidecar 启动 `health` 超时 **10s**，失败挂横幅继续 | `04-sidecar.md:243-250` |
| 1.39 | sidecar 重启退避 **1s→2s→5s→15s→30s→60s**；连续 **5 次**失败停止重启等人工；协议违规**连续 3 次**主动重启 | `04-sidecar.md:259-274` |
| 1.40 | sidecar 单次 IPC 超时：`health`/`providers.list` **3s**、`credentials.resolve`/`describe` **5s**、`models.list`/`credits.query` **15s**、`login.*` **10s**、`login.cancel` **3s**；**超时不取消** sidecar 侧操作 | `04-sidecar.md:276-288` |
| 1.41 | sidecar 关闭四级：**拒新 IPC 等 5s → 发 `shutdown` 帧等 3s → SIGTERM 等 2s → SIGKILL**；在 drain（30s）之后执行 | `04-sidecar.md:290-307`（三处已统一，见第 8.2 条） |
| 1.42 | 长期 goroutine **只有 4 类**；每请求 goroutine：非流式 **1**、流式 **2** | `06-runtime.md:29-59` |
| 1.43 | 全局并发闸门 **信号量容量 200**，队列超 **5s** 返回 503 | `06-runtime.md:68-83` `[无理由]`（200 与 5s 这两个数） |
| 1.44 | 超时四层：`connect` **5s** < `first_byte` **60s** < `attempt` **300s** < `request` **600s** | `06-runtime.md:168-185` |
| 1.45 | 候选尝试超时用**子 ctx**（不取消整个请求） | `06-runtime.md:150-151` |
| 1.46 | 取消后：客户端断开只记日志；总超时 → **504**（若还没写响应头）；已开始输出 → 只能发 SSE 错误事件 | `06-runtime.md:153-164` |
| 1.47 | SSE **每帧 Flush** 不缓冲；用 `ResponseController(w).Flush()` 而**不断言** `http.Flusher` | `06-runtime.md:193-218` |
| 1.48 | SSE 写超时 **30s**；**不做无限缓冲**，靠 TCP 背压 | `06-runtime.md:220-229` `[无理由]`（30s） |
| 1.49 | 用量在**流结束时**记录而非逐帧累加；中途断开按已收帧估算并标 `estimated` | `06-runtime.md:231-240` |
| 1.50 | 换候选白名单（传输失败 / 429 / 5xx / 模型不可用，且都要求**尚未写出可见内容**）；**绝不换**：400、**内容审核拒绝**、已开始输出 | `06-runtime.md:255-271` |
| 1.51 | 对外错误码 **10 个**；`model_not_found` **不区分**「不存在」与「无权访问」 | `06-runtime.md:273-290` |
| 1.52 | 错误体保留 **OpenAI 形状** | `06-runtime.md:292-309` |
| 1.53 | 优雅关闭 7 步（先停 listener + `/readyz` 不就绪 → drain → **强制取消根 ctx** → 关 sidecar → DB checkpoint → 退出）；`shutdown_grace` 默认 **30s** | `06-runtime.md:313-347` |
| 1.54 | 部署三形态共用一个二进制；**TLS 与反代是两件独立的事**：`tls.mode: off \| file`（默认 `off`，内置加载 cert/key），反代是可选部署形态 | `01-overview.md:228-241`（已重写，见第 8.10 条） |
| 1.55 | 数据目录 `$FR_DATA_DIR/{freerouter.db,-wal,-shm,sidecar.sock,master.key,backups/}`；主密钥支持文件与环境变量、**都不入库** | `01-overview.md:242-259` |
| 1.56 | 静态资源 SPA fallback；`Cache-Control: max-age=31536000, immutable`；`index.html` 必须 `no-cache`；静态资源**不走鉴权中间件** | `01-overview.md:336-338` |
| 1.57 | 时间列一律 `INTEGER` UTC unix 秒（仅 `usage_daily.day` 用 `TEXT "YYYY-MM-DD"`） | `05-storage.md:36-37` |
| 1.58 | 主键 `INTEGER PRIMARY KEY`；**对外不暴露自增 id**，对外标识用不可猜的 `TEXT` | `05-storage.md:38-39` |
| 1.59 | 纯联结表一律 `WITHOUT ROWID`；外键显式声明 + `PRAGMA foreign_keys=ON`；布尔用 `INTEGER 0/1` | `05-storage.md:40-42` |
| 1.60 | `sessions.token_hash` / `api_keys.key_hash` 用 **BLOB** 而非十六进制 TEXT | `05-storage.md:95-111` |
| 1.61 | 默认组用**部分唯一索引** `ON groups(is_default) WHERE is_default = 1` | `05-storage.md:117-161` |
| 1.62 | `group_policies` 用 `(group_id, policy_key)` + `policy_value TEXT JSON` 键值形状 | `05-storage.md:141-146` |
| 1.63 | 共享池**另开一表** `group_quota_pools`（主键 `(group_id, dimension, period)`）；「不设池」= **没有这一行**；**不加** `CHECK(pool_used<=pool_limit)` | `05-storage.md:149-198` |
| 1.64 | 池扣减用**单条条件 UPDATE**（判定即扣减）；重置用**惰性重置**而非定时任务 | `05-storage.md:636-699` |
| 1.65 | 替代链成环校验**放应用层**（DB 只有 `CHECK(replacement_model_id <> id)` 防自环） | `05-storage.md:217,254-256` |
| 1.66 | 额度上限列可空：`NULL`=不限，`0`=额度为零（全拒）；用量列同样可空（与 IR 三态对齐） | `05-storage.md:354,409` |
| 1.67 | 贡献统计**不建表**，用只读视图现算 | `05-storage.md:469-494` |
| 1.68 | 索引策略：时间序列索引**末列是 `ts`**；每个索引必须对应一个真实查询；不写覆盖索引、不 `ANALYZE` | `05-storage.md:499-517` |
| 1.69 | 迁移**自写**不用 goose；**只有前滚无 down**；`checksum` 校验；编号连续（缺号/重号拒绝启动）；**单事务**；启动时执行 | `05-storage.md:521-552` |
| 1.70 | PRAGMA：WAL / `synchronous=NORMAL` / `foreign_keys=ON` / `busy_timeout=5000` / `wal_autocheckpoint=1000` / `cache_size=-65536` / `temp_store=MEMORY` / `mmap_size=268435456` | `05-storage.md:561-570` |
| 1.71 | PRAGMA 绑每条连接；**分池** `writeDB` 上限 **1** / `readDB` **4**（`?mode=ro`） | `05-storage.md:572-593` |
| 1.72 | 一次模型请求 = 流结束后**一个事务**（`INSERT requests` + `usage_daily` upsert + 额度递增 + 组池扣减） | `05-storage.md:611-622` |
| 1.73 | 转发热路径**禁止 4 类**：事务里跨上游调用 / 请求开始时写 `requests` / 每条 SSE 写库 / 同步 `VACUUM` | `05-storage.md:624-631` |
| 1.74 | 备份用 `VACUUM INTO`、每日 1 次、保留 **7 份滚动** + `PRAGMA integrity_check` + 定期还原演练 | `05-storage.md:701-711` |
| 1.75 | **主密钥不随备份走**：只有库的备份恢复后所有上游凭据不可解密 | `05-storage.md:713-728` |
| 1.76 | 保留策略：`requests` **90 天** / `usage_daily` **永久** / `audit_log` **≥1 年** / `sessions` 过期即清 / `disabled` 凭据保留 | `05-storage.md:730-738` |
| 1.77 | 健康计数与目录缓存**不进 DB**，只留进程内存 | `05-storage.md:23-28` |
| 1.78 | sidecar 将来若重写为 Go，走**逐 provider 迁移**（契约不变、Go 侧零改动）；**允许混合 sidecar**（部分 provider 永久留在 Node）作为长期终态；重写成本评估已留档 | `04-sidecar.md:377-450` |

---

## 2. 账户、权限与安全（`docs/security/**`）

| # | 决策 | 位置 |
|---|---|---|
| 2.1 | 账户 schema：`id, display_name, username, password_hash, status, recovery_code_hash, created_at, last_login_at, note`；状态 **5 值** pending/active/disabled/rejected/merged；`display_name` **不承担唯一性** | `01-accounts.md:16-24,219-238` |
| 2.2 | 权限类**拆独立表** `account_classes(account_id, class)`，不放在账户上的单值字段 | `01-accounts.md:26-31` |
| 2.3 | 三条不变式：classes **非空**；**admin 只能由已有 admin 授予**（系统不自举、导入不隐式产生）；账户**无上级归属无邀请关系** | `01-accounts.md:33-37` |
| 2.4 | 合并语义：类取**并集**、Key/凭据/组关系**改挂** target、source 置 `merged`、**审计不重写历史**、留痕表 `account_merges`（含 `reversible_until`，可回滚） | `01-accounts.md:143-156` |
| 2.5 | 状态机 **8 条边**；`disabled→active` **不自动恢复提权状态**；`rejected→pending` 需显式动作；`disabled` **不删数据** | `01-accounts.md:176-191` |
| 2.6 | 类授予路径：tenant = 注册默认 + 审核确认 + 导入 + 单独授予；contributor = **贡献即得、无需审核**；撤销 tenant → 名下 Key 立即不可用 | `01-accounts.md:71-77` |
| 2.7 | 注册默认值：全局开关**默认开启但需审核**；初始 `pending`（未审核不能登录）；初始只给 tenant 类；按 IP 限速；密码 **≥12 字符** | `02-authn.md:24-29` |
| 2.8 | 密码哈希 **argon2id** + 定长常数时间比较；**具体参数（内存/迭代/并行度）标为待定** | `02-authn.md:100-102` |
| 2.9 | 防枚举 = **两条信道同时堵**（同一文案 + 同一状态码 + 等量工作，含哑哈希/哑校验），覆盖注册、密码登录、恢复码登录、「用户名是否可用」 | `02-authn.md:247-256` |
| 2.10 | 防爆破**两维计数**（账户 + IP）+ **指数退避** + 达阈值锁定；失败记录含 IP/UA/时间/目标用户名进审计；**曲线起点/倍数/上限/阈值/解除方式全部未定** | `02-authn.md:262-289` |
| 2.11 | **未配置 `TRUSTED_PROXIES` 则忽略 `X-Forwarded-For`** | `02-authn.md:274` |
| 2.12 | CSV 导入四列 `用户名,密码[,组][,日额度]`；密码留空→生成随机密码一次性展示；组空→默认组；日额度空→取组策略；**逐行结果**（整批回滚待定） | `02-authn.md:71-78,322-323` |
| 2.13 | 审计形态**两层**：一次导入 = 一条批次事件 + 每个成功账户一条创建事件 | `02-authn.md:90`、`06-audit.md:89-90` |
| 2.14 | 生成密码 / Key / 恢复码统一「**只展示一次、不落库明文**」 | `02-authn.md:88`、`05-keys.md:65` |
| 2.15 | 审计 `action` 定为**闭集合 11 类** | `06-audit.md:54-159` |
| 2.16 | 组策略键**闭集合 10 键**，预先分类：放开类 5（`visible_models`/`can_create_key`/`max_keys`/`key_allowlist_widen`/`model_class_allow`）、限制类 5（`daily_token_cap`/`monthly_token_cap`/`rpm_limit`/`tpm_limit`/`max_concurrency`）；共享池**不进**策略 | `03-groups.md:345-362` |
| 2.17 | 合并公式：可见模型 = ∪ 组模型；token/RPM/TPM/并发 = **min(各组)**；布尔 = **任一 deny 即 deny**；最终可用 = (∪组模型) ∩ Key 白名单 | `03-groups.md:96-114`、`04-authz.md:37` |
| 2.18 | 池判定排在**个人额度之后**（纯性能选择） | `03-groups.md:187` |
| 2.19 | 池参数三元组 `(dimension, limit, period)`；`period` ∈ {day, month} **必填无默认**；界面初值 day | `03-groups.md:139-153` |
| 2.20 | 池**禁止「保留配置但停用」**：删行 = 不设池是唯一表达 | `03-groups.md:203-211` |
| 2.21 | 池耗尽 = **429 + `pool_exhausted` + type `rate_limit_error`**，`Retry-After` 指向下个周期锚点；文案明说「组级限制，重试无效」；**错误体不指明是哪个组** | `03-groups.md:235-241` |
| 2.22 | Key 配额 **7 维**（总额度 / 日 token / 月 token / RPM / TPM / 并发 / 三维令牌桶 + 全局熔断）；**维度独立，任一触顶即拒** | `05-keys.md:69-87` |
| 2.23 | RPM/TPM/并发超限 → **429 + 有值 `Retry-After`**；日/月 token 耗尽 → 拒绝（是否复用 429 待定） | `05-keys.md:74-76` |
| 2.24 | Key **四态** `active/disabled/revoked/expired`：disabled 可恢复、revoked **单向不可恢复**、expired 由 `expires_at` 推导 | `05-keys.md:22-29,155-160` |
| 2.25 | Key 自身属性**可缓存**；**账户状态与权限类不进长 TTL 缓存**（若缓存必须带版本号或主动逐出） | `04-authz.md:135-138` |
| 2.26 | 会话只存 `token_hash`；`Secure` **仅在启用 TLS 时加**；ip/ua 只留痕**不强绑定** | `02-authn.md:138-145` |
| 2.27 | 会话吊销触发点**恰好 5 条**：登出 / 改密或重置（全部）/ 撤销 admin 类 / 转 disabled（全部）/ 被合并 | `02-authn.md:147-155` |
| 2.28 | 改密**吊销全部其它会话**，但**不影响 Key** | `02-authn.md:110-116` |
| 2.29 | 禁用联动**立即 401、不依赖缓存**；返回 **401 而非 403** | `04-authz.md:126-140` |
| 2.30 | Key 白名单默认**空**、**只能从组授权收窄**、元素只能是抽象模型、**被移出组后白名单不变**（靠交集自动收窄，不做级联清理） | `05-keys.md:97-110` |
| 2.31 | 授权判定结果**三值**（允许/拒绝/需提权）；主体带 `acting_class` + `elevated` | `04-authz.md:14-22` |
| 2.32 | 运行时鉴权固定 **10 步**、管理面鉴权固定 **6 步**，各自绑定失败码 | `04-authz.md:107-118,142-151` |
| 2.33 | 恢复码：只存 hash、**仅展示一次**、只给 contributor 类、允许 `password_hash=NULL`、**不用邮箱找回**；轮换/有效期/长度**全未定** | `02-authn.md:226-237` |
| 2.34 | 上游 OAuth 登录：**只有带 contributor 类的账户**能用、不要密码、会话**默认低权限**；`login_enabled` 可关闭某凭据的登录用途而仍留在池中 | `02-authn.md:196-209` |
| 2.35 | OAuth 会话**可以**提权进管理面（条件是账户带 admin 类） | `02-authn.md:186` |
| 2.36 | 审批队列三选一（批准/拒绝/搁置）；批准**必须同时选组 + 配额**；拒绝可填理由但用户提示不区分 | `02-authn.md:37-44` |
| 2.37 | 激活后第一条请求仍打不通是**有意行为**（active 只解除登录与建 Key 封锁） | `02-authn.md:48-54` |
| 2.38 | 默认组**必须始终存在**、`is_default=true` **唯一且不可删除**；**判定链不处理「无策略」**（把空洞消灭在数据层） | `03-groups.md:270-276` |
| 2.39 | 删组：成员**退回默认组**、三类行一并删除、**历史用量不追溯** | `03-groups.md:282-290` |
| 2.40 | 组资格：成员必须带 tenant 类；组**不持有** Key/凭据/端点；组策略里一律**抽象模型** | `03-groups.md:44-48` |
| 2.41 | 组可见性：组名（本人可见）、成员列表（仅管理员）、组策略（成员只看**自己生效结果**）、组用量汇总（仅管理员） | `03-groups.md:296-307` |
| 2.42 | 组生命周期 **7 个动作全部需提权 + 进审计**，其中 5 个（改授权/改策略/成员增删/默认组/删组）**同属「改组」一项，全部每次重验** | `03-groups.md:324-333` |
| 2.43 | 池余量**仅管理员**可见；**是否给成员看「本组池是否耗尽」倾向不做** | `03-groups.md:251-264` |
| 2.44 | 池日志**不额外按成员聚合**（避免成员排行） | `03-groups.md:256` |
| 2.45 | 审计 `target` 为「类型:ID」字符串、**不建外键**；账户删除后审计行**不级联删除**；时间由服务端生成（**不接受调用方传 TS**） | `06-audit.md:26-39,285` |
| 2.46 | 审计 `result` **三值闭集合** ok/failed/denied | `06-audit.md:235-241` |
| 2.47 | 禁止写入审计**四类**：Key 明文、会话令牌/密码/恢复码、上游凭据明文、请求响应正文（**排查模式也仍不写**） | `06-audit.md:45-52` |
| 2.48 | 脱敏模式：默认不记正文；排查模式**保结构抹文本**（`<redacted:N chars>`）、UI 显式提示、开关本身进审计 | `06-audit.md:163-179` |
| 2.49 | 审计 append-only、按时间/大小分段归档 | `06-audit.md:185-191` |
| 2.50 | 观测指标 **17 项** + 阈值动作 + 告警级别（必须告警只有「全局预算熔断」一条） | `06-audit.md:195-211` |
| 2.51 | 审计可见性矩阵：本人可见自己的登录/会话/改密与自己的 Key 事件；贡献者对 Key 事件**不可见**；真实模型归因链仅管理员 | `06-audit.md:219-230` |
| 2.52 | 三个数据集不合并：审计记行为、请求日志记计量、用量聚合记汇总 | `06-audit.md:13-22` |
| 2.53 | 危险操作**边界**：重置他人密码、吊销他人会话、上游目录与鉴定处置**均不在**每次重验清单内　**已定案（2026-10-20 用户拍板）**：三者都不列入，但都保留界面二次确认。原「列为待定」已关闭（Q152/Q153 结案） | `04-authz.md:86-89,229-231` |

---

## 3. 模型体系（`docs/models/**`）

| # | 决策 | 位置 |
|---|---|---|
| 3.1 | 目录状态与运行时健康**分两个字段**（normal/missing 天级 vs healthy/degraded/cooldown/blocked 秒级） | `01-catalog.md:187` |
| 3.2 | 过滤条件固定 **8 条 F1–F8** | `03-strategy.md:37-44` |
| 3.3 | `weight` 取值语义与默认值**待定**（越小优先还是越大优先） | `03-strategy.md:143` |
| 3.4 | 有两种能力模式：`strict`（min/交集，默认）与 `filter`（并集/最大，路由前筛，筛空 → **503 `no_candidate`**） | `02-abstract.md:186-201` |
| 3.5 | 重试 vs 切换**分层**：重试 = 同候选同凭据（连接层失败）、切换 = 换路径（确定不可用） | `03-strategy.md:209-215` |
| 3.6 | 切换唯一硬约束：一旦吐出**可见内容**就不能再换候选 | `03-strategy.md:172` |
| 3.7 | `Deprecation` 用 `@` + Unix 秒；`Sunset` 用 HTTP-date 且**必须带 GMT**；两头**分别格式化、绝不共用 helper** | `04-deprecation.md:101-130` |
| 3.8 | 三级鉴定流水线：L0 **几秒**（全部贡献无例外）→ L1 **约 1 分钟**（决定是否入池）→ L2 **分钟级**（抽检 + 可疑 + 定时复检） | `05-verification.md:71-85` |
| 3.9 | 信号按伪造抵抗性三级：**强**（数值随机指纹、能力边界、logprobs、金丝雀）、**中**（知识边界、计费一致性）、**弱**（usage 差值、自报身份、回显 model、响应头、错误格式）；弱信号不能单独定案 | `05-verification.md:49-60` |
| 3.10 | 请求级两道闸：Q1 个人额度 → **429 `quota_exceeded`**；Q2 组共享池 → **429 `pool_exhausted`**；在候选展开**之前**判一次 | `03-strategy.md:71-77` |
| 3.11 | 候选声明字段：`channel_scope{mode, channels[]}`、`credential_scope{mode: any/platform_only/contributor_only}`、`require_modalities[]`、`min_context`、`require_efforts[]` | `02-abstract.md:82-97` `[无理由]` |
| 3.12 | 候选保存校验 **6 条**（含 require_modalities 必须是子集、至少 1 个候选） | `02-abstract.md:125-130` |
| 3.13 | `aggregate/*` **默认排除**：刷新时置 `excluded_from_candidates=true`；引用它保存**被拒**；仍可见（22 个） | `01-catalog.md:139-145` |
| 3.14 | missing 判定：连续 **N 天**未出现（**建议 3–7 天**，未定值） | `01-catalog.md:152,158` |
| 3.15 | missing 只**提示**不自动弃用；确认后三选项：弃用 / 仅移出候选 / 忽略 | `01-catalog.md:153,174-176` |
| 3.16 | 刷新差分处置：消失时**不改 status**，交给巡检 | `01-catalog.md:115-117` |
| 3.17 | 健康路由：`degraded` **不剔除**只降权；`cooldown` 剔除、期满**半开**；`blocked` 剔除且**不自动恢复** | `01-catalog.md:198-200` |
| 3.18 | 健康**不建独立表**（从 `requests` 滚动窗口聚合）；熔断与冷却放**进程内存、重启即清**；**窗口长度/失败率阈值/连续失败次数/冷却时长全部未定** | `01-catalog.md:227-232` |
| 3.19 | 抽象模型命名：只允许小写字母数字 `-` `_`、**禁止含 `:`**、全局唯一、创建后**不可改** | `02-abstract.md:67-70` |
| 3.20 | 能力未知时按「不支持」处理、未知项**不入交集**；`capabilities` 只是摘要、**不作为路由依据** | `02-abstract.md:164-178` |
| 3.21 | 响应 `model` **永远回显抽象模型名**；用户可见日志只有抽象模型名 | `02-abstract.md:223-224,259-261` |
| 3.22 | `x-fr-trace` 头格式 `real=…; cred=…; attempt=…`，**默认关闭**，仅管理端或指定白名单组可见 | `02-abstract.md:232-233`、`api/03-errors.md:232` |
| 3.23 | `group_name`/`sort_order`/`hidden` 只作用于管理端呈现、**不影响路由** | `01-catalog.md:125-127` |
| 3.24 | F8 不通过时**降级为无替代、不报错**；过滤是**硬门槛不降权** | `03-strategy.md:44,52,58` |
| 3.25 | 过滤后为空 → **503 + `no_candidate`**，并落「若放宽某一级还剩几条路径」诊断（建议只给分类不给数量与模型名） | `03-strategy.md:62-66` |
| 3.26 | Q2 过滤阶段只是**预判**，权威判定是结算时的条件 UPDATE；预判**倾向不缓存** | `03-strategy.md:88-105` |
| 3.27 | `quality-tier` 把鉴定判定（trusted > verified > unverified）与运行时成功率**合成**档位 | `03-strategy.md:154` |
| 3.28 | 除 weighted/round-robin 外排序**必须确定性**；这两个必须带随机种子或游标快照进日志 | `03-strategy.md:160-166` |
| 3.29 | `thinking_delta` 按「**算**作可见内容」处理（保守，客户端会渲染它） | `03-strategy.md:187-199` |
| 3.30 | 非流式切换窗口 = 请求发出 → 收到完整响应之前 | `03-strategy.md:203` |
| 3.31 | 切换次数与单请求总时长**都必须有上限**（具体数值待定） | `03-strategy.md:221-225` |
| 3.32 | 策略链保存：重复项**拒绝保存**；**空链视为默认链**；变更必须**重跑干跑** | `03-strategy.md:231-236` |
| 3.33 | 干跑 **5 项**检查；**阻断保存 3 项**、**仅警告 2 项** | `03-strategy.md:248-257` |
| 3.34 | `requests` 落库字段含切换次数；是否把「切换次数/被过滤原因」作**独立字段**待定 | `03-strategy.md:273-279` |
| 3.35 | 弃用状态迁移：`active→deprecated` **必须**指定替代目标并通过能力校验；到 `sunset_at` **自动** disabled | `04-deprecation.md:30-38` |
| 3.36 | `disabled→active` 回滚时 `deprecated_at`/`sunset_at` 清空还是保留**待定**；`disabled` 一律 404 还是 410 **待定** | `04-deprecation.md:44-46` |
| 3.37 | 替代链**不压缩**（保留「第几手」）；达上限降级为「无替代」**不报错** | `04-deprecation.md:52,63-65` |
| 3.38 | 环检测**两处都要有**（配置时走一遍拒绝保存 + 运行时 visited 兜底），检出时降级 + **必须告警** | `04-deprecation.md:81-92` |
| 3.39 | `Link: rel="deprecation"` 指向**链终点**且指向人可读**文档**而非模型名 | `04-deprecation.md:137-140` |
| 3.40 | 带头范围：替代链成功的响应**带**、流式**带**、**错误响应不带**、`/v1/models` 列表**待定** | `04-deprecation.md:167-172` |
| 3.41 | 替代目标能力必须**逐项 ≥**（上下文/最大输出/工具调用/视觉），否则禁止保存；**逐跳比较还是终点 vs 原模型待定** | `04-deprecation.md:180-196` |
| 3.42 | 管理面链**必须实时预览 6 项** | `04-deprecation.md:206-211` |
| 3.43 | 运行时链解析**可缓存**但配置变更**必须立即失效**；`model` 字段回显 A 还是 C **待定** | `04-deprecation.md:219-233` |
| 3.44 | 各级信号**具体权重与阈值待定**（必须用自己的池子标定，不照抄上游项目默认值） | `05-verification.md:64,99` |
| 3.45 | `verify_detail` 必须可执行（suspect 给「哪条信号失败 + 失败方向」）；探针细节对管理端可见、对贡献者脱敏的分界**待定** | `05-verification.md:132-138` |
| 3.46 | 指纹库三来源（lmfpd / zing 的 85 模型 YAML / 自建采样）；硬约束「**参考样本绝不能取自待检的可疑渠道**」，自建采样唯一合法来源是 `owner_account_id = NULL` | `05-verification.md:153-163` |
| 3.47 | 指纹库必须有**版本号**；更新后已入池 verified **不回溯重判**，unverified 队列**按新库重排** | `05-verification.md:169-174` |
| 3.48 | 复检三触发（定时轮转 / 可疑 / **回归告警**）；三处置（保持或升级 / 告警 + 提高频率 + **降权** / 判 suspect 并**移出候选但不删除**） | `05-verification.md:190-200` |
| 3.49 | 鉴定成本四项与承担方**全部待定**（三候选：平台全担 / 贡献者全担 / 混合） | `05-verification.md:210-221` |
| 3.50 | `context_window` 三个候选来源作者只列选项**未拍板** | `01-catalog.md:82,266` |

---

## 4. 对外契约（`docs/api/**`）

| # | 决策 | 位置 |
|---|---|---|
| 4.1 | 契约原则：**`code` 是契约、`message` 不是**；三面共用同一套码与错误体 | `03-errors.md:5-21` |
| 4.2 | 默认错误体为 OpenAI 兼容形状；Anthropic 形状**仅** `/v1/messages` 使用 | `03-errors.md:28-51` |
| 4.3 | `type` 枚举固定 **8 值** | `03-errors.md:68-77` |
| 4.4 | `model_not_found` → **404**，单码覆盖三种情况（不存在 / 请求真实模型名 / 对当前 Key 不可见） | `03-errors.md:89,100` |
| 4.5 | `model_retired` → **410**，独立于 `model_not_found` | `03-errors.md:90,102` |
| 4.6 | **新造** `upstream_error` → 502、`upstream_timeout` → 504 | `03-errors.md:95-96,153` |
| 4.7 | `quota_exceeded` 与 `rate_limited` **都用 429**，靠 `code` 与 `Retry-After` 区分 | `03-errors.md:91-93` |
| 4.8 | `invalid_api_key` **一码覆盖 5 种情况**（缺头/不存在/吊销/过期/禁用/账户被禁用） | `03-errors.md:85` |
| 4.9 | **405 与 415 都复用 `invalid_request`** | `03-errors.md:146-157` |
| 4.10 | 明确**不设 402 / 418 / 451** | `03-errors.md:159-161` |
| 4.11 | `elevation_required` → **403**，与 `forbidden`（403）并存 | `03-errors.md:114-122` |
| 4.12 | 控制台其余码：`invalid_credentials` 401（**不复用** `invalid_api_key`）/ `session_expired` 401 / `conflict` 409 / `too_many_attempts` 429 | `03-errors.md:112-120` |
| 4.13 | 三条加码裁决原则：动作不同→拆码；泄露面不同→合并；仅文案不同→不新增 | `03-errors.md:130-135` |
| 4.14 | 请求 ID 头名 **`X-Free-Router-Request-Id`**，**每个响应都有**（含错误与流式） | `03-errors.md:169` |
| 4.15 | 明确**不发 `WWW-Authenticate`** | `03-errors.md:180` |
| 4.16 | 安全头 **8 项**固定清单 | `03-errors.md:170-176` |
| 4.17 | `Retry-After` 逐情况定（`quota_exceeded` 日/月上限有值、全局熔断省略、`no_candidate` 可选） | `03-errors.md:190-199` |
| 4.18 | 自定 **6 个 `x-ratelimit-*`** 头，成功响应上也发；**不设**额度剩余量响应头 | `03-errors.md:243-253` |
| 4.19 | `/v1/messages` 同时接受 `x-api-key` 与 `Authorization: Bearer`，并接受回显 `anthropic-version` | `01-inbound.md:35-41` |
| 4.20 | `GET /v1/models` **也要求鉴权**（未鉴权 401，不提供公开列表） | `01-inbound.md:37,224-226` |
| 4.21 | `owned_by` 固定为 `"free-router"` | `01-inbound.md:212-218` |
| 4.22 | 已弃用模型**仍列在** `/v1/models` 里，只靠响应头带弃用信号 | `01-inbound.md:232-241` |
| 4.23 | 鉴权失败**发生在限速与配额之前**，失败请求不进限速/配额计数 | `01-inbound.md:47` |
| 4.24 | 多模态**只支持** `content[].type=image_url` + `data:image/png;base64,…` | `01-inbound.md:101-119` |
| 4.25 | `reasoning_effort` 候选缺档位时**入站不拦截**，交给 L2 | `01-inbound.md:117` |
| 4.26 | Anthropic 侧 `max_tokens` 必填且**不补默认值** | `01-inbound.md:278` |
| 4.27 | 流响应头固定 `200` + `text/event-stream` + `no-cache` + `keep-alive` | `01-inbound.md:146-149` |
| 4.28 | `usage` 帧的 `choices` 为**空数组**且放在 `[DONE]` 之前 | `01-inbound.md:166-180` |
| 4.29 | 流内错误帧体 `{"error":{…},"request_id":"req_…"}`（同级）；**错误帧之后仍要发 `[DONE]`**（Anthropic 路径为 `message_stop`） | `03-errors.md:260-272` |
| 4.30 | Anthropic 路径**不发 `[DONE]`**，结束判据是 `message_stop`；每个 content block 成对 start/stop | `01-inbound.md:364-369` |
| 4.31 | MVP **不发** Anthropic 具名 `ping` 事件 | `01-inbound.md:367` |
| 4.32 | 管理 API **不签发任何 API Token**，认证只走会话 Cookie；Cookie 名 **`fr_session`**、`SameSite=Lax` | `02-admin.md:11,16,24-29` |
| 4.33 | 两层检查：无 admin 类 → **404**；提权过期 → **403 + `elevation_required`** | `02-admin.md:33-38` |
| 4.34 | 提权头名 **`X-FR-StepUp: <短时凭据>`**（凭据形态待定，但已定**一次性、单操作**） | `02-admin.md:42-50` |
| 4.35 | **连只读查询也需要 admin 类 + 提权** | `02-admin.md:57-62` |
| 4.36 | 危险写分**两层**：逐次重验**恰好四项**（⚠️），另有一批仅二次确认的危险写（🔒）；**不提供批量危险操作端点**　**2026-10-20 修正**：原写「六类危险写清单定死」，把「需二次确认的危险写」和「需逐次重验的危险操作」混成了一层，导致 ⚠️ 被贴到 12 个端点上。现已拆成两层，四项权威定义在 `security/02-authn.md` 第 6 节 | `02-admin.md:52-76` |
| 4.37 | 基础路径 `/api/admin`；初始化端点 `/api/setup` **不在** `/api/admin` 下 | `02-admin.md:76,114` |
| 4.38 | 分页统一 `?limit=&cursor=`，响应 `{"data":[…],"next_cursor":null}`（**默认条数未定**） | `02-admin.md:81` |
| 4.39 | 写响应统一带 `audit_id` 与 `elevated:true` | `02-admin.md:92-97` |
| 4.40 | 资源分**九组** | `02-admin.md:105-113` |
| 4.41 | **没有** `DELETE /api/admin/accounts/{id}`（物理删除会让历史引用悬空） | `02-admin.md:124-137` |
| 4.42 | 账户详情只返回 `key_count` / `credential_count`，**不返回列表** | `02-admin.md:157-160` |
| 4.43 | 组策略/模型授权用 **PUT 整体替换**（不用 PATCH 增删） | `02-admin.md:193-200` |
| 4.44 | 组策略 8 键示例值 | `02-admin.md:210-214` |
| 4.45 | `qualified_name` **不可写**；`aggregate/*` 默认排除落在数据层 | `02-admin.md:225-259` |
| 4.46 | 抽象模型名创建后不可改；`capability_mode` 二值；干跑是保存路径的**强制步骤**且要求新鲜结果 | `02-admin.md:265-333` |
| 4.47 | 弃用保存三条硬校验（环检测 / 深度 ≤5 / 能力逐项 ≥）；`migration_doc` 字段名与四项能力清单 | `02-admin.md:340-372` |
| 4.48 | 上游/凭据端点：`ban` 与禁用上游**需二次确认但不逐次重验**；**无任何端点返回凭据明文**；`kind=relay`（反代）**根本没有 `base_url` 可填**——端点由 adapter 内部决定　**2026-10-20 修正**：原写「ban 每次重验」，与「恰好四项」冲突 | `02-admin.md:378-405` |
| 4.49 | 审计查询参数固定 **7 个**，**无 DELETE** | `02-admin.md:435` |
| 4.50 | 需**二次确认**的设置键 **4 个**（不含已撤销的 `admin_ip_allowlist`），设置项**不逐次重验**；额外提供 `POST /api/admin/settings/ip-check`；脱敏模式是**设置**而非会话级参数　**2026-10-20 修正**：原写「需逐次重验的设置键 5 个」——5 个里含已明确不提供的 `admin_ip_allowlist`，且设置项本就不属四项 | `02-admin.md:444-460` |

---

## 5. 界面与路由（`docs/ui/**`）

| # | 决策 | 位置 |
|---|---|---|
| 5.1 | 单域单登入点，登录后按权限类分流；不做多站点/子域 | `01-ia.md:12` |
| 5.2 | 管理端路由 **19 条**名称（含 `/admin/abstract-models/:id/deprecation`） | `01-ia.md:142-164` |
| 5.3 | `/admin` 默认落地页固定为**审核队列**（不可配） | `01-ia.md:138` |
| 5.4 | 租户一级前缀选 **`/usage`**（否决 `/tenant` 与 `/keys`） | `01-ia.md:173-180` |
| 5.5 | 贡献者路由 5 条；**动作与名词拆两个路由**（`/contribute` 未登录可用 / `/contributions` 需登录） | `01-ia.md:186-192` |
| 5.6 | 账户自助 4 条；`/logout` 用 **POST** | `01-ia.md:127-130` |
| 5.7 | **改密即吊销全部会话（含当前）并强制重新登录** | `01-ia.md:128` |
| 5.8 | 错误页固定 4 个路由 `/error/403、404、410、500` | `01-ia.md:121` |
| 5.9 | `/setup` 完成向导后**永久返回 404** | `03-admin.md:71` |
| 5.10 | 已登录访问 `/` **不重定向**，内容区替换为「我的概览」 | `02-public.md:37-39` |
| 5.11 | 未登录首页 = 落地页 + 三动作，不做营销长页 | `02-public.md:18-25` |
| 5.12 | 贡献物做成**两个并列卡片**而不是多步向导 | `02-public.md:159` |
| 5.13 | 组**不产生任何新页面**，只在三处出现 | `01-ia.md:91-97` |
| 5.14 | 管理端**不设独立「全局仪表盘」** | `01-ia.md:268` |
| 5.15 | 无权限类账户只有静态提示，**不设**「申请权限类」入口 | `01-ia.md:270` |
| 5.16 | 账户自助**不提供**改显示名入口 | `01-ia.md:134,266` |
| 5.17 | 管理端账户详情**不提供「删除账户」**；凭据池**不提供「删除」**；审计页**不提供「清空」**（加二次确认也不行） | `03-admin.md:146,406,465` |
| 5.18 | 组详情**不放「组用量明细」表**，只显示汇总数字 | `03-admin.md:221` |
| 5.19 | 真实模型「一键弃用」**跳转**到抽象模型弃用页而非就地完成 | `03-admin.md:276` |
| 5.20 | `aggregate/*` 排除是**默认且不可选**，并带说明 | `03-admin.md:274-278` |
| 5.21 | 真实模型限定名只允许出现在 **5 类管理端页面**；租户可见处一律不得出现（含上游错误回显里的 `model` 字段须剥离） | `03-admin.md:18-26`、`04-tenant.md:6-16` |
| 5.22 | 公共区**没有任何模型列表**，只提「抽象模型」这一层 | `02-public.md:12` |
| 5.23 | 导航渲染输入**只有** `classes` 与 `status`（不设「角色」概念）；5 条按序求值规则 | `01-ia.md:40-59` |
| 5.24 | admin 入口未提权时**可见但锁定**，不隐藏 | `01-ia.md:61` |
| 5.25 | 管理端账户列表**不给**「查看该账户 Key」链接；租户端**不给**「查看我的权限类来源组」 | `01-ia.md:83-87` |
| 5.26 | 租户端用量只显示**生效额度**（各组最小值），不显示「因为你在 X 组所以是 N」 | `01-ia.md:97-99` |
| 5.27 | 组名只在「可用模型/配额」旁以只读文本展示，**不设**独立「我的组」页面 | `01-ia.md:101` |
| 5.28 | 注册对**已登录账户不可见**；贡献对 pending 与无类账户**仍可见** | `01-ia.md:213-217` |
| 5.29 | 三层守卫各自独立（渲染层/路由层/数据层）；**数据层查询强制带 `account_id`** | `01-ia.md:237-241` |
| 5.30 | 全站**禁用词表**逐条列出（含 `bashAllowlist`、`chatIdLock`、`proxyUrl`、各 IM 平台名） | `06-design.md:156-164` |
| 5.31 | 管理端**不给物理删除**，一律状态迁移 | `03-admin.md:10-12` |
| 5.32 | 用户可见文案**不用内部术语**（权限类/classes/acting_class）；租户端**不出现**渠道/供应商/上游概念 | `02-public.md:13`、`04-tenant.md:18` |
| 5.33 | 用户可见状态词用**中文**而非英文枚举 | `06-design.md:172-176` |
| 5.34 | 账户五态与各态登录结果文案定死（pending 可建会话但只能到 `/pending`） | `01-ia.md:43`、`02-public.md:82-88` |
| 5.35 | 合并 5 步流程 + 回滚入口放在**合并后立即出现的横幅**里 | `03-admin.md:156-164` |
| 5.36 | 共享池周期**单选且必填，不给默认**；计量方式**同组允许同时配两条**（请求数 + token） | `03-admin.md:191-198` |
| 5.37 | 池展示「周期锚点 + 下个锚点」，**不展示倒计时秒数** | `03-admin.md:199` |
| 5.38 | 三种额度组合用**独立输入**（否决模式选择器）；**留空即「不设」**（不用填 0） | `03-admin.md:201-209` |
| 5.39 | 个人上限与共享池是**交集**，不做自动收缩，两个数字原样呈现 | `03-admin.md:212-213` |
| 5.40 | 成员看不到池，**不提供**「给成员看」开关 | `03-admin.md:216-218` |
| 5.41 | 深度 > 2 跳告警必须是页面内**持续提示**而非一次性 Toast | `03-admin.md:357-365` |
| 5.42 | 鉴定三阶段界面**分开显示**；`unverified` 必须有独立标记，**不得与 `suspect` 同种颜色**；判定配色 verified 绿 / trusted 蓝 / **unverified 灰** / suspect 黄 | `05-contributor.md:80-89`、`03-admin.md:448` |
| 5.43 | 凭据状态三态「启用/暂停/封禁」，暂停与封禁是两个动作 | `03-admin.md:401-418` |
| 5.44 | 统一二次确认框形态（原型固定：影响 / 影响对象 / 重输凭据 / 具体动作按钮文案） | `03-admin.md:33-43`、`06-design.md:129-132` |
| 5.45 | 危险操作**判据 5 条**（= 界面二次确认的判据，**不等于**逐次重验清单） | `06-design.md:115-136,138-156` |
| 5.46 | 确认框 **AlertDialog 不可点击遮罩关闭** + 输入名称二次确认 | `06-design.md:236` |
| 5.47 | 提权参数：TOTP 或重输密码、15 分钟、空闲即失效、**成功回原目标**、危险操作不复用；提权页必须显示「这次提权能做什么」 | `02-public.md:217-225` |
| 5.48 | `/setup` **不做二次确认**；字段为显示名 + 密码 ≥12（无默认值）+ TOTP 绑定（建议非阻塞） | `03-admin.md:56-78` |
| 5.49 | 审核队列列与默认排序；**拒绝需要二次确认**、批准不需要；拒绝理由可被申请人看到 | `03-admin.md:88-101` |
| 5.50 | 各页操作 × 二次确认矩阵（账户 7 项、组 7 项、真实模型 4 项、抽象模型 3 项、候选 1 项、弃用 3 项、上游 1 项、鉴定 4 项、全局设置**恰好 4 项**、Key 3 项、恢复码 1 项）；矩阵里标 `⚠️` 的**只有**落在四项内的那些，其余标 `🔒`　**2026-10-20 对齐**：二次确认项与逐次重验项已用两套标记区分 | `03-admin.md:139-492`、`04-tenant.md:64-74` |
| 5.51 | **一次性展示统一形态 4 条**：整页替换、提供复制与下载、必须勾选「我已保存」才能离开、**不提供任何形式的「再次显示」**（管理员也不行） | `06-design.md:81-86`、`04-tenant.md:85-107`、`05-contributor.md:145-149` |
| 5.52 | 登录两种方式**同屏并列**不做 tab；OAuth 只列「你贡献过的供应商」，**不列全部 17 家** | `02-public.md:50-54` |
| 5.53 | 防枚举执行细节（完全相同文案 + 相近响应时间 + 退避倒计时） | `02-public.md:71-76` |
| 5.54 | 登录成功**不提示**「上次登录地点」 | `02-public.md:74` |
| 5.55 | 注册字段**只有 4 个**，**不收集邮箱**；显示名与用户名合并为一个输入框 | `02-public.md:96-105` |
| 5.56 | 注册三态；**显示名已存在与成功响应表现一致**（依然跳 `/pending`） | `02-public.md:111-113` |
| 5.57 | 待审核页**手动刷新，不做轮询自动跳转** | `02-public.md:126-143` |
| 5.58 | 实时流用 **SSE + 增量追加**（不用 WebSocket）；暗色主题与 i18n 列 P2 待定 | `06-design.md:238-245` |
| 5.59 | 三段式骨架与内容区固定 4 段顺序 | `06-design.md:17-49` |
| 5.60 | 四种页面状态**每页都必须定义**（缺一种就是缺陷）；加载态**不用全屏遮罩** | `06-design.md:90-97` |
| 5.61 | 错误态三级（字段级/页面级/全局级） | `06-design.md:109-111` |
| 5.62 | 页面级错误体格式定死：机器码 + 人话 + 请求 ID + 重试建议 | `06-design.md:96-103`、`02-public.md:242-247` |
| 5.63 | 响应式三断点（桌面 ≥1024 / 平板 768–1023 / 手机 <768，表格转卡片） | `06-design.md:184-186` |
| 5.64 | **管理端也必须支持手机** | `06-design.md:188-192` |
| 5.65 | 无障碍 **6 项**硬要求（对比度 ≥4.5:1、状态不只靠颜色等） | `06-design.md:198-203` |
| 5.66 | 交付前 **10 项**一致性检查清单（含「没有任何金额字段」） | `06-design.md:211-222` |
| 5.67 | 运行态横幅**恰好 3 条**（上游离线 / 迁移链过长 / 鉴定回归） | `02-public.md:256-258` |
| 5.68 | 前端实现级约定：shadcn `Skeleton`（列表 ≥3 行、详情 2–4 块）、React Router 嵌套路由、TanStack Query `staleTime` **30s**、react-hook-form + zod、表格状态**由 URL 驱动** | `06-design.md:232-237` |
| 5.69 | 未加**图形验证码**（待定）；贡献页未登录时**不要求**先同意「仅限本人账号」说明（待定） | `02-public.md:276-278` |
| 5.70 | `contributor` 能力边界逐条列死 | `01-flow.md:140-143` |

---

## 6. 贡献与凭据（`docs/contributions/**`）

| # | 决策 | 位置 |
|---|---|---|
| 6.1 | 已登录贡献者**给现有账户加 contributor 类**，不另建「贡献专用账户」；匿名贡献**当场建账户**（仅 contributor 类）、不需审批 | `01-flow.md:35-48` |
| 6.2 | OAuth 登录**只对 contributor 开放**（admin/tenant 均不可用）；只有 `login_enabled=true` 的凭据能登录 | `01-flow.md:79-89` |
| 6.3 | 入池次序：提交 → 落库（`status=pending`，**尚不可调用**）→ L0 → L1 → 写回 → 进候选池；**L0 是唯一切口闸门** | `01-flow.md:97-120` |
| 6.4 | 时间预算允许**同步完成**并当场展示判定/证据/池中状态（避免引入通知系统与邮箱） | `01-flow.md:129-132` |
| 6.5 | 撞身份三处置：同账户重复提交 → **幂等接受**；不同账户提交同身份 → 合并或拒绝；纯 Key 型不参与该约束 | `02-credentials.md:115-117` |
| 6.6 | **不得静默覆盖** | `02-credentials.md:99,119` |
| 6.7 | 恢复码：OAuth 型**不发**；纯 Key 型发一次性恢复码，只存 hash、只展示一次 | `01-flow.md:61-67` |
| 6.8 | 匿名贡献**中途关页面**：凭据与账户照常落库；恢复码未展示只能靠再次贡献同身份找回（纯 Key 型会丢），UI 必须明确警告 | `01-flow.md:158` |
| 6.9 | 撤销用 `status=revoked` **不删行**；三来源（本人/管理员需记理由/上游）；**贡献者主动撤销立即生效、不被挽留**；上游来源**不可逆** | `02-credentials.md:161-171,281` |
| 6.10 | 加密：AES-GCM（`upstream_secrets.ciphertext` + `nonce`）；主密钥来自环境变量或 `0600` 文件、**永不入库**；比较一律 `ConstantTimeCompare` **没有例外**；凭据明文**永不回显**（连本人也不行）；解密明文只在一次调用期间存活于内存 | `02-credentials.md:182-212` |
| 6.11 | `provider`/`external_id` **不拆**到独立「外部身份」表 | `02-credentials.md:30-36` |
| 6.12 | `external_label` 是自由文本，但 UI 与入库校验都要**拦明显密钥形态** | `02-credentials.md:95` |
| 6.13 | 审计事件固定 **5 类**（含**解密失败/密文被篡改**） | `02-credentials.md:301-305` |
| 6.14 | 对账用 `x-free-router-request-id` + 本地 `requests` **双向对账** | `02-credentials.md:307` |
| 6.15 | 贡献统计口径定死（5 项）；**被调用次数不按抽象模型拆分**；**不建汇总表** | `02-credentials.md:241-265` |
| 6.16 | 凭据状态机四值 `pending/active/degraded/revoked`；`degraded` 仍可进候选但**排序靠后** | `02-credentials.md:154-157` |
| 6.17 | `status` 与 `verify_status` 是**两个正交维度、不能混用** | `01-flow.md:125`、`02-credentials.md:143` |

---

## 7. 部署与运维（`docs/ops/**`）

| # | 决策 | 位置 |
|---|---|---|
| 7.1 | 部署形态重切为 **A 本地直连（默认）/ B Docker Compose / C 反代前置（可选）** | `02-deploy.md:40-49` |
| 7.2 | 容器形态**只给 Docker Compose**，不给 systemd unit 示例 | `02-deploy.md:290-329` `[无理由]` |
| 7.3 | 容器强制 `network_mode: host` | `02-deploy.md:64-74,302` |
| 7.4 | 「两台机器不支持」作为结论 | `02-deploy.md:72` |
| 7.5 | 默认监听 `127.0.0.1:8080`（**端口为何是 8080 无理由**） | `02-deploy.md:89` |
| 7.6 | `tls.mode` 默认 `off`；`server.insecure_public_listen` 默认 `warn` 并保留 `deny` | `02-deploy.md:91,106-132` |
| 7.7 | ~~内置 ACME（`tls.acme_email`/`acme_cache_dir`）~~ **已删除**：暂无域名，不引入 `autocert`；TLS 只支持自备 cert/key | `02-deploy.md:109-111`（见第 8.10 条） |
| 7.8 | 反代示例**只给 Caddy 片段**，Nginx/Traefik 仅在表里点名 | `02-deploy.md:44,340-374` `[无理由]` |
| 7.9 | 「要备份的三样」= 数据库 + 主密钥 + 配置 | `02-deploy.md:388-392` `[无理由]` |
| 7.10 | 升级为**停服式五步**；迁移只前进不回退，回退靠手工恢复备份 | `02-deploy.md:414-436` `[无理由]` |
| 7.11 | §8 上线检查固定 **12 项**、按顺序执行、「任一项不通过就不上线」 | `01-security.md:238-252` `[无理由]` |
| 7.12 | 管理面**不提供** IP 白名单功能（不只是默认关） | `01-security.md:129-147`、`02-deploy.md:152-155` |
| 7.13 | 配置环境变量前缀 **`FR_`** + 层级用 `_` 分隔；配置路径 `/etc/freerouter/config.yaml` + 7 个键族 | `02-deploy.md:81-85` `[无理由]` |
| 7.14 | 威胁模型定为 **T1–T4** 四类；设计原则四条；「不在防御范围」三条 | `01-security.md:18-48` |
| 7.15 | 管理面**不单开端口**，走同一端口不同路径 | `01-security.md:66-67` `[无理由]` |
| 7.16 | 安全头四项（CSP / nosniff / Referrer-Policy / X-Frame-Options） | `01-security.md:69-71` `[无理由]` |
| 7.17 | CORS 默认关闭、禁 `*` 与凭据同现 | `01-security.md:72-73` |
| 7.18 | 服务器 banner 不泄露版本号 | `01-security.md:75` |
| 7.19 | 会话 `session_ttl = 12h` | `02-deploy.md:144` `[无理由]` |
| 7.20 | 登录 `max_failures = 5` / `lockout = 15m` | `02-deploy.md:146-147` `[无理由]` |
| 7.21 | 注册两个独立开关 `registration_enabled` 与 `registration_requires_approval` | `02-deploy.md:148-149` |
| 7.22 | 默认值：`rpm = 60` / `tpm = 0` / `max_concurrency = 2` / `keys_per_account = 10` / `daily_tokens = 0` / `monthly_tokens = 0`；**`0` 有两种语义**：`default_daily/monthly_tokens = 0` 表示「**不可用**」（需显式授权），`default_tpm = 0` / `global_daily_tokens = 0` / `global_rpm = 0` 表示「**不限制**」 | `02-deploy.md:161-166`（两种语义已在同节写明，是刻意的，不是笔误） |
| 7.23 | `idle_timeout = 120s`；`read_header_timeout = 10s`；`shutdown_grace = 30s` | `02-deploy.md:93-94` |
| 7.24 | 上游默认：`timeout = 300s` / `connect_timeout = 5s` / `max_retries = 2` / `model_refresh_interval = 10m` / `aggregate_excluded = true` | `02-deploy.md:181-185` |
| 7.25 | sidecar 五项默认（`enabled`/`node_path`/`entry`/`socket`/`startup_timeout = 20s`） | `02-deploy.md:195-200` |
| 7.26 | `store.path = /var/lib/freerouter/freerouter.db`、`busy_timeout = 5s` | `02-deploy.md:209-212` |
| 7.27 | 主密钥生成方式 `head -c 32 /dev/urandom \| base64` | `02-deploy.md:255-257` `[无理由]` |
| 7.28 | 资源估算三档：**256 MiB/1 核**（≤5 并发）、**512 MiB/2 核**（≤50）、**1 GiB/4 核**（≤200） | `02-deploy.md:460-464` `[无理由]`（数字来源） |
| 7.29 | 磁盘口径按「**30 天**日志」 | `02-deploy.md:459` |
| 7.30 | 故障排查表固定 **8 行** | `02-deploy.md:445-454` |
| 7.31 | 容器加固三件套（非 root + `--read-only` + `--cap-drop=ALL`） | `01-security.md:226-227` |
| 7.32 | CI 跑 `govulncheck`「无高危才允许发布」 | `01-security.md:230-231` `[无理由]`（判定口径） |
| 7.33 | 审计保留 **180 天** / 请求日志保留 **30 天** | `02-deploy.md:213-214` `[无理由]` |
| 7.34 | 「是否内置 Prometheus `/metrics`」标为待定 | `02-deploy.md:470-471` |
| 7.35 | sidecar 传输与主进程一致：**stdio + NDJSON**，不走 Unix socket 也不走 TCP | `02-deploy.md:37-39`（已全库统一，见第 8.1 条） |

---

## 8. 文档内部矛盾的处置（11 条，已于 2026-10-20 全部就地修完）

审阅时发现的 11 条跨册矛盾**不需要你决定**：它们是同一件事在不同分册里被写成两个答案。
Lead 已全部统一到单一口径，下表保留原始记录供你抽查。

| # | 原矛盾 | 统一后的口径 |
|---|---|---|
| 8.1 | **硬冲突**：IPC 到底是 Unix socket 还是 stdio + NDJSON | **stdio + NDJSON**（一行一帧）。理由：进程边界与通道边界重合，sidecar 一死管道立刻 EOF；Unix socket 会留下无人应答的孤儿监听。已改 `01-overview.md` / `02-deploy.md` / `03-milestones.md` / `PLAN.md` / `RESEARCH-UPSTREAM-REUSE.md` |
| 8.2 | sidecar 关闭超时三处口径不一 | 统一为四级：**拒新 IPC 等 5s → 发 `shutdown` 帧等 3s → SIGTERM 等 2s → SIGKILL**（合计 10s），整体在 `shutdown_grace`（30s）之后串行执行 |
| 8.3 | `pool_exhausted` 是否已登记 | **已登记齐**（`api/03-errors.md` 错误码表 / 状态码映射 / `Retry-After` / 重试分类四处 + `06-runtime.md` 错误码表）。原报「缺口」不成立 |
| 8.4 | 组级并发上限归属，两处无交叉引用 | **运行时的闸门**（`06-runtime.md` 2.3），组策略提供数值；已在 `security/03-groups.md` 加交叉引用 |
| 8.5 | 全局熔断默认值冲突 | 统一为：**`global_daily_tokens` 默认 `0` = 不熔断**，但上线检查第 9 项要求**部署者显式填值**；配置项与检查表已互相引用 |
| 8.6 | 工日总数不一致（19.0 / 14 天 vs 「约 15 天」） | **统一为 19.0 工日 / 关键路径 14 天**；`PLAN.md` 第 12 章已改 |
| 8.7 | 里程碑 M0–M10 两套切分 | **合并为一套**：两处均为 M0–M10 同名同内容，工日与依赖图一致；`PLAN.md` 第 12 章已加「以运维分册为权威」的声明 |
| 8.8 | Anthropic Messages 入站缺失 | `ops/03-milestones.md` 的 **M5 已补 `/v1/messages` 任务与验收** |
| 8.9 | 流内错误帧 `type` 不一致 | 统一为 **`service_unavailable_error` + `"param": null`**；`api/01-inbound.md:197` 已改 |
| 8.10 | TLS 口径冲突 | 统一为：**`tls.mode: off \| file`（默认 `off`）**，内置 cert/key 加载；**反代是可选部署形态，不是 `tls.mode` 的取值**；不引入 ACME。`01-overview.md` 7.1 节已重写 |
| 8.11 | 无 IPv6 口径 | **已补**：监听支持双栈；`trusted_proxies` 同时收 IPv4/IPv6 网段；限速键的 IP 归一规则见 `security/02-authn.md` 10.1（IPv4-mapped 归一为 IPv4） |

---

## 9. 文档已自标「待定」的项（不是决策，但等你拍板）

截至 2026-10-20 全库共 **196 处** `> **待定**` 块，分布：security 54 / models 47 / architecture 37 / ui 25 / contributions 18 / api 13 / ops 2。它们**不是**「已写定的决策」，而是「写入者已识别但不敢拍板」的部分，不影响 M0–M3 开工；其中影响最大的 10 组：

- `docs/architecture/02-ir.md:312,553` — `thinking_delta` 是否算「可见内容」；tool_call 缓冲 **8 KiB/2s 是否合适**
- `docs/architecture/06-runtime.md:345` — `shutdown_grace` **30s 是否需要 > `request_timeout` 600s**（否则长回答会被截断）
- `docs/architecture/05-storage.md:740,748,760-775` — 审计 append-only 与保留期清理的冲突；`usage_daily` 用 UTC 日界（用户看到的「今天」从早 8 点开始）；池与 `usage_daily` 是否对账
- `docs/security/02-authn.md:157,192` — 会话绝对有效期与空闲超时；argon2id 具体参数
- `docs/models/03-strategy.md:143,221,225,315` — `weight` 方向；切换次数与总时长上限；Q1/Q2 同时不满足时报哪个
- `docs/models/05-verification.md:64,99,210-221` — 鉴定阈值；L2 抽检比例与轮转周期；**鉴定成本由谁承担**
- `docs/models/04-deprecation.md:44,46,196` — `disabled` 回滚时时间戳是否保留；404 还是 410；能力校验逐跳还是终点对比
- `docs/api/02-admin.md:81` — 管理 API 分页**默认条数未定**
- `docs/api/03-errors.md:302-310` — `x-ratelimit-*` 字段名集合；`Link` 路径可否配置；全局熔断是否独立码
- `docs/ui/06-design.md:241,243,245` — 手机断点下危险操作是否额外限制；暗色主题；i18n

---

## 10. 复核对齐用：你明确说过、因此**不在**上表里的

参考 Tingly Box 做 AI 中转站并结合 dsh-providers-hub · 不要其「智能应用/团队/远程」· 加账号功能（注册需审核或管理员导入账密）· 账户模型选 A · 贡献者无偿 · 租户/贡献者/管理员三类账户且**只有租户能调模型** · 租户也不能持有上游凭据 · 账户 = 显示名 + id + 权限类且可多类 · 租户可建多个 key 且管理员控制租户可访问模型 · 多租户可分组统一控制 · **组级额度都做**（每人 + 整体）· 任何人可贡献供应商账号 · 供应商与普通 API 等价要做抽象 · 贡献 key 需校验模型名匹配且**冷门默认放行** · 未登录贡献即建贡献者账户、可用贡献账号登录、可与租户账户合并 · 管理员可建「抽象模型」按策略路由且**只对外暴露抽象模型** · 模型可设「弃用」并路由到可用模型且**可链式** · **不预置**抽象模型名 · **先本地跑** · **不用反代、默认 IP+端口** · **管理面不限 IP** · 技术路线**单二进制 + Vite/React** · 后端 Go · 方案细化多文档 · 项目名 FreeRouter、仓库公开 · 其余待确认项直接向你提问。
