// sidecar/hub.mjs —— dsh-providers-hub 的真实装载与能力封装。
//
// ## 实测结论（2026-10-22，hub 0.2.1-rc.2 / node v26.5.0）
//
// | 入口 | 结果 | 证据 |
// |---|---|---|
// | `import 'dsh-providers-hub'`（`.` 导出） | **装载成功但不可用** | 导出 `{Config, apply, inject, makeReadImage, ...}`，是一个 cordis 插件；`apply(ctx)` 要一个完整的 Cordis Context（credentials/llm/settings/store 全在 ctx 上），在 DSH 之外没有 |
// | `import '.../client'` | **失败** | `ReferenceError: window is not defined`（`lib/client/jet-hub.js` 是 `window.__ModuleLoader__.load({...})` 浏览器格式） |
// | `import '.../lib/<provider>-adapter.js'` | **成功**（配合 deps.mjs 的解析钩子） | 例：`ModelscopeAdapter` 可实例化，注入假 key 后 `stream()` 真发出 `POST https://api-inference.modelscope.cn/v1/chat/completions` 并解析 SSE 成 StreamChunk 事件（**该 adapter 已随 modelscope 于 2026-10-24 下线**，这条记录保留为「深路径 import 可行」的实测证据） |
//
// 所以本 sidecar 选**深路径 adapter**，不加载 cordis 插件。代价与边界见下：
//   - OAuth 型 provider 的 adapter 从 `options.accountPool` 里取 `credential.access_token`，
//     账号池是 DSH 的运行态（凭据在 DSH 的 .credentials.yaml 里，池语义在 DSH 内存里）。
//     我们**能装载**它们的模块，但**不能**在没有账号池替身的情况下发起调用 —— 这类一律
//     返回 E_NOT_IMPLEMENTED 并写清原因，不做「看起来能用」的假实现。
//
// ## 红线
//
// 本文件**不读不写** `$DSH_HOME/jet-hub/state.json`，也**不支持**任何写凭据的操作：
// hub 的 adapter 只被当作「HTTP 协议实现」使用，凭据值由调用方（Go 侧）传入。

import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { loadDeps } from './deps.mjs';
import * as dshCredentials from './credentials.mjs';
// registry 提供「per-provider 凭据续期」的已登记实现（registry.refresh({credential})）。
// 为什么 hub 反向引 registry：续期原语各家形状不一，registry 是**已实测的那份**包装；
// provider 模块不 import hub，无环。
import * as registry from './providers/registry.mjs';

// HUB_PATH 与 index.mjs 同源（FR_HUB_PATH 由 Go 侧 Options.HubPath 注入）。
export function hubPath() {
  return process.env.FR_HUB_PATH
    || path.join(process.env.DSH_HOME || path.join(process.env.HOME || '', '.local/share/dsh'),
                 'profiles/web/node_modules/dsh-providers-hub');
}

