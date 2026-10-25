// 全局主题：Material UI 的设计令牌集中地。
//
// 为什么单独一个文件而不写在 main.tsx：主题是**设计决策**（配色/圆角/字号/
// 阴影），组件是**实现**。放在一起会让「改一个颜色」变成翻组件文件；
// 分开之后所有设计令牌都在这一个文件里可查、可调。色值本身在 tokens.ts。
//
// ## 设计取向（2026-10-25 参照 FlClash 重做）
//
// 这是一个给运维用的控制台，不是营销站，但"工具感"不等于"到处画框"：
//
//   - **用填充画层级，不用边框。** 卡片不再有 1px 描边，靠"比所在面亮一档"
//     站起来（浅色：灰页面 + 近白卡片；深色：深页面 + 提亮的卡片）。结构线
//     只在必须的地方出现——表格行之间、chrome 与内容的交界——且一律是
//     onSurface 的 13% 透明，不随所在面变色。
//   - **颜色只给状态。** 主色只出现在选中、开启、这一屏唯一的主动作上。
//     危险色必须稀缺，否则"哪个是危险操作"这个判断失效。
//   - **信息密度优先但字不缩水**：正文 14px、次要信息 12–13px，行高放宽到
//     1.55；表格行高 42px 上下，一屏仍然看得到十几行。
//   - **深色模式是一等公民**：从一开始就按"两套都要好看"设计，而不是浅色
//     做完再补一个反色。深色只往上提亮，不往暗里挖（挖下去读起来像洞）。
//   - **圆角比 MUI 默认大一档**：卡片 20px、按钮 12px、弹窗 28px。大圆角在
//     大面积深色卡片上不会显得方，这是 FlClash 与 iOS 27 的共同选择。

import { createTheme, alpha, type ThemeOptions } from '@mui/material/styles'
import { DARK, LIGHT, RADIUS, TYPE } from './tokens'

/** 字体栈：全部用系统字体，不引入 Web Font。 */
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

/**
 * 两套配色共享的组件默认值。
 *
 * 只有颜色走 palette / 令牌，形状与字重在这里一次定死——避免出现
 * "某个页面自己把卡片圆角改成 8px"这类漂移。
 */
