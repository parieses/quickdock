// Package env 环境管理服务
// 承载原 AppService 的环境管理（运行时安装/版本/服务）领域方法。
package env

import (
	"context"
	"fmt"
	"strings"

	envmgr "quickdock/internal/env"
	"quickdock/services"
)

// EnvironmentService 承载剪贴板历史领域方法。
// App 回指宿主 AppService：DB 与 MainWindow/ClipboardMode/GetClipboardWindow 等
// 均为宿主导出字段/注入回调，本包仅经 App 读取，不反向 import 宿主逻辑。
type EnvironmentService struct {
	App *services.AppService
}

// NewEnvironmentService 创建环境管理服务实例，App 为宿主服务引用。
func NewEnvironmentService(app *services.AppService) *EnvironmentService {
	return &EnvironmentService{App: app}
}

// ===== 内部辅助 =====

// envProgress 安装进度事件载荷，经 quickdock:env:progress 推送到前端
type envProgress struct {
	Runtime string `json:"runtime"` // node / php / go / redis / nginx
	Stage   string `json:"stage"`   // download | extract | log | done | error
	Message string `json:"message"`
	Written int64  `json:"written"` // 已下载字节
	Total   int64  `json:"total"`   // 总字节（未知为 -1）
}

// dbOK 检查宿主 DB 是否就绪
func (s *EnvironmentService) checkEnv() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	return nil
}

// ===== 环境管理方法 =====

// EnvList 返回所有受管运行时的概览：已装版本、可下载版本清单、可用下载源、当前活跃源。
func (s *EnvironmentService) EnvList() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	return services.Ok(s.App.Env.List())
}

// EnvRefresh 强制重新扫描所有运行时（便携目录 + 系统 PATH），完成后重新保存检测结果，
// 并经 quickdock:env:refreshed 事件通知前端刷新列表。
func (s *EnvironmentService) EnvRefresh() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
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
	if r := s.checkEnv(); r != nil {
		return r
	}
	srcs, err := s.App.Env.Sources(envmgr.Runtime(runtime))
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(srcs)
}

// EnvSetSource 切换某运行时的下载源，或设置/清除自定义源模板。
func (s *EnvironmentService) EnvSetSource(runtime, sourceID, custom string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.SetSource(envmgr.Runtime(runtime), sourceID, custom); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvInstall 安装指定运行时的指定版本。异步执行：立即返回，进度经 quickdock:env:progress 事件推送。
func (s *EnvironmentService) EnvInstall(runtime, version, sourceID, custom string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
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
		s.App.Env.RefreshDetected(rt)
	}()
	return services.Ok(nil)
}

// EnvAvailableVersions 返回某运行时全量可下载版本。
func (s *EnvironmentService) EnvAvailableVersions(runtime string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	vs := s.App.Env.AvailableVersions(envmgr.Runtime(runtime))
	return services.Ok(vs)
}

