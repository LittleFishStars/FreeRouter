// 展示层的小工具：时间、数字、状态词、CSV 下载。
//
// 状态词表来自 docs/ui/06-design.md 6.3（对象 → 用户可见中文词），
// 页面不得直接把英文枚举值渲染给用户。

const STATUS_LABELS: Record<string, string> = {
  // 账户
  pending: '待审核',
  active: '正常',
  disabled: '已禁用',
  rejected: '已拒绝',
  merged: '已合并',
  // API Key
  expired: '已过期',
  // 抽象模型
  deprecated: '已弃用',
  // 凭据
  invalid: '已失效',
  suspect: '未通过',
  verified: '已鉴定',
  trusted: '官方来源',
  unverified: '未鉴定',
  // 上游/组
  missing: '已消失',
  warning: '警告',
  error: '严重',
  info: '提示',
  ok: '成功',
  denied: '被拒绝'
}

export function statusLabel(v: string | null | undefined): string {
  if (!v) return '—'
  return STATUS_LABELS[v] ?? v
}

/** 权限类的用户可见称呼（不使用「权限类」这个内部术语当动词）。 */
export const CLASS_LABELS: Record<string, string> = {
  admin: '管理员',
  tenant: '调用者',
  contributor: '贡献者'
}

export function classLabel(c: string): string {
  return CLASS_LABELS[c] ?? c
}

/** 收件箱类型（docs/ui/03-admin.md 12.1）。 */
export const INBOX_KIND_LABELS: Record<string, string> = {
  registration: '注册申请',
  model_change: '模型变动',
  failure: '故障通知',
  pool_alert: '池告警',
  breaker_alert: '熔断告警'
}

export function inboxKindLabel(kind: string): string {
  return INBOX_KIND_LABELS[kind] ?? kind
}

/** 时间：接受 RFC3339 字符串或 epoch 秒（后端两种都可能给）。 */
export function fmtTime(v: string | number | null | undefined): string {
  if (v === null || v === undefined || v === '') return '—'
  const d = typeof v === 'number' ? new Date(v * 1000) : new Date(v)
  if (Number.isNaN(d.getTime())) return String(v)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 仅日期。 */
export function fmtDate(v: string | number | null | undefined): string {
  const t = fmtTime(v)
  return t === '—' ? t : t.slice(0, 10)
}

/** 数字：千分位；null 用破折号。 */
export function fmtNum(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  return v.toLocaleString('zh-CN')
}

/** 限额：-1 = 不限（docs/api/02-admin.md 5.2，界面不显示 -1）。 */
export function fmtLimit(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  if (v === -1) return '不限'
  if (v === 0) return '0（全拒）'
  return fmtNum(v)
}

/** 百分比（0..100，钳制）。 */
export function pct(used: number, limit: number): number {
  if (!limit || limit <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((used / limit) * 100)))
}

/** 截断长串（凭据/模型名列表）。 */
export function truncate(s: string, n = 40): string {
  return s.length > n ? `${s.slice(0, n)}…` : s
}

/** 触发浏览器下载（一次性展示、导出结果等）。 */
export function downloadText(filename: string, text: string, mime = 'text/plain;charset=utf-8') {
  const blob = new Blob([text], { type: mime })
  downloadBlob(filename, blob)
}

export function downloadBlob(filename: string, blob: Blob) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 释放要留出一帧：部分浏览器在 click 同步结束后才真正开始读取
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** 客户端 CSV 导出（管理端「导出 CSV」在没有端点时也能用）。 */
export function toCsv(rows: (string | number | boolean | null | undefined)[][]): string {
  const esc = (v: string | number | boolean | null | undefined) => {
    const s = v === null || v === undefined ? '' : String(v)
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  return rows.map((r) => r.map(esc).join(',')).join('\n')
}

/** JSON 展示（错误详情、证据、审计 detail）。 */
export function prettyJson(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

/** 剩余会话时长提示（docs/ui/02-public.md 3.3：剩余 1 小时内提示）。 */
export function sessionExpiringSoon(expiresAt?: string): boolean {
  if (!expiresAt) return false
  const t = new Date(expiresAt).getTime()
  if (Number.isNaN(t)) return false
  const left = t - Date.now()
  return left > 0 && left < 60 * 60 * 1000
}
