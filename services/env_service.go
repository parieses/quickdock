package services

import (
	"fmt"

	"quickdock/services/env"
)

// envProgress 安装进度事件载荷，经 quickdock:env:progress 推送到前端
type envProgress struct {
	Runtime string `json:"runtime"` // node / php / go / redis / nginx
	Stage   string `json:"stage"`   // download | extract | log | done | error
	Message string `json:"message"`
	Written int64  `json:"written"` // 已下载字节
	Total   int64  `json:"total"`   // 总字节（未知为 -1）
}

// EnvList 返回所有受管运行时的概览：已装版本、可下载版本清单、可用下载源、当前活跃源。
func (a *AppService) EnvList() *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	return Ok(a.Env.List())
}

// EnvSources 返回某运行时的可用下载源（含自定义源）。
func (a *AppService) EnvSources(runtime string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	srcs, err := a.Env.Sources(env.Runtime(runtime))
	if err != nil {
		return Fail(err)
	}
	return Ok(srcs)
}

// EnvSetSource 切换某运行时的下载源，或设置/清除自定义源模板。
func (a *AppService) EnvSetSource(runtime, sourceID, custom string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.SetSource(env.Runtime(runtime), sourceID, custom); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// EnvInstall 安装指定运行时的指定版本。异步执行：立即返回，进度经 quickdock:env:progress 事件推送。
// 网络不佳时可先调用 EnvSetSource 切换到自定义源，再触发安装。
func (a *AppService) EnvInstall(runtime, version, sourceID, custom string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	rt := env.Runtime(runtime)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("安装 %s 异常: %v", runtime, r)
				if a.app != nil {
					a.app.Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "error", Message: msg})
				}
			}
		}()
		cb := env.InstallCallback{
			OnProgress: func(written, total int64) {
				if a.app != nil {
					a.app.Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "download", Written: written, Total: total})
				}
			},
			OnLog: func(msg string) {
				if a.app != nil {
					a.app.Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "log", Message: msg})
				}
			},
			OnStage: func(stage, msg string) {
				if a.app != nil {
					a.app.Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: stage, Message: msg})
				}
			},
		}
		if err := a.Env.Install(rt, version, sourceID, custom, cb); err != nil {
			if a.app != nil {
				a.app.Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "error", Message: err.Error()})
			}
			return
		}
		if a.app != nil {
			a.app.Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "done", Message: "安装完成"})
		}
	}()
	return Ok(nil)
}

// EnvAvailableVersions 返回某运行时全量可下载版本（上游拉取，失败兜底推荐列表）。
// 用于前端「安装新版本」输入框的候选补全，覆盖不止硬编码的推荐版本。
func (a *AppService) EnvAvailableVersions(runtime string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	vs := a.Env.AvailableVersions(env.Runtime(runtime))
	return Ok(vs)
}

// EnvStart 启动某运行时的服务（仅 nginx/redis 支持）。前端随后轮询 EnvStatus 看运行状态。
func (a *AppService) EnvStart(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.Start(env.Runtime(runtime), version, nil); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// EnvStop 停止某运行时的服务。
func (a *AppService) EnvStop(runtime string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.Stop(env.Runtime(runtime), ""); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// EnvStatus 查询某运行时服务运行状态（nginx/redis）。非服务类运行时返回 running=false。
func (a *AppService) EnvStatus(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	st, err := a.Env.Status(env.Runtime(runtime), version)
	if err != nil {
		return Fail(err)
	}
	return Ok(st)
}

// EnvSetActive 设置某运行时的激活版本（其 bin 目录即“环境变量指向”的版本）。version=="" 表示清除激活。
// 这会决定该运行时在 QuickDock 内的默认使用版本。
func (a *AppService) EnvSetActive(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.SetActive(env.Runtime(runtime), version); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// EnvUnsetActive 取消某版本的环境变量指向：直接将其 bin 目录从系统 PATH 注销（不依赖 active 元数据）。
// 与 EnvSetActive(rt, "") 的区别：取消的是指定版本，而非仅当前 active 元数据指向的版本，
// 避免元数据漂移时取消无效、PATH 残留旧版本 bin。
func (a *AppService) EnvUnsetActive(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.UnsetActive(env.Runtime(runtime), version); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// EnvSetMeta 更新某版本的别名与备注（备注显示于版本列表，别名可替代版本号展示）。
func (a *AppService) EnvSetMeta(runtime, version, alias, note string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.SetVersionMeta(env.Runtime(runtime), version, alias, note); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// EnvDeleteVersion 删除某已安装版本（便携目录）及元数据；系统 PATH 上的版本无法在此删除。
func (a *AppService) EnvDeleteVersion(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.DeleteVersion(env.Runtime(runtime), version); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// EnvImportVersion 导入一个已存在的外部安装目录（探测版本号并登记），使其在环境管理中可见。
func (a *AppService) EnvImportVersion(runtime, dir string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	version, err := a.Env.ImportVersion(env.Runtime(runtime), dir)
	if err != nil {
		return Fail(err)
	}
	return Ok(version)
}

// EnvPHPConfigGet 读取某已装 PHP 版本的配置（php.ini 正文、禁用函数、错误日志、扩展列表）。
func (a *AppService) EnvPHPConfigGet(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	cfg, err := a.Env.PHPConfigGet(env.Runtime(runtime), version)
	if err != nil {
		return Fail(err)
	}
	return Ok(cfg)
}

// EnvPHPConfigSet 写回某已装 PHP 版本的配置（Raw 整体覆盖，或按结构化字段改写）。
func (a *AppService) EnvPHPConfigSet(runtime, version string, patch env.PHPConfigPatch) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.PHPConfigSet(env.Runtime(runtime), version, patch); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
