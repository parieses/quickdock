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
// MonitorFromPoint 的 POINT 是按值传入的，x64 下 8 字节的结构体走单个寄存器，
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
//
// 返回的 Bitmap 直接引用 DIB section 的内存，不再 copy 到 Go 堆（见 Bitmap.release）。
// 这对 4K 尤其重要：整屏 33MB，原来的 make+copy 等于白付一次分配、一次 33MB 拷贝，
// 而且新页未触碰，随后补 alpha / 暗化再遍历时又要逐页缺页。线上 4K 机器首次会话
// 「补 alpha」213ms、稳态 12.6ms，差的就是这批缺页。
//
// 代价是生命周期：调用方用完必须 Close（覆盖层在会话结束时负责，见 Overlay.finish）。
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
		procDeleteDC.Call(hMemDC)
		return nil, fmt.Errorf("screenshot: CreateDIBSection 失败")
	}

	oldBitmap, _, _ := procSelectObject.Call(hMemDC, hBitmap)
	if oldBitmap == 0 {
		procDeleteObject.Call(hBitmap)
		procDeleteDC.Call(hMemDC)
		return nil, fmt.Errorf("screenshot: SelectObject 失败")
	}

	// CAPTUREBLT 让分层窗口（部分悬浮窗、其他截图工具的选区层）也一并抓进来。
	// 覆盖层自身的显示必然发生在抓屏之后（见 SelectionOrErr），因此不会被拍进来。
	// 实测这个标志对性能没有影响（带与不带都在噪声内），不用为了速度去掉。
	ok, _, _ := procBitBlt.Call(
		hMemDC, 0, 0, uintptr(r.W), uintptr(r.H),
		hScreenDC, iptr(r.X), iptr(r.Y),
		srcCopy|captureBlt,
	)

	// 解绑与删 DC 必须在读取像素之前做：位图仍在 DC 里选中时，DeleteObject 会失败。
	// DIB section 的像素内存挂在 hBitmap 上，与 DC 无关，所以 DC 删掉后内存照样有效。
	procSelectObject.Call(hMemDC, oldBitmap)
	procDeleteDC.Call(hMemDC)
	if ok == 0 {
		procDeleteObject.Call(hBitmap)
		return nil, fmt.Errorf("screenshot: BitBlt 失败")
	}

	// GDI 是批处理的，BitBlt 返回不代表像素已经落到内存。以前每次都要 copy 一遍，
	// copy 顺带等到了写入完成；现在直接读这块内存，必须显式 flush，否则可能抓到
	// 上一帧的残留（表现为截图里混入旧画面）。
	procGdiFlush.Call()

	n := r.W * r.H * 4
	b := &Bitmap{W: r.W, H: r.H, Pix: unsafe.Slice((*byte)(pixels), n)}
	b.release = func() { procDeleteObject.Call(hBitmap) }
	return b, nil
}
