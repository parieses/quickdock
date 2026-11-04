//go:build windows

package screenshot

import (
	"fmt"
	"syscall"
	"unsafe"

	"quickdock/internal/logger"
)

// 文字标注的输入窗。
//
// 必须另开顶层窗口，不能在覆盖层里塞 EDIT 子控件：覆盖层是
// WS_EX_LAYERED + UpdateLayeredWindow，子窗口不会被合成，显示什么完全由位图决定。
// 中文标注必须走 IME，自己收 WM_CHAR 拼字符串对中文无效，标准 EDIT 由系统接 IME。
//
// 输入窗是覆盖层的 owned window，恒在其上、随其销毁；它同时是顶层窗口，
// 能正常接收焦点与键盘输入，不影响覆盖层的 SetForegroundWindow。
//
// 这个窗口**不再负责显示文字**：正在输入的内容由覆盖层用 drawText 直接画在
// 锚点上（见 drawTextPreview），用的就是最终落字的那条路径，所以回车提交后
// 一个像素都不会移动。早先把输入框摆在锚点右下 8px，输入完文字才「跳回去」，
// 观感很割裂；现在输入窗退化成纯粹的键盘 / IME 接收器。
//
// 因此它被设成几乎全透明（LWA_ALPHA=1）+ WS_EX_TRANSPARENT：
//   - 视觉上不遮挡底图，也不和覆盖层上的预览文字重影；
//   - 鼠标命中测试跳过它，点击一律落到覆盖层（输入中点任意处 = 提交）；
//   - 键盘焦点、IME 候选框定位完全不受影响，中文输入照常工作。
// 不能换成 UpdateLayeredWindow：后者要求整窗自绘，EDIT 与 IME 都会失效。
//
// 配色逻辑（applyTextInputColors 那套深浅底 + WM_CTLCOLOREDIT）刻意保留：
// 万一某个输入法在近乎透明的窗口上候选框定位异常，把 textInputAlpha 调回
// 可见值就能立刻退回「带输入框」的旧外观，不必重写一遍。
//
// 两处别改回去：
//   - 底色跟着标注色的明暗自适应。原先恒为深灰，选近黑色标注时输入框里
//     什么都看不见，容易被当成 bug。
//   - EDIT 铺满客户区，左右留白走 EM_SETMARGINS。这样窗口类背景刷永远不可见，
//     底色只剩 WM_CTLCOLOREDIT 一个来源，不必动态换类刷。

const (
	textEditPad = 6 // 输入框左右内边距

	// 输入窗整体不透明度（0-255）。1 而不是 0：alpha=0 在 Windows 上有
	// 「整窗不可见」的特殊语义，可能连带影响 IME 组合窗口；1 足够隐形，
	// 又保持窗口在系统中是可见的。
	textInputAlpha = 1
	textInputMinW  = 220 // 输入窗宽度下限
	textInputMaxW  = 460 // 输入窗宽度上限

	// textInputLumaSplit 是「换浅底」的亮度阈值。
	// 深灰底 #262626 的亮度约 38，亮度高于本阈值的颜色在它上面都读得清；
	// 低于本阈值的（近黑 33、紫 83）必须换浅底，否则字与底几乎同色。
	textInputLumaSplit = 96
)

// 输入窗配色（BGR 顺序）。
//
// 深浅两套底各配一把预建刷子：刷子句柄必须长期有效，不能每帧
// CreateSolidBrush（GDI 对象会泄漏，且每次重绘都要建一次太浪费）。
var (
	colTextBgDark  = col{0x26, 0x26, 0x26} // #262626 深灰底，配浅色字
	colTextBgLight = col{0xED, 0xED, 0xED} // #EDEDED 浅灰底，配深色字
)

type textInput struct {
	hwnd  uintptr
	edit  uintptr
	font  uintptr
	fontH int // font 对应的像素高度；为 0 表示还没建过字体
	class *uint16

	// 两把预建背景刷 + 当前生效的那把与其底色（WM_CTLCOLOREDIT 直接拿它们回话）。
	brushDark, brushLight uintptr
	brush                 uintptr
	bg                    col

	active bool
	anchor pt // 客户区坐标下的文字左上角落点

	// 每帧刷新一次的预览状态，由 redraw 开头的 refreshTextPreview 算好，
	// footprint 和 drawTextPreview 共用：避免两处各发一次 GetWindowText、
	// 各量一次文本宽度，也保证脏矩形与实际落笔范围严格一致。
	preview     string
	caretX      int  // 插入符相对锚点的 x（按 EM_GETSEL 的前缀宽度算）
	selStartX   int  // 选区起点相对锚点的 x（无选区时与 selEndX 相等）
	selEndX     int  // 选区终点相对锚点的 x
	previewW    int  // 预览文字宽度
	previewH    int  // 预览文字高度
	previewRect Rect // 预览 + 插入符 + 下划线的外接矩形

	// editIndex 是本次输入对应的既有文字在 Overlay.shapes 中的下标；
	// -1 表示新建（提交时追加）。据此决定提交是替换还是追加、清空是删除还是忽略。
	editIndex int
}

