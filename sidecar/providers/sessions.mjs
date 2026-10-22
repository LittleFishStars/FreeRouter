// sessions.mjs —— OAuth 授权会话簿记（进程内，带 TTL 与后台轮询）。
//
// 为什么需要它：上游 15 家里有 6 家是**阻塞式轮询**（`pollXxx` 内部 while 循环，
// 直到用户完成授权才返回）。若直接把这种调用挂在 sidecar 的一问一答通道上，
// 用户在浏览器里点两分钟，主进程的 RPC 就卡两分钟——而 sidecar 的 Call 超时
// 只有 10 秒（04-sidecar.md §5.3）。
//
// 所以把「轮询」变成**后台任务**：
//   oauth.authorize / oauth.start → 立刻返回授权材料（URL / user_code / 二维码）
//   oauth.poll                    → 查一次会话状态（非阻塞，毫秒级返回）
//   oauth.cancel                  → 取消并释放
//
// 会话本身带 TTL 自清理（默认 10 分钟），超时后后台轮询收到 abort。

import process from 'node:process'

/** 会话默认寿命：10 分钟（与主进程侧 oauthStateTTL 同源）。 */
const SESSION_TTL_MS = (() => {
  const v = Number(process.env.FR_OAUTH_SESSION_TTL_SEC || 600)
  return Number.isFinite(v) && v > 0 ? v * 1000 : 600_000
})()

/** 会话表：nonce → session。 */
const sessions = new Map()
let seq = 0

/**
 * 新建一条会话。
 *
 * entry:
 *   provider   供应商 id
 *   flow       'browser' | 'device_code' | 'sms' | 'qr'
 *   material   返回给主进程的授权材料（URL / user_code / qr_image_url…）
 *   runner     async ({ signal, report }) => credential —— 后台轮询任务
 *              report({ status, message }) 可上报进度（如 sms 等验证码）
 *   ttlMs      可选，覆盖默认
 */
export function create(entry) {
  const id = `o${Date.now().toString(36)}-${++seq}`
  const controller = new AbortController()
  const ttlMs = Number.isFinite(entry.ttlMs) && entry.ttlMs > 0 ? entry.ttlMs : SESSION_TTL_MS
  const session = {
    id,
    provider: entry.provider,
    flow: entry.flow,
    status: 'pending',
    message: entry.message ?? null,
    material: entry.material ?? {},
    credential: null,
    error: null,
    createdAt: Date.now(),
    expiresAt: new Date(Date.now() + ttlMs).toISOString(),
    controller,
  }
  sessions.set(id, session)

  if (typeof entry.runner === 'function') {
    const report = (patch) => {
      const s = sessions.get(id)
      if (!s) return
      if (typeof patch?.status === 'string') s.status = patch.status
      if (typeof patch?.message === 'string') s.message = patch.message
    }
    void (async () => {
      try {
        const credential = await entry.runner({ signal: controller.signal, report })
        const s = sessions.get(id)
        if (!s || s.status === 'cancelled') return
        s.credential = credential
        s.status = 'ok'
        s.message = null
      } catch (e) {
        const s = sessions.get(id)
        if (!s || s.status === 'cancelled') return
        s.status = controller.signal.aborted ? 'expired' : 'failed'
        s.error = String(e && e.message ? e.message : e)
      }
    })()
  }

  // 会话不常驻：定时器只负责把过期的 abort 掉（后台任务自己也会看 signal）。
  const timer = setTimeout(() => {
    const s = sessions.get(id)
    if (s && s.status === 'pending') {
      s.status = 'expired'
      s.error = '授权会话已超时，请重新发起'
      controller.abort()
    }
    sessions.delete(id)
  }, ttlMs + 5_000)
  timer.unref?.()

  return session
}

/** 取会话（不存在返回 undefined）。 */
export function get(id) {
  const s = sessions.get(id)
  if (!s) return undefined
  if (Date.now() > Date.parse(s.expiresAt) && s.status === 'pending') {
    s.status = 'expired'
    s.error = '授权会话已超时，请重新发起'
    s.controller.abort()
  }
  return s
}

/** 取消并释放（幂等：不存在也算成功）。 */
export function cancel(id) {
  const s = sessions.get(id)
  if (!s) return
  s.status = 'cancelled'
  s.controller.abort()
  sessions.delete(id)
}

/** 会话的对外快照（不含内部字段）。 */
export function snapshot(s) {
  return {
    session_id: s.id,
    provider: s.provider,
    flow: s.flow,
    status: s.status,
    message: s.message,
    ...s.material,
    credential: s.credential ? JSON.stringify(s.credential) : null,
    expires_at: s.expiresAt,
  }
}

/** 上报会话进度（供 sms 等「用户还要输入验证码」的流程使用）。 */
export function report(id, patch) {
  const s = sessions.get(id)
  if (!s) return
  if (typeof patch?.status === 'string') s.status = patch.status
  if (typeof patch?.message === 'string') s.message = patch.message
}

/** 给已存在的会话补一份 runner（sms 流程：先发码，用户回填验证码后才开始登录）。 */
export function attachRunner(id, runner) {
  const s = sessions.get(id)
  if (!s) return
  void (async () => {
    try {
      const credential = await runner({ signal: s.controller.signal, report: (p) => report(id, p) })
      if (s.status === 'cancelled') return
      s.credential = credential
      s.status = 'ok'
      s.message = null
    } catch (e) {
      if (s.status === 'cancelled') return
      s.status = s.controller.signal.aborted ? 'expired' : 'failed'
      s.error = String(e && e.message ? e.message : e)
    }
  })()
}
