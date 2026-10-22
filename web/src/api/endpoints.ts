// 各资源面的类型化端点封装。
//
// 纪律：路径只写在 api/client.ts 的 PATHS 里，本文件只负责「拼动态段 +
// 声明请求/响应类型」。页面不直接 import client 的 request。
//
// 管理面路径与字段严格对照 docs/api/02-admin.md；租户面与贡献者面在
// docs/api 尚无分册（只有 UI 分册的字段清单），路径暂按 REST 形状定，
// 函数上方都标了「暂定」。后端未接入时页面走「接口尚未接入」态。

import {
  deleteWrite,
  getBlob,
  getList,
  getOne,
  PATHS,
  patchWrite,
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
  UsageOverview
} from './types'

const seg = (v: string) => encodeURIComponent(v)

/** 分页参数（管理面统一 limit + cursor）。 */
export interface PageQuery {
  limit?: number
  cursor?: string
}

// ── 账户自助 ───────────────────────────────────────────────────────────

/** 暂定：POST /api/account/password，改密即吊销全部会话。 */
export function putAccountPassword(body: { current_password: string; new_password: string }) {
  return postWrite<{ ok: boolean }>(PATHS.accountPassword, body)
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
  return postWrite<{ ok: boolean }>(`${PATHS.accountSessions}/${seg(id)}/revoke`)
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

export interface ContributeProvider {
  id: string
  name: string
  available: boolean
  unavailable_reason?: string
}

/** 暂定：GET /api/contributions/providers —— 只列可用供应商，不可用折叠并给原因。 */
export function getContributeProviders() {
  return getList<ContributeProvider>(PATHS.contributeProviders)
}

/** 暂定：POST /api/contributions/oauth/{provider} —— 返回跳转授权地址。 */
export function startOAuthContribution(provider: string) {
  return postWrite<{ authorize_url: string }>(`${PATHS.contributions}/oauth/${seg(provider)}`)
}

export interface ApiKeyContributionInput {
  endpoint: string
  api_key: string
  label: string
  claimed_models: string[]
}

export interface ContributeResult {
  accepted: boolean
  account_created: boolean
  recovery_code?: string
  message?: string
}

/** 暂定：POST /api/contributions —— API Key 型贡献（服务端永不回显 Key）。 */
export function submitApiKeyContribution(body: ApiKeyContributionInput) {
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
  return postWrite<AdminAccount>(`${PATHS.adminAccounts}/${seg(id)}/password`, {
    password: password || undefined
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
  return putWrite<GroupPolicies>(`${PATHS.adminGroups}/${seg(id)}/policies`, policies)
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
  }
) {
  return patchWrite<AdminAbstractModel>(`${PATHS.adminAbstractModels}/${seg(id)}`, body)
}

export function getCandidates(id: string) {
  return getOne<{ candidates: Candidate[] }>(
    `${PATHS.adminAbstractModels}/${seg(id)}/candidates`
  )
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

export function listCredentials(
  q: PageQuery & { owner?: string; provider?: string; verify_status?: string; status?: string } = {}
) {
  return getList<AdminCredential>(PATHS.adminCredentials, { ...q })
}

export function getCredential(id: string) {
  return getOne<AdminCredential>(`${PATHS.adminCredentials}/${seg(id)}`)
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

export function setVerificationRecheck(id: string, enabled: boolean) {
  return postWrite<AdminVerification>(`${PATHS.adminVerifications}/${seg(id)}/recheck`, {
    enabled
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

export interface UsageLogQuery extends PageQuery {
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

export function setMyCredentialEnabled(id: string, enabled: boolean) {
  return postWrite<ContributionCredential>(
    `${PATHS.myCredentials}/${seg(id)}/${enabled ? 'resume' : 'pause'}`
  )
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
  ListEnvelope as Envelope,
  PendingAccount,
  SettingsDoc,
  UsageLog,
  UsageOverview
}
