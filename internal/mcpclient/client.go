package mcpclient

// MCP 客户端（阶段 D）：让内置 AI 能调用外部 MCP server 的工具。
//
// 协议面只实现内置 AI 真正用到的三个动作：initialize / tools/list / tools/call。
// resources、prompts、sampling 这些不做——接进来的只是「一批新工具」，
// 不是「另一个 AI 的全部能力」，多做的每一项都是新的攻击面与新的维护面。
//
// 两种传输：
//   - stdio：起子进程，JSON-RPC 按行分帧走 stdin/stdout（MCP 规范的 stdio 传输）。
//     连接保活：一次 initialize 之后复用到断线为止——Python/Node 系 server
//     冷启动动辄一两秒，每次调用都重启进程会把「查一下状态」变成等半天。
//   - HTTP：Streamable HTTP（POST，响应可能是 JSON 或 SSE）。会话级 Mcp-Session-Id
//     有就带上，丢了就重新握手——无状态 server 很多根本不发这个头。
//
// 并发模型：一个 client 一个互斥锁串行所有请求。MCP 的 tool 调用大多不快，
// 而内置 AI 对同一 server 的并发调用本来就是小概率事件；串行换实现简单，
// 出问题好查。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/sysutil"
)

const (
	mcpProtocolVersion = "2024-11-05"
	// initTimeout 握手超时。stdio server 冷启动（Python 解释器 + 包导入）可能要几秒，
	// 给到 15s；超过这个时间没起来的 server 通常就是起不来了。
	InitTimeout = 15 * time.Second
	// callTimeout 单次工具调用超时。外部 server 的能力五花八门，60s 覆盖
	// 绝大多数「查一下 / 算一下」，也不至于让对话挂死太久。
	callTimeout = 60 * time.Second
	// listTimeout 拉工具清单超时。
	listTimeout = 20 * time.Second
	// MaxToolsPerServer 单 server 工具数上限。工具爆炸是接外部 server 最现实的
	// 故障：模型的 tools[] 一大，请求容易被上游拒绝，token 账单也难看。
	// 一个正常的 server 不会超过这个数；超了说明配置错了（比如接了个网关）。
	MaxToolsPerServer = 128
)

// ErrDisabled server 已被禁用：调用方据此统一报错文案。
var ErrDisabled = errors.New("外部 MCP 服务器已禁用")

// ServerConfig 一个外部 server 的连接配置。
//
// 字段名与 Claude Desktop / Cursor 的 mcp.json 保持一致（command/args/env/url），
// 用户可以直接把现成配置粘过来——「格式兼容」比「功能齐全」更省支持成本。
// command 非空走 stdio，否则 url 非空走 HTTP。
type ServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	// Disabled 临时停用：配置保留、不连接、工具不注册。
	Disabled bool `json:"disabled,omitempty"`
}

// Tool 一个外部工具（tools/list 的结果）。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// CallResult 一次工具调用的文本结果。
type CallResult struct {
	// Text 所有 text 内容块按序拼接。外部 server 可能返回 image/resource 等块，
	// 它们不进文本，只在 Truncated 说明里提一句。
	Text string `json:"text"`
	// IsError server 明确报告的执行失败（isError=true）。传输失败不走这里。
	IsError bool `json:"isError"`
}

// Client 一个外部 server 的会话。
type Client interface {
	// ListTools 拉工具清单。会话断开时自动重连一次。
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool 调用工具。
	CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error)
	// Close 关闭会话（stdio 会杀子进程）。
	Close() error
}

// New 按配置建客户端。不在此连接：连接延迟到第一次使用，server 挂了不影响启动。
func New(name string, cfg ServerConfig) Client {
	if strings.TrimSpace(cfg.Command) != "" {
		return &stdioClient{name: name, cfg: cfg}
	}
	return &httpClient{name: name, cfg: cfg}
}

// -------- JSON-RPC 骨架 --------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  any             `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func rpcID(n int) json.RawMessage {
	b, _ := json.Marshal(n)
	return b
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

type callToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// initializeParams 每种传输都要发的握手请求。
func initializeParams() map[string]any {
	return map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "quickdock", "version": "1"},
	}
}

// -------- stdio 传输 --------

type stdioClient struct {
	name string
	cfg  ServerConfig

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pending map[int]chan rpcResponse
	// broken 连接是否已断。断线后第一次操作先重连再重试。
	broken bool
	closed bool
}

func (c *stdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := c.rpc(ctx, listTimeout, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res toolsListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("tools/list 响应解析失败: %w", err)
	}
	return clampTools(res.Tools), nil
}

func (c *stdioClient) CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := c.rpc(ctx, callTimeout, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var res callToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("tools/call 响应解析失败: %w", err)
	}
	var b strings.Builder
	var nonText int
	for _, blk := range res.Content {
		if blk.Type == "text" || blk.Text != "" {
			b.WriteString(blk.Text)
		} else {
			nonText++
		}
	}
	if nonText > 0 {
		fmt.Fprintf(&b, "\n[另有 %d 个非文本内容块未展示]", nonText)
	}
	return &CallResult{Text: b.String(), IsError: res.IsError}, nil
}

