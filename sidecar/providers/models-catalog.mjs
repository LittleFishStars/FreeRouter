// 反代供应商的静态模型目录（统一入口）。
//
// 数据源：dsh-providers-hub 各家 product 模块的 fallbackModels——**逐字段
// 对齐上游客户端实测**（hub 注记：改表必须重新对照官方 catalog，不要凭
// 印象填），shim 里动态 import 即可，零拷贝。反代型没有 OpenAI 兼容的
// /models 可探测，这条静态目录就是模型卡的数据来源。
//
// 形状：[{ id, display_name, context_window? }]
// 变体（qoder/qodercn、minimax/minimaxintl、autoclaw/autoclawintl、
// buddy/workbuddy）各自的表独立取。

const HUB = 'dsh-providers-hub/lib'

async function pick(mod, exportName) {
  const arr = mod?.[exportName]?.fallbackModels ?? mod?.[exportName] ?? null
  return Array.isArray(arr) ? arr : null
}

const CATALOG_BY_PROVIDER = {
  qoder: async () => pick(await import(`${HUB}/qoder-product.js`), 'QODER'),
  qodercn: async () => pick(await import(`${HUB}/qoder-product.js`), 'QODER_CN'),
  trae: async () => pick(await import(`${HUB}/trae-product.js`), 'TRAE'),
  loomy: async () => pick(await import(`${HUB}/loomy-product.js`), 'LOOMY'),
  minimax: async () => pick(await import(`${HUB}/minimax-product.js`), 'MINIMAX'),
  minimaxintl: async () => pick(await import(`${HUB}/minimax-product.js`), 'MINIMAX_INTL'),
  astudio: async () => pick(await import(`${HUB}/astudio-product.js`), 'ASTUDIO'),
  autoclaw: async () => pick(await import(`${HUB}/autoclaw-product.js`), 'AUTOCLAW'),
  autoclawintl: async () => pick(await import(`${HUB}/autoclaw-product.js`), 'AUTOCLAW_INTL'),
  cline: async () => pick(await import(`${HUB}/cline-product.js`), 'CLINE'),
  codely: async () => pick(await import(`${HUB}/codely-product.js`), 'CODERLY_FALLBACK_MODELS'),
  accio: async () => pick(await import(`${HUB}/accio-product.js`), 'ACCIO'),
  buddy: async () => pick(await import(`${HUB}/product.js`), 'CODEBUDDY'),
  workbuddy: async () => pick(await import(`${HUB}/product.js`), 'WORKBUDDY'),
  lobsterai: async () => pick(await import(`${HUB}/lobsterai-product.js`), 'LOBSTERAI'),
}

/** 支持静态目录的 provider 清单。 */
export function catalogSupportedProviders() {
  return Object.keys(CATALOG_BY_PROVIDER)
}

/**
 * 取一家的静态模型目录。
 * @returns [{id, display_name, context_window?}]；不支持/无表 → []。
 */
export async function modelCatalog(provider) {
  const loader = CATALOG_BY_PROVIDER[provider]
  if (!loader) return []
  let arr
  try {
    arr = await loader()
  } catch {
    return [] // shim 未就绪等运行态问题：目录可自愈（下次刷新再来）
  }
  if (!Array.isArray(arr)) return []
  const out = []
  for (const m of arr) {
    if (typeof m?.id !== 'string' || m.id.length === 0) continue
    out.push({
      id: m.id,
      display_name: typeof m.name === 'string' && m.name ? m.name : m.id,
      ...(typeof m.contextWindow === 'number' && Number.isFinite(m.contextWindow)
        ? { context_window: m.contextWindow }
        : {}),
    })
  }
  return out
}