var (
	textInputProcCallback = syscall.NewCallback(textInputWndProc)
	editSubclassCallback  = syscall.NewCallback(editSubclassProc)

	// oldEditProc 保存 EDIT 控件原本的窗口过程，子类化后要转发回去。
	// 全局单个：同一时刻只会有一个文本输入窗。
	oldEditProc uintptr

	// emptyUTF16 用于清空编辑框内容；SetWindowTextW 需要一个合法指针。
	emptyUTF16 = [1]uint16{0}
)

// luma 返回颜色的感知亮度（0-255，BT.601 权重）。
// col 是 BGR 顺序，所以 [2] 才是 R。
func luma(c col) int {
	return (299*int(c[2]) + 587*int(c[1]) + 114*int(c[0])) / 1000
}

// textInputSize 按当前字号给出输入窗的尺寸。
//
// 宽度跟着字号走：字号越大，单行能放下的字越多，否则写大标题时窗口会早早
// 横向滚起来。高度 = 字高 + 上下内边距。
func textInputSize(fontSize int) (int, int) {
	h := fontSize + textEditPad*2 + 4
	w := clampInt(fontSize*13, textInputMinW, textInputMaxW)
	return w, h
}

// ensureTextInput 懒创建输入窗（首次输入文字时）并同步字体。
func (o *Overlay) ensureTextInput() error {
	if o.ti.hwnd == 0 {
		if err := o.createTextInput(); err != nil {
			return err
		}
	}
	o.applyTextInputFont()
	return nil
}

func (o *Overlay) createTextInput() error {
	// 窗口类只注册一次：建窗失败后重试时若再注册，会拿到
	// ERROR_CLASS_ALREADY_EXISTS 而被误判成致命错误。
	if o.ti.class == nil {
		cls, _ := syscall.UTF16PtrFromString("QuickDock_Screenshot_TextInput_v1")

		// 两把背景刷一次建好，之后随输入窗一起销毁。
		// 它们既是 EDIT 的背景刷（见 wmCtlColorEdit），也充当窗口类背景刷——
		// EDIT 铺满客户区后类刷其实不可见，留一个只是不想让窗口类没有背景刷。
		dark, _, _ := procCreateSolidBrush.Call(colorRef(colTextBgDark))
		light, _, _ := procCreateSolidBrush.Call(colorRef(colTextBgLight))
		if dark == 0 || light == 0 {
			if dark != 0 {
				procDeleteObject.Call(dark)
			}
			if light != 0 {
				procDeleteObject.Call(light)
			}
			return fmt.Errorf("screenshot: 创建文字输入窗背景刷失败")
		}
		o.ti.brushDark, o.ti.brushLight = dark, light
		o.ti.brush = dark

		wc := wndClassW{
			LpfnWndProc:   textInputProcCallback,
			HInstance:     o.hInstance,
			HCursor:       loadCursor(idcIBeam),
			HbrBackground: dark,
			LpszClassName: cls,
		}
		if ret, _, _ := procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
			return fmt.Errorf("screenshot: 注册文字输入窗类失败")
		}
		o.ti.class = cls
	}

	// 无边框。多一圈系统边框只是往底图上糊脏像素。
	// WS_EX_TRANSPARENT 让鼠标穿透：输入中点任意处都落到覆盖层，走「提交输入」，
	// 不会被这个看不见的窗口吞掉点击。键盘焦点不受影响。
	// 初始尺寸随便给，真正的尺寸在每次 openTextInput 时按字号定。
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow|wsExLayered|wsExTransparent,
		uintptr(unsafe.Pointer(o.ti.class)),
		0,
		wsPopup,
		0, 0, 100, 100,
		o.hwnd, 0, o.hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("screenshot: 创建文字输入窗失败")
	}
	o.ti.hwnd = hwnd

	// 整窗近乎全透明：它只接键盘与 IME，可见文字全在覆盖层上。
	procSetLayeredWindowAttributes.Call(hwnd, 0, textInputAlpha, lwaAlpha)

	// 单行 EDIT，不换行、超长横向滚动。铺满整个客户区（尺寸在
	// openTextInput 里跟一次），左右留白交给 EM_SETMARGINS。
	editCls, _ := syscall.UTF16PtrFromString("EDIT")
	edit, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(editCls)),
		0,
		wsChild|wsVisible|esLeft|esAutoHScroll,
		0, 0, 100, 20,
		hwnd, 0, o.hInstance, 0,
	)
	if edit == 0 {
		return fmt.Errorf("screenshot: 创建文字输入控件失败")
	}
	o.ti.edit = edit
	procSendMessageW.Call(edit, emSetMargins,
		ecLeftMargin|ecRightMargin, makeLong(textEditPad, textEditPad))

	// 子类化：单行 EDIT 自己不管 Enter / Esc（那是对话框的活），
	// 必须自己拦下来做「提交 / 取消」。
	// 索引要按 int 的 32 位补码传，这里用变量转换避免常量负数溢出报错。
	idx := gwlpWndProc
	old, _, _ := procSetWindowLongPtrW.Call(edit, uintptr(idx), editSubclassCallback)
	oldEditProc = old

	return nil
}

