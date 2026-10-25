// 设计令牌：全站唯一的一份颜色/圆角/阴影定义。
//
// ## 取向来自哪里
//
// 2026-10-25 参照 FlClash 的界面体系（`lib/ui/surface.dart`、`lib/ui/color.dart`、
// `.agents/surfaces.md`）重做了控制台观感。它那边总结出四条规律，这里是同一套：
//
//   1. **用填充画层级，不用边框。** 四个深度：page → card → block → inset。
//      每层取比自己深一层的那档，而不是各自拍脑袋挑灰。浅色靠"灰底纸卡"
//      交替（page 灰、card 近白、card 里的 block 又变灰）；深色只往上提亮
//      （在更亮的面里放更暗的一块，读起来像洞而不像层）。
//   2. **颜色留给状态。** 选中 / 开启 / 失败 / 这一屏唯一的主操作才上色。
//      结构线一律走 onSurface 的 13% 透明（不是 outline 色），这样同一条线
//      放在 page、card、chrome 上观感一致。
//   3. **字号走 iOS Dynamic Type 台阶**（15/13/12 为主，标题 24/17），
//      数字用 tabular-nums 对齐。
//   4. **阴影只给浮层**（弹窗、菜单、抽屉、仪表盘卡），列表里的卡片不加阴影。
//
// ## 为什么令牌在 TS 里而不是 CSS 里
//
// MUI 的 theme 是 JS 对象，需要真色值才能算 alpha/contrastText；而 38 个页面里
// 仍有直接写 className 的原生控件需要 CSS 变量。所以：**色值只写在 TS 里**，
// 由 main.tsx 在首次渲染前把它们写成 `--fr-*` 自定义属性，style.css 只引用变量。
// 单一致源，改一处两边都跟着变。

/** 令牌表：角色名 → 颜色（或阴影 / 尺寸）字符串。 */
export type TokenMap = Record<string, string>

/** 浅色令牌。 */
export const LIGHT: TokenMap = {
  // ── 表面层级 ────────────────────────────────────────────
  page: '#eef0f4',
  card: '#fcfcfd',
  /* 注意 block 比 page 略深（FlClash 是取同一档，但那会让「直接放在页面上的
     block 元素」隐形）。这里每档都留 ~4 点明度差，保证任何一档放到任何一档
     上面都还看得出来。 */
  block: '#e9ebf0',
  inset: '#dfe2e9',
  /** 窗口镶边：桌面侧栏与顶栏。比 page 深一档，与内容区之间留一条分界线 */
  chrome: '#e4e7ed',
  /** 浮层（弹窗/菜单/抽屉）的底色 */
  floating: '#fbfbfd',

  // ── 文字 ────────────────────────────────────────────────
  'on-surface': '#16171b',
  'on-surface-variant': '#5c5f6a',
  outline: '#90939e',

  // ── 结构与交互 ──────────────────────────────────────────
  divider: 'rgba(22, 23, 27, 0.13)',
  'divider-strong': 'rgba(22, 23, 27, 0.24)',
  hover: 'rgba(22, 23, 27, 0.05)',
  pressed: 'rgba(22, 23, 27, 0.09)',
  'focus-ring': 'rgba(79, 70, 229, 0.32)',
  primary: '#4f46e5',
  /* hover 压深一档：按钮若在悬停时颜色不变，会读成"点了没反应" */
  'primary-hover': '#4338ca',
  'on-primary': '#ffffff',
  'primary-container': '#e3e1ff',
  'on-primary-container': '#251b6e',
  'primary-soft': 'rgba(79, 70, 229, 0.10)',

  // ── 状态（语义色只取一档 + 一个容器档，避免色阶泛滥） ────
  success: '#0e7a45',
  'success-container': '#dcf5e6',
  'on-success-container': '#06371c',
  warning: '#96500a',
  'warning-container': '#ffeed3',
  'on-warning-container': '#4a2400',
  error: '#c62f36',
  'error-container': '#ffe3e3',
  'on-error-container': '#54141a',
  info: '#0b6e99',
  'info-container': '#dcf0fb',
  'on-info-container': '#063043',

  // ── 阴影：只给浮层与仪表盘卡 ─────────────────────────────
  'e1': '0 1px 2px rgba(16, 18, 26, .06), 0 2px 6px rgba(16, 18, 26, .04), 0 12px 32px -6px rgba(16, 18, 26, .10)',
  'e2': '0 1px 3px rgba(16, 18, 26, .07), 0 10px 24px rgba(16, 18, 26, .08), 0 32px 64px -12px rgba(16, 18, 26, .22)'
}

