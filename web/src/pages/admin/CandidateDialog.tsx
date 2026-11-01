// 候选集编辑器（Connect AI 式弹窗，task-8）。
//
// 由 task-7 的 AddModelDialog（只支持追加）升级而来，收编了原独立候选集
// 编辑页 /admin/abstract-models/:id/candidates 的全部职责：
//
//   - 两段布局：上部左栏供应商 / 右栏模型卡网格（点卡加入/移出，不锁定已存在
//     候选）；底部「候选 N 条 · 已改 N 处 + 取消 + 保存」。
//   - 保存 = putCandidates(id, rows) 整表替换。有移除时保存按钮先切到内联
//     二次确认态（再点一次才提交）；纯新增直接保存。
//   - 脏检测：rows 与初始 JSON 不等才允许保存；「已改 N 处」= 新增数 + 移除数
//     + 调序数（简单近似）。
//
// ## 现状（2026-11-01）：本弹窗**不再负责顺序，也不再编辑渠道范围**
//
// 顶部原来有一条「当前候选（N）」横向芯片条（序号 + ←→ 调序 + ✕ 移除 + 点
// 芯片展开 channel_scope 编辑）。它被整体删除，理由是**第二处真相**：
//
//   抽象模型列表的展开区已经用卡片展示了同一份候选与顺序，并且卡片本身就是
//   拖动排序的载体。两处都能调序，改一处而忘另一处时，管理员无法判断哪边才
//   是当前状态——而「顺序即使用序」是这套路由唯一的语义，它不能有两个编辑面。
//
// 于是这里的职责收窄成两件事：**加候选 / 撤候选**，以及提交这张表。
// 顺序去列表展开区拖动即可（松手即保存，比这里点保存按钮更快）。
//
// channel_scope（渠道范围）的编辑面也随之消失。库里仍可能存在历史形状
// （字符串 `"all"`、`{mode:'exclude',…}` 等），它们**原样保留、不被本界面
// 改写**：rows 里只做增删，constraint 字段从原候选带过来不动。要改渠道范围
// 目前只能走 API——这是这次收窄的已知代价，需要时再补一个独立入口。

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
  IconRefresh,
  IconSearch,
} from '../../icons'
import { EmptyState, FormError, LoadingBlock } from '../../components/ui'
import { fmtNum, statusLabel } from '../../utils/format'
import { logoInfo } from '../../utils/providerLogo'

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

/**
 * ProtocolDots 是导出的协议圆点组（抽象模型列表的模型卡也用它）。
 *
 * 为什么导出而不是各写一份：协议 → 字母/颜色的映射必须与候选集里**完全**
 * 一致，两处各写一份时新增一个协议只改一处、另一处静默少一枚圆点。
 */
export function ProtocolDots({ protocols }: { protocols: string[] | undefined }) {
  if (!protocols || protocols.length === 0) return null
  return (
    <span className="proto-badges">
      {protocols.map((p) => (
        // 圆形 + 协议各自的品牌色（2026-11-01 用户给定样式）：O = 青、A = 橙。
        // 为什么给协议配色而不是全用中性色：卡片右下角这一对圆点是这张卡里
        // 唯一"一眼可区分"的信号，中性色会让两枚看起来像同一个东西。
        <span
          key={p}
          className="proto-dot"
          data-proto={p}
          title={AM_PROTOCOL_GLYPH[p]?.label ?? p}
        >
          {AM_PROTOCOL_GLYPH[p]?.letter ?? p}
        </span>
      ))}
    </span>
  )
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


  useEffect(() => {
    if (!candsQ.data) return
    const list = candsQ.data.candidates ?? []
    setRows(list)
    setOriginal(list)
    setConfirmRemove(false)
  }, [candsQ.data])


  // real_model_id → 来源供应商（候选芯片标题下要显示「来自哪家」）。口径与
  // modelFacts 相同：目录里首见的来源获胜；目录里找不到（模型已被移除）时
  // 返回 undefined，芯片显示「未知来源」——不猜、不冒充。
  const sourceOfReal = useMemo(() => {
    const idx = new Map<string, AdminModelSource>()
    for (const s of sourcesQ.data?.sources ?? []) {
      for (const m of s.models) if (!idx.has(m.real_model_id)) idx.set(m.real_model_id, s)
    }
    return idx
  }, [sourcesQ.data])

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
                  <ProtocolDots protocols={s.protocols} />
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
    const src = sourceOfReal.get(m.real_model_id)
    // 管理细节收进 title：卡片要保持「一眼可扫」的形态，塞满文字就失去意义。
    // 但**不能丢**——不可加入的原因（已排除/状态异常）必须优先占位，否则
    // 管理员只看到一张灰卡却不知道为什么。
    const detail = [
      `模型 ${m.upstream_model_id}`,
      `状态 ${statusLabel(m.status)}`,
      `可用实例 ${instancesText(m.available_instances)}`,
      `上下文 ${fmtNum(m.context_window)}`,
      m.channel ? `渠道 ${m.channel}` : '',
      m.excluded_from_candidates ? '已排除出候选' : ''
    ].filter(Boolean).join(' · ')
    const title = blocked ? `${blocked}\n${detail}` : `${selected ? '点击移出候选集' : '点击加入候选集'}\n${detail}`
    return (
      <div className="source-card model-card" key={m.real_model_id} data-selected={selected} data-blocked={blocked !== ''}>
        <div
          className="source-card-hit model-card-hit"
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
          {/* 卡片形态（2026-11-01 用户给定样式）：
              ① 模型名居中；
              ② 一条分隔线（用填充色阶画，不画边框）；
              ③ 底部一行：左「供应商 logo + 供应商名」，右「协议圆点」。
              选中态用主色软底 + 右上角 ✓（全站不用边框画状态）。
              状态/实例/上下文这些管理细节收进 title 提示——卡片本身要能
              一眼扫过几十张，塞满文字就失去卡片的意义了。 */}
          {selected && (
            <span className="model-card-check" aria-hidden>
              <IconCheck size="sm" />
            </span>
          )}
          <span className="model-card-name" title={m.upstream_model_id}>
            {m.display_name}
          </span>
          <span className="model-card-divider" />
          <span className="model-card-foot">
            <span className="model-card-provider">
              {(() => {
                const logo = logoInfo(src?.provider_id ?? '', src?.name ?? m.upstream_model_id)
                return (
                  <img
                    className="model-card-logo"
                    src={logo.url}
                    data-fallback={logo.fallback || undefined}
                    alt=""
                    loading="lazy"
                    draggable={false}
                  />
                )
              })()}
              <span className="model-card-provider-name">{src?.name ?? '未知来源'}</span>
            </span>
            <ProtocolDots protocols={m.protocols} />
          </span>
        </div>
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

        {/* 说明：这里原来是「当前候选（N）」芯片条（含顺序说明与拖动排序）。
            2026-11-01 用户要求去掉——抽象模型列表的展开区已经用卡片展示了同一
            份候选与顺序，弹窗里再来一排就成了**第二处真相**：两处都能调序，
            改一处忘另一处时管理员无法判断哪边是当前状态。
            候选的增删仍由下部模型卡片负责（点卡片加入/移出），顺序在展开区排。 */}

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
            {/* 候选条数必须留在这里：顶部的「当前候选」芯片条去掉后（2026-11-01
                用户要求），这是弹窗里唯一还在说「现在一共几条候选」的地方——
                少了它，管理员移出一条后无法确认到底还剩几条。 */}
            候选 {rows.length} 条 · 已改 {changedCount} 处
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