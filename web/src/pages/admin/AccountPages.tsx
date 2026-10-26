// 账户与审核：列表 / 详情 / 导入（docs/ui/03-admin.md 第 4 节）。
//
// 硬纪律：
//   - 列表只给 key_count / credential_count 数量，不给 Key 或凭据列表
//     （资源分表归属，01-ia.md 3.4）；
//   - 没有「删除账户」：物理删除会让 requests / audit_log 悬空，一律状态迁移；
//   - 改权限类、禁用、重置密码、合并、合并回滚都走统一二次确认框；
//   - 合并可回滚（7 天），但回滚只拆回类与资源归属，不回滚已产生的用量与审计。

import { useEffect, useMemo, useState } from 'react'
import { IconBack } from '../../icons'
import { Link, useParams } from 'react-router-dom'
import {
  createAccount,
  getAccount,
  importAccountsCsv,
  listAccounts,
  listGroups,
  mergeAccount,
  precheckMerge,
  type MergePlan,
  patchAccount,
  resetAccountPassword,
  revokeAccountSessions,
  rollbackMerge
} from '../../api/endpoints'
import type { AdminAccount, AdminAccountUpdate, AdminGroup, AdminQuota } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON, ToolButton } from '../../components/IconButtons'
import { IconDownload, IconRefresh, IconUpload } from '../../icons'
import ConfirmDialog from '../../components/ConfirmDialog'
import OneTimeSecret from '../../components/OneTimeSecret'
import {
  AsyncView,
  Badge,
  Banner,
  DataTable,
  EmptyState,
  Field,
  FormError,
  JsonDetails,
  KeyValues,
  LoadingBlock,
  Section,
  StatusBadge
} from '../../components/ui'
import {
  classLabel,
  downloadText,
  fmtLimit,
  fmtNum,
  fmtTime,
  statusLabel,
  toCsv
} from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

// ── 列表 ───────────────────────────────────────────────────────────────

