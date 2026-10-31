//go:build !windows

package platform

// 非 Windows 平台的多显示器定位占位。
//
// 待移植方案：
//   - macOS：NSScreen.frame + NSEvent.mouseLocation 取光标所在屏
//   - Linux X11：XQueryPointer + Xinerama/Randr；Wayland 协议不一，先退化为不动
//
// 当前行为：不做定位，窗口按创建时默认位置显示。

import "github.com/wailsapp/wails/v3/pkg/application"

func SetWindowToCursorScreen(win *application.WebviewWindow, winWidth, winHeight int) {}
