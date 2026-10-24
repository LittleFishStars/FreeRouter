// registry.mjs —— 供应商 OAuth 的注册表（15 家，三种交互形态）。
//
// ## 为什么要有注册表
//
// 上游 15 家的实现差异很大（PKCE / 设备码 / 短信 / 二维码 / 本机回环服务），
// 但**对主进程暴露的形状只有三种**：
//
//   1. browser      给 authorize_url，用户浏览器登录，code 回传到我们的回调
//   2. device_code  给 user_code + verification_uri，后台轮询直到授权完成
//   3. sms / qr     给手机号发码 / 出二维码，用户回填或扫码后后台轮询
//
// 注册表把每家上游的差异收敛成这三种之一。sidecar 的 oauth.* 方法只跟注册表
// 打交道，主进程也只认这三个形状。
//
// ## 关键设计：轮询放后台（见 sessions.mjs）
//
// 上游的 `pollXxx` / `startXxxFlow` 里带 `while` 循环（阻塞到授权完成）。直接
// 调它会卡住 sidecar 的 RPC（超时 10 秒）。所以这里一律用 `sessions.create({runner})`
// 启动后台任务，`oauth.poll` 只查状态。
//
// ## 纪律
//
// 适配器只做**翻译**。URL 参数、PKCE、签名、轮询节奏全部留在上游移植文件里
// ——那些是逆向得到的事实，改动会静默失效。

import * as accio from './accio/accio-oauth.js'
import * as cline from './cline/cline-oauth.js'
import * as gemini from './gemini/gemini-oauth.js'
import * as lobsterai from './lobsterai/lobsterai-oauth.js'
import * as loomy from './loomy/loomy-oauth.js'
import * as minimax from './minimax/minimax-oauth.js'
import * as qoder from './qoder/qoder-oauth.js'
import * as raccoon from './raccoon/raccoon-oauth.js'
import * as raccoonLib from './raccoon/raccoon.js'
import * as trae from './trae/trae-oauth.js'
import { TRAE } from './trae/trae-product.js'
import { generateDeviceId, generateMachineId } from './trae/trae.js'
import * as autoclaw from './autoclaw/autoclaw-oauth.js'
import * as officeace from './officeace/officeace-auth.js'
import * as codearts from './codearts/codearts.mjs'
import { CLINE } from './cline/cline-product.js'
import { MINIMAX } from './minimax/minimax-product.js'
import * as sessions from './sessions.mjs'
import { LOBSTERAI } from './lobsterai/lobsterai-product.js'
import * as zcode from './zcode/zcode-auth.js'
import { createMemoryContext } from './_shims/ctx-shim.mjs'
import { QODER } from './qoder/qoder-product.js'
import { LOOMY } from './loomy/loomy-product.js'
import { randomBytes } from 'node:crypto'

/** 回调路径：所有 browser 形态共用（供应商按端口 + 固定路径回调）。 */
export const CALLBACK_PATH = '/oauth/callback'

/**
 * 各 provider 的落库参数（主进程用它决定上游行怎么写）。
 *
 * apiBase  推理端点根（vendor 形态；CodeArts 走 snap-access）
 * kind     上游类型：vendor = 公网 https；selfhosted = 本机
 */
const ENDPOINTS = {
  codearts: { apiBase: 'https://snap-access.cn-north-4.myhuaweicloud.com', kind: 'vendor' },
  gemini: { apiBase: 'https://cloudcode-pa.googleapis.com', kind: 'vendor' },
  accio: { apiBase: 'https://api.accio.ai', kind: 'vendor' },
  minimax: { apiBase: 'https://api.minimax.chat', kind: 'vendor' },
  cline: { apiBase: 'https://api.cline.bot', kind: 'vendor' },
  qoder: { apiBase: 'https://api.qoder.com', kind: 'vendor' },
  loomy: { apiBase: 'https://api.loomy.ai', kind: 'vendor' },
  raccoon: { apiBase: 'https://api.raccoon.ai', kind: 'vendor' },
  lobsterai: { apiBase: 'https://api.lobsterai.com', kind: 'vendor' },
  autoclaw: { apiBase: 'https://api.autoclaw.ai', kind: 'vendor' },
  astudio: { apiBase: 'https://astronstudio-api-volces-prod.xf-yun.com', kind: 'vendor' },
  officeace: { apiBase: 'https://codearts.huaweicloud.com', kind: 'vendor' },
  trae: { apiBase: 'https://api.trae.ai', kind: 'vendor' },
  zcode: { apiBase: 'https://api.zcode.ai', kind: 'vendor' },
  modelscope: { apiBase: 'https://api.modelscope.cn', kind: 'vendor' },
  opencode: { apiBase: 'https://api.opencode.ai', kind: 'vendor' },
}

