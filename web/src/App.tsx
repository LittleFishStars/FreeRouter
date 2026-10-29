// 布局骨架 + 路由装配。
//
// 两级侧栏骨架（docs/ui/06-design.md 2.1，2026-10-30 参照 Tingly Box 改定）：
// 图标栏（一级导航）/ 二级面板（组内页面）/ 内容区 / 页脚。
// 路由一律从 PAGES 表生成：本文件不含任何页面级硬编码，IA 增删页面不动这里。
//
// 导航渲染是 docs/ui/01-ia.md 3.2 R1–R4 的纯函数：同样的 classes 永远得到
// 同样的导航。未登录只出现公共区；admin/tenant/contributor 各自派生自己的
// 一级入口；无任何权限类的账户在面板里得到空态提示（R4）。两级侧栏只改了
// 「怎么摆」，没改「谁能出现」——可见性仍由 routes.ts 的表 + classes 派生。
//
// ## 视觉取向（2026-10-30 起参照 Tingly Box）
//
// 壳层不再有横贯全宽的顶栏：品牌住在图标栏顶部，主题/账户住在图标栏底部。
// 窗口镶边只承担「导航 + 身份」，整条横向空间让给内容——运维真正要看的是
// 表格里的状态，不是"这个软件叫什么"。
//
// 二级面板可以整列收起（面板右上角按钮，localStorage 记忆）：收起后控制台
// 只剩图标栏，宽表格页多出 230px。点任意图标栏项都会重新展开面板。
//
// 两级导航的选中态统一用**主色实心块**（Tingly Box 式），替换之前"浅底 +
// 3px 竖条"的方案：单列侧栏里实心块会连成一片高对比色带，所以当年选了竖条；
// 现在选中态分散在两列（图标栏一个组 + 面板一个页，各只有一个），实心块反而
// 成了最醒目的"你在这里"路标。深色模式下主色是提亮过的 #7f86f7，实心块
// 不会糊成一团。

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
  Menu,
  MenuItem,
  Toolbar,
  Tooltip,
  Typography,
  useMediaQuery,
  useTheme
} from '@mui/material'
import {
  IconChevronsLeft,
  IconLock,
  IconLogout,
  IconMenu,
  IconMoon,
  IconNavAccount,
  IconNavContributions,
  IconNavHome,
  IconNavSettings,
  IconNavUsage,
  IconSun,
  IconUser
} from './icons'
import { ApiError, PATHS, postAuthLogout } from './api/client'
import { SessionProvider, useSession } from './api/session'
import { useColorMode } from './colorMode'
import { SURFACE } from './theme'
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

/** 图标栏宽度（桌面）。 */
const RAIL_WIDTH = 76

/** 二级面板宽度（桌面）。 */
const PANEL_WIDTH = 232

/** 二级面板展开/收起的 localStorage 键。 */
const PANEL_STORAGE_KEY = 'fr-nav-panel-open'

/** 移动端顶栏高度（与内容区的 paddingTop 成对）。 */
const MOBILE_BAR_HEIGHT = 56

/**
 * 图标栏分组条目：短标签 + 组图标。组的「身份图标」住在这里（壳层展示），
 * 页面的图标仍住在 routes.ts 的 PAGES 表里（IA 数据）——两层各自唯一。
 */
const NAV_RAIL: Record<NavGroup, { label: string; icon: NonNullable<PageDef['icon']> }> = {
  public: { label: '首页', icon: IconNavHome },
  account: { label: '账户', icon: IconNavAccount },
  admin: { label: '管理', icon: IconNavSettings },
  usage: { label: '使用', icon: IconNavUsage },
  contributions: { label: '贡献', icon: IconNavContributions }
}

/** 会话里参与导航可见性判断的最小形状（R1–R4 的渲染输入）。 */
type SessionLike = { classes: string[]; status?: string } | null | undefined

/**
 * 当前会话可见的一级分组（01-ia.md 3.2 R1–R4 的纯函数）：
 * 未登录只有公共区；pending 只剩空态；其余按 classes 过滤。
 */
function visibleGroups(session: SessionLike): NavGroup[] {
  if (!session) return ['public']
  if (session.status === 'pending') return []
  return NAV_ORDER.filter((g) => g === 'account' || navVisible(g, session.classes))
}

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

/**
 * 当前应点亮的一级分组：路由能匹配到页面就用它的 nav；匹配不到（错误页、
 * 回调页）记住最后一个分组，避免面板突然变成空壳。结果始终钳制在可见
 * 分组内——登出/降权后 last 里存着的旧分组不能继续显示。
 */
