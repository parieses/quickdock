package platform

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// InstalledApp 已安装应用信息
type InstalledApp struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Category   string `json:"category"`   // 开始菜单子目录名 / mac 为 Applications 子目录
	IconBase64 string `json:"iconBase64"` // 应用图标（base64 data URL，空则无图标）
}

// 需要跳过的系统级/噪音快捷方式关键词
var noiseApps = map[string]bool{
	"uninstall": true, "卸载": true, "help": true, "readme": true,
	"readme.txt": true, "release notes": true, "release_note": true,
	"changelog": true, "what's new": true, "license": true, "licence": true,
}

func isNoise(name string) bool {
	lower := strings.TrimSpace(strings.ToLower(name))
	if noiseApps[lower] {
		return true
	}
	prefixes := []string{"uninstall", "卸载", "help", "readme"}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// ---- 缓存（包级别，避免每次搜索都扫描磁盘）----
var (
	appsCache   []InstalledApp
	appsCacheMu sync.Mutex
)

// GetCachedApps 获取缓存的已安装应用列表（首次调用时扫描）
func GetCachedApps() ([]InstalledApp, error) {
	appsCacheMu.Lock()
	defer appsCacheMu.Unlock()
	if appsCache != nil {
		return appsCache, nil
	}
	a, err := ScanInstalledApps()
	if err != nil {
		appsCache = []InstalledApp{}
	} else {
		appsCache = a
	}
	return appsCache, err
}

// ResetAppsCache 清除缓存（用于重新扫描）
func ResetAppsCache() {
	appsCacheMu.Lock()
	defer appsCacheMu.Unlock()
	appsCache = nil
}

// LaunchApp 启动应用。
// 安全校验（拒绝 URL / shell 元字符 / 不存在的路径）为跨平台通用逻辑，
// 实际的按扩展名启动策略由平台相关实现 launchAppPath 完成。
//
// 背景：Windows 上 Ctrl+K 启动的「已安装应用」原先直接走 windows.ShellExecute，
// 子进程未脱离 QuickDock 进程组，主程序重启/退出时会被一并带走；
// 现由 windows 侧实现改为解析 .lnk → CreateProcess + sysutil.Detach 脱离启动。
func LaunchApp(appPath string) error {
	if appPath == "" {
		return fmt.Errorf("应用路径不能为空")
	}
	// 基本安全验证：拒绝 URL 或命令行注入特征
	if strings.Contains(appPath, "http://") || strings.Contains(appPath, "https://") {
		return fmt.Errorf("拒绝 URL 路径，请使用 OpenURL")
	}
	if strings.ContainsAny(appPath, "|&;<>`$") {
		return fmt.Errorf("应用路径包含非法字符")
	}
	// 文件存在性检查（不是必需的，但可以提供更好的错误提示）
	if _, err := os.Stat(appPath); err != nil {
		return fmt.Errorf("应用路径不存在: %s", appPath)
	}
	return launchAppPath(appPath)
}