/** 注册表条目：{ id, name, flow, needs?, endpoints }。 */
export const PROVIDERS = {}

function register(id, entry) {
  PROVIDERS[id] = {
    id,
    endpoints: ENDPOINTS[id] ?? { apiBase: '', kind: 'vendor' },
    ...entry,
  }
}

/** 统一把「阻塞流程」包成后台会话。 */
function background(provider, flow, material, runner, ttlMs) {
  const s = sessions.create({ provider, flow, material, runner, ttlMs })
  return sessions.snapshot(s)
}

// ===========================================================================
// 1. 浏览器授权码（browser）：codearts / gemini / accio
// ===========================================================================

register('codearts', {
  name: '华为云 CodeArts',
  flow: 'browser',
  async authorize({ port }) {
    const r = await codearts.authorize({ port })
    // codearts.mjs 的 state 是对象；其余 provider 统一用字符串，主进程只
    // 存字符串。这里序列化一次，exchange 时再交给上游（它自己兼容两种）。
    return {
      authorize_url: r.authorize_url,
      state: typeof r.state === 'string' ? r.state : JSON.stringify(r.state),
    }
  },
  exchange({ code, state }) {
    return codearts.exchange({ code, state })
  },
  refresh({ credential }) {
    return codearts.refresh({ credential })
  },
})

register('gemini', {
  name: 'Google Gemini',
  flow: 'browser',
  needs: ['port'],
  // client secret 不内置（见 gemini-oauth.js 的注释）：没配就直接说，
  // 而不是发一个必然 redirect_uri_mismatch / invalid_request 的请求。
  unavailable: gemini.GEMINI_DEFAULT_CLIENT_SECRET === ''
    ? '未配置 OAuth 客户端：请设 CMDC_PAK_GOOGLE_CLIENT_SECRET（建议自建 Desktop 类型客户端）'
    : null,
  authorize({ port }) {
    const state = cryptoRandomHex(16)
    const redirectUri = `http://127.0.0.1:${port}${gemini.GEMINI_CALLBACK_PATH}`
    return Promise.resolve({
      authorize_url: gemini.buildGeminiAuthUrl({ state, redirectUri }),
      state: JSON.stringify({ provider: 'gemini', state, redirectUri, created_at: Date.now() }),
    })
  },
  async exchange({ code, state }) {
    const st = JSON.parse(state)
    const grant = await gemini.exchangeGeminiCode(code, st.redirectUri)
    const credential = gemini.credentialFromGeminiGrant(grant)
    return { credential: JSON.stringify(credential), expires_at: credential.expiresAt ?? '' }
  },
  async refresh({ credential }) {
    const cred = typeof credential === 'string' ? JSON.parse(credential) : credential
    const next = await gemini.refreshGeminiCredential(cred)
    return { credential: JSON.stringify(next), expires_at: next.expiresAt ?? '' }
  },
})

register('accio', {
  name: 'Accio',
  flow: 'browser',
  needs: ['port'],
  authorize({ port }) {
    const pkce = accio.createAccioPkcePair()
    const state = accio.createAccioState()
    const redirectUri = `http://127.0.0.1:${port}${CALLBACK_PATH}`
    return Promise.resolve({
      authorize_url: accio.buildAccioAuthUrl({
        region: 'global',
        redirectUri,
        state,
        codeChallenge: pkce.challenge,
      }),
      state: JSON.stringify({
        provider: 'accio',
        state,
        redirectUri,
        codeVerifier: pkce.verifier,
        created_at: Date.now(),
      }),
    })
  },
  async exchange({ code, state }) {
    const st = JSON.parse(state)
    const tok = await accio.exchangeAccioCode({
      region: 'global',
      code,
      codeVerifier: st.codeVerifier,
      redirectUri: st.redirectUri,
    })
    return { credential: JSON.stringify(tok), expires_at: String(tok.expires_at ?? '') }
  },
  async refresh({ credential }) {
    const cred = typeof credential === 'string' ? JSON.parse(credential) : credential
    const next = await accio.refreshAccioCredential({ credential: cred })
    return { credential: JSON.stringify(next), expires_at: String(next.expires_at ?? '') }
  },
})