function useActiveGroup(groups: NavGroup[]): NavGroup | null {
  const { pathname } = useLocation()
  const [last, setLast] = useState<NavGroup | null>(null)
  const hit = matchPage(pathname, groups.flatMap((g) => pagesOfNav(g)))
  const current = hit?.nav ?? null
  useEffect(() => {
    if (current) setLast(current)
  }, [current])
  const resolved = current ?? last
  return resolved && groups.includes(resolved) ? resolved : (groups[0] ?? null)
}

/** 品牌方块：图标栏顶部 / 移动端顶栏共用。整块中性色，把主色留给选中态。 */
function BrandMark({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <Tooltip title="回到首页" placement="right">
      <Box
        component={Link}
        to="/"
        aria-label="回到首页"
        onClick={onNavigate}
        sx={{
          width: 34,
          height: 34,
          flexShrink: 0,
          borderRadius: '12px',
          bgcolor: SURFACE.card,
          color: 'text.primary',
          display: 'grid',
          placeItems: 'center',
          fontSize: '0.7rem',
          fontWeight: 800,
          letterSpacing: '-.03em',
          textDecoration: 'none',
          userSelect: 'none'
        }}
      >
        FR
      </Box>
    </Tooltip>
  )
}

/** 深浅色切换（图标栏底部 / 移动端顶栏共用）。 */
function ThemeToggle() {
  const { mode, toggle } = useColorMode()
  return (
    <Tooltip title={mode === 'dark' ? '切换到浅色' : '切换到深色'} placement="right">
      <IconButton onClick={toggle} size="small" aria-label="切换深浅色">
        {mode === 'dark' ? <IconSun size="md" /> : <IconMoon size="md" />}
      </IconButton>
    </Tooltip>
  )
}

/** 图标栏的一个分组项：图标在上、短标签在下，选中整块填充主色。 */
function RailItem({
  group,
  active,
  unread,
  onOpenPanel,
  onNavigate
}: {
  group: NavGroup
  active: boolean
  unread: number
  onOpenPanel: () => void
  onNavigate?: () => void
}) {
  const meta = NAV_RAIL[group]
  const first = pagesOfNav(group)[0]
  const Icon = meta.icon
  if (!first) return null
  return (
    <Tooltip title={NAV_LABELS[group]} placement="right">
      <Box
        component={Link}
        to={first.path}
        aria-current={active ? 'page' : undefined}
        onClick={(e) => {
          // 已在本组：不把人踢回组首页，只把二级面板展开（收起状态下
          // 图标栏是唯一的展开入口）。
          if (active) e.preventDefault()
          onOpenPanel()
          onNavigate?.()
        }}
        sx={{
          position: 'relative',
          width: 60,
          py: 1,
          mb: 0.25,
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          gap: 0.5,
          borderRadius: '16px',
          textDecoration: 'none',
          cursor: 'pointer',
          color: active ? 'primary.contrastText' : 'text.secondary',
          bgcolor: active ? 'primary.main' : 'transparent',
          transition: 'background-color .15s ease, color .15s ease',
          '&:hover': {
            bgcolor: active ? 'primary.main' : 'action.hover',
            color: active ? 'primary.contrastText' : 'text.primary'
          }
        }}
      >
        <Icon size="lg" />
        <Typography
          component="span"
          sx={{
            fontSize: '0.6875rem',
            lineHeight: 1.2,
            fontWeight: active ? 600 : 500,
            color: 'inherit',
            userSelect: 'none'
          }}
        >
          {meta.label}
        </Typography>
        {/* admin 组有待处理事项时给一颗红点：不进面板也能看见告警在跳 */}
        {group === 'admin' && unread > 0 && (
          <Box
            aria-hidden="true"
            sx={{
              position: 'absolute',
              top: 7,
              right: 9,
              width: 7,
              height: 7,
              borderRadius: '50%',
              bgcolor: 'error.main'
            }}
          />
        )}
      </Box>
    </Tooltip>
  )
}

