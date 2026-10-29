// 候选集编辑器（Connect AI 式弹窗，task-8）。
//
// 由 task-7 的 AddModelDialog（只支持追加）升级而来，收编了原独立候选集
// 编辑页 /admin/abstract-models/:id/candidates 的全部职责：
//
//   - 三段布局：顶部「当前候选」横向芯片条（带序号 + ←→ 调序 + ✕ 移除 +
//     点芯片展开渠道范围编辑）；中部左栏供应商 / 右栏模型卡网格（点卡加入/
//     移出，不再锁定已存在候选）；底部「已改 N 处 + 取消 + 保存」。
//   - 保存 = putCandidates(id, rows) 整表替换。有移除时保存按钮先切到内联
//     二次确认态（再点一次才提交）；纯新增 / 纯调序直接保存。
//   - 脏检测：rows 与初始 JSON 不等才允许保存；「已改 N 处」= 新增数 + 移除数
//     + 调序数（简单近似）。
//
// 渠道范围编辑的连线语义（契约 §3.2，与旧候选集页逐字一致）：
// channel_scope 在库里可能是字符串 `"all"`（旧界面留下的失效形状）或
// `{mode:'include'|'exclude', channels:[]}`；读取按形状分支，写入只写
// `{mode:'include', channels:[...]}`，一个都不选 = 删掉 channel_scope 键
// （等价全部渠道），绝不写 mode:'any'；非 include 形状原样保留不解释，并
// 给出「已有自定义约束」的提示行。这个形状由路由桥的 constraintJSON 决定，
// 写错会静默失效。

import { useEffect, useMemo, useState } from 'react'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON } from '../../components/IconButtons'
import {
  getCandidates,
  getModelSources,
  putCandidates,
  refreshRealModels
} from '../../api/endpoints'
import type {
  AdminModelSource,
  AdminModelSourceModel,
  Candidate
} from '../../api/types'
import {
  IconCheck,
  IconDelete,
  IconDown,
  IconRefresh,
  IconSearch,
  IconUp,
  IconWarn
} from '../../icons'
import { EmptyState, FormError, LoadingBlock, StatusBadge } from '../../components/ui'
import { fmtNum, statusLabel } from '../../utils/format'

// ── 协议徽标（契约 §1.2：只有 openai / anthropic 两个取值）──────────────
const AM_PROTOCOL_GLYPH: Record<string, { letter: string; label: string }> = {
  openai: { letter: 'O', label: 'OpenAI 兼容端点' },
  anthropic: { letter: 'A', label: 'Anthropic 原生端点' }
}

const KIND_LABEL: Record<string, string> = {
  relay: '反代',
  vendor: '供应商',
  selfhosted: '自建'
}

/** 徽标的语义边界：表达「供应商提供哪种协议的端点」，而非「可以用那种协议调它」。 */
const PROTOCOL_BOUNDARY =
  '协议徽标只表达「该供应商提供哪种协议的端点」这一事实：本站出站链路当前只实现 OpenAI 兼容，A 徽标是「供应商具备、本站未接入」。'

function ProtocolBadges({ protocols }: { protocols: string[] | undefined }) {
  if (!protocols || protocols.length === 0) return null
  return (
    <span className="proto-badges">
      {protocols.map((p) => (
        <span key={p} className="proto-badge" title={AM_PROTOCOL_GLYPH[p]?.label ?? p}>
          {AM_PROTOCOL_GLYPH[p]?.letter ?? p}
        </span>
      ))}
    </span>
  )
}

// ── channel_scope 的读写（契约 §3.2，与后端 routing_bridge 的 constraintJSON 对齐）──

/** 从 constraint 里读出本界面能编辑的 include 渠道集合（其它形状一律当作「读不出」）。 */
function includeChannels(c: Candidate['constraint']): string[] {
  const raw = c?.channel_scope
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return []
  const scope = raw as { mode?: unknown; channels?: unknown }
  if (scope.mode !== 'include' || !Array.isArray(scope.channels)) return []
  return scope.channels.filter((ch): ch is string => typeof ch === 'string')
}

/** channel_scope 存在但不是 include 形状（exclude / 历史字符串 `"all"` / 其它）。 */
function hasForeignScope(c: Candidate['constraint']): boolean {
  const raw = c?.channel_scope
  if (raw === undefined || raw === null) return false
  if (typeof raw !== 'object' || Array.isArray(raw)) return true
  return (raw as { mode?: unknown }).mode !== 'include'
}

