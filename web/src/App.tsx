// 布局骨架 + 路由装配。
//
// 三段式骨架（docs/ui/06-design.md 2.1）：顶栏 / 一级导航+内容区 / 页脚。
// 路由一律从 PAGES 表生成：本文件不含任何页面级硬编码，IA 增删页面不动这里。
//
// 导航渲染是 docs/ui/01-ia.md 3.2 R1–R4 的纯函数：同样的 classes 永远得到
// 同样的导航。未登录只出现公共区；admin/tenant/contributor 各自派生自己的
// 一级入口；无任何权限类的账户额外得到一行空态提示（R4）。
//
// ## 视觉取向（迁移 MUI 时定下）
//
// 顶栏刻意用 `color="inherit"` + 一条下边框，而不是 MUI 默认的主色填充条：
// 一个控制台的顶栏如果是一条饱和色带，整页最抢眼的东西就变成了"这个软件叫什么"，
// 而运维真正要看的是表格里的状态。灰阶外壳把注意力让给内容，这也是 Linear /
// Vercel 这类工具型界面共同的选择。
//
// 侧边导航的选中态用"浅底 + 主色文字"而不是"主色实心块"：实心块在深色模式
// 下会把整列侧栏压成一串高对比色块，扫读成本反而上升。

import { useEffect, useState } from 'react'
import {
  HashRouter,
  Link,
  Route,
  Routes,
  useLocation,
  useNavigate
} from 'react-router-dom'
import {
  AppBar,
  Avatar,
  Box,
  Chip,
  Divider,
  Drawer,
  IconButton,
  List,
  ListItemButton,
  ListItemText,
  Menu,
  MenuItem,
  Toolbar,
  Tooltip,
  Typography,
  useMediaQuery,
  useTheme
} from '@mui/material'
import MenuIcon from '@mui/icons-material/Menu'
import DarkModeIcon from '@mui/icons-material/DarkMode'
import LightModeIcon from '@mui/icons-material/LightMode'
import InboxIcon from '@mui/icons-material/Inbox'
import LogoutIcon from '@mui/icons-material/Logout'
import PersonOutlinedIcon from '@mui/icons-material/PersonOutlined'
import LockOutlinedIcon from '@mui/icons-material/LockOutlined'
import { ApiError, postAuthLogout } from './api/client'
import { SessionProvider, useSession } from './api/session'
import { useColorMode } from './colorMode'
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

/** 侧栏宽度（桌面）。 */
const DRAWER_WIDTH = 244

/**
 * 让页脚与内容区共享同一个「页面归属」判断：取路径前缀最长的那个导航项。
 *
 * 为什么不用 NavLink 的 active：HashRouter 下 NavLink 的 className 回调与
 * `end` 的组合行为不够直观（`/admin` 会命中 `/admin/accounts`）。而"最长前缀
 * 命中"正是侧栏高亮应有的语义——在账户详情页里，高亮的应该是「账户列表」
 * 而不是「管理工作台」。
 */
function matchPage(pathname: string, pages: PageDef[]): PageDef | null {
  let best: PageDef | null = null
  for (const p of pages) {
    const hit = pathname === p.path || pathname.startsWith(p.path + '/')
    if (!hit) continue
    if (!best || p.path.length > best.path.length) best = p
  }
  return best
}

