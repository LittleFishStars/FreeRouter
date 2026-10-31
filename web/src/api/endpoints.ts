// 各资源面的类型化端点封装。
//
// 纪律：路径只写在 api/client.ts 的 PATHS 里，本文件只负责「拼动态段 +
// 声明请求/响应类型」。页面不直接 import client 的 request。
//
// 管理面路径与字段严格对照 docs/api/02-admin.md；租户面与贡献者面在
// docs/api 尚无分册（只有 UI 分册的字段清单），路径暂按 REST 形状定，
// 函数上方都标了「暂定」。后端未接入时页面走「接口尚未接入」态。
//
// DTO 权威：管理面的请求/响应字段以 internal/httpapi/mw_admin.go 的
// Admin* DTO 为准（docs/api/02-admin.md 描述契约语义；两者冲突时先按
// DTO 落码，并把冲突回报 Lead）。UI 分册不定义接口字段，别拿 IA 当契约改。

import {
  deleteWrite,
  getBlob,
  getEnvelope,
  getList,
  getOne,
  PATHS,
  patchWrite,
  postPlain,
  postWrite,
  putWrite
} from './client'
import type {
  AdminAbstractModel,
  AdminAccount,
  AdminAccountUpdate,
  AdminCredential,
  AdminGroup,
  AdminGroupDetail,
  AdminModelSourcesResponse,
  AdminRealModel,
  AdminRealModelPatch,
  AdminUpstream,
  AdminVerification,
  ApiKey,
  ApiKeyCreated,
  ApproveInput,
  AuditEntry,
  AvailableModel,
  Candidate,
  ChainPreview,
  ContributionCredential,
  ContributionCredentialDetail,
  ContributionProviderCatalog,
  ContributionStats,
  DeprecationInput,
  DryRunResult,
  GroupPolicies,
  InboxItem,
  IpCheckResult,
  ListEnvelope,
  ModelCapabilities,
  PendingAccount,
  RecoveryCodeState,
  SettingsDoc,
  StrategyName,
  UpstreamKind,
  UsageLog,
  UsageStats,
  UsageStatsQuery,
  UsageOverview,
  ConnectivityResult,
  UpstreamModelProbeResult
} from './types'

const seg = (v: string) => encodeURIComponent(v)

/** 分页参数（管理面统一 limit + cursor）。 */
export interface PageQuery {
  limit?: number
  cursor?: string
}

// ── 账户自助 ───────────────────────────────────────────────────────────

/**
 * POST /api/account/password。改密即吊销全部会话（含当前），必须重新登录。
 * display_name 可选：首启强制改密时一并补全（写失败不影响改密结果）。
 */
export function putAccountPassword(body: {
  current_password: string
  new_password: string
  display_name?: string
}) {
  return postPlain<{ ok: boolean; message?: string; display_name_updated?: boolean }>(
    PATHS.accountPassword,
    body
  )
}

export interface AccountSession {
  id: string
  ip: string | null
  ua: string | null
  created_at: string | null
  expires_at: string | null
  current: boolean
}

/** 暂定：GET /api/account/sessions。 */
export function getAccountSessions() {
  return getList<AccountSession>(PATHS.accountSessions)
}

/** 暂定：POST /api/account/sessions/{id}/revoke。 */
export function revokeAccountSession(id: string) {
  return postPlain<{ ok: boolean }>(`${PATHS.accountSessions}/${seg(id)}/revoke`)
}

// ── 注册 / 待审核 ──────────────────────────────────────────────────────

/** 暂定：POST /api/register。成功与否的对外表现一致（防枚举，02-public 4.2）。 */
export function postRegister(body: { username: string; display_name: string; password: string }) {
  return postWrite<{ status: string }>(PATHS.register, body)
}

/** 暂定：GET /api/account/status —— 待审核页的「刷新状态」。 */
export function getAccountStatus() {
  return getOne<{ status: string; classes: string[] }>(PATHS.pendingStatus)
}

// ── 贡献（公共区，未登录可用）─────────────────────────────────────────