function buildComponents(t: Record<string, string>, mode: 'light' | 'dark'): ThemeOptions['components'] {
  const hover = t.hover
  return {
    MuiCssBaseline: {
      styleOverrides: {
        'code, kbd, pre, samp': { fontFamily: monoFamily },
        '*::-webkit-scrollbar': { width: 10, height: 10 },
        '*::-webkit-scrollbar-thumb': {
          backgroundColor: alpha(t['on-surface-variant'], 0.28),
          borderRadius: 8,
          border: '2px solid transparent',
          backgroundClip: 'content-box'
        },
        '*::-webkit-scrollbar-thumb:hover': { backgroundColor: alpha(t['on-surface-variant'], 0.45) },
        '*::-webkit-scrollbar-track': { backgroundColor: 'transparent' }
      }
    },
    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: {
        root: {
          textTransform: 'none',
          fontWeight: 600,
          fontSize: TYPE.body.size,
          borderRadius: RADIUS.md,
          paddingInline: 14,
          minHeight: 34
        },
        sizeSmall: { paddingInline: 10, minHeight: 30, borderRadius: RADIUS.sm + 2 },
        // 描边按钮不再画满一圈实色边：只留 1px 的 onSurface 13%，避免和填充层级打架
        outlined: {
          borderColor: t.divider,
          '&:hover': { borderColor: alpha(t['on-surface'], 0.28), backgroundColor: hover }
        },
        text: { '&:hover': { backgroundColor: hover } }
      }
    },
    MuiCard: {
      // 卡片没有边框、没有阴影：层级完全由底色承担（FlClash 的核心手法）
      defaultProps: { elevation: 0 },
      styleOverrides: {
        root: {
          borderRadius: RADIUS.xl,
          border: 'none',
          backgroundColor: t.card,
          backgroundImage: 'none',
          boxShadow: 'none',
          overflow: 'hidden'
        }
      }
    },
    MuiPaper: {
      defaultProps: { elevation: 0 },
      styleOverrides: { root: { backgroundImage: 'none', backgroundColor: t.card } }
    },
    MuiTableCell: {
      styleOverrides: {
        root: {
          paddingTop: 11,
          paddingBottom: 11,
          paddingInline: 14,
          borderBottom: `1px solid ${t.divider}`,
          fontSize: TYPE.label.size
        },
        head: {
          // FlClash 的表头不铺底色、不写全大写：靠字重和次要色分层
          fontWeight: 600,
          fontSize: TYPE.caption.size,
          letterSpacing: '0.01em',
          textTransform: 'none',
          color: t['on-surface-variant'],
          backgroundColor: 'transparent',
          borderBottom: `1px solid ${t.divider}`,
          whiteSpace: 'nowrap',
          height: 40
        },
        sizeSmall: { paddingTop: 9, paddingBottom: 9 }
      }
    },
    MuiTableRow: {
      styleOverrides: {
        root: {
          // 悬停整行提示（FlClash 表格给交替行抬升底色；控制台的表格更长、
          // 更需要"扫"，所以改成悬停才给）
          '&:hover': { backgroundColor: hover },
          '&:last-child td': { borderBottom: 'none' }
        }
      }
    },
    MuiTableContainer: { styleOverrides: { root: { borderRadius: RADIUS.lg } } },
    MuiChip: {
      styleOverrides: {
        root: { fontWeight: 600, borderRadius: RADIUS.full, fontSize: TYPE.caption.size },
        sizeSmall: { height: 24, '& .MuiChip-label': { paddingInline: 10 } },
        // 语义色用"容器档"：底色浅、文字深，像 iOS 的 tinted badge，
        // 而不是 MUI 默认那种整块饱和色（一屏几十个徽章时会糊成一片）
        colorSuccess: { backgroundColor: t['success-container'], color: t['on-success-container'] },
        colorWarning: { backgroundColor: t['warning-container'], color: t['on-warning-container'] },
        colorError: { backgroundColor: t['error-container'], color: t['on-error-container'] },
        colorInfo: { backgroundColor: t['info-container'], color: t['on-info-container'] },
        colorPrimary: { backgroundColor: t['primary-container'], color: t['on-primary-container'] },
        colorDefault: {
          backgroundColor: 'transparent',
          border: `1px solid ${t.divider}`,
          color: t['on-surface-variant']
        }
      }
    },
    MuiAlert: {
      styleOverrides: {
        message: { paddingTop: 1, fontSize: TYPE.body.size },
        // MUI v9 的 Alert 没有 standardSuccess 这类槽位名，用类名选择器接管：
        // 语义色一律走「容器底色 + 深色文字」，而不是整块饱和色。
        root: {
          borderRadius: RADIUS.lg,
          alignItems: 'flex-start',
          paddingBlock: 10,
          '&.MuiAlert-standardSuccess': {
            backgroundColor: t['success-container'],
            color: t['on-success-container']
          },
          '&.MuiAlert-standardWarning': {
            backgroundColor: t['warning-container'],
            color: t['on-warning-container']
          },
          '&.MuiAlert-standardError': {
            backgroundColor: t['error-container'],
            color: t['on-error-container']
          },
          '&.MuiAlert-standardInfo': {
            backgroundColor: t['info-container'],
            color: t['on-info-container']
          },
          '&.MuiAlert-outlined': { borderColor: t.divider }
        },
        icon: { opacity: 0.85, paddingTop: 3 }
      }
    },
    MuiTextField: { defaultProps: { size: 'small' } },
    MuiSelect: { defaultProps: { size: 'small' } },
    MuiOutlinedInput: {
      styleOverrides: {
        root: {
          borderRadius: RADIUS.md,
          // 输入框用"填充"而不是"描边"：一档比所在面深（浅色）/ 亮（深色）
          backgroundColor: mode === 'dark' ? t.block : t.inset,
          transition: 'box-shadow .12s ease, background-color .12s ease',
          '& .MuiOutlinedInput-notchedOutline': { borderColor: 'transparent' },
          '&:hover .MuiOutlinedInput-notchedOutline': { borderColor: alpha(t['on-surface'], 0.22) },
          '&.Mui-focused .MuiOutlinedInput-notchedOutline': {
            borderColor: t.primary,
            borderWidth: 2
          }
        },
        input: { fontSize: TYPE.body.size, paddingBlock: 9 }
      }
    },
    MuiInputLabel: { styleOverrides: { root: { fontSize: TYPE.body.size } } },
    MuiTooltip: {
      defaultProps: { arrow: true },
      styleOverrides: {
        tooltip: {
          fontSize: TYPE.caption.size,
          borderRadius: RADIUS.sm,
          padding: '6px 10px',
          backgroundColor: mode === 'dark' ? t.inset : t['on-surface'],
          color: mode === 'dark' ? t['on-surface'] : t.card
        },
        arrow: { color: mode === 'dark' ? t.inset : t['on-surface'] }
      }
    },
    MuiTab: {
      styleOverrides: {
        root: {
          textTransform: 'none',
          fontWeight: 600,
          minHeight: 44,
          fontSize: TYPE.body.size,
          color: t['on-surface-variant']
        }
      }
    },
    MuiTabs: { styleOverrides: { indicator: { height: 3, borderRadius: 3, backgroundColor: t.primary } } },
    MuiListItemButton: { styleOverrides: { root: { borderRadius: RADIUS.md, '&:hover': { backgroundColor: hover } } } },
    MuiMenuItem: {
      styleOverrides: { root: { fontSize: TYPE.body.size, minHeight: 38, '&:hover': { backgroundColor: hover } } }
    },
    MuiMenu: {
      styleOverrides: {
        paper: {
          borderRadius: RADIUS.lg,
          border: `1px solid ${t.divider}`,
          boxShadow: t.e2,
          backgroundColor: t.floating,
          backgroundImage: 'none'
        }
      }
    },
    MuiDialog: {
      styleOverrides: {
        paper: {
          borderRadius: RADIUS.xxl,
          boxShadow: t.e2,
          backgroundColor: t.floating,
          backgroundImage: 'none'
        }
      }
    },
    MuiDrawer: { styleOverrides: { paper: { backgroundColor: t.chrome, backgroundImage: 'none' } } },
    MuiAccordion: {
      defaultProps: { elevation: 0, disableGutters: true },
      styleOverrides: {
        root: { backgroundColor: 'transparent', backgroundImage: 'none', '&:before': { display: 'none' } }
      }
    },
    MuiDivider: { styleOverrides: { root: { borderColor: t.divider } } },
    MuiLinearProgress: {
      styleOverrides: {
        root: { height: 6, borderRadius: RADIUS.full, backgroundColor: t.inset },
        bar: { borderRadius: RADIUS.full }
      }
    },
    MuiSwitch: { styleOverrides: { track: { borderRadius: RADIUS.full } } },
    MuiSkeleton: {
      styleOverrides: { root: { backgroundColor: alpha(t['on-surface-variant'], 0.12), borderRadius: RADIUS.md } }
    },
    MuiAvatar: { styleOverrides: { root: { fontSize: '0.82rem', fontWeight: 700 } } },
    MuiBreadcrumbs: { styleOverrides: { separator: { color: t.outline }, li: { fontSize: TYPE.caption.size } } },
    MuiLink: { defaultProps: { underline: 'hover' } }
  }
}

