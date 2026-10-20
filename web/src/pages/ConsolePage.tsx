// 通用占位页：路由表里的绝大多数页面在骨架阶段都指向它。
// 之所以不逐页建空文件：IA 增删页面时只改 routes.ts 即可，占位不需要新组件。
// M9 联调后，有真实实现的页面在表里换成各自的组件。

import type { PageProps } from '../routes'
import PageFrame from './PageFrame'

export default function ConsolePage({ def }: PageProps) {
  return (
    <PageFrame def={def}>
      <p className="placeholder-note">
        占位页面 —— 路由 <code>{def.path}</code>，可见性要求 <code>{def.requires}</code>。
        具体字段与操作分别见 docs/ui 的 02-public / 03-admin / 04-tenant / 05-contributor 分册。
      </p>
    </PageFrame>
  )
}