func (c *stdioClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.cmd != nil && c.cmd.Process != nil {
		// 不等优雅退出：MCP server 对 SIGTERM 的处理五花八门，
		// 而它只是个工具进程，杀了就是了。
		_ = c.cmd.Process.Kill()
	}
	c.teardownLocked()
	return nil
}

// teardownLocked 清连接状态。调用方持锁。
func (c *stdioClient) teardownLocked() {
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = nil
	if c.cmd != nil {
		_ = c.cmd.Wait()
		c.cmd = nil
	}
	c.broken = true
}

// rpc 发一个请求并等响应。调用方持锁。
func (c *stdioClient) rpc(ctx context.Context, timeout time.Duration, method string, params any) (json.RawMessage, error) {
	if c.closed {
		return nil, ErrDisabled
	}
	if c.broken || c.cmd == nil {
		if err := c.startLocked(); err != nil {
			return nil, err
		}
	}
	id := nextID()
	req := rpcRequest{JSONRPC: "2.0", ID: rpcID(id), Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	defer delete(c.pending, id)

	if _, err := c.stdin.Write(append(body, '\n')); err != nil {
		// 进程死了：标记断线，下次重连。错误原样给出去，
		// 「重连再试一次」留给调用方决定（工具调用失败自动重试是危险默认值）。
		c.teardownLocked()
		return nil, fmt.Errorf("写入 %s 失败: %w", c.name, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("%s 调用超时（%s）", method, timeout)
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s 连接已断开", c.name)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

var idCounter struct {
	mu sync.Mutex
	n  int
}

func nextID() int {
	idCounter.mu.Lock()
	defer idCounter.mu.Unlock()
	idCounter.n++
	return idCounter.n
}

// startLocked 起子进程并完成握手。调用方持锁。
func (c *stdioClient) startLocked() error {
	cmd := sysutil.Command(c.cfg.Command, c.cfg.Args...)
	if len(c.cfg.Env) > 0 {
		env := cmd.Environ()
		for k, v := range c.cfg.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	cmd.Stderr = nil // 丢弃 stderr：server 的日志不该混进我们的日志
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin 管道: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout 管道: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s 失败: %w", c.cfg.Command, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.pending = map[int]chan rpcResponse{}
	c.broken = false

	// 读协程：按行分帧，把响应分发给等它的人。
	go c.readLoop(stdout)

	// 握手。initialize 响应里还有 serverInfo/capabilities，这里不关心——
	// 能回话就当作连上了，真正的兼容性问题会在 tools/list / tools/call 时暴露。
	if _, err := c.rpc(context.Background(), InitTimeout, "initialize", initializeParams()); err != nil {
		c.teardownLocked()
		return fmt.Errorf("与 %s 握手失败: %w", c.name, err)
	}
	if err := c.notifyLocked("notifications/initialized", nil); err != nil {
		c.teardownLocked()
		return fmt.Errorf("与 %s 完成握手失败: %w", c.name, err)
	}
	logger.I("[mcp-client] %s 已连接（stdio）", c.name)
	return nil
}

// notifyLocked 发通知（无 id、无响应）。调用方持锁。
func (c *stdioClient) notifyLocked(method string, params any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(body, '\n'))
	return err
}

// readLoop 读子进程输出并分发响应。按行分帧：MCP stdio 传输规定每行一个 JSON 消息，
// 大响应（比如一长串工具清单）也只是长一点的一行，bufio 的默认缓冲会自动扩。
func (c *stdioClient) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue // 空行与非 JSON 噪音（有的 server 往 stdout 打 banner）直接丢
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil || resp.ID == nil {
			continue // 通知/坏行：没有 id 的消息没人等它
		}
		var id int
		if err := json.Unmarshal(resp.ID, &id); err != nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		if !ok {
			c.mu.Unlock()
			continue
		}
		delete(c.pending, id)
		// 先解锁再投递：等的人可能立刻又发起下一个请求。
		c.mu.Unlock()
		ch <- resp
	}
	// 走到这里 = 进程退出 / 管道关闭。
	c.mu.Lock()
	c.teardownLocked()
	c.mu.Unlock()
}

// -------- HTTP（Streamable）传输 --------

type httpClient struct {
	name string
	cfg  ServerConfig

	mu      sync.Mutex
	session string // Mcp-Session-Id，server 给了就带上
	inited  bool
}

func (c *httpClient) ListTools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := c.rpc(ctx, listTimeout, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res toolsListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("tools/list 响应解析失败: %w", err)
	}
	return clampTools(res.Tools), nil
}

func (c *httpClient) CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := c.rpc(ctx, callTimeout, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var res callToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("tools/call 响应解析失败: %w", err)
	}
	var b strings.Builder
	for _, blk := range res.Content {
		b.WriteString(blk.Text)
	}
	return &CallResult{Text: b.String(), IsError: res.IsError}, nil
}

