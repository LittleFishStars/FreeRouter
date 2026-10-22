// 控制台面的唯一 HTTP 出口。
//
// 红线：
//   - API 路径只允许出现在本文件的 PATHS 表里，页面组件不得硬编码；
//   - 会话凭据是 HttpOnly Cookie（SameSite=Lax，后端签发），前端任何地方
//     都不读、不存 token——所以 fetch 必须 credentials:'include'，
//     localStorage/sessionStorage 里不允许出现任何会话痕迹；
//   - 错误体统一形状 {error:{code,message,type,param,details?}}
//     （internal/httpapi/errors.go / docs/api/03-errors.md），在此集中解析为
//     类型化 ApiError，页面只跟 code 打交道，不手写 JSON.parse。
//
// 方法语义（docs/api/02-admin.md 3.1）：GET 查询 / POST 创建或触发 /
// PATCH 局部更新 / PUT 整体替换（组的模型授权、策略、候选集）/ DELETE 仅
// 用于可逆移除（移出组成员）。写请求统一带 X-Requested-With 自定义头，
// 这是 mw_session 的 CSRF 第二道，浏览器跨站表单带不了它。

import type {
  ListEnvelope,
  Session,
  SetupStatus,
  WriteEnvelope
} from './types'

/** 控制台面端点清单。后端新增端点只改这里。 */
export const PATHS = {
  // 会话与初始化
  setupStatus: '/api/setup/status',
  setupInit: '/api/setup/init',
  authLogin: '/api/auth/login',
  authLogout: '/api/auth/logout',
  authSession: '/api/auth/session',
  // 账户自助（端点尚未在 docs/api 定稿，按 REST 形状暂定）
  accountPassword: '/api/account/password',
  accountSessions: '/api/account/sessions',
  // 注册 / 待审核（同上：审批流后端未交付，界面按契约先行）
  register: '/api/register',
  pendingStatus: '/api/account/status',
  // 贡献（公共区，未登录可用）
  contributions: '/api/contributions',
  contributeRecovery: '/api/contributions/recovery',
  contributeProviders: '/api/contributions/providers',
  // 租户面（端点未定稿）
  // 租户 Key 面：路径与后端一致（/api/account/keys，会话面）。
  keys: '/api/account/keys',
  usageOverview: '/api/usage/overview',
  usageLogs: '/api/usage/logs',
  usageModels: '/api/usage/models',
  // 管理面（docs/api/02-admin.md）
  adminAccounts: '/api/admin/accounts',
  adminAccountsPending: '/api/admin/accounts/pending',
  adminAccountsImport: '/api/admin/accounts/import',
  adminGroups: '/api/admin/groups',
  adminRealModels: '/api/admin/real-models',
  adminAbstractModels: '/api/admin/abstract-models',
  adminUpstreams: '/api/admin/upstreams',
  adminCredentials: '/api/admin/credentials',
  adminVerifications: '/api/admin/verifications',
  adminAudit: '/api/admin/audit',
  adminSettings: '/api/admin/settings',
  adminInbox: '/api/admin/inbox',
  adminInboxCount: '/api/admin/inbox/count',
  // 贡献者面（端点未定稿）
  myContributions: '/api/contributions',
  myCredentials: '/api/contributions/credentials',
  contributionStats: '/api/contributions/stats',
  recoveryCode: '/api/contributions/recovery-code',
  // 资产加载器
  healthz: '/healthz',
  readyz: '/readyz'
} as const

/** 权限类（PLAN 2.2：admin/tenant/contributor 的布尔组合，不是角色）。 */
export type { AccountClass } from './types'

// 供旧调用点继续从 client 导入会话/初始化类型（页面新代码可直接 import types）。
export type { ListEnvelope, Session, SetupStatus, WriteEnvelope } from './types'

