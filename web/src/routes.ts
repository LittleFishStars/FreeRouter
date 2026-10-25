// 路由表：全站页面清单的唯一事实来源（对照 docs/ui/01-ia.md 第 4 节）。
//
// 为什么做成纯数据表而不是散落的 <Route>：IA 文档可能随时增删页面
// （docs/ops/04-collaboration.md 第 6 节），表驱动下「改 IA」=「改这张表 +
// 需要时换 element 组件」，App.tsx 与布局骨架一行都不用动。
// 导航渲染、权限过滤（按 classes）、危险写标记，全部从这张表派生。
//
// 🔒 集合严格按 IA 4.2–4.5 逐行对齐：它是全站唯一的标记，多标/漏标都等于
// 让「标记的含义」失真（06-design.md 5.1：确认框不承担认证）。

import type { ComponentType } from 'react'
import type { IconSize } from './icons'
import {
  IconNavAbstractModels,
  IconNavAccount,
  IconNavAccounts,
  IconNavAdminReview,
  IconNavAudit,
  IconNavContribute,
  IconNavContributions,
  IconNavCredentials,
  IconNavGroups,
  IconNavHome,
  IconNavInbox,
  IconNavKeys,
  IconNavLogs,
  IconNavModels,
  IconNavPassword,
  IconNavRealModels,
  IconNavSessions,
  IconNavSettings,
  IconNavUpstreams,
  IconNavUsage,
  IconNavVerifications
} from './icons'
import SetupPage from './pages/SetupPage'
import LoginPage from './pages/LoginPage'
import HomePage from './pages/HomePage'
import RegisterPage from './pages/RegisterPage'
import PendingPage from './pages/PendingPage'
import ContributePage, {
  ContributeCallbackPage,
  ContributeOAuthPage,
  ContributeRecoveryPage
} from './pages/ContributePage'
import AccountPage, { PasswordPage, SessionsPage } from './pages/AccountPages'
import ErrorPage from './pages/ErrorPage'
import ReviewPage from './pages/admin/ReviewPage'
import InboxPage from './pages/admin/InboxPage'
import {
  AccountDetailPage,
  AccountsPage,
  ImportAccountsPage
} from './pages/admin/AccountPages'
import { GroupDetailPage, GroupsPage } from './pages/admin/GroupPages'
import { RealModelsPage } from './pages/admin/RealModelPage'
import { AbstractModelDetailPage, AbstractModelsPage } from './pages/admin/AbstractModelPage'
import { CandidatesPage, DeprecationPage } from './pages/admin/DerivationPages'
import {
  CredentialDetailPage,
  CredentialsPage,
  UpstreamsPage
} from './pages/admin/OpsPages'
import { VerificationDetailPage, VerificationsPage } from './pages/admin/VerificationPages'
import AuditPage from './pages/admin/AuditPage'
import SettingsPage from './pages/admin/SettingsPage'
import { KeyDetailPage, KeyNewPage, KeysPage } from './pages/tenant/KeyPages'
import { LogsPage, ModelsPage, UsagePage } from './pages/tenant/UsagePages'
import {
  ContributionCredentialDetailPage,
  ContributionStatsPage,
  ContributionsOverviewPage,
  MyCredentialsPage,
  RecoveryCodePage
} from './pages/contributor/ContributorPages'

/** 页面组件的统一 props：路由表条目本身。页面从中取标题与一句话说明。 */
export interface PageProps {
  def: PageDef
}

/** 一级导航分组（docs/ui/01-ia.md 3.3：各入口互不嵌套、互不跳转）。 */
export type NavGroup = 'public' | 'account' | 'admin' | 'usage' | 'contributions'

/** 页面可见性要求：导航与路由守卫都从这里派生（01-ia.md 3.2 R1–R4）。 */
export type Requires = 'none' | 'auth' | 'admin' | 'tenant' | 'contributor'

export interface PageDef {
  /** 路由路径（IA 里的 :id 参数原样保留）。 */
  path: string
  /** 页面标题（内容区固定顺序第 2 项，docs/ui/06-design.md 2.2）。 */
  title: string
  /** 标题下的一句话说明——规范要求：缺了就是缺陷。 */
  summary: string
  /** 所属一级导航分组；null 表示不出现在任何导航里（详情页、回调页、错误页）。 */
  nav: NavGroup | null
  /** 可见性要求（渲染输入 = classes ∪ status，docs/ui/01-ia.md 3.1）。 */
  requires: Requires
  /** IA 里的 🔒：该页存在危险写，提交前必须走统一二次确认框（06-design.md 5）。 */
  dangerous?: boolean
  /**
   * 导航项图标（详见 src/icons.tsx）。
   *
   * 为什么图标挂在**页面定义**上而不是导航渲染里：同一个页面可能同时出现在
   * 侧栏、面包屑、空态动作里，图标是这一页的身份，属于 IA 数据的一部分。
   * 详情页（nav=null）不需要图标——它们不在任何导航里出现。
   */
  icon?: ComponentType<{ size?: IconSize | number; className?: string }>
  /** 实现该页的组件。 */
  element: ComponentType<PageProps>
}

