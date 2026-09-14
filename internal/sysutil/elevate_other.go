//go:build !windows && !darwin && !linux

package sysutil

import "time"

// IsElevated 其它平台未实现，保守返回 false。
func IsElevated() bool { return false }

// RunElevated 其它平台没有统一的图形化提权通道，明确报「不支持」，
// 由调用方退化为「提示用户手动完成」。
func RunElevated(exe string, args []string, timeout time.Duration) error {
	return ErrElevationUnsupported
}
