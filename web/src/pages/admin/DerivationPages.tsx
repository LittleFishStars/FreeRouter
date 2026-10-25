// 抽象模型的派生页：候选集与弃用（docs/ui/03-admin.md 7.3 / 7.4）。
//
// 候选集是**声明式**的：只声明真实模型 × 渠道范围 × 约束，不绑定凭据。
// 运行时才展开成「候选 × 可用凭据实例」再排序——所以界面必须显示
// available_instances，否则「配了但永远选不中」会成为静默故障。
//
// 弃用是单向状态：目标必须是 active、能力逐项 ≥ 原模型、环检测兜底；
// 不提供「取消弃用」（要恢复只能新建一个同名的 active 模型）。

import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Box, List, ListItemButton, Typography } from '@mui/material'
import {
  getAbstractModel,
  getCandidates,
  getChain,
  getModelSources,
  listAbstractModels,
  patchAbstractModel,
  putCandidates,
  putDeprecation
} from '../../api/endpoints'
import type {
  AdminAbstractModel,
  AdminModelSource,
  AdminModelSourceModel,
  Candidate,
  ChainPreview,
  DeprecationInput
} from '../../api/types'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON } from '../../components/IconButtons'
import { IconBack, IconCheck, IconExternal, IconSearch, IconWarn } from '../../icons'
import ConfirmDialog from '../../components/ConfirmDialog'
import {
  AsyncView,
  Badge,
  Banner,
  EmptyState,
  Field,
  FormError,
  JsonDetails,
  KeyValues,
  Section,
  StatusBadge
} from '../../components/ui'
import { fmtNum, fmtTime, statusLabel } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

// ── 候选集 ─────────────────────────────────────────────────────────────

/** 协议事实徽标的字面（契约 §1.2：只有 openai / anthropic 两个取值）。 */
const PROTOCOL_GLYPH: Record<string, { letter: string; label: string }> = {
  openai: { letter: 'O', label: 'OpenAI 兼容端点' },
  anthropic: { letter: 'A', label: 'Anthropic 原生端点' }
}

const KIND_LABEL: Record<string, string> = {
  relay: '反代',
  vendor: '供应商',
  selfhosted: '自建'
}

/**
 * 徽标的语义边界（契约 §1.2）：它表达「该供应商提供哪种协议的端点」这一事实，
 * 而不是「可以用那种协议调它」——本站出站链路当前只实现 OpenAI 兼容。
 */
const PROTOCOL_BOUNDARY =
  '协议徽标只表达「该供应商提供哪种协议的端点」这一事实：本站出站链路当前只实现 OpenAI 兼容，A 徽标是「供应商具备、本站未接入」。'

function ProtocolBadges({ protocols }: { protocols: string[] }) {
  if (protocols.length === 0) return null
  return (
    <span className="proto-badges">
      {protocols.map((p) => (
        <span key={p} className="proto-badge" title={PROTOCOL_GLYPH[p]?.label ?? p}>
          {PROTOCOL_GLYPH[p]?.letter ?? p}
        </span>
      ))}
    </span>
  )
}

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
 * 合并写法（契约 §3.2）：只重写 channel_scope，constraint 其余键一个不动；
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

/**
 * 左栏供应商名后的地区后缀。
 *
 * 目录展示名有时已经包含地区（如 `Aliyun Dashscope (CN)`），再拼一次就成了
 * 「Aliyun Dashscope (CN)（CN）」。所以只在名字里没出现该后缀时才补：判断用
 * 大写包含，因为目录里的后缀写法不统一（`(CN)` / `CN` / `Global` 混着来），
 * 而 region 枚举值恒为小写 cn/intl/global。
 */
function regionSuffix(s: AdminModelSource): string {
  if (!s.region) return ''
  const tag = s.region.toUpperCase()
  return s.name.toUpperCase().includes(`(${tag})`) || s.name.toUpperCase() === tag ? '' : `（${tag}）`
}

/** 可用实例口径（契约 §1.1）：null = 池内（数量由账号池决定），0 = 无凭据支撑。 */
function instancesText(v: number | null | undefined): string {
  if (v === null || v === undefined) return '池内'
  if (v === 0) return '无凭据'
  return fmtNum(v)
}

function instancesTone(v: number | null | undefined): string {
  if (v === null || v === undefined) return 'muted'
  return v === 0 ? 'warn' : 'ok'
}

