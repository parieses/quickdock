package services

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/screenshot"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ===== 区域截图 =====
//
// 抓屏、框选、工具条、标注全部在 internal/screenshot 的同一个原生分层窗口里完成
// （Windows 侧自绘，不用 Wails 的 WebView 窗口——见该包注释，闪屏根因在那里）。
// 本文件只负责宿主侧编排：收起自身窗口 → 交给覆盖层 → 按用户选的动作复制或保存。
//
// 覆盖层不再弹出第二个标注窗口：工具条与标注图形都和框选共用同一张 DIB，
// 因此「框选完工具条出现」这个动作本身也不会有任何闪动。

// screenshotOverlay 覆盖窗口管理器。启动时预创建，热键触发路径只剩
// 「抓屏 + 显示覆盖层」，不再承担建窗开销。
var screenshotOverlay *screenshot.Overlay

// InitScreenshotOverlay 预创建截图覆盖窗口。必须在 app.Run() 之前调用：
// 与浮窗预创建同理，避免首次热键时在回调 goroutine 上跨线程创建窗口。
func InitScreenshotOverlay() {
	o := screenshot.NewOverlay()
	if err := o.Start(); err != nil {
		logger.W("[screenshot] 覆盖窗口预创建失败: %v", err)
		return
	}
	screenshotOverlay = o
	logger.I("[screenshot] 覆盖窗口已预创建")
}

// InitScreenshotPins 注入贴图钉屏所需的宿主能力。
//
// 钉图窗口是 internal/screenshot 里自建的原生 GDI 窗口（不占 WebView2 进程），
// 它拿不到 Wails 的对话框与剪贴板，所以由这里把两件事交出去：
// 右键菜单的「复制到剪贴板」和「保存为文件」。
func InitScreenshotPins(app *application.App) {
	screenshot.SetPinActions(screenshot.PinActions{
		Copy: func(b *screenshot.Bitmap) error {
			return platform.SetClipboardImageData(b.ToNRGBA())
		},
		Save: func(b *screenshot.Bitmap) (string, error) {
			return saveImageDialog(app, b.ToNRGBA())
		},
	})
}

// ScreenshotSupported 报告当前平台是否具备截图能力（darwin / linux 为 false）。
func ScreenshotSupported() bool { return screenshotOverlay != nil }

// CaptureSelection 触发一次区域截图：收起自身窗口 → 抓屏 → 框选/标注 →
// 按用户在工具条上选的动作复制到剪贴板或弹出保存对话框。
//
// 用户取消时 width/height 为 0 且 Code 仍为 0（取消不是错误）。
//
// 注意：本方法会阻塞到用户完成或取消。Wails 的绑定调用不占用 UI 线程，
// 但前端会一直 await，因此调用方应做好「正在截图」的界面提示。
func (a *AppService) CaptureSelection() *ApiResult {
	if screenshotOverlay == nil {
		return FailMsg("截图功能当前不可用")
	}

	restore := a.hideForCapture()
	res, err := screenshot.Select(screenshotOverlay)
	// 覆盖层收起后才轮到保存对话框，宿主窗口必须先回到屏幕。
	restore()

	if err != nil {
		return Fail(err)
	}
	if res.Empty() {
		return Ok(map[string]int{"width": 0, "height": 0})
	}

	img := res.Bitmap.ToNRGBA()

	switch res.Action {
	case screenshot.ActionSave:
		path, saveErr := saveImageDialog(a.app, img)
		if saveErr != nil {
			return Fail(saveErr)
		}
		if path == "" {
			// 保存对话框被取消：当作没发生，不算失败。
			return Ok(map[string]any{"width": res.Rect.W, "height": res.Rect.H, "path": ""})
		}
		return Ok(map[string]any{"width": res.Rect.W, "height": res.Rect.H, "path": path})

	case screenshot.ActionPin:
		// 钉回原位置：用户看到的图和它原来在屏幕上的位置一致。
		// 贴图不进剪贴板——它是「留在屏幕上对照」的用途，与复制是两条路。
		if !screenshot.PinImage(res.Bitmap, res.Rect.X, res.Rect.Y) {
			return FailMsg("贴图失败")
		}
		logger.I("[screenshot] 已贴图 %dx%d @ (%d,%d)",
			res.Rect.W, res.Rect.H, res.Rect.X, res.Rect.Y)
		return Ok(map[string]any{"width": res.Rect.W, "height": res.Rect.H, "pinned": true})
	}

	if err := platform.SetClipboardImageData(img); err != nil {
		return Fail(err)
	}
	logger.I("[screenshot] 已复制 %dx%d 区域到剪贴板", res.Rect.W, res.Rect.H)
	return Ok(map[string]int{"width": res.Rect.W, "height": res.Rect.H})
}

