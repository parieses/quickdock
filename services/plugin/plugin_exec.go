package plugin

import "quickdock/services"

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/plugin"
)

// 插件命令的执行来源，落 plugin_exec_logs.trigger 与操作流水。
//
// 区分来源不是为了统计，是为了事后能分清谁干的：用户回看历史时，
// 若 AI 触发的执行与手点的一样记成 manual，就永远查不出「这个插件为什么自己跑了」。
const (
	TriggerManual = "manual" // 面板/命令面板/插件界面点出来的
	TriggerHotkey = "hotkey" // 热键回调
	TriggerMCP    = "mcp"    // 经 MCP 由外部 AI 客户端发起
)

func (p *PluginService) ListPlugins() *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	plugins := p.App.PluginMgr.ListPlugins()
	// 从 usage_frecency 表查询每个插件的使用次数（一条 SQL 聚合全部，替代逐条查询）
	if p.App.DB != nil {
		if counts, err := p.App.DB.GetAllPluginUsageCounts(); err == nil {
			for i := range plugins {
				if c, ok := counts[plugins[i].ID]; ok && c > 0 {
					plugins[i].UsageCount = c
				}
			}
		}
		// 补齐安装 / 更新时间（来源 DB plugins 表），供前端按「最近安装/更新」排序。
		if stamps, err := p.App.DB.GetPluginTimestamps(); err == nil {
			for i := range plugins {
				if ts, ok := stamps[plugins[i].ID]; ok {
					plugins[i].InstalledAt = ts.InstalledAt
					plugins[i].UpdatedAt = ts.UpdatedAt
				}
			}
		}
	}
	return services.Ok(plugins)
}

// ExecutePluginCommand 执行插件命令（前端绑定入口，来源记为 manual）。
func (p *PluginService) ExecutePluginCommand(pluginID, commandID string, input map[string]interface{}) *services.ApiResult {
	return p.ExecutePluginCommandFrom(pluginID, commandID, TriggerManual, input)
}

// ExecutePluginCommandFrom 执行插件命令并记录来源。
//
// trigger 决定两件事：① 审计落哪（执行日志 + 操作流水）；② 是否过「清单声明」门禁
// ——MCP 触发时 commandID 必须在 manifest.commands[] 里，手动/热键路径不查。
// 未知来源按 manual 记，宁可少一个标签，也不要在日志里写进一个没人认识的枚举值。
func (p *PluginService) ExecutePluginCommandFrom(pluginID, commandID, trigger string, input map[string]interface{}) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	switch trigger {
	case TriggerManual, TriggerHotkey, TriggerMCP:
	default:
		trigger = TriggerManual
	}
	// 清单 commands[] 是「给 AI 的契约」：plugin_list 只从这里取可用命令，
	// 但仅靠「不声明」挡不住模型猜 id 硬调（裁剪清单就形同虚设），故 MCP 路径必须回查清单。
	//
	// 只在 MCP 触发时校验：命令面板与插件界面会调用一些未登记在清单里的内部命令
	//（历史插件包尤其多），手动/热键路径上加同一道门禁会当场打断这些界面。
	if trigger == TriggerMCP {
		inst := p.App.PluginMgr.GetPlugin(pluginID)
		if inst == nil {
			// 可能只是还没被拉起：EnsureLoaded 幂等，失败就原样透传，
			// 由它给出准确原因（禁用的插件、清单损坏等）。
			if err := p.App.PluginMgr.EnsureLoaded(pluginID); err != nil {
				return services.FailMsg(err.Error())
			}
			inst = p.App.PluginMgr.GetPlugin(pluginID)
		}
		if inst != nil && !hasDeclaredCommand(inst.Manifest.Commands, commandID) {
			return services.FailMsg(fmt.Sprintf(
				"插件 %s 没有向 AI 声明命令 %q：该命令只对插件界面开放，需要时请用 plugin_open 打开界面由用户操作。"+
					"可用命令见 plugin_list；若这是新版本才有的命令，可能是本地插件包还是旧版，升级插件后再试",
				pluginID, commandID))
		}
	}
	start := time.Now()
	result, err := p.App.PluginMgr.ExecuteCommand(pluginID, commandID, input)
	// 记录执行日志（5.2：忽略错误，不影响主流程）
	p.recordPluginExecLog(pluginID, commandID, trigger, start, result, err)
	// 记录插件使用次数
	if p.App.DB != nil {
		usageKey := "plugin:" + pluginID + "." + commandID
		// 记录插件使用并保留命令面板传入的附加输入（如端口号），避免用空 input 覆盖前端已存的 input
		inputText := ""
		if input != nil {
			if t, ok := input["text"].(string); ok {
				inputText = t
			}
		}
		p.App.DB.RecordUsageEx(usageKey, "plugin", "", "", inputText)
	}
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(result)
}

