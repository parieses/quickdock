package services

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
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
// 本文件只负责宿主侧编排：交给覆盖层 → 按用户选的动作复制或保存。
// 截图期间不收起自身窗口（见 CaptureSelection 说明）。
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

// CaptureSelection 触发一次区域截图：抓屏 → 框选/标注 →
// 按用户在工具条上选的动作复制到剪贴板或弹出保存对话框。
//
// 设计：截图期间**不隐藏**宿主窗口与插件窗口，截完也**不恢复/抢焦点**。
// 即软件本体与各插件窗口的可见性与焦点保持原样，避免「截图时最小化、截完又弹出」
// 的打断感——满足「F1 截图不打扰当前窗口布局」的诉求。
// 代价：若软件窗口当时正显示在屏幕上，会一并被截进图片（与干净的「纯桌面」截图不同）。
// 若日后想要「既不动窗口、又能截出干净桌面」，可改为对自有窗口设置
// SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE)，让它们「可见但被捕获排除」。
//
// 用户取消时 width/height 为 0 且 Code 仍为 0（取消不是错误）。
//
// 注意：本方法会阻塞到用户完成或取消。Wails 的绑定调用不占用 UI 线程，
// 但前端会一直 await，因此调用方应做好「正在截图」的界面提示。
func (a *AppService) CaptureSelection() *ApiResult {
	if screenshotOverlay == nil {
		return FailMsg("截图功能当前不可用")
	}

	res, err := screenshot.Select(screenshotOverlay)

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
