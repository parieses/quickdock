// Package collection 集合门面服务。
package collection

import (
	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/services"
)

// CollectionService 集合 CRUD 绑定服务。
type CollectionService struct {
	App *services.AppService
}

// NewCollectionService 创建 CollectionService。
func NewCollectionService(app *services.AppService) *CollectionService {
	return &CollectionService{App: app}
}

func (s *CollectionService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// ListCollections 列出某场景下的全部集合。
func (s *CollectionService) ListCollections(sceneID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.ListCollections(sceneID)
	return services.Wrap(data, err)
}

// CreateCollection 新建集合。
func (s *CollectionService) CreateCollection(workspaceID, sceneID, name, collType, openStrategy, group string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.CreateCollection(workspaceID, sceneID, name, collType, openStrategy, group)
	return services.Wrap(data, err)
}

// UpdateCollection 更新集合。
func (s *CollectionService) UpdateCollection(id string, updates map[string]interface{}) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateCollection(id, updates); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DeleteCollection 删除集合。
func (s *CollectionService) DeleteCollection(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteCollection(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ReorderCollections 排序集合。
func (s *CollectionService) ReorderCollections(orderedIDs []string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.Reorder("collections", orderedIDs); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// OpenItem 按 id 打开集合内单项。
//
// 签名只收 id（不再收整个 CollectionItem）：条目内容一律由后端按 id 从库里读，
// 调用方无法通过构造 type/value 让宿主任意执行命令。见 db.OpenItem 的注释。
func (s *CollectionService) OpenItem(id string) *services.ApiResult {
	return s.open(id, "")
}

// OpenItemWithQuery 打开条目，并把条目模板里的 {query} 换成命令面板的当前输入。
//
// 单独一个方法而不是给 OpenItem 加可选参数：Wails 绑定没有「可选参数」，
// 加个空串参数会让所有调用点都被迫传值，也模糊了「默认路径只认 id」这件事。
// query 只做占位符替换，命令/类型/参数仍然全部来自库里那一行。
func (s *CollectionService) OpenItemWithQuery(id, query string) *services.ApiResult {
	return s.open(id, query)
}

func (s *CollectionService) open(id, query string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	item, err := s.App.DB.OpenItem(id, query)
	if err != nil {
		// Fail() 只带 err 与调用点，排障时看不出是哪个条目打不开；
		// 这里补一条带条目身份的 W（不记 Value，可能是含敏感信息的命令/URL）。
		name, itemID := "", id
		if item != nil {
			name, itemID = item.Name, item.ID
		}
		logger.W("打开条目失败 name=%q id=%s: %v", name, itemID, err)
		return services.Fail(err)
	}
	// 操作流水：只在真正打开成功后记一次（db 层还有冷却去重，连点不会刷屏）
	s.App.NoteActivity(db.ActivityItemOpened, item.ID, item.Name)
	return services.Ok(nil)
}

// OpenAllInCollection 打开集合内全部项。
func (s *CollectionService) OpenAllInCollection(collectionID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.OpenAllInCollection(collectionID); err != nil {
		logger.W("批量打开集合失败 collection=%s: %v", collectionID, err)
		return services.Fail(err)
	}
	return services.Ok(nil)
}
