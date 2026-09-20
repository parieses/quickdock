//go:build darwin || linux

package screenshot

// darwin / linux 占位实现：贴图钉屏尚未移植。
// PinActions / SetPinActions 的声明在跨平台的 pin.go 里。

// PinImage 尚未实现。
func PinImage(img *Bitmap, x, y int) bool { return false }
