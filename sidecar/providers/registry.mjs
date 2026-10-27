// registry.mjs —— 供应商 OAuth 的注册表（14 家，三种交互形态）。
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
import * as loomyWechat from './loomy/loomy-wechat.js'
import * as minimax from './minimax/minimax-oauth.js'
import * as qoder from './qoder/qoder-oauth.js'
import * as raccoon from './raccoon/raccoon-oauth.js'
import * as raccoonLib from './raccoon/raccoon.js'
import * as trae from './trae/trae-oauth.js'
import { TRAE } from './trae/trae-product.js'
import { generateDeviceId, generateMachineId } from './trae/trae.js'
import * as autoclaw from './autoclaw/autoclaw-oauth.js'
import * as officeace from './officeace/officeace-auth.js'
import * as astudioOAuth from './astudio/astudio-oauth.js'
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
 * 把外部二维码图片抓下来转成 `data:` URL。
 *
 * ## 为什么必须这么做（2026-10-24 实测）
 *
 * 服务端的安全头是 `Content-Security-Policy: img-src 'self' data:`，微信的
 * 二维码域名（`mp.weixin.qq.com` / `open.weixin.qq.com`）**不在白名单里**，
 * 浏览器会直接拒绝加载——实测 `<img>` 的 naturalWidth 恒为 0，而且控制台
 * 连请求都不发（CSP 在请求前就拦了）。
 *
 * 两个可选做法：放宽 CSP 加两个外部域，或由 sidecar 抓取后以 `data:` 内联。
 * 选后者：① `data:` 本来就已在白名单里，不用为了一个图片削弱全站 CSP；
 * ② 抓取发生在服务端，不受用户网络环境（是否墙外）影响；③ 微信二维码
 * 约 40 KB，base64 后 ~55 KB，只在发起授权那一次返回，代价可接受。
 *
 * 抓取失败不致命：返回 null，由调用方回落到原始 `qr_image_url`
 * （若将来放宽了 CSP 或做了反代，那条路仍然能用）。
 */
async function qrAsDataUrl(url, extraHeaders = {}) {
  try {
    const r = await fetch(url, {
      headers: {
        // 微信对空 UA / 爬虫 UA 会拒绝，这里伪装成普通浏览器。
        'User-Agent':
          'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
        ...extraHeaders,
      },
      signal: AbortSignal.timeout(10_000),
    })
    if (!r.ok) return null
    const buf = Buffer.from(await r.arrayBuffer())
    if (buf.length === 0 || buf.length > 512 * 1024) return null
    const ct = r.headers.get('content-type')?.split(';')[0]?.trim() || 'image/png'
    if (!ct.startsWith('image/')) return null
    return `data:${ct};base64,${buf.toString('base64')}`
  } catch {
    return null
  }
}

/**
 * sleep：二维码/设备码形态的轮询间隔。
 *
 * 这个函数**之前只被引用、从未定义**（raccoon 的 runner 里 `await sleep(...)`），
 * 意味着商汤小浣熊扫码后一进入轮询就抛 `ReferenceError: sleep is not defined`。
 * 之所以没人发现：扫码流程要真人拿手机扫才会走到那一行。
 *
 * 支持 signal：用户点「放弃本次授权」时立刻中断等待，而不是干等到超时。
 */
function sleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new Error('登录已取消'))
      return
    }
    const timer = setTimeout(resolve, ms)
    signal?.addEventListener?.(
      'abort',
      () => {
        clearTimeout(timer)
        reject(new Error('登录已取消'))
      },
      { once: true },
    )
  })
}

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
  name: 'Loomy（讯飞）',
  /*
   * 改成**微信扫码**（原先是短信验证码，需要用户先输手机号）。
   *
   * 扫码的好处是不用手机号、路径更短；流程全在微信开放平台的标准接口上：
   *   ① 拉授权页 HTML → 正则取 uuid（**无需执行 JS**，这是能脱离浏览器的关键）
   *   ② 前端直接 <img src=open.weixin.qq.com/connect/qrcode/<uuid>>
   *   ③ 长轮询 long.open.weixin.qq.com/connect/l/qrconnect 读 wx_errcode
   *   ④ 405（已确认）那一帧带 wx_code → 换 rcode → 换 session
   *
   * 全部是 HTTP，**不需要本机监听**（上游另有一版会起本机服务渲染二维码页，
   * 与 sidecar 红线③冲突，故不采用）。
   *
   * 短信登录（loginLoomyBySmsCode）仍然可用、代码保留，只是清单里不再暴露
   * 那个入口——一个 provider 只能声明一个 flow，扫码是主流路径。
   */
  flow: 'qr',
  unavailable: LOOMY.accessKeySecret === ''
    ? '未配置讯飞 AccessKey：请设 LOOMY_ACCESS_KEY_ID / LOOMY_ACCESS_KEY_SECRET'
    : null,
  async start() {
    const uuid = await loomyWechat.fetchLoomyWechatUuid()
    const qrUrl = loomyWechat.buildLoomyWechatQrImageUrl(uuid)
    return background(
      'loomy',
      'qr',
      {
        authorize_url: qrUrl,
        qr_image_url: qrUrl,
        qr_image_data: await qrAsDataUrl(qrUrl, { Referer: 'https://open.weixin.qq.com/' }),
        message: '请用微信扫码，扫完在手机上确认',
      },
      async ({ signal }) => {
        const ST = loomyWechat.LOOMY_WECHAT_POLL_STATUS
        let lastErrcode = ''
        for (;;) {
          if (signal?.aborted) throw new Error('登录已取消')
          // pollLoomyWechatOnce 自己是长轮询（微信侧 ~25 秒才返回），正常帧
          // 不再额外 sleep。error 帧除外：网络故障往往立刻失败，不退避就是
          // 热旋（2026-10-27 排查「扫码无反应」时补的）。
          const r = await loomyWechat.pollLoomyWechatOnce(uuid, lastErrcode)
          if (r.status === ST.error) {
            await new Promise((ok) => setTimeout(ok, 3_000))
            continue
          }
          // last 只接受数字 errcode：pollLoomyWechatOnce 的 error 帧把错误
          // 文本（如 "TimeoutError"）放进了 errcode 字段，原样回传给微信
          // （last=TimeoutError）是协议外参数，行为不可预期。
          if (/^\d+$/.test(r.errcode ?? '')) lastErrcode = r.errcode
          if (r.status === ST.confirmed) {
            if (!r.code) continue // 405 却没带 code：异常帧，继续轮询，绝不拿空 code 去换
            const bound = await loomy.bindLoomyThirdAccount(r.code, LOOMY)
            if (bound.bind === 1) {
              // 该微信已绑手机号 → 直接换会话（skip 跳过的是"绑定手机号"步骤）
              const out = await loomy.bindLoomySkip(bound.rcode, LOOMY)
              // 凭据形状对齐 hub 的 buildCredential（loomy-auth.js）：转发
              // adapter 只读 credential.access_token（loomy-adapter.js 的
              // send(access_token)），"session" 字段名它不认识——旧写法落库的
              // 凭据即使扫码成功也完全不可用。expires_at 是毫秒字符串，
              // TTL 14 天（LOOMY_SESSION_TTL_SECONDS = 1_209_600）。
              return {
                access_token: out.session,
                userid: out.userid,
                expires_at: String(Date.now() + loomy.LOOMY_SESSION_TTL_SECONDS * 1000),
              }
            }
            // 未绑手机号：上游这套接口拿不到会话，只能请用户先绑一次。
            // 不假装成功，也不循环重试（同一个号再扫还是同样的结果）。
            throw new Error(
              '该微信尚未绑定 Loomy 账号：请先到 loomy 用手机号登录一次完成绑定，再回来扫码',
            )
          }
          if (r.status === ST.cancelled) throw new Error('用户取消了扫码')
          if (r.status === ST.expired) throw new Error('二维码已失效，请重新发起登录')
        }
      },
      300_000,
    )
  },
})

