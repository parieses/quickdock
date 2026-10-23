//go:build !windows

package env

// readExeVersion 非 Windows 平台（Everything 不发行）一律返回空串，调用方标记版本为 unknown。
func readExeVersion(path string) string { return "" }