/** 图标栏（一级导航）：品牌 → 分组 → 弹性空间 → 主题 / 账户。 */
function NavRail({
  unread,
  onOpenPanel,
  onNavigate
}: {
  unread: number
  onOpenPanel: () => void
  onNavigate?: () => void
}) {
  const { session } = useSession()
  const groups = visibleGroups(session)
  const activeGroup = useActiveGroup(groups)
  const pendingOnly = session?.status === 'pending'

  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        height: '100%',
        py: 1.25,
        px: 1
      }}
    >
      <BrandMark onNavigate={onNavigate} />
      <Box
        component="nav"
        aria-label="一级导航"
        sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', mt: 1.5 }}
      >
        {groups.map((g) => (
          <RailItem
            key={g}
            group={g}
            active={g === activeGroup}
            unread={unread}
            onOpenPanel={onOpenPanel}
            onNavigate={onNavigate}
          />
        ))}
      </Box>
      {pendingOnly && (
        <Typography
          variant="caption"
          color="text.disabled"
          sx={{ mt: 1.5, px: 0.5, textAlign: 'center' }}
        >
          审核中
        </Typography>
      )}
      <Box sx={{ flex: 1 }} />
      <ThemeToggle />
      <AccountMenu variant="rail" onNavigate={onNavigate} />
    </Box>
  )
}

/** 二级面板里的一页：图标 + 标题 + 一句话说明（截断）。选中整块填充主色。 */
function PanelItem({
  page,
  active,
  unread,
  onNavigate
}: {
  page: PageDef
  active: boolean
  unread: number
  onNavigate?: () => void
}) {
  const Icon = page.icon
  return (
    <ListItemButton
      component={Link}
      to={page.path}
      onClick={onNavigate}
      selected={active}
      aria-current={active ? 'page' : undefined}
      sx={{
        alignItems: 'flex-start',
        gap: 1,
        px: 1.25,
        py: 0.75,
        mb: 0.25,
        '&.Mui-selected': {
          bgcolor: 'primary.main',
          color: 'primary.contrastText',
          '&:hover': { bgcolor: 'primary.main' }
        }
      }}
    >
      {Icon && (
        <Box
          component="span"
          sx={{
            mt: '2px',
            display: 'inline-flex',
            flexShrink: 0,
            color: active ? 'inherit' : 'text.disabled',
            transition: 'color .12s ease'
          }}
        >
          <Icon size="md" />
        </Box>
      )}
      <Box sx={{ minWidth: 0, flex: '1 1 auto' }}>
        <Typography
          noWrap
          sx={{ fontSize: '0.8125rem', fontWeight: active ? 600 : 500, lineHeight: 1.4 }}
        >
          {page.title}
        </Typography>
        {/* 一句话说明当副行（Tingly Box 的团队项是"名字 + 小字副行"）：
            面板里就能预判每页干什么，不用点进去试错。 */}
        <Typography
          noWrap
          variant="caption"
          sx={{
            display: 'block',
            color: active ? 'inherit' : 'text.disabled',
            opacity: active ? 0.78 : 1
          }}
        >
          {page.summary}
        </Typography>
      </Box>
      {page.path === '/admin/inbox' && unread > 0 && (
        <Chip
          label={unread > 99 ? '99+' : unread}
          size="small"
          sx={{
            height: 20,
            fontSize: '0.6875rem',
            mt: '2px',
            flexShrink: 0,
            // 选中项本身已是主色实心，徽章翻成半透明白底才读得清
            ...(active
              ? { bgcolor: 'rgba(255,255,255,.3)', color: 'inherit' }
              : { bgcolor: 'primary.main', color: 'primary.contrastText' }),
            '& .MuiChip-label': { px: 0.75 }
          }}
          aria-label={`${unread} 条未读`}
        />
      )}
      {page.dangerous && (
        <Tooltip title="本页含危险写操作">
          <Box
            component="span"
            sx={{
              mt: '3px',
              display: 'inline-flex',
              flexShrink: 0,
              color: active ? 'inherit' : 'text.disabled',
              opacity: active ? 0.85 : 0.7
            }}
          >
            <IconLock size="xs" />
          </Box>
        </Tooltip>
      )}
    </ListItemButton>
  )
}

