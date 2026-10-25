// 全局设置 `/admin/settings`（docs/ui/03-admin.md 第 11 节）。
//
// 设置文档按分组返回（结构属于 config 域，前端不复制镜像）。这里按
// 值类型渲染控件，并且只提交**改动过的键**（PATCH 局部更新）。
//
// 五类危险项必须二次确认：
//   registration_enabled / global_budget_cap / trusted_proxies /
//   record_request_body / login_backoff + lockout_threshold。
// 二次确认是界面行为，接口不做额外验证。

import { useEffect, useMemo, useState } from 'react'
import { checkSettingsIp, getSettings, patchSettings } from '../../api/endpoints'
import type { IpCheckResult } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON, ToolButton } from '../../components/IconButtons'
import { IconCheck, IconSearch } from '../../icons'
import ConfirmDialog from '../../components/ConfirmDialog'
import {
  AsyncView,
  Banner,
  Field,
  FormError,
  JsonDetails,
  Section
} from '../../components/ui'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

// 危险项 = 改错会立即影响全站可用性或安全边界。这里只列**服务端真会返回**
// 的键：旧表里的 global_budget_cap / record_request_body / login_backoff /
// lockout_threshold 已经不在 /api/admin/settings 里，留着只会让顶部横幅
// 宣传一批不存在的项。
const DANGEROUS_KEYS = new Set([
  'registration_enabled',
  'registration_requires_approval',
  'trusted_proxies',
  'behind_proxy',
  'session_ttl'
])

// 服务端 /api/admin/settings 的真实键（internal/app/admin_settings_bridge.go 的
// GetSettings 是唯一事实来源）。之前的映射表里有多条**过期键名**
// （default_rpm_limit / default_tpm_limit / lockout_threshold…），永远命中不了，
// 界面上就一直显示 snake_case 原文——看起来像没做完。
const KEY_LABELS: Record<string, string> = {
  // security 组
  registration_enabled: '开放注册',
  registration_requires_approval: '注册需审核',
  session_ttl: '会话有效期',
  password_min_length: '口令最小长度',
  login_max_failures: '登录失败上限',
  login_lockout: '锁定时长',
  behind_proxy: '在反向代理之后',
  trusted_proxies: '可信代理地址',
  // limits 组
  default_rpm: '建账默认 RPM',
  default_tpm: '建账默认 TPM',
  default_daily_tokens: '建账默认日 token 上限',
  default_monthly_tokens: '建账默认月 token 上限',
  default_max_concurrency: '建账默认并发上限',
  default_keys_per_account: '每个账户的 Key 上限'
}

/** 分组中文名（服务端用 security / limits 做分组键）。 */
const GROUP_LABELS: Record<string, string> = {
  security: '安全与登录',
  limits: '默认限额（仅影响新建账户）'
}

/**
 * 值的单位说明。之所以要这个：-1 在 limits 里表示"不限"，0 表示"不可用"——
 * 两个特殊值不加注释，管理员改错一个就变成全站不可用或彻底放开。
 */
