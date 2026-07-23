//go:build !production

package main

// 开发版与正式版共用同一数据库（~/.quickdock），因此也共用同一把单实例锁，
// 保证同一机器上任意构建只运行一个实例，避免两个进程同时写同一 SQLite 库。
func init() {
	instanceMutexName = "Local\\QuickDock-Instance"
}
