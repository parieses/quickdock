// QuickDock Native 插件模板 — Go 语言
//
// 协议：JSON-RPC 2.0 (stdin/stdout)
// 生命周期：
//   1. QuickDock 启动子进程（my-plugin.exe）
//   2. 发 {"method":"initialize"} → 子进程返回 {"status":"ready"}
//   3. 每 5 秒发 {"method":"host.ping"} → 子进程返回 {"pong":true}
//   4. 用户执行命令时发 {"method":"plugin.execute", "params":{"command":"...", "input":{...}}}
//   5. 调 host.ai.ask 时若带 streamId，宿主会持续下发 {"method":"host.ai.delta",
//      "params":{"streamId":"...","delta":"..."}} 通知（无 id，不需要回复）。
//      注意：本模板是单线程 stdin 循环，会在 plugin.execute 返回后才读到这些通知；
//      要真正"边生成边显示"，需另起 goroutine 读 stdin，按 streamId 分发给 UI。
//   6. QuickDock 退出时发 {"method":"shutdown"} 或关闭 stdin
//
// 编译: go build -o my-plugin.exe main.go

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req RPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			sendError(0, -32700, "Parse error: "+err.Error())
			continue
		}

		handleRequest(req)
	}
}

func handleRequest(req RPCRequest) {
	switch req.Method {
	case "initialize":
		sendResult(req.ID, map[string]interface{}{
			"status":  "ready",
			"name":    "My Plugin",
			"version": "0.1.0",
		})

	case "host.ping":
		sendResult(req.ID, map[string]interface{}{"pong": true})

	case "host.shutdown":
		os.Exit(0)

	case "host.ai.delta":
		// AI 流式增量（宿主下发的通知，没有 id，不需要回复）。
		// 模板不消费增量，读到这里直接丢弃；真实插件应在这里把 delta 刷到自己的 UI 上。
		return

	case "plugin.execute":
		var params struct {
			Command string                 `json:"command"`
			Input   map[string]interface{} `json:"input"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			sendError(req.ID, -32602, "Invalid params: "+err.Error())
			return
		}
		handleExecute(req.ID, params.Command, params.Input)

	default:
		sendError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func handleExecute(id int64, command string, input map[string]interface{}) {
	switch command {
	case "do-something":
		text := ""
		if input != nil {
			if t, ok := input["text"].(string); ok {
				text = t
			}
		}
		sendResult(id, map[string]interface{}{
			"text":    "Hello from Native plugin! Input: " + text,
			"display": "接收到输入: " + text,
		})

	default:
		sendError(id, -32601, "unknown command: "+command)
	}
}

func sendResult(id int64, result interface{}) {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	data, _ := json.Marshal(resp)
	fmt.Println(string(data))
}

func sendError(id int64, code int, message string) {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
	data, _ := json.Marshal(resp)
	fmt.Println(string(data))
}
