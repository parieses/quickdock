package services

import (
	"fmt"
	"strings"

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
// 已装版本读检测结果缓存（启动扫描/导入/安装/删除时刷新并持久化），本方法毫秒级返回。
func (a *AppService) EnvList() *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	return Ok(a.Env.List())
}

// EnvRefresh 强制重新扫描所有运行时（便携目录 + 系统 PATH），完成后重新保存检测结果，
// 并经 quickdock:env:refreshed 事件通知前端刷新列表。供环境管理页的刷新按钮调用。
func (a *AppService) EnvRefresh() *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	a.Env.RefreshAllAsync(func() {
		if a.app != nil {
			a.app.Event.Emit("quickdock:env:refreshed")
		}
	})
	return Ok(nil)
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
		// 安装成功后刷新检测结果缓存，让前端列表立即显示新版本
		a.Env.RefreshDetected(rt)
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

// EnvRestart 重启某运行时的服务（先停后启，复用 Start 的端口冲突与配置校验）。
func (a *AppService) EnvRestart(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.Restart(env.Runtime(runtime), version, nil); err != nil {
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

// EnvPortConflict 查询某运行时默认服务端口是否被其它程序占用（启动前可视化提示）。
// 非服务类运行时返回 occupied=false 的零值。
func (a *AppService) EnvPortConflict(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	pc, err := a.Env.PortConflict(env.Runtime(runtime), version)
	if err != nil {
		return Fail(err)
	}
	return Ok(pc)
}

// EnvRabbitMQEnableMgmt 针对运行中的 RabbitMQ 启用管理后台插件（rabbitmq_management，端口 15672）。
// 返回命令完整输出；启用成功即可在浏览器访问 http://127.0.0.1:15672/ 。
func (a *AppService) EnvRabbitMQEnableMgmt(version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	var buf strings.Builder
	err := a.Env.EnableRabbitMQManagement(version, func(s string) {
		buf.WriteString(s)
		buf.WriteString("\n")
	})
	if err != nil {
		return FailMsg(err.Error() + "\n" + buf.String())
	}
	return Ok(buf.String())
}

// EnvRabbitMQDisableMgmt 关闭 RabbitMQ 管理后台插件（rabbitmq_management，端口 15672）。
func (a *AppService) EnvRabbitMQDisableMgmt(version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	var buf strings.Builder
	err := a.Env.DisableRabbitMQManagement(version, func(s string) {
		buf.WriteString(s)
		buf.WriteString("\n")
	})
	if err != nil {
		return FailMsg(err.Error() + "\n" + buf.String())
	}
	return Ok(buf.String())
}

// EnvRabbitMQIsMgmtEnabled 返回 RabbitMQ 管理后台是否已启用（决定是否显示「启用/关闭」）。
func (a *AppService) EnvRabbitMQIsMgmtEnabled(version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	return Ok(a.Env.IsRabbitMQManagementEnabled(version))
}

// EnvGitStatus 返回当前 Git 环境的综合状态（版本/路径/SSH/Git LFS），供环境管理页状态表展示。
func (a *AppService) EnvGitStatus() *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	return Ok(a.Env.GitStatus())
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
func (a *AppService) EnvDeleteVersion(runtime, version string, removeData bool) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.DeleteVersion(env.Runtime(runtime), version, removeData); err != nil {
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

// EnvConfigSupport 判断某 runtime 是否支持通用配置编辑（实现了 ConfigProvider）。
func (a *AppService) EnvConfigSupport(runtime string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	return Ok(a.Env.ConfigSupport(env.Runtime(runtime)))
}

// EnvConfigGet 读取某 runtime 某版本的配置文件（通用，适用于实现了 ConfigProvider 的运行时）。
func (a *AppService) EnvConfigGet(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	cfg, err := a.Env.ConfigGet(env.Runtime(runtime), version)
	if err != nil {
		return Fail(err)
	}
	return Ok(cfg)
}

// EnvConfigSet 写回某 runtime 某版本的配置文件（整体覆盖；需重启服务才生效）。
func (a *AppService) EnvConfigSet(runtime, version, raw string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	if err := a.Env.ConfigSet(env.Runtime(runtime), version, raw); err != nil {
		return Fail(err)
	}
	return Ok(nil)
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

// EnvLogGet 读取某运行时某版本的运行日志（通用，适用于实现了 LogProvider 的运行时，如 Redis）。
// 取代原先仅 Redis 可用的 EnvRedisLog，所有服务型运行时均可复用。
func (a *AppService) EnvLogGet(runtime, version string) *ApiResult {
	if a.Env == nil {
		return FailMsg("env 未初始化")
	}
	log, err := a.Env.LogGet(env.Runtime(runtime), version)
	if err != nil {
		return Fail(err)
	}
	return Ok(log)
}
