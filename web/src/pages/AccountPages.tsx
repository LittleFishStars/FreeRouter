// 账户自助：概览、改密、我的会话（docs/ui/01-ia.md 4.2）。
//
// 为什么不提供「改显示名」：显示名参与审核与审计的可读性，改名会让历史
// 记录难以对应；总纲未定义改名流程，因此不给入口（待定项）。
//
// 端点说明：/api/account/* 在 docs/api 尚未定稿，路径按 REST 形状暂定；
// 未接入时页面走「尚未接入」态。

import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ApiError } from '../api/client'
import {
  getAccountSessions,
  putAccountPassword,
  revokeAccountSession
} from '../api/endpoints'
import { useSession } from '../api/session'
import { useApi } from '../hooks/useApi'
import {
  AsyncView,
  Banner,
  DataTable,
  EmptyState,
  Field,
  FormError,
  KeyValues,
  LoadingBlock,
  Section,
  StatusBadge
} from '../components/ui'
import { classLabel, fmtTime, sessionExpiringSoon, statusLabel } from '../utils/format'
import PageFrame from './PageFrame'
import type { PageProps } from '../routes'

export default function AccountPage({ def }: PageProps) {
  const { session } = useSession()
  if (!session) return <LoadingBlock rows={3} />

  return (
    <PageFrame def={def} actions={<Link to="/account/password">自助改密</Link>}>
      <Section title="账户信息">
        <KeyValues
          items={[
            { label: '显示名', value: <strong>{session.display_name}</strong> },
            { label: '账户 ID', value: <code>{session.account_id}</code> },
            {
              label: '能力',
              value:
                session.classes.length === 0 ? (
                  <span className="muted">尚未获得任何权限类</span>
                ) : (
                  session.classes.map((c) => (
                    <span key={c} className="badge badge-muted">
                      {classLabel(c)}
                    </span>
                  ))
                )
            },
            ...(session.acting_class
              ? [{ label: '当前操作上下文', value: classLabel(session.acting_class) }]
              : []),
            { label: '账户状态', value: <StatusBadge status={session.status ?? 'active'} label={statusLabel(session.status ?? 'active')} /> },
            { label: '会话到期', value: fmtTime(session.expires_at) },
            {
              label: '最近登录',
              value: <span className="muted">由服务端记录；当前接口未提供该字段</span>
            }
          ]}
        />
      </Section>

      {sessionExpiringSoon(session.expires_at) && (
        <Banner tone="warn" title="登录即将过期">
          会话在 1 小时内到期。即将过期的会话不会自动续期——请在到期前保存工作，
          需要时重新登录。
        </Banner>
      )}

      <Section title="会话与安全">
        <p>
          <Link to="/account/sessions">查看我的会话</Link>（可单独吊销）。
        </p>
        <p className="muted">
          改密或管理员禁用会立即吊销全部会话（含当前会话）。会话是 HttpOnly Cookie，
          页面脚本无法读取或导出它。
        </p>
      </Section>

      <Section title="权限说明">
        <ul className="plain-list">
          <li>管理员：管理账户、组、模型、上游与全局设置。</li>
          <li>调用者：创建 API Key、调用抽象模型、查看自己的用量与日志。</li>
          <li>贡献者：贡献凭据并查看自己的贡献；不包含模型调用能力。</li>
        </ul>
        <p className="muted">权限只由账户决定，与登录方式（密码 / 上游 OAuth）无关。</p>
      </Section>
    </PageFrame>
  )
}

export function PasswordPage({ def }: PageProps) {
  const { session, setSession } = useSession()
  const navigate = useNavigate()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [fieldError, setFieldError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  if (!session) return <LoadingBlock rows={3} />

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setFieldError(null)
    if (next.length < 12) {
      setFieldError('新口令至少 12 位。')
      return
    }
    if (next !== confirm) {
      setFieldError('两次输入的新口令不一致。')
      return
    }
    setBusy(true)
    try {
      await putAccountPassword({ current_password: current, new_password: next })
      // 后端在改密时吊销全部会话；本地也必须清掉会话缓存并回登录页
      setSession(null)
      navigate('/login', { replace: true, state: { passwordChanged: true } })
    } catch (err) {
      setError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <PageFrame def={def}>
      <Banner tone="warn" title="改密会吊销全部会话">
        包括你当前正在使用的这个会话。提交成功后需要重新登录；其它设备上的登录会立即失效。
      </Banner>
      <form className="form-card" onSubmit={onSubmit}>
        <Field label="当前口令" required>
          <input
            type="password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            required
            autoComplete="current-password"
          />
        </Field>
        <Field label="新口令" required hint="至少 12 位。">
          <input
            type="password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            required
            minLength={12}
            autoComplete="new-password"
          />
        </Field>
        <Field label="确认新口令" required error={fieldError}>
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            required
            autoComplete="new-password"
          />
        </Field>
        <FormError error={error} />
        <button type="submit" className="btn-primary" disabled={busy}>
          {busy ? '正在提交…' : '改密并登出全部会话'}
        </button>
      </form>
      <p>
        <Link to="/account">返回账户概览</Link>
      </p>
    </PageFrame>
  )
}

export function SessionsPage({ def }: PageProps) {
  const q = useApi(() => getAccountSessions(), [])
  const [error, setError] = useState<unknown>(null)
  const [revoking, setRevoking] = useState<string | null>(null)

  async function revoke(id: string) {
    setRevoking(id)
    setError(null)
    try {
      await revokeAccountSession(id)
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setRevoking(null)
    }
  }

  return (
    <PageFrame def={def} actions={<Link to="/account">返回概览</Link>}>
      <FormError error={error} />
      <AsyncView
        state={q}
        loadingRows={4}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="没有可显示的会话"
              hint="会话接口返回空。若你确信已登录，请稍后刷新；这也可能意味着会话列表接口尚未接入。"
              action={<button type="button" onClick={q.reload}>刷新</button>}
            />
          ) : null
        }
      >
        {(d) => (
          <DataTable headers={['来源 IP', '浏览器', '登录时间', '过期时间', '']}>
            {d.data.map((s) => (
              <tr key={s.id}>
                <td>{s.ip ?? '—'}</td>
                <td className="cell-wrap">{s.ua ?? '—'}{s.current && <span className="badge badge-info">当前</span>}</td>
                <td>{fmtTime(s.created_at)}</td>
                <td>{fmtTime(s.expires_at)}</td>
                <td>
                  <button
                    type="button"
                    className="btn-ghost"
                    onClick={() => revoke(s.id)}
                    disabled={revoking === s.id}
                  >
                    {revoking === s.id ? '吊销中…' : '吊销'}
                  </button>
                </td>
              </tr>
            ))}
          </DataTable>
        )}
      </AsyncView>
      <Banner tone="muted">
        吊销当前会话会让这个浏览器立即回到未登录状态；吊销其它会话不影响当前操作。
      </Banner>
      {error instanceof ApiError && error.status === 401 && (
        <p className="muted">会话已失效，请重新登录。</p>
      )}
    </PageFrame>
  )
}
