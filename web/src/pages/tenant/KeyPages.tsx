// 租户端 API Key（docs/ui/04-tenant.md 第 3 节）。
//
// 三条硬约束：
//   - 列表只显示前缀（fr-xxxx…xxxx）；明文只在创建时出现一次；
//   - Key 没有自己的额度与速率：额度/速率是**账户级**的，Key 只剩
//     「认证、模型黑名单、生命周期」三件事；
//   - 黑名单默认为空 = 不收窄（组授权全集直接可用），2026-11-01 用户定案。
//
// 端点说明：租户面在 docs/api 尚无分册，路径按 REST 形状暂定；未接入时
// 页面走「接口尚未接入」态。

import { useEffect, useState, type FormEvent } from 'react'
import { IconBack } from '../../icons'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  createKey,
  getKey,
  getUsageOverview,
  listAvailableModels,
  listKeys,
  listUsageLogs,
  patchKey,
  revokeKey,
  setKeyEnabled
} from '../../api/endpoints'
import type { ApiKey, ApiKeyCreated, AvailableModel } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import ConfirmDialog from '../../components/ConfirmDialog'
import { ACTION_ICON, TableAction, TableActions } from '../../components/IconButtons'
import { IconDelete, IconEdit, IconPower } from '../../icons'
import OneTimeSecret from '../../components/OneTimeSecret'
import {
  AsyncView,
  Banner,
  DataTable,
  EmptyState,
  Field,
  FormError,
  KeyValues,
  Section,
  StatusBadge
} from '../../components/ui'
import { fmtNum, fmtTime, statusLabel, truncate } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

/** 新建/编辑共用的黑名单多选（勾选 = 对这把 Key 隐藏该模型）。 */
function DenylistPicker({
  models,
  value,
  onChange
}: {
  models: AvailableModel[]
  value: string[]
  onChange: (v: string[]) => void
}) {
  if (models.length === 0) {
    return (
      <p className="muted">
        当前没有可选的抽象模型。你所属的组可能还没有授权任何模型——请联系管理员。
      </p>
    )
  }
  return (
    <div className="checkbox-grid">
      {models.map((m) => (
        <label key={m.name} className="checkbox-line">
          <input
            type="checkbox"
            checked={value.includes(m.name)}
            onChange={() =>
              onChange(value.includes(m.name) ? value.filter((x) => x !== m.name) : [...value, m.name])
            }
          />
          <code>{m.name}</code>
          <span className="muted">{m.display_name}</span>
          {m.status === 'deprecated' && <span className="badge badge-warn">已弃用</span>}
        </label>
      ))}
    </div>
  )
}

/**
 * 后端 RFC3339（UTC）→ datetime-local 的 value（本地时区）。
 *
 * 为什么必须换算：datetime-local 的 value 不带时区，浏览器按**本地时间**解释。
 * 直接把后端的 UTC 串 slice(0,16) 填进去，等于把 UTC 时刻当本地时刻；用户不改
 * 时间直接保存就会把过期时间整体平移一个时区偏移（+08:00 下少 8 小时）。
 */