// EnvStart 启动某运行时的服务（仅 nginx/redis 支持）。
func (s *EnvironmentService) EnvStart(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.Start(envmgr.Runtime(runtime), version, nil); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvStop 停止某运行时的服务。
func (s *EnvironmentService) EnvStop(runtime string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.Stop(envmgr.Runtime(runtime), ""); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvRestart 重启某运行时的服务。
func (s *EnvironmentService) EnvRestart(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.Restart(envmgr.Runtime(runtime), version, nil); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvStatus 查询某运行时服务运行状态。
func (s *EnvironmentService) EnvStatus(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	st, err := s.App.Env.Status(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(st)
}

// EnvSetEnabled 设定某运行时的「常驻」期望状态（开/关）。
func (s *EnvironmentService) EnvSetEnabled(runtime string, on bool) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.SetEnabled(envmgr.Runtime(runtime), on); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvReconcile 手动触发一次对账：拉起所有「已开启但未运行」的常驻服务。
func (s *EnvironmentService) EnvReconcile() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	s.App.Env.ReconcileEnabled(context.Background())
	return services.Ok(nil)
}

// EnvPortConflict 查询某运行时默认服务端口是否被其它程序占用。
func (s *EnvironmentService) EnvPortConflict(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	pc, err := s.App.Env.PortConflict(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(pc)
}

// EnvRabbitMQEnableMgmt 针对运行中的 RabbitMQ 启用管理后台插件。
func (s *EnvironmentService) EnvRabbitMQEnableMgmt(version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
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

// EnvRabbitMQDisableMgmt 关闭 RabbitMQ 管理后台插件。
func (s *EnvironmentService) EnvRabbitMQDisableMgmt(version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
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

// EnvRabbitMQIsMgmtEnabled 返回 RabbitMQ 管理后台是否已启用。
func (s *EnvironmentService) EnvRabbitMQIsMgmtEnabled(version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	return services.Ok(s.App.Env.IsRabbitMQManagementEnabled(version))
}

// EnvGitStatus 返回当前 Git 环境的综合状态。
func (s *EnvironmentService) EnvGitStatus() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	return services.Ok(s.App.Env.GitStatus())
}

// EnvSetActive 设置某运行时的激活版本。
func (s *EnvironmentService) EnvSetActive(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.SetActive(envmgr.Runtime(runtime), version); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvUnsetActive 取消某版本的环境变量指向。
func (s *EnvironmentService) EnvUnsetActive(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.UnsetActive(envmgr.Runtime(runtime), version); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvSetMeta 更新某版本的别名与备注。
func (s *EnvironmentService) EnvSetMeta(runtime, version, alias, note string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.SetVersionMeta(envmgr.Runtime(runtime), version, alias, note); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvDeleteVersion 删除某已安装版本。
func (s *EnvironmentService) EnvDeleteVersion(runtime, version string, removeData bool) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.DeleteVersion(envmgr.Runtime(runtime), version, removeData); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvImportVersion 导入一个已存在的外部安装目录。
func (s *EnvironmentService) EnvImportVersion(runtime, dir string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	version, err := s.App.Env.ImportVersion(envmgr.Runtime(runtime), dir)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(version)
}

// EnvConfigSupport 判断某 runtime 是否支持通用配置编辑。
func (s *EnvironmentService) EnvConfigSupport(runtime string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	return services.Ok(s.App.Env.ConfigSupport(envmgr.Runtime(runtime)))
}

// EnvConfigGet 读取某 runtime 某版本的配置文件。
func (s *EnvironmentService) EnvConfigGet(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	cfg, err := s.App.Env.ConfigGet(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(cfg)
}

// EnvConfigSet 写回某 runtime 某版本的配置文件。
func (s *EnvironmentService) EnvConfigSet(runtime, version, raw string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.ConfigSet(envmgr.Runtime(runtime), version, raw); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvPHPConfigGet 读取某已装 PHP 版本的配置。
func (s *EnvironmentService) EnvPHPConfigGet(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	cfg, err := s.App.Env.PHPConfigGet(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(cfg)
}

// EnvPHPConfigSet 写回某已装 PHP 版本的配置。
func (s *EnvironmentService) EnvPHPConfigSet(runtime, version string, patch envmgr.PHPConfigPatch) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.PHPConfigSet(envmgr.Runtime(runtime), version, patch); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvLogGet 读取某运行时某版本的运行日志。
func (s *EnvironmentService) EnvLogGet(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	log, err := s.App.Env.LogGet(envmgr.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(log)
}

// EnvCertStatus 返回 mkcert 一键签发的前置状态。
func (s *EnvironmentService) EnvCertStatus() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
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

// EnvCertInstallRoot 信任 mkcert 本地根 CA。
func (s *EnvironmentService) EnvCertInstallRoot() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.CertInstallRoot(); err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(nil)
}

// EnvCertIssue 用 mkcert 为 hosts 一键签发本地可信证书。
func (s *EnvironmentService) EnvCertIssue(outDir, name string, hosts []string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	res, err := s.App.Env.CertIssue(outDir, name, hosts)
	if err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(res)
}