// saveImageDialog 弹原生保存对话框，把图片写成 PNG。
// 返回保存的完整路径；用户取消时返回空串且 err == nil。
func saveImageDialog(app *application.App, img image.Image) (string, error) {
	if app == nil {
		return "", errors.New("应用未初始化")
	}

	name := "screenshot-" + time.Now().Format("20060102-150405") + ".png"
	path, err := app.Dialog.SaveFile().
		SetMessage("保存截图").
		SetFilename(name).
		AddFilter("PNG 图片", "*.png").
		PromptForSingleSelection()
	if err != nil || path == "" {
		return "", nil // 用户取消（部分平台取消返回 error 而非空串）
	}
	if filepath.Ext(path) == "" {
		path += ".png"
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	logger.I("[screenshot] 已保存到 %s", path)
	return path, nil
}

// hideForCapture 临时收起会遮挡截图的宿主窗口，返回恢复函数。
//
// 与 plugin_host.go 的 host.window.hide 同源思路（那个是给屏幕取色用的）：
// 只隐藏原本可见的窗口、恢复时也只还原它们，避免截图后误弹出原本隐藏的窗口；
// 同时同步 WindowFlags，防止热键 toggle 与实际可见性错位。
//
// ⚠️ 两个必守的点（都踩过）：
//  1. WindowFlags 必须**成对**改。原先只在隐藏时置 false、恢复时不置回 true，
//     结果命令面板这类「本来就开着」的窗口截图后会变成「显示一个已显示的窗口」，
//     再按面板热键看着像没反应。
//  2. 独立插件窗口（「在窗口中打开」模式）同样会遮挡截图，必须一并收起——
//     它不归主窗口/浮窗的候选列表管，走 PluginWindowMgr。
func (a *AppService) hideForCapture() func() {
	// 每个被隐藏的窗口配上它对应的可见性标志，恢复时成对还原。
	type hiddenWin struct {
		w    *application.WebviewWindow
		flag *atomic.Bool
	}
	var mainFlag, clipFlag, palFlag, noteFlag *atomic.Bool
	if a.Flags != nil {
		mainFlag = &a.Flags.Main
		clipFlag = &a.Flags.Clipboard
		palFlag = &a.Flags.Palette
		noteFlag = &a.Flags.Note
	}

	var hidden []hiddenWin
	hide := func(w *application.WebviewWindow, flag *atomic.Bool) {
		if w == nil || !w.IsVisible() {
			return
		}
		w.Hide()
		if flag != nil {
			flag.Store(false)
		}
		hidden = append(hidden, hiddenWin{w: w, flag: flag})
	}

	hide(a.MainWindow, mainFlag)
	if a.GetClipboardWindow != nil {
		hide(a.GetClipboardWindow(), clipFlag)
	}
	if a.GetPaletteWindow != nil {
		hide(a.GetPaletteWindow(), palFlag)
	}
	if a.GetNoteWindow != nil {
		hide(a.GetNoteWindow(), noteFlag)
	}

	var pluginIDs []string
	if a.PluginWindowMgr != nil {
		pluginIDs = a.PluginWindowMgr.HideAllVisibleForCapture()
	}

	if len(hidden) == 0 && len(pluginIDs) == 0 {
		return func() {}
	}

	// Hide 是异步的：窗口过程要再跑一轮才真正从屏幕消失。抓屏紧随其后，
	// 必须给桌面留出重绘时间，否则仍会把窗口残影拍进截图。
	time.Sleep(180 * time.Millisecond)

	return func() {
		for _, h := range hidden {
			h.w.Show()
			if h.flag != nil {
				h.flag.Store(true)
			}
		}
		if a.PluginWindowMgr != nil {
			a.PluginWindowMgr.RestoreAfterCapture(pluginIDs)
		}
	}
}
