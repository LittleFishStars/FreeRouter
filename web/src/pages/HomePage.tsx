// 首页 `/`（docs/ui/02-public.md 第 2 节）。
//
// 未登录：落地页 + 三个动作（登录 / 注册 / 我有凭据要贡献），不做营销长页。
// 已登录：不重定向，按权限类渲染「我的概览」——tenant 给 Key 与用量摘要，
// admin 给审核队列待办计数，contributor 给凭据状态摘要。

import { Link } from 'react-router-dom'
import { Box } from '@mui/material'
import LockOutlinedIcon from '@mui/icons-material/LockOutlined'
import { useSession } from '../api/session'
import { getInboxCount, getUsageOverview, listAccounts, listMyCredentials } from '../api/endpoints'
import { useApi } from '../hooks/useApi'
import { AsyncView, Badge, Banner, Section } from '../components/ui'
import PageFrame from './PageFrame'
import type { PageProps } from '../routes'
import { classLabel, fmtNum } from '../utils/format'

/** 已登录概览里的单个数字卡。 */
function Metric({ label, value, to }: { label: string; value: string; to?: string }) {
  const body = (
    <div className="metric">
      <span className="metric-value">{value}</span>
      <span className="metric-label">{label}</span>
    </div>
  )
  return to ? <Link to={to}>{body}</Link> : body
}

function AdminSummary() {
  const q = useApi(() => getInboxCount(), [])
  return (
    <AsyncView state={q} loadingRows={1}>
      {(d) => (
        <Section title="管理工作台">
          <div className="metric-row">
            <Metric label="待处理事项（未读）" value={fmtNum(d.unread)} to="/admin/inbox" />
            <Metric label="注册审核队列" value="前往处理" to="/admin" />
          </div>
        </Section>
      )}
    </AsyncView>
  )
}

function TenantSummary() {
  const q = useApi(() => getUsageOverview(), [])
  return (
    <AsyncView state={q} loadingRows={2}>
      {(d) => (
        <Section title="我的使用">
          <div className="metric-row">
            <Metric label="Key 数量" value={fmtNum(d.key_count)} to="/usage/keys" />
            {d.dimensions.map((dim) => (
              <Metric
                key={dim.unit}
                label={`${dim.unit === 'request' ? '请求' : dim.unit === 'token' ? 'Token' : 'Credits'}（${dim.period === 'day' ? '日' : '月'}）`}
                value={`${fmtNum(dim.used)} / ${fmtNum(dim.limit)}`}
                to="/usage"
              />
            ))}
          </div>
        </Section>
      )}
    </AsyncView>
  )
}

function ContributorSummary() {
  const q = useApi(() => listMyCredentials({ limit: 5 }), [])
  return (
    <AsyncView state={q} loadingRows={2} empty={(d) => (d.data.length === 0 ? <Section title="我的贡献"><p>你还没有贡献过凭据。<Link to="/contribute">去贡献一个</Link></p></Section> : null)}>
      {(d) => (
        <Section title="我的贡献">
          <div className="metric-row">
            <Metric label="凭据数量" value={fmtNum(d.data.length)} to="/contributions/credentials" />
            <Metric label="贡献统计" value="查看" to="/contributions/stats" />
          </div>
        </Section>
      )}
    </AsyncView>
  )
}

/** 管理员首页顺带看一眼待审核人数（可失败，不阻塞页面）。 */
function PendingHint() {
  const q = useApi(() => listAccounts({ status: 'pending', limit: 1 }), [])
  if (!q.data) return null
  if ((q.data.total ?? q.data.data.length) === 0) return null
  return (
    <Banner tone="warn" title="有注册申请等待处理">
      有 {q.data.total ?? q.data.data.length} 个账户处于待审核状态。
      <Link to="/admin"> 去审核队列</Link>
    </Banner>
  )
}

export default function HomePage({ def }: PageProps) {
  const { session } = useSession()
  const classes = session?.classes ?? []
  const isAdmin = classes.includes('admin')
  const isTenant = classes.includes('tenant')
  const isContributor = classes.includes('contributor')

  if (!session) {
    return (
      <PageFrame def={def}>
        <div className="landing">
          <p className="landing-lead">
            FreeRouter 是一个统一的模型出口：你只需要记住抽象模型名（例如 <code>smart</code>），
            真实的渠道、候选与凭据全部留在管理员一侧。
          </p>
          <div className="landing-actions">
            <Link className="btn-primary" to="/login">
              登录
            </Link>
            <Link className="btn-ghost" to="/register">
              注册
            </Link>
            <Link className="btn-ghost" to="/contribute">
              我有凭据要贡献
            </Link>
          </div>
          <p className="muted">注册后需管理员审核；贡献凭据不需要审核，立即生效。</p>
        </div>
        <Section title="站点说明">
          <ul className="plain-list">
            <li>对外只暴露抽象模型：同一个名字背后可能有多条真实模型与凭据，由路由决定。</li>
            <li>调用用 API Key，管理用登录会话，两者互不复用。</li>
            <li>贡献者无偿贡献凭据；可以随时撤销，平台不提供额度或优先权作为回报。</li>
          </ul>
        </Section>
      </PageFrame>
    )
  }

  return (
    <PageFrame
      def={def}
      actions={
        <span className="muted">
          当前身份：
          {classes.length === 0 ? '无权限类' : classes.map((c) => `${classLabel(c)} `).join(' / ')}
        </span>
      }
    >
      <Banner tone="muted" title={`${session.display_name}，欢迎回来`}>
        {classes.length === 0 ? (
          <>你的账户还没有获得任何权限类：可以先去 <Link to="/contribute">贡献一个凭据</Link>，或等待管理员审核。</>
        ) : (
          <>这是你的概览；左侧导航只显示当前账户具备的入口。</>
        )}
      </Banner>
      {isAdmin && <PendingHint />}
      <div className="stack">
        {isAdmin && <AdminSummary />}
        {isTenant && <TenantSummary />}
        {isContributor && <ContributorSummary />}
      </div>
      {classes.length > 0 && (
        <Section title="常用入口">
          <div className="link-grid">
            {isTenant && (
              <>
                <Link to="/usage">用量与配额</Link>
                <Link to="/usage/keys">我的 API Key</Link>
                <Link to="/usage/models">可用抽象模型</Link>
              </>
            )}
            {isAdmin && (
              <>
                <Link to="/admin">审核队列</Link>
                <Link to="/admin/inbox">待处理事项</Link>
                <Link to="/admin/audit">审计日志</Link>
              </>
            )}
            {isContributor && (
              <>
                <Link to="/contributions">我的贡献</Link>
                <Link to="/contributions/credentials">我的凭据</Link>
              </>
            )}
            <Link to="/account">账户概览</Link>
          </div>
        </Section>
      )}
      <p className="muted">
        会话即将过期时，顶栏会提前提示；改密或管理员禁用会立即吊销全部会话。
      </p>
      <Badge tone="muted">
        <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.4 }}>
          提示：页面上的
          <LockOutlinedIcon sx={{ fontSize: 12 }} aria-hidden="true" />
          表示该路由存在危险写操作
        </Box>
      </Badge>
    </PageFrame>
  )
}
