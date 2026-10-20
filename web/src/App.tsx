// 布局骨架 + 路由装配。
// 三段式骨架（docs/ui/06-design.md 2.1）：顶栏 / 一级导航+内容区 / 页脚。
// 路由一律从 PAGES 表生成：本文件不含任何页面级硬编码，IA 增删页面不动这里。

import { HashRouter, NavLink, Route, Routes } from 'react-router-dom'
import { NAV_LABELS, PAGES, pagesOfNav, type NavGroup, type PageDef } from './routes'
import NotFoundPage from './pages/NotFoundPage'

// 骨架阶段用 HashRouter：产物被 go:embed 打进单二进制后由 Go 静态服务，
// 服务端不做 SPA fallback 时 BrowserRouter 的子路径刷新会 404。
// M9 与 httpapi 定稿静态服务方案后再决定是否切换（届时只改这一处）。
const Router = HashRouter

/** 占位期导航全集；接入鉴权后按账户 classes 过滤（01-ia.md 3.2 R1–R4）。 */
const VISIBLE_NAVS: NavGroup[] = ['public', 'account', 'admin', 'usage', 'contributions']

function Sidebar() {
  return (
    <nav className="sidenav" aria-label="一级导航">
      {VISIBLE_NAVS.map((g) => (
        <div key={g} className="nav-group">
          <h2>{NAV_LABELS[g]}</h2>
          <ul>
            {pagesOfNav(g).map((p) => (
              <li key={p.path}>
                <NavLink to={`#${p.path}`}>
                  {p.title}
                  {p.dangerous && <span aria-hidden="true"> 🔒</span>}
                </NavLink>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  )
}

/** 路由表条目 → <Route>。IA 的 :id 参数与 react-router 语法一致，原样透传。 */
function toRoute(p: PageDef) {
  const El = p.element
  return <Route key={p.path} path={p.path} element={<El def={p} />} />
}

export default function App() {
  return (
    <Router>
      <div className="app-shell">
        <header className="topbar">
          <span className="site-name">FreeRouter</span>
          {/* 顶栏只承载全站同质的运行态横幅（06-design.md 2.1），占位期留空 */}
          <span className="runstate" />
          <span className="account-menu">未登录（占位）</span>
        </header>
        <div className="main-row">
          <Sidebar />
          <main className="content">
            <Routes>
              {PAGES.map(toRoute)}
              {/* catch-all：未匹配路由与 /error/:code 共用错误页组件；
                  对当前身份不可见的入口也统一落这里（01-ia.md 第 7 节：路由层 404） */}
              <Route path="*" element={<NotFoundPage def={NOT_FOUND_DEF} />} />
            </Routes>
          </main>
        </div>
        <footer className="pagefoot">
          {/* 页脚：版本 · /healthz 与 /readyz 轻量指示 · 文档链接（06-design.md 2.1）。
              版本与探活指示依赖后端，占位期只留静态文本。 */}
          <span>FreeRouter 控制台 · 前端骨架</span>
        </footer>
      </div>
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
  element: NotFoundPage
}
