// 初始化向导占位（docs/ui/01-ia.md 4.3 末行）：站点无任何账户时唯一可见页面。
// 守卫逻辑（「无账户时其他路由全部不可达」）依赖后端状态，M9 实装。

import type { PageProps } from '../routes'
import PageFrame from './PageFrame'

export default function SetupPage({ def }: PageProps) {
  return (
    <PageFrame def={def}>
      <p className="placeholder-note">
        初始化向导占位：创建首个管理员账户。路由 <code>{def.path}</code>。
      </p>
    </PageFrame>
  )
}
