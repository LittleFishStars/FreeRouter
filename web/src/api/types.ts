// 控制台 API 的共享类型。
//
// 来源：
//   - 管理面：docs/api/02-admin.md 与 feat/admin-api 分支的 DTO（字段名即契约）；
//   - 会话/初始化：internal/httpapi/handlers_auth.go / handlers_setup.go；
//   - 租户面与贡献者面：docs/ui/04-tenant.md、05-contributor.md 的字段清单。
//     注意：这两块的 HTTP 端点尚未在 docs/api 定稿（只有管理面有分册），
//     src/api/endpoints.ts 里对应函数的路径按 REST 形状暂定并显式标注；
//     后端未接入时页面统一走「接口尚未接入」态，不伪造数据。

/** 权限类（PLAN 2.2：布尔组合，不是角色）。 */
export type AccountClass = 'admin' | 'tenant' | 'contributor'

/** 账户状态（docs/ui/06-design.md 6.3 的五态）。 */
export type AccountStatus = 'pending' | 'active' | 'disabled' | 'rejected' | 'merged'

/** 列表响应信封（docs/api/02-admin.md 3.1）。total 是后端多给的翻页辅助。 */
export interface ListEnvelope<T> {
  data: T[]
  next_cursor: string | null
  total?: number
}

/** 写响应信封（docs/api/02-admin.md 3.2：每个写响应带 audit_id 以便回指审计）。 */
export interface WriteEnvelope<T> {
  data: T
  audit_id?: string
}

// ── 会话 ────────────────────────────────────────────────────────────────

/** GET /api/auth/session 的响应（login 响应是它的子集 + expires_at）。 */
export interface Session {
  account_id: string
  display_name: string
  classes: AccountClass[]
  acting_class?: AccountClass
  session_id?: string
  expires_at?: string
  /**
   * true = 当前口令是一次性口令（首启随机生成/管理员重置）：
   * 改密前除会话/登出/改密外的控制台接口全 403 password_change_required。
   * 登录响应与会话响应都带这个字段。
   */
  must_change_password?: boolean
  /** 后端当前未返回；pending 判定一旦上线，这里即可驱动 /pending 强制跳转。 */
  status?: AccountStatus
}

/** GET /api/setup/status 的响应。 */
export interface SetupStatus {
  setup_required: boolean
  registration_enabled: boolean
  requires_approval: boolean
}

// ── 管理面：账户 ───────────────────────────────────────────────────────

export interface AdminQuota {
  /** request / token / credit */
  unit: 'request' | 'token' | 'credit'
  /** 0 = 零额度全拒；没有该行 = 不限 */
  limit: number
  /** 只读：由结算写入 */
  used: number
  period: 'day' | 'month'
}

export interface AdminAccount {
  id: string
  display_name: string
  username: string
  classes: string[]
  status: string
  groups: string[]
  created_at: string | null
  last_login_at: string | null
  note?: string
  /** -1 = 不限，界面显示为「不限」而不是 -1 */
  rpm_limit: number
  tpm_limit: number
  max_concurrency: number
  quota?: AdminQuota
  key_count: number
  credential_count: number
  /** 只在创建/重置密码且本轮生成时出现，一次性回显 */
  generated_password?: string
}

export interface AdminAccountUpdate {
  display_name?: string
  classes?: string[]
  status?: string
  groups?: string[]
  note?: string
  quota?: AdminQuota
  rpm_limit?: number
  tpm_limit?: number
  max_concurrency?: number
}

export interface PendingAccount {
  id: string
  display_name: string
  username: string
  classes: string[]
  status: string
  created_at: string | null
  /** 来源 IP：判断是否被批量刷号（docs/ui/03-admin.md 第 3 节） */
  source_ip?: string
  note?: string
}

export interface ApproveInput {
  /** 批准必须指定组（服务端要求非空数组） */
  groups: string[]
  /** 账户级额度（唯一一份，量词是"每人"） */
  quota: {
    unit: 'request' | 'token' | 'credit'
    limit: number
    period: 'day' | 'month'
  }
}

// ── 管理面：组 ─────────────────────────────────────────────────────────

export interface GroupPolicyQuota {
  unit: 'request' | 'token' | 'credit'
  limit: number
  period: 'day' | 'month'
}

export interface GroupModelQuotaCap extends GroupPolicyQuota {
  model: string
}

export interface GroupPool {
  /** null = 全模型合计池；有值 = 该模型的组级共享池 */
  model: string | null
  unit: 'request' | 'token' | 'credit'
  period: 'day' | 'month'
  limit: number
  used?: number
  /** 当前周期锚点（RFC3339 或 null） */
  period_start?: string | null
}

