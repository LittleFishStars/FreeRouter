// sidecar/deps.mjs —— B 层依赖（@deepseek-ai/*）的装载方案。
//
// ## 为什么需要这个文件（实测，见 README「hub 装载实测」）
//
// dsh-providers-hub 0.2.1-rc.2 的 package.json 只导出 "." 与 "./client"：
//   - "."     → lib/index.js，是 cordis 插件（需要 @deepseek-ai/cordis 的完整 Context）；
//   - "./client" → lib/client/jet-hub.js，是浏览器模块加载格式（顶层用 window）。
// 深路径 `lib/<provider>-adapter.js` 能 import —— **前提**是 B 层包能解析：每个 adapter
// 都 `import { LlmAdapter } from '@deepseek-ai/dsh-llm'`。而这些包**不在 profile 的
// node_modules 里**，它们随 DSH 安装目录走（实测 305 个包都在
// /usr/lib/node_modules/@deepseek-ai/dsh/node_modules/@deepseek-ai/）。
//
// ESM 不认 NODE_PATH（也不认 NODE_OPTIONS 里的旧式 require paths），hub 自己的
// node_modules/@deepseek-ai 是个空目录。所以可行的做法只有两条：
//   1. 在 hub 目录里建符号链接 —— 会写只读参考目录（红线：hub 只读）；
//   2. 用 Node 的模块解析钩子，把 @deepseek-ai/* 的解析起点改到一个**我们自己的
//      shim 目录**（里面是指向 DSH 依赖目录的符号链接），让 Node 用原生 exports 解析。
// 本文件实现第 2 条。选它的理由：不改 hub、不改 DSH 安装、不重写 exports 匹配规则
// （schemastery 的 exports 有 import/require 双条件，手写匹配迟早漂移）。
//
// ## 稳定性
//
// 钩子只拦 `@deepseek-ai/` 前缀的裸说明符，其余一律 `nextResolve` 透传；
// shim 里的链接每次启动按「目标是否指向当前 DSH 依赖根」幂等重建，升级 DSH 后自动跟上。

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// 已知 DSH 安装位的候选（顺序即优先级）。FR_DSH_DEPS_ROOT 永远优先（部署可覆盖）。
// 为什么要枚举这么多：node 可能是 nvm/volta 装的（execPath 指向 ~/.local/share/nvm/...），
// 而 DSH 可能是系统 npm 装的（/usr/lib/node_modules）——两者不在同一棵树下。
// 实测本机就是这种组合：node 在 nvm，dsh 在 /usr/lib。
function depRootCandidates() {
  const out = [];
  if (process.env.FR_DSH_DEPS_ROOT) out.push(process.env.FR_DSH_DEPS_ROOT);
  const execDir = path.dirname(process.execPath);
  const prefix = path.join(execDir, '..');
  out.push(path.join(prefix, 'lib', 'node_modules', '@deepseek-ai', 'dsh', 'node_modules'));
  out.push(path.join(prefix, 'node_modules', '@deepseek-ai', 'dsh', 'node_modules'));
  // PATH 里的 dsh 可执行文件是最终权威：它就在 DSH 安装树里（/usr/bin/dsh →
  // /usr/lib/node_modules/@deepseek-ai/dsh/lib/bin.js），顺着它找依赖根。
  for (const dir of (process.env.PATH || '').split(path.delimiter)) {
    if (!dir) continue;
    const exe = path.join(dir, 'dsh');
    try {
      const real = fs.realpathSync(exe);
      // …/@deepseek-ai/dsh/lib/bin.js → 上三级是 dsh 包根
      const pkgRoot = path.resolve(path.dirname(real), '..', '..');
      if (fs.existsSync(path.join(pkgRoot, 'package.json'))) {
        out.push(path.join(pkgRoot, 'node_modules'));
      }
    } catch { /* 该目录没有 dsh：继续 */ }
  }
  // 系统级安装位（不依赖 PATH 顺序）
  for (const p of ['/usr/lib/node_modules', '/usr/local/lib/node_modules']) {
    out.push(path.join(p, '@deepseek-ai', 'dsh', 'node_modules'));
  }
  // 常见用户级安装
  out.push(path.join(os.homedir(), '.local', 'lib', 'node_modules', '@deepseek-ai', 'dsh', 'node_modules'));
  out.push(path.join(os.homedir(), '.npm-global', 'lib', 'node_modules', '@deepseek-ai', 'dsh', 'node_modules'));
  return out;
}

