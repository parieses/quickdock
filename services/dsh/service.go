package dsh

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	dshcore "quickdock/internal/dsh"
	"quickdock/services"
)

// 自动启动键定义于引擎 dshcore.AutoStartKey（宿主生命周期与其共用）

// DSHService 承载原 AppService 的 DeepSeek Harness（dsh）领域方法：
// 运行环境检测/一键安装/更新与 dsh web 进程控制。
// 通过 App 回指宿主 AppService，以访问共享的 NodeEnv/DSH 管理器
// （引擎位于 internal/dsh，本包内以 dshcore 别名引用）。
// services 包保留 NodeEnv/DSH 字段（service.go 生命周期 SetApp 时注入 app，
// lifecycle.go 启动/退出时启停服务），本包只承载方法，services 不反向 import 本包。
type DSHService struct {
	App *services.AppService
}

// NewDSHService 创建 dsh 服务实例，App 为宿主服务引用。
func NewDSHService(app *services.AppService) *DSHService {
	return &DSHService{App: app}
}

// DetectNodeEnv 检测 node/npx/dsh 运行状态
func (s *DSHService) DetectNodeEnv() *services.ApiResult {
	if s.App.NodeEnv == nil {
		return services.FailMsg("node env 未初始化")
	}
	return services.Ok(s.App.NodeEnv.Detect())
}

// SetupDSH 一键安装运行环境（缺失时下载便携 Node + 安装 dsh），进度经事件推送前端。
// 异步执行：立即返回，前端订阅 quickdock:dsh:progress 展示进度。
func (s *DSHService) SetupDSH() *services.ApiResult {
	if s.App.NodeEnv == nil {
		return services.FailMsg("node env 未初始化")
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("安装 DeepSeek Harness 异常: %v", r)
				s.App.NodeEnv.EmitLog("error", msg)
				// panic 也必须补发 error 事件，否则前端 settingUp 永远无法复位，按钮永久禁用
				if app := s.App.App(); app != nil {
					app.Event.Emit("quickdock:dsh:progress", dshcore.SetupProgress{Stage: "error", Message: msg})
				}
			}
		}()
		_ = s.App.NodeEnv.SetupDSH(context.Background(), nil)
	}()
	return services.Ok(nil)
}

// DSHInstallPlugin 安装指定插件（执行 dsh plugin --profile web add <plugin>）。
// 异步执行，输出经 quickdock:dsh:log 事件推送到前端日志面板。
func (s *DSHService) DSHInstallPlugin(plugin string) *services.ApiResult {
	if s.App.DSH == nil {
		return services.FailMsg("DSH 未初始化")
	}
	plugin = strings.TrimSpace(plugin)
	if plugin == "" {
		return services.FailMsg("插件名不能为空")
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 与 SetupDSH/UpdateDSH 相同：goroutine 内 panic 必须消化，否则会崩掉整个应用
				msg := fmt.Sprintf("安装插件异常: %v", r)
				s.App.NodeEnv.EmitLog("error", msg)
				if app := s.App.App(); app != nil {
					app.Event.Emit("quickdock:dsh:plugin", map[string]bool{"ok": false})
				}
			}
		}()
		_ = s.App.DSH.InstallPlugin(plugin)
	}()
	return services.Ok(nil)
}

// DSHUpdateAllPlugins 将 profile 内所有插件升级到各自 semver 范围内最新版（git 依赖拉最新提交；
// 精确固定版不动）。异步执行，升级前自动备份 package.json+lock 便于回滚，进度经 quickdock:dsh:log
// 推送，完成经 quickdock:dsh:plugin{ok,backup,kind:"update"} 通知前端（含备份路径以启用回滚）。
func (s *DSHService) DSHUpdateAllPlugins() *services.ApiResult {
	if s.App.DSH == nil {
		return services.FailMsg("DSH 未初始化")
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 与 SetupDSH/UpdateDSH/InstallPlugin 相同：goroutine 内 panic 必须消化
				msg := fmt.Sprintf("更新插件异常: %v", r)
				s.App.NodeEnv.EmitLog("error", msg)
			}
		}()
		// UpdateAllPlugins 内部已在成功/失败（含 panic 触发的 defer）时 emit 完成事件，
		// 此处仅消费错误结果用于兜底日志，无需重复 emit。
		_ = s.App.DSH.UpdateAllPlugins()
	}()
	return services.Ok(nil)
}

// DSHRollbackPlugins 将 profile 插件回滚到最近一次「更新全部插件」之前的备份。异步执行，
// 完成经 quickdock:dsh:plugin-rollback{ok} 通知前端。
func (s *DSHService) DSHRollbackPlugins() *services.ApiResult {
	if s.App.DSH == nil {
		return services.FailMsg("DSH 未初始化")
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("回滚插件异常: %v", r)
				s.App.NodeEnv.EmitLog("error", msg)
			}
		}()
		_ = s.App.DSH.RollbackPlugins()
	}()
	return services.Ok(nil)
}

