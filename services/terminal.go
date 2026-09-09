package services

import (
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/terminal"
)

// 终端事件名（前端 Events.On 监听）
const (
	eventTermOutput = "quickdock:terminal:output" // {id, data}
	eventTermExit   = "quickdock:terminal:exit"   // {id, code}
)

// termMgr 终端会话管理器（懒创建，未使用终端时不占资源）
func (a *AppService) termMgr() *terminal.Manager {
	if a.Term == nil {
		a.Term = terminal.New()
		a.Term.OnOutput = func(id, data string) {
			if a.app != nil {
				a.app.Event.Emit(eventTermOutput, map[string]any{"id": id, "data": data})
			}
		}
		a.Term.OnExit = func(id string, code int) {
			if a.app != nil {
				a.app.Event.Emit(eventTermExit, map[string]any{"id": id, "code": code})
			}
		}
	}
	return a.Term
}

// TerminalStart 启动一个终端会话。
// runtimeID 非空时，把该运行时的激活版本 bin 目录前置到 PATH（例如 node / php / go）。
// shell: "powershell"（默认）或 "cmd"。
func (a *AppService) TerminalStart(id, shell, dir, runtimeID string, cols, rows int) *ApiResult {
	if strings.TrimSpace(id) == "" {
		return FailMsg("终端会话 ID 不能为空")
	}
	var extra []string
	if runtimeID = strings.TrimSpace(runtimeID); runtimeID != "" {
		extra = a.terminalRuntimePaths(runtimeID)
	}
	if err := a.termMgr().Start(id, shell, strings.TrimSpace(dir), extra, cols, rows); err != nil {
		logger.W("终端启动失败: %v", err)
		return FailMsg(err.Error())
	}
	return Ok(nil)
}

// TerminalWrite 向终端写入输入内容。
func (a *AppService) TerminalWrite(id, data string) *ApiResult {
	if err := a.termMgr().Write(id, data); err != nil {
		return FailMsg(err.Error())
	}
	return Ok(nil)
}

// TerminalResize 同步终端窗口尺寸（列数 × 行数）。
func (a *AppService) TerminalResize(id string, cols, rows int) *ApiResult {
	if err := a.termMgr().Resize(id, cols, rows); err != nil {
		return FailMsg(err.Error())
	}
	return Ok(nil)
}

// TerminalKill 关闭终端会话。
func (a *AppService) TerminalKill(id string) *ApiResult {
	if err := a.termMgr().Kill(id); err != nil {
		return FailMsg(err.Error())
	}
	return Ok(nil)
}

// TerminalKillAll 关闭所有终端会话（页面卸载时调用，避免残留子进程）。
func (a *AppService) TerminalKillAll() *ApiResult {
	if a.Term != nil {
		a.Term.KillAll()
	}
	return Ok(nil)
}

// TerminalRuntimeOptions 返回可用于注入终端 PATH 的运行时列表（只含已安装且有激活版本的）。
func (a *AppService) TerminalRuntimeOptions() *ApiResult {
	if a.Env == nil {
		return Ok([]map[string]string{})
	}
	out := make([]map[string]string, 0, 8)
	for _, e := range a.Env.PathInfo() {
		out = append(out, map[string]string{
			"id":      e.Runtime,
			"version": e.Version,
			"binDir":  e.BinDir,
			"inPath":  boolStr(e.InPath),
		})
	}
	return Ok(out)
}

// terminalRuntimePaths 取指定运行时的 bin 目录；未安装时返回 nil（终端仍按系统 PATH 启动）。
func (a *AppService) terminalRuntimePaths(runtimeID string) []string {
	if a.Env == nil {
		return nil
	}
	for _, e := range a.Env.PathInfo() {
		if strings.EqualFold(e.Runtime, runtimeID) && e.BinDir != "" {
			return []string{e.BinDir}
		}
	}
	return nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
