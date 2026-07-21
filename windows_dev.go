//go:build !production

package main

// 开发版（wails3 dev 或普通 go build，不带 production 标签）使用独立的锁名，
// 与正式版区分开，避免开发实例被已运行的正式版挡住而无法启动。
func init() {
	instanceMutexName = "Local\\QuickDock-Instance-Dev"
}
