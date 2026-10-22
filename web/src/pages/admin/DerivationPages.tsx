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
import {
  getAbstractModel,
  getCandidates,
  getChain,
  listAbstractModels,
  listRealModels,
  patchAbstractModel,
  putCandidates,
  putDeprecation
} from '../../api/endpoints'
import type {
  AdminAbstractModel,
  Candidate,
  ChainPreview,
  DeprecationInput
} from '../../api/types'
import { useApi } from '../../hooks/useApi'
import ConfirmDialog from '../../components/ConfirmDialog'
import {
  AsyncView,
  Badge,
  Banner,
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

const CHANNEL_SCOPES = [
  ['all', '全部渠道'],
  ['relay', '仅 relay'],
  ['vendor', '仅 vendor'],
  ['selfhosted', '仅 selfhosted']
] as const

export function CandidatesPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const modelQ = useApi(() => getAbstractModel(id), [id])
  const candidatesQ = useApi(() => getCandidates(id), [id])
  const realQ = useApi(() => listRealModels({ limit: 300 }), [])

  const [rows, setRows] = useState<Candidate[]>([])
  const [original, setOriginal] = useState<Candidate[]>([])
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
  const realModels = realQ.data?.data ?? []
  const changed = JSON.stringify(rows) !== JSON.stringify(original)

  function update(i: number, patch: Partial<Candidate>) {
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))
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
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  if (!model) {
    return (
      <PageFrame def={def}>
        <AsyncView state={modelQ} loadingRows={4}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  return (
    <PageFrame
      def={def}
      actions={
        <span className="inline-links">
          <Link to={`/admin/abstract-models/${encodeURIComponent(id)}`}>返回编辑</Link>
          <Link to={`/admin/abstract-models/${encodeURIComponent(id)}/deprecation`}>弃用配置</Link>
        </span>
      }
    >
      <Banner tone="muted">
        候选集只声明「真实模型 × 渠道范围 × 约束」，不绑定凭据；真正的展开在运行时。
        因此每行都显示可用实例数：0 表示这条候选当前没有任何凭据支撑。
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <AsyncView state={candidatesQ} loadingRows={4}>
        {() => (
          <Section
            title={`候选集（${rows.length} 行）`}
            actions={
              <button
                type="button"
                onClick={() =>
                  setRows([
                    ...rows,
                    { real_model_id: realModels[0]?.id ?? '', constraint: { channel_scope: 'all' }, weight: 100 }
                  ])
                }
                disabled={realModels.length === 0}
              >
                添加候选
              </button>
            }
            hint="权重只在排序链显式含 round-robin 时生效；默认链不使用权重。"
          >
            {rows.map((r, i) => (
              <div className="candidate-row" key={`${r.real_model_id}-${i}`}>
                <Field label="真实模型" required>
                  <select value={r.real_model_id} onChange={(e) => update(i, { real_model_id: e.target.value })}>
                    <option value="">选择真实模型</option>
                    {realModels
                      .filter((m) => !m.excluded_from_candidates)
                      .map((m) => (
                        <option key={m.id} value={m.id}>
                          {m.qualified_name}
                        </option>
                      ))}
                  </select>
                </Field>
                <Field label="渠道范围">
                  <select
                    value={r.constraint?.channel_scope ?? 'all'}
                    onChange={(e) =>
                      update(i, {
                        constraint: { ...(r.constraint ?? {}), channel_scope: e.target.value }
                      })
                    }
                  >
                    {CHANNEL_SCOPES.map(([v, l]) => (
                      <option key={v} value={v}>{l}</option>
                    ))}
                  </select>
                </Field>
                <Field label="权重" hint="仅 round-robin 平局决胜使用">
                  <input
                    type="number"
                    min={0}
                    value={r.weight}
                    onChange={(e) => update(i, { weight: Number(e.target.value) })}
                  />
                </Field>
                <Field label="可用实例（只读）">
                  <input value={fmtNum(r.available_instances ?? 0)} disabled />
                </Field>
                <button type="button" className="btn-ghost" onClick={() => setRows(rows.filter((_, idx) => idx !== i))}>
                  移除
                </button>
              </div>
            ))}
            {rows.length === 0 && (
              <p className="muted">候选集为空：该抽象模型的请求会立即返回 <code>no_candidate</code>。</p>
            )}
            <button type="button" className="btn-primary" onClick={() => setConfirming(true)} disabled={!!busy}>
              保存候选集…
            </button>
            {!changed && <p className="muted">没有未保存的变更。</p>}
          </Section>
        )}
      </AsyncView>

      <Section title="候选集口径">
        <ul className="plain-list">
          <li>替换是整表语义：空集合 = 清空，不是「不改」。</li>
          <li><code>aggregate/*</code> 默认不可选：与上游策略叠加会让问题无法归因。</li>
          <li>可用实例数是运行时展开结果，请求里给值会被忽略。</li>
          <li>增删候选与调权重属于「改抽象模型」：保存前需要二次确认。</li>
        </ul>
        <KeyValues
          items={[
            { label: '抽象模型', value: <code>{model.name}</code> },
            { label: '状态', value: <StatusBadge status={model.status} label={statusLabel(model.status)} /> },
            { label: '当前可用候选', value: fmtNum(model.available_candidate_count) }
          ]}
        />
      </Section>

      <ConfirmDialog
        open={confirming}
        title={`确认替换抽象模型 ${model.name} 的候选集`}
        impact={[
          `候选行数：${original.length} → ${rows.length}`,
          `可用候选：${fmtNum(model.available_candidate_count)} → 由服务端重新展开`,
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
