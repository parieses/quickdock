//go:build darwin || linux

package sysutil

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ListProcesses 列出本机全部进程（PID / 名称 / 常驻内存）。
// 遍历 /proc/<pid>/ 读取 comm 与 status 的 VmRSS（常驻内存，单位 KB → 字节）。
// 其他用户进程目录无读权限时静默跳过，不中断整体枚举。
func ListProcesses() ([]ProcessInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []ProcessInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		name := readProcComm(e.Name())
		mem := readProcRSS(e.Name())
		procs = append(procs, ProcessInfo{PID: pid, Name: name, MemBytes: mem})
	}
	return procs, nil
}

// readProcComm 读 /proc/<pid>/comm（进程名的截断形式，如 "chrome"）。
func readProcComm(pidStr string) string {
	b, err := os.ReadFile(filepath.Join("/proc", pidStr, "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readProcRSS 读 /proc/<pid>/status 的 VmRSS 行，单位 KB → 字节。
func readProcRSS(pidStr string) int64 {
	b, err := os.ReadFile(filepath.Join("/proc", pidStr, "status"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				if err == nil {
					return kb * 1024
				}
			}
			break
		}
	}
	return 0
}
