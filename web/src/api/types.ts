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

// ── 管理面：候选集编辑器（供应商 → 模型）───────────────────────────────

/**
 * 一个真实模型在候选集编辑器里的只读事实（GET /api/admin/model-sources，契约 §1.1）。
 * 全部字段都是事实：写回只走候选集的 PUT，这里没有任何可写字段。
 */
export interface AdminModelSourceModel {
  real_model_id: string
  upstream_model_id: string
  display_name: string
  /** 渠道归属；可空 */
  channel: string | null
  context_window: number | null
  modalities: string[]
  efforts: string[]
  status: string
  excluded_from_candidates: boolean
  /** 恒等于所属来源的 protocols（模型级协议事实不存在，继承来源） */
  protocols: string[]
  /** 可展开的凭据实例数：null = 由账号池决定，0 = 无凭据支撑 */
  available_instances: number | null
}

/** 候选集编辑器左栏的一行：一个上游来源（契约 §1.1）。 */
export interface AdminModelSource {
  id: string
  kind: UpstreamKind | string
  name: string
  /** 上游行原始 name（与 name 不同时在界面上展示它做归因） */
  upstream_name: string
  provider_id: string
  region: string
  website: string
  api_doc: string
  base_url: string
  enabled: boolean
  /** 该上游下存在凭据行 */
  contributed: boolean
  /** 账号授权型（反代）来源：凭据由 sidecar 从供应商账号会话换出。左栏分组按它，不按 kind（OAuth 反代的 upstream 也是 vendor） */
  oauth: boolean
  credential_count: number
  model_count: number
  /** 端点协议事实，只允许 "openai" / "anthropic"，openai 恒在前 */
  protocols: string[]
  /** null = 数量由 sidecar 账号池决定（relay），0 = 当前无可用凭据 */
  available_instances: number | null
  last_seen_at: number | null
  /** 该上游下全部凭据行（upstream_credentials，不筛状态；契约 §1.2） */
  credentials: AdminSourceCredential[]
  /** 来源合并（2026-10-27）：聚合的成员上游行 id（每把 key 一条）；停用/启用按成员批量调用 */
  upstream_ids: string[]
  models: AdminModelSourceModel[]
}

/**
 * model-sources 里某上游的一条凭据行（契约 §1.2）。
 * 字段口径与 AdminCredential 对齐但精简：id 是 cred_<n>，时间戳为秒级 number。
 */
export interface AdminSourceCredential {
  id: string
  /** 形如 api:<目录id> 或 custom */
  provider: string
  external_label?: string
  status: string
  verify_status: string
  created_at: number | null
  last_used_at: number | null
  /** 余额展示串（如 "1,234 积分"）；空 = 从未查询或查询失败 */
  balance_text?: string
  /** 该串的落库时刻（epoch 秒） */
  balance_checked_at?: number
  /** 非零分项的压缩串（hover 明细） */
  balance_detail?: string
  /** 签到展示串（如 "+200 积分" / 失败原因）；空 = 从未尝试 */
  checkin_text?: string
  /** 最近一次能证明今天已处理的 UTC+8 日期（YYYY-MM-DD）；空 = 今天还没签上 */
  checkin_date?: string
  /** 最近一次尝试时刻（epoch 秒） */
  checkin_at?: number
}

