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
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Box,
  Checkbox,
  Chip,
  FormControlLabel,
  List,
  ListItemButton,
  Tab,
  Tabs,
  Typography
} from '@mui/material'
import ExpandMoreIcon from '@mui/icons-material/ExpandMore'
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

/** 三个入口的标签与顺序（契约 v2 §8）：id 与后端 categories[].id 一一对应。 */
const CONTRIBUTE_TABS: { value: ContributionCategoryId; label: string }[] = [
  { value: 'reverse_proxy', label: '反代供应商' },
  { value: 'api', label: 'API 供应商' },
  { value: 'custom', label: '自定义供应商' }
]

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

/** 声称模型：界面允许逗号/空格分隔便于一次粘贴；后端只认第一个（claim 单模型）。 */
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
 * 贡献入口（公共区）：三类入口 = 反代供应商（供应商账号登录）/ API 供应商
 * （内置端点 + 粘贴 Key）/ 自定义供应商（任意 OpenAI 兼容端点）。
 *
 * 三段共用同一份无偿确认与同一张结果卡：确认是「贡献即无条件」的阻断点，
 * 放三份就会出现「这页勾了那页没勾」的裂缝；结果卡的恢复码更是只显示一次，
 * 分成三处展示等于给用户三条不同的回站路径。
 */
