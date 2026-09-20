//go:build windows

package screenshot

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"quickdock/internal/logger"
)

// 贴图钉屏：把一张截图钉在桌面最上层，可拖动、滚轮缩放、双击关闭、右键菜单。
//
// 为什么用原生 GDI 窗口而不是 Wails 浮窗：
//   - 每张钉图一个 WebView2 实例 = 约 30~50MB 常驻。钉 5 张就是 250MB，
//     与项目一直盯着的内存占用直接冲突。
//   - 原生窗口是纯 GDI 画一张位图，内存就是位图本身，窗口显示也不需要等渲染。
//
// 线程模型：每个钉图独占一个 OS 线程跑自己的消息泵（与覆盖层同样的 LockOSThread
// + GetMessage 范式）。钉图之间互不影响，坏掉一个不会拖垮其它的。数量本就个位数，
// 不值得为省线程去做「一个线程管 N 个窗口」的复杂度。

// PinActions / pinActions / SetPinActions 的声明在跨平台的 pin.go 里
// （services 层不分平台地引用它们），这里只放 Windows 实现。

const (
	// 缩放下限/上限。上限给到 8 倍，够看细节；下限 10% 便于把大图缩成缩略图摆着。
	pinMinZoom = 0.1
	pinMaxZoom = 8.0
	// 滚轮一格（WHEEL_DELTA=120）对应的缩放倍率。
	pinWheelDelta = 120.0
	pinWheelStep  = 1.1

	pinMenuCopy  = 1
	pinMenuSave  = 2
	pinMenuClose = 3
)

type pin struct {
	hwnd uintptr
	img  *Bitmap
	zoom float64

	// ===== 以下字段仅该钉图自己的消息线程访问 =====

	// 承载原图的 DIB。WM_PAINT 时从它 StretchBlt 到窗口 DC。
	dc     uintptr
	bmp    uintptr
	oldBmp uintptr

	curW, curH int

	dragging   bool
	dragFrom   pt // 按下时的光标屏幕坐标
	dragOrigin pt // 按下时的窗口左上角
}

var (
	pinWndProcCallback = syscall.NewCallback(pinWndProc)

	// pinRegistry 把 HWND 映射回实例。刻意不用 SetWindowLongPtrW(GWLP_USERDATA)：
	// 那需要把指针塞进 uintptr 再取回来，go vet 的 unsafeptr 检查会判为
	// "possible misuse of unsafe.Pointer"，而这里根本没有必要绕那一圈。
	pinRegistry sync.Map // hwnd(uintptr) -> *pin

	pinClassOnce sync.Once
	pinClassErr  error
	pinClassName *uint16
)

// PinImage 把 img 钉到虚拟桌面坐标 (x, y) 处（左上角），初始 1:1 显示。
// 返回是否创建成功（建窗在独立线程上完成，这里同步等它回结果）。
func PinImage(img *Bitmap, x, y int) bool {
	if img.Empty() {
		return false
	}
	// 复制一份像素：调用方（宿主服务）返回后就不该再持有它。
	snapshot := &Bitmap{W: img.W, H: img.H, Pix: make([]byte, len(img.Pix))}
	copy(snapshot.Pix, img.Pix)

	p := &pin{img: snapshot, zoom: 1, curW: snapshot.W, curH: snapshot.H}
	ready := make(chan error, 1)
	go p.run(x, y, ready)

	if err := <-ready; err != nil {
		logger.W("[pin] 贴图失败: %v", err)
		return false
	}
	return true
}

func (p *pin) run(x, y int, ready chan<- error) {
	// 消息泵必须独占线程：GetMessage 会阻塞。
	runtime.LockOSThread()

	if err := pinEnsureClass(); err != nil {
		ready <- err
		return
	}
	if !p.prepare() {
		ready <- fmt.Errorf("贴图: 创建位图资源失败")
		return
	}

	hInst, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(pinClassName)),
		0,
		wsPopup,
		iptr(x), iptr(y),
		uintptr(p.curW), uintptr(p.curH),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		p.release()
		ready <- fmt.Errorf("贴图: 创建窗口失败")
		return
	}
	p.hwnd = hwnd
	pinRegistry.Store(hwnd, p)

	// SW_SHOWNOACTIVATE：钉图不该抢走用户当前窗口的焦点。
	procShowWindow.Call(hwnd, swShowNoActivate)
	ready <- nil

	var msg msgStruct
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// prepare 把原图拷进一块顶朝下的 32bpp DIB。
func (p *pin) prepare() bool {
	hdc, _, _ := procCreateCompatibleDC.Call(0)
	if hdc == 0 {
		return false
	}
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(p.img.W),
			Height:      -int32(p.img.H), // top-down
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
		},
	}
	var bits unsafe.Pointer
	hb, _, _ := procCreateDIBSection.Call(
		hdc, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0,
	)
	if hb == 0 || bits == nil {
		procDeleteDC.Call(hdc)
		return false
	}
	copy(unsafe.Slice((*byte)(bits), p.img.W*p.img.H*4), p.img.Pix)

	p.dc, p.bmp = hdc, hb
	p.oldBmp, _, _ = procSelectObject.Call(hdc, hb)
	return true
}