// applyTextInputFont 让 EDIT 的字体跟上当前字号。
//
// 字号可以在会话中改（工具条的「字号」面板），重新编辑既有文字时还会同步成
// 该文字的字号，所以每次弹出前都要对一次；字号没变就直接返回，避免每帧重建字体。
func (o *Overlay) applyTextInputFont() {
	fs := clampInt(o.curFontSize, 8, 96)
	if o.ti.font != 0 && o.ti.fontH == fs {
		return
	}
	if o.ti.font != 0 {
		procDeleteObject.Call(o.ti.font)
		o.ti.font = 0
	}

	face, _ := syscall.UTF16PtrFromString("Microsoft YaHei UI")
	hFont, _, _ := procCreateFontW.Call(
		iptr(-fs), 0, 0, 0, 400, 0, 0, 0,
		1 /*DEFAULT_CHARSET*/, 0, 0, 4 /*ANTIALIASED_QUALITY*/, 0,
		uintptr(unsafe.Pointer(face)),
	)
	o.ti.font = hFont
	o.ti.fontH = fs
	if o.ti.edit != 0 {
		procSendMessageW.Call(o.ti.edit, wmSetFont, hFont, 1 /*TRUE*/)
	}
}

// applyTextInputColors 按当前标注色挑底色，并让 EDIT 立刻按新配色重画。
//
// 输入窗平时是隐形的，这套配色只在把 textInputAlpha 调回可见值（兼容某些
// 输入法的候选框定位）时才起作用：文字色恒等于标注色，变的只是底色，
// 暗色字给浅底、亮色字给深底（判据见 textInputLumaSplit）。
func (o *Overlay) applyTextInputColors() {
	bg := colTextBgDark
	brush := o.ti.brushDark
	if luma(o.curColor) < textInputLumaSplit {
		bg = colTextBgLight
		if o.ti.brushLight != 0 {
			brush = o.ti.brushLight
		}
	}
	if brush == 0 {
		return
	}
	o.ti.brush = brush
	o.ti.bg = bg
	if o.ti.edit != 0 {
		procInvalidateRect.Call(o.ti.edit, 0, 1 /*TRUE=擦背景*/)
	}
}

// showTextInput 新建一条文字标注：在客户区坐标 (x, y) 落锚点并开启输入态。
func (o *Overlay) showTextInput(x, y int) {
	o.openTextInput(pt{x, y}, "", -1)
}

// editTextShape 重新编辑第 i 条文字标注。
//
// 顺手把工具条的「颜色 / 线宽 / 字号」同步成该文字的值：这样面板高亮是对的，
// 而且用户「编辑 + 改色」可以一次做完——改完面板再回车即生效，
// 不必先撤销重画。
func (o *Overlay) editTextShape(i int) {
	if i < 0 || i >= len(o.shapes) {
		return
	}
	s := &o.shapes[i]
	o.curColor = s.color
	o.curWidth = s.width
	if s.fontSize > 0 {
		o.curFontSize = s.fontSize
	}
	o.openTextInput(pt{s.x0, s.y0}, s.text, i)
}

