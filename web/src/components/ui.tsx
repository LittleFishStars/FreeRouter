// 通用展示组件：四种页面状态的统一形态（docs/ui/06-design.md 第 4 节）。
//
// 每个列表页与详情页都必须有空态 / 加载态 / 错误态；缺一种就是缺陷。
// 另外「接口尚未接入」是独立状态：后端 404 not_found 时不能伪装成错误，
// 也不能伪造数据——它说明该能力还没上线（与 docs/api/02-admin.md §2.2
// 对无权限统一 404 的口径一致，界面上分开表述）。
//
// ## 迁移到 MUI 时的两条硬约束（本文件的设计边界）
//
// 1. **导出签名零改动**。38 个页面文件都只是消费这些组件；把签名改掉的代价是
//    改 38 个文件、并在这过程中引入与本次重设计无关的回归。
// 2. **`Field` 与 `DataTable` 的 children 形态不能变**：页面传进来的是原生
//    `<input>`/`<select>` 和 `<tr>`/`<td>`，不是 MUI 的受控组件。所以这两个
//    组件只换「外壳」（标签、提示、行距、表头样式），不强行替换内部控件。
//    原生控件的观感由 theme.ts 的 MuiCssBaseline 统一接管。

import type { ReactNode } from 'react'
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  AlertTitle,
  Box,
  Card,
  CardContent,
  Chip,
  Divider,
  Skeleton,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
  useTheme
} from '@mui/material'
import { IconChevronDown } from '../icons'
import { ApiError, isUnavailable } from '../api/client'
import { SURFACE } from '../theme'
import { ALERT_GLYPH, STATUS_GLYPH } from '../icons'
import { prettyJson } from '../utils/format'

export type Tone = 'ok' | 'warn' | 'err' | 'info' | 'muted'

/** Tone → MUI Chip/Alert 的语义色。muted 用 default（灰）。 */
const TONE_COLOR: Record<Tone, 'success' | 'warning' | 'error' | 'info' | 'default'> = {
  ok: 'success',
  warn: 'warning',
  err: 'error',
  info: 'info',
  muted: 'default'
}

/** Tone → Alert severity（Banner 用）。muted 在 Alert 里没有对应档，降到 info。 */
const TONE_SEVERITY: Record<Tone, 'success' | 'warning' | 'error' | 'info'> = {
  ok: 'success',
  warn: 'warning',
  err: 'error',
  info: 'info',
  muted: 'info'
}

/** severity → 已统一的线性图标（MUI Alert 的 icon 槽）。 */
export function AlertGlyph({ severity }: { severity: 'success' | 'warning' | 'error' | 'info' }) {
  const G = ALERT_GLYPH[severity]
  return <G size="md" />
}

export function Badge({ tone = 'muted', children, title }: { tone?: Tone; children: ReactNode; title?: string }) {
  return (
    <Chip
      label={children}
      color={TONE_COLOR[tone]}
      size="small"
      variant={tone === 'muted' ? 'outlined' : 'filled'}
      title={title}
      sx={{ fontWeight: 600 }}
    />
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
  // 图标是第二重信号：色盲用户单靠颜色无法区分四态（设计文档第 8 节）。
  // 用图标而不是 ✓/!/× 这类 Unicode 字符：字符在不同平台落到不同字体的
  // 不同字重上，四个徽章里的"符号"会看起来粗细不一（这正是风格不统一）。
  const Glyph = STATUS_GLYPH[tone]
  return (
    <Chip
      label={
        <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}>
          <Box component="span" sx={{ display: 'inline-flex', opacity: 0.85, ml: -0.25 }}>
            <Glyph size="xs" />
          </Box>
          {label}
        </Box>
      }
      color={TONE_COLOR[tone]}
      size="small"
      variant={tone === 'muted' ? 'outlined' : 'filled'}
      title={label}
      sx={{ fontWeight: 600 }}
    />
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
    <Alert
      severity={TONE_SEVERITY[tone]}
      variant={tone === 'muted' ? 'outlined' : 'standard'}
      // 与 Section/Card 的 mb:2 同一口径：提示条与后续内容之间保持标准区块距，
      // 不然 Banner 会贴着下一块（2026-10-30 用户实测概览页贴边）。
      sx={{ mb: 2, alignItems: 'center' }}
      // 不给 icon 的话 MUI 会塞它自带的实心图标（与全站线性图标两套画法）
      icon={<AlertGlyph severity={TONE_SEVERITY[tone]} />}
    >
      {title ? <AlertTitle sx={{ fontWeight: 700, mb: 0.25 }}>{title}</AlertTitle> : null}
      <Box sx={{ '& > :last-child': { mb: 0 } }}>{children}</Box>
    </Alert>
  )
}

