// 内容区固定顺序的骨架实现（docs/ui/06-design.md 2.2）：
//   1. 面包屑（仅在有层级时出现）
//   2. 页面标题 + 一句话说明
//   3. 主操作按钮组（右对齐）
//   4. 分区内容
// 所有页面共用这一处，保证「标题下必须有一句话说明」不会因为赶工而漏掉。

import type { ReactNode } from 'react'
import type { PageDef } from '../routes'

export interface PageFrameProps {
  def: PageDef
  /** 主操作按钮组（右对齐区）。占位页一般没有。 */
  actions?: ReactNode
  children?: ReactNode
}

/** 由路径推导面包屑段：/admin/accounts/:id → 管理 / 账户详情 / 详情。 */
function breadcrumbs(def: PageDef): string[] {
  const segs: string[] = []
  if (def.nav) segs.push(navCrumbLabel(def.nav))
  // 层级页面（nav=null 的子页）用标题作第二段；根路径不冗余显示
  if (def.path !== '/' && def.path.split('/').filter(Boolean).length > 1) {
    segs.push(def.title)
  }
  return segs
}

function navCrumbLabel(nav: NonNullable<PageDef['nav']>): string {
  switch (nav) {
    case 'public': return '公共区'
    case 'account': return '账户'
    case 'admin': return '管理'
    case 'usage': return '我的使用'
    case 'contributions': return '我的贡献'
  }
}

export default function PageFrame({ def, actions, children }: PageFrameProps) {
  const crumbs = breadcrumbs(def)
  return (
    <section className="page-frame">
      {crumbs.length > 1 && (
        <nav aria-label="面包屑" className="breadcrumb">
          {crumbs.map((c, i) => (
            <span key={i}>
              {i > 0 && <span aria-hidden="true"> / </span>}
              {c}
            </span>
          ))}
        </nav>
      )}
      <header className="page-header">
        <div>
          <h1>
            {def.title}
            {def.dangerous && (
              // IA 全站唯一的 🔒 标记：提示本页存在危险写（实装的确认框在 M9 后接入）
              <span title="本页含危险写操作，提交前须二次确认"> 🔒</span>
            )}
          </h1>
          <p className="page-summary">{def.summary}</p>
        </div>
        <div className="page-actions">{actions}</div>
      </header>
      <div className="page-body">{children}</div>
    </section>
  )
}
