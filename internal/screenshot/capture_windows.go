//go:build windows

package screenshot

import (
	"fmt"
	"unsafe"
)

// VirtualDesktopBounds 返回所有显示器的包围盒（虚拟桌面），单位为物理像素。
// 依赖进程的 per-monitor v2 DPI 感知（见 build/windows/wails.exe.manifest），
// 否则返回值会被系统按逻辑像素缩放。
func VirtualDesktopBounds() Rect {
	x, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	y, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	w, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	return Rect{X: int(int32(x)), Y: int(int32(y)), W: int(w), H: int(h)}
}

// monitorFromPoint 返回屏幕坐标点 (x, y) 所在显示器的句柄；失败返回 0。
//
// MonitorFromPoint 的 POINT 是**按值**传入的，x64 下 8 字节的结构体走单个寄存器，
// 因此这里把 x / y 打包成一个 uintptr：低 32 位 x、高 32 位 y（小端）。
func monitorFromPoint(x, y int) uintptr {
	h, _, _ := procMonitorFromPoint.Call(
		uintptr(uint32(int32(x)))|uintptr(uint32(int32(y)))<<32,
		monitorDefaultToNearest)
	return h
}

// monitorWork 返回显示器的工作区（排除任务栏、停靠栏等应用栏），屏幕物理像素坐标。
// 第二个返回值为 false 表示取不到，调用方需自行兜底。
func monitorWork(hmon uintptr) (Rect, bool) {
	if hmon == 0 {
		return Rect{}, false
	}
	mi := monitorInfoW{CbSize: uint32(unsafe.Sizeof(monitorInfoW{}))}
	ret, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi)))
	if ret == 0 {
		return Rect{}, false
	}
	r := Rect{
		X: int(mi.RcWork.Left),
		Y: int(mi.RcWork.Top),
		W: int(mi.RcWork.Right - mi.RcWork.Left),
		H: int(mi.RcWork.Bottom - mi.RcWork.Top),
	}
	if r.Empty() {
		return Rect{}, false
	}
	return r, true
}

// captureRect 用 GDI BitBlt 抓取虚拟桌面上的指定区域，返回 top-down 32bpp BGRA 位图。
//
// 实现上用 CreateDIBSection 建目标位图：BitBlt 直接把像素落进我们持有的内存，
// 省掉 GetDIBits 的二次拷贝，4K 双屏下能少一次几十 MB 的搬运。
func captureRect(r Rect) (*Bitmap, error) {
	if r.Empty() {
		return nil, fmt.Errorf("screenshot: 无效的抓取区域 %dx%d", r.W, r.H)
	}

	hScreenDC, _, _ := procGetDC.Call(0)
	if hScreenDC == 0 {
		return nil, fmt.Errorf("screenshot: GetDC(桌面) 失败")
	}
	defer procReleaseDC.Call(0, hScreenDC)

	hMemDC, _, _ := procCreateCompatibleDC.Call(hScreenDC)
	if hMemDC == 0 {
		return nil, fmt.Errorf("screenshot: CreateCompatibleDC 失败")
	}
	defer procDeleteDC.Call(hMemDC)

	// 负 height 表示 top-down：内存里第一行就是屏幕最上面一行，与 Bitmap.Pix 约定一致。
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(r.W),
			Height:      -int32(r.H),
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
		},
	}

	var pixels unsafe.Pointer
	hBitmap, _, _ := procCreateDIBSection.Call(
		hMemDC,
		uintptr(unsafe.Pointer(&bi)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&pixels)),
		0, 0,
	)
	if hBitmap == 0 || pixels == nil {
		return nil, fmt.Errorf("screenshot: CreateDIBSection 失败")
	}
	defer procDeleteObject.Call(hBitmap)

	oldBitmap, _, _ := procSelectObject.Call(hMemDC, hBitmap)
	if oldBitmap == 0 {
		return nil, fmt.Errorf("screenshot: SelectObject 失败")
	}
	defer procSelectObject.Call(hMemDC, oldBitmap)

	// CAPTUREBLT 让分层窗口（部分悬浮窗、其他截图工具的选区层）也一并抓进来。
	// 覆盖层自身的显示必然发生在抓屏之后（见 SelectionOrErr），因此不会被拍进来。
	ok, _, _ := procBitBlt.Call(
		hMemDC, 0, 0, uintptr(r.W), uintptr(r.H),
		hScreenDC, iptr(r.X), iptr(r.Y),
		srcCopy|captureBlt,
	)
	if ok == 0 {
		return nil, fmt.Errorf("screenshot: BitBlt 失败")
	}

	n := r.W * r.H * 4
	pix := make([]byte, n)
	copy(pix, unsafe.Slice((*byte)(pixels), n))

	return &Bitmap{W: r.W, H: r.H, Pix: pix}, nil
}
