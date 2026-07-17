package platform

import (
	"fmt"

	"golang.org/x/sys/windows"
)



// ExitWindowsEx 标志位（部分）
const (
	ewxShutdown    = 0x00000001
	ewxReboot      = 0x00000002
	ewxForceIfHung = 0x00000010 // 仅强制终止已挂起（无响应）的进程，避免误杀正常应用
)

// RunSystemCommand executes a system command: lock, shutdown, restart, sleep, emptytrash.
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
