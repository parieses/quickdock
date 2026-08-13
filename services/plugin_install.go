package services

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"quickdock/internal/platform"
	"quickdock/internal/plugin"
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