/** GET /api/admin/model-sources 的响应体（不是 ListEnvelope：没有 data 信封）。 */
export interface AdminModelSourcesResponse {
  sources: AdminModelSource[]
  /** 全池 real_models.channel 去重升序，供渠道范围多选 */
  channels: string[]
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

/** 渠道范围约束的形状（后端 internal/app/routing_bridge.go 的 constraintJSON.ChannelScope）。 */
export interface CandidateChannelScope {
  mode: 'any' | 'include' | 'exclude' | string
  channels?: string[]
}

/**
 * 候选约束。
 *
 * 除 `channel_scope` 外的键本界面不解释、原样透传；`channel_scope` 声明为
 * unknown：历史数据里出现过字符串形状（旧界面写 `"all"`，那个形状从来没有
 * 生效过），所以读取时必须按形状分支，不能假定它一定是 `{mode,channels}`。
 */
export interface CandidateConstraint {
  channel_scope?: unknown
  /** 与 channel_scope 并列的约束键（constraintJSON 的其余字段，本界面不编辑） */
  credential_scope?: { mode?: string }
  require_modalities?: string[]
  min_context?: number
  require_efforts?: string[]
  [key: string]: unknown
}

export interface Candidate {
  real_model_id: string
  constraint?: CandidateConstraint
  weight: number
  /** 响应侧展开结果：声明式候选 → 当前可用凭据实例数（桥恒填 0，不能当事实用） */
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

/** 供应商级「可用性检验」的逐模型结果（POST /upstreams/{id}/probe-models）。 */
export interface UpstreamModelProbeRow {
  real_model_id: string
  model_id: string
  ok: boolean
  latency_ms: number
  tokens?: number
  reason?: string
}

export interface UpstreamModelProbeResult {
  upstream_id: string
  name: string
  kind: string
  ok: number
  failed: number
  skipped: number
  results: UpstreamModelProbeRow[]
}

/** 一次真实端到端探测的逐候选尝试（POST /abstract-models/{id}/connectivity）。 */
export interface ConnectivityAttempt {
  /** 落点描述：渠道/真实模型#行ID（与 v1 trace 同口径） */
  display: string
  ok: boolean
  latency_ms: number
  tokens?: number
  reason?: string
}

export interface ConnectivityResult {
  model: string
  ok: boolean
  latency_ms: number
  /** 实际消耗（max_tokens=1 的 ping；探测不计入用量统计） */
  tokens?: number
  attempts: ConnectivityAttempt[]
  /** 路由诊断（候选剔除原因；no_candidate / 全失败时的「为什么」） */
  trace?: string
  reason?: string
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

// ── 仪表盘（GET /api/usage/stats；形状对齐 internal/httpapi UsageStats）────

export interface UsageStatsTotals {
  requests: number
  failed: number
  input_tokens: number
  output_tokens: number
  /** 范围内用过的 Key 数（DISTINCT；scope=all 时是整站去重） */
  active_keys: number
  /** 平均首字延迟；范围内无样本 = null（没有样本 ≠ 0ms） */
  avg_ttft_ms: number | null
  avg_duration_ms: number | null
}

export interface UsageStatsBucket {
  /** 桶起点（RFC3339）；服务端按调用方时区切桶 */
  start: string
  requests: number
  failed: number
  input_tokens: number
  output_tokens: number
}

export interface UsageStatsModelRow {
  /** 抽象模型名；模型被删后是空串（历史行仍算数） */
  model: string
  requests: number
  failed: number
  input_tokens: number
  output_tokens: number
}

export interface UsageStats {
  from: string
  to: string
  /** 实际桶宽（秒）：3600 或 86400 */
  bucket_seconds: number
  totals: UsageStatsTotals
  /** 服务端只回有数据的桶；空窗由前端补零铺满 */
  buckets: UsageStatsBucket[]
  models: UsageStatsModelRow[]
}

export interface UsageStatsQuery {
  from?: string
  to?: string
  bucket?: 'hour' | 'day'
  /** 调用方时区相对 UTC 的秒数（东八区 = 28800），按天切桶用 */
  tz_offset?: number
  /** own（缺省）| all（仅 admin 类） */
  scope?: 'own' | 'all'
}

export interface AvailableModel {
  name: string
  display_name: string
  // 没有 capabilities 字段：租户侧拿不到可靠的能力数据（存储层无此列），
  // 后端已移除该字段而不是发一个恒空对象——不存在的字段不能靠「永远是空」来兼容。
  capabilities?: never
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

// ── 贡献入口三分类（tmp/CONTRACT-contribution-v2.md §1，接口权威）────────
//
// 旧的「平铺供应商数组」形状已废弃：它把 OAuth 供应商、API Key 型供应商与
// 一条伪条目混在一个数组里，前端只能靠 flow 猜语义。现在后端按语义分成三类，
// 每类一个 id、一组 items，items 的字段各自独立（反代有 flow/available，
// API 目录没有这些——它恒可用）。

export type ContributionCategoryId = 'reverse_proxy' | 'api' | 'custom'

/** 反代供应商：用供应商账号登录，凭据由 sidecar 换出（不接触用户的 Key）。 */
export interface ReverseProxyProvider {
  id: string
  name: string
  /**
   * 交互形态：用户在供应商侧要做什么。
   *   browser / browser_callback → 跳转到授权页
   *   device_code → 跳转后输入设备码
   *   qr_scan → 本页扫码
   *   sms → 本页填短信验证码
   */
  flow?: string
  /** 发起前需要的参数（目前只有 sms 形态要 phone）。 */
  needs?: string[]
  available: boolean
  unavailable_reason?: string
  /**
   * 不可用的**性质**：
   *   config → 我们这边缺配置（补上就能用，可操作）
   *   unsupported → 上游没有可脱离 IDE 的 OAuth 登录面（补配置也不会变）
   * 两类在界面上必须给不同措辞。
   */
  unavailable_kind?: 'config' | 'unsupported'
  /** 该家 OpenAI 兼容 base URL（仅极少数不可用条目会给，用于引导到自定义入口）。 */
  suggested_endpoint?: string
  /** 「去哪儿拿凭据」的操作步骤。 */
  steps?: string[]
}

/** 内置 API 目录里的模型条目（只收事实：模型 id）。 */
export interface ApiProviderModel {
  id: string
}

/**
 * API 供应商：内置端点 + 用户粘贴 Key。来自 `internal/apidir/catalog.json`，
 * 不依赖 sidecar，**恒可用**（所以没有 available / unavailable_reason 字段）。
 */
export interface ApiProviderEntry {
  id: string
  name: string
  base_url: string
  /** 目前目录只收 openai 协议端点（只有 anthropic 端点的供应商不进目录）。 */
  protocol: string
  vendor_family: string
  /** 主要服务区域（cn / global …），仅作展示提示。 */
  region: string
  website: string
  api_doc: string
  models: ApiProviderModel[]
}

/**
 * 三类入口之一。用 id 做判别联合，页面按 id 取到的 items 才有正确类型
 * （否则 api 项会被当成反代项去读 available，类型层就该拦下）。
 */
export type ContributionProviderCategory =
  | { id: 'reverse_proxy'; name: string; description: string; items: ReverseProxyProvider[] }
  | { id: 'api'; name: string; description: string; items: ApiProviderEntry[] }
  | { id: 'custom'; name: string; description: string; items: [] }

/** GET /api/contributions/providers 的 data。分类顺序由后端给定，页面不要重排。 */
export interface ContributionProviderCatalog {
  categories: ContributionProviderCategory[]
}