/** GET /api/contributions/providers —— 三类入口（反代 / API / 自定义）。
 *
 * 形状是 `{data:{categories:[…]}}`（契约 v2 §1）：不再是平铺数组。旧的「api-key」
 * 伪条目与 api_key_only 供应商已经删除——API Key 贡献由 api 目录 + 自定义表单承担。
 */
export function getContributeProviders() {
  return getOne<ContributionProviderCatalog>(PATHS.contributeProviders)
}

/**
 * 发起授权的返回。两种形态由后端（sidecar 注册表）决定：
 *   redirect —— 给 authorize_url，浏览器跳过去，供应商回调到本站；
 *   session —— 给 session_id，sidecar 在后台轮询（设备码/二维码/短信），
 *              前端按 interval 轮询 pollOAuthContribution。
 */
export interface OAuthAuthorize {
  mode: 'redirect' | 'session'
  authorize_url?: string
  session_id?: string
  /** 会话形态：device_code | sms | qr */
  flow?: string
  user_code?: string
  verification_uri?: string
  qr_image_url?: string
  /** 服务端内联的二维码（data URL）。CSP 只允许 'self' 与 data:，外链图会被拦。 */
  qr_image_data?: string
  /** 二维码承载的原始文本（sidecar 无图片端点时下发，前端本地画码，如 Raccoon） */
  qr_code?: string
  interval?: number
  message?: string
}

export function startOAuthContribution(
  provider: string,
  opts?: { phone?: string; captchaParam?: string },
) {
  const q = new URLSearchParams()
  if (opts?.phone) q.set('phone', opts.phone)
  // 阿里云滑块产物（Raccoon 短信登录必需；其他 provider 忽略）
  if (opts?.captchaParam) q.set('captcha_param', opts.captchaParam)
  const qs = q.toString()
  return postWrite<OAuthAuthorize>(`${PATHS.contributions}/oauth/${seg(provider)}${qs ? `?${qs}` : ''}`)
}

export interface OAuthPollResult {
  status: 'pending' | 'ok' | 'failed' | 'expired'
  message?: string
  interval?: number
  outcome?: {
    accepted: boolean
    status: string
    verify_status: string
    account_created: boolean
    recovery_code?: string
    message: string
  }
}

/** 查一次后台授权会话（session 形态）。 */
export function pollOAuthContribution(
  provider: string,
  body: { session_id: string; code?: string }
) {
  return postWrite<OAuthPollResult>(
    `${PATHS.contributions}/oauth/${seg(provider)}/poll`,
    body
  )
}

/** 放弃一次后台授权会话（幂等）。 */
export function cancelOAuthContribution(provider: string, sessionId: string) {
  return postPlain<{ ok: boolean }>(`${PATHS.contributions}/oauth/${seg(provider)}/cancel`, {
    session_id: sessionId
  })
}

/**
 * 贡献提交入参（POST /api/contributions）。两类互斥，用 kind 判别：
 *   api    → endpoint 由后端从内置目录解析（请求体里不带 endpoint），provider 必填；
 *   custom → endpoint 必填（任意 OpenAI 兼容端点）。
 * label 在未登录建号时用作显示名，可留空。
 */
export type ContributionSubmitInput =
  // kind=api 不收 claimed_models（2026-10-25 定案）：可用模型由服务端探测得出。
  | { kind: 'api'; provider: string; api_key: string; label: string }
  // claimed_models 可为空数组（2026-10-26 决定：自定义供应商的该字段改为非必填，
  // 留空由服务端探测端点模型目录自动获取；探测不到时后端在落库前退回 400，
  // param=claimed_models，提示贡献者手工填写）。
  | { kind: 'custom'; endpoint: string; api_key: string; label: string; claimed_models?: string[] }

export interface ContributeResult {
  accepted: boolean
  /** 落库后的凭据状态（后端 store 的状态词，不是鉴定结论）。 */
  status: string
  verify_status: string
  /** 幂等复用：同一 (owner, provider, external_id) 重复提交时为 true。 */
  reused: boolean
  account_created: boolean
  recovery_code?: string
  message?: string
  /** 未声明模型时服务端自动获取到的模型目录（供贡献者确认，不影响本次判定）。 */
  discovered_models?: string[]
}

