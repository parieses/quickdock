//go:build !darwin

package platform

// ClearSelfQuarantine 非 darwin 平台无 quarantine 概念，no-op。
func ClearSelfQuarantine() {}
