// Package recycle 回收站（删除墓碑）门面服务。
//
// 误删的项目/集合/场景/笔记在这里找回：删除时 db 层已把整行
// （级联删除时整棵子树）拍进 tombstones，本服务负责列表/恢复/
// 彻底删除/清空。恢复走 BulkInsert 原样写回（id 不变）。
package recycle

import (
	"quickdock/services"
)

// RecycleService 回收站领域方法。
type RecycleService struct {
	App *services.AppService
}

// NewRecycleService 创建回收站门面服务。
func NewRecycleService(app *services.AppService) *RecycleService {
	return &RecycleService{App: app}
}

func (s *RecycleService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// RecycleEntry 回收站条目（与 db.Tombstone 同形，小写 json tag）。
type RecycleEntry struct {
	Collection string `json:"collection"` // workspaces | scenes | collections | items | notes
	ID         string `json:"id"`
	Label      string `json:"label"`
	DeletedAt  string `json:"deletedAt"`
}

// ListRecycle 回收站全部条目（新的在前）。
func (s *RecycleService) ListRecycle() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListTombstones()
	if err != nil {
		return services.Fail(err)
	}
	out := make([]RecycleEntry, 0, len(list))
	for _, t := range list {
		out = append(out, RecycleEntry{Collection: t.Collection, ID: t.ID, Label: t.Label, DeletedAt: t.DeletedAt})
	}
	return services.Ok(out)
}

// RestoreRecycle 恢复一条：原样写回各行（含级联子树），成功后移除墓碑。
func (s *RecycleService) RestoreRecycle(collection, id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.RestoreTombstone(collection, id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// PurgeRecycle 彻底删除一条墓碑（不可恢复）。
func (s *RecycleService) PurgeRecycle(collection, id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteTombstone(collection, id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ClearRecycle 清空回收站（全部墓碑）。
func (s *RecycleService) ClearRecycle() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.ClearTombstones(); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
