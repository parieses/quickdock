package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quickdock/internal/platform"
)

// ===== System commands =====

// ExecuteSystemCommand executes a system command (lock/shutdown/restart/sleep/emptytrash)
func (a *AppService) ExecuteSystemCommand(cmd string) *ApiResult {
	if err := platform.RunSystemCommand(cmd); err != nil {
		return Fail(fmt.Errorf("ExecuteSystemCommand: %v", err))
	}
	return Ok(nil)
}

// RevealInExplorer 在文件资源管理器中定位目标路径（目录直接打开，文件高亮选中）
func (a *AppService) RevealInExplorer(path string) *ApiResult {
	if err := platform.RevealInExplorer(path); err != nil {
		return Fail(fmt.Errorf("RevealInExplorer: %v", err))
	}
	return Ok(nil)
}

// LogsInfo 全局日志卡片数据：目录、当前日志文件与最近行
type LogsInfo struct {
	Dir         string   `json:"dir"`
	CurrentFile string   `json:"currentFile"`
	RecentLines []string `json:"recentLines"`
}

// GetLogsInfo 返回日志目录、当前日志文件路径与最近 N 行（默认 50）。
// 设置页「日志」卡片用：用户自诊断或一键打开目录给 AI 深度分析。
func (a *AppService) GetLogsInfo(maxLines int) *ApiResult {
	dir := filepath.Join(platform.DefaultDataDir(), "logs")
	day := time.Now().Format("2006-01-02")
	current := filepath.Join(dir, "quickdock-"+day+".log")
	if maxLines <= 0 {
		maxLines = 50
	}
	var lines []string
	if data, err := os.ReadFile(current); err == nil {
		all := strings.Split(string(data), "\n")
		// 去掉尾部空行
		for len(all) > 0 && all[len(all)-1] == "" {
			all = all[:len(all)-1]
		}
		if start := len(all) - maxLines; start > 0 {
			all = all[start:]
		}
		// 倒序（最新在前）：预览一眼看到最近事件，不用滚到底
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
		lines = all
	}
	return Ok(&LogsInfo{Dir: dir, CurrentFile: current, RecentLines: lines})
}

// OpenLogsDir 在文件资源管理器中打开日志目录
func (a *AppService) OpenLogsDir() *ApiResult {
	dir := filepath.Join(platform.DefaultDataDir(), "logs")
	if err := platform.RevealInExplorer(dir); err != nil {
		return Fail(fmt.Errorf("OpenLogsDir: %v", err))
	}
	return Ok(nil)
}
