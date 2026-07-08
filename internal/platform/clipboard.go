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
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// SimulatePaste sends Ctrl+V keystroke via keybd_event
func SimulatePaste() {
	user32 := syscall.NewLazyDLL("user32.dll")
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

	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")

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
	if handle != 0 {
		ptr, _, _ := kernel32.NewProc("GlobalLock").Call(handle)
		if ptr != 0 {
			copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data)), data)
			kernel32.NewProc("GlobalUnlock").Call(handle)
		}
		if ret, _, _ := user32.NewProc("SetClipboardData").Call(15, handle); ret == 0 {
			// 设置失败 → 释放已分配的内存
			kernel32.NewProc("GlobalFree").Call(handle)
		}
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

	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")

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
	if handle != 0 {
		ptr, _, _ := kernel32.NewProc("GlobalLock").Call(handle)
		if ptr != 0 {
			copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(dibData)), dibData)
			kernel32.NewProc("GlobalUnlock").Call(handle)
		}
		if ret, _, _ := user32.NewProc("SetClipboardData").Call(8, handle); ret == 0 {
			// 设置失败 → 释放已分配的内存
			kernel32.NewProc("GlobalFree").Call(handle)
		}
	}

	return nil
}

// GetActiveWindowTitle returns the title of the foreground window
func GetActiveWindowTitle() string {
	user32 := syscall.NewLazyDLL("user32.dll")
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
	dir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "quickdock", "images")
	}
	imageDir := filepath.Join(dir, ".quickdock", "images")
	os.MkdirAll(imageDir, 0755)
	return imageDir
}

// DibToImage converts CF_DIB raw data to a Go Image
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

	if compression != 0 && compression != 3 {
		return nil, fmt.Errorf("unsupported compression: %d", compression)
	}

	pixelOffset := headerSize
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
				img.Set(x, y, color.RGBA{
					R: data[pxOff+2], G: data[pxOff+1],
					B: data[pxOff+0], A: data[pxOff+3],
				})
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
