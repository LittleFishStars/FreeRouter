// cordis.mjs —— @deepseek-ai/cordis 的最小替身（只覆盖 provider auth 模块用到的部分）。
//
// 为什么需要：上游 provider 的 `*-auth.ts` 里有一批 `class XxxAuth extends Service`
// 的插件服务。我们只用它们**纯函数与常量**（parse/build/expiry 那些），不跑插件
// 生命周期——但 ESM 装载时会执行 `class ... extends Service` 这一行，所以必须
// 有一个可继承的基类，否则整个模块图装载失败。
//
// 注意：这不是「假装实现了 cordis」。sidecar 从不 `ctx.service()`、不注册插件，
// 只做 imports 里的静态求值。任何真正需要 cordis 运行时的路径都不会被调用。

/** 插件服务基类的最小形态：接受 ctx、暴露 ctx 与 dispose。 */
export class Service {
  constructor(ctx, name) {
    this.ctx = ctx ?? {}
    this.name = name ?? this.constructor.name
    this[Symbol.dispose] = () => {}
  }
  /** 上游 Service 上常见的可选钩子：这里一律 no-op。 */
  start() {}
  stop() {}
}

/** 事件总线的最小子集（上游若在构造期调用 on/emit 也不会炸）。 */
export class Context {
  constructor() {
    this.listeners = new Map()
  }
  on(event, fn) {
    const list = this.listeners.get(event) ?? []
    list.push(fn)
    this.listeners.set(event, list)
    return () => this.off(event, fn)
  }
  once(event, fn) {
    const off = this.on(event, (...args) => { off(); fn(...args) })
    return off
  }
  off(event, fn) {
    const list = this.listeners.get(event)
    if (list) this.listeners.set(event, list.filter((f) => f !== fn))
  }
  emit(event, ...args) {
    for (const fn of this.listeners.get(event) ?? []) fn(...args)
  }
  /** 服务查询：永远返回 undefined（我们从不依赖插件服务）。 */
  service() { return undefined }
  get(name) { return undefined }
  set(name, value) { return value }
}

/** 上游有的版本导出 `Schema`/`s`（schemastery）；这里给一个可调用占位。 */
export const Schema = Object.assign(
  (v) => v,
  {
    object: () => (v) => v,
    string: () => (v) => v,
    number: () => (v) => v,
    boolean: () => (v) => v,
    union: () => (v) => v,
    array: () => (v) => v,
    dict: () => (v) => v,
    natural: () => (v) => v,
  },
)

export default { Service, Context, Schema }