export function AccountsPage({ def }: PageProps) {
  const [classFilter, setClassFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  const [keyword, setKeyword] = useState('')
  const q = useApi(
    () =>
      listAccounts({
        class: classFilter || undefined,
        status: statusFilter || undefined,
        limit: 100
      }),
    [classFilter, statusFilter]
  )
  const rows = useMemo(() => {
    const all = q.data?.data ?? []
    const k = keyword.trim().toLowerCase()
    if (!k) return all
    return all.filter(
      (a) =>
        a.display_name.toLowerCase().includes(k) ||
        a.username.toLowerCase().includes(k) ||
        a.id.toLowerCase().includes(k)
    )
  }, [q.data, keyword])

  function exportCsv() {
    const header = ['display_name', 'username', 'id', 'classes', 'status', 'groups', 'key_count', 'credential_count', 'created_at']
    const body = rows.map((a) => [
      a.display_name,
      a.username,
      a.id,
      a.classes.join('|'),
      a.status,
      a.groups.join('|'),
      a.key_count,
      a.credential_count,
      a.created_at ?? ''
    ])
    downloadText('accounts.csv', toCsv([header, ...body]), 'text/csv;charset=utf-8')
  }

  return (
    <PageFrame
      def={def}
      actions={
        <span className="inline-links">
          <ToolButton
            icon={<IconDownload size={ACTION_ICON} />}
            onClick={exportCsv}
            disabled={rows.length === 0}
          >
            导出当前结果 CSV
          </ToolButton>
          <Link to="/admin/accounts/import" className="icon-link">
            <IconUpload size={ACTION_ICON} />
            导入账户
          </Link>
        </span>
      }
    >
      <Banner tone="muted">
        列表里的 Key / 凭据只给数量：管理面可以看到它们，但不提供「一次拿到某人全部资源」的入口；
        需要排查走调用日志与各自的分组端点。
      </Banner>
      <div className="toolbar">
        <label className="toolbar-field">
          权限类
          <select value={classFilter} onChange={(e) => setClassFilter(e.target.value)}>
            <option value="">全部</option>
            <option value="admin">管理员</option>
            <option value="tenant">调用者</option>
            <option value="contributor">贡献者</option>
          </select>
        </label>
        <label className="toolbar-field">
          状态
          <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
            <option value="">全部</option>
            <option value="pending">待审核</option>
            <option value="active">正常</option>
            <option value="disabled">已禁用</option>
            <option value="rejected">已拒绝</option>
            <option value="merged">已合并</option>
          </select>
        </label>
        <label className="toolbar-field">
          搜索
          <input
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            placeholder="显示名 / 用户名 / ID"
          />
        </label>
        <ToolButton icon={<IconRefresh size={ACTION_ICON} />} onClick={q.reload}>刷新</ToolButton>
      </div>

      <AsyncView
        state={q}
        loadingRows={6}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="没有匹配的账户"
              hint="调整筛选条件，或从注册审核队列批准新账户。"
              action={<Link to="/admin">去审核队列</Link>}
            />
          ) : null
        }
      >
        {(d) => (
          <Section title={`账户（${d.total ?? d.data.length}）`}>
            <DataTable headers={['显示名', '账户 ID', '权限类', '状态', '组', 'Key', '凭据', '创建时间']}>
              {rows.map((a) => (
                <tr key={a.id}>
                  <td>
                    <Link to={`/admin/accounts/${encodeURIComponent(a.id)}`}>{a.display_name}</Link>
                  </td>
                  <td><code className="cell-id">{a.id}</code></td>
                  <td>
                    {a.classes.length === 0
                      ? '—'
                      : a.classes.map((c) => (
                          <Badge key={c} tone="muted">{classLabel(c)}</Badge>
                        ))}
                  </td>
                  <td><StatusBadge status={a.status} label={statusLabel(a.status)} /></td>
                  <td>{a.groups.length}</td>
                  <td>{fmtNum(a.key_count)}</td>
                  <td>{fmtNum(a.credential_count)}</td>
                  <td>{fmtTime(a.created_at)}</td>
                </tr>
              ))}
            </DataTable>
            {rows.length === 0 && <p className="muted">筛选后没有结果。</p>}
          </Section>
        )}
      </AsyncView>
    </PageFrame>
  )
}

// ── 详情 ───────────────────────────────────────────────────────────────

type DialogKind = 'save' | 'disable' | 'reset' | 'merge' | 'rollback' | null

