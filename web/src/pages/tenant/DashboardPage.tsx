// 仪表盘 `/usage/dashboard`（docs/ui/04-tenant.md 4.4；形态参照 Tingly Box 的
// 用量仪表盘）。
//
// 页面结构（自上而下）：
//   1. 时间范围 + 口径切换（admin）——PageFrame 的主操作区
//   2. 五张指标卡：总请求数 / 总 Token / 错误率 / 平均首字延迟 / 活跃 Key
//   3. Token 用量随时间变化（堆叠面积图，输入/输出两列；recharts）
//   4. 按模型用量（占比条）
//   5. 最近请求（仅 scope=own：日志端点只有自己账户的读面）
//
// 与 Tingly Box 的差异都是数据面决定：它有缓存命中率与流式占比两卡，本站
// requests 表没有缓存与流式标记，硬造就是假数——换成错误率同源的两张延迟/
// 活跃 Key 卡。图表为什么不画「按请求」视图：单请求粒度在日志页已有表格，
// 仪表盘只承担聚合。

import { useMemo, useState } from 'react'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis
} from 'recharts'
import { Box, Typography } from '@mui/material'
import { IconRefresh } from '../../icons'
import { getUsageStats, listUsageLogs } from '../../api/endpoints'
import type { UsageStatsBucket } from '../../api/types'
import { useSession } from '../../api/session'
import { useApi } from '../../hooks/useApi'
import { useColorMode } from '../../colorMode'
import { DARK, LIGHT } from '../../tokens'
import { AsyncView, DataTable, EmptyState, Section } from '../../components/ui'
import PageFrame from '../PageFrame'
import type { PageProps } from '../../routes'
import { fmtNum, fmtTime } from '../../utils/format'

/** 时间范围档（Tingly Box 同款六档）：key → 天数（0 = 今天，-1 = 昨天）。 */
const RANGES = [
  { key: 'today', label: '今天', days: 0 },
  { key: 'yesterday', label: '昨天', days: -1 },
  { key: '3d', label: '近 3 天', days: 3 },
  { key: '7d', label: '近 7 天', days: 7 },
  { key: '30d', label: '近 30 天', days: 30 },
  { key: '90d', label: '近 90 天', days: 90 }
] as const

type RangeKey = (typeof RANGES)[number]['key']
type Scope = 'own' | 'all'

function startOfDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate())
}

/** 范围档 → [from, to]（本地时区）+ 桶宽：≤3 天按小时，更长按天。 */
function rangeWindow(key: RangeKey): { from: Date; to: Date; bucket: 'hour' | 'day' } {
  const now = new Date()
  const today = startOfDay(now)
  if (key === 'today') return { from: today, to: now, bucket: 'hour' }
  if (key === 'yesterday') {
    const from = new Date(today)
    from.setDate(from.getDate() - 1)
    return { from, to: today, bucket: 'hour' }
  }
  const days = RANGES.find((r) => r.key === key)?.days ?? 7
  const from = new Date(today)
  from.setDate(from.getDate() - (days - 1))
  return { from, to: now, bucket: 'day' }
}

