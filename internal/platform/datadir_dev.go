//go:build !production

package platform

import (
	"os"
	"path/filepath"
)

// defaultDataDirFallback 返回默认数据目录（~/.quickdock）。
// 开发版与正式版共用同一数据库，不再区分 _dev 目录。
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

// DefaultConfigDir 返回配置目录（%APPDATA%\QuickDock），与正式版共用。
func DefaultConfigDir() string {
	d := os.Getenv("APPDATA")
	if d == "" {
		d = os.Getenv("LOCALAPPDATA")
	}
	return d + "\\QuickDock"
}
