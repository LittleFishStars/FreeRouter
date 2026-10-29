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
  return { ok: false, error: '余额应答形状不符合预期' }
}

/**
 * 查一个凭据的余额/积分。
 *
 * @param credential 已解密的凭据对象（JSON.parse 后的明文形状——与
 *   oauth.exchange 的产出同源，各家字段天然匹配）。
 */
export async function queryCreditBalance(provider, credential) {
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
  try {
    if (typeof entry.fetchBalanceDetailed === 'function') {
      return normalize(await entry.fetchBalanceDetailed(credential))
    }
    if (typeof entry.fetchBalance === 'function') {
      return normalize(await entry.fetchBalance(credential))
    }
    return { ok: false, error: `${provider} 未实现余额查询` }
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