register('raccoon', {
  name: 'Raccoon（商汤小浣熊）',
  // 2026-10-27：扫码协议（微信路径 login/mp 已 404、App 报「内容无效」）
  // 双双失效，切回**短信验证码**登录——hub 的登录页实测这条路是活的。
  // captcha_param 来自阿里云滑块（前端贡献页嵌官方 AliyunCaptcha.js，
  // SceneId/prefix 是客户端逆向常量）。
  flow: 'sms',
  needs: ['phone'],
  async start({ phone, captchaParam }) {
    if (!phone) throw new Error('E_BAD_REQUEST: Raccoon 登录需要手机号')
    if (!captchaParam) throw new Error('E_BAD_REQUEST: 请先完成滑块验证')
    // 现行协议：滑块产物字段名 captcha_token，发码应答下发 verification_id
    //（登录的会话键，存进 material 让 submit 带回）。
    const verificationId = await raccoon.sendSmsVerification(
      { apiBase: raccoonLib.RACCOON_API_BASE, authApiPrefix: raccoonLib.RACCOON_AUTH_PREFIX },
      phone, captchaParam)
    return background(
      'raccoon',
      'sms',
      { authorize_url: '', phone, verification_id: verificationId, message: '验证码已发送，请查收短信' },
      null,
    )
  },
  // 用户回填验证码后，oauth.poll 携 code 进来（sessions.attachRunner）。
  async submit({ code, state }) {
    const st = JSON.parse(state)
    const cred = await raccoon.smsLogin(
      { apiBase: raccoonLib.RACCOON_API_BASE, authApiPrefix: raccoonLib.RACCOON_AUTH_PREFIX },
      st.phone, st.verification_id, code)
    return cred
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
    // callbackUrl 的路径必须是 /authorize（TRAE_CALLBACK_PATH，上游 IDE 插件
    // 的形态）：TRAE 对该形态有校验，换成 /oauth/callback 会让授权页报
    // 「网络错误」（2026-10-27 用户实测）。
    const callbackUrl = `http://127.0.0.1:${port}${trae.TRAE_CALLBACK_PATH}`
    return Promise.resolve({
      authorize_url: trae.buildTraeLoginURL(TRAE, machineId, deviceId, callbackUrl),
      state: JSON.stringify({
        provider: 'trae',
        session: { machineId, deviceId, callbackUrl },
        created_at: Date.now(),
      }),
    })
  },
  async exchange({ code, state, raw_query }) {
    // parseTraeCallbackDetailed 吃**完整回调 URL**：TRAE 的 query 里没有标准
    // code，而是 userInfo/userJwt/authCodeInfo 等自有字段——必须用主进程
    // 原样透传的 raw_query（标准 OAuth 的 code 单值拼法解析不出这些）。
    const st = JSON.parse(state)
    const raw = raw_query
      ? `http://127.0.0.1${trae.TRAE_CALLBACK_PATH}?${raw_query}`
      : `http://127.0.0.1${trae.TRAE_CALLBACK_PATH}?${code}`
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

// ===========================================================================
// 4. 其余：暂无
//    上一版这里还有 modelscope / opencode 两家「只能粘贴 Key」的条目。贡献入口改
//    成三分类后，这类需求由**内置 API 目录**（internal/apidir，52 家）承担：目录
//    本来就是「端点 + 用户粘贴 Key」的形态，不需要 sidecar 再列一遍。两家随之下线
//    （它们的 sidecar 模块也一并删除，见 tmp/CONTRACT-contribution-v2.md §7）。
// ===========================================================================

/**
 * 只能走「API Key 型贡献」的 provider。
 *
 * **现在为空**——modelscope / opencode 已按契约 v2 §7 移除：
 *   - 它们的凭据形态（控制台生成、粘贴使用）与「API 供应商」入口完全一致，
 *     由 internal/apidir 的目录承载更合适（目录不依赖 sidecar，也不区分登录面）；
 *   - 保留这个导出而不是删掉它，是因为 index.mjs 的 oauth.list 仍在应答里带
 *     `api_key_only` 字段——那是 04-sidecar.md 2.3 的应答形状，删字段属于契约变更，
 *     不该顺手做。语义从「有 N 家替代路径」变成「没有」，形状不变（空数组）。
 */
export const API_KEY_ONLY = []

/** 兼容旧名：待接入列表（现在恒为空，理由见 API_KEY_ONLY 的注释）。 */
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
