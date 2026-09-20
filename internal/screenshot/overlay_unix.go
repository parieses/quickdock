//go:build darwin || linux

package screenshot

// darwin / linux 占位实现。截图能力目前只在 Windows 落地。
// 提供与 Windows 版本完全一致的 API 形状，保证上层调用点跨平台编译通过。

// Overlay 在 darwin / linux 上是空占位类型。
type Overlay struct{}

// NewOverlay 返回占位实例。
func NewOverlay() *Overlay { return &Overlay{} }

// Start 尚未实现。
func (o *Overlay) Start() error { return ErrUnsupported }

// Stop 无操作。
func (o *Overlay) Stop() {}

// Show 立即以「用户取消」结束本次会话。
func (o *Overlay) Show(src *Bitmap, bounds Rect, result chan outcome) {
	select {
	case result <- outcome{}:
	default:
	}
	close(result)
}
