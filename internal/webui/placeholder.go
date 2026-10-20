//go:build !webui_dist

package webui

import (
	"embed"
	"net/http"
)

// placeholder 是未构建形态下的整页内容。
//
// 它必须是「提交在包目录根的普通文件」，而不是「提交在 dist 里的占位
// index.html」（任务给出的候选方案之一，实测不可行，原因有二）：
//  1. 根 .gitignore 的 dist/ 规则忽略整个 internal/webui/dist/，
//     占位放里面无法入库，而纪律不允许改根文件；
//  2. vite build 的 emptyOutDir 每次清空 dist/，提交进去的占位会被
//     下一次构建删除，导致「构建之后再次裸 go build」因 //go:embed dist
//     找不到目录而失败——核心约束就无法长期成立。
//
//go:embed placeholder.html
var placeholderFS embed.FS

// handler 未构建形态：直接整页输出占位说明。
// 刻意不经 http.FileServerFS：FileServer 对「目录必须以 IsDir 结尾」有强
// 假设，单文件 FS 会触发 500；形态分发本来就该在这里，而不是伪造目录语义。
func handler() http.Handler {
	page, _ := placeholderFS.ReadFile("placeholder.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})
}

const built = false
