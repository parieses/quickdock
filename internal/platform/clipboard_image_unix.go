//go:build darwin || linux

package platform

import (
	"fmt"
	"image"
)

// SetClipboardImageData 在 darwin / linux 上尚未实现。
func SetClipboardImageData(img image.Image) error {
	return fmt.Errorf("clipboard image write not implemented on this platform")
}
