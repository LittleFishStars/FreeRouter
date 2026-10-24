// 全局主题：Material UI 的设计令牌集中地。
//
// 为什么单独一个文件而不写在 main.tsx：主题是**设计决策**（配色/圆角/字号/
// 阴影），组件是**实现**。放在一起会让「改一个颜色」变成翻组件文件；
// 分开之后所有设计令牌都在这一个文件里可查、可调。
//
// ## 设计取向
//
// 这是一个给运维用的控制台，不是营销站：
//   - **信息密度优先**：字号不放大到正文 16px 级别，表格行高压到 40px 左右，
//     让一屏能看更多行（运维实际会在列表页停留很久）。
//   - **克制的强调色**：主色用一个靛蓝做交互色，其余全靠中性灰阶拉开层次。
//     太多彩色会让「哪个是危险操作」这个判断失效——危险色必须稀缺。
//   - **深色模式是一等公民**：运维常在深夜看面板，所以调色板从一开始就按
//     「两套都要好看」设计，而不是浅色做完再补一个反色。
//   - **圆角适中（10px）**：太大显得轻浮，太小像 2010 年的后台。
//
// 颜色取自 Tailwind 的 slate/indigo 色阶（业界共识的中性灰与蓝紫），
// 这样后续想加自定义样式时，同一个色阶里已有可用的深浅变体。

import { createTheme, type ThemeOptions } from '@mui/material/styles'

/** 中性色阶（slate）：从最浅的页面底色到最深的正文色。 */
const slate = {
  50: '#f8fafc',
  100: '#f1f5f9',
  200: '#e2e8f0',
  300: '#cbd5e1',
  400: '#94a3b8',
  500: '#64748b',
  600: '#475569',
  700: '#334155',
  800: '#1e293b',
  850: '#172033',
  900: '#0f172a',
  950: '#0b1220'
}

/** 主色阶（indigo）：交互色，只用于「可点击」与「当前选中」。 */
const indigo = {
  300: '#a5b4fc',
  400: '#818cf8',
  500: '#6366f1',
  600: '#4f46e5',
  700: '#4338ca'
}

/** 语义色（成功/警告/错误）。刻意各只取一档，避免色阶泛滥。 */
const semantic = {
  ok: '#10b981',
  okDark: '#34d399',
  warn: '#f59e0b',
  warnDark: '#fbbf24',
  err: '#ef4444',
  errDark: '#f87171',
  info: '#0ea5e9',
  infoDark: '#38bdf8'
}

/**
 * 字体栈：全部用系统字体，不引入 Web Font。
 *
 * 为什么不用 Inter/Roboto 之类：① 单二进制部署不该为了字形多几百 KB 请求；
 * ② 中文场景下系统字体（PingFang/微软雅黑/Noto Sans CJK）的回退质量远好于
 * 一个 Latin-only 的 Web Font 把中文交给随机 fallback；③ 系统字体在
 * 「等宽数字」这类细节上（表格里的统计数字）表现更稳。
 */
const fontFamily = [
  '-apple-system',
  'BlinkMacSystemFont',
  '"Segoe UI"',
  'Roboto',
  '"Helvetica Neue"',
  '"PingFang SC"',
  '"Hiragino Sans GB"',
  '"Microsoft YaHei"',
  '"Noto Sans CJK SC"',
  'sans-serif'
].join(',')

const monoFamily = [
  'ui-monospace',
  'SFMono-Regular',
  '"SF Mono"',
  'Menlo',
  'Consolas',
  '"Liberation Mono"',
  'monospace'
].join(',')

