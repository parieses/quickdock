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
func (s *CollectionService) CreateCollection(workspaceID, sceneID, name, collType, openStrategy string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.CreateCollection(workspaceID, sceneID, name, collType, openStrategy)
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

// OpenItem 打开集合内单项。
func (s *CollectionService) OpenItem(item db.CollectionItem) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.OpenItem(&item); err != nil {
		// Fail() 只带 err 与调用点，排障时看不出是哪个条目打不开；
		// 这里补一条带条目身份的 W（不记 Value，可能是含敏感信息的命令/URL）。
		logger.W("打开条目失败 name=%q type=%s id=%s: %v", item.Name, item.Type, item.ID, err)
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
