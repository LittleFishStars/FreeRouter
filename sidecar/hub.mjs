// sidecar/hub.mjs —— dsh-providers-hub 的真实装载与能力封装。
//
// ## 实测结论（2026-10-22，hub 0.2.1-rc.2 / node v26.5.0）
//
// | 入口 | 结果 | 证据 |
// |---|---|---|
// | `import 'dsh-providers-hub'`（`.` 导出） | **装载成功但不可用** | 导出 `{Config, apply, inject, makeReadImage, ...}`，是一个 cordis 插件；`apply(ctx)` 要一个完整的 Cordis Context（credentials/llm/settings/store 全在 ctx 上），在 DSH 之外没有 |
// | `import '.../client'` | **失败** | `ReferenceError: window is not defined`（`lib/client/jet-hub.js` 是 `window.__ModuleLoader__.load({...})` 浏览器格式） |
// | `import '.../lib/<provider>-adapter.js'` | **成功**（配合 deps.mjs 的解析钩子） | 例：`ModelscopeAdapter` 可实例化，注入假 key 后 `stream()` 真发出 `POST https://api-inference.modelscope.cn/v1/chat/completions` 并解析 SSE 成 StreamChunk 事件 |
//
// 所以本 sidecar 选**深路径 adapter**，不加载 cordis 插件。代价与边界见下：
//   - apiKey 型 provider（modelscope / opencode）adapter 的构造选项全是调用方注入
//     （`slots()` / `identitySlots()`），因此可以完全在 DSH 之外驱动；
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
// 为什么 modelscope 用 slots、opencode 用 identitySlots：两者在 hub 里是两套骨架——
// modelscope 的 `slots()` 返回 `{id, apiKey}`；opencode 的 `identitySlots()` 还要求
// `kind: 'account' | 'anonymous'`（决定付费目录可见性与指纹派生）。照抄对方的形状会
// 让「账号槽」被当成匿名槽，直接表现是「key 有效却看不到付费模型」。
const ADAPTERS = {
  modelscope: {
    module: 'lib/modelscope-adapter.js',
    cls: 'ModelscopeAdapter',
    slotOption: 'slots',
    shape: 'api_key',
    slotsOf: (apiKey) => [{ id: 'fr-slot', apiKey }],
  },
  opencode: {
    module: 'lib/opencode-adapter.js',
    cls: 'OpencodeAdapter',
    slotOption: 'identitySlots',
    shape: 'api_key',
    slotsOf: (apiKey) => [{ id: 'fr-slot', kind: 'account', apiKey, enabled: true }],
  },
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
  autoclaw: { module: 'lib/autoclaw-adapter.js', cls: 'AutoclawAdapter', shape: 'oauth' },
  astudio: { module: 'lib/astudio-adapter.js', cls: 'AstudioAdapter', shape: 'oauth' },
  gemini: { module: 'lib/gemini-adapter.js', cls: 'GeminiAdapter', shape: 'oauth' },
  // 实测类名是 OfficeAceAdapter（不是 OfficeaceAdapter）——版本漂移会在这里先炸，
  // 所以类名一律以实测导出为准，不按 provider 名猜。
  officeace: { module: 'lib/officeace-adapter.js', cls: 'OfficeAceAdapter', shape: 'oauth' },
  // codearts 没有独立 adapter 文件，它的 adapter 与注册器都在 llm-adapter.js 里。
  codearts: { module: 'lib/llm-adapter.js', cls: 'CodeArtsAdapter', shape: 'oauth' },
  // 地区别名共用同一份实现（04-sidecar 6.2 表的备注列）。
  minimaxintl: { module: 'lib/minimax-adapter.js', cls: 'MinimaxAdapter', shape: 'oauth' },
  qodercn: { module: 'lib/qoder-adapter.js', cls: 'QoderAdapter', shape: 'oauth' },
};

// 共用选项：adapter 会读但允许缺省的字段。给最小实现而不是 undefined，
// 避免每个 provider 在「没有账号池」时各抛各的 TypeError。
function baseOptions(apiKey, slotOption, slotsOf) {
  const slots = slotsOf(apiKey);
  return {
    // 槽位：只有 apiKey 型 adapter 会用到
    [slotOption]: async () => slots,
    // 目录拉取：adapter 自己在远端失败时会回退静态表，这里给真 fetch
    fetchRemoteCatalog: undefined,
    // 账号池替身：只提供 adapter 会调的那几个读方法（写方法不存在 = 我们不留状态）
    accountPool: {
      findAccountIdByCredential: async () => '',
      getAvailableAccount: async () => undefined,
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
  deps = await loadDeps(process.env.FR_DSH_DEPS_ROOT);
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
    await loadModule('lib/modelscope-adapter.js');
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
export async function streamInfer({ provider, model, body, credentialRef, apiKey }) {
  const adapter = await adapterFor(provider, resolveKey({ credentialRef, apiKey }));
  const opts = openaiBodyToGenerateOptions(provider, model, body || {});
  opts.signal = new AbortController().signal;
  const chunks = [];
  for await (const c of adapter.stream(opts)) chunks.push(c);
  return { sse: chunksToSSE(model, chunks) };
}

// SidecarHubError 是本模块的错误类型（index.mjs 统一转成帧错误码）。
export class SidecarHubError extends Error {
  constructor(code, message, retryable = false) {
    super(message);
    this.code = code;
    this.retryable = retryable;
  }
}
