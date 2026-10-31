// 租户端：用量与配额、调用日志、可用抽象模型（docs/ui/04-tenant.md）。
//
// 第一条硬约束：**整片租户界面不得出现任何真实模型名**。这里的每一个
// 字段都只到抽象模型维度；错误码也只展示机器码与人话，不透传上游错误体
// 里的 model 字段。
//
// 为什么没有「按真实模型 / 按渠道 / 费用」维度：真实目标与贡献者归因
// 仅管理端可见；订阅额度没有单价，算不出真钱。

import { useState } from 'react'
import { Link } from 'react-router-dom'
import {
  getUsageOverview,
  listAvailableModels,
  listKeys,
  listUsageLogs
} from '../../api/endpoints'
import type { UsageLog } from '../../api/types'
import { useApi } from '../../hooks/useApi'
import { ACTION_ICON, ToolButton } from '../../components/IconButtons'
import { IconDownload, IconRefresh } from '../../icons'
import {
  AsyncView,
  Banner,
  DataTable,
  EmptyState,
  Section,
  StatusBadge
} from '../../components/ui'
import {
  downloadText,
  fmtNum,
  fmtTime,
  pct,
  statusLabel,
  toCsv
} from '../../utils/format'
import type { PageProps } from '../../routes'
import PageFrame from '../PageFrame'

const UNIT_LABELS: Record<string, string> = {
  request: '请求次数',
  token: 'Token 数',
  credit: 'Credits'
}

function unitLabel(u: string): string {
  return UNIT_LABELS[u] ?? u
}

// ── 用量与配额 ─────────────────────────────────────────────────────────

export function UsagePage({ def }: PageProps) {
  const q = useApi(() => getUsageOverview(), [])

  return (
    <PageFrame def={def}>
      <Banner tone="muted" title="额度归属">
        以下额度与速率属于<strong>账户</strong>，名下所有 Key 共享；Key 只提供模型黑名单（按 Key 隐藏个别模型）。
        额度是「生效值」（各组取最严的合并结果），不显示它来自哪个组。
      </Banner>

      <AsyncView
        state={q}
        loadingRows={4}
        empty={() => (
          <EmptyState
            title="没有可显示的用量数据"
            hint="账户可能还没有被分配额度，或用量接口尚未接入。请稍后刷新，或联系管理员。"
            action={<ToolButton icon={<IconRefresh size={ACTION_ICON} />} onClick={q.reload}>刷新</ToolButton>}
          />
        )}
      >
        {(u) => {
          const alerts = u.dimensions.filter((d) => d.limit > 0 && pct(d.used, d.limit) >= 80)
          return (
            <>
              {alerts.length > 0 && (
                <Banner tone="warn" title="接近额度上限">
                  以下维度已使用超过 80%：
                  {alerts.map((d) => `${unitLabel(d.unit)}（${pct(d.used, d.limit)}%）`).join('、')}。
                  额度打满后，名下<strong>全部 Key</strong> 都会受影响。
                </Banner>
              )}

              <Section title="账户">
                <div className="metric-row">
                  <div className="metric">
                    <span className="metric-value">{fmtNum(u.key_count)}</span>
                    <span className="metric-label">Key 数量{u.key_limit ? `（上限 ${fmtNum(u.key_limit)}）` : ''}</span>
                  </div>
                  <div className="metric">
                    <span className="metric-value">{fmtNum(u.active_key_count)}</span>
                    <span className="metric-label">启用中的 Key</span>
                  </div>
                  <div className="metric">
                    <span className="metric-value">{fmtNum(u.keys_used_7d)}</span>
                    <span className="metric-label">最近 7 天有调用</span>
                  </div>
                </div>
                {u.group_names.length > 0 && (
                  <p className="muted">所属组：{u.group_names.join('、')}（只读）。</p>
                )}
              </Section>

              <Section title="额度（账户级）" hint="三种维度各自独立判定，哪个先到算哪个。">
                {u.dimensions.length === 0 ? (
                  <p className="muted">账户没有设置任何额度维度（视为不限）。</p>
                ) : (
                  u.dimensions.map((d) => {
                    const p = pct(d.used, d.limit)
                    return (
                      <div className="quota-row" key={d.unit}>
                        <div className="quota-head">
                          <span>
                            {unitLabel(d.unit)} · {d.period === 'day' ? '每日' : '每月'}
                          </span>
                          <span>
                            {fmtNum(d.used)} / {fmtNum(d.limit)}（{p}%）
                          </span>
                        </div>
                        <div className="progress" role="progressbar" aria-valuenow={p} aria-valuemin={0} aria-valuemax={100}>
                          <div className={`progress-bar${p >= 100 ? ' progress-full' : p >= 80 ? ' progress-warn' : ''}`} style={{ width: `${p}%` }} />
                        </div>
                        <span className="field-hint">
                          重置时间：{d.reset_at ? fmtTime(d.reset_at) : '周期锚点由服务端维护'}
                        </span>
                      </div>
                    )
                  })
                )}
                {u.model_limits && u.model_limits.length > 0 && (
                  <>
                    <h3>按模型限额</h3>
                    <ul className="plain-list">
                      {u.model_limits.map((m) => (
                        <li key={`${m.model}-${m.unit}`}>
                          <code>{m.model}</code> · {unitLabel(m.unit)} · {fmtNum(m.used)} / {fmtNum(m.limit)}（
                          {pct(m.used, m.limit)}%）
                        </li>
                      ))}
                    </ul>
                    <p className="muted">命中按模型限额时，可以改用其他可用模型。</p>
                  </>
                )}
              </Section>

              <Section title="速率（账户级）" hint="瞬时令牌桶状态，不画历史曲线：峰值才是生效值。">
                <div className="metric-row">
                  <div className="metric"><span className="metric-value">{u.rpm_limit === -1 ? '不限' : fmtNum(u.rpm_limit)}</span><span className="metric-label">RPM</span></div>
                  <div className="metric"><span className="metric-value">{u.tpm_limit === -1 ? '不限' : fmtNum(u.tpm_limit)}</span><span className="metric-label">TPM</span></div>
                  <div className="metric"><span className="metric-value">{u.max_concurrency === -1 ? '不限' : fmtNum(u.max_concurrency)}</span><span className="metric-label">并发</span></div>
                </div>
              </Section>

              <Section title="Credits 是什么">
                <p>
                  Credits 是模型间的相对价格单位（以最便宜的内置基准模型为 1），
                  公式：<code>入站 token × 入站乘数 + 出站 token × 出站乘数</code>。
                </p>
                <p className="muted">
                  它不是钱：本站没有单价，也不会出现任何金额。同一份额度下，调用能力更强的模型会掉得更快。
                </p>
              </Section>

              <Section title="快捷入口">
                <div className="link-grid">
                  <Link to="/usage/keys">我的 API Key</Link>
                  <Link to="/usage/logs">调用日志</Link>
                  <Link to="/usage/models">可用抽象模型</Link>
                </div>
              </Section>
            </>
          )
        }}
      </AsyncView>
    </PageFrame>
  )
}