function isoToLocalInput(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  // 把本地时刻换算成“假装是 UTC”的时刻，再取 YYYY-MM-DDTHH:mm 前 16 字符。
  return new Date(d.getTime() - d.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
}

/**
 * datetime-local 的值（本地时区）→ 请求体里的 expires_at；空 = 清空（发 null）。
 *
 * 返回 null 是**有意的三态里的“清空”**，不是“没填”：后端 PATCH 的 expires_at
 * 约定是「字段缺省=不变、null=清空、字符串=设定」，而这里始终显式发这个键，
 * 所以空表单 = 清空过期时间（永不过期）。
 */
function expiryToISO(local: string): string | null {
  if (!local) return null
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? null : d.toISOString()
}

/** 列表/详情：过期时间展示（未设 = 永不过期）。 */
function expiryText(v: string | null | undefined): string {
  return v ? fmtTime(v) : '永不过期'
}

// ── 列表 ───────────────────────────────────────────────────────────────

export function KeysPage({ def }: PageProps) {
  const q = useApi(() => listKeys({ limit: 100 }), [])
  const models = useApi(() => listAvailableModels(), [])
  const [editing, setEditing] = useState<ApiKey | null>(null)
  const [name, setName] = useState('')
  const [expiry, setExpiry] = useState('')
  const [denylist, setDenylist] = useState<string[]>([])
  const [revoking, setRevoking] = useState<ApiKey | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    if (!editing) return
    setName(editing.name)
    setExpiry(editing.expires_at ? isoToLocalInput(editing.expires_at) : '')
    setDenylist(editing.denylist ?? [])
  }, [editing])

  async function saveEdit() {
    if (!editing) return
    setBusy(true)
    setError(null)
    try {
      // expires_at 三态：缺省=不变、null=清空、字符串=设定。这里必须显式传这个键
      // （留空时值为 null）：省掉它就变成“不变”，过期时间依旧清不掉（审计 P1）。
      await patchKey(editing.id, { name, expires_at: expiryToISO(expiry), denylist })
      setNotice('已保存（黑名单立即生效）')
      setEditing(null)
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function toggle(k: ApiKey) {
    setBusy(true)
    setError(null)
    try {
      await setKeyEnabled(k.id, k.status !== 'active')
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame
      def={def}
      actions={<Link className="btn-primary" to="/usage/keys/new">新建 API Key</Link>}
    >
      <Banner tone="muted">
        额度与速率属于**账户**，名下所有 Key 共享；Key 只提供模型黑名单（按 Key 隐藏个别模型）与生命周期管理。
        列表只显示前缀，明文只在创建时出现一次。
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      {editing && (
        <Section
          title={`编辑 Key ${editing.name}`}
          actions={<button type="button" className="btn-ghost" onClick={() => setEditing(null)}>取消</button>}
          hint="黑名单只可能让这把 Key 的能力变小；组授权仍是天花板。"
        >
          <div className="field-row">
            <Field label="名称" required>
              <input value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label="过期时间" hint="留空保存 = 清除过期时间（永不过期）">
              <input type="datetime-local" value={expiry} onChange={(e) => setExpiry(e.target.value)} />
            </Field>
          </div>
          <DenylistPicker models={models.data?.data ?? []} value={denylist} onChange={setDenylist} />
          <button type="button" className="btn-primary" onClick={() => void saveEdit()} disabled={busy || !name.trim()}>
            保存
          </button>
        </Section>
      )}

      <AsyncView
        state={q}
        loadingRows={5}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="还没有任何 API Key"
              hint="新建的 Key 默认黑名单为空 = 组授权全集直接可用；额度是账户级的，不随 Key 变化。"
              action={<Link className="btn-primary" to="/usage/keys/new">新建 API Key</Link>}
            />
          ) : null
        }
      >
        {(d) => (
          <Section title={`我的 Key（${d.total ?? d.data.length}）`}>
            <DataTable headers={['名称', '前缀', '状态', '过期时间', '模型黑名单', '最近使用', '创建时间', '操作']}>
              {d.data.map((k) => (
                <tr key={k.id}>
                  <td><Link to={`/usage/keys/${encodeURIComponent(k.id)}`}>{k.name}</Link></td>
                  <td><code>{k.prefix}</code></td>
                  <td><StatusBadge status={k.status} label={statusLabel(k.status)} /></td>
                  <td>{expiryText(k.expires_at)}</td>
                  <td title={k.denylist.join(', ')}>
                    {k.denylist.length === 0 ? (
                      <span className="badge badge-warn">空（不能调用）</span>
                    ) : (
                      `${k.denylist.length} 个：${truncate(k.denylist.join(', '), 32)}`
                    )}
                  </td>
                  <td>{fmtTime(k.last_used_at)}</td>
                  <td>{fmtTime(k.created_at)}</td>
                  <td className="cell-actions">
                    <TableActions>
                      <TableAction
                        icon={<IconEdit size={ACTION_ICON} />}
                        label="编辑 Key（黑名单 / 过期 / 名称）"
                        onClick={() => setEditing(k)}
                      />
                      <TableAction
                        icon={<IconPower size={ACTION_ICON} />}
                        label={k.status === 'active' ? '禁用（可恢复）' : '启用'}
                        busy={busy}
                        onClick={() => void toggle(k)}
                      />
                      <TableAction
                        icon={<IconDelete size={ACTION_ICON} />}
                        label="吊销（终局，不可恢复）"
                        tone="danger"
                        onClick={() => setRevoking(k)}
                      />
                    </TableActions>
                  </td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>

      <Section title="禁用与吊销的区别">
        <ul className="plain-list">
          <li>禁用：可恢复（只是临时停用），列表标红，该 Key 的请求返回 401。</li>
          <li>吊销：终局，不可恢复；界面需要二次确认。</li>
          <li>合并成一个「删除」会让用户不敢用这个功能，而留着一把不再使用的 Key 才是安全事件。</li>
        </ul>
      </Section>

      {revoking && (
        <ConfirmDialog
          open
          title={`确认吊销 Key ${revoking.name}`}
          impact={[
            `Key ${revoking.prefix} 立即失效，使用它的请求返回 401`,
            '此操作不可恢复；需要新 Key 请重新创建',
            `该 Key 的黑名单：${revoking.denylist.join(', ') || '空'}`
          ]}
          irreversibility="不可恢复：被吊销的 Key 无法重新启用。"
          targetName={revoking.name}
          inputLabel="请输入 Key 名称以确认"
          confirmLabel="确认吊销"
          busy={busy}
          error={error}
          onCancel={() => setRevoking(null)}
          onConfirm={async () => {
            setBusy(true)
            setError(null)
            try {
              await revokeKey(revoking.id)
              setRevoking(null)
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

// ── 创建（一次性展示）──────────────────────────────────────────────────

export function KeyNewPage({ def }: PageProps) {
  const navigate = useNavigate()
  const models = useApi(() => listAvailableModels(), [])
  const [name, setName] = useState('')
  const [expiry, setExpiry] = useState('')
  const [denylist, setDenylist] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [created, setCreated] = useState<ApiKeyCreated | null>(null)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const r = await createKey({ name, expires_at: expiryToISO(expiry), denylist })
      setCreated(r.data)
    } catch (e2) {
      setError(e2)
    } finally {
      setBusy(false)
    }
  }

  if (created) {
    return (
      <OneTimeSecret
        title="API Key 已创建"
        secret={created.plaintext}
        description="这是唯一一次显示完整 Key。列表此后只显示前缀；服务端只保存哈希，任何页面都不会再显示它。"
        filename={`api-key-${created.key.name || 'key'}.txt`}
        doneLabel="完成并返回列表"
        onDone={() => navigate('/usage/keys', { replace: true })}
      />
    )
  }

  return (
    <PageFrame def={def} actions={<Link to="/usage/keys" className="icon-link"><IconBack size={ACTION_ICON} />返回列表</Link>}>
      <Banner tone="muted" title="新建的 Key 默认黑名单为空">
        黑名单为空时这把 Key 可用你所属组的全部授权模型；勾选模型即对这把 Key 隐藏它。额度与速率属于账户，表单里不出现额度字段。
      </Banner>
      <form className="form-card" onSubmit={onSubmit}>
        <Field label="名称" required hint="你自己认得出的名字，例如「本地调试」。">
          <input value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <Field label="过期时间" hint="留空 = 永不过期">
          <input type="datetime-local" value={expiry} onChange={(e) => setExpiry(e.target.value)} />
        </Field>
        <div className="field-block">
          <span className="field-label">模型黑名单</span>
          <span className="field-hint">只能收窄，不能超出所属组的授权；不勾选 = 拒绝所有调用。</span>
          <AsyncView state={models} loadingRows={3}>
            {(d) => <DenylistPicker models={d.data} value={denylist} onChange={setDenylist} />}
          </AsyncView>
        </div>
        <FormError error={error} />
        <button type="submit" className="btn-primary" disabled={busy || !name.trim()}>
          {busy ? '正在创建…' : '创建 Key'}
        </button>
      </form>
      <Section title="创建之后">
        <ul className="plain-list">
          <li>明文只出现一次；请立刻复制或下载，并勾选「我已保存」。</li>
          <li>调用时用 <code>Authorization: Bearer &lt;key&gt;</code>；模型名只能用抽象模型名。</li>
          <li>额度是账户级的：多建 Key 不会增加额度。</li>
        </ul>
      </Section>
    </PageFrame>
  )
}

// ── 详情 ───────────────────────────────────────────────────────────────

export function KeyDetailPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const q = useApi(() => getKey(id), [id])
  const usage = useApi(() => getUsageOverview(), [])
  const logs = useApi(() => listUsageLogs({ key_id: id, limit: 20 }), [id])
  const [revoking, setRevoking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const key = q.data

  if (!key) {
    return (
      <PageFrame def={def}>
        <AsyncView state={q} loadingRows={4}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  async function toggle() {
    setBusy(true)
    setError(null)
    try {
      await setKeyEnabled(key!.id, key!.status !== 'active')
      setNotice(key!.status === 'active' ? 'Key 已禁用' : 'Key 已启用')
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame def={def} actions={<Link to="/usage/keys" className="icon-link"><IconBack size={ACTION_ICON} />返回列表</Link>}>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <Section title="Key 信息">
        <KeyValues
          items={[
            { label: '名称', value: key.name },
            { label: '前缀', value: <code>{key.prefix}</code> },
            { label: '状态', value: <StatusBadge status={key.status} label={statusLabel(key.status)} /> },
            { label: '过期时间', value: expiryText(key.expires_at) },
            { label: '创建时间', value: fmtTime(key.created_at) },
            { label: '最近使用', value: fmtTime(key.last_used_at) },
            {
              label: '模型黑名单',
              value:
                key.denylist.length === 0 ? (
                  <span className="badge badge-ok">空（不收窄）</span>
                ) : (
                  key.denylist.map((m) => (
                    <span key={m} className="badge badge-muted"><code>{m}</code></span>
                  ))
                )
            }
          ]}
        />
        <p className="muted">
          黑名单默认空 = 不收窄（你所属组的全部授权模型都可用）；列出的模型对这把 Key 隐藏。
          额度与速率属于整个账户，不在这把 Key 上。
        </p>
      </Section>

      <Section title="账户额度（只读）" hint="额度是账户级的：改这里改不动额度，进度条在「用量与配额」页。">
        <AsyncView state={usage} loadingRows={2}>
          {(u) => (
            <>
              {u.dimensions.length === 0 ? (
                <p className="muted">账户没有设置任何额度维度。</p>
              ) : (
                <ul className="plain-list">
                  {u.dimensions.map((d) => (
                    <li key={d.unit}>
                      {d.unit === 'request' ? '请求次数' : d.unit === 'token' ? 'Token' : 'Credits'}（
                      {d.period === 'day' ? '日' : '月'}）：{fmtNum(d.used)} / {fmtNum(d.limit)}
                    </li>
                  ))}
                </ul>
              )}
              <p className="muted">
                速率：RPM {u.rpm_limit === -1 ? '不限' : fmtNum(u.rpm_limit)} · TPM{' '}
                {u.tpm_limit === -1 ? '不限' : fmtNum(u.tpm_limit)} · 并发{' '}
                {u.max_concurrency === -1 ? '不限' : fmtNum(u.max_concurrency)}
              </p>
              <p>
                <Link to="/usage">查看完整用量</Link>
              </p>
            </>
          )}
        </AsyncView>
      </Section>

      <Section title="最近 20 次调用" hint="只含抽象模型名，不含真实模型与贡献者归因。">
        <AsyncView state={logs} loadingRows={3}>
          {(d) =>
            d.data.length === 0 ? (
              <p className="muted">还没有调用记录。</p>
            ) : (
              <DataTable headers={['时间', '抽象模型', '状态', '错误码', '输入 / 输出 token']}>
                {d.data.map((l) => (
                  <tr key={l.id}>
                    <td>{fmtTime(l.ts)}</td>
                    <td><code>{l.abstract_model}</code></td>
                    <td><StatusBadge status={l.ok ? 'ok' : 'error'} label={l.ok ? '成功' : '失败'} /></td>
                    <td>{l.error_code ?? '—'}</td>
                    <td>{fmtNum(l.input_tokens ?? 0)} / {fmtNum(l.output_tokens ?? 0)}</td>
                  </tr>
                ))}
              </DataTable>
            )
          }
        </AsyncView>
      </Section>

      <Section title="生命周期">
        <div className="inline-links">
          <button type="button" className="btn-ghost" onClick={() => void toggle()} disabled={busy}>
            {key.status === 'active' ? '禁用这把 Key' : '启用这把 Key'}
          </button>
          <button type="button" className="btn-danger" onClick={() => setRevoking(true)}>吊销…</button>
          <Link to={`/usage/logs`}>查看全部调用日志</Link>
        </div>
        <p className="muted">禁用可恢复；吊销不可恢复。吊销后这把 Key 的请求立即 401。</p>
      </Section>

      <ConfirmDialog
        open={revoking}
        title={`确认吊销 Key ${key.name}`}
        impact={[`Key ${key.prefix} 立即失效`, '不可恢复', '名下的调用历史保留（审计需要）']}
        irreversibility="不可恢复：吊销后无法重新启用。"
        targetName={key.name}
        inputLabel="请输入 Key 名称以确认"
        confirmLabel="确认吊销"
        busy={busy}
        error={error}
        onCancel={() => setRevoking(false)}
        onConfirm={async () => {
          setBusy(true)
          setError(null)
          try {
            await revokeKey(key.id)
            setRevoking(false)
            q.reload()
          } catch (e) {
            setError(e)
          } finally {
            setBusy(false)
          }
        }}
      />
    </PageFrame>
  )
}
