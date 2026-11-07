package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---- 宿主 MCP 工具调用权限（host.mcp.call）----
//
// permissions.mcp 支持两种写法：
//
//	"mcp": true                                  全放行（仍受等级门约束）
//	"mcp": ["note_search", "clipboard_search"]   仅列表内的工具可调用
//
// 为什么必须显式声明：host.mcp.call 直接复用宿主内置 MCP 工具表，其中
// LevelWrite 一档含 env_stop / clipboard_copy / plugin_execute 等写操作。
// 过去 checkPermission 没有 mcp 分支，落到末尾的 return nil —— 声明零权限的
// 插件也能随意调用这些工具，等于绕过整张 permissions 表（等级门只挡 LevelRisk，
// 且用户可能把 MCP 等级调到最高）。
//
// 现在是三道独立闸：声明（这里）→ 等级门（mcp.Call 的 maxLvl）→ 插件路径
// 对 LevelRisk 的硬拒（services/plugin/plugin_host.go）。任何一道都不替代另一道。

// MCPPerm 宿主 MCP 工具调用权限
type MCPPerm struct {
	All   bool     // 全放行（mcp: true）
	Tools []string // 工具名白名单（mcp: [...]）
}

// UnmarshalJSON 兼容 bool 与数组两种写法，与 network/shell/process 同范式。
func (p *MCPPerm) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "", "null", "false":
		*p = MCPPerm{}
		return nil
	case "true":
		*p = MCPPerm{All: true}
		return nil
	}

	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf(`permissions.mcp 必须是 true/false 或 ["note_search", "clipboard_search"]: %w`, err)
	}
	*p = MCPPerm{Tools: arr}
	return nil
}

// MarshalJSON 与输入对称。零值写回 false 而不是 null 或 {}：前端 permTags 用
// 真值过滤权限标签，结构体零值若序列化成对象会变成 truthy，给每个插件挂一个
// 并不存在的「mcp」标签。
func (p MCPPerm) MarshalJSON() ([]byte, error) {
	if p.All {
		return json.Marshal(true)
	}
	if len(p.Tools) > 0 {
		return json.Marshal(p.Tools)
	}
	return json.Marshal(false)
}

// Granted 是否声明了任何 MCP 调用能力
func (p MCPPerm) Granted() bool {
	return p.All || len(p.Tools) > 0
}

// Validate 清单加载时的形式校验：白名单条目必须非空，避免插件带空串静默全拒。
func (p MCPPerm) Validate() error {
	if p.All {
		return nil
	}
	for _, t := range p.Tools {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("%w: permissions.mcp 存在空条目", ErrInvalidManifest)
		}
	}
	return nil
}

// AllowsTool 判断工具名是否在许可范围内。
// 全放行直接 true；白名单按工具名忽略大小写精确匹配（不做前缀通配——
// 一个前缀就能放行整个域的工具，粒度太粗）。
func (p MCPPerm) AllowsTool(name string) bool {
	if p.All {
		return true
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, t := range p.Tools {
		if strings.EqualFold(strings.TrimSpace(t), name) {
			return true
		}
	}
	return false
}