func (c *httpClient) Close() error { return nil } // HTTP 无进程可关

// rpc 发一个 JSON-RPC 请求。响应体可能是 application/json 或 SSE 帧，
// 两种都接：Streamable HTTP 允许 server 自由选择。调用方持锁。
func (c *httpClient) rpc(ctx context.Context, timeout time.Duration, method string, params any) (json.RawMessage, error) {
	if !c.inited {
		if err := c.initLocked(ctx); err != nil {
			return nil, err
		}
	}
	id := nextID()
	raw, retry, err := c.post(ctx, timeout, rpcRequest{JSONRPC: "2.0", ID: rpcID(id), Method: method, Params: params})
	if err != nil && retry {
		// 会话可能过期了（server 重启）。重新握手再试一次，只试一次——
		// 连续两次失败说明是配置或 server 本身的问题，重试无意义。
		c.inited = false
		if ierr := c.initLocked(ctx); ierr != nil {
			return nil, err // 报原始错误，握手错误只会更迷惑
		}
		raw, retry, err = c.post(ctx, timeout, rpcRequest{JSONRPC: "2.0", ID: rpcID(id), Method: method, Params: params})
	}
	if err != nil {
		return nil, err
	}
	if retry {
		return nil, fmt.Errorf("%s: 响应不含本请求的结果", method)
	}
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%s 响应解析失败: %w", method, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, resp.Error.Message)
	}
	return resp.Result, nil
}

// initLocked HTTP 握手。调用方持锁。
func (c *httpClient) initLocked(ctx context.Context) error {
	id := nextID()
	raw, retry, err := c.post(ctx, InitTimeout, rpcRequest{JSONRPC: "2.0", ID: rpcID(id), Method: "initialize", Params: initializeParams()})
	if err != nil {
		return fmt.Errorf("与 %s 握手失败: %w", c.name, err)
	}
	if retry {
		return fmt.Errorf("与 %s 握手失败：响应不含结果", c.name)
	}
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("握手响应解析失败: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("与 %s 握手失败: %s", c.name, resp.Error.Message)
	}
	// initialized 通知不带 id，server 不会回。失败不回滚——多数 server 不在意。
	ntf, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	_, _, _ = c.post(ctx, 3*time.Second, json.RawMessage(ntf))
	c.inited = true
	logger.I("[mcp-client] %s 已连接（HTTP）", c.name)
	return nil
}

// post 发一次 POST。返回 (响应体, 需要重试(会话失效), 错误)。调用方持锁。
func (c *httpClient) post(ctx context.Context, timeout time.Duration, payload any) (json.RawMessage, bool, error) {
	if strings.TrimSpace(c.cfg.URL) == "" {
		return nil, false, errors.New("未配置 url")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.session = sid
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// 会话丢了：让调用方重新握手。
		c.session = ""
		return nil, true, errors.New("server 返回 404（会话可能已过期）")
	case resp.StatusCode >= 400:
		return nil, false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	ctype := resp.Header.Get("Content-Type")
	if strings.Contains(ctype, "text/event-stream") {
		return readSSE(resp.Body)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	return json.RawMessage(raw), false, nil
}

// readSSE 从 SSE 流里取出第一个带 id 的 JSON-RPC 响应。
// Streamable HTTP 允许 server 用 SSE 返回单个响应（流的其余部分是进度通知等），
// 拿到响应就收——长驻流需要单独的心跳管理，工具调用用不上。
func readSSE(r io.Reader) (json.RawMessage, bool, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			continue
		}
		if resp.ID == nil {
			continue // 进度通知等，不是本次调用的响应
		}
		return json.RawMessage(data), false, nil
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, errors.New("SSE 流在收到响应前结束")
}

// clampTools 截断工具数并留痕。超出上限的静默丢弃会让用户对着清单数数，
// 一行说明比一个数字有用。
func clampTools(tools []Tool) []Tool {
	if len(tools) <= MaxToolsPerServer {
		return tools
	}
	logger.W("[mcp-client] 工具数 %d 超过上限 %d，已截断", len(tools), MaxToolsPerServer)
	return tools[:MaxToolsPerServer]
}

// ValidServerName server 名字约束：注册进内置注册表时要拼进工具名
// （mcp_<server>_<tool>），所以只允许安全字符。
var validServerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)

// ValidateConfig 建连接前的合法性检查。错误消息面向填写配置的用户。
func ValidateConfig(name string, cfg ServerConfig) error {
	if !validServerName.MatchString(name) {
		return errors.New("名称只能用字母、数字、下划线、连字符，且以字母或数字开头（最长 32 字符）")
	}
	cmd := strings.TrimSpace(cfg.Command)
	url := strings.TrimSpace(cfg.URL)
	if cmd == "" && url == "" {
		return errors.New("需要 command（本地命令）或 url（HTTP 地址）之一")
	}
	if cmd != "" && url != "" {
		return errors.New("command 与 url 只能填一个")
	}
	return nil
}
