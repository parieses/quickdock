// Package diag 崩溃与日志诊断服务
// 承载原 AppService 的崩溃现场/日志查看领域方法（ListCrashFiles/ReadLogFile 等）。
package diag

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
)

// DiagService 承载崩溃与日志诊断方法。
// 本域不依赖宿主 DB，仅操作日志目录；App 回指宿主以保持统一门面形态。
type DiagService struct {
	App *services.AppService
}

// NewDiagService 创建诊断门面服务，App 为宿主服务引用。
func NewDiagService(app *services.AppService) *DiagService {
	return &DiagService{App: app}
}

// CrashFile 崩溃/异常文件摘要
type CrashFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"` // 修改时间，格式 2006-01-02 15:04:05
	Kind string `json:"kind"` // panic（Go 崩溃） / js（前端异常） / other
}

// LogFile 日志文件摘要
type LogFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"` // 修改时间，格式 2006-01-02 15:04:05
}

func crashKind(name string) string {
	switch {
	case strings.HasPrefix(name, "panic-"):
		return "panic"
	case strings.HasPrefix(name, "js-"):
		return "js"
	}
	return "other"
}

func logDir() string {
	return filepath.Join(platform.DefaultDataDir(), "logs")
}

// ===== 日志占用 =====

// LogStorage 日志目录的占用概览。
// 日志与崩溃分开统计：两者的清理入口本来就不同（崩溃有独立的「清空崩溃记录」），
// 混成一个数字会让用户点错按钮。
type LogStorage struct {
	Dir        string `json:"dir"`
	TotalSize  int64  `json:"totalSize"`
	LogSize    int64  `json:"logSize"`
	CrashSize  int64  `json:"crashSize"`
	LogCount   int    `json:"logCount"`
	CrashCount int    `json:"crashCount"`
	Oldest     string `json:"oldest"` // 最早一份日志的日期（2006-01-02），无日志时为空
	Newest     string `json:"newest"`
	// RemovableSize 按「30 天以前」这条宿主自动清理线算出的可释放量，
	// 用来告诉用户「一键清理大概能腾出多少」，而不是让他猜。
	RemovableSize int64 `json:"removableSize"`
}

// GetLogStorage 统计日志目录占用（日志 + 崩溃记录）。
func (s *DiagService) GetLogStorage() *services.ApiResult {
	dir := logDir()
	st := LogStorage{Dir: dir}
	cutoff := time.Now().AddDate(0, 0, -30)

	collect := func(d string) (int64, int, string, string, int64) {
		var size int64
		var count int
		var oldest, newest string
		var removable int64
		entries, err := os.ReadDir(d)
		if err != nil {
			return 0, 0, "", "", 0
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			size += info.Size()
			count++
			day := info.ModTime().Format("2006-01-02")
			if oldest == "" || day < oldest {
				oldest = day
			}
			if newest == "" || day > newest {
				newest = day
			}
			if info.ModTime().Before(cutoff) {
				removable += info.Size()
			}
		}
		return size, count, oldest, newest, removable
	}

	ls, lc, lo, ln, lr := collect(dir)
	st.LogSize, st.LogCount, st.Oldest, st.Newest, st.RemovableSize = ls, lc, lo, ln, lr

	if cd := logger.CrashDir(); cd != "" {
		cs, cc, co, cn, cr := collect(cd)
		st.CrashSize, st.CrashCount = cs, cc
		st.RemovableSize += cr
		if st.Oldest == "" || (co != "" && co < st.Oldest) {
			st.Oldest = co
		}
		if cn > st.Newest {
			st.Newest = cn
		}
	}
	st.TotalSize = st.LogSize + st.CrashSize
	return services.Ok(st)
}

// ClearLogsResult 一次清理的结果
type ClearLogsResult struct {
	Removed int   `json:"removed"` // 删除的文件数
	Freed   int64 `json:"freed"`   // 释放的字节数
}

// ClearLogs 删除 keepDays 天之前的日志文件（keepDays<=0 表示除今天外全部）。
// includeCrash 为真时崩溃记录一并按同一条时间线清理。
//
// 今天的日志永远不删：logger 正持有它的写句柄，删掉只是把文件从目录里摘掉，
// 磁盘空间要等进程退出才真正释放，用户界面上却会显示「已清理」，是假释放。
func (s *DiagService) ClearLogs(keepDays int, includeCrash bool) *services.ApiResult {
	today := time.Now().Format("2006-01-02")
	cutoff := time.Now()
	if keepDays > 0 {
		cutoff = cutoff.AddDate(0, 0, -keepDays)
	} else {
		// keepDays<=0：清掉今天之外的全部。cutoff 取明天，等价于「非今天即删」。
		cutoff = cutoff.AddDate(0, 0, 1)
	}

	var res ClearLogsResult
	dirs := []string{logDir()}
	if includeCrash {
		if cd := logger.CrashDir(); cd != "" {
			dirs = append(dirs, cd)
		}
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			day := info.ModTime().Format("2006-01-02")
			if day == today {
				continue // 正在写的那份留着
			}
			if !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(d, e.Name())); err == nil {
				res.Removed++
				res.Freed += info.Size()
			}
		}
	}
	logger.I("[diag] 清理日志：删除 %d 个文件，释放 %d 字节（keepDays=%d crash=%v）",
		res.Removed, res.Freed, keepDays, includeCrash)
	return services.Ok(res)
}

