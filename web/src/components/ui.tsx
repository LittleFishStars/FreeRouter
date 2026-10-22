// 通用展示组件：四种页面状态的统一形态（docs/ui/06-design.md 第 4 节）。
//
// 每个列表页与详情页都必须有空态 / 加载态 / 错误态；缺一种就是缺陷。
// 另外「接口尚未接入」是独立状态：后端 404 not_found 时不能伪装成错误，
// 也不能伪造数据——它说明该能力还没上线（与 docs/api/02-admin.md §2.2
// 对无权限统一 404 的口径一致，界面上分开表述）。

import type { ReactNode } from 'react'
import { ApiError, isUnavailable } from '../api/client'
import { prettyJson } from '../utils/format'

export type Tone = 'ok' | 'warn' | 'err' | 'info' | 'muted'

export function Badge({ tone = 'muted', children, title }: { tone?: Tone; children: ReactNode; title?: string }) {
  return (
    <span className={`badge badge-${tone}`} title={title}>
      {children}
    </span>
  )
}

/** 状态徽章：状态词 + 颜色以外的第二重信号（图标），docs/ui/06-design.md 第 8 节。 */
export function StatusBadge({ status, label }: { status: string; label: string }) {
  const tone: Tone =
    status === 'active' || status === 'verified' || status === 'trusted' || status === 'ok'
      ? 'ok'
      : status === 'pending' || status === 'unverified'
        ? 'info'
        : status === 'warning' || status === 'suspect' || status === 'deprecated'
          ? 'warn'
          : status === 'disabled' || status === 'rejected' || status === 'invalid' || status === 'missing' || status === 'error'
            ? 'err'
            : 'muted'
  const icon = tone === 'ok' ? '✓' : tone === 'warn' ? '!' : tone === 'err' ? '×' : tone === 'info' ? '·' : '—'
  return (
    <span className={`badge badge-${tone}`} title={label}>
      <span aria-hidden="true">{icon}</span> {label}
    </span>
  )
}

/** 运行态 / 提示横幅（不可关闭的常量说明条用 muted 或 info）。 */
export function Banner({
  tone = 'info',
  title,
  children
}: {
  tone?: Tone
  title?: string
  children: ReactNode
}) {
  return (
    <div className={`banner banner-${tone}`} role={tone === 'err' ? 'alert' : undefined}>
      {title && <strong>{title}</strong>}
      <div>{children}</div>
    </div>
  )
}

/** 加载态：骨架屏（不用全屏遮罩，避免等待期间无法离开页面）。 */
export function LoadingBlock({ rows = 3, label = '正在加载…' }: { rows?: number; label?: string }) {
  return (
    <div className="loading-block" role="status" aria-live="polite" aria-label={label}>
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="skeleton-row" />
      ))}
      <span className="sr-only">{label}</span>
    </div>
  )
}

/** 空态：一句解释 + 一个下一步动作（docs/ui/06-design.md 4）。 */
export function EmptyState({
  title,
  hint,
  action
}: {
  title: string
  hint?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="state-card empty-state">
      <h3>{title}</h3>
      {hint && <p>{hint}</p>}
      {action && <div className="state-action">{action}</div>}
    </div>
  )
}

/** 接口尚未接入：后端 404 not_found 的专门表述（不伪造数据、不谎报故障）。 */
export function UnavailableState({ what, retry }: { what: string; retry?: () => void }) {
  return (
    <div className="state-card unavailable-state">
      <h3>该功能尚未接入</h3>
      <p>
        {what}的接口在当前版本还未提供（服务端返回 <code>not_found</code>）。
        界面已按契约实现，待后端上线后无需改动即可使用。
      </p>
      {retry && <button type="button" onClick={retry}>重新检查</button>}
    </div>
  )
}

/** 错误态：错误码 + 人话 + 请求 ID + 重试（docs/ui/06-design.md 4）。 */
export function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  if (isUnavailable(error)) {
    return <UnavailableState what="此页依赖的接口" retry={retry} />
  }
  const msg = error instanceof Error ? error.message : String(error)
  const code = error instanceof ApiError ? error.code : 'unknown_error'
  const requestId = error instanceof ApiError ? error.requestId : undefined
  return (
    <div className="state-card error-state" role="alert">
      <h3>请求失败</h3>
      <p>{msg}</p>
      <p className="meta-line">
        错误码：<code>{code}</code>
        {requestId && (
          <>
            {' · '}请求 ID：<code>{requestId}</code>
          </>
        )}
      </p>
      {retry && (
        <button type="button" onClick={retry}>
          重试
        </button>
      )}
    </div>
  )
}

