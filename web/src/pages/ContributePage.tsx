// 贡献入口（公共区）：`/contribute`、供应商授权、回调、恢复码兑换。
//
// 为什么贡献在公共区：众包供给是供给侧漏斗顶端，贡献即生效、无需审核
// （docs/ui/02-public.md 第 6 节）。把入口放在登录后才出现，等于用
// tenant 类的审核卡住了 contributor 类的能力。
//
// 三条界面义务：
//   1. 无偿声明是**阻断式确认**（复选框默认不勾选，未勾选不能提交）；
//   2. 凭据明文提交后永不回显，界面不提供任何「查看」；
//   3. 未登录贡献后的恢复路径按类型分叉：OAuth 型说明「用任意用过的账号
//      登录」，纯 Key 型一次性展示恢复码。

import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import {
  getContributeProviders,
  startOAuthContribution,
  submitApiKeyContribution,
  postContributeRecovery
} from '../api/endpoints'
import { useApi } from '../hooks/useApi'
import OneTimeSecret from '../components/OneTimeSecret'
import {
  AsyncView,
  Banner,
  EmptyState,
  ErrorState,
  Field,
  FormError,
  LoadingBlock,
  Section
} from '../components/ui'
import PageFrame from './PageFrame'
import type { PageProps } from '../routes'
import { FREE_NOTICE } from '../utils/contribute'


/** 无偿阻断式确认：复选框默认不勾选，未勾选时提交按钮不可用。 */
function FreeConfirm({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="checkbox-line free-confirm">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      我明白：贡献是无偿的，不提供额度、优先使用权或升级凭据作为回报
    </label>
  )
}

export default function ContributePage({ def }: PageProps) {
  const [freeConfirmed, setFreeConfirmed] = useState(false)
  const [endpoint, setEndpoint] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [label, setLabel] = useState('')
  const [claimedModels, setClaimedModels] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [result, setResult] = useState<{ recovery_code?: string; account_created?: boolean } | null>(null)
  const providers = useApi(() => getContributeProviders(), [])

  async function onSubmitKey(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const r = await submitApiKeyContribution({
        endpoint,
        api_key: apiKey,
        label,
        claimed_models: claimedModels
          .split(/[,，\s]+/)
          .map((s) => s.trim())
          .filter(Boolean)
      })
      setResult(r.data)
      setApiKey('') // 提交后立即从内存与输入框清掉
    } catch (err) {
      setError(err)
    } finally {
      setSubmitting(false)
    }
  }

  if (result?.recovery_code) {
    return (
      <OneTimeSecret
        title="已受理：请保存恢复码"
        secret={result.recovery_code}
        description={
          result.account_created
            ? '已为你创建一个仅含「贡献者」能力的账户。恢复码是回到这个账户的唯一方式，只显示一次。'
            : '凭据已受理。恢复码用于在丢失登录方式时回到这个账户，只显示一次。'
        }
        filename="freerouter-recovery-code.txt"
        onDone={() => setResult(null)}
        doneLabel="我已完成"
      />
    )
  }

  return (
    <PageFrame def={def}>
      <Banner tone="muted" title="贡献即生效，无需审核">
        {FREE_NOTICE}
      </Banner>

      <div className="cards-2">
        <Section title="贡献供应商账号" hint="选择供应商 → 跳转授权 → 回调到本站。授权成功后凭据入池，当前账户获得贡献者能力。">
          <AsyncView state={providers} loadingRows={3}>
            {(d) =>
              d.data.length === 0 ? (
                <EmptyState
                  title="当前没有可用的供应商"
                  hint="供应商授权服务尚未配置或暂时不可用。你仍然可以用下面的 API Key 方式贡献。"
                />
              ) : (
                <ul className="provider-grid">
                  {d.data.map((p) => (
                    <li key={p.id} className={p.available ? '' : 'provider-unavailable'}>
                      {p.available ? (
                        <Link
                          to={`/contribute/oauth/${encodeURIComponent(p.id)}`}
                          className="provider-link"
                          title={freeConfirmed ? `授权 ${p.name}` : '请先确认无偿声明'}
                          aria-disabled={!freeConfirmed}
                          onClick={(e) => {
                            if (!freeConfirmed) e.preventDefault()
                          }}
                        >
                          {p.name}
                        </Link>
                      ) : (
                        <span title={p.unavailable_reason ?? '当前不可用'}>{p.name}（不可用）</span>
                      )}
                    </li>
                  ))}
                </ul>
              )
            }
          </AsyncView>
          <FreeConfirm checked={freeConfirmed} onChange={setFreeConfirmed} />
        </Section>

        <Section title="贡献 API Key" hint="任意 OpenAI 兼容端点。提交后 Key 永不回显；请填写这个 Key 声称能提供的模型名，鉴定会核对声称与事实。">
          <form className="form-card" onSubmit={onSubmitKey}>
            <Field label="端点地址" required hint="该 Key 指向的 OpenAI 兼容 base URL，例如 https://api.example.com/v1">
              <input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} required placeholder="https://…" />
            </Field>
            <Field label="API Key" required hint="提交后不再显示，也不会出现在任何页面。">
              <input
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                required
                autoComplete="off"
              />
            </Field>
            <Field label="标签" required hint="你自己认得出的名字，例如「备用号」；凭据列表里只能靠它识别。">
              <input value={label} onChange={(e) => setLabel(e.target.value)} required />
            </Field>
            <Field label="声称的模型" required hint="用逗号分隔；鉴定检验的是「声称与事实是否相符」。">
              <input
                value={claimedModels}
                onChange={(e) => setClaimedModels(e.target.value)}
                required
                placeholder="例如 deepseek-chat, deepseek-reasoner"
              />
            </Field>
            <FreeConfirm checked={freeConfirmed} onChange={setFreeConfirmed} />
            <FormError error={error} />
            <button type="submit" className="btn-primary" disabled={!freeConfirmed || submitting}>
              {submitting ? '正在提交…' : '提交贡献'}
            </button>
          </form>
        </Section>
      </div>

      <Section title="未登录贡献后怎么回来">
        <ul className="plain-list">
          <li>供应商 OAuth 型：用任意一个你贡献过的账号登录即可回来。</li>
          <li>纯 API Key 型：提交成功后会展示一次恢复码；请立刻保存，它不会再次显示。</li>
          <li>已登录贡献：直接出现在「我的贡献」里，可随时暂停或撤销。</li>
        </ul>
        <p>
          <Link to="/contribute/recovery">已有恢复码？去兑换</Link>
        </p>
      </Section>
    </PageFrame>
  )
}

