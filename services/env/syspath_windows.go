//go:build windows

package env

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	winreg "golang.org/x/sys/windows/registry"
)

// sysRegisterPath 把 dir 加入当前用户（HKCU\Environment）的 PATH 环境变量，幂等（已存在则跳过），
// 并广播 WM_SETTINGCHANGE 使新进程生效。仅作用于便携版本，无需管理员权限。
func sysRegisterPath(dir string) error {
	if dir == "" {
		return nil
	}
	k, err := openUserEnv()
	if err != nil {
		return err
	}
	defer k.Close()

	val, typ := readPathValue(k)
	if containsPath(val, dir) {
		return nil
	}
	newVal := dir
	if val != "" {
		newVal = val + ";" + dir
	}
	if err := writePathValue(k, newVal, typ); err != nil {
		return err
	}
	broadcastEnvChange()
	return nil
}

// sysUnregisterPath 从当前用户 PATH 中移除 dir（若存在）。
func sysUnregisterPath(dir string) error {
	if dir == "" {
		return nil
	}
	k, err := openUserEnv()
	if err != nil {
		return err
	}
	defer k.Close()

	val, typ := readPathValue(k)
	if !containsPath(val, dir) {
		return nil
	}
	if err := writePathValue(k, removePath(val, dir), typ); err != nil {
		return err
	}
	broadcastEnvChange()
	return nil
}

func openUserEnv() (winreg.Key, error) {
	k, err := winreg.OpenKey(winreg.CURRENT_USER, `Environment`, winreg.READ|winreg.WRITE|winreg.SET_VALUE)
	if err != nil {
		// 键不存在则创建（用户级写入无需管理员）
		k, _, err := winreg.CreateKey(winreg.CURRENT_USER, `Environment`, winreg.WRITE|winreg.SET_VALUE)
		return k, err
	}
	return k, nil
}

// readPathValue 读取 PATH 的原始字符串与其类型（缺省 REG_SZ）。
func readPathValue(k winreg.Key) (string, uint32) {
	_, typ, err := k.GetValue("PATH", nil)
	if err != nil && typ != winreg.SZ && typ != winreg.EXPAND_SZ {
		// 键缺失或类型异常：尝试直接读字符串，失败则返回空
		if s, _, e2 := k.GetStringValue("PATH"); e2 == nil {
			return s, winreg.SZ
		}
		return "", winreg.SZ
	}
	s, _, e2 := k.GetStringValue("PATH")
	if e2 != nil {
		return "", winreg.SZ
	}
	return s, typ
}

func writePathValue(k winreg.Key, val string, typ uint32) error {
	if typ == winreg.EXPAND_SZ {
		return k.SetExpandStringValue("PATH", val)
	}
	return k.SetStringValue("PATH", val)
}

// sysReadPath 读取当前用户 PATH 的原始字符串与是否含环境变量占位符（REG_EXPAND_SZ）。
// 占位符（如 %USERPROFILE%）原样返回，不展开为绝对路径——保证 UI 能正确回显系统 PATH。
func sysReadPath() (value string, expand bool) {
	k, err := openUserEnv()
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, typ := readPathValue(k)
	return v, typ == winreg.EXPAND_SZ
}

func splitPath(s string) []string {
	parts := strings.Split(s, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsPath(path, dir string) bool {
	dl := strings.ToLower(filepath.Clean(dir))
	for _, p := range splitPath(path) {
		if strings.ToLower(filepath.Clean(p)) == dl {
			return true
		}
	}
	return false
}

func removePath(path, dir string) string {
	dl := strings.ToLower(filepath.Clean(dir))
	out := make([]string, 0)
	for _, p := range splitPath(path) {
		if strings.ToLower(filepath.Clean(p)) == dl {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}

// broadcastEnvChange 广播 WM_SETTINGCHANGE，通知 Explorer 等进程刷新环境变量。
// 失败不影响写入结果（写入已持久化，新进程仍会读到）。
func broadcastEnvChange() {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	if proc == nil {
		return
	}
	ptr, _ := syscall.UTF16PtrFromString("Environment")
	proc.Call(
		uintptr(0xFFFF),       // HWND_BROADCAST
		uintptr(0x001A),       // WM_SETTINGCHANGE
		0,
		uintptr(unsafe.Pointer(ptr)),
		uintptr(0x0002),       // SMTO_ABORTIFHUNG
		uintptr(5000),
		0,
	)
}
