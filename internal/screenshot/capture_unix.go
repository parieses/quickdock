//go:build darwin || linux

package screenshot

// darwin / linux 占位实现。截图能力目前只在 Windows 落地（与发布链路一致：
// 仓库当前仅发布 Windows 版本）。接口先占位，保证跨平台编译通过。

// VirtualDesktopBounds 返回空矩形。
func VirtualDesktopBounds() Rect { return Rect{} }

// captureRect 在 darwin / linux 上尚未实现。
func captureRect(r Rect) (*Bitmap, error) { return nil, ErrUnsupported }