/** 类型化错误：code 是契约（docs/api/03-errors.md），message 仅用于展示。 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly type: string
  readonly param: string | null
  /** 429 时后端给的 Retry-After（秒）；其余为 undefined。 */
  readonly retryAfterSeconds?: number
  /** x-free-router-request-id：错误态必须展示它（docs/ui/06-design.md 4） */
  readonly requestId?: string

  constructor(
    status: number,
    code: string,
    type: string,
    message: string,
    param: string | null,
    retryAfter?: number,
    requestId?: string
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.type = type
    this.param = param
    this.retryAfterSeconds = retryAfter
    this.requestId = requestId
  }
}

// 401 的兜底动作由路由层注入：直接改 window.location.hash 会绕过
// react-router 的 history 状态，也会丢掉守卫记录的 from 回跳目标。
let unauthorizedHandler: (() => void) | null = null

/** 由 SessionProvider 在挂载时注入（navigate('/login',{state:{from}})`）。 */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  unauthorizedHandler = fn
}

/** 判定「接口尚未接入」：后端未实现时一律 404 not_found（02-admin §2.2 口径）。 */
export function isUnavailable(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404 && err.code === 'not_found'
}

type Method = 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'

interface RequestOptions {
  method?: Method
  body?: unknown
  /** 查询参数：undefined / null / '' 的键会被跳过。 */
  query?: Record<string, string | number | boolean | null | undefined>
  /**
   * 401 不触发「跳登录页」的场合：会话探测本身、登录页内的请求。
   * 缺省时 401 视为会话失效，统一跳登录（HashRouter 路径）。
   */
  noAuthRedirect?: boolean
  /** 返回原始响应（下载导出用）。 */
  raw?: boolean
}

function buildURL(path: string, query?: RequestOptions['query']): string {
  if (!query) return path
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue
    params.set(k, String(v))
  }
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

/**
 * 统一请求入口。非 2xx 一律抛 ApiError；2xx 返回解析后的 JSON（204/空体返回 undefined）。
 * 网络/后端不可达统一折叠成 code='network_error'，页面拿到的是同一种类型化错误。
 */
async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? 'GET'
  let resp: Response
  try {
    resp = await fetch(buildURL(path, opts.query), {
      method,
      credentials: 'include', // HttpOnly Cookie 会话：必须带上，浏览器自动管理
      headers: {
        // CSRF 第二道（mw_session.go）：所有写请求必须带自定义头，浏览器
        // 跨站表单带不了它。同源 fetch 也一样要显式带——集中在此，页面无感。
        ...(method !== 'GET' ? { 'X-Requested-With': 'XMLHttpRequest' } : {}),
        ...(opts.body !== undefined ? { 'Content-Type': 'application/json' } : {})
      },
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined
    })
  } catch {
    // fetch 只在网络层失败时 reject（后端宕机、断网、CORS 拒绝）
    throw new ApiError(0, 'network_error', 'service_unavailable_error', '无法连接到服务，请稍后重试', null)
  }

  if (resp.ok) {
    if (opts.raw) return (await resp.blob()) as unknown as T
    if (resp.status === 204) return undefined as T
    const text = await resp.text()
    if (!text) return undefined as T
    try {
      return JSON.parse(text) as T
    } catch {
      // 200 但体不是 JSON（反代注入/截断）：折叠成同一套类型化错误，
      // 页面不必面对原生 SyntaxError（03-errors.md 的统一错误面）。
      throw new ApiError(resp.status, 'unknown_error', 'internal_error', '服务返回了无法解析的响应', null)
    }
  }

  // 错误体解析：形状不对（网关/代理拦截页）也不崩，折叠成 unknown_error
  let code = 'unknown_error'
  let type = 'internal_error'
  let message = `请求失败（HTTP ${resp.status}）`
  let param: string | null = null
  try {
    const payload = await resp.json()
    if (payload && typeof payload === 'object' && payload.error) {
      code = String(payload.error.code ?? code)
      type = String(payload.error.type ?? type)
      message = String(payload.error.message ?? message)
      param = payload.error.param ?? null
    }
  } catch {
    /* 保底文案已设置 */
  }

  // 会话失效统一跳登录（防枚举原则的界面侧：401 一律视为「该重新登录了」，
  // 不区分「没登录过」「过期」「被吊销」）。
  if (resp.status === 401 && !opts.noAuthRedirect) {
    if (unauthorizedHandler) unauthorizedHandler()
    else window.location.hash = '#/login'
  }

  const retryAfter = resp.headers.get('Retry-After')
  throw new ApiError(
    resp.status,
    code,
    type,
    message,
    param,
    retryAfter !== null && !Number.isNaN(Number(retryAfter)) ? Number(retryAfter) : undefined,
    resp.headers.get('x-free-router-request-id') ?? undefined
  )
}