export interface GroupPolicies {
  allow_create_key?: boolean
  max_keys?: number
  allow_widen_allowlist?: boolean
  account_quotas?: GroupPolicyQuota[]
  model_quota_caps?: GroupModelQuotaCap[]
  account_rpm_limit?: number
  account_tpm_limit?: number
  account_max_concurrency?: number
  /** 组级共享池：多行，每维一行 */
  pool?: GroupPool[]
  [key: string]: unknown
}

export interface AdminGroup {
  id: string
  name: string
  description?: string
  is_default: boolean
  member_count: number
  created_at: string | null
}

export interface AdminGroupDetail extends AdminGroup {
  members: string[]
  /** 抽象模型对外名 */
  models: string[]
  policies: GroupPolicies
}

// ── 管理面：真实模型 ───────────────────────────────────────────────────

export interface AdminRealModel {
  id: string
  display_name: string
  /** 只读：dsh:渠道/上游模型 id */
  qualified_name: string
  upstream_id: string
  upstream_model_id: string
  group_name: string | null
  channel?: string
  context_window: number | null
  modalities: string[]
  efforts: string[]
  status: 'active' | 'missing' | 'disabled' | string
  hidden: boolean
  sort_order: number
  source: 'auto' | 'override' | string
  credit_multiplier?: { in: number; out: number }
  last_seen_at: string | null
  missing_since: string | null
  excluded_from_candidates: boolean
}

export interface AdminRealModelPatch {
  display_name?: string
  group_name?: string | null
  context_window?: number | null
  modalities?: string[]
  efforts?: string[]
  hidden?: boolean
  sort_order?: number
  excluded_from_candidates?: boolean
  credit_multiplier?: { in: number; out: number }
}

// ── 管理面：抽象模型 ───────────────────────────────────────────────────

export type CapabilityMode = 'strict' | 'filter'
export type StrategyName = 'expiry-first' | 'balance' | 'speed' | 'price' | 'round-robin'

export interface ModelCapabilities {
  context_window?: number
  max_output?: number
  vision?: boolean
  tool_calling?: boolean
  json_mode?: boolean
  [key: string]: unknown
}

export interface AdminAbstractModel {
  id: string
  /** 对外名，创建后不可改 */
  name: string
  display_name: string
  capability_mode: CapabilityMode | string
  capabilities?: ModelCapabilities
  strategy: StrategyName[] | string[]
  status: 'active' | 'deprecated' | 'disabled' | string
  /** 替代目标（对外名） */
  replacement: string | null
  deprecated_at: string | null
  sunset_at: string | null
  migration_doc?: string
  note?: string
  candidate_count: number
  available_candidate_count: number
  chain: string[] | null
}

export interface Candidate {
  real_model_id: string
  constraint?: {
    channel_scope?: string
    require?: Record<string, unknown>
  }
  weight: number
  /** 响应侧展开结果：声明式候选 → 当前可用凭据实例数 */
  available_instances?: number
}

export interface ChainPreview {
  chain: string[]
  hops: number
  final_active: string
  warning: string | null
  capability_check: {
    ok: boolean
    checks: Record<string, boolean>
  }
}

export interface DryRunResult {
  reachable: number
  no_candidate: number
  fallback_used: number
  failures: { reason: string; count: number }[]
}

export interface DeprecationInput {
  /** deprecated 是单向弃用；disabled 是立即停用（对外 404/410） */
  status: 'deprecated' | 'disabled'
  replacement_model_id: string
  /** epoch 秒（契约例子 "@1688169599" 的数值形态） */
  deprecated_at?: number
  /** Sunset 时间：服务端 DTO 用 epoch 秒（与文档里的 RFC 8594 HTTP-date 表述不一致，以 DTO 为准） */
  sunset_at?: number
  migration_doc?: string
  note?: string
}

// ── 管理面：上游与凭据 ─────────────────────────────────────────────────

export type UpstreamKind = 'relay' | 'vendor' | 'selfhosted'

export interface AdminUpstream {
  id: string
  kind: UpstreamKind | string
  name: string
  base_url: string
  enabled: boolean
  owner_account_id?: string
  last_seen_at: string | null
  secret_present: boolean
}

export interface AdminCredential {
  id: string
  owner_account_id: string
  provider: string
  external_id: string
  external_label?: string
  status: string
  login_enabled: boolean
  verify_status: string
  verify_detail?: unknown
  created_at: string | null
  last_used_at: string | null
}

// ── 管理面：鉴定 ───────────────────────────────────────────────────────

export interface VerificationEvidence {
  signal: string
  strength: 'strong' | 'medium' | 'weak' | string
  result: string
}

