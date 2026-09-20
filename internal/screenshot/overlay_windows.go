//go:build windows

package screenshot

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"quickdock/internal/logger"
)

// 覆盖整个虚拟桌面的原生分层窗口，承载「框选 → 工具条 → 标注 → 导出」全流程。
//
// 为什么不用 Wails 的 WebviewWindow：
//   - WebView2 渲染在独立子 HWND 里，ShowWindow 之后要等它 paint 出第一帧；
//     这段时间窗口已覆盖全屏却是纯色空白——就是肉眼可见的"闪屏"。
//   - 且 v3 alpha2.122 在 Windows 上没有透明窗口的一等支持（WindowsWindow 结构体
//     无对应透明字段），做不出"暗化桌面 + 高亮选区"的效果。
//
// 工具条与标注图形同样画进这张 DIB，共用一次 UpdateLayeredWindow 提交。
// 因此「框选完工具条凭空出现」也不会闪——它不是第二个窗口，只是同一帧里多画了几百
// 个像素。
//
// 本实现的关键点：
//  1. 启动时预创建窗口（1×1 隐藏），热键触发时不再承担建窗开销。
//  2. 内容画进一张 32 位 top-down DIB，用 UpdateLayeredWindow 整帧提交，
//     不走 WM_PAINT，不存在"先铺底色、再等内容"的两步。
//  3. 严格「先画完、后显示」：ShowWindow 落下时像素已就位，无空窗期。
type Overlay struct {
	cmdCh  chan *overlayCmd
	mu     sync.Mutex
	closed bool

	// showing 标记覆盖层正在使用中。连按热键时用来挡掉重复请求：
	// 后到的直接以「取消」返回，不会打断正在进行的那次。
	showing atomic.Bool

	// hwnd 由消息线程写入，Show 只读；Start 返回后即为稳定值。
	hwnd uintptr

	// ===== 以下字段仅消息线程访问 =====

	className  *uint16
	hInstance  uintptr
	hMemDC     uintptr
	hBitmap    uintptr
	hOldBitmap uintptr
	hFont      uintptr
	pixels     unsafe.Pointer
	dibPix     []byte // 提交给窗口的整帧像素
	base       []byte // 截图 + 暗化的预合成底图
	src        *Bitmap
	bounds     Rect
	resultCh   chan outcome

	// 会话状态
	phase    phase
	drag     dragMode
	sel      Rect // 选区（客户区坐标）
	dragOrig Rect // 开始拖拽时的选区
	dragFrom pt   // 按下时的鼠标位置

	activeTool toolID
	hoverIdx   int
	shapes     []shape
	draft      *shape

	// 撤销 / 重做存的是「整张图形表的快照」，而不是逐条操作记录。
	//
	// 图形只是几十字节的小结构体，快照一份的代价远小于为每种操作（新增 / 移动 /
	// 改字 / 删除）各写一套逆操作；也顺带避免了一类很难解释的不一致——
	// 早先 redoStack 只有 shape，配合「改选区会裁剪图形」的行为，
	// 撤销有可能弹掉一个用户根本看不见的图形。
	history []snapshot
	future  []snapshot

	// selected 是当前被选中的图形下标（-1 表示无）。
	// moveIndex / moveOrig 记录拖动开始时的下标与原始图形，
	// 前者用来把每帧的位移换算成绝对位置（避免逐帧累加产生漂移），
	// 后者用来判断这次拖动到底有没有产生位移。
	selected  int
	moveIndex int
	moveOrig  shape

	// frameOrig 是选框平移 / 缩放开始时的选区，用来判断这次拖框有没有实际变化
	// （点了一下没动就不该占一个撤销位）。
	frameOrig Rect

	// 窗口吸附（实现见 snap_windows.go）。
	// snap 是光标下窗口在**客户区坐标**下的矩形；snapOK 为 false 时它无意义。
	snap   Rect
	snapOK bool
	// pendingSnap 是按下左键那一刻的吸附候选：松手时若几乎没拖动过，
	// 就把它当作「点选整个窗口」，否则按自由框选处理。
	pendingSnap   Rect
	pendingSnapOK bool

	// 标注属性（工具条上的「颜色」「线宽」「字号」按钮改的就是这三个）
	curColor    col
	curWidth    int
	curFontSize int
	panel       panelID

	// 文字标注：popup 输入窗 + GDI 文本渲染用的离屏资源
	ti        textInput
	scratch   textScratch
	fontCache map[int]uintptr

	mouseX, mouseY int
	mouseIn        bool
}

type overlayCmd struct {
	src      *Bitmap
	bounds   Rect
	resultCh chan outcome
}

type phase uint8

const (
	phaseSelecting phase = iota // 正在拖拽框选（尚无有效选区）
	phaseAdjusting              // 选区已定：可调框 / 可标注 / 工具条可见
)

type dragMode uint8

const (
	dragNone dragMode = iota
	dragNew
	dragMove
	dragResizeNW
	dragResizeN
	dragResizeNE
	dragResizeE
	dragResizeSE
	dragResizeS
	dragResizeSW
	dragResizeW
	dragShape
	// dragMoveShape 是拖动一条已画好的标注（区别于 dragShape「拖出一笔新的」）。
	dragMoveShape
)

const (
	minSelectExtent = 5 // 小于此尺寸的框选视为误触
	handleSize      = 7 // 控制点边长
	handleGrip      = 6 // 控制点的命中容差（±像素）
	hintHeight      = 22
	hintPadX        = 10

	// historyLimit 是撤销栈深度上限。图形是小结构体，60 步只占几 KB，
	// 足够覆盖一次截图会话里的全部操作。
	historyLimit = 60
)

// colSelBorder 是选区边框/控制点描边色（BGR 顺序的 #0078D7）。
var colSelBorder = col{0xD7, 0x78, 0x00}

var (
	// 回调必须保存在包级变量，否则会被 GC 回收导致窗口过程变成野指针。
	overlayWndProcCallback = syscall.NewCallback(overlayWndProc)
	overlayInstance        *Overlay
)

// NewOverlay 创建覆盖窗口管理器。真正建窗发生在 Start。
func NewOverlay() *Overlay { return &Overlay{} }

// Start 创建覆盖窗口并启动其消息泵（独立 OS 线程，GetMessage 阻塞）。
// 应在 app.Run() 之前调用：预创建后，热键触发路径只剩"抓屏 + 投递命令 + 显示窗口"。
func (o *Overlay) Start() error {
	o.cmdCh = make(chan *overlayCmd, 4)
	overlayInstance = o

	ready := make(chan error, 1)
	go o.messageLoop(ready)
	return <-ready
}

// Show 显示覆盖层并把会话结果写入返回的 channel。
// 用户取消或选区无效时，channel 收到零值 outcome。
func (o *Overlay) Show(src *Bitmap, bounds Rect, result chan outcome) {
	// 连按热键时挡掉重复请求：正在用就以「取消」立刻返回，
	// 不打断已显示的那一次。
	if !o.showing.CompareAndSwap(false, true) {
		close(result)
		return
	}

	o.mu.Lock()
	hwnd := o.hwnd
	closed := o.closed
	o.mu.Unlock()
	if closed || hwnd == 0 {
		o.showing.Store(false)
		close(result)
		return
	}

	o.cmdCh <- &overlayCmd{src: src, bounds: bounds, resultCh: result}
	procPostMessageW.Call(hwnd, wmOverlayShow, 0, 0)
}

// Stop 结束消息泵并销毁窗口。
func (o *Overlay) Stop() {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	hwnd := o.hwnd
	o.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmOverlayStop, 0, 0)
	}
}