/**
 * 合并写法：只重写 channel_scope，constraint 其余键一个不动；
 * 渠道集合为空 = **删掉** channel_scope 键（等价于任何渠道），而不写 mode:'any'。
 */
function withChannels(c: Candidate['constraint'], channels: string[]): Candidate['constraint'] {
  const next: Candidate['constraint'] = { ...(c ?? {}) }
  if (channels.length === 0) delete next.channel_scope
  else next.channel_scope = { mode: 'include', channels }
  return next
}

function scopeSummary(c: Candidate['constraint']): string {
  if (hasForeignScope(c)) return '已有自定义约束'
  const chs = includeChannels(c)
  return chs.length === 0 ? '全部渠道' : `仅 ${chs.join('、')}`
}

/** 左栏供应商名后的地区后缀（只在名字里没出现该后缀时才补）。 */
function regionSuffix(s: AdminModelSource): string {
  if (!s.region) return ''
  const tag = s.region.toUpperCase()
  return s.name.toUpperCase().includes(`(${tag})`) || s.name.toUpperCase() === tag ? '' : `（${tag}）`
}

function instancesText(v: number | null | undefined): string {
  if (v === null || v === undefined) return '池内'
  return v === 0 ? '无凭据' : fmtNum(v)
}

function instancesTone(v: number | null | undefined): string {
  if (v === null || v === undefined) return 'muted'
  return v === 0 ? 'warn' : 'ok'
}

/** 声明字段：保存时只带真实模型 + 约束 + weight（weight 已退役但 DTO 还带，恒写 0）。 */
function toPayload(rows: Candidate[]): Candidate[] {
  return rows.map((r) => ({
    real_model_id: r.real_model_id,
    constraint: r.constraint,
    weight: 0
  }))
}

