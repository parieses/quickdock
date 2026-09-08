//go:build windows

package platform

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
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
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modPowrprof = windows.NewLazySystemDLL("powrprof.dll")
	modOle32    = windows.NewLazySystemDLL("ole32.dll")

	procSHGetFileInfoW     = modShell32.NewProc("SHGetFileInfoW")
	procExtractIconExW     = modShell32.NewProc("ExtractIconExW")
	procDestroyIcon        = modUser32.NewProc("DestroyIcon")
	procGetIconInfo        = modUser32.NewProc("GetIconInfo")
	procCreateCompatibleDC = modGdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = modGdi32.NewProc("DeleteDC")
	procDeleteObject       = modGdi32.NewProc("DeleteObject")
	procGetDIBits          = modGdi32.NewProc("GetDIBits")
	procGetObject          = modGdi32.NewProc("GetObjectW")
	procCoInitializeEx     = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize     = modOle32.NewProc("CoUninitialize")
)

const (
	SHGFI_ICON      = 0x000000100
	SHGFI_LARGEICON = 0x000000000

	COINIT_APARTMENTTHREADED = 0x2
	COINIT_DISABLE_OLE1DDE   = 0x4
)

// extractIconRaw 通过 SHGetFileInfoW + GetDIBits 提取图标为 base64 PNG data URL
func extractIconRaw(filePath string) string {
	// 确保调用线程已初始化 COM 单线程公寓。SHGetFileInfoW(SHGFI_ICON) 在
	// 未初始化 COM 的线程（如 Wails JS→Go 回调所在的 goroutine）上可能偶发返回 0，
	// 导致图标提取失败。仅在我们真正完成初始化时才配对 CoUninitialize。
	if hr, _, _ := procCoInitializeEx.Call(0, uintptr(COINIT_APARTMENTTHREADED|COINIT_DISABLE_OLE1DDE)); hr == 0 {
		defer procCoUninitialize.Call()
	}

	// 1. SHGetFileInfoW 获取 HICON；失败则回退到直接从 exe 资源提取图标
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
	hIcon := fi.hIcon
	if ret == 0 || hIcon == 0 {
		hIcon = extractFirstIconFromExe(pathPtr)
	}
	if hIcon == 0 {
		return ""
	}
	defer procDestroyIcon.Call(uintptr(hIcon))

	// 2. GetIconInfo 获取位图句柄
	var ii iconinfo
	ret, _, _ = procGetIconInfo.Call(uintptr(hIcon), uintptr(unsafe.Pointer(&ii)))
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

// extractFirstIconFromExe 回退方案：直接用 ExtractIconExW 从 exe/lnk 资源中读取
// 第一个图标（大图标），不依赖文件类型关联的 Shell 解析，确定性更高。
func extractFirstIconFromExe(pathPtr *uint16) windows.Handle {
	var hLarge, hSmall windows.Handle
	ret, _, _ := procExtractIconExW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		0, // 图标索引 0 = 第一个
		uintptr(unsafe.Pointer(&hLarge)),
		uintptr(unsafe.Pointer(&hSmall)),
		1, // 提取 1 个图标
	)
	if ret == 0 {
		return 0
	}
	if hSmall != 0 {
		procDestroyIcon.Call(uintptr(hSmall))
	}
	return hLarge
}