/** POST /api/contributions —— API 供应商 / 自定义端点贡献（服务端永不回显 Key）。 */
export function submitContribution(body: ContributionSubmitInput) {
  return postWrite<ContributeResult>(PATHS.contributions, body)
}

/** 暂定：POST /api/contributions/recovery —— 恢复码兑换。 */
export function postContributeRecovery(code: string) {
  return postWrite<{ accepted: boolean }>(PATHS.contributeRecovery, { code })
}

// ── 管理面：账户 ───────────────────────────────────────────────────────

export interface AccountListQuery extends PageQuery {
  class?: string
  status?: string
  group_id?: string
}

export function listAccounts(q: AccountListQuery = {}) {
  return getList<AdminAccount>(PATHS.adminAccounts, { ...q })
}

export function getAccount(id: string) {
  return getOne<AdminAccount>(`${PATHS.adminAccounts}/${seg(id)}`)
}

export function patchAccount(id: string, body: AdminAccountUpdate) {
  return patchWrite<AdminAccount>(`${PATHS.adminAccounts}/${seg(id)}`, body)
}

export function createAccount(body: {
  username: string
  display_name: string
  password?: string
  classes: string[]
  status?: string
  groups?: string[]
}) {
  return postWrite<AdminAccount>(PATHS.adminAccounts, body)
}

export function resetAccountPassword(id: string, password?: string) {
  // 不给 password 时让服务端生成一次性随机值（generate），并吊销旧会话
  return postWrite<AdminAccount>(`${PATHS.adminAccounts}/${seg(id)}/password`, {
    ...(password ? { password } : { generate: true }),
    revoke_sessions: true
  })
}

export function revokeAccountSessions(id: string) {
  return postWrite<{ revoked: number }>(`${PATHS.adminAccounts}/${seg(id)}/sessions/revoke`)
}

export function listPendingAccounts(q: PageQuery = {}) {
  return getList<PendingAccount>(PATHS.adminAccountsPending, { ...q })
}

export function approvePendingAccount(id: string, body: ApproveInput) {
  return postWrite<AdminAccount>(`${PATHS.adminAccountsPending}/${seg(id)}/approve`, body)
}

export function rejectPendingAccount(id: string, reason: string) {
  return postWrite<AdminAccount>(`${PATHS.adminAccountsPending}/${seg(id)}/reject`, { reason })
}

/** 暂定：CSV 导入。后端尚未接线时该端点 404（页面走未接入态）。 */
export function importAccountsCsv(csv: string) {
  return postWrite<{ imported: number; generated: { username: string; password: string }[] }>(
    PATHS.adminAccountsImport,
    { csv }
  )
}

export interface MergePlan {
  source_id: string
  target_id: string
  /** 🔴 阻塞项（同 provider 凭据撞身份）：非空时不得执行合并。 */
  blocking: string[]
  /** 🟡 将被自动改名的 Key（名字不是身份，Key 的身份是它的 secret）。 */
  renamed_keys: string[]
  keys: number
  credentials: number
  group_relations: number
  source_classes: string[]
  target_classes: string[]
  merged?: boolean
  rolled_back?: boolean
}

/** 合并预检（只读）：确认页必须先看它，🔴 为空才允许点确认。 */
export function precheckMerge(id: string, target: string) {
  // 查询串手工拼：getOne 不带 query 参数（本文件里唯一的 GET 带参场景）。
  return getOne<MergePlan>(
    `${PATHS.adminAccounts}/${seg(id)}/merge/precheck?target=${seg(target)}`
  )
}

export function mergeAccount(id: string, targetAccountId: string) {
  return postWrite<{ merged: boolean }>(`${PATHS.adminAccounts}/${seg(id)}/merge`, {
    target_account_id: targetAccountId
  })
}

export function rollbackMerge(id: string) {
  return postWrite<{ rolled_back: boolean }>(`${PATHS.adminAccounts}/${seg(id)}/merge/rollback`)
}

// ── 管理面：组 ─────────────────────────────────────────────────────────

export function listGroups(q: PageQuery = {}) {
  return getList<AdminGroup>(PATHS.adminGroups, { ...q })
}

