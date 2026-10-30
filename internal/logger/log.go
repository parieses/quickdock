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
	mu     sync.Mutex
	f      *os.File
	logDir string
	curDay string
	size   int64
	// 插件专属日志文件（与主日志分离，便于插件作者单独排查）
	pf          *os.File
	pCurDay     string
	pSize       int64
	initialized bool
)

// MaxFileSize 单文件超过后自动滚动到新文件（保留同名不覆盖语义，日期变化即切换）
const MaxFileSize = 20 << 20 // 20 MB

// maxLogShards 单日最多滚动出多少个分片（含不带序号的首个）。
// 全部分片写满后继续往最后一个追加，故单日总量上界为
// maxLogShards*MaxFileSize，不会像早期版本那样单个文件只增不减。
const maxLogShards = 5

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

// pickLogName 选出当日应当写入的日志文件：首个写满就滚到 -2、-3…。
//
// 为什么必须滚动：早期版本超限后仍以固定文件名 + O_APPEND 重开同一个文件，
// size 被重置成当前大小（依旧超限），于是「超限」判定对之后每一条日志都成立——
// 每条都要白做一遍 Close+Open+Stat，而文件只增不减，最终能把磁盘撑爆。
// 分片名仍带 quickdock-/plugin- 前缀，cleanOld 的按前缀回收照旧生效。
func pickLogName(prefix, day string) string {
	name := prefix + day + ".log"
	for i := 2; i <= maxLogShards && logFull(filepath.Join(logDir, name)); i++ {
		name = fmt.Sprintf("%s%s-%d.log", prefix, day, i)
	}
	return name
}

// logFull 判断日志文件是否已写到上限；文件不存在视为未满。
func logFull(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() >= MaxFileSize
}

func openLocked() {
	// 切换目录/日期前先关闭旧文件，避免句柄泄漏（Windows 下还会锁文件）
	if f != nil {
		_ = f.Close()
		f = nil
	}
	now := time.Now()
	day := now.Format("2006-01-02")
	name := pickLogName("quickdock-", day)
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
	// dev 版同时写一份到 stderr（见 tee_dev.go）：文件是排障的事实来源，
	// 但「启动阶段就失败」时用户需要一个当场就能看见的提示，否则只会看到
	// 窗口没起来、控制台空白，得靠翻 log 才知道原委。
	if teeStderr {
		fmt.Fprint(os.Stderr, "QuickDock: "+line)
	}
	// 只给 E 级强制落盘：崩溃排障要看的是错误，而 I/W 级正好落在最热的路径上
	// （插件逐行转发子进程 stdout、监控高频轮询），每条都 fsync 一次会把整个
	// 宿主拖慢。其余级别交给 OS 回写，代价是最多丢崩溃前的最后几条。
	if level == "E" {
		_ = f.Sync()
	}
}

// I/W/E 便捷封装
func I(format string, args ...interface{}) { Logf("I", format, args...) }
func W(format string, args ...interface{}) { Logf("W", format, args...) }
func E(format string, args ...interface{}) { Logf("E", format, args...) }

// Trunc 把可能很长的外部文本（HTTP 响应、子进程输出、模型错误页）压成适合写入日志的一行：
// 换行/回车替换为空格，避免破坏「一行一条日志」的结构；按 rune 截断，不会切坏中文。
func Trunc(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	if maxRunes <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…(truncated)"
}

// ===== 插件专属日志 =====
//
// 插件日志与主日志分离到独立文件 <dataDir>/logs/plugin-YYYYMMDD.log，
// 并以 [plugin:<id>] 前缀标注来源，便于插件作者单独排查而不被宿主日志淹没。
// 设计要点：
//   - 复用同一把 mu，避免插件文件与主文件并发打开/滚动的竞态（日志写操作极短，性能影响可忽略）。
//   - 未初始化时回退到 os.Stderr（与 Logf 一致），保证任何情况下调用不阻塞主流程。
//   - 落盘策略与主日志一致（仅 E 级 fsync）。插件逐行转发子进程 stdout 是
//     最热的调用路径，每条都 fsync 会实打实拖慢宿主。

// openPluginLocked 打开/滚动插件日志文件；调用方须持有 mu。
func openPluginLocked() {
	if pf != nil {
		_ = pf.Close()
		pf = nil
	}
	now := time.Now()
	day := now.Format("2006-01-02")
	name := pickLogName("plugin-", day)
	var err error
	pf, err = os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
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
	// 与主日志同策略：只有 E 级 fsync，I/W 级交给 OS 回写。
	if level == "E" {
		_ = pf.Sync()
	}
}

// PluginI/PluginW/PluginE 插件日志便捷封装
func PluginI(pluginID, format string, args ...interface{}) { PluginLog(pluginID, "I", format, args...) }
func PluginW(pluginID, format string, args ...interface{}) { PluginLog(pluginID, "W", format, args...) }
func PluginE(pluginID, format string, args ...interface{}) { PluginLog(pluginID, "E", format, args...) }
