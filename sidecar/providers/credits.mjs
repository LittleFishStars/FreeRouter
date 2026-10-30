// 凭据余额/积分查询（统一入口）——**直接复用 dsh-providers-hub 的分发器**。
//
// 为什么不再逐家移植（2026-10-28 架构调整）：hub 的 provider-credits.js 是
// **17 家的统一分发器**（含各家 product/请求头/分项归一的正确实现），逐家
// 抄进 sidecar 等于维护两份必然漂移的实现。改为 sidecar 启动时把 hub lib
// 实体复制进依赖 shim（deps.mjs 的 buildShim，hubRoot 参数），运行期动态
// import 分发器——**17 家全部支持**，hub 升级即跟上。
//
// 形状契约（queryCreditBalance 的返回）：
//   { ok: true,  total: number|null, unit: string, packages?: [{name, amount}] }
//   { ok: false, error: string }
// 「查不到」与「失败」分开：上游没报余额 → ok:true + total:null；网络/鉴权
// 失败 → ok:false + error（可重试）。
//
// ⚠️ 动态 import 的时机：shim 就绪（loadDeps）之后才解析得了。旧注释说
// 「credits.query 都在 OAuth 装配完成后才会被调，时序天然满足」——**credits.claim
// 打破了这条**：定时签到在启动后 30 秒就跑，早于任何 OAuth 流程。故 distributor()
// 里显式补装一次 getDeps()（幂等，已装过是零开销内存读），不再赌调用时序。

import * as hub from '../hub.mjs'
let providerCredits = null

async function distributor() {
  // 只缓存成功；失败可重试——首次查询可能早于 shim 就绪（loadDeps 懒加载），
  // 永久缓存失败会让之后每一次都挂。
  if (providerCredits) return providerCredits
  // 先把 B 层依赖的解析钩子装上（hub.mjs 同款约定：调用可能先于 health 到达）。
  await hub.getDeps()
  const mod = await import('dsh-providers-hub/lib/provider-credits.js')
  providerCredits = mod.createProviderCredits()
  return providerCredits
}

/** 归一 hub 分发器的返回：fetchBalance → balance 对象；fetchBalanceDetailed → {ok, balance, error}。 */
function normalize(out) {
  // Detailed 形态：{ ok, balance: {total, unit, packages} | null, error }
  if (out && typeof out === 'object' && 'balance' in out) {
    if (out.balance && typeof out.balance.total === 'number') {
      return {
        ok: true, total: out.balance.total, unit: out.balance.unit ?? '',
        packages: Array.isArray(out.balance.packages)
          ? out.balance.packages.map((p) => ({ name: p.name ?? '余额', amount: p.amount ?? 0 }))
          : undefined,
      }
    }
    return { ok: false, error: out.error ?? '上游未报余额' }
  }
  // fetchBalance 形态：balance 对象直返（total/ unit/ packages）
  if (out && typeof out === 'object' && typeof out.total === 'number') {
    return {
      ok: true, total: out.total, unit: out.unit ?? '',
      packages: Array.isArray(out.packages)
        ? out.packages.map((p) => ({ name: p.name ?? '余额', amount: p.amount ?? 0 }))
        : undefined,
    }
  }
  return shapeErr('该渠道', r)
}

// ── API 型（用户粘 Key）的余额查询 ──
//
// API 型凭据的明文是裸 key 字符串（贡献提交时 PutSecret 存的就是 key 本身，
// 不是 JSON 对象）——与反代型的「凭据对象」不同源，走独立分发。各家端点与
// 应答形状逐家实测（无效 key 的应答形态已验证；「找到 balance 字段才报数，
// 找不到如实报」的原则与反代侧一致）。

const REQUEST_TIMEOUT_MS = 30_000

function bearerKey(credential) {
  if (typeof credential === 'string') return credential.trim()
  return String(credential?.api_key ?? credential?.access_token ?? '').trim()
}

async function getJson(url, key) {
  const response = await fetch(url, {
    method: 'GET',
    headers: { Accept: 'application/json', Authorization: `Bearer ${key}` },
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  })
  const body = await response.text()
  try { return { http: response.status, body: JSON.parse(body) } }
  catch { return { http: response.status, body: body.slice(0, 200) } }
}

