// 错误页占位：承载 /error/:code（403/404/410/500），同时也是 SPA 的 catch-all。
// 文案规范见 docs/ui/06-design.md；「无权访问的管理路由返回 404」的判定
// 在路由层/服务端（01-ia.md 第 7 节），前端只负责呈现。

import { useParams } from 'react-router-dom'
import type { PageProps } from '../routes'
import PageFrame from './PageFrame'

export default function NotFoundPage({ def }: PageProps) {
  const { code } = useParams<{ code: string }>()
  return (
    <PageFrame def={def}>
      <p className="placeholder-note">
        错误页占位 —— code <code>{code ?? '404'}</code>。未匹配到任何路由时也会落在这里。
      </p>
    </PageFrame>
  )
}
