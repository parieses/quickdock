package plugin

import "errors"

// 插件系统错误码（-100 系列）
const (
	ErrCodeInitTimeout     = -10001
	ErrCodeExecuteTimeout  = -10002
	ErrCodeShutdownTimeout = -10003
	ErrCodePermissionDenied= -10010
	ErrCodePluginNotFound  = -10011
	ErrCodePluginCrashed   = -10012
	ErrCodeInvalidManifest = -10020
	ErrCodeUnsupportedRuntime = -10021
	ErrCodeHotkeyConflict  = -10030
	ErrCodeZipSlipDetected = -10040
)

// 预定义错误
var (
	ErrPluginNotFound      = errors.New("插件未加载")
	ErrPluginCrashed       = errors.New("插件进程已崩溃")
	ErrPermissionDenied    = errors.New("权限不足")
	ErrInvalidManifest     = errors.New("插件清单格式无效")
	ErrUnsupportedRuntime  = errors.New("不支持的 runtime")
	ErrHotkeyConflict      = errors.New("热键冲突，已被其他插件占用")
	ErrResponseTimeout     = errors.New("插件响应超时")
	ErrStdinWriteFailed    = errors.New("插件 stdin 写入失败")
	ErrZipSlipDetected     = errors.New("检测到 Zip Slip 攻击")
)