// 形状不符的自诊文案：HTTP 状态 + body 摘要（截 100 字节）——上游改版时
// 用户截图就能定位，不用再猜「形状不符」是哪个字段没对上。余额应答不含
// 敏感信息，摘要安全。
function shapeErr(provider, r) {
  const bodyStr = typeof r.body === 'string' ? r.body : JSON.stringify(r.body)
  return { ok: false, error: `${provider} 余额应答形状不符（HTTP ${r.http}，body: ${String(bodyStr).slice(0, 100)}）` }
}

function readNumLoose(v) {
  if (typeof v === 'number' && Number.isFinite(v)) return v
  if (typeof v === 'string' && v !== '' && Number.isFinite(Number(v))) return Number(v)
  return undefined
}

async function balanceDeepSeek(key, baseUrl) {
  const r = await getJson(`${baseUrl}/user/balance`, key)
  if (r.http === 401) return { ok: false, error: '凭据失效（401）' }
  // 官方文档（api-docs.deepseek.com/api/get-user-balance）：响应是
  // { is_available, balance_infos: [{currency, total_balance, granted_balance, topped_up_balance}] }
  const infos = Array.isArray(r.body?.balance_infos) ? r.body.balance_infos : undefined
  if (infos !== undefined && infos.length > 0) {
    const info = infos.find((i) => i.currency === 'CNY') ?? infos[0]
    const total = readNumLoose(info.total_balance)
    if (total === undefined) return shapeErr('deepseek', r)
    const granted = readNumLoose(info.granted_balance)
    const topped = readNumLoose(info.topped_up_balance)
    const packages = []
    if (granted) packages.push({ name: '赠送余额', amount: granted })
    if (topped) packages.push({ name: '充值余额', amount: topped })
    return {
      ok: true, total, unit: info.currency === 'CNY' ? '元' : (info.currency ?? ''),
      packages: packages.length > 0 ? packages : undefined,
    }
  }
  // 兼容旧版单值字段
  const bal = readNumLoose(r.body?.balance)
  if (bal === undefined) return shapeErr('deepseek', r)
  return { ok: true, total: bal, unit: r.body?.currency === 'CNY' ? '元' : (r.body?.currency ?? '') }
}

// BigModel 用控制台侧的账户报告接口（用户实测提供；open.bigmodel 的
// /v4/finance/balance 对贡献 key 回形状不符）。注意该接口 HTTP 恒 200，
// 业务成败看 body.code/success。
async function balanceBigModel(key, baseUrl) {
  const r = await getJson('https://www.bigmodel.cn/api/biz/account/query-customer-account-report', key)
  if (r.body?.code === 401) return { ok: false, error: '凭据失效（401：令牌过期或验证不正确）' }
  if (r.body?.success === false) return { ok: false, error: `bigmodel: ${r.body?.msg ?? '查询失败'}` }
  const d = r.body?.data ?? r.body
  // 字段名逐层尝试：报告接口的字段名未在文档公开，按常见命名探测，
  // 都没中就带 body 摘要自诊（用户截图即可定位真实字段）。
  const total = readNumLoose(d?.total_balance) ?? readNumLoose(d?.balance) ?? readNumLoose(d?.totalBalance)
    ?? readNumLoose(d?.quota_total) ?? readNumLoose(d?.remaining_quota)
  if (total === undefined) return shapeErr('bigmodel', r)
  const gift = readNumLoose(d?.gift_balance)
  const purchase = readNumLoose(d?.purchased_balance) ?? readNumLoose(d?.charge_balance)
  const packages = []
  if (purchase !== undefined) packages.push({ name: '充值余额', amount: purchase })
  if (gift !== undefined) packages.push({ name: '赠送余额', amount: gift })
  return { ok: true, total, unit: '元', packages: packages.length > 0 ? packages : undefined }
}