// DSHCheckPluginUpdates 预检 registry 插件（git 依赖不纳入）是否有可用更新，返回列表。
// 联网查 npm registry，可能耗时数秒（同步返回，前端 await 时展示 loading）。
func (s *DSHService) DSHCheckPluginUpdates() *services.ApiResult {
	if s.App.DSH == nil {
		return services.FailMsg("DSH 未初始化")
	}
	list, err := s.App.DSH.CheckPluginUpdates()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(list)
}

// OpenDSHWindow 拉起 dsh web 并在原生窗口加载其 URL（dsh 未安装时返回错误）
func (s *DSHService) OpenDSHWindow() *services.ApiResult {
	if s.App.DSH == nil {
		return services.FailMsg("DSH 未初始化")
	}
	url, err := s.App.DSH.OpenDSHWindow()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]string{"url": url})
}

// DSHStatus 返回 dsh web 服务运行状态与自动启动配置（供设置页状态展示与开关）
func (s *DSHService) DSHStatus() *services.ApiResult {
	st := map[string]any{
		"running":   false,
		"autoStart": s.dshAutoStartEnabled(),
		"port":      dshcore.DefaultDSHPort,
		"url":       "",
	}
	if s.App.DSH != nil {
		port := s.App.DSH.Port()
		st["running"] = s.App.DSH.Running()
		st["port"] = port
		if s.App.DSH.Running() {
			st["url"] = "http://127.0.0.1:" + strconv.Itoa(port)
		}
	}
	return services.Ok(st)
}

// DSHStart 启动 dsh web 服务（只起进程，不开窗口；进程已运行则直接复用返回）
func (s *DSHService) DSHStart() *services.ApiResult {
	if s.App.DSH == nil {
		return services.FailMsg("DSH 未初始化")
	}
	url, err := s.App.DSH.Start()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]string{"url": url})
}

// DSHStop 停止 dsh web 服务（杀进程树，释放 3080 端口；窗口若开着会导航到"已停止"提示页）
func (s *DSHService) DSHStop() *services.ApiResult {
	if s.App.DSH == nil {
		return services.FailMsg("DSH 未初始化")
	}
	s.App.DSH.Stop()
	s.App.DSH.NotifyStopped()
	return services.Ok(nil)
}

// DSHSetAutoStart 开关 dsh web 随 QuickDock 启动延迟自动开启（默认开启）
func (s *DSHService) DSHSetAutoStart(enabled bool) *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("database not initialized")
	}
	v := "0"
	if enabled {
		v = "1"
	}
	if err := s.App.DB.SetValue(dshcore.AutoStartKey, v); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// dshAutoStartEnabled 读取 dsh web 自动启动配置（默认开启）
func (s *DSHService) dshAutoStartEnabled() bool {
	if s.App.DB == nil {
		return true
	}
	v, err := s.App.DB.GetValue(dshcore.AutoStartKey)
	if err != nil || v == "" {
		return true
	}
	return v == "1"
}

// CheckDSHUpdate 检测已安装 dsh 是否有新版本（联网查 latest；查询失败静默返回当前状态，不阻塞 UI）
func (s *DSHService) CheckDSHUpdate() *services.ApiResult {
	if s.App.NodeEnv == nil {
		return services.FailMsg("node env 未初始化")
	}
	return services.Ok(s.App.NodeEnv.DetectWithUpdate())
}

// UpdateDSH 将 dsh 更新到最新版（异步执行；进度经 quickdock:dsh:progress/log 事件推送前端）
func (s *DSHService) UpdateDSH() *services.ApiResult {
	if s.App.NodeEnv == nil {
		return services.FailMsg("node env 未初始化")
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("更新 DeepSeek Harness 异常: %v", r)
				s.App.NodeEnv.EmitLog("error", msg)
				if app := s.App.App(); app != nil {
					app.Event.Emit("quickdock:dsh:progress", dshcore.SetupProgress{Stage: "error", Message: msg})
				}
			}
		}()
		err := s.App.NodeEnv.UpdateDSH(context.Background())
		if err == nil && s.App.DSH != nil && s.App.DSH.Running() {
			// dsh 包已更新，但正在运行的进程仍是旧代码——自动重启服务让新版本生效，
			// 否则用户点完"更新"以为升级了，实际跑的还是旧版。
			s.App.NodeEnv.EmitLog("info", "dsh 更新完成，正在重启服务使新版本生效…")
			s.App.DSH.Stop()
			time.Sleep(500 * time.Millisecond) // 兜底等端口完全释放（Stop 内部已等）
			if _, err2 := s.App.DSH.Start(); err2 != nil {
				s.App.NodeEnv.EmitLog("error", "dsh 更新完成，但重启服务失败: "+err2.Error()+"（可在设置中手动启动）")
			}
		}
	}()
	return services.Ok(nil)
}
