package platform

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	user32DLL          = syscall.NewLazyDLL("user32.dll")
	procGetCursorPos   = user32DLL.NewProc("GetCursorPos")
	procMonitorFromPt  = user32DLL.NewProc("MonitorFromPoint")
	procGetMonitorInfo = user32DLL.NewProc("GetMonitorInfoW")
	procGetWindowRect  = user32DLL.NewProc("GetWindowRect")
	procSetWindowPos   = user32DLL.NewProc("SetWindowPos")
)

// SetWindowToCursorScreen repositions the window to be centered on the monitor
// where the mouse cursor is currently located (multi-monitor support).
// winWidth/winHeight are the desired DIP size (used only as fallback when
// the window's current physical size cannot be determined).
//
// All Windows API calls here operate in physical pixel coordinates.
// We bypass Wails' SetPosition() (which expects DIP and internally
// converts DIP→physical) to avoid double-scaling on high-DPI displays.
//
// IMPORTANT: We do NOT use w32.MonitorFromPoint() because that wrapper
// passes x and y as separate uintptr arguments, but the Windows API
// expects a POINT struct (two int32 packed into one 8-byte value).
// On 64-bit Windows this causes dwFlags to receive the y coordinate,
// making the function return NULL.
func SetWindowToCursorScreen(win *application.WebviewWindow, winWidth, winHeight int) {
	if win == nil {
		return
	}

	// 1. Get cursor position (physical pixel coordinates)
	var cursorPt struct{ X, Y int32 }
	ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursorPt)))
	if ret == 0 {
		fmt.Println("QuickDock: GetCursorPos failed, keeping default position")
		return
	}

	// 2. Find nearest monitor.
	// Pack POINT into a single uintptr: low 32 bits = X, high 32 bits = Y.
	// This is how the x64 calling convention passes an 8-byte struct by value.
	pointPacked := uintptr(uint32(cursorPt.X)) | (uintptr(uint32(cursorPt.Y)) << 32)
	const MONITOR_DEFAULTTONEAREST = 0x00000002
	hMonitor, _, _ := procMonitorFromPt.Call(pointPacked, uintptr(MONITOR_DEFAULTTONEAREST))
	if hMonitor == 0 {
		fmt.Println("QuickDock: MonitorFromPoint failed, keeping default position")
		return
	}

	// 3. Get monitor work area (physical coordinates — excludes taskbar etc.)
	var mi struct {
		CbSize    uint32
		RcMonitor struct{ Left, Top, Right, Bottom int32 }
		RcWork    struct{ Left, Top, Right, Bottom int32 }
		DwFlags   uint32
	}
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	ret, _, _ = procGetMonitorInfo.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
	if ret == 0 {
		fmt.Println("QuickDock: GetMonitorInfo failed, keeping default position")
		return
	}

	workLeft := int(mi.RcWork.Left)
	workTop := int(mi.RcWork.Top)
	workWidth := int(mi.RcWork.Right - mi.RcWork.Left)
	workHeight := int(mi.RcWork.Bottom - mi.RcWork.Top)

	// 4. Try to use SetWindowPos directly with physical coordinates
	hwnd := win.NativeWindow()
	if hwnd != nil {
		// Get the window's current physical size via GetWindowRect.
		// This works even on hidden windows (returns last known position/size).
		var winRect struct{ Left, Top, Right, Bottom int32 }
		ret, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&winRect)))

		var physW, physH int
		if ret != 0 && winRect.Right > winRect.Left && winRect.Bottom > winRect.Top {
			physW = int(winRect.Right - winRect.Left)
			physH = int(winRect.Bottom - winRect.Top)
		} else {
			// Window not yet sized — use DIP dimensions as approximation.
			physW = winWidth
			physH = winHeight
		}

		// 5. Calculate centered position (all physical coordinates)
		x := workLeft + (workWidth-physW)/2
		y := workTop + (workHeight-physH)/2

		// Clamp to work area bounds
		if x < workLeft {
			x = workLeft
		}
		if y < workTop {
			y = workTop
		}
		if x+physW > int(mi.RcWork.Right) {
			x = int(mi.RcWork.Right) - physW
		}
		if y+physH > int(mi.RcWork.Bottom) {
			y = int(mi.RcWork.Bottom) - physH
		}

		// 6. Move window using SetWindowPos (physical coordinates, no DPI conversion)
		const SWP_NOSIZE = 0x0001
		const SWP_NOZORDER = 0x0004
		procSetWindowPos.Call(
			uintptr(hwnd),
			0, // HWND_TOP (ignored with SWP_NOZORDER)
			uintptr(x), uintptr(y),
			0, 0, // width/height ignored (SWP_NOSIZE)
			uintptr(SWP_NOSIZE|SWP_NOZORDER),
		)

		return
	}

	// 7. Fallback: use Wails SetPosition (DIP coordinates).
	// This path is only hit if NativeWindow() returns nil (shouldn't happen on Windows).
	x := workLeft + (workWidth-winWidth)/2
	y := workTop + (workHeight-winHeight)/2

	if x < workLeft {
		x = workLeft
	}
	if y < workTop {
		y = workTop
	}
	if x+winWidth > int(mi.RcWork.Right) {
		x = int(mi.RcWork.Right) - winWidth
	}
	if y+winHeight > int(mi.RcWork.Bottom) {
		y = int(mi.RcWork.Bottom) - winHeight
	}

	win.SetPosition(x, y)
}
