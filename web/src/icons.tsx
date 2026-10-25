// 全站图标层：唯一的图标来源与唯一的风格参数。
//
// ## 为什么自己封一层而不是到处 `<IconXxx />`
//
// 图标的"风格统一"不是选一个库就自动成立的，它由四个参数决定：
//
//   1. **库与线型**：一个界面里只能有一种画法。混用实心（Material Icons 的
//      默认形态）与线性（Tabler / Lucide）会让一列图标看起来像两个时代的东西。
//   2. **线宽**：24 视口下 2px 是"描边"，1.5px 是"细线"。同一个尺寸档里混用
//      会显得有的字重有的字轻。
//   3. **端点与拐角**：round（圆头圆角）比 butt/miter 更"软"，后者在大圆角
//      卡片旁边会显得扎人。
//   4. **尺寸档**：只允许几档，且与字号对齐，不允许 13/14/15/17/18 这种随手值。
//
// 所以：**所有图标必须经由本文件导出**，调用方只给语义尺寸（SIZE.xs/sm/md/lg）
// 与颜色（默认继承 currentColor），不允许再写 `<IconXxx size={13} stroke={1.8} />`。
//
// ## 许可证
//
// 唯一来源是 **Tabler Icons（MIT）**：`@tabler/icons-react` v3，6239 个图标，
// 全部 24×24 视口、2px 描边、圆头圆角。MIT 允许商用、修改、再分发，只要求保留
// 版权声明——它在 `node_modules/@tabler/icons-react/LICENSE`，随包分发。
//
// 曾经的 `@mui/icons-material`（同为 MIT）已移除：它的默认形态是**实心**填充，
// 与本文件的线性风格冲突；同一个界面里两套画法，正是"风格不统一"的来源。

import type { ComponentType } from 'react'

import * as TB from '@tabler/icons-react'

/** Tabler 图标组件的公共形状（它的 props 里有 size/stroke/color，这里只留 size/color）。 */
export interface IconGlyphProps {
  /** 语义尺寸档或像素值。语义档见 ICON_SIZE。 */
  size?: IconSize | number
  color?: string
  stroke?: number
  className?: string
  'aria-hidden'?: boolean | 'true' | 'false'
}

/**
 * 尺寸档：只允许四档，且对齐字号。
 *
 *   xs 14 —— 行内（徽章里的锁、面包屑分隔符）
 *   sm 16 —— 紧凑控件（表格操作按钮、Chip 前缀）
 *   md 18 —— 默认（导航项、工具栏按钮、输入框前缀）
 *   lg 22 —— 顶栏主按钮、空态插图
 */
export const ICON_SIZE = { xs: 14, sm: 16, md: 18, lg: 22 } as const

/** 线宽：24 视口下 1.75 比 Tabler 默认的 2 略细，与 14px 正文的笔画更搭。 */
export const ICON_STROKE = 1.75

export type IconSize = keyof typeof ICON_SIZE


/**
 * 包一层统一默认值：所有图标都从这里出，尺寸默认 md、线宽统一、aria 默认隐藏。
 *
 * 为什么图标默认 `aria-hidden`：图标几乎总是**伴随文字**（按钮文字、导航项
 * 文字、表格列头）出现，此时读屏不该把它念第二遍。纯图标按钮（icon-only）
 * 必须自己给 `aria-label`——见 `ui.tsx` 的 `IconButton`。
 */
function glyph(Base: ComponentType<{ size?: number; stroke?: number; color?: string; className?: string; 'aria-hidden'?: boolean | 'true' | 'false' }>) {
  /**
   * 语义档 → 像素。**这一步不能省**：Tabler 把 `size` 直接写成 SVG 的
   * width/height，传字符串 `"md"` 会得到 `width="md"`——属性非法，浏览器
   * 按 0 处理，图标整片消失（这个坑实测踩过一次，页面上只剩文字和间距）。
   */
  const Wrapped = ({ size = 'md', stroke = ICON_STROKE, ...rest }: IconGlyphProps) => (
    <Base
      size={typeof size === 'number' ? size : ICON_SIZE[size]}
      stroke={stroke}
      aria-hidden="true"
      {...rest}
    />
  )
  Wrapped.displayName = `Glyph(${Base.displayName ?? 'icon'})`
  return Wrapped
}

/* ── 导航与信息架构 ─────────────────────────────────────────────── */