export const NAV_LABELS: Record<NavGroup, string> = {
  public: '公共区',
  account: '账户',
  admin: '管理',
  usage: '我的使用',
  contributions: '我的贡献'
}

/** 进入某个一级导航所需的权限类（public/account 恒可见）。 */
export const NAV_REQUIRES: Partial<Record<NavGroup, Requires>> = {
  admin: 'admin',
  usage: 'tenant',
  contributions: 'contributor'
}

export const PAGES: PageDef[] = [
  // ── 4.1 公共区 ──────────────────────────────────────────────
  { path: '/', title: '首页', summary: '未登录时是落地页；已登录按权限类渲染概览。', nav: 'public', requires: 'none', icon: IconNavHome, element: HomePage },
  { path: '/login', title: '登录', summary: '密码登录；会话为 HttpOnly Cookie，浏览器自动管理。', nav: null, requires: 'none', element: LoginPage },
  { path: '/register', title: '注册', summary: '默认开启但需审核；审核只针对调用能力。', nav: null, requires: 'none', element: RegisterPage },
  { path: '/pending', title: '待审核提示', summary: '仅待审核账户登录后可见的状态页。', nav: null, requires: 'auth', element: PendingPage },
  { path: '/contribute', title: '贡献入口', summary: '左栏反代供应商（账号登录），右栏 API 供应商（内置目录 + 自定义端点）；未登录可用。', nav: 'public', requires: 'none', icon: IconNavContribute, element: ContributePage },
  { path: '/contribute/oauth/:provider', title: '供应商授权', summary: '跳转第三方授权，回调落在 /contribute/callback。', nav: null, requires: 'none', element: ContributeOAuthPage },
  { path: '/contribute/callback', title: '贡献回调', summary: '受理结果与（未登录时）可恢复路径。', nav: null, requires: 'none', element: ContributeCallbackPage },
  { path: '/contribute/recovery', title: '恢复码兑换', summary: '纯 Key 型贡献者的一次性恢复路径。', nav: null, requires: 'none', element: ContributeRecoveryPage },

  // ── 4.2 账户自助（任何已登录 active 账户）───────────────────
  { path: '/account', title: '账户概览', summary: '显示名、账户 ID、权限、登录方式与最近登录。', nav: 'account', requires: 'auth', icon: IconNavAccount, element: AccountPage },
  { path: '/account/password', title: '自助改密', summary: '改密即吊销全部会话（含当前会话）并强制重新登录。', nav: 'account', requires: 'auth', icon: IconNavPassword, element: PasswordPage },
  { path: '/account/sessions', title: '我的会话', summary: '会话列表与单独吊销。', nav: 'account', requires: 'auth', icon: IconNavSessions, element: SessionsPage },

  // ── 4.3 管理入口（admin 类）─────────────────────────────────
  { path: '/admin', title: '审核队列', summary: '管理入口默认落点：注册申请队列置顶。', nav: 'admin', requires: 'admin', icon: IconNavAdminReview, dangerous: true, element: ReviewPage },
  { path: '/admin/inbox', title: '待处理事项', summary: '注册申请 / 模型变动 / 故障通知 / 池与熔断告警（导航唯一带徽标项）。', nav: 'admin', requires: 'admin', icon: IconNavInbox, dangerous: true, element: InboxPage },
  { path: '/admin/accounts', title: '账户列表', summary: '账户检索与筛选；只看数量，不给跨类资源列表。', nav: 'admin', requires: 'admin', icon: IconNavAccounts, dangerous: true, element: AccountsPage },
  { path: '/admin/accounts/import', title: '导入账密', summary: '管理员批量导入账户（CSV），一次性展示生成的密码。', nav: null, requires: 'admin', dangerous: true, element: ImportAccountsPage },
  { path: '/admin/accounts/:id', title: '账户详情', summary: '权限类管理、禁用 / 合并 / 合并回滚。', nav: null, requires: 'admin', dangerous: true, element: AccountDetailPage },
  { path: '/admin/groups', title: '组列表', summary: '组是管理员的批量控制单元，不产生成员可见的组织感。', nav: 'admin', requires: 'admin', icon: IconNavGroups, dangerous: true, element: GroupsPage },
  { path: '/admin/groups/:id', title: '组详情', summary: '组成员、模型授权与策略（授权 / 策略 / 成员 / 默认组 / 删组均为危险写）。', nav: null, requires: 'admin', dangerous: true, element: GroupDetailPage },
  { path: '/admin/real-models', title: '真实模型目录', summary: '含 missing 巡检；真实模型名仅管理员可见。', nav: 'admin', requires: 'admin', icon: IconNavRealModels, dangerous: true, element: RealModelsPage },
  { path: '/admin/abstract-models', title: '抽象模型列表', summary: '对用户暴露的唯一模型命名空间。', nav: 'admin', requires: 'admin', icon: IconNavAbstractModels, dangerous: true, element: AbstractModelsPage },
  { path: '/admin/abstract-models/:id', title: '抽象模型编辑', summary: '能力与策略链编辑（保存类动作需二次确认）。', nav: null, requires: 'admin', dangerous: true, element: AbstractModelDetailPage },
  { path: '/admin/abstract-models/:id/candidates', title: '候选集编辑', summary: '该抽象模型的真实模型候选集与可用实例预览。', nav: null, requires: 'admin', dangerous: true, element: CandidatesPage },
  { path: '/admin/abstract-models/:id/deprecation', title: '弃用配置', summary: '弃用与替代链预览（对应用户可见的 410 Gone）。', nav: null, requires: 'admin', dangerous: true, element: DeprecationPage },
  { path: '/admin/upstreams', title: '上游管理', summary: 'relay 与 vendor（含 selfhosted）渠道。', nav: 'admin', requires: 'admin', icon: IconNavUpstreams, dangerous: true, element: UpstreamsPage },
  { path: '/admin/credentials', title: '凭据池', summary: '含平台自有凭据；明文永不显示。', nav: 'admin', requires: 'admin', icon: IconNavCredentials, dangerous: true, element: CredentialsPage },
  { path: '/admin/credentials/:id', title: '凭据详情', summary: '封禁动作走统一二次确认框。', nav: null, requires: 'admin', dangerous: true, element: CredentialDetailPage },
  { path: '/admin/verifications', title: '鉴定队列', summary: '鉴定与复检队列。', nav: 'admin', requires: 'admin', icon: IconNavVerifications, dangerous: true, element: VerificationsPage },
  { path: '/admin/verifications/:id', title: '鉴定结果', summary: '判定与证据明细。', nav: null, requires: 'admin', dangerous: true, element: VerificationDetailPage },
  { path: '/admin/audit', title: '审计日志', summary: 'append-only，只读。', nav: 'admin', requires: 'admin', icon: IconNavAudit, dangerous: true, element: AuditPage },
  { path: '/admin/settings', title: '全局设置', summary: '启动期配置的只读展示；开放注册与代理相关项改动需二次确认。', nav: 'admin', requires: 'admin', icon: IconNavSettings, dangerous: true, element: SettingsPage },

  // ── 4.4 租户入口（tenant 类）────────────────────────────────
  { path: '/usage', title: '用量与配额', summary: '账户级额度总览（不显示额度来自哪个组）。', nav: 'usage', requires: 'tenant', icon: IconNavUsage, element: UsagePage },
  { path: '/usage/keys', title: '我的 API Key', summary: 'Key 列表：名称、前缀、白名单、生命周期。', nav: 'usage', requires: 'tenant', icon: IconNavKeys, element: KeysPage },
  { path: '/usage/keys/new', title: '创建 Key', summary: '明文一次性展示，离开后不再可见。', nav: null, requires: 'tenant', dangerous: true, element: KeyNewPage },
  { path: '/usage/keys/:id', title: 'Key 详情', summary: '模型白名单、过期时间、最近调用；额度与速率只读展示账户值。', nav: null, requires: 'tenant', element: KeyDetailPage },
  { path: '/usage/logs', title: '调用日志', summary: '只出现抽象模型名，不泄露真实模型。', nav: 'usage', requires: 'tenant', icon: IconNavLogs, element: LogsPage },
  { path: '/usage/models', title: '可用抽象模型', summary: '（∪ 所属组的授权）∩ Key 白名单的收敛结果。', nav: 'usage', requires: 'tenant', icon: IconNavModels, element: ModelsPage },

  // ── 4.5 贡献者入口（contributor 类）─────────────────────────
  { path: '/contributions', title: '我的贡献', summary: '贡献总览与入口。', nav: 'contributions', requires: 'contributor', icon: IconNavContributions, element: ContributionsOverviewPage },
  { path: '/contributions/credentials', title: '我的凭据', summary: '凭据列表与鉴定状态。', nav: 'contributions', requires: 'contributor', element: MyCredentialsPage },
  { path: '/contributions/credentials/:id', title: '凭据详情', summary: '标签、状态、鉴定判定与证据；不显示被哪个请求用掉。', nav: null, requires: 'contributor', element: ContributionCredentialDetailPage },
  { path: '/contributions/stats', title: '贡献统计', summary: '被调用统计与无偿说明（没有「收益」区域）。', nav: 'contributions', requires: 'contributor', element: ContributionStatsPage },
  { path: '/contributions/recovery-code', title: '恢复码', summary: '仅纯 Key 型账户，展示一次。', nav: null, requires: 'contributor', dangerous: true, element: RecoveryCodePage },

  // ── 错误页（4.1 末行，四个合法码显式入表；其余走 catch-all 404）──────
  { path: '/error/403', title: '无权访问', summary: '当前账户没有该页所需的能力。', nav: null, requires: 'none', element: ErrorPage },
  { path: '/error/404', title: '页面不存在', summary: '资源不存在，或对该账户不可见。', nav: null, requires: 'none', element: ErrorPage },
  { path: '/error/410', title: '已不可用', summary: '实体曾经存在，现已禁用或弃用。', nav: null, requires: 'none', element: ErrorPage },
  { path: '/error/500', title: '服务异常', summary: '未预期的服务端错误。', nav: null, requires: 'none', element: ErrorPage },

  // ── 初始化向导（4.3 末行；无账户时唯一可见页面）──────────────
  { path: '/setup', title: '初始化向导', summary: '无账户时唯一可见的页面：创建第一个管理员。', nav: null, requires: 'none', element: SetupPage }
]

