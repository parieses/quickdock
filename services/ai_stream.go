package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
)

// aiStreamServer 本地 AI 流式服务。
// 仅监听 127.0.0.1 随机端口，前端通过 fetch 读取 SSE 分块响应
//（text/event-stream + Write+Flush），实现真正的逐字流式，不依赖 Wails 事件
//（事件在方法执行期间会被攒到返回后才一次性投递，无法逐字显示）。API Key
// 始终在 Go 后端（DPAPI 解密后调用模型），不暴露给前端。
type aiStreamServer struct {
	svc   *AppService
	srv   *http.Server
	ln    net.Listener
	port  int
	token string
}

// StartAIStreamServer 启动本地流式服务（绑定 127.0.0.1:0 随机端口）
func (a *AppService) StartAIStreamServer() {
	s := &aiStreamServer{svc: a}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		fmt.Println("QuickDock: AI 流式令牌生成失败:", err)
		return
	}
	s.token = hex.EncodeToString(b)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("QuickDock: AI 流式服务启动失败:", err)
		return
	}
	s.ln = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/ai/stream", s.handle)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	a.aiStream = s
	go func() {
		defer recoverPanic("ai stream server")
		_ = s.srv.Serve(ln)
	}()
	fmt.Printf("QuickDock: AI 流式服务已启动 http://127.0.0.1:%d/ai/stream\n", s.port)
}

// StopAIStreamServer 关闭本地流式服务（应用退出时调用）
func (a *AppService) StopAIStreamServer() {
	s := a.aiStream
	if s != nil && s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	}
}

func (s *aiStreamServer) handle(w http.ResponseWriter, r *http.Request) {
	// CORS: 限制为请求来源或默认 127.0.0.1（不设通配符，防止本机其他页面随意访问）
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "http://127.0.0.1"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Stream-Token")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 随机令牌校验（通过 Header 传递，避免 URL 泄露到日志/Referer），防止本机其他程序随意调用
	if r.Header.Get("X-Stream-Token") != s.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// 必须在写入任何响应之前读取并解析请求体。若先 WriteHeader+Flush 再读
	// r.Body，net/http 会使请求体为空（读到 EOF），导致“请求解析失败”。
	var req struct {
		ConvID  string `json:"convId"`
		Mode    string `json:"mode"`
		Message string `json:"message"`
	}
	raw, rerr := io.ReadAll(r.Body)
	if rerr != nil {
		http.Error(w, "请求读取失败", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "请求解析失败", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 禁用代理缓冲，确保分块即时到达
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	convID := req.ConvID
	mode := req.Mode
	message := strings.TrimSpace(req.Message)

	cfg, okp := s.svc.getActiveAIProfile()
	if !okp || cfg.APIKey == "" {
		s.writeSSE(w, flusher, "error", map[string]string{"message": "请在设置中配置 API Key"})
		return
	}
	if message == "" {
		s.writeSSE(w, flusher, "error", map[string]string{"message": "消息为空"})
		return
	}
	if mode == "" || aiModePrompts[mode] == "" {
		mode = "chat"
	}

	// 跟随请求上下文，前端 abort 时 r.Context() 取消，立即停止读取模型流
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var conv *db.AIConversation
	if convID == "" {
		created, err := s.svc.DB.CreateAIConversation("")
		if err != nil {
			s.writeSSE(w, flusher, "error", map[string]string{"message": err.Error()})
			return
		}
		conv = created
		convID = conv.ID
		s.writeSSE(w, flusher, "conv", map[string]string{"id": conv.ID, "title": conv.Title})
	} else {
		conv, _ = s.svc.DB.GetAIConversation(convID)
	}

	// 组装上下文（含摘要压缩）
	// 注意：用户消息尚未存 DB，buildAIMessages 会从 DB 加载历史 + 追加 currentUser 作为最后一条。
	// 模型回复后统一落库两条消息，避免重复。
	messages, err := s.svc.buildAIMessages(ctx, cfg, convID, mode, message)
	if err != nil {
		s.writeSSE(w, flusher, "error", map[string]string{"message": err.Error()})
		return
	}

	// 首次消息自动生成标题
	if conv != nil && conv.Title == "" {
		title := message
		if utf8.RuneCountInString(title) > 30 {
			title = string([]rune(title)[:30]) + "…"
		}
		_ = s.svc.DB.UpdateAIConversationMeta(convID, title, "")
		s.writeSSE(w, flusher, "conv", map[string]string{"id": convID, "title": title})
	}

	// 流式调用模型，逐段写入响应
	var reasoningBuf strings.Builder
	var reasonCb func(text string)
	if cfg.ThinkingEnabled {
		reasonCb = func(text string) {
			reasoningBuf.WriteString(text)
			if err := s.writeSSE(w, flusher, "reasoning", map[string]string{"text": text, "convId": convID}); err != nil {
				cancel()
			}
		}
	}
	assistant, err := s.svc.streamAIChat(ctx, cfg, messages, convID,
		func(text string) {
			if err := s.writeSSE(w, flusher, "token", map[string]string{"text": text, "convId": convID}); err != nil {
				cancel() // 客户端已断开，取消上游请求
			}
		},
		reasonCb,
		func(promptTokens, completionTokens int) {
			_ = s.svc.DB.UpdateAIConversationUsage(convID, promptTokens, completionTokens)
		},
	)

	// 统一落库用户消息 + 助手回复（无论正常结束还是取消，有 assistant 就存）
	saveBoth := func(asst string) {
		if _, e := s.svc.DB.AddAIMessage(convID, "user", message); e != nil {
			s.writeSSE(w, flusher, "error", map[string]string{"message": e.Error()})
			return
		}
		if _, e := s.svc.DB.AddAIMessageFull(convID, "assistant", asst, reasoningBuf.String()); e != nil {
			s.writeSSE(w, flusher, "error", map[string]string{"message": e.Error()})
		}
	}

	if err != nil {
		if ctx.Err() != nil {
			// 用户主动取消，保存已收到部分
			saveBoth(assistant)
			s.writeSSE(w, flusher, "done", map[string]string{"convId": convID})
			return
		}
		s.writeSSE(w, flusher, "error", map[string]string{"message": err.Error()})
		return
	}

	// 正常结束，落库
	saveBoth(assistant)
	_ = s.svc.DB.UpdateAIConversationMeta(convID, "", "")
	s.writeSSE(w, flusher, "done", map[string]string{"convId": convID})
}

// writeSSE 以 SSE 格式写出一条事件并立即 Flush。返回 error 说明客户端已断开。
func (s *aiStreamServer) writeSSE(w http.ResponseWriter, f http.Flusher, event string, data map[string]string) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	if err != nil {
		return err
	}
	f.Flush()
	return nil
}
