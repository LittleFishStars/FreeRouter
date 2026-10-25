// 错误页：403 / 404 / 410 / 500 的统一文案页，同时服务 catch-all。
//
// 文案规范（docs/ui/06-design.md 6.1）：说清「发生了什么」和「你能做什么」；
// 保留可折叠的机器码；500 给出可复制的请求 ID（06-design.md 4）。
// 403 与 404 分页：403 是可行动信息（去申请权限），404 不确认资源是否存在。

import { Link, useLocation } from 'react-router-dom'
import type { PageProps } from '../routes'
import PageFrame from './PageFrame'
import { Banner } from '../components/ui'
import { ACTION_ICON } from '../components/IconButtons'
import { IconBack } from '../icons'

const ERROR_TEXTS: Record<string, { title: string; what: string; next: string }> = {
  '403': {
    title: '没有权限',
    what: '当前账户没有该页面所需的能力。权限由管理员按账户授予，不会因为你换一个入口而改变。',
    next: '如果这出乎意料，请联系管理员确认你的账户应具备哪类能力。'
  },
  '404': {
    title: '页面不存在',
    what: '地址可能有误，或该入口对你当前的身份不可见（管理面统一不确认资源是否存在）。',
    next: '返回首页，或从左侧导航选择你能访问的页面。'
  },
  '410': {
    title: '该资源已不可用',
    what: '它曾经存在，但已被禁用或弃用；与 404 的区别是「它曾经存在」。',
    next: '如果这是一个抽象模型，请查看模型目录里的替代提示。'
  },
  '500': {
    title: '服务内部错误',
    what: '请求没有完成，原因在服务端。',
    next: '请稍后重试；若持续出现，请把页面上的请求 ID 报给管理员。'
  }
}

function codeFromPath(path: string): string {
  const m = path.match(/^\/error\/(\d{3})$/)
  if (m && ERROR_TEXTS[m[1]]) return m[1]
  return '404'
}

export default function ErrorPage({ def }: PageProps) {
  const location = useLocation()
  const code = codeFromPath(location.pathname)
  const text = ERROR_TEXTS[code]
  const requestId = new URLSearchParams(location.search).get('request_id') ?? undefined

  return (
    <PageFrame def={{ ...def, title: `${code} ${text.title}`, summary: text.what }}>
      <Banner tone={code === '500' ? 'warn' : 'info'}>
        {text.next}
        {code === '500' && (
          <span className="block-meta">
            请求 ID：<code>{requestId ?? '（响应头 x-free-router-request-id）'}</code>
          </span>
        )}
      </Banner>
      <p className="meta-line">
        机器码：<code>{code === '410' ? 'gone' : code === '404' ? 'not_found' : code === '403' ? 'forbidden' : 'internal_error'}</code>
      </p>
      <p>
        <Link to="/" className="icon-link">
          <IconBack size={ACTION_ICON} />
          返回首页
        </Link>
      </p>
    </PageFrame>
  )
}
