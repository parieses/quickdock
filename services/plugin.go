package services

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"quickdock/internal/plugin"
)

// ===== 插件热键注册管理 =====

// 预编译的正则表达式（用于插件前端 HTML 内联）
var (
	inlineCSSRe = regexp.MustCompile(`<link\s[^>]*?(?:rel="stylesheet"|rel='stylesheet')[^>]*?>`)
	inlineJSRe  = regexp.MustCompile(`<script[^>]*src\s*=\s*["'][^"']*["'][^>]*>`)
)

func (a *AppService) InstallPlugin(zipPath string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	dir, err := a.PluginMgr.InstallFromZip(zipPath)
	if err != nil {
		return Fail(err)
	}
	// 读取 manifest 以获取插件元信息
	manifest, err := plugin.LoadManifest(dir + "/plugin.json")
	if err != nil {
		return Ok(map[string]interface{}{
			"dir": dir,
			"note": "安装完成但读取 manifest 失败: " + err.Error(),
		})
	}
	// 写入数据库记录（含 capabilities / permissions / category）
	permissions := make(map[string]interface{})
	if manifest.Permissions.Network || manifest.Permissions.Filesystem || manifest.Permissions.Clipboard {
		permissions["network"] = manifest.Permissions.Network
		permissions["filesystem"] = manifest.Permissions.Filesystem
		permissions["clipboard"] = manifest.Permissions.Clipboard
	}
	if err := a.DB.InsertPluginFull(manifest.ID, manifest.Name, manifest.Version, manifest.Author, manifest.Description, manifest.Category, manifest.Capabilities, permissions); err != nil {
		fmt.Printf("QuickDock: 插件 %s 写入数据库记录失败: %v\n", manifest.ID, err)
	}
	return Ok(map[string]interface{}{
		"id":      manifest.ID,
		"name":    manifest.Name,
		"version": manifest.Version,
		"dir":     dir,
	})
}

// SelectAndInstallPlugin 打开原生文件对话框选择 .zip 并安装
func (a *AppService) SelectAndInstallPlugin() *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	if a.app == nil {
		return FailMsg("app not initialized")
	}

	filePath, err := a.app.Dialog.OpenFile().
		SetTitle("选择插件包 (.zip)").
		AddFilter("插件包", "*.zip").
		PromptForSingleSelection()
	if err != nil {
		return Fail(err)
	}
	if filePath == "" {
		return Ok(nil) // 用户取消了选择
	}
	return a.InstallPlugin(filePath)
}

// InstallPluginFromBytes 接受前端上传的文件字节安装插件（拖拽 fallback）
func (a *AppService) InstallPluginFromBytes(fileName string, fileData []byte) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	// 写入临时文件
	tmpDir := filepath.Join(os.TempDir(), "quickdock-plugin-install")
	os.MkdirAll(tmpDir, 0755)
	tmpPath := filepath.Join(tmpDir, fileName)
	if err := os.WriteFile(tmpPath, fileData, 0644); err != nil {
		return Fail(fmt.Errorf("写入临时文件失败: %w", err))
	}
	defer os.Remove(tmpPath)

	// 调用标准的 InstallFromZip
	return a.InstallPlugin(tmpPath)
}

// PluginHotkeyRegistry 管理插件声明的全局热键
type PluginHotkeyRegistry struct {
	mu       sync.Mutex
	accelMap map[string]string // "Ctrl+Shift+T" → "pluginID.commandID"
	byPlugin map[string][]string // pluginID → []accel （便于卸载时批量清理）
}

func NewPluginHotkeyRegistry() *PluginHotkeyRegistry {
	return &PluginHotkeyRegistry{
		accelMap: make(map[string]string),
		byPlugin: make(map[string][]string),
	}
}

// Register 注册插件热键，返回错误如果冲突
func (r *PluginHotkeyRegistry) Register(accel, pluginID, commandID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 冲突检测
	if existing, ok := r.accelMap[accel]; ok {
		return fmt.Errorf("热键 %s 已被 %s 占用: %w", accel, existing, plugin.ErrHotkeyConflict)
	}

	r.accelMap[accel] = pluginID + "." + commandID
	r.byPlugin[pluginID] = append(r.byPlugin[pluginID], accel)
	return nil
}