// ── 供应商 OAuth 跳转 ─────────────────────────────────────────────────

export function ContributeOAuthPage({ def }: PageProps) {
  const { provider = '' } = useParams<{ provider: string }>()
  const [error, setError] = useState<unknown>(null)
  const [starting, setStarting] = useState(true)

  useEffect(() => {
    let alive = true
    startOAuthContribution(provider)
      .then((r) => {
        if (!alive) return
        if (r.data.authorize_url) {
          window.location.assign(r.data.authorize_url)
          return
        }
        setError(new Error('服务端没有返回授权地址'))
        setStarting(false)
      })
      .catch((e) => {
        if (alive) {
          setError(e)
          setStarting(false)
        }
      })
    return () => {
      alive = false
    }
  }, [provider])

  return (
    <PageFrame def={def}>
      {starting ? (
        <LoadingBlock rows={2} label={`正在跳转 ${provider} 授权…`} />
      ) : (
        <>
          <ErrorState error={error} />
          <p>
            <Link to="/contribute">返回贡献入口</Link>
          </p>
        </>
      )}
    </PageFrame>
  )
}

// ── OAuth 回调落地 ────────────────────────────────────────────────────

export function ContributeCallbackPage({ def }: PageProps) {
  const [params] = useSearchParams()
  const accepted = params.get('accepted') === '1' || params.get('status') === 'accepted'
  const accountCreated = params.get('account_created') === '1'
  const recoveryCode = params.get('recovery_code')
  const message = params.get('message')

  if (recoveryCode) {
    return (
      <OneTimeSecret
        title="贡献成功：请保存恢复码"
        secret={recoveryCode}
        description={FREE_NOTICE}
        filename="freerouter-recovery-code.txt"
        onDone={() => undefined}
      />
    )
  }

  return (
    <PageFrame def={def}>
      {accepted ? (
        <Banner tone="ok" title="已受理">
          凭据已加入池子，鉴定会随后进行。{accountCreated && '已为你的贡献创建一个仅含贡献者能力的账户。'}
          {message && <span className="block-meta">{message}</span>}
        </Banner>
      ) : (
        <Banner tone="warn" title="授权没有完成">
          供应商没有返回可用的授权结果。你可以重新发起一次授权，或改用 API Key 方式贡献。
          {message && <span className="block-meta">{message}</span>}
        </Banner>
      )}
      <Section title="下一步">
        <ul className="plain-list">
          <li>已登录：去「我的贡献」查看鉴定进度。</li>
          <li>未登录且是 OAuth 型：用任意一个你贡献过的账号登录即可回来。</li>
        </ul>
        <p className="inline-links">
          <Link to="/contributions">我的贡献</Link>
          <Link to="/contribute">再贡献一个</Link>
          <Link to="/login">去登录</Link>
        </p>
      </Section>
    </PageFrame>
  )
}

// ── 恢复码兑换 ────────────────────────────────────────────────────────

export function ContributeRecoveryPage({ def }: PageProps) {
  const [code, setCode] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [done, setDone] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const navigate = useNavigate()

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      await postContributeRecovery(code.trim())
      setDone(true)
      window.setTimeout(() => navigate('/login', { replace: true }), 1200)
    } catch (err) {
      setError(err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <PageFrame def={def}>
      <Banner tone="muted">
        恢复码用于纯 API Key 型贡献者：这类账户没有密码，也没有可识别的上游账号。
        兑换成功后会回到贡献者上下文。
      </Banner>
      {done ? (
        <Banner tone="ok" title="恢复码已兑换">
          正在前往登录页…若没有自动跳转，<Link to="/login">点击这里</Link>。
        </Banner>
      ) : (
        <form className="form-card" onSubmit={onSubmit}>
          <Field label="恢复码" required hint="区分大小写；只可使用一次。">
            <input value={code} onChange={(e) => setCode(e.target.value)} required autoComplete="off" />
          </Field>
          <FormError error={error} />
          <button type="submit" className="btn-primary" disabled={submitting}>
            {submitting ? '正在验证…' : '兑换恢复码'}
          </button>
        </form>
      )}
      <Section title="找不到恢复码？">
        <p className="muted">
          OAuth 型贡献者可以直接用上游账号登录；纯 Key 型如果没有保存恢复码，只能联系管理员。
          这也是恢复码必须一次性展示并勾选「我已保存」的原因。
        </p>
      </Section>
    </PageFrame>
  )
}
