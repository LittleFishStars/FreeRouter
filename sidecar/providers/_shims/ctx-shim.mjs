// ctx-shim.mjs —— 给「必须继承 cordis Service」的 provider 一个最小可用 ctx。
//
// ## 为什么需要
//
// zcode / modelscope 的登录入口是 `class XxxAuth extends Service` 的**方法**，
// 而这些方法会碰 `this.ctx.credentials.{resolve,set}`（凭据读写）与 `this.ctx.logger`。
// 我们的场景里凭据由 **Go 主进程**密封存储在 SQLite，sidecar 不落盘（红线①）。
//
// 所以这里提供一个**只活在本次调用里**的内存 ctx：
//   credentials.set    写进内存 → 登录完立刻由调用方取走（不落盘）
//   credentials.resolve 从同一内存读（登录过程中的自读自写能闭环）
//   logger             丢弃（sidecar stdout 是协议通道，不能混日志）
//
// **红线不变**：sidecar 永不写 DSH 的凭据库；这里写的只是一个会被立刻取走、
// 随即随进程/调用结束而消失的 Map。

/** 内存凭据表：ref → JSON 字符串。一次登录用完即弃。 */
export function createMemoryContext(options = {}) {
  const store = new Map()
  const logger = {
    debug: () => {},
    info: () => {},
    warn: () => {},
    error: () => {},
    trace: () => {},
  }
  const credentials = {
    async resolve(ref) {
      const value = store.get(String(ref))
      if (value === undefined) return undefined
      return { value, ref: String(ref) }
    },
    async set(ref, value) {
      store.set(String(ref), String(value))
    },
    async unset(ref) {
      store.delete(String(ref))
    },
    async delete(ref) {
      store.delete(String(ref))
    },
    async list() {
      return [...store.entries()].map(([ref, value]) => ({ ref, value }))
    },
  }
  const ctx = {
    credentials,
    logger: options.logger ?? logger,
    get(name) {
      if (name === 'credentials') return credentials
      if (name === 'logger') return ctx.logger
      return undefined
    },
    service(name) {
      return ctx.get(name)
    },
    on() {
      return () => {}
    },
    once(fn) {
      return () => {}
    },
    emit() {},
    effect(fn) {
      return fn?.()
    },
    /** 取走刚刚写入的凭据（登录成功后由适配器调用一次）。 */
    __take(ref) {
      const v = store.get(String(ref))
      store.clear()
      return v
    },
    __all() {
      return [...store.entries()]
    },
  }
  return ctx
}
