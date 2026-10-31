//go:build !windows

package platform

import (
	"fmt"
	"image"
)

// SetClipboardImageData 在 darwin / linux 上尚未实现。
func SetClipboardImageData(img image.Image) error {
	return fmt.Errorf("clipboard image write not implemented on this platform")
}

// SetClipboardDIBData 在 darwin / linux 上尚未实现（截图能力同样只在 Windows 落地）。
func SetClipboardDIBData(pix []byte, w, h int) error {
	return fmt.Errorf("clipboard DIB write not implemented on this platform")
}
