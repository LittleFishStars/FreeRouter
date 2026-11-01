// 抽象模型：列表与编辑（docs/ui/03-admin.md 第 7.1–7.2 节）。
//
// 对外名创建后不可改：契约的稳定性来自名字，改名等于把旧名字的所有客户端
// 一次性打断；正确手段是弃用 + 替代链。
//
// 能力模式与能力声明属于「改抽象模型」危险写：保存前必须走统一二次确认框。
//
// 2026-11-01 定案：**排序链已退役**。候选的使用顺序就是管理员在候选集编辑器
// 里排列的顺序（PUT /candidates 的数组顺序），仅当前一个不可用时才用下一个。
// 因此本页不再有排序策略选择器；位次与不可用状态都挪到候选集编辑弹窗里。

import { useEffect, useRef, useState } from 'react'
import { useDragReorder, moveItem } from '../../hooks/useDragReorder'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON, ToolButton } from '../../components/IconButtons'
import {
  checkAbstractModelConnectivity,
  createAbstractModel,
  dryRunAbstractModel,
  getAbstractModel,
  getCandidates,
  getModelSources,
  getReferences,
  listAbstractModels,
  patchAbstractModel,
  putCandidates,
} from '../../api/endpoints'
import type {
  AdminAbstractModel,
  AdminModelSource,
  AdminModelSourceModel,
  Candidate,
  ConnectivityResult,
  DryRunResult,
  ModelCapabilities
} from '../../api/types'
import {
  IconBack,
  IconCheck,
  IconChevronDown,
  IconPlus,
  IconRefresh,
  IconTest,
  IconWarn,
} from '../../icons'
import { CandidateDialog, ProtocolDots } from './CandidateDialog'
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
import { logoInfo } from '../../utils/providerLogo'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

