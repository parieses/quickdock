package services

import (
	"reflect"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// windowHWND 通过反射从 *application.WebviewWindow 取出底层 Win32 窗口句柄。
// Wails3 未公开 HWND 取值器，impl 是 webviewWindowImpl 接口，运行期具体类型为
// *windowsWebviewWindow（含 hwnd w32.HWND）。取不到时返回 0（调用方应忽略）。
func windowHWND(win *application.WebviewWindow) uintptr {
	if win == nil {
		return 0
	}
	v := reflect.ValueOf(win).Elem()
	implF := v.FieldByName("impl")
	if !implF.IsValid() || implF.IsNil() {
		return 0
	}
	implV := implF.Elem() // 解引用接口，得到具体值（*windowsWebviewWindow 指针）
	if !implV.IsValid() {
		return 0
	}
	// 具体类型是 *windowsWebviewWindow（指针），需再解引用一层拿到结构体
	if implV.Kind() == reflect.Ptr {
		implV = implV.Elem()
	}
	if !implV.IsValid() {
		return 0
	}
	hwndF := implV.FieldByName("hwnd")
	if !hwndF.IsValid() || (hwndF.Kind() != reflect.Uint && hwndF.Kind() != reflect.Uintptr) {
		return 0
	}
	return uintptr(hwndF.Uint())
}

// themeBackground 返回与 App 主题匹配的主窗口/插件窗口底色，避免黑底露出。
// 深：#1b1b1b（--color-bg-primary 深色）；浅：#f7f7f5（--color-bg-primary 浅色）。
func themeBackground(isDark bool) application.RGBA {
	if isDark {
		return application.RGBA{Red: 23, Green: 24, Blue: 27, Alpha: 255}
	}
	return application.RGBA{Red: 245, Green: 246, Blue: 248, Alpha: 255}
}

// ApplyTheme 将 App 主题应用到所有原生窗口：
//   - 主窗口（Frameless:false，系统原生标题栏）：通过 DWM 沉浸式暗色模式让标题栏
//     跟随 App 主题（浅色下不再发黑）。
//   - 插件窗口（Frameless:true）：原生标题栏不存在，只需让窗口底色跟随主题，
//     消除内容未填满时露出的黑底；同时记录到 PluginWindowMgr 供后续新建窗口沿用。
//
// 由前端 App.vue 在挂载与切换主题时调用。
func (a *AppService) ApplyTheme(isDark bool) *ApiResult {
	// 主窗口：原生标题栏 + WebView2 底色
	if a.MainWindow != nil {
		if hwnd := windowHWND(a.MainWindow); hwnd != 0 {
			// 使用 w32.SetTheme：内部先 DwmSetWindowAttribute(20, dark) 设沉浸式暗色属性，
			// 再 SetMenuTheme 调 AllowDarkModeForWindow + SetWindowTheme + InvalidateRect，
			// 让非客户区（标题栏）真正生效并触发一次重绘。
			// 经验：在本机（Windows 系统主题=浅色）下，这是唯一能让标题栏跟随 App 主题
			// 生效的写法——切换后略有延迟，但稳定可变。单独调 DwmSetWindowAttribute 或
			// 额外加 SetWindowPos/RedrawWindow 强制重绘反而会让标题栏「切了不变」，已验证。
			w32.SetTheme(hwnd, isDark)
		}
		a.MainWindow.SetBackgroundColour(themeBackground(isDark))
	}

	// 插件窗口：底色跟随主题（原生标题栏由 PluginPage 自定义、已随宿主主题）
	if a.PluginWindowMgr != nil {
		a.PluginWindowMgr.SetDarkMode(isDark)
	}

	return Ok(nil)
}