export const IconNavHome = glyph(TB.IconHome)
export const IconNavContribute = glyph(TB.IconPlug)
export const IconNavAccount = glyph(TB.IconUser)
export const IconUser = IconNavAccount
export const IconNavAdminReview = glyph(TB.IconClipboardCheck)
export const IconNavInbox = glyph(TB.IconInbox)
export const IconNavAccounts = glyph(TB.IconUsers)
export const IconNavGroups = glyph(TB.IconFolders)
export const IconNavRealModels = glyph(TB.IconDatabase)
export const IconNavAbstractModels = glyph(TB.IconRoute)
export const IconNavUpstreams = glyph(TB.IconDevices)
export const IconNavCredentials = glyph(TB.IconKey)
export const IconNavVerifications = glyph(TB.IconShieldCheck)
export const IconNavAudit = glyph(TB.IconFileAnalytics)
export const IconNavSettings = glyph(TB.IconSettings)
export const IconNavUsage = glyph(TB.IconChartBar)
export const IconNavKeys = glyph(TB.IconKey)
export const IconNavLogs = glyph(TB.IconListDetails)
export const IconNavModels = glyph(TB.IconBox)
export const IconNavContributions = glyph(TB.IconGift)
export const IconNavSessions = glyph(TB.IconDevices)
export const IconNavPassword = glyph(TB.IconLock)

/* ── 通用动作 ───────────────────────────────────────────────────── */

export const IconRefresh = glyph(TB.IconRefresh)
export const IconPlus = glyph(TB.IconPlus)
export const IconEdit = glyph(TB.IconPencil)
export const IconDelete = glyph(TB.IconTrash)
export const IconView = glyph(TB.IconEye)
export const IconCopy = glyph(TB.IconCopy)
export const IconDownload = glyph(TB.IconDownload)
export const IconUpload = glyph(TB.IconUpload)
export const IconSearch = glyph(TB.IconSearch)
export const IconCheck = glyph(TB.IconCheck)
export const IconClose = glyph(TB.IconX)
export const IconUp = glyph(TB.IconArrowUp)
export const IconDown = glyph(TB.IconArrowDown)
export const IconBack = glyph(TB.IconArrowLeft)
export const IconExternal = glyph(TB.IconExternalLink)
export const IconPower = glyph(TB.IconPower)
export const IconPlay = glyph(TB.IconPlayerPlay)
export const IconTest = glyph(TB.IconActivity)
export const IconClear = glyph(TB.IconEraser)
export const IconTune = glyph(TB.IconAdjustments)

/* ── 外壳与状态 ─────────────────────────────────────────────────── */

export const IconMenu = glyph(TB.IconMenu2)
export const IconSun = glyph(TB.IconSun)
export const IconMoon = glyph(TB.IconMoon)
export const IconLogout = glyph(TB.IconLogout)
export const IconLogin = glyph(TB.IconLogin)
export const IconChevronRight = glyph(TB.IconChevronRight)
export const IconChevronDown = glyph(TB.IconChevronDown)
export const IconLock = glyph(TB.IconLock)
export const IconOk = glyph(TB.IconCircleCheck)
export const IconWarn = glyph(TB.IconAlertTriangle)
export const IconAlert = glyph(TB.IconAlertCircle)
export const IconInfo = glyph(TB.IconInfoCircle)
export const IconIdle = glyph(TB.IconAdjustments)

/**
 * 状态图标表：`StatusBadge` 与各处状态提示共用。
 *
 * 为什么状态必须有图标而不只是颜色：色盲用户单靠颜色分不清四态（设计文档
 * 第 8 节的无障碍要求）；而且"绿点"在一屏几十行里并不比一个符号更容易扫。
 */
/**
 * Alert / 横幅的 severity → 图标。
 *
 * MUI 的 `Alert` **自带** Material Icons 的实心图标（圆底 info/exclamation）。
 * 不显式传 `icon` 的话，模块内每个横幅都会在满屏线性图标里插进一个实心图形——
 * 这正是"两套画法并存"的来源。所有 `<Alert>` 调用点都从这里取图标。
 */
export const ALERT_GLYPH = {
  success: IconOk,
  info: IconInfo,
  warning: IconWarn,
  error: IconAlert
} as const

export const STATUS_GLYPH = {
  ok: IconOk,
  warn: IconWarn,
  err: IconClose,
  info: IconInfo,
  muted: IconIdle
} as const

/** 成员数（组行卡）：复用导航账户同款人形图标，语义是"这个组里有几个人"。 */
export const IconMembers = glyph(TB.IconUsers)