export function getGroup(id: string) {
  return getOne<AdminGroupDetail>(`${PATHS.adminGroups}/${seg(id)}`)
}

export function createGroup(body: {
  name: string
  description?: string
  models?: string[]
  policies?: GroupPolicies
}) {
  return postWrite<AdminGroup>(PATHS.adminGroups, body)
}

export function patchGroup(
  id: string,
  body: { name?: string; description?: string; is_default?: boolean }
) {
  return patchWrite<AdminGroup>(`${PATHS.adminGroups}/${seg(id)}`, body)
}

export function deleteGroup(id: string) {
  return deleteWrite<{ deleted: boolean }>(`${PATHS.adminGroups}/${seg(id)}`)
}

export function duplicateGroup(id: string, newName: string) {
  return postWrite<AdminGroup>(`${PATHS.adminGroups}/${seg(id)}/duplicate`, { name: newName })
}

export function putGroupModels(id: string, models: string[]) {
  return putWrite<{ models: string[] }>(`${PATHS.adminGroups}/${seg(id)}/models`, { models })
}

export function putGroupPolicies(id: string, policies: GroupPolicies) {
  // 端点载荷是 {policies: 文档原文}（docs/api/02-admin.md 6：整文档替换）
  return putWrite<GroupPolicies>(`${PATHS.adminGroups}/${seg(id)}/policies`, { policies })
}

export function addGroupMembers(id: string, accountIds: string[]) {
  return postWrite<{ members: string[] }>(`${PATHS.adminGroups}/${seg(id)}/members`, {
    account_ids: accountIds
  })
}

export function removeGroupMember(id: string, accountId: string) {
  return deleteWrite<{ members: string[] }>(
    `${PATHS.adminGroups}/${seg(id)}/members/${seg(accountId)}`
  )
}

// ── 管理面：真实模型 ───────────────────────────────────────────────────

export function listRealModels(
  q: PageQuery & { status?: string; hidden?: boolean; source?: string; upstream_id?: string } = {}
) {
  return getList<AdminRealModel>(PATHS.adminRealModels, { ...q })
}

export function getRealModel(id: string) {
  return getOne<AdminRealModel>(`${PATHS.adminRealModels}/${seg(id)}`)
}

/** 手工添加模型（契约 §1.3）。POST /api/admin/real-models，source=override。
 * 响应 = 渲染后的 AdminRealModel（同 GET 详情形状）；同 upstream_model_id 已存在时幂等返回既有行。 */
export function createRealModel(body: {
  upstream_id: string
  upstream_model_id: string
  display_name?: string
  context_window?: number
}) {
  return postWrite<AdminRealModel>(PATHS.adminRealModels, body)
}

export function refreshRealModels() {
  return postWrite<Record<string, unknown>>(`${PATHS.adminRealModels}/refresh`)
}
export function patchRealModel(id: string, body: AdminRealModelPatch) {
  return patchWrite<AdminRealModel>(`${PATHS.adminRealModels}/${seg(id)}`, body)
}

export function excludeRealModel(id: string, excluded: boolean) {
  return postWrite<AdminRealModel>(`${PATHS.adminRealModels}/${seg(id)}/exclude`, { excluded })
}

// ── 管理面：抽象模型 ───────────────────────────────────────────────────

export function listAbstractModels(q: PageQuery = {}) {
  return getList<AdminAbstractModel>(PATHS.adminAbstractModels, { ...q })
}

export function getAbstractModel(id: string) {
  return getOne<AdminAbstractModel>(`${PATHS.adminAbstractModels}/${seg(id)}`)
}

export function createAbstractModel(body: {
  name: string
  display_name: string
  capability_mode?: string
  capabilities?: ModelCapabilities
  strategy?: StrategyName[]
  candidates?: Candidate[]
}) {
  return postWrite<AdminAbstractModel>(PATHS.adminAbstractModels, body)
}

