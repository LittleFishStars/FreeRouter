// 内容区固定顺序的骨架实现（docs/ui/06-design.md 2.2）：
//   1. 面包屑（仅在有层级时出现）
//   2. 页面标题 + 一句话说明
//   3. 主操作按钮组（右对齐）
//   4. 分区内容
// 所有页面共用这一处，保证「标题下必须有一句话说明」不会因为赶工而漏掉。
//
// 面包屑按路由表前缀推导（breadcrumbTrail），不依赖 nav 字段：详情页
// （nav=null）同样能显示「管理层 / 账户列表 / 账户详情」。

import type { ReactNode } from 'react'
import { Box, Breadcrumbs, Stack, Typography } from '@mui/material'
import { IconChevronRight, IconLock } from '../icons'
import { breadcrumbTrail, type PageDef } from '../routes'

export interface PageFrameProps {
  def: PageDef
  /** 主操作按钮组（右对齐区）。 */
  actions?: ReactNode
  children?: ReactNode
}

export default function PageFrame({ def, actions, children }: PageFrameProps) {
  const crumbs = breadcrumbTrail(def)
  return (
    <Box component="section">
      {crumbs.length > 1 && (
        <Breadcrumbs
          aria-label="面包屑"
          separator={<IconChevronRight size="xs" />}
          sx={{
            mb: 1.25,
            '& .MuiBreadcrumbs-ol': { flexWrap: 'wrap' },
            '& .MuiBreadcrumbs-li': { fontSize: '0.75rem' }
          }}
        >
          {crumbs.map((c, i) => (
            <Typography
              key={`${c}-${i}`}
              component="span"
              variant="caption"
              // 最后一段是当前页：给正文色以示「你在这里」，其余做灰
              sx={{ color: i === crumbs.length - 1 ? 'text.secondary' : 'text.disabled' }}
            >
              {c}
            </Typography>
          ))}
        </Breadcrumbs>
      )}

      <Box
        component="header"
        sx={{
          display: 'flex',
          alignItems: 'flex-start',
          justifyContent: 'space-between',
          gap: 2,
          flexWrap: 'wrap',
          mb: 2.5
        }}
      >
        <Box sx={{ minWidth: 0, flex: '1 1 auto' }}>
          <Typography variant="h1" component="h1" sx={{ display: 'flex', alignItems: 'center', gap: 1, m: 0 }}>
            {def.title}
            {def.dangerous && (
              // IA 全站唯一的安全标记：本页存在危险写；提交前必须走统一确认框。
              // 用 MUI 图标而非 emoji——emoji 在各平台渲染不一致（彩色/黑白/缺字）。
              <Box
                component="span"
                title="本页含危险写操作，提交前须二次确认"
                sx={{ display: 'inline-flex', color: 'text.disabled', ml: 0.25 }}
              >
                <IconLock size="md" />
              </Box>
            )}
          </Typography>
          {/* 「标题下必须有一句话说明」是全站纪律：缺了这句，运维得点进去才知道这页干什么 */}
          <Typography variant="body2" color="text.secondary" sx={{ mt: 0.75, maxWidth: '72ch' }}>
            {def.summary}
          </Typography>
        </Box>
        {actions ? (
          <Stack direction="row" spacing={1} sx={{ flexWrap: 'wrap', alignItems: 'center', pt: 0.5 }}>
            {actions}
          </Stack>
        ) : null}
      </Box>

      {/* 内容区不再卡在 1400px：宽屏控制台的表格需要横向空间；
          文字段落由各处自己的 maxWidth ch 限制（如上面那行说明） */}
      <Box>{children}</Box>
    </Box>
  )
}