// layoutTextInput 按当前字号把隐形输入窗摆到锚点附近。
//
// 窗口已不显示文字，位置只影响 IME 候选框的落点：贴着锚点摆，候选框就出现在
// 正在打的字下面。所以不再需要「避免盖住落字位置」的 +8 偏移，也不做越界翻转
// （一翻转候选框就飘走了）；只在贴着屏幕边缘时把窗口压回屏内。
//
// 字号在会话中可改（工具条「字号」面板），改完必须重排一遍：
// 窗口和 EDIT 的尺寸是按字号算出来的，不重排的话大字号会被裁掉。
func (o *Overlay) layoutTextInput() {
	if o.ti.hwnd == 0 {
		return
	}
	w, h := textInputSize(clampInt(o.curFontSize, 8, 96))

	px := o.bounds.X + o.ti.anchor.x
	py := o.bounds.Y + o.ti.anchor.y
	px = clampInt(px, o.bounds.X, maxInt(o.bounds.X, o.bounds.X+o.bounds.W-w))
	py = clampInt(py, o.bounds.Y, maxInt(o.bounds.Y, o.bounds.Y+o.bounds.H-h))

	// 尺寸带字号信息，所以每次都要连尺寸一起设（不能再用 swpNoSize）。
	procSetWindowPos.Call(o.ti.hwnd, hwndTopmost,
		iptr(px), iptr(py),
		uintptr(w), uintptr(h),
		swpNoZOrder)

	// EDIT 铺满客户区（左右留白已由 EM_SETMARGINS 处理），
	// 大字号时不会因为子控件没跟上而被裁掉。
	procSetWindowPos.Call(o.ti.edit, 0, 0, 0,
		uintptr(w), uintptr(h),
		swpNoMove|swpNoZOrder)
}

// openTextInput 开启文字输入态：隐形输入窗负责接键盘 / IME，
// 可见文字由覆盖层画在 anchor 上（drawTextPreview）。
//
// editIndex >= 0 表示这是对既有文字的再编辑：提交时替换该条（清空则删除），
// 而不是追加一条新的。
func (o *Overlay) openTextInput(anchor pt, initial string, editIndex int) {
	if err := o.ensureTextInput(); err != nil {
		// 原实现只打 err 正文，看不出是哪一步失败（建窗/子类化/建字体）；
		// 补上动作名，配合 ensureTextInput 内的错误前缀即可定位。
		logger.W("[screenshot] 文字输入窗打开失败（本次无法输入文字）: %v", err)
		return
	}
	o.ti.editIndex = editIndex
	o.ti.anchor = anchor
	o.applyTextInputColors()

	if initial == "" {
		procSetWindowTextW.Call(o.ti.edit, uintptr(unsafe.Pointer(&emptyUTF16[0])))
	} else {
		// 原文写回后全选：像重命名文件一样，直接打字即整体覆写，
		// 想局部改也可以先用方向键把选区取消掉。
		u16, _ := syscall.UTF16FromString(initial)
		procSetWindowTextW.Call(o.ti.edit, uintptr(unsafe.Pointer(&u16[0])))
		procSendMessageW.Call(o.ti.edit, emSetSel, 0, iptr(-1))
	}

	o.layoutTextInput()
	procShowWindow.Call(o.ti.hwnd, swShow)

	// 焦点必须交给 EDIT，键盘与 IME 才会走它。
	procSetForegroundWindow.Call(o.ti.hwnd)
	procSetFocus.Call(o.ti.edit)

	o.ti.active = true
	o.redraw()
}

// refocusTextInput 把焦点还给输入框。
//
// 用在「打字打到一半去点了工具条」之后：那一下点击把覆盖层变成了活动窗口，
// 焦点不再在 EDIT 上——不还回去的话键盘和 IME 全都收不到，看起来就像输入框卡死了。
// 这里不会被前台上锁拦住：点击刚把本线程激活，SetForegroundWindow 允许自线程调用；
// 而且输入窗是覆盖层的 owned window，同属一个线程。
func (o *Overlay) refocusTextInput() {
	if !o.ti.active || o.ti.hwnd == 0 {
		return
	}
	procSetForegroundWindow.Call(o.ti.hwnd)
	procSetFocus.Call(o.ti.edit)
	// 工具条刚改过颜色 / 字号：预览得跟着变，否则所见和提交结果会不一致。
	o.requestFrame()
}