func (o *Overlay) messageLoop(ready chan<- error) {
	// GetMessage 阻塞等待，必须独占一个 OS 线程。
	runtime.LockOSThread()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	o.hInstance = hInst

	cls, _ := syscall.UTF16PtrFromString("QuickDock_Screenshot_Overlay_v1")
	o.className = cls

	// 用 RegisterClassW + WNDCLASSW（与 platform 包的剪贴板监听窗口同一范式）。
	// HbrBackground 留 0：否则 ShowWindow 时系统会先擦一遍背景，照样闪。
	wc := wndClassW{
		LpfnWndProc:   overlayWndProcCallback,
		HInstance:     hInst,
		HCursor:       loadCursor(idcCross),
		LpszClassName: cls,
	}
	if ret, _, _ := procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		ready <- fmt.Errorf("screenshot: 注册覆盖窗口类失败")
		return
	}

	// 预创建，初始 1×1 且不显示；真正的位置尺寸在每次 Show 时给定。
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExLayered|wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(cls)),
		0,
		wsPopup,
		0, 0, 1, 1,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		ready <- fmt.Errorf("screenshot: 创建覆盖窗口失败")
		return
	}

	o.mu.Lock()
	o.hwnd = hwnd
	o.mu.Unlock()
	ready <- nil

	var msg msgStruct
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 { // WM_QUIT
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	o.releaseDraw()
	o.scratch.release()
	o.destroyTextInput()
	if o.className != nil {
		procUnregisterClassW.Call(uintptr(unsafe.Pointer(o.className)), o.hInstance)
	}
	logger.I("[screenshot] 覆盖窗口消息泵已退出")
}

// ===== 会话生命周期 =====

// handleShow 在消息线程上执行一次完整的"显示覆盖层"流程。
func (o *Overlay) handleShow(cmd *overlayCmd) {
	if cmd == nil || cmd.src == nil || cmd.bounds.Empty() {
		return
	}
	// src 必须是整虚拟桌面的截图：尺寸对不上时逐行拷贝会越界。
	if len(cmd.src.Pix) < cmd.bounds.W*cmd.bounds.H*4 {
		logger.W("[screenshot] 截图尺寸与虚拟桌面不符，放弃本次框选")
		o.deliver(outcome{})
		return
	}

	// 1. 先藏起来，清掉上一轮的残留内容。
	procShowWindow.Call(o.hwnd, swHide)

	o.src = cmd.src
	o.bounds = cmd.bounds
	o.resultCh = cmd.resultCh
	o.hideTextInput()
	o.resetSession()

	// 2. 建绘制资源 + 预合成暗化底图（一次性开销，不参与逐帧重绘）。
	if !o.prepareDraw() {
		o.cancel()
		return
	}
	o.composeBase()

	// 3. 位置尺寸在显示之前定死，避免先显示再 resize 造成的"跳一下"。
	procSetWindowPos.Call(o.hwnd, hwndTopmost,
		iptr(o.bounds.X), iptr(o.bounds.Y),
		uintptr(o.bounds.W), uintptr(o.bounds.H), 0)

	// 4. 把像素全部画完——此刻窗口仍不可见，用户看不到任何中间态。
	o.redraw()

	// 5. 再显示：UpdateLayeredWindow 已把整帧内容提交，Show 落下一帧到位。
	procShowWindow.Call(o.hwnd, swShow)

	// 6. 抢焦点以接收 Esc 与快捷键。
	procSetForegroundWindow.Call(o.hwnd)
	procSetFocus.Call(o.hwnd)
}

func (o *Overlay) resetSession() {
	o.phase = phaseSelecting
	o.drag = dragNone
	o.sel = Rect{}
	o.dragOrig = Rect{}
	o.dragFrom = pt{}
	o.activeTool = toolNone
	o.hoverIdx = -1
	o.shapes = o.shapes[:0]
	o.draft = nil
	o.history = o.history[:0]
	o.future = o.future[:0]
	o.selected = -1
	o.snap = Rect{}
	o.snapOK = false
	o.pendingSnap = Rect{}
	o.pendingSnapOK = false
	o.curColor = colAnnot
	o.curWidth = annotWidths[0] // 默认最细
	o.curFontSize = annotFontSize
	o.panel = panelNone
	o.mouseX, o.mouseY = -1, -1
	o.mouseIn = false
}

// finishWith 结束会话并按 action 交付结果。
//
// 会话结束前先把进行中的文字输入结掉：正在打字时点「复制」应该带上这段字，
// 而不是静默丢掉。
func (o *Overlay) finishWith(a Action) {
	if o.ti.active {
		if a == ActionCancel {
			o.cancelTextInput()
		} else {
			o.commitTextInput()
		}
	}
	if a == ActionCancel || o.sel.Empty() {
		o.deliver(outcome{})
		return
	}
	o.deliver(outcome{action: a, rect: o.selAbs(), image: o.renderSelection()})
}

// cancel 放弃本次会话。
func (o *Overlay) cancel() { o.deliver(outcome{}) }

// deliver 把结果交给等待方（非阻塞，避免卡住消息线程）。
func (o *Overlay) deliver(res outcome) {
	ch := o.resultCh
	o.resultCh = nil
	o.finish()
	if ch != nil {
		select {
		case ch <- res:
		default:
		}
		close(ch)
	}
}

// finish 隐藏覆盖层并释放本轮的位图引用。
func (o *Overlay) finish() {
	procShowWindow.Call(o.hwnd, swHide)
	o.hideTextInput()
	o.releaseDraw()
	o.src = nil
	o.base = nil
	o.shapes = o.shapes[:0]
	o.history = o.history[:0]
	o.future = o.future[:0]
	o.draft = nil
	o.selected = -1
	o.snapOK = false
	o.pendingSnapOK = false
	o.panel = panelNone
	o.showing.Store(false)
}

// selAbs 把选区换算到虚拟桌面坐标系。
func (o *Overlay) selAbs() Rect {
	return Rect{
		X: o.bounds.X + o.sel.X,
		Y: o.bounds.Y + o.sel.Y,
		W: o.sel.W,
		H: o.sel.H,
	}
}

// ===== 绘制资源 =====

// prepareDraw 建 32bpp top-down DIB 与提示文字字体。
func (o *Overlay) prepareDraw() bool {
	o.releaseDraw()

	hScreenDC, _, _ := procGetDC.Call(0)
	if hScreenDC == 0 {
		return false
	}
	defer procReleaseDC.Call(0, hScreenDC)

	hMemDC, _, _ := procCreateCompatibleDC.Call(hScreenDC)
	if hMemDC == 0 {
		return false
	}
	o.hMemDC = hMemDC

	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(o.bounds.W),
			Height:      -int32(o.bounds.H), // top-down
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
		},
	}
	var pixels unsafe.Pointer
	hBitmap, _, _ := procCreateDIBSection.Call(
		hMemDC, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&pixels)), 0, 0,
	)
	if hBitmap == 0 || pixels == nil {
		return false
	}
	o.hBitmap = hBitmap
	o.pixels = pixels
	o.dibPix = unsafe.Slice((*byte)(pixels), o.bounds.W*o.bounds.H*4)

	o.hOldBitmap, _, _ = procSelectObject.Call(hMemDC, hBitmap)

	// 提示文字用 GDI 绘制，支持中文；字体建一次复用。
	face, _ := syscall.UTF16PtrFromString("Microsoft YaHei UI")
	// 字号必须走变量：常量负值转 uintptr 属于编译期求值，会被判溢出
	// （iptr(-14) 同样报错）；运行时转换才拿得到正确的 32 位补码。
	fontHeight := -14
	hFont, _, _ := procCreateFontW.Call(
		iptr(fontHeight), 0, 0, 0, 400, 0, 0, 0,
		1 /*DEFAULT_CHARSET*/, 0, 0, 5 /*CLEARTYPE_QUALITY*/, 0,
		uintptr(unsafe.Pointer(face)),
	)
	o.hFont = hFont

	return true
}