func (p *pin) release() {
	if p.bmp != 0 {
		procSelectObject.Call(p.dc, p.oldBmp)
		procDeleteObject.Call(p.bmp)
	}
	if p.dc != 0 {
		procDeleteDC.Call(p.dc)
	}
	p.dc, p.bmp, p.oldBmp = 0, 0, 0
}

// ===== 交互 =====

func (p *pin) onPaint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc != 0 && p.dc != 0 {
		// HALFTONE 让缩放有插值（默认 COLORONCOLOR 会丢行，缩小后全是锯齿）。
		// 用 HALFTONE 必须先归位画刷原点，否则图案刷会错位。
		procSetStretchBltMode.Call(hdc, halftone)
		procSetBrushOrgEx.Call(hdc, 0, 0, 0)
		procStretchBlt.Call(hdc,
			0, 0, uintptr(p.curW), uintptr(p.curH),
			p.dc, 0, 0, uintptr(p.img.W), uintptr(p.img.H),
			srcCopy,
		)
	}
	procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
}

func (p *pin) beginDrag(hwnd uintptr) {
	var cur pointStruct
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
	var rc rectStruct
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))

	p.dragging = true
	p.dragFrom = pt{int(cur.X), int(cur.Y)}
	p.dragOrigin = pt{int(rc.Left), int(rc.Top)}
	procSetCapture.Call(hwnd)
}

func (p *pin) dragMove() {
	if !p.dragging {
		return
	}
	var cur pointStruct
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
	x := p.dragOrigin.x + int(cur.X) - p.dragFrom.x
	y := p.dragOrigin.y + int(cur.Y) - p.dragFrom.y
	procSetWindowPos.Call(p.hwnd, 0, iptr(x), iptr(y), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate)
}

func (p *pin) endDrag() {
	if !p.dragging {
		return
	}
	p.dragging = false
	procReleaseCapture.Call()
}

// zoomBy 以光标为锚点缩放：光标下的那个图像点保持不动。
func (p *pin) zoomBy(delta int) {
	if delta == 0 {
		return
	}
	steps := float64(delta) / pinWheelDelta
	nz := p.zoom * math.Pow(pinWheelStep, steps)
	nz = math.Min(pinMaxZoom, math.Max(pinMinZoom, nz))
	if math.Abs(nz-p.zoom) < 1e-4 {
		return
	}

	var cur pointStruct
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))
	var rc rectStruct
	procGetWindowRect.Call(p.hwnd, uintptr(unsafe.Pointer(&rc)))

	w0, h0 := float64(p.curW), float64(p.curH)
	// 光标在图像中的相对位置（0..1）；窗口异常时退化为 0.5 居中缩放。
	fx, fy := 0.5, 0.5
	if w0 > 0 && h0 > 0 {
		fx = (float64(cur.X) - float64(rc.Left)) / w0
		fy = (float64(cur.Y) - float64(rc.Top)) / h0
	}

	nw := maxInt(1, int(math.Round(float64(p.img.W)*nz)))
	nh := maxInt(1, int(math.Round(float64(p.img.H)*nz)))
	nx := int(math.Round(float64(cur.X) - fx*float64(nw)))
	ny := int(math.Round(float64(cur.Y) - fy*float64(nh)))

	p.zoom, p.curW, p.curH = nz, nw, nh
	procSetWindowPos.Call(p.hwnd, 0, iptr(nx), iptr(ny),
		uintptr(nw), uintptr(nh), swpNoZOrder|swpNoActivate)
	procInvalidateRect.Call(p.hwnd, 0, 1)
}

