// 登录页：密码登录；错误文案遵循防枚举原则——
// 401 一律「用户名或口令错误」，不区分「用户不存在」（后端 LoginService
// 已把两条分支收敛为同码同文案同工作量，前端不得再区分开）。
// 429 锁定提示 + Retry-After 剩余秒数。

import { useState, type FormEvent } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { ApiError, postAuthLogin, type Session } from '../api/client'
import { useSession } from '../api/session'

export default function LoginPage() {
  const { session, setSession } = useSession()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [lockSeconds, setLockSeconds] = useState<number | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const navigate = useNavigate()
  const location = useLocation()
  // 初始化向导完成后跳过来的场合，给一句确认文案
  const justInited = (location.state as { inited?: boolean } | null)?.inited === true
  // 登录后回跳目标：守卫记录的原路径优先
  const from = (location.state as { from?: string } | null)?.from ?? '/'

  if (session) return <Navigate to={from} replace /> // 已登录不再展示登录表单

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const s: Session = await postAuthLogin({ username, password })
      setSession(s)
      navigate(from, { replace: true })
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === 'invalid_credentials') {
          setError('用户名或口令错误') // 防枚举：不区分用户是否存在
        } else if (err.code === 'too_many_attempts') {
          setLockSeconds(err.retryAfterSeconds ?? null)
          setError('尝试次数过多，账户已临时锁定')
        } else {
          setError(err.message)
        }
      } else {
        setError('登录失败，请重试')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section className="login-page">
      <h1>登录</h1>
      <p className="page-summary">密码登录；会话为 HttpOnly Cookie，浏览器自动管理。</p>
      {justInited && <p className="placeholder-note">初始化完成——请用刚创建的管理员账户登录。</p>}
      <form className="login-card" onSubmit={onSubmit}>
        <label>用户名 <span aria-hidden="true">*</span>
          <input value={username} onInput={(e) => setUsername((e.target as HTMLInputElement).value)} required autoComplete="username" />
        </label>
        <label>口令 <span aria-hidden="true">*</span>
          <input type="password" value={password} onInput={(e) => setPassword((e.target as HTMLInputElement).value)} required autoComplete="current-password" />
        </label>
        {error && (
          <p role="alert" className="form-error">
            {error}
            {lockSeconds !== null && `（约 ${lockSeconds} 秒后可再试）`}
          </p>
        )}
        <button type="submit" disabled={submitting}>{submitting ? '正在登录…' : '登录'}</button>
      </form>
    </section>
  )
}
