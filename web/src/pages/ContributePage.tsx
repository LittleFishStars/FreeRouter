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

import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { QRCodeSVG } from 'qrcode.react'
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Box,
  Checkbox,
  FormControlLabel,
  List,
  ListItemButton,
  Typography
} from '@mui/material'
import {
  cancelOAuthContribution,
  getContributeProviders,
  pollOAuthContribution,
  startOAuthContribution,
  submitContribution,
  postContributeRecovery,
  type ContributeResult,
  type OAuthAuthorize
} from '../api/endpoints'
import type {
  ContributionCategoryId,
  ContributionProviderCatalog,
  ContributionProviderCategory
} from '../api/types'
import { useApi } from '../hooks/useApi'
import { ACTION_ICON } from '../components/IconButtons'
import { IconBack } from '../icons'
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
import { PLAIN_FIELD_PROPS, SECRET_FIELD_PROPS } from '../utils/formFields'
import { IconChevronDown } from '../icons'

/** 自定义供应商在右栏列表里的哨兵 id。
 *
 * 为什么用哨兵而不是单独一组 state：右栏的交互是统一的「从列表选一项 →
 * 下方给表单」，自定义端点只是列表里的一项（「供应商清单」从内置目录换成
 * 用户自己的地址栏）。目录 id 都是厂商 slug（deepseek-com / opencode-ai …），
 * 不会与这个值撞车。
 */
const CUSTOM_PROVIDER_ID = '__custom__'

/**
 * 按 id 取分类。categories 是判别联合：用类型守卫收窄后调用方拿到的 items
 * 才有正确类型——否则把 API 目录项当反代项读 available，编译器看不出来。
 */
function findCategory<K extends ContributionCategoryId>(
  catalog: ContributionProviderCatalog,
  id: K
): Extract<ContributionProviderCategory, { id: K }> | undefined {
  return catalog.categories.find(
    (c): c is Extract<ContributionProviderCategory, { id: K }> => c.id === id
  )
}

/** flow → 「用户在供应商侧要做什么」的措辞。未知 flow 原样显示，不猜。 */
const FLOW_LABELS: Record<string, string> = {
  browser: '网页授权',
  browser_callback: '网页授权',
  device_code: '设备码',
  qr_scan: '扫码',
  sms: '短信验证码'
}

function flowLabel(flow?: string): string {
  if (!flow) return '授权'
  return FLOW_LABELS[flow] ?? flow
}