// commitTextInput 读回输入内容：新建则追加一条，再编辑则替换原条（清空即删除）。
func (o *Overlay) commitTextInput() {
	if !o.ti.active {
		return
	}
	o.ti.active = false
	idx := o.ti.editIndex
	o.ti.editIndex = -1

	procShowWindow.Call(o.ti.hwnd, swHide)
	text := o.readTextInput()
	procSetWindowTextW.Call(o.ti.edit, uintptr(unsafe.Pointer(&emptyUTF16[0])))

	switch {
	case idx >= 0 && idx < len(o.shapes):
		o.pushHistory()
		if text == "" {
			// 清空即删除：想擦掉一条文字，重新编辑按 Delete 清空比找撤销更直觉。
			o.shapes = append(o.shapes[:idx], o.shapes[idx+1:]...)
			o.selected = -1
		} else {
			s := &o.shapes[idx]
			s.text = text
			s.color = o.curColor
			s.width = o.curWidth
			s.fontSize = clampInt(o.curFontSize, 8, 96)
		}
	case text != "":
		o.pushHistory()
		o.shapes = append(o.shapes, shape{
			kind:     shapeText,
			x0:       o.ti.anchor.x,
			y0:       o.ti.anchor.y,
			text:     text,
			width:    o.curWidth,
			color:    o.curColor,
			fontSize: clampInt(o.curFontSize, 8, 96),
		})
	}

	// 焦点还给覆盖层，否则 Esc / Ctrl+C 这些快捷键全失效。
	// 光 SetFocus 不够：隐藏掉当前活动窗口后，Windows 会把激活权交给别的顶层窗口，
	// 覆盖层这时候可能已经不是活动窗口，键盘消息就收不到了。
	procSetForegroundWindow.Call(o.hwnd)
	procSetFocus.Call(o.hwnd)
	o.redraw()
}

// cancelTextInput 丢弃本次输入。
func (o *Overlay) cancelTextInput() {
	if !o.ti.active {
		return
	}
	o.ti.active = false
	o.ti.editIndex = -1
	procShowWindow.Call(o.ti.hwnd, swHide)
	procSetWindowTextW.Call(o.ti.edit, uintptr(unsafe.Pointer(&emptyUTF16[0])))
	procSetForegroundWindow.Call(o.hwnd)
	procSetFocus.Call(o.hwnd)
	o.redraw()
}

// hideTextInput 无条件收起输入窗（会话结束 / 重新显示覆盖层时用）。
func (o *Overlay) hideTextInput() {
	if o.ti.hwnd == 0 {
		return
	}
	o.ti.active = false
	o.ti.editIndex = -1
	procShowWindow.Call(o.ti.hwnd, swHide)
}

// editingIndex 返回当前正在编辑的既有文字下标；-1 表示没有（或本次是新建）。
// 重绘时据此把原文暂时隐藏——否则原文与插入符叠在一起，看起来像重影。
func (o *Overlay) editingIndex() int {
	if !o.ti.active {
		return -1
	}
	return o.ti.editIndex
}

func (o *Overlay) destroyTextInput() {
	// 注意不要用 hwnd == 0 提前返回：建窗失败时也可能已经建了刷子。
	if o.ti.hwnd != 0 {
		procDestroyWindow.Call(o.ti.hwnd)
	}
	if o.ti.font != 0 {
		procDeleteObject.Call(o.ti.font)
	}
	if o.ti.brushDark != 0 {
		procDeleteObject.Call(o.ti.brushDark)
	}
	if o.ti.brushLight != 0 {
		procDeleteObject.Call(o.ti.brushLight)
	}
	if o.ti.class != nil {
		procUnregisterClassW.Call(uintptr(unsafe.Pointer(o.ti.class)), o.hInstance)
	}
	// 类刷（brushDark）已随窗口类注销，这里只重置状态。
	o.ti = textInput{editIndex: -1}
}

// readTextInput 取编辑框当前内容。
func (o *Overlay) readTextInput() string {
	n, _, _ := procGetWindowTextLengthW.Call(o.ti.edit)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	got, _, _ := procGetWindowTextW.Call(
		o.ti.edit,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if got == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:got])
}

// textInputSel 读 EDIT 当前选区（UTF-16 code unit 索引）；无选区时起止相等，
// 即插入符位置。
//
// 方向键 / Home / End 只挪光标、不改文字，EN_CHANGE 不会来；预览仍要跟着把
// 插入符挪到对应位置，否则在句子中间插字时看起来像「光标钉在末尾」。
// 双击编辑既有文字时原文是全选的，起止不相等，预览要给出选区提示。
func (o *Overlay) textInputSel() (int, int) {
	var start, end uint32
	procSendMessageW.Call(o.ti.edit, emGetSel,
		uintptr(unsafe.Pointer(&start)), uintptr(unsafe.Pointer(&end)))
	return int(start), int(end)
}

