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
	if s.App.DB == nil {
		logger.E("QuickDock: database not initialized")
		return services.FailMsg("database not initialized")
	}
	return nil
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
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// OpenAllInCollection 打开集合内全部项。
func (s *CollectionService) OpenAllInCollection(collectionID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.OpenAllInCollection(collectionID); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
