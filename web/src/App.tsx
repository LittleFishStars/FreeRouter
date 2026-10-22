// 布局骨架 + 路由装配。
//
// 三段式骨架（docs/ui/06-design.md 2.1）：顶栏 / 一级导航+内容区 / 页脚。
// 路由一律从 PAGES 表生成：本文件不含任何页面级硬编码，IA 增删页面不动这里。
//
// 导航渲染是 docs/ui/01-ia.md 3.2 R1–R4 的纯函数：同样的 classes 永远得到
// 同样的导航。未登录只出现公共区；admin/tenant/contributor 各自派生自己的
// 一级入口；无任何权限类的账户额外得到一行空态提示（R4）。

import { useEffect, useState } from 'react'
import {
  HashRouter,
  Link,
  NavLink,
  Route,
  Routes,
  useLocation,
  useNavigate
} from 'react-router-dom'
import { ApiError, postAuthLogout } from './api/client'
import { SessionProvider, useSession } from './api/session'
import RequirePage from './components/RequirePage'
import {
  NAV_LABELS,
  PAGES,
  pagesOfNav,
  navVisible,
  type NavGroup,
  type PageDef
} from './routes'
import ErrorPage from './pages/ErrorPage'
import { useInboxUnread } from './hooks/useInboxUnread'
import { classLabel, sessionExpiringSoon } from './utils/format'

// 骨架阶段用 HashRouter：产物被 go:embed 打进单二进制后由 Go 静态服务，
// 服务端不做 SPA fallback 时 BrowserRouter 的子路径刷新会 404。
// M9 与 httpapi 定稿静态服务方案后再决定是否切换（届时只改这一处）。
const Router = HashRouter

/** 导航分组顺序：管理排在「我的使用」前（01-ia.md 3.3）。 */
const NAV_ORDER: NavGroup[] = ['public', 'account', 'admin', 'usage', 'contributions']

/** 侧边导航：分组与条目都由 classes 派生，未登录只留公共区。 */
function Sidebar({ unread }: { unread: number }) {
  const { session } = useSession()
  const classes = session?.classes ?? []
  const pendingOnly = session?.status === 'pending'
  const visible = pendingOnly
    ? []
    : NAV_ORDER.filter((g) => {
        if (!session) return g === 'public' // 未登录：公共区（登录/注册是页面动作，不做导航项）
        if (g === 'account') return true
        return navVisible(g, classes)
      })

  return (
    <nav className="sidenav" aria-label="一级导航">
      {visible.map((g) => {
        const pages = pagesOfNav(g)
        if (pages.length === 0) return null
        return (
          <div key={g} className="nav-group">
            <h2>{NAV_LABELS[g]}</h2>
            <ul>
              {pages.map((p) => (
                <li key={p.path}>
                  {/* HashRouter 下必须传路径本身：to="#/admin" 会被当成
                      「只带 hash 的相对跳转」，href 变成 #/<当前>#/admin，
                      pathname 不变 → 导航点击无反应（code-review 严重 1）。 */}
                  <NavLink
                    to={p.path}
                    end
                    className={({ isActive }) => (isActive ? 'nav-link active' : 'nav-link')}
                  >
                    {p.title}
                    {p.dangerous && (
                      <span aria-hidden="true" title="本页含危险写操作">
                        {' '}🔒
                      </span>
                    )}
                    {p.path === '/admin/inbox' && unread > 0 && (
                      <span className="nav-badge" aria-label={`${unread} 条未读`}>
                        {unread > 99 ? '99+' : unread}
                      </span>
                    )}
                  </NavLink>
                </li>
              ))}
            </ul>
          </div>
        )
      })}
      {pendingOnly && (
        <p className="nav-empty">审核期间只能访问「待审核」页；审核通过后这里会出现可用入口。</p>
      )}
      {!pendingOnly && session && session.classes.length === 0 && (
        <p className="nav-empty">你的账户尚未获得任何权限类。等待管理员审核通过后即可调用模型。</p>
      )}
    </nav>
  )
}

/** 顶栏账户菜单：显示名、权限徽章、会话到期提示、登出。 */
function AccountMenu() {
  const { session, setSession } = useSession()
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)

  if (!session) {
    return (
      <nav className="top-links" aria-label="账户">
        <Link to="/login">登录</Link>
        <Link to="/register">注册</Link>
      </nav>
    )
  }

  async function logout() {
    setBusy(true)
    try {
      await postAuthLogout()
    } catch (err) {
      // 登出接口幂等；网络故障时本地也清掉登录态，避免把用户困在已失效会话里
      if (!(err instanceof ApiError)) console.warn('登出请求失败', err)
    } finally {
      setBusy(false)
      setSession(null)
      navigate('/login', { replace: true })
    }
  }

  return (
    <div className="top-links">
      <Link to="/account" className="top-user">
        {session.display_name}
      </Link>
      <span className="top-classes">
        {session.classes.map((c) => (
          <span key={c} className="badge badge-muted">
            {classLabel(c)}
          </span>
        ))}
      </span>
      {sessionExpiringSoon(session.expires_at) && (
        <span className="session-warning" title={session.expires_at}>
          登录即将过期
        </span>
      )}
      <button type="button" className="btn-link" onClick={logout} disabled={busy}>
        {busy ? '正在登出…' : '登出'}
      </button>
    </div>
  )
}

