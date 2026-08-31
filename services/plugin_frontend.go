package services

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"quickdock/internal/platform"
)

// 预编译的正则表达式（用于插件前端 HTML 内联）
var (
	inlineCSSRe = regexp.MustCompile(`<link\s[^>]*?(?:rel="stylesheet"|rel='stylesheet')[^>]*?>`)
	inlineJSRe  = regexp.MustCompile(`<script[^>]*src\s*=\s*["'][^"']*["'][^>]*>`)
	attrDblRe   = regexp.MustCompile(`([\w-]+)\s*=\s*"([^"]*)"`)
	attrSglRe   = regexp.MustCompile(`([\w-]+)\s*=\s*'([^']*)'`)
)

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
	// 防路径穿越：manifest 声明的图标必须落在插件目录内
	iconPath, err := safePluginPath(inst.Dir, inst.Manifest.Icon)
	if err != nil {
		return Fail(fmt.Errorf("图标路径非法: %w", err))
	}
	const maxIconSize = 2 << 20 // 2MB
	if fi, serr := os.Stat(iconPath); serr == nil && fi.Size() > maxIconSize {
		return Fail(fmt.Errorf("图标文件过大 (%d bytes)", fi.Size()))
	}
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
func (a *AppService) GetPluginFrontendPage(pluginID string, theme string, locale string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	inst := a.PluginMgr.GetPlugin(pluginID)
	if inst == nil {
		return FailMsg("插件未加载")
	}
	if inst.GetStatus() != "running" {
		return FailMsg("插件未运行，无法打开前端页面")
	}
	if !inst.Manifest.Frontend.Enabled {
		return FailMsg("插件未启用前端")
	}
	// 防路径穿越：manifest 声明的前端入口必须落在插件目录内
	entryPath, err := safePluginPath(inst.Dir, inst.Manifest.Frontend.Entry)
	if err != nil {
		return Fail(fmt.Errorf("前端入口路径非法: %w", err))
	}

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

	// 强制注入 common.css（QuickDock 插件通用主题）
	commonCSSPath := filepath.Join(a.PluginsDir, "builtin", "common.css")
	if commonData, err := os.ReadFile(commonCSSPath); err == nil {
		commonStyle := "<style id=\"quickdock-common-css\">\n" + string(commonData) + "\n</style>\n"
		if idx := strings.Index(html, "<head>"); idx >= 0 {
			html = html[:idx+6] + "\n" + commonStyle + html[idx+6:]
		} else {
			html = commonStyle + html
		}
	}

	// 注入 common.js
	commonJSPath := filepath.Join(a.PluginsDir, "builtin", "common.js")
	if commonJSData, err := os.ReadFile(commonJSPath); err == nil {
		commonJS := "<script id=\"quickdock-common-js\">\n" + string(commonJSData) + "\n</script>\n"
		if idx := strings.Index(html, "<head>"); idx >= 0 {
			html = html[:idx+6] + "\n" + commonJS + html[idx+6:]
		} else {
			html = commonJS + html
		}
	}

	// 注入 QuickDock 运行时脚本
	safeTheme := "dark"
	if theme == "light" {
		safeTheme = "light"
	}
	safeLocale := locale
	if safeLocale == "" {
		safeLocale = "zh-CN"
	}
	runtimeScript := "<script id=\"quickdock-runtime\">\n" +
		"(function(){" +
		"var t='" + safeTheme + "';" +
		"var l='" + safeLocale + "';" +
		"document.documentElement.setAttribute('data-theme',t);" +
		"document.documentElement.setAttribute('lang',l);" +
		"window.addEventListener('message',function(e){" +
		"if(e.data&&e.data.type==='plugin:theme'){" +
		"var dt=(e.data.data&&e.data.data.theme)||e.data.theme;" +
		"var dl=(e.data.data&&e.data.data.locale)||e.data.locale;" +
		"if(dt)document.documentElement.setAttribute('data-theme',dt);" +
		"if(dl)document.documentElement.setAttribute('lang',dl);" +
		"}});" +
		"})();" +
		"</script>\n"
	if idx := strings.Index(html, "</head>"); idx >= 0 {
		html = html[:idx] + runtimeScript + html[idx:]
	} else {
		html += runtimeScript
	}

	// 内联 CSS
	html = inlineFileRefs(html, baseDir, inlineCSSRe, func(match string) string {
		href := extractAttrValue(match, "href")
		if href == "" {
			return "<!-- quickdock: empty css href -->"
		}
		var data []byte
		var err error
		if strings.HasSuffix(href, "common.css") {
			data, err = os.ReadFile(filepath.Join(a.PluginsDir, "builtin", "common.css"))
		} else {
			var p string
			if p, err = safeInlinePath(baseDir, href); err == nil {
				data, err = os.ReadFile(p)
			}
		}
		if err != nil {
			return "<!-- quickdock: css inline failed: " + href + " -->"
		}
		return "<style>\n" + string(data) + "\n</style>"
	})

	// 内联 JS
	html = inlineFileRefs(html, baseDir, inlineJSRe, func(match string) string {
		src := extractAttrValue(match, "src")
		if src == "" {
			return "<!-- quickdock: empty js src -->"
		}
		var data []byte
		var err error
		if strings.HasSuffix(src, "common.js") {
			data, err = os.ReadFile(filepath.Join(a.PluginsDir, "builtin", "common.js"))
		} else {
			var p string
			if p, err = safeInlinePath(baseDir, src); err == nil {
				data, err = os.ReadFile(p)
			}
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
func inlineFileRefs(html, baseDir string, re *regexp.Regexp, loader func(string) string) string {
	return re.ReplaceAllStringFunc(html, func(match string) string {
		inlined := loader(match)
		if inlined == "" {
			return match
		}
		return inlined
	})
}

// safePluginPath 解析插件清单中声明的相对路径（图标/前端入口），强制落在插件目录内，
// 防止 icon/entry 声明为 "../../secret" 等路径穿越读取宿主任意文件。
func safePluginPath(pluginDir, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("资源路径为空")
	}
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") {
		return "", fmt.Errorf("资源路径不支持 URL: %s", ref)
	}
	cleanDir := filepath.Clean(pluginDir)
	p := filepath.Clean(filepath.Join(cleanDir, ref))
	if p != cleanDir && !strings.HasPrefix(p, cleanDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("资源路径越出插件目录: %s", ref)
	}
	return p, nil
}

// safeInlinePath 解析插件页面内引用的相对资源路径，并强制其落在插件目录内。
// 防 href/src 形如 "../../" 的路径穿越：否则恶意插件可让宿主读取任意本地文件、
// 内联进自己的 iframe 页面后经 postMessage 外传（backend.entry 已有 safePluginEntry，
// 前端资源此前缺失同等防护）。仅接受本地相对路径，拒绝 URL/协议前缀形式。
func safeInlinePath(baseDir, ref string) (string, error) {
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") {
		return "", fmt.Errorf("内联资源不支持 URL 引用: %s", ref)
	}
	p := filepath.Clean(filepath.Join(baseDir, ref))
	if !strings.HasPrefix(p, filepath.Clean(baseDir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("资源路径越出插件目录: %s", ref)
	}
	return p, nil
}

// extractAttrValue 从 HTML 标签中提取指定属性的值
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