/** 加载态：骨架屏（不用全屏遮罩，避免等待期间无法离开页面）。 */
export function LoadingBlock({ rows = 3, label = '正在加载…' }: { rows?: number; label?: string }) {
  return (
    <Box role="status" aria-live="polite" aria-label={label} sx={{ width: '100%' }}>
      <Stack spacing={1}>
        {Array.from({ length: rows }).map((_, i) => (
          <Skeleton
            key={i}
            variant="rounded"
            height={i === 0 ? 44 : 32}
            // 逐行略微递减宽度，视觉上像内容在填充而不是几条等长灰条
            width={i === 0 ? '100%' : `${92 - i * 4}%`}
          />
        ))}
      </Stack>
      <Box
        sx={{
          position: 'absolute',
          width: 1,
          height: 1,
          overflow: 'hidden',
          clip: 'rect(0 0 0 0)',
          whiteSpace: 'nowrap'
        }}
      >
        {label}
      </Box>
    </Box>
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
    // 空态不再用虚线框：虚线在控制台里读起来像「这里坏了 / 还没做完」。
    // 改成一块比所在面深一档的填充区（FlClash 的做法），它是一块安静的地方，
    // 不是一根警线。
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        textAlign: 'center',
        gap: 1,
        py: 5.5,
        px: 3,
        bgcolor: SURFACE.block,
        borderRadius: 3
      }}
    >
      <Typography variant="h3" color="text.secondary">
        {title}
      </Typography>
      {hint ? (
        <Typography variant="body2" color="text.secondary" sx={{ maxWidth: 520 }}>
          {hint}
        </Typography>
      ) : null}
      {action ? <Box sx={{ mt: 1 }}>{action}</Box> : null}
    </Box>
  )
}

/** 接口尚未接入：后端 404 not_found 的专门表述（不伪造数据、不谎报故障）。 */
export function UnavailableState({ what, retry }: { what: string; retry?: () => void }) {
  return (
    <Alert
      severity="info"
      variant="outlined"
      action={
        retry ? (
          <Box
            component="button"
            type="button"
            onClick={retry}
            sx={{
              all: 'unset',
              cursor: 'pointer',
              fontWeight: 600,
              fontSize: '0.82rem',
              color: 'primary.main',
              px: 1
            }}
          >
            重新检查
          </Box>
        ) : undefined
      }
    >
      <AlertTitle sx={{ fontWeight: 700 }}>该功能尚未接入</AlertTitle>
      {what}的接口在当前版本还未提供（服务端返回 <code>not_found</code>）。界面已按契约实现，待后端上线后无需改动即可使用。
    </Alert>
  )
}

