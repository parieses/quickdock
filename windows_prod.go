//go:build production

package main

// 正式版（wails3 build，带 -tags production）使用与主实例一致的锁名，
// 保证同一台机器上只运行一个正式版实例。
func init() {
	instanceMutexName = "Local\\QuickDock-Instance"
}