export default function ContributePage({ def }: PageProps) {
  const [tab, setTab] = useState<ContributionCategoryId>('reverse_proxy')
  const [freeConfirmed, setFreeConfirmed] = useState(false)
  const [result, setResult] = useState<ContributeResult | null>(null)
  const providers = useApi(() => getContributeProviders(), [])

  // 自定义供应商（任意 OpenAI 兼容端点）
  const [endpoint, setEndpoint] = useState('')
  const [customKey, setCustomKey] = useState('')
  const [customLabel, setCustomLabel] = useState('')
  const [customModels, setCustomModels] = useState('')
  const [customSubmitting, setCustomSubmitting] = useState(false)
  const [customError, setCustomError] = useState<unknown>(null)

  // API 供应商（内置目录 55 家；端点由后端解析，前端只给 provider id + Key）
  const [apiSearch, setApiSearch] = useState('')
  const [apiProviderId, setApiProviderId] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [apiLabel, setApiLabel] = useState('')
  const [apiModel, setApiModel] = useState('')
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
    if (!apiProviderId) {
      setApiError(new Error('请先在左侧选择一家 API 供应商'))
      return
    }
    setApiError(null)
    setApiSubmitting(true)
    try {
      // 不带 endpoint：契约 §3 规定 kind=api 的端点由后端从内置目录解析
      const r = await submitContribution({
        kind: 'api',
        provider: apiProviderId,
        api_key: apiKey,
        label: apiLabel,
        claimed_models: apiModel.trim() ? [apiModel.trim()] : []
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

      <Box sx={{ borderBottom: 1, borderColor: 'divider', mb: 1.5 }}>
        <Tabs
          value={tab}
          onChange={(_, v: ContributionCategoryId) => setTab(v)}
          aria-label="贡献入口"
          variant="scrollable"
          allowScrollButtonsMobile
        >
          {CONTRIBUTE_TABS.map((t) => (
            <Tab key={t.value} value={t.value} label={t.label} />
          ))}
        </Tabs>
      </Box>

      {/* 三个入口共用一份确认：反代点的是授权链接、两个表单点的是提交按钮，
          阻断点不同但语义相同——只放一份，避免「这个勾了那个没勾」。 */}
      <Box
        sx={{
          mb: 2,
          p: 1.25,
          border: 1,
          borderRadius: 2,
          borderColor: freeConfirmed ? 'success.main' : 'divider',
          bgcolor: 'background.paper'
        }}
      >
        <FreeConfirm checked={freeConfirmed} onChange={setFreeConfirmed} />
        <Typography variant="caption" color="text.secondary" component="p" sx={{ m: 0, mt: 0.25 }}>
          未勾选时：反代供应商的授权链接不可点，两个表单的提交按钮不可用。
        </Typography>
      </Box>

      {tab === 'reverse_proxy' && (
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
                    hint="供应商授权服务尚未配置。你仍然可以用「API 供应商」或「自定义供应商」入口贡献。"
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
                      <AccordionSummary expandIcon={<ExpandMoreIcon fontSize="small" />} sx={{ px: 0, minHeight: 40 }}>
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
                                    // 端点是自定义入口的输入；留在本页只会让人找不到填在哪
                                    setEndpoint(p.suggested_endpoint!)
                                    setTab('custom')
                                  }}
                                >
                                  填入自定义入口
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
      )}

      {tab === 'api' && (
        <Section
          title="API 供应商"
          hint="内置端点，只需粘贴该家的 Key；端点由服务端按目录解析，你不需要填。"
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
              const selected = items.find((p) => p.id === apiProviderId)

              return (
                <Box
                  sx={{
                    display: 'grid',
                    gridTemplateColumns: { xs: '1fr', md: 'minmax(15rem, 20rem) minmax(0, 1fr)' },
                    gap: 2,
                    alignItems: 'start'
                  }}
                >
                  <Box>
                    <Field label="搜索供应商" hint={`按名称或端点过滤，共 ${items.length} 家`}>
                      <input
                        value={apiSearch}
                        onChange={(e) => setApiSearch(e.target.value)}
                        placeholder="例如 deepseek 或 https://"
                      />
                    </Field>
                    <List
                      dense
                      sx={{
                        mt: 1,
                        maxHeight: 420,
                        overflowY: 'auto',
                        border: 1,
                        borderColor: 'divider',
                        borderRadius: 2,
                        py: 0
                      }}
                    >
                      {filtered.map((p) => (
                        <ListItemButton
                          key={p.id}
                          selected={p.id === apiProviderId}
                          onClick={() => {
                            setApiProviderId(p.id)
                            setApiModel('') // 换供应商即清空声称模型：模型 id 是分家的
                          }}
                          sx={{ flexDirection: 'column', alignItems: 'flex-start', gap: 0.25 }}
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
                        没有匹配的供应商，换个关键词试试。
                      </Typography>
                    )}
                  </Box>

                  <Box>
                    {!selected ? (
                      <EmptyState
                        title="先选择一家供应商"
                        hint="从左侧列表选一家，右侧会显示它的端点与可点选的模型。"
                      />
                    ) : (
                      <form className="form-card" onSubmit={onSubmitApi}>
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

                        <Field
                          label="声称的模型"
                          required
                          hint={
                            selected.models.length > 0
                              ? '点下面的模型填入，也可以手写目录外的模型名；鉴定核对的是声称与事实是否相符。'
                              : '这家的目录里没有登记模型，请手写模型名。'
                          }
                        >
                          <input
                            value={apiModel}
                            onChange={(e) => setApiModel(e.target.value)}
                            required
                            placeholder="例如 deepseek-flash"
                          />
                        </Field>
                        {selected.models.length > 0 && (
                          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75 }}>
                            {selected.models.map((m) => (
                              <Chip
                                key={m.id}
                                label={m.id}
                                size="small"
                                variant={apiModel === m.id ? 'filled' : 'outlined'}
                                color={apiModel === m.id ? 'primary' : 'default'}
                                onClick={() => setApiModel(m.id)}
                              />
                            ))}
                          </Box>
                        )}

                        <Field label="API Key" required hint="提交后不再显示，也不会出现在任何页面。">
                          <input
                            type="password"
                            value={apiKey}
                            onChange={(e) => setApiKey(e.target.value)}
                            required
                            autoComplete="off"
                          />
                        </Field>
                        <Field label="标签" hint="可选；未登录建号时用作显示名，凭据列表里也只能靠它识别。">
                          <input value={apiLabel} onChange={(e) => setApiLabel(e.target.value)} />
                        </Field>

                        <FormError error={apiError} />
                        <button type="submit" className="btn-primary" disabled={!freeConfirmed || apiSubmitting}>
                          {apiSubmitting ? '正在提交…' : `提交到 ${selected.name}`}
                        </button>
                      </form>
                    )}
                  </Box>
                </Box>
              )
            }}
          </AsyncView>
        </Section>
      )}

      {tab === 'custom' && (
        <Section
          title="自定义供应商"
          hint="任意 OpenAI 兼容端点（自建网关、局域网服务、其它中转站）。提交后 Key 永不回显；请填写这个 Key 声称能提供的模型。"
        >
          <form className="form-card" onSubmit={onSubmitCustom}>
            <Field label="端点地址" required hint="该 Key 指向的 OpenAI 兼容 base URL，例如 https://api.example.com/v1">
              <input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} required placeholder="https://…" />
            </Field>
            <Field label="API Key" required hint="提交后不再显示，也不会出现在任何页面。">
              <input
                type="password"
                value={customKey}
                onChange={(e) => setCustomKey(e.target.value)}
                required
                autoComplete="off"
              />
            </Field>
            <Field label="标签" required hint="你自己认得出的名字，例如「备用号」；凭据列表里只能靠它识别。">
              <input value={customLabel} onChange={(e) => setCustomLabel(e.target.value)} required />
            </Field>
            <Field
              label="声称的模型"
              required
              hint="一次提交只声明一个模型（后端取第一个值）；多填也只会用第一个。"
            >
              <input
                value={customModels}
                onChange={(e) => setCustomModels(e.target.value)}
                required
                placeholder="例如 deepseek-chat"
              />
            </Field>
            <FormError error={customError} />
            <button type="submit" className="btn-primary" disabled={!freeConfirmed || customSubmitting}>
              {customSubmitting ? '正在提交…' : '提交贡献'}
            </button>
          </form>
        </Section>
      )}

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
  const [smsCode, setSmsCode] = useState('')
  const [polling, setPolling] = useState(false)
  const [phone, setPhone] = useState('')
  const [result, setResult] = useState<{ ok: boolean; message: string; recoveryCode?: string } | null>(null)
  const navigate = useNavigate()

  // 发起（短信形态由页内表单再调一次，故抽成函数）。
  function start(phone?: string) {
    setStarting(true)
    setError(null)
    startOAuthContribution(provider, phone)
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
  useEffect(() => {
    if (!session?.session_id) return
    let alive = true
    const tick = async () => {
      if (polling) return
      setPolling(true)
      try {
        const r = await pollOAuthContribution(provider, {
          session_id: session.session_id!,
          ...(smsCode && session.flow === 'sms' ? { code: smsCode } : {})
        })
        if (!alive) return
        const d = r.data
        if (d.status === 'ok' && d.outcome) {
          setResult({
            ok: true,
            message: d.outcome.message,
            recoveryCode: d.outcome.recovery_code
          })
          return
        }
        if (d.status === 'failed' || d.status === 'expired') {
          setResult({ ok: false, message: d.message ?? '授权未完成' })
          return
        }
      } catch (e) {
        if (alive) setError(e)
      } finally {
        if (alive) setPolling(false)
      }
    }
    const timer = window.setInterval(tick, Math.max(2, session.interval ?? 3) * 1000)
    void tick()
    return () => {
      alive = false
      window.clearInterval(timer)
    }
  }, [session, provider, smsCode, polling])

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
          <Link to="/contribute">返回贡献入口</Link>
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
            start(phone.trim())
          }}
        >
          <Field label="手机号" required hint="供应商将向该号码发送验证码。">
            <input value={phone} onChange={(e) => setPhone(e.target.value)} required autoComplete="tel" inputMode="tel" />
          </Field>
          <FormError error={error} />
          <button type="submit" className="btn-primary">发送验证码</button>
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
          {session.flow === 'sms' && (
            <Field label="短信验证码" required hint="收到验证码后填入，本页会自动提交并继续检测。">
              <input value={smsCode} onChange={(e) => setSmsCode(e.target.value)} autoComplete="one-time-code" inputMode="numeric" />
            </Field>
          )}
          <p className="muted" aria-live="polite">
            {session.message ??
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