/** 按一级导航分组取页面。 */
export function pagesOfNav(group: NavGroup): PageDef[] {
  return PAGES.filter((p) => p.nav === group)
}

/** 用路径查表（面包屑与导航高亮用）。 */
export function findPage(path: string): PageDef | undefined {
  return PAGES.find((p) => p.path === path)
}

// ── 路由表派生的可见性 / 面包屑辅助 ──────────────────────────────────

/** 会话权限类是否满足页面的 requires。 */
export function canAccess(requires: Requires, classes: string[]): boolean {
  switch (requires) {
    case 'none':
    case 'auth':
      return true
    case 'admin':
      return classes.includes('admin')
    case 'tenant':
      return classes.includes('tenant')
    case 'contributor':
      return classes.includes('contributor')
  }
}

/** 某个一级导航是否应对该 classes 渲染（01-ia.md 3.2 R1–R4 的纯函数）。 */
export function navVisible(group: NavGroup, classes: string[]): boolean {
  const need = NAV_REQUIRES[group]
  if (!need) return true
  return canAccess(need, classes)
}

function patternSegments(path: string): string[] {
  return path.split('/').filter(Boolean)
}

/** pattern 是否是 actual 的「严格前缀」（允许 :param 段，长度必须 actual 少一段）。 */
function isParentPattern(pattern: string, actual: string): boolean {
  const ps = patternSegments(pattern)
  const as = patternSegments(actual)
  if (ps.length === 0 || ps.length !== as.length - 1) return false
  return ps.every((seg, i) => seg.startsWith(':') || seg === as[i])
}