// showMenu 弹原生右键菜单。用 TPM_RETURNCMD 直接拿返回值，
// 省掉一套 WM_COMMAND 分发。
func (p *pin) showMenu() {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	appendMenu(hMenu, "复制到剪贴板", pinMenuCopy)
	appendMenu(hMenu, "保存为文件…", pinMenuSave)
	appendMenu(hMenu, "", 0) // 分隔线
	appendMenu(hMenu, "关闭", pinMenuClose)

	var cur pointStruct
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cur)))

	// 让菜单能随「点别处」正常消失，必须先把自己的窗口置前。
	procSetForegroundWindow.Call(p.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(
		hMenu,
		tpmReturnCmd|tpmRightButton,
		iptr(int(cur.X)), iptr(int(cur.Y)),
		0, p.hwnd, 0,
	)
	procDestroyMenu.Call(hMenu)

	switch cmd {
	case pinMenuCopy:
		p.invokeCopy()
	case pinMenuSave:
		p.invokeSave()
	case pinMenuClose:
		procDestroyWindow.Call(p.hwnd)
	}
}

// invokeCopy / invokeSave 把菜单动作丢到新 goroutine 上执行。
//
// 原因：这两个动作分别要用 Wails 的原生保存对话框与剪贴板，它们各有自己的线程
// 约束，在钉图的消息线程上直接跑容易踩坑；而且保存对话框是阻塞的，放在消息线程
// 会把这张钉图冻住（拖都拖不动）。
func (p *pin) invokeCopy() {
	fn := pinActions.Copy
	if fn == nil {
		return
	}
	img := p.img
	go func() {
		if err := fn(img); err != nil {
			logger.W("[pin] 复制贴图失败: %v", err)
		}
	}()
}

func (p *pin) invokeSave() {
	fn := pinActions.Save
	if fn == nil {
		return
	}
	img := p.img
	go func() {
		if _, err := fn(img); err != nil {
			logger.W("[pin] 保存贴图失败: %v", err)
		}
	}()
}

func appendMenu(hMenu uintptr, text string, id uintptr) {
	if text == "" {
		procAppendMenuW.Call(hMenu, 0x00000800 /*MF_SEPARATOR*/, 0, 0)
		return
	}
	u, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	procAppendMenuW.Call(hMenu, mfString, id, uintptr(unsafe.Pointer(u)))
}

// ===== 窗口过程 =====

func pinEnsureClass() error {
	pinClassOnce.Do(func() {
		hInst, _, _ := procGetModuleHandleW.Call(0)
		cls, err := syscall.UTF16PtrFromString("QuickDock_Screenshot_Pin_v1")
		if err != nil {
			pinClassErr = err
			return
		}
		wc := wndClassW{
			// CS_DBLCLKS：不给这个样式就收不到 WM_LBUTTONDBLCLK（双击关闭）。
			Style:         csDblClks,
			LpfnWndProc:   pinWndProcCallback,
			HInstance:     hInst,
			HCursor:       loadCursor(idcSizeAll),
			LpszClassName: cls,
		}
		if ret, _, _ := procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
			pinClassErr = fmt.Errorf("贴图: 注册窗口类失败")
			return
		}
		pinClassName = cls
	})
	return pinClassErr
}

func pinFromWindow(hwnd uintptr) *pin {
	v, ok := pinRegistry.Load(hwnd)
	if !ok {
		return nil
	}
	p, _ := v.(*pin)
	return p
}

func pinWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	p := pinFromWindow(hwnd)
	if p == nil {
		// 建窗期间（GWLP_USERDATA 还没写入）走默认处理。
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}

	switch msg {
	case wmPaint:
		p.onPaint(hwnd)
		return 0

	case wmEraseBkgnd:
		// 返回 1 = 已擦除。位图在 WM_PAINT 里一次性铺满整块客户区，
		// 让系统再擦一遍背景只会闪。
		return 1

	case wmLButtonDown:
		p.beginDrag(hwnd)
		return 0

	case wmMouseMove:
		p.dragMove()
		return 0

	case wmLButtonUp:
		p.endDrag()
		return 0

	case wmLButtonDblClk:
		procDestroyWindow.Call(hwnd)
		return 0

	case wmMouseWheel:
		p.zoomBy(int(int16((wParam >> 16) & 0xFFFF)))
		return 0

	case wmRButtonUp:
		p.showMenu()
		return 0

	case wmNCHitTest:
		return htClient

	case wmSetCursor:
		procSetCursor.Call(loadCursor(idcSizeAll))
		return 1

	case wmDestroy:
		p.release()
		procPostQuitMessage.Call(0)
		return 0
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}
