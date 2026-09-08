//go:build windows

package platform

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

// procGlobalSize w32 未导出 GlobalSize，保留手写声明（仅此处使用）。
var procGlobalSize = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalSize")

// globalSize 返回全局内存块实际大小（字节）。
func globalSize(h uintptr) uintptr {
	sz, _, _ := procGlobalSize.Call(h)
	return sz
}

// SimulatePaste sends Ctrl+V keystroke via keybd_event
func SimulatePaste() {
	user32 := modUser32
	keybd := user32.NewProc("keybd_event")

	const (
		VK_CONTROL       = 0x11
		VK_V              = 0x56
		KEYEVENTF_KEYDOWN = 0x0000
		KEYEVENTF_KEYUP   = 0x0002
	)

	keybd.Call(VK_CONTROL, 0, KEYEVENTF_KEYDOWN, 0)
	keybd.Call(VK_V, 0, KEYEVENTF_KEYDOWN, 0)
	keybd.Call(VK_V, 0, KEYEVENTF_KEYUP, 0)
	time.Sleep(5 * time.Millisecond)
	keybd.Call(VK_CONTROL, 0, KEYEVENTF_KEYUP, 0)
}

// SetClipboardFiles writes a list of file paths to the system clipboard (CF_HDROP)
func SetClipboardFiles(hwnd uintptr, paths []string) error {
	if hwnd == 0 {
		return fmt.Errorf("window not initialized")
	}
	if len(paths) == 0 {
		return nil
	}

	var u16buf []uint16
	for _, p := range paths {
		u16buf = append(u16buf, utf16.Encode([]rune(p))...)
		u16buf = append(u16buf, 0)
	}
	u16buf = append(u16buf, 0)

	drophdrSize := 20
	totalSize := drophdrSize + len(u16buf)*2
	data := make([]byte, totalSize)

	binary.LittleEndian.PutUint32(data[0:4], uint32(drophdrSize))
	binary.LittleEndian.PutUint32(data[16:20], 1)

	for i, ch := range u16buf {
		binary.LittleEndian.PutUint16(data[drophdrSize+i*2:], ch)
	}

	if !w32.OpenClipboard(w32.HWND(hwnd)) {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer w32.CloseClipboard()

	w32.EmptyClipboard()

	handle := w32.GlobalAlloc(0x0042, uint32(len(data)))
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr := w32.GlobalLock(handle)
	if ptr == nil {
		// GlobalLock 失败：内存从未被写入，绝不能提交给系统剪贴板，
		// 否则会把未初始化（清零）数据当成真实内容，且需释放句柄避免泄漏。
		w32.GlobalFree(handle)
		return fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(ptr), len(data)), data)
	w32.GlobalUnlock(handle)
	if w32.SetClipboardData(15, handle) == 0 {
		// 设置失败 → 释放已分配的内存
		w32.GlobalFree(handle)
		return fmt.Errorf("SetClipboardData failed")
	}

	return nil
}

// SetClipboardImage writes a PNG image to the system clipboard (CF_DIB)
func SetClipboardImage(hwnd uintptr, imagePath string) error {
	if hwnd == 0 {
		return fmt.Errorf("window not initialized")
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

	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, src, bounds.Min, draw.Src)

	headerSize := 40
	stride := width * 4
	dibSize := headerSize + stride*height
	dibData := make([]byte, dibSize)

	binary.LittleEndian.PutUint32(dibData[0:4], uint32(headerSize))
	binary.LittleEndian.PutUint32(dibData[4:8], uint32(width))
	binary.LittleEndian.PutUint32(dibData[8:12], uint32(height))
	binary.LittleEndian.PutUint16(dibData[12:14], 1)
	binary.LittleEndian.PutUint16(dibData[14:16], 32)
	binary.LittleEndian.PutUint32(dibData[16:20], 0)

	for y := 0; y < height; y++ {
		destY := height - 1 - y
		rowOffset := headerSize + destY*stride
		for x := 0; x < width; x++ {
			off := rgba.PixOffset(x, y)
			pxOff := rowOffset + x*4
			dibData[pxOff+0] = rgba.Pix[off+2]
			dibData[pxOff+1] = rgba.Pix[off+1]
			dibData[pxOff+2] = rgba.Pix[off+0]
			dibData[pxOff+3] = rgba.Pix[off+3]
		}
	}

	if !w32.OpenClipboard(w32.HWND(hwnd)) {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer w32.CloseClipboard()

	w32.EmptyClipboard()

	handle := w32.GlobalAlloc(0x0042, uint32(len(dibData)))
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr := w32.GlobalLock(handle)
	if ptr == nil {
		w32.GlobalFree(handle)
		return fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(ptr), len(dibData)), dibData)
	w32.GlobalUnlock(handle)
	if w32.SetClipboardData(8, handle) == 0 {
		// 设置失败 → 释放已分配的内存
		w32.GlobalFree(handle)
		return fmt.Errorf("SetClipboardData failed")
	}

	return nil
}

// GetClipboardText reads the current system clipboard text (CF_UNICODETEXT).
// Used by snippet variable {clipboard}. Returns "" on any failure
// (e.g. another app holds the clipboard open) — never errors.
func GetClipboardText() string {
	if !w32.OpenClipboard(0) {
		return ""
	}
	defer w32.CloseClipboard()

	handle := w32.GetClipboardData(13) // CF_UNICODETEXT
	if handle == 0 {
		return ""
	}
	ptr := w32.GlobalLock(handle)
	if ptr == nil {
		return ""
	}
	defer w32.GlobalUnlock(handle)

	size := globalSize(uintptr(handle))
	if size == 0 {
		return ""
	}
	n := int(size) / 2
	if n > 1<<20 {
		n = 1 << 20 // 安全上限 1MB
	}
	buf := unsafe.Slice((*uint16)(ptr), n)
	for i := 0; i < len(buf); i++ {
		if buf[i] == 0 {
			buf = buf[:i]
			break
		}
	}
	return string(utf16.Decode(buf))
}

// GetActiveWindowTitle returns the title of the foreground window
func GetActiveWindowTitle() string {
	hwnd := w32.GetForegroundWindow()
	if hwnd == 0 {
		return ""
	}
	return w32.GetWindowText(hwnd)
}

// UTF16PtrToString converts a UTF-16 pointer to a Go string
func UTF16PtrToString(ptr uintptr, maxLen int) string {
	if ptr == 0 {
		return ""
	}
	header := struct {
		Data uintptr
		Len  int
		Cap  int
	}{ptr, maxLen, maxLen}
	buf := *(*[]uint16)(unsafe.Pointer(&header))
	for i, ch := range buf {
		if ch == 0 {
			return string(utf16.Decode(buf[:i]))
		}
	}
	return string(utf16.Decode(buf))
}

