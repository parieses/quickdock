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

// 增强项（旋转 / 翻转 / 透明度 / 穿透 / 置顶 / 1:1）仍在同一层实现：
// 每个钉图还是「一个 OS 线程 + 一张 GDI DIB」，没有第二个窗口或 WebView。
// 透明度与穿透靠窗口扩展样式，不碰 WM_PAINT 的绘制路径。
const (
	// 缩放下限/上限。上限给到 8 倍，够看细节；下限 10% 便于把大图缩成缩略图摆着。
	pinMinZoom = 0.1
	pinMaxZoom = 8.0
	// 滚轮一格（WHEEL_DELTA=120）对应的缩放倍率。
	pinWheelDelta = 120.0
	pinWheelStep  = 1.1

	// 整体不透明度。下限留到 15% 左右：再低就完全看不见、也没法点回来了。
	// 步进 20/255 ≈ 8%，连按几下就能到想要的档位。
	pinMinAlpha    = 40
	pinMaxAlpha    = 255
	pinAlphaStep   = 20
	pinNudgeStep   = 10
	pinTimerPierce = 2 // 穿透期间的轮询定时器 id（与覆盖层的 timerFrame 属于不同窗口）

	pinMenuCopy      = 1
	pinMenuSave      = 2
	pinMenuClose     = 3
	pinMenuRotateCW  = 4
	pinMenuRotateCCW = 5
	pinMenuFlipH     = 6
	pinMenuFlipV     = 7
	pinMenuAlphaUp   = 8
	pinMenuAlphaDown = 9
	pinMenuPierce    = 10
	pinMenuTopmost   = 11
	pinMenuResetZoom = 12
)

// pinAlphaPercent 把 0..255 换算成菜单里显示的百分比。
func pinAlphaPercent(a byte) int { return (int(a)*100 + 127) / 255 }