/** 顶栏：站点名 · 待处理入口 · 全局运行态横幅位 · 账户菜单。 */
function Topbar({ unread }: { unread: number }) {
  const { session } = useSession()
  const isAdmin = session?.classes.includes('admin') ?? false
  return (
    <header className="topbar">
      <Link to="/" className="site-name">
        FreeRouter
      </Link>
      {/* 全站同质的运行态横幅（上游离线等，docs/ui/02-public.md 7.3）。
          后端尚未提供运行态端点，这里只保留位置，不放任何占位文案。 */}
      <span className="runstate" aria-live="polite" />
      <span className="topbar-spacer" />
      {isAdmin && (
        <Link to="/admin/inbox" className="inbox-entry" aria-label={`待处理事项，${unread} 条未读`}>
          待处理
          {unread > 0 && <span className="nav-badge">{unread > 99 ? '99+' : unread}</span>}
        </Link>
      )}
      <AccountMenu />
    </header>
  )
}

/** 页脚：版本 · /healthz 与 /readyz 的轻量指示。 */
function Footer() {
  const [health, setHealth] = useState<{ healthz: boolean | null; readyz: boolean | null; version: string }>({
    healthz: null,
    readyz: null,
    version: ''
  })
  const [built, setBuilt] = useState('')

  useEffect(() => {
    let alive = true
    const probe = (path: string) =>
      fetch(path, { credentials: 'include' })
        .then((r) => r.ok)
        .catch(() => false)
    void Promise.all([probe('/healthz'), probe('/readyz')]).then(([h, r]) => {
      if (alive) setHealth((s) => ({ ...s, healthz: h, readyz: r }))
    })
    fetch('/api/version', { credentials: 'include' })
      .then((r) => (r.ok ? r.json() : null))
      .then((v) => {
        if (alive && v && typeof v.version === 'string') {
          setHealth((s) => ({ ...s, version: v.version }))
          setBuilt(typeof v.webui_built === 'boolean' ? (v.webui_built ? '已嵌入前端' : '未嵌入前端') : '')
        }
      })
      .catch(() => {
        /* 版本是装饰，取不到就留空 */
      })
    return () => {
      alive = false
    }
  }, [])

  const mark = (ok: boolean | null) => (ok === null ? '…' : ok ? '正常' : '离线')
  return (
    <footer className="pagefoot">
      <span>
        FreeRouter 控制台{health.version ? ` · v${health.version}` : ''}
        {built ? ` · ${built}` : ''}
      </span>
      <span className="foot-status">
        /healthz：{mark(health.healthz)} · /readyz：{mark(health.readyz)}
      </span>
    </footer>
  )
}

/** 让 /pending 之外的页面在 pending 会话下自动让路（RequirePage 已处理页面级；
 *  这里处理「登录后落在 /」这种默认入口）。 */
function PendingGate() {
  const { session } = useSession()
  const location = useLocation()
  const navigate = useNavigate()
  useEffect(() => {
    if (session?.status === 'pending' && !location.pathname.startsWith('/pending')) {
      navigate('/pending', { replace: true })
    }
  }, [session, location.pathname, navigate])
  return null
}

/** 路由表条目 → <Route>。IA 的 :id 参数与 react-router 语法一致，原样透传。 */
function toRoute(p: PageDef) {
  const El = p.element
  const body = <El def={p} />
  const guarded = p.requires !== 'none' ? <RequirePage def={p}>{body}</RequirePage> : body
  return <Route key={p.path} path={p.path} element={guarded} />
}

function Shell() {
  const { session } = useSession()
  const isAdmin = session?.classes.includes('admin') ?? false
  // 未读数只在 admin 类登录后拉取：登录时一次 + 60s 轮询 + focus 立即刷
  const { unread } = useInboxUnread(isAdmin)

  return (
    <div className="app-shell">
      <Topbar unread={unread} />
      <div className="main-row">
        <Sidebar unread={unread} />
        <main className="content">
          <PendingGate />
          <Routes>
            {PAGES.map(toRoute)}
            {/* catch-all：未匹配路由与非法 /error/:code 共用 404 页；
                对当前身份不可见的入口由 RequirePage 统一送 /error/404
                （01-ia.md 第 7 节：路由层不确认资源是否存在）。 */}
            <Route path="*" element={<ErrorPage def={NOT_FOUND_DEF} />} />
          </Routes>
        </main>
      </div>
      <Footer />
    </div>
  )
}

export default function App() {
  return (
    <Router>
      <SessionProvider>
        <Shell />
      </SessionProvider>
    </Router>
  )
}

// catch-all 的路由表条目：不在 PAGES 里（它没有固定路径），单独构造。
const NOT_FOUND_DEF: PageDef = {
  path: '*',
  title: '页面不存在',
  summary: '未匹配到任何路由；也可能该入口对当前身份不可见（统一 404，见 01-ia.md 第 7 节）。',
  nav: null,
  requires: 'none',
  element: ErrorPage
}
