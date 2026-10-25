// 审计日志 `/admin/audit`（docs/ui/03-admin.md 第 10 节）。
//
// append-only，只读：没有删除、没有编辑、没有清空——哪怕加二次确认也不行。
// 一个可以被管理员清空的审计等于没有审计。
//
// 筛选维度：时间 / 账户 / 权限类 / 动作 / 结果；动作支持前缀（如 "group."）。
// 详情默认不记录请求正文；正文记录开启时是脱敏模式，必须在 UI 明示。

import { useState } from 'react'
import { exportAudit, listAudit } from '../../api/endpoints'
import type { AuditEntry } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON, ToolButton } from '../../components/IconButtons'
import { IconDownload } from '../../icons'
import {
  AsyncView,
  Banner,
  DataTable,
  EmptyState,
  Field,
  FormError,
  JsonDetails,
  Section,
  StatusBadge
} from '../../components/ui'
import { downloadBlob, fmtTime, statusLabel } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

function toRFC3339(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

export default function AuditPage({ def }: PageProps) {
  const [accountId, setAccountId] = useState('')
  const [actingClass, setActingClass] = useState('')
  const [action, setAction] = useState('')
  const [result, setResult] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [format, setFormat] = useState<'jsonl' | 'csv'>('jsonl')
  const [exportError, setExportError] = useState<unknown>(null)
  const [exporting, setExporting] = useState(false)

  const query = {
    account_id: accountId || undefined,
    acting_class: actingClass || undefined,
    action: action || undefined,
    result: result || undefined,
    from: toRFC3339(from),
    to: toRFC3339(to),
    limit: 100
  }
  const q = useApi(() => listAudit(query), [accountId, actingClass, action, result, from, to])

  async function doExport() {
    setExporting(true)
    setExportError(null)
    try {
      const blob = await exportAudit(query, format)
      downloadBlob(`audit.${format}`, blob)
    } catch (e) {
      setExportError(e)
    } finally {
      setExporting(false)
    }
  }

  return (
    <PageFrame
      def={def}
      actions={
        <span className="inline-links">
          <label className="toolbar-field">
            格式
            <select value={format} onChange={(e) => setFormat(e.target.value as 'jsonl' | 'csv')}>
              <option value="jsonl">JSONL</option>
              <option value="csv">CSV</option>
            </select>
          </label>
          <ToolButton icon={<IconDownload size={ACTION_ICON} />} busy={exporting} onClick={() => void doExport()}>
            导出当前筛选
          </ToolButton>
        </span>
      }
    >
      <Banner tone="muted">
        append-only，只读：没有删除、编辑或清空入口。每条写操作都会留下记录；
        登录事件单列可筛（成功 / 失败 / 被锁定）。
      </Banner>

      <div className="toolbar">
        <label className="toolbar-field">
          从
          <input type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="toolbar-field">
          到
          <input type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
        <label className="toolbar-field">
          账户 ID
          <input value={accountId} onChange={(e) => setAccountId(e.target.value)} placeholder="acc_…" />
        </label>
        <label className="toolbar-field">
          权限类
          <select value={actingClass} onChange={(e) => setActingClass(e.target.value)}>
            <option value="">全部</option>
            <option value="admin">管理员</option>
            <option value="tenant">调用者</option>
            <option value="contributor">贡献者</option>
          </select>
        </label>
        <label className="toolbar-field">
          动作
          <input value={action} onChange={(e) => setAction(e.target.value)} placeholder="group. / login." />
        </label>
        <label className="toolbar-field">
          结果
          <select value={result} onChange={(e) => setResult(e.target.value)}>
            <option value="">全部</option>
            <option value="ok">成功</option>
            <option value="denied">被拒绝</option>
            <option value="error">错误</option>
          </select>
        </label>
        <button type="button" className="btn-ghost" onClick={q.reload}>查询</button>
      </div>

      <FormError error={exportError} />

      <AsyncView
        state={q}
        loadingRows={8}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState title="没有匹配的审计记录" hint="调整时间范围或筛选条件；写操作才会产生记录，读操作不产生。" />
          ) : null
        }
      >
        {(d) => (
          <Section title={`审计记录（${d.total ?? d.data.length}）`}>
            <DataTable headers={['时间', '账户', '权限类', '动作', '目标', '结果', '来源 IP', '详情']}>
              {d.data.map((e: AuditEntry) => (
                <tr key={e.id}>
                  <td>{fmtTime(e.ts)}</td>
                  <td><code>{e.account_id ?? '—'}</code></td>
                  <td>{e.acting_class ?? '—'}</td>
                  <td><code>{e.action}</code></td>
                  <td className="cell-wrap">{e.target ?? '—'}</td>
                  <td><StatusBadge status={e.result} label={statusLabel(e.result)} /></td>
                  <td>{e.ip ?? '—'}</td>
                  <td>{e.detail ? <JsonDetails summary="展开" value={e.detail} /> : '—'}</td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>

      <Section title="口径">
        <ul className="plain-list">
          <li><code>acting_class</code> 记录操作发生在哪个能力面：同一账户的「改抽象模型」与「建 Key」性质完全不同。</li>
          <li>详情默认不记录请求正文；开启正文记录时是脱敏模式（保留结构、抹掉文本），界面必须显著提示。</li>
          <li>审计没有保留期定案；在定案前不自动删除。</li>
          <li>登录事件（成功 / 失败 / 被锁定）是判断「账户最近有没有被试着进入」的唯一线索。</li>
        </ul>
        <Field label="请求 ID 对齐">
          <p className="muted">
            排障时让用户提供页面错误态里的请求 ID（<code>x-free-router-request-id</code>），
            与这里的动作时间对齐即可定位到具体一次操作。
          </p>
        </Field>
      </Section>
    </PageFrame>
  )
}
