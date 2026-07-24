package platform

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"
)



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

	user32 := modUser32
	kernel32 := modKernel32

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

	openClipboard := user32.NewProc("OpenClipboard")
	if ret, _, _ := openClipboard.Call(hwnd); ret == 0 {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer func() {
		closeClipboard := user32.NewProc("CloseClipboard")
		closeClipboard.Call()
	}()

	user32.NewProc("EmptyClipboard").Call()

	handle, _, _ := kernel32.NewProc("GlobalAlloc").Call(0x0042, uintptr(len(data)))
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := kernel32.NewProc("GlobalLock").Call(handle)
	if ptr == 0 {
		// GlobalLock 失败：内存从未被写入，绝不能提交给系统剪贴板，
		// 否则会把未初始化（清零）数据当成真实内容，且需释放句柄避免泄漏。
		kernel32.NewProc("GlobalFree").Call(handle)
		return fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data)), data)
	kernel32.NewProc("GlobalUnlock").Call(handle)
	if ret, _, _ := user32.NewProc("SetClipboardData").Call(15, handle); ret == 0 {
		// 设置失败 → 释放已分配的内存
		kernel32.NewProc("GlobalFree").Call(handle)
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

	user32 := modUser32
	kernel32 := modKernel32

	openClipboard := user32.NewProc("OpenClipboard")
	if ret, _, _ := openClipboard.Call(hwnd); ret == 0 {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer func() {
		closeClipboard := user32.NewProc("CloseClipboard")
		closeClipboard.Call()
	}()

	user32.NewProc("EmptyClipboard").Call()

	handle, _, _ := kernel32.NewProc("GlobalAlloc").Call(0x0042, uintptr(len(dibData)))
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := kernel32.NewProc("GlobalLock").Call(handle)
	if ptr == 0 {
		kernel32.NewProc("GlobalFree").Call(handle)
		return fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(dibData)), dibData)
	kernel32.NewProc("GlobalUnlock").Call(handle)
	if ret, _, _ := user32.NewProc("SetClipboardData").Call(8, handle); ret == 0 {
		// 设置失败 → 释放已分配的内存
		kernel32.NewProc("GlobalFree").Call(handle)
		return fmt.Errorf("SetClipboardData failed")
	}

	return nil
}

// GetClipboardText reads the current system clipboard text (CF_UNICODETEXT).
// Used by snippet variable {clipboard}. Returns "" on any failure
// (e.g. another app holds the clipboard open) — never errors.
func GetClipboardText() string {
	user32 := modUser32
	kernel32 := modKernel32

	openClipboard := user32.NewProc("OpenClipboard")
	if ret, _, _ := openClipboard.Call(0); ret == 0 {
		return ""
	}
	defer user32.NewProc("CloseClipboard").Call()

	getClipboardData := user32.NewProc("GetClipboardData")
	handle, _, _ := getClipboardData.Call(13) // CF_UNICODETEXT
	if handle == 0 {
		return ""
	}
	globalLock := kernel32.NewProc("GlobalLock")
	ptr, _, _ := globalLock.Call(handle)
	if ptr == 0 {
		return ""
	}
	defer kernel32.NewProc("GlobalUnlock").Call(handle)

	size, _, _ := kernel32.NewProc("GlobalSize").Call(handle)
	if size == 0 {
		return ""
	}
	n := int(size) / 2
	if n > 1<<20 {
		n = 1 << 20 // 安全上限 1MB
	}
	buf := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), n)
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
	user32 := modUser32
	getForeground := user32.NewProc("GetForegroundWindow")
	hwnd, _, _ := getForeground.Call()
	if hwnd == 0 {
		return ""
	}
	getText := user32.NewProc("GetWindowTextW")
	var buf [256]uint16
	getText.Call(hwnd, uintptr(unsafe.Pointer(&buf)), 256)
	return UTF16PtrToString(uintptr(unsafe.Pointer(&buf)), 256)
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

// GetImageDir returns the image storage directory
func GetImageDir() string {
	imageDir := filepath.Join(DefaultDataDir(), "images")
	os.MkdirAll(imageDir, 0755)
	return imageDir
}