/** 分段选择器：选中项主色实心块（与壳层两级导航同一选中语言）。 */
function Segmented<T extends string>({
  options,
  value,
  onChange,
  label
}: {
  options: { key: T; label: string }[]
  value: T
  onChange: (v: T) => void
  label: string
}) {
  return (
    <div className="seg" role="group" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.key}
          type="button"
          className={o.key === value ? 'seg-item seg-on' : 'seg-item'}
          aria-pressed={o.key === value}
          onClick={() => onChange(o.key)}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

/** 指标卡：数字是主角（2rem + tabular-nums），sub 是同源补语、没有就留空。 */
function DashCard({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="dash-card">
      <span className="metric-value dash-value">{value}</span>
      <span className="metric-label">{label}</span>
      {sub ? <span className="dash-sub">{sub}</span> : null}
    </div>
  )
}

/** Token 数字缩写（图表轴用）：千/万/亿三档，中文站比 K/M 直读。 */
function compact(n: number): string {
  if (n >= 1e8) return `${(n / 1e8).toFixed(1)}亿`
  if (n >= 1e4) return `${(n / 1e4).toFixed(n >= 1e5 ? 0 : 1)}万`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`
  return String(n)
}

/**
 * 把服务端「只含有数据的桶」铺成连续序列：空窗补零。
 * 没有这一步，面积图的 X 轴会把相邻两个有数据的桶画成紧挨着，
 * 「三天没用」在图上看起来像「数据点很密」。
 */
function fillBuckets(
  from: Date,
  to: Date,
  bucketSeconds: number,
  rows: UsageStatsBucket[]
): { t: number; inTok: number; outTok: number; requests: number }[] {
  const byStart = new Map(rows.map((r) => [Math.floor(new Date(r.start).getTime() / 1000), r]))
  const out: { t: number; inTok: number; outTok: number; requests: number }[] = []
  const fromSec = Math.floor(from.getTime() / 1000)
  const toSec = Math.floor(to.getTime() / 1000)
  // 桶起点与服务端同法：当地偏移后的整除（服务端已把 tz 折进 start）。
  const step = bucketSeconds
  const first = Math.floor((fromSec + 3600) / step) * step // hour 桶按整点对齐
  for (let t = first; t <= toSec; t += step) {
    const row = byStart.get(t)
    out.push({
      t,
      inTok: row?.input_tokens ?? 0,
      outTok: row?.output_tokens ?? 0,
      requests: row?.requests ?? 0
    })
  }
  // 桶数异常（比如 to 刚好压线）时兜底：至少把有数据的行画出来。
  if (out.length === 0 && rows.length > 0) {
    return rows.map((r) => ({
      t: Math.floor(new Date(r.start).getTime() / 1000),
      inTok: r.input_tokens,
      outTok: r.output_tokens,
      requests: r.requests
    }))
  }
  return out
}

export default function DashboardPage({ def }: PageProps) {
  const { session } = useSession()
  const isAdmin = session?.classes.includes('admin') ?? false
  const [range, setRange] = useState<RangeKey>('7d')
  // admin 默认看整站（Tingly Box 的 owner 视角）；租户只看自己且不见切换。
  const [scope, setScope] = useState<Scope>(isAdmin ? 'all' : 'own')
  const { mode } = useColorMode()
  const palette = mode === 'dark' ? DARK : LIGHT

  const stats = useApi(() => {
    const { from, to, bucket } = rangeWindow(range)
    return getUsageStats({
      from: from.toISOString(),
      to: to.toISOString(),
      bucket,
      tz_offset: -new Date().getTimezoneOffset() * 60,
      scope
    })
  }, [range, scope])

  // 最近请求只在自己的口径下显示（日志端点没有整站读面）。
  const recent = useApi(() => listUsageLogs({ limit: 8 }), [scope === 'own'])

  const series = useMemo(() => {
    if (!stats.data) return []
    const { from } = rangeWindow(range)
    return fillBuckets(from, new Date(stats.data.to), stats.data.bucket_seconds, stats.data.buckets)
  }, [stats.data, range])

  const totals = stats.data?.totals
  const errorRate = totals && totals.requests > 0 ? totals.failed / totals.requests : null
  const maxModelTokens = useMemo(() => {
    if (!stats.data) return 0
    return Math.max(1, ...stats.data.models.map((m) => m.input_tokens + m.output_tokens))
  }, [stats.data])

  const tick = (t: number) => {
    const d = new Date(t * 1000)
    if (stats.data?.bucket_seconds === 3600) {
      return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    }
    return `${d.getMonth() + 1}/${d.getDate()}`
  }

  return (
    <PageFrame
      def={def}
      actions={
        <>
          <Segmented
            label="时间范围"
            options={RANGES.map((r) => ({ key: r.key, label: r.label }))}
            value={range}
            onChange={setRange}
          />
          {isAdmin && (
            <Segmented
              label="统计口径"
              options={[
                { key: 'all' as Scope, label: '全站' },
                { key: 'own' as Scope, label: '我的' }
              ]}
              value={scope}
              onChange={setScope}
            />
          )}
          <button type="button" className="btn-ghost" onClick={stats.reload}>
            <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}>
              <IconRefresh size="sm" />
              刷新
            </Box>
          </button>
        </>
      }
    >
      <AsyncView state={stats} loadingRows={3}>
        {(d) =>
          d.totals.requests === 0 && d.models.length === 0 ? (
            <Section title="这个范围里还没有请求">
              <EmptyState
                title="暂无用量数据"
                hint="换个更长的范围，或稍后再试。发出的请求会实时出现在这里。"
              />
            </Section>
          ) : (
            <div className="stack">
              <div className="dash-grid">
                <DashCard
                  label="总请求数"
                  value={fmtNum(d.totals.requests)}
                  sub={d.totals.failed > 0 ? `失败 ${fmtNum(d.totals.failed)}` : undefined}
                />
                <DashCard
                  label="总 Token"
                  value={fmtNum(d.totals.input_tokens + d.totals.output_tokens)}
                  sub={`输入 ${fmtNum(d.totals.input_tokens)} · 输出 ${fmtNum(d.totals.output_tokens)}`}
                />
                <DashCard
                  label="错误率"
                  value={errorRate === null ? '—' : `${(errorRate * 100).toFixed(1)}%`}
                  sub={errorRate === null ? '范围内没有请求' : undefined}
                />
                <DashCard
                  label="平均首字延迟"
                  value={d.totals.avg_ttft_ms === null ? '—' : `${fmtNum(d.totals.avg_ttft_ms)} ms`}
                  sub={
                    d.totals.avg_duration_ms === null
                      ? undefined
                      : `平均总耗时 ${fmtNum(d.totals.avg_duration_ms)} ms`
                  }
                />
                <DashCard label="活跃 Key" value={fmtNum(d.totals.active_keys)} />
              </div>

              <Section
                title="Token 用量随时间变化"
                hint={
                  d.bucket_seconds === 3600
                    ? '每小时一格；堆叠面积为输入 + 输出 Token。'
                    : '每天一格；堆叠面积为输入 + 输出 Token（按本地时区切天）。'
                }
              >
                <div className="dash-chart">
                  <ResponsiveContainer width="100%" height={300}>
                    <AreaChart data={series} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                      <CartesianGrid stroke={palette.divider} vertical={false} />
                      <XAxis
                        dataKey="t"
                        tickFormatter={tick}
                        tick={{ fill: palette.outline, fontSize: 12 }}
                        axisLine={{ stroke: palette.divider }}
                        tickLine={false}
                        minTickGap={24}
                      />
                      <YAxis
                        tickFormatter={compact}
                        tick={{ fill: palette.outline, fontSize: 12 }}
                        axisLine={false}
                        tickLine={false}
                        width={56}
                      />
                      <Tooltip
                        formatter={(value, name) => [fmtNum(Number(value)), String(name)] as [string, string]}
                        labelFormatter={(t) => fmtTime(new Date(Number(t) * 1000).toISOString())}
                        contentStyle={{
                          background: palette.floating,
                          border: 'none',
                          borderRadius: 12,
                          boxShadow: palette.e1,
                          color: palette['on-surface']
                        }}
                      />
                      <Area
                        type="monotone"
                        dataKey="inTok"
                        name="输入"
                        stackId="tok"
                        stroke={palette.primary}
                        fill={palette.primary}
                        fillOpacity={0.55}
                        strokeWidth={1.75}
                      />
                      <Area
                        type="monotone"
                        dataKey="outTok"
                        name="输出"
                        stackId="tok"
                        stroke={palette.info}
                        fill={palette.info}
                        fillOpacity={0.55}
                        strokeWidth={1.75}
                      />
                    </AreaChart>
                  </ResponsiveContainer>
                </div>
              </Section>

              {d.models.length > 0 && (
                <Section
                  title="按模型用量"
                  hint={
                    scope === 'all'
                      ? '整站聚合；占比按输入 + 输出 Token 计。'
                      : '你的调用聚合；占比按输入 + 输出 Token 计。'
                  }
                >
                  <ul className="dash-models">
                    {d.models.map((m) => {
                      const total = m.input_tokens + m.output_tokens
                      return (
                        <li key={m.model || '(已删除模型)'} className="dash-model">
                          <span className="dash-model-name">
                            {m.model || <span className="muted">（已删除的模型）</span>}
                          </span>
                          <span className="dash-model-bar" aria-hidden="true">
                            <span
                              style={{
                                width: `${Math.max(2, Math.round((total / maxModelTokens) * 100))}%`
                              }}
                            />
                          </span>
                          <span className="dash-model-meta">
                            {fmtNum(m.requests)} 次 · {fmtNum(total)} tok
                            {m.failed > 0 ? ` · 失败 ${fmtNum(m.failed)}` : ''}
                          </span>
                        </li>
                      )
                    })}
                  </ul>
                </Section>
              )}

              {scope === 'own' && (
                <Section
                  title="最近请求"
                  actions={
                    <Typography variant="body2" component="span" className="inline-links">
                      全部记录在调用日志页。
                    </Typography>
                  }
                >
                  <AsyncView state={recent} loadingRows={3}>
                    {(rd) =>
                      rd.data.length === 0 ? (
                        <p className="muted">这个范围里没有你发起的请求。</p>
                      ) : (
                        <DataTable headers={['时间', '模型', '状态', '输入', '输出', '总耗时']}>
                          {rd.data.map((row) => (
                            <tr key={row.id}>
                              <td>{fmtTime(row.ts)}</td>
                              <td>{row.abstract_model}</td>
                              <td>
                                {row.ok ? (
                                  <span className="badge badge-ok">成功</span>
                                ) : (
                                  <span className="badge badge-err">
                                    {row.error_code ?? '失败'}
                                  </span>
                                )}
                              </td>
                              <td>{row.input_tokens === null ? '—' : fmtNum(row.input_tokens)}</td>
                              <td>{row.output_tokens === null ? '—' : fmtNum(row.output_tokens)}</td>
                              <td>
                                {row.duration_ms === null ? '—' : `${fmtNum(row.duration_ms)} ms`}
                              </td>
                            </tr>
                          ))}
                        </DataTable>
                      )
                    }
                  </AsyncView>
                </Section>
              )}
            </div>
          )
        }
      </AsyncView>
    </PageFrame>
  )
}
