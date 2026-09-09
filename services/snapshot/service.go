// Package snapshot 快照备份门面服务。
package snapshot

import (
	"quickdock/internal/logger"
	"quickdock/services"
)

// SnapshotService 快照备份绑定服务。
type SnapshotService struct {
	App *services.AppService
}

// NewSnapshotService 创建 SnapshotService。
func NewSnapshotService(app *services.AppService) *SnapshotService {
	return &SnapshotService{App: app}
}

func (s *SnapshotService) dbOK() *services.ApiResult {
	if s.App.DB == nil {
		logger.E("QuickDock: database not initialized")
		return services.FailMsg("database not initialized")
	}
	return nil
}

// CreateSnapshot 创建全量快照。
func (s *SnapshotService) CreateSnapshot(label, note string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.CreateFullSnapshot(label, note)
	return services.Wrap(data, err)
}

// ListSnapshots 列出全部快照。
func (s *SnapshotService) ListSnapshots() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.ListSnapshots()
	return services.Wrap(data, err)
}

// GetSnapshot 获取单个快照详情。
func (s *SnapshotService) GetSnapshot(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.GetSnapshot(id)
	return services.Wrap(data, err)
}

// DeleteSnapshot 删除快照。
func (s *SnapshotService) DeleteSnapshot(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteSnapshot(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// RestoreSnapshot 恢复快照。
func (s *SnapshotService) RestoreSnapshot(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.RestoreSnapshot(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
