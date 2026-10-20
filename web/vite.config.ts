import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
// 产物落点：web/ 的同级目录 internal/webui/dist。
// 为什么是这个目录而不是 web/dist：
//   go:embed 只能嵌入「自己所在包目录及其子目录」的路径，
//   embed.go 在 internal/webui/ 下，所以 dist 必须是 internal/webui/dist，
//   跨目录（../dist）在 go:embed 里会被直接拒绝。
const outDir = resolve(here, '../internal/webui/dist')

export default defineConfig({
  plugins: [react()],
  // 相对路径：产物被 go:embed 打进二进制后，服务它的 URL 前缀由 Go 侧决定
  // （可能是 /、/console/、或被反代挂在子路径）。写死 '/' 会让子路径部署下
  // 所有 assets 404。相对 base 让资源引用变成 ./assets/xxx，
  // 由浏览器按文档 URL 解析。
  base: './',
  build: {
    outDir,
    // 上一轮的 chunk 必须清干净：文件名带 hash，清不干净会留下永远不会被引用
    // 的旧文件，二进制体积白涨，且排查线上加载的是哪一版会变得困难。
    emptyOutDir: true,
    // 产物里不暴露源码映射：单二进制分发出去后 sourcemap 等于把源码一起发了。
    sourcemap: false,
    target: 'es2022'
  },
  server: {
    port: 5173,
    // 开发期由 Vite dev server 把 /api 代理到 Go 进程（docs/PLAN.md 7.1 的「连带约定」）。
    // 占位阶段后端还没有这些路由，代理先写好，M9 联调时不用回来改配置。
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
      '/readyz': 'http://127.0.0.1:8080'
    }
  }
})
