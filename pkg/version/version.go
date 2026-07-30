package version

import "fmt"

var (
	// Version 版本号，由 goreleaser 注入
	Version = "dev"
	// Commit Git 提交哈希，由 goreleaser 注入
	Commit = "none"
	// Date 构建日期，由 goreleaser 注入
	Date = "unknown"
)

// String 返回完整版本信息
func String() string {
	return fmt.Sprintf("syncmedia %s (commit: %s, built: %s)", Version, Commit, Date)
}