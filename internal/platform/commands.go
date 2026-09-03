package platform

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"quickdock/internal/sysutil"
)

// ExitWindowsEx 标志位（部分）
const (
	ewxShutdown    = 0x00000001
	ewxReboot      = 0x00000002
	ewxForceIfHung = 0x00000010 // 仅强制终止已挂起（无响应）的进程，避免误杀正常应用
)

// 虚拟键码（部分）
const (
	vkLWin        = 0x5B
	vkLeft        = 0x25
	vkRight       = 0x27
	vkVolumeUp    = 0xAF
	vkVolumeDown  = 0xAE
	vkVolumeMute  = 0xAD
	keyEventFKeyUp = 0x0002
)

// RunSystemCommand executes a system command.
// Commands: lock, shutdown, restart, sleep, emptytrash,
//
//	window-left, window-right (窗口半屏),
//	volume-up, volume-down, volume-mute (音量),
//	wifi-toggle (WiFi 开关), kill-foreground (结束前台窗口进程).
//
// Returns an error if the command fails.
func RunSystemCommand(cmd string) error {
	switch cmd {
	case "lock":
		user32 := modUser32
		proc := user32.NewProc("LockWorkStation")
		ret, _, err := proc.Call()
		if ret == 0 {
			return fmt.Errorf("LockWorkStation failed: %v", err)
		}
	case "shutdown":
		if err := enableShutdownPrivilege(); err != nil {
			return err
		}
		user32 := modUser32
		proc := user32.NewProc("ExitWindowsEx")
		// EWX_FORCEIFHUNG 而非 EWX_FORCE：允许正常应用优雅保存退出，
		// 仅当应用已无响应（挂起）时才强制终止，避免未保存数据丢失。
		ret, _, err := proc.Call(ewxShutdown|ewxForceIfHung, 0)
		if ret == 0 {
			return fmt.Errorf("ExitWindowsEx failed: %v", err)
		}
	case "restart":
		if err := enableShutdownPrivilege(); err != nil {
			return err
		}
		user32 := modUser32
		proc := user32.NewProc("ExitWindowsEx")
		ret, _, err := proc.Call(ewxReboot|ewxForceIfHung, 0)
		if ret == 0 {
			return fmt.Errorf("ExitWindowsEx failed: %v", err)
		}
	case "sleep":
		powrprof := modPowrprof
		proc := powrprof.NewProc("SetSuspendState")
		ret, _, err := proc.Call(0, 0, 0)
		if ret == 0 {
			return fmt.Errorf("SetSuspendState failed: %v", err)
		}
	case "emptytrash":
		shell32 := modShell32
		proc := shell32.NewProc("SHEmptyRecycleBinW")
		ret, _, err := proc.Call(0, 0, 0x07)
		// SHEmptyRecycleBinW 返回 HRESULT，S_OK(0) 表示成功。
		// 原判断 ret==0 为失败是反向的：成功报失败、失败报成功。
		if ret != 0 {
			return fmt.Errorf("SHEmptyRecycleBinW failed: %v (hresult=0x%x)", err, uint32(ret))
		}
	case "window-left":
		sendWinArrow(vkLeft)
	case "window-right":
		sendWinArrow(vkRight)
	case "volume-up":
		sendMediaKey(vkVolumeUp)
	case "volume-down":
		sendMediaKey(vkVolumeDown)
	case "volume-mute":
		sendMediaKey(vkVolumeMute)
	case "wifi-toggle":
		return toggleWifi()
	case "kill-foreground":
		return killForegroundWindow()
	default:
		return fmt.Errorf("unknown system command: %s", cmd)
	}
	return nil
}

// sendWinArrow 模拟 Win+方向键，将前台窗口吸附到屏幕左/右半边。
func sendWinArrow(vk uint16) {
	ke := modUser32.NewProc("keybd_event")
	ke.Call(vkLWin, 0, 0, 0)
	time.Sleep(30 * time.Millisecond)
	ke.Call(uintptr(vk), 0, 0, 0)
	time.Sleep(30 * time.Millisecond)
	ke.Call(uintptr(vk), 0, keyEventFKeyUp, 0)
	time.Sleep(30 * time.Millisecond)
	ke.Call(vkLWin, 0, keyEventFKeyUp, 0)
}

// sendMediaKey 发送多媒体键（音量增减/静音），无需修饰键。
func sendMediaKey(vk uint16) {
	ke := modUser32.NewProc("keybd_event")
	ke.Call(uintptr(vk), 0, 0, 0)
	time.Sleep(20 * time.Millisecond)
	ke.Call(uintptr(vk), 0, keyEventFKeyUp, 0)
}