export function patchAbstractModel(
  id: string,
  body: {
    display_name?: string
    capability_mode?: string
    capabilities?: ModelCapabilities
    strategy?: string[]
    /** 停用 / 恢复（弃用走 /deprecation，单向） */
    status?: 'active' | 'disabled'
  }
) {
  return patchWrite<AdminAbstractModel>(`${PATHS.adminAbstractModels}/${seg(id)}`, body)
}

/**
 * 候选集读取。
 * 契约（docs/api/02-admin.md 8.2）没有单列 GET candidates 端点；管理面把
 * 候选挂在抽象模型详情的 `candidates` 字段上（与 data 并列）。这里读详情，
 * 让页面在「只有详情端点」的版本上也能工作。
 */
export async function getCandidates(id: string): Promise<{ candidates: Candidate[] }> {
  const env = await getEnvelope<{ data: AdminAbstractModel; candidates?: Candidate[] }>(
    `${PATHS.adminAbstractModels}/${seg(id)}`
  )
  return { candidates: env.candidates ?? [] }
}

/**
 * 候选集编辑器的「供应商 → 模型」两栏数据（契约 §1.1）。
 * 注意它不是 ListEnvelope：响应体就是 `{sources, channels}`，不解信封。
 */
export function getModelSources(): Promise<AdminModelSourcesResponse> {
  return getEnvelope<AdminModelSourcesResponse>(PATHS.adminModelSources)
}

export function putCandidates(id: string, candidates: Candidate[]) {
  return putWrite<{ candidates: Candidate[] }>(
    `${PATHS.adminAbstractModels}/${seg(id)}/candidates`,
    { candidates }
  )
}

export function dryRunAbstractModel(
  id: string,
  body: { strategy: string[]; sample_size?: number }
) {
  return postWrite<DryRunResult>(`${PATHS.adminAbstractModels}/${seg(id)}/dry-run`, body)
}

export function getChain(id: string) {
  return getOne<ChainPreview>(`${PATHS.adminAbstractModels}/${seg(id)}/chain`)
}

export function putDeprecation(id: string, body: DeprecationInput) {
  return putWrite<AdminAbstractModel>(
    `${PATHS.adminAbstractModels}/${seg(id)}/deprecation`,
    body
  )
}

export function getReferences(id: string) {
  return getOne<AdminGroup[]>(`${PATHS.adminAbstractModels}/${seg(id)}/references`)
}

// ── 管理面：上游与凭据 ─────────────────────────────────────────────────

export function listUpstreams(q: PageQuery = {}) {
  return getList<AdminUpstream>(PATHS.adminUpstreams, { ...q })
}

export function getUpstream(id: string) {
  return getOne<AdminUpstream>(`${PATHS.adminUpstreams}/${seg(id)}`)
}

export function createUpstream(body: {
  kind: UpstreamKind
  name: string
  base_url?: string
  enabled?: boolean
}) {
  return postWrite<AdminUpstream>(PATHS.adminUpstreams, body)
}

export function patchUpstream(
  id: string,
  body: { name?: string; base_url?: string; enabled?: boolean }
) {
  return patchWrite<AdminUpstream>(`${PATHS.adminUpstreams}/${seg(id)}`, body)
}

export function changeUpstreamKind(id: string, kind: UpstreamKind) {
  return postWrite<AdminUpstream>(`${PATHS.adminUpstreams}/${seg(id)}/kind`, { kind })
}

export function testUpstream(id: string) {
  return postWrite<Record<string, unknown>>(`${PATHS.adminUpstreams}/${seg(id)}/test`)
}

/**
 * POST /api/admin/upstreams/{id}/remove —— 移除供应商（2026-10-31）。
 *
 * 与「停用」的分界：停用是可逆开关（凭据与密文都留着）；移除是**撤销**——
 * 密钥材料被墓碑覆写（不可恢复）、行退出来源聚合与候选池。行本身保留
 * （归因外键与审计要它）。不可逆，界面走二次确认。
 */
export function removeUpstream(id: string) {
  return postWrite<AdminUpstream>(`${PATHS.adminUpstreams}/${seg(id)}/remove`)
}

export function listCredentials(
  q: PageQuery & { owner?: string; provider?: string; verify_status?: string; status?: string } = {}
) {
  return getList<AdminCredential>(PATHS.adminCredentials, { ...q })
}

