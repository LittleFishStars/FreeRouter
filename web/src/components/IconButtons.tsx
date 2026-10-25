// 通用图标按钮：全站"用图标代替文字"的统一出口。
//
// ## 为什么要专门做这一层
//
// 表格的操作列（详情/编辑/测试/禁用/删除…）如果每个都写成文字按钮，一列会占
// 掉 200px 以上，而且每行重复十次同样的名词，扫读成本极高。图标按钮把它们压到
// 每个 26px，代价是**必须补上可读的名字**——否则鼠标用户靠 tooltip、键盘用户
// 靠 focus、读屏用户靠 aria-label，三者少一个就是无障碍缺陷。
//
// 所以这个组件的契约是：**图标必填、label 必填**（label 同时用作 tooltip 与
// aria-label，不允许只给其中一个）。
//
// ## 风格
//
// 尺寸/线宽/圆角全部来自 `src/icons.tsx`（Tabler 线性、1.75 线宽、圆头圆角）。
// 危险动作只用 `tone="danger"` 变色，不换形状——形状变化会让"这一列图标"显得
// 不是一套东西。

import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { IconButton as MuiIconButton, Tooltip } from '@mui/material'
import { ICON_SIZE } from '../icons'

export type ActionTone = 'default' | 'primary' | 'danger'

/**
 * 表格行内的图标按钮。
 *
 * - `label` 同时作为 tooltip 与无障碍名称（必填）。
 * - `to` 存在时渲染成路由链接（用于「详情/查看」这类导航动作）。这里必须用
 *   react-router 的 `Link`，不能给 MUI IconButton 传 `href`——项目用 HashRouter，
 *   原生 href 会触发一次整页跳转，把 SPA 的状态全丢掉。
 * - `busy` 时禁用并降低不透明度，位置不移动（避免第二次点击落到别的按钮上）。
 */
export function TableAction({
  icon,
  label,
  onClick,
  to,
  tone = 'default',
  disabled,
  busy
}: {
  icon: ReactNode
  label: string
  onClick?: () => void
  /** 路由目标（走 HashRouter，不触发整页跳转）。 */
  to?: string
  tone?: ActionTone
  disabled?: boolean
  busy?: boolean
}) {
  const color =
    tone === 'danger' ? 'error.main' : tone === 'primary' ? 'primary.main' : 'text.secondary'
  return (
    <Tooltip title={busy ? `${label}…` : label}>
      <span>
        <MuiIconButton
          size="small"
          disabled={disabled || busy}
          onClick={onClick}
          {...(to ? { component: Link, to } : {})}
          aria-label={label}
          sx={{
            p: 0.5,
            borderRadius: 2,
            color,
            opacity: busy ? 0.5 : 1,
            // 悬停给一档底色（不换前景色）：颜色留给"这是危险操作"这一个信号
            '&:hover': { bgcolor: 'var(--fr-hover)' },
            // 26px 的可点击区域：比 16px 的图标本身更难误点，又不撑高行高
            width: 26,
            height: 26
          }}
        >
          {icon}
        </MuiIconButton>
      </span>
    </Tooltip>
  )
}

/** 表格操作列的容器：横向排列图标按钮，间距统一。 */
export function TableActions({ children }: { children: ReactNode }) {
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        // 负外边距抵消第一个按钮的内边距，让图标与列头文字左对齐
        marginLeft: -5,
        gap: 0
      }}
    >
      {children}
    </span>
  )
}

/**
 * 工具栏按钮：图标 + 文字。
 *
 * 与 `TableAction` 的分工：工具栏是"这一屏要做什么"，一行只有 2–4 个，值得
 * 给文字；表格操作列是"这一行能做什么"，一行十几个，只能给图标。
 */
export function ToolButton({
  icon,
  children,
  onClick,
  disabled,
  busy,
  primary
}: {
  icon: ReactNode
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  busy?: boolean
  primary?: boolean
}) {
  return (
    <button
      type="button"
      // 主操作沿用既有 .btn-primary（实心主色），次操作用 .btn-ghost：
      // 两者在 style.css 里已经定义好 hover / disabled，这里不重复实现。
      className={primary ? 'btn-primary' : 'btn-ghost'}
      onClick={onClick}
      disabled={disabled || busy}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        minHeight: 30,
        paddingInline: 10
      }}
    >
      <span style={{ display: 'inline-flex', opacity: busy ? 0.5 : 1 }}>{icon}</span>
      {busy ? `${String(children)}…` : children}
    </button>
  )
}

/** 图标尺寸速查（供调用方取用，避免再次出现 13/14/15 的随手值）。 */
export const ACTION_ICON = ICON_SIZE.sm
