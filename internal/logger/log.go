// Package logger 提供 QuickDock 全局日志（按日文件 + 滚动 + 线程安全）。
//
// 定位：主程序与插件管理器排障的唯一事实来源。所有关键链路
// （启动/热键/插件加载与崩溃重启/ping 与 unresponsive/安装卸载/AI/更新）
// 统一经此记录到 <dataDir>/logs/quickdock-YYYYMMDD.log。
//
// 级别约定：I=常规 W=告警 E=错误。初始化失败时 Logf 回退到 os.Stderr，
// 保证任何情况下日志调用都不影响主流程。
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	mu          sync.Mutex
	f           *os.File
	logDir      string
	curDay      string
	size        int64
	// 插件专属日志文件（与主日志分离，便于插件作者单独排查）
	pf      *os.File
	pCurDay string
	pSize   int64
	initialized bool
)

// MaxFileSize 单文件超过后自动滚动到新文件（保留同名不覆盖语义，日期变化即切换）
const MaxFileSize = 20 << 20 // 20 MB

// Init 初始化日志目录并打开当日文件；幂等（同目录多次调用无副作用）。
func Init(dir string) {
	mu.Lock()
	defer mu.Unlock()
	if initialized && logDir == dir {
		return
	}
	if dir == "" {
		dir = "logs"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return // 目录创建失败：保持未初始化，Logf 回退 stderr
	}
	cleanOld(dir)
	logDir = dir
	initialized = true
	openLocked()
	openPluginLocked() // 插件日志文件与主日志一同打开
}

// Close 关闭当前日志文件（应用优雅退出时调用；不调用也由 OS 回收）。
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if f != nil {
		_ = f.Close()
		f = nil
	}
	if pf != nil {
		_ = pf.Close()
		pf = nil
	}
}

func openLocked() {
	// 切换目录/日期前先关闭旧文件，避免句柄泄漏（Windows 下还会锁文件）
	if f != nil {
		_ = f.Close()
		f = nil
	}
	now := time.Now()
	day := now.Format("2006-01-02")
	name := "quickdock-" + day + ".log"
	var err error
	f, err = os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		f = nil
		return
	}
	curDay = day
	if st, serr := f.Stat(); serr == nil {
		size = st.Size()
	}
}

// cleanOld 清理 30 天前的旧日志文件（含主日志 quickdock- 与插件日志 plugin-）
func cleanOld(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "quickdock-") && !strings.HasPrefix(name, "plugin-") {
			continue
		}
		if info, ierr := e.Info(); ierr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Logf 记录一条日志（级别字符串由调用方给：I/W/E）。
func Logf(level, format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	if !initialized || logDir == "" {
		fmt.Fprintf(os.Stderr, "QuickDock: "+format+"\n", args...)
		return
	}
	now := time.Now()
	if f == nil || curDay != now.Format("2006-01-02") || size > MaxFileSize {
		if f != nil {
			_ = f.Close()
			f = nil
		}
		openLocked()
	}
	if f == nil {
		fmt.Fprintf(os.Stderr, "QuickDock: "+format+"\n", args...)
		return
	}
	line := fmt.Sprintf("%s [%s] %s\n",
		now.Format("2006-01-02 15:04:05.000"), level, fmt.Sprintf(format, args...))
	n, _ := f.WriteString(line)
	size += int64(n)
	_ = f.Sync() // 及时落盘：排障场景要实时可读
}

// I/W/E 便捷封装
func I(format string, args ...interface{}) { Logf("I", format, args...) }
func W(format string, args ...interface{}) { Logf("W", format, args...) }
func E(format string, args ...interface{}) { Logf("E", format, args...) }

// ===== 插件专属日志 =====
//
// 插件日志与主日志分离到独立文件 <dataDir>/logs/plugin-YYYYMMDD.log，
// 并以 [plugin:<id>] 前缀标注来源，便于插件作者单独排查而不被宿主日志淹没。
// 设计要点：
//   - 复用同一把 mu，避免插件文件与主文件并发打开/滚动的竞态（日志写操作极短，性能影响可忽略）。
//   - 未初始化时回退到 os.Stderr（与 Logf 一致），保证任何情况下调用不阻塞主流程。
//   - 同样走 Sync 实时落盘，排障场景要实时可读。

// openPluginLocked 打开/滚动插件日志文件；调用方须持有 mu。
func openPluginLocked() {
	if pf != nil {
		_ = pf.Close()
		pf = nil
	}
	now := time.Now()
	day := now.Format("2006-01-02")
	var err error
	pf, err = os.OpenFile(filepath.Join(logDir, "plugin-"+day+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		pf = nil
		return
	}
	pCurDay = day
	if st, serr := pf.Stat(); serr == nil {
		pSize = st.Size()
	}
}

// PluginLog 写一条插件日志（level 由调用方给：I/W/E）。pluginID 作为前缀。
func PluginLog(pluginID, level, format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if !initialized || logDir == "" {
		fmt.Fprintf(os.Stderr, "QuickDock[plugin %s]: %s\n", pluginID, msg)
		return
	}
	now := time.Now()
	if pf == nil || pCurDay != now.Format("2006-01-02") || pSize > MaxFileSize {
		openPluginLocked()
	}
	if pf == nil {
		fmt.Fprintf(os.Stderr, "QuickDock[plugin %s]: %s\n", pluginID, msg)
		return
	}
	line := fmt.Sprintf("%s [%s] [plugin:%s] %s\n",
		now.Format("2006-01-02 15:04:05.000"), level, pluginID, msg)
	n, _ := pf.WriteString(line)
	pSize += int64(n)
	_ = pf.Sync()
}

// PluginI/PluginW/PluginE 插件日志便捷封装
func PluginI(pluginID, format string, args ...interface{}) { PluginLog(pluginID, "I", format, args...) }
func PluginW(pluginID, format string, args ...interface{}) { PluginLog(pluginID, "W", format, args...) }
func PluginE(pluginID, format string, args ...interface{}) { PluginLog(pluginID, "E", format, args...) }