// ===========================================================================
// 2. 设备码（device_code）：minimax / cline / qoder
//    授权请求本身是网络调用（快），轮询必须后台化。
// ===========================================================================

register('minimax', {
  name: 'MiniMax',
  flow: 'device_code',
  async start() {
    const auth = await minimax.startMinimaxDeviceAuthorization(fetch, MINIMAX)
    // 先把授权拿到手（一次 POST），再把**轮询**丢后台。
    return background(
      'minimax',
      'device_code',
      {
        authorize_url: auth.verificationUriComplete ?? auth.verificationUri,
        user_code: auth.userCode,
        verification_uri: auth.verificationUri,
        interval: auth.intervalSec,
      },
      async ({ signal }) => minimax.pollMinimaxDeviceToken(auth, { signal, product: MINIMAX }),
    )
  },
  async refresh({ credential }) {
    const cred = typeof credential === 'string' ? JSON.parse(credential) : credential
    const next = await minimax.refreshMinimaxCredential(cred)
    return { credential: JSON.stringify(next), expires_at: String(next.expires_at ?? '') }
  },
})

register('cline', {
  name: 'Cline',
  flow: 'device_code',
  async start() {
    const auth = await cline.requestClineDeviceAuthorization(CLINE)
    return background(
      'cline',
      'device_code',
      {
        // 有 verification_uri_complete 时优先：用户不必再手输 user_code。
        authorize_url: auth.verificationUriComplete ?? auth.verificationUri,
        user_code: auth.userCode,
        verification_uri: auth.verificationUri,
        interval: Math.floor(auth.intervalMs / 1000),
      },
      async ({ signal }) => {
        const tokens = await cline.pollClineWorkOsTokens(auth, { signal, product: CLINE })
        const registered = await cline.registerClineTokens(tokens, { product: CLINE, signal })
        return registered
      },
      auth.expiresInMs,
    )
  },
})

register('qoder', {
  name: 'Qoder',
  flow: 'device_code',
  async start() {
    // startQoderLoginFlow 立即返回 {loginUrl, result, close}——result 就是阻塞轮询的
    // Promise。这里把 result 交给后台会话，close 绑到 abort 上。
    const started = await qoder.startQoderLoginFlow({ product: QODER })
    return background(
      'qoder',
      'device_code',
      { authorize_url: started.loginUrl },
      async ({ signal }) => {
        signal.addEventListener('abort', () => { void started.close() }, { once: true })
        const out = await started.result
        return out.access ?? out.credential ?? out
      },
    )
  },
})

// ===========================================================================
// 3. 短信 / 二维码（sms / qr）：loomy / raccoon / autoclaw
// ===========================================================================

register('loomy', {
  name: 'Loomy',
  flow: 'sms',
  needs: ['phone'],
  // 讯飞 AccessKey 不内置：没配 secret 就报未配置（上游会回签名错误，
  // 那个错误与真实原因无关，用户看不出是配置缺失）。
  unavailable: LOOMY.accessKeySecret === ''
    ? '未配置讯飞 AccessKey：请设 LOOMY_ACCESS_KEY_ID / LOOMY_ACCESS_KEY_SECRET'
    : null,
  async start({ phone }) {
    if (!phone) throw new Error('E_BAD_REQUEST: Loomy 登录需要手机号')
    // 上游 sendLoomySmsCode 只返回 **msgid**（登录时必须带它），所以存进会话材料。
    const msgid = await loomy.sendLoomySmsCode(phone, LOOMY)
    // 发码完成、等用户回填：这一步没有后台任务，poll 时由 submit 续上。
    return background(
      'loomy',
      'sms',
      { authorize_url: '', message: '验证码已发送，请回填', phone, msgid },
      null,
    )
  },
  submit({ code, state }) {
    const st = JSON.parse(state)
    if (!st.phone || !st.msgid) throw new Error('会话缺少手机号或 msgid，请重新发起')
    return loomy.loginLoomyBySmsCode(st.phone, code, st.msgid, LOOMY)
  },
})

