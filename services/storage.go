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

// NormalizeDataDir 把用户选择的目录规范化成最终的数据目录路径（仅用于 UI 展示）。
//
// 规则见 platform.NormalizeTargetDir：选盘符根目录（D:\）→ D:\.quickdock；
// 选普通目录 → 追加 .quickdock。用户选 D 盘时看到「D:\.quickdock」才不会疑惑
// 「我明明选了 D 盘，怎么变成 D:\.quickdock 了」。
func (a *AppService) NormalizeDataDir(path string) *ApiResult {
	return Ok(platform.NormalizeTargetDir(path))
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

// GetUiZoom 返回当前生效的界面缩放系数（1 = 标准）。
// 与数据目录一样走 platform 配置（而非数据库）：窗口在创建时就固定了 Zoom，
// 而数据库要等服务启动才打开，那时窗口已经建好了。
func (a *AppService) GetUiZoom() *ApiResult {
	return Ok(platform.LoadUiZoom())
}

// SetUiZoom 设置界面缩放并持久化。
// 修改不会即时生效：各窗口的 Zoom 与尺寸在创建时即已固定，运行期改不了，
// 故需重启应用（前端保存后提示「重启后生效」并调用 RestartApp）。
func (a *AppService) SetUiZoom(zoom float64) *ApiResult {
	if err := platform.SetUiZoom(zoom); err != nil {
		return Fail(err)
	}
	return OkMsg(nil, "已保存，重启后生效")
}

// RestartApp 真正重启应用（脱离式拉起新进程 + 等待旧进程退出后统一退出）。
// 存储位置修改必须重启才能生效：日志、数据库、运行时都已按旧目录打开。
// 与 UpdateService.RestartApp（就地替换更新专用）语义不同，那个无待更新时会失败。
func (a *AppService) RestartApp() *ApiResult {
	if a.RestartAppFn == nil {
		return FailMsg("重启能力未初始化")
	}
	a.RestartAppFn()
	return Ok(nil)
}
