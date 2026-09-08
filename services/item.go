package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ===== 项目 =====

// GetPathQuickInfo 返回路径的文件系统元数据（命令面板 QuickLook 预览用）：
// 存在性 / 目录标志 / 大小 / 修改时间。路径不存在或入参为空时 exists=false，不报错。
func (a *AppService) GetPathQuickInfo(path string) *ApiResult {
	path = strings.TrimSpace(path)
	if path == "" {
		return Ok(map[string]any{"exists": false})
	}
	info, err := os.Stat(path)
	if err != nil {
		return Ok(map[string]any{"exists": false, "path": path})
	}
	m := map[string]any{
		"exists":   true,
		"isDir":    info.IsDir(),
		"path":     path,
		"name":     filepath.Base(path),
		"size":     info.Size(),
		"sizeText": formatSize(info.Size()),
		"modified": info.ModTime().Format("2006-01-02 15:04:05"),
	}
	return Ok(m)
}

// formatSize 把字节数格式化为人类可读文本（B/KB/MB/GB/TB）。
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (a *AppService) ListItems(collectionID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListItems(collectionID)
	return wrap(data, err)
}

func (a *AppService) CreateItem(workspaceID, collectionID, name, itemType, value string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateItem(workspaceID, collectionID, name, itemType, value)
	return wrap(data, err)
}

func (a *AppService) UpdateItem(id string, updates map[string]interface{}) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateItem(id, updates); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) DeleteItem(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteItem(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) ReorderItems(orderedIDs []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.Reorder("items", orderedIDs); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
