// Package mcp 实现 QuickDock 自带的 Model Context Protocol 服务端。
//
// 与 Wails 框架内置的 MCP（//go:build mcp，编译期 tag、App.Run 内自动启动、只做 UI 自动化）
// 不同，本实现：
//   - 常驻在二进制里但默认不监听，由环境管理页手动启停；
//   - 暴露的是 QuickDock 的业务能力（环境/工作空间/待办/笔记/剪贴板等），不是窗口与 DOM；
//   - 零外部依赖，只实现 Streamable HTTP 传输所需的最小方法集。
//
// 分层约束：本包不 import services（业务工具由 services/mcp 注册进来），避免 internal → services 反向依赖。
package mcp

import "encoding/json"

// ProtocolVersion 实现的 MCP 协议版本（2025-06-18 修订，Streamable HTTP 传输）。
const ProtocolVersion = "2025-06-18"

// rpcRequest JSON-RPC 2.0 请求。ID 为 nil 表示通知（notification，不回响应体）。
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// isNotification 判断请求是否为通知：无 id 字段（或显式 null）。
func (r *rpcRequest) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// toolCallParams tools/call 的参数
type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// toolDefinition tools/list 返回的工具描述
type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolResult tools/call 的结果。IsError 为 true 时 Text 为错误信息（区别于协议级错误）。
type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
