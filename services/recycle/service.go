// Package recycle 回收站（删除墓碑）门面服务。
//
// 误删的项目/集合/场景/笔记在这里找回：删除时 db 层已把整行
// （级联删除时整棵子树）拍进 tombstones，本服务负责列表/恢复/
// 彻底删除/清空。恢复走 BulkInsert 原样写回（id 不变）。
package recycle

import (
	"sort"

	"quickdock/internal/db"
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
	Collection string `json:"collection"` // tombstones 的 8 类 + monitors（软删标记，不写墓碑）
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
	// 监控不写墓碑：它用 monitors.deleted_at 软删并保留日志，恢复只清标记。
	// 在这里映射成同一种条目，对外仍然只有一个回收站入口。
	mons, err := s.App.DB.ListDeletedMonitors()
	if err != nil {
		return services.Fail(err)
	}
	for _, m := range mons {
		out = append(out, RecycleEntry{Collection: db.RecycleMonitor, ID: m.ID, Label: m.Name, DeletedAt: m.DeletedAt})
	}
	// 两处来源各自有序，合并后要重排一次，否则刚删的监控会沉到墓碑列表下面。
	sort.SliceStable(out, func(i, j int) bool { return out[i].DeletedAt > out[j].DeletedAt })
	return services.Ok(out)
}

// RestoreRecycle 恢复一条：原样写回各行（含级联子树），成功后移除墓碑。
//
// 定时任务额外走 AppService.RescheduleAfterRestore：墓碑里的 next_run 可能早已过期，
// 不重排会在恢复那一刻被调度循环当成到期任务立刻跑一次。
func (s *RecycleService) RestoreRecycle(collection, id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	// 监控没有墓碑可写回：清掉软删标记即可，日志与运行数据本来就在原表里。
	// 恢复后必须唤醒检查器，否则要等下一个 interval 才会重新探测。
	if collection == db.RecycleMonitor {
		if err := s.App.DB.RestoreMonitor(id); err != nil {
			return services.Fail(err)
		}
		s.App.WakeMonitorChecker()
		return services.Ok(nil)
	}
	if err := s.App.DB.RestoreTombstone(collection, id); err != nil {
		return services.Fail(err)
	}
	if collection == "schedules" {
		s.App.RescheduleAfterRestore(id)
	}
	return services.Ok(nil)
}

// PurgeRecycle 彻底删除一条墓碑（不可恢复）。
func (s *RecycleService) PurgeRecycle(collection, id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	// 彻底删除监控 = 删 monitors 行 + 它的全部日志，别留下孤儿日志行。
	if collection == db.RecycleMonitor {
		if err := s.App.DB.DeleteMonitor(id); err != nil {
			return services.Fail(err)
		}
		s.App.WakeMonitorChecker()
		return services.Ok(nil)
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
	// 软删监控不占墓碑行，得单独清一遍，否则「清空回收站」名不副实。
	if _, err := s.App.DB.PurgeDeletedMonitors(); err != nil {
		return services.Fail(err)
	}
	s.App.WakeMonitorChecker()
	return services.Ok(nil)
}