/** 侧栏内部内容（桌面常驻与移动端抽屉共用一份）。 */
function NavList({ unread, onNavigate }: { unread: number; onNavigate?: () => void }) {
  const { session } = useSession()
  const { pathname } = useLocation()
  const classes = session?.classes ?? []
  const pendingOnly = session?.status === 'pending'
  const visible = pendingOnly
    ? []
    : NAV_ORDER.filter((g) => {
        if (!session) return g === 'public' // 未登录：公共区（登录/注册是页面动作，不做导航项）
        if (g === 'account') return true
        return navVisible(g, classes)
      })

  const allPages = visible.flatMap((g) => pagesOfNav(g))
  const activePage = matchPage(pathname, allPages)

  return (
    <Box sx={{ px: 1.25, py: 1.5 }}>
      {visible.map((g) => {
        const pages = pagesOfNav(g)
        if (pages.length === 0) return null
        return (
          <Box key={g} sx={{ mb: 1.5 }}>
            {/* 分组标题做成小号大写灰字：层级靠字重与字号，不靠加粗色块 */}
            <Typography
              variant="caption"
              sx={{
                display: 'block',
                px: 1.25,
                pb: 0.5,
                fontWeight: 700,
                fontSize: '0.68rem',
                letterSpacing: '.08em',
                textTransform: 'uppercase',
                color: 'text.disabled'
              }}
            >
              {NAV_LABELS[g]}
            </Typography>
            <List dense disablePadding>
              {pages.map((p) => {
                const active = activePage?.path === p.path
                return (
                  <ListItemButton
                    key={p.path}
                    component={Link}
                    to={p.path}
                    onClick={onNavigate}
                    selected={active}
                    sx={{
                      py: 0.6,
                      mb: 0.25,
                      // 选中态：浅底 + 主色文字 + 左侧 2px 主色竖条（比实心块安静）
                      '&.Mui-selected': {
                        bgcolor: (t) =>
                          t.palette.mode === 'dark' ? 'rgba(129,140,248,.14)' : 'rgba(79,70,229,.08)',
                        color: 'primary.main',
                        '&:hover': {
                          bgcolor: (t) =>
                            t.palette.mode === 'dark' ? 'rgba(129,140,248,.2)' : 'rgba(79,70,229,.12)'
                        }
                      }
                    }}
                  >
                    <ListItemText
                      primary={p.title}
                      slotProps={{
                        primary: {
                          noWrap: true,
                          sx: { fontSize: '0.855rem', fontWeight: active ? 650 : 500 }
                        }
                      }}
                    />
                    {p.dangerous && (
                      <Tooltip title="本页含危险写操作">
                        {/* 用图标而不是 emoji：emoji 在 Linux/Win/mac 上渲染成三种
                            不同的彩色图形，会在一列纯文字导航里跳出来，破坏扫读。 */}
                        <LockOutlinedIcon
                          sx={{ fontSize: 13, ml: 0.6, color: 'text.disabled', flexShrink: 0 }}
                          aria-hidden="true"
                        />
                      </Tooltip>
                    )}
                    {p.path === '/admin/inbox' && unread > 0 && (
                      <Chip
                        label={unread > 99 ? '99+' : unread}
                        size="small"
                        color="primary"
                        sx={{ height: 18, fontSize: '0.68rem', ml: 0.75, '& .MuiChip-label': { px: 0.75 } }}
                        aria-label={`${unread} 条未读`}
                      />
                    )}
                  </ListItemButton>
                )
              })}
            </List>
          </Box>
        )
      })}
      {pendingOnly && (
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', px: 1.25 }}>
          审核期间只能访问「待审核」页；审核通过后这里会出现可用入口。
        </Typography>
      )}
      {!pendingOnly && session && session.classes.length === 0 && (
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', px: 1.25 }}>
          你的账户尚未获得任何权限类。等待管理员审核通过后即可调用模型。
        </Typography>
      )}
    </Box>
  )
}