/** 深色令牌：同一批角色，只换色调（不是反色）。 */
export const DARK: TokenMap = {
  page: '#0c0d10',
  card: '#191a1e',
  block: '#212226',
  inset: '#292a2f',
  /* 深色里 chrome 只有微弱提亮就够：压得比 page 还暗会读成「洞」 */
  chrome: '#121317',
  floating: '#212226',

  'on-surface': '#f2f3f6',
  'on-surface-variant': '#b4b7c0',
  outline: '#767983',

  divider: 'rgba(242, 243, 246, 0.13)',
  'divider-strong': 'rgba(242, 243, 246, 0.26)',
  hover: 'rgba(242, 243, 246, 0.06)',
  pressed: 'rgba(242, 243, 246, 0.10)',
  'focus-ring': 'rgba(127, 134, 247, 0.40)',
  primary: '#7f86f7',
  'primary-hover': '#9aa0ff',
  'on-primary': '#0b0a1f',
  'primary-container': '#2f2d63',
  'on-primary-container': '#dcd9ff',
  'primary-soft': 'rgba(127, 134, 247, 0.16)',

  success: '#6fdda5',
  'success-container': '#12341f',
  'on-success-container': '#b8f4d0',
  warning: '#ffb968',
  'warning-container': '#3c2708',
  'on-warning-container': '#ffe3bd',
  error: '#ff8a8a',
  'error-container': '#3d1417',
  'on-error-container': '#ffdada',
  info: '#6cc5ee',
  'info-container': '#0b2f3d',
  'on-info-container': '#cfeeff',

  'e1': '0 1px 2px rgba(0, 0, 0, .50), 0 10px 28px -6px rgba(0, 0, 0, .55)',
  'e2': '0 2px 6px rgba(0, 0, 0, .50), 0 32px 64px -12px rgba(0, 0, 0, .70)'
}

/**
 * 圆角台阶（对齐 FlClash 的 AppCorner）。
 *
 * 比 MUI 默认大得多：控制台以前的 10px 在深色大面积卡片上显得"方"。
 * md 用于按钮与输入框，lg/xl 用于卡片，xxl 用于弹窗与一次性密钥页。
 */
export const RADIUS = {
  xs: 4,
  sm: 8,
  md: 12,
  lg: 16,
  xl: 20,
  xxl: 28,
  full: 999
} as const

/** 字号台阶（iOS Dynamic Type 在 14–15px 基线上的一档）。 */
export const TYPE = {
  pageTitle: { size: '1.5rem', line: 1.25, weight: 600 },
  sectionTitle: { size: '1.0625rem', line: 1.35, weight: 600 },
  rowTitle: { size: '0.9375rem', line: 1.4, weight: 600 },
  body: { size: '0.875rem', line: 1.55, weight: 400 },
  label: { size: '0.8125rem', line: 1.5, weight: 400 },
  caption: { size: '0.75rem', line: 1.45, weight: 400 }
} as const

/**
 * 字体栈（非颜色令牌，两种模式共用）。
 *
 * 与 `theme.ts` 里的 `fontFamily` 必须一致：MUI 组件走 theme，页面里的原生
 * 控件与语义类名走这里。两边都指向同一款字体（JetBrains Maple Mono），
 * 兜底也相同——否则输入框和它旁边的按钮会长得不是一套东西。
 */
const FONT_UI = [
  '"JetBrains Maple Mono"',
  'ui-monospace',
  'SFMono-Regular',
  'Menlo',
  'Consolas',
  '"PingFang SC"',
  '"Hiragino Sans GB"',
  '"Microsoft YaHei"',
  '"Noto Sans CJK SC"',
  'sans-serif'
].join(',')

/** 把某个模式的令牌写成 `<html>` 上的 CSS 自定义属性（`--fr-page` 等）。 */
export function applyCssTokens(mode: 'light' | 'dark'): void {
  const tokens = mode === 'dark' ? DARK : LIGHT
  const root = document.documentElement
  for (const [role, value] of Object.entries(tokens)) {
    root.style.setProperty(`--fr-${role}`, value)
  }
  root.style.setProperty('--fr-radius-xs', `${RADIUS.xs}px`)
  root.style.setProperty('--fr-radius-sm', `${RADIUS.sm}px`)
  root.style.setProperty('--fr-radius-md', `${RADIUS.md}px`)
  root.style.setProperty('--fr-radius-lg', `${RADIUS.lg}px`)
  root.style.setProperty('--fr-radius-xl', `${RADIUS.xl}px`)
  root.style.setProperty('--fr-radius-xxl', `${RADIUS.xxl}px`)
  root.style.setProperty('--fr-radius-full', `${RADIUS.full}px`)
  root.style.setProperty('--fr-font-ui', FONT_UI)
  root.style.setProperty('--fr-font-mono', FONT_UI)
}
