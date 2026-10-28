//go:build windows

package platform

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

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

// platformReadUserEnvVars 读取当前用户（HKCU\Environment）的全部环境变量。
func platformReadUserEnvVars() (map[string]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		v, _, err := k.GetStringValue(name)
		if err != nil {
			continue // 非字符串类型（如 REG_BINARY）跳过
		}
		out[name] = v
	}
	return out, nil
}

// platformSetUserEnvVar 写入当前用户环境变量；value 为空表示删除。
// 写入后广播 WM_SETTINGCHANGE，使新启动的进程能读到新值。
func platformSetUserEnvVar(name, value string) error {
	if name == "" {
		return nil
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		if err != registry.ErrNotExist {
			return err
		}
		k, _, err = registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
		if err != nil {
			return err
		}
	}
	defer k.Close()
	if value == "" {
		if err := k.DeleteValue(name); err != nil && err != registry.ErrNotExist {
			return err
		}
	} else if err := k.SetStringValue(name, value); err != nil {
		return err
	}
	broadcastEnvChange()
	return nil
}
