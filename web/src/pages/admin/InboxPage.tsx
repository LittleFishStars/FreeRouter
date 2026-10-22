// 待处理事项 `/admin/inbox`（docs/ui/03-admin.md 第 12 节）。
//
// 这一页把「需要管理员动手的事」从散落的页面数字收敛成一屏工作队列：
// 注册申请 / 模型变动 / 故障通知 / 池告警 / 熔断告警。
//
// 两条纪律：
//   - 默认按严重度降序（error > warning > info），同档内按时间倒序；
//   - 「标记已读」只是看板状态，不触发任何业务动作；处理仍在各自页面
//     （就地处理只对输入足够简单的两类成立，本轮统一跳转，避免复制校验）。
//
// 未读数由顶栏与侧边栏共享（60s 轮询 + focus 立即刷，见 useInboxUnread）。

import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  listInbox,
  markInboxReadBatch,
  setInboxRead
} from '../../api/endpoints'
import type { InboxItem } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import {
  AsyncView,
  Banner,
  DataTable,
  EmptyState,
  FormError,
  StatusBadge
} from '../../components/ui'
import { fmtTime, inboxKindLabel, statusLabel } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

/** related_id + kind → 具体处理页（不是就地处理）。 */
function targetFor(item: InboxItem): string {
  switch (item.kind) {
    case 'registration':
      return '/admin'
    case 'model_change':
      return '/admin/real-models'
    case 'failure':
      return '/admin/upstreams'
    case 'pool_alert':
      return item.related_id
        ? `/admin/groups/${encodeURIComponent(item.related_id)}`
        : '/admin/groups'
    case 'breaker_alert':
      return '/admin/settings'
    default:
      return '/admin'
  }
}

const KIND_OPTIONS = [
  ['', '全部类型'],
  ['registration', '注册申请'],
  ['model_change', '模型变动'],
  ['failure', '故障通知'],
  ['pool_alert', '池告警'],
  ['breaker_alert', '熔断告警']
] as const

export default function InboxPage({ def }: PageProps) {
  const [kind, setKind] = useState('')
  const [severity, setSeverity] = useState('')
  const [readFilter, setReadFilter] = useState<'all' | 'unread' | 'read'>('all')
  const [selected, setSelected] = useState<string[]>([])
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)

  const query = useMemo(
    () => ({
      kind: kind || undefined,
      severity: severity || undefined,
      read: readFilter === 'all' ? undefined : readFilter === 'read'
    }),
    [kind, severity, readFilter]
  )
  const q = useApi(() => listInbox({ ...query, limit: 100 }), [query.kind, query.severity, query.read])

  const items = q.data?.data ?? []
  const allSelected = items.length > 0 && selected.length === items.length

  function toggle(id: string) {
    setSelected((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]))
  }

  async function toggleRead(item: InboxItem) {
    setBusy(true)
    setError(null)
    try {
      await setInboxRead(item.id, !item.read_at)
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function markSelectedRead() {
    if (selected.length === 0) return
    setBusy(true)
    setError(null)
    try {
      await markInboxReadBatch(selected)
      setSelected([])
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame
      def={def}
      actions={
        <button type="button" onClick={markSelectedRead} disabled={busy || selected.length === 0}>
          {busy ? '正在提交…' : `标记所选已读（${selected.length}）`}
        </button>
      }
    >
      <Banner tone="muted">
        列表按严重度降序、同档按时间倒序；「标记已读」不会改变关联对象的状态。
        处理请点关联对象跳到原页面，那里的校验规则才是唯一的。
      </Banner>

      <div className="toolbar">
        <label className="toolbar-field">
          类型
          <select value={kind} onChange={(e) => setKind(e.target.value)}>
            {KIND_OPTIONS.map(([v, label]) => (
              <option key={v} value={v}>{label}</option>
            ))}
          </select>
        </label>
        <label className="toolbar-field">
          严重度
          <select value={severity} onChange={(e) => setSeverity(e.target.value)}>
            <option value="">全部</option>
            <option value="error">严重</option>
            <option value="warning">警告</option>
            <option value="info">提示</option>
          </select>
        </label>
        <label className="toolbar-field">
          已读状态
          <select value={readFilter} onChange={(e) => setReadFilter(e.target.value as 'all' | 'unread' | 'read')}>
            <option value="all">全部</option>
            <option value="unread">未读</option>
            <option value="read">已读</option>
          </select>
        </label>
        <button type="button" className="btn-ghost" onClick={q.reload}>刷新</button>
      </div>

      <FormError error={error} />

      <AsyncView
        state={q}
        loadingRows={6}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="当前没有待处理事项"
              hint="注册申请、模型目录变动、渠道故障与额度告警都会汇总到这里；空态说明暂时没有需要你动手的事。"
              action={<button type="button" onClick={q.reload}>刷新</button>}
            />
          ) : null
        }
      >
        {(d) => (
          <DataTable
            headers={[
              <input
                key="select-all"
                type="checkbox"
                checked={allSelected}
                aria-label="全选"
                onChange={(e) => setSelected(e.target.checked ? d.data.map((i) => i.id) : [])}
              />,
              '严重度',
              '类型',
              '标题',
              '时间',
              '已读',
              '处理'
            ]}
          >
            {d.data.map((item) => (
              <tr key={item.id} className={item.read_at ? 'row-read' : 'row-unread'}>
                <td>
                  <input
                    type="checkbox"
                    checked={selected.includes(item.id)}
                    aria-label={`选择 ${item.title}`}
                    onChange={() => toggle(item.id)}
                  />
                </td>
                <td>
                  <StatusBadge status={item.severity} label={statusLabel(item.severity)} />
                </td>
                <td>{inboxKindLabel(item.kind)}</td>
                <td className="cell-wrap">
                  <Link to={targetFor(item)}>{item.title}</Link>
                </td>
                <td>{fmtTime(item.created_at)}</td>
                <td>{item.read_at ? `已读（${item.read_by ?? '—'}）` : '未读'}</td>
                <td className="cell-actions">
                  <button type="button" className="btn-ghost" onClick={() => void toggleRead(item)} disabled={busy}>
                    {item.read_at ? '标为未读' : '标为已读'}
                  </button>
                  <Link to={targetFor(item)}>去处理</Link>
                </td>
              </tr>
            ))}
          </DataTable>
        )}
      </AsyncView>
    </PageFrame>
  )
}
