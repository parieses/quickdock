package services

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/platform"
	"quickdock/internal/plugin"
)

// ===== 插件热键注册管理 =====

// 预编译的正则表达式（用于插件前端 HTML 内联）
var (
	inlineCSSRe = regexp.MustCompile(`<link\s[^>]*?(?:rel="stylesheet"|rel='stylesheet')[^>]*?>`)
	inlineJSRe  = regexp.MustCompile(`<script[^>]*src\s*=\s*["'][^"']*["'][^>]*>`)
	attrDblRe   = regexp.MustCompile(`([\w-]+)\s*=\s*"([^"]*)"`)
	attrSglRe   = regexp.MustCompile(`([\w-]+)\s*=\s*'([^']*)'`)
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
			"dir":  dir,
			"note": "安装完成但读取 manifest 失败: " + err.Error(),
		})
	}
	// 读取图标
	iconData := ""
	if manifest.Icon != "" {
		iconPath := filepath.Join(dir, manifest.Icon)
		if icoBytes, err := os.ReadFile(iconPath); err == nil && len(icoBytes) > 0 {
			mime := platform.IconMIME(filepath.Ext(manifest.Icon))
			iconData = fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(icoBytes))
		}
	}
	// 写入数据库记录（含 capabilities / permissions / category / icon）
	permissions := make(map[string]interface{})
	if manifest.Permissions.Network || manifest.Permissions.Filesystem || manifest.Permissions.Clipboard {
		permissions["network"] = manifest.Permissions.Network
		permissions["filesystem"] = manifest.Permissions.Filesystem
		permissions["clipboard"] = manifest.Permissions.Clipboard
	}
	if err := a.DB.InsertPluginFull(manifest.ID, manifest.Name, manifest.Version, manifest.Author, manifest.Description, manifest.Category, iconData, manifest.Capabilities, permissions); err != nil {
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
	if err != nil || filePath == "" {
		// 用户取消（某些系统取消会返回错误而非空路径）
		return Ok(nil)
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
	accelMap map[string]string   // "Ctrl+Shift+T" → "pluginID.commandID"
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
	plugins := a.PluginMgr.ListPlugins()
	// 从 usage_frecency 表查询每个插件的使用次数（一条 SQL 聚合全部，替代逐条查询）
	if a.DB != nil {
		if counts, err := a.DB.GetAllPluginUsageCounts(); err == nil {
			for i := range plugins {
				if c, ok := counts[plugins[i].ID]; ok && c > 0 {
					plugins[i].UsageCount = c
				}
			}
		}
	}
	return Ok(plugins)
}

func (a *AppService) ExecutePluginCommand(pluginID, commandID string, input map[string]interface{}) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	start := time.Now()
	result, err := a.PluginMgr.ExecuteCommand(pluginID, commandID, input)
	// 记录执行日志（5.2：忽略错误，不影响主流程）
	a.recordPluginExecLog(pluginID, commandID, "manual", start, result, err)
	// 记录插件使用次数
	if a.DB != nil {
		usageKey := "plugin:" + pluginID + "." + commandID
		// 记录插件使用并保留命令面板传入的附加输入（如端口号），避免用空 input 覆盖前端已存的 input
		inputText := ""
		if input != nil {
			if t, ok := input["text"].(string); ok {
				inputText = t
			}
		}
		a.DB.RecordUsageEx(usageKey, "plugin", "", "", inputText)
	}
	if err != nil {
		return Fail(err)
	}
	return Ok(result)
}

// recordPluginExecLog 写入一条插件命令执行日志（5.2）
func (a *AppService) recordPluginExecLog(pluginID, commandID, trigger string, start time.Time, result interface{}, execErr error) {
	if a.DB == nil {
		return
	}
	log := &db.PluginExecLog{
		PluginID:   pluginID,
		CommandID:  commandID,
		Success:    execErr == nil,
		DurationMs: int(time.Since(start).Milliseconds()),
		Trigger:    trigger,
	}
	if execErr != nil {
		log.Error = execErr.Error()
	} else if result != nil {
		if b, mErr := json.Marshal(result); mErr == nil {
			log.Result = string(b)
		} else {
			log.Result = fmt.Sprintf("%v", result)
		}
	}
	if len(log.Result) > 2000 {
		log.Result = log.Result[:2000]
	}
	if len(log.Error) > 2000 {
		log.Error = log.Error[:2000]
	}
	if err := a.DB.AddPluginExecLog(log); err != nil {
		fmt.Printf("QuickDock: 写入插件执行日志失败: %v\n", err)
	}
}

