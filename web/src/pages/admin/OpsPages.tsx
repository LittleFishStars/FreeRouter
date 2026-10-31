// 上游与凭据（docs/ui/03-admin.md 第 8 节）。
//
// relay 类型没有 base_url：它的调用路径是 Node sidecar 直接调 providers-hub
// 的 provider adapter，不经过任何 HTTP 网关；给地址字段等于留一个必然失败
// 且会破坏归因的配置项。
//
// 凭据明文永不显示：界面连「查看」按钮都不提供，任何「点击查看」的实现
// 都会留下截图/缓存/审计日志的可利用路径，而便利为零。

import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  banCredential,
  createRealModel,
  createUpstream,
  getCredential,
  getModelSources,
  notifyCredential,
  patchRealModel,
  patchUpstream,
  removeUpstream,
  checkinCredential,
  getBalanceSyncStatus,
  runBalanceSync,
  setBalanceSyncEnabled,
  setBalanceSyncInterval,
  getCheckinStatus,
  probeUpstreamModels,
  refreshCredentialBalance,
  refreshRealModels,
  runCheckinRound,
  setCheckinEnabled,
  suspendCredential
} from '../../api/endpoints'
import type {
  AdminCredential,
  AdminModelSource,
  AdminModelSourceModel,
  UpstreamKind,
  UpstreamModelProbeResult
} from '../../api/types'
import { useApi } from '../../hooks/useApi'
import ConfirmDialog from '../../components/ConfirmDialog'
import { ACTION_ICON, ToolButton } from '../../components/IconButtons'
import {
  IconBack,
  IconCalendarCheck,
  IconCheck,
  IconDelete,
  IconEdit,
  IconExternal,
  IconPlus,
  IconPower,
  IconRefresh,
  IconTest,
  IconWarn,
  IconSearch
} from '../../icons'
import {
  AsyncView,
  Banner,
  EmptyState,
  Field,
  FormError,
  JsonDetails,
  KeyValues,
  Section,
  StatusBadge,
  Toast
} from '../../components/ui'
import { fmtNum, fmtTime, statusLabel } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

const KIND_LABELS: Record<string, string> = {
  relay: 'relay（复用 providers-hub 池）',
  vendor: 'vendor（贡献者凭据直连官方）',
  selfhosted: 'selfhosted（自建兼容端点）'
}

const KIND_SHORT: Record<string, string> = {
  relay: 'relay',
  vendor: 'vendor',
  selfhosted: 'selfhosted'
}

const PROTOCOL_GLYPH: Record<string, { letter: string; label: string }> = {
  openai: { letter: 'O', label: 'OpenAI 兼容端点' },
  anthropic: { letter: 'A', label: 'Anthropic 原生端点' }
}