/** 错误态：错误码 + 人话 + 请求 ID + 重试（docs/ui/06-design.md 4）。 */
export function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  if (isUnavailable(error)) {
    return <UnavailableState what="此页依赖" retry={retry} />
  }
  const msg = error instanceof Error ? error.message : String(error)
  const code = error instanceof ApiError ? error.code : 'unknown_error'
  const requestId = error instanceof ApiError ? error.requestId : undefined
  return (
    <Alert
      severity="error"
      variant="outlined"
      action={
        retry ? (
          <Box
            component="button"
            type="button"
            onClick={retry}
            sx={{
              all: 'unset',
              cursor: 'pointer',
              fontWeight: 600,
              fontSize: '0.82rem',
              color: 'primary.main',
              px: 1
            }}
          >
            重试
          </Box>
        ) : undefined
      }
    >
      <AlertTitle sx={{ fontWeight: 700 }}>请求失败</AlertTitle>
      <Typography variant="body2" sx={{ mb: 0.5 }}>
        {msg}
      </Typography>
      <Typography variant="caption" color="text.secondary" component="p">
        错误码：<code>{code}</code>
        {requestId ? (
          <>
            {' · '}请求 ID：<code>{requestId}</code>
          </>
        ) : null}
      </Typography>
    </Alert>
  )
}

/** 局部失败的内联提示（成功部分照常渲染）。 */
export function InlineError({ error }: { error: unknown }) {
  if (!error) return null
  const msg = error instanceof Error ? error.message : String(error)
  const code = error instanceof ApiError ? error.code : 'unknown_error'
  return (
    <Typography variant="body2" color="error" role="alert" sx={{ display: 'flex', gap: 0.5 }}>
      {msg}
      <Box component="span" sx={{ color: 'text.secondary' }}>
        （{code}）
      </Box>
    </Typography>
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
  hint,
  id
}: {
  title: string
  actions?: ReactNode
  children: ReactNode
  hint?: ReactNode
  /** DOM 锚点：跨页快捷入口用 #hash 直达某个分区（如组详情的成员区）。 */
  id?: string
}) {
  return (
    // 卡片的分区头不再用 CardHeader（它自带一套 20px 上下内距，与正文
    // 自己的间距叠在一起显得头重脚轻）。改成显式 padding 后，标题到正文的
    // 间距全站一致，且 hint 位置可以精确到 4px 网格。
    <Card sx={{ mb: 2 }} id={id}>
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 1.5,
          flexWrap: 'wrap',
          px: 2,
          pt: 1.75,
          pb: hint ? 0.5 : 1.25
        }}
      >
        <Typography variant="h2">{title}</Typography>
        {actions ? <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>{actions}</Box> : null}
      </Box>
      {hint ? (
        <Typography variant="body2" color="text.secondary" sx={{ px: 2, pb: 1 }}>
          {hint}
        </Typography>
      ) : null}
      <CardContent sx={{ pt: hint ? 1 : 0.5, px: 2, '&:last-child': { pb: 2 } }}>{children}</CardContent>
    </Card>
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
    // className="field" 是页面级布局契约：.field-row > .field 靠它做
    // 「一行多列、列宽 12rem 起」的排布。MUI 化时漏掉这个类会让所有表单
    // 退化成单列全宽——这是肉眼最难发现、但影响最大的一类回归。
    <Stack spacing={0.5} className="field" sx={{ width: '100%' }}>
      <Typography
        component="label"
        variant="body2"
        sx={{ fontWeight: 600, color: error ? 'error.main' : 'text.primary', display: 'inline-flex', gap: 0.25 }}
      >
        {label}
        {required ? (
          <Box component="span" aria-hidden="true" sx={{ color: 'error.main' }}>
            *
          </Box>
        ) : null}
      </Typography>
      {children}
      {error ? (
        <Typography variant="caption" color="error" sx={{ mt: 0.25 }}>
          {error}
        </Typography>
      ) : hint ? (
        <Typography variant="caption" color="text.secondary" sx={{ mt: 0.25 }}>
          {hint}
        </Typography>
      ) : null}
    </Stack>
  )
}

