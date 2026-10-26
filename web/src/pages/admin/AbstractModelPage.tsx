// 抽象模型：列表与编辑（docs/ui/03-admin.md 第 7.1–7.2 节）。
//
// 对外名创建后不可改：契约的稳定性来自名字，改名等于把旧名字的所有客户端
// 一次性打断；正确手段是弃用 + 替代链。
//
// 能力模式、排序链与能力声明都属于「改抽象模型」危险写：保存前必须走
// 统一二次确认框。排序策略只有四种 + 链末 round-robin 平局决胜。

import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON, TableAction, TableActions, ToolButton } from '../../components/IconButtons'
import {
  createAbstractModel,
  dryRunAbstractModel,
  getAbstractModel,
  getCandidates,
  getModelSources,
  getReferences,
  listAbstractModels,
  patchAbstractModel,
  putCandidates,
  refreshRealModels
} from '../../api/endpoints'
import type {
  AdminAbstractModel,
  AdminModelSource,
  AdminModelSourceModel,
  Candidate,
  DryRunResult,
  ModelCapabilities
} from '../../api/types'
import {
  IconBack,
  IconCheck,
  IconChevronDown,
  IconDelete,
  IconDown,
  IconLock,
  IconPlus,
  IconRefresh,
  IconSearch,
  IconUp,
  IconWarn
} from '../../icons'
import ConfirmDialog from '../../components/ConfirmDialog'
import {
  AsyncView,
  Badge,
  Banner,
  EmptyState,
  LoadingBlock,
  Field,
  FormError,
  JsonDetails,
  KeyValues,
  Section,
  StatusBadge
} from '../../components/ui'
import { fmtNum, statusLabel } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

const STATUS_OPTIONS = [
  ['', '全部状态'],
  ['active', '可用'],
  ['deprecated', '已弃用'],
  ['disabled', '已停用']
] as const

const STRATEGIES = [
  ['expiry-first', 'expiry-first（最快作废的先烧）'],
  ['balance', 'balance（余额最少的先用）'],
  ['speed', 'speed（更快）'],
  ['price', 'price（更便宜）']
] as const

type ExpDetail =
  | { state: 'loading' }
  | { state: 'ready'; candidates: Candidate[] }
  | { state: 'error' }

