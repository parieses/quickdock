package env

import "quickdock/services"

// EnvironmentService 承载原 AppService 的环境管理（运行时安装/版本/服务）领域方法。
// 通过 App 回指宿主 AppService，以访问共享的 Env 运行时管理器
// （引擎位于 internal/env，本包内以 envmgr 别名引用）。
// services 包保留 Env 字段（service.go 生命周期启动时调用 RefreshAllAsync），
// 本包只承载方法，services 不反向 import 本包，避免循环依赖。
type EnvironmentService struct {
	App *services.AppService
}

// NewEnvironmentService 创建环境管理服务实例，App 为宿主服务引用。
func NewEnvironmentService(app *services.AppService) *EnvironmentService {
	return &EnvironmentService{App: app}
}