async function balanceSiliconFlow(key, baseUrl) {
  const r = await getJson(`${baseUrl}/user/info`, key)
  if (r.http === 401 || r.body?.code === 30014) return { ok: false, error: '凭据失效（401）' }
  const bal = readNumLoose(r.body?.data?.totalBalance)
  if (bal === undefined) return shapeErr('该渠道', r)
  const charge = readNumLoose(r.body?.data?.chargeBalance)
  return {
    ok: true, total: bal, unit: '元',
    packages: charge !== undefined ? [{ name: '充值余额', amount: charge }] : undefined,
  }
}

async function balanceMoonshot(key, baseUrl) {
  const r = await getJson(`${baseUrl}/users/me/balance`, key)
  if (r.http === 401) return { ok: false, error: '凭据失效（401）' }
  const avail = readNumLoose(r.body?.data?.available_balance)
  if (avail === undefined) return shapeErr('该渠道', r)
  const vip = readNumLoose(r.body?.data?.vip_lease_balance)
  return {
    ok: true, total: avail, unit: '元',
    packages: vip !== undefined && vip > 0 ? [{ name: '会员租赁', amount: vip }] : undefined,
  }
}

async function balanceOpenRouter(key, baseUrl) {
  const r = await getJson(`${baseUrl}/credits`, key)
  if (r.http === 401) return { ok: false, error: '凭据失效（401）' }
  const d = r.body?.data ?? {}
  const total = readNumLoose(d.total_credits)
  const used = readNumLoose(d.total_usage)
  if (total === undefined) return shapeErr('该渠道', r)
  return {
    ok: true,
    total: used !== undefined ? total - used : total,
    unit: 'USD',
    packages: used !== undefined ? [{ name: '已用', amount: used }] : undefined,
  }
}

// 目录 id → 余额查询（同厂商多部署共用同一查询形状）。
const API_BALANCE_BY_PROVIDER = {
  'deepseek-com': { fn: balanceDeepSeek, base: 'https://api.deepseek.com' },
  'bigmodel-cn': { fn: balanceBigModel, base: 'https://open.bigmodel.cn/api/paas/v4' },
  'bigmodel-cn-coding': { fn: balanceBigModel, base: 'https://open.bigmodel.cn/api/paas/v4' },
  'siliconflow-cn': { fn: balanceSiliconFlow, base: 'https://api.siliconflow.cn/v1' },
  'siliconflow-com': { fn: balanceSiliconFlow, base: 'https://api.siliconflow.com/v1' },
  'moonshot-ai': { fn: balanceMoonshot, base: 'https://api.moonshot.ai/v1' },
  'moonshot-cn': { fn: balanceMoonshot, base: 'https://api.moonshot.cn/v1' },
  'openrouter-ai': { fn: balanceOpenRouter, base: 'https://openrouter.ai/api/v1' },
}

async function queryApiBalance(provider, key) {
  // upstream 行的 provider 形如 "api:deepseek-com"（目录 id 前缀）——剥掉再查表
  const bare = provider.startsWith('api:') ? provider.slice(4) : provider
  const entry = API_BALANCE_BY_PROVIDER[bare]
  if (!entry) return { ok: false, error: `${bare} 暂不支持余额查询` }
  if (!key) return { ok: false, error: '凭据缺少 API Key，无法查询余额' }
  try {
    return await entry.fn(key, entry.base)
  } catch (error) {
    return { ok: false, error: `${bare}: ${error instanceof Error ? error.message : String(error)}` }
  }
}

/** 按家分发续期：都返回「合并后的完整新凭据对象」供写回。 */
async function selfRefresh(provider, credential) {
  if (provider === 'minimax') return refreshMinimaxCredential(credential)
  if (provider === 'raccoon') return refreshRaccoonCred(credential)
  if (provider === 'cline') return refreshClineCred(credential)
  throw new Error(`${provider} 不支持续期`)
}

/** raccoon 续期：refreshRaccoonCredential 返回 token grant（含轮换的
 * refresh_token）——合并回旧凭据（保留 user_id 等元数据）。 */