export function AbstractModelsPage({ def }: PageProps) {
  const navigate = useNavigate()
  const [status, setStatus] = useState('')
  const q = useApi(
    () => listAbstractModels({ limit: 200 }),
    [status]
  )
  // 候选芯片的 display_name 需要真实模型事实（详情端点的候选只给 real_model_id）。
  const sourcesQ = useApi(() => getModelSources(), [])
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [mode, setMode] = useState('strict')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  // 同时只允许一行展开；展开某行时按需拉它的候选。
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [details, setDetails] = useState<Record<string, ExpDetail>>({})

  const rows = (q.data?.data ?? []).filter((m) => !status || m.status === status)

  // real_model_id → 模型事实索引（外面已去重，越靠前的来源先占位）。
  const modelFacts = useRef(new Map<string, AdminModelSourceModel>())
  const factsReady = sourcesQ.data !== undefined
  if (factsReady) {
    const idx = modelFacts.current
    for (const s of sourcesQ.data?.sources ?? []) {
      for (const m of s.models) if (!idx.has(m.real_model_id)) idx.set(m.real_model_id, m)
    }
  }

  // 展开某行：若无缓存则拉取详情候选；同时只保留一个展开行。
  useEffect(() => {
    if (!expandedId) return
    const cached = details[expandedId]
    if (cached) return
    setDetails((d) => ({ ...d, [expandedId]: { state: 'loading' } }))
    getCandidates(expandedId)
      .then((r) => {
        setDetails((d) => ({ ...d, [expandedId]: { state: 'ready', candidates: r.candidates } }))
      })
      .catch(() => {
        setDetails((d) => ({ ...d, [expandedId]: { state: 'error' } }))
      })
  }, [expandedId]) // eslint-disable-line react-hooks/exhaustive-deps

  function toggle(id: string) {
    setExpandedId((prev) => (prev === id ? null : id))
  }

  // 弹窗保存成功后：刷新列表，并删掉该模型的展开缓存（下次展开重新拉候选）。
  function handleAddSaved(id: string) {
    q.reload()
    setDetails((d) => {
      const next = { ...d }
      delete next[id]
      return next
    })
  }

  async function submitCreate() {
    setBusy(true)
    setError(null)
    try {
      const r = await createAbstractModel({
        name: name.trim(),
        display_name: displayName.trim() || name.trim(),
        capability_mode: mode,
        strategy: ['expiry-first', 'balance']
      })
      setCreating(false)
      setName('')
      setDisplayName('')
      navigate(`/admin/abstract-models/${encodeURIComponent(r.data.id)}`)
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
        <ToolButton primary icon={<IconPlus size={ACTION_ICON} />} onClick={() => setCreating((v) => !v)}>
          {creating ? '收起' : '新建抽象模型'}
        </ToolButton>
      }
    >
      <Banner tone="muted">
        抽象模型是对用户暴露的唯一命名空间。列表里同时显示「候选数 / 可用候选数」：
        状态是「可用」但可用候选为 0，对外就是 <code>no_candidate</code>。
      </Banner>

      {creating && (
        <Section title="新建抽象模型" hint="对外名创建后不可改；能力模式与策略链可稍后在编辑页调整。">
          <div className="field-row">
            <Field label="对外名" required hint="客户端请求时用的名字，例如 smart。">
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="smart" />
            </Field>
            <Field label="显示名">
              <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="通用智能" />
            </Field>
            <Field label="能力模式">
              <select value={mode} onChange={(e) => setMode(e.target.value)}>
                <option value="strict">严格（取候选最小值）</option>
                <option value="filter">过滤（取并集 + 路由前筛）</option>
              </select>
            </Field>
            <button type="button" className="btn-primary" onClick={() => void submitCreate()} disabled={busy || !name.trim()}>
              {busy ? '正在创建…' : '创建'}
            </button>
          </div>
          <FormError error={error} />
        </Section>
      )}

      <div className="toolbar">
        <label className="toolbar-field">
          状态
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUS_OPTIONS.map(([v, l]) => (
              <option key={v} value={v}>{l}</option>
            ))}
          </select>
        </label>
        <ToolButton icon={<IconRefresh size={ACTION_ICON} />} onClick={q.reload}>刷新</ToolButton>
      </div>

      <AsyncView
        state={q}
        loadingRows={5}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="还没有抽象模型"
              hint="系统不预置任何抽象模型名。先创建一个对外名，再配置候选集与策略链。"
              action={<button type="button" onClick={() => setCreating(true)}>新建抽象模型</button>}
            />
          ) : null
        }
      >
        {() => (
          <Section title={`抽象模型（${rows.length}）`}>
            <div className="am-list">
              {rows.map((m) => (
                <ModelRow
                  key={m.id}
                  m={m}
                  expanded={expandedId === m.id}
                  onToggle={() => toggle(m.id)}
                  detail={expandedId === m.id ? (details[m.id] ?? undefined) : undefined}
                  facts={modelFacts.current}
                  onAddSaved={handleAddSaved}
                />
              ))}
            </div>
            {rows.length === 0 && <p className="muted">该状态下没有模型。</p>}
          </Section>
        )}
      </AsyncView>
    </PageFrame>
  )
}

