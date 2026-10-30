#!/usr/bin/env node
// FreeRouter sidecar —— providers-hub 的 Node 侧适配器。
//
// 契约权威：docs/architecture/04-sidecar.md 第 2 节（stdio + NDJSON，一问一答）。
// 本文件只做四件事：
//   1. 守住 stdout —— 它只准出现 NDJSON 应答帧，其余一切（含第三方库的 console.*）改道 stderr；
//   2. 按**映射表** METHODS 分派方法 —— 方法集随文档增删时只动这张表，主循环不随之膨胀；
//   3. 缺 hub / 缺 B 层依赖时返回结构化错误（E_DEPENDENCY_MISSING），**绝不崩溃** ——
//      主进程对 sidecar 的全部容错设计（5.1「挂横幅继续」）都建立在「进程活着但会说 error」上；
//   4. 守三条红线 —— METHODS 里**永远不出现** credentials.set/unset/refresh 与 state.*，
//      触碰即返回守卫码 E_CREDENTIAL_READONLY / E_STATE_FORBIDDEN 并打审计行（2.4 设计说明）。
//   5. （未来若开本地端口）只准绑 127.0.0.1、拒绝通配地址 —— OAuth 回调监听是登录流的
//      既有需求（04-sidecar.md 4.2），但 `0.0.0.0`/空主机监听会把回环信任边界打破；
//      新增监听前先重读红线③与 config 校验（validate.go 对非回环地址直接拒绝）。
// 关于 hub 的现实（2026-10-22 实测，hub 0.2.1-rc.2）：根导出是 cordis 插件（要完整
// Cordis Context），./client 是浏览器模块格式（要 window），两者都**不是**能给 Go 复用的
// 调用面。可行路径是深路径 import `lib/<provider>-adapter.js`，前提是 B 层依赖
// （@deepseek-ai/dsh-llm 等，随 DSH 安装目录走）能被解析 —— 见 deps.mjs 的解析钩子。
// 因此本 sidecar 的能力分级变成：
//   * health / providers.list / login 会话簿记：不依赖 hub，始终可用；
//   * credentials.resolve/describe：读 $DSH_HOME/.credentials.yaml（**只读**，见 credentials.mjs）；
//   * models.list / infer.stream：当前**没有可独立驱动的 apiKey 型 adapter**
//     （modelscope / opencode 已按契约 v2 §7 移除），OAuth 型需要 DSH 账号池，
//     两条路都返回结构化 E_NOT_IMPLEMENTED；apiKey 那种「粘贴 Key」的贡献形态
//     由主进程的内置目录 + 直连出站承担，不再经过 sidecar。
//   * credits.query：各家余额/积分查询（providers/credits.mjs，分发复用 hub）。
//   * credits.claim：每日签到/领取积分（同上分发表；不写凭据存储——轮换新值经
//     refreshed_credential 随应答上行，由 Go 桥写回密文，与静默续期同通道）。
// 任何路径的失败都归一成结构化错误，进程不崩（5.1「挂横幅继续」依赖这点）。

import process from 'node:process';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import path from 'node:path';
import * as hub from './hub.mjs';
import * as dshCredentials from './credentials.mjs';
import { redactLine } from './redact.mjs';
import * as registry from './providers/registry.mjs';
import * as credits from './providers/credits.mjs';
import * as modelCatalog from './providers/models-catalog.mjs';
import * as oauthSessions from './providers/sessions.mjs';

const SIDECAR_VERSION = '0.1.0';

// —— 1. stdout 守护 ————————————————————————————————————————————
// 为什么改写 console 而不是「约定大家都用 logger」：providers-hub 及其依赖大量直接调
// console.log，一旦它们进来就把 NDJSON 帧流污染了（04-sidecar.md §8 点名的风险）。
// 在进程级把 stdout 让出来，比在几十个调用点各自自律可靠得多。
const realStdoutWrite = process.stdout.write.bind(process.stdout);
function toStderr(...args) {
  // 所有 stderr 输出都过一遍遮蔽：这一路既承载我们自己的审计行，也承载第三方库
  // 经 stdout-guard 改道的内容（OAuth 授权码/token 碎片最可能从后者漏出来）。
  const line = args.map((a) => (typeof a === 'string' ? a : safeStringify(a))).join(' ');
  process.stderr.write(redactLine(line) + '\n');
}
for (const level of ['log', 'info', 'warn', 'error', 'debug', 'trace']) {
  console[level] = toStderr;
}
process.stdout.write = (...args) => {
  // 任何绕过 console 直写 stdout 的内容（典型：第三方库 banner）改道 stderr。
  // 帧本身走 realStdoutWrite，不经过这里。
  toStderr('[stdout-guard]', ...(typeof args[0] === 'string' ? [args[0]] : [args]));
  return true;
};

