//go:build darwin || linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"quickdock/internal/sysutil"
)

// ScanInstalledApps 扫描已安装应用（非 Windows 实现）。
//   - darwin：扫描 /Applications 与 ~/Applications 下的 .app 包
//   - linux：暂返回空列表（.desktop 解析待补）
//
// 图标提取依赖 ExtractIconBase64，非 Windows 上其底层实现为空，故图标字段为空串。
func ScanInstalledApps() ([]InstalledApp, error) {
	if runtime.GOOS != "darwin" {
		return []InstalledApp{}, nil
	}

	roots := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "Applications"))
	}

	seen := make(map[string]bool)
	apps := make([]InstalledApp, 0, 64)
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if filepath.Ext(name) != ".app" {
				continue
			}
			base := strings.TrimSuffix(name, ".app")
			if isNoise(base) {
				continue
			}
			key := strings.ToLower(base)
			if seen[key] {
				continue
			}
			seen[key] = true
			apps = append(apps, InstalledApp{
				Name:       base,
				Path:       filepath.Join(root, name),
				Category:   "应用程序",
				IconBase64: ExtractIconBase64(filepath.Join(root, name)),
			})
		}
	}

	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	return apps, nil
}

// launchAppPath 非 Windows 启动策略：交由系统 open / xdg-open 处理。
// darwin 的 .app 目录用 open -a 启动（等同双击），其余按默认关联程序打开。
func launchAppPath(appPath string) error {
	if runtime.GOOS == "darwin" {
		if err := sysutil.Command("open", "-a", appPath).Start(); err != nil {
			return fmt.Errorf("open 启动失败: %v", err)
		}
		return nil
	}
	if err := sysutil.Command("xdg-open", appPath).Start(); err != nil {
		return fmt.Errorf("xdg-open 启动失败: %v", err)
	}
	return nil
}
