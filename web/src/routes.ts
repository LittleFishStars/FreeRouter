// 路由表：全站页面清单的唯一事实来源（对照 docs/ui/01-ia.md 第 4 节）。
import type { ComponentType } from 'react'
//
// 为什么做成纯数据表而不是散落的 <Route>：IA 文档可能随时增删页面
// （docs/ops/04-collaboration.md 第 6 节），表驱动下「改 IA」=「改这张表 +
// 需要时换 element 组件」，App.tsx 与布局骨架一行都不用动。
// 导航渲染、权限过滤（按 classes）、危险写标记，全部从这张表派生。

// 值导入（不是 import type）：下面 PAGES 数组要把组件当值引用。
// verbatimModuleSyntax 下 `import type X` 再用作值会直接编译报错。
import SetupPage from './pages/SetupPage'
import LoginPage from './pages/LoginPage'
import ConsolePage from './pages/ConsolePage'
import NotFoundPage from './pages/NotFoundPage'

/** 页面组件的统一 props：路由表条目本身。页面从中取标题与一句话说明。 */
export interface PageProps {
  def: PageDef
}

/** 一级导航分组（docs/ui/01-ia.md 3.3：各入口互不嵌套、互不跳转）。 */
export type NavGroup = 'public' | 'account' | 'admin' | 'usage' | 'contributions'

/** 页面可见性要求：占位阶段仅用于分组标注，接入鉴权后由 classes 过滤。 */
export type Requires = 'none' | 'auth' | 'admin' | 'tenant' | 'contributor'

export interface PageDef {
  /** 路由路径（IA 里的 :id 参数原样保留）。 */
  path: string
  /** 页面标题（内容区固定顺序第 2 项，docs/ui/06-design.md 2.2）。 */
  title: string
  /** 标题下的一句话说明——规范要求：缺了就是缺陷。 */
  summary: string
  /** 所属一级导航分组；null 表示不出现在任何导航里（如回调页、错误页）。 */
  nav: NavGroup | null
  /** 可见性要求（渲染输入 = classes ∪ status，docs/ui/01-ia.md 3.1）。 */
  requires: Requires
  /** IA 里的 🔒：该页存在危险写，提交前必须走统一二次确认框（06-design.md 5）。 */
  dangerous?: boolean
  /** 实现该页的组件。占位期绝大多数指向 ConsolePage。 */
  element: ComponentType<PageProps>
}

export const NAV_LABELS: Record<NavGroup, string> = {
  public: '公共区',
  account: '账户',
  admin: '管理',
  usage: '我的使用',
  contributions: '我的贡献'
}