// —— 2. provider 目录（04-sidecar.md 6.2 分类表的代码化） ————————
// 为什么内置静态表而不是问 hub：providers.list 的超时只有 3s（5.3），纯内存操作；
// 而「登录是否已实现」是**版本属性**不是运行时状态，随文档改表即可，不需要 hub 活着。
const PROVIDERS = [
  { provider: 'accio',        displayName: 'Accio',            loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'autoclaw',     displayName: 'AutoClaw',         loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'buddy',        displayName: 'CodeBuddy',        loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'workbuddy',    displayName: 'WorkBuddy',        loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'cline',        displayName: 'Cline',            loginKind: 'device_code',      loginImplemented: true },
  // codearts：lib/ 下只有余额模块，登录实现位置未确认（§8 待定）→ 入口必须隐藏。
  { provider: 'codearts',     displayName: 'CodeArts',         loginKind: 'browser_callback', loginImplemented: false },
  { provider: 'lobsterai',    displayName: 'LobsterAI',        loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'loomy',        displayName: 'Loomy（微信扫码）',  loginKind: 'qr_scan',          loginImplemented: true },
  { provider: 'minimax',      displayName: 'MiniMax',          loginKind: 'device_code',      loginImplemented: true },
  { provider: 'minimaxintl',  displayName: 'MiniMax 国际版',     loginKind: 'device_code',      loginImplemented: true },
  { provider: 'qoder',        displayName: 'Qoder',            loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'qodercn',      displayName: 'Qoder 国内版',      loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'raccoon',      displayName: 'Raccoon（扫码/短信）', loginKind: 'qr_scan',        loginImplemented: true },
  { provider: 'trae',         displayName: 'Trae',             loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'zcode',        displayName: 'ZCode',            loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'codely',       displayName: 'Codely',           loginKind: 'device_code',      loginImplemented: true },
  { provider: 'officeace',    displayName: 'OfficeAce',        loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'astudio',      displayName: 'AStudio',          loginKind: 'qr_scan',          loginImplemented: true },
  { provider: 'autoclawintl', displayName: 'AutoClaw (国际版)', loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'gemini',       displayName: 'Gemini Code Assist', loginKind: 'browser_callback', loginImplemented: true },
  // astudio：hub 的「网页三合一快捷登录」（手机短信/账号密码/微信扫码，不
  // 依赖桌面端）——微信扫码支已接（sso.xfyun.cn 的 fd 流程纯函数面）。
  // modelscope / opencode：契约 v2 §7 已删（无 OAuth，仅目录/LLM 注册）。
];

// —— 3. hub 装载与 B 层依赖探测 ————————————————————————————————
// 路径来源：FR_HUB_PATH（Go 侧 Options.HubPath 注入）。留空则尝试 DSH 默认位置。
// 真实解析（含候选枚举）在 hub.mjs，这里只留一个名字给错误信息用。
const HUB_PATH = hub.hubPath();

// depProbe 判断某个 B 层包在**依赖根**里是否存在。
//
// 为什么按文件存在性而不是 createRequire().resolve()：实测 createRequire 从 hub 的
// package.json 起解析时**不受** registerHooks 影响（只有 ESM import 走了钩子），
// 于是 @deepseek-ai/dsh-credentials 在装了依赖的情况下仍报「找不到」——假阴性。
// 依赖根由 hub.probe() 给出（它自己就是靠 dsh-llm 定位的），存在性即结论。
function depProbe(root, name) {
  if (!root) return { name, ok: false, detail: '未定位到 DSH 依赖根' };
  const pkg = path.join(root, name, 'package.json');
  try {
    fs.accessSync(pkg, fs.constants.R_OK);
    return { name, ok: true, detail: '' };
  } catch {
    return { name, ok: false, detail: `依赖根里没有 ${name}` };
  }
}

