package services

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// ===== 崩溃与异常诊断 =====
//
// 与 system.go 的 GetLogsInfo/OpenLogsDir 互补：那组面向「日常日志流水」，
// 这组面向「崩溃现场」——panic 与前端 JS 异常各落一个文件，便于用户自诊断或导出给 AI 分析。

// CrashFile 崩溃/异常文件摘要
type CrashFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"` // 修改时间，格式 2006-01-02 15:04:05
	Kind string `json:"kind"` // panic（Go 崩溃） / js（前端异常） / other
}

// ReportFrontendError 前端 JS 异常上报（window.onerror / unhandledrejection）。
// 落盘到 <logs>/crash/js-*.log。
func (a *AppService) ReportFrontendError(kind, message, stack, url string) *ApiResult {
	if strings.TrimSpace(message) == "" {
		return FailMsg("上报内容为空")
	}
	logger.ReportFrontend(kind, message, stack, url)
	return Ok(nil)
}

// ListCrashFiles 列出崩溃/异常记录（新的在前）。目录不存在时返回空列表而非错误。
func (a *AppService) ListCrashFiles() *ApiResult {
	dir := logger.CrashDir()
	if dir == "" {
		return Ok([]CrashFile{})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Ok([]CrashFile{})
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
	return Ok(list)
}

// ReadCrashFile 读取单个崩溃文件内容。仅允许 crash 目录下的纯文件名，防路径穿越。
func (a *AppService) ReadCrashFile(name string) *ApiResult {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return FailMsg("非法文件名")
	}
	dir := logger.CrashDir()
	if dir == "" {
		return FailMsg("日志目录未初始化")
	}
	return wrap(os.ReadFile(filepath.Join(dir, filepath.Base(name))))
}

// ClearCrashFiles 清空崩溃/异常记录。
func (a *AppService) ClearCrashFiles() *ApiResult {
	dir := logger.CrashDir()
	if dir == "" {
		return Ok(nil)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Ok(nil)
	}
	for _, e := range entries {
		if !e.IsDir() {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return Ok(nil)
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

// ===== 日志查看器 =====

// LogFile 日志文件摘要
type LogFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"` // 修改时间，格式 2006-01-02 15:04:05
}

func logDir() string {
	return filepath.Join(platform.DefaultDataDir(), "logs")
}

// ListLogFiles 列出日志目录下的 .log 文件（新的在前）。
// 同时包含 crash/ 子目录的崩溃记录，文件名以 "crash/" 前缀区分，便于在统一日志查看器中查看。
func (a *AppService) ListLogFiles() *ApiResult {
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
	return Ok(list)
}

// ReadLogFile 读取指定日志文件内容（按时间正序，最多 tail 行；tail<=0 表示全部）。
// 仅允许日志目录及其 crash 子目录下的相对路径（如 "crash/panic-xxx.log"），防路径穿越。
func (a *AppService) ReadLogFile(name string, tail int) *ApiResult {
	name = filepath.Clean(name)
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "..") || !strings.HasSuffix(name, ".log") {
		return FailMsg("非法文件名")
	}
	full := filepath.Join(logDir(), name)
	if !strings.HasPrefix(full, logDir()+string(os.PathSeparator)) {
		return FailMsg("非法文件名")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return FailMsg("读取失败: " + err.Error())
	}
	all := strings.Split(string(data), "\n")
	for len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if tail > 0 && len(all) > tail {
		all = all[len(all)-tail:]
	}
	return Ok(all)
}
