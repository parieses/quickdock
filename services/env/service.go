// Package env 环境管理服务
// 承载原 AppService 的环境管理（运行时安装/版本/服务）领域方法。
package env

import (
	"context"
	"fmt"
	"strings"
	"sync"

	envengine "quickdock/internal/env"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
)

// EnvironmentService 承载环境管理领域方法（运行时安装/版本/服务/配置）。
// App 回指宿主 AppService：DB 与 MainWindow/ClipboardMode/GetClipboardWindow 等
// 均为宿主导出字段/注入回调，本包仅经 App 读取，不反向 import 宿主逻辑。
type EnvironmentService struct {
	App *services.AppService

	// 源体检的并发闸门：体检要跑几十个网络探测，不允许重复触发（否则两轮结果会
	// 互相覆盖前端状态，也白耗带宽）。
	checkMu      sync.Mutex
	checkRunning bool
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

// EnvInstall 安装指定运行时的指定版本。异步执行：立即返回，进度经 quickdock:env:progress 事件推送。
func (s *EnvironmentService) EnvInstall(runtime, version, sourceID, custom string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	rt := envengine.Runtime(runtime)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("安装 %s 异常: %v", runtime, r)
				if s.App.App() != nil {
					s.App.App().Event.Emit("quickdock:env:progress", envProgress{Runtime: runtime, Stage: "error", Message: msg})
				}
			}
		}()
		cb := envengine.InstallCallback{
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
	vs := s.App.Env.AvailableVersions(envengine.Runtime(runtime))
	return services.Ok(vs)
}

