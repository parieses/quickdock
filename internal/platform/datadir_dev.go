//go:build !production

package platform

import (
	"os"
	"path/filepath"
)

// DefaultDataDir 返回数据目录 (~/.quickdock)
// 开发版与正式版共用同一数据库，不再区分 _dev 目录。
func DefaultDataDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ".quickdock"
	}
	return filepath.Join(home, ".quickdock")
}

// DefaultConfigDir 返回配置目录 (%APPDATA%\QuickDock)
// 与正式版共用，彻底去掉 _dev 区分。
func DefaultConfigDir() string {
	d := os.Getenv("APPDATA")
	if d == "" {
		d = os.Getenv("LOCALAPPDATA")
	}
	return d + "\\QuickDock"
}