/** 表格容器：窄屏横向滚动（原 CSS 的「转卡片」在 MUI 下改为滚动容器）。 */
export function DataTable({ headers, children, empty }: { headers: ReactNode[]; children: ReactNode; empty?: boolean }) {
  const theme = useTheme()
  return (
    /* 不再画外框：表格基本都长在 Section 的 Card 里，两层边框会让每个列表
       都像"卡片套卡片"。表头字重 + 行分隔线已经足够界定边界。
       窄屏首列吸附：横滑时不至丢掉"我在看哪一行"。 */
    <TableContainer sx={{ mx: -2, px: 2 }}>
      <Table
        size="small"
        sx={{
          minWidth: 640,
          // 首列吸附只在窄屏生效：桌面宽度下横向滚动很少发生，而 sticky 列
          // 必须自带不透明底色（否则内容会从它下面透出来），那与「表格长在
          // 卡片里、行底色跟着卡片走」冲突。
          [theme.breakpoints.down('md')]: {
            '& td:first-of-type, & th:first-of-type': {
              position: 'sticky',
              left: 0,
              zIndex: 1,
              backgroundColor: 'background.paper'
            },
            '& th:first-of-type': { zIndex: 2 }
          }
        }}
      >
        <TableHead>
          <TableRow>
            {headers.map((h, i) => (
              <TableCell key={i}>{h}</TableCell>
            ))}
          </TableRow>
        </TableHead>
        <TableBody>
          {empty ? (
            <TableRow>
              <TableCell colSpan={headers.length}>
                <Typography variant="body2" color="text.secondary" sx={{ py: 4, textAlign: 'center' }}>
                  暂无数据
                </Typography>
              </TableCell>
            </TableRow>
          ) : (
            children
          )}
        </TableBody>
      </Table>
    </TableContainer>
  )
}

/** 键值详情列表。 */
export function KeyValues({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <Box component="dl" sx={{ m: 0 }}>
      {items.map((it, i) => (
        <Box key={it.label}>
          {i > 0 ? <Divider sx={{ my: 0.75 }} /> : null}
          <Box
            sx={{
              display: 'grid',
              gridTemplateColumns: { xs: '1fr', sm: '180px 1fr' },
              gap: { xs: 0.25, sm: 2 },
              alignItems: 'baseline',
              py: 0.25
            }}
          >
            <Typography component="dt" variant="body2" color="text.secondary" sx={{ fontWeight: 500 }}>
              {it.label}
            </Typography>
            <Typography component="dd" variant="body2" sx={{ m: 0, wordBreak: 'break-word' }}>
              {it.value}
            </Typography>
          </Box>
        </Box>
      ))}
    </Box>
  )
}

/** 折叠的机器可读详情（错误码/证据/审计 detail）。 */
export function JsonDetails({ summary = '查看原始数据', value }: { summary?: string; value: unknown }) {
  return (
    <Accordion
      // 折叠面板自带上下边框，与所在 Card 的边框叠在一起会显脏——只留分隔线。
      sx={{ border: 0, '&:before': { display: 'none' }, bgcolor: 'transparent' }}
    >
      <AccordionSummary expandIcon={<IconChevronDown size="md" />} sx={{ px: 0, minHeight: 40 }}>
        <Typography variant="body2" color="primary" sx={{ fontWeight: 600 }}>
          {summary}
        </Typography>
      </AccordionSummary>
      <AccordionDetails sx={{ px: 0, pt: 0 }}>
        <Box
          component="pre"
          sx={{
            m: 0,
            p: 1.5,
            borderRadius: 1.5,
            bgcolor: (t) => (t.palette.mode === 'dark' ? 'rgba(255,255,255,.04)' : 'rgba(15,23,42,.035)'),
            fontSize: '0.78rem',
            lineHeight: 1.6,
            overflow: 'auto',
            maxHeight: 420
          }}
        >
          {prettyJson(value)}
        </Box>
      </AccordionDetails>
    </Accordion>
  )
}

/** 表单级错误（提交失败保留已填内容，只加这行提示）。 */
export function FormError({ error }: { error: unknown }) {
  if (!error) return null
  const msg = error instanceof Error ? error.message : String(error)
  const code = error instanceof ApiError ? error.code : 'unknown_error'
  return (
    <Typography variant="body2" color="error" role="alert" sx={{ display: 'flex', gap: 0.5 }}>
      {msg}
      <Box component="span" sx={{ color: 'text.secondary' }}>
        （{code}）
      </Box>
    </Typography>
  )
}
