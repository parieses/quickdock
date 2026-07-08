package platform

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/windows"
)

// RunSystemCommand executes a system command: lock, shutdown, restart, sleep, emptytrash.
// Returns an error if the command fails.
func RunSystemCommand(cmd string) error {
	switch cmd {
	case "lock":
		user32 := syscall.NewLazyDLL("user32.dll")
		proc := user32.NewProc("LockWorkStation")
		ret, _, err := proc.Call()
		if ret == 0 {
			return fmt.Errorf("LockWorkStation failed: %v", err)
		}
	case "shutdown":
		if err := enableShutdownPrivilege(); err != nil {
			return err
		}
		user32 := syscall.NewLazyDLL("user32.dll")
		proc := user32.NewProc("ExitWindowsEx")
		ret, _, err := proc.Call(0x05, 0) // EWX_SHUTDOWN | EWX_FORCE
		if ret == 0 {
			return fmt.Errorf("ExitWindowsEx failed: %v", err)
		}
	case "restart":
		if err := enableShutdownPrivilege(); err != nil {
			return err
		}
		user32 := syscall.NewLazyDLL("user32.dll")
		proc := user32.NewProc("ExitWindowsEx")
		ret, _, err := proc.Call(0x06, 0) // EWX_REBOOT | EWX_FORCE
		if ret == 0 {
			return fmt.Errorf("ExitWindowsEx failed: %v", err)
		}
	case "sleep":
		powrprof := syscall.NewLazyDLL("powrprof.dll")
		proc := powrprof.NewProc("SetSuspendState")
		ret, _, err := proc.Call(0, 0, 0)
		if ret == 0 {
			return fmt.Errorf("SetSuspendState failed: %v", err)
		}
	case "emptytrash":
		shell32 := syscall.NewLazyDLL("shell32.dll")
		proc := shell32.NewProc("SHEmptyRecycleBinW")
		ret, _, err := proc.Call(0, 0, 0x07)
		if ret == 0 {
			return fmt.Errorf("SHEmptyRecycleBinW failed: %v", err)
		}
	default:
		return fmt.Errorf("unknown system command: %s", cmd)
	}
	return nil
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
