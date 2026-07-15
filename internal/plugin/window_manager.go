package plugin

import (
	"fmt"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// PluginWindowManager 管理每个插件的独立窗口（窗口注册表模式）
// 每个 pluginID 对应一个独立的 WebviewWindow，互不干扰
type PluginWindowManager struct {
	mu         sync.Mutex
	windows    map[string]*application.WebviewWindow // pluginID → 独立窗口
	app        *application.App
	baseWidth  int
	baseHeight int
}

// NewPluginWindowManager 创建窗口管理器
func NewPluginWindowManager(app *application.App) *PluginWindowManager {
	return &PluginWindowManager{
		windows:    make(map[string]*application.WebviewWindow),
		app:        app,
		baseWidth:  800,
		baseHeight: 600,
	}
}

// Show 显示插件窗口。如果窗口不存在则创建新窗口。
// showInTaskbar: 是否在任务栏显示图标（分离模式 = true）
// 返回 (窗口, 是否为新创建)
func (m *PluginWindowManager) Show(pluginID, title string, showInTaskbar bool) (*application.WebviewWindow, bool) {
	// 检查是否已有该插件的窗口
	m.mu.Lock()
	if win, ok := m.windows[pluginID]; ok {
		m.mu.Unlock()
		win.Show()
		win.Focus()
		return win, false
	}
	m.mu.Unlock()

	// 创建新窗口
	win := m.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "快启坞 - " + title,
		Width:            m.baseWidth,
		Height:           m.baseHeight,
		MinWidth:         400,
		MinHeight:        300,
		Frameless:        true,
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
		URL:              "/#/plugin/" + pluginID,
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: !showInTaskbar,
		},
	})

	// 用户点击关闭按钮 → 真正销毁窗口，并从注册表删除
	win.OnWindowEvent(events.Common.WindowClosing, func(e *application.WindowEvent) {
		m.mu.Lock()
		delete(m.windows, pluginID)
		m.mu.Unlock()
		// 不调用 Cancel()，让窗口正常关闭销毁
	})

	win.Show()

	m.mu.Lock()
	m.windows[pluginID] = win
	m.mu.Unlock()

	return win, true
}

// ShowInPanel 在面板中显示插件窗口（任务栏隐藏，用于浮动面板模式）
func (m *PluginWindowManager) ShowInPanel(pluginID, title string) (*application.WebviewWindow, bool) {
	return m.Show(pluginID, title, false)
}

// ShowAsWindow 以独立窗口显示插件（任务栏可见，有独立图标，仅能通过 X 关闭）
func (m *PluginWindowManager) ShowAsWindow(pluginID, title string) (*application.WebviewWindow, bool) {
	return m.Show(pluginID, title, true)
}

// Hide 关闭并隐藏指定插件的窗口（从注册表移除，WebView2 进程保持以加速下次打开）
func (m *PluginWindowManager) Hide(pluginID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if win, ok := m.windows[pluginID]; ok {
		delete(m.windows, pluginID)
		win.Hide()
	}
}

// CloseAll 关闭所有插件窗口（应用退出时调用）
func (m *PluginWindowManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, win := range m.windows {
		delete(m.windows, id)
		win.Hide()
	}
}

// Minimize 最小化指定插件的窗口
func (m *PluginWindowManager) Minimize(pluginID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if win, ok := m.windows[pluginID]; ok {
		win.Minimise()
	}
}

// ToggleMaximize 切换指定插件的窗口最大化/还原
func (m *PluginWindowManager) ToggleMaximize(pluginID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if win, ok := m.windows[pluginID]; ok {
		if win.IsMaximised() {
			win.Restore()
		} else {
			win.Maximise()
		}
	}
}

// InjectInitText 向指定插件窗口注入初始文本（从命令面板跨窗口传递）
func (m *PluginWindowManager) InjectInitText(pluginID, text string) {
	m.mu.Lock()
	win, ok := m.windows[pluginID]
	m.mu.Unlock()
	if !ok {
		return
	}
	safeText := fmt.Sprintf("%q", text)
	win.ExecJS(fmt.Sprintf(
		`setTimeout(function(){
			var ifr = document.querySelector('iframe');
			if (ifr && ifr.contentWindow) ifr.contentWindow.postMessage({type:'plugin:init', data:{text:%s}}, '*');
		}, 400)`, safeText))
}