/** 声称模型：文本拆分（自定义表单用，便于一次粘贴多个；后端全量接收）。 */
function splitModels(v: string): string[] {
  return v
    .split(/[,，\s]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

/** 无偿阻断式确认：三个入口共用一份，未勾选时授权链接与提交按钮都不可用。 */
function FreeConfirm({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <FormControlLabel
      className="checkbox-line free-confirm"
      sx={{ m: 0 }}
      control={<Checkbox size="small" checked={checked} onChange={(e) => onChange(e.target.checked)} />}
      label="我明白：贡献是无偿的，不提供额度、优先使用权或升级凭据作为回报"
    />
  )
}

/**
 * 贡献入口（公共区）：左栏反代供应商（供应商账号登录，凭据由 sidecar 换出），
 * 右栏 API 供应商（内置目录 + 粘 Key），自定义端点是右栏列表的置顶项。
 *
 * 为什么是左右双栏而不是 Tab 切换：两类入口是**并列的选择**而不是同一件事
 * 的几种形态——用户进页第一眼该同时看到「用账号登录一家」和「贴一把 Key」
 * 两条路；Tab 会把其中一条藏进一次点击之后（2026-10-25 用户定案）。
 *
 * 自定义供应商为什么并入右栏：它的交互与 API 供应商同构（一个端点 + 一把
 * Key），只是端点来源从内置目录换成用户手填——放在同一个列表里，搜索落空
 * 的用户会自然落到它，而不是面对一个死胡同。
 *
 * 两栏共用同一份无偿确认与同一张结果卡：确认是「贡献即无条件」的阻断点，
 * 放两份就会出现「这边勾了那边没勾」的裂缝；结果卡的恢复码更是只显示一次，
 * 分开展示等于给用户两条不同的回站路径。
 */
export default function ContributePage({ def }: PageProps) {
  const [freeConfirmed, setFreeConfirmed] = useState(false)
  const [result, setResult] = useState<ContributeResult | null>(null)
  const providers = useApi(() => getContributeProviders(), [])

  // 右栏选中项：API 目录里的 id，或自定义项的哨兵 id。
  const [selectedId, setSelectedId] = useState('')
  const isCustom = selectedId === CUSTOM_PROVIDER_ID

  // 自定义供应商（任意 OpenAI 兼容端点；右栏置顶项的表单状态）
  const [endpoint, setEndpoint] = useState('')
  const [customKey, setCustomKey] = useState('')
  const [customLabel, setCustomLabel] = useState('')
  const [customModels, setCustomModels] = useState('')
  const [customSubmitting, setCustomSubmitting] = useState(false)
  const [customError, setCustomError] = useState<unknown>(null)

  // API 供应商（内置目录 55 家；端点由后端解析，前端只给 provider id + Key）
  const [apiSearch, setApiSearch] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [apiLabel, setApiLabel] = useState('')
  const [apiSubmitting, setApiSubmitting] = useState(false)
  const [apiError, setApiError] = useState<unknown>(null)

  async function onSubmitCustom(e: FormEvent) {
    e.preventDefault()
    // 按钮虽然已置灰，但表单回车仍可能触发 onSubmit：阻断确认在这里再拦一次
    if (!freeConfirmed) {
      setCustomError(new Error('请先勾选上方的无偿确认'))
      return
    }
    setCustomError(null)
    setCustomSubmitting(true)
    try {
      const r = await submitContribution({
        kind: 'custom',
        endpoint,
        api_key: customKey,
        label: customLabel,
        claimed_models: splitModels(customModels)
      })
      setResult(r.data)
      setCustomKey('') // 提交后立即从内存与输入框清掉
    } catch (err) {
      setCustomError(err)
    } finally {
      setCustomSubmitting(false)
    }
  }

  async function onSubmitApi(e: FormEvent) {
    e.preventDefault()
    // 同上：disabled 只是提示，真正的阻断点必须在这里
    if (!freeConfirmed) {
      setApiError(new Error('请先勾选上方的无偿确认'))
      return
    }
    if (!selectedId || isCustom) {
      setApiError(new Error('请先在上方列表选择一家 API 供应商'))
      return
    }
    setApiError(null)
    setApiSubmitting(true)
    try {
      // 不带 endpoint：契约 §3 规定 kind=api 的端点由后端从内置目录解析。
      // 也不带 claimed_models（2026-10-25 定案）：API 供应商这一栏只收
      // 「供应商 + Key」。凭据的可用模型由服务端探测 /models 得出，不让用户抄一遍
      // ——用户抄的那个名字和上游真实 id 常常不一致，反而制造假阴性。
      // 未声明的凭据按 L0 放行（可达且鉴权通过 → unverified）并进复检队列，
      // 由探测结果确定实际模型集。
      const r = await submitContribution({
        kind: 'api',
        provider: selectedId,
        api_key: apiKey,
        label: apiLabel
      })
      setResult(r.data)
      setApiKey('') // 同上：Key 不留在内存里
    } catch (err) {
      setApiError(err)
    } finally {
      setApiSubmitting(false)
    }
  }

  // 结果卡优先：有恢复码走一次性展示（未登录贡献唯一能自助回站的凭据）。
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

  // 已登录贡献没有恢复码：也必须给结果，否则提交后界面什么都不发生。
  if (result) {
    return (
      <PageFrame def={def}>
        <Banner tone={result.accepted ? 'ok' : 'info'} title={result.accepted ? '已受理' : '已记录，等待鉴定'}>
          {result.message ?? '凭据已提交。'}
          {result.account_created && ' 已为你的贡献创建一个仅含「贡献者」能力的账户。'}
        </Banner>
        {result.discovered_models && result.discovered_models.length > 0 && (
          <Banner tone="info" title="自动获取到的模型">
            <p className="muted" style={{ margin: 0 }}>
              这些是探测该端点 <code>/models</code> 得到的条目，凭据仍按「未鉴定」进入复检队列，
              后续由服务端核对实际可用集——无需你再补声明。
            </p>
            <p style={{ margin: '8px 0 0' }}>
              <code>{result.discovered_models.join('、')}</code>
            </p>
          </Banner>
        )}
        <Section title="下一步">
          <ul className="plain-list">
            <li>已登录：去「我的贡献」查看鉴定进度。</li>
            <li>未登录且是反代型：用任意一个你贡献过的账号登录即可回来。</li>
          </ul>
          <p className="inline-links">
            <Link to="/contributions">我的贡献</Link>
          </p>
        </Section>
        <p>
          <button type="button" className="btn-primary" onClick={() => setResult(null)}>
            继续贡献
          </button>
        </p>
      </PageFrame>
    )
  }

  return (
    <PageFrame def={def}>
      <Banner tone="muted" title="贡献即生效，无需审核">
        {FREE_NOTICE}
      </Banner>

      {/* 两栏共用一份确认：左栏点的是授权链接、右栏点的是提交按钮，
          阻断点不同但语义相同——只放一份，避免「这边勾了那边没勾」。 */}
      {/* 无偿确认条：它是一条「阻断点」，所以允许有强调——未勾选时用警告容器色，
          勾上后回落成中性填充。整站的边框都改成了填充，这里也一致。 */}
      <Box
        sx={{
          mb: 2,
          p: 1.5,
          borderRadius: 3,
          bgcolor: freeConfirmed ? 'var(--fr-success-container)' : 'var(--fr-warning-container)',
          color: freeConfirmed ? 'var(--fr-on-success-container)' : 'var(--fr-on-warning-container)',
          transition: 'background-color .15s ease'
        }}
      >
        <FreeConfirm checked={freeConfirmed} onChange={setFreeConfirmed} />
        <Typography variant="caption" color="text.secondary" component="p" sx={{ m: 0, mt: 0.25 }}>
          未勾选时：左栏的授权链接不可点，右栏的提交按钮不可用。
        </Typography>
      </Box>

      {/* 左右双栏（lg 起并排；窄屏纵向堆叠，反代在上）：两类入口是并列的选择，
          同屏可见而不是藏在 Tab 之后。 */}
      <Box
        sx={{
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', lg: 'minmax(0, 1fr) minmax(0, 1.15fr)' },
          gap: 2,
          alignItems: 'start'
        }}
      >
        {/* ── 左栏：反代供应商 ── */}
        <Section
          title="反代供应商"
          hint="用供应商账号登录，由 sidecar 换出凭据（不接触你的 Key）。选一家 → 跳转授权 / 扫码 / 填码 → 完成后凭据入池。"
        >
          <AsyncView state={providers} loadingRows={3}>
            {(d) => {
              const items = findCategory(d, 'reverse_proxy')?.items ?? []
              const available = items.filter((p) => p.available)
              const unavailable = items.filter((p) => !p.available)

              if (items.length === 0) {
                return (
                  <EmptyState
                    title="当前没有接入反代供应商"
                    hint="供应商授权服务尚未配置。你仍然可以在右侧贡献 API Key。"
                  />
                )
              }

              return (
                <>
                  {available.length === 0 ? (
                    <p className="muted">当前没有可用的反代供应商，原因见下方「暂不可用」。</p>
                  ) : (
                    <ul className="provider-grid">
                      {available.map((p) => (
                        <li key={p.id}>
                          <span className="provider-name">
                            <Link
                              to={`/contribute/oauth/${encodeURIComponent(p.id)}${
                                p.needs?.includes('phone') ? '?needs=phone' : ''
                              }`}
                              className="provider-link"
                              title={freeConfirmed ? `授权 ${p.name}` : '请先勾选上方的无偿确认'}
                              aria-disabled={!freeConfirmed}
                              onClick={(e) => {
                                if (!freeConfirmed) e.preventDefault()
                              }}
                            >
                              {p.name}
                            </Link>
                            {/* flow 决定去哪儿完成授权；这里只标注形态，交互仍由
                                ContributeOAuthPage 按 flow 处理（不在入口页分叉）。 */}
                            <span className="provider-tag">{flowLabel(p.flow)}</span>
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}

                  {unavailable.length > 0 && (
                    <Accordion
                      // 折叠区不再画外框：它长在 Section 的 Card 里，两层边框像套娃
                      disableGutters
                      sx={{ mt: 1, border: 0, '&:before': { display: 'none' }, bgcolor: 'transparent' }}
                    >
                      <AccordionSummary expandIcon={<IconChevronDown size="md" />} sx={{ px: 0, minHeight: 40 }}>
                        <Typography variant="body2" sx={{ fontWeight: 600 }}>
                          暂不可用（{unavailable.length}）
                        </Typography>
                      </AccordionSummary>
                      <AccordionDetails sx={{ px: 0, pt: 0 }}>
                        <Typography variant="caption" color="text.secondary" component="p" sx={{ mb: 1 }}>
                          「配置缺失」是我们这边还没配置好，可以反馈；「上游不支持」是供应商没有可脱离
                          IDE 的 OAuth 登录面，补配置也不会变。
                        </Typography>
                        <ul className="provider-grid">
                          {unavailable.map((p) => (
                            <li
                              key={p.id}
                              className="provider-unavailable"
                              // 两类不可用对用户的含义完全不同：用 data 属性让 CSS 分色。
                              data-unavailable={p.unavailable_kind ?? 'config'}
                            >
                              <span className="provider-name">
                                {p.name}
                                <span className="provider-tag">
                                  {p.unavailable_kind === 'unsupported' ? '上游不支持' : '配置缺失'}
                                </span>
                              </span>
                              {p.unavailable_reason && (
                                <span className="provider-reason">{p.unavailable_reason}</span>
                              )}
                              {p.steps && p.steps.length > 0 && (
                                <ol className="provider-steps">
                                  {p.steps.map((s, i) => (
                                    <li key={i}>{s}</li>
                                  ))}
                                </ol>
                              )}
                              {p.suggested_endpoint && (
                                <button
                                  type="button"
                                  className="btn-ghost provider-fill"
                                  onClick={() => {
                                    // 端点填进右栏「自定义供应商」项的表单并选中它：
                                    // 停在左栏只会让人找不到值填去了哪里。
                                    setEndpoint(p.suggested_endpoint!)
                                    setSelectedId(CUSTOM_PROVIDER_ID)
                                  }}
                                >
                                  填入右侧自定义供应商
                                </button>
                              )}
                            </li>
                          ))}
                        </ul>
                      </AccordionDetails>
                    </Accordion>
                  )}
                </>
              )
            }}
          </AsyncView>
        </Section>

        {/* ── 右栏：API 供应商（目录来自 Tingly Box）＋ 自定义端点 ── */}
        <Section
          title="API 供应商"
          hint="内置端点（目录来自 Tingly Box），只需粘贴该家的 Key，端点由服务端解析；目录里没有的选「自定义供应商」手填端点。"
        >
          <AsyncView state={providers} loadingRows={4}>
            {(d) => {
              const items = findCategory(d, 'api')?.items ?? []
              const keyword = apiSearch.trim().toLowerCase()
              // 只按事实字段过滤（名称 / 端点 / id）；模型名不参与——同名模型跨家很常见
              const filtered = keyword
                ? items.filter(
                    (p) =>
                      p.name.toLowerCase().includes(keyword) ||
                      p.base_url.toLowerCase().includes(keyword) ||
                      p.id.toLowerCase().includes(keyword)
                  )
                : items
              const selected = isCustom ? undefined : items.find((p) => p.id === selectedId)

              return (
                <Box>
                  <Field label="搜索供应商" hint={`按名称或端点过滤，共 ${items.length} 家`}>
                    <input
                      value={apiSearch}
                      onChange={(e) => setApiSearch(e.target.value)}
                      placeholder="例如 deepseek 或 https://"
                    />
                  </Field>
                  {/* 列表容器用「比卡片深一档的填充」圈定范围，而不是描边：
                      整站在用填充画层级，一根 1px 线在这里会显得是新来的。 */}
                  <List
                    dense
                    sx={{
                      mt: 1,
                      maxHeight: 360,
                      overflowY: 'auto',
                      bgcolor: 'var(--fr-block)',
                      borderRadius: 2.5,
                      py: 0.5,
                      '& .MuiListItemButton-root': { borderRadius: 2, mx: 0.5 }
                    }}
                  >
                    {/* 自定义项固定置顶、不参与过滤：搜索落空时它是兜底路径，
                        被过滤掉用户就进了死胡同。 */}
                    <ListItemButton
                      selected={isCustom}
                      onClick={() => setSelectedId(CUSTOM_PROVIDER_ID)}
                      sx={{ flexDirection: 'column', alignItems: 'flex-start', gap: 0.25 }}
                    >
                      <Typography variant="body2" sx={{ fontWeight: 600 }}>
                        自定义供应商
                      </Typography>
                      <Typography variant="caption" color="text.secondary" component="p">
                        任意 OpenAI 兼容端点，手填 base URL
                      </Typography>
                    </ListItemButton>
                    {filtered.map((p) => (
                      <ListItemButton
                        key={p.id}
                        selected={p.id === selectedId}
                        onClick={() => setSelectedId(p.id)}
                        sx={{
                          flexDirection: 'column',
                          alignItems: 'flex-start',
                          gap: 0.25,
                          position: 'relative',
                          '&.Mui-selected': {
                            bgcolor: 'var(--fr-inset)',
                            '&::before': {
                              content: '""',
                              position: 'absolute',
                              left: 0,
                              top: '50%',
                              transform: 'translateY(-50%)',
                              width: 3,
                              height: 16,
                              borderRadius: 2,
                              bgcolor: 'primary.main'
                            }
                          }
                        }}
                      >
                        <Typography variant="body2" sx={{ fontWeight: 600 }}>
                          {p.name}
                        </Typography>
                        <Typography
                          variant="caption"
                          color="text.secondary"
                          component="p"
                          sx={{ wordBreak: 'break-all' }}
                        >
                          {p.base_url}
                        </Typography>
                      </ListItemButton>
                    ))}
                  </List>
                  {filtered.length === 0 && (
                    <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
                      没有匹配的供应商：换个关键词，或选「自定义供应商」手填端点。
                    </Typography>
                  )}

                  <Box sx={{ mt: 2 }}>
                    {isCustom ? (
                      <form className="form-card" name="contribute-custom" autoComplete="off" onSubmit={onSubmitCustom}>
                        <Typography variant="body2" sx={{ fontWeight: 600, mb: 1 }}>
                          自定义供应商
                        </Typography>
                        <Field
                          label="端点地址"
                          required
                          hint="该 Key 指向的 OpenAI 兼容 base URL，例如 https://api.example.com/v1"
                        >
                          <input
                            value={endpoint}
                            onChange={(e) => setEndpoint(e.target.value)}
                            required
                            placeholder="https://…"
                            {...PLAIN_FIELD_PROPS}
                          />
                        </Field>
                        <Field label="API Key" required hint="提交后不再显示，也不会出现在任何页面。">
                          <input
                            type="password"
                            value={customKey}
                            onChange={(e) => setCustomKey(e.target.value)}
                            required
                            {...SECRET_FIELD_PROPS}
                          />
                        </Field>
                        <Field
                          label="标签"
                          required
                          hint="你自己认得出的名字，例如「备用号」；凭据列表里只能靠它识别。"
                        >
                          <input
                            value={customLabel}
                            onChange={(e) => setCustomLabel(e.target.value)}
                            required
                            {...PLAIN_FIELD_PROPS}
                          />
                        </Field>
                        <Field
                          label="声称的模型"
                          hint="留空则自动获取：提交时我们探测该端点的模型目录，取不到会退回让你填写。手动声明可多个（逗号/空格分隔），鉴定会逐个核对。"
                        >
                          <input
                            value={customModels}
                            onChange={(e) => setCustomModels(e.target.value)}
                            placeholder="留空自动获取，或例如 deepseek-chat"
                          />
                        </Field>
                        <FormError error={customError} />
                        <button type="submit" className="btn-primary" disabled={!freeConfirmed || customSubmitting}>
                          {customSubmitting ? '正在提交…' : '提交贡献'}
                        </button>
                      </form>
                    ) : selected ? (
                      <form className="form-card" name="contribute-api" autoComplete="off" onSubmit={onSubmitApi}>
                        <div>
                          <Typography variant="body2" sx={{ fontWeight: 600 }}>
                            {selected.name}
                          </Typography>
                          <Typography
                            variant="body2"
                            color="text.secondary"
                            component="p"
                            sx={{ wordBreak: 'break-all', m: 0 }}
                          >
                            端点：<code>{selected.base_url}</code>
                          </Typography>
                          <Typography variant="caption" color="text.secondary" component="p" sx={{ mt: 0.5 }}>
                            协议 {selected.protocol || 'openai'} · 区域 {selected.region || '—'}
                            {selected.website && (
                              <>
                                {' · '}
                                <a href={selected.website} target="_blank" rel="noreferrer">
                                  官网
                                </a>
                              </>
                            )}
                            {selected.api_doc && (
                              <>
                                {' · '}
                                <a href={selected.api_doc} target="_blank" rel="noreferrer">
                                  API 文档
                                </a>
                              </>
                            )}
                          </Typography>
                        </div>
                        {/* 这一栏只收「供应商 + Key」（2026-10-25 用户定案）。
                            不要求填写模型：凭据的可用模型由服务端探测 /models 得出。
                            让用户手抄模型名反而常与上游真实 id 不一致，制造假阴性。
                            未声明模型的凭据按 L0 放行（可达 + 鉴权通过 → unverified）
                            并进复检队列，由探测结果确定实际模型集。 */}

                        <Field label="API Key" required hint="提交后不再显示，也不会出现在任何页面。">
                          <input
                            type="password"
                            value={apiKey}
                            onChange={(e) => setApiKey(e.target.value)}
                            required
                            {...SECRET_FIELD_PROPS}
                          />
                        </Field>
                        <Field label="标签" hint="可选；未登录建号时用作显示名，凭据列表里也只能靠它识别。">
                          <input value={apiLabel} onChange={(e) => setApiLabel(e.target.value)} {...PLAIN_FIELD_PROPS} />
                        </Field>

                        <FormError error={apiError} />
                        <button type="submit" className="btn-primary" disabled={!freeConfirmed || apiSubmitting}>
                          {apiSubmitting ? '正在提交…' : `提交到 ${selected.name}`}
                        </button>
                      </form>
                    ) : (
                      <EmptyState
                        title="先选择一家供应商"
                        hint="从上方列表选一家：目录里的家端点自动带出；没有的选「自定义供应商」手填端点。"
                      />
                    )}
                  </Box>
                </Box>
              )
            }}
          </AsyncView>
        </Section>
      </Box>

      <Section title="未登录贡献后怎么回来">
        <ul className="plain-list">
          <li>供应商 OAuth 型：用任意一个你贡献过的账号登录即可回来。</li>
          <li>API 供应商 / 自定义型：提交成功后会展示一次恢复码；请立刻保存，它不会再次显示。</li>
          <li>已登录贡献：直接出现在「我的贡献」里，可随时暂停或撤销。</li>
        </ul>
        <p>
          <Link to="/contribute/recovery">已有恢复码？去兑换</Link>
        </p>
      </Section>
    </PageFrame>
  )
}

// ── 供应商 OAuth（两种形态）─────────────────────────────────────
//
// redirect：拿到 authorize_url 就整页跳过去，供应商回调到 /oauth/callback，
// 302 回 /contribute/callback 展示结果。
// session：设备码/二维码/短信。sidecar 已在后台轮询，本页按 interval 轮询
// /poll 展示进度；短信形态多一步：用户回填验证码后一并交给 /poll。

export function ContributeOAuthPage({ def }: PageProps) {
  const { provider = '' } = useParams<{ provider: string }>()
  const [params] = useSearchParams()
  const [error, setError] = useState<unknown>(null)
  const [starting, setStarting] = useState(true)
  const [session, setSession] = useState<OAuthAuthorize | null>(null)
  // 轮询进展文案：sidecar 侧 runner 通过 report 上报（已扫码/失败原因），
  // pending 应答的 message 每帧都可能是新的——不能只看 start 时的 session.message。
  const [pollMsg, setPollMsg] = useState('')
  const [smsCode, setSmsCode] = useState('')
  // 确认标记：验证码**输完点确认**后 poll 才携 code。为什么必须显式确认：
  // poll 每帧都发，若 code 一有值就随帧上行，用户输入到一半（4 位码输到 2 位）
  // 就会被 submit 一次——服务端判 invalid，正确的码还没输完流程已终结
  //（2026-10-28 用户实测「验证码还没输完呢」）。ref 承载判定（不触发渲染，
  // 规避轮询 effect 的重跑陷阱），state 只驱动按钮禁用态。
  const [smsConfirmed, setSmsConfirmed] = useState(false)
  const smsConfirmedRef = useRef(false)
  const [phone, setPhone] = useState('')
  const [result, setResult] = useState<{ ok: boolean; message: string; recoveryCode?: string } | null>(null)
  const navigate = useNavigate()

  // ── 阿里云滑块（Raccoon 短信登录的 captcha_param 来源）──
  // 官方脚本按需注入：只有走滑块流程的 provider 才加载（CSP 只对
  // /contribute/ 与 /api/contributions/oauth/ 路径放行 alicdn/aliyuncs）。
  // SceneId/prefix 是商汤客户端的逆向常量（hub raccoon-product.js 同值）。
  const captchaReadyRef = useRef(false)
  function launchRaccoonCaptcha(phone: string) {
    const el = document.getElementById('captcha-element')
    if (!el) { setError(new Error('滑块容器缺失，请刷新页面')); return }
    const begin = (captchaParam: string) => {
      captchaReadyRef.current = true
      start(phone, captchaParam)
    }
    const w = window as unknown as {
      initAliyunCaptcha?: (o: Record<string, unknown>) => void
    }
    if (typeof w.initAliyunCaptcha === 'function') {
      w.initAliyunCaptcha({
        SceneId: '1pkmy0x3', prefix: 'hk1r5l', mode: 'popup',
        element: '#captcha-element', button: '#send-code-btn',
        captchaVerifyCallback: (cp: string) => { begin(cp); return { captchaResult: true, bizResult: true } },
        getInstance: () => null, slideStyle: { width: 320, height: 40 }, language: 'cn',
      })
      return
    }
    const sc = document.createElement('script')
    sc.src = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js'
    sc.onload = () => {
      if (typeof w.initAliyunCaptcha === 'function') {
        w.initAliyunCaptcha({
          SceneId: '1pkmy0x3', prefix: 'hk1r5l', mode: 'popup',
          element: '#captcha-element', button: '#send-code-btn',
          captchaVerifyCallback: (cp: string) => { begin(cp); return { captchaResult: true, bizResult: true } },
          getInstance: () => null, slideStyle: { width: 320, height: 40 }, language: 'cn',
        })
      } else { setError(new Error('滑块脚本加载失败，请检查网络后重试')) }
    }
    sc.onerror = () => setError(new Error('滑块脚本加载失败，请检查网络后重试'))
    document.head.appendChild(sc)
  }

  // 发起（短信形态由页内表单再调一次，故抽成函数）。
  function start(phone?: string, captchaParam?: string) {
    setStarting(true)
    setError(null)
    startOAuthContribution(provider, { phone, captchaParam })
      .then((r) => {
        const d = r.data
        // **先判断"要不要用户去供应商那边操作"，再判断"我们怎么收结果"。**
        //
        // 这两个维度是独立的，早先这里写成"mode 是 session 就不跳"，结果
        // zcode / officeace（flow=browser，mode=session）和 minimax / cline /
        // qoder（flow=device_code，mode=session）全都卡在"等待授权…"——
        // 它们恰恰是必须让用户去登录页/填码的那批。用户看到的就是一个
        // 什么都不发生的中转页。
        //
        // 正确判据是 flow（用户在供应商侧要做什么）：
        //   browser / device_code → 需要离开本页去完成
        //   qr  → 本页扫码，不离开
        //   sms → 本页填码，不离开
        const needsUserAction = !!d.authorize_url && d.flow !== 'qr' && d.flow !== 'sms'

        if (d.mode === 'session' && d.session_id) {
          // session 形态的结果由 sidecar 后台轮询取得，session_id 必须留在本页。
          // 所以用新标签打开授权页、本页继续轮询；直接 location.assign 会把
          // session 一起带走，回来后反而接不上。
          setSession(d)
          setStarting(false)
          if (needsUserAction) {
            // effect 里开新窗大概率被浏览器拦（非用户手势），拦了也没关系：
            // 页面上有醒目的「打开授权页」按钮兜底，点它一定能开。
            window.open(d.authorize_url, '_blank', 'noopener,noreferrer')
          }
          return
        }
        if (d.authorize_url) {
          window.location.assign(d.authorize_url) // redirect：本页就此离开，回调回本站
          return
        }
        setError(new Error('服务端没有返回授权地址'))
        setStarting(false)
      })
      .catch((e) => {
        setError(e)
        setStarting(false)
      })
  }

  useEffect(() => {
    // 短信形态不自动发起（需要手机号）；其余进页即发起。
    if (params.get('needs') === 'phone') {
      setStarting(false)
      return
    }
    start()
    return () => {
      // 离开页面时释放 sidecar 的后台轮询（幂等；redirect 路径无会话可取消）。
      if (session?.session_id) void cancelOAuthContribution(provider, session.session_id)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [provider])

  // session 形态：轮询进度。
  // 在飞标记为什么用 ref 而不是 state：之前用 useState(true/false) 时，
  // setPolling(true) 会把 polling 塞进依赖数组触发 effect 重跑——旧 effect 的
  // alive 翻 false，在飞应答被 `if (!alive) return` 丢弃，而 finally 里的
  // setPolling(false) 又被同一个 alive 挡住不再执行，polling 永久卡在 true，
  // 后续 tick 全部 no-op——轮询整体死锁。实测表现就是「手机上已确认扫码，
  // 页面永远等在『等待扫码…』」（2026-10-27 用户报告 Loomy 扫码无反应的根因）。
  // ref 不参与渲染，翻转它不会触发重跑；alive 语义保持「effect 还挂着」，
  // 组件卸载前应答总能被处理。
  const pollingRef = useRef(false)
  // 终态标记：到 ok/failed/expired 就停轮询。为什么要单独一个 ref：服务端
  // 对已完成 session 的每次 poll 都返回同一凭据，终端态后继续轮询会让
  // Go 侧重复 settle（服务端已幂等，但客户端仍不该无意义地打）。
  const doneRef = useRef(false)
  useEffect(() => {
    if (!session?.session_id) return
    let alive = true
    const tick = async () => {
      if (pollingRef.current || doneRef.current) return
      pollingRef.current = true
      try {
        const r = await pollOAuthContribution(provider, {
          session_id: session.session_id!,
          ...(smsConfirmedRef.current && smsCode && session.flow === 'sms' ? { code: smsCode } : {})
        })
        if (!alive) return
        const d = r.data
        if (d.status === 'ok' && d.outcome) {
          doneRef.current = true
          setResult({
            ok: true,
            message: d.outcome.message,
            recoveryCode: d.outcome.recovery_code
          })
          return
        }
        if (d.status === 'failed' || d.status === 'expired') {
          doneRef.current = true
          setResult({ ok: false, message: d.message ?? '授权未完成' })
          return
        }
        if (typeof d.message === 'string' && d.message) setPollMsg(d.message)
      } catch (e) {
        if (alive) setError(e)
      } finally {
        // 必须无条件复位：alive 翻 false 只说明组件卸载，ref 复位与否已无所谓；
        // 但只要 effect 还挂着（alive=true），下一次 tick 必须能进。
        pollingRef.current = false
      }
    }
    // interval 为 0 是合法形状（sidecar 未下发时 Go 侧零值），用 || 而不是 ?? 才能落回 3 秒默认。
    const timer = window.setInterval(tick, Math.max(2, session.interval || 3) * 1000)
    void tick()
    return () => {
      alive = false
      window.clearInterval(timer)
    }
  }, [session, provider, smsCode])

  // 完成：有恢复码走一次性展示（回跳贡献页），否则直接回贡献页带 query。
  if (result?.ok && result.recoveryCode) {
    return (
      <OneTimeSecret
        title="贡献成功：请保存恢复码"
        secret={result.recoveryCode}
        description="这个账户是用本次授权自动创建的，恢复码是唯一能自助回到它的凭据，只显示一次。"
        filename="freerouter-recovery-code.txt"
        onDone={() => navigate('/contribute')}
      />
    )
  }
  if (result) {
    return (
      <PageFrame def={def}>
        <Banner tone={result.ok ? 'ok' : 'warn'} title={result.ok ? '授权完成' : '授权没有完成'}>
          {result.message}
        </Banner>
        <p>
          <Link to="/contribute" className="icon-link"><IconBack size={ACTION_ICON} />返回贡献入口</Link>
        </p>
      </PageFrame>
    )
  }

  return (
    <PageFrame def={def}>
      {starting && !session ? (
        <LoadingBlock rows={2} label={`正在发起 ${provider} 授权…`} />
      ) : !session && params.get('needs') === 'phone' ? (
        <form
          className="form-card"
          onSubmit={(e: FormEvent) => {
            e.preventDefault()
            // Raccoon 的 send_sms 必须携带阿里云滑块产物（captcha_param）：
            // 提交时先弹滑块，滑过再真正发起；其他 provider 直发。
            if (provider === 'raccoon') { launchRaccoonCaptcha(phone.trim()); return }
            start(phone.trim())
          }}
        >
          <Field label="手机号" required hint="供应商将向该号码发送验证码。">
            <input value={phone} onChange={(e) => setPhone(e.target.value)} required autoComplete="tel" inputMode="tel" />
          </Field>
          {provider === 'raccoon' && <div id="captcha-element" />}
          <FormError error={error} />
          <button id="send-code-btn" type="submit" className="btn-primary">发送验证码</button>
        </form>
      ) : session ? (
        <Section
          title="完成授权"
          hint={
            session.flow === 'qr'
              ? '用手机 App 扫码；本页会自动检测结果，完成后自动继续。'
              : session.flow === 'sms'
                ? '收到短信验证码后填在下面；本页会自动提交并继续检测。'
                : '在新标签页完成授权后回到本页即可——本页一直在后台检测，不需要刷新。'
          }
        >
          {/* device_code 形态必须先让用户看到码，所以码放在按钮**上面** */}
          {session.user_code && (
            <p>
              验证码：<code className="user-code">{session.user_code}</code>
              <span className="block-meta">在授权页输入该码完成确认</span>
            </p>
          )}
          {session.authorize_url && session.flow !== 'qr' && (
            <p>
              <a
                href={session.authorize_url}
                target="_blank"
                rel="noreferrer"
                className="btn-primary"
                style={{ display: 'inline-block' }}
              >
                {session.flow === 'device_code' ? '打开授权页并输入上方验证码' : '打开授权页'}
              </a>
              <span className="block-meta muted">若新标签被浏览器拦截，点这里即可。</span>
            </p>
          )}
          {session.flow === 'qr' && (session.qr_image_data || session.qr_image_url) && (
            <p>
              {/* 优先用 qr_image_data（服务端抓好的 data URL）：CSP 是
                  `img-src 'self' data:`，外链二维码会被浏览器静默拦掉
                  ——实测 naturalWidth 恒为 0 且不发请求。 */}
              <img
                src={session.qr_image_data ?? session.qr_image_url}
                alt="登录二维码"
                width={220}
                height={220}
                style={{ borderRadius: 8, background: '#fff', padding: 8 }}
              />
              <span className="block-meta">用手机 App 扫码授权</span>
            </p>
          )}
          {session.flow === 'qr' && !session.qr_image_data && !session.qr_image_url && session.authorize_url && (
            <p>
              {/* Raccoon 这类供应商没有图片端点：material 只有登录页 URL，
                  由前端本地画码（qrcode.react，MIT）。内容是 authorize_url
                  ——扫出来的就是「手机内打开的登录页」。CSP 无碍：SVG 是
                  内联元素不是外部资源。 */}
              <QRCodeSVG
                value={session.authorize_url}
                size={220}
                marginSize={1}
                style={{ borderRadius: 8, background: '#fff', padding: 8 }}
              />
              <span className="block-meta">用手机 App 扫码授权</span>
            </p>
          )}
          {session.flow === 'sms' && (
            <>
              <Field
                label="短信验证码"
                required
                hint="收到验证码后填入，点「确认登录」开始验证——输完再点，避免半截码被提交。"
              >
                <input
                  value={smsCode}
                  onChange={(e) => setSmsCode(e.target.value)}
                  autoComplete="one-time-code"
                  inputMode="numeric"
                />
              </Field>
              <p>
                <button
                  type="button"
                  className="btn-primary"
                  disabled={smsConfirmed || smsCode.length === 0}
                  onClick={() => {
                    smsConfirmedRef.current = true
                    setSmsConfirmed(true)
                  }}
                >
                  {smsConfirmed ? '验证中…' : '确认登录'}
                </button>
              </p>
            </>
          )}
          <p className="muted" aria-live="polite">
            {pollMsg || session.message ||
              (session.flow === 'qr'
                ? '等待扫码…'
                : session.authorize_url
                  ? '等待你在授权页完成操作…'
                  : '等待授权结果…')}
          </p>
          <FormError error={error} />
          <p>
            <Link
              to="/contribute"
              onClick={() => session.session_id && void cancelOAuthContribution(provider, session.session_id)}
            >
              放弃本次授权
            </Link>
          </p>
        </Section>
      ) : (
        <>
          <ErrorState error={error} />
          <p>
            <Link to="/contribute" className="icon-link"><IconBack size={ACTION_ICON} />返回贡献入口</Link>
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
  // 自动关闭尝试：本页可能是授权标签页到达的终点。window.close() 只对脚本
  // 打开的标签有效——对用户手动打开的标签是静默 no-op，失败也不打扰。
  // 为什么不提示「正在关闭」：脚本关不掉时那句提示是谎言，用户会以为卡死。
  useEffect(() => {
    window.close()
  }, [])
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
      {/* 关闭引导（用户报的「授权标签页不会自动关闭且无反应」）：
          部分供应商（如 CodeArts portal）在授权成功后不自动回跳，用户会停在
          供应商的成功页等待。本页能被渲染说明回跳已发生——此时本标签的历史使命
          已结束，明确告诉用户可以关闭，并给出在**原窗口**继续操作的入口。
          为什么不用 window.close 的结果做条件：脚本能否关标签由浏览器决定
          （只对脚本打开的标签有效），无法预知，UI 不该承诺。 */}
      <Section title="下一步">
        {/* 先说关闭，再说后续：用户报的卡点就是「授权成功后标签页悬着无反应」——
            本页渲染出来即代表回跳已完成，必须第一时间解除等待状态。 */}
        <p className="muted" aria-live="polite">
          授权流程已结束：本标签页可以关闭了。请回到发起授权的那个窗口继续操作；
          若供应商页面没有自动跳回，直接关闭它即可，不影响本次贡献结果。
        </p>
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
            <input value={code} onChange={(e) => setCode(e.target.value)} required {...SECRET_FIELD_PROPS} />
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
