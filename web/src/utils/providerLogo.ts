// providerLogo —— 供应商图标的统一解析（2026-11-01）。
//
// 为什么要抽出来：候选集编辑器的模型卡要显示「供应商 logo + 名字」，而
// 同一套 logo 资源在 ContributePage 里已经有一套解析（含 LOGO_ALIAS 与
// 字母兜底）。两处各写一份必然漂移——新增一个供应商时只改一处、另一处
// 静默回落成字母图标，那种"某个供应商突然没图标了"的故障最难查。
//
// ## 资源布局（web/public/logos/）
//
//	logos/<provider>.svg|png      主品牌（反代/OAuth 类，hub 给的那批）
//	logos/api/<provider>.ico      官方 favicon（API 类供应商，vendored）
//
// `import.meta.glob` 让 vite 在**构建期**枚举磁盘上真实存在的文件：
// 清单里没有的 id 不会被请求，也不会产生 404 噪声。缺图标时回落成
// 「首字母 + 按 id hash 的底色」内联 SVG（与反代卡片的字母占位同款）。
//
// ## 为什么 id 先查别名再查原名
//
// hub 只给主品牌一份 logo（minimaxintl → minimax），本地别名表就是那条
// 事实的固化。

/** 国际版变体复用主品牌图标（hub 只给主品牌一份 logo）。 */
const LOGO_ALIAS: Record<string, string> = {
  minimaxintl: 'minimax',
  autoclawintl: 'autoclaw'
}

// 图标资源的三层目录（2026-11-01 起接入 Tingly Box 同源的 lobehub 图标）：
//   logos/vendors/<provider-id>.svg   —— catalog 55 家的**品牌彩色**图标
//                                       （@lobehub/icons-static-svg，MIT；
//                                       映射表沿用 Tingly Box 的
//                                       ProviderIcon.tsx，见目录内 LICENSE.txt）
//   logos/<provider>.svg|png          —— 主品牌（反代/OAuth 类，hub 给的那批）
//   logos/api/<provider>.ico|png      —— API 供应商的官方 favicon（vendored）
//   logos/vendors/fam-<family>.svg    —— vendor_family 聚合名，最后的静态兜底
const VENDOR_SVG = import.meta.glob('/public/logos/vendors/*.svg', { eager: true, query: '?url', import: 'default' }) as Record<string, string>
const MAIN_SVG = import.meta.glob('/public/logos/*.svg', { eager: true, query: '?url', import: 'default' }) as Record<string, string>
const MAIN_PNG = import.meta.glob('/public/logos/*.png', { eager: true, query: '?url', import: 'default' }) as Record<string, string>
const API_ICON = import.meta.glob('/public/logos/api/*.*', { eager: true, query: '?url', import: 'default' }) as Record<string, string>

/** 把 import.meta.glob 的路径表折成 id → url。取路径尾段的文件名主干。 */
function indexByID(map: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [path, url] of Object.entries(map)) {
    const m = path.match(/logos\/(?:api\/|vendors\/)?([a-z0-9-]+)\.(svg|png|ico)$/)
    if (m) out[m[1]] = url
  }
  return out
}

const VENDOR_BY_ID = indexByID(VENDOR_SVG)
const MAIN_BY_ID = { ...indexByID(MAIN_SVG), ...indexByID(MAIN_PNG) }
const API_BY_ID = indexByID(API_ICON)

/**
 * 静态兜底：provider id 与名字都查不到图时，先试「厂商族」（fam-<family>）。
 * family 由 id 前缀推断——目录 id 的命名规则就是「slug[-变体]」，slug 与
 * family 高度同源（bigmodel-cn → bigmodel→zhipu 那类映射在 vendors 里已经
 * 按 catalog id 落了盘，这里只兜「新 id 还没进图标目录」的窗口期）。
 */
function familyOf(id: string): string {
  const m = id.match(/^([a-z0-9]+?)(?:-(?:com|cn|ai|io|xyz|intl|coding|token|oauth))$/)
  return m ? m[1] : ''
}

/** 字母兜底的底色档（与反代卡片的字母占位一致，风格统一）。 */
const FALLBACK_HUES = [212, 262, 330, 18, 42, 96, 152, 190]

/**
 * logoURL 返回供应商图标 URL；找不到资源时返回内联字母 SVG（永不 404）。
 *
 * providerID 是目录 id（如 `deepseek-com` / `bigmodel-cn`），name 用于
 * 字母兜底的第一个字符。
 */
export function logoURL(providerID: string, name: string): string {
  return logoInfo(providerID, name).url
}

/** logoInfo 同时返回「这是不是字母兜底」——卡片据此决定要不要给它垫底色。
 *
 * 为什么需要这个标志：真图（.ico/.png）常是浅底，在深色卡片上需要一层容器
 * 色托住；而字母兜底自带圆角底色块，再垫一层会叠成双色边（实测很难看）。 */
export function logoInfo(providerID: string, name: string): { url: string; fallback: boolean } {
  // 候选 id 的优先序（命中即返回）：
  //   1. provider_id（目录 id，如 deepseek-com / bigmodel-cn）——最权威；
  //   2. 上游名（如 bigmodel）——自建/无凭据上游没有 provider_id（它是从
  //      凭据的 api:<id> 剥出来的），只能用上游名；
  //   3. 上游名 + "-cn"/"-com" 后缀变体——目录 id 常带地区后缀，而上游名
  //      是人起的短名（bigmodel → bigmodel-cn）。这一条是实测踩到的：
  //      少了它，自建上游的卡片全退化成字母图标。
  const cands = [
    providerID,
    name,
    `${name}-cn`,
    `${name}-com`,
    `${name}-coding`
  ].filter((x) => x !== '')

  for (const raw of cands) {
    const id = LOGO_ALIAS[raw] ?? raw
    const hit = VENDOR_BY_ID[id] ?? MAIN_BY_ID[id] ?? API_BY_ID[id]
    if (hit) return { url: hit, fallback: false }
  }
  // 厂商族兜底（ vendors/fam-*.svg ），仍属「有真图」。
  for (const raw of cands) {
    const fam = familyOf(LOGO_ALIAS[raw] ?? raw)
    if (fam) {
      const hit = VENDOR_BY_ID[`fam-${fam}`]
      if (hit) return { url: hit, fallback: false }
    }
  }
  const id = LOGO_ALIAS[providerID] ?? providerID

  // 字母兜底：首字母 + 按 id hash 的底色。hash 用乘法累积而不是取模字符
  // 和——后者会让 an 与 na 撞同一个颜色，看起来像"这两个是同一家"。
  let hash = 0
  for (const ch of id) hash = (hash * 31 + ch.charCodeAt(0)) >>> 0
  const hue = FALLBACK_HUES[hash % FALLBACK_HUES.length]
  const letter = (name.trim()[0] ?? '?').toUpperCase()
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48">` +
    `<rect width="48" height="48" rx="10" fill="hsl(${hue} 62% 46%)"/>` +
    `<text x="24" y="33" font-family="system-ui,sans-serif" font-size="26" ` +
    `font-weight="700" fill="#fff" text-anchor="middle">${letter}</text></svg>`
  return { url: `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`, fallback: true }
}