// toggleWifi 切换 Wi-Fi 适配器开关（需要管理员权限；netsh 失败会返回错误）。
func toggleWifi() error {
	// netsh 是控制台程序：GUI 主进程（正式版 -H windowsgui）直接拉起会弹 cmd 窗，须隐藏控制台
	q := exec.Command("netsh", "interface", "show", "interface")
	sysutil.Hide(q)
	out, err := q.Output()
	if err != nil {
		return fmt.Errorf("查询网络接口失败: %v", err)
	}
	text := string(out)
	// 确定 Wi-Fi 适配器名称（中文系统常见为“Wi-Fi”或“WLAN”）
	name := "Wi-Fi"
	if strings.Contains(text, "WLAN") {
		name = "WLAN"
	}
	// 在对应行中判断是否“已禁用”
	disabled := false
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, name) {
			disabled = strings.Contains(line, "已禁用") ||
				strings.Contains(line, "Administratively Disabled")
			break
		}
	}
	action := "enable"
	if !disabled {
		action = "disable"
	}
	cmd := exec.Command("netsh", "interface", "set", "interface", "name="+name, "admin="+action)
	sysutil.Hide(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("切换 Wi-Fi 失败(%s): %v %s", action, err, string(out))
	}
	return nil
}

// killForegroundWindow 结束当前前台窗口所属的进程。
// 安全防护：不允许结束 explorer.exe 与 QuickDock 自身进程。
func killForegroundWindow() error {
	hwnd, _, _ := modUser32.NewProc("GetForegroundWindow").Call()
	if hwnd == 0 {
		return fmt.Errorf("没有前台窗口")
	}
	var pid uint32
	modUser32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return fmt.Errorf("无法获取前台窗口进程")
	}
	// 防护：不杀 QuickDock 自身
	if uint32(windows.GetCurrentProcessId()) == pid {
		return fmt.Errorf("不能结束 QuickDock 自身")
	}
	// 防护：不杀 explorer.exe（会破坏任务栏）
	if name, _ := processNameByPID(pid); name == "explorer.exe" {
		return fmt.Errorf("不允许结束 explorer.exe")
	}
	const processTerminate = 0x0001
	const processQueryInfo = 0x0400
	h, _, err := modKernel32.NewProc("OpenProcess").Call(processTerminate|processQueryInfo, 0, uintptr(pid))
	if h == 0 {
		return fmt.Errorf("OpenProcess failed: %v", err)
	}
	defer modKernel32.NewProc("CloseHandle").Call(h)
	ret, _, err := modKernel32.NewProc("TerminateProcess").Call(h, 0)
	if ret == 0 {
		return fmt.Errorf("TerminateProcess failed: %v", err)
	}
	return nil
}

// processNameByPID 返回进程名（含 .exe），获取失败返回空串。
func processNameByPID(pid uint32) (string, error) {
	snapshot, _, err := modKernel32.NewProc("CreateToolhelp32Snapshot").Call(0x00000002, uintptr(pid)) // TH32CS_SNAPPROCESS
	if snapshot == 0 || snapshot == ^uintptr(0) {
		return "", fmt.Errorf("CreateToolhelp32Snapshot failed: %v", err)
	}
	defer modKernel32.NewProc("CloseHandle").Call(snapshot)
	var entry struct {
		Size              uint32
		CntUsage          uint32
		ProcessID         uint32
		DefaultHeapID     uintptr
		ModuleID          uint32
		CntThreads        uint32
		ParentProcessID   uint32
		PriClassBase      int32
		Flags             uint32
		ExeFile           [260]uint16
	}
	entry.Size = uint32(unsafe.Sizeof(entry))
	ret, _, _ := modKernel32.NewProc("Process32FirstW").Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for ret != 0 {
		if entry.ProcessID == pid {
			return windows.UTF16PtrToString(&entry.ExeFile[0]), nil
		}
		ret, _, _ = modKernel32.NewProc("Process32NextW").Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}
	return "", fmt.Errorf("process %d not found", pid)
}

// enableShutdownPrivilege 启用当前进程令牌的 SeShutdownPrivilege 权限。
// 现代 Windows 上该权限默认禁用，若不启用，ExitWindowsEx 关机会静默失败。
func enableShutdownPrivilege() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("OpenProcessToken failed: %v", err)
	}
	defer token.Close()

	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeShutdownPrivilege"), &luid); err != nil {
		return fmt.Errorf("LookupPrivilegeValue failed: %v", err)
	}

	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED},
		},
	}
	if err := windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil); err != nil {
		return fmt.Errorf("AdjustTokenPrivileges failed: %v", err)
	}
	return nil
}
