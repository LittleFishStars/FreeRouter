// oauth.mjs —— CodeArts 供应商 OAuth（PKCE + DPoP + STS token 换取）。
//
// 来源：deepseek-harness-codearts src/oauth.ts + src/login.ts（Apache-2.0，
// gitee.com/iJetLi/deepseek-harness-codearts），按贡献链路提取计划移植；
// 常量与请求形状逐项对齐真实插件（逆向常量勿改）。
//
// 与上游的唯一实现差异：**不引入 jose**。上游用 jose 生成/导入 ES256 密钥并签
// DPoP JWS；本模块用 node:crypto 直接做同一件事（P-256 + ES256 的 JWS 编码是
// 固定的 Base64URL(header).Base64URL(payload) + R||S 原始签名）。少一个依赖，
// sidecar 就能在"只有 Node、没有其它 npm 包"的部署里工作。
//
// 设计（与桌面插件相比）：本模块**不持有状态**、不打开浏览器——授权 URL 由
// Go 主进程下发，回调由 Go 侧路由承接；换取/刷新以无状态函数暴露。

import {
  createHash,
  createPrivateKey,
  generateKeyPairSync,
  randomBytes,
  sign as cryptoSign,
} from 'node:crypto'

/** CodeArts Agent 的 OAuth client_id（即其 URI scheme，来自 product.json）。 */
export const CLIENT_ID = 'codearts-agent'
/** 本地回调路径（对齐真实插件的 AUTH_REDIRECT_URL）。 */
export const REDIRECT_PATH = '/oauth/callback'
/** 华为 STS token 端点（对齐真实插件的 IAM_TOKEN_API）。 */
export const STS_TOKEN_ENDPOINT = 'https://sts.cn-north-4.myhuaweicloud.com/v1/oauth2/tokens'
/** token 请求超时（与真实插件一致）。 */
export const TOKEN_TIMEOUT_MS = 60_000
export const GRANT_AUTHORIZATION_CODE = 'authorization_code'
export const GRANT_REFRESH_TOKEN = 'refresh_token'

/** 新式 IAM OAuth 的 portal 授权端点（对齐真实插件 getPortalHost + /authorize）。 */
export const PORTAL_AUTHORIZE_BASE = 'https://codearts.huaweicloud.com/portal/authorize'
/** portal 期望的插件名/版本（逆向常量，硬编码为真实扩展版本）。 */
export const LOGIN_PLUGIN_NAME = 'snap_AIIDE'
export const LOGIN_PLUGIN_VERSION = '5.2.0'
export const OAUTH_THEME = '2'
export const OAUTH_LOCALE = 'zh-cn'

/** PKCE 配对：验证器 + S256 挑战。 */
export function generatePkcePair() {
  const codeVerifier = randomBytes(48).toString('base64url')
  const codeChallenge = createHash('sha256').update(codeVerifier).digest('base64url')
  return { codeVerifier, codeChallenge }
}

/**
 * 生成 ES256（P-256）DPoP 密钥对（JWK 形式）。
 *
 * JWK 形状与上游一致：私钥含 d，公钥含 kty/crv/x/y。只存私钥 JWK 即可
 * 重建两者（x/y 从私钥 JWK 读出，见 keyPairFromStoredJwk）。
 */
export function generateDpopKeyPair() {
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' })
  const pub = publicKey.export({ format: 'jwk' })
  const priv = privateKey.export({ format: 'jwk' })
  const privateKeyJwk = { kty: 'EC', crv: 'P-256', x: pub.x, y: pub.y, d: priv.d }
  const publicKeyJwk = { kty: 'EC', crv: 'P-256', x: pub.x, y: pub.y }
  return { privateKeyJwk, publicKeyJwk }
}

/**
 * 用私钥 JWK 签发 dpop+jwt（htm=HTTP 方法，htu=完整 URL）。
 *
 * ES256 签名的字节序是 R||S（各 32 字节），**不是** DER——node:crypto 默认给 DER，
 * 所以这里用 dsaEncoding: 'ieee-p1363' 让 Node 直接输出 JWS 需要的原始形态。
 * 这是把 jose 换掉时唯一真正容易写错的地方。
 */
export function signDpopJws(keyPair, htm, htu) {
  const key = privateKeyFromJwk(keyPair.privateKeyJwk)
  const header = { alg: 'ES256', typ: 'dpop+jwt', jwk: keyPair.publicKeyJwk }
  const payload = {
    htm,
    htu,
    iat: Math.floor(Date.now() / 1000),
    jti: randomBytes(32).toString('hex'),
  }
  const signingInput = `${b64u(JSON.stringify(header))}.${b64u(JSON.stringify(payload))}`
  const signature = cryptoSign('sha256', Buffer.from(signingInput), { key, dsaEncoding: 'ieee-p1363' })
  return `${signingInput}.${signature.toString('base64url')}`
}

/** 从私钥 JWK 构造 node KeyObject（P-256）。 */
function privateKeyFromJwk(jwk) {
  // node:crypto.createPrivateKey 支持 JWK 输入（Node ≥ 15.12）。
  return createPrivateKey({ key: { kty: 'EC', crv: 'P-256', x: jwk.x, y: jwk.y, d: jwk.d }, format: 'jwk' })
}

/** b64u：Base64URL 无填充（JWS 段编码）。 */
function b64u(input) {
  return Buffer.from(input).toString('base64url')
}

/**
 * 从 JWT 载荷里读 `exp` / `iat`（秒级 → 毫秒）。
 *
 * ⚠️ **不解签名**：只用于「本地估算还剩多久」的调度判据；解析失败一律
 * 返回 `undefined`，调用方必须按「读不到」处理（不得 fail-closed 成「已过期」）。
 */