// —— 能力表 ——
//
// 只登记**实测过**的 provider。字段含义：
//   module     : 相对 hub 根的文件（深路径 import 的目标）
//   cls        : 导出的 adapter 类名
//   slotOption : 注入槽位的选项名（两种 adapter 各用各的名字，见下方注释）
//   slotsOf    : 把「一个明文 key」翻译成该 adapter 期望的槽位数组
//   shape      : 'api_key' = 可独立驱动；'oauth' = 需要 DSH 账号池，仅报告能力
//
// 为什么现在没有 shape: 'api_key' 的条目：modelscope / opencode 两家已按契约 v2 §7
// 从本表与 sidecar/providers/ 一起删除，而**其余 OAuth 型 provider 依赖 DSH 账号池**，
// 在 sidecar 里没有替身。所以本侧不再提供任何「粘贴一把 key 就能驱动」的 adapter——
// 那种形态由主进程的内置目录 + 直连出站承担（少一层进程，也少一处明文 key 落点）。
// 表里仍保留 shape 字段：若将来重新登记 apiKey 型 adapter，判定分支已在 adapterFor 里。
const ADAPTERS = {
  // 以下是「模块可装载、调用需 DSH 账号池」的 provider：登记它们是为了让
  // health/deps 能如实报告「17 家里哪几家 adapter 模块在位」，而不是假装全部不可用。
  cline: { module: 'lib/cline-adapter.js', cls: 'ClineAdapter', shape: 'oauth' },
  minimax: { module: 'lib/minimax-adapter.js', cls: 'MinimaxAdapter', shape: 'oauth' },
  accio: { module: 'lib/accio-adapter.js', cls: 'AccioAdapter', shape: 'oauth' },
  trae: { module: 'lib/trae-adapter.js', cls: 'TraeAdapter', shape: 'oauth' },
  qoder: { module: 'lib/qoder-adapter.js', cls: 'QoderAdapter', shape: 'oauth' },
  zcode: { module: 'lib/zcode-adapter.js', cls: 'ZcodeAdapter', shape: 'oauth' },
  lobsterai: { module: 'lib/lobsterai-adapter.js', cls: 'LobsteraiAdapter', shape: 'oauth' },
  raccoon: { module: 'lib/raccoon-adapter.js', cls: 'RaccoonAdapter', shape: 'oauth' },
  loomy: { module: 'lib/loomy-adapter.js', cls: 'LoomyAdapter', shape: 'oauth' },
  autoclaw: { module: 'lib/autoclaw-adapter.js', cls: 'AutoclawAdapter', shape: 'oauth', productModule: 'lib/autoclaw-product.js', productName: 'AUTOCLAW' },
  astudio: { module: 'lib/astudio-adapter.js', cls: 'AstudioAdapter', shape: 'oauth' },
  gemini: { module: 'lib/gemini-adapter.js', cls: 'GeminiAdapter', shape: 'oauth' },
  // 实测类名是 OfficeAceAdapter（不是 OfficeaceAdapter）——版本漂移会在这里先炸，
  // 所以类名一律以实测导出为准，不按 provider 名猜。
  officeace: { module: 'lib/officeace-adapter.js', cls: 'OfficeAceAdapter', shape: 'oauth' },
  // codearts 没有独立 adapter 文件，它的 adapter 与注册器都在 llm-adapter.js 里。
  codearts: { module: 'lib/llm-adapter.js', cls: 'CodeArtsAdapter', shape: 'oauth' },
  // 地区别名共用同一份实现（04-sidecar 6.2 表的备注列）。
  minimaxintl: { module: 'lib/minimax-adapter.js', cls: 'MinimaxAdapter', shape: 'oauth', productModule: 'lib/minimax-product.js', productName: 'MINIMAX_INTL' },
  qodercn: { module: 'lib/qoder-adapter.js', cls: 'QoderAdapter', shape: 'oauth', productModule: 'lib/qoder-product.js', productName: 'QODER_CN' },
  // 腾讯 CodeBuddy / WorkBuddy（2026-11-01 移植）：同一家实现两个 product，
  // 身份头（User-Agent）必须跟产品走——productModule 显式注入。
  buddy: { module: 'lib/buddy-adapter.js', cls: 'BuddyAdapter', shape: 'oauth', productModule: 'lib/product.js', productName: 'CODEBUDDY' },
  workbuddy: { module: 'lib/buddy-adapter.js', cls: 'BuddyAdapter', shape: 'oauth', productModule: 'lib/product.js', productName: 'WORKBUDDY' },
};

// 共用选项：adapter 会读但允许缺省的字段。给最小实现而不是 undefined，
// 避免每个 provider 在「没有账号池」时各抛各的 TypeError。
function baseOptions(apiKey, slotOption, slotsOf) {
  // slotsOf 缺省兜底（2026-10-31）：adapterForWithCredential 的调用不带槽位
  // （oauth 形状的 ADAPTERS 条目没有 slotOption/slotsOf 字段）——槽位是
  // apiKey 形状专属的机制，oauth 形状走 resolveCredential 钩子。
  const slots = typeof slotsOf === 'function' ? slotsOf(apiKey) : [];
  return {
    // 槽位：只有 apiKey 型 adapter 会用到（当前表里没有这类条目，保留机制因为它
    // 是把「一把明文 key」翻译成 DSH 槽位形状的唯一入口）
    [slotOption]: async () => slots,
    // 目录拉取：adapter 自己在远端失败时会回退静态表，这里给真 fetch
    fetchRemoteCatalog: undefined,
    // 账号池替身：只提供 adapter 会调的那几个读方法（写方法不存在 = 我们不留状态）
    accountPool: {
      findAccountIdByCredential: async () => '',
      // 必须是 null 不是 undefined：adapter 的换号判据是 `next === null`
      // （qoder-adapter 换号路径实测，undefined 会在 next.entry.id 上 TypeError）。
      getAvailableAccount: async () => null,
      isRateLimited: async () => false,
      // 这两个是写语义（标记限流/更新限流）：不实现 —— 红线①的延伸，
      // sidecar 不为 DSH 的池状态做任何持久化决定。
      updateModelRateLimit: async () => {},
      markRateLimited: async () => {},
    },
    providerClosed: () => false,
    warn: (msg) => process.stderr.write(`[hub-adapter] ${msg}\n`),
  };
}

