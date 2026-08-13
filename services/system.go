package services

import (
	"fmt"

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
