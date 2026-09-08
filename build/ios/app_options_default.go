//go:build !ios

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// modifyOptionsForIOS is a no-op on non-iOS platforms
func modifyOptionsForIOS(opts *application.Options) {
	// No modifications needed for non-iOS platforms
}

// main 仅为让 host 平台（非 ios）的 `go build ./...` 能正常编译本包而存在的空桩：
// 本目录是 iOS 专属构建脚手架，iOS 构建经 main_ios.go 导出的 WailsIOSMain 进入，
// 不需要 Go 的 main()；但 app_options_default.go 带 `!ios` 约束会在 host 构建时进入包，
// 导致 `function main is undeclared` 报错（每次全量构建都报、掩盖真实错误）。
// iOS 构建（GOOS=ios）下此文件被排除，不受影响。
func main() {}