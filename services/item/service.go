// Package item 项目（场景内条目）门面服务。
package item

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"quickdock/internal/fsguard"
	"quickdock/internal/fsview"
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
	return services.CheckDB(s.App.DB)
}

// itemsChanged 条目变更后让文件白名单缓存立刻失效。
//
// 不这么做的话「刚把一个目录加为快捷项、紧接着让 AI 读它」会撞上 fsguard 的 3 秒
// 缓存窗口，报「不在可读范围内」——用户刚做完授权动作，这时候最没耐心重试。
func itemsChanged() { fsguard.InvalidateRoots() }

// GetPathQuickInfo 返回路径的文件系统元数据：
// 存在性 / 目录标志 / 大小 / 修改时间。路径不存在或入参为空时 exists=false，不报错。
//
// 命令面板已改用 PreviewPath —— 它的载荷里带了同样的元数据，一次 IPC 就够；
// 本方法留给「只要元数据、不想读文件内容」的调用方（读内容对大文件是有代价的）。
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

// 预览引擎与媒体服务都是进程级单例：媒体服务要占用一个端口，不能每次预览都新开。
// 打包成 once 是因为第一个用它的可能是任意一次面板选择 —— 不该等到启动时才有。
var (
	previewEngineOnce sync.Once
	previewEngine     *fsview.Viewer
)

func getPreviewEngine() *fsview.Viewer {
	previewEngineOnce.Do(func() {
		srv, err := fsview.StartMediaServer()
		if err != nil {
			// 端口都占不到时不让预览整体失效：文本/代码/压缩包列表仍然可用，
			// 只有图片/PDF/音视频退化成「显示元数据」。
			logger.W("[item] 预览媒体服务启动失败，二进制类预览将只显示元数据: %v", err)
			previewEngine = fsview.New(nil)
			return
		}
		previewEngine = fsview.New(srv.URL)
		// 图片走服务端缩略：面板里只显示两三百像素高，没必要让 WebView 解原图。
		previewEngine.SetThumbURL(srv.ThumbURL)
	})
	return previewEngine
}

// PreviewPath 返回一个路径的可预览载荷（命令面板 QuickLook 预览）。
//
// 与 GetPathQuickInfo 的分工：那个只给「存在/大小/修改时间」这类元数据，够列表用；
// 这个真的把内容读出来，只在需要展示正文时才调，所以它更重、也必须带截断。
func (s *ItemService) PreviewPath(path string) *services.ApiResult {
	return services.Ok(getPreviewEngine().Preview(path))
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
	itemsChanged()
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
	itemsChanged()
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
	itemsChanged()
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
