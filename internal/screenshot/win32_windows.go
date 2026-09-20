//go:build windows

package screenshot

import "syscall"

// 本文件集中声明抓屏与覆盖窗口所需的全部 Win32 入口、常量与结构体，
// 供同包的 capture_windows.go / overlay_windows.go 共用，避免 proc 重复定义。
//
// 与 internal/platform/clipboard_listener_windows.go 保持同一范式：直接用
// syscall.NewLazyDLL 声明，不引 wails 的 pkg/w32——后者未覆盖
// UpdateLayeredWindow / CreateDIBSection / BitBlt 等绘制入口。
//
// 覆盖窗口不用 Wails 的 WebviewWindow：WebView2 是独立子 HWND，
// 窗口显示到内容 paint 之间存在空窗期，全屏覆盖时会表现为明显的闪屏。
// 详见 overlay_windows.go 顶部的说明。

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	// dwmapi 只用于 DwmGetWindowAttribute 判「窗口是否被 DWM 隐去」，
	// 没有它就会把最小化的 UWP 窗口、非当前虚拟桌面里的窗口也当成可吸附目标。
	dwmapi = syscall.NewLazyDLL("dwmapi.dll")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	procRegisterClassW      = user32.NewProc("RegisterClassW")
	procUnregisterClassW    = user32.NewProc("UnregisterClassW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procSetFocus            = user32.NewProc("SetFocus")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procSetCapture          = user32.NewProc("SetCapture")
	procReleaseCapture      = user32.NewProc("ReleaseCapture")
	procSetCursor           = user32.NewProc("SetCursor")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	procDrawTextW           = user32.NewProc("DrawTextW")

	// 工具条 / 弹出面板避让任务栏（toolbar_windows.go 的 clientWorkArea）用。
	// 任务栏是工作区之外的应用栏，只有取到 MONITORINFO.rcWork 才知道它占了哪条边。
	procMonitorFromPoint = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW  = user32.NewProc("GetMonitorInfoW")

	// SetLayeredWindowAttributes 让文字输入窗整体半透明（见 textinput_windows.go）。
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")

	// 文字标注输入窗（textinput_windows.go）用
	procSetWindowTextW       = user32.NewProc("SetWindowTextW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procSetWindowLongPtrW    = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW      = user32.NewProc("CallWindowProcW")
	procSendMessageW         = user32.NewProc("SendMessageW")

	// 贴图钉屏（pin_windows.go）用
	procGetCursorPos    = user32.NewProc("GetCursorPos")
	procGetWindowRect   = user32.NewProc("GetWindowRect")
	procInvalidateRect  = user32.NewProc("InvalidateRect")
	procBeginPaint      = user32.NewProc("BeginPaint")
	procEndPaint        = user32.NewProc("EndPaint")
	procCreatePopupMenu = user32.NewProc("CreatePopupMenu")
	procAppendMenuW     = user32.NewProc("AppendMenuW")
	procTrackPopupMenu  = user32.NewProc("TrackPopupMenu")
	procDestroyMenu     = user32.NewProc("DestroyMenu")

	// 窗口吸附（snap_windows.go）用。
	// 注意这里**不用** WindowFromPoint：覆盖层铺满整个虚拟桌面且是 topmost，
	// WindowFromPoint 永远只会返回覆盖层自己。只能自己按 z 序从顶往下试。
	procGetTopWindow           = user32.NewProc("GetTopWindow")
	procGetWindow              = user32.NewProc("GetWindow")
	procIsWindowVisible        = user32.NewProc("IsWindowVisible")
	procGetWindowLongPtrW      = user32.NewProc("GetWindowLongPtrW")
	procGetClassNameW          = user32.NewProc("GetClassNameW")
	procScreenToClient         = user32.NewProc("ScreenToClient")
	procChildWindowFromPointEx = user32.NewProc("ChildWindowFromPointEx")

	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")

	procCreateCompatibleDC    = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC              = gdi32.NewProc("DeleteDC")
	procCreateDIBSection      = gdi32.NewProc("CreateDIBSection")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procBitBlt                = gdi32.NewProc("BitBlt")
	procStretchBlt            = gdi32.NewProc("StretchBlt")
	procSetStretchBltMode     = gdi32.NewProc("SetStretchBltMode")
	procSetBrushOrgEx         = gdi32.NewProc("SetBrushOrgEx")
	procSetBkMode             = gdi32.NewProc("SetBkMode")
	procSetBkColor            = gdi32.NewProc("SetBkColor")
	procSetTextColor          = gdi32.NewProc("SetTextColor")
	procCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	procCreateFontW           = gdi32.NewProc("CreateFontW")
	procTextOutW              = gdi32.NewProc("TextOutW")
	procGetTextExtentPoint32W = gdi32.NewProc("GetTextExtentPoint32W")
)

// GetSystemMetrics 索引
const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

// BitBlt 光栅操作码
const (
	srcCopy    = 0x00CC0020
	captureBlt = 0x40000000
)

// CreateDIBSection / BITMAPINFOHEADER
const (
	dibRGBColors = 0
	biRGB        = 0
)

// 窗口样式
const (
	wsPopup         = 0x80000000
	wsChild         = 0x40000000
	wsVisible       = 0x10000000
	wsBorder        = 0x00800000
	esLeft          = 0x0000
	esAutoHScroll   = 0x0080
	wsExLayered     = 0x00080000
	wsExTopmost     = 0x00000008
	wsExTransparent = 0x00000020 // 点击穿透：命中测试会跳过它
	wsExToolWindow  = 0x00000080
)

// COLOR_WINDOW 系统颜色索引；窗口类 HbrBackground 传 index+1 即系统画刷。
const colorWindow = 5

// SetWindowLongPtrW / GetWindowLongPtrW 索引
const (
	gwlpWndProc = -4
	gwlpExStyle = -20
)

// GetWindow
const gwHwndNext = 2

// ChildWindowFromPointEx 跳过标志
const (
	cwpSkipInvisible   = 0x0001
	cwpSkipDisabled    = 0x0002
	cwpSkipTransparent = 0x0004
)

// DwmGetWindowAttribute
const dwmwaCloaked = 14

// ShowWindow
const (
	swHide           = 0
	swShowNoActivate = 4
	swShow           = 5
)

// SetWindowPos
const (
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

// 窗口类样式
const csDblClks = 0x0008

// SetStretchBltMode
const halftone = 4

// TrackPopupMenu
const (
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
)

// AppendMenuW
const mfString = 0x0000

// hwndTopmost 等价于 Win32 的 (HWND)-1。
var hwndTopmost = ^uintptr(0)

// MonitorFromPoint
const monitorDefaultToNearest = 0x00000002

// UpdateLayeredWindow
const (
	ulwAlpha   = 0x00000002
	acSrcOver  = 0
	acSrcAlpha = 1
)

// SetLayeredWindowAttributes
const lwaAlpha = 0x00000002

// 系统光标资源
const (
	idcArrow    = 32512
	idcIBeam    = 32513
	idcCross    = 32515
	idcSizeNWSE = 32642
	idcSizeNESW = 32643
	idcSizeWE   = 32644
	idcSizeNS   = 32645
	idcSizeAll  = 32646
	// idcHand 用于「吸附到某个窗口」的悬停态：手形说明「点一下就是选它」。
	idcHand = 32649
)

// 虚拟键码
const (
	vkShift   = 0x10
	vkControl = 0x11
	vkBack    = 0x08
	vkReturn  = 0x0D
	vkEscape  = 0x1B
	vkSpace   = 0x20
	vkInsert  = 0x2D
	vkDelete  = 0x2E
	vkLeft    = 0x25
	vkUp      = 0x26
	vkRight   = 0x27
	vkDown    = 0x28

	vkA = 0x41
	vkC = 0x43
	vkE = 0x45
	vkL = 0x4C
	vkM = 0x4D
	vkR = 0x52
	vkS = 0x53
	vkT = 0x54
	vkY = 0x59
	vkZ = 0x5A
)

// 窗口消息
const (
	wmDestroy       = 0x0002
	wmPaint         = 0x000F
	wmClose         = 0x0010
	wmEraseBkgnd    = 0x0014
	wmSetCursor     = 0x0020
	wmMouseMove     = 0x0200
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonDown   = 0x0204
	wmRButtonUp     = 0x0205
	wmMouseWheel    = 0x020A
	wmKeyDown       = 0x0100
	wmKeyUp         = 0x0101
	wmSetFocus      = 0x0007
	wmKillFocus     = 0x0008
	wmSetFont       = 0x0030
	wmCommand       = 0x0111
	wmCtlColorEdit  = 0x0133
	wmNCHitTest     = 0x0084
	wmApp           = 0x8000
)

// 覆盖层的跨线程命令，落在 WM_APP 段。
const (
	wmOverlayShow = wmApp + 1
	wmOverlayStop = wmApp + 2
)

// WM_NCHITTEST 返回值
const htClient = 1

// EDIT 控件的 EM_SETMARGINS。
//
// 用来替代「窗口内边距」：文字输入窗让 EDIT 铺满整个客户区（这样窗口类背景刷
// 永远不可见，底色只有 WM_CTLCOLOREDIT 一个来源），左右留白改由 EDIT 自己的
// 内边距实现。
const (
	emSetMargins  = 0x00D3
	emSetSel      = 0x00B1
	ecLeftMargin  = 0x0001
	ecRightMargin = 0x0002
)

// DrawTextW 格式
const (
	dtCenter      = 0x00000001
	dtVCenter     = 0x00000004
	dtSingleLine  = 0x00000020
	dtNoPrefix    = 0x00000800
	dtTransparent = 1 // SetBkMode(TRANSPARENT)
)

// wndClassW 对应 Win32 WNDCLASSW（RegisterClassW 用；注意不是 WNDCLASSEXW，
// 后者多一个 cbSize 与 hIconSm，字段错了会注册出畸形窗口类）。
type wndClassW struct {
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
}

// msgStruct 对应 Win32 MSG。Go 的 struct 对齐规则与 C 一致，
// message/time 之后会自动补 4 字节 padding 以对齐后续的 uintptr。
type msgStruct struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// bitmapInfoHeader 对应 Win32 BITMAPINFOHEADER（40 字节）。
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// bitmapInfo 对应 Win32 BITMAPINFO。
type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// blendFunction 对应 Win32 BLENDFUNCTION。
type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

// pointStruct 对应 Win32 POINT / SIZE（两者都是两个 LONG）。
type pointStruct struct{ X, Y int32 }

// rectStruct 对应 Win32 RECT。
type rectStruct struct{ Left, Top, Right, Bottom int32 }

// monitorInfoW 对应 Win32 MONITORINFO。
// CbSize 必须由调用方填 sizeof(MONITORINFO)，否则 GetMonitorInfoW 直接失败。
// 四个字段都是 4 字节对齐，无隐式填充，尺寸正好 40 字节。
type monitorInfoW struct {
	CbSize    uint32
	RcMonitor rectStruct
	RcWork    rectStruct
	DwFlags   uint32
}

// sizeStruct 对应 Win32 SIZE（GetTextExtentPoint32W 的输出）。
type sizeStruct struct{ CX, CY int32 }

// paintStruct 对应 Win32 PAINTSTRUCT。字段顺序与对齐必须严格一致，
// 否则 BeginPaint 会写坏后面几个字段。
type paintStruct struct {
	HDC         uintptr
	FErase      int32
	RcPaint     rectStruct
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

// colorRef 把 BGR 三字节转成 GDI 的 COLORREF（0x00BBGGRR）。
func colorRef(c col) uintptr {
	return uintptr(c[0]) | uintptr(c[1])<<8 | uintptr(c[2])<<16
}

// iptr 把 int 参数按 Win32 的 32 位有符号语义转成 uintptr。
// 虚拟桌面坐标常为负（显示器位于主屏左侧/上方），直接 uintptr(负数) 会得到
// 64 位符号扩展值，语义含糊；这里显式截成低 32 位补码，与 API 期望一致。
func iptr(v int) uintptr { return uintptr(uint32(int32(v))) }

// loadCursor 取系统光标句柄。
func loadCursor(id uintptr) uintptr {
	c, _, _ := procLoadCursorW.Call(0, id)
	return c
}

// lowWord / highWord 从 lParam 中拆出鼠标坐标（int16 有符号，覆盖负值场景）。
func lowWord(lParam uintptr) int  { return int(int16(lParam & 0xFFFF)) }
func highWord(lParam uintptr) int { return int(int16((lParam >> 16) & 0xFFFF)) }

// makeLong 是 Win32 的 MAKELONG：低 16 位 lo、高 16 位 hi。
func makeLong(lo, hi int) uintptr {
	return uintptr(uint16(lo)) | uintptr(uint16(hi))<<16
}

// clampInt / minInt / maxInt：minInt、maxInt 定义在跨平台的 screenshot.go 中。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
