package platform

import (
	"fmt"
	"syscall"
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
		user32 := syscall.NewLazyDLL("user32.dll")
		proc := user32.NewProc("ExitWindowsEx")
		ret, _, err := proc.Call(0x05, 0) // EWX_SHUTDOWN | EWX_FORCE
		if ret == 0 {
			return fmt.Errorf("ExitWindowsEx failed: %v", err)
		}
	case "restart":
		user32 := syscall.NewLazyDLL("user32.dll")
		proc := user32.NewProc("ExitWindowsEx")
		ret, _, err := proc.Call(0x06, 0) // EWX_REBOOT | EWX_FORCE
		if ret == 0 {
			return fmt.Errorf("ExitWindowsEx failed: %v", err)
		}
	case "sleep":
		user32 := syscall.NewLazyDLL("user32.dll")
		proc := user32.NewProc("SetSuspendState")
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
