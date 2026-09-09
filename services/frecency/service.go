// Package frecency 使用频率（frecency）追踪门面服务。
package frecency

import (
	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/services"
)

// FrecencyService frecency 追踪绑定服务。
type FrecencyService struct {
	App *services.AppService
}

// NewFrecencyService 创建 FrecencyService。
func NewFrecencyService(app *services.AppService) *FrecencyService {
	return &FrecencyService{App: app}
}

func (s *FrecencyService) dbOK() *services.ApiResult {
	if s.App.DB == nil {
		logger.E("QuickDock: database not initialized")
		return services.FailMsg("database not initialized")
	}
	return nil
}

// usageResult 将 DB 查询结果包装为 ApiResult，消除重复的 err/nil/Ok 模板。
func (s *FrecencyService) usageResult(entries []db.FrecencyEntry, err error) *services.ApiResult {
	if err != nil {
		return services.Fail(err)
	}
	if entries == nil {
		entries = []db.FrecencyEntry{}
	}
	return services.Ok(entries)
}

// RecordUsage 记录一次使用（跨窗口共享的 frecency 追踪）。
// key 格式：item:{id} | note:{id} | app:{name} | plugin:{pluginId}.{cmdId}。
func (s *FrecencyService) RecordUsage(key string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.RecordUsage(key); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// RecordUsageEx 记录一次使用并附带展示信息（type/label/desc/input 用于「最近使用」直接展示与回放）。
func (s *FrecencyService) RecordUsageEx(key, type_, label, desc, input string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.RecordUsageEx(key, type_, label, desc, input); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// GetAllUsage 返回全部 frecency 记录（前端初始化一次性加载）。
func (s *FrecencyService) GetAllUsage() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	return s.usageResult(s.App.DB.GetAllUsage())
}

// GetRecentUsage 返回最近使用的 N 条记录（命令面板「最近使用」专用）。
func (s *FrecencyService) GetRecentUsage(limit int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	return s.usageResult(s.App.DB.GetRecentUsage(limit))
}

// GetTopUsage 返回使用次数最多的 N 条记录。
func (s *FrecencyService) GetTopUsage(limit int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	return s.usageResult(s.App.DB.GetTopUsage(limit))
}
