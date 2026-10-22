// 控制台面的唯一 HTTP 出口。
//
// 红线（task-17）：
//   - API 路径只允许出现在本文件的 PATHS 表里，页面组件不得硬编码；
//   - 会话凭据是 HttpOnly Cookie（SameSite=Lax，后端签发），前端任何地方
//     都不读、不存 token——所以 fetch 必须 credentials:'include'，
//     localStorage/sessionStorage 里不允许出现任何会话痕迹；
//   - 错误体统一形状 {error:{code,message,type,param,details?}}
//     （internal/httpapi/errors.go / docs/api/03-errors.md），在此集中解析为
//     类型化 ApiError，页面只跟 code 打交道，不手写 JSON.parse。

/** 控制台面端点清单。后端新增端点只改这里。 */
export const PATHS = {
  setupStatus: '/api/setup/status',
  setupInit: '/api/setup/init',
  authLogin: '/api/auth/login',
  authLogout: '/api/auth/logout',
  authSession: '/api/auth/session'
} as const

/** 权限类（PLAN 2.2：admin/tenant/contributor 的布尔组合，不是角色）。 */
export type AccountClass = 'admin' | 'tenant' | 'contributor'

/** GET /api/setup/status 的响应。 */
export interface SetupStatus {
  setup_required: boolean
  registration_enabled: boolean
  requires_approval: boolean
}

/** GET /api/auth/session 的响应（login 响应是它的子集 + expires_at）。 */
export interface Session {
  account_id: string
  display_name: string
  classes: AccountClass[]
  acting_class?: AccountClass
  session_id?: string
  expires_at?: string
}

/** 类型化错误：code 是契约（03-errors.md），message 仅用于展示。 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly type: string
  readonly param: string | null
  /** 429 时后端给的 Retry-After（秒）；其余为 undefined。 */
  readonly retryAfterSeconds?: number

  constructor(status: number, code: string, type: string, message: string, param: string | null, retryAfter?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.type = type
    this.param = param
    this.retryAfterSeconds = retryAfter
  }
}

interface RequestOptions {
  method?: 'GET' | 'POST'
  body?: unknown
  /**
   * 401 不触发「跳登录页」的场合：会话探测本身、登录页内的请求。
   * 缺省时 401 视为会话失效，统一跳登录（HashRouter 路径）。
   */
  noAuthRedirect?: boolean
}

/**
 * 统一请求入口。非 2xx 一律抛 ApiError；2xx 返回解析后的 JSON（204/空体返回 undefined）。
 * 网络/后端不可达统一折叠成 code='network_error'，页面拿到的是同一种类型化错误。
 */
async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  let resp: Response
  try {
    resp = await fetch(path, {
      method: opts.method ?? 'GET',
      credentials: 'include', // HttpOnly Cookie 会话：必须带上，浏览器自动管理
      headers: {
        // CSRF 第二道（mw_session.go）：非 GET 请求必须带自定义头，浏览器
        // 跨站表单带不了它。同源 fetch 也一样要显式带——集中在此，页面无感。
        ...(opts.method === 'POST' ? { 'X-Requested-With': 'XMLHttpRequest' } : {}),
        ...(opts.body !== undefined ? { 'Content-Type': 'application/json' } : {})
      },
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined
    })
  } catch {
    // fetch 只在网络层失败时 reject（后端宕机、断网、CORS 拒绝）
    throw new ApiError(0, 'network_error', 'service_unavailable_error', '无法连接到服务，请稍后重试', null)
  }

  if (resp.ok) {
    if (resp.status === 204) return undefined as T
    const text = await resp.text()
    return (text ? JSON.parse(text) : undefined) as T
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
    window.location.hash = '#/login'
  }

  const retryAfter = resp.headers.get('Retry-After')
  throw new ApiError(
    resp.status,
    code,
    type,
    message,
    param,
    retryAfter !== null && !Number.isNaN(Number(retryAfter)) ? Number(retryAfter) : undefined
  )
}

// ── 各端点的类型化封装（页面只 import 这些，不直接用 request）──────────

/** GET /api/setup/status。503 not_ready（读不到库）原样抛出。 */
export function getSetupStatus(): Promise<SetupStatus> {
  return request<SetupStatus>(PATHS.setupStatus, { noAuthRedirect: true })
}

/** POST /api/setup/init。201 成功（后端刻意不发 Cookie：向导后正常登录一次）。 */
export function postSetupInit(body: { username: string; display_name: string; password: string }): Promise<{ ok: boolean; message: string }> {
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
