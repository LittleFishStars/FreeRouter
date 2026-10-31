// 账户自助 · 我的贡献（2026-10-31 用户要求）：管理自己贡献的凭据。
//
// 为什么放在账户下而不是只在贡献者导航：contributor 类只在「未登录自动建号」
// 的贡献路径上授予——已登录的 admin/tenant 账户贡献后并不获得该类，贡献者
// 导航对他们不可见；而「我贡献过什么、怎么停掉、失效了怎么重新登录」是
// 任何账户都该能自助回答的问题。本页对全部已登录账户开放（requires: auth）。
//
// 与贡献者导航（/contributions/*）的关系：那套页面是 contributor 类账户的
// 完整面（鉴定证据、统计、恢复码）；本页是账户自助的凭据管理面：
// 停止/恢复贡献、失效凭据重新登录、供应商状态（口径同凭据池页）、添加贡献。
//
// 三个状态平面分开显示，不合并成一个词（合并必然说谎）：
//   - status/verdict：鉴定链路的结论（凭据本身能不能用）；
//   - paused/upstream_found：属主自己的上游行开关（停止贡献）——暂停不改
//     status，只有分开带才能同时回答「凭据能不能用」与「你有没有撤下它」；
//   - 余额/签到：上游侧格式化好的展示串（与凭据池页同源，空 = 从未查询）。
//
// 隐私边界与贡献者页一致：只看自己（owner 过滤在后端）、不看明文、
// 不看路由归因（不给「被哪个请求用掉」的细节）。

import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  listMyCredentials,
  pauseMyCredential,
  resumeMyCredential
} from '../../api/endpoints'
import type { ContributionCredential } from '../../api/types'
import { useSession } from '../../api/session'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON, TableAction, TableActions } from '../../components/IconButtons'
import { IconLogin, IconPower, IconView } from '../../icons'
import {
  AsyncView,
  Badge,
  Banner,
  DataTable,
  EmptyState,
  FormError,
  Section,
  StatusBadge
} from '../../components/ui'
import { fmtNum, fmtTime, statusLabel } from '../../utils/format'
import { FREE_NOTICE } from '../../utils/contribute'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

/** 贡献形态的用户可见称呼（kind 由后端按 provider 列推导）。 */
function kindLabel(kind: string | undefined): string {
  if (kind === 'oauth') return '反代供应商'
  return kind === 'api_key' ? 'API 供应商' : '—'
}

/** 自定义贡献没有名字可查，后端给固定文案；其余空值回退 id。 */
function providerDisplayName(c: ContributionCredential): string {
  return c.provider_name || c.provider || '—'
}

/** 供应商行的聚合视图（「供应商状态」区，口径参考凭据池页）。 */
interface ProviderGroup {
  provider: string
  name: string
  kind: string
  total: number
  inPool: number
  paused: number
  invalid: number
  lastCalledAt: string | null
  rows: ContributionCredential[]
}

function groupByProvider(rows: ContributionCredential[]): ProviderGroup[] {
  const map = new Map<string, ProviderGroup>()
  for (const c of rows) {
    const key = c.provider || c.id
    let g = map.get(key)
    if (!g) {
      g = {
        provider: key,
        name: providerDisplayName(c),
        kind: c.kind,
        total: 0,
        inPool: 0,
        paused: 0,
        invalid: 0,
        lastCalledAt: null,
        rows: []
      }
      map.set(key, g)
    }
    g.total++
    if (c.in_pool) g.inPool++
    if (c.status === 'active' && c.paused) g.paused++
    if (c.status === 'invalid' || c.status === 'expired') g.invalid++
    if (c.last_called_at && (!g.lastCalledAt || c.last_called_at > g.lastCalledAt)) {
      g.lastCalledAt = c.last_called_at
    }
    g.rows.push(c)
  }
  return [...map.values()]
}

/**
 * 状态列的三段判定：active 行按「停止贡献」开关显示，非 active 行显示
 * 生命周期状态。判定顺序是契约：found=false 的 active 行是「系统里没有
 * 可调用的载体」（历史行），不能冒充「贡献中」。
 */
