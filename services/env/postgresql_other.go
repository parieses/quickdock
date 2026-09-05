//go:build !windows

package env

// isElevated 非 Windows 平台恒为 false（PostgreSQL 在其它平台无管理员令牌限制）。
func isElevated() bool { return false }