let deps = null;      // loadDeps() 的结果（进程内一次）
let depsTried = false;
const moduleCache = new Map(); // module rel → exports | Error
const adapterCache = new Map(); // provider + key 指纹 → adapter 实例

// getDeps 懒装载依赖（health 第一次调用时触发），失败不抛——返回结构化状态。
export async function getDeps() {
  if (depsTried) return deps;
  depsTried = true;
  // hubRoot 传给 shim：hub lib 实体复制进 shim 后，credits 分发器（17 家）才能被解析。
  deps = await loadDeps(process.env.FR_DSH_DEPS_ROOT, hubPath());
  return deps;
}

// loadModule 深路径 import 一个 hub 模块（带缓存与错误归一）。
// 为什么不用 import() 裸跑：失败原因要能原样进 health（缺 B 层包 / 文件不存在 / 语法错误
// 是三种完全不同的处置），所以这里保留第一行错误文本。
async function loadModule(rel) {
  if (moduleCache.has(rel)) {
    const hit = moduleCache.get(rel);
    if (hit instanceof Error) throw hit;
    return hit;
  }
  const file = path.join(hubPath(), rel);
  try {
    const mod = await import(pathToFileURL(file).href);
    moduleCache.set(rel, mod);
    return mod;
  } catch (e) {
    const err = new Error(String(e.message || e).split('\n')[0]);
    err.code = e.code || 'ERR_HUB_MODULE';
    moduleCache.set(rel, err);
    throw err;
  }
}

// adapterFor 取一个已实例化的 adapter。
// key 参与缓存键但不进日志/错误：同一进程内换 key 会得到新实例（OAuth token 轮换语义）。
export async function adapterFor(provider, apiKey) {
  // 先把 B 层依赖的解析钩子装上：任何 adapter 模块的 import 都依赖它。
  // 放在这里而不是只放在 probe()：调用可能先于 health 到达（Go 侧启动顺序不保证）。
  await getDeps();
  const spec = ADAPTERS[provider];
  if (!spec) return null;
  if (spec.shape !== 'api_key') {
    throw new SidecarHubError('E_NOT_IMPLEMENTED',
      `${provider} 的 adapter 依赖 DSH 账号池（credential.access_token），sidecar 无账号池替身`);
  }
  if (!apiKey) throw new SidecarHubError('E_BAD_REQUEST', `${provider} 需要明文 key 才能发起调用`);
  const cacheKey = `${provider}\u0000${apiKey}`; // key 只在内存里做缓存键，不落盘不落日志
  const cached = adapterCache.get(cacheKey);
  if (cached) return cached;
  const mod = await loadModule(spec.module);
  const Cls = mod[spec.cls];
  if (typeof Cls !== 'function') {
    throw new SidecarHubError('E_NOT_IMPLEMENTED', `${spec.module} 未导出 ${spec.cls}（hub 版本漂移）`);
  }
  const inst = new Cls(baseOptions(apiKey, spec.slotOption, spec.slotsOf));
  adapterCache.set(cacheKey, inst);
  return inst;
}

