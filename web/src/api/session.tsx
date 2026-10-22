// 会话状态的单一来源：App 顶层挂一次，登录/登出/刷新全部走这里。
//
// 为什么不用 localStorage：会话凭据是 HttpOnly Cookie，JS 侧根本拿不到
// token——「缓存」的只是会话主体（display_name/classes 等展示信息），
// 失效与吊销的判定权永远在服务端（每次敏感操作后端都会再验）。

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { getAuthSession, type Session } from './client'

interface SessionState {
  /** null = 未登录/会话失效；undefined = 探测中（页面应渲染过渡态）。 */
  session: Session | null | undefined
  /** 登录成功后写入（login 响应）；登出后清空。 */
  setSession: (s: Session | null) => void
  /** 重新探测一次（会话过期、顶栏刷新等场景）。 */
  refresh: () => Promise<void>
}

const SessionContext = createContext<SessionState | null>(null)

export function SessionProvider({ children }: { children: ReactNode }) {
  const [session, setSessionState] = useState<Session | null | undefined>(undefined)

  const refresh = useCallback(async () => {
    try {
      setSessionState(await getAuthSession())
    } catch {
      // 探测失败（后端不可达等）不阻塞界面：按未登录渲染，真实请求时再报错
      setSessionState(null)
    }
  }, [])

  // 顶层只探测一次；之后由登录/登出动作驱动
  useEffect(() => { void refresh() }, [refresh])

  const value = useMemo<SessionState>(() => ({
    session,
    setSession: setSessionState,
    refresh
  }), [session, refresh])

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}

export function useSession(): SessionState {
  const ctx = useContext(SessionContext)
  if (!ctx) throw new Error('useSession 必须在 <SessionProvider> 内使用')
  return ctx
}
