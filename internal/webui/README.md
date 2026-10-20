# internal/webui —— 前端产物的嵌入层

单二进制分发要求把 Vite 构建产物打进 Go 二进制。本包用 `go:embed` 承担这件事，
并对调用方（M9 起为 internal/httpapi）只暴露一个 `webui.Handler()`。

## 为什么产物落 internal/webui/dist（而不是 web/dist）

`go:embed` 只能嵌入**本包目录及其子目录**中的文件——`//go:embed ../web/dist`
会在编译期被直接拒绝。所以构建链必须让 vite 把 `build.outDir` 写到
`internal/webui/dist`（见 `web/vite.config.ts`）。`emptyOutDir: true` 保证每次
构建清掉带 hash 的旧 chunk，不让二进制里积累永远不会被引用的死文件。

## 为什么用 build tag 而不是「提交占位 dist/index.html」

任务给出的两个候选方案里，**提交占位文件进 dist/ 不可行**，实测原因有二：

1. 仓库根 `.gitignore` 有 `dist/` 规则（不限定 `/dist/`），`internal/webui/dist/`
   整目录被忽略；占位文件无法作为普通提交入库，而任务纪律不允许改根文件。
2. 更致命的：`vite build` 的 `emptyOutDir` 每次清空 `dist/`。任何提交在
   dist 里的占位都会被构建删除——于是「构建后再次裸 `go build`」会因
   `//go:embed dist` 找不到目录而编译失败，违反核心约束的长期成立性。

因此采用 **build tag 双形态**：

| 命令 | 生效文件 | 行为 |
|---|---|---|
| `go build ./...`（默认） | `placeholder.go`（`!webui_dist`） | 任何路径都返回 `placeholder.html`「前端未构建」 |
| `go build -tags webui_dist ./...` | `dist.go`（`webui_dist`） | 嵌入 `dist/` 全部产物并正常服务 |

`placeholder.html` 是提交在**包根**的普通文件（不在 dist/ 下，不受 ignore
与清空影响），被 `//go:embed placeholder.html` 嵌入。

`-tags webui_dist` 且 dist 不存在 → 编译失败。这是刻意的：带 tag 即声明
「已按顺序构建前端」，此时静默回退占位页反而会做出一个「看起来正常、
实际没带前端」的发布二进制。

## 构建顺序

```bash
# 1. 前端（产物直落 internal/webui/dist）
cd web
pnpm install --store-dir .pnpm-store   # store 落本地并被 web/.gitignore 忽略
pnpm build

# 2. Go：带 tag 才嵌前端；CI 发布二进制的标准姿势
cd ..
go build -tags webui_dist -o bin/freerouter ./cmd/freerouter

# 开发机日常（没跑 pnpm build 也不报错）
go build ./...
```

Makefile / CI 里发布目标应使用 `-tags webui_dist`；裸 `go build ./...`
保留给「只要后端能编译」的开发与测试循环。

## 前端路由模式对服务端的要求

骨架期前端用 HashRouter（`web/src/App.tsx` 注释说明了原因），因此本包**不做
SPA fallback**，纯静态服务即可。若 M9 切 BrowserRouter，index.html 回退与
鉴权守卫（docs/ui/01-ia.md 第 7 节的三层守卫）归 httpapi 挂载层决定，
本包继续只出静态字节。