/** 账户菜单：头像 + 下拉（显示名、权限类、到期提示、登出）。 */
function AccountMenu() {
  const { session, setSession } = useSession()
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  const [anchor, setAnchor] = useState<null | HTMLElement>(null)

  if (!session) {
    return (
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
        <Typography
          component={Link}
          to="/login"
          variant="body2"
          sx={{ color: 'text.secondary', textDecoration: 'none', '&:hover': { color: 'text.primary' } }}
        >
          登录
        </Typography>
        <Typography
          component={Link}
          to="/register"
          variant="body2"
          sx={{ color: 'text.secondary', textDecoration: 'none', '&:hover': { color: 'text.primary' } }}
        >
          注册
        </Typography>
      </Box>
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

  const initial = (session.display_name || '?').trim().slice(0, 1).toUpperCase() || '?'

  return (
    <>
      <Tooltip title="账户">
        <IconButton onClick={(e) => setAnchor(e.currentTarget)} size="small" sx={{ ml: 0.5 }}>
          <Avatar sx={{ width: 30, height: 30, fontSize: '0.82rem', fontWeight: 700, bgcolor: 'primary.main' }}>
            {initial}
          </Avatar>
        </IconButton>
      </Tooltip>
      <Menu
        anchorEl={anchor}
        open={Boolean(anchor)}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
        transformOrigin={{ vertical: 'top', horizontal: 'right' }}
        slotProps={{ paper: { sx: { minWidth: 230, mt: 0.75, borderRadius: 2.5 } } }}
      >
        <Box sx={{ px: 2, py: 1.25 }}>
          <Typography variant="body2" sx={{ fontWeight: 700 }}>
            {session.display_name}
          </Typography>
          <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap', mt: 0.75 }}>
            {session.classes.length === 0 ? (
              <Chip label="无权限类" size="small" variant="outlined" />
            ) : (
              session.classes.map((c) => <Chip key={c} label={classLabel(c)} size="small" variant="outlined" />)
            )}
          </Box>
          {sessionExpiringSoon(session.expires_at) && (
            <Typography variant="caption" color="warning.main" sx={{ display: 'block', mt: 0.75 }}>
              登录即将过期（{session.expires_at}）
            </Typography>
          )}
        </Box>
        <Divider />
        <MenuItem
          onClick={() => {
            setAnchor(null)
            navigate('/account')
          }}
        >
          <PersonOutlinedIcon fontSize="small" sx={{ mr: 1.25, color: 'text.secondary' }} />
          账户设置
        </MenuItem>
        <MenuItem onClick={logout} disabled={busy}>
          <LogoutIcon fontSize="small" sx={{ mr: 1.25, color: 'text.secondary' }} />
          {busy ? '正在登出…' : '登出'}
        </MenuItem>
      </Menu>
    </>
  )
}

/** 顶栏：站点名 · 待处理入口 · 主题切换 · 账户菜单。 */
function Topbar({ unread, onOpenNav }: { unread: number; onOpenNav: () => void }) {
  const { session } = useSession()
  const { mode, toggle } = useColorMode()
  const isAdmin = session?.classes.includes('admin') ?? false
  const desktop = useMediaQuery(useTheme().breakpoints.up('md'))

  return (
    <AppBar
      position="fixed"
      color="inherit"
      elevation={0}
      sx={{
        zIndex: (t) => t.zIndex.drawer + 1,
        bgcolor: 'background.paper',
        borderBottom: '1px solid',
        borderColor: 'divider'
      }}
    >
      <Toolbar sx={{ minHeight: { xs: 56, md: 58 }, gap: 1 }}>
        {!desktop && (
          <IconButton edge="start" onClick={onOpenNav} size="small" aria-label="打开导航">
            <MenuIcon />
          </IconButton>
        )}
        <Box
          component={Link}
          to="/"
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 1,
            textDecoration: 'none',
            color: 'text.primary'
          }}
        >
          {/* 用一个小方块做品牌标记而不是图片：单二进制里塞 logo 不值当，
              纯 CSS 标记在深浅两种模式下都能自适应 */}
          <Box
            aria-hidden="true"
            sx={{
              width: 22,
              height: 22,
              borderRadius: 1,
              bgcolor: 'primary.main',
              display: 'grid',
              placeItems: 'center',
              color: 'primary.contrastText',
              fontSize: '0.72rem',
              fontWeight: 800,
              letterSpacing: '-.03em'
            }}
          >
            FR
          </Box>
          <Typography sx={{ fontWeight: 750, fontSize: '0.98rem', letterSpacing: '-.01em' }}>
            FreeRouter
          </Typography>
        </Box>

        <Box sx={{ flex: 1 }} />

        {isAdmin && (
          <Tooltip title={unread > 0 ? `${unread} 条待处理` : '待处理事项'}>
            <IconButton component={Link} to="/admin/inbox" size="small" aria-label="待处理事项">
              <Box sx={{ position: 'relative', display: 'inline-flex' }}>
                <InboxIcon fontSize="small" />
                {unread > 0 && (
                  <Box
                    aria-hidden="true"
                    sx={{
                      position: 'absolute',
                      top: -3,
                      right: -5,
                      minWidth: 14,
                      height: 14,
                      px: 0.4,
                      borderRadius: 7,
                      bgcolor: 'error.main',
                      color: '#fff',
                      fontSize: '0.6rem',
                      fontWeight: 700,
                      lineHeight: '14px',
                      textAlign: 'center'
                    }}
                  >
                    {unread > 99 ? '99+' : unread}
                  </Box>
                )}
              </Box>
            </IconButton>
          </Tooltip>
        )}

        <Tooltip title={mode === 'dark' ? '切换到浅色' : '切换到深色'}>
          <IconButton onClick={toggle} size="small" aria-label="切换深浅色">
            {mode === 'dark' ? <LightModeIcon fontSize="small" /> : <DarkModeIcon fontSize="small" />}
          </IconButton>
        </Tooltip>

        <AccountMenu />
      </Toolbar>
    </AppBar>
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
  const dot = (ok: boolean | null) =>
    ok === null ? 'text.disabled' : ok ? 'success.main' : 'error.main'

  return (
    <Box
      component="footer"
      sx={{
        mt: 'auto',
        px: { xs: 2, md: 3.5 },
        py: 1.5,
        borderTop: '1px solid',
        borderColor: 'divider',
        display: 'flex',
        flexWrap: 'wrap',
        gap: 1.5,
        justifyContent: 'space-between',
        color: 'text.secondary'
      }}
    >
      <Typography variant="caption">
        FreeRouter 控制台{health.version ? ` · v${health.version}` : ''}
        {built ? ` · ${built}` : ''}
      </Typography>
      <Box sx={{ display: 'flex', gap: 2 }}>
        {(['healthz', 'readyz'] as const).map((k) => (
          <Box key={k} sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.75 }}>
            <Box
              aria-hidden="true"
              sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: dot(health[k]) }}
            />
            <Typography variant="caption">
              /{k}：{mark(health[k])}
            </Typography>
          </Box>
        ))}
      </Box>
    </Box>
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

