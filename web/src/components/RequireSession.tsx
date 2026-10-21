// 路由级会话守卫（IA 第 7 节三层守卫的渲染层）。
// App.tsx 按 PAGES 表的 requires 字段决定哪些路由包上它——
// requires !== 'none' 的页面未登录直接回登录页，不渲染页面内容。
//
// 边界：这只拦截「渲染」，真正的授权判定在服务端（数据层带 account_id
// 过滤、路由层 404/403）。前端守卫存在是为了体验，不是为了安全。

import { Navigate, useLocation } from 'react-router-dom'
import { useSession } from '../api/session'
import type { ReactNode } from 'react'

export default function RequireSession({ children }: { children: ReactNode }) {
  const { session } = useSession()
  const location = useLocation()

  if (session === undefined) {
    // 会话探测中：避免登录→闪回登录页→又跳回来的抖动，静默等待
    return <p className="placeholder-note">正在恢复会话…</p>
  }
  if (session === null) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
  }
  return <>{children}</>
}
