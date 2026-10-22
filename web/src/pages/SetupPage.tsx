// 初始化向导（docs/ui/01-ia.md 4.3 末行）：库中 0 账户时唯一可见页面。
// 行为契约：
//   - setup_required=false（已有任何账户）→ 直接去登录页，向导关闭
//     （防二次初始化劫持，internal/httpapi/handlers_setup.go 同款原则）；
//   - 密码前端先做 ≥12 位校验（后端 400 invalid_request 兜底，双保险不冲突）；
//   - 提交成功 201 → 去登录页（后端刻意不发 Cookie：创建账户与建立登录态
//     是两步，登录路径上的审计与限速不因向导被绕过）；
//   - 409 conflict → 已被初始化，引导去登录。

import { useEffect, useState, type FormEvent } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { ApiError, getSetupStatus, postSetupInit } from '../api/client'
import PageFrame from './PageFrame'

export default function SetupPage({ def }: { def: import('../routes').PageDef }) {
  const [status, setStatus] = useState<'checking' | 'required' | 'done'>('checking')
  const [username, setUsername] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const navigate = useNavigate()
  const location = useLocation()

  useEffect(() => {
    let alive = true
    getSetupStatus()
      .then((s) => { if (alive) setStatus(s.setup_required ? 'required' : 'done') })
      .catch((e) => { if (alive) setError(e instanceof ApiError ? e.message : '初始化状态查询失败') })
    return () => { alive = false }
  }, [])

  if (status === 'checking') return <PageFrame def={def}><p className="placeholder-note">正在检查初始化状态…</p></PageFrame>
  if (status === 'done') return <Navigate to="/login" replace state={{ inited: true, from: (location.state as { from?: string } | null)?.from }} />

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    if (password.length < 12) {
      setError('口令至少 12 位（后端同样校验，这里先拦一道省一次往返）')
      return
    }
    setSubmitting(true)
    try {
      await postSetupInit({ username, display_name: displayName || username, password })
      navigate('/login', { state: { inited: true } })
    } catch (err) {
      if (err instanceof ApiError && err.code === 'conflict') {
        // 已有账户：向导已关闭，正常走登录
        navigate('/login', { state: { inited: true } })
        return
      }
      setError(err instanceof ApiError ? err.message : '初始化失败，请重试')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <PageFrame def={def}>
      <form className="login-card" onSubmit={onSubmit}>
        <label>管理员用户名 <span aria-hidden="true">*</span>
          <input value={username} onInput={(e) => setUsername((e.target as HTMLInputElement).value)} required autoComplete="username" />
        </label>
        <label>显示名（可留空，默认同用户名）
          <input value={displayName} onInput={(e) => setDisplayName((e.target as HTMLInputElement).value)} />
        </label>
        <label>口令 <span aria-hidden="true">*</span>（至少 12 位）
          <input type="password" value={password} onInput={(e) => setPassword((e.target as HTMLInputElement).value)} required minLength={12} autoComplete="new-password" />
        </label>
        {error && <p role="alert" className="form-error">{error}</p>}
        {/* 06-design.md 3.2：提交中禁用 + 文案变化，位置不动 */}
        <button type="submit" disabled={submitting}>{submitting ? '正在创建…' : '创建首个管理员'}</button>
      </form>
    </PageFrame>
  )
}
