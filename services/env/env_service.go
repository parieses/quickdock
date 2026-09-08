package env

import (
	"context"
	"fmt"
	"strings"

	envmgr "quickdock/internal/env"
	"quickdock/services"
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
func (s *EnvironmentService) EnvList() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	return services.Ok(s.App.Env.List())
}

// EnvRefresh 强制重新扫描所有运行时（便携目录 + 系统 PATH），完成后重新保存检测结果，
// 并经 quickdock:env:refreshed 事件通知前端刷新列表。供环境管理页的刷新按钮调用。
func (s *EnvironmentService) EnvRefresh() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	s.App.Env.RefreshAllAsync(func() {
		if s.App.App() != nil {
			s.App.App().Event.Emit("quickdock:env:refreshed")
		}
	})
	return services.Ok(nil)
}

// EnvSources 返回某运行时的可用下载源（含自定义源）。
func (s *EnvironmentService) EnvSources(runtime string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	srcs, err := s.App.Env.Sources(envmgr.Runtime(runtime))
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(srcs)
}

// EnvSetSource 切换某运行时的下载源，或设置/清除自定义源模板。
func (s *EnvironmentService) EnvSetSource(runtime, sourceID, custom string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.SetSource(envmgr.Runtime(runtime), sourceID, custom); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvInstall 安装指定运行时的指定版本。异步执行：立即返回，进度经 quickdock:env:progress 事件推送。
// 网络不佳时可先调用 EnvSetSource 切换到自定义源，再触发安装。
func (s *EnvironmentService) EnvInstall(runtime, version, sourceID, custom string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	rt := envmgr.Runtime(runtime)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("安装 %s 异常: %v", runtime, r)
				if s.App.App() != nil {
					s.App.App().Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "error", Message: msg})
				}
			}
		}()
		cb := envmgr.InstallCallback{
			OnProgress: func(written, total int64) {
				if s.App.App() != nil {
					s.App.App().Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "download", Written: written, Total: total})
				}
			},
			OnLog: func(msg string) {
				if s.App.App() != nil {
					s.App.App().Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "log", Message: msg})
				}
			},
			OnStage: func(stage, msg string) {
				if s.App.App() != nil {
					s.App.App().Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: stage, Message: msg})
				}
			},
		}
		if err := s.App.Env.Install(rt, version, sourceID, custom, cb); err != nil {
			if s.App.App() != nil {
				s.App.App().Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "error", Message: err.Error()})
			}
			return
		}
		if s.App.App() != nil {
			s.App.App().Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "done", Message: "安装完成"})
		}
		// 安装成功后刷新检测结果缓存，让前端列表立即显示新版本
		s.App.Env.RefreshDetected(rt)
	}()
	return services.Ok(nil)
}

// EnvAvailableVersions 返回某运行时全量可下载版本（上游拉取，失败兜底推荐列表）。
// 用于前端「安装新版本」输入框的候选补全，覆盖不止硬编码的推荐版本。
func (s *EnvironmentService) EnvAvailableVersions(runtime string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	vs := s.App.Env.AvailableVersions(envmgr.Runtime(runtime))
	return services.Ok(vs)
}