register('raccoon', {
  name: 'Raccoon（商汤小浣熊）',
  flow: 'qr',
  async start() {
    // 上游没有 product 对象（只有 RACCOON_API_BASE 等常量），这里就地拼出
    // pollRaccoonQrLogin / buildQrImageUrl 需要的最小形状——字段名照上游。
    const product = {
      apiBase: raccoonLib.RACCOON_API_BASE,
      authApiPrefix: raccoonLib.RACCOON_AUTH_PREFIX,
    }
    const code = raccoon.generateQrCode()
    return background(
      'raccoon',
      'qr',
      {
        // 二维码承载的是「微信内打开的登录页」；没有图片 URL，前端渲染
        // 这个链接 + 文本码让用户自己扫。
        authorize_url: raccoon.buildQrImageUrl(product, code),
        qr_code: code,
      },
      async ({ signal }) => {
        for (;;) {
          if (signal?.aborted) throw new Error('登录已取消')
          const r = await raccoon.pollRaccoonQrLogin(product, code)
          if (r.status === 'success') {
            return { access_token: r.accessToken, refresh_token: r.refreshToken, expires_at: r.expiresAt ?? '' }
          }
          if (r.status === 'canceled') throw new Error('用户取消了扫码')
          await sleep(raccoonLib.RACCOON_QR_POLL_INTERVAL_MS ?? 2000)
        }
      },
    )
  },
  async refresh({ credential }) {
    const product = { apiBase: raccoonLib.RACCOON_API_BASE, authApiPrefix: raccoonLib.RACCOON_AUTH_PREFIX }
    const cred = typeof credential === 'string' ? JSON.parse(credential) : credential
    const next = await raccoon.refreshRaccoonCredential(product, cred)
    return { credential: JSON.stringify(next), expires_at: String(next.expires_at ?? '') }
  },
})

register('lobsterai', {
  name: 'LobsterAI',
  flow: 'browser',
  needs: ['port'],
  authorize({ port }) {
    // 上游是「portal 登录页 + 回环回调」：session 只有 uuid/firstKeyfrom，
    // URL 由 buildLobsteraiLoginUrl(port, state, product) 构造。
    const session = lobsterai.createLobsteraiLoginSession()
    const authorize_url = lobsterai.buildLobsteraiLoginUrl(port, session.uuid, LOBSTERAI)
    return Promise.resolve({
      authorize_url,
      state: JSON.stringify({
        provider: 'lobsterai',
        session,
        port,
        clientVersion: LOBSTERAI.fallbackClientVersion ?? '',
        created_at: Date.now(),
      }),
    })
  },
  async exchange({ code, state }) {
    const st = JSON.parse(state)
    const cred = await lobsterai.exchangeLobsteraiAuthCode(
      code, st.session, st.clientVersion, LOBSTERAI)
    return { credential: JSON.stringify(cred), expires_at: String(cred.expires_at ?? cred.expiresAt ?? '') }
  },
})

register('trae', {
  name: 'TRAE',
  flow: 'browser',
  needs: ['port'],
  authorize({ port }) {
    // 上游 startTraeLoginFlow 会**自己起一个本机 HTTP 服务**收回调——那违反
    // sidecar 红线③（不监听任何 TCP 端口）。纯函数面是齐的，所以这里走
    // redirect 形态：自己造 machineId/deviceId、拼 URL、把 session 存进 state，
    // 回调由主进程的路由收下后再调 exchange。
    const machineId = generateMachineId()
    const deviceId = generateDeviceId()
    const callbackUrl = `http://127.0.0.1:${port}${CALLBACK_PATH}`
    return Promise.resolve({
      authorize_url: trae.buildTraeLoginURL(TRAE, machineId, deviceId, callbackUrl),
      state: JSON.stringify({
        provider: 'trae',
        session: { machineId, deviceId, callbackUrl },
        created_at: Date.now(),
      }),
    })
  },
  async exchange({ code, state }) {
    // code 是主进程从回调 query 里取的；parseTraeCallback 吃**完整回调 URL**。
    const st = JSON.parse(state)
    const raw = `http://127.0.0.1${CALLBACK_PATH}?${code}`
    const parsed = trae.parseTraeCallbackDetailed(raw)
    if (!parsed.ok) throw new Error(parsed.reason ?? '回调解析失败')
    const cred = await trae.exchangeTraeCallback(parsed.info, st.session, TRAE)
    return { credential: JSON.stringify(cred), expires_at: String(cred.expires_at ?? cred.expiresAt ?? '') }
  },
})

