// 组管理（docs/ui/03-admin.md 第 5 节）。
//
// 组是管理员的批量控制单元，不产生成员可见的组织感。界面必须避免出现
// 「组端点 / 组密钥 / 组邀请」三类元素——它们是把组误做成团队的最短路径。
//
// 策略编辑的两条定案：
//   - 每人额度 = 每个**账户**的额度（Key 不持有额度），量词一律「每人」；
//   - 个人上限与共享池是交集，界面不做「取最严」的自动收缩：两个数字原样
//     呈现，并说明「个人额度用完只停这个人，池子用完全组都停」。
//   留空 = 不设（不是 0）；0 在语义上是零额度全拒。

import { useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  addGroupMembers,
  createGroup,
  deleteGroup,
  duplicateGroup,
  getGroup,
  listAbstractModels,
  listAccounts,
  listGroups,
  patchGroup,
  putGroupModels,
  putGroupPolicies,
  removeGroupMember
} from '../../api/endpoints'
import type { AdminGroup, GroupPool, GroupPolicyQuota, GroupPolicies } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import ConfirmDialog from '../../components/ConfirmDialog'
import {
  AsyncView,
  Badge,
  Banner,
  DataTable,
  EmptyState,
  Field,
  FormError,
  KeyValues,
  Section
} from '../../components/ui'
import { fmtNum, fmtTime, truncate } from '../../utils/format'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'

const UNITS = [
  ['request', '请求次数'],
  ['token', 'Token 数'],
  ['credit', 'Credits']
] as const

// ── 列表 ───────────────────────────────────────────────────────────────