const VALUE_HINTS: Record<string, string> = {
  session_ttl: 'Go duration 写法，如 24h / 12h',
  login_lockout: 'Go duration 写法，如 15m',
  default_tpm: '-1 = 不限；0 = 不可用',
  default_rpm: '-1 = 不限；0 = 不可用',
  default_daily_tokens: '0 = 不限',
  default_monthly_tokens: '0 = 不限',
  password_min_length: '不得小于 8',
  trusted_proxies: '逗号分隔的 CIDR；不配则忽略 X-Forwarded-For'
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

type Draft = Record<string, unknown>

export default function SettingsPage({ def }: PageProps) {
  const q = useApi(() => getSettings(), [])
  const [draft, setDraft] = useState<Draft>({})
  const [original, setOriginal] = useState<Draft>({})
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [ip, setIp] = useState<IpCheckResult | null>(null)
  const [ipBusy, setIpBusy] = useState(false)

  // 后端文档是 {分组: {键: 值}}；拍平成 key→value 后统一渲染与提交
  useEffect(() => {
    if (!q.data) return
    const flat: Draft = {}
    for (const group of Object.values(q.data)) {
      if (!isPlainObject(group)) continue
      for (const [k, v] of Object.entries(group)) flat[k] = v
    }
    setDraft(flat)
    setOriginal(flat)
  }, [q.data])

  const changedKeys = useMemo(
    () => Object.keys(draft).filter((k) => JSON.stringify(draft[k]) !== JSON.stringify(original[k])),
    [draft, original]
  )
  const dangerousChanged = changedKeys.filter((k) => DANGEROUS_KEYS.has(k))

  async function checkIp() {
    setIpBusy(true)
    setError(null)
    try {
      const r = await checkSettingsIp()
      setIp(r.data)
    } catch (e) {
      setError(e)
    } finally {
      setIpBusy(false)
    }
  }

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const patch: Record<string, unknown> = {}
      for (const k of changedKeys) patch[k] = draft[k]
      const r = await patchSettings(patch)
      setNotice(`设置已保存（审计 ${r.audit_id ?? '—'}）`)
      setConfirming(false)
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
      actions={
        <span className="inline-links">
          <ToolButton
            icon={<IconSearch size={ACTION_ICON} />}
            busy={ipBusy}
            onClick={() => void checkIp()}
          >
            源 IP 自检
          </ToolButton>
          <ToolButton
            primary
            icon={<IconCheck size={ACTION_ICON} />}
            onClick={() => setConfirming(true)}
            disabled={changedKeys.length === 0 || busy}
          >
            保存设置
          </ToolButton>
        </span>
      }
    >
      <Banner tone="muted">
        修改会写入审计（逐键记录旧值 → 新值）。需要二次确认的危险项：
        {Array.from(DANGEROUS_KEYS)
          .map((k) => KEY_LABELS[k] ?? k)
          .join('、')}
        。管理面不提供 IP 允许列表——那是唯一能把管理员自己锁在门外的配置。
      </Banner>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      {ip && (
        <Section title="源 IP 自检" actions={<button type="button" className="btn-ghost" onClick={() => setIp(null)}>关闭</button>}>
          <p>
            服务端看到的源 IP：<code>{ip.source_ip}</code>；判定：
            <strong>{ip.allowed ? '会被正确解析' : '不会被正确解析'}</strong>；原因：{ip.reason}
          </p>
          <p className="muted">
            代理链路：{ip.behind_proxy ? '检测到代理头' : '未检测到代理头'}；可信代理：
            {ip.trusted_proxies?.length ? ip.trusted_proxies.join(', ') : '未配置'}。
          </p>
          <JsonDetails summary="查看自检原始结果" value={ip} />
        </Section>
      )}

      <AsyncView state={q} loadingRows={8}>
        {(doc) => (
          <>
            {Object.entries(doc).map(([group, values]) => (
              <Section
            key={group}
            title={GROUP_LABELS[group] ?? group}
            hint="这些是启动期配置项：改动需要重启才生效，所以本页只读展示当前生效值。"
          >
                {isPlainObject(values) ? (
                  Object.entries(values).map(([key, value]) => (
                    <SettingField
                      key={key}
                      name={key}
                      value={draft[key] ?? value}
                      dangerous={DANGEROUS_KEYS.has(key)}
                      onChange={(v) => setDraft((d) => ({ ...d, [key]: v }))}
                    />
                  ))
                ) : (
                  <JsonDetails summary="查看该分组原始值" value={values} />
                )}
              </Section>
            ))}
            {Object.keys(doc).length === 0 && (
              <Section title="设置">
                <p className="muted">服务端返回了空设置文档；这可能意味着设置端点尚未接线。</p>
              </Section>
            )}
          </>
        )}
      </AsyncView>

      <Section title="本次变更" hint="只提交改动过的键（PATCH 局部更新）；未改动的键不受影响。">
        {changedKeys.length === 0 ? (
          <p className="muted">没有未保存的变更。</p>
        ) : (
          <ul className="plain-list">
            {changedKeys.map((k) => (
              <li key={k}>
                <code>{k}</code>
                {DANGEROUS_KEYS.has(k) && <span className="badge badge-warn">危险项</span>}
                {' '}
                {String(JSON.stringify(original[k]))} → {String(JSON.stringify(draft[k]))}
              </li>
            ))}
          </ul>
        )}
        <p className="muted">
          恢复默认：本页没有独立端点；把值改回原值并保存即可抵消本次修改。
        </p>
      </Section>

      {confirming && (
        <ConfirmDialog
          open
          title="确认保存全局设置"
          impact={[
            `将提交 ${changedKeys.length} 个键的修改`,
            dangerousChanged.length > 0
              ? `其中危险项：${dangerousChanged.map((k) => KEY_LABELS[k] ?? k).join('、')}`
              : '本次没有危险项变更',
            // 每条后果描述都对着一个真实存在、且列表里出现过的键。
            // 写在这里而不是塞进 KEY_LABELS，是因为它是"后果"而不是"名称"。
            dangerousChanged.includes('registration_enabled')
              ? '关闭注册会立刻切断新增账户'
              : '',
            dangerousChanged.includes('registration_requires_approval')
              ? '关闭审核会让新注册账户在注册成功的瞬间就具备调用能力'
              : '',
            dangerousChanged.includes('trusted_proxies')
              ? '代理配置配错会让 IP 限速失效或误封'
              : '',
            dangerousChanged.includes('behind_proxy')
              ? '代理开关必须与可信代理列表配套，否则源 IP 判定会错'
              : '',
            dangerousChanged.includes('session_ttl')
              ? '会话有效期调长会把会话泄露的可利用窗口一起拉长'
              : ''
          ].filter(Boolean)}
          irreversibility="可恢复（再改回来），但设置写入后立即对全站生效，期间可能已放行/拦截请求。"
          targetName="settings"
          inputLabel="请输入 settings 以确认"
          confirmLabel="确认保存设置"
          busy={busy}
          error={error}
          onCancel={() => setConfirming(false)}
          onConfirm={() => void save()}
        />
      )}
    </PageFrame>
  )
}

