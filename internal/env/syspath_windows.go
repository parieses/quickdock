//go:build windows

package env

import (
	"os"
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

// sysSetEnvVar 设置当前用户（HKCU\Environment）的环境变量，并广播 WM_SETTINGCHANGE 使新进程生效。
// name 为空时直接返回；value 为空表示删除该变量。
// 供 ActiveEnvProvider 使用（如 Go 激活时写入版本专属的 GOPATH/GOENV），无需管理员权限。
func sysSetEnvVar(name, value string) error {
	if name == "" {
		return nil
	}
	k, err := openUserEnv()
	if err != nil {
		return err
	}
	defer k.Close()

	if value == "" {
		// 变量可能本就不存在，删除失败视为已达成目标
		_ = k.DeleteValue(name)
	} else if err := k.SetStringValue(name, value); err != nil {
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

// normalizePathEntry 把单个 PATH 条目规范成可比较的形式：展开环境变量占位符
// （如 %SystemRoot%）、去掉包裹引号、清理尾部分隔符。仅供比较/匹配使用，
// **不回写**——写回 PATH 时仍保留用户原始写法，避免把 %VAR% 全部展开。
func normalizePathEntry(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	// 用 os.Expand 自定义映射：已定义的变量展开，未定义的保留原样（os.ExpandEnv 会把它们变成空串）
	p = os.Expand(p, func(name string) string {
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return "%" + name + "%"
	})
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	if p == "" {
		return ""
	}
	if c := filepath.Clean(p); c != "." {
		p = c
	}
	return p
}

// pathEntryEqual 判断两个 PATH 条目是否指向同一目录（Windows 大小写不敏感）。
func pathEntryEqual(a, b string) bool {
	na, nb := normalizePathEntry(a), normalizePathEntry(b)
	if na == "" || nb == "" {
		return false
	}
	return strings.EqualFold(na, nb)
}

func containsPath(path, dir string) bool {
	if normalizePathEntry(dir) == "" {
		return false
	}
	for _, p := range splitPath(path) {
		if pathEntryEqual(p, dir) {
			return true
		}
	}
	return false
}

func removePath(path, dir string) string {
	if normalizePathEntry(dir) == "" {
		// dir 无效时不做任何删除，原样返回
		return strings.Join(splitPath(path), ";")
	}
	out := make([]string, 0)
	for _, p := range splitPath(path) {
		if pathEntryEqual(p, dir) {
			continue
		}
		out = append(out, p) // 保留原始写法（含 %VAR%）
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
		uintptr(0xFFFF), // HWND_BROADCAST
		uintptr(0x001A), // WM_SETTINGCHANGE
		0,
		uintptr(unsafe.Pointer(ptr)),
		uintptr(0x0002), // SMTO_ABORTIFHUNG
		uintptr(5000),
		0,
	)
}
