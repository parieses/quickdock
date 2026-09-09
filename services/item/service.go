// Package item 项目（场景内条目）门面服务。
package item

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
	"quickdock/services"
)

// ItemService 项目 CRUD 绑定服务。
type ItemService struct {
	App *services.AppService
}

// NewItemService 创建 ItemService。
func NewItemService(app *services.AppService) *ItemService {
	return &ItemService{App: app}
}

func (s *ItemService) dbOK() *services.ApiResult {
	if s.App.DB == nil {
		logger.E("QuickDock: database not initialized")
		return services.FailMsg("database not initialized")
	}
	return nil
}

// GetPathQuickInfo 返回路径的文件系统元数据（命令面板 QuickLook 预览用）：
// 存在性 / 目录标志 / 大小 / 修改时间。路径不存在或入参为空时 exists=false，不报错。
func (s *ItemService) GetPathQuickInfo(path string) *services.ApiResult {
	path = strings.TrimSpace(path)
	if path == "" {
		return services.Ok(map[string]any{"exists": false})
	}
	info, err := os.Stat(path)
	if err != nil {
		return services.Ok(map[string]any{"exists": false, "path": path})
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
	return services.Ok(m)
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

// ListItems 列出集合内全部项。
func (s *ItemService) ListItems(collectionID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.ListItems(collectionID)
	return services.Wrap(data, err)
}

// CreateItem 新建项。
func (s *ItemService) CreateItem(workspaceID, collectionID, name, itemType, value string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.CreateItem(workspaceID, collectionID, name, itemType, value)
	return services.Wrap(data, err)
}

// UpdateItem 更新项。
func (s *ItemService) UpdateItem(id string, updates map[string]interface{}) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateItem(id, updates); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DeleteItem 删除项。
func (s *ItemService) DeleteItem(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteItem(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ReorderItems 排序项。
func (s *ItemService) ReorderItems(orderedIDs []string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.Reorder("items", orderedIDs); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
