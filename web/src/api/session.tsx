// 会话状态的单一来源：App 顶层挂一次，登录/登出/刷新全部走这里。
//
// 为什么不用 localStorage：会话凭据是 HttpOnly Cookie，JS 侧根本拿不到
// token——「缓存」的只是会话主体（display_name/classes 等展示信息），
// 失效与吊销的判定权永远在服务端（每次敏感操作后端都会再验）。
//
// 401 兜底不直接在 HTTP 层改 hash：client 暴露 setUnauthorizedHandler，
// 由这里用 react-router 的 navigate 跳登录页，并带上 from 回跳目标
// （直接改 window.location.hash 会与内部 history 状态脱同步，见 code-review）。

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode
} from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { getAuthSession, setUnauthorizedHandler, type Session } from './client'

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
  const navigate = useNavigate()
  const location = useLocation()

  const refresh = useCallback(async () => {
    try {
      setSessionState(await getAuthSession())
    } catch {
      // 探测失败（后端不可达等）不阻塞界面：按未登录渲染，真实请求时再报错
      setSessionState(null)
    }
  }, [])

  // 顶层只探测一次；之后由登录/登出动作驱动
  useEffect(() => {
    void refresh()
  }, [refresh])

  // 401 统一跳登录：保留当前路径作为回跳目标（客户端守卫的 from 语义）。
  //
  // 关键：同时**清掉内存会话**。只跳转不清会话时，登录页看到 session
  // 非空会立刻 Navigate 回受保护页，而该页的请求继续 401，形成
  // login ↔ 受保护页的无限跳转（实测表现：页面不断闪烁后空白，
  // 典型触发是清库/改密后仍开着的旧标签页，Cookie 已失效而内存会话还在）。
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setSessionState(null)
      if (location.pathname !== '/login') {
        navigate('/login', { replace: true, state: { from: location.pathname } })
      }
    })
    return () => setUnauthorizedHandler(null)
  }, [navigate, location.pathname])

  const value = useMemo<SessionState>(
    () => ({
      session,
      setSession: setSessionState,
      refresh
    }),
    [session, refresh]
  )

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}

export function useSession(): SessionState {
  const ctx = useContext(SessionContext)
  if (!ctx) throw new Error('useSession 必须在 <SessionProvider> 内使用')
  return ctx
}
