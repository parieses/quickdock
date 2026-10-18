//go:build production

package platform

import (
	"os"
	"path/filepath"
)

// defaultDataDirFallback 返回正式版的默认数据目录（~/.quickdock）。
// 仅当未配置自定义覆盖（见 datadir_override.go）时生效。
func defaultDataDirFallback() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ".quickdock"
	}
	return filepath.Join(home, ".quickdock")
}

// DefaultDataDir 返回最终生效的数据目录（可能已被自定义覆盖）。
func DefaultDataDir() string {
	return resolveDataDir()
}

// DefaultConfigDir 返回正式版配置目录 (%APPDATA%\QuickDock)
func DefaultConfigDir() string {
	d := os.Getenv("APPDATA")
	if d == "" {
		d = os.Getenv("LOCALAPPDATA")
	}
	return d + "\\QuickDock"
}
