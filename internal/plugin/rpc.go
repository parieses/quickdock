package plugin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strconv"
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

	// 注册 pending 请求（以 id 的 JSON 文本为键，兼容 string/number id）
	inst.readMu.Lock()
	inst.NextID++
	id := inst.NextID
	idKey := strconv.FormatInt(id, 10)
	ch := make(chan *RPCResponse, 1)
	inst.Pending[idKey] = ch
	inst.readMu.Unlock()

	// 构建请求
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		inst.readMu.Lock()
		delete(inst.Pending, idKey)
		inst.readMu.Unlock()
		return nil, fmt.Errorf("序列化参数失败: %w", err)
	}
	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(idKey),
		Method:  method,
		Params:  paramsJSON,
	}

	data, err := json.Marshal(req)
	if err != nil {
		inst.readMu.Lock()
		delete(inst.Pending, idKey)
		inst.readMu.Unlock()
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}
	data = append(data, '\n')

	// 串行写入 stdin ← P0 修复：sendMu 防止多协程写入交错
	// 写操作带超时：插件活着但不读 stdin 时管道写满会永久阻塞，而 select 里的
	// 30s 超时根本到不了（Write 同步阻塞）。用 goroutine+select 复用 SendNotification 的做法。
	inst.sendMu.Lock()
	writeDone := make(chan error, 1)
	go func() {
		_, werr := inst.Stdin.Write(data)
		writeDone <- werr
	}()
	select {
	case werr := <-writeDone:
		inst.sendMu.Unlock()
		if werr != nil {
			inst.readMu.Lock()
			delete(inst.Pending, idKey)
			inst.readMu.Unlock()
			return nil, fmt.Errorf("写入插件 stdin 失败: %w", werr)
		}
	case <-time.After(2 * time.Second):
		inst.sendMu.Unlock()
		inst.readMu.Lock()
		delete(inst.Pending, idKey)
		inst.readMu.Unlock()
		return nil, fmt.Errorf("写入插件 stdin 超时（插件无响应）")
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
		delete(inst.Pending, idKey)
		inst.readMu.Unlock()
		return nil, ErrResponseTimeout
	case <-inst.doneCh:
		inst.readMu.Lock()
		delete(inst.Pending, idKey)
		inst.readMu.Unlock()
		return nil, ErrPluginCrashed
	}
}

// maxPluginLineBytes 限制单条插件 stdout 行的最大字节数。
// 用 bufio.Reader 而非 Scanner（Scanner 即使放大 buffer 仍有 1MB 单行硬上限，
// 插件返回大 base64/文件内容 >1MB 会触发 ErrTooLong → readLoop 退出 → 实例被标 crashed → 重启循环），
// 但同时给出一个较大但有限的上限，防止异常/恶意插件无限输出导致宿主机内存无界增长。
const maxPluginLineBytes = 64 << 20 // 64 MiB

// boundedReadLine 从 r 读取一行（含行尾的 '\n'），但把单行累计长度限制在 limit 内。
// 若单行长度超过 limit，返回已读到的前缀（截断）以保证调用方不阻塞，同时避免无界内存增长。
// 返回 (数据, 是否截断, error)；err 语义与 bufio.ReadString 一致。
func boundedReadLine(r *bufio.Reader, limit int) ([]byte, bool, error) {
	var buf []byte
	for {
		chunk, err := r.ReadBytes('\n')
		if len(chunk) > 0 {
			// 本行累计已超过上限 → 截断丢弃行的剩余部分直到读走换行，返回已积累的前缀
			if len(buf)+len(chunk) > limit {
				// 丢弃完整行，避免在该行剩余部分反复分配
				if err == nil {
					// chunk 以换行结尾，说明该行已完整读入，只需保留前缀
					maxCopy := limit - len(buf)
					if maxCopy > len(chunk) {
						maxCopy = len(chunk)
					}
					buf = append(buf, chunk[:maxCopy]...)
					return buf, true, nil
				}
				// err != nil 且还没读到换行，保留前缀后返回截断标志
				maxCopy := limit - len(buf)
				if maxCopy > len(chunk) {
					maxCopy = len(chunk)
				}
				buf = append(buf, chunk[:maxCopy]...)
				return buf, true, err
			}
			buf = append(buf, chunk...)
			// 已读到换行 → 正常单行返回
			if err == nil {
				return buf, false, nil
			}
			// 读到 EOF 但仍有数据（最后一行无换行），ReadBytes 返回数据 + io.EOF
			return buf, false, err
		}
		if err != nil {
			return buf, false, err
		}
	}
}