// hubStatus 缓存装载探测结果（进程内一次）。
// 为什么缓存：probe() 会 import 十几个 adapter 模块，而 health 是 3s 超时的纯内存操作
// （5.3）——每次重跑一遍只会让 health 变慢，不会让结论更准（模块装载结果是进程常量）。
let hubStatus = null;
async function hubProbe() {
  if (!hubStatus) hubStatus = await hub.probe();
  return hubStatus;
}

// requireHub 把「hub 不可用」变成 E_DEPENDENCY_MISSING。
// 与旧实现的关键差别：**不再** import 根的 cordis 插件。它其实 import 得动，但导出的是
// `{apply(ctx)}`——「能 import」不等于「能用」，拿它当 hub 可用是 code-review 点名的
// 同类静默降级（能力缺失必须显式说）。这里以「adapter 模块 + B 层依赖都就位」为可用判据。
async function requireHub(method) {
  const st = await hubProbe();
  if (!st.hubOk) {
    throw new SidecarError('E_DEPENDENCY_MISSING',
      `${method} 需要可装载的 dsh-providers-hub adapter（hub_path=${HUB_PATH}）：${st.depsDetail || '未知原因'}`);
  }
  return st;
}

// —— 4. 错误类型与红线守卫 —————————————————————————————————————
class SidecarError extends Error {
  constructor(code, message, retryable = false) {
    super(message);
    this.code = code;
    this.retryable = retryable;
  }
}

// 红线①②的**守卫表**：这些方法不存在于 METHODS，但如果有人在版本漂移中发来了它们，
// 我们返回专用错误码 + 审计行，而不是含糊的 E_UNKNOWN_METHOD —— 二者的区别是
// 「对面以为我们有」vs「对面打错字」，前者是需要立刻响铃的事（2.4 的设计说明）。
const REDLINE_GUARDS = {
  'credentials.set': 'E_CREDENTIAL_READONLY',
  'credentials.unset': 'E_CREDENTIAL_READONLY',
  'credentials.refresh': 'E_CREDENTIAL_READONLY',
  'credentials.write': 'E_CREDENTIAL_READONLY',
  'credentials.delete': 'E_CREDENTIAL_READONLY',
  'state.read': 'E_STATE_FORBIDDEN',
  'state.write': 'E_STATE_FORBIDDEN',
  'state.update': 'E_STATE_FORBIDDEN',
};

// —— 5. 登录会话簿记 ———————————————————————————————————————————
// 为什么 sidecar 必须自己管会话：5.3 明说超时后主进程**无法中断** Node 里的登录流，
// 所以会话生命周期归 sidecar，带 expiresAt 自清理，否则会泄漏（连同回环监听端口）。
const sessions = new Map();
let sessionSeq = 0;

// 会话默认寿命：5 分钟。
// 出处依据（04-sidecar.md 5.3）：单次 Call 的超时（login.* 10s）只决定「主进程等多久」，
// 会话寿命归 sidecar 自管——用户输码/扫码/浏览器回调远慢于任何一次 RPC，
// 所以 TTL 必须 ≫ 10s；5 分钟与 login.start 应答里的 expiresAt 字段同源，
// 避免出现「帧里的过期时间还没到、会话已被回收」的自相矛盾。
// FR_LOGIN_SESSION_TTL_SEC 可覆盖（秒），非法值或 ≤0 回落默认——仅供测试与特殊部署。
const SESSION_TTL_MS = (() => {
  const v = Number(process.env.FR_LOGIN_SESSION_TTL_SEC || 300);
  return Number.isFinite(v) && v > 0 ? v * 1000 : 300_000;
})();

function newSession(provider) {
  const id = `s${Date.now().toString(36)}-${++sessionSeq}`;
  const expiresAt = new Date(Date.now() + SESSION_TTL_MS).toISOString();
  sessions.set(id, { id, provider, status: 'pending', expiresAt, created: Date.now() });
  return sessions.get(id);
}

// 惰性回收：轮询/取消时顺带扫一遍过期会话（会话数量级是「同时几个」，不值得定时器）。
// 终态取舍：status=ok/failed/expired 的会话也一并删——E_LOGIN_TIMEOUT 的既定恢复路径是
// 「重新 login.start」（2.4），不存在对旧终态的二次确认需求；若未来 Go 侧需要终态审计，
// 从删除前的 stderr 审计行里找，而不是指望这里保留（内存里的会话簿记不是账本）。
function reapExpired() {
  const now = Date.now();
  for (const [id, s] of sessions) {
    if (s.status === 'ok' || s.status === 'failed' || s.status === 'expired' || now > Date.parse(s.expiresAt)) {
      sessions.delete(id);
    }
  }
}