export function getCredential(id: string) {
  return getOne<AdminCredential>(`${PATHS.adminCredentials}/${seg(id)}`)
}

// ── 定时签到领积分（2026-10-31，参考 dsh-providers-hub auto-checkin） ──────

/** 一次签到的结果（与 CredentialBalanceResult 同款：上游拒绝不是 5xx）。 */
export interface CheckinClaimResult {
  ok: boolean
  /** claimed / already-claimed / inactive / failed */
  kind: string
  credit?: number
  unit?: string
  message?: string
  /** 展示串（如 "+200 积分"） */
  text?: string
  /** 该渠道没有签到能力（记一次，当天不再试） */
  unsupported?: boolean
}

/** 一轮签到的汇总。 */
export interface CheckinRoundResult {
  attempted: number
  claimed: number
  already_claimed: number
  inactive: number
  failed: number
  unsupported: number
  summary: string
}

/** 自动签到状态视图。 */
export interface CheckinState {
  enabled: boolean
  last_run_date?: string
  last_run_at?: number
  last_run_result?: string
  due_count: number
}

// ── 抽象模型连通性检验（2026-10-31）────────────────────────────────

/** 一条候选的连通性结果。 */
/** POST /api/admin/upstreams/{id}/probe-models —— 凭据池页「检验可用性」：对该上游全部 active 模型各发一次真实推理。 */
export function probeUpstreamModels(id: string) {
  return postWrite<UpstreamModelProbeResult>(`${PATHS.adminUpstreams}/${seg(id)}/probe-models`)
}

/** POST /api/admin/abstract-models/{id}/connectivity —— 逐候选实时探测出站路径（dry-run 是静态干跑，这是真发请求）。 */
export function checkAbstractModelConnectivity(id: string) {
  return postWrite<ConnectivityResult>(`${PATHS.adminAbstractModels}/${seg(id)}/connectivity`)
}

/** POST /api/admin/credentials/{id}/checkin —— 手动签到一条凭据（显式操作，不受开关/到期约束）。 */
export function checkinCredential(id: string) {
  return postWrite<CheckinClaimResult>(`${PATHS.adminCredentials}/${seg(id)}/checkin`)
}

/** POST /api/admin/checkin/run —— 手动跑一轮（只签当前到期的凭据）。 */
export function runCheckinRound() {
  return postWrite<CheckinRoundResult>(`${PATHS.adminCheckin}/run`)
}

/** GET /api/admin/checkin/status —— 自动签到状态。 */
export function getCheckinStatus() {
  return getOne<CheckinState>(`${PATHS.adminCheckin}/status`)
}

/** PUT /api/admin/checkin/enabled —— 自动签到开关。 */
export function setCheckinEnabled(enabled: boolean) {
  return putWrite<{ enabled: boolean }>(`${PATHS.adminCheckin}/enabled`, { enabled })
}

/** POST /api/admin/credentials/{id}/balance-refresh —— 刷新一条凭据的余额（2026-10-28）。 */
export function refreshCredentialBalance(id: string) {
  return postWrite<CredentialBalanceResult>(`${PATHS.adminCredentials}/${seg(id)}/balance-refresh`)
}

export interface CredentialBalanceResult {
  ok: boolean
  total?: number | null
  unit?: string
  reason?: string
  balance_text?: string
}

export function suspendCredential(id: string) {
  return postWrite<AdminCredential>(`${PATHS.adminCredentials}/${seg(id)}/suspend`)
}

export function banCredential(id: string) {
  return postWrite<AdminCredential>(`${PATHS.adminCredentials}/${seg(id)}/ban`)
}

export function notifyCredential(id: string, message: string) {
  return postWrite<{ ok: boolean }>(`${PATHS.adminCredentials}/${seg(id)}/notify`, { message })
}

// ── 管理面：鉴定 ───────────────────────────────────────────────────────

export function listVerifications(
  q: PageQuery & { verdict?: string; provider?: string; source?: string } = {}
) {
  return getList<AdminVerification>(PATHS.adminVerifications, { ...q })
}