// adapterForWithCredential 用**调用方显式传入的凭据**实例化 oauth 形状的 adapter。
//
// 为什么存在（2026-10-31 qodercn 移植）：FreeRouter 的贡献凭据存在主进程密文库里
// （不进 DSH 账号池），推理时主进程解密后经 stdio 单帧传入——把「账号池」依赖压缩
// 成「单凭据注入」的最小替身：oauth 形状 adapter 取凭据的两个钩子
// （resolveCredential / refresh）接到这份凭据上，其余池语义保持 baseOptions 空实现。
// 凭据只在本调用内存活：不缓存（token 会轮换，按内容缓存会命中旧实例）、不落盘
// ——红线①的推理侧镜像（只读使用，没有写回路径）。
export async function adapterForWithCredential(provider, credential, onRefreshed) {
  await getDeps();
  const spec = ADAPTERS[provider];
  if (!spec) return null;
  const mod = await loadModule(spec.module);
  const Cls = mod[spec.cls];
  if (typeof Cls !== 'function') {
    throw new SidecarHubError('E_NOT_IMPLEMENTED', `${spec.module} 未导出 ${spec.cls}（hub 版本漂移）`);
  }
  const opts = baseOptions('', spec.slotOption, spec.slotsOf);
  // ⚠️ **product 必须按 provider 设置**（2026-10-31 qodercn 移植实测的真缺陷）：
  // adapter 构造器 `this.product = options.product ?? <国际版默认>`——不传时
  // qodercn 的凭据会被发到国际版网关（api2.qoder.sh），服务端回
  // 「Login expired (105)」而真实原因只是「渠道错配」。变体渠道（intl/cn）
  // 的 product 常量在各自的 product 模块里，按清单加载注入。
  if (spec.productModule !== undefined && spec.productName !== undefined) {
    const pmod = await loadModule(spec.productModule);
    opts.product = pmod[spec.productName];
  }
  opts.resolveCredential = async () => credential;
  // 续期钩子（2026-10-31 批量移植泛化）：qoder 系保留专用链路（wasm 原语 +
  // 105 重绑，见下）；其余 provider 优先走 registry 已登记的 refresh
  // （gemini/accio），没登记的降级为 no-op——主动续期窗口沿用当前凭据
  // （token 还没过期就能用），真过期时上游 401 → 用户重新授权。宁可降级
  // 也不猜：各家续期原语形状不一，写错等于把好凭据换坏。
  if (provider !== 'qoder' && provider !== 'qodercn') {
    // minimax 系：hub 有完整续期原语（refreshMinimaxCredential，refresh_token
    // 换新）——不接它，access_token 过期后推理就死到重新授权为止（实测 30 分钟
    // 级 TTL，必接）。productModule 决定国内/国际网关，与构造时同源。
    if (provider === 'minimax' || provider === 'minimaxintl') {
      opts.refresh = async () => {
        const mo = await loadModule('lib/minimax-oauth.js');
        const pm = await loadModule(spec.productModule ?? 'lib/minimax-product.js');
        const product = pm[spec.productName ?? 'MINIMAX'];
        if (!credential || !credential.refresh_token) {
          throw new Error('minimax: 凭据不可续期（缺 refresh_token），请重新授权');
        }
        const next = await mo.refreshMinimaxCredential(credential.refresh_token, fetch, product);
        credential = next;
        if (typeof onRefreshed === 'function') onRefreshed(next);
      };
      return new Cls(opts);
    }
    // autoclaw：hub 有 refreshAutoclawToken（refresh_token 换新，双端点回退）。
    // 不接它，401 Invalid token 后推理死到重新授权为止（实测 token 会过期）。
    if (provider === 'autoclaw') {
      opts.refresh = async () => {
        const ao = await loadModule('lib/autoclaw-oauth.js');
        const pm = await loadModule('lib/autoclaw-product.js');
        if (!credential || !credential.refresh_token) {
          throw new Error('autoclaw: 凭据不可续期（缺 refresh_token），请重新授权');
        }
        const next = await ao.refreshAutoclawToken(credential.refresh_token, {
          product: pm[spec.productName ?? 'AUTOCLAW'],
        });
        credential = next;
        if (typeof onRefreshed === 'function') onRefreshed(next);
      };
      return new Cls(opts);
    }
    // buddy 系：hub 的 refreshToken(credential, fetcher, signal, product)——
    // refresh_token 过期抛 RefreshTokenExpiredError，由 adapter 的 401 路径
    // 归类为 AUTH → 用户重新授权。
    if (provider === 'buddy' || provider === 'workbuddy') {
      opts.refresh = async () => {
        const bo = await loadModule('lib/buddy-oauth.js');
        const pm = await loadModule(spec.productModule ?? 'lib/product.js');
        const product = pm[spec.productName ?? 'CODEBUDDY'];
        if (!credential || !credential.refresh_token) {
          throw new Error('buddy: 凭据不可续期（缺 refresh_token），请重新授权');
        }
        const next = await bo.refreshToken(credential, fetch, undefined, product);
        if (next && typeof next === 'object') {
          credential = next;
          if (typeof onRefreshed === 'function') onRefreshed(next);
        }
      };
      return new Cls(opts);
    }
    // 其余：registry 已登记 refresh 的（gemini/accio）走登记实现；没登记的
    // 降级为 no-op——主动续期窗口沿用当前凭据（token 还没过期就能用），真
    // 过期时上游 401 → 用户重新授权。宁可降级也不猜：各家续期原语形状不一，
    // 写错等于把好凭据换坏。
    const entry = registry.PROVIDERS[provider];
    const regRefresh = entry && typeof entry.refresh === 'function' ? entry.refresh : null;
    opts.refresh = async () => {
      if (!regRefresh) return; // 降级：沿用当前凭据（no-op），过期走重新授权
      const next = await regRefresh({ credential });
      if (next && typeof next === 'object') {
        credential = next;
        if (typeof onRefreshed === 'function') onRefreshed(next);
      }
    };
    return new Cls(opts);
  }
  // 105（auth_error）的续期钩子：用 refresh_token 走 deviceToken/refresh 换新
  // （hub qoder.js 原语 + QODER_REFRESH_PATH）。新凭据**重绑闭包**——adapter 的
  // 重试经 resolveCredential 拿到新 token；并经 onRefreshed 透传，由 streamInfer
  // 装进应答信封回 Go 写回密文（与 credits.query 静默续期同一条通道）。
  opts.refresh = async () => {
    const qm = await loadModule('lib/qoder.js');
    const prod = await loadModule('lib/qoder-product.js');
    const product = provider === 'qodercn' ? prod.QODER_CN : prod.QODER;
    if (typeof mod.isQoderRefreshable !== 'function' || !mod.isQoderRefreshable(credential)) {
      throw new Error('qoder: 凭据不可续期（缺 refresh_token），请重新授权');
    }
    const r = await fetch(product.openApiBase + mod.QODER_REFRESH_PATH, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
        'User-Agent': `${product.userAgentPrefix}/1.0.0`,
      },
      body: JSON.stringify(mod.qoderRefreshBody(credential)),
      signal: AbortSignal.timeout(20000),
    });
    if (r.status === 401 || r.status === 403) {
      throw new Error(`qoder: 续期被拒（HTTP ${r.status}），请重新授权`);
    }
    const payload = await r.json();
    credential = mod.applyQoderRefresh(credential, payload);
    if (typeof onRefreshed === 'function') onRefreshed(credential);
  };
  return new Cls(opts);
}

