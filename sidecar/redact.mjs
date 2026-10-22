// sidecar/redact.mjs —— 日志/错误文本的敏感遮蔽。
//
// ⚠️ 同一组规则共三处，改一处必须同步其余两处：
//   1. 本文件（sidecar 自己外发的错误信息与 stderr）；
//   2. internal/upstream/dshprovider/client.go 的 RedactLine（Go 侧唯一实现）；
//   3. internal/upstream/contribution.go 的 redactText —— 转调第 2 处，不复制模式。
// Go 侧读 sidecar stderr 时会再遮一次（双层保险，幂等）。
// 两边的语义由同一组表驱动用例钉住：本目录 selftest.mjs 与 Go 的 TestContractRedactLineTable。
//
// 两条被实践修正过的设计点：
//   - **键名要有左边界**：裸 `code`/`token` 会命中 `error_code`/`status_code`，
//     把排障字段（sidecar 错误帧正是 `{code:"E_..."}` 形态）一并抹掉。
//   - **带引号的值要保留配对引号**：否则日志里的 JSON 不再可解析，排障更难受。
//
// 值什么时候不遮：snake_case 标识符（E_DEPENDENCY_MISSING / no_candidate / model_not_found）
// 是错误码而不是凭据——宁可漏遮「长成这样」的真凭据（极少见），也不抹掉「为什么失败」。

// 鉴权头：Bearer <tok> / Basic <base64(user:pass)>。
const BEARER_RE = /\bBearer\s+[A-Za-z0-9._~+/=-]{8,}/gi;
const BASIC_RE = /\bBasic\s+[A-Za-z0-9+/=]{8,}/gi;
// 裸密钥前缀（OpenAI 风格）：没有键名也要遮。
// 左边界用「非词字符」而不是 \b：\b 在「粘在字母后面」的 key 上会静默不匹配；
// 这个写法同时挡住 task-management 这类误伤。
const SK_RE = /(^|[^A-Za-z0-9_])sk-[A-Za-z0-9_-]{8,}/g;

// 敏感键名表。刻意不含 error_code/status_code 这类复合名——它们由左边界挡掉。
const KEYS =
  '(?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?code|oauth[_-]?code|authorization[_-]?code|client[_-]?secret|secret|token|code|key|password)';

// 「键 = 值 / 键: 值」。group1=左边界，group2=键与分隔符，group3=值（带引号形态整体接住）。
// 值的字符集排除引号与空白/分隔符，避免把 JSON 结构字符吞进来。
const PAIR_RE = new RegExp(
  `(^|[^A-Za-z0-9_])(${KEYS}["']?\\s*[=:]\\s*)("[^"]*"|'[^']*'|[^\\s,;&)}"']{8,})`,
  'gi',
);

// 标识符形态（至少一个下划线）——判定「这是错误码/状态名，不是凭据」。
const IDENTIFIER_RE = /^[A-Za-z][A-Za-z0-9]*(?:_[A-Za-z0-9]+)+$/;

function looksLikeIdentifier(raw) {
  const v = String(raw).replace(/^["']|["']$/g, '');
  // 长度约束与 Go 侧一致：太短本来就不会被遮，太长多半是 base64/hex（不能放过）。
  return v.length >= 8 && v.length <= 64 && IDENTIFIER_RE.test(v);
}

// redactLine 返回遮蔽后的文本（幂等：对已遮蔽的文本再跑一次不变）。
export function redactLine(line, maxLen = 2048) {
  let s = String(line ?? '');
  // 先截断再遮蔽：遮完再截可能把 *** 截掉一半，留下半截密钥的前缀。
  if (s.length > maxLen) s = s.slice(0, maxLen) + `…(+${s.length - maxLen}B)`;
  s = s.replace(BEARER_RE, 'Bearer ***').replace(BASIC_RE, 'Basic ***').replace(SK_RE, '$1sk-***');
  s = s.replace(PAIR_RE, (match, sep, key, value) => {
    if (looksLikeIdentifier(value)) return match; // 错误码/状态名：原样保留
    const quote = value[0] === '"' || value[0] === "'" ? value[0] : '';
    // 保留配对引号，保证日志里的 JSON 仍可解析。
    return `${sep}${key}${quote}***${quote}`;
  });
  return s;
}
