// 贡献者端：总览 / 我的凭据 / 凭据详情 / 统计 / 恢复码
// （docs/ui/05-contributor.md）。
//
// 三条边界：只看自己（只列 owner = 当前账户）、不看明文、不看路由
// （看不到自己的凭据被哪个抽象模型选中或被谁的请求用走）。
//
// 统计页**没有「收益」区域**：无偿是定案，不是「尚未设计」。该区域不是
// 遗漏，而是不应存在。

import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  getContributionStats,
  getMyCredential,
  getRecoveryCodeState,
  listMyCredentials,
  pauseMyCredential,
  regenerateRecoveryCode,
  resumeMyCredential
} from '../../api/endpoints'
import type {
  ContributionCredential,
  ContributionCredentialDetail,
  RecoveryCodeState
} from '../../api/types'
import { useApi } from '../../hooks/useApi'
import ConfirmDialog from '../../components/ConfirmDialog'
import OneTimeSecret from '../../components/OneTimeSecret'
import {
  AsyncView,
  Badge,
  Banner,
  DataTable,
  EmptyState,
  FormError,
  JsonDetails,
  KeyValues,
  Section,
  StatusBadge
} from '../../components/ui'
import { downloadText, fmtNum, fmtTime, statusLabel } from '../../utils/format'
import { FREE_NOTICE } from '../../utils/contribute'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

// ── 总览 ───────────────────────────────────────────────────────────────

export function ContributionsOverviewPage({ def }: PageProps) {
  const q = useApi(() => listMyCredentials({ limit: 100 }), [])
  const rows = q.data?.data ?? []
  const inPool = rows.filter((c) => c.in_pool).length
  const last = rows
    .map((c) => c.created_at)
    .filter((v): v is string => Boolean(v))
    .sort()
    .at(-1)

  return (
    <PageFrame
      def={def}
      actions={<Link className="btn-primary" to="/contribute">再贡献一个</Link>}
    >
      <Banner tone="muted" title="贡献即生效，无需审核">
        {FREE_NOTICE}
      </Banner>

      <Section title="我的贡献概览">
        <AsyncView state={q} loadingRows={3}>
          {() => (
            <>
              <div className="metric-row">
                <div className="metric"><span className="metric-value">{fmtNum(rows.length)}</span><span className="metric-label">凭据数量</span></div>
                <div className="metric"><span className="metric-value">{fmtNum(inPool)}</span><span className="metric-label">在候选池中</span></div>
                <div className="metric"><span className="metric-value">{fmtTime(last)}</span><span className="metric-label">上次贡献</span></div>
              </div>
              <p className="muted">
                贡献者能力只包含「贡献与查看自己的贡献」，不包含模型调用。如需调用模型，请另行申请。
              </p>
            </>
          )}
        </AsyncView>
      </Section>

      <Section title="两种贡献方式">
        <div className="cards-2">
          <div className="card">
            <h3>贡献供应商账号</h3>
            <p className="muted">走供应商 OAuth 授权；完成后自动获得贡献者能力，可随时撤销。</p>
            <Link to="/contribute">去授权</Link>
          </div>
          <div className="card">
            <h3>贡献 API Key</h3>
            <p className="muted">填端点、Key、声称的模型与标签；进入 L0 + L1 鉴定。</p>
            <Link to="/contribute">去提交</Link>
          </div>
        </div>
      </Section>

      <Section title="快捷入口">
        <div className="link-grid">
          <Link to="/contributions/credentials">我的凭据</Link>
          <Link to="/contributions/stats">贡献统计</Link>
          <Link to="/contributions/recovery-code">恢复码</Link>
          <Link to="/contribute/recovery">用恢复码兑换</Link>
        </div>
      </Section>
    </PageFrame>
  )
}

// ── 凭据列表 ───────────────────────────────────────────────────────────