/** 由令牌表造一个主题（浅/深只是 token 不同）。 */
function buildTheme(mode: 'light' | 'dark') {
  const t = mode === 'dark' ? DARK : LIGHT
  return createTheme({
    cssVariables: { cssVarPrefix: 'fr' },
    palette: {
      mode,
      primary: {
        main: t.primary,
        light: mode === 'dark' ? '#9aa0ff' : '#6a62ec',
        dark: mode === 'dark' ? '#5f66e0' : '#3d34c9',
        contrastText: t['on-primary']
      },
      secondary: { main: t.info },
      success: { main: t.success, light: t.success, dark: t.success, contrastText: t['on-success-container'] },
      warning: { main: t.warning, light: t.warning, dark: t.warning, contrastText: t['on-warning-container'] },
      error: { main: t.error, light: t.error, dark: t.error, contrastText: t['on-error-container'] },
      info: { main: t.info, light: t.info, dark: t.info, contrastText: t['on-info-container'] },
      background: { default: t.page, paper: t.card },
      divider: t.divider,
      text: { primary: t['on-surface'], secondary: t['on-surface-variant'], disabled: t.outline },
      action: {
        hover: t.hover,
        selected: t['primary-soft'],
        disabled: t.outline,
        disabledBackground: t.inset
      }
    },
    shape: { borderRadius: RADIUS.md },
    typography: {
      fontFamily,
      fontSize: 14,
      // 标题：字重 600 + 负字距——比 MUI 默认的 500/正字距"紧"
      h1: {
        fontSize: TYPE.pageTitle.size,
        lineHeight: TYPE.pageTitle.line,
        fontWeight: TYPE.pageTitle.weight,
        letterSpacing: '-0.022em'
      },
      h2: {
        fontSize: TYPE.sectionTitle.size,
        lineHeight: TYPE.sectionTitle.line,
        fontWeight: TYPE.sectionTitle.weight,
        letterSpacing: '-0.012em'
      },
      h3: { fontSize: TYPE.rowTitle.size, lineHeight: TYPE.rowTitle.line, fontWeight: TYPE.rowTitle.weight },
      body1: { fontSize: TYPE.body.size, lineHeight: TYPE.body.line },
      body2: { fontSize: TYPE.label.size, lineHeight: TYPE.label.line },
      caption: { fontSize: TYPE.caption.size, lineHeight: TYPE.caption.line },
      button: { fontSize: TYPE.body.size, fontWeight: 600 },
      overline: { fontSize: '0.6875rem', fontWeight: 700, letterSpacing: '0.08em' }
    },
    components: buildComponents(t, mode)
  })
}

/** 浅色主题。 */
export const lightTheme = buildTheme('light')

/** 深色主题：同一套令牌，只换色阶（不是简单反色）。 */
export const darkTheme = buildTheme('dark')

/** 深浅模式偏好键（localStorage）。 */
export const MODE_STORAGE_KEY = 'fr-color-mode'

/**
 * 表面层级（MUI palette 里没有 block/inset/chrome 这几档）。
 *
 * 组件里写 `bgcolor: SURFACE.block` 而不是硬编码 rgba：这样深色模式自动跟着
 * 令牌走，不需要每个调用点判断 `mode === 'dark'`——那正是 FlClash 明确否掉的
 * 反模式（"在调用点按位置挑底色"）。
 */
export const SURFACE = {
  page: 'var(--fr-page)',
  card: 'var(--fr-card)',
  block: 'var(--fr-block)',
  inset: 'var(--fr-inset)',
  chrome: 'var(--fr-chrome)',
  floating: 'var(--fr-floating)'
} as const

/** 阴影档（只给浮层与仪表盘卡）。 */
export const ELEVATION = { e1: 'var(--fr-e1)', e2: 'var(--fr-e2)' } as const