// —— 6. 方法映射表（唯一的分派点） ——————————————————————————————
// 增删方法只改这张表；主循环、错误归一、守卫都不动。shutdown 也在表里 ——
// 它不是业务方法（契约 5.4 的关闭帧），但用同一通道表达最省事。
const METHODS = {
  health: async () => {
    // state.json 只取 mtime 做审计基线（红线②禁止**读写内容**；stat 不触碰内容，
    // 且 4.1 的检测手段就是「启动时记录 mtime，运行期变化即告警」）。
    const dshHome = process.env.DSH_HOME || path.join(process.env.HOME || '', '.local/share/dsh');
    const stateFile = path.join(dshHome, 'jet-hub/state.json');
    try {
      const st = fs.statSync(stateFile);
      toStderr(`[audit] state.json mtime=${st.mtime.toISOString()}（本进程绝不读写其内容，红线②）`);
    } catch { /* 不存在也不算错，health 的语义是「我活着」 */ }

    // 顺序有讲究：**先**让 hubProbe() 装上依赖解析钩子，再 depProbe()。
    // 反过来的话 createRequire 走的是未打补丁的解析器，B 层包一律报「找不到」——
    // 实测就是这种假阴性（依赖其实在 DSH 安装目录里）。
    const st = await hubProbe();
    const deps = ['@deepseek-ai/dsh-credentials', '@deepseek-ai/dsh-llm', '@deepseek-ai/cordis',
      '@deepseek-ai/cosmokit', '@deepseek-ai/schemastery']
      .map((name) => depProbe(st.depRoot, name));
    deps.push({
      name: 'dsh-providers-hub',
      ok: st.hubOk,
      detail: st.hubOk ? `依赖根=${st.depRoot || '(自动)'}` : (st.depsDetail || '装载失败'),
    });
    return {
      version: SIDECAR_VERSION,
      providers: PROVIDERS.map((p) => p.provider),
      deps,
      // hubAdapters 让 Go 侧一眼看到「哪几家 adapter 模块在位、哪几家需要账号池」，
      // 而不是靠试调用去猜（04-sidecar 3 节的同一原则）。
      hubAdapters: st.adapters,
      stateReadonly: true,
      nodeVersion: process.version,
    };
  },

  'providers.list': async () => {
    // hasAccount：有 DSH 凭据引用即视为「存在账号元数据」（不含值）。
    // 为什么不去 hub 里问账号池：池是 DSH 运行态，sidecar 没有它；凭据文件是同一份事实的
    // 持久形态，且这个字段只影响引导（报错也不会造成错误调用）。
    const st = await hubProbe();
    const adapters = new Map((st.adapters || []).map((a) => [a.provider, a]));
    return PROVIDERS.map((p) => {
      const a = adapters.get(p.provider);
      return {
        ...p,
        // 契约字段：hasAccount 只表示「存在元数据」，绝不含凭据值
        hasAccount: dshCredentials.hasProvider(p.provider),
        // 附加能力位（未知字段主进程应忽略，契约 3）：adapter 是否可独立驱动
        adapterAvailable: Boolean(a && a.ok),
        adapterShape: a ? a.shape : '',
        adapterDetail: a && !a.ok ? (a.detail || '') : '',
      };
    });
  },

  'models.list': async ({ provider, credentialRef, apiKey }) => {
    if (!provider) throw new SidecarError('E_BAD_REQUEST', 'provider 不能为空');
    if (!PROVIDERS.some((p) => p.provider === provider)) {
      throw new SidecarError('E_BAD_REQUEST', `未知 provider：${provider}`);
    }
    await requireHub('models.list');
    try {
      return await hub.listModels({ provider, credentialRef, apiKey });
    } catch (e) {
      throw asSidecarError(e, 'models.list');
    }
  },

  'credentials.resolve': async ({ credentialRef }) => {
    if (!credentialRef) throw new SidecarError('E_BAD_REQUEST', 'credentialRef 不能为空');
    // 只读：读 $DSH_HOME/.credentials.yaml（见 credentials.mjs 的红线段落）。
    // 返回 null 表示「不存在」——对齐 DSH 的空值口径（契约 2.2）。
    const value = dshCredentials.resolve(credentialRef);
    if (value === null) return null;
    return { value, source: 'dsh-credentials-yaml' };
  },

  'credentials.describe': async ({ credentialRef }) => {
    if (!credentialRef) throw new SidecarError('E_BAD_REQUEST', 'credentialRef 不能为空');
    const d = dshCredentials.describe(credentialRef);
    if (!d) throw new SidecarError('E_CREDENTIAL_MISSING', `凭据引用不存在：${credentialRef}`);
    return d; // 不含值（契约 2.2：describe 只给元数据）
  },

  'login.start': async ({ provider }) => {
    if (!provider) throw new SidecarError('E_BAD_REQUEST', 'provider 不能为空');
    const p = PROVIDERS.find((x) => x.provider === provider);
    if (!p) throw new SidecarError('E_BAD_REQUEST', `未知 provider：${provider}`);
    if (!p.loginImplemented) {
      // 6.1：控制台要隐藏入口，而不是给用户一个必失败的按钮。
      throw new SidecarError('E_NOT_IMPLEMENTED', `${provider} 的登录流未实现`);
    }
    await requireHub('login.start'); // 真实登录流在 hub 里，缺它必失败 —— 先探再建会话，
    const s = newSession(provider);  // 免得攒一堆永远不可能完成的会话。
    return {
      sessionId: s.id, kind: p.loginKind, instructions: `请在 DSH 侧完成 ${provider} 登录后由 hub 接管`,
      verificationUrl: null, userCode: null, qrData: null, callbackPort: null, expiresAt: s.expiresAt,
    };
  },

  'login.poll': async ({ sessionId }) => {
    if (!sessionId) throw new SidecarError('E_BAD_REQUEST', 'sessionId 不能为空');
    reapExpired();
    const s = sessions.get(sessionId);
    if (!s) return { status: 'expired', credentialRef: null, secretRef: null, detail: '会话不存在或已过期' };
    return { status: s.status, credentialRef: s.credentialRef ?? null, secretRef: s.secretRef ?? null, detail: s.detail ?? null };
  },

  'login.submit': async ({ sessionId, input }) => {
    if (!sessionId) throw new SidecarError('E_BAD_REQUEST', 'sessionId 不能为空');
    reapExpired();
    const s = sessions.get(sessionId);
    if (!s) throw new SidecarError('E_LOGIN_TIMEOUT', '会话不存在或已过期，请重新 login.start', true);
    if (s.status !== 'awaiting_input') throw new SidecarError('E_SUBMIT_REJECTED', `当前状态 ${s.status} 不接受输入`, true);
    void input; // 校验在 hub 的短信流里；缺 hub 到不了这里。
    s.status = 'pending';
    return { status: s.status };
  },

  'login.cancel': async ({ sessionId }) => {
    if (!sessionId) throw new SidecarError('E_BAD_REQUEST', 'sessionId 不能为空');
    sessions.delete(sessionId); // 不关心会话是否存在：取消语义就是「确保它不在」。
    return {};
  },

  // 反代供应商的静态模型目录（hub 各家 product 的 fallbackModels，零拷贝）。
  // 贡献 settle 后与目录刷新都调这里——反代型没有 OpenAI 兼容 /models 可探测。
  'models.catalog': async ({ provider }) => {
    if (!provider) throw new SidecarError('E_BAD_REQUEST', 'provider 不能为空')
    return { models: await modelCatalog.modelCatalog(provider) }
  },

  // 凭据余额/积分查询（只读）：管理面凭据池的「刷新余额」按钮调这里。
  // credential 是已解密的明文凭据对象（与 oauth.exchange 的产出同形——
  // 授权时 credential 本就以明文 JSON 过这条 stdio 边界，红线一致）。
  // 支持面见 providers/credits.mjs 的 QUERY_BY_PROVIDER（raccoon/loomy/minimax）。
  'credits.query': async ({ provider, credential }) => {
    if (!provider) throw new SidecarError('E_BAD_REQUEST', 'provider 不能为空');
    if (!credential) throw new SidecarError('E_BAD_REQUEST', 'credential 不能为空');
    try {
      return await credits.queryCreditBalance(provider, credential);
    } catch (e) {
      if (e instanceof SidecarError) throw e;
      throw new SidecarError('E_INTERNAL', `余额查询失败：${String(e && e.message ? e.message : e)}`, false);
    }
  },

  // 每日签到/领取积分（写上游业务，**不写凭据存储**）：管理面「签到」按钮与
  // 定时签到调度器调这里。credential 同 credits.query 的二分（反代型 JSON
  // 对象 / API 型裸 key——后者直接报 unsupported，没有签到活动）。
  // 轮换出的新凭据（autoclaw 领取可能换 refresh_token）放进 refreshed_credential
  // 随应答上行，由 Go 桥原地写回密文 —— sidecar 自己不落盘（红线①）。
  'credits.claim': async ({ provider, credential }) => {
    if (!provider) throw new SidecarError('E_BAD_REQUEST', 'provider 不能为空');
    if (!credential) throw new SidecarError('E_BAD_REQUEST', 'credential 不能为空');
    try {
      return await credits.claimCreditBalance(provider, credential);
    } catch (e) {
      if (e instanceof SidecarError) throw e;
      throw new SidecarError('E_INTERNAL', `签到失败：${String(e && e.message ? e.message : e)}`, false);
    }
  },

  // —— 供应商 OAuth 贡献（docs/contributions/01-flow.md）————————————
  // 为什么由 sidecar 承担：PLAN §5.5 定案「17 家供应商的 OAuth 与静默续期不可能
  // 重写」。15 家的实现已按提取计划移植进 sidecar/providers/，由 registry.mjs
  // 收敛成三种交互形态（browser / device_code / sms|qr）。
  //
  // 关键：轮询一律放**后台会话**（providers/sessions.mjs）——上游的 pollXxx 是
  // 阻塞循环，直接调用会卡住本通道（Call 超时只有 10 秒）。
  'oauth.list': async () => {
    // 主进程据此渲染入口与「不可用原因」。
    return {
      providers: registry.available(),
      api_key_only: registry.API_KEY_ONLY,
    }
  },

  // 发起授权。redirect 形态返回 {authorize_url, state}；session 形态（设备码/
  // 二维码/短信）返回 {session_id, authorize_url, user_code, ...} 并已在后台轮询。
  'oauth.authorize': async ({ provider, port, phone, captcha_param }) => {
    const entry = registry.get(provider)
    if (!entry) {
      throw new SidecarError('E_NOT_IMPLEMENTED',
        `供应商 ${provider} 的 OAuth 未接入（可改用 API Key 型贡献）`)
    }
    // 配置缺失（如未设 OAuth 客户端 secret）：报未配置而不是发一个必然失败的请求。
    if (entry.unavailable) {
      throw new SidecarError('E_NOT_CONFIGURED', entry.unavailable)
    }
    try {
      if (typeof entry.start === 'function') {
        // captcha_param 是阿里云滑块的产物（Raccoon 短信登录必需），原样透传。
        const s = await entry.start({ port, phone, captchaParam: captcha_param })
        return { ...s, mode: 'session' }
      }
      const r = await entry.authorize({ port, phone })
      return { ...r, mode: 'redirect' }
    } catch (e) {
      if (e instanceof SidecarError) throw e
      throw new SidecarError('E_OAUTH_AUTHORIZE_FAILED',
        String(e && e.message ? e.message : e), false)
    }
  },

  // 回调换取凭据（仅 browser + redirect 形态：主进程收到 code 后调）。
  'oauth.exchange': async ({ provider, code, state }) => {
    const entry = registry.get(provider)
    if (!entry) {
      throw new SidecarError('E_NOT_IMPLEMENTED', `供应商 ${provider} 的 OAuth 未接入`)
    }
    if (typeof entry.exchange !== 'function') {
      throw new SidecarError('E_BAD_REQUEST', `${provider} 不走回调换取（它是轮询型流程）`)
    }
    try {
      return await entry.exchange({ code, state })
    } catch (e) {
      // 换取失败几乎都是上游给的「可执行原因」（授权码过期/已被用过/回调地址
      // 不匹配）。用专用码 + 原文上抛，界面才有话说。
      throw new SidecarError('E_OAUTH_EXCHANGE_FAILED',
        String(e && e.message ? e.message : e), false)
    }
  },

  // 查一次后台会话状态（非阻塞）。status: pending | ok | failed | expired | cancelled
  'oauth.poll': async ({ provider, sessionId, code }) => {
    if (!sessionId) throw new SidecarError('E_BAD_REQUEST', 'sessionId 不能为空')
    const s = oauthSessions.get(sessionId)
    if (!s) {
      return { status: 'expired', message: '授权会话不存在或已过期，请重新发起' }
    }
    if (provider && s.provider !== provider) {
      throw new SidecarError('E_BAD_REQUEST', '会话与 provider 不匹配')
    }
    // 短信形态：用户回填验证码后才真正开始登录。
    if (typeof code === 'string' && code.length > 0) {
      const entry = registry.get(s.provider)
      if (s.status === 'pending' && typeof entry?.submit === 'function' && !s.__submitted) {
        s.__submitted = true
        oauthSessions.attachRunner(sessionId, async () =>
          entry.submit({ code, state: JSON.stringify(s.material ?? {}) }))
      }
    }
    if (s.status === 'failed') {
      return { status: 'failed', message: s.error ?? '授权失败' }
    }
    if (s.status === 'ok' && s.credential !== null) {
      const cred = typeof s.credential === 'string' ? s.credential : JSON.stringify(s.credential)
      const expires = s.credential?.expires_at ?? s.credential?.expiresAt ?? ''
      return { status: 'ok', credential: cred, expires_at: String(expires) }
    }
    return oauthSessions.snapshot(s)
  },

  'oauth.cancel': async ({ sessionId }) => {
    if (sessionId) oauthSessions.cancel(sessionId) // 幂等：不存在也算取消成功
    return {}
  },

  // 静默续期（调度器调用）。未接 refresh 的 provider 报未实现而不是静默失败。
  'oauth.refresh': async ({ provider, credential }) => {
    const entry = registry.get(provider)
    if (!entry) {
      throw new SidecarError('E_NOT_IMPLEMENTED', `供应商 ${provider} 的 OAuth 未接入`)
    }
    if (typeof entry.refresh !== 'function') {
      throw new SidecarError('E_NOT_IMPLEMENTED', `${provider} 不支持静默续期（上游无 refresh 端点）`)
    }
    try {
      return await entry.refresh({ credential })
    } catch (e) {
      if (e && e.name === 'RefreshTokenExpiredError') {
        throw new SidecarError('E_REFRESH_TOKEN_EXPIRED', String(e.message), false)
      }
      throw new SidecarError('E_OAUTH_REFRESH_FAILED',
        String(e && e.message ? e.message : e), false)
    }
  },
  // —— 推理方法（03-upstream.md §3.1：relay 的生成请求必须经 provider adapter）——
  // 调用形状：{ provider, model, body, stream, credentialRef?, apiKey? }
  //   * body 是候选方言已编码的请求体（Go 侧 openai.Adapter 产出）；
  //   * 凭据二选一：credentialRef 指向 DSH 的只读凭据存储；apiKey 是 FreeRouter 自有
  //     凭据（Go 侧用主密钥解密后单帧传入）。两者都在本进程内就地消费，不进日志、
  //     不进应答、不写回任何存储（红线①）。
  // 应答：{ sse: "<OpenAI chat.completion.chunk 的 SSE 全文>" }——relay.go 的消费面
  //      是「OpenAI 形态流原文」，这样 Go 侧 StreamReader 一行都不用改。
  'infer.stream': async ({ provider, model, body, stream, credentialRef, apiKey }) => {
    if (!provider) throw new SidecarError('E_BAD_REQUEST', 'provider 不能为空');
    if (!model) throw new SidecarError('E_BAD_REQUEST', 'model 不能为空');
    if (body == null || typeof body !== 'object') {
      throw new SidecarError('E_BAD_REQUEST', 'body 必须是请求体对象（候选方言已编码的 JSON）');
    }
    await requireHub('infer.stream');
    void stream; // 流式与否已编码在 body 里；adapter 自己决定线上形态
    try {
      return await hub.streamInfer({ provider, model, body, credentialRef, apiKey });
    } catch (e) {
      throw asSidecarError(e, 'infer.stream');
    }
  },
  shutdown: async () => {
    // 5.4 步骤 4：主动清理登录会话（回环监听、定时器）后退出，让主进程不用 SIGKILL。
    sessions.clear();
    setImmediate(() => process.exit(0));
    return {};
  },
};