// ── 调用日志 ─────────────────────────────────────────────────────────────

// LOGS_PAGE_SIZE 是调用日志的每页行数（后端 limit 上限 200）。
// 这里与后端默认值（50）一致：不改 limit 也能翻页，改了也不会被截断。
const LOGS_PAGE_SIZE = 50
export function LogsPage({ def }: PageProps) {
  const keys = useApi(() => listKeys({ limit: 100 }), [])
  const models = useApi(() => listAvailableModels(), [])
  const [keyId, setKeyId] = useState('')
  const [model, setModel] = useState('')
  const [status, setStatus] = useState<'' | 'ok' | 'failed'>('')
  const [errorCode, setErrorCode] = useState('')
  const [offset, setOffset] = useState(0)
  // 分页只用 offset（后端 GET /api/usage/logs 只认 limit/offset，没有 cursor）。
  // 为什么先拼成一个对象再传：endpoints.ts 里的 UsageLogQuery 还没改成 offset，
  // 以字面量直接传会报“多余属性”；先绑成变量走结构化赋值，前端改完也不用回头改这里。
  const query = {
    key_id: keyId || undefined,
    model: model || undefined,
    status: status || undefined,
    error_code: errorCode || undefined,
    limit: LOGS_PAGE_SIZE,
    offset
  }
  const q = useApi(
    () => listUsageLogs(query),
    // offset 必须在依赖里：否则“下一页”只改了页码不发请求。
    // error_code 同理——它是后端已支持的筛选（修了 P3 后端忽略它的那条）。
    [keyId, model, status, errorCode, offset]
  )
  // 换筛选条件必须回到第 1 页：停在第 3 页换筛选会得到空页，看起来像“没结果”，
  // 实际只是越界。
  function filterChanged(apply: () => void) {
    apply()
    setOffset(0)
  }

  const firstModel = models.data?.data[0]?.name ?? '<抽象模型名>'
  const curl = `curl https://<你的站点>/v1/chat/completions \\
  -H "Authorization: Bearer fr-..." \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${firstModel}","messages":[{"role":"user","content":"hello"}]}'`

  function exportCsv() {
    const rows = q.data?.data ?? []
    downloadText(
      'usage-logs.csv',
      toCsv([
        ['time', 'endpoint', 'abstract_model', 'key_prefix', 'ok', 'error_code', 'ttft_ms', 'duration_ms', 'input_tokens', 'output_tokens'],
        ...rows.map((l: UsageLog) => [
          l.ts ?? '',
          l.endpoint,
          l.abstract_model,
          l.key_prefix,
          l.ok ? 'ok' : 'failed',
          l.error_code ?? '',
          l.ttft_ms ?? '',
          l.duration_ms ?? '',
          l.input_tokens ?? '',
          l.output_tokens ?? ''
        ])
      ]),
      'text/csv;charset=utf-8'
    )
  }

  return (
    <PageFrame
      def={def}
      actions={
        // 只导当前页：导全量需要后端另开导出端点（不受 limit 限制），
        // 在这里拼多次请求会让「筛选条件已变、导出还在跑」变成静默的错页。
        <ToolButton
          icon={<IconDownload size={ACTION_ICON} />}
          onClick={exportCsv}
          disabled={(q.data?.data.length ?? 0) === 0}
        >
          导出 CSV
        </ToolButton>
      }
    >
      <Banner tone="muted">
        日志只出现抽象模型名；真实落点与贡献者归因仅管理端可见。租户端不提供请求正文。
      </Banner>

      <div className="toolbar">
        <label className="toolbar-field">
          Key
          <select value={keyId} onChange={(e) => filterChanged(() => setKeyId(e.target.value))}>
            <option value="">全部</option>
            {keys.data?.data.map((k) => (
              <option key={k.id} value={k.id}>{k.name}（{k.prefix}）</option>
            ))}
          </select>
        </label>
        <label className="toolbar-field">
          抽象模型
          <select value={model} onChange={(e) => filterChanged(() => setModel(e.target.value))}>
            <option value="">全部</option>
            {models.data?.data.map((m) => (
              <option key={m.name} value={m.name}>{m.name}</option>
            ))}
          </select>
        </label>
        <label className="toolbar-field">
          状态
          <select value={status} onChange={(e) => filterChanged(() => setStatus(e.target.value as '' | 'ok' | 'failed'))}>
            <option value="">全部</option>
            <option value="ok">成功</option>
            <option value="failed">失败</option>
          </select>
        </label>
        <label className="toolbar-field">
          错误码
          <input value={errorCode} onChange={(e) => filterChanged(() => setErrorCode(e.target.value))} placeholder="no_candidate" />
        </label>
        <ToolButton icon={<IconRefresh size={ACTION_ICON} />} onClick={q.reload}>刷新</ToolButton>
      </div>

      <AsyncView
        state={q}
        loadingRows={7}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="还没有调用记录"
              hint={
                <>
                  从「注册好了」到「真的跑通」之间还有一步：先建一把 Key 并勾选模型，再发一次请求。
                  <pre className="code-block">{curl}</pre>
                  示例里的 <code>{firstModel}</code> 取自你当前可用的抽象模型。
                </>
              }
              action={<Link to="/usage/keys/new">创建 API Key</Link>}
            />
          ) : null
        }
      >
        {(d) => (
          <DataTable headers={['时间', '端点', '抽象模型', 'Key', '状态', '错误码', 'TTFT', '总耗时', '输入 / 输出 token']}>
            {d.data.map((l) => (
              <tr key={l.id}>
                <td>{fmtTime(l.ts)}</td>
                <td><code>{l.endpoint}</code></td>
                <td><code>{l.abstract_model}</code></td>
                <td>{l.key_name}（{l.key_prefix}）</td>
                <td><StatusBadge status={l.ok ? 'ok' : 'error'} label={l.ok ? '成功' : '失败'} /></td>
                <td>{l.error_code ?? '—'}</td>
                <td>{l.ttft_ms === null ? '—' : `${fmtNum(l.ttft_ms)} ms`}</td>
                <td>{l.duration_ms === null ? '—' : `${fmtNum(l.duration_ms)} ms`}</td>
                <td>{fmtNum(l.input_tokens ?? 0)} / {fmtNum(l.output_tokens ?? 0)}</td>
              </tr>
            ))}
          </DataTable>
        )}
      </AsyncView>

      {/* 分页：后端只认 limit/offset（信封里的 total 是总行数，不是本页行数）。
          只在本页不是全部时渲染，免得只有两三页时也冒出一排按钮。 */}
      {(q.data?.total ?? 0) > LOGS_PAGE_SIZE && (
        <div className="toolbar">
          <span className="muted">
            第 {offset + 1}–{offset + (q.data?.data.length ?? 0)} 条，共 {fmtNum(q.data?.total ?? 0)} 条
          </span>
          <button
            type="button"
            className="btn-ghost"
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - LOGS_PAGE_SIZE))}
          >
            上一页
          </button>
          <button
            type="button"
            className="btn-ghost"
            disabled={offset + LOGS_PAGE_SIZE >= (q.data?.total ?? 0)}
            onClick={() => setOffset(offset + LOGS_PAGE_SIZE)}
          >
            下一页
          </button>
        </div>
      )}

      <Section title="错误码怎么用">
        <ul className="plain-list">
          <li><code>no_candidate</code>：当前没有可用资源，稍后重试或换模型。</li>
          <li><code>rate_limited</code>：触发速率限制，按 Retry-After 等待。</li>
          <li><code>quota_exceeded</code>：额度打满，等周期重置或联系管理员调高。</li>
          <li><code>model_not_found</code>：名字不在你当前可见集合里（组授权减去 Key 黑名单）。</li>
        </ul>
        <p className="muted">完整映射见文档 docs/api/03-errors.md；日志保留期尚未定案。</p>
      </Section>
    </PageFrame>
  )
}

