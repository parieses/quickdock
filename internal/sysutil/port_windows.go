//go:build windows

package sysutil

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procQueryFullProcessImageNameW 内核态 PID→映像路径查询入口（延迟加载，避免未使用时的加载开销）。
var procQueryFullProcessImageNameW = windows.NewLazySystemDLL("kernel32.dll").
	NewProc("QueryFullProcessImageNameW")

// processPath 取指定 PID 进程的可执行文件完整路径（Win32 API，单次调用毫秒级）。
// 刻意不用 PowerShell `Get-CimInstance Win32_Process` 全量枚举：冷启动 + CIM 查询要 5~15 秒，
// 会把整个端口列表拖成「半天加载不出来」，表现为「开了 HTTP 服务却看不到它占用的端口」。
// 无权限读取时（部分系统进程）返回空串，属正常。
func processPath(pid int) string {
	if pid <= 0 {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	r, _, e := procQueryFullProcessImageNameW.Call(uintptr(h), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		// LazyProc.Call 成功时也可能带回 Errno(0)，这里只看返回值 r 是否为 0。
		_ = e
		return ""
	}
	return strings.TrimSpace(windows.UTF16ToString(buf[:size]))
}

// ListListeningPorts 列出本机所有 LISTENING 端口及占用进程。
// 走 netstat -ano + tasklist 解析（隐藏控制台），按端口升序返回；
// 同时经 Get-CimInstance 取各占用进程的映像完整路径，便于定位「是谁占了这个端口」。
func ListListeningPorts() ([]PortInfo, error) {
	out, err := Command("netstat", "-ano").Output()
	if err != nil {
		return nil, err
	}
	names := processNameMap()
	// 同一进程常占多个端口（svchost 尤其多），路径按 PID 缓存，避免重复 OpenProcess。
	pathCache := map[int]string{}
	pathOf := func(pid int) string {
		if pid <= 0 {
			return ""
		}
		if v, ok := pathCache[pid]; ok {
			return v
		}
		v := processPath(pid)
		pathCache[pid] = v
		return v
	}
	seen := map[string]bool{}
	var ports []PortInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "LISTEN") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		local := fields[1]
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(local[idx+1:])
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(fields[len(fields)-1])
		proto := "tcp"
		if strings.Contains(line, "UDP") {
			proto = "udp"
		}
		key := proto + ":" + strconv.Itoa(port) + ":" + strconv.Itoa(pid)
		if seen[key] {
			continue
		}
		seen[key] = true
		ports = append(ports, PortInfo{
			Port:     port,
			Protocol: proto,
			PID:      pid,
			Process:  names[pid],
			Path:     pathOf(pid),
			// 自身进程监听的端口即 QuickDock 内置服务（HTTP 静态服务等），
			// 端口全景里据此高亮，避免混在一堆系统进程里认不出。
			Self: pid == os.Getpid(),
		})
	}
	return ports, nil
}

// KillProcess 结束指定 PID 的进程（含安全校验，避免误杀系统关键进程）。
func KillProcess(pid int) error {
	if pid <= 4 {
		return fmt.Errorf("拒绝操作：系统关键进程 (PID ≤ 4)")
	}
	if pid == os.Getpid() {
		return fmt.Errorf("拒绝操作：不能结束自身进程")
	}
	name := nameOf(pid)
	if isSystemProcess(name) {
		return fmt.Errorf("拒绝操作：系统关键进程: %s", name)
	}
	cmd := Command("taskkill", "/F", "/PID", strconv.Itoa(pid))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("结束进程失败: %v | %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// processNameMap 一次性解析 tasklist，建立 PID → 进程名 映射。
func processNameMap() map[int]string {
	out, err := Command("tasklist", "/NH", "/FO", "CSV").Output()
	if err != nil {
		return map[int]string{}
	}
	m := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		name := strings.Trim(parts[0], "\"")
		pid, err := strconv.Atoi(strings.Trim(parts[1], "\""))
		if err == nil && name != "" {
			m[pid] = name
		}
	}
	return m
}

func nameOf(pid int) string {
	return processNameMap()[pid]
}

// isSystemProcess 判断是否为 Windows 系统关键进程（按镜像名）。
func isSystemProcess(name string) bool {
	switch strings.ToLower(strings.TrimSuffix(name, ".exe")) {
	case "system", "smss", "csrss", "wininit", "winlogon",
		"services", "lsass", "svchost", "lsm",
		"explorer", "taskhost", "taskhostw",
		"runtimebroker", "sihost", "ctfmon",
		"dwm", "conhost", "fontdrvhost",
		"spoolsv", "securityhealthservice",
		"trustedinstaller", "ntoskrnl":
		return true
	}
	return false
}
