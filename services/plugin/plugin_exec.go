package plugin

import "quickdock/services"

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/internal/logger"
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
	}
	return services.Ok(plugins)
}

func (p *PluginService) ExecutePluginCommand(pluginID, commandID string, input map[string]interface{}) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	start := time.Now()
	result, err := p.App.PluginMgr.ExecuteCommand(pluginID, commandID, input)
	// 记录执行日志（5.2：忽略错误，不影响主流程）
	p.recordPluginExecLog(pluginID, commandID, "manual", start, result, err)
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

// recordPluginExecLog 写入一条插件命令执行日志（5.2）
func (p *PluginService) recordPluginExecLog(pluginID, commandID, trigger string, start time.Time, result interface{}, execErr error) {
	if p.App.DB == nil {
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