// probe 报告 hub 装载状态：hub 根是否可读、依赖根是否找到、各 adapter 模块是否可 import。
// health 直接把它摊开给 Go 侧，避免「能用/不能用」靠试调用去猜（04-sidecar 3 节的同一原则）。
export async function probe() {
  const d = await getDeps();
  const out = {
    hubPath: hubPath(), hubOk: false,
    depsOk: Boolean(d && d.ok), depsDetail: d?.detail || '',
    // depRoot 是 B 层依赖的真实根（health 用它做逐包存在性判定，而不是靠 require 解析）
    depRoot: (d && d.root) || '',
    adapters: [],
  };
  try {
    // 用任意一个仍在表里的模块探「hub 根 + 依赖钩子是否可用」：modelscope 曾充当这个
    // 探针，它下线后换 cline（同属「模块可装载」的一类，不涉及账号池实例化）。
    await loadModule('lib/cline-adapter.js');
    out.hubOk = true;
  } catch (e) {
    out.depsDetail = out.depsDetail || e.message;
    return out;
  }
  for (const [provider, spec] of Object.entries(ADAPTERS)) {
    try {
      const mod = await loadModule(spec.module);
      const ok = typeof mod[spec.cls] === 'function';
      // 记录「模块在但类名不对」这种漂移：它的表现与「模块缺了」一样是 E_NOT_IMPLEMENTED，
      // 但修法不同（前者是 hub 改导出名，后者是版本/路径问题）。
      out.adapters.push({ provider, module: spec.module, shape: spec.shape, ok,
        detail: ok ? '' : `${spec.module} 未导出 ${spec.cls}（导出键：${Object.keys(mod).slice(0, 6).join(',')}）` });
    } catch (e) {
      out.adapters.push({ provider, module: spec.module, shape: spec.shape, ok: false, detail: e.message });
    }
  }
  return out;
}