function SettingField({
  name,
  value,
  dangerous,
  onChange
}: {
  name: string
  value: unknown
  dangerous: boolean
  onChange: (v: unknown) => void
}) {
  const label = KEY_LABELS[name] ?? name
  // 危险项的提示优先（它更紧急），否则给单位说明
  const valueHint = VALUE_HINTS[name]
  if (typeof value === 'boolean') {
    return (
      <label className={`checkbox-line${dangerous ? ' dangerous-field' : ''}`}>
        <input type="checkbox" checked={value} onChange={(e) => onChange(e.target.checked)} />
        {label}
        {dangerous && <span className="badge badge-warn">危险</span>}
      </label>
    )
  }
  if (typeof value === 'number') {
    return (
      <Field label={label} hint={dangerous ? '危险项：保存前会二次确认' : valueHint}>
        <input type="number" value={value} onChange={(e) => onChange(Number(e.target.value))} />
      </Field>
    )
  }
  if (typeof value === 'string') {
    return (
      <Field label={label} hint={dangerous ? '危险项：保存前会二次确认' : valueHint}>
        <input value={value} onChange={(e) => onChange(e.target.value)} />
      </Field>
    )
  }
  // 数组 / 对象 / null：以 JSON 文本编辑，保存前解析；解析失败保留原文
  return <JsonSettingField name={name} label={label} value={value} dangerous={dangerous} onChange={onChange} />
}

function JsonSettingField({
  name,
  label,
  value,
  dangerous,
  onChange
}: {
  name: string
  label: string
  value: unknown
  dangerous: boolean
  onChange: (v: unknown) => void
}) {
  const [text, setText] = useState(() => JSON.stringify(value, null, 2) ?? '')
  const [error, setError] = useState<string | null>(null)
  return (
    <Field
      label={label}
      hint={dangerous ? '危险项：保存前会二次确认' : 'JSON 值（数组 / 对象）'}
      error={error}
    >
      <textarea
        className="code-input"
        rows={4}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          try {
            onChange(e.target.value === '' ? null : JSON.parse(e.target.value))
            setError(null)
          } catch {
            setError('不是合法 JSON；修正后才会提交该键')
          }
        }}
        aria-label={`${name} JSON`}
      />
    </Field>
  )
}
