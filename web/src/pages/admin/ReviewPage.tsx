// 管理入口默认落点：注册审核队列 `/admin`（docs/ui/03-admin.md 第 3 节）。
//
// 本页只处理注册这一件事；全部待办汇总在 /admin/inbox。
// 「批准」必须同时选组 + 账户级额度：组决定模型可见性，额度决定消耗上限，
// 缺任一项都会让新账户落在「有账户但什么都不可见」的状态。
// 「拒绝」是面向人的负面动作且不可恢复 → 走统一二次确认框。

import { useState } from 'react'
import { Link } from 'react-router-dom'
import {
  approvePendingAccount,
  listGroups,
  listPendingAccounts,
  rejectPendingAccount
} from '../../api/endpoints'
import type { PendingAccount } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import ConfirmDialog from '../../components/ConfirmDialog'
import {
  AsyncView,
  Banner,
  DataTable,
  EmptyState,
  Field,
  FormError,
  Section
} from '../../components/ui'
import { fmtTime } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

type QuotaUnit = 'request' | 'token' | 'credit'

export default function ReviewPage({ def }: PageProps) {
  const pending = useApi(() => listPendingAccounts({ limit: 50 }), [])
  const groups = useApi(() => listGroups({ limit: 200 }), [])

  const [approving, setApproving] = useState<PendingAccount | null>(null)
  const [groupId, setGroupId] = useState('')
  const [quotaUnit, setQuotaUnit] = useState<QuotaUnit>('token')
  const [quotaLimit, setQuotaLimit] = useState('200000')
  const [quotaPeriod, setQuotaPeriod] = useState<'day' | 'month'>('day')
  const [approveError, setApproveError] = useState<unknown>(null)
  const [approvingBusy, setApprovingBusy] = useState(false)

  const [rejecting, setRejecting] = useState<PendingAccount | null>(null)
  const [rejectReason, setRejectReason] = useState('')
  const [rejectError, setRejectError] = useState<unknown>(null)
  const [rejectingBusy, setRejectingBusy] = useState(false)

  function startApprove(a: PendingAccount) {
    setApproving(a)
    setApproveError(null)
    setQuotaLimit('200000')
    setQuotaUnit('token')
    setQuotaPeriod('day')
    const first = groups.data?.data[0]
    setGroupId(first?.id ?? '')
  }

  async function submitApprove() {
    if (!approving) return
    setApprovingBusy(true)
    setApproveError(null)
    try {
      await approvePendingAccount(approving.id, {
        groups: [groupId],
        quota: { unit: quotaUnit, limit: Number(quotaLimit), period: quotaPeriod }
      })
      setApproving(null)
      pending.reload()
    } catch (e) {
      setApproveError(e)
    } finally {
      setApprovingBusy(false)
    }
  }

  async function submitReject() {
    if (!rejecting) return
    setRejectingBusy(true)
    setRejectError(null)
    try {
      await rejectPendingAccount(rejecting.id, rejectReason)
      setRejecting(null)
      setRejectReason('')
      pending.reload()
    } catch (e) {
      setRejectError(e)
    } finally {
      setRejectingBusy(false)
    }
  }

  const groupList = groups.data?.data ?? []

  return (
    <PageFrame
      def={def}
      actions={
        <span className="inline-links">
          <Link to="/admin/inbox">全部待办</Link>
          <Link to="/admin/accounts/import">导入账户</Link>
        </span>
      }
    >
      <Banner tone="muted">
        批准时必须同时选择组与账户级额度；额度是<strong>每人</strong>一份，名下多把 Key 共享，
        不会因为多建 Key 而翻倍。
      </Banner>

      {approving && (
        <Section
          title={`批准 ${approving.display_name}`}
          actions={
            <button type="button" className="btn-ghost" onClick={() => setApproving(null)} disabled={approvingBusy}>
              取消
            </button>
          }
        >
          <form
            className="form-card"
            onSubmit={(e) => {
              e.preventDefault()
              void submitApprove()
            }}
          >
            <Field label="加入组" required hint="组决定该账户可见的抽象模型；留空会退回默认组。">
              <select value={groupId} onChange={(e) => setGroupId(e.target.value)} required>
                <option value="">请选择组</option>
                {groupList.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name}
                    {g.is_default ? '（默认）' : ''}
                  </option>
                ))}
              </select>
            </Field>
            <div className="field-row">
              <Field label="额度维度" required>
                <select value={quotaUnit} onChange={(e) => setQuotaUnit(e.target.value as QuotaUnit)}>
                  <option value="request">请求次数</option>
                  <option value="token">Token 数</option>
                  <option value="credit">Credits</option>
                </select>
              </Field>
              <Field label="额度上限" required hint="0 = 全拒；上限按人计。">
                <input
                  type="number"
                  min={0}
                  value={quotaLimit}
                  onChange={(e) => setQuotaLimit(e.target.value)}
                  required
                />
              </Field>
              <Field label="周期" required>
                <select value={quotaPeriod} onChange={(e) => setQuotaPeriod(e.target.value as 'day' | 'month')}>
                  <option value="day">每日</option>
                  <option value="month">每月</option>
                </select>
              </Field>
            </div>
            <FormError error={approveError} />
            <button type="submit" className="btn-primary" disabled={approvingBusy || !groupId}>
              {approvingBusy ? '正在批准…' : '确认批准'}
            </button>
          </form>
        </Section>
      )}

      {rejecting && (
        <>
          <Section title={`拒绝 ${rejecting.display_name}`}>
            <Field label="拒绝理由（可选）" hint="理由会在申请人登录时展示，请写得可行动。">
              <textarea
                rows={3}
                value={rejectReason}
                onChange={(e) => setRejectReason(e.target.value)}
              />
            </Field>
            <FormError error={rejectError} />
          </Section>
          <ConfirmDialog
            open
            title={`确认拒绝 ${rejecting.display_name}`}
            impact={[
              `账户 ${rejecting.display_name}（${rejecting.id}）将转为「已拒绝」`,
              '该账户无法再登录，也无法自助恢复'
            ]}
            irreversibility="不可恢复：拒绝是终局状态，只能由管理员重新创建账户。"
            targetName={rejecting.display_name}
            inputLabel="请输入账户显示名以确认"
            confirmLabel="确认拒绝"
            busy={rejectingBusy}
            error={rejectError}
            onCancel={() => {
              setRejecting(null)
              setRejectReason('')
            }}
            onConfirm={() => void submitReject()}
          />
        </>
      )}

      <AsyncView
        state={pending}
        loadingRows={5}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="没有待审核的注册申请"
              hint="注册关闭或已经清理完毕。你可以去「待处理事项」查看模型变动与故障告警。"
              action={<Link to="/admin/inbox">查看待处理事项</Link>}
            />
          ) : null
        }
      >
        {(d) => (
          <Section title={`待审核账户（${d.total ?? d.data.length}）`}>
            <DataTable headers={['显示名', '申请时间', '来源 IP', '申请的类', '备注', '操作']}>
              {d.data.map((a) => (
                <tr key={a.id}>
                  <td>
                    <Link to={`/admin/accounts/${encodeURIComponent(a.id)}`}>{a.display_name}</Link>
                  </td>
                  <td>{fmtTime(a.created_at)}</td>
                  <td>{a.source_ip ?? '—'}</td>
                  <td>{a.classes.length ? a.classes.join(' / ') : '—'}</td>
                  <td className="cell-wrap">{a.note ?? '—'}</td>
                  <td className="cell-actions">
                    <button type="button" className="btn-primary" onClick={() => startApprove(a)}>
                      批准
                    </button>
                    <button type="button" className="btn-ghost" onClick={() => setRejecting(a)}>
                      拒绝
                    </button>
                  </td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>
    </PageFrame>
  )
}
