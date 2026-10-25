// 鉴定队列与结果（docs/ui/03-admin.md 第 9 节）。
//
// 两个必须区分开的判定：
//   - unverified（未鉴定）是**放行**：库里没有参考指纹的冷门模型，进复检队列；
//   - suspect（未通过）是**拒绝并给证据**。
// 如果界面把两者渲染成同一种红色异常，管理员会倾向去处理 unverified，
// 等于把放行策略又收紧回去。这里用不同色 + 不同文案做第二重信号。
//
// 人工改判必须填理由：它是覆盖自动结论的动作，理由进审计。

import { useState } from 'react'
import { IconBack } from '../../icons'
import { Link, useParams } from 'react-router-dom'
import {
  getVerification,
  listVerifications,
  notifyCredential,
  overrideVerification,
  setVerificationRecheck,
  triggerVerificationL2
} from '../../api/endpoints'
import type { AdminVerification, VerificationEvidence } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON, TableAction, TableActions, ToolButton } from '../../components/IconButtons'
import { IconRefresh, IconView } from '../../icons'
import ConfirmDialog from '../../components/ConfirmDialog'
import {
  AsyncView,
  Badge,
  Banner,
  DataTable,
  EmptyState,
  Field,
  FormError,
  JsonDetails,
  KeyValues,
  Section,
  StatusBadge
} from '../../components/ui'
import { fmtTime, statusLabel } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

const VERDICTS = [
  ['verified', '已鉴定'],
  ['trusted', '官方来源'],
  ['unverified', '未鉴定（放行）'],
  ['suspect', '未通过（停止使用）']
] as const