/**
 * 找直接父页面：按路由表前缀推导，而不是依赖 nav 字段。
 * 详情页（nav=null）因此也能有面包屑（06-design.md 2.2 第 1 项）。
 */
export function findParentPage(path: string): PageDef | undefined {
  const candidates = PAGES.filter((p) => p.path !== path && isParentPattern(p.path, path))
  if (candidates.length === 0) return undefined
  // 更长的模式优先（含参数段的父级同样参与比较）
  candidates.sort((a, b) => patternSegments(b.path).length - patternSegments(a.path).length)
  return candidates[0]
}

/**
 * 面包屑链：一级导航标签 → 祖先页面标题 → 当前页标题。
 * 只向上追溯到第一个「本身在一级导航里的父页面」为止，避免把 -admin
 * 这种默认落点当成所有子页面的父级（那会读成「管理 / 审核队列 / 账户列表」）。
 */
export function breadcrumbTrail(def: PageDef): string[] {
  if (def.nav) return [NAV_LABELS[def.nav], def.title]
  const chain: string[] = [def.title]
  let cur = findParentPage(def.path)
  let navLabel: string | undefined
  while (cur) {
    chain.unshift(cur.title)
    if (cur.nav) {
      navLabel = NAV_LABELS[cur.nav]
      break
    }
    cur = findParentPage(cur.path)
  }
  if (navLabel) chain.unshift(navLabel)
  return chain
}