export function readJwtTime(token, claim) {
  const parts = token.split('.')
  if (parts.length !== 3) return undefined
  const payload = parts[1]
  if (payload === undefined || payload.length === 0) return undefined
  try {
    const json = Buffer.from(payload, 'base64url').toString('utf8')
    const value = JSON.parse(json)[claim]
    if (typeof value !== 'number' || !Number.isFinite(value)) return undefined
    const ms = value * 1000
    return Math.abs(ms) > 1e14 ? undefined : ms
  } catch {
    return undefined
  }
}

/** refresh_token 的到期时刻（毫秒）；读不到返回 `undefined`。 */
export function refreshTokenExpiresAt(credential) {
  const token = credential?.refresh_token
  if (typeof token !== 'string' || token.length === 0) return undefined
  return readJwtTime(token, 'exp')
}

/** refresh_token 已失效/被拒绝时抛出；调度器据此停止续期。 */
export class RefreshTokenExpiredError extends Error {
  constructor(message) {
    super(message)
    this.name = 'RefreshTokenExpiredError'
  }
}

/** 向 STS token 端点发起一次带 DPoP 的 token 请求。 */
export async function requestToken(body, keyPair, fetcher = fetch) {
  const dpop = signDpopJws(keyPair, 'POST', STS_TOKEN_ENDPOINT)
  let response
  try {
    response = await fetcher(STS_TOKEN_ENDPOINT, {
      method: 'POST',
      headers: {
        DPoP: dpop,
        'Content-Type': 'application/x-www-form-urlencoded',
      },
      body: new URLSearchParams(body).toString(),
      signal: AbortSignal.timeout(TOKEN_TIMEOUT_MS),
    })
  } catch (error) {
    throw new Error(`CodeArts token request network error: ${String(error)}`)
  }
  let data = null
  try {
    data = await response.json()
  } catch {
    data = null
  }
  if (!response.ok || !data?.credentials) {
    const message = `CodeArts token request failed: ${response.status}${data ? ` ${JSON.stringify(data)}` : ''}`
    // 终态判定只认 refresh_token 自己失效的信号（invalid_grant / ExpiredRefreshToken）。
    // InvalidDPoPHeader 是请求级拒绝（时钟偏差/重放判定/网关抖动），不当终态——
    // 上游的缺陷史见 providers-codearts.md：误当终态会把完好账号一步标死。
    const errorCode = String(data?.error_code ?? '')
    if (data?.error === 'invalid_grant' || errorCode.includes('ExpiredRefreshToken')) {
      throw new RefreshTokenExpiredError(message)
    }
    throw new Error(message)
  }
  return data
}

/**
 * 构建 portal 授权 URL（参数完全对齐真实插件 buildLoginUrl）。
 *
 * `port` 必须是**回环侧可回调的端口**：portal 仅凭 port 参数构造
 * 127.0.0.1:`${port}` 的回调，因此本流程只对「服务监听在 127.0.0.1」的部署成立
 * （FreeRouter 默认形态）。
 */
export function buildAuthorizeUrl(port, pkce, ticketId) {
  return `${PORTAL_AUTHORIZE_BASE}?theme=${OAUTH_THEME}&locale=${OAUTH_LOCALE}`
    + `&uri_scheme=${CLIENT_ID}&client_id=${CLIENT_ID}&port=${port}`
    // code_challenge_method 对齐真实插件 PKCEGenerator.CODE_CHALLENGE_METHOD = "SHA-256"
    // （非 RFC 缩写 S256；portal 以此识别 OAuth 授权，错值会回退旧 ticket 流程）。
    + `&code_challenge=${pkce.codeChallenge}&code_challenge_method=SHA-256`
    // 真实插件 URL 不含 auth_callback_url——portal 仅凭 port 构造回调，
    // 多余参数会被 portal 视为异常并回退旧流程，勿添加。
    + `&ticket_id=${ticketId}&plugin-name=${LOGIN_PLUGIN_NAME}&plugin-version=${LOGIN_PLUGIN_VERSION}`
}

/** 授权码换取（回调收到 code 后调用）。 */
export async function exchangeAuthorizationCode(code, codeVerifier, port, keyPair, fetcher = fetch) {
  return requestToken({
    client_id: CLIENT_ID,
    code,
    code_verifier: codeVerifier,
    grant_type: GRANT_AUTHORIZATION_CODE,
    redirect_uri: `http://127.0.0.1:${port}${REDIRECT_PATH}`,
  }, keyPair, fetcher)
}

/** 刷新令牌换取（静默续期）。 */
export async function exchangeRefreshToken(refreshToken, codeVerifier, keyPair, fetcher = fetch) {
  return requestToken({
    client_id: CLIENT_ID,
    code_verifier: codeVerifier,
    grant_type: GRANT_REFRESH_TOKEN,
    refresh_token: refreshToken,
  }, keyPair, fetcher)
}

/** 将 token 响应组装为持久化凭据 JSON（含刷新所需字段）。 */
export function credentialFromTokenResponse(token, pkce, keyPair) {
  const credentials = token.credentials ?? {}
  return {
    access_key_id: credentials.access_key_id ?? '',
    secret_access_key: credentials.secret_access_key ?? '',
    security_token: credentials.security_token ?? '',
    expires_at: credentials.expiration ?? '',
    refresh_token: token.refresh_token,
    code_verifier: pkce.codeVerifier,
    dpop_private_key_jwk: keyPair.privateKeyJwk,
  }
}

/** 从持久化的私钥 JWK 恢复 DPoP 密钥对。 */
export function keyPairFromStoredJwk(jwk) {
  return {
    privateKeyJwk: jwk,
    publicKeyJwk: { kty: jwk.kty, crv: jwk.crv, x: jwk.x, y: jwk.y },
  }
}
