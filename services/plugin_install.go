package services

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"quickdock/internal/platform"
	"quickdock/internal/plugin"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// dialogParentWindow 为原生文件/目录对话框挑选父窗口：
//  1. 命令面板使用中（PaletteMode=true）→ 面板窗口。面板是 AlwaysOnTop，
//     无父对话框会跑到所有窗口后面（用户反馈的「弹框在最后面」），
//     绑定父窗口后对话框作为 owned 窗口始终显示在面板之上。
//     （面板打开时窗口必然已创建，工厂调用无副作用）
//  2. 否则优先当前聚焦的插件窗口（插件独立窗口场景）。
//  3. 兜底主窗口。
func (a *AppService) dialogParentWindow() *application.WebviewWindow {
	if a.PaletteMode != nil && a.PaletteMode.Load() {
		if fn := a.GetPaletteWindow; fn != nil {
			if w := fn(); w != nil {
				return w
			}
		}
	}
	if a.PluginWindowMgr != nil {
		if w := a.PluginWindowMgr.FocusedWindow(); w != nil {
			return w
		}
	}
	return a.MainWindow
}

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
	} else {
		// 安装 / 更新成功 → 标记「新」角标（首次打开后由前端清除）
		a.MarkPluginNew(manifest.ID)
	}
	return Ok(map[string]interface{}{
		"id":      manifest.ID,
		"name":    manifest.Name,
		"version": manifest.Version,
		"dir":     dir,
	})
}

// PickFilePath 打开原生文件选择对话框，返回所选路径（取消返回 null）。
// 供插件桥接 qdPickFile 使用：插件 iframe 内的 <input type=file> 受沙箱限制
// 且会触发宿主窗口失焦问题，统一引导走原生对话框。
func (a *AppService) PickFilePath(title, filterName, pattern string) *ApiResult {
	if a.app == nil {
		return FailMsg("应用未初始化")
	}
	if title == "" {
		title = "选择文件"
	}
	if filterName == "" || pattern == "" {
		filterName, pattern = "所有文件", "*.*"
	}
	filePath, err := a.app.Dialog.OpenFile().
		SetTitle(title).
		AddFilter(filterName, pattern).
		AttachToWindow(a.dialogParentWindow()).
		PromptForSingleSelection()
	if err != nil || filePath == "" {
		return Ok(nil)
	}
	return Ok(filePath)
}

// PickFolderPath 打开原生目录选择对话框，返回所选目录路径（取消返回 null）。
// 供插件桥接 qdPickFolder 使用：复用 Wails v3 OpenFile 的 CanChooseDirectories(true)，
// 走与 qdPickFile 一致的原生对话框，规避插件自行 spawn PowerShell/AppleScript 的脆弱实现。
func (a *AppService) PickFolderPath(title string) *ApiResult {
	if a.app == nil {
		return FailMsg("应用未初始化")
	}
	if title == "" {
		title = "选择目录"
	}
	folderPath, err := a.app.Dialog.OpenFile().
		SetTitle(title).
		CanChooseDirectories(true).
		CanChooseFiles(false).
		AttachToWindow(a.dialogParentWindow()).
		PromptForSingleSelection()
	if err != nil || folderPath == "" {
		return Ok(nil)
	}
	return Ok(folderPath)
}

// pickedFileMaxSize 单个文件读取上限：插件场景（配置/文本/二维码图）足够，
// 同时防住超大文件把 WebView2 桥接消息撑爆。
const pickedFileMaxSize = 20 << 20

// ReadPickedFile 读取插件经 qdPickFile 选中的文件，返回内容载荷：
//   - 可无损 UTF-8 解码且不含 NUL 的文件 → {"type":"text","content":原文}
//   - 其余（图片/二进制）→ {"type":"dataurl","content":"data:<mime>;base64,..."}
//
// 仅供桥接 qdReadFile 使用；路径须为绝对路径且不带 URL scheme。
// 信任边界说明：external 插件为本仓库第一方构建产物，与宿主同信任级；
// 若未来引入第三方市场分发，此绑定必须改为按“本次会话 qdPickFile 返回的路径”白名单校验。
func (a *AppService) ReadPickedFile(path string) *ApiResult {
	if a.app == nil {
		return FailMsg("应用未初始化")
	}
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || strings.Contains(path, "://") {
		return FailMsg("路径无效")
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return FailMsg("文件不存在")
	}
	if fi.Size() > pickedFileMaxSize {
		return FailMsg("文件超过 20MB 上限")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FailMsg("读取失败")
	}
	ext := strings.ToLower(filepath.Ext(path))
	imageExts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".bmp": true, ".ico": true,
	}
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	textual := !imageExts[ext] && !bytes.Contains(probe, []byte{0}) && utf8.Valid(data)
	if textual {
		return Ok(map[string]string{"type": "text", "content": string(data)})
	}
	m := mime.TypeByExtension(ext)
	if m == "" {
		m = http.DetectContentType(data)
	}
	return Ok(map[string]string{"type": "dataurl", "content": "data:" + m + ";base64," + base64.StdEncoding.EncodeToString(data)})
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
		AttachToWindow(a.dialogParentWindow()).
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
	// 写入临时文件。fileName 来自前端，仅取 Base 防路径穿越（..\..\ 逃逸临时目录）
	baseName := filepath.Base(fileName)
	if baseName == "" || baseName == "." || baseName == string(filepath.Separator) {
		return FailMsg("非法的文件名")
	}
	tmpDir := filepath.Join(os.TempDir(), "quickdock-plugin-install")
	os.MkdirAll(tmpDir, 0755)
	tmpPath := filepath.Join(tmpDir, baseName)
	if err := os.WriteFile(tmpPath, fileData, 0644); err != nil {
		return Fail(fmt.Errorf("写入临时文件失败: %w", err))
	}
	defer os.Remove(tmpPath)

	// 调用标准的 InstallFromZip
	return a.InstallPlugin(tmpPath)
}
