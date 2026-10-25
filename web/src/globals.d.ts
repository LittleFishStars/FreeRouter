// 构建期注入的全局量（由 vite.config.ts 的 define 提供）。
//
// 为什么需要它：控制台的版本号来自后端 /api/version，而那个值只在
// 「带 ldflags 的构建」里才非空——直接 `go build` 出来永远是 dev。
// 页面上必须有一个**随前端产物变化**的标识，否则「我看到的是不是新版本」
// 只能靠猜（2026-10-25 就因此把「浏览器跑着旧 chunk」误判成功能没实现）。
declare const __BUILD_STAMP__: string
