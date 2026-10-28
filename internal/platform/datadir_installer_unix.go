//go:build !windows

package platform

// readInstallerDataDir 非 Windows 平台无安装器注册表，恒返回空。
func readInstallerDataDir() string { return "" }