// ── 通用信封辅助（页面只 import 这些，不直接用 request）─────────────────

/** GET 列表：返回 data 数组与翻页信息（docs/api/02-admin.md 3.1）。 */
export function getList<T>(
  path: string,
  query?: RequestOptions['query']
): Promise<ListEnvelope<T>> {
  return request<ListEnvelope<T>>(path, { query })
}

/** GET 单条：/{id}。后端统一包 {data:...}（docs/api/02-admin.md 3.1），这里拆封。 */
export async function getOne<T>(path: string): Promise<T> {
  const env = await request<{ data: T }>(path)
  return env.data
}

/** GET 但不拆封：有些端点在 data 旁边带附加字段（如抽象模型详情的 candidates）。 */
export function getEnvelope<T>(path: string): Promise<T> {
  return request<T>(path)
}

/** POST 写：返回 {data, audit_id}。 */
export function postWrite<T>(path: string, body?: unknown): Promise<WriteEnvelope<T>> {
  return request<WriteEnvelope<T>>(path, { method: 'POST', body })
}

/** PATCH 局部更新。 */
export function patchWrite<T>(path: string, body?: unknown): Promise<WriteEnvelope<T>> {
  return request<WriteEnvelope<T>>(path, { method: 'PATCH', body })
}

/** PUT 整体替换。 */
export function putWrite<T>(path: string, body?: unknown): Promise<WriteEnvelope<T>> {
  return request<WriteEnvelope<T>>(path, { method: 'PUT', body })
}

/** DELETE 可逆移除。 */
export function deleteWrite<T>(path: string): Promise<WriteEnvelope<T>> {
  return request<WriteEnvelope<T>>(path, { method: 'DELETE' })
}

/** 原始下载（审计导出等）：返回 Blob，由页面触发下载。 */
export function getBlob(path: string, query?: RequestOptions['query']): Promise<Blob> {
  return request<Blob>(path, { query, raw: true })
}

// ── 各端点的类型化封装（页面只 import 这些）───────────────────────────

/** GET /api/setup/status。503 not_ready（读不到库）原样抛出。 */
export function getSetupStatus(): Promise<SetupStatus> {
  return request<SetupStatus>(PATHS.setupStatus, { noAuthRedirect: true })
}

/** POST /api/setup/init。201 成功（后端刻意不发 Cookie：向导后正常登录一次）。 */
export function postSetupInit(body: {
  username: string
  display_name: string
  password: string
}): Promise<{ ok: boolean; message: string }> {
  return request(PATHS.setupInit, { method: 'POST', body, noAuthRedirect: true })
}

/** POST /api/auth/login。成功响应即会话主体（Cookie 已由浏览器落盘）。 */
export function postAuthLogin(body: { username: string; password: string }): Promise<Session> {
  return request<Session>(PATHS.authLogin, { method: 'POST', body, noAuthRedirect: true })
}

/** POST /api/auth/logout。幂等（后端会话已不存在也 200）。 */
export function postAuthLogout(): Promise<void> {
  return request<void>(PATHS.authLogout, { method: 'POST', noAuthRedirect: true })
}

/**
 * GET /api/auth/session。401（未登录/过期）返回 null 而不是抛错——
 * 「有没有会话」是正常业务状态，不是异常；其余错误照常抛。
 */
export async function getAuthSession(): Promise<Session | null> {
  try {
    return await request<Session>(PATHS.authSession, { noAuthRedirect: true })
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return null
    throw e
  }
}