export function GroupsPage({ def }: PageProps) {
  const q = useApi(() => listGroups({ limit: 100 }), [])
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState<AdminGroup | null>(null)
  const [duplicating, setDuplicating] = useState<AdminGroup | null>(null)
  const [newName, setNewName] = useState('')

  async function submitCreate() {
    setBusy(true)
    setError(null)
    try {
      await createGroup({ name, description })
      setName('')
      setDescription('')
      setCreating(false)
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
        <button type="button" className="btn-primary" onClick={() => setCreating((v) => !v)}>
          {creating ? '收起' : '新建组'}
        </button>
      }
    >
      <Banner tone="muted">
        组没有端点、密钥或邀请入口。成员之间互不可见；额度以「每人」计，删组时成员退回默认组（删组不删人）。
      </Banner>

      {creating && (
        <Section title="新建组">
          <div className="field-row">
            <Field label="组名" required>
              <input value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label="描述">
              <input value={description} onChange={(e) => setDescription(e.target.value)} />
            </Field>
            <button type="button" className="btn-primary" onClick={() => void submitCreate()} disabled={busy || !name.trim()}>
              {busy ? '正在创建…' : '创建'}
            </button>
          </div>
          <FormError error={error} />
        </Section>
      )}

      {duplicating && (
        <Section title={`复制组 ${duplicating.name}`} hint="复制会带上模型授权与策略，新组不继承默认组身份。">
          <div className="field-row">
            <Field label="新组名" required>
              <input value={newName} onChange={(e) => setNewName(e.target.value)} />
            </Field>
            <button
              type="button"
              className="btn-primary"
              disabled={busy || !newName.trim()}
              onClick={async () => {
                setBusy(true)
                setError(null)
                try {
                  await duplicateGroup(duplicating.id, newName.trim())
                  setDuplicating(null)
                  setNewName('')
                  q.reload()
                } catch (e) {
                  setError(e)
                } finally {
                  setBusy(false)
                }
              }}
            >
              复制
            </button>
            <button type="button" className="btn-ghost" onClick={() => setDuplicating(null)}>取消</button>
          </div>
          <FormError error={error} />
        </Section>
      )}

      <FormError error={error} />

      <AsyncView
        state={q}
        loadingRows={5}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState title="还没有组" hint="组用于批量控制模型授权与额度；至少保留一个默认组作为退路。" action={<button type="button" onClick={() => setCreating(true)}>新建组</button>} />
          ) : null
        }
      >
        {(d) => (
          <Section title={`组（${d.total ?? d.data.length}）`}>
            <DataTable headers={['组名', '描述', '默认组', '成员数', '创建时间', '操作']}>
              {d.data.map((g) => (
                <tr key={g.id}>
                  <td><Link to={`/admin/groups/${encodeURIComponent(g.id)}`}>{g.name}</Link></td>
                  <td className="cell-wrap">{g.description || '—'}</td>
                  <td>{g.is_default ? <Badge tone="info">默认</Badge> : '—'}</td>
                  <td>{fmtNum(g.member_count)}</td>
                  <td>{fmtTime(g.created_at)}</td>
                  <td className="cell-actions">
                    <Link to={`/admin/groups/${encodeURIComponent(g.id)}`}>详情</Link>
                    <button type="button" className="btn-ghost" onClick={() => { setDuplicating(g); setNewName(`${g.name}-copy`) }}>复制</button>
                    {!g.is_default && (
                      <button type="button" className="btn-danger" onClick={() => setDeleting(g)}>删除…</button>
                    )}
                  </td>
                </tr>
              ))}
            </DataTable>
          </Section>
        )}
      </AsyncView>

      {deleting && (
        <ConfirmDialog
          open
          title={`确认删除组 ${deleting.name}`}
          impact={[
            `组 ${deleting.name}（${deleting.id}）将被删除`,
            `当前 ${deleting.member_count} 名成员退回默认组（降级，不会被删）`,
            '该组的模型授权与策略一并消失'
          ]}
          irreversibility="不可恢复：被删除的组策略无法还原，只能重新配置。"
          targetName={deleting.name}
          inputLabel="请输入组名以确认"
          confirmLabel="确认删除组"
          busy={busy}
          error={error}
          onCancel={() => setDeleting(null)}
          onConfirm={async () => {
            setBusy(true)
            setError(null)
            try {
              await deleteGroup(deleting.id)
              setDeleting(null)
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

// ── 详情 ───────────────────────────────────────────────────────────────

interface PendingAction {
  kind: 'default' | 'members-add' | 'member-remove' | 'models' | 'policies' | 'delete'
  title: string
  impact: string[]
  irreversibility: string
  targetName: string
  inputLabel: string
  confirmLabel: string
  run: () => Promise<void>
}

export function GroupDetailPage({ def }: PageProps) {
  const { id = '' } = useParams<{ id: string }>()
  const q = useApi(() => getGroup(id), [id])
  const abstracts = useApi(() => listAbstractModels({ limit: 200 }), [])
  const accounts = useApi(() => listAccounts({ limit: 200 }), [])

  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [isDefault, setIsDefault] = useState(false)
  const [policy, setPolicy] = useState<GroupPolicies>({})
  const [memberToAdd, setMemberToAdd] = useState('')
  const [modelDraft, setModelDraft] = useState<string[]>([])
  const [pending, setPending] = useState<PendingAction | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const group = q.data
  useEffect(() => {
    if (!group) return
    setName(group.name)
    setDescription(group.description ?? '')
    setIsDefault(group.is_default)
    setPolicy(group.policies ?? {})
    setModelDraft(group.models ?? [])
  }, [group])

  const accountName = useMemo(() => {
    const map = new Map<string, string>()
    for (const a of accounts.data?.data ?? []) map.set(a.id, a.display_name)
    return map
  }, [accounts.data])

  if (!group) {
    return (
      <PageFrame def={def}>
        <AsyncView state={q} loadingRows={5}>{() => <p>加载中…</p>}</AsyncView>
      </PageFrame>
    )
  }

  const modelOptions = abstracts.data?.data ?? []
  const memberOptions = (accounts.data?.data ?? []).filter(
    (a) => (a.classes.includes('tenant') || a.classes.includes('admin')) && !group.members.includes(a.id)
  )

  function patchPolicy<K extends keyof GroupPolicies>(key: K, value: GroupPolicies[K]) {
    setPolicy((p) => ({ ...p, [key]: value }))
  }

  const quotas = policy.account_quotas ?? []
  const caps = policy.model_quota_caps ?? []
  const pools = policy.pool ?? []

  function setQuota(i: number, v: Partial<GroupPolicyQuota>) {
    patchPolicy('account_quotas', quotas.map((r, idx) => (idx === i ? { ...r, ...v } : r)))
  }
  function setPool(i: number, v: Partial<GroupPool>) {
    patchPolicy('pool', pools.map((r, idx) => (idx === i ? { ...r, ...v } : r)))
  }

  async function saveBasic() {
    if (!group) return
    await patchGroup(group.id, { name, description, is_default: isDefault })
    setNotice('基本信息已保存')
    q.reload()
  }

  const dialog = pending && (
    <ConfirmDialog
      open
      title={pending.title}
      impact={pending.impact}
      irreversibility={pending.irreversibility}
      targetName={pending.targetName}
      inputLabel={pending.inputLabel}
      confirmLabel={pending.confirmLabel}
      busy={busy}
      error={error}
      onCancel={() => setPending(null)}
      onConfirm={async () => {
        setBusy(true)
        setError(null)
        try {
          await pending.run()
          setNotice('已保存')
          setPending(null)
          q.reload()
        } catch (e) {
          setError(e)
        } finally {
          setBusy(false)
        }
      }}
    />
  )

  return (
    <PageFrame def={def} actions={<Link to="/admin/groups">返回组列表</Link>}>
      <FormError error={error} />
      {notice && <Banner tone="ok">{notice}</Banner>}

      <Section title="基本信息">
        <div className="field-row">
          <Field label="组名" required>
            <input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label="描述">
            <input value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <label className="checkbox-line">
            <input type="checkbox" checked={isDefault} onChange={(e) => setIsDefault(e.target.checked)} />
            设为默认组
          </label>
          <button type="button" className="btn-primary" onClick={() => void saveBasic()} disabled={busy}>
            保存基本信息
          </button>
        </div>
        <KeyValues
          items={[
            { label: '组 ID', value: <code>{group.id}</code> },
            { label: '成员数', value: fmtNum(group.member_count) },
            { label: '默认组', value: group.is_default ? '是' : '否' },
            { label: '创建时间', value: fmtTime(group.created_at) }
          ]}
        />
        <p className="muted">默认组是删组时的退路；改默认组属于「改组」，需要二次确认。</p>
        {!group.is_default && (
          <button
            type="button"
            className="btn-ghost"
            onClick={() =>
              setPending({
                kind: 'default',
                title: `确认把 ${group.name} 设为默认组`,
                impact: [`${group.name} 成为默认组`, '原本的默认组变为普通组', '新账户将自动进入该组'],
                irreversibility: '可恢复（再设回即可），但期间新账户的默认归属已改变。',
                targetName: group.name,
                inputLabel: '请输入组名以确认',
                confirmLabel: '确认设为默认组',
                run: async () => {
                  await patchGroup(group.id, { is_default: true })
                }
              })
            }
            disabled={busy}
          >
            设为默认组…
          </button>
        )}
      </Section>

      <Section title={`成员（${group.members.length}）`} hint="成员必须是带「调用者」能力的账户；成员之间互不可见。">
        <ul className="member-list">
          {group.members.map((m) => (
            <li key={m}>
              <span>{accountName.get(m) ?? m}</span>
              <code className="muted">{m}</code>
              <button
                type="button"
                className="btn-ghost"
                onClick={() =>
                  setPending({
                    kind: 'member-remove',
                    title: `确认把 ${accountName.get(m) ?? m} 移出组 ${group.name}`,
                    impact: [
                      '该账户的可访问抽象模型集合立即收窄（可能变为空）',
                      '该账户的额度归属改为剩余组的合并结果',
                      '撤销只能改回来；期间他已据此发起过请求'
                    ],
                    irreversibility: '可恢复（重新加入），但期间可见模型与额度已改变。',
                    targetName: group.name,
                    inputLabel: '请输入组名以确认',
                    confirmLabel: '确认移出成员',
                    run: async () => {
                      await removeGroupMember(group.id, m)
                    }
                  })
                }
              >
                移出…
              </button>
            </li>
          ))}
        </ul>
        <div className="field-row">
          <Field label="添加成员" hint="只列尚未在该组、且具备调用能力的账户。">
            <select value={memberToAdd} onChange={(e) => setMemberToAdd(e.target.value)}>
              <option value="">选择账户</option>
              {memberOptions.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.display_name}（{a.username}）
                </option>
              ))}
            </select>
          </Field>
          <button
            type="button"
            className="btn-primary"
            disabled={!memberToAdd}
            onClick={() =>
              setPending({
                kind: 'members-add',
                title: `确认把账户加入组 ${group.name}`,
                impact: [
                  '该账户的可访问抽象模型集合立即扩大',
                  '该账户的额度归属改变（限制类取最严，放开类取并集）'
                ],
                irreversibility: '可恢复（移出即可），但期间他已据此发起过请求。',
                targetName: group.name,
                inputLabel: '请输入组名以确认',
                confirmLabel: '确认添加成员',
                run: async () => {
                  await addGroupMembers(group.id, [memberToAdd])
                  setMemberToAdd('')
                }
              })
            }
          >
            添加
          </button>
        </div>
      </Section>

      <Section title="模型授权" hint="可选抽象模型多选；保存会整体替换授权集合，立即改变一批人的可见模型。">
        <div className="checkbox-grid">
          {modelOptions.map((m) => (
            <label key={m.id} className="checkbox-line">
              <input
                type="checkbox"
                checked={modelDraft.includes(m.name)}
                onChange={() =>
                  setModelDraft((s) => (s.includes(m.name) ? s.filter((x) => x !== m.name) : [...s, m.name]))
                }
              />
              {m.name}
              <span className="muted">{m.display_name}</span>
              {m.status !== 'active' && <Badge tone="warn">{m.status === 'deprecated' ? '已弃用' : '已停用'}</Badge>}
            </label>
          ))}
          {modelOptions.length === 0 && <p className="muted">还没有抽象模型；请先在「抽象模型列表」创建。</p>}
        </div>
        <button
          type="button"
          className="btn-primary"
          onClick={() =>
            setPending({
              kind: 'models',
              title: `确认替换组 ${group.name} 的模型授权`,
              impact: [
                `授权集合：${group.models.join(' / ') || '无'} → ${modelDraft.join(' / ') || '无'}`,
                `受影响的成员数：${group.member_count}`,
                '保存后成员可用模型立即变化'
              ],
              irreversibility: '可恢复（再替换回来），但期间成员的可见模型已改变。',
              targetName: group.name,
              inputLabel: '请输入组名以确认',
              confirmLabel: '确认替换授权',
              run: async () => {
                await putGroupModels(group.id, modelDraft)
              }
            })
          }
          disabled={busy}
        >
          保存模型授权…
        </button>
        <p className="muted">
          当前授权：{group.models.length === 0 ? '无（成员看不到任何抽象模型）' : group.models.join(' / ')}
        </p>
      </Section>

      <Section title="放开类策略" hint="布尔开关与上限；改变它们立即影响该组的每个账户。">
        <div className="field-row">
          <label className="checkbox-line">
            <input
              type="checkbox"
              checked={Boolean(policy.allow_create_key)}
              onChange={(e) => patchPolicy('allow_create_key', e.target.checked)}
            />
            允许成员创建 Key
          </label>
          <label className="checkbox-line">
            <input
              type="checkbox"
              checked={Boolean(policy.allow_widen_allowlist)}
              onChange={(e) => patchPolicy('allow_widen_allowlist', e.target.checked)}
            />
            允许放宽 Key 白名单
          </label>
          <Field label="Key 数量上限（每人）" hint="留空 = 不限">
            <input
              type="number"
              value={policy.max_keys ?? ''}
              onChange={(e) => patchPolicy('max_keys', e.target.value === '' ? undefined : Number(e.target.value))}
            />
          </Field>
        </div>
      </Section>

      <Section title="限制类策略 · 每人额度" hint="量词是「每个账户」：名下所有 Key 共享同一份额度。留空 = 不设；0 = 零额度全拒。">
        {quotas.map((r, i) => (
          <div className="field-row" key={i}>
            <Field label="计量维度">
              <select value={r.unit} onChange={(e) => setQuota(i, { unit: e.target.value as GroupPolicyQuota['unit'] })}>
                {UNITS.map(([v, l]) => (
                  <option key={v} value={v}>{l}</option>
                ))}
              </select>
            </Field>
            <Field label="周期">
              <select value={r.period} onChange={(e) => setQuota(i, { period: e.target.value as 'day' | 'month' })}>
                <option value="day">每日</option>
                <option value="month">每月</option>
              </select>
            </Field>
            <Field label="上限">
              <input type="number" min={0} value={r.limit} onChange={(e) => setQuota(i, { limit: Number(e.target.value) })} />
            </Field>
            <button type="button" className="btn-ghost" onClick={() => patchPolicy('account_quotas', quotas.filter((_, idx) => idx !== i))}>
              删除本行
            </button>
          </div>
        ))}
        <button
          type="button"
          className="btn-ghost"
          onClick={() => patchPolicy('account_quotas', [...quotas, { unit: 'token', limit: 100000, period: 'day' }])}
        >
          添加一行额度
        </button>

        <h3>每人按模型限额</h3>
        <p className="muted">逐抽象模型的每人限额；同一 (维度, 周期) 必须 ≤ 该维合计上界。</p>
        {caps.map((r, i) => (
          <div className="field-row" key={i}>
            <Field label="模型">
              <select
                value={r.model}
                onChange={(e) =>
                  patchPolicy('model_quota_caps', caps.map((x, idx) => (idx === i ? { ...x, model: e.target.value } : x)))
                }
              >
                <option value="">选择模型</option>
                {group.models.map((m) => (
                  <option key={m} value={m}>{m}</option>
                ))}
              </select>
            </Field>
            <Field label="维度">
              <select
                value={r.unit}
                onChange={(e) =>
                  patchPolicy('model_quota_caps', caps.map((x, idx) => (idx === i ? { ...x, unit: e.target.value as GroupPolicyQuota['unit'] } : x)))
                }
              >
                {UNITS.map(([v, l]) => (
                  <option key={v} value={v}>{l}</option>
                ))}
              </select>
            </Field>
            <Field label="周期">
              <select
                value={r.period}
                onChange={(e) =>
                  patchPolicy('model_quota_caps', caps.map((x, idx) => (idx === i ? { ...x, period: e.target.value as 'day' | 'month' } : x)))
                }
              >
                <option value="day">每日</option>
                <option value="month">每月</option>
              </select>
            </Field>
            <Field label="上限">
              <input
                type="number"
                min={0}
                value={r.limit}
                onChange={(e) =>
                  patchPolicy('model_quota_caps', caps.map((x, idx) => (idx === i ? { ...x, limit: Number(e.target.value) } : x)))
                }
              />
            </Field>
            <button type="button" className="btn-ghost" onClick={() => patchPolicy('model_quota_caps', caps.filter((_, idx) => idx !== i))}>
              删除本行
            </button>
          </div>
        ))}
        <button
          type="button"
          className="btn-ghost"
          disabled={group.models.length === 0}
          onClick={() =>
            patchPolicy('model_quota_caps', [
              ...caps,
              { model: group.models[0] ?? '', unit: 'request', limit: 50, period: 'day' }
            ])
          }
        >
          添加按模型限额
        </button>

        <h3>速率（每人）</h3>
        <div className="field-row">
          <Field label="RPM 上限" hint="-1 = 不限">
            <input
              type="number"
              value={policy.account_rpm_limit ?? -1}
              onChange={(e) => patchPolicy('account_rpm_limit', Number(e.target.value))}
            />
          </Field>
          <Field label="TPM 上限" hint="-1 = 不限">
            <input
              type="number"
              value={policy.account_tpm_limit ?? -1}
              onChange={(e) => patchPolicy('account_tpm_limit', Number(e.target.value))}
            />
          </Field>
          <Field label="并发上限" hint="-1 = 不限">
            <input
              type="number"
              value={policy.account_max_concurrency ?? -1}
              onChange={(e) => patchPolicy('account_max_concurrency', Number(e.target.value))}
            />
          </Field>
        </div>
      </Section>

      <Section
        title="组级共享池"
        hint="周期必填（日 / 月），不给「默认」；每维各占一行，多个池都要满足，哪个先到算哪个。个人额度用完只停这个人，池子用完全组都停。"
      >
        {pools.map((r, i) => (
          <div className="field-row" key={i}>
            <Field label="模型限定" hint="留空 = 全模型合计池">
              <select value={r.model ?? ''} onChange={(e) => setPool(i, { model: e.target.value || null })}>
                <option value="">全模型合计</option>
                {group.models.map((m) => (
                  <option key={m} value={m}>{m}</option>
                ))}
              </select>
            </Field>
            <Field label="维度">
              <select value={r.unit} onChange={(e) => setPool(i, { unit: e.target.value as GroupPool['unit'] })}>
                {UNITS.map(([v, l]) => (
                  <option key={v} value={v}>{l}</option>
                ))}
              </select>
            </Field>
            <Field label="周期" required>
              <select value={r.period} onChange={(e) => setPool(i, { period: e.target.value as 'day' | 'month' })}>
                <option value="day">每日</option>
                <option value="month">每月</option>
              </select>
            </Field>
            <Field label="上限">
              <input type="number" min={0} value={r.limit} onChange={(e) => setPool(i, { limit: Number(e.target.value) })} />
            </Field>
            <Field label="当期已用（只读）">
              <input value={fmtNum(r.used ?? 0)} disabled />
            </Field>
            <button type="button" className="btn-ghost" onClick={() => patchPolicy('pool', pools.filter((_, idx) => idx !== i))}>
              删除本行
            </button>
          </div>
        ))}
        <button
          type="button"
          className="btn-ghost"
          onClick={() =>
            patchPolicy('pool', [
              ...pools,
              { model: null, unit: 'request', period: 'day', limit: 500, used: 0 }
            ])
          }
        >
          添加共享池行
        </button>
      </Section>

      <Section title="保存策略" hint="策略以整文档替换（PUT），保存后立即生效。">
        <Banner tone="muted">
          个人上限与共享池是交集，界面不自动收缩：你填的数字原样生效。
        </Banner>
        <button
          type="button"
          className="btn-primary"
          onClick={() =>
            setPending({
              kind: 'policies',
              title: `确认保存组 ${group.name} 的策略`,
              impact: [
                `每人额度行：${quotas.length} 条；按模型限额：${caps.length} 条；共享池：${pools.length} 条`,
                `速率：RPM ${policy.account_rpm_limit ?? -1} / TPM ${policy.account_tpm_limit ?? -1} / 并发 ${policy.account_max_concurrency ?? -1}`,
                `受影响成员：${group.member_count}`
              ],
              irreversibility: '可恢复（再改回来），但期间该组成员已按新额度/速率发起过请求。',
              targetName: group.name,
              inputLabel: '请输入组名以确认',
              confirmLabel: '确认保存策略',
              run: async () => {
                await putGroupPolicies(group.id, policy)
              }
            })
          }
          disabled={busy}
        >
          保存策略…
        </button>
      </Section>

      <Section title="危险区">
        <button
          type="button"
          className="btn-danger"
          disabled={group.is_default}
          onClick={() =>
            setPending({
              kind: 'delete',
              title: `确认删除组 ${group.name}`,
              impact: [
                `${group.member_count} 名成员退回默认组（降级，不会被删）`,
                '模型授权与策略一并消失'
              ],
              irreversibility: '不可恢复：策略无法还原，只能重新配置。',
              targetName: group.name,
              inputLabel: '请输入组名以确认',
              confirmLabel: '确认删除组',
              run: async () => {
                await deleteGroup(group.id)
                window.location.hash = '#/admin/groups'
              }
            })
          }
        >
          {group.is_default ? '默认组不可删除' : '删除组…'}
        </button>
        <p className="muted">删组不删人：成员会退回默认组，而不是失去所有权限。</p>
        <p className="muted">组 ID：<code>{truncate(group.id, 60)}</code></p>
      </Section>

      {dialog}
    </PageFrame>
  )
}
