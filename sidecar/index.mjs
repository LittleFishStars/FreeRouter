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
//
// 关于 hub 的现实（实测 0.2.1-rc.2）：其根导出是 cordis 插件（依赖 @deepseek-ai/cordis、
// cosmokit、dsh-credentials 等 B 层包），./client 导出是浏览器模块加载格式（要 window）。
// 两者在纯 Node 里都 import 不动。所以本 sidecar 的当前形态是「契约完整、能力分级」：
// health / providers.list / login 会话簿记等不依赖 hub 的方法真实可用；
// models.list / credentials.* / credits.query / login.start 需要 hub，缺则 E_DEPENDENCY_MISSING。
// hub 一旦能在 Node 侧装载（或被移植出可独立调用的入口），只需给对应 handler 接上 hubCall。

import process from 'node:process';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import path from 'node:path';

const SIDECAR_VERSION = '0.1.0';

// —— 1. stdout 守护 ————————————————————————————————————————————
// 为什么改写 console 而不是「约定大家都用 logger」：providers-hub 及其依赖大量直接调
// console.log，一旦它们进来就把 NDJSON 帧流污染了（04-sidecar.md §8 点名的风险）。
// 在进程级把 stdout 让出来，比在几十个调用点各自自律可靠得多。
const realStdoutWrite = process.stdout.write.bind(process.stdout);
function toStderr(...args) {
  const line = args.map((a) => (typeof a === 'string' ? a : safeStringify(a))).join(' ');
  process.stderr.write(line + '\n');
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
  // modelscope 只有刷新逻辑、无交互登录（§8 待定首次凭据来源）；opencode 是粘贴 API Key。
  // 两者没有「可发起的登录流」，login.start 一律 E_NOT_IMPLEMENTED（6.1：隐藏入口）。
  { provider: 'modelscope',   displayName: 'ModelScope',       loginKind: 'plain_credential', loginImplemented: false },
  { provider: 'opencode',     displayName: 'OpenCode',         loginKind: 'plain_credential', loginImplemented: false },
  { provider: 'qoder',        displayName: 'Qoder',            loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'qodercn',      displayName: 'Qoder 国内版',      loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'raccoon',      displayName: 'Raccoon（扫码/短信）', loginKind: 'qr_scan',        loginImplemented: true },
  { provider: 'trae',         displayName: 'Trae',             loginKind: 'browser_callback', loginImplemented: true },
  { provider: 'zcode',        displayName: 'ZCode',            loginKind: 'browser_callback', loginImplemented: true },
];

// —— 3. hub 装载与 B 层依赖探测 ————————————————————————————————
// 路径来源：FR_HUB_PATH（Go 侧 Options.HubPath 注入）。留空则尝试 DSH 默认位置。
const HUB_PATH = process.env.FR_HUB_PATH
  || path.join(process.env.DSH_HOME || path.join(process.env.HOME || '', '.local/share/dsh'),
              'profiles/web/node_modules/dsh-providers-hub');

function depProbe(name) {
  // 只探测「可解析」，不执行模块 —— 执行是 hub 自己的事（且可能抛）。
  try {
    const req = createRequire(path.join(HUB_PATH, 'package.json'));
    req.resolve(name);
    return { name, ok: true, detail: '' };
  } catch (e) {
    return { name, ok: false, detail: String(e.message || e).split('\n')[0] };
  }
}

let hubState = null; // { hub, error }
async function loadHub() {
  if (hubState) return hubState;
  try {
    const mod = await import(path.join(HUB_PATH, 'lib/index.js'));
    hubState = { hub: mod, error: '' };
  } catch (e) {
    // 记住失败原因而不是每次调用重新 import 一次（import 缓存会记住 rejection，但错误文案
    // 会被 Node 包一层 ERR_MODULE_NOT_FOUND 链；这里保留第一手信息给 health/deps 用）。
    hubState = { hub: null, error: String(e.message || e).split('\n')[0] };
  }
  return hubState;
}

function requireHub(method) {
  // 同步入口给异步 handler 用：把「缺 hub」变成一次可 await 的抛出。
  return loadHub().then(({ hub, error }) => {
    if (!hub) {
      throw new SidecarError('E_DEPENDENCY_MISSING',
        `${method} 需要 dsh-providers-hub，装载失败：${error || '未知原因'}（hub_path=${HUB_PATH}）`);
    }
    return hub;
  });
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

function newSession(provider) {
  const id = `s${Date.now().toString(36)}-${++sessionSeq}`;
  const expiresAt = new Date(Date.now() + 5 * 60_000).toISOString();
  sessions.set(id, { id, provider, status: 'pending', expiresAt, created: Date.now() });
  return sessions.get(id);
}

// 惰性回收：轮询/取消时顺带扫一遍过期会话（会话数量级是「同时几个」，不值得定时器）。
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

    const deps = ['@deepseek-ai/dsh-credentials', '@deepseek-ai/cosmokit', '@deepseek-ai/schemastery']
      .map(depProbe);
    const { hub, error } = await loadHub();
    if (!hub) deps.push({ name: 'dsh-providers-hub', ok: false, detail: error });
    return {
      version: SIDECAR_VERSION,
      providers: PROVIDERS.map((p) => p.provider),
      deps,
      stateReadonly: true,
      nodeVersion: process.version,
    };
  },

  'providers.list': async () => {
    // hasAccount 需要 hub 的账号池（读凭据存储元数据）；缺 hub 时如实报 false ——
    // 它是「存在元数据」的弱断言，报 false 只影响引导，不影响正确性。
    const { hub } = await loadHub();
    return PROVIDERS.map((p) => ({ ...p, hasAccount: hub ? Boolean(hub.hasAccount?.(p.provider)) : false }));
  },

  'models.list': async ({ provider }) => {
    if (!provider) throw new SidecarError('E_BAD_REQUEST', 'provider 不能为空');
    if (!PROVIDERS.some((p) => p.provider === provider)) {
      throw new SidecarError('E_BAD_REQUEST', `未知 provider：${provider}`);
    }
    await requireHub('models.list'); // hub 就位后：return hubListModels(hub, provider)
    throw new SidecarError('E_NOT_IMPLEMENTED', 'models.list 的 hub 调用面尚未在纯 Node 下暴露（等待上游导出适配）');
  },

  'credentials.resolve': async ({ credentialRef }) => {
    if (!credentialRef) throw new SidecarError('E_BAD_REQUEST', 'credentialRef 不能为空');
    await requireHub('credentials.resolve');
    throw new SidecarError('E_NOT_IMPLEMENTED', 'credentials.resolve 的 hub 调用面尚未在纯 Node 下暴露');
  },

  'credentials.describe': async ({ credentialRef }) => {
    if (!credentialRef) throw new SidecarError('E_BAD_REQUEST', 'credentialRef 不能为空');
    await requireHub('credentials.describe');
    throw new SidecarError('E_NOT_IMPLEMENTED', 'credentials.describe 的 hub 调用面尚未在纯 Node 下暴露');
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

  'credits.query': async ({ provider, credentialRef }) => {
    if (!provider) throw new SidecarError('E_BAD_REQUEST', 'provider 不能为空');
    await requireHub('credits.query');
    void credentialRef;
    throw new SidecarError('E_NOT_IMPLEMENTED', 'credits.query 的 hub 调用面尚未在纯 Node 下暴露');
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

function safeStringify(v) {
  try { return JSON.stringify(v); } catch { return String(v); }
}

main();
