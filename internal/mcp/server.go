package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
)

// DefaultPort MCP 默认监听端口。刻意避开 9099（Wails 内置 MCP 的默认值），两者可共存。
const DefaultPort = 9230

// Server MCP 服务端。宿主进程内单例，由环境管理页启停。
type Server struct {
	mu     sync.Mutex
	srv    *http.Server
	ln     net.Listener
	addr   string
	ver    string
	sessID string
}

// Default 进程内唯一实例：环境管理（启停/状态）与业务工具注册共用。
var Default = &Server{}

// Start 在 127.0.0.1 上监听 port（0=由系统分配空闲端口），返回实际监听地址。
func (s *Server) Start(port int, version string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return s.addr, errors.New("MCP 服务已在运行")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		return "", err
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.ver = version
	s.sessID = "qd-" + randHex(8)
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(s.handle),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.E("[mcp] 监听异常: %v", err)
		}
	}()
	logger.I("[mcp] 服务已启动 url=http://%s/mcp", s.addr)
	return s.addr, nil
}

// Stop 关闭监听。未运行时返回 nil。
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	err := s.srv.Close()
	s.srv, s.ln, s.addr, s.sessID = nil, nil, "", ""
	logger.I("[mcp] 服务已停止")
	return err
}

// Running 是否在运行
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

// Addr 返回实际监听地址（host:port），未运行时为空串。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Port 返回实际监听端口，未运行返回 0。
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return 0
	}
	if tcp, ok := s.ln.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}

// Endpoint 返回客户端配置用的完整地址，未运行返回空串。
func (s *Server) Endpoint() string {
	a := s.Addr()
	if a == "" {
		return ""
	}
	return "http://" + a + "/mcp"
}

// handle 统一入口：POST /mcp 走 JSON-RPC，GET /mcp 返回 405（本实现不做 SSE 推流），
// GET / 返回服务自描述，便于用户用浏览器确认是否活着。
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if !localOrigin(r) {
		http.Error(w, "forbidden: cross-origin request rejected", http.StatusForbidden)
		return
	}
	switch {
	case r.URL.Path == "/mcp" && r.Method == http.MethodPost:
		s.handleRPC(w, r)
	case r.URL.Path == "/mcp" && r.Method == http.MethodDelete:
		// 客户端显式结束会话；本实现无会话状态，直接确认。
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"service":  "quickdock-mcp",
			"endpoint": s.Endpoint(),
			"tools":    len(List()),
		})
	case r.URL.Path == "/mcp" && r.Method == http.MethodGet:
		http.Error(w, "本服务仅支持 Streamable HTTP 的 POST 通道", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: nil, Error: &rpcError{Code: codeParseError, Message: "读取请求体失败"}})
		return
	}
	// 单请求 or 批量数组
	var batch []rpcRequest
	single := rpcRequest{}
	if strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		if err := json.Unmarshal(body, &batch); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: err.Error()}})
			return
		}
	} else {
		if err := json.Unmarshal(body, &single); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: err.Error()}})
			return
		}
		batch = []rpcRequest{single}
	}

	results := make([]rpcResponse, 0, len(batch))
	for i := range batch {
		req := batch[i]
		if req.Method == "" {
			results = append(results, errResp(req.ID, codeInvalidRequest, "缺少 method"))
			continue
		}
		if req.isNotification() {
			// 通知（如 notifications/initialized）：不回响应体
			continue
		}
		res := s.dispatch(req)
		res.JSONRPC = "2.0"
		results = append(results, res)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Session-Id", s.sessionID())
	if len(results) == 0 {
		// 全部是通知：MCP 规范要求 202 Accepted 且无响应体
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if len(batch) == 1 {
		writeJSON(w, http.StatusOK, results[0])
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) dispatch(req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcResponse{ID: req.ID, Result: map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "quickdock", "version": s.version()},
		}}
	case "ping":
		return rpcResponse{ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return rpcResponse{ID: req.ID, Result: map[string]any{"tools": List()}}
	case "tools/call":
		var p toolCallParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return errResp(req.ID, codeInvalidParams, err.Error())
			}
		}
		res, err := Call(p.Name, p.Arguments)
		if err != nil {
			// 工具执行失败按 MCP 约定回 isError 内容块，而非协议错误，便于模型读懂并自我纠正
			logger.W("[mcp] 工具 %s 执行失败: %v", p.Name, err)
			return rpcResponse{ID: req.ID, Result: toolResult{
				Content: []contentBlock{{Type: "text", Text: err.Error()}},
				IsError: true,
			}}
		}
		return rpcResponse{ID: req.ID, Result: toolResult{
			Content: []contentBlock{{Type: "text", Text: JSON(res)}},
		}}
	case "resources/list":
		return rpcResponse{ID: req.ID, Result: map[string]any{"resources": []any{}}}
	case "prompts/list":
		return rpcResponse{ID: req.ID, Result: map[string]any{"prompts": []any{}}}
	default:
		return errResp(req.ID, codeMethodNotFound, "不支持的方法: "+req.Method)
	}
}

func (s *Server) sessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessID
}

func (s *Server) version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ver == "" {
		return "dev"
	}
	return s.ver
}

// -------- helpers --------

func errResp(id json.RawMessage, code int, msg string) rpcResponse {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return rpcResponse{ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// localOrigin DNS-rebinding 防护：只接受来自本机的请求（无 Origin 头的 CLI 客户端放行）。
func localOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	host := origin
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}
