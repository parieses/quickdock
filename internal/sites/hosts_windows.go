//go:build windows

package sites

import (
	"os"
	"path/filepath"
)

// hostsFilePath 系统 hosts 文件路径。
// 优先用 SystemRoot 环境变量拼（Windows 盘符/目录名可能非默认），取不到再退回默认路径。
func hostsFilePath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return filepath.Join("C:", "Windows", "System32", "drivers", "etc", "hosts")
}