// listModels 拉某个 provider 的模型目录（apiKey 型）。
// 归一成本契约的形状（04-sidecar 2.2）：{upstreamModelId, displayName, contextWindow, modalities, efforts}。
export async function listModels({ provider, credentialRef, apiKey }) {
  const adapter = await adapterFor(provider, resolveKey({ credentialRef, apiKey }));
  const raw = await adapter.listModels(provider);
  return (raw || []).map((m) => ({
    upstreamModelId: m.id,
    displayName: m.name || m.id,
    contextWindow: typeof m.contextWindow === 'number' ? m.contextWindow : null,
    modalities: Array.isArray(m.inputModalities) ? m.inputModalities : (m.modalities || []),
    efforts: Array.isArray(m.reasoningEfforts) ? m.reasoningEfforts : [],
  }));
}

// openaiBodyToGenerateOptions：把 Go 侧编好的 OpenAI Chat 请求体翻译成 adapter 的
// GenerateOptions。**只翻译结构，不改语义**：模型名/消息/采样参数/工具原样带过。
// 为什么需要这一层：adapter 收的是 DSH 的 IR 形状（messages 是 block 数组），
// 而不是线上 OpenAI 形状——这层翻译是「Go 编码成 OpenAI、sidecar 再解回 IR」的代价，
// 比让 Go 侧直接产出 IR JSON 更稳（IR 形状是 DSH 内部契约，随版本漂移）。
function openaiBodyToGenerateOptions(provider, model, body) {
  const msgs = Array.isArray(body.messages) ? body.messages : [];
  const messages = msgs.map((m) => {
    const content = m && m.content;
    if (typeof content === 'string') {
      return { role: m.role, content: [{ type: 'text', text: content }] };
    }
    if (Array.isArray(content)) {
      return {
        role: m.role,
        content: content.map((part) => {
          if (part && part.type === 'text') return { type: 'text', text: String(part.text ?? '') };
          // 图片等富模态：保留原始片段，交给 adapter 自己判能力（不在这里丢字段）
          return part;
        }),
      };
    }
    return { role: m.role, content: [] };
  });
  const opts = { provider, model, messages };
  if (typeof body.temperature === 'number') opts.temperature = body.temperature;
  if (typeof body.max_tokens === 'number') opts.maxTokens = body.max_tokens;
  if (Array.isArray(body.stop)) opts.stop = body.stop;
  if (body.reasoning_effort) opts.reasoningEffort = body.reasoning_effort;
  if (Array.isArray(body.tools)) {
    opts.tools = body.tools.map((t) => ({
      name: t?.function?.name,
      description: t?.function?.description,
      parameters: t?.function?.parameters,
    })).filter((t) => t.name);
  }
  return opts;
}

// chunksToSSE 把 StreamChunk 序列还原成 OpenAI chat.completion.chunk 的 SSE 文本。
// 为什么还原成 SSE 而不是自造 JSON：Go 侧的 StreamReader 是按候选方言（openai chat）
// 解包的，返回线上格式意味着 Go 侧一行都不用改（relay.go 的既定消费面）。
function chunksToSSE(model, chunks) {
  const id = `chatcmpl-fr-${Date.now().toString(36)}`;
  const created = Math.floor(Date.now() / 1000);
  let out = '';
  const frame = (delta, finish) => {
    out += 'data: ' + JSON.stringify({
      id, object: 'chat.completion.chunk', created, model,
      choices: [{ index: 0, delta, finish_reason: finish ?? null }],
    }) + '\n\n';
  };
  frame({ role: 'assistant', content: '' });
  let sawText = false;
  for (const c of chunks) {
    if (c.type === 'text-delta' && c.text) { frame({ content: c.text }); sawText = true; }
    else if (c.type === 'reasoning-delta' && c.text) { frame({ reasoning_content: c.text }); sawText = true; }
    else if (c.type === 'tool-call-delta') {
      frame({ tool_calls: [{ index: c.index ?? 0, id: c.id, function: { name: c.name, arguments: c.argumentsDelta || '' } }] });
      sawText = true;
    }
  }
  const last = [...chunks].reverse().find((c) => c.type === 'finish');
  frame({}, last ? (last.reason === 'stop' ? 'stop' : String(last.reason)) : (sawText ? 'stop' : 'stop'));
  out += 'data: [DONE]\n\n';
  return out;
}

