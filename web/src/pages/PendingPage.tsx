// 待审核提示 `/pending`（docs/ui/02-public.md 第 5 节）。
//
// 只有 pending 状态的账户会停在这里。三件事必须可达：
//   - 看清楚当前状态与已获得的权限；
//   - 贡献入口（不需要审核，立即生效）——把「等待」变成「有效动作」；
//   - 刷新状态与登出（不能把用户卡在一个没有出口的页面）。
//
// 不做自动轮询：这样不会把「审核队列置顶」的运营手段变成无声的后台流量。

import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { postAuthLogout } from '../api/client'
import { getAccountStatus } from '../api/endpoints'
import { useSession } from '../api/session'
import { Banner, Field, FormError, Section, StatusBadge } from '../components/ui'
import { classLabel, fmtTime, statusLabel } from '../utils/format'
import PageFrame from './PageFrame'
import type { PageProps } from '../routes'

export default function PendingPage({ def }: PageProps) {
  const { session, setSession, refresh } = useSession()
  const navigate = useNavigate()
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  async function refreshStatus() {
    setRefreshing(true)
    setError(null)
    setNotice(null)
    try {
      const s = await getAccountStatus()
      if (s.status !== 'pending') {
        setNotice(`状态已变为「${statusLabel(s.status)}」，正在刷新会话…`)
        await refresh()
      } else {
        setNotice('仍是「待审核」。管理员处理后，这里会显示最新状态。')
      }
    } catch (e) {
      // 状态端点未接入时退化为重新探测会话（登录态本身仍有效）
      setError(e)
      await refresh()
    } finally {
      setRefreshing(false)
    }
  }

  async function logout() {
    try {
      await postAuthLogout()
    } finally {
      setSession(null)
      navigate('/login', { replace: true })
    }
  }

  if (!session) return null

  return (
    <PageFrame def={def} actions={<button type="button" className="btn-ghost" onClick={logout}>登出</button>}>
      <Banner tone="warn" title="你的账户正在等待管理员审核">
        显示名：<strong>{session.display_name}</strong>
        <span className="block-meta">当前状态：<StatusBadge status="pending" label={statusLabel('pending')} /></span>
        <span className="block-meta">
          已获得的能力：{session.classes.length === 0 ? '暂无（审核通过后会获得调用能力）' : session.classes.map(classLabel).join(' / ')}
        </span>
      </Banner>

      <Section title="在等待期间你可以">
        <p>
          <Link className="btn-primary" to="/contribute">
            贡献一个凭据
          </Link>
        </p>
        <p className="muted">
          贡献不需要审核、立即生效，并会给你「贡献者」能力；它不包含模型调用能力。
        </p>
      </Section>

      <Section
        title="审核状态"
        actions={
          <button type="button" onClick={refreshStatus} disabled={refreshing}>
            {refreshing ? '正在刷新…' : '刷新状态'}
          </button>
        }
        hint="我们不会自动轮询审核结果；请在你认为管理员处理过之后手动刷新。"
      >
        <FormError error={error} />
        {notice && <p role="status">{notice}</p>}
        <dl className="kv">
          <div className="kv-row">
            <dt>账户 ID</dt>
            <dd><code>{session.account_id}</code></dd>
          </div>
          <div className="kv-row">
            <dt>会话到期</dt>
            <dd>{fmtTime(session.expires_at)}</dd>
          </div>
        </dl>
        <Field label="提示">
          <p className="muted">审核只针对调用能力；贡献能力不受审核影响。</p>
        </Field>
      </Section>
    </PageFrame>
  )
}
