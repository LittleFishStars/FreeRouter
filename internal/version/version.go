// Package version 保存构建期注入的版本信息。
// 为什么单独一个包：`--version` 与 User-Agent 都要用，放在 main 里会被其他包反向依赖。
package version

// 以下变量在构建时通过 -ldflags -X 注入（见 Makefile）。
var (
	// Version 语义化版本号，开发构建为 dev。
	Version = "dev"
	// Commit git 短哈希。
	Commit = "unknown"
	// BuildTime 构建时间（RFC3339）。
	BuildTime = "unknown"
)

// String 返回单行版本串，供 --version 与日志使用。
func String() string {
	return "freerouter " + Version + " (" + Commit + ", " + BuildTime + ")"
}
