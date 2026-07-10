package services

import "quickdock/internal/db"

// RecordUsage 记录一次使用（跨窗口共享的 frecency 追踪）
// key 格式：item:{id} | snippet:{id} | app:{name} | plugin:{pluginId}.{cmdId}
func (a *AppService) RecordUsage(key string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.RecordUsage(key); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

// GetAllUsage 返回全部 frecency 记录（前端初始化一次性加载）
func (a *AppService) GetAllUsage() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entries, err := a.DB.GetAllUsage()
	if err != nil {
		return dberr(err)
	}
	if entries == nil {
		entries = []db.FrecencyEntry{}
	}
	return Ok(entries)
}

// GetTopUsage 返回使用次数最多的 N 条记录
func (a *AppService) GetTopUsage(limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	entries, err := a.DB.GetTopUsage(limit)
	if err != nil {
		return dberr(err)
	}
	if entries == nil {
		entries = []db.FrecencyEntry{}
	}
	return Ok(entries)
}