export function CandidateDialog({
  modelId,
  modelName,
  onClose,
  onSaved
}: {
  modelId: string
  modelName: string
  onClose: () => void
  onSaved: (id: string) => void
}) {
  const sourcesQ = useApi(() => getModelSources(), [])
  const candsQ = useApi(() => getCandidates(modelId), [modelId])

  // 本地编辑副本（声明序 = 数组序）。
  const [rows, setRows] = useState<Candidate[]>([])
  const [original, setOriginal] = useState<Candidate[]>([])

  const [activeId, setActiveId] = useState<string | null>(null)
  const [vendorQuery, setVendorQuery] = useState('')
  const [modelQuery, setModelQuery] = useState('')
  const [busy, setBusy] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<unknown>(null)
  // 有移除时，保存按钮先切到内联确认态；arms=true 且再点一次才提交。
  const [confirmRemove, setConfirmRemove] = useState(false)

  // 展开当前候选芯片的渠道范围编辑（null = 不展开）。
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null)

  useEffect(() => {
    if (!candsQ.data) return
    const list = candsQ.data.candidates ?? []
    setRows(list)
    setOriginal(list)
    setConfirmRemove(false)
  }, [candsQ.data])

  // ── 事实索引（real_model_id → 模型事实）──
  const modelFacts = useMemo(() => {
    const idx = new Map<string, AdminModelSourceModel>()
    for (const s of sourcesQ.data?.sources ?? []) {
      for (const m of s.models) if (!idx.has(m.real_model_id)) idx.set(m.real_model_id, m)
    }
    return idx
  }, [sourcesQ.data])

  const channels = sourcesQ.data?.channels ?? []
  const sources = sourcesQ.data?.sources ?? []

  // 已存在的候选 id 集合（用于判断「有移除」）。
  const originalIds = useMemo(() => new Set(original.map((c) => c.real_model_id)), [original])

  // 「已改 N 处」：新增数 + 移除数 + 调序数（简单近似，不苛求精确）。
  const changed = JSON.stringify(rows) !== JSON.stringify(original)
  const changedCount = useMemo(() => {
    // 调序 = 行序与原始声明序不同。为简单，检测「相对原始的顺序被破坏」。
    const origOrder = new Map(original.map((r, i) => [r.real_model_id, i]))
    let reordered = 0
    let prevIdx = -1
    for (const r of rows) {
      const oi = origOrder.get(r.real_model_id)
      if (oi === undefined) {
        reordered += 1 // 新增
      } else if (oi < prevIdx) {
        reordered += 1 // 相对顺序变了
      }
      prevIdx = oi ?? prevIdx
    }
    // 契约：已改 N 处 = 新增数 + 移除数 + 调序数。旧实现漏加了移除数，
    // 会出现「已改 0 处 + 含移除 1 条」这种自相矛盾的底部文案。
    return reordered + [...originalIds].filter((id) => !new Set(rows.map((r) => r.real_model_id)).has(id)).length
  }, [rows, original])

  // 有移除 = 原候选里有某行已不在 rows 里。
  const rowIds = useMemo(() => new Set(rows.map((r) => r.real_model_id)), [rows])
  const removedCount = [...originalIds].filter((id) => !rowIds.has(id)).length
  const hasRemoval = removedCount > 0

  const candsLoading = candsQ.loading && !candsQ.data
  const canSave = changed && !candsLoading && !busy

  // ── 左栏分组 / 右栏可见模型 ──
  const vq = vendorQuery.trim().toLowerCase()
  const mq = modelQuery.trim().toLowerCase()

  const visibleSources = useMemo(
    () =>
      sources.filter(
        (s) => !vq || `${s.name} ${s.upstream_name} ${s.id}`.toLowerCase().includes(vq)
      ),
    [sources, vq]
  )
  // 分组口径与凭据池左栏一致（2026-10-30 用户决定）：反代供应商（oauth，
  // 账号授权，凭据由 sidecar 换出）与 API 供应商（粘 Key 直连端点）。旧的
  // 「贡献凭据 / 平台渠道」按来源归属切，与接入形态是两个维度——两处两种
  // 切法只会让人来回翻译。
  const relaySources = visibleSources.filter((s) => s.oauth)
  const apiSources = visibleSources.filter((s) => !s.oauth)
  const active = activeId ? (sources.find((s) => s.id === activeId) ?? null) : null
  const visibleModels = useMemo(() => {
    if (!active) return []
    if (!mq) return active.models
    return active.models.filter(
      (m) => `${m.display_name} ${m.upstream_model_id}`.toLowerCase().includes(mq)
    )
  }, [active, mq])

  function toggleModel(m: AdminModelSourceModel) {
    if (m.excluded_from_candidates || m.status !== 'active') return
    setRows((rs) => {
      const i = rs.findIndex((r) => r.real_model_id === m.real_model_id)
      if (i === -1) return [...rs, { real_model_id: m.real_model_id, constraint: {}, weight: 0 }]
      return rs.filter((_, idx) => idx !== i)
    })
  }

  function move(i: number, dir: -1 | 1) {
    const j = i + dir
    if (j < 0 || j >= rows.length) return
    const next = [...rows]
    ;[next[i], next[j]] = [next[j], next[i]]
    setRows(next)
  }

  function removeRow(i: number) {
    setRows(rows.filter((_, idx) => idx !== i))
  }

  function toggleChannel(i: number, channel: string) {
    const current = includeChannels(rows[i].constraint)
    const next = current.includes(channel) ? current.filter((c) => c !== channel) : [...current, channel]
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, constraint: withChannels(r.constraint, next) } : r)))
  }

  async function refresh() {
    setRefreshing(true)
    try {
      await refreshRealModels()
      sourcesQ.reload()
    } catch {
      // 刷新失败不阻塞，错误由下一次 sources 拉取暴露。
    } finally {
      setRefreshing(false)
    }
  }

  async function submit() {
    setBusy(true)
    setError(null)
    try {
      await putCandidates(modelId, toPayload(rows))
      onSaved(modelId)
      onClose()
    } catch (e) {
      setError(e)
      setBusy(false)
    }
  }

  // 保存按钮：有移除先切内联确认态；纯新增/纯调序直接提交。
  function onSaveClick() {
    if (hasRemoval && !confirmRemove) {
      setConfirmRemove(true)
      return
    }
    void submit()
  }

  function railGroup(title: string, list: AdminModelSource[]) {
    return (
      <div className="amd-group">
        <p className="amd-group-title">
          {title}
          <span className="source-group-count">{list.length}</span>
        </p>
        {list.length === 0 ? (
          <p className="muted amd-group-empty">（无）</p>
        ) : (
          <ul className="amd-source-list">
            {list.map((s) => (
              <li key={s.id}>
                <button
                  type="button"
                  className={'amd-source-row' + (s.id === activeId ? ' amd-source-row-active' : '')}
                  data-off={!s.enabled}
                  onClick={() => setActiveId(s.id)}
                >
                  <span className="amd-source-row-name">
                    {s.name}
                    {regionSuffix(s)}
                  </span>
                  <ProtocolBadges protocols={s.protocols} />
                  <span className="source-count" title={`${s.model_count} 个模型`}>
                    {s.model_count}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    )
  }

  function modelCard(m: AdminModelSourceModel) {
    const inRows = rows.findIndex((r) => r.real_model_id === m.real_model_id)
    const selected = inRows !== -1
    const blocked =
      m.excluded_from_candidates
        ? '该模型已在候选中被排除，不能加入候选集'
        : m.status !== 'active'
          ? `状态为「${statusLabel(m.status)}」，不能加入候选集`
          : ''
    const title = blocked || (selected ? '点击移出候选集' : '点击加入候选集')
    return (
      <div className="source-card" key={m.real_model_id} data-selected={selected} data-blocked={blocked !== ''}>
        <div
          className="source-card-hit"
          role="checkbox"
          aria-checked={selected}
          aria-disabled={blocked !== ''}
          tabIndex={blocked === '' ? 0 : -1}
          title={title}
          onClick={blocked === '' ? () => toggleModel(m) : undefined}
          onKeyDown={
            blocked === ''
              ? (e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    toggleModel(m)
                  }
                }
              : undefined
          }
        >
          <span className="source-card-title">
            <span className="source-card-name">{m.display_name}</span>
            {selected && <IconCheck size="sm" />}
          </span>
          <span className="source-card-sub">{m.upstream_model_id}</span>
          <span className="source-card-meta">
            <ProtocolBadges protocols={m.protocols} />
            <StatusBadge status={m.status} label={statusLabel(m.status)} />
            <span className="source-inst" data-tone={instancesTone(m.available_instances)}>
              {m.available_instances === 0 && <IconWarn size="xs" />}
              可用实例 {instancesText(m.available_instances)}
            </span>
          </span>
          <span className="source-card-meta">
            <span className="muted">上下文 {fmtNum(m.context_window)}</span>
            {m.channel && <span className="muted">渠道 {m.channel}</span>}
            {m.excluded_from_candidates && <span className="muted">已排除</span>}
          </span>
        </div>
      </div>
    )
  }

  function curChip(r: Candidate, i: number) {
    const fact = modelFacts.get(r.real_model_id)
    const name = fact?.display_name ?? r.real_model_id
    return (
      <div key={r.real_model_id} className="amd-cur-chip" data-expanded={expandedIdx === i} data-off={fact ? undefined : true}>
        <button type="button" className="amd-cur-chip-main" onClick={() => setExpandedIdx(expandedIdx === i ? null : i)}>
          <span className="amd-cur-chip-idx">{i + 1}</span>
          <span className="amd-cur-chip-name">{name}</span>
          <span className="amd-cur-chip-scope">{scopeSummary(r.constraint)}</span>
        </button>
        <span className="amd-cur-chip-ops">
          <button type="button" className="amd-cur-op" onClick={() => move(i, -1)} disabled={i === 0} aria-label="上移">
            <IconUp size="xs" />
          </button>
          <button type="button" className="amd-cur-op" onClick={() => move(i, 1)} disabled={i === rows.length - 1} aria-label="下移">
            <IconDown size="xs" />
          </button>
          <button type="button" className="amd-cur-op" onClick={() => removeRow(i)} aria-label="移除">
            <IconDelete size="xs" />
          </button>
        </span>
        {expandedIdx === i && (
          <div className="amd-cur-edit" onClick={(e) => e.stopPropagation()}>
            <span className="source-param-label">渠道范围</span>
            <div className="source-chips">
              {channels.length === 0 && <span className="muted">池内还没有任何渠道</span>}
              {channels.map((ch) => {
                const on = includeChannels(rows[i].constraint).includes(ch)
                return (
                  <button
                    key={ch}
                    type="button"
                    className="source-chip"
                    data-on={on}
                    aria-pressed={on}
                    onClick={() => toggleChannel(i, ch)}
                  >
                    {on && <IconCheck size="xs" />}
                    {ch}
                  </button>
                )
              })}
            </div>
            <span className="source-param-hint">
              {hasForeignScope(rows[i].constraint)
                ? '当前是可编辑范围之外的自定义约束（见下）；本界面只编辑渠道范围。'
                : `${scopeSummary(rows[i].constraint)}；一个都不选 = 全部渠道（不写 channel_scope）`}
            </span>
            {hasForeignScope(rows[i].constraint) && (
              <span className="source-param-hint">
                已有自定义约束（本界面只编辑渠道范围）：一动芯片就按 include 形状重写，其它约束键原样保留。
              </span>
            )}
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div
        className="dialog amd-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={`候选集 · ${modelName}`}
        onClick={(e) => e.stopPropagation()}
      >
        <h2>
          候选集 · <code>{modelName}</code>
        </h2>

        <div className="amd-toolbar">
          <div className="source-search">
            <IconSearch size="sm" />
            <input
              value={vendorQuery}
              onChange={(e) => setVendorQuery(e.target.value)}
              placeholder="搜索供应商…"
              aria-label="搜索供应商"
            />
          </div>
          <button type="button" className="btn-ghost" onClick={() => void refresh()} disabled={refreshing}>
            <IconRefresh size={ACTION_ICON} /> 刷新目录
          </button>
          <div className="source-search">
            <IconSearch size="sm" />
            <input
              value={modelQuery}
              onChange={(e) => setModelQuery(e.target.value)}
              placeholder="搜索模型…"
              aria-label="搜索模型"
            />
          </div>
        </div>

        {/* 顶部：当前候选芯片条 */}
        <div className="amd-cur">
          <p className="amd-cur-title">
            当前候选（{rows.length}）
          </p>
          {rows.length === 0 ? (
            <p className="muted amd-cur-empty">no_candidate；请从下部模型区点卡片加入。</p>
          ) : (
            <div className="amd-cur-row">
              {rows.map(curChip)}
            </div>
          )}
        </div>

        {/* 中部：左栏供应商 / 右栏模型卡网格 */}
        <div className="amd-split">
          <div className="amd-rail">
            {railGroup('反代供应商', relaySources)}
            {railGroup('API 供应商', apiSources)}
            {vendorQuery && relaySources.length === 0 && apiSources.length === 0 && (
              <p className="muted">没有匹配的供应商。</p>
            )}
          </div>

          <div className="amd-detail">
            {!active ? (
              <EmptyState
                title="先从左侧选一个供应商"
                hint="左栏按「反代供应商 / API 供应商」分组；选中后这里显示它的模型卡片，点卡片即加入或移出候选集。"
              />
            ) : (
              <>
                <div className="amd-head">
                  <span className="amd-head-name">{active.name}</span>
                  <span className="amd-head-tag">{KIND_LABEL[active.kind] ?? active.kind}</span>
                  <code className="amd-head-endpoint">{active.base_url || '未提供端点'}</code>
                </div>
                <p className="muted amd-head-note">{PROTOCOL_BOUNDARY}</p>
                {visibleModels.length === 0 ? (
                  <EmptyState title="该供应商下没有模型" hint="模型清单来自 real_models；先让它被采集到，或换一个供应商。" />
                ) : (
                  <div className="source-grid amd-grid">{visibleModels.map(modelCard)}</div>
                )}
              </>
            )}
          </div>
        </div>

        <div className="dialog-actions amd-footer">
          <span className="amd-selected-count">
            已改 {changedCount} 处
            {hasRemoval && <span className="source-inst" data-tone="warn">含移除 {removedCount} 条</span>}
          </span>
          <button type="button" className="btn-ghost" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button
            type="button"
            className={confirmRemove ? 'btn-danger' : 'btn-primary'}
            onClick={onSaveClick}
            disabled={!canSave}
          >
            {busy ? '正在保存…' : confirmRemove ? `移除 ${removedCount} 条，确认保存` : '保存候选集'}
          </button>
        </div>
        <FormError error={error} />
        {candsLoading && <LoadingBlock rows={1} />}
      </div>
    </div>
  )
}