// 单行模型卡片 + （展开时）横向候选芯片条。
function ModelRow({
  m,
  expanded,
  onToggle,
  detail,
  facts,
  onAddSaved
}: {
  m: AdminAbstractModel
  expanded: boolean
  onToggle: () => void
  detail: ExpDetail | undefined
  facts: Map<string, AdminModelSourceModel>
  onAddSaved: (id: string) => void
}) {
  const editTo = `/admin/abstract-models/${encodeURIComponent(m.id)}`
  const candTo = `${editTo}/candidates`
  const [adding, setAdding] = useState(false)

  return (
    <div className="am-row" data-expanded={expanded}>
      <div className="am-row-head" onClick={onToggle}>
        <span className="am-row-main">
          <Link to={editTo} onClick={(e) => e.stopPropagation()}><code>{m.name}</code></Link>
        </span>
        <span className="am-row-display">{m.display_name}</span>
        <span className="am-row-cell"><StatusBadge status={m.status} label={statusLabel(m.status)} /></span>
        <span className="am-row-cell">
          <Badge tone={m.available_candidate_count === 0 ? 'warn' : 'muted'}>
            {m.candidate_count} / {m.available_candidate_count}
          </Badge>
        </span>
        <span className="am-row-cell am-row-mode">
          {m.capability_mode === 'strict' ? '严格' : '过滤'}
        </span>
        <span className="am-row-chain">{(m.strategy ?? []).join(' → ') || '—'}</span>
        <span className="am-row-ops" onClick={(e) => e.stopPropagation()}>
          <Link to={candTo}>候选集</Link>
          <Link to={`${editTo}/deprecation`}>弃用</Link>
          <button type="button" className="am-expand" data-open={expanded} onClick={onToggle} aria-expanded={expanded} aria-label={expanded ? '收起候选' : '展开候选'}>
            <IconChevronDown size="sm" />
          </button>
        </span>
      </div>
      {expanded && (
        <div className="am-expand-body">
          {detail === undefined && <div className="am-explain">展开候选…</div>}
          {detail?.state === 'loading' && <div className="am-loading"><LoadingBlock rows={1} /></div>}
          {detail?.state === 'error' && <div className="am-error muted">候选加载失败，请重试。</div>}
          {detail?.state === 'ready' && (
            <div className="am-chip-row">
              {detail.candidates.length === 0 ? (
                <span className="muted am-empty">尚未配置候选。</span>
              ) : (
                detail.candidates.map((c) => {
                  const fact = facts.get(c.real_model_id)
                  const name = fact?.display_name ?? c.real_model_id
                  const supported = c.available_instances !== 0 && c.available_instances !== undefined
                  return (
                    <Link key={c.real_model_id} to={candTo} className="am-chip">
                      <span className="am-chip-name">{name}</span>
                      {supported && <IconCheck size="xs" />}
                    </Link>
                  )
                })
              )}
              <button type="button" className="am-add-chip" onClick={() => setAdding(true)}>
                <IconPlus size="xs" /> 添加模型
              </button>
            </div>
          )}
        </div>
      )}
      {adding && (
        <AddModelDialog
          modelId={m.id}
          modelName={m.name}
          onClose={() => setAdding(false)}
          onSaved={onAddSaved}
        />
      )}
    </div>
  )
}

// ── Connect AI 式「添加模型」弹窗 ───────────────────────────────────────
// 左栏供应商（贡献凭据 / 平台渠道分组 + O/A 徽标 + 模型数），右栏该供应商
// 的模型卡网格。点卡加入/移出本次新选；已在现有候选里的模型锁定不可取消。
// 保存是追加语义：现有候选原样在前，新勾选原样追加在后。

const AM_PROTOCOL_GLYPH: Record<string, { letter: string; label: string }> = {
  openai: { letter: 'O', label: 'OpenAI 兼容端点' },
  anthropic: { letter: 'A', label: 'Anthropic 原生端点' }
}