// hasDeclaredCommand 判断 commandID 是否出现在插件清单的 commands[] 里。
//
// 只认清单声明，不看插件后端是否「实际支持」：宿主不知道插件的内部命令表，
// 能查的契约就这一份。
func hasDeclaredCommand(cmds []plugin.Command, commandID string) bool {
	for _, c := range cmds {
		if c.ID == commandID {
			return true
		}
	}
	return false
}

// recordPluginExecLog 写入一条插件命令执行日志（5.2）
func (p *PluginService) recordPluginExecLog(pluginID, commandID, trigger string, start time.Time, result interface{}, execErr error) {
	if p.App.DB == nil {
		return
	}
	// 跳过只读轮询类命令（如 dup-finder 的 task-status，前端 1 秒一次拉进度）。
	// 这些是进度轮询而非用户操作，落审计日志既无意义又会刷爆 plugin_exec_logs。
	if strings.HasSuffix(commandID, "-status") {
		return
	}
	log := &db.PluginExecLog{
		PluginID:   pluginID,
		CommandID:  commandID,
		Success:    execErr == nil,
		DurationMs: int(time.Since(start).Milliseconds()),
		Trigger:    trigger,
	}
	if execErr != nil {
		log.Error = execErr.Error()
	} else if result != nil {
		if b, mErr := json.Marshal(result); mErr == nil {
			log.Result = string(b)
		} else {
			log.Result = fmt.Sprintf("%v", result)
		}
	}
	if utf8.RuneCountInString(log.Result) > 2000 {
		log.Result = string([]rune(log.Result)[:2000])
	}
	if utf8.RuneCountInString(log.Error) > 2000 {
		log.Error = string([]rune(log.Error)[:2000])
	}
	if err := p.App.DB.AddPluginExecLog(log); err != nil {
		logger.E("写入插件执行日志失败: %v", err)
	}

	// 操作流水：与执行日志同源（同样已跳过 -status 轮询）。
	// 这里要的是插件显示名，直接落到时间线上的是
	// io.github.parieses.database 这种 id 等于没写。
	name := pluginID
	if p.App.PluginMgr != nil {
		if inst := p.App.PluginMgr.GetPlugin(pluginID); inst != nil && inst.Manifest.Name != "" {
			name = inst.Manifest.Name
		}
	}
	p.App.NoteActivity(db.ActivityPluginRun, pluginID+"."+commandID, name)
}

// ListPluginExecLogs 返回最近 limit 条插件命令执行日志（前端历史展示，5.2）
func (p *PluginService) ListPluginExecLogs(limit int) *services.ApiResult {
	if p.App.DB == nil {
		return services.FailMsg("database not initialized")
	}
	logs, err := p.App.DB.ListPluginExecLogs(limit)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(logs)
}