type pin struct {
	hwnd uintptr
	img  *Bitmap
	zoom float64

	// 窗口显示状态。alpha 走 SetLayeredWindowAttributes；pierce 走
	// WS_EX_TRANSPARENT；topmost 走 SetWindowPos 的 hWndInsertAfter。
	alpha   byte
	pierce  bool
	topmost bool

	// ===== 以下字段仅该钉图自己的消息线程访问 =====

	// 承载原图的 DIB。WM_PAINT 时从它 StretchBlt 到窗口 DC。
	dc     uintptr
	bmp    uintptr
	oldBmp uintptr

	curW, curH int

	dragging   bool
	dragFrom   pt // 按下时的光标屏幕坐标
	dragOrigin pt // 按下时的窗口左上角

	// pierceTimer 表示穿透轮询定时器正挂着（避免重复 SetTimer）。
	pierceTimer bool
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

	p := &pin{
		img:     snapshot,
		zoom:    1,
		curW:    snapshot.W,
		curH:    snapshot.H,
		alpha:   pinMaxAlpha,
		topmost: true,
	}
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
		// WS_EX_LAYERED 是整体透明度（SetLayeredWindowAttributes）与鼠标穿透
		// （WS_EX_TRANSPARENT）的前提；窗口仍走普通 WM_PAINT，不用
		// UpdateLayeredWindow，所以绘制代码一行都不用改。
		wsExLayered|wsExTopmost|wsExToolWindow,
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

	// 先把整体 alpha 设成不透明：加了 WS_EX_LAYERED 却没设属性的窗口
	// 在部分系统上是全透明的。之后调透明度只改这一个字节。
	procSetLayeredWindowAttributes.Call(hwnd, 0, uintptr(p.alpha), lwaAlpha)

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
// 实现挪到 pin_transform_windows.go，让「变换后重建」共用同一份资源代码。
func (p *pin) prepare() bool {
	dc, bmp, old, ok := makePinDIB(p.img)
	if !ok {
		return false
	}
	p.dc, p.bmp, p.oldBmp = dc, bmp, old
	return true
}

func (p *pin) release() {
	freePinDIB(p.dc, p.bmp, p.oldBmp)
	p.dc, p.bmp, p.oldBmp = 0, 0, 0
}

// setImage 换掉贴图内容并保持窗口中心不动。
//
// 旋转 90° 会交换宽高，如果只改尺寸不重摆位置，图会往右下角长出去一截；
// 保持中心是唯一不会让「刚刚对齐好的参考图」跑掉的锚点。
// 新 DIB 建成功之后才释放旧的，中途失败时原图与资源都还在。
func (p *pin) setImage(img *Bitmap) bool {
	dc, bmp, old, ok := makePinDIB(img)
	if !ok {
		return false
	}
	var rc rectStruct
	procGetWindowRect.Call(p.hwnd, uintptr(unsafe.Pointer(&rc)))
	cx := int(rc.Left) + p.curW/2
	cy := int(rc.Top) + p.curH/2

	freePinDIB(p.dc, p.bmp, p.oldBmp)
	p.dc, p.bmp, p.oldBmp = dc, bmp, old
	p.img = img

	nw := maxInt(1, int(math.Round(float64(img.W)*p.zoom)))
	nh := maxInt(1, int(math.Round(float64(img.H)*p.zoom)))
	p.curW, p.curH = nw, nh
	procSetWindowPos.Call(p.hwnd, 0,
		iptr(cx-nw/2), iptr(cy-nh/2), uintptr(nw), uintptr(nh),
		swpNoZOrder|swpNoActivate)
	procInvalidateRect.Call(p.hwnd, 0, 1)
	return true
}

// transformImage 应用一次像素级变换（旋转 / 翻转）。
func (p *pin) transformImage(fn func(*Bitmap) *Bitmap) {
	if p.img == nil || p.img.Empty() {
		return
	}
	next := fn(p.img)
	if next == nil || next.Empty() {
		return
	}
	if !p.setImage(next) {
		logger.W("[pin] 变换贴图失败：重建位图资源失败")
	}
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
	p.zoomTo(p.zoom * math.Pow(pinWheelStep, steps))
}

// zoomTo 把窗口缩放到指定倍率。单独拆出来是为了「恢复 1:1」能复用
// 同一套「以光标为锚点 + 夹住倍率」的逻辑，不出现第二份位置计算。
func (p *pin) zoomTo(nz float64) {
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

// adjustAlpha 调整整体不透明度。只改窗口属性，不动 DIB：
// SetLayeredWindowAttributes 会在合成阶段把整张窗口乘上这个 alpha。
func (p *pin) adjustAlpha(delta int) {
	v := clampInt(int(p.alpha)+delta, pinMinAlpha, pinMaxAlpha)
	if v == int(p.alpha) {
		return
	}
	p.alpha = byte(v)
	procSetLayeredWindowAttributes.Call(p.hwnd, 0, uintptr(p.alpha), lwaAlpha)
	procInvalidateRect.Call(p.hwnd, 0, 1)
}

// setPierce 切换鼠标穿透。
//
// 【为什么必须配一个逃生通道】WS_EX_TRANSPARENT 对分层窗口的命中测试生效后，
// 这张贴图既收不到鼠标消息、也弹不出右键菜单，用户没法靠「再点一下菜单」关掉它。
// 因此穿透期间挂一个 120ms 定时器轮询全局按键：Esc 或 Ctrl+Shift+T 退出。
// GetAsyncKeyState 读的是全局异步状态，窗口有没有焦点都能读；WM_TIMER 是投递给
// 窗口的消息，穿透不影响它到达。定时器建不起来时也不算致命，菜单里的 T 仍能开，
// 而 Esc 在窗口还有焦点时由 onKeyDown 兜住。
func (p *pin) setPierce(on bool) {
	if p.pierce == on {
		return
	}
	ex, _, _ := procGetWindowLongPtrW.Call(p.hwnd, iptr(gwlpExStyle))
	if on {
		ex |= wsExTransparent
	} else {
		ex &^= wsExTransparent
	}
	procSetWindowLongPtrW.Call(p.hwnd, iptr(gwlpExStyle), ex)
	p.pierce = on

	if on {
		if !p.pierceTimer {
			if r, _, _ := procSetTimer.Call(p.hwnd, pinTimerPierce, 120, 0); r != 0 {
				p.pierceTimer = true
			}
		}
		return
	}
	if p.pierceTimer {
		procKillTimer.Call(p.hwnd, pinTimerPierce)
		p.pierceTimer = false
	}
}

func (p *pin) onPierceTimer() {
	if !p.pierce {
		return
	}
	if keyPressed(vkEscape) || (keyPressed(vkControl) && keyPressed(vkShift) && keyPressed(vkT)) {
		p.setPierce(false)
	}
}

// setTopmost 切换置顶。hWndInsertAfter 不能配 SWP_NOZORDER，
// 否则这个参数会被系统忽略、看起来「点了没反应」。
func (p *pin) setTopmost(on bool) {
	if p.topmost == on {
		return
	}
	after := hwndNoTopmost
	if on {
		after = hwndTopmost
	}
	procSetWindowPos.Call(p.hwnd, after, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoActivate)
	p.topmost = on
}

// nudge 用方向键微调贴图位置（Shift 加速 10px）。
func (p *pin) nudge(dx, dy int) {
	var rc rectStruct
	procGetWindowRect.Call(p.hwnd, uintptr(unsafe.Pointer(&rc)))
	procSetWindowPos.Call(p.hwnd, 0,
		iptr(int(rc.Left)+dx), iptr(int(rc.Top)+dy), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate)
}

// onKeyDown 处理贴图窗口的快捷键。菜单里每条都标了对应的键，
// 两条入口走的是同一组方法，不会出现「菜单能用、快捷键不能用」的分叉。
func (p *pin) onKeyDown(vk uintptr) {
	step := 1
	if keyPressed(vkShift) {
		step = pinNudgeStep
	}
	switch vk {
	case vkEscape:
		if p.pierce {
			p.setPierce(false)
			return
		}
		procDestroyWindow.Call(p.hwnd)
	case vkR:
		if keyPressed(vkShift) {
			p.transformImage(rotateBitmapCCW)
		} else {
			p.transformImage(rotateBitmapCW)
		}
	case vkH:
		p.transformImage(flipBitmapH)
	case vkV:
		p.transformImage(flipBitmapV)
	case vkT:
		// 组合键留给穿透期间的全局轮询，别在这里抢着 toggle 两次。
		if !keyPressed(vkControl) {
			p.setPierce(!p.pierce)
		}
	case vk0:
		p.zoomTo(1)
	case vkOEMPlus:
		p.adjustAlpha(pinAlphaStep)
	case vkOEMMinus:
		p.adjustAlpha(-pinAlphaStep)
	case vkLeft:
		p.nudge(-step, 0)
	case vkRight:
		p.nudge(step, 0)
	case vkUp:
		p.nudge(0, -step)
	case vkDown:
		p.nudge(0, step)
	}
}

// showMenu 弹原生右键菜单。用 TPM_RETURNCMD 直接拿返回值，
// 省掉一套 WM_COMMAND 分发。
func (p *pin) showMenu() {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	appendMenu(hMenu, "顺时针旋转 90° (R)", pinMenuRotateCW)
	appendMenu(hMenu, "逆时针旋转 90° (Shift+R)", pinMenuRotateCCW)
	appendMenu(hMenu, "水平翻转 (H)", pinMenuFlipH)
	appendMenu(hMenu, "垂直翻转 (V)", pinMenuFlipV)
	appendMenu(hMenu, "", 0) // 分隔线
	appendMenu(hMenu, fmt.Sprintf("更不透明 (+)  当前 %d%%", pinAlphaPercent(p.alpha)), pinMenuAlphaUp)
	appendMenu(hMenu, "更透明 (-)  ·  Ctrl+滚轮", pinMenuAlphaDown)
	appendMenu(hMenu, "", 0)
	appendMenu(hMenu, pinCheckText("鼠标穿透 (T，Esc 退出)", p.pierce), pinMenuPierce)
	appendMenu(hMenu, pinCheckText("置顶显示", p.topmost), pinMenuTopmost)
	appendMenu(hMenu, "恢复 1:1 (0)", pinMenuResetZoom)
	appendMenu(hMenu, "", 0)
	appendMenu(hMenu, "复制到剪贴板", pinMenuCopy)
	appendMenu(hMenu, "保存为文件…", pinMenuSave)
	appendMenu(hMenu, "", 0) // 分隔线
	appendMenu(hMenu, "关闭 (Esc / 双击)", pinMenuClose)

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
	case pinMenuRotateCW:
		p.transformImage(rotateBitmapCW)
	case pinMenuRotateCCW:
		p.transformImage(rotateBitmapCCW)
	case pinMenuFlipH:
		p.transformImage(flipBitmapH)
	case pinMenuFlipV:
		p.transformImage(flipBitmapV)
	case pinMenuAlphaUp:
		p.adjustAlpha(pinAlphaStep)
	case pinMenuAlphaDown:
		p.adjustAlpha(-pinAlphaStep)
	case pinMenuPierce:
		p.setPierce(!p.pierce)
	case pinMenuTopmost:
		p.setTopmost(!p.topmost)
	case pinMenuResetZoom:
		p.zoomTo(1)
	}
}

// pinCheckText 给可开关的菜单项加统一的勾选框。用文字标记而不是 MF_CHECKED，
// 是为了不改动 appendMenu 的签名（它还被分隔线复用），两种状态宽度一致不跳字。
func pinCheckText(s string, on bool) string {
	if on {
		return "✓ " + s
	}
	return "□ " + s
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
			// CS_DROPSHADOW：让系统给无边框 WS_POPUP 画一圈投影，贴图压在
			// 文档上时能看出边界。DWM 对个别 layered 窗口可能不画投影，
			// 只是少了视觉层次，不影响任何功能。
			Style:         csDblClks | csDropShadow,
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
		// 贴图初始是 SW_SHOWNOACTIVATE（刻意不抢用户当前窗口的焦点）。
		// 点过一下之后把焦点交给它，键盘快捷键才收得到；否则用户得先
		// 右键开一次菜单（showMenu 里会置前）才能用 R / H / V / T。
		procSetForegroundWindow.Call(hwnd)
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
		delta := highWord(wParam)
		if keyPressed(vkControl) {
			// Ctrl+滚轮调透明度；不按 Ctrl 仍是原来的以光标为锚点缩放。
			step := pinAlphaStep
			if delta < 0 {
				step = -step
			}
			p.adjustAlpha(step)
		} else {
			p.zoomBy(delta)
		}
		return 0

	case wmKeyDown:
		p.onKeyDown(wParam)
		return 0

	case wmTimer:
		if wParam == pinTimerPierce {
			p.onPierceTimer()
		}
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
		if p.pierceTimer {
			procKillTimer.Call(hwnd, pinTimerPierce)
			p.pierceTimer = false
		}
		p.release()
		// 钉钉张图就往注册表里塞一个 HWND，关掉不删会一直攒着。
		pinRegistry.Delete(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}
