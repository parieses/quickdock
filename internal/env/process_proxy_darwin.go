//go:build darwin

package env

import (
	"os/exec"
	"strconv"
	"strings"
)

// processExePath 返回占用 pid 的进程完整可执行文件路径。
// macOS 没有 /proc，改用 lsof 取该进程的 text section（即可执行镜像）路径：
// 输出形如 "p1234\nn/usr/local/bin/redis-server"，取首个 n 开头的行。
// 一个进程通常只有一条 txt 映射（主可执行文件），多行时取第一条。
func processExePath(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-d", "txt", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "n/") {
			return strings.TrimPrefix(ln, "n")
		}
	}
	return ""
}
