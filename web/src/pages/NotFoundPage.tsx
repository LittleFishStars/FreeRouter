// 错误页占位：承载 /error/:code（403/404/410/500），同时也是 SPA 的 catch-all。
// 文案规范见 docs/ui/06-design.md；「无权访问的管理路由返回 404」的判定
// 在路由层/服务端（01-ia.md 第 7 节），前端只负责呈现。

import { useParams } from 'react-router-dom'
import type { PageProps } from '../routes'
import PageFrame from './PageFrame'

// code → 可读文案。契约见 docs/api/03-errors.md §3.2；
// 界面只给「发生了什么 + 该做什么」，绝不回显堆栈或内部路径。
const ERROR_TEXTS: Record<string, string> = {
  '403': '没有权限访问该页面。如果这出乎意料，请联系管理员确认你的权限类。',
  '404': '页面不存在。地址可能有误，或该入口对你当前的身份不可见。',
  '410': '该资源已被弃用。请从目录中改用替代模型或页面。',
  '500': '服务内部错误。请稍后重试；若持续出现请联系管理员。'
}

export default function NotFoundPage({ def }: PageProps) {
  const { code } = useParams<{ code: string }>()
  const c = code ?? '404'
  return (
    <PageFrame def={def}>
      <p className="placeholder-note">
        <strong>HTTP {c}</strong> —— {ERROR_TEXTS[c] ?? '请求未能完成，请稍后重试或返回首页。'}
        {' '}<a href="#/">返回首页</a>
      </p>
    </PageFrame>
  )
}
