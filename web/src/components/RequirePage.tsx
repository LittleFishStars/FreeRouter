// 路由级守卫（docs/ui/01-ia.md 第 7 节「三层守卫」的渲染层与路由层）。
//
// 三层各自独立：渲染层（导航是否出现）、路由层（直接访问 URL 的判定）、
// 数据层（服务端强制 account_id 过滤）。前端守卫只做前两层，且**不是授权**。
//
// 无权访问的管理路由统一落 /error/404 而不是 403：管理面的路由结构本身
// 就是信息，不确认资源是否存在（01-ia.md 第 7 节）。

import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { useSession } from '../api/session'
import { canAccess, type PageDef } from '../routes'

export default function RequirePage({ def, children }: { def: PageDef; children: ReactNode }) {
  const { session } = useSession()
  const location = useLocation()

  if (session === undefined) {
    // 会话探测中：避免登录→闪回登录页→又跳回来的抖动，静默等待
    return <p className="placeholder-note">正在恢复会话…</p>
  }

  if (session === null) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
  }

  // pending 账户只能停在 /pending（01-ia.md 3.1：status 也是渲染输入）。
  // 后端的会话响应当前不带 status，字段一旦上线这里立即生效。
  if (session.status === 'pending' && def.path !== '/pending') {
    return <Navigate to="/pending" replace />
  }
	// 强制改密窗口：一次性口令（首启随机生成/管理员重置）之下只能去改密页。
	// 前端守卫只是体验层；服务端 mwSession/mwAdmin 仍有同一道闸。
	if (session.must_change_password && def.path !== '/account/password') {
		return <Navigate to="/account/password" replace state={{ firstLogin: true }} />
	}

  if (!canAccess(def.requires, session.classes)) {
    return <Navigate to="/error/404" replace />
  }

  return <>{children}</>
}
