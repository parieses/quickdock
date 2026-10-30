package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	// appsCacheKey 上次实际扫描时记录的基准时间，用于判断来源目录是否发生过增删。
	appsCacheKey time.Time
)

// GetCachedApps 获取缓存的已安装应用列表（首次调用时扫描）。
// 缓存采用「目录树修改时间惰性失效」：每次调用时检查应用来源目录（开始菜单 /
// 应用程序目录）自上次扫描以来是否发生过增删（目录 mtime 变化），
// 仅当确实变化时才重新扫描磁盘；否则直接返回内存缓存，零开销。
// 前端在每次 Ctrl+K 唤起时都会调用本方法主动刷新，因此用户新安装 / 卸载软件后，
// 下一次唤起面板即可被发现，无需重启 QuickDock——
// 原先缓存首次扫描后永久复用，导致后续安装的软件永远搜不到。
func GetCachedApps() ([]InstalledApp, error) {
	appsCacheMu.Lock()
	defer appsCacheMu.Unlock()
	dirs := appSourceDirs()
	if appsCache != nil && !appDirsChanged(dirs, appsCacheKey) {
		return appsCache, nil
	}
	// 以「扫描起点」而非终点作为失效基准：若扫描期间或之后又有新装/卸载，
	// 其目录 mtime 会晚于该时刻，下次调用即触发重扫，避免「刚好错过」的边界问题。
	cutoff := time.Now()
	a, err := ScanInstalledApps()
	if err != nil {
		appsCache = []InstalledApp{}
	} else {
		appsCache = a
	}
	appsCacheKey = cutoff
	return appsCache, err
}

// appDirsChanged 判断任一监控目录自 since 之后是否发生增删（目录树 mtime 变化）。
func appDirsChanged(dirs []string, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	for _, d := range dirs {
		if treeNewerThan(d, since) {
			return true
		}
	}
	return false
}

// treeNewerThan 递归判断 dir 目录树中是否存在修改时间晚于 since 的目录。
// 仅 stat 目录（不读取文件内容），用于在廉价前提下捕捉任意嵌套层级的 .lnk/.app 增删。
// 新增软件会更新对应（可能深层）子目录的 mtime，故必须递归而非只看顶层。
func treeNewerThan(dir string, since time.Time) bool {
	info, err := os.Stat(dir)
	if err != nil {
		// 目录不存在 / 不可访问：该目录不会贡献任何应用，视为「无变化」。
		// 切勿返回 true，否则在 ~/Applications 缺失等常态场景下，每次 Ctrl+K
		// 都会触发一次全量重扫（walk + 图标提取），带来无谓开销。
		return false
	}
	if info.ModTime().After(since) {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if treeNewerThan(filepath.Join(dir, e.Name()), since) {
			return true
		}
	}
	return false
}

// ResetAppsCache 清除缓存（用于重新扫描）
func ResetAppsCache() {
	appsCacheMu.Lock()
	defer appsCacheMu.Unlock()
	appsCache = nil
	appsCacheKey = time.Time{}
}

// LaunchApp 启动应用。
// 安全校验（拒绝 URL / shell 元字符 / 不存在的路径）为跨平台通用逻辑，
// 实际的按扩展名启动策略由平台相关实现 launchAppPath 完成。
//
// Windows 上 Ctrl+K 启动的「已安装应用」原先直接走 windows.ShellExecute，
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
