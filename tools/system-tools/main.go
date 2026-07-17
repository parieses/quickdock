package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// ---- JSON-RPC structures ----

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---- Execute params ----

type ExecuteParams struct {
	Command string                 `json:"command"`
	Input   map[string]interface{} `json:"input"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req RPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		handleRequest(req)
	}
}

func handleRequest(req RPCRequest) {
	switch req.Method {
	case "initialize":
		respond(req.ID, map[string]interface{}{
			"status": "ready",
			"name":   "QuickDock System Tools",
		})
	case "host.ping":
		respond(req.ID, map[string]interface{}{"pong": true})
	case "plugin.execute":
		handleExecute(req)
	default:
		respondError(req.ID, -32601, "unknown method: "+req.Method)
	}
}

func handleExecute(req RPCRequest) {
	var params ExecuteParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		respondError(req.ID, -32602, "invalid params: "+err.Error())
		return
	}

	cmd := params.Command

	// 兼容前端 pluginExec 打包格式：input = {text: JSON.stringify(实际参数)}
	// 自动解包，使各 handler 能直接访问 input["ssid"]、input["port"] 等
	input := params.Input
	if textRaw, ok := input["text"].(string); ok && textRaw != "" {
		var nested map[string]interface{}
		if strings.HasPrefix(textRaw, "{") || strings.HasPrefix(textRaw, "[") {
			if err := json.Unmarshal([]byte(textRaw), &nested); err == nil {
				for k, v := range nested {
					if _, exists := input[k]; !exists {
						input[k] = v
					}
				}
			}
		}
	}

	switch {
	case strings.HasPrefix(cmd, "hosts-"):
		handleHostsCommand(req.ID, cmd, input)
	case strings.HasPrefix(cmd, "port-"):
		handlePortCommand(req.ID, cmd, input)
	case strings.HasPrefix(cmd, "wifi-"):
		handleWifiCommand(req.ID, cmd, input)
	default:
		respondError(req.ID, -32601, "unknown command: "+cmd)
	}
}

func respond(id int64, result interface{}) {
	data, _ := json.Marshal(RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  mustMarshal(result),
	})
	data = append(data, '\n')
	os.Stdout.Write(data)
}

func respondError(id int64, code int, msg string) {
	data, _ := json.Marshal(RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	})
	data = append(data, '\n')
	os.Stdout.Write(data)
}

func mustMarshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// hiddenCmd 创建一个不弹 CMD 窗口的 exec.Command（父进程是 GUI 类型时，
// 启动 netsh/netstat/tasklist/taskkill 等控制台子进程会弹出 CMD 窗口）
// visibleCmd 创建一个正常显示的 exec.Command（用于需要显式控制台交互的命令）
func hiddenCmd(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
