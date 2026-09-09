// Package workspace 工作空间服务
// 承载原 AppService 的工作空间领域方法（列表/增删改/排序）。
package workspace

import (
	"quickdock/services"
)

// WorkspaceService 承载工作空间领域方法。
// App 回指宿主 AppService：DB 为宿主导出字段，本包仅经 App 读取。
type WorkspaceService struct {
	App *services.AppService
}

// NewWorkspaceService 创建工作空间门面服务，App 为宿主服务引用。
func NewWorkspaceService(app *services.AppService) *WorkspaceService {
	return &WorkspaceService{App: app}
}

// dbOK 检查宿主 DB 是否就绪（原 AppService.dbOK 的包内副本，跨包不可访问宿主私有方法）
func (s *WorkspaceService) dbOK() *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("database not initialized")
	}
	return nil
}

// ===== 工作空间 =====

// ListWorkspaces 列出所有工作空间
func (s *WorkspaceService) ListWorkspaces() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.ListWorkspaces()
	return services.Wrap(data, err)
}

// CreateWorkspace 新建工作空间
func (s *WorkspaceService) CreateWorkspace(name string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.CreateWorkspace(name)
	return services.Wrap(data, err)
}

// DeleteWorkspace 删除工作空间（默认工作空间不允许删除）
func (s *WorkspaceService) DeleteWorkspace(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	ws, err := s.App.DB.GetWorkspace(id)
	if err != nil {
		return services.Fail(err)
	}
	if ws.Name == services.DefaultWorkspaceName {
		return services.FailMsg("默认工作空间不允许删除")
	}
	if err := s.App.DB.DeleteWorkspace(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// UpdateWorkspace 更新工作空间名称
func (s *WorkspaceService) UpdateWorkspace(id, name string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateWorkspace(id, name); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// GetWorkspace 获取单个工作空间
func (s *WorkspaceService) GetWorkspace(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.GetWorkspace(id)
	return services.Wrap(data, err)
}

// ReorderWorkspaces 工作空间排序
func (s *WorkspaceService) ReorderWorkspaces(orderedIDs []string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.Reorder("workspaces", orderedIDs); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
