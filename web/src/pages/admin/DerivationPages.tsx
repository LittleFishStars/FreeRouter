// 抽象模型的派生页：弃用（docs/ui/03-admin.md 7.4）。
//
// 候选集编辑页（原 7.3）已于 2026-10-26 收编进 CandidateDialog 弹窗——
// 独立路由 /admin/abstract-models/:id/candidates 已删除，本页顶栏的
// 「候选集」入口改为按钮打开同一个编辑器（与抽象模型列表/编辑页一致）。
//
// 弃用是单向状态：目标必须是 active、能力逐项 ≥ 原模型、环检测兜底；
// 不提供「取消弃用」（要恢复只能新建一个同名的 active 模型）。

import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  getAbstractModel,
  getChain,
  listAbstractModels,
  patchAbstractModel,
  putDeprecation
} from '../../api/endpoints'
import type { AdminAbstractModel, ChainPreview, DeprecationInput } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import ConfirmDialog from '../../components/ConfirmDialog'
import { CandidateDialog } from './CandidateDialog'
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
import { fmtTime, statusLabel } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

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
  const [candOpen, setCandOpen] = useState(false)
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
          <button type="button" className="am-op-link" onClick={() => setCandOpen(true)}>候选集</button>
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

      {candOpen && (
        <CandidateDialog
          modelId={model.id}
          modelName={model.name}
          onClose={() => setCandOpen(false)}
          onSaved={() => modelQ.reload()}
        />
      )}
    </PageFrame>
  )
}
