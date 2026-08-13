package services

import (
	"encoding/json"
	"fmt"
	"time"

	"quickdock/internal/db"
)

func (a *AppService) ListPlugins() *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	plugins := a.PluginMgr.ListPlugins()
	// 从 usage_frecency 表查询每个插件的使用次数（一条 SQL 聚合全部，替代逐条查询）
	if a.DB != nil {
		if counts, err := a.DB.GetAllPluginUsageCounts(); err == nil {
			for i := range plugins {
				if c, ok := counts[plugins[i].ID]; ok && c > 0 {
					plugins[i].UsageCount = c
				}
			}
		}
	}
	return Ok(plugins)
}

func (a *AppService) ExecutePluginCommand(pluginID, commandID string, input map[string]interface{}) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	start := time.Now()
	result, err := a.PluginMgr.ExecuteCommand(pluginID, commandID, input)
	// 记录执行日志（5.2：忽略错误，不影响主流程）
	a.recordPluginExecLog(pluginID, commandID, "manual", start, result, err)
	// 记录插件使用次数
	if a.DB != nil {
		usageKey := "plugin:" + pluginID + "." + commandID
		// 记录插件使用并保留命令面板传入的附加输入（如端口号），避免用空 input 覆盖前端已存的 input
		inputText := ""
		if input != nil {
			if t, ok := input["text"].(string); ok {
				inputText = t
			}
		}
		a.DB.RecordUsageEx(usageKey, "plugin", "", "", inputText)
	}
	if err != nil {
		return Fail(err)
	}
	return Ok(result)
}

// recordPluginExecLog 写入一条插件命令执行日志（5.2）
func (a *AppService) recordPluginExecLog(pluginID, commandID, trigger string, start time.Time, result interface{}, execErr error) {
	if a.DB == nil {
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
	if len(log.Result) > 2000 {
		log.Result = log.Result[:2000]
	}
	if len(log.Error) > 2000 {
		log.Error = log.Error[:2000]
	}
	if err := a.DB.AddPluginExecLog(log); err != nil {
		fmt.Printf("QuickDock: 写入插件执行日志失败: %v\n", err)
	}
}

// ListPluginExecLogs 返回最近 limit 条插件命令执行日志（前端历史展示，5.2）
func (a *AppService) ListPluginExecLogs(limit int) *ApiResult {
	if a.DB == nil {
		return FailMsg("database not initialized")
	}
	logs, err := a.DB.ListPluginExecLogs(limit)
	if err != nil {
		return Fail(err)
	}
	return Ok(logs)
}