function ProtocolBadges({ protocols }: { protocols: string[] | undefined }) {
  if (!protocols || protocols.length === 0) return null
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

function regionSuffix(s: { name: string; region: string }): string {
  if (!s.region) return ''
  const tag = s.region.toUpperCase()
  return s.name.toUpperCase().includes(`(${tag})`) || s.name.toUpperCase() === tag ? '' : `（${tag}）`
}

function instancesText(v: number | null | undefined): string {
  if (v === null || v === undefined) return '池内'
  if (v === 0) return '无凭据'
  return fmtNum(v)
}

function instancesTone(v: number | null | undefined): string {
  if (v === null || v === undefined) return 'muted'
  return v === 0 ? 'warn' : 'ok'
}

/** 轻量表单弹窗（非危险操作；危险停用走 ConfirmDialog）。 */
function PoolModal({
  open,
  title,
  children,
  footer,
  onClose
}: {
  open: boolean
  title: string
  children: ReactNode
  footer: ReactNode
  onClose: () => void
}) {
  if (!open) return null
  return (
    <div className="dialog-overlay" onClick={onClose}>
      <div
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
      >
        {title && <h2>{title}</h2>}
        <div className="dialog-body">{children}</div>
        <div className="dialog-actions">{footer}</div>
      </div>
    </div>
  )
}

function railGroup(
  title: string,
  list: AdminModelSource[],
  activeId: string | null,
  onSelect: (id: string) => void
) {
  return (
    <div className="source-group">
      <p className="source-group-title">
        {title}
        <span className="source-group-count">{list.length}</span>
      </p>
      {list.length === 0 ? (
        <p className="muted source-group-empty">（无）</p>
      ) : (
        <ul className="pool-source-list">
          {list.map((s) => (
            <li key={s.id}>
              <button
                type="button"
                className={'pool-source-row' + (s.id === activeId ? ' pool-source-row-active' : '')}
                data-off={!s.enabled}
                onClick={() => onSelect(s.id)}
              >
                <span className="pool-source-row-main">
                  <span className="pool-source-row-name">
                    {s.name}
                    {regionSuffix(s)}
                    {!s.enabled && <span className="source-off-tag">已停用</span>}
                  </span>
                  <span className="pool-source-row-facts">
                    <ProtocolBadges protocols={s.protocols} />
                    <span className="source-count" title={`${s.model_count} 个模型`}>
                      {s.model_count}
                    </span>
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// ── 凭据池（供应商两栏：左栏供应商，右栏凭据 + 模型） ───────────────────────

export function CredentialsPage({ def }: PageProps) {
  const sourcesQ = useApi(() => getModelSources(), [])
  const [activeId, setActiveId] = useState<string | null>(null)
  const [keyword, setKeyword] = useState('')

  // 通用提交态
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  // 添加供应商
  const [adding, setAdding] = useState(false)
  const [kind, setKind] = useState<UpstreamKind>('vendor')
  const [name, setName] = useState('')
  const [baseURL, setBaseURL] = useState('')

  // 编辑供应商
  const [editing, setEditing] = useState<AdminModelSource | null>(null)
  const [editName, setEditName] = useState('')
  const [editBase, setEditBase] = useState('')

  // 停用确认
  const [disableTarget, setDisableTarget] = useState<AdminModelSource | null>(null)
  // 移除（2026-10-31）：不可逆撤销——密钥材料被墓碑覆写，行退出池。
  const [removeTarget, setRemoveTarget] = useState<AdminModelSource | null>(null)

  // 添加模型
  const [addingModel, setAddingModel] = useState(false)
  const [newModelId, setNewModelId] = useState('')
  const [newDisplay, setNewDisplay] = useState('')
  const [newCtx, setNewCtx] = useState('')

  // 编辑模型
  const [editingModel, setEditingModel] = useState<AdminModelSourceModel | null>(null)
  const [emDisplay, setEmDisplay] = useState('')
  const [emCtx, setEmCtx] = useState('')
  const [emExcluded, setEmExcluded] = useState(false)

  const sources = useMemo(() => sourcesQ.data?.sources ?? [], [sourcesQ.data])
  // 余额刷新的按行 busy 标记（2026-10-28）：一条凭据一次上游查询，行级互不阻塞
  const [balBusy, setBalBusy] = useState<Record<string, boolean>>({})
  const [balNote, setBalNote] = useState<{ id: string; text: string; ok: boolean } | null>(null)

  async function refreshBalance(id: string) {
    setBalBusy((m) => ({ ...m, [id]: true }))
    setBalNote(null)
    try {
      const r = await refreshCredentialBalance(id)
      const d = r.data
      // 成功/失败都弹右下角 toast（2026-10-30 改定）：不占行内空间，几秒后自动退场；
      // 列表数据（balance_text）由 reload 拉新
      setBalNote({ id, text: d.ok ? `余额已更新：${d.balance_text ?? ''}` : `查询失败：${d.reason ?? '未知原因'}`, ok: d.ok })
      sourcesQ.reload()
    } catch (e) {
      setBalNote({ id, text: e instanceof Error ? e.message : '刷新失败', ok: false })
    } finally {
      setBalBusy((m) => ({ ...m, [id]: false }))
    }
  }
  // —— 定时签到（2026-10-31）——
  // checkinQ 是全局自动签到状态（开关/最近一轮/到期数）；checkBusy 是按行 busy。
  const checkinQ = useApi(() => getCheckinStatus(), [])
  const [checkBusy, setCheckBusy] = useState<Record<string, boolean>>({})
  const [checkRoundBusy, setCheckRoundBusy] = useState(false)
  const [checkNote, setCheckNote] = useState<{ text: string; ok: boolean } | null>(null)

  async function checkinOne(id: string) {
    setCheckBusy((m) => ({ ...m, [id]: true }))
    setCheckNote(null)
    try {
      const d = (await checkinCredential(id)).data
      // 成功/失败都弹 toast：text 是 Go 侧格式化好的展示串（"+200 积分"/失败原因）
      setCheckNote({ text: d.text || d.message || (d.ok ? '签到完成' : '签到失败'), ok: d.kind !== 'failed' })
      sourcesQ.reload()
      checkinQ.reload()
    } catch (e) {
      setCheckNote({ text: e instanceof Error ? e.message : '签到失败', ok: false })
    } finally {
      setCheckBusy((m) => ({ ...m, [id]: false }))
    }
  }

  async function runCheckinNow() {
    setCheckRoundBusy(true)
    setCheckNote(null)
    try {
      const d = (await runCheckinRound()).data
      setCheckNote({ text: `本轮签到：${d.summary}`, ok: d.failed === 0 })
      sourcesQ.reload()
      checkinQ.reload()
    } catch (e) {
      setCheckNote({ text: e instanceof Error ? e.message : '签到失败', ok: false })
    } finally {
      setCheckRoundBusy(false)
    }
  }

  async function toggleCheckin(next: boolean) {
    setCheckRoundBusy(true)
    setCheckNote(null)
    try {
      await setCheckinEnabled(next)
      checkinQ.reload()
    } catch (e) {
      setCheckNote({ text: e instanceof Error ? e.message : '开关保存失败', ok: false })
    } finally {
      setCheckRoundBusy(false)
    }
  }

  // —— 定时刷新全部余额（2026-11-01）——
  // balanceQ 是全局状态（开关/周期/最近一轮/下次时刻/待刷数）。
  const balanceQ = useApi(() => getBalanceSyncStatus(), [])
  const [balanceBusy, setBalanceBusy] = useState(false)
  const [balanceNote, setBalanceNote] = useState<{ text: string; ok: boolean } | null>(null)

  async function runBalanceNow() {
    setBalanceBusy(true)
    setBalanceNote(null)
    try {
      const d = (await runBalanceSync()).data
      setBalanceNote({ text: `本轮余额刷新：${d.summary}`, ok: d.failed === 0 })
      sourcesQ.reload()
      balanceQ.reload()
    } catch (e) {
      setBalanceNote({ text: e instanceof Error ? e.message : '刷新失败', ok: false })
    } finally {
      setBalanceBusy(false)
    }
  }

  async function toggleBalanceSync(next: boolean) {
    setBalanceBusy(true)
    setBalanceNote(null)
    try {
      await setBalanceSyncEnabled(next)
      balanceQ.reload()
    } catch (e) {
      setBalanceNote({ text: e instanceof Error ? e.message : '开关保存失败', ok: false })
    } finally {
      setBalanceBusy(false)
    }
  }

  async function changeBalanceInterval(minutes: number) {
    setBalanceBusy(true)
    setBalanceNote(null)
    try {
      await setBalanceSyncInterval(minutes)
      balanceQ.reload()
    } catch (e) {
      setBalanceNote({ text: e instanceof Error ? e.message : '周期保存失败', ok: false })
    } finally {
      setBalanceBusy(false)
    }
  }

  // —— 供应商可用性检验（2026-10-31）——
  // 按供应商逐模型真实请求一次（max_tokens=1）；结果芯片条挂在来源详情头部下
  const [probeRes, setProbeRes] = useState<Record<string, UpstreamModelProbeResult>>({})
  const [probeBusy, setProbeBusy] = useState<Record<string, boolean>>({})
  const [probeErr, setProbeErr] = useState<Record<string, string>>({})

  async function probeSource(id: string) {
    setProbeBusy((m) => ({ ...m, [id]: true }))
    setProbeErr((m) => ({ ...m, [id]: '' }))
    try {
      const d = (await probeUpstreamModels(id)).data
      setProbeRes((m) => ({ ...m, [id]: d }))
    } catch (e) {
      setProbeErr((m) => ({ ...m, [id]: e instanceof Error ? e.message : '检验失败' }))
    } finally {
      setProbeBusy((m) => ({ ...m, [id]: false }))
    }
  }

  // 分组（2026-10-27 用户决定）：反代供应商（账号授权，凭据由 sidecar 换出）与
  // API 供应商（用户粘 Key 直连端点）是两个不同的接入形态。
  // 判据用 oauth 标记而不是 kind：OAuth 反代的 upstream 也是 vendor
  // （凭据走 OpenAI 兼容端点），按 kind 分组会把 Loomy 这类错归进 API 组
  //（2026-10-27 用户报告）。旧分组「贡献凭据/平台渠道」按贡献与否切，
  // 与接入形态无关，已废弃。
  const relaySources = sources.filter((s) => s.oauth)
  const apiSources = sources.filter((s) => !s.oauth)
  const active = sources.find((s) => s.id === activeId) ?? sources[0] ?? null

  function visibleSources(list: AdminModelSource[]): AdminModelSource[] {
    const kw = keyword.trim().toLowerCase()
    if (!kw) return list
    return list.filter((s) => {
      if (s.name.toLowerCase().includes(kw) || s.upstream_name.toLowerCase().includes(kw)) return true
      return s.models.some(
        (m) =>
          m.display_name.toLowerCase().includes(kw) ||
          m.upstream_model_id.toLowerCase().includes(kw)
      )
    })
  }

  async function refreshDir() {
    setBusy(true)
    setError(null)
    try {
      await refreshRealModels()
      setNotice('已触发目录刷新；结果会在后台任务完成后体现。')
      sourcesQ.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function createUpstreamSubmit() {
    setBusy(true)
    setError(null)
    try {
      await createUpstream({
        kind,
        name,
        base_url: kind === 'relay' ? undefined : baseURL,
        enabled: true
      })
      setAdding(false)
      setName('')
      setBaseURL('')
      setNotice('供应商已创建')
      sourcesQ.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function saveEdit() {
    if (!editing) return
    setBusy(true)
    setError(null)
    try {
      await patchUpstream(editing.id, {
        name: editName,
        ...(editing.kind === 'relay' || editing.oauth ? {} : { base_url: editBase })
      })
      setEditing(null)
      setNotice('已保存')
      sourcesQ.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function setEnabled(s: AdminModelSource, enabled: boolean) {
    setBusy(true)
    setError(null)
    try {
      // 来源合并后一条来源聚合多条上游行（每把 key 一条）：停用/启用是
      // 来源级操作，逐个成员调用；漏一个成员就会出现"来源显示启用但
      // 有一半凭据实际不可调用"的割裂。
      for (const id of s.upstream_ids ?? [s.id]) {
        await patchUpstream(id, { enabled })
      }
      sourcesQ.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function removeSource(s: AdminModelSource) {
    setBusy(true)
    setError(null)
    try {
      // 来源合并后一条来源聚合多条上游行：移除是来源级操作，逐个成员调用；
      // 漏一个成员会留下「来源没了但还有一把 key 在池里」。
      for (const id of s.upstream_ids ?? [s.id]) {
        await removeUpstream(id)
      }
      sourcesQ.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function createModelSubmit() {
    if (!active) return
    setBusy(true)
    setError(null)
    try {
      await createRealModel({
        upstream_id: active.id,
        upstream_model_id: newModelId.trim(),
        ...(newDisplay.trim() ? { display_name: newDisplay.trim() } : {}),
        ...(newCtx !== '' ? { context_window: Number(newCtx) } : {})
      })
      setAddingModel(false)
      setNotice('已登记')
      sourcesQ.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  function startEditModel(m: AdminModelSourceModel) {
    setEditingModel(m)
    setEmDisplay(m.display_name)
    setEmCtx(m.context_window === null ? '' : String(m.context_window))
    setEmExcluded(m.excluded_from_candidates)
  }

  async function saveModelEdit() {
    if (!editingModel) return
    setBusy(true)
    setError(null)
    try {
      await patchRealModel(editingModel.real_model_id, {
        display_name: emDisplay,
        ...(emCtx === '' ? { context_window: null } : { context_window: Number(emCtx) }),
        excluded_from_candidates: emExcluded
      })
      setEditingModel(null)
      setNotice('已保存（写入 source=override，目录刷新不再覆盖）')
      sourcesQ.reload()
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
        <>
          <ToolButton icon={<IconRefresh size={ACTION_ICON} />} onClick={() => void refreshDir()} busy={busy}>
            刷新目录
          </ToolButton>
          <ToolButton primary icon={<IconPlus size={ACTION_ICON} />} onClick={() => setAdding(true)}>
            添加供应商
          </ToolButton>
        </>
      }
    >
      <Banner tone="muted">
        凭据明文永不显示，界面也不提供「查看」。手工添加的模型写入 <code>source=override</code>，
        之后的目录刷新不会覆盖其显示名与上下文窗口。
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}
      {checkNote && <Banner tone={checkNote.ok ? 'ok' : 'warn'}>{checkNote.text}</Banner>}

      {/* 自动签到状态条（2026-10-31）：开关与最近一轮是全池级状态，不是单个
          供应商的——放页面级而不是 pool 头部，避免逐源重复渲染。 */}
      <AsyncView state={checkinQ} loadingRows={1}>
        {(st) => (
          <div className="checkin-strip">
            <button
              type="button"
              className="btn-ghost"
              disabled={checkRoundBusy}
              onClick={() => void toggleCheckin(!st.enabled)}
            >
              <IconPower size="xs" />
              自动签到{st.enabled ? '已开启' : '已关闭'}
            </button>
            <span className="checkin-strip-summary muted">
              {st.last_run_result
                ? `上次：${st.last_run_result}${st.last_run_date ? `（${st.last_run_date}）` : ''}`
                : '尚未运行过；每天首次到期后自动执行'}
              {st.due_count > 0 && ` · 待签 ${st.due_count}`}
            </span>
            <button type="button" className="btn-ghost" disabled={checkRoundBusy} onClick={() => void runCheckinNow()}>
              <IconCalendarCheck size="xs" />
              全部签到
            </button>
          </div>
        )}
      </AsyncView>

      {/* 定时刷新全部余额（2026-11-01）：与签到同属全池级状态，放页面级。 */}
      <AsyncView state={balanceQ} loadingRows={1}>
        {(bs) => (
          <div className="checkin-strip">
            <button
              type="button"
              className="btn-ghost"
              disabled={balanceBusy}
              onClick={() => void toggleBalanceSync(!bs.enabled)}
            >
              <IconPower size="xs" />
              定时刷新余额{bs.enabled ? '已开启' : '已关闭'}
            </button>
            <span className="checkin-strip-summary muted">
              {bs.last_run_result
                ? `上次：${bs.last_run_result}${bs.last_run_at ? `（${fmtTime(bs.last_run_at)}）` : ''}`
                : '尚未运行过；开启后按周期自动执行'}
              {bs.enabled && bs.next_run_at
                ? ` · 下次 ${fmtTime(bs.next_run_at)}`
                : bs.enabled
                  ? ' · 首轮约在启动 90 秒后'
                  : ''}
              {bs.due_count > 0 && ` · 可刷 ${bs.due_count} 条`}
            </span>
            <label className="toolbar-field">
              周期
              <select
                value={bs.interval_minutes}
                disabled={balanceBusy}
                onChange={(e) => void changeBalanceInterval(Number(e.target.value))}
              >
                <option value={15}>15 分钟</option>
                <option value={30}>30 分钟</option>
                <option value={60}>1 小时</option>
                <option value={180}>3 小时</option>
                <option value={720}>12 小时</option>
                <option value={1440}>24 小时</option>
              </select>
            </label>
            <button type="button" className="btn-ghost" disabled={balanceBusy} onClick={() => void runBalanceNow()}>
              <IconRefresh size="xs" />
              立即刷新全部余额
            </button>
          </div>
        )}
      </AsyncView>
      {balanceNote && <Banner tone={balanceNote.ok ? 'ok' : 'warn'}>{balanceNote.text}</Banner>}

      <AsyncView state={sourcesQ} loadingRows={6}>
        {() => (
          <div className="source-split">
            <div className="source-rail">
              <div className="source-search">
                <IconSearch size="sm" />
                <input
                  value={keyword}
                  onChange={(e) => setKeyword(e.target.value)}
                  placeholder="搜索供应商或模型"
                  aria-label="搜索供应商或模型"
                />
              </div>
              {railGroup('反代供应商', visibleSources(relaySources), active?.id ?? null, setActiveId)}
              {railGroup('API 供应商', visibleSources(apiSources), active?.id ?? null, setActiveId)}
              {keyword &&
                visibleSources(relaySources).length === 0 &&
                visibleSources(apiSources).length === 0 && <p className="muted">没有匹配的来源或模型。</p>}
            </div>

            <div className="source-detail">
              {active === null ? (
                <EmptyState
                  title="先从左侧选一个供应商"
                  hint="左栏按「反代供应商 / API 供应商」分组列出，同一供应商的多把凭据已合并为一行；选中后这里显示它的凭据与真实模型。"
                />
              ) : (
                <div className="pool-detail">
                  <div className="source-head">
                    <div className="source-head-title">
                      <span className="source-head-name">{active.name}</span>
                      {regionSuffix(active) && (
                        <span className="source-head-tag">{regionSuffix(active).replace(/[（）]/g, '')}</span>
                      )}
                      <span className="source-head-tag">{KIND_SHORT[active.kind] ?? active.kind}</span>
                      {!active.enabled && (
                        <span className="source-head-tag" data-tone="warn">已停用</span>
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
                    <div className="pool-head-actions">
                      {active.enabled ? (
                        <button type="button" className="btn-ghost" onClick={() => setDisableTarget(active)} disabled={busy}>
                          <IconPower size="sm" />
                          停用
                        </button>
                      ) : (
                        <button type="button" className="btn-ghost" onClick={() => void setEnabled(active, true)} disabled={busy}>
                          <IconPower size="sm" />
                          启用
                        </button>
                      )}
                      <button
                        type="button"
                        className="btn-ghost"
                        onClick={() => {
                          setEditName(active.name)
                          setEditBase(active.kind === 'relay' || active.oauth ? '' : active.base_url)
                          setEditing(active)
                        }}
                        disabled={busy}
                      >
                        <IconEdit size="sm" />
                        编辑
                      </button>
                      <button
                        type="button"
                        className="btn-ghost"
                        disabled={probeBusy[active.id]}
                        title="对该供应商的全部模型各发一次真实推理（max_tokens=1），检验可用性；消耗极少量额度，不计入用量统计"
                        onClick={() => void probeSource(active.id)}
                      >
                        <IconTest size="sm" />
                        {probeBusy[active.id] ? '检验中…' : '检验可用性'}
                      </button>
                      <button
                        type="button"
                        className="btn-ghost"
                        disabled={busy}
                        title="移除供应商：销毁该来源的密钥材料并退出候选池，不可恢复"
                        onClick={() => setRemoveTarget(active)}
                      >
                        <IconDelete size="sm" />
                        移除
                      </button>
                    </div>
                  </div>

                  {probeErr[active.id] && (
                    <div className="am-explain" data-tone="warn">检验失败：{probeErr[active.id]}</div>
                  )}
                  {probeRes[active.id] && (
                    <div className="am-conn">
                      <span className="am-conn-sum" data-tone={probeRes[active.id].failed === 0 ? 'ok' : 'warn'}>
                        {probeRes[active.id].failed === 0
                          ? `全部 ${probeRes[active.id].ok} 个模型可达`
                          : `${probeRes[active.id].ok} 可达 / ${probeRes[active.id].failed} 不通${probeRes[active.id].skipped > 0 ? ` / 跳过 ${probeRes[active.id].skipped}` : ''}`}
                      </span>
                      {probeRes[active.id].results.map((r) => (
                        <span
                          key={r.real_model_id}
                          className="am-conn-item"
                          data-ok={r.ok}
                          title={`${r.model_id}：${r.reason ?? (r.ok ? '可达' : '不通')}${r.latency_ms ? `（${r.latency_ms}ms）` : ''}`}
                        >
                          {r.ok ? <IconCheck size="xs" /> : <IconWarn size="xs" />}
                          <span className="am-conn-model">{r.model_id}</span>
                          {r.ok && r.latency_ms > 0 && <span className="am-conn-lat">{r.latency_ms}ms</span>}
                          {!r.ok && r.reason && <span className="am-conn-reason">{r.reason}</span>}
                        </span>
                      ))}
                      <button type="button" className="btn-ghost am-conn-close" onClick={() => setProbeRes((m) => ({ ...m, [active.id]: undefined as unknown as UpstreamModelProbeResult }))}>
                        收起
                      </button>
                    </div>
                  )}

                  <div className="pool-blocks">
                    <div className="pool-block">
                      <div className="pool-block-head">
                        <span className="pool-block-title">凭据（{active.credentials.length}）</span>
                        {/* 凭据只能通过贡献得来，头部不放「添加凭据」入口——
                            空态里的贡献链接才是唯一的引导。 */}
                      </div>
                      {active.credentials.length === 0 ? (
                        <p className="muted pool-empty">
                          还没有凭据；凭据只能通过贡献得来，
                          <Link to="/contribute">去贡献入口提交</Link>。
                        </p>
                      ) : (
                        <ul className="pool-cred-list">
                          {active.credentials.map((c) => (
                            <li key={c.id}>
                              <Link className="pool-cred-row" to={`/admin/credentials/${encodeURIComponent(c.id)}`}>
                                <span className="pool-cred-main">
                                  <code>{c.id}</code>
                                  <span>{c.external_label ?? '贡献凭据'}</span>
                                </span>
                                <span className="pool-cred-badges">
                                  <StatusBadge status={c.status} label={statusLabel(c.status)} />
                                  <StatusBadge status={c.verify_status} label={statusLabel(c.verify_status)} />
                                  {/* 余额徽标（2026-10-28）：text 非空才显示——「从未查询」
                                      与「查询失败」不冒充余额（失败文案在刷新动作的结果里） */}
                                  {c.balance_text && (
                                    <span className="am-chip-tag" data-tone="ok" title={c.balance_detail ? `明细：${c.balance_detail}\n查询于 ${fmtTime(c.balance_checked_at)}` : `查询于 ${fmtTime(c.balance_checked_at)}`}>
                                      {c.balance_text}
                                    </span>
                                  )}
                                  {c.checkin_text && (
                                    <span className="am-chip-tag" data-tone={c.checkin_date ? 'ok' : 'warn'} title={c.checkin_date ? `签到于 ${c.checkin_date}` : '最近一次尝试没有签上（失败会按退避自动重试）'}>
                                      {c.checkin_text}
                                    </span>
                                  )}
                                </span>
                                <span className="pool-cred-time">{fmtTime(c.created_at)}</span>
                              </Link>
                              {/* 刷新余额：按行独立（一条凭据一次上游查询），busy 期间禁用 */}
                              <div className="pool-cred-actions">
                                <button
                                  type="button"
                                  className="btn-ghost"
                                  disabled={busy || balBusy[c.id]}
                                  onClick={() => void refreshBalance(c.id)}
                                >
                                  <IconRefresh size="xs" />
                                  {balBusy[c.id] ? '查询中…' : '刷新余额'}
                                </button>
                                <button
                                  type="button"
                                  className="btn-ghost"
                                  disabled={busy || checkBusy[c.id]}
                                  onClick={() => void checkinOne(c.id)}
                                >
                                  <IconCalendarCheck size="xs" />
                                  {checkBusy[c.id] ? '签到中…' : '签到'}
                                </button>
                              </div>
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>

                    <div className="pool-block">
                      <div className="pool-block-head">
                        <span className="pool-block-title">模型（{active.models.length}）</span>
                        <button type="button" className="btn-ghost pool-add-chip-btn" onClick={() => setAddingModel(true)} disabled={busy}>
                          <IconPlus size="xs" />
                          添加模型
                        </button>
                      </div>
                      {active.models.length === 0 ? (
                        <p className="muted pool-empty">还没有模型，刷新目录或手工添加。</p>
                      ) : (
                        <div className="am-chip-row">
                          {active.models.map((m) => {
                            const excluded = m.excluded_from_candidates
                            const missing = m.status === 'missing'
                            return (
                              <button
                                key={m.real_model_id}
                                type="button"
                                className={'am-chip' + (missing ? ' am-chip-missing' : '')}
                                onClick={() => startEditModel(m)}
                                title={excluded ? '已从候选中排除，点按编辑' : missing ? '已消失，点按编辑' : '点按编辑'}
                              >
                                <span className="am-chip-name">{m.display_name}</span>
                                {!missing && !excluded && <IconCheck size="xs" />}
                                {missing && <span className="am-chip-tag" data-tone="muted">已消失</span>}
                                {excluded && <span className="am-chip-tag" data-tone="warn">已排除</span>}
                              </button>
                            )
                          })}
                        </div>
                      )}
                    </div>
                  </div>
                </div>
              )}
            </div>
          </div>
        )}
      </AsyncView>

      {/* 添加供应商弹窗 */}
      <PoolModal
        open={adding}
        title="添加供应商"
        onClose={() => setAdding(false)}
        footer={
          <>
            <button type="button" className="btn-ghost" onClick={() => setAdding(false)}>取消</button>
            <button
              type="button"
              className="btn-primary"
              disabled={busy || !name.trim() || (kind !== 'relay' && !baseURL.trim())}
              onClick={() => void createUpstreamSubmit()}
            >
              {busy ? '正在创建…' : '创建'}
            </button>
          </>
        }
      >
        <div className="field-row">
          <Field label="类型" required>
            <select
              value={kind}
              onChange={(e) => {
                const k = e.target.value as UpstreamKind
                setKind(k)
                if (k === 'relay') setBaseURL('')
              }}
            >
              {Object.entries(KIND_LABELS).map(([v, l]) => (
                <option key={v} value={v}>{l}</option>
              ))}
            </select>
          </Field>
          <Field label="名称" required>
            <input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          {kind !== 'relay' ? (
            <Field label="base_url" required hint="vendor 填官方地址；selfhosted 填自建兼容端点。">
              <input value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="https://…" />
            </Field>
          ) : (
            <p className="muted">relay 由 sidecar 直连 providers-hub，不需要也不会接受地址。</p>
          )}
        </div>
      </PoolModal>

      {/* 编辑供应商弹窗 */}
      <PoolModal
        open={editing !== null}
        title={`编辑供应商 ${editing?.name ?? ''}`}
        onClose={() => setEditing(null)}
        footer={
          <>
            <button type="button" className="btn-ghost" onClick={() => setEditing(null)}>取消</button>
            <button type="button" className="btn-primary" disabled={busy} onClick={() => void saveEdit()}>
              {busy ? '正在保存…' : '保存'}
            </button>
          </>
        }
      >
        <div className="field-row">
          <Field label="名称">
            <input value={editName} onChange={(e) => setEditName(e.target.value)} />
          </Field>
          {editing && editing.kind !== 'relay' && (
            <Field label="base_url">
              <input value={editBase} onChange={(e) => setEditBase(e.target.value)} />
            </Field>
          )}
        </div>
      </PoolModal>

      {/* 添加模型弹窗 */}
      <PoolModal
        open={addingModel}
        title="添加模型"
        onClose={() => setAddingModel(false)}
        footer={
          <>
            <button type="button" className="btn-ghost" onClick={() => setAddingModel(false)}>取消</button>
            <button
              type="button"
              className="btn-primary"
              disabled={busy || !newModelId.trim()}
              onClick={() => void createModelSubmit()}
            >
              {busy ? '正在提交…' : '登记'}
            </button>
          </>
        }
      >
        <div className="field-row">
          <Field label="upstream_model_id" required hint="该上游目录里的真实模型 id。">
            <input value={newModelId} onChange={(e) => setNewModelId(e.target.value)} placeholder="my-model-x" />
          </Field>
          <Field label="显示名" hint="可空，缺省取 upstream_model_id。">
            <input value={newDisplay} onChange={(e) => setNewDisplay(e.target.value)} />
          </Field>
          <Field label="上下文窗口" hint="数字，可空。">
            <input type="number" value={newCtx} onChange={(e) => setNewCtx(e.target.value)} placeholder="如 128000" />
          </Field>
        </div>
      </PoolModal>

      {/* 编辑模型弹窗 */}
      <PoolModal
        open={editingModel !== null}
        title={`编辑模型 ${editingModel?.upstream_model_id ?? ''}`}
        onClose={() => setEditingModel(null)}
        footer={
          <>
            <button type="button" className="btn-ghost" onClick={() => setEditingModel(null)}>取消</button>
            <button type="button" className="btn-primary" disabled={busy} onClick={() => void saveModelEdit()}>
              {busy ? '正在保存…' : '保存'}
            </button>
          </>
        }
      >
        <div className="field-row">
          <Field label="显示名">
            <input value={emDisplay} onChange={(e) => setEmDisplay(e.target.value)} />
          </Field>
          <Field label="上下文窗口" hint="留空表示未知；保存后目录刷新对 override 行不再覆盖。">
            <input type="number" value={emCtx} onChange={(e) => setEmCtx(e.target.value)} />
          </Field>
          <label className="checkbox-line">
            <input type="checkbox" checked={emExcluded} onChange={(e) => setEmExcluded(e.target.checked)} />
            从候选中排除
          </label>
        </div>
      </PoolModal>

      {/* 停用确认 */}
      {disableTarget && (
        <ConfirmDialog
          open
          title={`确认停用供应商 ${disableTarget.name}`}
          impact={[
            `供应商 ${disableTarget.name}（${disableTarget.id}）立即停止服务`,
            '所有依赖该供应商的候选在下一次请求时不可用',
            '依赖它的凭据不会被删除，重新启用即可恢复'
          ]}
          irreversibility="可恢复（重新启用），但期间依赖它的请求已经失败。"
          targetName={disableTarget.name}
          inputLabel="请输入供应商名称以确认"
          confirmLabel="确认停用"
          busy={busy}
          error={error}
          onCancel={() => setDisableTarget(null)}
          onConfirm={async () => {
            setBusy(true)
            setError(null)
            try {
              await setEnabled(disableTarget, false)
              setDisableTarget(null)
            } catch (e) {
              setError(e)
            } finally {
              setBusy(false)
            }
          }}
        />
      )}

      {/* 移除确认（不可逆撤销） */}
      {removeTarget && (
        <ConfirmDialog
          open
          title={`确认移除供应商 ${removeTarget.name}`}
          impact={[
            `供应商 ${removeTarget.name}（${removeTarget.id}）的密钥材料被销毁，无法恢复`,
            '该来源的全部凭据立即退出候选池，不再被任何请求使用',
            '该来源的真实模型从候选集中移除',
            '想再用它只能重新提交凭据并重新走一遍鉴定'
          ]}
          irreversibility="不可逆：密钥材料被覆写，无法恢复；只能重新贡献。"
          targetName={removeTarget.name}
          inputLabel="请输入供应商名称以确认"
          confirmLabel="确认移除"
          busy={busy}
          error={error}
          onCancel={() => setRemoveTarget(null)}
          onConfirm={async () => {
            setBusy(true)
            setError(null)
            try {
              await removeSource(removeTarget)
              setRemoveTarget(null)
              setNotice(`已移除供应商 ${removeTarget.name}`)
            } catch (e) {
              setError(e)
            } finally {
              setBusy(false)
            }
          }}
        />
      )}

      {/* 余额刷新结果走右下角 toast：行内不再有「✓ 已更新」提示 */}
      <Toast
        open={balNote !== null}
        tone={balNote?.ok ? 'ok' : 'err'}
        onClose={() => setBalNote(null)}
      >
        {balNote?.text}
      </Toast>
    </PageFrame>
  )
}

export function CredentialDetailPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const q = useApi(() => getCredential(id), [id])
  const [banning, setBanning] = useState(false)
  const [notifyText, setNotifyText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const c: AdminCredential | undefined = q.data

  if (!c) {
    return (
      <PageFrame def={def}>
        <AsyncView state={q} loadingRows={4}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  async function action(fn: () => Promise<unknown>, message: string) {
    setBusy(true)
    setError(null)
    try {
      await fn()
      setNotice(message)
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame def={def} actions={<Link to="/admin/credentials" className="icon-link"><IconBack size={ACTION_ICON} />返回凭据池</Link>}>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <Section title="凭据信息">
        <KeyValues
          items={[
            { label: '凭据 ID', value: <code>{c.id}</code> },
            { label: '归属账户', value: <code>{c.owner_account_id}</code> },
            { label: '供应商', value: c.provider },
            { label: '外部 ID / 标签', value: `${c.external_id} / ${c.external_label ?? '—'}` },
            { label: '状态', value: <StatusBadge status={c.status} label={statusLabel(c.status)} /> },
            { label: '登录可用', value: c.login_enabled ? '是（可用于 OAuth 登录）' : '否' },
            { label: '鉴定判定', value: <StatusBadge status={c.verify_status} label={statusLabel(c.verify_status)} /> },
            { label: '创建 / 最后使用', value: `${fmtTime(c.created_at)} / ${fmtTime(c.last_used_at)}` }
          ]}
        />
        <Banner tone="muted">明文永不显示：服务端不保存可逆明文给界面，任何「再显示一次」的实现都会破坏这条约束。</Banner>
      </Section>

      <Section title="鉴定证据">
        {c.verify_detail ? <JsonDetails summary="查看鉴定明细" value={c.verify_detail} /> : <p className="muted">暂无鉴定明细。</p>}
      </Section>

      <Section title="处置">
        <div className="inline-links">
          <button type="button" className="btn-ghost" onClick={() => void action(() => suspendCredential(c.id), '凭据已暂停')} disabled={busy}>
            暂停
          </button>
          <button type="button" className="btn-danger" onClick={() => setBanning(true)} disabled={busy || c.status === 'disabled'}>
            封禁…
          </button>
        </div>
        <p className="muted">
          暂停 = 候选级熔断的手动版（过一会可能恢复）；封禁 = 永久不可用（鉴定未通过或被上游封）。
          没有删除：封禁保留 owner 归因与审计。
        </p>
        <Field label="通知贡献者" hint="站内提示；内容会进审计摘要。">
          <input value={notifyText} onChange={(e) => setNotifyText(e.target.value)} placeholder="例如：你的凭据暂时被上游限流，请稍后恢复。" />
        </Field>
        <button
          type="button"
          className="btn-ghost"
          onClick={() => void action(() => notifyCredential(c.id, notifyText), '已通知贡献者')}
          disabled={busy || !notifyText.trim()}
        >
          发送通知
        </button>
      </Section>

      <ConfirmDialog
        open={banning}
        title={`确认封禁凭据 ${c.external_label ?? c.id}`}
        impact={[
          `凭据 ${c.id}（${c.provider}）永久不可用`,
          '依赖它的候选立即失去这个实例；若这是唯一实例，相关抽象模型可能返回 no_candidate',
          '贡献者端会看到状态变化与（若已填）通知'
        ]}
        irreversibility="封禁可解除（后台状态改回启用），但期间该凭据已不再参与路由。"
        targetName={c.external_label ?? c.id}
        inputLabel="请输入凭据标签或 ID 以确认"
        confirmLabel="确认封禁"
        busy={busy}
        error={error}
        onCancel={() => setBanning(false)}
        onConfirm={async () => {
          await action(() => banCredential(c.id), '凭据已封禁')
          setBanning(false)
        }}
      />
    </PageFrame>
  )
}
