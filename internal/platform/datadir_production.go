//go:build production

package platform

import (
	"os"
	"path/filepath"
)

// DefaultDataDir 返回正式版数据目录 (~/.quickdock)
func DefaultDataDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ".quickdock"
	}
	return filepath.Join(home, ".quickdock")
}

// DefaultConfigDir 返回正式版配置目录 (%APPDATA%\QuickDock)
func DefaultConfigDir() string {
	d := os.Getenv("APPDATA")
	if d == "" {
		d = os.Getenv("LOCALAPPDATA")
	}
	return d + "\\QuickDock"
}