// resolveDepsRoot 找出真正含有 @deepseek-ai/dsh-llm 的那个依赖根。
// 探测 dsh-llm 而不是随便一个包：它是所有 adapter 的硬依赖，缺它整套装载必失败。
export function resolveDepsRoot(hint) {
  const cands = hint ? [hint, ...depRootCandidates()] : depRootCandidates();
  for (const base of cands) {
    if (!base) continue;
    const dir = path.join(base, '@deepseek-ai');
    if (fs.existsSync(path.join(dir, 'dsh-llm', 'package.json'))) return base;
    // 也接受直接给到 `@deepseek-ai` 这一层的路径
    if (path.basename(base) === '@deepseek-ai' && fs.existsSync(path.join(base, 'dsh-llm', 'package.json'))) {
      return path.dirname(base);
    }
  }
  return '';
}

// shimDir 决定符号链接农场放哪。为什么首选临时目录而不是缓存目录：
// 本机沙箱/只读工作目录下 ~/.cache 可能不可写（实测 mkdir ENOENT），而 /tmp 总是可用；
// shim 只是符号链接，丢了重建成本极低，不值得为它挡启动。
function shimDir() {
  if (process.env.FR_SIDECAR_SHIM_DIR) return process.env.FR_SIDECAR_SHIM_DIR;
  return path.join(os.tmpdir(), `freerouter-sidecar-${process.getuid?.() ?? 'u'}`, 'deps-shim');
}

// buildShim 建（或刷新）shim 目录：node_modules/@deepseek-ai/<pkg> → 真实包目录。
// 幂等：已存在且指向同一目标的链接跳过；目标变了（DSH 升级换了路径）就重建。
export function buildShim(depsRoot, hubRoot = '') {
  const dir = shimDir();
  const pkgRoot = path.join(dir, 'node_modules', '@deepseek-ai');
  fs.mkdirSync(pkgRoot, { recursive: true, mode: 0o700 });
  const src = path.join(depsRoot, '@deepseek-ai');
  let linked = 0;
  for (const name of fs.readdirSync(src)) {
    const target = path.join(src, name);
    if (!fs.existsSync(path.join(target, 'package.json'))) continue; // 跳过非包目录
    const link = path.join(pkgRoot, name);
    try {
      const cur = fs.readlinkSync(link);
      if (cur === target) continue;
      fs.unlinkSync(link);
    } catch { /* 不存在或不是链接：下面重建 */ }
    fs.symlinkSync(target, link, 'dir');
    linked++;
  }
  // 解析起点：一个空的 .mjs，Node 从它所在的 node_modules 往上找包。
  const entry = path.join(dir, '__entry.mjs');
  if (!fs.existsSync(entry)) fs.writeFileSync(entry, '// FreeRouter sidecar 依赖 shim 的解析起点（内容无意义）\n');

  // ── hub lib 实体复制（凭据余额查询的依赖，2026-10-28）──
  // hubRoot 的 lib/ 整体复制进 shim（dsh-providers-hub/lib/…），它依赖的
  // 平铺 npm 包从 hub 的上级 node_modules 逐个链。为什么实体复制而不是
  // symlink：hub lib 装在 profile 的 node_modules 下，symlink 解析后 Node
  // 从真实路径向上找包，找不到 shim 里链的 dsh-llm（实测 ERR_MODULE_NOT_FOUND）。
  if (hubRoot) {
    const hubLibSrc = path.join(hubRoot, 'lib');
    const hubDest = path.join(dir, 'node_modules', 'dsh-providers-hub');
    if (fs.existsSync(path.join(hubLibSrc, 'provider-credits.js'))) {
      fs.mkdirSync(hubDest, { recursive: true });
      // 版本没变就跳过（幂等）：用 lib 目录的文件数近似判同
      const marker = path.join(hubDest, '.copied-marker');
      const srcCount = fs.readdirSync(hubLibSrc).length;
      let skip = false;
      try {
        if (fs.readFileSync(marker, 'utf8') === String(srcCount)) skip = true;
      } catch { /* 首次复制 */ }
      if (!skip) {
        fs.rmSync(path.join(hubDest, 'lib'), { recursive: true, force: true });
        fs.cpSync(hubLibSrc, path.join(hubDest, 'lib'), { recursive: true });
        // hub 的平铺 npm 依赖（cosmokit/schemastery/…）逐个链（存在才链）
        // hubRoot 是 hub 包目录（…/node_modules/dsh-providers-hub）——平铺依赖在它
        // 的上级 node_modules（上级的上级再上一级：profiles/web/node_modules）。
        const hubNm = path.dirname(path.dirname(hubRoot));
        if (fs.existsSync(hubNm)) {
          for (const name of fs.readdirSync(hubNm)) {
            if (name === 'dsh-providers-hub' || name.startsWith('.')) continue;
            const target = path.join(hubNm, name);
            const link = path.join(dir, 'node_modules', name);
            if (fs.existsSync(link)) continue; // 已有（@deepseek-ai 循环或别的源）不覆盖
            try { fs.symlinkSync(target, link, 'dir'); } catch { /* 已存在 */ }
          }
        }
        fs.writeFileSync(marker, String(srcCount));
      }
    }
  }

  return { dir, entry, linked };
}