export function getVerification(id: string) {
  return getOne<AdminVerification>(`${PATHS.adminVerifications}/${seg(id)}`)
}

export function triggerVerificationL2(id: string) {
  return postWrite<AdminVerification>(`${PATHS.adminVerifications}/${seg(id)}/l2`)
}

export function overrideVerification(id: string, verdict: string, reason: string) {
  return postWrite<AdminVerification>(`${PATHS.adminVerifications}/${seg(id)}/override`, {
    verdict,
    reason
  })
}

// 请求体的键是 recheck 而不是 enabled：后端的 body 结构是
// struct{ Recheck bool `json:"recheck"` }（admin_ops.go 的 recheck handler），
// 发 {enabled} 会被 json 静默忽略——按钮看着能点，实际每次都写 false。
// 响应里的 recheck.enabled 是回读字段，两者不是同一个键。
export function setVerificationRecheck(id: string, enabled: boolean) {
  return postWrite<AdminVerification>(`${PATHS.adminVerifications}/${seg(id)}/recheck`, {
    recheck: enabled
  })
}

// ── 管理面：审计 ───────────────────────────────────────────────────────

export interface AuditQuery extends PageQuery {
  from?: string
  to?: string
  account_id?: string
  acting_class?: string
  action?: string
  result?: string
}

export function listAudit(q: AuditQuery = {}) {
  return getList<AuditEntry>(PATHS.adminAudit, { ...q })
}

export function getAudit(id: string) {
  return getOne<AuditEntry>(`${PATHS.adminAudit}/${seg(id)}`)
}

export function exportAudit(q: AuditQuery = {}, format: 'csv' | 'jsonl' = 'jsonl') {
  return getBlob(`${PATHS.adminAudit}/export`, { ...q, format })
}

// ── 管理面：设置 ───────────────────────────────────────────────────────

export function getSettings() {
  return getOne<SettingsDoc>(PATHS.adminSettings)
}

export function patchSettings(patch: Record<string, unknown>) {
  return patchWrite<{ ok: boolean }>(PATHS.adminSettings, patch)
}

export function checkSettingsIp() {
  return postWrite<IpCheckResult>(`${PATHS.adminSettings}/ip-check`)
}

// ── 管理面：待处理事项 ─────────────────────────────────────────────────

export interface InboxQuery extends PageQuery {
  kind?: string
  severity?: string
  read?: boolean
  from?: string
  to?: string
}

export function listInbox(q: InboxQuery = {}) {
  return getList<InboxItem>(PATHS.adminInbox, { ...q })
}

export function getInboxCount() {
  return getOne<{ unread: number }>(PATHS.adminInboxCount)
}

export function getInboxItem(id: string) {
  return getOne<InboxItem>(`${PATHS.adminInbox}/${seg(id)}`)
}

export function setInboxRead(id: string, read: boolean) {
  return patchWrite<InboxItem>(`${PATHS.adminInbox}/${seg(id)}`, { read })
}

export function markInboxReadBatch(ids: string[]) {
  return postWrite<{ updated: number }>(`${PATHS.adminInbox}/read`, { ids })
}

// ── 租户面（路径暂定）──────────────────────────────────────────────────

export function listKeys(q: PageQuery = {}) {
  return getList<ApiKey>(PATHS.keys, { ...q })
}

export function getKey(id: string) {
  return getOne<ApiKey>(`${PATHS.keys}/${seg(id)}`)
}

export function createKey(body: {
  name: string
  expires_at?: string | null
  allowlist: string[]
}) {
  return postWrite<ApiKeyCreated>(PATHS.keys, body)
}

export function patchKey(
  id: string,
  body: { name?: string; expires_at?: string | null; allowlist?: string[] }
) {
  return patchWrite<ApiKey>(`${PATHS.keys}/${seg(id)}`, body)
}

export function setKeyEnabled(id: string, enabled: boolean) {
  return postWrite<ApiKey>(`${PATHS.keys}/${seg(id)}/${enabled ? 'enable' : 'disable'}`)
}

export function revokeKey(id: string) {
  return postWrite<{ revoked: boolean }>(`${PATHS.keys}/${seg(id)}/revoke`)
}