// PluginCatalog 返回插件清单，mode="brief"（默认）只给「该调哪个插件/哪条命令」所需字段。
//
// 为什么不直接给 ListPlugins 的结果：PluginInfo 带 permissions（文件域、域名、shell 规则）、
// capabilities、安装时间与用量，装满一屏就是几千 token，而这些字段对模型决定
// 「调用哪条命令」毫无帮助——真正需要权限细节的是管理页与安装期审计，不是执行方。
// mode="full" 仍返回完整结构，留给需要审计的场景。
func (p *PluginService) PluginCatalog(mode string) *services.ApiResult {
	res := p.ListPlugins()
	if res.Code != 0 || mode == "full" {
		return res
	}
	list, ok := res.Data.([]plugin.PluginInfo)
	if !ok {
		// 类型不符（未来 ListPlugins 换了返回类型）时退回完整结果，
		// 宁可多给字段，也不要给模型一个空清单。
		return res
	}
	out := make([]map[string]interface{}, 0, len(list))
	for _, info := range list {
		cmds := make([]map[string]interface{}, 0, len(info.Commands))
		for _, c := range info.Commands {
			cmd := map[string]interface{}{
				"id":           c.ID,
				"title":        c.Title,
				"acceptsInput": c.AcceptsInput,
			}
			// description / params 只在插件真的写了才给：空字段既占 token，
			// 又会让模型以为「有说明但没写」，反而降低对缺省的容忍。
			if d := strings.TrimSpace(c.Description); d != "" {
				cmd["description"] = d
			}
			if len(c.Params) > 0 {
				cmd["params"] = c.Params
			}
			cmds = append(cmds, cmd)
		}
		out = append(out, map[string]interface{}{
			"id":          info.ID,
			"name":        info.Name,
			"description": info.Description,
			"runtime":     info.Runtime,
			"status":      info.Status,
			"disabled":    info.Disabled,
			"hasFrontend": info.HasFrontend,
			"commands":    cmds,
		})
	}
	return services.Ok(out)
}

// PluginCommandParams 返回某条命令声明的 params（无声明、插件或命令不存在时 ok=false）。
//
// 给 MCP 的 plugin_execute 用：调用前先看 required，缺键就当场报错并说清该填什么，
// 而不是把不完整的 input 交给插件——插件多半会静默用默认值或返回一句模糊错误，
// 事后分不清是宿主丢了参数还是插件自己出错。
//
// 拿不到就返回 false，不把它做成错误：这条只是「更早报错」的增强，
// 不该让一个读不到清单的时序问题挡住命令本身。
func (p *PluginService) PluginCommandParams(pluginID, commandID string) (map[string]interface{}, bool) {
	if p.App.PluginMgr == nil || pluginID == "" || commandID == "" {
		return nil, false
	}
	inst := p.App.PluginMgr.GetPlugin(pluginID)
	if inst == nil {
		return nil, false
	}
	for _, c := range inst.Manifest.Commands {
		if c.ID != commandID {
			continue
		}
		if len(c.Params) == 0 {
			return nil, false
		}
		return c.Params, true
	}
	return nil, false
}

// PluginExecLogs 返回最近执行记录，pluginID 非空时只看该插件。
//
// 结果与错误都截到 400 字：库里单条最多存 2000 字，20 条就是四万字符，整包灌进
// 模型上下文会挤掉真正要判断的东西（成功没有、报的什么错），而这两个判断
// 看开头就够了。需要全文时去插件管理页的历史里看。
func (p *PluginService) PluginExecLogs(pluginID string, limit int) *services.ApiResult {
	if p.App.DB == nil {
		return services.FailMsg("database not initialized")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	logs, err := p.App.DB.ListPluginExecLogsBy(pluginID, limit)
	if err != nil {
		return services.Fail(err)
	}
	for i := range logs {
		logs[i].Result = truncateRunes(logs[i].Result, 400)
		logs[i].Error = truncateRunes(logs[i].Error, 400)
	}
	return services.Ok(logs)
}

// truncateRunes 按字符（非字节）截断，避免把中文/emoji 从中间切断成乱码。
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}
