// sidecar/resolve-hook.mjs —— 异步解析钩子（仅当 Node 缺少同步的 module.registerHooks 时用到）。
//
// 逻辑与 deps.mjs 里内联的 registerHooks 完全一致：把 `@deepseek-ai/*` 的解析起点
// 改到 shim 的 __entry.mjs，让 Node 用原生 exports 解析找到 B 层包。
// 为什么还留这个文件：registerHooks 是 Node ≥ 22.15 才有的 API；更老的运行时要能装载
// （README 的「运行要求」写的是 Node ≥ 22.19，但留一条退路比 future-proof 的假设便宜）。

export async function resolve(specifier, context, nextResolve) {
  const entry = process.env.FR_SIDECAR_SHIM_ENTRY;
  if (entry && specifier.startsWith('@deepseek-ai/')) {
    const { pathToFileURL } = await import('node:url');
    return nextResolve(specifier, { ...context, parentURL: pathToFileURL(entry).href });
  }
  return nextResolve(specifier, context);
}
