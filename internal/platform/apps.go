package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// InstalledApp 已安装应用信息
type InstalledApp struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Category   string `json:"category"`    // 开始菜单子目录名
	IconBase64 string `json:"iconBase64"`  // 应用图标（base64 data URL，空则无图标）
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

func getStartMenuDirs() []string {
	var dirs []string
	if progData := os.Getenv("ProgramData"); progData != "" {
		dirs = append(dirs, filepath.Join(progData, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		dirs = append(dirs, filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	return dirs
}

// systemDir 返回 System32 目录路径
func systemDir() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32")
	}
	return `C:\Windows\System32`
}

// builtinWindowsApps 内置 Windows 应用（中文名 + 英文 exe）
// 确保记事本/计算器等常用系统应用始终可搜索到
var builtinWindowsApps = []struct {
	name    string
	exeName string
}{
	{"记事本", "notepad.exe"},
	{"计算器", "calc.exe"},
	{"画图", "mspaint.exe"},
	{"截图工具", "SnippingTool.exe"},
	{"任务管理器", "taskmgr.exe"},
	{"文件资源管理器", "explorer.exe"},
	{"注册表编辑器", "regedit.exe"},
	{"命令提示符", "cmd.exe"},
	{"控制面板", "control.exe"},
	{"资源监视器", "resmon.exe"},
	{"磁盘清理", "cleanmgr.exe"},
	{"字符映射表", "charmap.exe"},
	{"远程桌面连接", "mstsc.exe"},
	{"系统信息", "msinfo32.exe"},
	{"任务计划程序", "taskschd.msc"},
	{"服务管理器", "services.msc"},
	{"事件查看器", "eventvwr.msc"},
	{"设备管理器", "devmgmt.msc"},
	{"磁盘管理", "diskmgmt.msc"},
	{"组策略编辑器", "gpedit.msc"},
	{"组件服务", "dcomcnfg.exe"},
	{"性能监视器", "perfmon.msc"},
	{"证书管理器", "certmgr.msc"},
	{"数据源(ODBC)", "odbcad32.exe"},
	{"Telnet 客户端", "telnet.exe"},
	{"写字板", "write.exe"},
	{"步骤记录器", "psr.exe"},
	{"放大镜", "magnify.exe"},
	{"屏幕键盘", "osk.exe"},
	{"讲述人", "narrator.exe"},
}

// addBuiltinApps 添加内置 Windows 应用（去重：跳过开始菜单已有的同名应用）
func addBuiltinApps(apps []InstalledApp, seen map[string]bool) []InstalledApp {
	sysDir := systemDir()
	for _, b := range builtinWindowsApps {
		// 中文名和英文 exe 名都去重
		if seen[strings.ToLower(b.name)] || seen[strings.ToLower(b.exeName)] {
			continue
		}
		fullPath := filepath.Join(sysDir, b.exeName)
		// 检查文件是否存在
		if _, err := os.Stat(fullPath); err != nil {
			continue
		}
		seen[strings.ToLower(b.name)] = true
		seen[strings.ToLower(b.exeName)] = true

		// 提取图标
		icon := ExtractIconBase64(fullPath)

		apps = append(apps, InstalledApp{
			Name:       b.name,
			Path:       fullPath,
			Category:   "系统工具",
			IconBase64: icon,
		})
	}
	return apps
}

// ScanInstalledApps 扫描 Windows 开始菜单中的已安装应用
// 收集 .lnk 快捷方式，使用文件名（不含扩展名）作为应用名
// 子目录名作为分类（如 "Accessories", "Administrative Tools"）
// 同时添加内置 Windows 应用（记事本/计算器等）
func ScanInstalledApps() ([]InstalledApp, error) {
	dirs := getStartMenuDirs()
	if len(dirs) == 0 {
		return nil, fmt.Errorf("无法定位开始菜单目录")
	}

	seen := make(map[string]bool) // 按名称去重（不区分大小写）
	var apps []InstalledApp

	for _, root := range dirs {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(info.Name()))
			if ext != ".lnk" {
				return nil
			}
			name := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
			if isNoise(name) {
				return nil
			}
			key := strings.ToLower(name)
			if seen[key] {
				return nil
			}
			seen[key] = true

			// 计算分类
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			category := "其他"
			if rel != "." && rel != "" {
				category = rel
			}

			// 提取真实应用图标
			icon := ExtractIconBase64(path)

			apps = append(apps, InstalledApp{
				Name:       name,
				Path:       path,
				Category:   category,
				IconBase64: icon,
			})
			return nil
		})
		if err != nil {
			fmt.Printf("QuickDock: 扫描开始菜单 %s 失败: %v\n", root, err)
		}
	}

	// 添加内置 Windows 应用
	apps = addBuiltinApps(apps, seen)

	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})

	return apps, nil
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

// LaunchApp 通过 ShellExecute 启动应用
func LaunchApp(appPath string) error {
	return windows.ShellExecute(0,
		windows.StringToUTF16Ptr("open"),
		windows.StringToUTF16Ptr(appPath),
		nil, nil, windows.SW_SHOWNORMAL)
}