export function AccountDetailPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const q = useApi(() => getAccount(id), [id])
  const groupsQ = useApi(() => listGroups({ limit: 200 }), [])

  const [classes, setClasses] = useState<string[]>([])
  const [status, setStatus] = useState('active')
  const [groups, setGroups] = useState<string[]>([])
  const [note, setNote] = useState('')
  const [quota, setQuota] = useState<AdminQuota | null>(null)
  const [rpm, setRpm] = useState('-1')
  const [tpm, setTpm] = useState('-1')
  const [conc, setConc] = useState('-1')

  const [dialog, setDialog] = useState<DialogKind>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [mergeTarget, setMergeTarget] = useState('')
  // 合并预检结果：文档要求「只有🔴为空时才能点确认」，所以确认框的可用性
  // 由它决定，而不是让管理员盲签一个必然被服务端拒的操作。
  const [mergePlan, setMergePlan] = useState<MergePlan | null>(null)
  const [mergePlanErr, setMergePlanErr] = useState<string | null>(null)
  const [mergePlanBusy, setMergePlanBusy] = useState(false)

  const [generatedPassword, setGeneratedPassword] = useState<string | null>(null)

  const account = q.data

  // 合并预检：文档要求「只有🔴为空时才能点确认」（01-accounts §4）。
  // 定义在 account 之后，闭包里读它的 id 才不是「用未声明的变量」。
  async function runMergePrecheck() {
    const target = mergeTarget.trim()
    if (!target || !account) return
    setMergePlanBusy(true)
    setMergePlanErr(null)
    setMergePlan(null)
    try {
      setMergePlan(await precheckMerge(account.id, target))
    } catch (e) {
      setMergePlanErr(e instanceof Error ? e.message : '预检失败')
    } finally {
      setMergePlanBusy(false)
    }
  }
  useEffect(() => {
    if (!account) return
    setClasses(account.classes)
    setStatus(account.status)
    setGroups(account.groups)
    setNote(account.note ?? '')
    setQuota(account.quota ?? null)
    setRpm(String(account.rpm_limit))
    setTpm(String(account.tpm_limit))
    setConc(String(account.max_concurrency))
  }, [account])

  if (!account) {
    return (
      <PageFrame def={def}>
        <AsyncView state={q} loadingRows={5}>
          {() => <LoadingBlock />}
        </AsyncView>
      </PageFrame>
    )
  }

  if (generatedPassword) {
    return (
      <OneTimeSecret
        title={`已重置 ${account.display_name} 的口令`}
        secret={generatedPassword}
        description="该账户的全部会话已立即吊销。请把口令交给账户本人；它不会再次显示。"
        filename={`password-${account.username}.txt`}
        onDone={() => {
          setGeneratedPassword(null)
          q.reload()
        }}
      />
    )
  }

  const groupList: AdminGroup[] = groupsQ.data?.data ?? []
  const classChanged = [...classes].sort().join(',') !== [...account.classes].sort().join(',')
  const statusChanged = status !== account.status
  const basicChanged =
    classChanged ||
    statusChanged ||
    [...groups].sort().join(',') !== [...account.groups].sort().join(',') ||
    note !== (account.note ?? '')

  function toggleClass(c: string) {
    setClasses((s) => (s.includes(c) ? s.filter((x) => x !== c) : [...s, c]))
  }

  async function runDialog() {
    if (!dialog || !account) return
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      if (dialog === 'save') {
        const body: AdminAccountUpdate = {}
        if (classChanged) body.classes = classes
        if (statusChanged) body.status = status
        if ([...groups].sort().join(',') !== [...account.groups].sort().join(',')) body.groups = groups
        if (note !== (account.note ?? '')) body.note = note
        if (Number(rpm) !== account.rpm_limit) body.rpm_limit = Number(rpm)
        if (Number(tpm) !== account.tpm_limit) body.tpm_limit = Number(tpm)
        if (Number(conc) !== account.max_concurrency) body.max_concurrency = Number(conc)
        if (quota) {
          const same =
            account.quota &&
            account.quota.unit === quota.unit &&
            account.quota.limit === quota.limit &&
            account.quota.period === quota.period
          if (!same) body.quota = quota
        }
        const r = await patchAccount(account.id, body)
        setNotice(`已保存（审计 ${r.audit_id ?? '—'}）`)
      } else if (dialog === 'disable') {
        setStatus('disabled')
        await patchAccount(account.id, { status: 'disabled' })
        setNotice('账户已禁用：名下全部 Key 立即 401，全部会话已吊销。')
      } else if (dialog === 'reset') {
        const r = await resetAccountPassword(account.id)
        setGeneratedPassword(r.data.generated_password ?? null)
        setNotice('口令已重置；若服务端未返回明文，请让账户本人走改密流程。')
      } else if (dialog === 'merge') {
        await mergeAccount(account.id, mergeTarget.trim())
        setNotice('合并已发起；7 天内可在本页回滚。')
      } else if (dialog === 'rollback') {
        await rollbackMerge(account.id)
        setNotice('已回滚合并（类与资源归属），已产生的用量与审计不回滚。')
      }
      setDialog(null)
      q.reload()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  const dialogProps = (() => {
    switch (dialog) {
      case 'save':
        return {
          title: `确认保存 ${account.display_name} 的变更`,
          impact: [
            classChanged ? `权限类：${account.classes.join(' / ') || '无'} → ${classes.join(' / ') || '无'}` : '',
            statusChanged ? `状态：${statusLabel(account.status)} → ${statusLabel(status)}` : '',
            `组数量：${account.groups.length} → ${groups.length}`,
            '保存后立即生效；审计会记录旧值与新值'
          ].filter(Boolean),
          irreversibility: '可恢复（再改回来），但期间该账户已按新权限发起过请求。',
          confirmLabel: '确认保存'
        }
      case 'disable':
        return {
          title: `确认禁用账户 ${account.display_name}`,
          impact: [
            '名下所有 API Key 立即返回 401',
            '全部会话立即吊销（含该账户正在使用的设备）',
            '账户仍保留，可随时重新启用'
          ],
          irreversibility: '可恢复（重新启用即可），但期间请求已失败、会话已丢失。',
          confirmLabel: '确认禁用'
        }
      case 'reset':
        return {
          title: `确认重置 ${account.display_name} 的口令`,
          impact: ['生成新的随机口令并一次性展示', '该账户全部会话立即吊销', '旧口令立即失效'],
          irreversibility: '旧口令不可恢复；新口令只显示一次。',
          confirmLabel: '确认重置'
        }
      case 'merge':
        return {
          title: `确认把 ${account.display_name} 合并到目标账户`,
          impact: [
            `源账户 ${account.id} 将转为「已合并」`,
            '权限类取并集；凭据与 Key 迁移到目标账户',
            '源账户全部会话立即失效',
            '7 天内可回滚（只拆回类与资源归属）'
          ],
          irreversibility: '7 天内可回滚；已产生的用量与审计不可回滚。',
          confirmLabel: '确认合并'
        }
      case 'rollback':
        return {
          title: `确认回滚 ${account.display_name} 的合并`,
          impact: ['把类与资源归属拆回源账户', '已产生的用量与审计保持原样'],
          irreversibility: '回滚本身可再次合并；但不会恢复已失效的会话。',
          confirmLabel: '确认回滚'
        }
      default:
        return null
    }
  })()

  return (
    <PageFrame
      def={def}
      actions={
        <span className="inline-links">
          <Link to="/admin/accounts" className="icon-link"><IconBack size={ACTION_ICON} />返回列表</Link>
          <ToolButton icon={<IconRefresh size={ACTION_ICON} />} onClick={q.reload}>刷新</ToolButton>
        </span>
      }
    >
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <Section title="基本信息">
        <KeyValues
          items={[
            { label: '显示名', value: <strong>{account.display_name}</strong> },
            { label: '用户名', value: account.username },
            { label: '账户 ID', value: <code>{account.id}</code> },
            { label: '状态', value: <StatusBadge status={account.status} label={statusLabel(account.status)} /> },
            { label: '创建时间', value: fmtTime(account.created_at) },
            { label: '最近登录', value: fmtTime(account.last_login_at) },
            { label: '名下 Key / 凭据', value: `${fmtNum(account.key_count)} / ${fmtNum(account.credential_count)}` }
          ]}
        />
      </Section>

      <Section
        title="权限类"
        hint="增删权限类立即改变该账户的能力面；授予管理员等于交出全站控制权，请确认对象。"
      >
        {(['admin', 'tenant', 'contributor'] as const).map((c) => (
          <label key={c} className="checkbox-line">
            <input type="checkbox" checked={classes.includes(c)} onChange={() => toggleClass(c)} />
            {classLabel(c)}
            {c === 'admin' && <span className="muted">（全站最高权限）</span>}
          </label>
        ))}
        {classes.includes('admin') && !account.classes.includes('admin') && (
          <Banner tone="warn" title="即将授予管理员">
            这会把这个账户提升为全站最高权限，能改模型、凭据、设置与所有账户。
          </Banner>
        )}
      </Section>

      <Section title="状态与组">
        <div className="field-row">
          <Field label="状态" hint="禁用会立即吊销会话并让 Key 返回 401；启用不会自动恢复已吊销的会话。">
            <select value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="active">正常</option>
              <option value="disabled">已禁用</option>
              <option value="rejected">已拒绝</option>
            </select>
          </Field>
          <Field label="组" hint="组决定可见的抽象模型与额度归属；成员关系不向成员展示。">
            <select
              multiple
              value={groups}
              onChange={(e) => setGroups(Array.from(e.target.selectedOptions).map((o) => o.value))}
              size={Math.min(6, Math.max(3, groupList.length))}
            >
              {groupList.map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name}
                  {g.is_default ? '（默认）' : ''}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Field label="内部备注" hint="不进审计正文。">
          <textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>
      </Section>

      <Section title="账户级额度与速率" hint="额度是账户级的：名下多把 Key 共享同一份额度与速率，写入 -1 表示不限。">
        {quota ? (
          <div className="field-row">
            <Field label="计量维度">
              <select
                value={quota.unit}
                onChange={(e) => setQuota({ ...quota, unit: e.target.value as AdminQuota['unit'] })}
              >
                <option value="request">请求次数</option>
                <option value="token">Token 数</option>
                <option value="credit">Credits</option>
              </select>
            </Field>
            <Field label="上限">
              <input
                type="number"
                min={0}
                value={quota.limit}
                onChange={(e) => setQuota({ ...quota, limit: Number(e.target.value) })}
              />
            </Field>
            <Field label="周期">
              <select
                value={quota.period}
                onChange={(e) => setQuota({ ...quota, period: e.target.value as 'day' | 'month' })}
              >
                <option value="day">每日</option>
                <option value="month">每月</option>
              </select>
            </Field>
            <Field label="当期已用（只读）">
              <input value={fmtNum(quota.used)} disabled />
            </Field>
          </div>
        ) : (
          <p className="muted">
            当前没有合计额度行（不限）。{' '}
            <button
              type="button"
              className="btn-ghost"
              onClick={() => setQuota({ unit: 'token', limit: 100000, used: 0, period: 'day' })}
            >
              添加一行
            </button>
          </p>
        )}
        {quota && (
          <button type="button" className="btn-ghost" onClick={() => setQuota(null)}>
            移除额度行（改为不限）
          </button>
        )}
        <div className="field-row">
          <Field label="RPM 上限" hint="-1 = 不限">
            <input type="number" value={rpm} onChange={(e) => setRpm(e.target.value)} />
          </Field>
          <Field label="TPM 上限" hint="-1 = 不限">
            <input type="number" value={tpm} onChange={(e) => setTpm(e.target.value)} />
          </Field>
          <Field label="并发上限" hint="-1 = 不限">
            <input type="number" value={conc} onChange={(e) => setConc(e.target.value)} />
          </Field>
        </div>
        <p className="muted">
          当前生效：RPM {fmtLimit(account.rpm_limit)} · TPM {fmtLimit(account.tpm_limit)} · 并发{' '}
          {fmtLimit(account.max_concurrency)}
        </p>
        <button type="button" className="btn-primary" onClick={() => setDialog('save')} disabled={!basicChanged && !quotaChanged(account, quota, rpm, tpm, conc)}>
          保存账户变更
        </button>
        {!basicChanged && !quotaChanged(account, quota, rpm, tpm, conc) && (
          <p className="muted">没有未保存的变更。</p>
        )}
      </Section>

      <Section title="会话与口令">
        <div className="inline-links">
          <button type="button" className="btn-ghost" onClick={() => setDialog('reset')}>重置口令…</button>
          <button
            type="button"
            className="btn-ghost"
            onClick={async () => {
              setBusy(true)
              setError(null)
              try {
                const r = await revokeAccountSessions(account.id)
                setNotice(`已吊销 ${r.data.revoked ?? 0} 个会话。`)
              } catch (e) {
                setError(e)
              } finally {
                setBusy(false)
              }
            }}
            disabled={busy}
          >
            吊销全部会话
          </button>
        </div>
        <p className="muted">重置口令与禁用账户都会立即吊销全部会话；吊销会话不改变账户状态。</p>
      </Section>

      <Section title="合并账户">
        <p className="muted">
          合并 = 两个账户并成一个、类取并集；源账户转「已合并」，凭据与 Key 迁到目标账户。
          7 天内可回滚，但回滚不恢复已产生的用量与审计。
        </p>
        {account.status === 'merged' ? (
          <button type="button" className="btn-ghost" onClick={() => setDialog('rollback')}>
            回滚这次合并…
          </button>
        ) : (
          <>
          <div className="field-row">
            <Field label="目标账户 ID" required hint="合并前请确认对象：这是本操作最贵的错误。">
              <input value={mergeTarget} onChange={(e) => { setMergeTarget(e.target.value); setMergePlan(null) }} placeholder="UUID（36 字符）" />
            </Field>
            <button
              type="button"
              className="btn-ghost"
              onClick={() => void runMergePrecheck()}
              disabled={!mergeTarget.trim() || mergePlanBusy}
            >
              {mergePlanBusy ? '正在预检…' : '先跑预检'}
            </button>
            <button
              type="button"
              className="btn-danger"
              onClick={() => setDialog('merge')}
              disabled={!mergeTarget.trim() || !mergePlan || mergePlan.blocking.length > 0}
            >
              合并到该账户…
            </button>
          </div>
          {mergePlanErr ? <p className="form-error">{mergePlanErr}</p> : null}
          {mergePlan ? (
            <div className="merge-precheck">
              {mergePlan.blocking.length > 0 ? (
                <Banner tone="warn" title="存在阻塞项：不能合并">
                  <ul className="plain-list">
                    {mergePlan.blocking.map((b, i) => (
                      <li key={i}>{b}</li>
                    ))}
                  </ul>
                  <p className="muted">
                    同一个上游凭据挂在两个账户上是「身份重叠」——静默合并会把两个自然人
                    并成一个且不可逆。请先手工处理上面这几条凭据再回来。
                  </p>
                </Banner>
              ) : (
                <Banner tone="ok" title="预检通过：可以合并">
                  将改挂 {mergePlan.keys} 把 Key、{mergePlan.credentials} 个凭据、
                  {mergePlan.group_relations} 条组关系；
                  类并集：{mergePlan.source_classes.join(' / ') || '无'} ∪{' '}
                  {mergePlan.target_classes.join(' / ') || '无'}。
                  {/* 短码取 id 末 8 位，与后端 store.shortCode 同口径：UUID 前段
                      在所有 id 里都一样，区分度全在随机段；整串塞进 Key 名会撑破列表。 */}
                  {mergePlan.renamed_keys.length > 0 &&
                    ` 其中 ${mergePlan.renamed_keys.length} 把同名 Key 会被自动改名（追加 -from-${mergePlan.source_id.slice(-8)}）。`}
                </Banner>
              )}
            </div>
          ) : null}
          </>
        )}
      </Section>

      {dialogProps && (
        <ConfirmDialog
          open
          title={dialogProps.title}
          impact={dialogProps.impact}
          irreversibility={dialogProps.irreversibility}
          targetName={dialog === 'merge' ? mergeTarget.trim() : dialog === 'rollback' ? account.id : account.display_name}
          inputLabel={
            dialog === 'merge'
              ? '请输入目标账户 ID 以确认'
              : dialog === 'rollback'
                ? '请输入源账户 ID 以确认'
                : '请输入账户显示名以确认'
          }
          confirmLabel={dialogProps.confirmLabel}
          busy={busy}
          error={error}
          onCancel={() => setDialog(null)}
          onConfirm={() => void runDialog()}
        />
      )}
    </PageFrame>
  )
}