let installed = false;

// installResolver 安装 ESM 解析钩子。registerHooks 是同步版（Node ≥ 22.15），
// 在主线程里就地生效；老版本回落到异步的 module.register（需要单独的 hook 文件）。
export async function installResolver(entryPath) {
  if (installed) return { mode: 'already' };
  process.env.FR_SIDECAR_SHIM_ENTRY = entryPath;
  const mod = await import('node:module');
  const here = new URL('./resolve-hook.mjs', import.meta.url);
  if (typeof mod.registerHooks === 'function') {
    mod.registerHooks({
      // 只改 @deepseek-ai/* 的解析起点；其余原样透传（副作用最小化）。
      resolve(specifier, context, nextResolve) {
        // dsh-providers-hub 的深路径（credits 分发器）也走 shim 解析起点：
        // hub lib 是实体复制进 shim 的，它的裸包依赖从 shim 的 node_modules 找。
        if (specifier.startsWith('@deepseek-ai/') || specifier.startsWith('dsh-providers-hub/')) {
          return nextResolve(specifier, { ...context, parentURL: pathToFileURL(entryPath).href });
        }
        return nextResolve(specifier, context);
      },
    });
    installed = true;
    return { mode: 'registerHooks' };
  }
  mod.register(here, import.meta.url);
  installed = true;
  return { mode: 'register' };
}

// loadDeps 是给 hub.mjs 的唯一入口：解析根 → 建 shim → 装钩子。
// 返回结构化结果而不是抛错：health 要把「依赖缺了」当成一条可展示的状态（04-sidecar §2.4）。
export async function loadDeps(hint, hubRoot = '') {
  const root = resolveDepsRoot(hint);
  if (!root) {
    return { ok: false, detail: '找不到 DSH 的 @deepseek-ai 依赖根（可用 FR_DSH_DEPS_ROOT 指定）', root: '' };
  }
  let shim;
  try {
    shim = buildShim(root, hubRoot);
  } catch (e) {
    return { ok: false, detail: `建依赖 shim 失败: ${String(e.message || e).split('\n')[0]}`, root };
  }
  try {
    const mode = await installResolver(shim.entry);
    return { ok: true, root, shimDir: shim.dir, entry: shim.entry, linked: shim.linked, mode: mode.mode };
  } catch (e) {
    return { ok: false, detail: `安装解析钩子失败: ${String(e.message || e).split('\n')[0]}`, root };
  }
}
