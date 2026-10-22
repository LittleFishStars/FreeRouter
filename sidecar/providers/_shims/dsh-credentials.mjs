// dsh-credentials.mjs —— @deepseek-ai/dsh-credentials 的最小替身。
//
// 上游 provider 用它两件事：
//   1. `credentialRef(name)` —— 生成**带品牌的引名字符串**（`Ref<string>` 的运行时形态
//      就是一个字符串，品牌只存在于类型层）；
//   2. 少数模块在构造期 `ctx.credentials.resolve(ref)` —— 我们从不调用，且没有 ctx。
//
// 所以替身只需要一个恒等函数 + 一个永远抛错的 resolve（错误信息写清楚为什么）。
// 红线不变：sidecar 仍然**只读** DSH 凭据（真正读凭据的路径在 credentials.mjs，
// 不经过这个替身）。

/** 把普通字符串标成「凭据引用」。运行期就是字符串本身。 */
export function credentialRef(name) {
  if (typeof name !== 'string' || name.length === 0) {
    throw new Error('credentialRef: 名字必须是非空字符串')
  }
  return name
}

/** 反向：拿回原始名字（品牌在类型层，运行期同值）。 */
export function refName(ref) {
  return String(ref)
}

/**
 * 替身不提供真正的凭据服务：任何 resolve 都是装配错误。
 *
 * 为什么抛错而不是返回 undefined：返回 undefined 会让上游代码走「凭据不存在」
 * 分支并静默降级成不可用，排查时看不出真正原因。
 */
export async function resolve() {
  throw new Error('sidecar 不提供 DSH 凭据服务：provider auth 模块在本进程内只做纯计算')
}

export default { credentialRef, refName, resolve }