// ===== 崩溃与异常诊断 =====

// ReportFrontendError 前端 JS 异常上报（window.onerror / unhandledrejection）。
// 落盘到 <logs>/crash/js-*.log。
func (s *DiagService) ReportFrontendError(kind, message, stack, url string) *services.ApiResult {
	if strings.TrimSpace(message) == "" {
		return services.FailMsg("上报内容为空")
	}
	logger.ReportFrontend(kind, message, stack, url)
	return services.Ok(nil)
}

// ListCrashFiles 列出崩溃/异常记录（新的在前）。目录不存在时返回空列表而非错误。
func (s *DiagService) ListCrashFiles() *services.ApiResult {
	dir := logger.CrashDir()
	if dir == "" {
		return services.Ok([]CrashFile{})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return services.Ok([]CrashFile{})
	}
	list := make([]CrashFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		list = append(list, CrashFile{
			Name: e.Name(),
			Size: info.Size(),
			Time: info.ModTime().Format("2006-01-02 15:04:05"),
			Kind: crashKind(e.Name()),
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Time > list[j].Time })
	return services.Ok(list)
}

// ReadCrashFile 读取单个崩溃文件内容。仅允许 crash 目录下的纯文件名，防路径穿越。
func (s *DiagService) ReadCrashFile(name string) *services.ApiResult {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return services.FailMsg("非法文件名")
	}
	dir := logger.CrashDir()
	if dir == "" {
		return services.FailMsg("日志目录未初始化")
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.Base(name)))
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(data)
}

// ClearCrashFiles 清空崩溃/异常记录。
func (s *DiagService) ClearCrashFiles() *services.ApiResult {
	dir := logger.CrashDir()
	if dir == "" {
		return services.Ok(nil)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return services.Ok(nil)
	}
	for _, e := range entries {
		if !e.IsDir() {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return services.Ok(nil)
}

// ===== 日志查看器 =====

// ListLogFiles 列出日志目录下的 .log 文件（新的在前）。
// 同时包含 crash/ 子目录的崩溃记录，文件名以 "crash/" 前缀区分。
func (s *DiagService) ListLogFiles() *services.ApiResult {
	list := make([]LogFile, 0)
	collect := func(dir, prefix string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			list = append(list, LogFile{Name: prefix + e.Name(), Size: info.Size(), Time: info.ModTime().Format("2006-01-02 15:04:05")})
		}
	}
	collect(logDir(), "")
	if cd := logger.CrashDir(); cd != "" {
		collect(cd, "crash/")
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name > list[j].Name })
	return services.Ok(list)
}

// ReadLogFile 读取指定日志文件内容（按时间正序，最多 tail 行；tail<=0 表示全部）。
// 仅允许日志目录及其 crash 子目录下的相对路径（如 "crash/panic-xxx.log"），防路径穿越。
// logReadCache 对「未变更」的日志文件做整文件读缓存：跟随模式下前端每 2s 拉一次尾，
// 文件静止时反复 ReadFile + Split 纯属浪费。以 (mtime, size) 作为变更指纹，命中即返回
// 上次切好的行。键是带目录的全路径，日志文件数量很少，内存占用可忽略。
var (
	logReadCacheMu sync.Mutex
	logReadCache   = map[string]logReadEntry{}
)

type logReadEntry struct {
	modTime time.Time
	size    int64
	lines   []string
}

func (s *DiagService) ReadLogFile(name string, tail int) *services.ApiResult {
	name = filepath.Clean(name)
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "..") || !strings.HasSuffix(name, ".log") {
		return services.FailMsg("非法文件名")
	}
	full := filepath.Join(logDir(), name)
	if !strings.HasPrefix(full, logDir()+string(os.PathSeparator)) {
		return services.FailMsg("非法文件名")
	}
	fi, statErr := os.Stat(full)
	if statErr != nil {
		return services.FailMsg("读取失败: " + statErr.Error())
	}
	// 命中缓存（文件指纹未变）：直接返回上次切好的行，省掉磁盘读 + 全量 split
	logReadCacheMu.Lock()
	if c, ok := logReadCache[full]; ok && c.modTime.Equal(fi.ModTime()) && c.size == fi.Size() {
		lines := c.lines
		logReadCacheMu.Unlock()
		return services.Ok(lines)
	}
	logReadCacheMu.Unlock()

	data, err := os.ReadFile(full)
	if err != nil {
		return services.FailMsg("读取失败: " + err.Error())
	}
	all := strings.Split(string(data), "\n")
	for len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if tail > 0 && len(all) > tail {
		all = all[len(all)-tail:]
	}
	logReadCacheMu.Lock()
	logReadCache[full] = logReadEntry{modTime: fi.ModTime(), size: fi.Size(), lines: all}
	logReadCacheMu.Unlock()
	return services.Ok(all)
}
