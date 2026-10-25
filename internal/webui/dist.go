//go:build webui_dist

package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist 是前端构建产物目录。go:embed 的约束决定了整个方案的形状：
// 只能嵌「本包目录及其子目录」，所以 vite outDir 必须是 internal/webui/dist
// （跨目录 ../web/dist 会被 go 直接拒绝），见 web/vite.config.ts 注释。
//
//go:embed all:dist
var distFS embed.FS

// handler 用 FileServerFS 服务 dist；index.html 禁缓存——它是产物文件名的
// 清单，缓存它等于让用户拿旧清单去加载已 404 的旧 chunk。带 hash 的
// assets 可以长缓存，但分路径设头属于 M9 中间件层的事。
func handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// embed 常量路径不可能失败；失败即代码写错，崩给开发者看。
		panic("webui: internal embed layout broken: " + err.Error())
	}
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		// index.html 是产物文件名的清单：缓存它等于让浏览器拿旧清单去
		// 加载已被新构建删掉的旧 chunk（表现为白屏，且硬刷新前“总好不了”）。
		// 这里对根路径与所有 .html 加 no-store；带 hash 的 assets 不在此列，
		// 仍可被缓存（文件名变则内容变，缓存不会错配）。
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			// 其余是带内容 hash 的产物（assets/index-<hash>.js 等）：文件名变则
			// 内容变，所以可以放心长缓存。写明 immutable 而不是不给头：
			// 不给头时浏览器会按启发式规则反复发条件请求，白白吃掉一次往返。
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}

const built = true
