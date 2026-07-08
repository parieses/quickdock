package services

import (
	"quickdock/internal/platform"
)

// ===== 应用启动器 =====

// ScanInstalledApps 扫描已安装应用（缓存，首次调用后复用）
func (a *AppService) ScanInstalledApps() *ApiResult {
	apps, err := platform.GetCachedApps()
	if err != nil {
		return Fail(err)
	}
	return Ok(apps)
}

// ResetInstalledAppsCache 清除应用缓存（下次搜索时重新扫描）
func (a *AppService) ResetInstalledAppsCache() *ApiResult {
	platform.ResetAppsCache()
	return Ok(nil)
}

// LaunchInstalledApp 启动指定路径的应用
func (a *AppService) LaunchInstalledApp(path string) *ApiResult {
	if path == "" {
		return FailMsg("应用路径不能为空")
	}
	if err := platform.LaunchApp(path); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