export function VerificationsPage({ def }: PageProps) {
  const [verdict, setVerdict] = useState('')
  const [provider, setProvider] = useState('')
  const q = useApi(
    () => listVerifications({ verdict: verdict || undefined, provider: provider || undefined, limit: 200 }),
    [verdict, provider]
  )

  return (
    <PageFrame def={def}>
      <Banner tone="muted">
        unverified 是默认放行（没有参考指纹），不是异常；suspect 才是拒绝并给证据。
        「人工改判」必须填写理由并进审计。
      </Banner>
      <div className="toolbar">
        <label className="toolbar-field">
          判定
          <select value={verdict} onChange={(e) => setVerdict(e.target.value)}>
            <option value="">全部</option>
            {VERDICTS.map(([v, l]) => (
              <option key={v} value={v}>{l}</option>
            ))}
          </select>
        </label>
        <label className="toolbar-field">
          供应商
          <input value={provider} onChange={(e) => setProvider(e.target.value)} placeholder="provider id" />
        </label>
        <ToolButton icon={<IconRefresh size={ACTION_ICON} />} onClick={q.reload}>刷新</ToolButton>
      </div>

      <AsyncView
        state={q}
        loadingRows={6}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState title="鉴定队列为空" hint="新贡献先跑 L0 形式校验，再跑 L1 标准鉴定；L2 是抽检。" />
          ) : null
        }
      >
        {(d) => (
          <Section title={`鉴定条目（${d.total ?? d.data.length}）`}>
            <DataTable headers={['凭据', '供应商', '声称的模型', '来源', '阶段', '判定', '复检', '']}>
              {d.data.map((v) => (
                <tr key={v.id}>
                  <td><code>{v.credential_id}</code></td>
                  <td>{v.provider}</td>
                  <td>{v.claimed_model ?? '—'}</td>
                  <td>{v.source ?? '—'}</td>
                  <td>{v.stage ?? '—'}</td>
                  <td>
                    <StatusBadge status={v.verdict} label={statusLabel(v.verdict)} />
                  </td>
                  <td>{v.recheck?.enabled ? `复检中（${fmtTime(v.recheck.last_at)}）` : '否'}</td>
                  <td className="cell-actions">
                    <TableActions>
                      <TableAction
                        icon={<IconView size={ACTION_ICON} />}
                        label="鉴定结果"
                        to={`/admin/verifications/${encodeURIComponent(v.id)}`}
                      />
                    </TableActions>
                  </td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>

      <Section title="判定口径">
        <ul className="plain-list">
          <li>L0 形式校验（几秒）→ L1 标准鉴定（约一分钟）→ L2 深度鉴定（抽检）。</li>
          <li>判定四态：verified / trusted / unverified / suspect；unverified 放行，suspect 拒绝。</li>
          <li>复检得分下滑会触发管理端告警（不进租户端）。</li>
        </ul>
      </Section>
    </PageFrame>
  )
}

export function VerificationDetailPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const q = useApi(() => getVerification(id), [id])
  const v: AdminVerification | undefined = q.data
  const [overrideOpen, setOverrideOpen] = useState(false)
  const [verdict, setVerdict] = useState('verified')
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  if (!v) {
    return (
      <PageFrame def={def}>
        <AsyncView state={q} loadingRows={4}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  const evidence: VerificationEvidence[] = v.evidence ?? []

  async function act(fn: () => Promise<unknown>, message: string) {
    setBusy(true)
    setError(null)
    try {
      await fn()
      setNotice(message)
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame def={def} actions={<Link to="/admin/verifications" className="icon-link"><IconBack size={ACTION_ICON} />返回队列</Link>}>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}
      {v.verdict === 'unverified' && (
        <Banner tone="info" title="未鉴定 = 放行">
          这条不是异常：库里没有该模型的参考指纹，因此默认放行并进入复检队列。
        </Banner>
      )}
      {v.verdict === 'suspect' && (
        <Banner tone="warn" title="未通过 = 已停止使用">
          凭据已停止参与路由。下面列出证据，供管理员复核与给贡献者解释。
        </Banner>
      )}

      <Section title="基本信息">
        <KeyValues
          items={[
            { label: '条目 ID', value: <code>{v.id}</code> },
            { label: '凭据', value: <code>{v.credential_id}</code> },
            { label: '供应商', value: v.provider },
            { label: '声称的模型', value: v.claimed_model ?? '—' },
            { label: '来源', value: v.source ?? '—' },
            { label: '当前阶段', value: v.stage ?? '—' },
            { label: '判定', value: <StatusBadge status={v.verdict} label={statusLabel(v.verdict)} /> },
            { label: '复检', value: v.recheck?.enabled ? `已加入（上次 ${fmtTime(v.recheck.last_at)}）` : '未加入' }
          ]}
        />
      </Section>

      <Section title="流水线进度" hint="三级耗时差异很大：合并成一个「鉴定中」会让贡献者以为卡住了。">
        {(['L0', 'L1', 'L2'] as const).map((stage) => {
          const res = v.stage_results?.[stage]
          return (
            <div className="stage-row" key={stage}>
              <Badge tone={res ? 'ok' : 'muted'}>{stage}</Badge>
              {res ? <JsonDetails summary="查看阶段结果" value={res} /> : <span className="muted">尚未运行</span>}
            </div>
          )
        })}
      </Section>

      <Section title="证据" hint="各信号的分级（strong / medium / weak）与命中结果；具体探测方法的披露粒度仍是待定项。">
        {evidence.length === 0 ? (
          <p className="muted">没有证据条目（鉴定可能尚未跑完，或该判定不需要证据）。</p>
        ) : (
          <DataTable headers={['信号', '强度', '结果']}>
            {evidence.map((e, i) => (
              <tr key={`${e.signal}-${i}`}>
                <td>{e.signal}</td>
                <td><Badge tone={e.strength === 'strong' ? 'err' : e.strength === 'medium' ? 'warn' : 'muted'}>{e.strength}</Badge></td>
                <td>{e.result}</td>
              </tr>
            ))}
          </DataTable>
        )}
      </Section>

      {v.override && (
        <Section title="人工改判记录">
          <JsonDetails summary="查看改判详情" value={v.override} />
        </Section>
      )}

      <Section title="处置">
        <div className="inline-links">
          <button type="button" className="btn-ghost" onClick={() => void act(() => triggerVerificationL2(v.id), '已触发 L2 深度鉴定')} disabled={busy}>
            手动触发 L2
          </button>
          <button type="button" className="btn-ghost" onClick={() => setOverrideOpen(true)} disabled={busy}>
            复核并改判…
          </button>
          <button
            type="button"
            className="btn-ghost"
            onClick={() => void act(() => setVerificationRecheck(v.id, !v.recheck?.enabled), v.recheck?.enabled ? '已移出复检队列' : '已加入复检队列')}
            disabled={busy}
          >
            {v.recheck?.enabled ? '移出复检队列' : '加入复检队列'}
          </button>
          <button
            type="button"
            className="btn-ghost"
            onClick={() => void act(() => notifyCredential(v.credential_id, `鉴定判定：${statusLabel(v.verdict)}`), '已通知贡献者')}
            disabled={busy}
          >
            通知贡献者
          </button>
          <Link to={`/admin/credentials/${encodeURIComponent(v.credential_id)}`}>查看凭据</Link>
        </div>
        <p className="muted">改判会写回凭据的鉴定状态，并把新旧状态记进审计；理由必填。</p>
      </Section>

      <ConfirmDialog
        open={overrideOpen}
        title={`确认改判 ${v.credential_id} 的鉴定结论`}
        impact={[
          `当前判定：${statusLabel(v.verdict)} → ${statusLabel(verdict)}`,
          verdict === 'suspect' ? '改为未通过会停止该凭据参与路由' : '改为放行/通过的判定会让凭据重新参与路由',
          '改判结论会回显给贡献者，并写入审计'
        ]}
        irreversibility="可再次改判，但每次改判都会留下独立的审计记录；期间路由结果已改变。"
        targetName={v.credential_id}
        inputLabel="请输入凭据 ID 以确认"
        confirmLabel="确认改判"
        busy={busy}
        error={error}
        onCancel={() => setOverrideOpen(false)}
        onConfirm={async () => {
          if (!reason.trim()) {
            setError(new Error('请填写改判理由（必填，进审计）'))
            return
          }
          await act(() => overrideVerification(v.id, verdict, reason.trim()), '已改判并记录审计')
          setOverrideOpen(false)
          setReason('')
        }}
      />

      {overrideOpen && (
        <Section title="改判表单" hint="理由为必填项；请在确认前填写。">
          <div className="field-row">
            <Field label="新判定" required>
              <select value={verdict} onChange={(e) => setVerdict(e.target.value)}>
                {VERDICTS.map(([val, l]) => (
                  <option key={val} value={val}>{l}</option>
                ))}
              </select>
            </Field>
            <Field label="改判理由" required>
              <input value={reason} onChange={(e) => setReason(e.target.value)} />
            </Field>
          </div>
        </Section>
      )}
    </PageFrame>
  )
}
