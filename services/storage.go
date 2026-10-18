package services

import (
	"quickdock/internal/platform"
)

// GetDataDir 返回当前生效的数据目录、是否自定义、以及默认位置（仅用于 UI 展示）。
// 无论 DB 是否就绪都可调用：数据存储位置属于进程级配置，与数据库无关。
func (a *AppService) GetDataDir() *ApiResult {
	cur, custom := platform.DataDirInfo()
	return Ok(map[string]any{
		"current":  cur,
		"isCustom": custom,
		"default":  platform.DefaultDataDirHome(),
	})
}

// SetDataDir 设置自定义数据目录。
// copyExisting=true 时，会在下次启动把旧目录（当前生效目录）的数据整体迁移到新目录。
// 修改不会即时生效：日志、数据库、运行时等已按旧目录打开，故需重启应用。
func (a *AppService) SetDataDir(path string, copyExisting bool) *ApiResult {
	if err := platform.SetDataDir(path, copyExisting); err != nil {
		return Fail(err)
	}
	return OkMsg(nil, "已保存，重启后生效")
}

// ResetDataDir 清除自定义配置，恢复默认数据目录（~/.quickdock）。
func (a *AppService) ResetDataDir() *ApiResult {
	if err := platform.ResetDataDir(); err != nil {
		return Fail(err)
	}
	return OkMsg(nil, "已恢复默认，重启后生效")
}