// EnvStart 启动某运行时的服务（仅 nginx/redis 支持）。前端随后轮询 EnvStatus 看运行状态。
func (s *EnvironmentService) EnvStart(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.Start(envmgr.Runtime(runtime), version, nil); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvStop 停止某运行时的服务。
func (s *EnvironmentService) EnvStop(runtime string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.Stop(envmgr.Runtime(runtime), ""); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvRestart 重启某运行时的服务（先停后启，复用 Start 的端口冲突与配置校验）。
func (s *EnvironmentService) EnvRestart(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.Restart(envmgr.Runtime(runtime), version, nil); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvStatus 查询某运行时服务运行状态（nginx/redis）。非服务类运行时返回 running=false。
func (s *EnvironmentService) EnvStatus(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	st, err := s.App.Env.Status(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(st)
}

// EnvSetEnabled 设定某运行时的「常驻」期望状态（开/关）。on=true 立即拉起（用激活/首个版本），
// on=false 立即停止。前端把原来的「运行/停止」按钮改为开关即调用此方法。
func (s *EnvironmentService) EnvSetEnabled(runtime string, on bool) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.SetEnabled(envmgr.Runtime(runtime), on); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvReconcile 手动触发一次对账：拉起所有「已开启但未运行」的常驻服务。应用启动时会由宿主自动调用。
func (s *EnvironmentService) EnvReconcile() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	s.App.Env.ReconcileEnabled(context.Background())
	return services.Ok(nil)
}

// EnvPortConflict 查询某运行时默认服务端口是否被其它程序占用（启动前可视化提示）。
// 非服务类运行时返回 occupied=false 的零值。
func (s *EnvironmentService) EnvPortConflict(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	pc, err := s.App.Env.PortConflict(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(pc)
}

// EnvRabbitMQEnableMgmt 针对运行中的 RabbitMQ 启用管理后台插件（rabbitmq_management，端口 15672）。
// 返回命令完整输出；启用成功即可在浏览器访问 http://127.0.0.1:15672/ 。
func (s *EnvironmentService) EnvRabbitMQEnableMgmt(version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	var buf strings.Builder
	err := s.App.Env.EnableRabbitMQManagement(version, func(s string) {
		buf.WriteString(s)
		buf.WriteString("\n")
	})
	if err != nil {
		return services.FailMsg(err.Error() + "\n" + buf.String())
	}
	return services.Ok(buf.String())
}

// EnvRabbitMQDisableMgmt 关闭 RabbitMQ 管理后台插件（rabbitmq_management，端口 15672）。
func (s *EnvironmentService) EnvRabbitMQDisableMgmt(version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	var buf strings.Builder
	err := s.App.Env.DisableRabbitMQManagement(version, func(s string) {
		buf.WriteString(s)
		buf.WriteString("\n")
	})
	if err != nil {
		return services.FailMsg(err.Error() + "\n" + buf.String())
	}
	return services.Ok(buf.String())
}

// EnvRabbitMQIsMgmtEnabled 返回 RabbitMQ 管理后台是否已启用（决定是否显示「启用/关闭」）。
func (s *EnvironmentService) EnvRabbitMQIsMgmtEnabled(version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	return services.Ok(s.App.Env.IsRabbitMQManagementEnabled(version))
}

// EnvGitStatus 返回当前 Git 环境的综合状态（版本/路径/SSH/Git LFS），供环境管理页状态表展示。
func (s *EnvironmentService) EnvGitStatus() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	return services.Ok(s.App.Env.GitStatus())
}

// EnvSetActive 设置某运行时的激活版本（其 bin 目录即“环境变量指向”的版本）。version=="" 表示清除激活。
// 这会决定该运行时在 QuickDock 内的默认使用版本。
func (s *EnvironmentService) EnvSetActive(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.SetActive(envmgr.Runtime(runtime), version); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvUnsetActive 取消某版本的环境变量指向：直接将其 bin 目录从系统 PATH 注销（不依赖 active 元数据）。
// 与 EnvSetActive(rt, "") 的区别：取消的是指定版本，而非仅当前 active 元数据指向的版本，
// 避免元数据漂移时取消无效、PATH 残留旧版本 bin。
func (s *EnvironmentService) EnvUnsetActive(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.UnsetActive(envmgr.Runtime(runtime), version); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvSetMeta 更新某版本的别名与备注（备注显示于版本列表，别名可替代版本号展示）。
func (s *EnvironmentService) EnvSetMeta(runtime, version, alias, note string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.SetVersionMeta(envmgr.Runtime(runtime), version, alias, note); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvDeleteVersion 删除某已安装版本（便携目录）及元数据；系统 PATH 上的版本无法在此删除。
func (s *EnvironmentService) EnvDeleteVersion(runtime, version string, removeData bool) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.DeleteVersion(envmgr.Runtime(runtime), version, removeData); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvImportVersion 导入一个已存在的外部安装目录（探测版本号并登记），使其在环境管理中可见。
func (s *EnvironmentService) EnvImportVersion(runtime, dir string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	version, err := s.App.Env.ImportVersion(envmgr.Runtime(runtime), dir)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(version)
}

// EnvConfigSupport 判断某 runtime 是否支持通用配置编辑（实现了 ConfigProvider）。
func (s *EnvironmentService) EnvConfigSupport(runtime string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	return services.Ok(s.App.Env.ConfigSupport(envmgr.Runtime(runtime)))
}

// EnvConfigGet 读取某 runtime 某版本的配置文件（通用，适用于实现了 ConfigProvider 的运行时）。
func (s *EnvironmentService) EnvConfigGet(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	cfg, err := s.App.Env.ConfigGet(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(cfg)
}

// EnvConfigSet 写回某 runtime 某版本的配置文件（整体覆盖；需重启服务才生效）。
func (s *EnvironmentService) EnvConfigSet(runtime, version, raw string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.ConfigSet(envmgr.Runtime(runtime), version, raw); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvPHPConfigGet 读取某已装 PHP 版本的配置（php.ini 正文、禁用函数、错误日志、扩展列表）。
func (s *EnvironmentService) EnvPHPConfigGet(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	cfg, err := s.App.Env.PHPConfigGet(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(cfg)
}

// EnvPHPConfigSet 写回某已装 PHP 版本的配置（Raw 整体覆盖，或按结构化字段改写）。
func (s *EnvironmentService) EnvPHPConfigSet(runtime, version string, patch envmgr.PHPConfigPatch) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.PHPConfigSet(envmgr.Runtime(runtime), version, patch); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvLogGet 读取某运行时某版本的运行日志（通用，适用于实现了 LogProvider 的运行时，如 Redis）。
// 取代原先仅 Redis 可用的 EnvRedisLog，所有服务型运行时均可复用。
func (s *EnvironmentService) EnvLogGet(runtime, version string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	log, err := s.App.Env.LogGet(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(log)
}

// EnvCertStatus 返回 mkcert 一键签发的前置状态：
//   - ExeAvailable：是否已装可用 mkcert（决定前端提示先安装还是可直接签发）
//   - RootTrusted：本地根 CA 是否已安装到系统信任（决定「信任根」按钮是否需要）
func (s *EnvironmentService) EnvCertStatus() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	exeOK := false
	rootOK := false
	var msg string
	if _, err := s.App.Env.MkcertExe(); err == nil {
		exeOK = true
		if ok, rerr := s.App.Env.CertCARootTrusted(); rerr == nil && ok {
			rootOK = true
		}
	} else {
		msg = err.Error()
	}
	return services.Ok(map[string]interface{}{"exe": exeOK, "rootTrusted": rootOK, "message": msg})
}

// EnvCertInstallRoot 信任 mkcert 本地根 CA（等价 mkcert -install，幂等）。
func (s *EnvironmentService) EnvCertInstallRoot() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.CertInstallRoot(); err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(nil)
}

// EnvCertIssue 用 mkcert 为 hosts 一键签发本地可信证书到 outDir。
// name 为证书名（产 <name>-cert.pem / <name>-key.pem）；返回含 cert/key 绝对路径的 map。
func (s *EnvironmentService) EnvCertIssue(outDir, name string, hosts []string) *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	res, err := s.App.Env.CertIssue(outDir, name, hosts)
	if err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(res)
}