function quotaChanged(
  account: AdminAccount,
  quota: AdminQuota | null,
  rpm: string,
  tpm: string,
  conc: string
): boolean {
  const q = account.quota
  const quotaDiff = q
    ? !quota || q.unit !== quota.unit || q.limit !== quota.limit || q.period !== quota.period
    : quota !== null
  return quotaDiff || Number(rpm) !== account.rpm_limit || Number(tpm) !== account.tpm_limit || Number(conc) !== account.max_concurrency
}

// ── 导入 ───────────────────────────────────────────────────────────────

const CSV_SAMPLE = `用户名,密码,组,额度单位,额度上限
alice,S3cret-passphrase-2026,grp_power,token,200000
bob,,grp_default,request,1000`

export function ImportAccountsPage({ def }: PageProps) {
  const [csv, setCsv] = useState('')
  const [result, setResult] = useState<{ imported: number; generated: { username: string; password: string }[] } | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [single, setSingle] = useState({ username: '', display_name: '', password: '' })

  async function submitCsv() {
    setBusy(true)
    setError(null)
    try {
      const r = await importAccountsCsv(csv)
      setResult(r.data)
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  async function submitSingle() {
    setBusy(true)
    setError(null)
    try {
      const r = await createAccount({
        username: single.username,
        display_name: single.display_name || single.username,
        password: single.password || undefined,
        classes: ['tenant'],
        status: 'active'
      })
      if (r.data.generated_password) {
        setResult({ imported: 1, generated: [{ username: r.data.username, password: r.data.generated_password }] })
      } else {
        setResult({ imported: 1, generated: [] })
      }
      setSingle({ username: '', display_name: '', password: '' })
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  function downloadPasswords() {
    if (!result) return
    downloadText(
      'imported-passwords.csv',
      toCsv([['username', 'password'], ...result.generated.map((g) => [g.username, g.password])]),
      'text/csv;charset=utf-8'
    )
  }

  return (
    <PageFrame def={def} actions={<Link to="/admin/accounts" className="icon-link"><IconBack size={ACTION_ICON} />返回账户列表</Link>}>
      <Banner tone="warn" title="导入即生效">
        导入的账户直接是「正常」状态并进入所选组。密码留空会生成随机值，生成的密码只在本页显示一次。
      </Banner>
      <Section title="单个添加">
        <div className="field-row">
          <Field label="用户名" required>
            <input value={single.username} onChange={(e) => setSingle({ ...single, username: e.target.value })} />
          </Field>
          <Field label="显示名" hint="留空则同用户名。">
            <input value={single.display_name} onChange={(e) => setSingle({ ...single, display_name: e.target.value })} />
          </Field>
          <Field label="口令" hint="留空则生成随机口令并一次性展示。">
            <input value={single.password} onChange={(e) => setSingle({ ...single, password: e.target.value })} />
          </Field>
          <button type="button" className="btn-primary" onClick={() => void submitSingle()} disabled={busy || !single.username}>
            添加
          </button>
        </div>
      </Section>

      <Section
        title="CSV 批量导入"
        hint="列：用户名,密码[,组][,额度单位][,额度上限]。密码留空则生成随机值；额度单位取 request / token / credit。"
        actions={<button type="button" className="btn-ghost" onClick={() => setCsv(CSV_SAMPLE)}>填入示例</button>}
      >
        <textarea
          className="code-input"
          rows={8}
          value={csv}
          onChange={(e) => setCsv(e.target.value)}
          placeholder={CSV_SAMPLE}
        />
        <FormError error={error} />
        <button type="button" className="btn-primary" onClick={() => void submitCsv()} disabled={busy || !csv.trim()}>
          {busy ? '正在导入…' : '导入 CSV'}
        </button>
      </Section>

      {result && (
        <Section title="导入结果" actions={<button type="button" onClick={downloadPasswords} disabled={result.generated.length === 0}>下载密码 CSV</button>}>
          <p>成功导入 {result.imported} 个账户。</p>
          {result.generated.length > 0 ? (
            <DataTable headers={['用户名', '一次性口令']}>
              {result.generated.map((g) => (
                <tr key={g.username}>
                  <td>{g.username}</td>
                  <td><code>{g.password}</code></td>
                </tr>
              ))}
            </DataTable>
          ) : (
            <p className="muted">本次没有生成随机口令。</p>
          )}
          <Banner tone="warn">离开本页后这些密码不再显示。请先下载或复制。</Banner>
          <JsonDetails summary="查看原始响应" value={result} />
          <button type="button" className="btn-ghost" onClick={() => setResult(null)}>我已完成</button>
        </Section>
      )}

      <Section title="导入的边界">
        <ul className="plain-list">
          <li>导入的账户没有「待审核」阶段，管理员就是批准人。</li>
          <li>全量写入审计；若中途失败，已成功的行仍然存在（CSV 不是事务）。</li>
          <li>该端点在后端未接线时会返回 404，页面会显示「尚未接入」。</li>
        </ul>
      </Section>
    </PageFrame>
  )
}