/** 共享的组件默认值：两套配色都用同一份（只让颜色走 palette）。 */
const components: ThemeOptions['components'] = {
  MuiCssBaseline: {
    styleOverrides: {
      // 表格里的数字用等宽字：列对齐后一眼能扫出量级差异（0.9 与 1.1 的宽度
      // 差异在比例字体里会破坏对齐）。
      'code, kbd, pre, samp': { fontFamily: monoFamily },
      // 滚动条做细一点：控制台里内容区经常要滚，默认滚动条太占宽度。
      '*::-webkit-scrollbar': { width: 10, height: 10 },
      '*::-webkit-scrollbar-thumb': {
        backgroundColor: 'rgba(100,116,139,.35)',
        borderRadius: 8,
        border: '2px solid transparent',
        backgroundClip: 'content-box'
      },
      '*::-webkit-scrollbar-thumb:hover': { backgroundColor: 'rgba(100,116,139,.55)' },
      '*::-webkit-scrollbar-track': { backgroundColor: 'transparent' }
    }
  },
  MuiButton: {
    defaultProps: { disableElevation: true },
    styleOverrides: {
      root: {
        textTransform: 'none', // MUI 默认全大写，中文没有大小写但英文标签会显得吵
        fontWeight: 600,
        borderRadius: 8,
        paddingInline: 14
      },
      sizeSmall: { paddingInline: 10 }
    }
  },
  MuiCard: {
    defaultProps: { elevation: 0 },
    styleOverrides: {
      root: ({ theme }) => ({
        borderRadius: 12,
        border: `1px solid ${theme.palette.divider}`,
        backgroundImage: 'none' // 深色模式下 MUI 默认有渐变叠加，控制台里不需要
      })
    }
  },
  MuiPaper: {
    defaultProps: { elevation: 0 },
    styleOverrides: { root: { backgroundImage: 'none' } }
  },
  MuiTableCell: {
    styleOverrides: {
      root: ({ theme }) => ({
        // 表格行高：40px 上下，一屏能多放 2-3 行（运维会长时间看列表）。
        paddingTop: 8,
        paddingBottom: 8,
        borderColor: theme.palette.divider,
        // 窄屏：首列吸附。
        //
        // 03-ui §7 的理想做法是「宽表在手机上转卡片」，但 DataTable 的 children
        // 契约是页面直接给的 <tr>/<td>（38 个页面都这么写），转卡片要给每个
        // 单元格补列名，等于逐页重写。首列吸附是能立刻拿到的 80% 收益：
        // 横滑时你始终知道自己在看哪一行，而不是一片数字对不上号。
        [theme.breakpoints.down('md')]: {
          '&:first-of-type': {
            position: 'sticky',
            left: 0,
            zIndex: 2,
            backgroundColor: theme.palette.background.paper
          }
        }
      }),
      head: ({ theme }) => ({
        fontWeight: 600,
        fontSize: '0.78rem',
        letterSpacing: '.02em',
        textTransform: 'uppercase',
        color: theme.palette.text.secondary,
        backgroundColor: theme.palette.mode === 'dark' ? 'rgba(255,255,255,.03)' : slate[50],
        whiteSpace: 'nowrap',
        // 表头首列必须盖住正文首列（否则横滑时正文会从表头底下钻出来）
        [theme.breakpoints.down('md')]: {
          '&:first-of-type': { zIndex: 3, backgroundColor: theme.palette.mode === 'dark' ? '#151e30' : slate[50] }
        }
      })
    }
  },
  MuiChip: {
    styleOverrides: { root: { fontWeight: 600, borderRadius: 6 }, sizeSmall: { height: 22 } }
  },
  MuiAlert: {
    styleOverrides: {
      root: { borderRadius: 10, alignItems: 'flex-start' },
      message: { paddingTop: 2 }
    }
  },
  MuiTextField: { defaultProps: { size: 'small' } },
  MuiSelect: { defaultProps: { size: 'small' } },
  MuiTooltip: {
    defaultProps: { arrow: true },
    styleOverrides: { tooltip: { fontSize: '0.78rem', borderRadius: 6 } }
  },
  MuiTab: {
    styleOverrides: { root: { textTransform: 'none', fontWeight: 600, minHeight: 44 } }
  },
  MuiListItemButton: {
    styleOverrides: { root: { borderRadius: 8 } }
  },
  MuiDialog: { styleOverrides: { paper: { borderRadius: 14 } } },
  MuiAccordion: { defaultProps: { elevation: 0, disableGutters: true } }
}

/** 浅色主题。 */
export const lightTheme = createTheme({
  cssVariables: { cssVarPrefix: 'fr' },
  palette: {
    mode: 'light',
    primary: { main: indigo[600], light: indigo[400], dark: indigo[700] },
    secondary: { main: semantic.info },
    success: { main: semantic.ok },
    warning: { main: semantic.warn },
    error: { main: semantic.err },
    info: { main: semantic.info },
    background: { default: slate[50], paper: '#ffffff' },
    divider: slate[200],
    text: { primary: slate[800], secondary: slate[500], disabled: slate[400] }
  },
  shape: { borderRadius: 10 },
  typography: {
    fontFamily,
    // 控制台字号整体压一档：14px 作正文（MUI 默认 16px 在密集表格里偏大）。
    fontSize: 14,
    h1: { fontSize: '1.6rem', fontWeight: 700, letterSpacing: '-0.015em' },
    h2: { fontSize: '1.15rem', fontWeight: 650, letterSpacing: '-0.01em' },
    h3: { fontSize: '1rem', fontWeight: 650 },
    body2: { fontSize: '0.86rem' },
    caption: { fontSize: '0.76rem' },
    button: { fontSize: '0.86rem' }
  },
  components
})

/** 深色主题：同一套令牌，只换色阶（不是简单反色）。 */
export const darkTheme = createTheme({
  cssVariables: { cssVarPrefix: 'fr' },
  palette: {
    mode: 'dark',
    primary: { main: indigo[400], light: indigo[300], dark: indigo[500] },
    secondary: { main: semantic.infoDark },
    success: { main: semantic.okDark },
    warning: { main: semantic.warnDark },
    error: { main: semantic.errDark },
    info: { main: semantic.infoDark },
    // 背景不用纯黑：纯黑在 OLED 上对比过强，长时间看列表容易累。
    background: { default: slate[950], paper: slate[900] },
    divider: 'rgba(148,163,184,.16)',
    text: { primary: slate[100], secondary: slate[400], disabled: slate[600] }
  },
  shape: { borderRadius: 10 },
  typography: {
    fontFamily,
    fontSize: 14,
    h1: { fontSize: '1.6rem', fontWeight: 700, letterSpacing: '-0.015em' },
    h2: { fontSize: '1.15rem', fontWeight: 650, letterSpacing: '-0.01em' },
    h3: { fontSize: '1rem', fontWeight: 650 },
    body2: { fontSize: '0.86rem' },
    caption: { fontSize: '0.76rem' },
    button: { fontSize: '0.86rem' }
  },
  components
})

/** 深浅模式偏好键（localStorage）。 */
export const MODE_STORAGE_KEY = 'fr-color-mode'
