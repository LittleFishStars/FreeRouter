// sign.mjs —— 华为云 SDK-HMAC-SHA256 请求签名。
//
// 来源：deepseek-harness-codearts src/sign.ts（Apache-2.0，gitee.com/iJetLi/deepseek-harness-codearts），
// 按贡献链路的提取计划原样移植（TS→ESM 去类型）；行为逐行对齐，勿做"顺手优化"。
// 语义说明见原文件与 docs/agent-notes/providers-codearts.md。

/** sha256 的 hex 摘要。 */
export async function sha256Hex(data) {
  const hash = await crypto.subtle.digest('SHA-256', data.slice().buffer)
  return Array.from(new Uint8Array(hash)).map((b) => b.toString(16).padStart(2, '0')).join('')
}

/** HMAC-SHA256 的 hex 摘要。 */
export async function hmacSha256Hex(key, data) {
  const cryptoKey = await crypto.subtle.importKey('raw', key.slice().buffer, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  const sig = await crypto.subtle.sign('HMAC', cryptoKey, data.slice().buffer)
  return Array.from(new Uint8Array(sig)).map((b) => b.toString(16).padStart(2, '0')).join('')
}

/** SDK-HMAC-SHA256 的 canonical request 拼装（华为规范：排序后的签名头 + 空 key 行）。 */
export function buildCanonicalRequest(method, uri, query, headers, payloadHash) {
  const signedHeaders = []
  headers.forEach((_, k) => signedHeaders.push(k))
  signedHeaders.sort()
  const headerLines = signedHeaders.map((k) => `${k}:${headers.get(k) ?? ''}`)
  return [method, uri, query, headerLines.join('\n'), '', signedHeaders.join(';'), payloadHash].join('\n')
}

/** 签名一个华为请求；返回需合并到请求中的头映射（host 头由调用方剔除）。 */
export async function signRequestHuawei(ak, sk, securityToken, method, urlStr, body, extraHeaders) {
  const url = new URL(urlStr)
  let uri = url.pathname
  if (!uri.endsWith('/')) uri += '/'
  const query = url.search.slice(1)
  const dateStamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z')
  const payloadHash = await sha256Hex(body)

  const headers = new Map()
  headers.set('host', url.host)
  headers.set('x-sdk-date', dateStamp)
  headers.set('x-sdk-content-sha256', payloadHash)
  headers.set('x-security-token', securityToken)
  // 额外的签名头（如 glm-5.3-flash 的 maas_type: benefit）：参与 canonical 计算
  // 并包含在 SignedHeaders 中，需原样随请求发送，否则服务端验签失败。
  if (extraHeaders !== undefined) {
    for (const [key, value] of Object.entries(extraHeaders)) headers.set(key, value)
  }
  // GET 请求（排队状态轮询）不携带请求体，因此无 content-type。
  if (method.toUpperCase() !== 'GET') headers.set('content-type', 'application/json')

  const canonicalRequest = buildCanonicalRequest(method, uri, query, headers, payloadHash)
  const canonicalHash = await sha256Hex(new TextEncoder().encode(canonicalRequest))
  const stringToSign = `SDK-HMAC-SHA256\n${dateStamp}\n${canonicalHash}`
  const signature = await hmacSha256Hex(new TextEncoder().encode(sk), new TextEncoder().encode(stringToSign))
  // 与原实现对齐：SignedHeaders 用**排序后的全集**（含 host）——原实现在真实端点
  // 上实跑通过，勿按规范直觉改成剔除 host。
  headers.set('Authorization', `SDK-HMAC-SHA256 Access=${ak},SignedHeaders=${[...headers.keys()].sort().join(';')},Signature=${signature}`)
  return headers
}