func (o *Overlay) releaseDraw() {
	if o.hMemDC != 0 {
		if o.hOldBitmap != 0 {
			procSelectObject.Call(o.hMemDC, o.hOldBitmap)
		}
		if o.hBitmap != 0 {
			procDeleteObject.Call(o.hBitmap)
		}
		procDeleteDC.Call(o.hMemDC)
	}
	if o.hFont != 0 {
		procDeleteObject.Call(o.hFont)
	}
	// 文字标注的字号按高度缓存，会话结束一并释放，避免跨会话累积 GDI 对象。
	for h, hf := range o.fontCache {
		if hf != 0 {
			procDeleteObject.Call(hf)
		}
		delete(o.fontCache, h)
	}
	o.hMemDC, o.hBitmap, o.hOldBitmap, o.hFont = 0, 0, 0, 0
	o.pixels, o.dibPix = nil, nil
}

// composeBase 把原始截图按 50% 亮度合成到底图，作为每帧重绘的还原起点。
// 这样逐帧重绘只需要一次 memmove + 选区局部还原，而不是每次全屏逐像素重算。
func (o *Overlay) composeBase() {
	if o.src == nil {
		return
	}
	n := o.bounds.W * o.bounds.H * 4
	if len(o.base) != n {
		o.base = make([]byte, n)
	}
	copy(o.base, o.src.Pix)

	// 亮度 ×0.5（右移一位）；alpha 保持不透明。
	for i := 0; i+3 < n; i += 4 {
		o.base[i] >>= 1
		o.base[i+1] >>= 1
		o.base[i+2] >>= 1
		o.base[i+3] = 0xFF
	}
}

// screenPainter 是不带裁剪的画笔：用于选区边框、控制点、尺寸提示、辅助线、工具条。
// 这些元素本就压在选区边界上或选区之外，带选区裁剪会被切掉一半。
func (o *Overlay) screenPainter() *painter {
	return &painter{pix: o.dibPix, w: o.bounds.W, h: o.bounds.H}
}

// shapePainter 是裁剪到选区的画笔：标注图形不允许画出选区。
func (o *Overlay) shapePainter() *painter {
	return &painter{pix: o.dibPix, w: o.bounds.W, h: o.bounds.H, clip: o.sel}
}

// ===== 逐帧重绘 =====

func (o *Overlay) redraw() {
	if o.dibPix == nil || len(o.base) == 0 {
		return
	}

	copy(o.dibPix, o.base)

	// 窗口吸附高亮先画：它在选区之外，会被后面的选区边框/工具条压住是正确的层次。
	// 拖拽中不画——那会儿用户在做别的事，一个跟着光标跳的窗口框只有干扰。
	if o.snapOK && o.drag == dragNone && !o.ti.active {
		o.drawSnapHighlight()
	}

	sel := o.sel
	if !sel.Empty() {
		// 选区内还原成未暗化的原图
		o.restoreRegion(sel)

		// 重放标注图形（含正在拖拽中的那一笔）。
		// 正在被重新编辑的那条文字要跳过：它已经在输入框里了，
		// 再叠一份原文会在插入符旁边形成重影。
		sp := o.shapePainter()
		editing := o.editingIndex()
		for i := range o.shapes {
			if i == editing {
				continue
			}
			o.drawShape(sp, &o.shapes[i])
		}
		if o.draft != nil {
			o.drawShape(sp, o.draft)
		}

		// 被选中的图形套一圈虚线框，说明「拖动我 = 移动这个图形」。
		// 正在编辑的那条文字不画：它此刻不在画面上，画个框会很怪。
		if o.selected >= 0 && o.selected < len(o.shapes) && o.selected != editing {
			o.drawShapeSelection(o.shapeBounds(&o.shapes[o.selected]))
		}

		// 十字对齐辅助线：**只在拖拽过程中出现**。
		// 早先的写法是「编辑态只要鼠标在屏内就画」，那等于全程挂两条贯穿全屏的线，
		// 既挡内容又没有信息量。对齐只在拖的时候才有意义。
		if o.drag != dragNone {
			o.drawGuides()
		}

		// 文字输入中：在落字位置画一根插入符，告诉用户文字会从哪开始
		if o.ti.active {
			o.drawTextCaret()
		}

		o.drawBorder(sel)
		o.drawSizeHint(sel)

		if o.phase == phaseAdjusting {
			o.drawHandles(sel)
		}
	}

	if o.phase == phaseAdjusting {
		o.drawToolbar()
		if o.panel != panelNone {
			o.drawPanel()
		}
	}

	o.flush()
}

// drawTextCaret 在文字锚点画一根插入符标记，高度即当前字号。
func (o *Overlay) drawTextCaret() {
	p := o.screenPainter()
	h := clampInt(o.curFontSize, 8, maxInt(8, o.sel.H))
	p.fillRect(Rect{X: o.ti.anchor.x, Y: o.ti.anchor.y, W: 2, H: h}, colActiveBg)
}

// restoreRegion 把选区内恢复成原始（未暗化）内容。
func (o *Overlay) restoreRegion(r Rect) {
	if o.src == nil || r.Empty() {
		return
	}
	w := o.bounds.W
	rowBytes := r.W * 4
	for y := 0; y < r.H; y++ {
		py := r.Y + y
		off := (py*w + r.X) * 4
		copy(o.dibPix[off:off+rowBytes], o.src.Pix[off:off+rowBytes])
	}
}

// drawBorder 画 2px 选区边框。
func (o *Overlay) drawBorder(r Rect) {
	o.screenPainter().strokeRect(r, 2, colSelBorder)
}

// drawHandles 画 8 个控制点（4 角 + 4 边中点）。
func (o *Overlay) drawHandles(r Rect) {
	p := o.screenPainter()
	half := handleSize / 2
	for _, h := range handlePoints(r) {
		box := Rect{X: h.x - half, Y: h.y - half, W: handleSize, H: handleSize}
		p.fillRect(box, colActiveIcon)
		p.strokeRect(box, 1, colSelBorder)
	}
}

// handlePoints 返回选区上 8 个控制点的位置（客户区坐标）。
func handlePoints(r Rect) [8]pt {
	cx := r.X + r.W/2
	cy := r.Y + r.H/2
	ex := r.X + r.W - 1
	ey := r.Y + r.H - 1
	return [8]pt{
		{r.X, r.Y}, {cx, r.Y}, {ex, r.Y},
		{r.X, cy}, {ex, cy},
		{r.X, ey}, {cx, ey}, {ex, ey},
	}
}

// drawGuides 画贯穿全屏的十字对齐辅助线。
// 用「提亮」而非覆盖色，这样既不遮挡底图内容，也不会在暗化背景上糊成一条白线。
//
// 只由 redraw 在拖拽中调用（见那里的注释），不要改回「编辑态常驻」。
func (o *Overlay) drawGuides() {
	if !o.mouseIn {
		return
	}
	if o.mouseX < 0 || o.mouseY < 0 || o.mouseX >= o.bounds.W || o.mouseY >= o.bounds.H {
		return
	}
	p := o.screenPainter()
	for x := 0; x < o.bounds.W; x++ {
		p.boost(x, o.mouseY, colGuideBoost)
	}
	for y := 0; y < o.bounds.H; y++ {
		p.boost(o.mouseX, y, colGuideBoost)
	}
}

