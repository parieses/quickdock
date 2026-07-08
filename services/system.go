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
