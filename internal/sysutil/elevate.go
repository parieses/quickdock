package sysutil

import "errors"

// ErrElevationCancelled 用户拒绝了 UAC / 授权框（或被组策略拦下）。
// 单独区分出来，是为了让调用方能提示「未获授权，可重试或手动处理」，
// 而不是把它当成「程序坏了」。
var ErrElevationCancelled = errors.New("用户取消了管理员授权")

// ErrElevationUnsupported 当前平台没有可用的图形化提权通道，
// 调用方应退化为「提示用户手动以管理员身份完成该操作」。
var ErrElevationUnsupported = errors.New("当前平台不支持自动提权")
