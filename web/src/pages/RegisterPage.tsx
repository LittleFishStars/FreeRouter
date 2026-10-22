// 注册 `/register`（docs/ui/02-public.md 第 4 节）。
//
// 字段只有三个：显示名、密码、密码确认。MVP 不收集邮箱（P1 才有验证与找回）。
// 防枚举要求「显示名已存在」与成功响应表现一致——服务端负责把两条分支收敛，
// 前端不得据此给出「这个名字被占用了」的差异提示。
//
// 端点说明：POST /api/register 在 docs/api 尚未定稿（审批流后端也未交付），
// 路径按 REST 形状暂定；未接入时页面显示「尚未接入」而不是伪造成功。

import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ApiError, getSetupStatus, isUnavailable } from '../api/client'
import { postRegister } from '../api/endpoints'
import { useSession } from '../api/session'
import { Banner, Field, FormError, LoadingBlock, Section, UnavailableState } from '../components/ui'
import PageFrame from './PageFrame'
import type { PageProps } from '../routes'

export default function RegisterPage({ def }: PageProps) {
  const { session } = useSession()
  const navigate = useNavigate()
  const [registrationEnabled, setRegistrationEnabled] = useState<boolean | null>(null)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [fieldError, setFieldError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [unavailable, setUnavailable] = useState(false)

  useEffect(() => {
    getSetupStatus()
      .then((s) => setRegistrationEnabled(s.registration_enabled))
      .catch(() => setRegistrationEnabled(true)) // 状态接口失败不阻断表单；提交时再报错
  }, [])

  if (session) {
    // 已登录账户注册只会制造一个孤儿 pending 账户（02-public 第 5 节矩阵）
    return (
      <PageFrame def={def}>
        <Banner tone="muted" title="你已经登录">
          注册用于创建新账户；当前会话不会因此改变。如需为新账户走审核，请先登出。
          <Link to="/account"> 去账户概览</Link>
        </Banner>
      </PageFrame>
    )
  }

  if (registrationEnabled === null) {
    return (
      <PageFrame def={def}>
        <LoadingBlock rows={3} />
      </PageFrame>
    )
  }

  if (!registrationEnabled) {
    return (
      <PageFrame def={def}>
        <Banner tone="warn" title="本站当前未开放注册">
          管理员关闭了自助注册。请联系管理员创建账户，或先 <Link to="/contribute">贡献一个凭据</Link>。
        </Banner>
      </PageFrame>
    )
  }

  if (unavailable) {
    return (
      <PageFrame def={def}>
        <UnavailableState what="注册" retry={() => setUnavailable(false)} />
      </PageFrame>
    )
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setFieldError(null)
    if (password.length < 12) {
      setFieldError('口令至少 12 位。')
      return
    }
    if (password !== confirm) {
      setFieldError('两次输入的口令不一致。')
      return
    }
    setSubmitting(true)
    try {
      await postRegister({ username, display_name: username, password })
      // 显式跳待审核页：即便名字已存在，服务端也返回同样的表现（防枚举）
      navigate('/pending', { replace: true, state: { fresh: true } })
    } catch (err) {
      if (isUnavailable(err)) {
        setUnavailable(true)
        return
      }
      // 409/冲突的对外表现与成功一致（防枚举）：仍跳待审核页
      if (err instanceof ApiError && (err.status === 409 || err.code === 'conflict')) {
        navigate('/pending', { replace: true, state: { fresh: true } })
        return
      }
      setError(err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <PageFrame def={def}>
      <form className="form-card" onSubmit={onSubmit} noValidate>
        <Field label="显示名" required hint="登录用，也是审计里出现的名字。">
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            required
            autoComplete="username"
            name="username"
          />
        </Field>
        <Field label="口令" required hint="至少 12 位；服务端以 argon2id 存储。">
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            minLength={12}
            autoComplete="new-password"
            name="password"
          />
        </Field>
        <Field label="确认口令" required error={fieldError}>
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            required
            autoComplete="new-password"
            name="password-confirm"
          />
        </Field>
        <Banner tone="muted">
          注册后账户处于「待审核」。审核通过后可创建 API Key、调用模型；贡献凭据不需要审核。
        </Banner>
        <FormError error={error} />
        <button type="submit" className="btn-primary" disabled={submitting}>
          {submitting ? '正在提交…' : '注册'}
        </button>
        <p className="muted">
          已有账户？<Link to="/login">去登录</Link>
        </p>
      </form>
      <Section title="注册之后">
        <ul className="plain-list">
          <li>你的账户会先得到「调用者」能力，但需要管理员批准后才会生效。</li>
          <li>等待期间可以贡献凭据，贡献立即可用，无需审核。</li>
          <li>本站不收集邮箱；MVP 没有找回密码功能（见文档待定项）。</li>
        </ul>
      </Section>
    </PageFrame>
  )
}