// DibToImage converts CF_DIB / CF_DIBV5 raw data to a Go Image.
// 支持 BI_RGB(0)、BI_BITFIELDS(3)、BI_ALPHABITFIELDS(6)。
// 截图工具（Win+Shift+S）导出的 DIBV5 带 alpha 通道时 biCompression=6，
// 必须按颜色掩码解码，否则会被旧逻辑判定为 "unsupported compression" 而丢弃整张图。
func DibToImage(data []byte) (image.Image, error) {
	if len(data) < 40 {
		return nil, fmt.Errorf("DIB data too short: %d bytes", len(data))
	}

	headerSize := int(binary.LittleEndian.Uint32(data[0:4]))
	if headerSize < 40 {
		return nil, fmt.Errorf("BITMAPINFOHEADER too small: %d", headerSize)
	}

	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	height := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	bitCount := int(binary.LittleEndian.Uint16(data[14:16]))
	compression := binary.LittleEndian.Uint32(data[16:20])

	if width <= 0 || width > 10000 || height == 0 || height > 10000 || height < -10000 {
		return nil, fmt.Errorf("invalid image dimensions: %dx%d", width, height)
	}

	topDown := height < 0
	if topDown {
		height = -height
	}

	// 0=BI_RGB, 3=BI_BITFIELDS, 6=BI_ALPHABITFIELDS(V4/V5 带 alpha)
	if compression != 0 && compression != 3 && compression != 6 {
		return nil, fmt.Errorf("unsupported compression: %d", compression)
	}

	// 颜色掩码（BI_BITFIELDS / BI_ALPHABITFIELDS 时存在）
	var rMask, gMask, bMask, aMask uint32
	if compression == 3 || compression == 6 {
		if len(data) >= 52 {
			rMask = binary.LittleEndian.Uint32(data[40:44])
			gMask = binary.LittleEndian.Uint32(data[44:48])
			bMask = binary.LittleEndian.Uint32(data[48:52])
		}
		if compression == 6 && len(data) >= 56 {
			aMask = binary.LittleEndian.Uint32(data[52:56])
		}
	}

	// 像素数据起始偏移
	pixelOffset := headerSize
	if (compression == 3 || compression == 6) && headerSize <= 52 {
		// V3 header(40) + 3 个 mask(各 4 字节) → 像素从 52 起
		pixelOffset = 40 + 12
	}
	if bitCount <= 8 {
		pixelOffset += (1 << uint(bitCount)) * 4
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	switch bitCount {
	case 32:
		stride := width * 4
		paddedRowLen := (stride + 3) & ^3
		for y := 0; y < height; y++ {
			srcY := y
			if !topDown {
				srcY = height - 1 - y
			}
			rowOffset := pixelOffset + srcY*paddedRowLen
			for x := 0; x < width; x++ {
				pxOff := rowOffset + x*4
				if pxOff+3 >= len(data) {
					continue
				}
				var r, g, b, a uint8
				if rMask != 0 || gMask != 0 || bMask != 0 {
					// 有显式掩码：按掩码提取通道（兼容 ARGB/BGRA/RGBA 等布局）
					val := binary.LittleEndian.Uint32(data[pxOff : pxOff+4])
					r = extractMask(val, rMask)
					g = extractMask(val, gMask)
					b = extractMask(val, bMask)
					if aMask != 0 {
						a = extractMask(val, aMask)
					} else {
						a = 255
					}
				} else {
					// 无掩码：默认小端 BGRA（BI_RGB / BI_ALPHABITFIELDS 未给 mask 的常见情形）
					r = data[pxOff+2]
					g = data[pxOff+1]
					b = data[pxOff+0]
					a = data[pxOff+3]
				}
				img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: a})
			}
		}
	case 24:
		stride := width * 3
		paddedRowLen := (stride + 3) & ^3
		for y := 0; y < height; y++ {
			srcY := y
			if !topDown {
				srcY = height - 1 - y
			}
			rowOffset := pixelOffset + srcY*paddedRowLen
			for x := 0; x < width; x++ {
				pxOff := rowOffset + x*3
				if pxOff+2 >= len(data) {
					continue
				}
				img.Set(x, y, color.RGBA{
					R: data[pxOff+2], G: data[pxOff+1],
					B: data[pxOff+0], A: 255,
				})
			}
		}
	default:
		return nil, fmt.Errorf("unsupported bit depth: %d", bitCount)
	}

	return img, nil
}

// extractMask 根据颜色掩码从 32 位像素值中提取 0-255 通道值。
// 假定 mask 为连续位段（标准 BITMAP 掩码均满足），自动计算移位并归一化到 8 位。
func extractMask(val, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := uint32(0)
	m := mask
	for (m & 1) == 0 && shift < 32 {
		m >>= 1
		shift++
	}
	bits := (val >> shift) & m
	bitLen := uint32(0)
	for m != 0 {
		bitLen++
		m >>= 1
	}
	if bitLen >= 8 {
		return uint8((bits >> (bitLen - 8)) & 0xFF)
	}
	return uint8(bits << (8 - bitLen))
}

// ParseHDROP parses CF_HDROP raw data into a list of file paths
func ParseHDROP(data []byte) []string {
	if len(data) < 20 {
		return nil
	}
	pFiles := binary.LittleEndian.Uint32(data[0:4])
	fWide := binary.LittleEndian.Uint32(data[16:20])

	if int(pFiles) >= len(data) {
		return nil
	}

	var paths []string
	if fWide != 0 {
		raw := data[pFiles:]
		if len(raw)%2 != 0 {
			raw = raw[:len(raw)-1]
		}
		u16 := make([]uint16, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			ch := binary.LittleEndian.Uint16(raw[i:])
			if ch == 0 && len(u16) > 0 && u16[len(u16)-1] == 0 {
				break
			}
			u16 = append(u16, ch)
		}
		var current []uint16
		for _, ch := range u16 {
			if ch == 0 {
				if len(current) > 0 {
					paths = append(paths, string(utf16.Decode(current)))
					current = nil
				}
			} else {
				current = append(current, ch)
			}
		}
		if len(current) > 0 {
			paths = append(paths, string(utf16.Decode(current)))
		}
	} else {
		raw := data[pFiles:]
		var current []byte
		for i := 0; i < len(raw); i++ {
			if raw[i] == 0 {
				if len(current) > 0 {
					paths = append(paths, string(current))
					current = nil
				} else if len(paths) > 0 {
					break
				}
			} else {
				current = append(current, raw[i])
			}
		}
		if len(current) > 0 {
			paths = append(paths, string(current))
		}
	}
	return paths
}

// IsFilePathsAsText checks if text content is just a repetition of file paths
func IsFilePathsAsText(paths []string, text string) bool {
	if text == "" || len(paths) == 0 {
		return true
	}
	joined := strings.Join(paths, "\n")
	if text == joined {
		return true
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == len(paths) {
		match := true
		for i := range paths {
			if strings.TrimSpace(lines[i]) != paths[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	if len(paths) == 1 && strings.TrimSpace(text) == paths[0] {
		return true
	}
	return false
}

// MD5Hash computes MD5 hex string from data
func MD5Hash(data []byte) string {
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}
