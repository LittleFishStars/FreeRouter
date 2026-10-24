# 前后端契约漂移审计（2026-10-24）

> 分册：运维 · 相关：[api/02-admin.md](../api/02-admin.md)、[api/04-tenant.md](../api/04-tenant.md)、
> [05-static-analysis.md](05-static-analysis.md)

**为什么要做这份审计**：`staticcheck` / `tsc` / 单元测试都只看每一侧自己是否自洽——
前端声明 `postWrite<{ok}>` 类型能通过，后端返回裸 `{ok}` 也能通过，**两者之间的缝没人检查**。
这类缝的表现是「按钮点得动、状态不变」，比编译错误难发现得多。

**方法**：把后端注册点（`server.go` 的探针与 `/v1`、`mw_admin.go` 的 `/api/admin`、
`console_routes.go` 的 `/api` 与 `/oauth/callback`）与前端封装（`endpoints.ts` / `client.ts`）
逐条对表，再抽查 18 组 DTO 的 JSON tag。**这是静态比对**：标注为「已确认」的条目都回到
代码里核过一遍，但除已修项外没有运行时验证。

---

## 1. 已修（两轮，2026-10-24）

### 1.1 第一轮（`be52ea7`）

| 问题 | 位置 | 处置 |
|---|---|---|
| 复检开关的请求体键名写成 `enabled`，后端读 `json:"recheck"` —— JSON 静默忽略未知字段，按钮每次都写 `false` | `web/src/api/endpoints.ts`、`internal/httpapi/admin_ops.go` | 前端改发 `{"recheck": bool}`；请求体形状补进 [api/02-admin.md](../api/02-admin.md) §10 |
| 改密 / 吊销会话 / OAuth 取消三个端点返回裸 `{ok:...}`，前端却用 `postWrite` 声明 `{data,audit_id}`，`data` 恒 `undefined`（类型说谎） | `web/src/api/client.ts`、`endpoints.ts` | 新增 `postPlain`，三处改用它；响应形状补进 [api/04-tenant.md](../api/04-tenant.md) §2.2 |
| `App.tsx` 页脚硬编码 `/healthz`、`/readyz`、`/api/version`，违反 `client.ts` 自定的「路径只写在 PATHS」红线 | `web/src/App.tsx`、`client.ts` | 三处改走 `PATHS`，新增 `PATHS.version` |

### 1.2 第二轮（贡献入口重构同期）

| # | 问题 | 处置 |
|---|---|---|
| D1 | 贡献者页的停用/恢复按钮打 `/credentials/{id}/resume\|pause`，**后端没有这两条路由**，404 被折成「接口尚未接入」 | 后端补 `POST /api/contributions/credentials/{id}/pause\|resume`（只切 `upstreams.enabled`，属主校验、幂等、恢复仅对 `status=active` 生效——不能让未通过鉴定的坏 Key 借「恢复」回到候选池） |
| P1 | Key 的 `expires_at: null` 清空不掉（`*string` 分不出「缺省」与「null」） | 后端改用显式三态包装（`Set`+`Value`）；顺带堵住「按别人的 Key id 能清掉过期时间」的越权（归属检查前置）；前端修了 `datetime-local` 的 UTC/本地时区平移 8 小时 bug |
| P2 | `GET /api/contributions/stats?range=` 后端完全不读，统计不随周期切换 | 支持 `day`/`month`（UTC 日界，0 起），非法值 `400 param=range`；`range` 只作用于调用量三项，凭据数保持当前快照 |
| P3 | 调用日志前端用 `cursor` 分页、带 `error_code`；后端只认 `offset`、忽略 `error_code` | 前端改 `offset` 分页；后端 `UsageLogFilter`/`store.RequestLogFilter` 补 `ErrorCode` 精确匹配（COUNT 与 LIMIT 共用同一 where，翻页总数不再错位） |
| E | `AvailableModel.capabilities` 恒空对象，界面永远渲染空徽标 | 后端删除该字段（租户侧无可靠数据源），前端同步删除；`ModelCapabilities` 保留（管理面仍在用） |
| E | `Session.status` 前端有、后端曾缺 | 已于前一轮补上（本轮补回归测试钉住，含与 `must_change_password` 互不干扰的断言） |

---

## 2. 仍未决（**未改动**）

### 2.1 字段口径需拍板

| # | 位置 | 现状 | 需要谁拍板 |
|---|---|---|---|
| D2 | `types.ts` 的 `ContributionCredentialDetail` | `recheck_history` / `status_reason` 后端给的是 `stage_results` / `evidence_text`；`kind` / `claimed_model` 后端硬编码 `"api_key"` / `""`（OAuth 型贡献在界面上看不出来） | 贡献者详情页的字段口径（改前端对齐后端，还是后端补列/补值） |

### 2.2 后端已实现但界面没有入口

| # | 端点 | 说明 |
|---|---|---|
| B1 | `POST /api/account/keys/{id}/rotate` | Key 轮换只有 API，`KeyPages.tsx` 无入口 |
| B2 | `PATCH /api/account/profile` | 改显示名只有 API（当前只能随改密顺带传） |
| B3 | `GET /api/admin/real-models/{id}`、`/upstreams/{id}`、`/audit/{id}`、`/inbox/{id}` | 后端在册，前端封装了但没做详情页 |

### 2.3 死封装与恒空字段（不影响运行）

- 前端死封装：`getRealModel` / `getUpstream` / `getAudit` / `getInboxItem`（给未做的详情页预留，
  保留即可）、`PATHS.myContributions`（零引用）。`InboxCount`、`ErrorPageState`、`Envelope`
  别名已在第二轮删除；`GroupModelQuotaCap` / `CapabilityMode` / `UsageDimension` 仍被引用，保留。
- 后端恒 `null` 字段：`ApiKey.last_used_at`、`RecoveryCodeState.generated_at`/`last_used_at`。
  界面若已渲染，要么后端补值，要么前端去掉展示。

---

## 3. 纪律结论

1. **请求体的键名必须进契约**：两轮已修项里三个都是「响应字段有文档、请求体没有」，
   而请求体恰恰是最容易写错、且错了会被 JSON 静默吞掉的那一侧。
2. **两种信封各自有函数**：`{data,audit_id}`（管理面）与裸 `{ok}`（控制台面少量端点）
   在前端必须用不同的封装表达，否则类型系统在对人撒谎。
3. **发出去但没人读的参数是 bug**：`cursor`（后端只认 offset）、`error_code`（后端不读）、
   `enabled`（后端读 recheck）、`range`（后端不读）——四个都属于「前端发了、后端静默无视」，
   界面上表现为「设置了但没效果」。审计这类缝要比对**参数名本身**，不能只看端点是通的。
4. **死封装好过错封装**：后端在册、前端未做的详情页保留封装；反过来「前端自己发明一条
   后端没有的路由」是 P0 级问题（D1 就是），它会被 `isUnavailable` 伪装成正常空态。