export function CandidatesPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const modelQ = useApi(() => getAbstractModel(id), [id])
  const candidatesQ = useApi(() => getCandidates(id), [id])
  // 「可用实例」的唯一事实源：候选集详情里的 available_instances 恒为 0（桥的静态值）
  const sourcesQ = useApi(() => getModelSources(), [])

  const [rows, setRows] = useState<Candidate[]>([])
  const [original, setOriginal] = useState<Candidate[]>([])
  const [pickId, setPickId] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    if (!candidatesQ.data) return
    const list = candidatesQ.data.candidates ?? []
    setRows(list)
    setOriginal(list)
  }, [candidatesQ.data])

  const model = modelQ.data
  const changed = JSON.stringify(rows) !== JSON.stringify(original)

  const sources = sourcesQ.data?.sources ?? []
  const channels = sourcesQ.data?.channels ?? []
  const keyword = search.trim().toLowerCase()

  /** 模型 → 事实的索引：候选行的「可用实例」一律取新接口的数字。 */
  const modelFacts = new Map<string, AdminModelSourceModel>()
  for (const s of sources) {
    for (const m of s.models) if (!modelFacts.has(m.real_model_id)) modelFacts.set(m.real_model_id, m)
  }
  const pickedRow = new Map(rows.map((r, i) => [r.real_model_id, i]))
  const factsReady = sourcesQ.data !== undefined
  const noCredential = factsReady ? rows.filter((r) => modelFacts.get(r.real_model_id)?.available_instances === 0) : []
  const foreign = factsReady ? rows.filter((r) => !modelFacts.has(r.real_model_id)) : []
  // 「可用候选」与卡片同源：GET /abstract-models 的 available_candidate_count
  // 在桥里是静态 0（admin_bridge_models.go:550 的注释），而同一页的卡片已用新接口
  // 给出了真实数字。同屏两个相反的数字比只给一个 0 更坏，所以摘要改由事实推导：
  // 有凭据支撑的候选数（null = 池内，也算支撑）。foreign 的候选没有事实可用，
  // 不计入（宁可少报，不凭空宣称可用）。
  const usableCount = factsReady
    ? rows.filter((r) => {
        const f = modelFacts.get(r.real_model_id)
        return f !== undefined && f.available_instances !== 0
      }).length
    : null

  const contributed = sources.filter((s) => s.contributed)
  const platform = sources.filter((s) => !s.contributed)
  const active = sources.find((s) => s.id === pickId) ?? null
  const visibleModels = active === null ? [] : modelsFor(active)

  /** 只有供应商名 / 上游名 / provider_id 命中，就算这个来源整体命中。 */
  function sourceHit(s: AdminModelSource): boolean {
    return (
      s.name.toLowerCase().includes(keyword) ||
      s.upstream_name.toLowerCase().includes(keyword) ||
      s.provider_id.toLowerCase().includes(keyword)
    )
  }

  function modelHit(m: AdminModelSourceModel): boolean {
    return m.display_name.toLowerCase().includes(keyword) || m.upstream_model_id.toLowerCase().includes(keyword)
  }

  /** 搜索同时过滤供应商与模型；模型名命中时该来源仍列出，右栏只显示命中的模型。 */
  function visibleSources(list: AdminModelSource[]): AdminModelSource[] {
    if (!keyword) return list
    return list.filter((s) => sourceHit(s) || s.models.some(modelHit))
  }

  function modelsFor(s: AdminModelSource): AdminModelSourceModel[] {
    if (!keyword || sourceHit(s)) return s.models
    return s.models.filter(modelHit)
  }

  function update(i: number, patch: Partial<Candidate>) {
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))
  }

  /** 点卡片 = 加入 / 移出候选集；新加入的按点击顺序追在载入顺序之后。 */
  function toggleModel(m: AdminModelSourceModel) {
    const i = pickedRow.get(m.real_model_id)
    if (i !== undefined) {
      setRows((rs) => rs.filter((_, idx) => idx !== i))
      return
    }
    setRows((rs) => [...rs, { real_model_id: m.real_model_id, constraint: {}, weight: 100 }])
  }

  function toggleChannel(i: number, channel: string) {
    const current = includeChannels(rows[i].constraint)
    const next = current.includes(channel) ? current.filter((c) => c !== channel) : [...current, channel]
    update(i, { constraint: withChannels(rows[i].constraint, next) })
  }

  function removeRow(i: number) {
    setRows(rows.filter((_, idx) => idx !== i))
  }

  async function save() {
    setBusy(true)
    setError(null)
    try {
      // available_instances 是响应字段，请求里不带
      const payload = rows.map((r) => ({
        real_model_id: r.real_model_id,
        constraint: r.constraint,
        weight: r.weight
      }))
      await putCandidates(id, payload)
      setNotice('候选集已整体替换')
      setConfirming(false)
      candidatesQ.reload()
      sourcesQ.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  /** 左栏一组：组内顺序就是接口给的顺序（后端已按 contributed / name 排好）。 */
  function railGroup(title: string, list: AdminModelSource[]) {
    return (
      <div className="source-group">
        <p className="source-group-title">
          {title}
          <span className="source-group-count">{list.length}</span>
        </p>
        {list.length === 0 ? (
          <p className="muted source-group-empty">（无）</p>
        ) : (
          <List dense disablePadding sx={{ '& .MuiListItemButton-root': { borderRadius: 2, px: 1, py: 0.5 } }}>
            {list.map((s) => (
              <ListItemButton
                key={s.id}
                selected={s.id === active?.id}
                onClick={() => setPickId(s.id)}
                className="source-row"
                data-off={!s.enabled}
                sx={{
                  '&.Mui-selected': { bgcolor: 'var(--fr-primary-soft)' },
                  '&.Mui-selected:hover': { bgcolor: 'var(--fr-primary-soft)' }
                }}
              >
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.75, width: '100%', minWidth: 0 }}>
                  <Typography
                    variant="body2"
                    sx={{ fontWeight: 600, flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                  >
                    {s.name}
                    {regionSuffix(s)}
                    {!s.enabled && <span className="source-off-tag">已停用</span>}
                  </Typography>
                  <ProtocolBadges protocols={s.protocols} />
                  <span className="source-count" title={`${s.model_count} 个模型`}>
                    {s.model_count}
                  </span>
                </Box>
              </ListItemButton>
            ))}
          </List>
        )}
      </div>
    )
  }

  /** 右栏一张模型卡：卡头是选择区，选中后就地展开参数行，两者是兄弟节点。 */
  function modelCard(m: AdminModelSourceModel) {
    const idx = pickedRow.get(m.real_model_id)
    const selected = idx !== undefined
    const blocked = m.excluded_from_candidates
      ? '已从候选中排除，不能加入候选集'
      : m.status !== 'active'
        ? `状态为「${statusLabel(m.status)}」，不能加入候选集`
        : ''
    const chipList = idx === undefined ? [] : Array.from(new Set([...channels, ...includeChannels(rows[idx].constraint)]))
    return (
      <div className="source-card" key={m.real_model_id} data-selected={selected} data-blocked={blocked !== ''}>
        <div
          className="source-card-hit"
          role="checkbox"
          aria-checked={selected}
          aria-disabled={blocked !== ''}
          tabIndex={blocked === '' ? 0 : -1}
          title={blocked || (selected ? '点击移出候选集' : '点击加入候选集')}
          onClick={
            blocked === ''
              ? () => toggleModel(m)
              : undefined
          }
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
        {selected && idx !== undefined && (
          <div className="source-params">
            <label className="source-param">
              <span className="source-param-label">权重</span>
              <input
                type="number"
                min={0}
                value={rows[idx].weight}
                onChange={(e) => update(idx, { weight: Number(e.target.value) })}
              />
            </label>
            <div className="source-param source-param-wide">
              <span className="source-param-label">渠道范围</span>
              <div className="source-chips">
                {chipList.length === 0 && <span className="muted">池内还没有任何渠道</span>}
                {chipList.map((ch) => {
                  const on = includeChannels(rows[idx].constraint).includes(ch)
                  return (
                    <button
                      key={ch}
                      type="button"
                      className="source-chip"
                      data-on={on}
                      aria-pressed={on}
                      onClick={() => toggleChannel(idx, ch)}
                    >
                      {on && <IconCheck size="xs" />}
                      {ch}
                    </button>
                  )
                })}
              </div>
              <span className="source-param-hint">
                {/* exclude / 历史字符串形状下，"当前"不是渠道集合而是别的判据，
                    先说清当前值再说操作规则，否则第一行会被读成"当前=全部渠道"。 */}
                {hasForeignScope(rows[idx].constraint)
                  ? '当前是可编辑范围之外的自定义约束（见下）；本界面只编辑渠道范围。'
                  : `${scopeSummary(rows[idx].constraint)}；一个都不选 = 全部渠道（不写 channel_scope）`}
              </span>
              {hasForeignScope(rows[idx].constraint) && (
                <span className="source-param-hint">
                  已有自定义约束（本界面只编辑渠道范围）：一动芯片就按 include 形状重写，其它约束键原样保留。
                </span>
              )}
            </div>
            <button type="button" className="btn-ghost" onClick={() => removeRow(idx)}>
              移出候选集
            </button>
          </div>
        )}
      </div>
    )
  }

  if (!model) {
    return (
      <PageFrame def={def}>
        <AsyncView state={modelQ} loadingRows={4}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  const railUnavailable = sourcesQ.error !== undefined || sourcesQ.data === undefined

  return (
    <PageFrame
      def={def}
      actions={
        <span className="inline-links">
          <Link to={`/admin/abstract-models/${encodeURIComponent(id)}`} className="icon-link">
            <IconBack size={ACTION_ICON} />
            返回编辑
          </Link>
          <Link
            to={`/admin/abstract-models/${encodeURIComponent(id)}/deprecation`}
            className="icon-link"
          >
            <IconWarn size={ACTION_ICON} />
            弃用配置
          </Link>
        </span>
      }
    >
      <Banner tone="muted">
        候选集只声明「真实模型 × 渠道范围 × 约束」，不绑定凭据；真正的展开在运行时。
        因此每张卡片都显示可用实例数：0 表示这条模型当前没有任何凭据支撑。
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <AsyncView state={candidatesQ} loadingRows={4}>
        {() => (
          <Section
            title={`候选集（${rows.length} 行）`}
            hint="点模型卡片加入 / 移出候选集；选中后就地展开权重与渠道范围。权重只在排序链显式含 round-robin 时生效；默认链不使用权重。"
          >
            {railUnavailable ? (
              <>
                <AsyncView state={sourcesQ} loadingRows={5}>
                  {() => null}
                </AsyncView>
                {/* 来源清单读不到时也不能变成死路：至少保留「看得见已选、能移除、能保存」 */}
                {sourcesQ.error !== undefined && rows.length > 0 && (
                  <ul className="source-fallback">
                    {rows.map((r, i) => (
                      <li key={`${r.real_model_id}-${i}`}>
                        <code>{r.real_model_id}</code>
                        <span className="muted">权重 {r.weight}</span>
                        <span className="muted">渠道范围：{scopeSummary(r.constraint)}</span>
                        <button type="button" className="btn-ghost" onClick={() => removeRow(i)}>
                          移除
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
              </>
            ) : (
              <div className="source-split">
                <div className="source-rail">
                  <div className="source-search">
                    <IconSearch size="sm" />
                    <input
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                      placeholder="搜索供应商或模型"
                      aria-label="搜索供应商或模型"
                    />
                  </div>
                  {railGroup('贡献凭据', visibleSources(contributed))}
                  {railGroup('平台渠道', visibleSources(platform))}
                  {keyword &&
                    visibleSources(contributed).length === 0 &&
                    visibleSources(platform).length === 0 && <p className="muted">没有匹配的来源或模型。</p>}
                </div>

                <div className="source-detail">
                  {active === null ? (
                    <EmptyState
                      title="先从左侧选一个来源"
                      hint="左栏按「贡献凭据 / 平台渠道」分组列出候选来源；选中后这里显示它的模型卡片，点卡片即加入或移出候选集。"
                    />
                  ) : (
                    <>
                      <div className="source-head">
                        <div className="source-head-title">
                          <span className="source-head-name">{active.name}</span>
                          {active.region && <span className="source-head-tag">{active.region.toUpperCase()}</span>}
                          <span className="source-head-tag">{KIND_LABEL[active.kind] ?? active.kind}</span>
                          {!active.enabled && (
                            <span className="source-head-tag" data-tone="warn">
                              已停用
                            </span>
                          )}
                          {active.website && (
                            <a className="icon-link" href={active.website} target="_blank" rel="noreferrer noopener">
                              <IconExternal size={ACTION_ICON} />
                              官网
                            </a>
                          )}
                        </div>
                        <p className="source-head-facts">
                          <code>{active.base_url || '未提供端点'}</code>
                          <span>凭据 {fmtNum(active.credential_count)}</span>
                          <span>模型 {fmtNum(active.model_count)}</span>
                          <span className="source-inst" data-tone={instancesTone(active.available_instances)}>
                            可用实例 {instancesText(active.available_instances)}
                          </span>
                          <ProtocolBadges protocols={active.protocols} />
                        </p>
                        {active.name !== active.upstream_name && (
                          <p className="muted source-head-alias">
                            上游行名：<code>{active.upstream_name}</code>
                          </p>
                        )}
                        <p className="muted source-head-note">{PROTOCOL_BOUNDARY}</p>
                      </div>
                      {visibleModels.length === 0 ? (
                        <EmptyState title="该来源下没有模型" hint="模型清单来自 real_models：先让它被采集到，或换一个来源。" />
                      ) : (
                        <div className="source-grid">{visibleModels.map((m) => modelCard(m))}</div>
                      )}
                    </>
                  )}
                </div>
              </div>
            )}

            <div className="source-footer">
              <span className="source-footer-sum">
                已选 {rows.length} 条
                {noCredential.length > 0 && (
                  <span className="source-inst" data-tone="warn">
                    <IconWarn size="xs" />
                    {noCredential.length} 条当前无凭据支撑
                  </span>
                )}
                {foreign.length > 0 && (
                  <span className="muted">{foreign.length} 条不在当前来源清单中（原样保留）</span>
                )}
              </span>
              <button type="button" className="btn-primary" onClick={() => setConfirming(true)} disabled={!!busy}>
                保存候选集…
              </button>
            </div>
            {rows.length === 0 && (
              <p className="muted">候选集为空：该抽象模型的请求会立即返回 <code>no_candidate</code>。</p>
            )}
            {!changed && <p className="muted">没有未保存的变更。</p>}
          </Section>
        )}
      </AsyncView>

      <Section title="候选集口径">
        <ul className="plain-list">
          <li>替换是整表语义：空集合 = 清空，不是「不改」。</li>
          <li><code>aggregate/*</code> 默认不可选：与上游策略叠加会让问题无法归因。</li>
          <li>可用实例数是运行时展开结果，请求里给值会被忽略。</li>
          <li>渠道范围按 <code>{'{mode,channels}'}</code> 形状写入：一个都不选 = 全部渠道，等价于不写 channel_scope。</li>
          <li>增删候选与调权重属于「改抽象模型」：保存前需要二次确认。</li>
        </ul>
        <KeyValues
          items={[
            { label: '抽象模型', value: <code>{model.name}</code> },
            { label: '状态', value: <StatusBadge status={model.status} label={statusLabel(model.status)} /> },
            {
              label: '当前可用候选',
              // null = 事实清单没读到时不做断言（显示「—」而不是一个可能是假的数字）。
              value: usableCount === null ? '—' : `${fmtNum(usableCount)} / ${fmtNum(rows.length)}`
            }
          ]}
        />
      </Section>

      <ConfirmDialog
        open={confirming}
        title={`确认替换抽象模型 ${model.name} 的候选集`}
        impact={[
          `候选行数：${original.length} → ${rows.length}`,
          `可用候选：${usableCount === null ? '—' : fmtNum(usableCount)} → 由服务端重新展开`,
          '保存后线上路由立即使用新候选集',
          '如果有正在使用被移除候选的请求，服务端结果会体现在可用数上'
        ]}
        irreversibility="可恢复（再替换回来），但期间路由结果已改变。"
        targetName={model.name}
        inputLabel="请输入对外名以确认"
        confirmLabel="确认替换候选集"
        busy={busy}
        error={error}
        onCancel={() => setConfirming(false)}
        onConfirm={() => void save()}
      />
    </PageFrame>
  )
}

// ── 弃用配置 ───────────────────────────────────────────────────────────

export function DeprecationPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const modelQ = useApi(() => getAbstractModel(id), [id])
  const abstractsQ = useApi(() => listAbstractModels({ limit: 200 }), [])
  // 没有替代目标时 /chain 会 404；这里折叠成 null，页面用说明代替错误
  const chainQ = useApi(() => getChain(id).catch(() => null), [id, modelQ.data?.replacement])

  const model = modelQ.data
  const [replacement, setReplacement] = useState('')
  const [deprecatedAt, setDeprecatedAt] = useState('')
  const [sunsetAt, setSunsetAt] = useState('')
  const [migrationDoc, setMigrationDoc] = useState('')
  const [note, setNote] = useState('')
  const [confirming, setConfirming] = useState<'deprecate' | 'disable' | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    if (!model) return
    setReplacement(model.replacement ?? '')
    setMigrationDoc(model.migration_doc ?? '')
    setNote(model.note ?? '')
  }, [model, model?.replacement])

  if (!model) {
    return (
      <PageFrame def={def}>
        <AsyncView state={modelQ} loadingRows={4}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  const candidates = (abstractsQ.data?.data ?? []).filter(
    (m: AdminAbstractModel) => m.status === 'active' && m.id !== model.id
  )
  const chain: ChainPreview | null = chainQ.data ?? null
  const canDeprecate = Boolean(replacement)

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const body: DeprecationInput = {
        status: 'deprecated',
        replacement_model_id: replacement,
        migration_doc: migrationDoc || undefined,
        note: note || undefined,
        ...(deprecatedAt ? { deprecated_at: Math.floor(new Date(deprecatedAt).getTime() / 1000) } : {}),
        ...(sunsetAt ? { sunset_at: Math.floor(new Date(sunsetAt).getTime() / 1000) } : {})
      }
      await putDeprecation(id, body)
      setNotice('弃用配置已保存；该模型对外进入「已弃用」，仍接受请求')
      setConfirming(null)
      modelQ.reload()
      chainQ.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function disable() {
    setBusy(true)
    setError(null)
    try {
      // 停用是 PATCH status=disabled；deprecated 只能走 /deprecation 且单向
      await patchAbstractModel(id, { status: 'disabled' })
      setNotice('模型已设为停用：对外立即 404 / 410 Gone')
      setConfirming(null)
      modelQ.reload()
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
        <span className="inline-links">
          <Link to={`/admin/abstract-models/${encodeURIComponent(id)}`}>返回编辑</Link>
          <Link to={`/admin/abstract-models/${encodeURIComponent(id)}/candidates`}>候选集</Link>
        </span>
      }
    >
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}
      {model.status === 'deprecated' && (
        <Banner tone="warn" title="该模型已弃用">
          弃用不是关闭：它仍然接受请求，响应里的 <code>model</code> 仍回显旧名字。
          请引导客户端迁移到替代目标。取消弃用不受支持（要恢复只能新建同名 active 模型）。
        </Banner>
      )}
      {model.status === 'disabled' && (
        <Banner tone="err" title="该模型已停用">
          它不再接受请求（404 / 410 Gone）。重新启用需要新建同名 active 模型。
        </Banner>
      )}

      <Section title="当前状态">
        <KeyValues
          items={[
            { label: '对外名', value: <code>{model.name}</code> },
            { label: '状态', value: <StatusBadge status={model.status} label={statusLabel(model.status)} /> },
            { label: '替代目标', value: model.replacement ?? '未配置' },
            { label: '弃用时间', value: fmtTime(model.deprecated_at) },
            { label: 'Sunset 时间', value: model.sunset_at ?? '未设置（永久兼容壳）' },
            { label: '迁移文档', value: model.migration_doc ?? '—' }
          ]}
        />
      </Section>

      <Section
        title="弃用配置"
        hint="替代目标必须是一跳且状态为 active；能力必须逐项 ≥ 原模型。保存时服务端还会做环检测（防御性）。"
      >
        <div className="field-row">
          <Field label="替代目标（一跳）" required hint="只列 active 且能力足够的模型；保存时会校验能力逐项 ≥。">
            <select value={replacement} onChange={(e) => setReplacement(e.target.value)}>
              <option value="">选择替代模型</option>
              {candidates.map((m) => (
                <option key={m.id} value={m.name}>
                  {m.name}（{m.display_name}）
                </option>
              ))}
            </select>
          </Field>
          <Field label="Deprecation 时间" hint="RFC 9745 Structured Field Date（本地时间转换后提交 epoch）">
            <input type="datetime-local" value={deprecatedAt} onChange={(e) => setDeprecatedAt(e.target.value)} />
          </Field>
          <Field label="Sunset 时间" hint="可选；不设 = 永久兼容壳（本地时间转换后提交 epoch）">
            <input type="datetime-local" value={sunsetAt} onChange={(e) => setSunsetAt(e.target.value)} />
          </Field>
        </div>
        <Field label="迁移文档链接" hint={'用于 Link: rel="deprecation"。'}>
          <input value={migrationDoc} onChange={(e) => setMigrationDoc(e.target.value)} placeholder="/docs/models/fast" />
        </Field>
        <Field label="备注">
          <input value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>
        <button
          type="button"
          className="btn-primary"
          onClick={() => setConfirming('deprecate')}
          disabled={!canDeprecate || model.status !== 'active' || busy}
        >
          保存弃用配置…
        </button>
        {model.status !== 'active' && <p className="muted">只有 active 的模型可以进入弃用；弃用是单向状态。</p>}
        {!canDeprecate && <p className="muted">请先选择替代目标。</p>}
      </Section>

      <Section title="替代链预览" hint="管理员配一跳，客户端看到的是链尾：预览必须显示最终 active 落点，否则会漏掉「实际上跟到第三手」的问题。">
        {chainQ.loading && <p>正在读取链…</p>}
        {!chainQ.loading && !chain && (
          <p className="muted">尚无替代链（未配置替代目标，或链读取暂时失败）。</p>
        )}
        {chain && (
          <>
            <p className="chain-preview">
              {chain.chain.map((c, i) => (
                <span key={`${c}-${i}`}>
                  {i > 0 && <span aria-hidden="true"> → </span>}
                  <code>{c}</code>
                </span>
              ))}
              {chain.final_active && <strong>（最终 active：{chain.final_active}）</strong>}
            </p>
            <p>跳数：{chain.hops}；警告：{chain.warning ?? '无'}</p>
            <div className="field-row">
              {Object.entries(chain.capability_check.checks).map(([k, ok]) => (
                <Badge key={k} tone={ok ? 'ok' : 'err'}>
                  {k}：{ok ? '达标' : '不足'}
                </Badge>
              ))}
            </div>
            {!chain.capability_check.ok && (
              <Banner tone="err">能力校验未通过：保存弃用配置会被服务端拒绝。请换一个能力 ≥ 原模型的替代目标。</Banner>
            )}
            <JsonDetails summary="查看链原始结果" value={chain} />
          </>
        )}
      </Section>

      <Section title="停用">
        <Banner tone="warn" title="停用是立即生效的关闸">
          设为 disabled 后该模型立即 404 / 410 Gone，所有指向它的客户端会失败。
          与弃用的区别：弃用仍接受请求，停用不接受。
        </Banner>
        <button
          type="button"
          className="btn-danger"
          onClick={() => setConfirming('disable')}
          disabled={model.status === 'disabled' || busy}
        >
          {model.status === 'disabled' ? '已经停用' : '设为停用…'}
        </button>
      </Section>

      {confirming === 'deprecate' && (
        <ConfirmDialog
          open
          title={`确认弃用抽象模型 ${model.name}`}
          impact={[
            `替代目标：${replacement}（必须是 active）`,
            `该模型仍接受请求，但会带上弃用元数据；客户端应迁移`,
            `当前候选 ${model.candidate_count} 条 / 可用 ${model.available_candidate_count} 条`,
            chain ? `链尾最终 active：${chain.final_active}` : '尚未读取替代链'
          ]}
          irreversibility="弃用是单向状态：不提供取消弃用；要恢复只能新建同名的 active 模型。"
          targetName={model.name}
          inputLabel="请输入对外名以确认"
          confirmLabel={`确认弃用 ${model.name}`}
          busy={busy}
          error={error}
          onCancel={() => setConfirming(null)}
          onConfirm={() => void save()}
        />
      )}

      {confirming === 'disable' && (
        <ConfirmDialog
          open
          title={`确认停用抽象模型 ${model.name}`}
          impact={[
            `所有指向该模型的客户端将收到 404 / 410 Gone`,
            `目标对象：${model.name}（当前 ${model.candidate_count} 条候选）`,
            '停用后候选集与授权仍保留，但不再被路由使用'
          ]}
          irreversibility="可恢复（需新建同名 active 模型），但期间请求已失败。"
          targetName={model.name}
          inputLabel="请输入对外名以确认"
          confirmLabel={`确认停用 ${model.name}`}
          busy={busy}
          error={error}
          onCancel={() => setConfirming(null)}
          onConfirm={() => void disable()}
        />
      )}
    </PageFrame>
  )
}
