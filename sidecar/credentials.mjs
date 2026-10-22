// sidecar/credentials.mjs —— DSH 凭据存储的**只读**视图。
//
// ## 为什么读文件而不是调 ctx.credentials.resolve
//
// 04-sidecar.md 4.1 红线①的实现手段写的是「sidecar 内部只调用 ctx.credentials.resolve /
// describe」。在 DSH 之外没有 ctx：那个 Service 属于 cordis 容器（dsh-credentials-local），
// 要拿到它就得跑起整个 DSH 进程——那就不是 sidecar 而是 DSH 插件了（04-sidecar 1.1
// 明确否掉）。所以我们退到**更低一层但同样只读**的面：$DSH_HOME/.credentials.yaml。
//
// 实测（2026-10-22）：该文件是 YAML，顶层 `refs:` 是 CredentialRef → 字符串的映射
// （22 条），另有 `records:`（DSH 自己的结构化记录，本模块不碰）与 `version`。
//
// ## 红线（本模块的自我约束）
//
//   * 只以 O_RDONLY 打开，**没有任何写路径**——不存在 set/unset/refresh 的实现，
//     连"顺手把刷新后的 token 写回去"的私货接口都没有（红线①的要求不是"别调用"，
//     是"根本不存在"）；
//   * 不读 `jet-hub/state.json`（红线②）；
//   * 值只在本进程内存里存活一次调用，不进日志、不进错误信息、不在 health 里回显。

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// credentialsFile 解析凭据文件位置：DSH_HOME 优先，其次 ~/.local/share/dsh。
export function credentialsFile() {
  const home = process.env.DSH_HOME || path.join(process.env.HOME || os.homedir(), '.local/share/dsh');
  return process.env.FR_DSH_CREDENTIALS_FILE || path.join(home, '.credentials.yaml');
}

// unquoteYAML 处理 YAML 标量的三种形态（plain / '单引号' / "双引号"）。
// 为什么自己解而不引 yaml 包：sidecar 不带依赖，而这里只需要标量——
// flow collection / 块标量在 refs 里不会出现（实测 22 条全是标量字符串）。
function unquoteYAML(raw) {
  const s = raw.trim();
  if (s.startsWith('"')) {
    // 双引号：YAML 双引号语法与 JSON 基本一致（\n \t \" \\ \uXXXX）
    try { return JSON.parse(s); } catch { /* 落到下面的手工去引号 */ }
    return s.slice(1, s.lastIndexOf('"'));
  }
  if (s.startsWith("'")) {
    // 单引号：只有一个转义 '' → '
    const end = s.lastIndexOf("'");
    return s.slice(1, end).replaceAll("''", "'");
  }
  // plain：去掉行尾注释（" #" 之后）与尾随空白
  const hash = s.indexOf(' #');
  return (hash >= 0 ? s.slice(0, hash) : s).trim();
}

// parseRefs 只解析顶层 `refs:` 段。返回 Map<ref, value>。
//
// 为什么不整文件 parse：records 段是 DSH 的内部结构（payload/token/issuer…），
// 我们没有任何使用它的理由；少解析一段就少一段误读风险。
export function parseRefs(text) {
  const refs = new Map();
  let inRefs = false;
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim() || line.trimStart().startsWith('#')) continue;
    if (!/^\s/.test(line)) {
      // 顶层键：只有 refs 段被吸收，其余段直接结束
      inRefs = line.trimEnd() === 'refs:';
      continue;
    }
    if (!inRefs) continue;
    const m = /^\s{2}([^:#][^:]*):(?:\s(.*))?$/.exec(line);
    if (!m) continue;
    const key = m[1].trim();
    const raw = m[2] === undefined ? '' : m[2];
    refs.set(key, unquoteYAML(raw));
  }
  return refs;
}

// resolve 返回凭据值；不存在返回 null（对齐契约：空值视为不存在）。
// 每次都重新读文件：DSH 端 refresh 会原子替换该文件（withFileLock + writeFileAtomic），
// 缓存住旧值等于让 sidecar 拿着过期 token 去调用——那正是红线①想避免的竞争。
export function resolve(credentialRef) {
  if (!credentialRef) return null;
  const file = credentialsFile();
  let text;
  try {
    // 只读打开：mode 参数只在创建时生效，这里传 'r' 明确语义
    text = fs.readFileSync(file, { encoding: 'utf8', flag: 'r' });
  } catch {
    return null; // 文件不存在 = 没有凭据（不是错误，health 里也不该报红）
  }
  const refs = parseRefs(text);
  const v = refs.get(credentialRef);
  if (v === undefined || v === '') return null; // DSH 的"空值视为不存在"口径
  return v;
}

// describe 给出凭据的元数据。**不含值**。
//
// expiresAt / refreshable 恒为 null/false：DSH 的 yaml 里没有过期时间（OAuth 的
// 过期信息在 records 段的 payload 里，格式随 provider 而变；猜它比不给更糟）。
// 契约 2.2 说未知字段主进程应忽略，所以这里如实说"不知道"。
export function describe(credentialRef) {
  if (!credentialRef) return null;
  const present = resolve(credentialRef) !== null;
  if (!present && !parseRefs(safeRead()).has(credentialRef)) return null;
  // provider 从 ref 的前缀推断（DSH 的 ref 形如 BUDDY_ACCOUNT_xxx / DEEPSEEK_API_KEY）：
  // 只用于展示，不参与任何判定。
  const head = credentialRef.split('_')[0] || '';
  return {
    provider: head.toLowerCase(),
    externalLabel: credentialRef,
    expiresAt: null,
    refreshable: false,
  };
}

// hasProvider 判断某 provider 是否存在凭据引用（**不含值**）。
// DSH 的 ref 命名是 `PROVIDER_ACCOUNT_xxx` / `PROVIDER_API_KEY` / `DEEPSEEK_API_KEY` 这类，
// 所以按「前缀段匹配」判定；只用于 providers.list 的 hasAccount 展示位。
export function hasProvider(provider) {
  if (!provider) return false;
  const want = provider.toUpperCase().replace(/-/g, '_');
  for (const ref of parseRefs(safeRead()).keys()) {
    const segs = ref.toUpperCase().split('_');
    if (segs.includes(want)) return true;
    // 复合名（MINIMAXINTL、WORKBUDDY）在前缀段里
    if (ref.toUpperCase().startsWith(want + '_')) return true;
  }
  return false;
}

function safeRead() {
  try {
    return fs.readFileSync(credentialsFile(), { encoding: 'utf8', flag: 'r' });
  } catch {
    return '';
  }
}