/** 局部失败的内联提示（成功部分照常渲染）。 */
export function InlineError({ error }: { error: unknown }) {
  if (!error) return null
  const msg = error instanceof Error ? error.message : String(error)
  const code = error instanceof ApiError ? error.code : 'unknown_error'
  return (
    <p className="inline-error" role="alert">
      {msg} <span className="muted">（{code}）</span>
    </p>
  )
}

/**
 * AsyncView：把 useApi 的四态收敛成一次渲染。
 * children 只在 data 存在时调用，避免页面到处写 `if (!data) return …`。
 */
export function AsyncView<T>({
  state,
  children,
  loadingRows,
  empty
}: {
  state: { data: T | undefined; error: unknown; loading: boolean; reload: () => void }
  children: (data: T) => ReactNode
  loadingRows?: number
  /** data 判定为空时渲染空态（由调用方给）。 */
  empty?: (data: T) => ReactNode
}) {
  if (state.loading) return <LoadingBlock rows={loadingRows} />
  if (state.error) return <ErrorState error={state.error} retry={state.reload} />
  if (state.data === undefined) return <LoadingBlock rows={loadingRows} />
  if (empty) {
    const e = empty(state.data)
    if (e) return <>{e}</>
  }
  return <>{children(state.data)}</>
}

/** 分区：带标题的内容块（详情页按区块给骨架）。 */
export function Section({
  title,
  actions,
  children,
  hint
}: {
  title: string
  actions?: ReactNode
  children: ReactNode
  hint?: ReactNode
}) {
  return (
    <section className="panel">
      <header className="panel-header">
        <h2>{title}</h2>
        {actions && <div className="panel-actions">{actions}</div>}
      </header>
      {hint && <p className="panel-hint">{hint}</p>}
      {children}
    </section>
  )
}

/** 字段：标签 + 控件 + 恒定占位的帮助文本（docs/ui/06-design.md 3.1）。 */
export function Field({
  label,
  required,
  hint,
  error,
  children
}: {
  label: string
  required?: boolean
  hint?: ReactNode
  error?: string | null
  children: ReactNode
}) {
  return (
    <label className={`field${error ? ' field-invalid' : ''}`}>
      <span className="field-label">
        {label}
        {required && (
          <span aria-hidden="true" className="required">
            {' '}*
          </span>
        )}
      </span>
      {children}
      {error ? <span className="field-error">{error}</span> : hint ? <span className="field-hint">{hint}</span> : null}
    </label>
  )
}

/** 表格容器：窄屏转卡片由 CSS 断点承担。 */
export function DataTable({ headers, children, empty }: { headers: ReactNode[]; children: ReactNode; empty?: boolean }) {
  return (
    <div className="table-wrap">
      <table className="data-table">
        <thead>
          <tr>
            {headers.map((h, i) => (
              <th key={i}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>{empty ? (
          <tr>
            <td colSpan={headers.length} className="table-empty">暂无数据</td>
          </tr>
        ) : children}</tbody>
      </table>
    </div>
  )
}

/** 键值详情列表。 */
export function KeyValues({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="kv">
      {items.map((it) => (
        <div key={it.label} className="kv-row">
          <dt>{it.label}</dt>
          <dd>{it.value}</dd>
        </div>
      ))}
    </dl>
  )
}

/** 折叠的机器可读详情（错误码/证据/审计 detail）。 */
export function JsonDetails({ summary = '查看原始数据', value }: { summary?: string; value: unknown }) {
  return (
    <details className="json-details">
      <summary>{summary}</summary>
      <pre>{prettyJson(value)}</pre>
    </details>
  )
}

/** 表单级错误（提交失败保留已填内容，只加这行提示）。 */
export function FormError({ error }: { error: unknown }) {
  if (!error) return null
  const msg = error instanceof Error ? error.message : String(error)
  const code = error instanceof ApiError ? error.code : 'unknown_error'
  return (
    <p className="form-error" role="alert">
      {msg} <span className="muted">（{code}）</span>
    </p>
  )
}