// readLoop 后台循环读取插件 stdout
// 必须在子进程启动后以 goroutine 方式运行
func (inst *PluginInstance) readLoop(manager *Manager) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[plugin %s] readLoop panic: %v\n", inst.Manifest.ID, r)
			inst.SetStatus("crashed")
		}
	}()
	// 就绪信号 ← P0 修复：确保 readLoop 已开始监听再发送 initialize
	close(inst.readyCh)

	reader := bufio.NewReaderSize(inst.Stdout, 64*1024)

	for {
		line, truncated, err := boundedReadLine(reader, maxPluginLineBytes)
		if truncated {
			fmt.Printf("[plugin %s] stdout 单行超过 %d 字节，已截断\n", inst.Manifest.ID, maxPluginLineBytes)
			// 截断后的行无法解析为合法 JSON-RPC，直接走到 err 分支继续循环
		}
		if len(line) > 0 {
			// 去掉行尾换行（兼容 \r\n）
			lb := line
			if n := len(lb); n > 0 && lb[n-1] == '\n' {
				lb = lb[:n-1]
				if n = len(lb); n > 0 && lb[n-1] == '\r' {
					lb = lb[:n-1]
				}
			}
			if len(lb) > 0 {
				b := lb
				// 先尝试解析为请求（包含 method 字段）
				var req RPCRequest
				if jerr := json.Unmarshal(b, &req); jerr == nil && req.Method != "" {
					// 这是插件发起的回调请求或通知
					if manager != nil {
						manager.handleCallback(inst, &req, b)
					}
				} else {
					// 再尝试解析为响应
					var resp RPCResponse
					if jerr := json.Unmarshal(b, &resp); jerr != nil {
						// 无法解析的 stdout 行，静默忽略（插件自己的调试打印不应干扰通信协议）
						// 如需调试可取消下行注释：
						// fmt.Printf("QuickDock [plugin %s debug]: %s\n", inst.Manifest.ID, string(lb))
					} else {
						// 匹配 pending 请求（id 以 JSON 文本为键，兼容 string/number id）
						inst.readMu.Lock()
						if ch, ok := inst.Pending[string(resp.ID)]; ok {
							ch <- &resp
							delete(inst.Pending, string(resp.ID))
						}
						inst.readMu.Unlock()
					}
				}
			}
		}
		if err != nil {
			break
		}
	}

	// scanner 退出说明进程结束或 stdout 关闭
	inst.closeOnce.Do(func() {
		close(inst.doneCh)
	})
	if !inst.stopped.Load() {
		inst.SetStatus("crashed")
	}

	// 回收已退出的子进程句柄：崩溃场景下 readLoop 先于 stopPlugin 发现进程结束，
	// 若不及时 Wait，句柄/僵尸会累积到下次 stopPlugin 才回收。进程已结束时 Wait 立即返回；
	// 若 stdout 关闭但进程尚存（罕见），在独立 goroutine 中阻塞等待，由后续 stopPlugin(Kill+Wait) 兜底。
	// 第二次 Wait（stopPlugin 中）返回 ErrProcessDone，安全。
	if inst.Cmd != nil && inst.Cmd.Process != nil {
		go func() { _ = inst.Cmd.Wait() }()
	}
}

// waitForExit 等待子进程退出（通过 doneCh 信号，不自行调用 Cmd.Wait 避免双重 Wait）
func (inst *PluginInstance) waitForExit() {
	<-inst.doneCh
}

// SendNotification 发送 JSON-RPC 通知（无需响应）
func (inst *PluginInstance) SendNotification(method string, params interface{}) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("序列化通知参数失败: %w", err)
	}
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
	// 写入带超时：插件挂死不读 stdin 时管道写满会永久阻塞，
	// 而 stopPlugin 在持有管理器锁时调用本方法，无超时会导致整个插件管理器（含 ShutdownAll）死锁
	writeDone := make(chan error, 1)
	go func() {
		_, err := inst.Stdin.Write(data)
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			return fmt.Errorf("写入插件 stdin 失败: %w", err)
		}
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("写入插件 stdin 超时（插件无响应，将强制终止）")
	}
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
func MakeResponse(id json.RawMessage, result interface{}) []byte {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
	}
	resp.Result, _ = json.Marshal(result)
	data, _ := json.Marshal(resp)
	return append(data, '\n')
}

// MakeError 构建 JSON-RPC 错误响应（用于插件开发辅助）
func MakeError(id json.RawMessage, code int, message string) []byte {
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
		ID:      json.RawMessage(strconv.FormatInt(id, 10)),
		Method:  method,
		Params:  paramsJSON,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
