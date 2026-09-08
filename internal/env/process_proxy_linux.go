//go:build linux

package env

import (
	"os"
	"strconv"
	"strings"
)

// processExePath 返回占用 pid 的进程完整可执行文件路径。
// Linux 直接读 /proc/<pid>/exe 符号链接，无需 fork 子进程。
// mac/linux 不通用：macOS 无 /proc，见 process_proxy_darwin.go。
func processExePath(pid int) string {
	if pid <= 0 {
		return ""
	}
	p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(p)
}
