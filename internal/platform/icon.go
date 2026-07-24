package platform

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ===== Windows API 类型定义 =====

type shfileinfow struct {
	hIcon         windows.Handle
	iIcon         int32
	dwAttributes  uint32
	szDisplayName [windows.MAX_PATH]uint16
	szTypeName    [80]uint16
}

type iconinfo struct {
	fIcon    uint32 // BOOL
	xHotspot uint32
	yHotspot uint32
	hbmMask  windows.Handle
	hbmColor windows.Handle
}

type bitmapinfoheader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

// GDI BITMAP 结构（用于 GetObjectW 获取尺寸）
type gdiBitmap struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes uint32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

var (
	modShell32 = windows.NewLazySystemDLL("shell32.dll")
	modUser32  = windows.NewLazySystemDLL("user32.dll")
	modGdi32   = windows.NewLazySystemDLL("gdi32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modNtdll    = windows.NewLazySystemDLL("ntdll.dll")
	modPowrprof = windows.NewLazySystemDLL("powrprof.dll")

	procSHGetFileInfoW     = modShell32.NewProc("SHGetFileInfoW")
	procDestroyIcon        = modUser32.NewProc("DestroyIcon")
	procGetIconInfo        = modUser32.NewProc("GetIconInfo")
	procCreateCompatibleDC = modGdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = modGdi32.NewProc("DeleteDC")
	procDeleteObject       = modGdi32.NewProc("DeleteObject")
	procGetDIBits          = modGdi32.NewProc("GetDIBits")
	procGetObject          = modGdi32.NewProc("GetObjectW")
)

const (
	SHGFI_ICON      = 0x000000100
	SHGFI_LARGEICON = 0x000000000
)


// IconMIME 根据扩展名返回图标 data URI 的 MIME 类型。
// 支持 svg/png/ico/jpg/jpeg，未知扩展名默认 image/svg+xml（内置插件图标均为 SVG）。
func IconMIME(ext string) string {
	switch strings.ToLower(ext) {
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return "image/svg+xml"
	}
}
// iconCacheDir 返回图标缓存目录
func iconCacheDir() string {
	dir := filepath.Join(DefaultDataDir(), "icons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Println("QuickDock: 图标缓存目录创建失败:", err)
	}
	return dir
}

// sanitizeIconName 将路径转为安全的缓存文件名。
// 同时纳入目录归一化哈希，避免不同目录下同名 exe（如两个 notepad.exe）共用同一缓存而串图。
func sanitizeIconName(path string) string {
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return dirHash(dir) + "_" + safeBaseName(base)
}

// safeBaseName 仅保留文件名词中的安全字符
func safeBaseName(base string) string {
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, base)
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

// dirHash 对目录做 FNV-1a 哈希，得到稳定的短标识，用于区分同名文件的不同来源目录
func dirHash(dir string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(filepath.Clean(dir))))
	return fmt.Sprintf("%08x", h.Sum32())
}

// ExtractIconBase64 从文件（.lnk 或 .exe）提取图标，返回 base64 data URL。
// 优先读取磁盘缓存；若源文件比缓存更新则重新提取（避免图标永不刷新）。
// 失败时返回空字符串。
func ExtractIconBase64(filePath string) string {
	cacheKey := sanitizeIconName(filePath)
	cachePath := filepath.Join(iconCacheDir(), cacheKey+".png")

	// 源文件比缓存新 → 跳过缓存，重新提取
	if srcInfo, err := os.Stat(filePath); err == nil {
		if cacheInfo, err := os.Stat(cachePath); err == nil {
			if srcInfo.ModTime().After(cacheInfo.ModTime()) {
				goto extract
			}
		}
	}

	// 1. 尝试读缓存
	if data, err := os.ReadFile(cachePath); err == nil {
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	}

extract:
	// 2. 提取图标
	dataURL := extractIconRaw(filePath)
	if dataURL == "" {
		return ""
	}

	// 3. 写入缓存
	if idx := bytes.IndexByte([]byte(dataURL), ','); idx >= 0 {
		if pngData, err := base64.StdEncoding.DecodeString(dataURL[idx+1:]); err == nil {
			_ = os.WriteFile(cachePath, pngData, 0o644)
		}
	}

	return dataURL
}