register('autoclaw', {
  name: 'AutoClaw',
  flow: 'sms',
  needs: ['phone'],
  async start({ phone }) {
    if (!phone) throw new Error('E_BAD_REQUEST: AutoClaw 登录需要手机号')
    const s = await autoclaw.startAutoclawSmsLoginFlow({ phone })
    return background('autoclaw', 'sms', { authorize_url: '', phone, ...s }, null)
  },
})

register('officeace', {
  name: 'OfficeAce（华为云）',
  flow: 'browser',
  // 上游是「云侧 state + PKCE + DPoP + 轮询 code」的两阶段流程：
  // startLogin 拿 URL，pollLoginCode 取 code，completeLogin 换凭据。
  // 回调**不走我们的路由**（走云侧 /v1/claw/auth/code?state=），所以不需要 port。
  async start() {
    const started = await officeace.startLogin()
    const internal = started._internal ?? {}
    return background(
      'officeace',
      'browser',
      { authorize_url: started.authorizeUrl },
      async ({ signal }) => {
        // 轮询 code：用户未授权时 pollLoginCode 返回 undefined。
        const code = await waitForCode(started.state, signal)
        return await officeace.completeLogin(code, internal.codeVerifier, internal.dpopPrivateJwk)
      },
    )
  },
})

register('zcode', {
  name: 'ZCode',
  flow: 'browser',
  // 上游入口是 Service 方法（`new ZcodeAuth(ctx).startLogin()`），需要
  // `ctx.credentials`。这里给它一个**只活一次登录**的内存 ctx：凭据写进
  // 内存后立刻取走，交给主进程密封入库——sidecar 仍然不落盘（红线①）。
  //
  // 登录链路：startLogin 返回 {loginUrl, result}；result 是阻塞到授权完成的
  // Promise，里面还会顺手换 coding-plan key（换不到不影响登录）。
  async start() {
    const ctx = createMemoryContext()
    const auth = new zcode.ZcodeAuth(ctx, {})
    const started = await auth.startLogin({})
    const refName = auth.product?.defaultCredentialRef ?? 'ZCODE'
    return background(
      'zcode',
      'browser',
      { authorize_url: started.loginUrl },
      async () => {
        const out = await started.result
        // 优先用 result 里的 credential；否则从内存 ctx 取（两条路径上游都可能走）。
        const credential = out?.credential ?? tryParse(ctx.__take(refName))
        if (!credential) throw new Error('ZCode 登录未返回凭据')
        return credential
      },
    )
  },
})

// astudio 的登录入口（startAstudioLoginFlow）会**自己起一个本机 HTTP 服务**
// 收微信/账号回调，且微信扫码需要它渲染的页面。sidecar 不监听任何 TCP 端口
// （红线③），所以这家暂不接入——列在 API_KEY_ONLY 里并写明原因，而不是
// 给一个会静默失败的入口。
//
// ===========================================================================
// 4. 其余（modelscope / opencode）
//    上游 auth 模块没有可脱离 IDE 的 OAuth 登录面（只有凭据读取/账号池工具），
//    所以走「API Key 型贡献」。**显式列出并给原因**，而不是让调用方拿到
//    一个空对象。
// ===========================================================================