// —— 7. 主循环 —————————————————————————————————————————————————
// 每行一帧；应答永远回填原 id；任何异常都归一成 ok:false 帧，进程不崩。
function frame(id, payload) {
  realStdoutWrite(JSON.stringify({ id, ...payload }) + '\n');
}

async function handleLine(line) {
  let req;
  try {
    req = JSON.parse(line);
  } catch {
    // 入站方向的脏行不可能有 id 可回填，唯一正确的动作是记 stderr 丢弃。
    toStderr('[bad-frame]', line.slice(0, 200));
    return;
  }
  const { id, method, params } = req ?? {};
  if (typeof id !== 'number' || typeof method !== 'string') {
    toStderr('[bad-frame] 缺少 id/method', line.slice(0, 200));
    return;
  }
  if (REDLINE_GUARDS[method]) {
    // 审计行是红线检测的一部分（4.1 表格「违反的检测」列），运维 grep 'REDLINE' 即可。
    toStderr(`[REDLINE] 违规方法被拒：${method}（${REDLINE_GUARDS[method]}）`);
    return frame(id, { ok: false, error: { code: REDLINE_GUARDS[method], message: `该方法违反红线，永远不存在：${method}`, retryable: false } });
  }
  const handler = METHODS[method];
  if (!handler) {
    // 未知方法与守卫码的区分本身就携带信息（版本漂移 vs 打错字）。
    return frame(id, { ok: false, error: { code: 'E_UNKNOWN_METHOD', message: `未知方法：${method}`, retryable: false } });
  }
  try {
    const result = await handler(params ?? {});
    frame(id, { ok: true, result });
  } catch (e) {
    if (e instanceof SidecarError) {
      return frame(id, { ok: false, error: { code: e.code, message: e.message, retryable: Boolean(e.retryable) } });
    }
    // handler 里的意外异常按 E_INTERNAL 收口 —— 契约 3：未知错误按 E_INTERNAL 处理，
    // 让「sidecar 内部炸了」永远不至于炸掉通道，也永远给得出一个能显示的字符串。
    toStderr('[internal]', e && e.stack ? e.stack : String(e));
    frame(id, { ok: false, error: { code: 'E_INTERNAL', message: String((e && e.message) || e), retryable: false } });
  }
}