// UnregisterAll 卸载插件时清理所有热键
func (r *PluginHotkeyRegistry) UnregisterAll(pluginID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	accels := r.byPlugin[pluginID]
	for _, accel := range accels {
		delete(r.accelMap, accel)
	}
	delete(r.byPlugin, pluginID)
	return accels
}

// GetPluginAccels 返回插件注册的所有热键（用于外部注销系统快捷键）
func (r *PluginHotkeyRegistry) GetPluginAccels(pluginID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]string, len(r.byPlugin[pluginID]))
	copy(result, r.byPlugin[pluginID])
	return result
}

// ===== 插件系统 API =====

func (a *AppService) ListPlugins() *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	return Ok(a.PluginMgr.ListPlugins())
}

func (a *AppService) ExecutePluginCommand(pluginID, commandID string, input map[string]interface{}) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	result, err := a.PluginMgr.ExecuteCommand(pluginID, commandID, input)
	if err != nil {
		return Fail(err)
	}
	return Ok(result)
}

func (a *AppService) EnablePlugin(id string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	// 启用：更新数据库状态后加载插件
	if err := a.DB.SetPluginEnabled(id, 1); err != nil {
		return dberr(err)
	}
	// 从插件目录重新加载
	manifest, err := a.PluginMgr.ReloadPlugin(id)
	if err != nil {
		return Fail(err)
	}

	// 注册插件声明的热键：先清理旧的热键避免自冲突
	if a.PluginHotkeys != nil && manifest != nil {
		// 先注销该插件之前注册的所有热键（系统级 + 内部注册表）
		if a.app != nil {
			for _, accel := range a.PluginHotkeys.GetPluginAccels(id) {
				_ = a.app.GlobalShortcut.Unregister(accel)
			}
		}
		a.PluginHotkeys.UnregisterAll(id)

		// 重新注册
		for _, cmd := range manifest.Commands {
			if cmd.Hotkey == "" {
				continue
			}
			accel := hotkeyStringToAccel(cmd.Hotkey)
			if err := a.PluginHotkeys.Register(accel, id, cmd.ID); err != nil {
				fmt.Printf("QuickDock: 插件 %s 热键 %s 注册失败: %v\n", id, accel, err)
			} else if a.app != nil {
				_ = a.app.GlobalShortcut.Register(accel, func() {
					a.executePluginCommand(id, cmd.ID)
				})
			}
		}
	}

	return Ok(manifest)
}

func (a *AppService) DisablePlugin(id string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	// StopPlugin 停止进程但保留在列表中，禁用后仍然能看到并重新启用
	if err := a.PluginMgr.StopPlugin(id); err != nil {
		// 插件可能不在内存中（初次启动时 DB 禁用但未加载），这不是错误
		_ = err
	}
	if err := a.DB.SetPluginEnabled(id, 0); err != nil {
		return dberr(err)
	}

	// 清理插件热键（内部注册表 + 系统全局快捷键）
	if a.PluginHotkeys != nil {
		accels := a.PluginHotkeys.UnregisterAll(id)
		if a.app != nil {
			for _, accel := range accels {
				_ = a.app.GlobalShortcut.Unregister(accel)
			}
		}
	}

	return Ok(nil)
}

// executePluginCommand 内部调用插件命令（供热键回调使用）
func (a *AppService) executePluginCommand(pluginID, commandID string) {
	result, err := a.PluginMgr.ExecuteCommand(pluginID, commandID, nil)
	if err != nil {
		fmt.Printf("QuickDock: 插件 %s 命令 %s 执行失败: %v\n", pluginID, commandID, err)
	} else if result != nil {
		fmt.Printf("QuickDock: 插件 %s 命令 %s 执行成功\n", pluginID, commandID)
	}
}

// hotkeyStringToAccel 将 "Ctrl+Shift+T" 转为 Wails Accelerator 格式 "Ctrl+Shift+T"
// Wails 的 Accelerator 格式与标准表示法一致
func hotkeyStringToAccel(hotkey string) string {
	parts := strings.Split(hotkey, "+")
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "ctrl":
			parts[i] = "Ctrl"
		case "alt":
			parts[i] = "Alt"
		case "shift":
			parts[i] = "Shift"
		case "win", "super", "cmd":
			parts[i] = "Super"
		default:
			// 非修饰键统一小写，确保 "Ctrl+T" 和 "Ctrl+t" 被视为同一热键
			parts[i] = strings.ToLower(p)
		}
	}
	return strings.Join(parts, "+")
}

