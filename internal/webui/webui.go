// Package webui 把控制台前端产物（web/ 下 pnpm build 生成 internal/webui/dist/）
// 经 go:embed 打进单二进制，并暴露一个只读 http.Handler。
//
// 两种构建形态（取舍原因详见同目录 README.md）：
//
//	go build ./...                    未嵌前端：整页输出 placeholder.html。
//	                                  「未构建前端时 go build ./... 必须通过」
//	                                  这条硬约束由 placeholder.go 的 tag 隔离成立。
//	go build -tags webui_dist ./...   嵌入 internal/webui/dist/ 全部产物并静态服务；
//	                                  dist 不存在时编译失败——这是期望行为：
//	                                  显式带 tag 即声明「构建方保证了产物存在」。
//
// 挂载方式（M9 起由 Lead 在 internal/httpapi 集成；本包不依赖 httpapi，不自行注册路由）：
//
//	http.Handle("/", webui.Handler())
package webui

import "net/http"

// Handler 返回服务内嵌前端产物的处理器。各形态的具体实现见 dist.go /
// placeholder.go——这里只约定共同语义：
//
//   - 未构建形态：任何路径都返回占位说明页。比 404 更利于排查
//     「忘了跑 pnpm build 就重新编译」的人。
//   - 构建形态：服务 dist 静态产物。vite base:'./' 决定了资源全走相对路径，
//     挂在任意 URL 前缀（/、/console/、反代子路径）下都成立。
//
// SPA fallback（子路径回退 index.html）不在本包做：骨架期前端用 HashRouter，
// 子路径不产生服务端需求；M9 若切 BrowserRouter，回退策略归 httpapi 挂载层
// 决定——那是路由与鉴权守卫（docs/ui/01-ia.md 第 7 节）的领地，webui 只出字节。
func Handler() http.Handler { return handler() }

// Built 报告当前二进制是否嵌入了真实前端产物，供版本信息/页脚展示用（M9）。
func Built() bool { return built }