function main() {
  process.stdin.setEncoding('utf8');
  let buf = '';
  // 串行处理：契约是一问一答，但我们**不依赖**对面守约 —— 即便主进程并发发来多行，
  // 这里也用链式队列保证「处理完一个才读下一个」，两端的串行假设互为保险。
  let chain = Promise.resolve();
  process.stdin.on('data', (chunk) => {
    buf += chunk;
    let idx;
    while ((idx = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, idx);
      buf = buf.slice(idx + 1);
      if (line.trim().length > 0) chain = chain.then(() => handleLine(line));
    }
  });
  process.stdin.on('end', () => process.exit(0));
  process.on('uncaughtException', (e) => toStderr('[uncaught]', e && e.stack ? e.stack : String(e)));
  process.on('unhandledRejection', (e) => toStderr('[unhandled]', String(e)));
}

// asSidecarError 把 hub.mjs 抛的错误翻成契约错误码。
// 关键点：**不把原始异常文本原样吐给 Go**——adapter 的错误信息可能带上游响应片段
// （含 key 前缀），那正是 02-credentials §6.4「错误信息要脱敏」要拦的。这里只保留
// 第一行并做一次保守遮蔽（token/secret/authorization 形态）。
function asSidecarError(e, method) {
  if (e instanceof SidecarError) return e;
  if (e && e.code === 'E_NOT_IMPLEMENTED') return new SidecarError('E_NOT_IMPLEMENTED', String(e.message || e));
  if (e && e.code === 'E_BAD_REQUEST') return new SidecarError('E_BAD_REQUEST', String(e.message || e));
  const first = redactSensitive(String((e && e.message) || e).split('\n')[0]);
  return new SidecarError('E_INTERNAL', `${method} 失败：${first}`,
    // 上游 429/超时类（adapter 的 LlmError 用 code 表达）值得重试一次
    /rate|timeout|429/i.test(first));
}

// 错误文本外发前统一走 redactLine（模式定义在 redact.mjs，与 Go 侧同组）。
const redactSensitive = redactLine;

function safeStringify(v) {
  // 为什么单独处理 Error：第三方库常把 Error 直接丢进 console.*，JSON.stringify(new Error('x'))
  // 是 "{}"——排查时看到空对象比看到堆栈糟得多。
  if (v instanceof Error) return v.stack || `${v.name}: ${v.message}`;
  try {
    return JSON.stringify(v);
  } catch {
    // 循环引用 / BigInt：回退到 String()，保证日志里至少有一个可读形态
    return String(v);
  }
}

main();