export interface AdminVerification {
  id: string
  credential_id: string
  provider: string
  claimed_model?: string
  source?: string
  stage?: string
  verdict: string
  evidence?: VerificationEvidence[]
  override?: { verdict?: string; reason?: string; operator?: string; at?: string } | null
  stage_results?: { L0?: unknown; L1?: unknown; L2?: unknown }
  recheck?: { enabled: boolean; last_at?: string } | null
}

// ── 管理面：审计与设置 ─────────────────────────────────────────────────

export interface AuditEntry {
  id: string
  ts: string | null
  account_id?: string
  acting_class?: string
  action: string
  target: string | null
  ip?: string
  ua?: string
  result: string
  detail?: unknown
}

export interface SettingsDoc {
  [group: string]: {
    [key: string]: unknown
  }
}

export interface IpCheckResult {
  source_ip: string
  allowed: boolean
  reason: string
  behind_proxy: boolean
  trusted_proxies?: string[]
}

// ── 管理面：待处理事项 ─────────────────────────────────────────────────

export type InboxKind =
  | 'registration'
  | 'model_change'
  | 'failure'
  | 'pool_alert'
  | 'breaker_alert'

export interface InboxItem {
  id: string
  kind: InboxKind | string
  severity: 'error' | 'warning' | 'info' | string
  title: string
  detail_json?: unknown
  related_id: string | null
  created_at: string | null
  read_at: string | null
  read_by: string | null
}

export interface InboxCount {
  count: number
}

// ── 租户面（docs/ui/04-tenant.md；端点按 REST 形状暂定）───────────────

export interface ApiKey {
  id: string
  name: string
  /** 列表只显示前缀（fr-xxxx…xxxx） */
  prefix: string
  status: 'active' | 'disabled' | 'expired' | string
  expires_at: string | null
  /** 白名单里的抽象模型名 */
  allowlist: string[]
  last_used_at: string | null
  created_at: string | null
}

export interface ApiKeyCreated {
  key: ApiKey
  /** 明文只出现一次（服务端只存 hash） */
  plaintext: string
}

export interface UsageDimension {
  unit: 'request' | 'token' | 'credit'
  limit: number
  used: number
  period: 'day' | 'month'
  /** 周期重置时刻（RFC3339） */
  reset_at?: string | null
}

export interface UsageOverview {
  display_name: string
  /** 组名只读展示（不显示「因为你在 X 组」） */
  group_names: string[]
  key_count: number
  key_limit: number | null
  active_key_count: number
  keys_used_7d: number
  dimensions: UsageDimension[]
  rpm_limit: number
  tpm_limit: number
  max_concurrency: number
  /** 按模型单独限额的行（只列被限的模型） */
  model_limits?: {
    model: string
    unit: 'request' | 'token' | 'credit'
    limit: number
    used: number
    period: 'day' | 'month'
  }[]
}

export interface UsageLog {
  id: string
  ts: string | null
  endpoint: string
  /** 只出现抽象模型名（绝不含真实模型名） */
  abstract_model: string
  key_id: string
  key_name: string
  key_prefix: string
  ok: boolean
  error_code: string | null
  ttft_ms: number | null
  duration_ms: number | null
  input_tokens: number | null
  output_tokens: number | null
}

export interface AvailableModel {
  name: string
  display_name: string
  capabilities: ModelCapabilities
  status: 'active' | 'deprecated' | 'disabled' | string
  replacement?: string | null
  migration_doc?: string
}

// ── 贡献者面（docs/ui/05-contributor.md；端点按 REST 形状暂定）─────────

export interface ContributionCredential {
  id: string
  external_label: string
  kind: 'oauth' | 'api_key'
  claimed_model: string
  status: 'active' | 'pending' | 'invalid' | 'expired' | 'disabled' | string
  verdict: 'verified' | 'suspect' | 'unverified' | 'trusted' | string
  stage?: 'L0' | 'L1' | 'L2' | string
  created_at: string | null
  last_called_at: string | null
  /** 是否在候选池中（不给「被哪些抽象模型引用」） */
  in_pool?: boolean
}

export interface ContributionCredentialDetail extends ContributionCredential {
  evidence?: VerificationEvidence[]
  stage_results?: { L0?: unknown; L1?: unknown; L2?: unknown }
  recheck_history?: { at: string; verdict: string }[]
  status_reason?: string
}

export interface ContributionStats {
  credential_count: number
  in_pool_count: number
  claimed_model_count: number
  call_count: number
  input_tokens: number
  output_tokens: number
  health: { active: number; disabled: number; suspect: number }
}

export interface RecoveryCodeState {
  generated: boolean
  generated_at: string | null
  last_used_at: string | null
}

// ── 错误页 ─────────────────────────────────────────────────────────────

export interface ErrorPageState {
  code: string
  requestId?: string
}
