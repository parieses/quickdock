//go:build windows

package sysutil

import (
	"strconv"
	"strings"
)

// ListProcesses 列出本机全部进程（PID / 名称 / 常驻内存）。
// 走 tasklist /NH /FO CSV 解析；内存字段为 KB，转字节返回。
// 与 KillProcess 共用 tasklist 输出，不引入新进程查询开销。
func ListProcesses() ([]ProcessInfo, error) {
	out, err := Command("tasklist", "/NH", "/FO", "CSV").Output()
	if err != nil {
		return nil, err
	}
	var procs []ProcessInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 5 {
			continue
		}
		name := strings.Trim(parts[0], "\"")
		pid, err := strconv.Atoi(strings.Trim(parts[1], "\""))
		if err != nil {
			continue
		}
		// parts[4] 形如 "1,234 K"，去掉千分位逗号与 K 单位
		memK := strings.TrimSpace(strings.TrimSuffix(strings.ReplaceAll(parts[4], ",", ""), " K"))
		memBytes, _ := strconv.ParseInt(memK, 10, 64)
		procs = append(procs, ProcessInfo{
			PID:      pid,
			Name:     name,
			MemBytes: memBytes * 1024,
		})
	}
	return procs, nil
}
