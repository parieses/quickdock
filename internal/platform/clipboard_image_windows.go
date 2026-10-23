//go:build windows

package platform

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"

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

// SetClipboardDIBData 直接把内存里的 top-down 32bpp BGRA 像素写进剪贴板（CF_DIB）。
//
// 截图场景专用：像素本来就已经是 BGRA，走 SetClipboardImageData 那条路要先转成
// image.NRGBA（逐像素倒通道，1080p 实测 13ms）、再被 draw.Draw 复制一遍、
// 再逐像素翻成 BGRA 并倒行——同一份数据被搬了四遍。这里只做一次「倒行拷贝 + 补
// alpha」，1080p 约 2ms。
//
// pix 必须是 top-down（第一行就是画面最上面一行），长度至少 w*h*4。
func SetClipboardDIBData(pix []byte, w, h int) error {
	if w <= 0 || h <= 0 || len(pix) < w*h*4 {
		return fmt.Errorf("invalid image data")
	}
	return writeClipboardDIB(ClipboardWindowHandle(), encodeDIBFromBGRA(pix, w, h))
}

// encodeDIBFromBGRA 把 top-down BGRA 像素编码成 CF_DIB：
// BITMAPINFOHEADER(40 字节) + 32bpp bottom-up BGRA 像素。
//
// 行倒序是 CF_DIB 的惯例（biHeight 取正 = bottom-up），各取用方支持最一致。
// alpha 统一置 0xFF：屏幕抓到的像素 alpha 没有保证，写 0 会让「粘贴出来是空白」。
func encodeDIBFromBGRA(pix []byte, w, h int) []byte {
	const headerSize = 40
	stride := w * 4
	dib := make([]byte, headerSize+stride*h)

	binary.LittleEndian.PutUint32(dib[0:4], headerSize)
	binary.LittleEndian.PutUint32(dib[4:8], uint32(w))
	binary.LittleEndian.PutUint32(dib[8:12], uint32(h))
	binary.LittleEndian.PutUint16(dib[12:14], 1)  // biPlanes
	binary.LittleEndian.PutUint16(dib[14:16], 32) // biBitCount

	for y := 0; y < h; y++ {
		dstOff := headerSize + (h-1-y)*stride
		srcOff := y * stride
		row := dib[dstOff : dstOff+stride]
		copy(row, pix[srcOff:srcOff+stride])
		for i := 3; i < stride; i += 4 {
			row[i] = 0xFF
		}
	}
	return dib
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
	// 全局内存准备好之后才动剪贴板，避免「清空了旧内容但新内容没写进去」。
	handle, err := allocGlobal(dib)
	if err != nil {
		return err
	}
	return writeClipboardFormats(hwnd, []clipboardFormat{{format: 8, handle: handle}})
}

// SetClipboardFilesAndImage 一次会话同时写入 CF_HDROP(文件) 与 CF_DIB(图片)。
//
// 用于「剪贴板历史里的图片条目本身来自文件」这类内容：用户当初复制的是文件，
// 粘回资源管理器应当还是文件，粘到聊天窗口应当还是图。两次会话分别写会让后写的
// 格式把先写的格式冲掉（EmptyClipboard 是全局的），因此必须由这里一次写完。
func SetClipboardFilesAndImage(hwnd uintptr, paths []string, imagePath string) error {
	if hwnd == 0 {
		return fmt.Errorf("window not initialized")
	}
	if len(paths) == 0 {
		return SetClipboardImage(hwnd, imagePath)
	}

	f, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("failed to open image file: %w", err)
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("PNG decode failed: %w", err)
	}
	dib := encodeDIB(src)
	if len(dib) == 0 {
		return fmt.Errorf("empty image data")
	}

	hFile, err := allocGlobal(hdropBytes(paths))
	if err != nil {
		return err
	}
	hDib, err := allocGlobal(dib)
	if err != nil {
		w32.GlobalFree(hFile) // 文件句柄尚未提交给系统，须自行释放
		return err
	}
	return writeClipboardFormats(hwnd, []clipboardFormat{
		{format: 15, handle: hFile},
		{format: 8, handle: hDib},
	})
}