// textWidthN 用与 drawText 同一套字体度量算前 n 个 UTF-16 单元的宽度。
// n=0 直接给 0，避免对空切片取地址。
func (o *Overlay) textWidthN(chars []uint16, n, fs int) int {
	if n <= 0 || len(chars) == 0 {
		return 0
	}
	if n > len(chars) {
		n = len(chars)
	}
	w, _ := o.measureText(chars, n, fs)
	return w
}

// refreshTextPreview 在每帧绘制前算一次预览状态。
//
// 必须在 footprint 之前调用：脏矩形要用这里的 previewRect，才能把上一帧更长的
// 文字完整擦掉（redraw 取「上一帧落笔范围 ∪ 本帧落笔范围」做还原）。
func (o *Overlay) refreshTextPreview() {
	o.ti.preview = ""
	o.ti.caretX, o.ti.previewW, o.ti.previewH = 0, 0, 0
	o.ti.selStartX, o.ti.selEndX = 0, 0
	o.ti.previewRect = Rect{}
	if !o.ti.active || o.ti.edit == 0 {
		return
	}

	fs := clampInt(o.curFontSize, 8, 96)
	txt := o.readTextInput()
	o.ti.preview = txt

	u16, err := syscall.UTF16FromString(txt)
	if err == nil && len(u16) > 1 {
		chars := u16[:len(u16)-1] // 去掉结尾的 NUL
		tw, th := o.measureText(chars, len(chars), fs)
		s, e := o.textInputSel()
		s = clampInt(s, 0, len(chars))
		e = clampInt(e, 0, len(chars))
		if s > e {
			s, e = e, s
		}
		o.ti.caretX = o.textWidthN(chars, s, fs)
		if e > s {
			o.ti.selStartX = o.ti.caretX
			o.ti.selEndX = o.textWidthN(chars, e, fs)
		}
		o.ti.previewW, o.ti.previewH = tw, maxInt(th, fs)
	}
	if o.ti.previewH < fs {
		o.ti.previewH = fs // 空文本也要有插入符与下划线
	}

	// +8 留给插入符，+6 留给下划线和抗锯齿边缘。
	a := o.ti.anchor
	o.ti.previewRect = Rect{
		X: a.x - 2,
		Y: a.y - 2,
		W: maxInt(o.ti.previewW, 8) + 8,
		H: o.ti.previewH + 6,
	}
}

// ===== 窗口过程 =====

func textInputWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	o := overlayInstance
	if o != nil {
		switch msg {
		case wmClose:
			// Alt+F4 / 系统关闭：等同于取消输入。
			o.cancelTextInput()
			return 0
		case wmCtlColorEdit:
			// EDIT 每次重绘都会问父窗口「用什么刷子擦背景、用什么颜色写字」。
			// DefWindowProc 默认返回系统白刷，会把输入框变成一块白底。
			// wParam 是绘制用的 HDC。
			if o.ti.brush != 0 {
				procSetBkColor.Call(wParam, colorRef(o.ti.bg))
				procSetTextColor.Call(wParam, colorRef(o.curColor))
				return o.ti.brush
			}
		case wmCommand:
			// EDIT 内容变化（打字 / 删除 / 粘贴 / IME 上屏）都走 EN_CHANGE；
			// 光标类通知不出帧，由 editSubclassProc 的按键分支负责。
			// 只 PostMessage：此刻还在 EDIT 的通知链里，同步 redraw 会反过来
			// 向 EDIT 发 GetWindowText，属于重入。连续输入会被帧预算自动合帧。
			if highWord(wParam) == enChange {
				procPostMessageW.Call(o.hwnd, wmTextPreview, 0, 0)
			}
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

func editSubclassProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	o := overlayInstance
	if o != nil {
		if msg == wmKeyDown {
			switch wParam {
			case vkReturn:
				o.commitTextInput()
				return 0
			case vkEscape:
				o.cancelTextInput()
				return 0
			}
		}
		// 方向键 / Home / End 只挪光标、不改文字，EN_CHANGE 不会来，
		// 但预览里的插入符必须跟着动；打字时的按键也顺带催一帧。
		if msg == wmKeyDown || msg == wmKeyUp {
			procPostMessageW.Call(o.hwnd, wmTextPreview, 0, 0)
		}
	}
	r, _, _ := procCallWindowProcW.Call(oldEditProc, hwnd, msg, wParam, lParam)
	return r
}