/** 强制改密窗口全局让路：任何入口（含「/」与需登录的页面）都先落到
 *  /account/password。页面级由 RequirePage 处理，这里补 requires:
 *  'none' 的入口与登录后直接落地的情况；服务端 mwSession/mwAdmin 同闸。 */
function MustChangeGate() {
  const { session } = useSession()
  const location = useLocation()
  const navigate = useNavigate()
  useEffect(() => {
    if (session?.must_change_password && location.pathname !== '/account/password') {
      navigate('/account/password', { replace: true, state: { firstLogin: true } })
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
  const theme = useTheme()
  const desktop = useMediaQuery(theme.breakpoints.up('md'))
  const [navOpen, setNavOpen] = useState(false)

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', minHeight: '100vh' }}>
      <Topbar unread={unread} onOpenNav={() => setNavOpen(true)} />

      {/* 移动端抽屉：只在 < md 出现，点条目自动收起 */}
      <Drawer
        open={!desktop && navOpen}
        onClose={() => setNavOpen(false)}
        ModalProps={{ keepMounted: true }}
        sx={{ display: { xs: 'block', md: 'none' } }}
        slotProps={{ paper: { sx: { width: DRAWER_WIDTH } } }}
      >
        <Toolbar sx={{ minHeight: { xs: 56, md: 58 } }} />
        <NavList unread={unread} onNavigate={() => setNavOpen(false)} />
      </Drawer>

      <Box sx={{ display: 'flex', flex: 1, pt: { xs: '56px', md: '58px' } }}>
        {/* 桌面常驻侧栏：固定宽度，不动不缩放（固定比流体更可预测） */}
        <Box
          component="nav"
          aria-label="一级导航"
          sx={{
            display: { xs: 'none', md: 'block' },
            width: DRAWER_WIDTH,
            flexShrink: 0,
            borderRight: '1px solid',
            borderColor: 'divider',
            position: 'sticky',
            top: 0,
            alignSelf: 'flex-start',
            height: 'calc(100vh - 58px)',
            overflowY: 'auto'
          }}
        >
          <NavList unread={unread} />
        </Box>

        <Box
          component="main"
          sx={{
            flex: 1,
            minWidth: 0, // 关键：没有它，宽表格会把整个 flex 容器撑破
            display: 'flex',
            flexDirection: 'column'
          }}
        >
          <Box sx={{ px: { xs: 2, md: 3.5 }, py: { xs: 2.5, md: 3.5 }, flex: 1 }}>
            <PendingGate />
            <MustChangeGate />
            {/* 内容区上限：超宽屏上让正文不至于拉成一条 2000px 的长行 */}
            <Box sx={{ maxWidth: 1400 }}>
              <Routes>
                {PAGES.map(toRoute)}
                {/* catch-all：未匹配路由与非法 /error/:code 共用 404 页；
                    对当前身份不可见的入口由 RequirePage 统一送 /error/404
                    （01-ia.md 第 7 节：路由层不确认资源是否存在）。 */}
                <Route path="*" element={<ErrorPage def={NOT_FOUND_DEF} />} />
              </Routes>
            </Box>
          </Box>
          <Footer />
        </Box>
      </Box>
    </Box>
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
