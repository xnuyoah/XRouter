// Package buildinfo 暴露服务端共享的编译期元数据。
package buildinfo

// 以下变量在发布构建时通过 ldflags 覆盖。
// 默认值覆盖本地开发构建。
var (
	// Version 是二进制文件的语义化版本或 git describe 输出。
	Version = "dev"

	// Commit 是打包进二进制文件的 git 提交 SHA。
	Commit = "none"

	// BuildDate 记录二进制文件以 UTC 构建的时间。
	BuildDate = "unknown"
)
