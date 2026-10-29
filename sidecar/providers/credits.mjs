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
// ⚠️ 动态 import 的时机：shim 就绪（loadDeps）之后才解析得了——credits.query
// 都在 OAuth 装配完成后才会被调（管理面按钮），时序天然满足；缓存首次结果。

let providerCredits = null
let loadAttempted = false

async function distributor() {
  if (providerCredits) return providerCredits
  if (loadAttempted) throw new Error('hub 分发器加载失败（已尝试过）')
  loadAttempted = true
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

async function balanceBigModel(key, baseUrl) {
  const r = await getJson(`${baseUrl}/finance/balance`, key)
  if (r.http === 401) return { ok: false, error: '凭据失效（401）' }
  const d = r.body?.data ?? {}
  const total = readNumLoose(d.total_balance)
  if (total === undefined) return shapeErr('该渠道', r)
  const gift = readNumLoose(d.gift_balance)
  const purchase = readNumLoose(d.purchased_balance)
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
    // minimax 静默续期：401（token 过期）时用 refresh_token 换新后重查一次。
    // 新凭据随应答上行（refreshed_credential），Go 桥原地写回密文。
    if (
      provider === 'minimax' && !first.ok &&
      /401|凭据失效|invalid/i.test(first.error ?? '')
    ) {
      try {
        const fresh = await refreshMinimaxCredential(credential)
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
