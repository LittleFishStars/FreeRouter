# 前后端契约漂移审计（2026-10-24）

> 分册：运维 · 相关：[api/02-admin.md](../api/02-admin.md)、[api/04-tenant.md](../api/04-tenant.md)、
> [05-static-analysis.md](05-static-analysis.md)

**为什么要做这份审计**：`staticcheck` / `tsc` / 单元测试都只看每一侧自己是否自洽——
前端声明 `postWrite<{ok}>` 类型能通过，后端返回裸 `{ok}` 也能通过，**两者之间的缝没人检查**。
这类缝的表现是「按钮点得动、状态不变」，比编译错误难发现得多。

**方法**：把后端 107 条注册点（`server.go` 的 3 条探针 + 4 条 `/v1`、`mw_admin.go` 的
`/api/admin` 下 65 条、`console_routes.go` 的 34 条 `/api` 与 1 条 `/oauth/callback`）
与前端 95 个封装（`endpoints.ts` 90 + `client.ts` 5）逐条对表，再抽查 18 组 DTO 的
JSON tag。**这是静态比对**：标注为「已确认」的条目都回到代码里核过一遍，但除已修项外
没有运行时验证。

---

## 1. 本次已修（有代码改动）

| 问题 | 位置 | 处置 |
|---|---|---|
| 复检开关的请求体键名写成 `enabled`，后端读 `json:"recheck"` —— JSON 静默忽略未知字段，按钮每次都写 `false` | `web/src/api/endpoints.ts`、`internal/httpapi/admin_ops.go` | 前端改发 `{"recheck": bool}`；请求体形状补进 [api/02-admin.md](../api/02-admin.md) §10 |
| 改密 / 吊销会话 / OAuth 取消三个端点返回裸 `{ok:...}`，前端却用 `postWrite` 声明 `{data,audit_id}`，`data` 恒 `undefined`（类型说谎） | `web/src/api/client.ts`、`endpoints.ts` | 新增 `postPlain`，三处改用它；响应形状补进 [api/04-tenant.md](../api/04-tenant.md) §2.2 |
| `App.tsx` 页脚硬编码 `/healthz`、`/readyz`、`/api/version`，违反 `client.ts` 自定的「路径只写在 PATHS」红线 | `web/src/App.tsx`、`client.ts` | 三处改走 `PATHS`，新增 `PATHS.version` |

---

## 2. 待决（**未改动**，按影响排序）

### 2.1 会让界面功能落空

| # | 位置 | 现状 | 需要谁拍板 |
|---|---|---|---|
| D1 | `web/src/api/endpoints.ts` `setMyCredentialEnabled` | 前端 POST `/api/contributions/credentials/{id}/resume\|pause`，**后端没有这两条路由**（`console_routes.go` 只有 GET 列表/详情/统计/恢复码 + regenerate）。404 被 `isUnavailable` 折成「接口尚未接入」，贡献者页的停用/恢复按钮实际无效 | 是否本轮补路由（要动 store 的凭据状态机），还是先在端点表标注「暂仅 API 可用」 |
| D2 | `types.ts` 的 `ContributionCredentialDetail` | `recheck_history` / `status_reason` 后端给的是 `stage_results` / `evidence_text`；`kind` / `claimed_model` 后端硬编码 `"api_key"` / `""`（OAuth 型贡献在界面上看不出来） | 贡献者详情页的字段口径 |

### 2.2 参数被后端忽略（前端发了、后端不读）

| # | 前端 | 后端 | 后果 |
|---|---|---|---|
| P1 | `patchKey` 用 `expires_at: null` 表示清空 | `account_keys.go` 用 `*string`，无法区分 `null` 与缺省 | 过期时间改得动、清不掉 |
| P2 | `getContributionStats({range})` | `handlers_contribution.go` 不读 `range` | 统计不随周期切换 |
| P3 | `listUsageLogs` 用 `cursor` 分页、带 `error_code` 筛选 | `handlers_usage.go` 只认 `offset`，`error_code` 未读 | 翻页参数错位、筛选项无效 |

### 2.3 后端已实现但界面没有入口

| # | 端点 | 说明 |
|---|---|---|
| B1 | `POST /api/account/keys/{id}/rotate` | Key 轮换只有 API，`KeyPages.tsx` 无入口 |
| B2 | `PATCH /api/account/profile` | 改显示名只有 API（当前只能随改密顺带传） |
| B3 | `GET /api/admin/real-models/{id}`、`/upstreams/{id}`、`/audit/{id}`、`/inbox/{id}` | 后端在册，前端封装了但没做详情页 |

### 2.4 前端死代码（未使用，不影响运行）

`types.ts` 的 `InboxCount`（字段名 `count`，后端实为 `unread`）、`GroupModelQuotaCap`、
`CapabilityMode`、`UsageDimension`、`ErrorPageState`，`endpoints.ts` 的 `Envelope` 别名、
`client.ts` 的 `PATHS.myContributions`。删不删看下一轮是否还要用——它们是给未做的详情页
预留的封装，`getRealModel` 一类保留即可。

### 2.5 后端恒空 / 恒 `null` 的字段（前端会显示为空）

`ApiKey.last_used_at`、`RecoveryCodeState.generated_at`/`last_used_at`、`Session.status`、
`AvailableModel.capabilities`、`AdminAbstractModel.chain`（列表态；`/chain` 端点有值）、
`ContributionStats.health`。界面若已渲染这些字段，要么后端补值，要么前端去掉对应展示。

---

## 3. 纪律结论

1. **请求体的键名必须进契约**：本次三个已修项里两个都是「响应字段有文档、请求体没有」，
   而请求体恰恰是最容易写错、且错了会被 JSON 静默吞掉的那一侧。
2. **两种信封各自有函数**：`{data,audit_id}`（管理面）与裸 `{ok}`（控制台面少量端点）
   在前端必须用不同的封装表达，否则类型系统在对人撒谎。
3. **死封装好过错封装**：后端在册、前端未做的详情页保留封装；反过来「前端自己发明一条
   后端没有的路由」是 P0 级问题（见 D1），它会被 `isUnavailable` 伪装成正常空态。