// EnvStart 启动某运行时的服务（仅 nginx/redis 支持）。
func (s *EnvironmentService) EnvStart(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.Start(envengine.Runtime(runtime), version, nil); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvStop 停止某运行时的服务。
func (s *EnvironmentService) EnvStop(runtime string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.Stop(envengine.Runtime(runtime), ""); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvRestart 重启某运行时的服务。
func (s *EnvironmentService) EnvRestart(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.Restart(envengine.Runtime(runtime), version, nil); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvStatus 查询某运行时服务运行状态。
func (s *EnvironmentService) EnvStatus(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	st, err := s.App.Env.Status(envengine.Runtime(runtime), version)
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
	if err := s.App.Env.SetEnabled(envengine.Runtime(runtime), on); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvPortConflict 查询某运行时默认服务端口是否被其它程序占用。
func (s *EnvironmentService) EnvPortConflict(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	pc, err := s.App.Env.PortConflict(envengine.Runtime(runtime), version)
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
	if err := s.App.Env.SetActive(envengine.Runtime(runtime), version); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvUnsetActive 取消某版本的环境变量指向。
func (s *EnvironmentService) EnvUnsetActive(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.UnsetActive(envengine.Runtime(runtime), version); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvSetMeta 更新某版本的别名与备注。
func (s *EnvironmentService) EnvSetMeta(runtime, version, alias, note string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.SetVersionMeta(envengine.Runtime(runtime), version, alias, note); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvDeleteVersion 删除某已安装版本。
func (s *EnvironmentService) EnvDeleteVersion(runtime, version string, removeData bool) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.DeleteVersion(envengine.Runtime(runtime), version, removeData); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvImportVersion 导入一个已存在的外部安装目录。
func (s *EnvironmentService) EnvImportVersion(runtime, dir string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	version, err := s.App.Env.ImportVersion(envengine.Runtime(runtime), dir)
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
	return services.Ok(s.App.Env.ConfigSupport(envengine.Runtime(runtime)))
}

// EnvConfigGet 读取某 runtime 某版本的配置文件。
func (s *EnvironmentService) EnvConfigGet(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	cfg, err := s.App.Env.ConfigGet(envengine.Runtime(runtime), version)
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
	if err := s.App.Env.ConfigSet(envengine.Runtime(runtime), version, raw); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvPHPConfigGet 读取某已装 PHP 版本的配置。
func (s *EnvironmentService) EnvPHPConfigGet(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	cfg, err := s.App.Env.PHPConfigGet(envengine.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(cfg)
}

// EnvPHPConfigSet 写回某已装 PHP 版本的配置。
func (s *EnvironmentService) EnvPHPConfigSet(runtime, version string, patch envengine.PHPConfigPatch) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.PHPConfigSet(envengine.Runtime(runtime), version, patch); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvLogGet 读取某运行时某版本的运行日志。
func (s *EnvironmentService) EnvLogGet(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	log, err := s.App.Env.LogGet(envengine.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(log)
}

// EnvGoEnvKeys 返回某运行时可编辑的环境变量清单（含中文标签/说明/候选值）。
func (s *EnvironmentService) EnvGoEnvKeys(runtime string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	keys, err := s.App.Env.GoEnvKeys(envengine.Runtime(runtime))
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(keys)
}

// EnvGoEnvGet 读取某运行时某版本的环境变量（go env 等）。
func (s *EnvironmentService) EnvGoEnvGet(runtime, version string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	m, err := s.App.Env.GoEnvGet(envengine.Runtime(runtime), version)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(m)
}

// EnvGoEnvSet 写回某运行时某版本的环境变量。patch 中 value 为空表示清除该变量。
func (s *EnvironmentService) EnvGoEnvSet(runtime, version string, patch map[string]string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	if err := s.App.Env.GoEnvSet(envengine.Runtime(runtime), version, patch); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvRuntimeTasks 返回某运行时支持的「特色操作」清单（只声明有哪些，不执行）。
//
// 之所以单独一个接口而不是塞进 EnvList：任务表是静态的、与已装版本无关，
// 前端只在选中某个运行时的时候拉一次，避免每次刷新列表都多传一份。
func (s *EnvironmentService) EnvRuntimeTasks(runtime string) *services.ApiResult {
	return services.Ok(envengine.TasksFor(envengine.Runtime(runtime)))
}

// EnvRunTask 执行一个特色操作并同步返回文本结果（如 npm 全局包清单、go clean -modcache）。
// 返回结构里 error 字段非空表示执行失败——此时 output 仍可能带有出错前的有效输出。
func (s *EnvironmentService) EnvRunTask(runtime, version, taskID string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	return services.Ok(s.App.Env.RunTask(envengine.Runtime(runtime), version, taskID))
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

// projectVersionStatus 一个运行时在某个项目目录下的版本现状，供前端逐行展示。
type projectVersionStatus struct {
	Runtime   string   `json:"runtime"`
	Require   string   `json:"require"`   // 声明文件里的原始要求串，如 "^8.1"
	Source    string   `json:"source"`    // 来源文件，如 ".nvmrc"
	Matched   string   `json:"matched"`   // 匹配到的已装版本；"" = 该要求当前无对应已装版本
	Active    string   `json:"active"`    // 当前激活版本；"" = 未激活
	Installed []string `json:"installed"` // 该运行时全部已装版本，供手动改绑
}

// EnvProjectVersions 探测项目目录（及其祖先目录）内的版本声明文件——
// .nvmrc / .node-version / .php-version / .python-version / .tool-versions /
// package.json engines / composer.json require.php / go.mod——
// 逐条返回「要求什么版本 → 匹配到哪个已装版本 → 当前激活哪个」。
//
// dir 为空、目录不存在、无任何声明文件时一律返回空数组而不是错误：
// 条目可能是网页/命令，本就没有项目目录，把"没探测到"当失败会污染调用方逻辑。
func (s *EnvironmentService) EnvProjectVersions(dir string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	hints := envengine.DetectVersionHints(dir)
	out := make([]projectVersionStatus, 0, len(hints))
	for _, h := range hints {
		st := projectVersionStatus{Runtime: string(h.Runtime), Require: h.Version, Source: h.Source}
		installs, err := s.App.Env.InstalledVersions(h.Runtime)
		if err == nil {
			st.Installed = make([]string, 0, len(installs))
			for _, ins := range installs {
				st.Installed = append(st.Installed, ins.Version)
				if ins.Active {
					st.Active = ins.Version
				}
			}
			st.Matched = envengine.MatchInstalledVersion(h.Version, st.Installed)
		}
		out = append(out, st)
	}
	return services.Ok(out)
}

// ===== 源体检 / 磁盘占用 / 镜像加速 / 终端直开 / 清单导入导出（2026-10）=====

// sourceCheckEvent 源体检的逐条事件载荷（kind=source/version 二选一）。
type sourceCheckEvent struct {
	Kind    string                        `json:"kind"` // source | version
	Source  *envengine.SourceCheck        `json:"source,omitempty"`
	Version *envengine.VersionSourceCheck `json:"version,omitempty"`
}

// EnvSourceCheck 启动一次全量下载源体检（异步）。
//
// 立即返回，逐条结果经 quickdock:env:sourcecheck 推送，结束后经
// quickdock:env:sourcecheck-done 推送汇总报告。走异步是因为一次全量体检要探测
// 50 个左右地址，同步调用会让前端卡在等待里没有任何反馈。
func (s *EnvironmentService) EnvSourceCheck() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	s.checkMu.Lock()
	if s.checkRunning {
		s.checkMu.Unlock()
		return services.FailMsg("源体检正在进行中，请稍候")
	}
	s.checkRunning = true
	s.checkMu.Unlock()

	go func() {
		defer func() {
			s.checkMu.Lock()
			s.checkRunning = false
			s.checkMu.Unlock()
			if rec := recover(); rec != nil {
				if s.App.App() != nil {
					s.App.App().Event.Emit("quickdock:env:sourcecheck-done", map[string]interface{}{
						"error": fmt.Sprintf("体检异常: %v", rec),
					})
				}
			}
		}()
		// 事件推送是可观测性的关键一环：前端一旦收不到，界面表现为「全是 0」，
		// 与「后端没跑」无法区分。故这里显式记录 App 是否就绪、推了多少条。
		//
		// emit 会被 sourceCheckWorkers 个 goroutine 并发调用（每个源探完即回调），
		// 而 Wails 的事件发射器不保证可并发复用，故串行化：体检本身是网络耗时，
		// 多一层小锁的开销可以忽略。
		var emitMu sync.Mutex
		var emitted int
		emit := func(ev sourceCheckEvent) {
			app := s.App.App()
			if app == nil {
				return
			}
			emitMu.Lock()
			defer emitMu.Unlock()
			app.Event.Emit("quickdock:env:sourcecheck", ev)
			emitted++
		}
		if s.App.App() == nil {
			logger.W("[env][check] App 尚未就绪，体检结果将无法推送到前端")
		}
		report := envengine.CheckAllSources(context.Background(),
			func(it envengine.SourceCheck) {
				item := it
				emit(sourceCheckEvent{Kind: "source", Source: &item})
			},
			func(it envengine.VersionSourceCheck) {
				item := it
				emit(sourceCheckEvent{Kind: "version", Version: &item})
			})
		if app := s.App.App(); app != nil {
			app.Event.Emit("quickdock:env:sourcecheck-done", report)
			logger.I("[env][check] 事件推送完成 emitted=%d sources=%d versions=%d ok=%d mirrorOnly=%d failed=%d",
				emitted, len(report.Sources), len(report.Versions), report.OK, report.MirrorOnly, report.Failed)
		}
	}()
	return services.Ok(nil)
}

// EnvDiskUsage 统计各运行时（及各版本）的磁盘占用。
func (s *EnvironmentService) EnvDiskUsage() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	return services.Ok(envengine.DiskUsage())
}

// EnvUserEnv 列出当前用户（HKCU\Environment）的全部环境变量。
//
// 这一块此前只有「激活版本时自动写入」的单向通路，界面上看不到写进去了什么——
// 用户排查「PATH 里这条 ...\go\1.27.2\bin 是谁加的」时无从下手，故补上读写接口。
// 只作用于当前用户，不动系统级（HKLM），无需管理员权限。
func (s *EnvironmentService) EnvUserEnv() *services.ApiResult {
	rep, err := envengine.UserEnvList()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(rep)
}

// EnvUserEnvSetVar 新增/修改一条用户环境变量；value 为空表示删除。
func (s *EnvironmentService) EnvUserEnvSetVar(name, value string) *services.ApiResult {
	if err := envengine.UserEnvSetVar(name, value); err != nil {
		return services.Fail(err)
	}
	logger.I("[env][envvar] 写入用户环境变量 %s", name)
	return services.Ok(nil)
}

// EnvUserEnvDeleteVar 删除一条用户环境变量。
func (s *EnvironmentService) EnvUserEnvDeleteVar(name string) *services.ApiResult {
	if err := envengine.UserEnvDeleteVar(name); err != nil {
		return services.Fail(err)
	}
	logger.I("[env][envvar] 删除用户环境变量 %s", name)
	return services.Ok(nil)
}

// EnvUserEnvSetPath 整体重写用户 PATH 的条目列表。
func (s *EnvironmentService) EnvUserEnvSetPath(entries []string) *services.ApiResult {
	if err := envengine.UserEnvSetPath(entries); err != nil {
		return services.Fail(err)
	}
	logger.I("[env][envvar] 重写用户 PATH，共 %d 条", len(entries))
	return services.Ok(nil)
}

// mirrorKeyForRuntime 运行时 → 该运行时所用包管理器的镜像键。
//
// 只有「装完之后还要用它拉包」的运行时才有镜像项：Node 用 npm、Python 用 pip、
// PHP 用 composer、Go 用 GOPROXY。其余运行时（nginx/mysql/redis 等）没有包管理器，
// 不给入口，避免菜单里出现点了没反应的项。
func mirrorKeyForRuntime(rt string) (string, bool) {
	switch rt {
	case "node":
		return "npm", true
	case "python":
		return "pip", true
	case "composer":
		return "composer", true
	case "go":
		return "go", true
	}
	return "", false
}

// EnvMirrorState 读取某运行时对应包管理器的镜像配置现状（只读）。
func (s *EnvironmentService) EnvMirrorState(runtime string) *services.ApiResult {
	key, ok := mirrorKeyForRuntime(runtime)
	if !ok {
		return services.FailMsg(envengine.DisplayName(envengine.Runtime(runtime)) + " 没有可配置的包源镜像")
	}
	st, err := envengine.MirrorStateFor(key)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(st)
}

// EnvMirrorToggle 切换某运行时的包源镜像（enable=false 恢复官方源）。
func (s *EnvironmentService) EnvMirrorToggle(runtime string, enable bool) *services.ApiResult {
	key, ok := mirrorKeyForRuntime(runtime)
	if !ok {
		return services.FailMsg(envengine.DisplayName(envengine.Runtime(runtime)) + " 没有可配置的包源镜像")
	}
	st, err := envengine.MirrorApplyFor(key, enable)
	if err != nil {
		return services.FailMsg(err.Error())
	}
	logger.I("[env][mirror] %s 镜像切换 enable=%v 当前=%s", key, enable, st.Current)
	return services.Ok(st)
}

// EnvGitHubAccelState 读取 GitHub 下载加速开关（影响 Release 资产是否走 gh-proxy）。
func (s *EnvironmentService) EnvGitHubAccelState() *services.ApiResult {
	return services.Ok(map[string]bool{"enabled": envengine.GitHubAccelerateEnabled()})
}

// EnvGitHubAccelSet 设置 GitHub 下载加速开关。
func (s *EnvironmentService) EnvGitHubAccelSet(enable bool) *services.ApiResult {
	if err := envengine.SetGitHubAccelerate(enable); err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]bool{"enabled": enable})
}

// EnvManifestExport 导出当前环境清单（JSON 文本，前端保存为文件）。
func (s *EnvironmentService) EnvManifestExport() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	m := envengine.BuildManifest(s.App.AppVersion, s.App.Env.List())
	return services.Ok(envengine.ManifestString(m))
}

// EnvManifestImport 导入环境清单并与本机比对。
// applyActive=true 时，对「清单要求激活且本机已装」的版本一并切换激活指向；
// 未安装的版本只报告缺失，绝不自动下载（装什么版本由用户决定）。
func (s *EnvironmentService) EnvManifestImport(data string, applyActive bool) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	list := s.App.Env.List()
	report, err := envengine.CompareManifest([]byte(data), list)
	if err != nil {
		return services.Fail(err)
	}
	applied := []string{}
	if applyActive {
		if got, aerr := s.App.Env.ApplyManifestActive([]byte(data)); aerr == nil {
			applied = got
		}
	}
	merged := envengine.MergeManifestNotes(s.App.Env, []byte(data))
	return services.Ok(map[string]interface{}{
		"report":        report,
		"appliedActive": applied,
		"mergedNotes":   merged,
	})
}

// ===== Everything 文件检索（效率工具） =====

// EnvEverythingStatus 返回 Everything 的综合四态状态（未安装 / 缺少 es.exe / 已停止 / 运行中）。
func (s *EnvironmentService) EnvEverythingStatus() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	e := s.App.Env.EverythingRuntime()
	if e == nil {
		return services.FailMsg("Everything 未启用")
	}
	return services.Ok(e.EverythingStatus())
}

// EnvEverythingSearch 通过 es.exe 检索本地文件（命令面板 / MCP 复用）。
// 总开关关闭、es.exe 缺失、Everything 未运行、查询超时等全部静默降级为空结果，绝不阻塞调用方。
func (s *EnvironmentService) EnvEverythingSearch(query string, limit int, sortBy string) *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	// 总开关关闭时直接返回空，不触发 es.exe。
	if !envengine.FileSearchEnabled() {
		return services.Ok([]envengine.FileSearchResult{})
	}
	e := s.App.Env.EverythingRuntime()
	if e == nil {
		return services.Ok([]envengine.FileSearchResult{})
	}
	results, err := e.Search(context.Background(), query, limit, sortBy)
	if err != nil {
		// 仅参数非法（如空查询）会走到这里，正常返回错误；其余降级已在引擎内吞掉。
		return services.Fail(err)
	}
	if results == nil {
		results = []envengine.FileSearchResult{}
	}
	return services.Ok(results)
}

// EnvEverythingStart 启动 Everything.exe（外部工具，直接拉起其进程）。
func (s *EnvironmentService) EnvEverythingStart() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	e := s.App.Env.EverythingRuntime()
	if e == nil {
		return services.FailMsg("Everything 未启用")
	}
	if err := e.Start(context.Background(), "", nil); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvEverythingStop 停止 Everything.exe（结束其进程）。
func (s *EnvironmentService) EnvEverythingStop() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	e := s.App.Env.EverythingRuntime()
	if e == nil {
		return services.FailMsg("Everything 未启用")
	}
	if err := e.Stop(""); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvEverythingOpenDownload 打开 Everything 官网下载页（64 位安装包）。
func (s *EnvironmentService) EnvEverythingOpenDownload() *services.ApiResult {
	if err := platform.ShellOpen("https://www.voidtools.com/downloads/", ""); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvEverythingOpenCliDownload 打开 Everything CLI（es.exe）下载页，提示放入 Everything 目录或 PATH。
func (s *EnvironmentService) EnvEverythingOpenCliDownload() *services.ApiResult {
	if err := platform.ShellOpen("https://www.voidtools.com/downloads/#cli", ""); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvEverythingInstall 程序托管下载 Everything 便携版（便捷版）：按系统 32/64 位自动选包，
// 下载并解压到 QuickDock 托管目录，不再跳转到官网手动下载。
func (s *EnvironmentService) EnvEverythingInstall() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	e := s.App.Env.EverythingRuntime()
	if e == nil {
		return services.FailMsg("Everything 未启用")
	}
	cb := envengine.InstallCallback{
		OnLog: func(msg string) { logger.I("[env][everything] %s", msg) },
	}
	if err := e.Install(context.Background(), "", cb); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvEverythingInstallEs 程序托管下载 es.exe 命令行工具（文件检索依赖它），落盘到 Everything 同目录。
func (s *EnvironmentService) EnvEverythingInstallEs() *services.ApiResult {
	if r := s.checkEnv(); r != nil {
		return r
	}
	e := s.App.Env.EverythingRuntime()
	if e == nil {
		return services.FailMsg("Everything 未启用")
	}
	cb := envengine.InstallCallback{
		OnLog: func(msg string) { logger.I("[env][everything] %s", msg) },
	}
	if err := e.InstallEs(context.Background(), cb); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// EnvEverythingGetSettings 返回文件搜索相关设置（自定义安装目录 / es.exe 路径 / 总开关）。
func (s *EnvironmentService) EnvEverythingGetSettings() *services.ApiResult {
	settings := envengine.EverythingSettings{
		InstallPath:       envengine.GetEverythingInstallPath(),
		EsExePath:         envengine.GetEsExePath(),
		FileSearchEnabled: envengine.FileSearchEnabled(),
	}
	return services.Ok(settings)
}

// EnvEverythingSetSettings 写入文件搜索设置；空字符串表示恢复默认（自动探测）。
func (s *EnvironmentService) EnvEverythingSetSettings(installPath string, esExePath string, enabled bool) *services.ApiResult {
	if err := envengine.SetEverythingInstallPath(installPath); err != nil {
		return services.Fail(err)
	}
	if err := envengine.SetEsExePath(esExePath); err != nil {
		return services.Fail(err)
	}
	if err := envengine.SetFileSearchEnabled(enabled); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
