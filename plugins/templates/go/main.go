package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// JSON-RPC 2.0 结构
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
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
		var req RPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}

		// ---- 方法分发 ----
		switch req.Method {
		case "initialize":
			// 主程序发来的初始化请求
			respond(req.ID, map[string]string{
				"status":   "ready",
				"pluginId": "com.quickdock.my-go-plugin",
			})

		case "plugin.execute":
			// 用户执行插件命令
			var params struct {
				Command string                 `json:"command"`
				Input   map[string]interface{} `json:"input"`
			}
			json.Unmarshal(req.Params, &params)

			switch params.Command {
			case "hello":
				name := "World"
				if params.Input != nil {
					if n, ok := params.Input["name"]; ok {
						name = fmt.Sprintf("%v", n)
					}
				}
				respond(req.ID, map[string]string{
					"message": fmt.Sprintf("Hello, %s! 👋", name),
				})
			default:
				respondError(req.ID, -10001, "未知命令: "+params.Command)
			}

		case "shutdown":
			// 主程序要求关闭
			respond(req.ID, map[string]string{"status": "bye"})
			os.Exit(0)
		}
	}
}

func respond(id int64, result interface{}) {
	resp := RPCResponse{JSONRPC: "2.0", ID: id, Result: result}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(data))
}

func respondError(id int64, code int, message string) {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(data))
}
