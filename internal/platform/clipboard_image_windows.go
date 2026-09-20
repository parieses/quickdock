//go:build windows

package platform

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

// 内存图片 → 系统剪贴板（CF_DIB）。
//
// 从 clipboard_windows.go 抽出：原 SetClipboardImage 把「读 PNG 文件」和
// 「构造 DIB + 写剪贴板」混在一个函数里。截图场景下像素已在内存中，
// 不应再走一遍「编码 PNG → 写盘 → 读盘 → 解码」的往返，故拆出内存入口，
// 两个入口共用同一份 DIB 构造与写入逻辑。

// SetClipboardImageData 把内存中的图片写入系统剪贴板（CF_DIB，32bpp bottom-up）。
//
// 句柄取宿主自持的隐藏消息窗口（与读取剪贴板路径一致）；监听窗口尚未启动时
// 该值为 0，OpenClipboard(0) 在 Win32 语义下同样合法（绑定到当前任务）。
func SetClipboardImageData(img image.Image) error {
	if img == nil {
		return fmt.Errorf("nil image")
	}
	return writeClipboardDIB(ClipboardWindowHandle(), encodeDIB(img))
}

// encodeDIB 把任意 image.Image 编码成 CF_DIB：
// BITMAPINFOHEADER(40 字节) + 32bpp bottom-up BGRA 像素。
//
// 用 bottom-up（biHeight 取正）而非 top-down：这是 CF_DIB 的惯例形式，
// 各取用方支持最一致。
//
// 不用 24bpp：32bpp 免去行 4 字节对齐的填充计算，且 alpha 由调用方保证为不透明
// （见 screenshot.Bitmap.ToNRGBA），不会出现「粘贴出来是空白」的经典故障。
func encodeDIB(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}

	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)

	const headerSize = 40
	stride := w * 4
	dib := make([]byte, headerSize+stride*h)

	binary.LittleEndian.PutUint32(dib[0:4], headerSize)
	binary.LittleEndian.PutUint32(dib[4:8], uint32(w))
	binary.LittleEndian.PutUint32(dib[8:12], uint32(h))
	binary.LittleEndian.PutUint16(dib[12:14], 1)  // biPlanes
	binary.LittleEndian.PutUint16(dib[14:16], 32) // biBitCount
	// biCompression = BI_RGB(0)、biSizeImage = 0，保持 0 即可

	for y := 0; y < h; y++ {
		destRow := headerSize + (h-1-y)*stride
		srcRow := rgba.PixOffset(0, y)
		for x := 0; x < w; x++ {
			s, d := srcRow+x*4, destRow+x*4
			dib[d+0] = rgba.Pix[s+2] // B
			dib[d+1] = rgba.Pix[s+1] // G
			dib[d+2] = rgba.Pix[s+0] // R
			dib[d+3] = rgba.Pix[s+3] // A
		}
	}
	return dib
}

// writeClipboardDIB 以 CF_DIB(8) 写入剪贴板。
func writeClipboardDIB(hwnd uintptr, dib []byte) error {
	if len(dib) == 0 {
		return fmt.Errorf("empty image data")
	}
	if !w32.OpenClipboard(w32.HWND(hwnd)) {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer w32.CloseClipboard()

	w32.EmptyClipboard()

	// 0x0042 = GMEM_MOVEABLE | GMEM_ZEROINIT
	handle := w32.GlobalAlloc(0x0042, uint32(len(dib)))
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr := w32.GlobalLock(handle)
	if ptr == nil {
		// 内存从未被写入，绝不能提交给系统剪贴板，否则会把清零数据当成真实内容；
		// 同时释放句柄避免泄漏。
		w32.GlobalFree(handle)
		return fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(ptr), len(dib)), dib)
	w32.GlobalUnlock(handle)

	if w32.SetClipboardData(8, handle) == 0 {
		// 设置失败 → 内存所有权未转移，需自行释放。
		w32.GlobalFree(handle)
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}
