// Package screenshot 提供屏幕捕获与区域框选能力。
//
// 坐标系约定：本包所有矩形均为「虚拟桌面坐标系」，单位为物理像素，原点为
// 所有显示器包围盒的左上角。主显示器不一定位于原点——位于其左侧或上方的
// 显示器会引入负坐标。
//
// 宿主已在 build/windows/wails.exe.manifest 声明 permonitorv2 DPI 感知，
// 因此 GetSystemMetrics / BitBlt / SetWindowPos 全部以物理像素工作，无需换算。
//
// 平台实现划分（业务文件不引 syscall，符合仓库的平台隔离约定）：
//   - win32_windows.go / capture_windows.go / overlay_windows.go — Windows 实现
//   - capture_unix.go / overlay_unix.go — darwin / linux 占位
package screenshot

import (
	"errors"
	"image"
)

// ErrUnsupported 表示当前平台尚未实现截图能力。
var ErrUnsupported = errors.New("screenshot: 当前平台暂不支持")

// Rect 是虚拟桌面坐标系下的矩形。
type Rect struct {
	X, Y, W, H int
}

// Empty 报告矩形是否没有面积。
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Bitmap 是 top-down 排列的 32 位 BGRA 位图，内存布局与 GDI 的 BI_RGB 32bpp 一致。
// 每像素 4 字节，顺序为 B、G、R、A。
type Bitmap struct {
	W   int
	H   int
	Pix []byte // len(Pix) == W*H*4
}

// Empty 报告位图是否没有有效像素。
func (b *Bitmap) Empty() bool {
	return b == nil || b.W <= 0 || b.H <= 0 || len(b.Pix) < b.W*b.H*4
}

// Action 是用户在覆盖层里选的收尾动作。
type Action int

const (
	ActionCancel Action = iota // 取消（Esc / 右键 / 关闭按钮）
	ActionCopy                 // 复制到剪贴板
	ActionSave                 // 保存为文件
	ActionPin                  // 贴到屏幕上（钉屏）
)

// Result 是一次截图会话的结果。
//
// Action 决定上层怎么处置 Bitmap：ActionCopy 写剪贴板、ActionSave 弹保存对话框。
// 取消（含选区无效）时 Action 为 ActionCancel 且 Bitmap 为空。
type Result struct {
	Action Action
	Rect   Rect    // 选区（虚拟桌面坐标系）
	Bitmap *Bitmap // 已裁剪、已合成标注的位图
}

// Empty 报告本次会话是否没有产出内容。
func (r Result) Empty() bool { return r.Bitmap.Empty() }

// Contains 报告点 (x, y) 是否落在矩形内（左闭右开）。
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// intersect 返回两矩形的交集；无交集时返回零值矩形。
func (r Rect) intersect(o Rect) Rect {
	x0 := maxInt(r.X, o.X)
	y0 := maxInt(r.Y, o.Y)
	x1 := minInt(r.X+r.W, o.X+o.W)
	y1 := minInt(r.Y+r.H, o.Y+o.H)
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// ensureOpaque 强制把 alpha 通道置为不透明。
//
// GDI BitBlt 抓屏写进 32bpp DIB 时 alpha 字节没有保证——屏幕 DC 本身不含 alpha
// 概念，抓到的位图 alpha 常为 0。而覆盖层用 UpdateLayeredWindow(ULW_ALPHA) 提交，
// alpha=0 会让整块区域在合成时变成透明洞。抓屏后随手抹一遍是最省心的解法。
func (b *Bitmap) ensureOpaque() {
	if b.Empty() {
		return
	}
	for i := 3; i < len(b.Pix); i += 4 {
		b.Pix[i] = 0xFF
	}
}

// Crop 按 r 裁出子图。r 使用 b 自身的像素坐标系（原点为 b 的左上角），
// 超出 b 覆盖范围的部分被截掉。
func (b *Bitmap) Crop(r Rect) *Bitmap {
	if b.Empty() || r.Empty() {
		return &Bitmap{}
	}

	x0 := maxInt(r.X, 0)
	y0 := maxInt(r.Y, 0)
	x1 := minInt(r.X+r.W, b.W)
	y1 := minInt(r.Y+r.H, b.H)
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return &Bitmap{}
	}

	dst := &Bitmap{W: w, H: h, Pix: make([]byte, w*h*4)}
	rowBytes := w * 4
	for y := 0; y < h; y++ {
		src := ((y0+y)*b.W + x0) * 4
		dstOff := y * rowBytes
		copy(dst.Pix[dstOff:dstOff+rowBytes], b.Pix[src:src+rowBytes])
	}
	return dst
}

// ToNRGBA 转成 Go 的 image.NRGBA（BGRA → RGBA，alpha 统一置为不透明），
// 供 PNG 编码使用。截图不含透明通道，强制不透明可避免 alpha=0 被当成全透明。
func (b *Bitmap) ToNRGBA() *image.NRGBA {
	if b.Empty() {
		return image.NewNRGBA(image.Rect(0, 0, 0, 0))
	}
	img := image.NewNRGBA(image.Rect(0, 0, b.W, b.H))
	for y := 0; y < b.H; y++ {
		srcRow := y * b.W * 4
		dstRow := y * img.Stride
		for x := 0; x < b.W; x++ {
			s := srcRow + x*4
			d := dstRow + x*4
			img.Pix[d+0] = b.Pix[s+2] // R
			img.Pix[d+1] = b.Pix[s+1] // G
			img.Pix[d+2] = b.Pix[s+0] // B
			img.Pix[d+3] = 255
		}
	}
	return img
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