function AmProtocolBadges({ protocols }: { protocols: string[] | undefined }) {
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

function amInstancesText(v: number | null | undefined): string {
  if (v === null || v === undefined) return '池内'
  return v === 0 ? '无凭据' : fmtNum(v)
}

function amInstancesTone(v: number | null | undefined): string {
  if (v === null || v === undefined) return 'muted'
  return v === 0 ? 'warn' : 'ok'
}

function AddModelDialog({
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
  const [vendorQuery, setVendorQuery] = useState('')
  const [modelQuery, setModelQuery] = useState('')
  const [activeId, setActiveId] = useState<string | null>(null)
  const [picked, setPicked] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const existing = candsQ.data?.candidates ?? []
  const existingIds = useMemo(() => new Set(existing.map((c) => c.real_model_id)), [existing])

  const sources = sourcesQ.data?.sources ?? []
  const vq = vendorQuery.trim().toLowerCase()
  const visibleSources = useMemo(
    () =>
      sources.filter(
        (s) => !vq || `${s.name} ${s.upstream_name} ${s.id}`.toLowerCase().includes(vq)
      ),
    [sources, vq]
  )
  const contributed = visibleSources.filter((s) => s.contributed)
  const platformSources = visibleSources.filter((s) => !s.contributed)
  const active = activeId ? (sources.find((s) => s.id === activeId) ?? null) : null

  const mq = modelQuery.trim().toLowerCase()
  const visibleModels = useMemo(() => {
    if (!active) return []
    if (!mq) return active.models
    return active.models.filter(
      (m) => `${m.display_name} ${m.upstream_model_id}`.toLowerCase().includes(mq)
    )
  }, [active, mq])

  function toggleModel(m: AdminModelSourceModel) {
    // 已在现有候选集的模型不可取消（取消 = 移除候选，那是候选集编辑页的整表语义）。
    if (existingIds.has(m.real_model_id)) return
    setPicked((prev) =>
      prev.includes(m.real_model_id)
        ? prev.filter((id) => id !== m.real_model_id)
        : [...prev, m.real_model_id]
    )
  }

  async function refresh() {
    setRefreshing(true)
    try {
      await refreshRealModels()
      sourcesQ.reload()
    } catch {
      // 刷新失败不阻塞关窗，错误由下一次 sources 拉取暴露。
    } finally {
      setRefreshing(false)
    }
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
                  <span className="amd-source-row-name">{s.name}</span>
                  <AmProtocolBadges protocols={s.protocols} />
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
    const already = existingIds.has(m.real_model_id)
    const blocked = already
      ? ''
      : m.excluded_from_candidates
        ? '该模型已在候选中被排除，不能加入候选集'
        : m.status !== 'active'
          ? `状态为「${statusLabel(m.status)}」，不能加入候选集`
          : ''
    const title = already ? '已在候选集，去候选集编辑页移除' : blocked || '点击加入候选集'
    const selected = already || picked.includes(m.real_model_id)
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
            {already && <IconLock size="sm" aria-label="已在候选集" />}
            {selected && <IconCheck size="sm" />}
          </span>
          <span className="source-card-sub">{m.upstream_model_id}</span>
          <span className="source-card-meta">
            <AmProtocolBadges protocols={m.protocols} />
            <StatusBadge status={m.status} label={statusLabel(m.status)} />
            <span className="source-inst" data-tone={amInstancesTone(m.available_instances)}>
              {m.available_instances === 0 && <IconWarn size="xs" />}
              可用实例 {amInstancesText(m.available_instances)}
            </span>
          </span>
        </div>
      </div>
    )
  }

  async function submit() {
    setBusy(true)
    setError(null)
    try {
      const newCands: Candidate[] = picked.map((mid) => ({
        real_model_id: mid,
        constraint: {},
        weight: 0
      }))
      await putCandidates(modelId, [...existing, ...newCands])
      onSaved(modelId)
      onClose()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  const candsLoading = candsQ.loading && !candsQ.data
  const canSave = picked.length > 0 && !candsLoading && !busy

  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div
        className="dialog amd-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={`添加模型到 ${modelName}`}
        onClick={(e) => e.stopPropagation()}
      >
        <h2>
          添加模型到 <code>{modelName}</code>
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

        <div className="amd-split">
          <div className="amd-rail">
            {railGroup('贡献凭据', contributed)}
            {railGroup('平台渠道', platformSources)}
            {vendorQuery && contributed.length === 0 && platformSources.length === 0 && (
              <p className="muted">没有匹配的供应商。</p>
            )}
          </div>

          <div className="amd-detail">
            {!active ? (
              <EmptyState
                title="先从左侧选一个供应商"
                hint="左栏按「贡献凭据 / 平台渠道」分组；选中后这里显示它的模型卡片，点卡片即加入候选。"
              />
            ) : (
              <>
                <div className="amd-head">
                  <span className="amd-head-name">{active.name}</span>
                  <span className="amd-head-tag">{active.kind}</span>
                  <code className="amd-head-endpoint">{active.base_url || '未提供端点'}</code>
                </div>
                {visibleModels.length === 0 ? (
                  <EmptyState title="该供应商下没有模型" hint="模型清单来自 real_models；先被采集到，或换一个供应商。" />
                ) : (
                  <div className="source-grid amd-grid">{visibleModels.map(modelCard)}</div>
                )}
              </>
            )}
          </div>
        </div>

        <div className="dialog-actions amd-footer">
          <span className="amd-selected-count">已选 {picked.length} 条</span>
          <button type="button" className="btn-ghost" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button type="button" className="btn-primary" onClick={() => void submit()} disabled={!canSave}>
            {busy ? '正在加入…' : '加入候选集'}
          </button>
        </div>
        <FormError error={error} />
      </div>
    </div>
  )
}
// ── 编辑 ───────────────────────────────────────────────────────────────

export function AbstractModelDetailPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const q = useApi(() => getAbstractModel(id), [id])
  const refs = useApi(() => getReferences(id), [id])
  const model = q.data

  const [displayName, setDisplayName] = useState('')
  const [mode, setMode] = useState('strict')
  const [capabilities, setCapabilities] = useState<ModelCapabilities>({})
  const [strategy, setStrategy] = useState<string[]>([])
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [dry, setDry] = useState<DryRunResult | null>(null)
  const [dryBusy, setDryBusy] = useState(false)

  useEffect(() => {
    if (!model) return
    setDisplayName(model.display_name)
    setMode(model.capability_mode)
    setCapabilities(model.capabilities ?? {})
    setStrategy((model.strategy ?? []) as string[])
  }, [model])

  if (!model) {
    return (
      <PageFrame def={def}>
        <AsyncView state={q} loadingRows={5}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  const strategyChanged =
    JSON.stringify(strategy) !== JSON.stringify((model.strategy ?? []) as string[])
  const changed =
    displayName !== model.display_name ||
    mode !== model.capability_mode ||
    JSON.stringify(capabilities) !== JSON.stringify(model.capabilities ?? {}) ||
    strategyChanged

  function addStrategy(s: string) {
    if (s === 'round-robin') {
      if (strategy.includes('round-robin')) return
      setStrategy([...strategy.filter((x) => x !== 'round-robin'), 'round-robin'])
      return
    }
    if (strategy.includes(s)) return
    // round-robin 只能留在链末
    setStrategy([...strategy.filter((x) => x !== 'round-robin'), s, ...(strategy.includes('round-robin') ? ['round-robin'] : [])])
  }

  function move(i: number, dir: -1 | 1) {
    const j = i + dir
    if (j < 0 || j >= strategy.length) return
    const next = [...strategy]
    ;[next[i], next[j]] = [next[j], next[i]]
    setStrategy(next)
  }

  async function runDryRun() {
    if (!model) return
    setDryBusy(true)
    setError(null)
    try {
      const r = await dryRunAbstractModel(model.id, { strategy, sample_size: 200 })
      setDry(r.data)
    } catch (e) {
      setError(e)
    } finally {
      setDryBusy(false)
    }
  }

  async function save() {
    if (!model) return
    setBusy(true)
    setError(null)
    try {
      await patchAbstractModel(model.id, {
        display_name: displayName,
        capability_mode: mode,
        capabilities,
        strategy
      })
      setNotice('已保存（审计会记录旧值与新值）')
      setConfirming(false)
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  const cap = capabilities
  const setCap = (v: Partial<ModelCapabilities>) => setCapabilities({ ...cap, ...v })

  return (
    <PageFrame
      def={def}
      actions={
        <span className="inline-links">
          <Link to="/admin/abstract-models" className="icon-link"><IconBack size={ACTION_ICON} />返回列表</Link>
          <Link to={`/admin/abstract-models/${encodeURIComponent(model.id)}/candidates`}>候选集</Link>
          <Link to={`/admin/abstract-models/${encodeURIComponent(model.id)}/deprecation`}>弃用配置</Link>
        </span>
      }
    >
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <Section title="基本信息">
        <KeyValues
          items={[
            { label: '对外名', value: <code>{model.name}</code> },
            { label: '模型 ID', value: <code>{model.id}</code> },
            { label: '状态', value: <StatusBadge status={model.status} label={statusLabel(model.status)} /> },
            { label: '候选 / 可用', value: `${fmtNum(model.candidate_count)} / ${fmtNum(model.available_candidate_count)}` },
            { label: '替代目标', value: model.replacement ?? '—' }
          ]}
        />
        <p className="muted">对外名不可改：改名等于换了一个模型，正确手段是弃用 + 替代链。</p>
        <Field label="显示名" hint="仅管理端可读的别名。">
          <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </Field>
      </Section>

      <Section title="能力模式与能力声明" hint="严格模式取候选最小值（多数值由候选集推导）；过滤模式取并集并在路由前筛掉不支持的候选。">
        <Field label="能力模式" hint="切换会面向所有客户端改变语义，保存需要二次确认。">
          <select value={mode} onChange={(e) => setMode(e.target.value)}>
            <option value="strict">严格（strict）</option>
            <option value="filter">过滤（filter）</option>
          </select>
        </Field>
        <div className="field-row">
          <Field label="上下文窗口">
            <input
              type="number"
              value={cap.context_window ?? ''}
              onChange={(e) => setCap({ context_window: e.target.value === '' ? undefined : Number(e.target.value) })}
            />
          </Field>
          <Field label="最大输出">
            <input
              type="number"
              value={cap.max_output ?? ''}
              onChange={(e) => setCap({ max_output: e.target.value === '' ? undefined : Number(e.target.value) })}
            />
          </Field>
          <label className="checkbox-line">
            <input type="checkbox" checked={Boolean(cap.vision)} onChange={(e) => setCap({ vision: e.target.checked })} />
            视觉
          </label>
          <label className="checkbox-line">
            <input type="checkbox" checked={Boolean(cap.tool_calling)} onChange={(e) => setCap({ tool_calling: e.target.checked })} />
            工具调用
          </label>
          <label className="checkbox-line">
            <input type="checkbox" checked={Boolean(cap.json_mode)} onChange={(e) => setCap({ json_mode: e.target.checked })} />
            JSON mode
          </label>
        </div>
      </Section>

      <Section title="排序链" hint="默认 [expiry-first, balance]：先烧最快作废的额度，再用余额最少的账号。round-robin 只能追加在链末作平局决胜。">
        <ol className="strategy-chain">
          {strategy.map((s, i) => (
            <li key={s}>
              <code>{s}</code>
              {/* 策略链每行都重复这三个动作：只给图标，名字走 tooltip */}
              <TableActions>
                <TableAction
                  icon={<IconUp size={ACTION_ICON} />}
                  label="上移"
                  disabled={i === 0}
                  onClick={() => move(i, -1)}
                />
                <TableAction
                  icon={<IconDown size={ACTION_ICON} />}
                  label="下移"
                  disabled={i === strategy.length - 1}
                  onClick={() => move(i, 1)}
                />
                <TableAction
                  icon={<IconDelete size={ACTION_ICON} />}
                  label="从链中移除"
                  tone="danger"
                  onClick={() => setStrategy(strategy.filter((_, idx) => idx !== i))}
                />
              </TableActions>
            </li>
          ))}
          {strategy.length === 0 && <li className="muted">链为空：使用默认链 [expiry-first, balance]。</li>}
        </ol>
        <div className="field-row">
          {STRATEGIES.map(([v, l]) => (
            <button key={v} type="button" className="btn-ghost" onClick={() => addStrategy(v)} disabled={strategy.includes(v)}>
              添加 {l}
            </button>
          ))}
          <button
            type="button"
            className="btn-ghost"
            onClick={() => addStrategy('round-robin')}
            disabled={strategy.includes('round-robin') || strategy.length === 0}
          >
            加平局决胜 round-robin
          </button>
        </div>
      </Section>

      <Section title="保存前干跑" hint="用历史请求回放验证候选可达；策略配错会导致全站 no_candidate，干跑是保存路径上的强制步骤。">
        <button type="button" onClick={() => void runDryRun()} disabled={dryBusy}>
          {dryBusy ? '正在干跑…' : '用当前策略干跑（200 条样本）'}
        </button>
        {dry && (
          <>
            <div className="metric-row">
              <div className="metric"><span className="metric-value">{fmtNum(dry.reachable)}</span><span className="metric-label">可达</span></div>
              <div className="metric"><span className="metric-value">{fmtNum(dry.no_candidate)}</span><span className="metric-label">无候选</span></div>
              <div className="metric"><span className="metric-value">{fmtNum(dry.fallback_used)}</span><span className="metric-label">使用兜底</span></div>
            </div>
            {dry.failures.length > 0 && (
              <ul className="plain-list">
                {dry.failures.map((f) => (
                  <li key={f.reason}>{f.reason}：{f.count} 次</li>
                ))}
              </ul>
            )}
            <JsonDetails summary="查看干跑原始结果" value={dry} />
          </>
        )}
      </Section>

      <Section title="引用与危险写">
        <h3>被哪些组引用</h3>
        <AsyncView state={refs} loadingRows={2}>
          {(d) =>
            d.length === 0 ? (
              <p className="muted">没有任何组授权这个抽象模型；成员看不到它。</p>
            ) : (
              <ul className="plain-list">
                {d.map((g) => (
                  <li key={g.id}>
                    <Link to={`/admin/groups/${encodeURIComponent(g.id)}`}>{g.name}</Link>
                    {g.is_default ? '（默认组）' : ''} · {g.member_count} 名成员
                  </li>
                ))}
              </ul>
            )
          }
        </AsyncView>
        <button type="button" className="btn-primary" onClick={() => setConfirming(true)} disabled={!changed || busy}>
          保存抽象模型变更…
        </button>
        {!changed && <p className="muted">没有未保存的变更。</p>}
      </Section>

      <ConfirmDialog
        open={confirming}
        title={`确认修改抽象模型 ${model.name}`}
        impact={[
          mode !== model.capability_mode ? `能力模式：${model.capability_mode} → ${mode}` : '',
          strategyChanged ? `排序链：${(model.strategy ?? []).join(' → ') || '默认'} → ${strategy.join(' → ') || '默认'}` : '',
          `候选数：${model.candidate_count}（可用 ${model.available_candidate_count}）`,
          '保存后立即影响所有指向该模型的客户端'
        ].filter(Boolean)}
        irreversibility="可恢复（再改回来），但期间路由结果与能力语义已经改变，客户端可能已失败。"
        targetName={model.name}
        inputLabel="请输入对外名以确认"
        confirmLabel="确认保存"
        busy={busy}
        error={error}
        onCancel={() => setConfirming(false)}
        onConfirm={() => void save()}
      />
    </PageFrame>
  )
}
