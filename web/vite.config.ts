import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))

// 构建标识：git 短哈希 + 构建时刻。注入进产物，页脚会显示它。
//
// 为什么必须有个随产物变化的标识：2026-10-25 排查「多选没生效」花了两轮，
// 因为页面上只有后端 /api/version —— 而那个值在没有 ldflags 的 go build 下
// 恒为 dev，看不出前端到底是哪一版。构建标识让「我看到的页面是哪次构建」
// 在页面本身就能回答，不必去猜浏览器缓存。
const buildStamp = (() => {
  let hash = 'nogit'
  try {
    hash = execFileSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: here }).toString().trim()
  } catch {
    /* 非 git 环境（发行包解压后构建）：保留 nogit，不因此让构建失败 */
  }
  const t = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  const when = `${pad(t.getMonth() + 1)}-${pad(t.getDate())} ${pad(t.getHours())}:${pad(t.getMinutes())}`
  return `${hash} · ${when}`
})()

// 产物落点：web/ 的同级目录 internal/webui/dist。
// 为什么是这个目录而不是 web/dist：
//   go:embed 只能嵌入「自己所在包目录及其子目录」的路径，
//   embed.go 在 internal/webui/ 下，所以 dist 必须是 internal/webui/dist，
//   跨目录（../dist）在 go:embed 里会被直接拒绝。
const outDir = resolve(here, '../internal/webui/dist')

export default defineConfig({
  plugins: [react()],
  define: {
    __BUILD_STAMP__: JSON.stringify(buildStamp)
  },
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
    target: 'es2022',
    rollupOptions: {
      output: {
        // 手写分包而不是交给默认策略：MUI 一家就占了大半个 bundle，
        // 与应用代码混在一个 chunk 里，意味着**改一行业务代码**就让用户
        // 重新下载整个 MUI。分成 vendor 之后，应用代码的 hash 变化只会
        // 让那几十 KB 失效，MUI/React 从浏览器缓存直接命中。
        // 用函数而不是包名清单：@mui/material 只是一层壳，真正的体积在
        // @mui/system、@mui/utils、@mui/styled-engine、@emotion/* 这些子包里。
        // 按「是不是 node_modules」划分才不漏；再把 react 单独拎出来，
        // 因为它几乎从不变动，值得独占一个长期缓存。
        manualChunks(id: string) {
          if (!id.includes('node_modules')) return undefined
          if (/[\\/]node_modules[\\/](react|react-dom|react-router|react-router-dom|scheduler)[\\/]/.test(id)) {
            return 'react'
          }
          return 'vendor'
        }
      }
    }
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
