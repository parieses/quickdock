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

// GetSystemStatus 返回系统资源概览（CPU / 内存 / 磁盘 / IP）
func (a *AppService) GetSystemStatus() *ApiResult {
	st, err := platform.GetSystemStatus()
	if err != nil {
		return Fail(fmt.Errorf("GetSystemStatus: %v", err))
	}
	return Ok(st)
}
