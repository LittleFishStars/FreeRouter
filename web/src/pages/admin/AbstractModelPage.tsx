// 抽象模型：列表与编辑（docs/ui/03-admin.md 第 7.1–7.2 节）。
//
// 对外名创建后不可改：契约的稳定性来自名字，改名等于把旧名字的所有客户端
// 一次性打断；正确手段是弃用 + 替代链。
//
// 能力模式、排序链与能力声明都属于「改抽象模型」危险写：保存前必须走
// 统一二次确认框。排序策略只有四种 + 链末 round-robin 平局决胜。

import { useEffect, useRef, useState } from 'react'
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
} from '../../api/endpoints'
import type {
  AdminAbstractModel,
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
  IconPlus,
  IconRefresh,
  IconUp,
} from '../../icons'
import { CandidateDialog } from './CandidateDialog'
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
  // 弹窗保存后的重拉信号（见下面 effect 的依赖说明）。
  const [refreshSeq, setRefreshSeq] = useState(0)

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
  // refreshSeq 是弹窗保存后的重拉信号：保存时删了缓存，但 expandedId 没变，
  // 只依赖 [expandedId] 的 effect 不会重跑——展开中的行会一直停在「展开候选…」，
  // 必须收起再展开才刷新（用户报的 bug）。把 seq 加进依赖后，删缓存与重拉在
  // 同一次提交里发生。
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
  }, [expandedId, refreshSeq]) // eslint-disable-line react-hooks/exhaustive-deps

  function toggle(id: string) {
    setExpandedId((prev) => (prev === id ? null : id))
  }

  // 弹窗保存成功后：刷新列表，删该模型的展开缓存并 bump refreshSeq，让
  // 正在展开的那一行立即重拉芯片条（只删缓存不 bump 的话，effect 依赖的
  // expandedId 没变，不会重跑）。
  function handleCandidateSaved(id: string) {
    q.reload()
    setDetails((d) => {
      const next = { ...d }
      delete next[id]
      return next
    })
    setRefreshSeq((n) => n + 1)
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
                  onAddSaved={handleCandidateSaved}
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
  const [open, setOpen] = useState(false)

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
          {/* 行头不放「候选集」按钮：展开区的「+ 添加模型」与候选芯片都打开
              同一个编辑弹窗，行头只留低频的「弃用」入口。 */}
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
                    <button key={c.real_model_id} type="button" className="am-chip" onClick={() => setOpen(true)}>
                      <span className="am-chip-name">{name}</span>
                      {supported && <IconCheck size="xs" />}
                    </button>
                  )
                })
              )}
              <button type="button" className="am-add-chip" onClick={() => setOpen(true)}>
                <IconPlus size="xs" /> 添加模型
              </button>
            </div>
          )}
        </div>
      )}
      {open && (
        <CandidateDialog
          modelId={m.id}
          modelName={m.name}
          onClose={() => setOpen(false)}
          onSaved={onAddSaved}
        />
      )}
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
  const [candOpen, setCandOpen] = useState(false)

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
          <button type="button" className="am-op-link" onClick={() => setCandOpen(true)}>候选集</button>
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
      {candOpen && (
        <CandidateDialog
          modelId={model.id}
          modelName={model.name}
          onClose={() => setCandOpen(false)}
          onSaved={() => q.reload()}
        />
      )}
    </PageFrame>
  )
}