func (a *AppService) UninstallPlugin(id string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	a.PluginMgr.UnloadPlugin(id)
	if err := a.PluginMgr.UninstallPlugin(id); err != nil {
		return Fail(err)
	}
	// 清理热键（内部注册表 + 系统全局快捷键）
	if a.PluginHotkeys != nil {
		accels := a.PluginHotkeys.UnregisterAll(id)
		if a.app != nil {
			for _, accel := range accels {
				_ = a.app.GlobalShortcut.Unregister(accel)
			}
		}
	}
	// 清理数据库记录和数据
	if err := a.DB.DeletePlugin(id); err != nil {
		return dberr(err)
	}
	if err := a.DB.CleanPluginData(id); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) GetPluginFrontendURL(pluginID string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	path, err := a.PluginMgr.GetFrontendPath(pluginID)
	if err != nil {
		return Fail(err)
	}
	return Ok(path)
}

// GetPluginIcon 获取插件图标（返回 base64 data URI）
func (a *AppService) GetPluginIcon(pluginID string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	inst := a.PluginMgr.GetPlugin(pluginID)
	if inst == nil {
		return FailMsg("插件未加载")
	}
	if inst.Manifest.Icon == "" {
		return Ok(nil)
	}
	iconPath := filepath.Join(inst.Dir, inst.Manifest.Icon)
	data, err := os.ReadFile(iconPath)
	if err != nil {
		return Ok(nil) // 图标文件不存在不是致命错误
	}
	// 根据扩展名推断 MIME
	ext := strings.ToLower(filepath.Ext(inst.Manifest.Icon))
	var mime string
	switch ext {
	case ".svg":
		mime = "image/svg+xml"
	case ".png":
		mime = "image/png"
	case ".ico":
		mime = "image/x-icon"
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	default:
		mime = "image/png"
	}
	dataURI := fmt.Sprintf("data:%s;base64,%s", mime, base64Encode(data))
	return Ok(dataURI)
}

// GetPluginFrontendPage 获取插件前端页面（内联 CSS/JS 的单 HTML 文件）
func (a *AppService) GetPluginFrontendPage(pluginID string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	inst := a.PluginMgr.GetPlugin(pluginID)
	if inst == nil {
		return FailMsg("插件未加载")
	}
	if !inst.Manifest.Frontend.Enabled {
		return FailMsg("插件未启用前端")
	}
	entryPath := filepath.Join(inst.Dir, inst.Manifest.Frontend.Entry)

	// 检查缓存（以文件 mtime 为缓存 key）
	const maxHTMLSize = 10 << 20
	fi, err := os.Stat(entryPath)
	if err != nil {
		return Fail(err)
	}
	if fi.Size() > maxHTMLSize {
		return Fail(fmt.Errorf("插件前端文件过大 (%d bytes)", fi.Size()))
	}

	a.frontendCacheMu.RLock()
	entry, cached := a.frontendCache[pluginID]
	a.frontendCacheMu.RUnlock()
	if cached && entry.mtime.Equal(fi.ModTime()) {
		return Ok(entry.html)
	}

	htmlData, err := os.ReadFile(entryPath)
	if err != nil {
		return Fail(err)
	}
	html := string(htmlData)
	baseDir := filepath.Dir(entryPath)

	// 内联 CSS：匹配 <link ... href="..." rel="stylesheet" .../> 无论属性顺序和引号类型
	html = inlineFileRefs(html, baseDir, inlineCSSRe, func(match string) string {
		href := extractAttrValue(match, "href")
		if href == "" {
			return match
		}
		data, err := os.ReadFile(filepath.Join(baseDir, href))
		if err != nil {
			return ""
		}
		return "<style>\n" + string(data) + "\n</style>"
	})

	// 内联 JS：匹配 <script ... src="..." ...></script>
	html = inlineFileRefs(html, baseDir, inlineJSRe, func(match string) string {
		src := extractAttrValue(match, "src")
		if src == "" {
			return match
		}
		data, err := os.ReadFile(filepath.Join(baseDir, src))
		if err != nil {
			return ""
		}
		return "<script>\n" + string(data) + "\n</script>"
	})

	// 写入缓存
	a.frontendCacheMu.Lock()
	a.frontendCache[pluginID] = &frontendCacheEntry{html: html, mtime: fi.ModTime()}
	a.frontendCacheMu.Unlock()

	return Ok(html)
}

