//go:build !windows

package env

// killEpmdBeforeDelete 非 Windows 平台无 epmd 锁文件问题，空实现。
func killEpmdBeforeDelete(dir string) {}