async function refreshRaccoonCred(credential) {
  if (typeof credential?.refresh_token !== 'string' || credential.refresh_token.length === 0) {
    throw new Error('无 refresh_token，无法续期')
  }
  const { refreshRaccoonCredential } = await import('./raccoon/raccoon-oauth.js')
  const { RACCOON_API_BASE, RACCOON_AUTH_PREFIX } = await import('./raccoon/raccoon.js')
  // raccoon 没有 product 常量（函数吃 {apiBase, authApiPrefix} 形状）——内联构造
  const product = { apiBase: RACCOON_API_BASE, authApiPrefix: RACCOON_AUTH_PREFIX }
  const grant = await refreshRaccoonCredential(product, credential)
  const merged = { ...credential, ...grant }
  if (!merged.refresh_token) merged.refresh_token = credential.refresh_token
  return merged
}

/** cline 续期：POST /api/v1/auth/refresh（字段是驼峰 refreshToken/grantType
 * ——写错字段名服务端只回泛化认证失败，见 cline.js 的源码注记）。响应过
 * parseClineTokenPayload 解包，applyClineRefresh 合并回旧凭据（account_id/
 * email/nickname 不在续期响应里，丢了余额查询就废）。 */
async function refreshClineCred(credential) {
  if (typeof credential?.refresh_token !== 'string' || credential.refresh_token.length === 0) {
    throw new Error('无 refresh_token，无法续期')
  }
  const { CLINE } = await import('./cline/cline-product.js')
  const { parseClineTokenPayload, applyClineRefresh, buildClineCredential } = await import('./cline/cline.js')
  const response = await fetch(`${CLINE.apiBase ?? 'https://api.cline.bot'}/api/v1/auth/refresh`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ refreshToken: credential.refresh_token, grantType: 'refresh_token' }),
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  })
  const body = await response.json().catch(() => undefined)
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}：${typeof body === 'object' && body?.message ? body.message : '刷新被拒'}`)
  }
  const payload = parseClineTokenPayload(body)
  if (!payload.accessToken || payload.accessToken.length === 0) {
    throw new Error('续期响应里没有 accessToken')
  }
  const base = buildClineCredential(parseClineTokenPayload(credential), CLINE)
  return applyClineRefresh(base, payload, CLINE)
}

/** minimax 静默续期：401 时用 refresh_token 换新 token。返回合并后的新凭据
 * （旧 uid/scope 等元数据保留；上游没回新 refresh_token 就沿用旧的），
 * 供 Go 桥写回密文。失败抛错——refresh_token 也过期时只能重新授权。 */
async function refreshMinimaxCredential(credential) {
  if (typeof credential?.refresh_token !== 'string' || credential.refresh_token.length === 0) {
    throw new Error('无 refresh_token，无法续期')
  }
  const { refreshMinimaxCredential } = await import('./minimax/minimax-oauth.js')
  const grant = await refreshMinimaxCredential(credential.refresh_token)
  const merged = { ...credential, ...grant }
  if (!merged.refresh_token) merged.refresh_token = credential.refresh_token
  return merged
}

/** qoder 余额：hub 的 qoder-credits 没接进分发器（官方口径 QoderCredential
 * 泛型不匹配 collectCreditBalances），但函数本体可用——直调。国际版失败
 * （401/形状）再试中国版：凭据可能来自任一部署。 */
async function queryQoder(credential) {
  const mod = await import('dsh-providers-hub/lib/qoder-credits.js')
  const products = await import('dsh-providers-hub/lib/qoder-product.js')
  let lastErr = '未知原因'
  for (const product of [products.QODER, products.QODER_CN]) {
    try {
      const detailed = await mod.fetchQoderBalanceDetailed(credential, product)
      if (detailed.balance && typeof detailed.balance.total === 'number') {
        return {
          ok: true, total: detailed.balance.total, unit: detailed.balance.unit ?? '积分',
          packages: Array.isArray(detailed.balance.packages)
            ? detailed.balance.packages.map((p) => ({ name: p.name ?? '余额', amount: p.amount ?? 0 }))
            : undefined,
        }
      }
      lastErr = detailed.error ?? '上游未报余额'
      if (/401|失效|invalid/i.test(lastErr)) continue // 换另一部署再试
      return { ok: false, error: lastErr }
    } catch (error) {
      lastErr = error instanceof Error ? error.message : String(error)
    }
  }
  return { ok: false, error: `qoder: ${lastErr}` }
}

/**
 * 查一个凭据的余额/积分。
 *
 * @param credential 已解密的凭据明文：反代型是 JSON 对象（与
 *   oauth.exchange 的产出同源，各家字段天然匹配）；API 型（目录 id 或
 *   custom）是裸 key 字符串（PutSecret 存的就是 key 本身）。
 */
/** cline 存量凭据自愈：早期落库存的是原始响应包 {success, data:{…}} 而非
 * 凭据对象——账号 id（余额查询必需）都嵌在 data 里。用 hub 自己的解包 +
 * 构造函数转成标准凭据形状（与 oauth.exchange 的正确产出逐字段一致）。 */
async function unwrapClineEnvelope(credential) {
  if (
    provider_cline_envelope(credential)
  ) {
    try {
      const mod = await import('dsh-providers-hub/lib/cline.js')
      const { CLINE } = await import('./cline/cline-product.js')
      const payload = mod.parseClineTokenPayload(credential)
      return mod.buildClineCredential(payload, CLINE)
    } catch {
      return credential // 自愈失败按原样试（hub 会报出真正的缺字段）
    }
  }
  return credential
}

function provider_cline_envelope(credential) {
  return (
    credential !== null && typeof credential === 'object' &&
    !Array.isArray(credential) &&
    'data' in credential && 'success' in credential
  )
}

export async function queryCreditBalance(provider, credential) {
  // API 型先分（api:/custom 不在 hub 分发器里，且凭据是裸 key 不是对象）
  if (API_BALANCE_BY_PROVIDER[provider] || provider === 'custom' || provider.startsWith('api:')) {
    return queryApiBalance(provider, bearerKey(credential))
  }
  if (provider === 'cline') {
    credential = await unwrapClineEnvelope(credential)
  }
  // qoder 不在分发器里（官方刻意缺席）但函数可用——直调，双部署都试
  if (provider === 'qoder' || provider === 'qodercn') {
    return queryQoder(credential)
  }
  // cline 与 raccoon：存量凭据可能是信封形态，先自愈再走续期判断
  if (provider === 'cline') {
    credential = await unwrapClineEnvelope(credential)
  }
  const canSelfRefresh = provider === 'minimax' || provider === 'cline' || provider === 'raccoon'
  let pc
  try {
    pc = await distributor()
  } catch (error) {
    return { ok: false, error: `余额查询不可用：${error instanceof Error ? error.message : String(error)}` }
  }
  const entry = pc[provider]
  if (!entry) {
    return { ok: false, error: `${provider} 暂不支持余额查询` }
  }
  async function callDistributor(cred) {
    if (typeof entry.fetchBalanceDetailed === 'function') {
      return normalize(await entry.fetchBalanceDetailed(cred))
    }
    if (typeof entry.fetchBalance === 'function') {
      return normalize(await entry.fetchBalance(cred))
    }
    return { ok: false, error: `${provider} 未实现余额查询` }
  }
  try {
    const first = await callDistributor(credential)
    // 静默续期（minimax/cline/raccoon）：401（token 过期）时用 refresh_token
    // 换新后重查一次。新凭据随应答上行（refreshed_credential），Go 桥原地
    // 写回密文——下次查询/推理直接用新 token。
    if (
      canSelfRefresh && !first.ok &&
      /401|凭据失效|登录态已过期|invalid|Unauthorized/i.test(first.error ?? '')
    ) {
      try {
        const fresh = await selfRefresh(provider, credential)
        const second = await callDistributor(fresh)
        if (second.ok) return { ...second, refreshed_credential: fresh }
        return { ok: false, error: `续期后仍失败：${second.error ?? '未知'}`, refreshed_credential: fresh }
      } catch (e) {
        return { ok: false, error: `续期失败（${e instanceof Error ? e.message : String(e)}），请重新授权` }
      }
    }
    return first
  } catch (error) {
    return { ok: false, error: `${provider}: ${error instanceof Error ? error.message : String(error)}` }
  }
}

/** 支持余额查询的 provider 清单（管理面据此显示「刷新余额」能力）。 */
export async function balanceSupportedProviders() {
  try {
    return Object.keys(await distributor())
  } catch {
    return []
  }
}

// ── 每日签到/领取（写上游业务的积分活动） ────────────────────────────
//
// 参考实现：dsh-providers-hub 的 credits.claimAll（jet-hub-rpc.js 的
// collectClaimResults 骨架）。分发仍走 createProviderCredits() 这一份表 ——
// claim / claimStatus / precheckStatus / claimUnavailableReason 全登记在条目上，
// 另抄一份能力名单必然漂移（hub 在 auto-checkin 里记过同一条成训）。
//
// 与 hub 骨架的三处差异（sidecar 没有账号池，逐条注明为什么安全）：
// - entry.id：用「provider:凭据内容指纹」——只作 trae 设备指纹代次与
//   minimaxintl 按账号 fetcher 的**内存键**；进程重启即失，trae 的代次回落 0
//   后按 credential.uid 确定性重派生（trae-credits 的设备身份设计），不丢稳定性。
// - call.accountPool：trae 需要的代次读写 —— 进程内 Map 桩（见上）。
// - call.persistCredential：autoclaw 领取可能轮换 refresh_token —— sidecar
//   **不落盘**（红线①：凭据续期只能由 Go 桥写回密文），新值放进应答的
//   refreshed_credential，与 credits.query 静默续期走同一条写回通道。
//
// afterClaimNote（trae 1024 打卡抽奖）：照实接 —— 表条目自带，单次快速上游调用；
// note 拼进 outcome.message，格式与 hub 逐字一致（见 claim 内的实现注释）。
// ⚠️ afterClaim（CodeBuddy 成长中心）**刻意不接**：单账号一轮 90～270s，会把
// 与推理共享的串行 stdio 管道阻塞几分钟（hub 是并发 RPC 网关才跑得动）；
// 需要时去 DSH 面板手动领取。

import { createHash } from 'node:crypto'

/** trae 设备指纹代次的进程内桩（entryId → 代次）。 */
const claimDeviceGenerations = new Map()

/** 凭据内容指纹：仅作内存键，不进日志、不进应答。 */
function credentialFingerprint(provider, credential) {
  const raw = typeof credential === 'string' ? credential : JSON.stringify(credential)
  return `${provider}:${createHash('sha256').update(String(raw)).digest('hex').slice(0, 16)}`
}

function traeAccountPoolStub() {
  return {
    traeCheckinDeviceGenerationFor: (id) => claimDeviceGenerations.get(id) ?? 0,
    updateTraeCheckinDeviceGeneration: (id, next) => { claimDeviceGenerations.set(id, next) },
  }
}

/** 归一 hub 的 ClaimOutcome：未知 kind 按 failed 计入（computeClaimSummary 同款兜底）。 */
function normalizeClaimOutcome(outcome) {
  const o = outcome && typeof outcome === 'object' ? outcome : {}
  const known = ['claimed', 'already-claimed', 'inactive', 'failed']
  const kind = known.includes(o.kind) ? o.kind : 'failed'
  const res = { kind }
  if (typeof o.credit === 'number') res.credit = o.credit
  if (typeof o.unit === 'string') res.unit = o.unit
  if (typeof o.message === 'string') res.message = o.message
  if (typeof o.code === 'number') res.code = o.code
  // coversToday=false 是渠道自己标的「看到的是上一轮的痕迹」（Qoder 刷新前那轮），
  // 原样透传给 Go 侧，记账口径由那边决定。
  if (o.coversToday === false) res.covers_today = false
  if (res.kind === 'failed' && res.message === undefined) res.message = '未知失败原因'
  return res
}

/**
 * 对一个凭据执行每日签到/领取积分。
 *
 * @returns {{ ok: true, outcome, unsupported?: boolean, refreshed_credential?: object }}
 *   outcome 形如 hub 的 ClaimOutcome：{kind, credit?, unit?, message?, code?, covers_today?}；
 *   unsupported=true 表示该渠道没有签到能力（不发上游请求就判定的那些），
 *   调度器据此「记一次、当天不再试」，与失败（网络/上游错，退避重试）区分开。
 */
export async function claimCreditBalance(provider, credential) {
  // API 型（裸 key）没有签到活动：如实报，unsupported 而不是 error ——
  // 它不是故障，天天重试只会制造噪音。
  if (API_BALANCE_BY_PROVIDER[provider] || provider === 'custom' || provider.startsWith('api:')) {
    return {
      ok: true,
      outcome: { kind: 'failed', code: -1, message: 'API 型凭据没有签到活动' },
      unsupported: true,
    }
  }
  // qoder 家族不在分发表里（hub 刻意缺席：QoderCredential 泛型与表形态不匹配，
  // jet-hub-rpc 的 claimAll 保留特例分支直调 qoder-credits）——sidecar 同样直调，
  // 语义照抄 hub：**不发外部状态预检**（领取流程自带 loadCampaigns，幂等判据
  // replayed:true 在 claim 内部处理；「10 点刷新前看到的是昨天的活动」也由它
  // 返回 coversToday:false，Go 侧不会把昨天记成今天）；provider id 直接映射
  // product（QODER.id='qoder' / QODER_CN.id='qodercn'，与 hub qoderFamily 一致）——
  // 领取是写操作，不做双部署盲试：打错站不是查不到，是真领错账号的活动。
  // 国际版的签到端点同样走 openapi.qoder.sh，无出口代理时会失败（错误原样
  // 上行，退避重试；需要代理是部署侧配置，不在本模块猜）。
  if (provider === 'qoder' || provider === 'qodercn') {
    try {
      // 裸导入前先装解析钩子（与 distributor 同一纪律：不赌调用时序——
      // 生产上 health 握手总会先到，但测试与早到的 claim 不该靠这个活）。
      await hub.getDeps()
      const mod = await import('dsh-providers-hub/lib/qoder-credits.js')
      const products = await import('dsh-providers-hub/lib/qoder-product.js')
      const product = provider === 'qodercn' ? products.QODER_CN : products.QODER
      const outcome = await mod.claimQoderDailyCheckin(credential, product)
      return { ok: true, outcome: normalizeClaimOutcome(outcome) }
    } catch (error) {
      console.warn(`[credits.claim] ${provider} 领取异常: ${String(error)}`)
      return {
        ok: true,
        outcome: { kind: 'failed', code: -1, message: error instanceof Error ? error.message : String(error) },
      }
    }
  }
  // ⚠️ 此后的分发面 = createProviderCredits() 表；qoder 家族在上面已特例分流。
  let pc
  try {
    pc = await distributor()
  } catch (error) {
    return { ok: false, error: `签到不可用：${error instanceof Error ? error.message : String(error)}` }
  }
  const entry = pc[provider]
  if (!entry) {
    return { ok: true, outcome: { kind: 'failed', code: -1, message: `${provider} 暂不支持每日签到` }, unsupported: true }
  }
  // 「调用即判定」：claimUnavailableReason 是渠道显式登记的「不支持」，
  // claim 缺席同理 —— 都不发上游请求，计为 unsupported 让调度器跳过。
  if (typeof entry.claimUnavailableReason === 'string') {
    return { ok: true, outcome: { kind: 'failed', code: -1, message: entry.claimUnavailableReason }, unsupported: true }
  }
  if (typeof entry.claim !== 'function') {
    return { ok: true, outcome: { kind: 'failed', code: -1, message: `${provider} 不支持每日签到` }, unsupported: true }
  }
  const entryId = credentialFingerprint(provider, credential)
  let persisted = null
  const call = {
    warn: (m) => console.warn(`[credits.claim] ${provider}: ${m}`),
    persistCredential: async (_ref, value) => {
      try { persisted = JSON.parse(value) } catch { persisted = value }
    },
    accountPool: traeAccountPoolStub(),
  }
  // attempt 把「预检 → 领取 → 收尾提示」封装成可重试单元：401 自愈时换新
  // 凭据整体重跑一遍（hub 在 claimAll 入口先 healExpiredAccounts，issue
  // IKKHL5——只修余额不修领取，症状是「余额能查、领取恒 401」）。
  const attempt = async (cred) => {
  // 状态预检：precheckStatus 缺省 true（与 hub 骨架同语义）——先查状态，
  // 未开启/已签到就不发领取请求；状态查询失败照常尝试领取（「无法确认
  // 不代表不能领」，交给领取接口以响应体定夺）。
  if (entry.precheckStatus !== false) {
    const statusFn = typeof entry.claimStatus === 'function' ? entry.claimStatus : entry.fetchStatus
    if (typeof statusFn === 'function') {
      try {
        const status = await statusFn(cred)
        if (status && typeof status === 'object') {
          if (status.active === false) {
            return { ok: true, outcome: { kind: 'inactive', message: '签到活动未开启' } }
          }
          if (status.todayCheckedIn === true) {
            return { ok: true, outcome: { kind: 'already-claimed', message: '今天已签到' } }
          }
        }
      } catch {
        // 预检失败不阻断领取 —— 与 hub claimAll 的判定逐字一致。
      }
    }
  }
  try {
    const outcome = await entry.claim(cred, { id: entryId }, call)
    const res = { ok: true, outcome: normalizeClaimOutcome(outcome) }
    // 签到收尾的附加提示（hub 骨架 afterClaimNote 同款）：表里只有 trae 登记
    // （1024 打卡抽奖），对 claimed 与 already-claimed 都跑。任何异常都不得
    // 影响签到结果；note 拼接格式与 hub 逐字一致（`原文 · 提示`，claimed 本无
    // message 就直接补上）。单次快速上游调用，不会阻塞 sidecar 串行管道。
    if (typeof entry.afterClaimNote === 'function') {
      try {
        const note = await entry.afterClaimNote(credential)
        if (typeof note === 'string' && note.length > 0) {
          res.outcome.message = typeof res.outcome.message === 'string' && res.outcome.message.length > 0
            ? `${res.outcome.message} · ${note}`
            : note
        }
      } catch (error) {
        console.warn(`[credits.claim] ${provider} 收尾提示失败: ${String(error)}`)
      }
    }
    // ⚠️ afterClaim（CodeBuddy 成长中心）**刻意不接**：成长一轮单账号实测
    // 90～270s（任务间 1.5s 节流），而本 sidecar 是与推理共享的**串行 stdio
    // 管道**——一轮成长会把全部 relay 流量阻塞几分钟。hub 能跑是因为它是
    // 并发 RPC 网关。需要成长任务时去 DSH 面板手动领取（那边有并发与预算闸门）。
    if (persisted && typeof persisted === 'object') res.refreshed_credential = persisted
    return res
  } catch (error) {
    console.warn(`[credits.claim] ${provider} 领取异常: ${String(error)}`)
    return {
      ok: true,
      outcome: { kind: 'failed', code: -1, message: error instanceof Error ? error.message : String(error) },
    }
  }
  }
  let res = await attempt(credential)
  // 401 自愈（hub healExpiredAccounts 的失败后等价形态）：minimax/raccoon 的
  // token 过期时用 refresh_token 换新再试一次；新凭据随应答上行，Go 桥写回
  // 密文。多花一次失败请求，换取不做「调用前预续期」的复杂度——结果等价。
  const canSelfRefresh = provider === 'minimax' || provider === 'raccoon'
  if (
    canSelfRefresh && res.outcome?.kind === 'failed' &&
    /401|凭据失效|登录态已过期|invalid|Unauthorized/i.test(res.outcome.message ?? '')
  ) {
    try {
      const fresh = await selfRefresh(provider, credential)
      res = await attempt(fresh)
      if (fresh && typeof fresh === 'object') res.refreshed_credential = res.refreshed_credential ?? fresh
    } catch (error) {
      return {
        ok: true,
        outcome: { kind: 'failed', code: -1, message: `续期失败（${error instanceof Error ? error.message : String(error)}），请重新授权` },
      }
    }
  }
  return res
}