// inlineFileRefs 替换 HTML 中引用的外部文件为内联内容
// re 应匹配整个标签，由 loader 从 match 中自行提取路径
func inlineFileRefs(html, baseDir string, re *regexp.Regexp, loader func(string) string) string {
	return re.ReplaceAllStringFunc(html, func(match string) string {
		inlined := loader(match)
		if inlined == "" {
			return match // 保留原引用
		}
		return inlined
	})
}

// extractAttrValue 从 HTML 标签中提取属性值（支持单引号和双引号）
func extractAttrValue(tag, attrName string) string {
	// 尝试双引号: attr="value"
	re := regexp.MustCompile(attrName + `\s*=\s*"([^"]*)"`)
	if m := re.FindStringSubmatch(tag); len(m) >= 2 {
		return m[1]
	}
	// 尝试单引号: attr='value'
	re = regexp.MustCompile(attrName + `\s*=\s*'([^']*)'`)
	if m := re.FindStringSubmatch(tag); len(m) >= 2 {
		return m[1]
	}
	return ""
}

// base64Encode 辅助函数
func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}


// ===== 插件独立窗口 =====
// 插件前端页面在独立窗口中打开（使用 iframe 嵌入）

// ShowPluginWindow 打开插件独立窗口（或切换插件 ID）
func (a *AppService) ShowPluginWindow(pluginID string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	inst := a.PluginMgr.GetPlugin(pluginID)
	if inst == nil {
		return FailMsg("插件未加载")
	}
	if !inst.Manifest.Frontend.Enabled {
		return FailMsg("插件未启用前端")
	}

	winFn := a.GetPluginWindow
	if winFn == nil {
		return FailMsg("plugin window not initialized")
	}
	win := winFn()
	if win == nil {
		return FailMsg("failed to create plugin window")
	}

	// 先显示窗口激活 webview，再通过 JS 切换 hash（带时间戳确保每次 hash 都变化）
	win.SetTitle("快启坞 - " + inst.Manifest.Name)
	win.Show()
	win.Focus()
	// 使用 strconv.Quote 安全转义 pluginID，防止 JS 注入
	hashValue := "#/plugin/" + pluginID + "?t=" + fmt.Sprintf("%d", time.Now().UnixNano())
	win.ExecJS("window.location.hash = " + strconv.Quote(hashValue))
	return Ok(nil)
}

// SetPendingPluginInit 设置待传递给插件窗口的初始文本（从命令面板→插件窗口跨窗口传递）
func (a *AppService) SetPendingPluginInit(text string) *ApiResult {
	a.pendingInitTextMu.Lock()
	a.pendingInitText = text
	a.pendingInitTextMu.Unlock()
	return Ok(nil)
}

// GetAndClearPendingPluginInit 获取并清除待传递的初始文本
func (a *AppService) GetAndClearPendingPluginInit() *ApiResult {
	a.pendingInitTextMu.Lock()
	text := a.pendingInitText
	a.pendingInitText = ""
	a.pendingInitTextMu.Unlock()
	if text == "" {
		return Ok(nil)
	}
	return Ok(text)
}

// MinimizePluginWindow 最小化插件独立窗口
func (a *AppService) MinimizePluginWindow() *ApiResult {
	if fn := a.GetPluginWindow; fn != nil {
		if win := fn(); win != nil {
			win.Minimise()
		}
	}
	return Ok(nil)
}

// ToggleMaximizePluginWindow 切换插件窗口最大化/还原
func (a *AppService) ToggleMaximizePluginWindow() *ApiResult {
	if fn := a.GetPluginWindow; fn != nil {
		if win := fn(); win != nil {
			if win.IsMaximised() {
				win.Restore()
			} else {
				win.Maximise()
			}
		}
	}
	return Ok(nil)
}

// HidePluginWindow 隐藏插件独立窗口（关闭按钮）
// 隐藏前清理 iframe 的 hash，使 PluginPage 组件卸载并释放 iframe 资源
func (a *AppService) HidePluginWindow() *ApiResult {
	if fn := a.GetPluginWindow; fn != nil {
		if win := fn(); win != nil {
			win.ExecJS("window.location.hash = ''")
			win.Hide()
		}
	}
	return Ok(nil)
}

// GetPluginWindowPluginID 获取当前插件窗口显示的插件 ID
func (a *AppService) GetPluginWindowPluginID() *ApiResult {
	return Ok(nil) // 由前端通过 hash 自行判断
}
