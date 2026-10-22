// sidecar/redact.mjs —— 日志/错误文本的敏感遮蔽。
//
// 为什么单独一个模块：sidecar 的两处出口都要遮蔽——(1) 错误信息回给 Go 前的第一行；
// (2) 第三方库经 stdout-guard 改道 stderr 的内容。同一套模式写在两处必然漂移，
// 而漂移的表现是「漏遮一处而没人发现」，所以模式只此一份。
//
// 与 Go 侧 internal/upstream/dshprovider 的 redactLine 是**同一组模式**的两端实现：
// Go 侧遮 sidecar stderr 进主进程日志，这里遮 sidecar 自己吐出去的文本。
// 两边都改才叫修完，只改一边等于把泄露点挪了个位置。

// 需要遮蔽的形态（保守优先：宁可多遮，不可少遮）：
//   Authorization: Bearer <tok>
//   ?code=xxx / access_token=xxx / api_key=xxx / secret=xxx（含 JSON 与 query 两种写法）
//   裸的 sk- 前缀密钥
const PATTERNS = [
  [/(Bearer)(\s+)([A-Za-z0-9._~+/=-]{8,})/gi, '$1$2***'],
  [/((?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?code|client[_-]?secret|secret|token|code)["']?\s*[=:]\s*)("?)([A-Za-z0-9._~+/=-]{8,})\2/gi, '$1$2***$2'],
  [/(sk-)[A-Za-z0-9_-]{8,}/g, '$1***'],
];

// redactLine 返回遮蔽后的文本（幂等：对已遮蔽的文本再跑一次不变）。
export function redactLine(line, maxLen = 2048) {
  let s = String(line ?? '');
  // 先截断再遮蔽：遮完再截可能把 *** 截掉一半，留下半截密钥的前缀。
  if (s.length > maxLen) s = s.slice(0, maxLen) + `…(+${s.length - maxLen}B)`;
  for (const [re, rep] of PATTERNS) s = s.replace(re, rep);
  return s;
}
