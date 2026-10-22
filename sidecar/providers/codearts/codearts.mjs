// codearts.mjs —— CodeArts 供应商贡献的 sidecar 方法实现（OAuth 授权 + 续期）。
//
// 设计（对齐 docs/contributions/01-flow.md 与 PLAN §5.5「登录/凭据续期必须
// 复用 providers-hub 的实现」）：本文件把上游仓库（deepseek-harness-codearts，
// Apache-2.0）的 OAuth/PKCE/DPoP/签名实现提取为**无状态函数**，由 Go 侧按
// NDJSON 方法调用。凭据 JSON 的持久化在 Go 侧（secret 密封存储），本模块
// 不落盘、不读 DSH 状态（三条红线不变）。
//
// 方法面（在 sidecar/index.mjs 的 METHODS 表注册）：
//   oauth.codearts.authorize { port }            → { authorize_url, ticket_id, state }
//   oauth.codearts.exchange  { code, state }     → { credential, expires_at }
//   oauth.codearts.refresh   { credential }      → { credential, expires_at }

import {
  buildAuthorizeUrl,
  credentialFromTokenResponse,
  exchangeAuthorizationCode,
  exchangeRefreshToken,
  generateDpopKeyPair,
  generatePkcePair,
  keyPairFromStoredJwk,
  RefreshTokenExpiredError,
  refreshTokenExpiresAt,
} from './oauth.mjs'

/** 生成一次授权会话的全部材料（Go 侧持久化 state，5 分钟 TTL）。 */
export async function authorize({ port }) {
  const p = Number(port)
  if (!Number.isInteger(p) || p <= 0 || p > 65535) {
    throw new Error('E_BAD_REQUEST: port 必须是 1-65535 的回环监听端口')
  }
  const pkce = generatePkcePair()
  const dpop = await generateDpopKeyPair()
  const ticketId = randomTicket()
  const authorizeUrl = buildAuthorizeUrl(p, pkce, ticketId)
  return {
    authorize_url: authorizeUrl,
    ticket_id: ticketId,
    // state 是 Go 侧回调校验与换取的完整材料（含机密：verifier/私钥），
    // 只回传不落盘；Go 侧加密保管并在回调时原样送回。
    state: {
      provider: 'codearts',
      ticket_id: ticketId,
      port: p,
      code_verifier: pkce.codeVerifier,
      dpop_private_key_jwk: dpop.privateKeyJwk,
      created_at: Date.now(),
    },
  }
}

/** 用回调 code 换取凭据（Go 侧校验 state 后调用）。 */
export async function exchange({ code, state }) {
  const st = assertState(state)
  const codeStr = String(code ?? '').trim()
  if (!codeStr) throw new Error('E_BAD_REQUEST: code 不能为空')
  const keyPair = keyPairFromStoredJwk(st.dpop_private_key_jwk)
  const token = await exchangeAuthorizationCode(codeStr, st.code_verifier, st.port, keyPair)
  const credential = credentialFromTokenResponse(token, { codeVerifier: st.code_verifier }, keyPair)
  return { credential: JSON.stringify(credential), expires_at: credential.expires_at ?? '' }
}

/** 用 refresh_token 静默续期（调度器调用；RefreshTokenExpiredError 原样上抛）。 */
export async function refresh({ credential }) {
  const cred = typeof credential === 'string' ? JSON.parse(credential) : credential
  if (!cred?.refresh_token || !cred?.code_verifier || !cred?.dpop_private_key_jwk) {
    throw new Error('E_BAD_REQUEST: 凭据缺少 refresh_token / code_verifier / dpop_private_key_jwk（旧 ticket 凭据不可静默刷新）')
  }
  const keyPair = keyPairFromStoredJwk(cred.dpop_private_key_jwk)
  try {
    const token = await exchangeRefreshToken(cred.refresh_token, cred.code_verifier, keyPair)
    const next = credentialFromTokenResponse(token, { codeVerifier: cred.code_verifier }, keyPair)
    return { credential: JSON.stringify(next), expires_at: next.expires_at ?? '' }
  } catch (e) {
    if (e instanceof RefreshTokenExpiredError) throw e
    throw e
  }
}

/** 凭据健康检查：access 过期而 refresh 仍有效 → true（应续期）。 */
export async function needsRefresh({ credential }) {
  const cred = typeof credential === 'string' ? JSON.parse(credential) : credential
  const accessExp = readExpiresAtMs(cred?.expires_at)
  const refreshExp = refreshTokenExpiresAt(cred ?? {})
  const now = Date.now()
  if (accessExp !== undefined && accessExp > now) return false
  if (refreshExp !== undefined) return refreshExp > now
  return false
}

function readExpiresAtMs(expiresAt) {
  if (typeof expiresAt !== 'string' || expiresAt === '') return undefined
  const ms = Date.parse(expiresAt)
  return Number.isFinite(ms) ? ms : undefined
}

function assertState(state) {
  const st = typeof state === 'string' ? JSON.parse(state) : state
  if (!st || st.provider !== 'codearts' || !st.code_verifier || !st.dpop_private_key_jwk) {
    throw new Error('E_BAD_REQUEST: state 形状非法或已失效')
  }
  if (typeof st.created_at === 'number' && Date.now() - st.created_at > 10 * 60_000) {
    throw new Error('E_BAD_REQUEST: 授权会话已超时，请重新发起')
  }
  return st
}

function randomTicket() {
  return randomId(16)
}

function randomId(bytes) {
  const raw = new Uint8Array(bytes)
  crypto.getRandomValues(raw)
  return Array.from(raw).map((b) => b.toString(16).padStart(2, '0')).join('')
}
