//go:build !windows

package platform

import "github.com/wailsapp/wails/v3/pkg/application"

// ReapplyWindowBackdrop 非 Windows 下是 no-op：宿主材质只存在于 Windows 的 DWM，
// mac / Linux 的窗口透明由 Wails 自己的背景类型处理，不需要运行期重写。
func ReapplyWindowBackdrop(win *application.WebviewWindow) {}

// KeepBackdropOnShow 非 Windows 下是 no-op（同上）。
func KeepBackdropOnShow(win *application.WebviewWindow) {}