/** 二级面板：组名 + 收起钮 → 组内页面 → 版本。桌面与移动抽屉共用一份。 */
function NavPanel({
  unread,
  version,
  onCollapse,
  onNavigate
}: {
  unread: number
  version: string
  onCollapse?: () => void
  onNavigate?: () => void
}) {
  const { session } = useSession()
  const groups = visibleGroups(session)
  const activeGroup = useActiveGroup(groups)
  const { pathname } = useLocation()
  const pages = activeGroup ? pagesOfNav(activeGroup) : []
  const activePage = matchPage(pathname, groups.flatMap((g) => pagesOfNav(g)))
  const pendingOnly = session?.status === 'pending'

  return (
    <Box
      sx={{
        width: PANEL_WIDTH,
        flexShrink: 0,
        display: 'flex',
        flexDirection: 'column',
        height: '100%'
      }}
    >
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          gap: 1,
          px: 1.5,
          pt: 1.25,
          pb: 0.75,
          minHeight: 52
        }}
      >
        <Typography
          noWrap
          sx={{ fontWeight: 700, fontSize: '0.9375rem', letterSpacing: '-.01em' }}
        >
          {activeGroup ? NAV_LABELS[activeGroup] : '导航'}
        </Typography>
        <Box sx={{ flex: 1 }} />
        {onCollapse && (
          <Tooltip title="收起面板（点左侧图标可再展开）">
            <IconButton size="small" onClick={onCollapse} aria-label="收起二级导航面板">
              <IconChevronsLeft size="md" />
            </IconButton>
          </Tooltip>
        )}
      </Box>
      <Box
        component="nav"
        aria-label="二级导航"
        sx={{ flex: 1, overflowY: 'auto', overscrollBehavior: 'contain', px: 1, pb: 1 }}
      >
        <List dense disablePadding>
          {pages.map((p) => (
            <PanelItem
              key={p.path}
              page={p}
              active={activePage?.path === p.path}
              unread={unread}
              onNavigate={onNavigate}
            />
          ))}
        </List>
        {pendingOnly && (
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', px: 1.5, pt: 1 }}>
            审核期间只能访问「待审核」页；审核通过后这里会出现可用入口。
          </Typography>
        )}
        {!pendingOnly && session && session.classes.length === 0 && (
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', px: 1.5, pt: 1 }}>
            你的账户尚未获得任何权限类。等待管理员审核通过后即可调用模型。
          </Typography>
        )}
      </Box>
      {/* 版本常驻面板底部（Tingly Box 同位）：排查"页面是不是旧版"时不用翻页脚 */}
      {version && (
        <Typography
          variant="caption"
          align="center"
          sx={{ py: 1, color: 'text.disabled', userSelect: 'text' }}
        >
          v{version}
        </Typography>
      )}
    </Box>
  )
}

/** 账户菜单：头像 + 下拉（显示名、权限类、到期提示、登出）。 */
function AccountMenu({
  variant = 'bar',
  onNavigate
}: {
  /** rail = 图标栏底部（未登录渲染成跳登录的图标钮）；bar = 移动端顶栏（保留文字链接）。 */
  variant?: 'rail' | 'bar'
  onNavigate?: () => void
}) {
  const { session, setSession } = useSession()
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  const [anchor, setAnchor] = useState<null | HTMLElement>(null)

  if (!session) {
    if (variant === 'rail') {
      return (
        <Tooltip title="登录 / 注册" placement="right">
          <IconButton
            component={Link}
            to="/login"
            onClick={onNavigate}
            size="small"
            aria-label="登录或注册"
          >
            <IconUser size="md" />
          </IconButton>
        </Tooltip>
      )
    }
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
      onNavigate?.()
      navigate('/login', { replace: true })
    }
  }

  const initial = (session.display_name || '?').trim().slice(0, 1).toUpperCase() || '?'

  return (
    <>
      <Tooltip title="账户" placement={variant === 'rail' ? 'right' : 'bottom'}>
        <IconButton
          onClick={(e) => setAnchor(e.currentTarget)}
          size="small"
          sx={variant === 'rail' ? { mt: 0.75 } : { ml: 0.5 }}
        >
          <Avatar
            sx={{ width: 30, height: 30, fontSize: '0.82rem', fontWeight: 700, bgcolor: SURFACE.inset, color: 'text.primary' }}
          >
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
        slotProps={{ paper: { sx: { minWidth: 230, mt: 0.75 } } }}
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
            onNavigate?.()
            navigate('/account')
          }}
        >
          <Box sx={{ mr: 1.25, display: 'inline-flex', color: 'text.secondary' }}>
            <IconUser size="md" />
          </Box>
          账户设置
        </MenuItem>
        <MenuItem onClick={logout} disabled={busy}>
          <Box sx={{ mr: 1.25, display: 'inline-flex', color: 'text.secondary' }}>
            <IconLogout size="md" />
          </Box>
          {busy ? '正在登出…' : '登出'}
        </MenuItem>
      </Menu>
    </>
  )
}

