// 控制台落地页（路由 '/'）。两种形态：
//   - 未登录：引导卡（登录 / 初始化向导两个动作，IA 4.1 落地页职责）；
//   - 已登录：当前账户信息（display_name/classes/acting_class）+ 模型目录
//     占位 + 登出。登录后的功能页随后续里程碑逐页替换本组件。
// TODO(M5-front)：GET /v1/models 需要 API Key（Bearer），Key 管理在
//   /usage/keys 实装后接上；当前按任务要求显示占位说明。

import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiError, postAuthLogout, PATHS } from '../api/client'
import { useSession } from '../api/session'
import type { PageProps } from '../routes'
import PageFrame from './PageFrame'

const CLASS_LABELS: Record<string, string> = {
  admin: '管理',
  tenant: '使用',
  contributor: '贡献'
}

function ClassBadges({ classes }: { classes: string[] }) {
  if (classes.length === 0) return <span>（无权限类——只能贡献，IA 3.2 R4）</span>
  return (
    <>
      {classes.map((c) => (
        <span key={c} className="badge">{CLASS_LABELS[c] ?? c}</span>
      ))}
    </>
  )
}

export default function ConsolePage({ def }: PageProps) {
  const { session, setSession } = useSession()
  const navigate = useNavigate()
  const [loggingOut, setLoggingOut] = useState(false)
  const [logoutError, setLogoutError] = useState<string | null>(null)

  async function onLogout(e: FormEvent) {
    e.preventDefault()
    setLoggingOut(true)
    setLogoutError(null)
    try {
      await postAuthLogout()
      setSession(null)
      navigate('/login', { replace: true })
    } catch (err) {
      // 登出接口幂等且通常不会失败；网络故障时本地仍然清掉登录态，
      // 避免把用户困在已失效的会话里
      setLogoutError(err instanceof ApiError ? err.message : '登出请求失败，本地已清除登录态')
      setSession(null)
      navigate('/login', { replace: true })
    } finally {
      setLoggingOut(false)
    }
  }

  if (!session) {
    // 未登录落地页（IA 4.1：登录/注册/贡献三个动作；M9 只接了登录与向导）
    return (
      <PageFrame def={def}>
        <p className="placeholder-note">
          尚未登录。请 <a href="#/login">登录</a>；首次部署（还没有任何账户）时，
          <a href="#/setup"> 初始化向导</a> 会引导你创建第一个管理员。
        </p>
      </PageFrame>
    )
  }

  return (
    <PageFrame
      def={def}
      actions={
        <form onSubmit={onLogout} className="inline-form">
          <button type="submit" disabled={loggingOut}>{loggingOut ? '正在登出…' : '登出'}</button>
        </form>
      }
    >
      <section>
        <h2>当前账户</h2>
        <ul className="account-info">
          <li>显示名：<strong>{session.display_name}</strong></li>
          <li>账户 ID：<code>{session.account_id}</code></li>
          <li>权限类：<ClassBadges classes={session.classes} /></li>
          {session.acting_class && <li>当前操作类：<code>{session.acting_class}</code></li>}
          {logoutError && <li role="alert">{logoutError}</li>}
        </ul>
      </section>
      <section>
        <h2>模型目录</h2>
        <p className="placeholder-note">
          需要持有 API Key 才能列出可用抽象模型（GET <code>{PATHS.authSession}</code> 之外的
          调用面走 /v1/*，鉴权用 Key 不用会话）。Key 管理实装后此处显示你的可用模型。
        </p>
      </section>
    </PageFrame>
  )
}
