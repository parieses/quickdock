// Package scene 场景门面服务。
package scene

import (
	"quickdock/internal/logger"
	"quickdock/services"
)

// SceneService 场景 CRUD 绑定服务。
type SceneService struct {
	App *services.AppService
}

// NewSceneService 创建 SceneService。
func NewSceneService(app *services.AppService) *SceneService {
	return &SceneService{App: app}
}

func (s *SceneService) dbOK() *services.ApiResult {
	if s.App.DB == nil {
		logger.E("QuickDock: database not initialized")
		return services.FailMsg("database not initialized")
	}
	return nil
}

// ListScenes 列出某工作空间下的全部场景。
func (s *SceneService) ListScenes(workspaceID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.ListScenes(workspaceID)
	return services.Wrap(data, err)
}

// CreateScene 新建场景。
func (s *SceneService) CreateScene(workspaceID, name, sceneType string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.CreateScene(workspaceID, name, sceneType)
	return services.Wrap(data, err)
}

// UpdateScene 更新场景。
func (s *SceneService) UpdateScene(id string, updates map[string]interface{}) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateScene(id, updates); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DeleteScene 删除场景。
func (s *SceneService) DeleteScene(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteScene(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ReorderScenes 排序场景。
func (s *SceneService) ReorderScenes(orderedIDs []string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.Reorder("scenes", orderedIDs); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