// drawSizeHint 在选区左上角上方画「宽 × 高」。
func (o *Overlay) drawSizeHint(r Rect) {
	if o.hMemDC == 0 {
		return
	}
	text := fmt.Sprintf("%d × %d", r.W, r.H)
	txt, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}

	h := hintHeight
	w := len([]rune(text))*8 + hintPadX*2
	x := r.X
	y := r.Y - h - 6
	if y < 0 {
		y = r.Y + 6 // 上方放不下就贴选区内部顶边
	}
	x = clampInt(x, 0, maxInt(0, o.bounds.W-w))
	y = clampInt(y, 0, maxInt(0, o.bounds.H-h))

	box := Rect{X: x, Y: y, W: w, H: h}
	o.screenPainter().fillRoundRect(box, 4, colHintBg)

	if o.hFont != 0 {
		old, _, _ := procSelectObject.Call(o.hMemDC, o.hFont)
		procSetBkMode.Call(o.hMemDC, dtTransparent)
		procSetTextColor.Call(o.hMemDC, 0x00FFFFFF) // COLORREF 为 0x00BBGGRR

		rc := rectStruct{
			Left:   int32(x),
			Top:    int32(y),
			Right:  int32(x + w),
			Bottom: int32(y + h),
		}
		// cchText 传 -1 表示按 NUL 结尾取整串；常量负值同样要走变量转换。
		cchText := -1
		procDrawTextW.Call(
			o.hMemDC,
			uintptr(unsafe.Pointer(txt)),
			iptr(cchText),
			uintptr(unsafe.Pointer(&rc)),
			dtCenter|dtVCenter|dtSingleLine|dtNoPrefix,
		)
		procSelectObject.Call(o.hMemDC, old)

		// GDI 文本绘制对 alpha 字节没有保证，被写 0 时文字会变成透明洞。
		o.fixAlpha(box)
	}
}

// fixAlpha 把矩形范围内的 alpha 强制补齐为不透明。
func (o *Overlay) fixAlpha(r Rect) {
	r = r.intersect(Rect{X: 0, Y: 0, W: o.bounds.W, H: o.bounds.H})
	if r.Empty() {
		return
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		off := (y*o.bounds.W + r.X) * 4
		for x := 0; x < r.W; x++ {
			o.dibPix[off+3] = 0xFF
			off += 4
		}
	}
}

