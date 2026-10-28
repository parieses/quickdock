//go:build windows

package platform

import (
	"golang.org/x/sys/windows/registry"
)

// readInstallerDataDir 读取安装器在首次启动前写入的、用户级（HKCU）数据目录覆盖：
// `Software\QuickDock\DataDir`。
//
// 它只作为「一次性种子」：应用首次启动、且用户尚未在应用内自定义时，
// 由 loadDataDirOverride 把它固化进用户配置 quickdock.json，之后完全由应用自身管理，
// 安装器键不再参与（卸载时一并清除，但不触碰用户数据目录本身）。
//
// 用 HKCU 而非 HKLM：与安装器写 InstallDir 的既有做法一致；在常见的「同账号提权」安装下
// 提权进程与运行进程共享同一 HKCU，且对「按用户安装（无 UAC）」天然可写。HKCU 无 32/64 位分裂，
// 无需 SetRegView。
func readInstallerDataDir() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\QuickDock`, registry.READ)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("DataDir")
	if err != nil || v == "" {
		return ""
	}
	return v
}
