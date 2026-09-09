package services

import "quickdock/internal/sysutil"

// ListListeningPorts 列出本机监听端口及占用进程（Windows 支持，其余平台返回提示）。
func (a *AppService) ListListeningPorts() *ApiResult {
	return wrap(sysutil.ListListeningPorts())
}

// KillProcess 结束指定 PID 的进程（含安全校验，Windows 支持）。
func (a *AppService) KillProcess(pid int) *ApiResult {
	if pid <= 0 {
		return FailMsg("无效的 PID")
	}
	if err := sysutil.KillProcess(pid); err != nil {
		return Fail(err)
	}
	return Ok(map[string]any{"pid": pid, "success": true})
}