export function getUsageOverview() {
  return getOne<UsageOverview>(PATHS.usageOverview)
}

// UsageLogQuery 不继承 PageQuery：后端只给 limit/offset（没有 cursor 语义），
// 继承会把 cursor 带进一个后端从未读过的参数，形成「发了但没人读」的静默失效。
export interface UsageLogQuery {
  limit?: number
  offset?: number
  from?: string
  to?: string
  key_id?: string
  model?: string
  status?: 'ok' | 'failed'
  error_code?: string
}

export function listUsageLogs(q: UsageLogQuery = {}) {
  return getList<UsageLog>(PATHS.usageLogs, { ...q })
}

/**
 * GET /api/usage/stats：仪表盘聚合（总量 + 时间桶 + 按模型）。
 * tz_offset 传「当地 - UTC」秒数（JS 的 getTimezoneOffset 是反号，这里由调用方翻好）；
 * scope=all 仅 admin 类可用（后端 403 把关）。
 */
export function getUsageStats(q: UsageStatsQuery = {}) {
  return getOne<UsageStats>(PATHS.usageStats, { ...q })
}

/** 可用抽象模型：可选按 Key 收窄（总纲 11.3 三层收敛）。 */
export function listAvailableModels(keyId?: string) {
  return getList<AvailableModel>(PATHS.usageModels, { key_id: keyId })
}

// ── 贡献者面（路径暂定）────────────────────────────────────────────────

export function listMyCredentials(q: PageQuery = {}) {
  return getList<ContributionCredential>(PATHS.myCredentials, { ...q })
}

export function getMyCredential(id: string) {
  return getOne<ContributionCredentialDetail>(`${PATHS.myCredentials}/${seg(id)}`)
}

/** 凭据启停的返回（契约 v2 §5：`{"data":{"ok":true,"status":"paused"|"enabled"}}`）。 */
export interface CredentialToggleResult {
  ok: boolean
  status: 'paused' | 'enabled'
}

/**
 * POST /api/contributions/credentials/{id}/pause —— 暂停自己贡献的凭据。
 * 会话端点，只能操作属主自己的凭据（非属主 404）；重复调用幂等。
 */
export function pauseMyCredential(id: string) {
  return postWrite<CredentialToggleResult>(`${PATHS.myCredentials}/${seg(id)}/pause`)
}

/** POST /api/contributions/credentials/{id}/resume —— 恢复（不重置鉴定结论）。 */
export function resumeMyCredential(id: string) {
  return postWrite<CredentialToggleResult>(`${PATHS.myCredentials}/${seg(id)}/resume`)
}

/**
 * POST /api/contributions/credentials/{id}/remove —— 移除自己的贡献
 * （2026-10-31）。不可逆：密钥材料被墓碑覆写、行退出列表与候选池。
 * 恢复的唯一途径是重新提交/重新授权（走完整鉴定流）。
 */
export function removeMyCredential(id: string) {
  return postWrite<{ ok: boolean; status: 'removed' }>(`${PATHS.myCredentials}/${seg(id)}/remove`)
}

export function getContributionStats(range: 'day' | 'month' = 'month') {
  return getOne<ContributionStats>(`${PATHS.contributionStats}?range=${range}`)
}

export function getRecoveryCodeState() {
  return getOne<RecoveryCodeState>(PATHS.recoveryCode)
}

export function regenerateRecoveryCode() {
  return postWrite<{ code: string }>(`${PATHS.recoveryCode}/regenerate`)
}

// ── 类型再导出（页面少写一个 import 路径）──────────────────────────────

export type {
  AdminAbstractModel,
  AdminAccount,
  AdminCredential,
  AdminGroup,
  AdminGroupDetail,
  AdminRealModel,
  AdminUpstream,
  AdminVerification,
  ApiKey,
  AuditEntry,
  AvailableModel,
  Candidate,
  ChainPreview,
  ContributionCredential,
  ContributionCredentialDetail,
  ContributionStats,
  DryRunResult,
  GroupPolicies,
  InboxItem,
  ListEnvelope,
  PendingAccount,
  SettingsDoc,
  UsageLog,
  UsageOverview
}