/** 健康与版本诊断：Shell 取一次，页脚与二级面板底部共用。 */
interface Diagnostics {
  healthz: boolean | null
  readyz: boolean | null
  version: string
  built: string
}

function useDiagnostics(): Diagnostics {
  const [diag, setDiag] = useState<Diagnostics>({ healthz: null, readyz: null, version: '', built: '' })

  useEffect(() => {
    let alive = true
    const probe = (path: string) =>
      fetch(path, { credentials: 'include' })
        .then((r) => r.ok)
        .catch(() => false)
    void Promise.all([probe(PATHS.healthz), probe(PATHS.readyz)]).then(([h, r]) => {
      if (alive) setDiag((s) => ({ ...s, healthz: h, readyz: r }))
    })
    fetch(PATHS.version, { credentials: 'include' })
      .then((r) => (r.ok ? r.json() : null))
      .then((v) => {
        if (alive && v && typeof v.version === 'string') {
          setDiag((s) => ({
            ...s,
            version: v.version,
            built: typeof v.webui_built === 'boolean' ? (v.webui_built ? '已嵌入前端' : '未嵌入前端') : ''
          }))
        }
      })
      .catch(() => {
        /* 版本是装饰，取不到就留空 */
      })
    return () => {
      alive = false
    }
  }, [])

  return diag
}

