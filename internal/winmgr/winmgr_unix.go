//go:build !windows

package winmgr

import "fmt"

// 非 Windows 平台占位：窗口管理依赖 Win32 API，Unix 下不支持，统一返回错误。
// 业务层调用前可用 build tag 隔离，或捕获错误提示用户。

func ForegroundWindow() (uintptr, error) {
	return 0, fmt.Errorf("window management is only supported on Windows")
}

func ForegroundInfo() (WindowInfo, error) {
	return WindowInfo{}, fmt.Errorf("window management is only supported on Windows")
}

func IsSelfWindow(hwnd uintptr) bool {
	return false
}

func WindowAlive(hwnd uintptr) bool {
	return false
}

func ToggleAlwaysOnTop(hwnd uintptr) (bool, error) {
	return false, fmt.Errorf("window management is only supported on Windows")
}

func ApplyLayout(hwnd uintptr, layout string, ratio float64, monitorIndex int) error {
	return fmt.Errorf("window management is only supported on Windows")
}

// WindowInfo 第三方窗口元信息（Unix 占位，仅用于跨平台符号一致；不实际使用）。
type WindowInfo struct {
	HWND  uintptr
	Title string
	Rect  Rect
}

// TilingCell 排版模板里的单个格子：归一化矩形（相对目标屏工作区，0~1）。
// Unix 占位，仅用于跨平台符号一致；不实际使用。
type TilingCell struct {
	X, Y, W, H float64
}

// TilingResult 一次排版的执行结果（Unix 占位）。
type TilingResult struct {
	Applied int
	Peers   int
	Titles  []string
}

// TilingInfo 排版作用范围描述（Unix 占位）。
type TilingInfo struct {
	ScreenW int
	ScreenH int
	Peers   int
}

func EnumWindows() ([]WindowInfo, error) {
	return nil, fmt.Errorf("window management is only supported on Windows")
}

func PlanTiling(targetHWND uintptr, useCursorScreen bool) (TilingInfo, error) {
	return TilingInfo{}, fmt.Errorf("window management is only supported on Windows")
}

func ApplyTiling(cells []TilingCell, targetCell int, targetHWND uintptr, useCursorScreen bool) (TilingResult, error) {
	return TilingResult{}, fmt.Errorf("window management is only supported on Windows")
}