// flush 把整帧 DIB 提交给分层窗口。
func (o *Overlay) flush() {
	ptSrc := pointStruct{X: 0, Y: 0}
	ptDst := pointStruct{X: int32(o.bounds.X), Y: int32(o.bounds.Y)}
	size := pointStruct{X: int32(o.bounds.W), Y: int32(o.bounds.H)}
	blend := blendFunction{
		BlendOp:             acSrcOver,
		SourceConstantAlpha: 255,
		AlphaFormat:         acSrcAlpha,
	}
	procUpdateLayeredWindow.Call(
		o.hwnd, 0,
		uintptr(unsafe.Pointer(&ptDst)),
		uintptr(unsafe.Pointer(&size)),
		o.hMemDC,
		uintptr(unsafe.Pointer(&ptSrc)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		ulwAlpha,
	)
}

// renderSelection 生成最终结果位图：选区原图 + 重放全部标注图形。
//
// 刻意不直接截取 dibPix——那上面还叠着选框边框、控制点、尺寸提示、十字辅助线
// 和工具条，没有一样能进最终图片。重新合成比事后再擦一遍可靠得多。
func (o *Overlay) renderSelection() *Bitmap {
	r := o.sel
	out := &Bitmap{W: r.W, H: r.H, Pix: make([]byte, r.W*r.H*4)}

	rowBytes := r.W * 4
	for y := 0; y < r.H; y++ {
		srcOff := ((r.Y+y)*o.bounds.W + r.X) * 4
		copy(out.Pix[y*rowBytes:(y+1)*rowBytes], o.src.Pix[srcOff:srcOff+rowBytes])
	}

	// dx/dy 把客户区坐标平移进结果位图，clip 保证图形不越出选区。
	p := &painter{pix: out.Pix, w: r.W, h: r.H, dx: -r.X, dy: -r.Y, clip: r}
	for i := range o.shapes {
		o.drawShape(p, &o.shapes[i])
	}
	return out
}

// ===== 撤销 / 重做 =====

// snapshot 是一次可撤销的会话状态：选区 + 全部图形。
//
// 把选区也放进快照，是因为「移动 / 缩放选框」本身就是一个会改变最终结果的动作，
// 而且它还会连带裁掉框外的图形——只有连框一起还原，撤销才是完整的。
type snapshot struct {
	sel    Rect
	shapes []shape
}

// cloneShapes 复制一份图形表。
// 快照必须是独立数组：还原之后用户还会继续原地修改图形，共享底层数组会写脏历史。
func (o *Overlay) cloneShapes() []shape {
	out := make([]shape, len(o.shapes))
	copy(out, o.shapes)
	return out
}

func copyShapes(s []shape) []shape {
	out := make([]shape, len(s))
	copy(out, s)
	return out
}

// snapshotNow 取当前状态的快照。
func (o *Overlay) snapshotNow() snapshot {
	return snapshot{sel: o.sel, shapes: o.cloneShapes()}
}

// pushSnapshot 压入一条撤销记录并清空重做栈。
func (o *Overlay) pushSnapshot(s snapshot) {
	o.history = append(o.history, s)
	if len(o.history) > historyLimit {
		o.history = o.history[len(o.history)-historyLimit:]
	}
	o.future = o.future[:0]
}

// pushHistory 在**任何会改变结果的拖拽开始时**调用：先把当前状态存起来。
//
// 配套约定：如果这次拖拽最后什么也没改（点了一下没动、拖出的图形太小被丢弃），
// 调用 dropLastHistory 把这条记录弹掉——否则撤销栈里会塞满「按了没反应」的空步。
func (o *Overlay) pushHistory() { o.pushSnapshot(o.snapshotNow()) }

// dropLastHistory 撤回最后一次 pushHistory（本次操作没产生实际变化）。
func (o *Overlay) dropLastHistory() {
	if n := len(o.history); n > 0 {
		o.history = o.history[:n-1]
	}
}

func (o *Overlay) applySnapshot(s snapshot) {
	o.sel = s.sel
	o.shapes = copyShapes(s.shapes)
	o.clampSelected()
}

func (o *Overlay) undo() {
	if len(o.history) == 0 {
		return
	}
	o.future = append(o.future, o.snapshotNow())
	last := o.history[len(o.history)-1]
	o.history = o.history[:len(o.history)-1]
	o.applySnapshot(last)
}

func (o *Overlay) redo() {
	if len(o.future) == 0 {
		return
	}
	o.history = append(o.history, o.snapshotNow())
	last := o.future[len(o.future)-1]
	o.future = o.future[:len(o.future)-1]
	o.applySnapshot(last)
}

// ===== 图形选中 / 移动 / 删除 =====

// clampSelected 在图形表变化后清掉越界的选中下标。
func (o *Overlay) clampSelected() {
	if o.selected >= len(o.shapes) {
		o.selected = -1
	}
	if o.moveIndex >= len(o.shapes) {
		o.moveIndex = -1
	}
}

// shapeBounds 返回图形在客户区坐标下的外接矩形（按线宽外扩）。
//
// 外扩是必需的：水平或垂直的线 / 箭头外接框有一边为 0，
// 直接拿去判「是否还在选区内」会把它们全部当成空而误删。
func (o *Overlay) shapeBounds(s *shape) Rect {
	var r Rect
	if s.kind == shapeText {
		r = o.textBounds(s)
	} else {
		r = normRect(s.x0, s.y0, s.x1, s.y1)
	}
	pad := s.width
	if pad < 1 {
		pad = 1
	}
	return Rect{X: r.X - pad, Y: r.Y - pad, W: r.W + pad*2, H: r.H + pad*2}
}

// nearRectBorder 报告点是否落在矩形边框的 pad 像素内。
func nearRectBorder(r Rect, x, y, pad int) bool {
	if !r.Contains(x, y) {
		return false
	}
	return x < r.X+pad || x >= r.X+r.W-pad || y < r.Y+pad || y >= r.Y+r.H-pad
}

// distToSegment 返回点 (px, py) 到线段 (x0,y0)-(x1,y1) 的距离。
func distToSegment(px, py, x0, y0, x1, y1 int) float64 {
	dx := float64(x1 - x0)
	dy := float64(y1 - y0)
	if dx == 0 && dy == 0 {
		return math.Hypot(float64(px-x0), float64(py-y0))
	}
	t := (float64(px-x0)*dx + float64(py-y0)*dy) / (dx*dx + dy*dy)
	switch {
	case t < 0:
		t = 0
	case t > 1:
		t = 1
	}
	return math.Hypot(float64(px)-(float64(x0)+t*dx), float64(py)-(float64(y0)+t*dy))
}

// shapeHit 报告点是否命中第 i 个图形。
//
// 命中策略按图形类型区分，不是简单的「外接框内即命中」：
//   - 线 / 箭头：点到线段的距离 ≤ 半线宽 + 容差（外接框对斜线来说太大了）；
//   - 矩形 / 椭圆：边框附近直接命中；内部只有在「它已经被选中」时才算命中，
//     否则一个画满整框的大矩形会把里面所有小图形全挡住、谁也选不中；
//   - 马赛克 / 文字：整个区域都算（前者本来就是一片色块，后者笔画太细要点得中）。
func (o *Overlay) shapeHit(i, x, y int) bool {
	s := &o.shapes[i]
	switch s.kind {
	case shapeLine, shapeArrow:
		return distToSegment(x, y, s.x0, s.y0, s.x1, s.y1) <= float64(s.width)/2+5

	case shapeRect, shapeEllipse:
		r := o.shapeBounds(s)
		if !r.Contains(x, y) {
			return false
		}
		if nearRectBorder(r, x, y, s.width+5) {
			return true
		}
		return i == o.selected

	default: // 文字 / 马赛克
		return o.shapeBounds(s).Contains(x, y)
	}
}

// hitShapeAt 返回点命中的图形下标；从后往前找（后画的盖在上面）。无则 -1。
func (o *Overlay) hitShapeAt(x, y int) int {
	for i := len(o.shapes) - 1; i >= 0; i-- {
		if o.shapeHit(i, x, y) {
			return i
		}
	}
	return -1
}

// selectShape 选中第 i 个图形，并把工具条的颜色 / 线宽 / 字号同步成它的值。
//
// 同步是为了「选中 → 改色 → 回车」这类操作成立：面板高亮的是这个图形的属性，
// 改完直接写回它，不必先撤销重画。
func (o *Overlay) selectShape(i int) {
	if i < 0 || i >= len(o.shapes) {
		o.selected = -1
		return
	}
	o.selected = i
	s := &o.shapes[i]
	o.curColor = s.color
	if s.width > 0 {
		o.curWidth = s.width
	}
	if s.fontSize > 0 {
		o.curFontSize = s.fontSize
	}
}

// beginShapeMove 开始拖动第 i 个图形。
func (o *Overlay) beginShapeMove(hwnd uintptr, i, x, y int) {
	if i < 0 || i >= len(o.shapes) {
		return
	}
	o.pushHistory()
	o.drag = dragMoveShape
	o.moveIndex = i
	o.moveOrig = o.shapes[i]
	o.dragFrom = pt{x, y}
	procSetCapture.Call(hwnd)
}

// moveShapeTo 把正在拖动的图形按光标位移摆到该在的位置。
//
// 每帧都从 moveOrig 重新算绝对位置（而不是逐帧累加 dx/dy）：
// 累加会把「被边界夹住的那几帧」的损失带下去，手感上表现为拖回来时对不上。
func (o *Overlay) moveShapeTo(x, y int) {
	if o.moveIndex < 0 || o.moveIndex >= len(o.shapes) {
		return
	}
	b := o.shapeBounds(&o.moveOrig)
	if b.Empty() {
		return
	}
	dx := x - o.dragFrom.x
	dy := y - o.dragFrom.y
	// 位移夹到「图形整体不越出选区」：拖出去就再也看不见、也点不回来了。
	dx = clampSpan(b.X+dx, o.sel.X, o.sel.W, b.W) - b.X
	dy = clampSpan(b.Y+dy, o.sel.Y, o.sel.H, b.H) - b.Y

	s := o.moveOrig
	s.x0, s.y0 = s.x0+dx, s.y0+dy
	s.x1, s.y1 = s.x1+dx, s.y1+dy
	o.shapes[o.moveIndex] = s
}

// endShapeMove 结束图形拖动：没真的动过就把刚压的历史弹掉。
func (o *Overlay) endShapeMove() {
	if o.moveIndex >= 0 && o.moveIndex < len(o.shapes) && o.shapes[o.moveIndex] == o.moveOrig {
		o.dropLastHistory()
	}
	o.moveIndex = -1
}

// deleteSelected 删除当前选中的图形，返回是否删掉了东西。
func (o *Overlay) deleteSelected() bool {
	i := o.selected
	if i < 0 || i >= len(o.shapes) {
		return false
	}
	o.pushHistory()
	o.shapes = append(o.shapes[:i], o.shapes[i+1:]...)
	o.selected = -1
	o.moveIndex = -1
	return true
}

// pruneShapes 丢掉完全落在选区之外的图形。
//
// 用在选框被移动 / 缩放之后。图形是**钉在图像像素上**的（改框不会跟着挪，
// 这正是为了让标注始终和它标注的内容对齐），但落到框外的那些已经不在最终结果里了，
// 留在表里只会让「撤销」弹掉一个用户根本看不见的东西。
func (o *Overlay) pruneShapes() {
	kept := o.shapes[:0]
	for i := range o.shapes {
		if o.shapeBounds(&o.shapes[i]).intersect(o.sel).Empty() {
			continue
		}
		kept = append(kept, o.shapes[i])
	}
	if len(kept) != len(o.shapes) {
		o.shapes = kept
		o.clampSelected()
	}
}

// drawShapeSelection 给选中的图形套一圈虚线框，并附一条操作提示。
//
// 提示不是装饰：选中 / 移动 / 删除这三个能力没有任何工具栏按钮承载，
// 不写出来用户不会知道「点一下图形就能拖」，也不知道 Delete 能删。
func (o *Overlay) drawShapeSelection(b Rect) {
	if b.Empty() {
		return
	}
	const pad = 2
	box := Rect{X: b.X - pad, Y: b.Y - pad, W: b.W + pad*2, H: b.H + pad*2}
	box = box.intersect(Rect{X: 0, Y: 0, W: o.bounds.W, H: o.bounds.H})
	if box.Empty() {
		return
	}
	o.screenPainter().dashedRect(box, colSelectBox)
	o.drawShapeHint(box)
}

// shapeHintText 是选中图形后的操作提示。
const shapeHintText = "拖动移动 · Delete 删除"

// drawShapeHint 在被选中图形下方（放不下则上方）画一条深色提示条。
func (o *Overlay) drawShapeHint(b Rect) {
	const (
		h     = 20
		fontH = 13
		padX  = 10
	)
	tw, th := o.textSize(shapeHintText, fontH)
	if tw <= 0 || th <= 0 {
		return
	}
	w := tw + padX*2
	x := b.X
	y := b.Y + b.H + 6
	if y+h > o.bounds.H {
		y = b.Y - h - 6
	}
	x = clampInt(x, 0, maxInt(0, o.bounds.W-w))
	y = clampInt(y, 0, maxInt(0, o.bounds.H-h))

	p := o.screenPainter()
	box := Rect{X: x, Y: y, W: w, H: h}
	p.fillRoundRect(box, 4, colHintBg)
	inner := Rect{X: x + padX, Y: y, W: w - padX*2, H: h}
	o.drawTextVCentered(p, shapeHintText, inner, fontH, colHintText)
}

// clampSpan 把一个跨度为 size 的区间起点限制在 [lo, lo+span] 之内。
// size > span（图形比选区还大）时退化为「起点落在 [lo+span-size, lo]」，
// 两端交换后再夹。
func clampSpan(v, lo, span, size int) int {
	a, b := lo, lo+span-size
	if a > b {
		a, b = b, a
	}
	return clampInt(v, a, b)
}

func (o *Overlay) setTool(t toolID) {
	if o.activeTool == t {
		o.activeTool = toolNone
	} else {
		o.activeTool = t
	}
	o.redraw()
}

// ===== 交互 =====

func (o *Overlay) onLButtonDown(hwnd uintptr, x, y int) {
	o.mouseX, o.mouseY, o.mouseIn = x, y, true

	// 吸附高亮的使命是「按下之前先告诉用户会选到哪」，按下即收。
	// 同时把它记成候选：松手时若几乎没拖动过，就把它当作「点选整个窗口」。
	o.pendingSnap, o.pendingSnapOK = o.snap, o.snapOK && o.snap.Contains(x, y)
	o.snapOK = false

	if o.ti.active {
		// 输入中点在工具条 / 弹出面板上：**不提交输入**，让工具条照常工作，
		// 之后把焦点交还输入框。这样「编辑一条文字时顺手改颜色 / 字号」一次做完。
		if o.onChromeClickIfHit(x, y) {
			o.refocusTextInput()
			return
		}
		// 其它任何位置（含选区、选区外）的点击都视为「确认输入」。
		// （点在输入窗上的消息归输入窗自己，不会走到这里。）
		// 顺带收起面板：面板开着时点空白只提交输入、不关面板会显得迟钝。
		o.panel = panelNone
		o.commitTextInput()
		return
	}

	if o.phase == phaseAdjusting {
		if o.onChromeClick(x, y) {
			return
		}
		if m, ok := o.hitHandle(x, y); ok {
			o.beginFrameDrag(hwnd, m, x, y)
			return
		}
		if o.sel.Contains(x, y) {
			switch {
			case o.activeTool == toolText:
				// 文字不走拖拽：落点即锚点，随后弹输入窗。
				// 点在已有文字上则进编辑态（改它），点在空白才新建。
				if i := o.hitTextAt(x, y); i >= 0 {
					o.editTextShape(i)
					return
				}
				o.showTextInput(x, y)
				return
			case o.activeTool != toolNone:
				// 拖出一笔新的标注。历史先压一份，松手时若这笔太小被丢弃再弹掉。
				// 顺带清掉已有选中：正在画新的，旧的那个虚线框留着只会混乱。
				o.selected = -1
				o.pushHistory()
				o.drag = dragShape
				o.draft = &shape{
					kind:  shapeKindOf(o.activeTool),
					x0:    x,
					y0:    y,
					x1:    x,
					y1:    y,
					width: o.curWidth,
					color: o.curColor,
				}
			default:
				// 没有激活绘制工具时，点在已有标注上 = 选中并拖动它。
				// 选中 / 移动 / 删除只有这一个入口，所以刻意不给绘制工具让路：
				// 否则一个画满整框的矩形会把里面所有图形都变成点不着的。
				if i := o.hitShapeAt(x, y); i >= 0 {
					o.selectShape(i)
					o.beginShapeMove(hwnd, i, x, y)
					o.redraw()
					return
				}
				o.selected = -1
				o.beginFrameDrag(hwnd, dragMove, x, y)
			}
			procSetCapture.Call(hwnd)
			return
		}
	}

	// 点在选区之外（或还没选区）→ 重开一个框，顺带清掉上一轮标注
	o.phase = phaseSelecting
	o.sel = Rect{X: x, Y: y}
	o.dragOrig = Rect{X: x, Y: y}
	o.dragFrom = pt{x, y}
	o.drag = dragNew
	o.activeTool = toolNone
	o.panel = panelNone
	o.hoverIdx = -1
	o.selected = -1
	o.shapes = o.shapes[:0]
	o.history = o.history[:0]
	o.future = o.future[:0]
	o.draft = nil
	procSetCapture.Call(hwnd)
	o.redraw()
}

// beginFrameDrag 开始一次选框平移 / 缩放。
//
// 历史先压一份（选区与图形一起进快照），松手时若框其实没动过再弹掉——
// 单纯点一下空白不该占一个撤销位。
func (o *Overlay) beginFrameDrag(hwnd uintptr, mode dragMode, x, y int) {
	o.pushHistory()
	o.frameOrig = o.sel
	o.drag = mode
	o.dragOrig = o.sel
	o.dragFrom = pt{x, y}
	procSetCapture.Call(hwnd)
}

// onChromeClick 处理落在工具条与弹出面板上的点击（普通态）。
// 返回 true 表示这次点击已被消费，调用方不该再继续别的判定。
//
// 「整条 bar 都要消费掉」（含按钮之间的内边距与分隔格）是必须的：
// 否则点在缝隙上会掉进下面「重开框」的分支，把已有选区清掉。
func (o *Overlay) onChromeClick(x, y int) bool {
	if o.phase != phaseAdjusting {
		return false
	}
	if o.panel != panelNone {
		pr := o.panelRect()
		if pr.Contains(x, y) {
			o.onPanelClick(pr, x, y)
			return true
		}
		// 点在面板外 → 关掉面板，本次点击不再有别的含义（与 Snipaste 一致，
		// 避免「关面板顺手把选区推了」的误操作）。
		o.panel = panelNone
		o.redraw()
		return true
	}
	if lay := o.layoutToolbar(o.sel); lay.bar.Contains(x, y) {
		if i := lay.hit(x, y); i >= 0 {
			o.onToolbarClick(i)
		}
		return true
	}
	return false
}

// onChromeClickIfHit 只消费**真正命中工具条或弹出面板内部**的点击。
//
// 与 onChromeClick 的差别：它不会因为「点在面板外」就顺手关面板——
// 调用它的场景是文字输入中，那里「点空白」要留给「提交输入」。
func (o *Overlay) onChromeClickIfHit(x, y int) bool {
	if o.phase != phaseAdjusting {
		return false
	}
	if o.panel != panelNone {
		if pr := o.panelRect(); pr.Contains(x, y) {
			o.onPanelClick(pr, x, y)
			return true
		}
		return false
	}
	if lay := o.layoutToolbar(o.sel); lay.bar.Contains(x, y) {
		if i := lay.hit(x, y); i >= 0 {
			o.onToolbarClick(i)
		}
		return true
	}
	return false
}

// onLButtonDblClk 处理双击：命中文字即进入编辑，与当前工具无关。
//
// 这是「点一下再编辑」的第二条路径（第一条是文字工具下的单击，见 onLButtonDown）。
// 之所以还要双击：只想改一条旧文字时，先去工具条切到文字工具、点完再切回来太绕。
func (o *Overlay) onLButtonDblClk(x, y int) {
	o.mouseX, o.mouseY, o.mouseIn = x, y, true
	if o.phase != phaseAdjusting {
		return
	}

	i := o.hitTextAt(x, y)
	if i < 0 {
		return
	}
	// 已经在编辑这一条：别重开，否则会把用户刚敲的字全选覆盖掉。
	if o.ti.active && o.ti.editIndex == i {
		return
	}

	// 双击的第一次 WM_LBUTTONDOWN 已经走过 onLButtonDown 了：文字工具下它会弹出一个
	// 空输入窗，其它工具下可能已经拉起选区平移；文字工具之外还可能留下半笔草稿。
	// 进编辑态之前先把这些全部撤掉。
	if o.ti.active {
		o.cancelTextInput()
	}
	procReleaseCapture.Call()
	o.drag = dragNone
	o.draft = nil
	o.editTextShape(i)
}

func (o *Overlay) onMouseMove(x, y int) {
	o.mouseX, o.mouseY, o.mouseIn = x, y, true

	switch {
	case o.drag == dragNew:
		o.sel = normRect(o.dragFrom.x, o.dragFrom.y, x, y)
		o.snapOK = false
	case o.drag == dragShape:
		if o.draft != nil {
			o.draft.x1, o.draft.y1 = x, y
		}
	case o.drag == dragMoveShape:
		o.moveShapeTo(x, y)
	case o.drag != dragNone:
		o.applyDrag(x, y)
	default:
		o.updateHover(x, y)
	}

	o.applyCursor(x, y)
	o.redraw()
}

// updateHover 更新悬停态：工具条高亮 + 光标下窗口的吸附候选。
func (o *Overlay) updateHover(x, y int) {
	hover := -1
	if o.phase == phaseAdjusting {
		hover = o.layoutToolbar(o.sel).hit(x, y)
	}
	o.hoverIdx = hover

	o.snapOK = false
	// 工具条 / 弹出面板 / 正在输入文字时不做吸附：这些场景下光标下方是什么窗口
	// 与用户当下要做的事无关，一个跟着跑的窗口框只会干扰。
	if hover >= 0 || o.ti.active || !o.panelRect().Empty() {
		return
	}
	// 选区内部也不吸附——那是标注的地盘。
	if !o.sel.Empty() && o.sel.Contains(x, y) {
		return
	}
	// 按住 Ctrl 下钻到元素级（光标下的子控件），默认整窗。
	if r, ok := o.snapTargetAt(o.bounds.X+x, o.bounds.Y+y, keyPressed(vkControl)); ok {
		o.snap = Rect{X: r.X - o.bounds.X, Y: r.Y - o.bounds.Y, W: r.W, H: r.H}
		o.snapOK = true
	}
}

func (o *Overlay) onLButtonUp(x, y int) {
	procReleaseCapture.Call()

	switch o.drag {
	case dragNew:
		o.drag = dragNone
		switch {
		case o.sel.W >= minSelectExtent && o.sel.H >= minSelectExtent:
			o.phase = phaseAdjusting
		case o.pendingSnapOK:
			// 按下了但几乎没拖动，且光标下有个可吸附的窗口 → 当作「点选整个窗口」。
			// 这是窗口吸附的实际生效点：拖 = 自由框选，点一下 = 整个窗口。
			o.sel = o.pendingSnap
			o.phase = phaseAdjusting
		default:
			// 误触：回到等待框选的状态，不结束会话。
			o.sel = Rect{}
			o.phase = phaseSelecting
		}
	case dragShape:
		o.drag = dragNone
		if o.draft != nil {
			if o.draft.meaningful() {
				// 刻意不自动选中刚画的这一笔：选中会带出一条操作提示，
				// 每画一笔弹一次太吵；要移动它，点它一下即可。
				o.shapes = append(o.shapes, *o.draft)
			} else {
				// 太小的一笔被丢弃 → 刚压的历史也一并弹掉。
				o.dropLastHistory()
			}
			o.draft = nil
		}
	case dragMoveShape:
		o.drag = dragNone
		o.endShapeMove()
	case dragMove,
		dragResizeNW, dragResizeN, dragResizeNE,
		dragResizeE, dragResizeSE, dragResizeS, dragResizeSW, dragResizeW:
		o.drag = dragNone
		// 选框真的动过才裁剪图形；没动过就把它白占的那个撤销位弹掉。
		if o.sel == o.frameOrig {
			o.dropLastHistory()
		} else {
			o.pruneShapes()
		}
	default:
		o.drag = dragNone
	}

	o.pendingSnapOK = false
	o.hoverIdx = -1
	// 松手后光标往往还停在原处，这里补一次悬停计算，
	// 否则「刚刚选中的那个窗口」的高亮要等下一次移动鼠标才出现。
	if o.phase == phaseSelecting {
		o.updateHover(x, y)
	}
	o.applyCursor(x, y)
	o.redraw()
}

// applyDrag 处理选框平移与八向缩放。
func (o *Overlay) applyDrag(x, y int) {
	orig := o.dragOrig
	l, t := orig.X, orig.Y
	r, b := orig.X+orig.W, orig.Y+orig.H

	switch o.drag {
	case dragMove:
		dx := x - o.dragFrom.x
		dy := y - o.dragFrom.y
		l, r = l+dx, r+dx
		t, b = t+dy, b+dy
		// 平移时保持尺寸不变，整体夹回屏幕内
		if l < 0 {
			r -= l
			l = 0
		}
		if r > o.bounds.W {
			l -= r - o.bounds.W
			r = o.bounds.W
		}
		if t < 0 {
			b -= t
			t = 0
		}
		if b > o.bounds.H {
			t -= b - o.bounds.H
			b = o.bounds.H
		}
	case dragResizeNW:
		l, t = x, y
	case dragResizeN:
		t = y
	case dragResizeNE:
		r, t = x, y
	case dragResizeE:
		r = x
	case dragResizeSE:
		r, b = x, y
	case dragResizeS:
		b = y
	case dragResizeSW:
		l, b = x, y
	case dragResizeW:
		l = x
	}

	l = clampInt(l, 0, o.bounds.W-1)
	t = clampInt(t, 0, o.bounds.H-1)
	r = clampInt(r, 1, o.bounds.W)
	b = clampInt(b, 1, o.bounds.H)

	// 缩放时以对边为锚，不允许把框翻过来
	if r-l < minSelectExtent {
		if o.drag == dragResizeNW || o.drag == dragResizeW || o.drag == dragResizeSW {
			l = maxInt(0, r-minSelectExtent)
		} else {
			r = l + minSelectExtent
			if r > o.bounds.W {
				r = o.bounds.W
				l = r - minSelectExtent
			}
		}
	}
	if b-t < minSelectExtent {
		if o.drag == dragResizeNW || o.drag == dragResizeN || o.drag == dragResizeNE {
			t = maxInt(0, b-minSelectExtent)
		} else {
			b = t + minSelectExtent
			if b > o.bounds.H {
				b = o.bounds.H
				t = b - minSelectExtent
			}
		}
	}

	o.sel = Rect{X: l, Y: t, W: r - l, H: b - t}
}

// hitHandle 判断点是否落在某个控制点的命中范围内。
func (o *Overlay) hitHandle(x, y int) (dragMode, bool) {
	if o.sel.Empty() {
		return dragNone, false
	}
	modes := [8]dragMode{
		dragResizeNW, dragResizeN, dragResizeNE,
		dragResizeW, dragResizeE,
		dragResizeSW, dragResizeS, dragResizeSE,
	}
	pts := handlePoints(o.sel)
	for i, p := range pts {
		if absInt(x-p.x) <= handleGrip && absInt(y-p.y) <= handleGrip {
			return modes[i], true
		}
	}
	return dragNone, false
}

func cursorForHandle(m dragMode) uintptr {
	switch m {
	case dragResizeNW, dragResizeSE:
		return idcSizeNWSE
	case dragResizeNE, dragResizeSW:
		return idcSizeNESW
	case dragResizeN, dragResizeS:
		return idcSizeNS
	case dragResizeE, dragResizeW:
		return idcSizeWE
	}
	return idcSizeAll
}

func (o *Overlay) applyCursor(x, y int) {
	id := uintptr(idcCross)

	switch {
	case o.drag == dragMove:
		id = idcSizeAll
	case o.drag != dragNone && o.drag != dragNew && o.drag != dragShape:
		id = cursorForHandle(o.drag)
	case o.drag == dragNew || o.drag == dragShape:
		id = idcCross
	case o.phase == phaseAdjusting:
		lay := o.layoutToolbar(o.sel)
		switch {
		case lay.bar.Contains(x, y), o.panelRect().Contains(x, y):
			id = idcArrow
		default:
			if m, ok := o.hitHandle(x, y); ok {
				id = cursorForHandle(m)
			} else if o.sel.Contains(x, y) {
				switch o.activeTool {
				case toolNone:
					id = idcSizeAll
				case toolText:
					id = idcIBeam
				}
			}
		}
	}

	// 吸附到某个窗口时用手形：手形说明「点一下就把它整个选走」，
	// 而十字只是「在这儿可以开始拖」。snapOK 只在不压选区/工具条时才会置位，
	// 所以这里覆盖上面的判定不会打架。
	if o.snapOK && o.drag == dragNone {
		id = idcHand
	}

	procSetCursor.Call(loadCursor(id))
}

func (o *Overlay) onToolbarClick(i int) {
	it := tbItems[i]
	switch it.kind {
	case tbTool:
		if o.activeTool == it.tool {
			o.activeTool = toolNone
		} else {
			o.activeTool = it.tool
		}
		o.panel = panelNone
	case tbColor, tbWidth, tbFontSize:
		o.panel = togglePanel(o.panel, panelOf(it.kind))
	case tbUndo:
		o.undo()
	case tbRedo:
		o.redo()
	case tbCopy:
		o.finishWith(ActionCopy)
		return
	case tbSave:
		o.finishWith(ActionSave)
		return
	case tbPin:
		o.finishWith(ActionPin)
		return
	case tbClose:
		o.finishWith(ActionCancel)
		return
	}
	o.hoverIdx = -1
	o.redraw()
}

func togglePanel(cur, want panelID) panelID {
	if cur == want {
		return panelNone
	}
	return want
}

// onPanelClick 处理调色板 / 线宽面板里的一次点击。
func (o *Overlay) onPanelClick(pr Rect, x, y int) {
	switch o.panel {
	case panelColor:
		for i := range annotColors {
			if colorSwatchRect(pr, i).Contains(x, y) {
				o.curColor = annotColors[i]
				break
			}
		}
	case panelWidth:
		for i, w := range annotWidths {
			if widthRowRect(pr, i).Contains(x, y) {
				o.curWidth = w
				break
			}
		}
	case panelFontSize:
		for i, fs := range annotFontSizes {
			if fontRowRect(pr, i).Contains(x, y) {
				o.curFontSize = fs
				break
			}
		}
	}
	// 正在输入文字时改配色 / 字号，输入框要立刻跟着变：
	// 否则用户选了红色、输入框里还是白的，「所见即所得」就断了；
	// 字号还会改变窗口尺寸，所以重排也要跟上。
	if o.ti.active {
		o.applyTextInputFont()
		o.applyTextInputColors()
		o.layoutTextInput()
	}
	// 选完不关面板：连续比较几个颜色/线宽是常见操作。
	o.redraw()
}

func (o *Overlay) onKeyDown(vk uintptr) {
	// 文字输入中键盘本该全归输入框（焦点在它上面）。这里兜一手：
	// 万一焦点回到了覆盖层（比如刚点过工具条），Enter / Esc 仍应作用于这次输入，
	// 而不是被下面当成「复制」「取消截图」。
	if o.ti.active {
		switch vk {
		case vkReturn:
			o.commitTextInput()
		case vkEscape:
			o.cancelTextInput()
		}
		return
	}

	ctrl := keyPressed(vkControl)
	shift := keyPressed(vkShift)

	if ctrl {
		switch vk {
		case vkC, vkInsert:
			o.finishWith(ActionCopy)
		case vkS:
			o.finishWith(ActionSave)
		case vkZ:
			if shift {
				o.redo()
			} else {
				o.undo()
			}
			o.redraw()
		case vkY:
			o.redo()
			o.redraw()
		case vkA:
			// 全选整个虚拟桌面
			o.sel = Rect{X: 0, Y: 0, W: o.bounds.W, H: o.bounds.H}
			o.phase = phaseAdjusting
			o.redraw()
		}
		return
	}

	switch vk {
	case vkEscape:
		// Esc 逐层退出：弹出面板 → 激活的工具 → 选中的图形 → 整个会话（Snipaste 同序）
		if o.panel != panelNone {
			o.panel = panelNone
			o.redraw()
			return
		}
		if o.activeTool != toolNone {
			o.activeTool = toolNone
			o.draft = nil
			o.redraw()
			return
		}
		if o.selected >= 0 {
			o.selected = -1
			o.redraw()
			return
		}
		o.cancel()
	case vkReturn:
		o.finishWith(ActionCopy)
	case vkDelete, vkBack:
		// 删掉选中的标注。Backspace 一并接受：中文输入习惯下两个键都会被按到。
		if o.deleteSelected() {
			o.redraw()
		}
	case vkR:
		o.setTool(toolRect)
	case vkE:
		o.setTool(toolEllipse)
	case vkA:
		o.setTool(toolArrow)
	case vkL:
		o.setTool(toolLine)
	case vkM:
		o.setTool(toolMosaic)
	case vkT:
		o.setTool(toolText)
	case vkLeft, vkRight, vkUp, vkDown:
		o.nudge(vk, shift)
	}
}

// nudge 用方向键微调选区位置（Shift 加速 10px）。
func (o *Overlay) nudge(vk uintptr, shift bool) {
	if o.sel.Empty() {
		return
	}
	step := 1
	if shift {
		step = 10
	}

	r := o.sel
	switch vk {
	case vkLeft:
		r.X -= step
	case vkRight:
		r.X += step
	case vkUp:
		r.Y -= step
	case vkDown:
		r.Y += step
	}
	r.X = clampInt(r.X, 0, maxInt(0, o.bounds.W-r.W))
	r.Y = clampInt(r.Y, 0, maxInt(0, o.bounds.H-r.H))
	o.sel = r
	o.redraw()
}

// keyPressed 查询按键的当前按下状态（用于读 Ctrl / Shift 修饰键）。
func keyPressed(vk uintptr) bool {
	s, _, _ := procGetAsyncKeyState.Call(vk)
	return s&0x8000 != 0
}

// ===== 窗口过程 =====

func overlayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	o := overlayInstance
	if o == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}

	switch msg {
	case wmOverlayShow:
		select {
		case cmd := <-o.cmdCh:
			o.handleShow(cmd)
		default:
			// 命令已被其他路径消费，忽略。
		}
		return 0

	case wmOverlayStop:
		procPostQuitMessage.Call(0)
		return 0

	case wmNCHitTest:
		return htClient // 整个窗口都是客户区，保证能收到鼠标消息

	case wmSetCursor:
		o.applyCursor(o.mouseX, o.mouseY)
		return 1

	case wmMouseMove:
		o.onMouseMove(lowWord(lParam), highWord(lParam))
		return 0

	case wmLButtonDown:
		o.onLButtonDown(hwnd, lowWord(lParam), highWord(lParam))
		return 0

	case wmLButtonUp:
		o.onLButtonUp(lowWord(lParam), highWord(lParam))
		return 0

	case wmLButtonDblClk:
		o.onLButtonDblClk(lowWord(lParam), highWord(lParam))
		return 0

	case wmRButtonDown:
		// 右键取消。Esc 依赖 SetForegroundWindow 成功才收得到键盘消息，
		// 被前台锁打断时它是唯一出口——没有它用户会被全屏覆盖层困住。
		o.cancel()
		return 0

	case wmKeyDown:
		o.onKeyDown(wParam)
		return 0

	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}
