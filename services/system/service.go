// Package system 系统级服务
// 承载原 AppService 的系统命令/日志目录领域方法。
package system

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quickdock/internal/platform"
	"quickdock/services"
)

// SystemService 承载系统命令领域方法。
// 本域不依赖宿主 DB，仅调 platform 能力；App 回指宿主以保持统一门面形态。
type SystemService struct {
	App *services.AppService
}

// NewSystemService 创建系统门面服务，App 为宿主服务引用。
func NewSystemService(app *services.AppService) *SystemService {
	return &SystemService{App: app}
}

// LogsInfo 全局日志卡片数据：目录、当前日志文件与最近行
type LogsInfo struct {
	Dir         string   `json:"dir"`
	CurrentFile string   `json:"currentFile"`
	RecentLines []string `json:"recentLines"`
}

// ===== System commands =====

// ExecuteSystemCommand executes a system command (lock/shutdown/restart/sleep/emptytrash)
func (s *SystemService) ExecuteSystemCommand(cmd string) *services.ApiResult {
	if err := platform.RunSystemCommand(cmd); err != nil {
		return services.Fail(fmt.Errorf("ExecuteSystemCommand: %v", err))
	}
	return services.Ok(nil)
}

// RevealInExplorer 在文件资源管理器中定位目标路径（目录直接打开，文件高亮选中）
func (s *SystemService) RevealInExplorer(path string) *services.ApiResult {
	if err := platform.RevealInExplorer(path); err != nil {
		return services.Fail(fmt.Errorf("RevealInExplorer: %v", err))
	}
	return services.Ok(nil)
}

// GetLogsInfo 返回日志目录、当前日志文件路径与最近 N 行（默认 50）。
// 设置页「日志」卡片用：用户自诊断或一键打开目录给 AI 深度分析。
func (s *SystemService) GetLogsInfo(maxLines int) *services.ApiResult {
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
		// 倒序（最新在前）
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
		lines = all
	}
	return services.Ok(&LogsInfo{Dir: dir, CurrentFile: current, RecentLines: lines})
}

// OpenLogsDir 在文件资源管理器中打开日志目录
func (s *SystemService) OpenLogsDir() *services.ApiResult {
	dir := filepath.Join(platform.DefaultDataDir(), "logs")
	if err := platform.RevealInExplorer(dir); err != nil {
		return services.Fail(fmt.Errorf("OpenLogsDir: %v", err))
	}
	return services.Ok(nil)
}