// extractIconRaw 通过 SHGetFileInfoW + GetDIBits 提取图标为 base64 PNG data URL
func extractIconRaw(filePath string) string {
	// 1. SHGetFileInfoW 获取 HICON
	pathPtr, err := windows.UTF16PtrFromString(filePath)
	if err != nil {
		return ""
	}

	var fi shfileinfow
	ret, _, _ := procSHGetFileInfoW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		uintptr(unsafe.Pointer(&fi)),
		unsafe.Sizeof(fi),
		SHGFI_ICON|SHGFI_LARGEICON,
	)
	if ret == 0 || fi.hIcon == 0 {
		return ""
	}
	defer procDestroyIcon.Call(uintptr(fi.hIcon))

	// 2. GetIconInfo 获取位图句柄
	var ii iconinfo
	ret, _, _ = procGetIconInfo.Call(uintptr(fi.hIcon), uintptr(unsafe.Pointer(&ii)))
	if ret == 0 {
		return ""
	}
	defer func() {
		if ii.hbmMask != 0 {
			procDeleteObject.Call(uintptr(ii.hbmMask))
		}
		if ii.hbmColor != 0 {
			procDeleteObject.Call(uintptr(ii.hbmColor))
		}
	}()

	if ii.hbmColor == 0 {
		return ""
	}

	// 3. GetObjectW 获取位图尺寸
	var bm gdiBitmap
	ret, _, _ = procGetObject.Call(
		uintptr(ii.hbmColor),
		unsafe.Sizeof(bm),
		uintptr(unsafe.Pointer(&bm)),
	)
	if ret == 0 {
		return ""
	}

	width := int(bm.bmWidth)
	height := int(bm.bmHeight)
	if width <= 0 || height <= 0 || width > 256 || height > 256 {
		return ""
	}

	// 4. 构造 BITMAPINFOHEADER（32位 BGRA, top-down）
	var bih bitmapinfoheader
	bih.biSize = uint32(unsafe.Sizeof(bih))
	bih.biWidth = int32(width)
	bih.biHeight = -int32(height) // 负值 = top-down
	bih.biPlanes = 1
	bih.biBitCount = 32
	bih.biCompression = 0 // BI_RGB

	// 5. 创建兼容 DC
	hdc, _, _ := procCreateCompatibleDC.Call(0)
	if hdc == 0 {
		return ""
	}
	defer procDeleteDC.Call(hdc)

	// 6. GetDIBits 获取像素数据
	bufSize := width * height * 4
	pixels := make([]byte, bufSize)
	ret, _, _ = procGetDIBits.Call(
		hdc,
		uintptr(ii.hbmColor),
		0,
		uintptr(uint32(height)),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bih)),
		0, // DIB_RGB_COLORS
	)
	if ret == 0 {
		return ""
	}

	// 7. BGRA → RGBA
	for i := 0; i < len(pixels); i += 4 {
		pixels[i], pixels[i+2] = pixels[i+2], pixels[i] // B ↔ R
	}

	// 8. 检测全零 alpha（旧图标常见问题），设为不透明
	allZeroAlpha := true
	for i := 3; i < len(pixels); i += 4 {
		if pixels[i] != 0 {
			allZeroAlpha = false
			break
		}
	}
	if allZeroAlpha {
		for i := 3; i < len(pixels); i += 4 {
			pixels[i] = 255
		}
	}

	// 9. 编码为 PNG
	img := &image.NRGBA{
		Pix:    pixels,
		Stride: width * 4,
		Rect:   image.Rect(0, 0, width, height),
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return ""
	}

	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}