/** 页脚：版本与构建标识 · /healthz 与 /readyz 的轻量指示。 */
function Footer({ diag }: { diag: Diagnostics }) {
  const mark = (ok: boolean | null) => (ok === null ? '…' : ok ? '正常' : '离线')
  const dot = (ok: boolean | null) =>
    ok === null ? 'text.disabled' : ok ? 'success.main' : 'error.main'

  return (
    <Box
      component="footer"
      sx={{
        px: { xs: 2, md: 3 },
        py: 1.25,
        // 页脚用 chrome 同色收底：它是窗口镶边的延续，不与内容混在一起
        bgcolor: SURFACE.chrome,
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
        FreeRouter 控制台{diag.version ? ` · v${diag.version}` : ''}
        {diag.built ? ` · ${diag.built}` : ''}
        {/* 前端构建标识：排查“页面是不是旧版”时唯一能自助回答的东西。
            （服务端 version 可能恒为 dev——不经 ldflags 的 go build 就会这样。） */}
        {` · 前端构建 ${__BUILD_STAMP__}`}
      </Typography>
      <Box sx={{ display: 'flex', gap: 2 }}>
        {(['healthz', 'readyz'] as const).map((k) => (
          <Box key={k} sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.75 }}>
            <Box
              aria-hidden="true"
              sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: dot(diag[k]) }}
            />
            <Typography variant="caption">
              /{k}：{mark(diag[k])}
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
  // 二级面板展开/收起：localStorage 记忆（收起是明确偏好，刷新后保持）
  const [panelOpen, setPanelOpen] = useState(() => {
    try {
      return localStorage.getItem(PANEL_STORAGE_KEY) !== '0'
    } catch {
      return true
    }
  })
  useEffect(() => {
    try {
      localStorage.setItem(PANEL_STORAGE_KEY, panelOpen ? '1' : '0')
    } catch {
      /* 隐私模式写不进就算了 */
    }
  }, [panelOpen])
  const diag = useDiagnostics()
  const openPanel = () => setPanelOpen(true)

  return (
    <Box sx={{ height: '100dvh', display: 'flex', overflow: 'hidden', bgcolor: SURFACE.page }}>
      {/* 移动端顶栏（< md）：品牌 + 全局动作；业务导航在抽屉里 */}
      <AppBar
        position="fixed"
        color="inherit"
        elevation={0}
        sx={{
          display: { xs: 'block', md: 'none' },
          bgcolor: SURFACE.chrome,
          backdropFilter: 'saturate(140%) blur(12px)',
          borderBottom: '1px solid',
          borderColor: 'divider'
        }}
      >
        <Toolbar sx={{ minHeight: MOBILE_BAR_HEIGHT, gap: 1 }}>
          <BrandMark onNavigate={() => setNavOpen(false)} />
          <Typography sx={{ fontWeight: 700, fontSize: '0.9375rem', letterSpacing: '-.015em' }}>
            FreeRouter
          </Typography>
          <Box sx={{ flex: 1 }} />
          <ThemeToggle />
          <AccountMenu variant="bar" onNavigate={() => setNavOpen(false)} />
          <IconButton
            edge="end"
            onClick={() => setNavOpen(true)}
            size="small"
            aria-label="打开导航"
            sx={{ ml: 0.5 }}
          >
            <IconMenu size="md" />
          </IconButton>
        </Toolbar>
      </AppBar>

      {/* 移动端抽屉：内嵌同款「图标栏 + 二级面板」两列，交互与桌面完全一致 */}
      <Drawer
        open={!desktop && navOpen}
        onClose={() => setNavOpen(false)}
        ModalProps={{ keepMounted: true }}
        sx={{ display: { xs: 'block', md: 'none' } }}
        slotProps={{
          paper: {
            sx: {
              width: RAIL_WIDTH - 4 + PANEL_WIDTH,
              bgcolor: SURFACE.chrome,
              backgroundImage: 'none'
            }
          }
        }}
      >
        <Toolbar sx={{ minHeight: MOBILE_BAR_HEIGHT }} />
        <Box sx={{ display: 'flex', height: `calc(100% - ${MOBILE_BAR_HEIGHT}px)` }}>
          <Box
            component="nav"
            aria-label="一级导航"
            sx={{ width: RAIL_WIDTH - 4, flexShrink: 0, borderRight: '1px solid', borderColor: 'divider' }}
          >
            <NavRail
              unread={unread}
              onOpenPanel={openPanel}
              onNavigate={() => setNavOpen(false)}
            />
          </Box>
          <Box component="nav" aria-label="二级导航" sx={{ flex: 1, minWidth: 0 }}>
            <NavPanel
              unread={unread}
              version={diag.version}
              onNavigate={() => setNavOpen(false)}
            />
          </Box>
        </Box>
      </Drawer>

      {/* 桌面图标栏（一级导航）：固定宽度，不动不缩放 */}
      <Box
        component="nav"
        aria-label="一级导航"
        sx={{
          display: { xs: 'none', md: 'flex' },
          flexDirection: 'column',
          width: RAIL_WIDTH,
          flexShrink: 0,
          // chrome：镶边比内容区深一档，与内容交界处一条细线
          bgcolor: SURFACE.chrome,
          borderRight: '1px solid',
          borderColor: 'divider'
        }}
      >
        <NavRail unread={unread} onOpenPanel={openPanel} />
      </Box>

      {/* 桌面二级面板：可整列收起（宽度动画，内容固定宽不被压扁） */}
      <Box
        component="nav"
        aria-label="二级导航"
        sx={{
          display: { xs: 'none', md: 'flex' },
          flexDirection: 'column',
          width: panelOpen ? PANEL_WIDTH : 0,
          flexShrink: 0,
          bgcolor: SURFACE.chrome,
          borderRight: panelOpen ? '1px solid' : 'none',
          borderColor: 'divider',
          overflow: 'hidden',
          transition: theme.transitions.create('width', { duration: 180 })
        }}
      >
        <NavPanel
          unread={unread}
          version={diag.version}
          onCollapse={() => setPanelOpen(false)}
        />
      </Box>

      {/* 内容列：内部滚动（应用式壳），页脚钉在列底不随内容滚走 */}
      <Box
        component="main"
        sx={{
          flex: 1,
          minWidth: 0, // 关键：没有它，宽表格会把整个 flex 容器撑破
          display: 'flex',
          flexDirection: 'column',
          pt: { xs: `${MOBILE_BAR_HEIGHT}px`, md: 0 }
        }}
      >
        <Box
          sx={{
            flex: 1,
            overflowY: 'auto',
            overscrollBehavior: 'contain',
            px: { xs: 2, md: 3 },
            py: { xs: 2.5, md: 3 }
          }}
        >
          <PendingGate />
          <MustChangeGate />
          {/* 内容区不再封顶到 1400px：宽屏上控制台的表格需要横向空间，
              而长段落已各自用 maxWidth ch 限制可读宽度 */}
          <Box>
            <Routes>
              {PAGES.map(toRoute)}
              {/* catch-all：未匹配路由与非法 /error/:code 共用 404 页；
                  对当前身份不可见的入口由 RequirePage 统一送 /error/404
                  （01-ia.md 第 7 节：路由层不确认资源是否存在）。 */}
              <Route path="*" element={<ErrorPage def={NOT_FOUND_DEF} />} />
            </Routes>
          </Box>
        </Box>
        <Footer diag={diag} />
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
