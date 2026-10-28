//go:build windows

package services

import (
	"bytes"
	"image/png"

	"quickdock/internal/platform"
)

// writeClipboardPNG 把 PNG 字节解码成内存图像后以 CF_DIB 写入剪贴板。
//
// 解码成 image.Image 再交给 platform 是刻意的：platform 侧只认「已解码的像素」，
// 由它统一构造 DIB（32bpp bottom-up，alpha 置 0xFF），避免这里再写一份格式细节。
func writeClipboardPNG(data []byte) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return platform.SetClipboardImageData(img)
}