const STATUS_OPTIONS = [
  ['', '全部状态'],
  ['active', '可用'],
  ['deprecated', '已弃用'],
  ['disabled', '已停用']
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
  // real_model_id → 来源（供应商）。卡片要显示「供应商 logo + 名字 + 协议圆点」，
  // 而模型事实里只有模型自身字段——供应商是模型**所属**的来源，必须另存一份。
  // 口径与 modelFacts 一致（首见来源获胜）：两处都按同一顺序扫描，否则同一个
  // 模型在卡片上显示的供应商会与候选集弹窗里不一致。
  const modelSourceOf = useRef(new Map<string, AdminModelSource>())
  const factsReady = sourcesQ.data !== undefined
  if (factsReady) {
    const idx = modelFacts.current
    const src = modelSourceOf.current
    for (const s of sourcesQ.data?.sources ?? []) {
      for (const m of s.models) {
        if (!idx.has(m.real_model_id)) idx.set(m.real_model_id, m)
        if (!src.has(m.real_model_id)) src.set(m.real_model_id, s)
      }
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
        capability_mode: mode
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
                  sourceOf={modelSourceOf.current}
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
  sourceOf,
  onAddSaved
}: {
  m: AdminAbstractModel
  expanded: boolean
  onToggle: () => void
  detail: ExpDetail | undefined
  facts: Map<string, AdminModelSourceModel>
  sourceOf: Map<string, AdminModelSource>
  onAddSaved: (id: string) => void
}) {
  const editTo = `/admin/abstract-models/${encodeURIComponent(m.id)}`
  const [open, setOpen] = useState(false)

  // 展开区芯片条的拖动排序（2026-11-01）：候选的使用顺序就是这里的排列，
  // 所以直接在芯片条上拖，不用进弹窗。松手才保存（拖动中只是预览序）。
  const [chipRows, setChipRows] = useState<Candidate[] | null>(null)
  const [savingOrder, setSavingOrder] = useState(false)
  const [orderNotice, setOrderNotice] = useState<string | null>(null)
  // 「刚拖完」标记：pointerup（hook onEnd）与浏览器合成的 click 是两个事件，
  // 但 click 里读 state 拿到的可能是批处理前的旧值——ref 的写入在两次事件
  // 之间同步可见，这才是可靠的守卫（state 守卫实测会漏：拖完仍会打开弹窗）。
  const justDragged = useRef(false)
  // 预览序的初始值来自当前渲染的声明序（detail.candidates）——必须在这里
  // 拿到，不能等 setChipRows 自己从 null 长出来：第一帧 preview 时 chipRows
  // 还是 null，`cur ? … : cur` 会原样返回 null，拖动看起来毫无反应
  //（2026-11-01 实测踩到这个空转）。
  // 拖动起始快照：每次 preview 都从它出发重排，而不是在当前预览序上再挪一次。
  //
  // 为什么必须从快照出发：from/to 是**相对拖动开始时的索引空间**（hook 冻结
  // 了几何快照，fromIndex 也是那时算的）。若在已重排过的列表上再 apply 一次
  // moveItem，位移会被叠加两次，顺序在两三个状态间来回跳（实测：x 每走一步
  // 顺序就反转一次）。
  const dragOrigin = useRef<Candidate[] | null>(null)
  const drag = useDragReorder({
    onPreview: (from, to) => {
      if (!dragOrigin.current) {
        dragOrigin.current = detail?.state === 'ready' ? detail.candidates : []
      }
      setChipRows(moveItem(dragOrigin.current, from, to))
    },
    onEnd: (moved) => {
      dragOrigin.current = null
      if (!moved) {
        setChipRows(null)
        return
      }
      justDragged.current = true
      // click 在 pointerup 之后同一轮宏任务里派发；用宏任务边界复位，
      // 保证「拖完的那次 click」被吞、「下一次真点击」放行。
      setTimeout(() => { justDragged.current = false }, 0)
      void saveOrder()
    }
  })

  // 连通性检验（2026-10-31）：逐候选实时探测出站路径，结果芯片条挂在行头下
  const [conn, setConn] = useState<ConnectivityResult | null>(null)
  const [connBusy, setConnBusy] = useState(false)
  const [connError, setConnError] = useState<string | null>(null)

  // 把预览序写回（整表替换；约束与不可用标记由后端透传语义决定，这里只换顺序）。
  // chipRows 为 null 时说明没有拖动过，不该触发。
  async function saveOrder() {
    if (!chipRows || savingOrder) return
    setSavingOrder(true)
    setOrderNotice(null)
    try {
      await putCandidates(m.id, chipRows.map((r) => ({
        real_model_id: r.real_model_id,
        constraint: r.constraint,
        weight: 0
      })))
      setChipRows(null)
      onAddSaved(m.id) // 复用候选保存后的刷新信号（列表计数 + 展开缓存重拉）
    } catch (e) {
      setChipRows(null)
      setOrderNotice(e instanceof Error ? `顺序保存失败：${e.message}` : '顺序保存失败')
    } finally {
      setSavingOrder(false)
    }
  }

  async function runConnectivity() {
    setConnBusy(true)
    setConnError(null)
    try {
      setConn((await checkAbstractModelConnectivity(m.id)).data)
    } catch (e) {
      setConnError(e instanceof Error ? e.message : '检验失败')
    } finally {
      setConnBusy(false)
    }
  }

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
        {/* 行头原显示排序链；策略链退役后改为显示「不可用候选数」——
            它才是管理员在这个位置真正要看的信号（有几条在等探测恢复）。 */}
        <span className="am-row-chain">
          {m.unavailable_candidate_count ? `${fmtNum(m.unavailable_candidate_count)} 条不可用` : ''}
        </span>
        <span className="am-row-ops" onClick={(e) => e.stopPropagation()}>
          {/* 行头不放「候选集」按钮：展开区的「+ 添加模型」与候选芯片都打开
              同一个编辑弹窗，行头只留低频的「弃用」入口。 */}
          <button
            type="button"
            className="btn-ghost"
            disabled={connBusy}
            title="实际请求一次（max_tokens=1 的 ping），打穿候选→凭据→出站→响应全链路；消耗极少量额度，不计入用量统计"
            onClick={() => void runConnectivity()}
          >
            <IconTest size="xs" />
            {connBusy ? '请求中…' : '检验连通性'}
          </button>
          <Link to={`${editTo}/deprecation`}>弃用</Link>
          <button type="button" className="am-expand" data-open={expanded} onClick={onToggle} aria-expanded={expanded} aria-label={expanded ? '收起候选' : '展开候选'}>
            <IconChevronDown size="sm" />
          </button>
        </span>
      </div>
      {connError && <div className="am-explain" data-tone="warn">检验失败：{connError}</div>}
      {orderNotice && <div className="am-explain" data-tone="warn">{orderNotice}</div>}
      {conn && (
        <div className="am-conn">
          <span className="am-conn-sum" data-tone={conn.ok ? 'ok' : 'warn'}>
            {conn.ok
              ? `连通 ✓ ${conn.latency_ms}ms${conn.tokens != null ? ` · ${conn.tokens} tok` : ''}`
              : '不通'}
          </span>
          {conn.attempts.map((a, i) => (
            <span
              key={i}
              className="am-conn-item"
              data-ok={a.ok}
              title={`${a.display}：${a.reason ?? (a.ok ? '成功' : '失败')}`}
            >
              {a.ok ? <IconCheck size="xs" /> : <IconWarn size="xs" />}
              <span className="am-conn-model">{a.display}</span>
              {a.ok && a.latency_ms != null && <span className="am-conn-lat">{a.latency_ms}ms</span>}
              {!a.ok && a.reason && <span className="am-conn-reason">{a.reason}</span>}
            </span>
          ))}
          {!conn.ok && conn.trace && (
            <details className="am-conn-trace">
              <summary className="muted">路由诊断</summary>
              <code className="am-conn-trace-body">{conn.trace}</code>
            </details>
          )}
          <button type="button" className="btn-ghost am-conn-close" onClick={() => setConn(null)}>收起</button>
        </div>
      )}
      {expanded && (
        <div className="am-expand-body">
          {detail === undefined && <div className="am-explain">展开候选…</div>}
          {detail?.state === 'loading' && <div className="am-loading"><LoadingBlock rows={1} /></div>}
          {detail?.state === 'error' && <div className="am-error muted">候选加载失败，请重试。</div>}
          {detail?.state === 'ready' && (() => {
            // 预览序：拖动中用 chipRows（拖动实时让位），否则用加载到的声明序。
            const rows = chipRows ?? detail.candidates
            const dragProps = rows.length > 1
              ? { ref: drag.containerRef, 'data-drag-active': drag.draggingKey !== null || undefined }
              : {}
            return (
              <>
                {rows.length > 1 && (
                  <p className="am-chip-hint" data-saving={savingOrder || undefined}>
                    {savingOrder
                      ? '正在保存顺序…'
                      : '按住拖动可调整使用顺序（仅当前一个不可用时才用下一个）'}
                  </p>
                )}
                <div className="am-card-row" {...dragProps}>
                  {rows.length === 0 ? (
                    <span className="muted am-empty">尚未配置候选。</span>
                  ) : (
                    rows.map((c, i) => {
                      const fact = facts.get(c.real_model_id)
                      const src = sourceOf.get(c.real_model_id)
                      const name = fact?.display_name ?? c.real_model_id
                      const supported = c.available_instances !== 0 && c.available_instances !== undefined
                      // 单条候选没有排序意义，退回纯点击；多条时挂拖动属性。
                      // 显式标类型：写成 `: {}` 会让联合类型退化成 never，
                      // 下面展开属性时全部报索引错误（TS7053）。
                      const dp: ReturnType<typeof drag.itemProps> | null =
                        rows.length > 1 ? drag.itemProps(c.real_model_id, i) : null
                      const logo = logoInfo(src?.provider_id ?? '', src?.name ?? name)
                      // 「为什么点不开/为什么打不通」统一进 title：卡面只留三段
                      // （名字 / 分隔线 / 供应商+协议），与候选集弹窗的模型卡同一形态。
                      const tip = [
                        c.unavailable ? `已判定不可用${c.unavailable_reason ? `（${c.unavailable_reason}）` : ''}` : '',
                        supported ? '可用' : '无可用凭据',
                        fact?.upstream_model_id ? `模型 ${fact.upstream_model_id}` : '',
                        '点击打开候选集'
                      ].filter(Boolean).join(' · ')
                      return (
                        <button
                          key={c.real_model_id}
                          type="button"
                          className="am-card model-card"
                          data-unavailable={c.unavailable || undefined}
                          {...dp}
                          title={tip}
                          onClick={() => {
                            // 拖动结束的松手也会合成一次 click；刚拖完就不打开编辑器。
                            // （state 守卫不可靠——React 批处理让 click 读到旧值。）
                            if (justDragged.current) return
                            setOpen(true)
                          }}
                        >
                          {/* 位次：手动排序下的使用位次（第 N 个尝试）。胶囊时代
                              靠排列位置隐含，卡片有了固定形态后必须显式标出来。 */}
                          <span className="am-card-idx">{i + 1}</span>
                          {c.unavailable && (
                            <span className="am-card-bad" aria-hidden>
                              <IconWarn size="xs" />
                            </span>
                          )}
                          <span className="model-card-name">{name}</span>
                          <span className="model-card-divider" />
                          <span className="model-card-foot">
                            <span className="model-card-provider">
                              <img
                                className="model-card-logo"
                                src={logo.url}
                                data-fallback={logo.fallback || undefined}
                                alt=""
                                loading="lazy"
                                draggable={false}
                              />
                              <span className="model-card-provider-name">
                                {(src?.name) || '未知来源'}
                              </span>
                            </span>
                            <ProtocolDots protocols={fact?.protocols} />
                          </span>
                        </button>
                      )
                    })
                  )}
                  <button type="button" className="am-add-chip" onClick={() => setOpen(true)}>
                    <IconPlus size="xs" /> 添加模型
                  </button>
                </div>
              </>
            )
          })()}
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
  }, [model])

  if (!model) {
    return (
      <PageFrame def={def}>
        <AsyncView state={q} loadingRows={5}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  const changed =
    displayName !== model.display_name ||
    mode !== model.capability_mode ||
    JSON.stringify(capabilities) !== JSON.stringify(model.capabilities ?? {})

  async function runDryRun() {
    if (!model) return
    setDryBusy(true)
    setError(null)
    try {
      // 策略链已退役（2026-11-01）：干跑载荷只剩采样量。
      const r = await dryRunAbstractModel(model.id, { sample_size: 200 })
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
        capabilities
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
      {model.name === 'auto' && (
        <Banner tone="info" title="此模型由系统自动维护">
          候选集每分钟与全池可路由模型对齐，手工编辑会被覆盖；它已默认授予每个组，
          在组编辑里撤回后不会被自动加回。要下线请「停用」——删除后下一轮同步会重建。
        </Banner>
      )}

      <Section title="基本信息">
        <KeyValues
          items={[
            { label: '对外名', value: <code>{model.name}</code> },
            { label: '模型 ID', value: <code>{model.id}</code> },
            { label: '状态', value: <StatusBadge status={model.status} label={statusLabel(model.status)} /> },
            { label: '候选 / 可用', value: `${fmtNum(model.candidate_count)} / ${fmtNum(model.available_candidate_count)}` },
            {
              label: '不可用',
              value: model.unavailable_candidate_count
                ? `${fmtNum(model.unavailable_candidate_count)} 条（等待自动探测恢复）`
                : '0'
            },
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

      <Section
        title="候选使用顺序"
        hint="顺序就是候选集编辑器里排列的顺序（手动排序，2026-11-01 定案）：仅当前一个不可用时才用下一个，不可用的排到末尾但不是被剔除。改顺序请到「候选集」。"
      >
        <p className="muted">
          排序链（expiry-first / balance / speed / price / round-robin）已退役：
          自动排序的输入（余额/到期/延迟）都是近似值，同一份配置在不同测量状态下
          会给出不同顺序，故障反而更难复现。手动序是唯一「配置即事实」的表达。
        </p>
        <button type="button" className="btn-ghost" onClick={() => setCandOpen(true)}>
          编辑候选集（顺序 / 增删 / 渠道范围）
        </button>
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