export const PAGES: PageDef[] = [
  // ── 4.1 公共区 ──────────────────────────────────────────────
  { path: '/', title: '首页', summary: '未登录时是落地页；已登录按权限类分流。', nav: 'public', requires: 'none', element: ConsolePage },
  { path: '/login', title: '登录', summary: '密码登录；OAuth 登录入口见 02-public。', nav: 'public', requires: 'none', element: LoginPage },
  { path: '/register', title: '注册', summary: '默认开启但需审核；审核只针对 tenant 类。', nav: 'public', requires: 'none', element: ConsolePage },
  { path: '/pending', title: '待审核提示', summary: '仅 pending 账户登录后可见的唯一页面。', nav: null, requires: 'auth', element: ConsolePage },
  { path: '/contribute', title: '贡献入口', summary: '供应商 OAuth 与 API Key 两种贡献物，未登录可用。', nav: 'public', requires: 'none', element: ConsolePage },
  { path: '/contribute/oauth/:provider', title: '供应商 OAuth 跳转', summary: '跳转第三方授权，回调落在 /contribute/callback。', nav: null, requires: 'none', element: ConsolePage },
  { path: '/contribute/callback', title: '贡献回调', summary: '成功后进入「已受理」或新建账户的恢复路径页。', nav: null, requires: 'none', element: ConsolePage },
  { path: '/contribute/recovery', title: '恢复码兑换', summary: '纯 Key 型贡献者的一次性恢复路径。', nav: 'public', requires: 'none', element: ConsolePage },

  // ── 4.2 账户自助（任何已登录 active 账户）───────────────────
  { path: '/account', title: '账户概览', summary: '显示名、账户 ID、权限类徽章、登录方式、最近登录。', nav: 'account', requires: 'auth', element: ConsolePage },
  { path: '/account/password', title: '自助改密', summary: '改密即吊销全部会话（含当前会话）并强制重新登录。', nav: 'account', requires: 'auth', dangerous: true, element: ConsolePage },
  { path: '/account/sessions', title: '我的会话', summary: '会话列表与单独吊销。', nav: 'account', requires: 'auth', dangerous: true, element: ConsolePage },

  // ── 4.3 管理入口（admin 类）─────────────────────────────────
  { path: '/admin', title: '审核队列', summary: '管理入口默认落点：注册申请队列置顶。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/inbox', title: '待处理事项', summary: '注册申请 / 模型变动 / 故障通知 / 池与熔断告警（导航唯一带徽标项）。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/accounts', title: '账户列表', summary: '账户检索与筛选。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/accounts/:id', title: '账户详情', summary: '权限类管理、禁用 / 合并 / 合并回滚。', nav: null, requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/accounts/import', title: '导入账密', summary: '管理员批量导入账户（单个 / CSV）。', nav: null, requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/groups', title: '组列表', summary: '组是管理员的批量控制单元，不产生成员可见的组织感。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/groups/:id', title: '组详情', summary: '组成员、模型授权、策略（授权 / 策略 / 成员 / 默认组 / 删组均为危险写）。', nav: null, requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/real-models', title: '真实模型目录', summary: '含 missing 巡检；真实模型名仅管理员可见。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/abstract-models', title: '抽象模型列表', summary: '对用户暴露的唯一模型命名空间。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/abstract-models/:id', title: '抽象模型编辑', summary: '能力与策略链编辑（保存类动作需二次确认）。', nav: null, requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/abstract-models/:id/candidates', title: '候选集编辑', summary: '该抽象模型的真实模型候选集。', nav: null, requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/abstract-models/:id/deprecation', title: '弃用配置', summary: '弃用与替代链预览（对应用户可见的 410 Gone）。', nav: null, requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/upstreams', title: '上游管理', summary: 'relay 与 vendor（含 selfhosted）渠道。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/credentials', title: '凭据池', summary: '含平台自有凭据；明文永不显示。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/credentials/:id', title: '凭据详情', summary: '封禁动作走统一二次确认框。', nav: null, requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/verifications', title: '鉴定队列', summary: '鉴定与复检队列。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/verifications/:id', title: '鉴定结果', summary: '判定与证据明细。', nav: null, requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/admin/audit', title: '审计日志', summary: 'append-only，只读。', nav: 'admin', requires: 'admin', element: ConsolePage },
  { path: '/admin/settings', title: '全局设置', summary: '危险项（如关闭注册）动作需二次确认。', nav: 'admin', requires: 'admin', dangerous: true, element: ConsolePage },
  { path: '/setup', title: '初始化向导', summary: '无账户时唯一可见的页面：创建第一个管理员。', nav: null, requires: 'none', element: SetupPage },

  // ── 4.4 租户入口（tenant 类）────────────────────────────────
  { path: '/usage', title: '用量与配额', summary: '账户级额度总览（不显示额度来自哪个组）。', nav: 'usage', requires: 'tenant', element: ConsolePage },
  { path: '/usage/keys', title: '我的 API Key', summary: 'Key 列表。', nav: 'usage', requires: 'tenant', element: ConsolePage },
  { path: '/usage/keys/new', title: '创建 Key', summary: '明文一次性展示，离开后不再可见。', nav: null, requires: 'tenant', dangerous: true, element: ConsolePage },
  { path: '/usage/keys/:id', title: 'Key 详情', summary: '模型白名单、过期时间、最近调用；额度与速率只读展示账户值。', nav: null, requires: 'tenant', element: ConsolePage },
  { path: '/usage/logs', title: '调用日志', summary: '只出现抽象模型名，不泄露真实模型。', nav: 'usage', requires: 'tenant', element: ConsolePage },
  { path: '/usage/models', title: '可用抽象模型', summary: '（∪ 所属组的授权）∩ Key 白名单的收敛结果。', nav: 'usage', requires: 'tenant', element: ConsolePage },

  // ── 4.5 贡献者入口（contributor 类）─────────────────────────
  { path: '/contributions', title: '我的贡献', summary: '贡献总览。', nav: 'contributions', requires: 'contributor', element: ConsolePage },
  { path: '/contributions/credentials', title: '我的凭据', summary: '凭据列表与鉴定状态。', nav: 'contributions', requires: 'contributor', element: ConsolePage },
  { path: '/contributions/credentials/:id', title: '凭据详情', summary: '标签、状态、鉴定判定与证据；不显示被哪个请求用掉。', nav: null, requires: 'contributor', element: ConsolePage },
  { path: '/contributions/stats', title: '贡献统计', summary: '被调用统计（无「收益」区域——反面清单）。', nav: 'contributions', requires: 'contributor', element: ConsolePage },
  { path: '/contributions/recovery-code', title: '恢复码', summary: '仅纯 Key 型账户，展示一次。', nav: null, requires: 'contributor', dangerous: true, element: ConsolePage },

  // ── 错误页（4.1 末行）───────────────────────────────────────
  { path: '/error/:code', title: '错误页', summary: '403 / 404 / 410 / 500 的统一文案页。', nav: null, requires: 'none', element: NotFoundPage }
]

/** 按一级导航分组取页面（渲染层按 classes 过滤在接入鉴权后进行）。 */
export function pagesOfNav(group: NavGroup): PageDef[] {
  return PAGES.filter((p) => p.nav === group)
}

/** 用路径查表（面包屑与导航高亮用）。 */
export function findPage(path: string): PageDef | undefined {
  return PAGES.find((p) => p.path === path)
}