// ListPluginExecLogs 返回最近 limit 条插件命令执行日志（前端历史展示，5.2）
func (a *AppService) ListPluginExecLogs(limit int) *ApiResult {
	if a.DB == nil {
		return FailMsg("database not initialized")
	}
	logs, err := a.DB.ListPluginExecLogs(limit)
	if err != nil {
		return Fail(err)
	}
	return Ok(logs)
}

func (a *AppService) EnablePlugin(id string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	// 启用：更新数据库状态后加载插件
	if err := a.DB.SetPluginEnabled(id, 1); err != nil {
		return Fail(err)
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
		return Fail(err)
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
	start := time.Now()
	result, err := a.PluginMgr.ExecuteCommand(pluginID, commandID, nil)
	// 记录执行日志（5.2：忽略错误，不影响主流程）
	a.recordPluginExecLog(pluginID, commandID, "hotkey", start, result, err)
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
		return Fail(err)
	}
	if err := a.DB.CleanPluginData(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) GetPluginFrontendURL(pluginID string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	path, err := a.PluginMgr.GetFrontendPath(pluginID)
	return wrap(path, err)
}

// GetPluginIcon 获取插件图标（返回 base64 data URI）
func (a *AppService) GetPluginIcon(pluginID string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}

	// 优先从数据库读取图标
	if a.DB != nil {
		if iconData, err := a.DB.GetValue("plugin_icon_" + pluginID); err == nil && iconData != "" {
			return Ok(iconData)
		}
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
	mime := platform.IconMIME(filepath.Ext(inst.Manifest.Icon))
	dataURI := fmt.Sprintf("data:%s;base64,%s", mime, base64Encode(data))

	// 写入数据库缓存
	if a.DB != nil {
		a.DB.SetValue("plugin_icon_"+pluginID, dataURI)
	}

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
	if inst.Status != "running" {
		return FailMsg("插件未运行，无法打开前端页面")
	}
	if !inst.Manifest.Frontend.Enabled {
		return FailMsg("插件未启用前端")
	}
	entryPath := filepath.Join(inst.Dir, inst.Manifest.Frontend.Entry)

	// 检查缓存（以文件 mtime 为缓存 key，含 common.css mtime）
	const maxHTMLSize = 10 << 20
	fi, err := os.Stat(entryPath)
	if err != nil {
		return Fail(err)
	}
	if fi.Size() > maxHTMLSize {
		return Fail(fmt.Errorf("插件前端文件过大 (%d bytes)", fi.Size()))
	}

	// 读取 common.css / common.js 的最新 mtime（用于缓存失效判断，任一变更即失效）
	var commonMtime time.Time
	for _, name := range []string{"common.css", "common.js"} {
		p := filepath.Join(a.PluginsDir, "builtin", name)
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(commonMtime) {
			commonMtime = fi.ModTime()
		}
	}

	a.frontendCacheMu.RLock()
	entry, cached := a.frontendCache[pluginID]
	a.frontendCacheMu.RUnlock()
	if cached && entry.htmlMtime.Equal(fi.ModTime()) && entry.commonMtime.Equal(commonMtime) {
		return Ok(entry.html)
	}

	htmlData, err := os.ReadFile(entryPath)
	if err != nil {
		return Fail(err)
	}
	html := string(htmlData)
	baseDir := filepath.Dir(entryPath)

	// 强制注入 common.css（QuickDock 插件通用主题），确保所有插件都有正确的暗色/浅色适配
	commonCSSPath := filepath.Join(a.PluginsDir, "builtin", "common.css")
	if commonData, err := os.ReadFile(commonCSSPath); err == nil {
		commonStyle := "<style id=\"quickdock-common-css\">\n" + string(commonData) + "\n</style>\n"
		// 插入到 <head> 标签之后（或文档最前）
		if idx := strings.Index(html, "<head>"); idx >= 0 {
			html = html[:idx+6] + "\n" + commonStyle + html[idx+6:]
		} else {
			html = commonStyle + html
		}
	}

	// 注入 common.js（插件共享的前端工具函数：escapeHtml / copyText 等）
	// 注意：common.js 必须早于各插件 app.js 注入，保证全局函数可用
	commonJSPath := filepath.Join(a.PluginsDir, "builtin", "common.js")
	if commonJSData, err := os.ReadFile(commonJSPath); err == nil {
		commonJS := "<script id=\"quickdock-common-js\">\n" + string(commonJSData) + "\n</script>\n"
		if idx := strings.Index(html, "<head>"); idx >= 0 {
			html = html[:idx+6] + "\n" + commonJS + html[idx+6:]
		} else {
			html = commonJS + html
		}
	}

	// 注入 QuickDock 运行时脚本（主题/语言等控制）
	// 默认使用暗色主题 + 简体中文，父窗口可通过 plugin:theme 消息动态更新
	runtimeScript := "<script id=\"quickdock-runtime\">\n" +
		"(function(){" +
		"var t=(document.querySelector('meta[name=\"qd-theme\"]')||{}).getAttribute('content')||'dark';" +
		"var l=(document.querySelector('meta[name=\"qd-locale\"]')||{}).getAttribute('content')||'zh-CN';" +
		"document.documentElement.setAttribute('data-theme',t);" +
		"document.documentElement.setAttribute('lang',l);" +
		"window.addEventListener('message',function(e){" +
		"if(e.data&&e.data.type==='plugin:theme'){" +
		"if(e.data.theme)document.documentElement.setAttribute('data-theme',e.data.theme);" +
		"if(e.data.locale)document.documentElement.setAttribute('lang',e.data.locale);" +
		"}});" +
		"})();" +
		"</script>\n"
	if idx := strings.Index(html, "</head>"); idx >= 0 {
		html = html[:idx] + runtimeScript + html[idx:]
	} else {
		html += runtimeScript
	}

	// 内联 CSS：匹配 <link ... href="..." rel="stylesheet" .../> 无论属性顺序和引号类型
	html = inlineFileRefs(html, baseDir, inlineCSSRe, func(match string) string {
		href := extractAttrValue(match, "href")
		if href == "" {
			return "<!-- quickdock: empty css href -->"
		}
		var data []byte
		var err error
		// ../common.css 实际位于 builtin 共享目录，需从 PluginsDir/builtin 读取
		if strings.HasSuffix(href, "common.css") {
			data, err = os.ReadFile(filepath.Join(a.PluginsDir, "builtin", "common.css"))
		} else {
			data, err = os.ReadFile(filepath.Join(baseDir, href))
		}
		if err != nil {
			return "<!-- quickdock: css inline failed: " + href + " -->"
		}
		return "<style>\n" + string(data) + "\n</style>"
	})

	// 内联 JS：匹配 <script ... src="..." ...></script>
	html = inlineFileRefs(html, baseDir, inlineJSRe, func(match string) string {
		src := extractAttrValue(match, "src")
		if src == "" {
			return "<!-- quickdock: empty js src -->"
		}
		var data []byte
		var err error
		// ../common.js 同样位于 builtin 共享目录
		if strings.HasSuffix(src, "common.js") {
			data, err = os.ReadFile(filepath.Join(a.PluginsDir, "builtin", "common.js"))
		} else {
			data, err = os.ReadFile(filepath.Join(baseDir, src))
		}
		if err != nil {
			return "<!-- quickdock: js inline failed: " + src + " -->"
		}
		return "<script>\n" + string(data) + "\n</script>"
	})

	// 写入缓存
	a.frontendCacheMu.Lock()
	a.frontendCache[pluginID] = &frontendCacheEntry{html: html, htmlMtime: fi.ModTime(), commonMtime: commonMtime}
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

// extractAttrValue 从 HTML 标签中提取指定属性的值（支持单引号和双引号）。
// 注意：一个标签可能有多个属性（如 <link rel="stylesheet" href="x.css">），
// 必须用 FindAllStringSubmatch 遍历所有匹配，返回属性名等于 attrName 的那一个，
// 不能只取第一个匹配（否则会错把 rel 当成 href 导致提取为空）。
func extractAttrValue(tag, attrName string) string {
	for _, m := range attrDblRe.FindAllStringSubmatch(tag, -1) {
		if len(m) >= 3 && m[1] == attrName {
			return m[2]
		}
	}
	for _, m := range attrSglRe.FindAllStringSubmatch(tag, -1) {
		if len(m) >= 3 && m[1] == attrName {
			return m[2]
		}
	}
	return ""
}

// base64Encode 辅助函数
func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// ===== 插件独立窗口 =====
// 插件前端页面在独立窗口中打开（使用 iframe 嵌入）

// ShowPluginWindow 打开插件独立窗口（每个插件拥有自己的独立窗口）
func (a *AppService) ShowPluginWindow(pluginID string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	inst := a.PluginMgr.GetPlugin(pluginID)
	if inst == nil {
		return FailMsg("插件未加载")
	}
	if inst.Status != "running" {
		return FailMsg("插件未运行，无法打开独立窗口")
	}
	if !inst.Manifest.Frontend.Enabled {
		return FailMsg("插件未启用前端")
	}
	if a.PluginWindowMgr == nil {
		return FailMsg("plugin window manager not initialized")
	}

	// 以独立窗口显示（任务栏可见，仅能通过 X 关闭）
	a.PluginWindowMgr.ShowAsWindow(pluginID, inst.Manifest.Name)

	// 检查是否有待传递的初始文本 + 子命令，注入到窗口 iframe
	a.pendingInitTextMu.Lock()
	initText := a.pendingInitText
	initCommand := a.pendingInitCommand
	a.pendingInitText = ""
	a.pendingInitCommand = ""
	a.pendingInitTextMu.Unlock()
	if initText != "" {
		a.PluginWindowMgr.InjectInitText(pluginID, initText, initCommand)
	}

	return Ok(nil)
}

// SetPendingPluginInit 设置待传递给插件窗口的初始文本 + 命中的子命令
// （从命令面板→插件窗口跨窗口传递，便于插件进入后默认选中对应功能并回显文字）
func (a *AppService) SetPendingPluginInit(text string, command string) *ApiResult {
	a.pendingInitTextMu.Lock()
	a.pendingInitText = text
	a.pendingInitCommand = command
	a.pendingInitTextMu.Unlock()
	return Ok(nil)
}

// GetAndClearPendingPluginInit 获取并清除待传递的初始文本与子命令
func (a *AppService) GetAndClearPendingPluginInit() *ApiResult {
	a.pendingInitTextMu.Lock()
	text := a.pendingInitText
	command := a.pendingInitCommand
	a.pendingInitText = ""
	a.pendingInitTextMu.Unlock()
	if text == "" && command == "" {
		return Ok(nil)
	}
	return Ok(map[string]string{"text": text, "command": command})
}

// MinimizePluginWindow 最小化指定插件的独立窗口
func (a *AppService) MinimizePluginWindow(pluginID string) *ApiResult {
	if a.PluginWindowMgr != nil {
		a.PluginWindowMgr.Minimize(pluginID)
	}
	return Ok(nil)
}

// ToggleMaximizePluginWindow 切换指定插件的窗口最大化/还原
func (a *AppService) ToggleMaximizePluginWindow(pluginID string) *ApiResult {
	if a.PluginWindowMgr != nil {
		a.PluginWindowMgr.ToggleMaximize(pluginID)
	}
	return Ok(nil)
}

// HidePluginWindow 关闭并销毁指定插件的独立窗口
func (a *AppService) HidePluginWindow(pluginID string) *ApiResult {
	if a.PluginWindowMgr != nil {
		a.PluginWindowMgr.Hide(pluginID)
	}
	return Ok(nil)
}