// chunksToCompletion 把 StreamChunk 流合成一条**非流式** chat.completion。
// 为什么需要：relay 的非流式消费面（proxyOnce → DecodeResponse）要 JSON 体，
// 而 oauth 形状 adapter 只有流式出口——sidecar 负责把流攒回整响应。
// reasoning_content 按 DeepSeek 扩展字段回填（ir 解码器忽略未知字段，
// 探测/兼容流量不受影响；完整语义走流式路径）。
export function chunksToCompletion(model, chunks) {
  const id = `chatcmpl-fr-${Date.now().toString(36)}`;
  const created = Math.floor(Date.now() / 1000);
  let content = '';
  let reasoning = '';
  const toolCalls = [];
  let finish = 'stop';
  for (const c of chunks) {
    if (c.type === 'text-delta' && c.text) content += c.text;
    else if (c.type === 'reasoning-delta' && c.text) reasoning += c.text;
    else if (c.type === 'tool-call-delta') {
      const idx = c.index ?? 0;
      while (toolCalls.length <= idx) toolCalls.push({ id: '', type: 'function', function: { name: '', arguments: '' } });
      if (c.id) toolCalls[idx].id = c.id;
      if (c.name) toolCalls[idx].function.name = c.name;
      toolCalls[idx].function.arguments += c.argumentsDelta || '';
    } else if (c.type === 'finish') {
      finish = c.reason === 'stop' ? 'stop' : String(c.reason ?? 'stop');
    }
  }
  const message = { role: 'assistant', content };
  if (reasoning) message.reasoning_content = reasoning;
  if (toolCalls.length > 0) message.tool_calls = toolCalls;
  return {
    id, object: 'chat.completion', created, model,
    choices: [{ index: 0, message, finish_reason: finish }],
    usage: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
  };
}

// resolveKey 取明文 key：优先显式 apiKey（FreeRouter 自有凭据，由 Go 侧解密后单帧传入），
// 否则按 credentialRef 读 DSH 的只读存储（红线①：只读，没有写回路径）。
// 为什么 key 走**顶层参数**而不是塞进 body：body 会被原样交给 adapter，且一旦进身体
// 就可能随请求体进日志/进上游错误回显；顶层字段在 handler 里就地消费，不落任何结构。
function resolveKey({ credentialRef, apiKey }) {
  if (apiKey) return apiKey;
  if (credentialRef) return dshCredentials.resolve(credentialRef) || '';
  return '';
}

// streamInfer 执行一次推理，返回 SSE 文本（relay.go 期待的形状）。
// 为什么整段缓冲而不是逐帧回传：NDJSON 是一问一答（04-sidecar 2.1：sidecar 不主动推送），
// 真流式要改契约（多帧应答），属于单独的一次接口演进；当前先把「adapter 真能跑通」落地。
export async function streamInfer({ provider, model, body, credentialRef, apiKey, credential, stream = true }) {
  // 凭据三选一：显式 credential（FreeRouter 贡献凭据，单帧传入）> apiKey > credentialRef。
  let refreshed;
  const adapter = credential !== undefined && credential !== null
    ? await adapterForWithCredential(provider, credential, (c) => { refreshed = c; })
    : await adapterFor(provider, resolveKey({ credentialRef, apiKey }));
  const opts = openaiBodyToGenerateOptions(provider, model, body || {});
  opts.signal = new AbortController().signal;
  const chunks = [];
  for await (const c of adapter.stream(opts)) chunks.push(c);
  // 流式 → SSE 文本（relay_stream 逐帧解）；非流式 → 合成完整 chat.completion
  // （proxyOnce → DecodeResponse 要 JSON 体）。续期出的新凭据随信封回 Go 写回。
  const payload = stream ? { sse: chunksToSSE(model, chunks) } : { completion: chunksToCompletion(model, chunks) };
  if (refreshed !== undefined) payload.refreshed_credential = refreshed;
  return payload;
}

// SidecarHubError 是本模块的错误类型（index.mjs 统一转成帧错误码）。
export class SidecarHubError extends Error {
  constructor(code, message, retryable = false) {
    super(message);
    this.code = code;
    this.retryable = retryable;
  }
}
