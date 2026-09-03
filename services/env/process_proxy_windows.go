//go:build windows

package env

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	winreg "golang.org/x/sys/windows/registry"
)

var procQueryFullProcessImageNameW = windows.NewLazySystemDLL("kernel32.dll").
	NewProc("QueryFullProcessImageNameW")

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").
	NewProc("GetExtendedTcpTable")

// tcpTableOwnerPidListener = TCP_TABLE_OWNER_PID_LISTENER：只要 LISTEN 状态且带 PID 的行，
// 比全表小一个数量级，无需再起 netstat 解析几千行文本。
const tcpTableOwnerPidListener = 3

// mibTCPRowOwnerPid 对应 MIB_TCPROW_OWNER_PID（6 个 DWORD = 24 字节）。
type mibTCPRowOwnerPid struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}

// findListenPIDWin 返回正在 LISTEN 状态占用 port 的进程 PID（仅 IPv4，与旧 netstat -p tcp 一致）。
// 走 Win32 GetExtendedTcpTable：无子进程、无文本解析、毫秒级，适合 3 秒一次的状态轮询。
func findListenPIDWin(port int) int {
	if port <= 0 || port > 65535 {
		return 0
	}
	var size uint32
	// 先以空缓冲取所需大小（返回 ERROR_INSUFFICIENT_BUFFER=122）
	r, _, _ := procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0,
		uintptr(windows.AF_INET), tcpTableOwnerPidListener, 0)
	if r != 0 && r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
		return 0
	}
	if size < 4 {
		return 0
	}
	buf := make([]byte, size)
	r, _, _ = procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)), 0, uintptr(windows.AF_INET), tcpTableOwnerPidListener, 0)
	if r != 0 {
		return 0
	}
	count := int(*(*uint32)(unsafe.Pointer(&buf[0])))
	rowSize := int(unsafe.Sizeof(mibTCPRowOwnerPid{}))
	if max := (len(buf) - 4) / rowSize; count > max {
		count = max
	}
	rows := unsafe.Slice((*mibTCPRowOwnerPid)(unsafe.Pointer(&buf[4])), count)
	for i := range rows {
		// dwLocalPort 是网络字节序存在 DWORD 低 16 位，需按字节翻转
		if uint16(rows[i].LocalPort>>8)|uint16(rows[i].LocalPort<<8) == uint16(port) {
			return int(rows[i].OwningPid)
		}
	}
	return 0
}

// processExePathWin 取指定 pid 进程的可执行文件完整路径。
// 用 Win32 API 而非 `powershell (Get-Process -Id N).Path`：后者是控制台程序，
// GUI 主进程拉起它必定闪出窗口，且每 3 秒的状态轮询里冷启动要几百毫秒。
func processExePathWin(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid pid")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	r, _, e := procQueryFullProcessImageNameW.Call(uintptr(h), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		if e != nil && e != syscall.Errno(0) {
			return "", e
		}
		return "", errors.New("QueryFullProcessImageNameW failed")
	}
	return windows.UTF16ToString(buf[:size]), nil
}

// proxyTransport 返回代理感知的 http transport：优先环境变量，回退 WinINET 系统代理（注册表）。
func proxyTransport() http.RoundTripper {
	return &http.Transport{Proxy: systemProxyURL, TLSHandshakeTimeout: 10 * time.Second}
}

func systemProxyURL(req *http.Request) (*url.URL, error) {
	if u, err := http.ProxyFromEnvironment(req); u != nil || err != nil {
		return u, err
	}
	k, err := winreg.OpenKey(winreg.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, winreg.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return nil, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil || server == "" {
		return nil, nil
	}
	host := server
	if i := strings.Index(server, "="); i >= 0 {
		rest := server[i+1:]
		if j := strings.Index(rest, ";"); j >= 0 {
			rest = rest[:j]
		}
		host = rest
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return url.Parse(host)
}
