// Package port 端口与进程管理门面服务。
package port

import (
	"quickdock/internal/sysutil"
	"quickdock/services"
)

// PortService 端口与进程管理绑定服务。
type PortService struct {
	App *services.AppService
}

// NewPortService 创建 PortService。
func NewPortService(app *services.AppService) *PortService {
	return &PortService{App: app}
}

// ListListeningPorts 列出本机监听端口及占用进程（Windows 支持，其余平台返回提示）。
func (s *PortService) ListListeningPorts() *services.ApiResult {
	return services.Wrap(sysutil.ListListeningPorts())
}

// KillProcess 结束指定 PID 的进程（含安全校验，Windows 支持）。
func (s *PortService) KillProcess(pid int) *services.ApiResult {
	if pid <= 0 {
		return services.FailMsg("无效的 PID")
	}
	if err := sysutil.KillProcess(pid); err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]any{"pid": pid, "success": true})
}
