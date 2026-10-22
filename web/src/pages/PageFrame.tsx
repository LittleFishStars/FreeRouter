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
    <section className="page-frame">
      {crumbs.length > 1 && (
        <nav aria-label="面包屑" className="breadcrumb">
          {crumbs.map((c, i) => (
            <span key={`${c}-${i}`}>
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
              // IA 全站唯一的 🔒 标记：本页存在危险写；提交前必须走统一确认框。
              <span className="lock-mark" title="本页含危险写操作，提交前须二次确认">
                {' '}🔒
              </span>
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
