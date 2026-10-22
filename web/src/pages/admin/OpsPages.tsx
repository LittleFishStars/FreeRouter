// 上游与凭据（docs/ui/03-admin.md 第 8 节）。
//
// relay 类型没有 base_url：它的调用路径是 Node sidecar 直接调 providers-hub
// 的 provider adapter，不经过任何 HTTP 网关；给地址字段等于留一个必然失败
// 且会破坏归因的配置项。
//
// 凭据明文永不显示：界面连「查看」按钮都不提供，任何「点击查看」的实现
// 都会留下截图/缓存/审计日志的可利用路径，而便利为零。

import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  banCredential,
  changeUpstreamKind,
  createUpstream,
  getCredential,
  listCredentials,
  listUpstreams,
  notifyCredential,
  patchUpstream,
  suspendCredential,
  testUpstream
} from '../../api/endpoints'
import type { AdminCredential, AdminUpstream, UpstreamKind } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import ConfirmDialog from '../../components/ConfirmDialog'
import {
  AsyncView,
  Banner,
  DataTable,
  EmptyState,
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

const KIND_LABELS: Record<string, string> = {
  relay: 'relay（复用 providers-hub 池）',
  vendor: 'vendor（贡献者凭据直连官方）',
  selfhosted: 'selfhosted（自建兼容端点）'
}

export function UpstreamsPage({ def }: PageProps) {
  const q = useApi(() => listUpstreams({ limit: 200 }), [])
  const [creating, setCreating] = useState(false)
  const [kind, setKind] = useState<UpstreamKind>('vendor')
  const [name, setName] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [editing, setEditing] = useState<AdminUpstream | null>(null)
  const [editName, setEditName] = useState('')
  const [editBase, setEditBase] = useState('')
  const [kindTarget, setKindTarget] = useState<AdminUpstream | null>(null)
  const [pendingKind, setPendingKind] = useState<UpstreamKind>('vendor')
  const [disableTarget, setDisableTarget] = useState<AdminUpstream | null>(null)
  const [testResult, setTestResult] = useState<{ id: string; value: unknown } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    if (!editing) return
    setEditName(editing.name)
    setEditBase(editing.kind === 'relay' ? '' : editing.base_url)
  }, [editing])

  async function create() {
    setBusy(true)
    setError(null)
    try {
      await createUpstream({
        kind,
        name,
        base_url: kind === 'relay' ? undefined : baseURL,
        enabled: true
      })
      setCreating(false)
      setName('')
      setBaseURL('')
      setNotice('上游已创建')
      q.reload()
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
        ...(editing.kind === 'relay' ? {} : { base_url: editBase })
      })
      setEditing(null)
      setNotice('上游已保存')
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function setEnabled(u: AdminUpstream, enabled: boolean) {
    setBusy(true)
    setError(null)
    try {
      await patchUpstream(u.id, { enabled })
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function test(u: AdminUpstream) {
    setBusy(true)
    setError(null)
    setTestResult(null)
    try {
      const r = await testUpstream(u.id)
      setTestResult({ id: u.id, value: r.data })
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame
      def={def}
      actions={<button type="button" className="btn-primary" onClick={() => setCreating((v) => !v)}>{creating ? '收起' : '新建上游'}</button>}
    >
      <Banner tone="muted">
        relay 不填地址（sidecar 直连 provider adapter）；vendor 必填官方地址；selfhosted 填自建端点。
        禁用上游会立即让依赖它的候选不可用。
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      {creating && (
        <Section title="新建上游">
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
            {kind !== 'relay' && (
              <Field label="base_url" required hint="vendor 填官方地址；selfhosted 填自建兼容端点。">
                <input value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="https://…" />
              </Field>
            )}
            {kind === 'relay' && (
              <p className="muted">relay 由 sidecar 直连 providers-hub，不需要也不会接受地址。</p>
            )}
            <button type="button" className="btn-primary" onClick={() => void create()} disabled={busy || !name.trim() || (kind !== 'relay' && !baseURL.trim())}>
              {busy ? '正在创建…' : '创建'}
            </button>
          </div>
        </Section>
      )}

      {editing && (
        <Section title={`编辑上游 ${editing.name}`} actions={<button type="button" className="btn-ghost" onClick={() => setEditing(null)}>取消</button>}>
          <div className="field-row">
            <Field label="名称">
              <input value={editName} onChange={(e) => setEditName(e.target.value)} />
            </Field>
            {editing.kind !== 'relay' && (
              <Field label="base_url">
                <input value={editBase} onChange={(e) => setEditBase(e.target.value)} />
              </Field>
            )}
            <button type="button" className="btn-primary" onClick={() => void saveEdit()} disabled={busy}>
              保存
            </button>
          </div>
        </Section>
      )}

      {testResult && (
        <Section title="连通性测试结果" actions={<button type="button" className="btn-ghost" onClick={() => setTestResult(null)}>关闭</button>}>
          <JsonDetails summary="查看原始结果" value={testResult.value} />
        </Section>
      )}

      <AsyncView
        state={q}
        loadingRows={5}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState title="还没有上游" hint="配置 relay / vendor / selfhosted 渠道后，真实模型目录才能发现模型。" action={<button type="button" onClick={() => setCreating(true)}>新建上游</button>} />
          ) : null
        }
      >
        {(d) => (
          <Section title={`上游（${d.total ?? d.data.length}）`}>
            <DataTable headers={['类型', '名称', 'base_url', '归属', '启用', '最后连通', '操作']}>
              {d.data.map((u) => (
                <tr key={u.id}>
                  <td>{u.kind}</td>
                  <td>{u.name}</td>
                  <td className="cell-wrap">
                    {u.kind === 'relay' ? <span className="muted">sidecar 直连（无地址）</span> : <code>{u.base_url}</code>}
                  </td>
                  <td>{u.owner_account_id ? '贡献者凭据' : '平台自有'}</td>
                  <td>
                    <StatusBadge status={u.enabled ? 'active' : 'disabled'} label={u.enabled ? '启用' : '已禁用'} />
                  </td>
                  <td>{fmtTime(u.last_seen_at)}</td>
                  <td className="cell-actions">
                    <button type="button" className="btn-ghost" onClick={() => setEditing(u)}>编辑</button>
                    <button type="button" className="btn-ghost" onClick={() => void test(u)} disabled={busy}>测试</button>
                    {u.enabled ? (
                      <button type="button" className="btn-danger" onClick={() => setDisableTarget(u)}>禁用…</button>
                    ) : (
                      <button type="button" className="btn-ghost" onClick={() => void setEnabled(u, true)} disabled={busy}>启用</button>
                    )}
                    <button type="button" className="btn-ghost" onClick={() => { setKindTarget(u); setPendingKind(u.kind === 'vendor' ? 'selfhosted' : 'vendor') }}>
                      改类型…
                    </button>
                  </td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>

      <Section title="类型口径">
        <ul className="plain-list">
          <li>kind 的变更走独立端点并标危险：它改变的是「这条上游怎么被调用」，不是名字或开关。</li>
          <li>relay 调用路径全程不经过 HTTP 网关，填地址在保存前就会被拒绝。</li>
          <li>禁用前请确认当前有多少候选依赖该上游（确认框会要求输入名称）。</li>
          <li>secret_present 只告诉你有密文行，永远不会返回密文本身。</li>
        </ul>
      </Section>

      {disableTarget && (
        <ConfirmDialog
          open
          title={`确认禁用上游 ${disableTarget.name}`}
          impact={[
            `上游 ${disableTarget.name}（${disableTarget.id}）立即停止服务`,
            '所有依赖该上游的候选在下一次请求时不可用',
            '依赖它的凭据不会被删除，重新启用即可恢复'
          ]}
          irreversibility="可恢复（重新启用），但期间依赖它的请求已经失败。"
          targetName={disableTarget.name}
          inputLabel="请输入上游名称以确认"
          confirmLabel="确认禁用上游"
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

      {kindTarget && (
        <ConfirmDialog
          open
          title={`确认变更上游 ${kindTarget.name} 的调用形态`}
          impact={[
            `类型：${kindTarget.kind} → ${pendingKind}`,
            '所有依赖该上游的候选在下一次请求时走一条完全不同的代码路径',
            pendingKind === 'relay' ? '改为 relay 后将不接受 base_url' : '改为直连类型后需要填写 base_url'
          ]}
          irreversibility="可恢复（改回），但期间路由路径已改变，可能产生一批失败请求。"
          targetName={kindTarget.name}
          inputLabel="请输入上游名称以确认"
          confirmLabel="确认变更类型"
          busy={busy}
          error={error}
          onCancel={() => setKindTarget(null)}
          onConfirm={async () => {
            setBusy(true)
            setError(null)
            try {
              await changeUpstreamKind(kindTarget.id, pendingKind)
              setKindTarget(null)
              setNotice('上游类型已变更')
              q.reload()
            } catch (e) {
              setError(e)
            } finally {
              setBusy(false)
            }
          }}
        />
      )}
    </PageFrame>
  )
}

// ── 凭据池 ─────────────────────────────────────────────────────────────

export function CredentialsPage({ def }: PageProps) {
  const [status, setStatus] = useState('')
  const [verify, setVerify] = useState('')
  const q = useApi(
    () =>
      listCredentials({
        status: status || undefined,
        verify_status: verify || undefined,
        limit: 200
      }),
    [status, verify]
  )

  return (
    <PageFrame def={def}>
      <Banner tone="muted">
        凭据明文永不显示，界面也不提供「查看」。暂停与封禁是两个动作：暂停对应「过一会可能恢复」，
        封禁对应「被上游封了或鉴定未通过」。
      </Banner>
      <div className="toolbar">
        <label className="toolbar-field">
          状态
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">全部</option>
            <option value="pending">待鉴定</option>
            <option value="active">启用</option>
            <option value="disabled">已暂停/封禁</option>
            <option value="invalid">已失效</option>
            <option value="expired">已过期</option>
          </select>
        </label>
        <label className="toolbar-field">
          鉴定
          <select value={verify} onChange={(e) => setVerify(e.target.value)}>
            <option value="">全部</option>
            <option value="verified">已鉴定</option>
            <option value="trusted">官方来源</option>
            <option value="unverified">未鉴定</option>
            <option value="suspect">未通过</option>
          </select>
        </label>
        <button type="button" className="btn-ghost" onClick={q.reload}>刷新</button>
      </div>

      <AsyncView
        state={q}
        loadingRows={6}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState title="没有匹配的凭据" hint="贡献者提交的凭据与平台自有凭据都会出现在这里；调整筛选或等待新贡献。" />
          ) : null
        }
      >
        {(d) => (
          <Section title={`凭据（${d.total ?? d.data.length}）`}>
            <DataTable headers={['归属账户', '供应商', '外部标签', '状态', '登录可用', '鉴定', '创建时间', '最后使用', '']}>
              {d.data.map((c) => (
                <tr key={c.id}>
                  <td><code>{c.owner_account_id}</code></td>
                  <td>{c.provider}</td>
                  <td className="cell-wrap">{c.external_label ?? '—'}</td>
                  <td><StatusBadge status={c.status} label={statusLabel(c.status)} /></td>
                  <td>{c.login_enabled ? '是' : '否'}</td>
                  <td><StatusBadge status={c.verify_status} label={statusLabel(c.verify_status)} /></td>
                  <td>{fmtTime(c.created_at)}</td>
                  <td>{fmtTime(c.last_used_at)}</td>
                  <td><Link to={`/admin/credentials/${encodeURIComponent(c.id)}`}>详情</Link></td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>
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
    <PageFrame def={def} actions={<Link to="/admin/credentials">返回凭据池</Link>}>
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