export function MyCredentialsPage({ def }: PageProps) {
  const q = useApi(() => listMyCredentials({ limit: 100 }), [])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  async function toggle(c: ContributionCredential) {
    setBusy(true)
    setError(null)
    try {
      // 暂停/恢复是两个独立端点（契约 v2 §5）。提示词用返回的 status 决定，
      // 不用本地的 c.status 反推——列表可能是几秒前的快照。
      // 「请求恢复、返回仍是 paused」不是失败：那是后端判该凭据不可恢复
      // （未通过鉴定的 enabled=false 是判定结果，不是暂停），界面必须说清，
      // 否则用户会以为按钮没生效。
      const wantPause = c.status === 'active'
      const r = wantPause ? await pauseMyCredential(c.id) : await resumeMyCredential(c.id)
      setNotice(
        r.data.status === 'paused'
          ? wantPause
            ? '已暂停这条凭据'
            : '该凭据当前不可恢复：未通过鉴定的凭据不会进入候选池'
          : '已恢复这条凭据'
      )
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame def={def} actions={<Link className="btn-primary" to="/contribute">再贡献一个</Link>}>
      <Banner tone="muted">
        只列出你自己贡献的凭据；明文永不显示。「最近被调用」只给有没有与时间，
        不给次数与对象——次数会反推份额，对象会暴露归因。
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <AsyncView
        state={q}
        loadingRows={5}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="还没有任何凭据"
              hint="贡献即生效，无需审核。你可以贡献供应商账号或一个 OpenAI 兼容端点的 API Key。"
              action={<Link className="btn-primary" to="/contribute">开始贡献</Link>}
            />
          ) : null
        }
      >
        {(d) => (
          <Section title={`我的凭据（${d.total ?? d.data.length}）`}>
            <DataTable headers={['标签', '类型', '声称的模型', '状态', '鉴定', '贡献时间', '最近被调用', '操作']}>
              {d.data.map((c) => (
                <tr key={c.id}>
                  <td><Link to={`/contributions/credentials/${encodeURIComponent(c.id)}`}>{c.external_label}</Link></td>
                  <td>{c.kind === 'oauth' ? '供应商 OAuth' : 'API Key'}</td>
                  <td><code>{c.claimed_model}</code></td>
                  <td>{c.stage && !c.verdict ? <Badge tone="info">鉴定中（{c.stage}）</Badge> : <StatusBadge status={c.status} label={statusLabel(c.status)} />}</td>
                  <td><StatusBadge status={c.verdict} label={statusLabel(c.verdict)} /></td>
                  <td>{fmtTime(c.created_at)}</td>
                  <td>{c.last_called_at ? fmtTime(c.last_called_at) : '未被调用'}</td>
                  <td className="cell-actions">
                    <Link to={`/contributions/credentials/${encodeURIComponent(c.id)}`}>详情</Link>
                    <button type="button" className="btn-ghost" onClick={() => void toggle(c)} disabled={busy || c.status === 'invalid'}>
                      {c.status === 'active' ? '暂停' : '恢复'}
                    </button>
                  </td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>

      <Section title="暂停与恢复">
        <p className="muted">
          暂停由你自己控制：贡献即生效，对称地，你也可以随时把自己贡献的凭据撤下。
          发现异常用量时不需要等管理员在线。
        </p>
        <p className="muted">恢复不会重置鉴定结论；被鉴定为未通过的凭据需要管理员复核。</p>
      </Section>
    </PageFrame>
  )
}

// ── 凭据详情 ───────────────────────────────────────────────────────────

export function ContributionCredentialDetailPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const q = useApi(() => getMyCredential(id), [id])
  const c: ContributionCredentialDetail | undefined = q.data
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  if (!c) {
    return (
      <PageFrame def={def}>
        <AsyncView state={q} loadingRows={4}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  async function toggle() {
    setBusy(true)
    setError(null)
    try {
      // 同上：写端点返回的 status 才是生效后的状态（幂等重复调用也成立）。
      const wantPause = c!.status === 'active'
      const r = wantPause ? await pauseMyCredential(c!.id) : await resumeMyCredential(c!.id)
      setNotice(
        r.data.status === 'paused'
          ? wantPause
            ? '已暂停'
            : '该凭据当前不可恢复：未通过鉴定的凭据不会进入候选池'
          : '已恢复'
      )
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame def={def} actions={<Link to="/contributions/credentials">返回列表</Link>}>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}
      {c.verdict === 'unverified' && (
        <Banner tone="info" title="未鉴定 = 放行">
          没有参考指纹的冷门模型默认放行，并进入复检；这不是错误，也不代表被拒绝。
        </Banner>
      )}
      {c.verdict === 'suspect' && (
        <Banner tone="warn" title="未通过 = 已停止使用">
          下面列出证据。你可以导出结果用于申诉，或更新凭据后重新提交。
        </Banner>
      )}

      <Section title="基本信息">
        <KeyValues
          items={[
            { label: '标签', value: c.external_label },
            { label: '类型', value: c.kind === 'oauth' ? '供应商 OAuth' : 'API Key' },
            { label: '声称的模型', value: <code>{c.claimed_model}</code> },
            { label: '贡献时间', value: fmtTime(c.created_at) },
            { label: '最近被调用', value: c.last_called_at ? fmtTime(c.last_called_at) : '未被调用' },
            { label: '状态', value: <StatusBadge status={c.status} label={statusLabel(c.status)} /> },
            { label: '状态原因', value: c.status_reason ?? '—' },
            { label: '是否在候选池', value: c.in_pool ? '是' : '否' },
            { label: '鉴定判定', value: <StatusBadge status={c.verdict} label={statusLabel(c.verdict)} /> }
          ]}
        />
        <p className="muted">
          这里不显示凭据被哪个抽象模型引用、被谁的请求用掉——那是池结构信息，归管理端。
        </p>
      </Section>

      <Section title="鉴定流水线" hint="L0 几秒、L1 约一分钟、L2 是抽检；合并成一个「鉴定中」会让人以为卡住了。">
        {(['L0', 'L1', 'L2'] as const).map((stage) => {
          const res = c.stage_results?.[stage]
          return (
            <div className="stage-row" key={stage}>
              <Badge tone={res ? 'ok' : 'muted'}>{stage}</Badge>
              {res ? <JsonDetails summary="查看阶段结果" value={res} /> : <span className="muted">尚未运行</span>}
            </div>
          )
        })}
      </Section>

      <Section title="证据">
        {!c.evidence || c.evidence.length === 0 ? (
          <p className="muted">没有证据条目。</p>
        ) : (
          <DataTable headers={['信号', '强度', '结果']}>
            {c.evidence.map((e, i) => (
              <tr key={`${e.signal}-${i}`}>
                <td>{e.signal}</td>
                <td><Badge tone={e.strength === 'strong' ? 'err' : e.strength === 'medium' ? 'warn' : 'muted'}>{e.strength}</Badge></td>
                <td>{e.result}</td>
              </tr>
            ))}
          </DataTable>
        )}
      </Section>

      {c.recheck_history && c.recheck_history.length > 0 && (
        <Section title="复检历史">
          <DataTable headers={['时间', '判定']}>
            {c.recheck_history.map((h, i) => (
              <tr key={`${h.at}-${i}`}>
                <td>{fmtTime(h.at)}</td>
                <td><StatusBadge status={h.verdict} label={statusLabel(h.verdict)} /></td>
              </tr>
            ))}
          </DataTable>
        </Section>
      )}

      <Section title="处置">
        <div className="inline-links">
          <button type="button" className="btn-ghost" onClick={() => void toggle()} disabled={busy}>
            {c.status === 'active' ? '暂停这条凭据' : '恢复这条凭据'}
          </button>
          <Link to="/contribute">更新凭据（重新授权 / 换 Key）</Link>
          <button
            type="button"
            className="btn-ghost"
            onClick={() =>
              downloadText(
                `credential-${c.external_label || c.id}.json`,
                JSON.stringify(
                  {
                    id: c.id,
                    label: c.external_label,
                    kind: c.kind,
                    claimed_model: c.claimed_model,
                    status: c.status,
                    verdict: c.verdict,
                    evidence: c.evidence ?? [],
                    stage_results: c.stage_results ?? {},
                    recheck_history: c.recheck_history ?? []
                  },
                  null,
                  2
                ),
                'application/json'
              )
            }
          >
            导出鉴定结果（申诉用）
          </button>
        </div>
        <p className="muted">导出内容不含凭据本身，也不含路由归因。</p>
      </Section>

    </PageFrame>
  )
}

// ── 贡献统计 ───────────────────────────────────────────────────────────

export function ContributionStatsPage({ def }: PageProps) {
  const [range, setRange] = useState<'day' | 'month'>('month')
  const q = useApi(() => getContributionStats(range), [range])

  return (
    <PageFrame def={def} actions={
      <label className="toolbar-field">
        时间范围
        <select value={range} onChange={(e) => setRange(e.target.value as 'day' | 'month')}>
          <option value="day">日</option>
          <option value="month">月</option>
        </select>
      </label>
    }>
      <Banner tone="warn" title="贡献是无偿的">
        {FREE_NOTICE}
      </Banner>

      <AsyncView
        state={q}
        loadingRows={4}
        empty={() => (
          <EmptyState
            title="还没有产生调用"
            hint="你的凭据还没有被路由到；这不代表它有问题，可能只是暂时没有匹配的请求。"
            action={<Link to="/contributions/credentials">查看我的凭据</Link>}
          />
        )}
      >
        {(s) => (
          <>
            <Section title="贡献量">
              <div className="metric-row">
                <div className="metric"><span className="metric-value">{fmtNum(s.credential_count)}</span><span className="metric-label">凭据数量</span></div>
                <div className="metric"><span className="metric-value">{fmtNum(s.in_pool_count)}</span><span className="metric-label">在池中</span></div>
                <div className="metric"><span className="metric-value">{fmtNum(s.claimed_model_count)}</span><span className="metric-label">覆盖的声称模型</span></div>
              </div>
            </Section>
            <Section title="被调用" hint="聚合总量，不按抽象模型拆分：拆分会让贡献者画出池结构的画像。">
              <div className="metric-row">
                <div className="metric"><span className="metric-value">{fmtNum(s.call_count)}</span><span className="metric-label">被调用次数</span></div>
                <div className="metric"><span className="metric-value">{fmtNum(s.input_tokens)}</span><span className="metric-label">输入 token</span></div>
                <div className="metric"><span className="metric-value">{fmtNum(s.output_tokens)}</span><span className="metric-label">输出 token</span></div>
              </div>
            </Section>
            <Section title="健康度">
              <div className="metric-row">
                <div className="metric"><span className="metric-value">{fmtNum(s.health.active)}</span><span className="metric-label">正常</span></div>
                <div className="metric"><span className="metric-value">{fmtNum(s.health.disabled)}</span><span className="metric-label">暂停 / 封禁</span></div>
                <div className="metric"><span className="metric-value">{fmtNum(s.health.suspect)}</span><span className="metric-label">鉴定未通过</span></div>
              </div>
              <p>
                <Link to="/contributions/credentials">查看凭据详情</Link>
              </p>
            </Section>
            <Section title="关于「收益」">
              <p>
                本页没有收益区域，也不会有：贡献不提供额度、优先使用权或升级凭据作为回报。
                以上统计是纯展示，不参与任何分配。
              </p>
              <p className="muted">
                无偿只取消「分配」，没有取消「可见」：你需要这些数字判断自己提供的东西有没有用、还值不值得继续提供。
              </p>
            </Section>
          </>
        )}
      </AsyncView>
    </PageFrame>
  )
}

// ── 恢复码 ─────────────────────────────────────────────────────────────

export function RecoveryCodePage({ def }: PageProps) {
  const q = useApi(() => getRecoveryCodeState(), [])
  const navigate = useNavigate()
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [fresh, setFresh] = useState<string | null>(null)

  if (fresh) {
    return (
      <OneTimeSecret
        title="新的恢复码"
        secret={fresh}
        description="旧恢复码已立即失效。新恢复码只显示这一次；请保存到安全的地方。"
        filename="freerouter-recovery-code.txt"
        onDone={() => {
          setFresh(null)
          q.reload()
          navigate('/contributions/recovery-code')
        }}
      />
    )
  }

  const state: RecoveryCodeState | undefined = q.data

  return (
    <PageFrame def={def} actions={<Link to="/contributions">返回我的贡献</Link>}>
      <Banner tone="muted">
        仅纯 API Key 型账户需要恢复码：这类账户没有密码，也没有可识别的上游账号，
        恢复码是唯一能自助回站的路径。OAuth 型账户用任意用过的账号登录即可，不需要恢复码。
      </Banner>
      <FormError error={error} />
      <AsyncView state={q} loadingRows={3}>
        {() => (
          <>
            <Section title="恢复码状态">
              <KeyValues
                items={[
                  { label: '状态', value: state?.generated ? '已生成' : '未生成' },
                  { label: '生成时间', value: fmtTime(state?.generated_at) },
                  { label: '最近使用', value: fmtTime(state?.last_used_at) }
                ]}
              />
              <button type="button" className="btn-danger" onClick={() => setConfirming(true)} disabled={busy}>
                {state?.generated ? '重新生成恢复码…' : '生成恢复码…'}
              </button>
              <p className="muted">
                重新生成会立即作废旧码；新码只显示一次。如果没保存好，账户就失去自助找回路径，
                只能由管理员介入。
              </p>
            </Section>
            <Section title="绑定 OAuth 身份">
              <p>
                把已贡献过的供应商账号绑定为登录身份，可以少记一个秘密。
                绑定后仍需保留恢复码作为兜底。
              </p>
              <p className="muted">绑定动作不在 MVP 接口内；请从供应商授权入口完成一次贡献。</p>
            </Section>
            <Section title="找回说明">
              <ul className="plain-list">
                <li>OAuth 型：用任意一个你贡献过的账号登录即可回来。</li>
                <li>纯 Key 型：用恢复码兑换（<Link to="/contribute/recovery">恢复码兑换页</Link>）。</li>
                <li>恢复码用与 API Key 相同的一次性展示形态：勾选「我已保存」后才能离开。</li>
              </ul>
            </Section>
          </>
        )}
      </AsyncView>

      <ConfirmDialog
        open={confirming}
        title={state?.generated ? '确认重新生成恢复码' : '确认生成恢复码'}
        impact={[
          state?.generated ? '旧恢复码立即失效' : '将生成一个新的恢复码',
          '新恢复码只显示一次，离开后无法再次查看',
          '如果未保存且旧码已作废，账户将失去自助找回路径'
        ]}
        irreversibility="可恢复性损失：旧码作废不可撤销；未保存新码只能联系管理员。"
        targetName={state?.generated ? 'regenerate' : 'generate'}
        inputLabel="请输入 regenerate 或 generate 以确认"
        confirmLabel="确认生成"
        busy={busy}
        error={error}
        onCancel={() => setConfirming(false)}
        onConfirm={async () => {
          setBusy(true)
          setError(null)
          try {
            const r = await regenerateRecoveryCode()
            setFresh(r.data.code)
            setConfirming(false)
          } catch (e) {
            setError(e)
          } finally {
            setBusy(false)
          }
        }}
      />
    </PageFrame>
  )
}