/** 只能走「API Key 型贡献」的 provider（上游没有可脱离 IDE 的 OAuth 登录面）。 */
/*
 * 这三家没有可用的 OAuth 登录面，但**凭据照样可以贡献**——上游自己用的就是
 * 「粘贴凭据」这条路。所以每一项都带上 base_url 与 steps：
 * base_url 让前端一键填进 API Key 表单，steps 告诉用户去哪儿拿凭据。
 *
 * 各家的依据（都来自 dsh-providers-hub 的源码注释，不是推测）：
 *   - modelscope：`modelscope-auth.js` 明确写「没有登录流程，也不能续期」，
 *     凭据是控制台手工生成的 API Key（`ms-…`），上游入口就是粘贴弹窗。
 *   - astudio：`astudio-oauth.js` 明确写「没有 OAuth2 / PKCE / 授权 URL / 回调」，
 *     登录是 Electron 客户端里的微信扫码，授权页与回调都握在客户端手里。
 *     上游能「用」是因为它跑在用户本机、能直接读 `astron-session.json`；
 *     FreeRouter 是服务端，读不到别人的桌面文件，所以只能请用户把
 *     `modelBearerToken` 粘过来。
 *   - opencode：`opencode-auth.js` 是账号池/指纹工具，不是登录流程；
 *     凭据就是 API Key。
 *
 * 为什么不索性从清单里删掉：删掉之后用户搜不到「这家能不能贡献」，
 * 会以为平台不支持；列出来 + 给替代路径，才是完整回答。
 */
export const API_KEY_ONLY = [
  {
    id: 'modelscope',
    name: 'ModelScope',
    reason: '魔搭没有登录流程：凭据是控制台手工生成的 API Key（ms- 开头），上游入口本身就是"粘贴 Key"',
    baseUrl: 'https://api-inference.modelscope.cn/v1',
    steps: [
      '到 modelscope.cn 控制台 → 访问令牌，生成一个 API Key（ms- 开头）',
      '点下方「填入端点」把 base URL 带进来，再把 Key 粘到 API Key 输入框',
    ],
  },
  {
    id: 'astudio',
    name: 'Astudio',
    reason: 'AStudio 的登录是桌面客户端里的微信扫码，授权页与回调都在客户端内，没有可程序化走完的 OAuth',
    baseUrl: 'https://astronstudio-api-volces-prod.xf-yun.com/model-gateway/v1',
    steps: [
      '先在 AStudio 桌面客户端登录，从本机会话文件 astron-session.json 里取出 modelBearerToken',
      '点下方「填入端点」带入 base URL，把 token 粘到 API Key 输入框',
    ],
  },
  {
    id: 'opencode',
    name: 'OpenCode',
    reason: 'OpenCode 的登录在 adapter 层，上游 auth 只导出账号池指纹工具，不提供可复用的登录流',
    baseUrl: 'https://opencode.ai/inference/openai/v1',
    steps: [
      '到 opencode.ai 生成 API Key',
      '点下方「填入端点」带入 base URL，把 Key 粘到 API Key 输入框',
    ],
  },
]

/** 兼容旧名：待接入列表（现在只剩「本就没有 OAuth」的两家）。 */
export const PENDING = API_KEY_ONLY

/** 取一个 provider 的适配器。 */
export function get(id) {
  return PROVIDERS[id]
}

/** 全部已接入 provider（给主进程渲染入口与可用性判断）。 */
export function available() {
  return Object.values(PROVIDERS).map((p) => ({
    id: p.id,
    name: p.name,
    flow: p.flow,
    needs: p.needs ?? [],
    // 配置缺失的 provider 仍然列出，但带 unavailable 原因：界面上是
    // 「不可用 + 为什么」，而不是一个点了才失败的按钮。
    unavailable: p.unavailable ?? null,
    ...p.endpoints,
  }))
}

/** 未接入的 provider（带原因）。 */
export function pending() {
  return PENDING
}

// —— 工具 ——————————————————————————————————————————————

/** 16 字节 hex（state 用；与上游 gemini/accio 同款长度）。 */
function cryptoRandomHex(bytes) {
  return randomBytes(bytes).toString('hex')
}
/** officeace 的 code 轮询：pollLoginCode 返回 undefined = 用户还没授权。 */
async function waitForCode(state, signal, intervalMs = 2000) {
  for (;;) {
    if (signal?.aborted) throw new Error('登录已取消')
    const code = await officeace.pollLoginCode(state).catch(() => undefined)
    if (typeof code === 'string' && code.length > 0) return code
    await new Promise((r) => setTimeout(r, intervalMs))
  }
}

/** 把内存 ctx 里的凭据字符串解析成对象；解析失败返回 undefined。 */
function tryParse(raw) {
  if (typeof raw !== 'string' || raw.length === 0) return undefined
  try {
    return JSON.parse(raw)
  } catch {
    return raw
  }
}
