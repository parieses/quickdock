//go:build !production

package platform

import (
	"os"
	"path/filepath"
)

// DefaultDataDir 返回开发版数据目录 (~/.quickdock_dev)
func DefaultDataDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ".quickdock_dev"
	}
	return filepath.Join(home, ".quickdock_dev")
}

// DefaultConfigDir 返回开发版配置目录 (%APPDATA%\QuickDock_dev)
func DefaultConfigDir() string {
	d := os.Getenv("APPDATA")
	if d == "" {
		d = os.Getenv("LOCALAPPDATA")
	}
	return d + "\\QuickDock_dev"
}
