package plugin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"time"
)

// Call 发送 JSON-RPC 请求并等待响应
// method: 方法名（如 "initialize"、"plugin.execute"）
// params: 参数（会被序列化为 JSON）
// timeout: 超时时间（0 使用默认值）
func (inst *PluginInstance) Call(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
	// 检查进程是否已退出
	select {
	case <-inst.doneCh:
		return nil, ErrPluginCrashed
	default:
	}

	// 注册 pending 请求
	inst.readMu.Lock()
	inst.NextID++
	id := inst.NextID
	ch := make(chan *RPCResponse, 1)
	inst.Pending[id] = ch
	inst.readMu.Unlock()

	// 构建请求
	paramsJSON, _ := json.Marshal(params)
	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  paramsJSON,
	}

	data, err := json.Marshal(req)
	if err != nil {
		inst.readMu.Lock()
		delete(inst.Pending, id)
		inst.readMu.Unlock()
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}
	data = append(data, '\n')

	// 串行写入 stdin ← P0 修复：sendMu 防止多协程写入交错
	inst.sendMu.Lock()
	_, err = inst.Stdin.Write(data)
	inst.sendMu.Unlock()

	if err != nil {
		inst.readMu.Lock()
		delete(inst.Pending, id)
		inst.readMu.Unlock()
		return nil, fmt.Errorf("写入插件 stdin 失败: %w", err)
	}

	// 默认超时
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// 等待响应
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-time.After(timeout):
		inst.readMu.Lock()
		delete(inst.Pending, id)
		inst.readMu.Unlock()
		return nil, ErrResponseTimeout
	case <-inst.doneCh:
		inst.readMu.Lock()
		delete(inst.Pending, id)
		inst.readMu.Unlock()
		return nil, ErrPluginCrashed
	}
}

// readLoop 后台循环读取插件 stdout
// 必须在子进程启动后以 goroutine 方式运行
func (inst *PluginInstance) readLoop(manager *Manager) {
	// 就绪信号 ← P0 修复：确保 readLoop 已开始监听再发送 initialize
	close(inst.readyCh)

	scanner := bufio.NewScanner(inst.Stdout)
	// 1MB buffer 应对大响应
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// 先尝试解析为请求（包含 method 字段）
		var req RPCRequest
		if err := json.Unmarshal(line, &req); err == nil && req.Method != "" {
			// 这是插件发起的回调请求或通知
			if manager != nil {
				manager.handleCallback(inst, &req)
			}
			continue
		}

		// 再尝试解析为响应
		var resp RPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			// 无法解析的 stdout 行，忽略（可能是插件自己的日志）
			continue
		}

		// 匹配 pending 请求
		inst.readMu.Lock()
		if ch, ok := inst.Pending[resp.ID]; ok {
			ch <- &resp
			delete(inst.Pending, resp.ID)
		}
		inst.readMu.Unlock()
	}

	// scanner 退出说明进程结束或 stdout 关闭
	inst.closeOnce.Do(func() {
		close(inst.doneCh)
	})
	inst.Status = "crashed"
}

// waitForExit 等待子进程退出，确保 doneCh 被关闭
// 必须在子进程启动后以 goroutine 方式运行
func (inst *PluginInstance) waitForExit() {
	if inst.Cmd == nil {
		return
	}
	_ = inst.Cmd.Wait()

	inst.closeOnce.Do(func() {
		close(inst.doneCh)
	})
	inst.Status = "crashed"
}

// SendNotification 发送 JSON-RPC 通知（无需响应）
func (inst *PluginInstance) SendNotification(method string, params interface{}) error {
	paramsJSON, _ := json.Marshal(params)
	req := RPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("序列化通知失败: %w", err)
	}
	data = append(data, '\n')

	inst.sendMu.Lock()
	defer inst.sendMu.Unlock()
	_, err = inst.Stdin.Write(data)
	if err != nil {
		return fmt.Errorf("写入插件 stdin 失败: %w", err)
	}
	return nil
}

// Close 关闭插件通信管道
func (inst *PluginInstance) Close() {
	inst.sendMu.Lock()
	defer inst.sendMu.Unlock()
	if inst.Stdin != nil {
		inst.Stdin.Close()
	}
	// stdout 由 readLoop 持有，不需要在此关闭
}

// ---- 辅助函数 ----

// MakeResponse 构建 JSON-RPC 成功响应（用于插件开发辅助）
func MakeResponse(id int64, result interface{}) []byte {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
	}
	resp.Result, _ = json.Marshal(result)
	data, _ := json.Marshal(resp)
	return append(data, '\n')
}

// MakeError 构建 JSON-RPC 错误响应（用于插件开发辅助）
func MakeError(id int64, code int, message string) []byte {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
	}
	data, _ := json.Marshal(resp)
	return append(data, '\n')
}

// MakeRequest 构建 JSON-RPC 请求（用于单元测试/模拟）
func MakeRequest(method string, id int64, params interface{}) ([]byte, error) {
	paramsJSON, _ := json.Marshal(params)
	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  paramsJSON,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
