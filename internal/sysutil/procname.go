package sysutil

import "strings"

// ProcessName 返回指定 PID 的进程镜像名（含 .exe 后缀，视平台而定）。
// 找不到（进程已退出 / 权限不足 / 平台不支持）时返回空串。
//
// 用途：宿主在代插件执行高危操作（如 host.process.kill）前，需要先知道
// 「这一刀下去砍的是谁」，才能按插件的白名单决定放行与否。
func ProcessName(pid int) string {
	return nameOf(pid)
}

// NormalizeProcName 归一化进程名用于白名单比对：统一小写、去掉 .exe 后缀。
// 与 internal/plugin 的同名函数保持同一口径（此处导出供跨包复用）。
func NormalizeProcName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".exe")
	return n
}
