//go:build !windows

package services

import "fmt"

// writeClipboardPNG 非 Windows 下的占位实现（签名与 _windows.go 保持一致，
// 否则共享代码里调用它的地方在跨平台编译时报 undefined）。
func writeClipboardPNG([]byte) error {
	return fmt.Errorf("当前平台暂不支持把图片写入剪贴板")
}
