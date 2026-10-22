// 真实模型目录 `/admin/real-models`（docs/ui/03-admin.md 第 6 节）。
//
// 真实模型名（dsh:渠道/上游模型 id）只允许出现在管理端：目录、候选集、
// 凭据/鉴定、调用日志（管理视图）与审计。租户端任何页面都不得出现。
//
// missing 的处置必须是「跳到抽象模型弃用页」而不是就地弃用：弃用是抽象
// 模型层的动作，且要过两条硬校验（目标必须 active、弃用不可逆）。

import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  excludeRealModel,
  listRealModels,
  patchRealModel,
  refreshRealModels
} from '../../api/endpoints'
import type { AdminRealModel, AdminRealModelPatch } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import {
  AsyncView,
  Banner,
  DataTable,
  EmptyState,
  Field,
  FormError,
  Section,
  StatusBadge
} from '../../components/ui'
import { fmtNum, fmtTime, statusLabel, truncate } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

const STATUS_OPTIONS = [
  ['', '全部状态'],
  ['active', '正常'],
  ['missing', '已消失'],
  ['disabled', '已停用']
] as const

export function RealModelsPage({ def }: PageProps) {
  const [status, setStatus] = useState('')
  const [hidden, setHidden] = useState<'all' | 'visible' | 'hidden'>('all')
  const q = useApi(
    () =>
      listRealModels({
        status: status || undefined,
        hidden: hidden === 'all' ? undefined : hidden === 'hidden',
        limit: 200
      }),
    [status, hidden]
  )
  const [editing, setEditing] = useState<AdminRealModel | null>(null)
  const [draft, setDraft] = useState<AdminRealModelPatch>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    if (!editing) return
    setDraft({
      display_name: editing.display_name,
      group_name: editing.group_name ?? '',
      context_window: editing.context_window,
      hidden: editing.hidden,
      sort_order: editing.sort_order,
      credit_multiplier: editing.credit_multiplier ?? { in: 1, out: 1 }
    })
  }, [editing])

  async function refresh() {
    setBusy(true)
    setError(null)
    try {
      await refreshRealModels()
      setNotice('已触发目录刷新；结果会在后台任务完成后体现。')
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function save() {
    if (!editing) return
    setBusy(true)
    setError(null)
    try {
      await patchRealModel(editing.id, draft)
      setNotice('已保存（写入 source=override，后续刷新不会覆盖）')
      setEditing(null)
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function toggleExcluded(m: AdminRealModel) {
    setBusy(true)
    setError(null)
    try {
      await excludeRealModel(m.id, !m.excluded_from_candidates)
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
        <button type="button" onClick={() => void refresh()} disabled={busy}>
          {busy ? '正在刷新…' : '刷新上游目录'}
        </button>
      }
    >
      <Banner tone="muted">
        真实模型名仅管理员可见。编辑会写入 <code>source=override</code>，之后的目录刷新不再覆盖；
        内置价格表覆盖不到的模型乘数默认 1（避免新模型被算成免费或天价）。
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <div className="toolbar">
        <label className="toolbar-field">
          状态
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUS_OPTIONS.map(([v, l]) => (
              <option key={v} value={v}>{l}</option>
            ))}
          </select>
        </label>
        <label className="toolbar-field">
          隐藏
          <select value={hidden} onChange={(e) => setHidden(e.target.value as 'all' | 'visible' | 'hidden')}>
            <option value="all">全部</option>
            <option value="visible">未隐藏</option>
            <option value="hidden">已隐藏</option>
          </select>
        </label>
        <button type="button" className="btn-ghost" onClick={q.reload}>刷新列表</button>
      </div>

      {editing && (
        <Section
          title={`编辑 ${editing.qualified_name}`}
          actions={<button type="button" className="btn-ghost" onClick={() => setEditing(null)}>取消</button>}
          hint="限定名只读；显示名/分组/能力/隐藏/排序/价格乘数可改。"
        >
          <div className="field-row">
            <Field label="显示名">
              <input value={draft.display_name ?? ''} onChange={(e) => setDraft({ ...draft, display_name: e.target.value })} />
            </Field>
            <Field label="分组">
              <input value={draft.group_name ?? ''} onChange={(e) => setDraft({ ...draft, group_name: e.target.value })} />
            </Field>
            <Field label="上下文窗口">
              <input
                type="number"
                value={draft.context_window ?? ''}
                onChange={(e) => setDraft({ ...draft, context_window: e.target.value === '' ? null : Number(e.target.value) })}
              />
            </Field>
            <Field label="排序">
              <input
                type="number"
                value={draft.sort_order ?? 0}
                onChange={(e) => setDraft({ ...draft, sort_order: Number(e.target.value) })}
              />
            </Field>
            <label className="checkbox-line">
              <input type="checkbox" checked={Boolean(draft.hidden)} onChange={(e) => setDraft({ ...draft, hidden: e.target.checked })} />
              在候选编辑里隐藏
            </label>
          </div>
          <div className="field-row">
            <Field label="Credits 输入乘数" hint="基准模型 = 1">
              <input
                type="number"
                step="0.1"
                value={draft.credit_multiplier?.in ?? 1}
                onChange={(e) =>
                  setDraft({ ...draft, credit_multiplier: { in: Number(e.target.value), out: draft.credit_multiplier?.out ?? 1 } })
                }
              />
            </Field>
            <Field label="Credits 输出乘数" hint="基准模型 = 1">
              <input
                type="number"
                step="0.1"
                value={draft.credit_multiplier?.out ?? 1}
                onChange={(e) =>
                  setDraft({ ...draft, credit_multiplier: { in: draft.credit_multiplier?.in ?? 1, out: Number(e.target.value) } })
                }
              />
            </Field>
            <button type="button" className="btn-primary" onClick={() => void save()} disabled={busy}>
              {busy ? '正在保存…' : '保存覆盖'}
            </button>
          </div>
        </Section>
      )}

      <AsyncView
        state={q}
        loadingRows={6}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="目录里还没有真实模型"
              hint="先配置上游并触发一次目录刷新；missing 巡检也会在这里标记连续未出现的模型。"
              action={<button type="button" onClick={() => void refresh()}>刷新上游目录</button>}
            />
          ) : null
        }
      >
        {(d) => (
          <Section title={`真实模型（${d.total ?? d.data.length}）`}>
            <DataTable headers={['限定名', '显示名', '分组', '状态', '隐藏', '来源', '最后出现', 'Credits', '操作']}>
              {d.data.map((m) => (
                <tr key={m.id}>
                  <td className="cell-wrap"><code>{truncate(m.qualified_name, 56)}</code></td>
                  <td>{m.display_name}</td>
                  <td>{m.group_name ?? '—'}</td>
                  <td><StatusBadge status={m.status} label={statusLabel(m.status)} /></td>
                  <td>{m.hidden ? '是' : '否'}</td>
                  <td>{m.source === 'override' ? '管理员覆盖' : '自动发现'}</td>
                  <td>{fmtTime(m.last_seen_at)}</td>
                  <td>
                    {m.credit_multiplier
                      ? `${m.credit_multiplier.in} / ${m.credit_multiplier.out}`
                      : '1 / 1'}
                  </td>
                  <td className="cell-actions">
                    <button type="button" className="btn-ghost" onClick={() => setEditing(m)}>编辑</button>
                    <button type="button" className="btn-ghost" onClick={() => void toggleExcluded(m)} disabled={busy}>
                      {m.excluded_from_candidates ? '恢复候选' : '排除候选'}
                    </button>
                    {m.status === 'missing' && (
                      <Link to="/admin/abstract-models" title="弃用是抽象模型层的动作，需过两条硬校验">
                        去弃用页
                      </Link>
                    )}
                  </td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>

      <Section title="目录口径">
        <ul className="plain-list">
          <li>限定名由上游目录决定，不可改写；这是 missing 巡检的判据。</li>
          <li><code>aggregate/*</code> 默认排除出候选：我们的策略与上游策略叠加会导致无法归因。</li>
          <li>连续 N 天未出现 → 自动标 missing；处置是弃用抽象模型或忽略。</li>
          <li>价格乘数同时服务 Credits 扣减与 price 排序，两处读同一份。</li>
        </ul>
        <p className="muted">共 {fmtNum(dReloadCount(q.data?.total))} 条；隐藏项仍在目录中，只是候选编辑时折叠。</p>
      </Section>
    </PageFrame>
  )
}

function dReloadCount(total: number | undefined): number {
  return total ?? 0
}