// ── 可用抽象模型 ───────────────────────────────────────────────────────

export function ModelsPage({ def }: PageProps) {
  const keys = useApi(() => listKeys({ limit: 100 }), [])
  const [keyId, setKeyId] = useState('')
  const models = useApi(() => listAvailableModels(keyId || undefined), [keyId])
  const [copied, setCopied] = useState<string | null>(null)

  async function copy(name: string) {
    try {
      await navigator.clipboard.writeText(name)
      setCopied(name)
      window.setTimeout(() => setCopied(null), 1500)
    } catch {
      setCopied(null)
    }
  }

  return (
    <PageFrame def={def}>
      <Banner tone="muted">
        这里列的是「你所属组的授权」减去「当前 Key 的黑名单」的收敛结果；未在此列出的名字调用会失败。
        弃用的模型仍然接受请求，只是建议迁移。
      </Banner>
      <div className="toolbar">
        <label className="toolbar-field">
          按 Key 查看
          <select value={keyId} onChange={(e) => setKeyId(e.target.value)}>
            <option value="">全部（组授权）</option>
            {keys.data?.data.map((k) => (
              <option key={k.id} value={k.id}>{k.name}（{k.prefix}）</option>
            ))}
          </select>
        </label>
      </div>
      {/* 黑名单为空是常态（= 不收窄）——不再提示；只有「可见集合真的为空」
          才在下面的空态里解释（组授权没给任何模型）。 */}


      <AsyncView
        state={models}
        loadingRows={4}
        empty={(d) =>
          d.data.length === 0 ? (
            <EmptyState
              title="没有可用的抽象模型"
              hint="你所属的组当前没有授权任何模型（或这些模型正好都在选中 Key 的黑名单里）。请联系管理员或调整 Key 黑名单。"
              action={<Link to="/usage/keys">管理 Key 黑名单</Link>}
            />
          ) : null
        }
      >
        {(d) => (
          <div className="model-grid">
            {d.data.map((m) => (
              <article className="model-card" key={m.name}>
                <header>
                  <h3><code>{m.name}</code></h3>
                  <StatusBadge status={m.status} label={statusLabel(m.status)} />
                </header>
                <p className="muted">{m.display_name}</p>
                {/* 这里曾经渲染上下文窗口/最大输出/能力三个键值（取自
                    capabilities），但后端该字段恒为空对象（无存储列，也没有
                    对租户可信的来源），界面上只会出现一排“—”。后端已删该
                    字段：无值比假空值诚实，真有能力数据时再加回来。 */}
                {m.status === 'deprecated' && (
                  <Banner tone="warn">
                    建议改用 {m.replacement ?? '替代模型'}。弃用不是关闭，请求仍然可用。
                    {m.migration_doc && (
                      <>
                        {' '}迁移指引：<a href={m.migration_doc} target="_blank" rel="noreferrer">{m.migration_doc}</a>
                      </>
                    )}
                  </Banner>
                )}
                <button type="button" className="btn-ghost" onClick={() => void copy(m.name)}>
                  {copied === m.name ? '已复制' : '复制模型名'}
                </button>
              </article>
            ))}
          </div>
        )}
      </AsyncView>

      <Section title="可见性说明">
        <p className="muted">
          可见集合 =（∪ 所属组的抽象模型授权）减去 Key 黑名单。不同 Key 的黑名单可能不同，
          所以同一模型在 A Key 上能用、在 B Key 上不能。渠道与真实模型构成对租户不可见。
        </p>
      </Section>
    </PageFrame>
  )
}