function contributionState(c: ContributionCredential) {
  if (c.status !== 'active') {
    return <StatusBadge status={c.status} label={statusLabel(c.status)} />
  }
  if (!c.upstream_found) {
    return <Badge tone="muted">未挂载</Badge>
  }
  if (c.paused) {
    return <Badge tone="warn">已停止贡献</Badge>
  }
  return <Badge tone="ok">贡献中</Badge>
}

/** 失效/过期才谈得上「重新登录」；禁用是管理员处置，重新登录救不回来。 */
function isCredentialBroken(c: ContributionCredential): boolean {
  return c.status === 'invalid' || c.status === 'expired'
}

export default function MyContributionsPage({ def }: PageProps) {
  const { session } = useSession()
  const q = useApi(() => listMyCredentials({ limit: 100 }), [])
  const rows = q.data?.data ?? []
  const groups = useMemo(() => groupByProvider(rows), [rows])
  const [busyId, setBusyId] = useState<string | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  /** 是否能看贡献者完整面（详情/证据）。contributor 类只在自动建号路径授予。 */
  const canSeeDetail = session?.classes.includes('contributor') ?? false

  async function toggle(c: ContributionCredential) {
    setBusyId(c.id)
    setError(null)
    setNotice(null)
    try {
      // 与贡献者页同一条纪律：写端点返回的 status 才是生效后的状态，
      // 不用本地的 c.paused 反推——列表可能是几秒前的快照。
      const wantPause = !c.paused
      const r = wantPause ? await pauseMyCredential(c.id) : await resumeMyCredential(c.id)
      setNotice(
        r.data.status === 'paused'
          ? wantPause
            ? '已停止这条贡献（可随时恢复）'
            : '该凭据当前不可恢复：未通过鉴定的凭据不会进入候选池'
          : '已恢复这条贡献'
      )
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusyId(null)
    }
  }

  return (
    <PageFrame
      def={def}
      actions={<Link className="btn-primary" to="/contribute">添加贡献</Link>}
    >
      <Banner tone="muted" title="贡献即生效，无需审核">
        {FREE_NOTICE}
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <AsyncView
        state={q}
        loadingRows={5}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="还没有任何贡献"
              hint="你可以贡献供应商账号（OAuth 授权），或一个 OpenAI 兼容端点的 API Key；贡献即生效，无需审核。"
              action={<Link className="btn-primary" to="/contribute">去贡献入口</Link>}
            />
          ) : null
        }
      >
        {(d) => (
          <>
            <Section
              title={`供应商状态（${groups.length}）`}
              hint="口径与凭据池页一致：鉴定状态是系统结论，「已停止」是你自己的开关；余额/签到是上游侧最近一次查询的结果。"
            >
              <DataTable
                headers={['供应商', '形态', '凭据', '在池中', '已停止', '失效 / 过期', '最近被调用']}
              >
                {groups.map((g) => (
                  <tr key={g.provider}>
                    <td><strong>{g.name}</strong></td>
                    <td>{kindLabel(g.kind)}</td>
                    <td>{fmtNum(g.total)}</td>
                    <td>{fmtNum(g.inPool)}</td>
                    <td>{g.paused > 0 ? <Badge tone="warn">{fmtNum(g.paused)}</Badge> : '0'}</td>
                    <td>{g.invalid > 0 ? <Badge tone="err">{fmtNum(g.invalid)}</Badge> : '0'}</td>
                    <td>{g.lastCalledAt ? fmtTime(g.lastCalledAt) : <span className="muted">未被调用</span>}</td>
                  </tr>
                ))}
              </DataTable>
              <p className="muted">
                「在池中」= 鉴定通过且你没有停止贡献的凭据数。停止是可逆的开关（凭据与密文都保留），
                与撤销（销毁密文，属管理面动作）不同。
              </p>
            </Section>

            <Section title={`我的凭据（${d.total ?? d.data.length}）`}>
              <DataTable
                headers={['供应商', '标签', '形态', '状态', '鉴定', '供应商侧', '贡献时间', '操作']}
              >
                {d.data.map((c) => (
                  <tr key={c.id}>
                    <td>{providerDisplayName(c)}</td>
                    <td className="cell-wrap">{c.external_label || '贡献凭据'}</td>
                    <td>{kindLabel(c.kind)}</td>
                    <td>{contributionState(c)}</td>
                    <td>
                      {c.stage && !c.verdict ? (
                        <Badge tone="info">鉴定中（{c.stage}）</Badge>
                      ) : (
                        <StatusBadge status={c.verdict} label={statusLabel(c.verdict)} />
                      )}
                    </td>
                    <td className="cell-wrap">
                      {/* 与凭据池页同源的展示串：空 = 从未查询/未签到，不冒充值。 */}
                      {c.balance_text && (
                        <span className="am-chip-tag" data-tone="ok" title={`查询于 ${fmtTime(c.balance_checked_at)}`}>
                          {c.balance_text}
                        </span>
                      )}
                      {c.checkin_text && (
                        <span
                          className="am-chip-tag"
                          data-tone={c.checkin_date ? 'ok' : 'warn'}
                          title={c.checkin_date ? `签到于 ${c.checkin_date}` : '最近一次尝试没有签上（失败会按退避自动重试）'}
                        >
                          {c.checkin_text}
                        </span>
                      )}
                      {!c.balance_text && !c.checkin_text && <span className="muted">—</span>}
                    </td>
                    <td>{fmtTime(c.created_at)}</td>
                    <td className="cell-actions">
                      <TableActions>
                        {/* 停止/恢复只对 active 行有意义：非 active 行的停用是
                            判定结果或管理员处置，不是你的开关。 */}
                        {c.status === 'active' && c.upstream_found && (
                          <TableAction
                            icon={<IconPower size={ACTION_ICON} />}
                            label={c.paused ? '恢复贡献' : '停止贡献'}
                            busy={busyId === c.id}
                            onClick={() => void toggle(c)}
                          />
                        )}
                        {c.kind === 'oauth' && isCredentialBroken(c) && (
                          <TableAction
                            icon={<IconLogin size={ACTION_ICON} />}
                            label="重新登录"
                            to={`/contribute/oauth/${encodeURIComponent(c.provider || '')}`}
                          />
                        )}
                        {c.kind === 'api_key' && isCredentialBroken(c) && (
                          <TableAction
                            icon={<IconLogin size={ACTION_ICON} />}
                            label="换 Key"
                            to="/contribute"
                          />
                        )}
                        {canSeeDetail && (
                          <TableAction
                            icon={<IconView size={ACTION_ICON} />}
                            label="凭据详情"
                            to={`/contributions/credentials/${encodeURIComponent(c.id)}`}
                          />
                        )}
                      </TableActions>
                    </td>
                  </tr>
                ))}
              </DataTable>
            </Section>

            <Section title="停止与恢复">
              <ul className="plain-list">
                <li>
                  <strong>停止贡献</strong>：立即把你贡献的凭据撤出候选池，不再被任何请求使用；
                  凭据与密文都保留，随时可以恢复。发现异常用量时不需要等管理员在线。
                </li>
                <li>
                  <strong>恢复贡献</strong>：只有鉴定通过的凭据能恢复；未通过鉴定的凭据撤下是
                  判定结果，不是你停的，恢复按钮对它不出现。
                </li>
                <li>
                  <strong>重新登录</strong>：OAuth 型凭据失效（供应商侧过期/吊销）时，走一次
                  供应商授权即可换上新凭据；新的授权会替换旧凭据的密文并重新鉴定。
                </li>
                <li>
                  <strong>换 Key</strong>：API Key 型凭据失效时没有「登录」可谈——重新提交一把
                  新 Key 即可（<Link to="/contribute">贡献入口</Link>）。
                </li>
              </ul>
            </Section>
          </>
        )}
      </AsyncView>
    </PageFrame>
  )
}
