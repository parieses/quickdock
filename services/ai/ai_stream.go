package ai

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
	"sync"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// aiStreamServer 本地 AI 流式服务。
// 仅监听 127.0.0.1 随机端口，前端通过 fetch 读取 SSE 分块响应
// （text/event-stream + Write+Flush），实现真正的逐字流式，不依赖 Wails 事件
// （事件在方法执行期间会被攒到返回后才一次性投递，无法逐字显示）。API Key
// 始终在 Go 后端（DPAPI 解密后调用模型），不暴露给前端。
type aiStreamServer struct {
	svc   *AIService
	srv   *http.Server
	ln    net.Listener
	port  int
	token string
}

// StartAIStreamServer 启动本地流式服务（绑定 127.0.0.1:0 随机端口）
func (a *AIService) StartAIStreamServer() {
	a.aiStreamMu.Lock()
	defer a.aiStreamMu.Unlock()
	if a.aiStream != nil {
		return // 已启动：幂等，避免重复监听泄漏 listener
	}
	s := &aiStreamServer{svc: a}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		logger.W("QuickDock: AI 流式令牌生成失败: %v", err)
		return
	}
	s.token = hex.EncodeToString(b)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		logger.W("QuickDock: AI 流式服务启动失败: %v", err)
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
	logger.I("QuickDock: AI 流式服务已启动 http://127.0.0.1:%d/ai/stream", s.port)
}

// StopAIStreamServer 关闭本地流式服务（应用退出时调用）
func (a *AIService) StopAIStreamServer() {
	a.aiStreamMu.Lock()
	s := a.aiStream
	a.aiStream = nil
	a.aiStreamMu.Unlock()
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
		ConvID  string        `json:"convId"`
		Mode    string        `json:"mode"`
		Message string        `json:"message"`
		Images  []AIChatImage `json:"images"`
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
		// 用户报「AI 不回话」时，最常见原因就是没配 Key；这条必须留痕。
		logger.W("[ai] 请求被拒：未配置 API Key（conv=%s mode=%s）", convID, mode)
		s.writeSSE(w, flusher, "error", map[string]any{"message": "请在设置中配置 API Key"})
		return
	}
	// 纯图片提问（“这张图里有什么”）是合法场景：文字为空但有图片时放行。
	if message == "" && len(req.Images) == 0 {
		s.writeSSE(w, flusher, "error", map[string]any{"message": "消息为空"})
		return
	}
	if mode == "" || aiModePrompts[mode] == "" {
		mode = "chat"
	}

	// 图片闸门 + 落盘：按档案声明的 imageInput 能力校验（见 ai_image.go），
	// 本地图写盘后只把路径引用带去上下文。放在建会话之前——图片不合法时
	// 不该先留下一个空会话。
	imagesJSON, err := s.svc.prepareChatImages(cfg, req.Images)
	if err != nil {
		logger.W("[ai] 图片校验失败 conv=%s: %v", convID, err)
		s.writeSSE(w, flusher, "error", map[string]any{"message": err.Error()})
		return
	}

	// 跟随请求上下文，前端 abort 时 r.Context() 取消，立即停止读取模型流
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 流式统计：首字延迟（用户感知的等待时长）与本次用量，用于回传前端展示。
	// streamStart 取「收到请求」时刻而非「调用模型」时刻——用户等的是从按下回车
	// 到第一个字出现，含上下文组装（查库/摘要压缩）的耗时，这段以前是黑盒。
	streamStart := time.Now()
	var firstTokenAt time.Duration
	var usageIn, usageOut int

	var conv *db.AIConversation
	if s.svc.App.DB == nil {
		s.writeSSE(w, flusher, "error", map[string]any{"message": "数据库尚未就绪，请稍后重试"})
		return
	}
	if convID == "" {
		created, err := s.svc.App.DB.CreateAIConversation("")
		if err != nil {
			s.writeSSE(w, flusher, "error", map[string]any{"message": err.Error()})
			return
		}
		conv = created
		convID = conv.ID
		s.writeSSE(w, flusher, "conv", map[string]any{"id": conv.ID, "title": conv.Title})
	} else {
		conv, _ = s.svc.App.DB.GetAIConversation(convID)
	}
	// 会话可能被用户在流式进行中删除：此时继续写入会产生孤儿消息，直接终止并提示。
	if conv == nil {
		logger.E("[ai-stream] 会话不存在或已被删除，终止流式 convID=%s", convID)
		s.writeSSE(w, flusher, "error", map[string]any{"message": "会话不存在或已被删除"})
		return
	}

	// 从这里开始，fl 是响应体的唯一写者（后台协程会批量写 token）。
	// 之前的所有 writeSSE 都发生在此之前，不存在并发写 ResponseWriter 的问题。
	fl := newSSEFlusher(w, flusher, convID)
	defer fl.close()

	// 调模型前先落库用户消息：无论后续成功 / 失败 / 被打断，历史都不会出现空会话，
	// 用户刷新或重进会话都能看到自己问了什么，而不是点进来一片空白。
	if err := s.svc.saveUserTurn(convID, message, imagesJSON); err != nil {
		logger.W("[ai] 保存用户消息失败 conv=%s: %v", convID, err)
		fl.writeEvent("error", map[string]any{"message": err.Error()})
		return
	}

	// 组装上下文（含摘要压缩）
	// 注意：用户消息尚未存 DB，buildAIMessages 会从 DB 加载历史 + 追加 currentUser 作为最后一条。
	// 模型回复后统一落库两条消息，避免重复。
	messages, err := s.svc.buildAIMessages(ctx, cfg, convID, mode)
	if err != nil {
		logger.W("[ai] 组装上下文失败 conv=%s mode=%s: %v", convID, mode, err)
		fl.writeEvent("error", map[string]any{"message": err.Error()})
		return
	}

	// 首次消息自动生成标题
	if conv != nil && conv.Title == "" {
		title := message
		if title == "" {
			// 纯图片提问：用固定词兜底，否则标题为空、列表里显示“新对话”
			title = "图片问答"
		}
		if utf8.RuneCountInString(title) > 30 {
			title = string([]rune(title)[:30]) + "…"
		}
		_ = s.svc.App.DB.UpdateAIConversationMeta(convID, title, "")
		fl.writeEvent("conv", map[string]any{"id": convID, "title": title})
	}

	// 流式调用模型，逐段写入响应
	var reasoningBuf strings.Builder
	var reasonCb func(text string)
	if cfg.ThinkingEnabled {
		reasonCb = func(text string) {
			reasoningBuf.WriteString(text)
			fl.push("reasoning", text)
			if fl.failed() {
				cancel()
			}
		}
	}
	assistant, err := s.svc.streamAIChat(ctx, cfg, messages, convID,
		func(text string) {
			// 首字延迟：从收到请求到第一个内容 token（含上下文组装耗时），只记第一次
			if firstTokenAt == 0 {
				firstTokenAt = time.Since(streamStart)
			}
			fl.push("token", text)
			if fl.failed() {
				cancel() // 客户端已断开，取消上游请求
			}
		},
		reasonCb,
		func(promptTokens, completionTokens int) {
			usageIn, usageOut = promptTokens, completionTokens
			_ = s.svc.App.DB.UpdateAIConversationUsage(convID, promptTokens, completionTokens)
		},
	)

	// 仅落库助手回复：用户消息已在调模型前由 saveUserTurn 落库（幂等，重试不重复）。
	// 模型成功返回但内容为空（上游限流/工具调用/内容过滤都可能）时不写空回复，避免污染历史。
	saveAssistant := func(asst string) {
		asst = strings.TrimSpace(asst)
		if asst == "" {
			logger.W("[ai] 模型未产出内容，跳过落库助手回复 conv=%s mode=%s", convID, mode)
			return
		}
		if _, e := s.svc.App.DB.AddAIMessageFull(convID, "assistant", asst, reasoningBuf.String()); e != nil {
			logger.W("[ai] 保存助手回复失败 conv=%s: %v", convID, e)
			fl.writeEvent("error", map[string]any{"message": e.Error()})
		}
	}

	if err != nil {
		if ctx.Err() != nil {
			// 用户主动取消，保存已收到部分（用户消息已在调模型前落库）
			saveAssistant(assistant)
			fl.writeEvent("done", doneEvent(convID, firstTokenAt, usageIn, usageOut))
			return
		}
		// 上游报错：用户消息已落库，历史不会空。若疑似因 base64 本地图片被拒，给明确提示。
		msg := err.Error()
		if hint := s.svc.imageErrorHint(err, req.Images); hint != "" {
			msg += hint
		}
		fl.writeEvent("error", map[string]any{"message": msg})
		return
	}

	// 正常结束，落库助手回复（用户消息已在调模型前落库）
	saveAssistant(assistant)
	_ = s.svc.App.DB.UpdateAIConversationMeta(convID, "", "")
	logger.I("[ai] 流式结束 conv=%s 首字=%dms in=%d out=%d", convID, firstTokenAt.Milliseconds(), usageIn, usageOut)
	fl.writeEvent("done", doneEvent(convID, firstTokenAt, usageIn, usageOut))

	// 用户已经切走了才提醒：盯着屏幕看完整段回答的人再收到一条「回答完成」纯属噪音，
	// 而趁回答生成的时间去干别的事、回来发现早就好了，才是最需要提醒的场景。
	s.notifyAIDone(convID, message, assistant)
}

// doneEvent 组装流式结束事件：附带首字延迟与本次用量，回传前端展示。
// 字段名与前端 AIPage 的读取保持一致（promptTokens/completionTokens）。
// 字段缺失（上游未回 usage）时为 0，前端按 0 隐藏即可。
func doneEvent(convID string, firstTokenAt time.Duration, usageIn, usageOut int) map[string]any {
	return map[string]any{
		"convId":           convID,
		"firstTokenMs":     firstTokenAt.Milliseconds(),
		"promptTokens":     usageIn,
		"completionTokens": usageOut,
	}
}

// sseFlusher 把上游逐 token 的写入合并成批量的 SSE 帧。
//
// 背景：上游每吐一个 token 就 Write+Flush 一次，一次长回复能产生上千次
// 系统调用 + 上千个 SSE 帧，浏览器侧也要解析上千次。前端已做 16ms 合帧，
// 服务端这层再合并一次，能进一步减少网络包与事件数。
//
// 并发约束：http.ResponseWriter 不是并发安全的，且 SSE 要求帧序严格。
// 因此本类型是**响应体的唯一写者**：token 先进队列，由后台协程按 20ms 窗口
// 批量写出；控制类事件（error/done）走 writeEvent，会先在锁内把队列里的
// token 冲刷干净再写自己，保证「先 token 后 done」的顺序不变。
type sseFlusher struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	f      http.Flusher
	convID string

	pending []sseFrame // 待下发的 token 帧（按产生顺序）
	err     error
	closed  bool

	stop chan struct{}
	done chan struct{}
}

type sseFrame struct {
	event string
	text  string
}

// sseBatchWindow 合并窗口：20ms 约 3 帧以内，人眼不可辨延迟。
const sseBatchWindow = 20 * time.Millisecond

func newSSEFlusher(w http.ResponseWriter, f http.Flusher, convID string) *sseFlusher {
	s := &sseFlusher{
		w:      w,
		f:      f,
		convID: convID,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go s.loop()
	return s
}

// push 把一段文本入队，等待批量下发。
func (s *sseFlusher) push(event, text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.err != nil {
		return
	}
	s.pending = append(s.pending, sseFrame{event: event, text: text})
}

// writeEvent 同步写出一条控制类事件（error/done/conv）。
// 写之前会在锁内冲刷待发 token，保证帧序：所有 token 先于 done 送达。
func (s *sseFlusher) writeEvent(event string, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if err := s.flushLocked(); err != nil {
		return err
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		s.err = err
		return err
	}
	s.f.Flush()
	return nil
}

// failed 报告是否已发生写错误（客户端断开）。用于触发上游请求取消。
func (s *sseFlusher) failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err != nil
}

// close 停止后台协程并冲刷残留帧。流结束后必须调用。
func (s *sseFlusher) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	<-s.done
}

// flushLocked 冲刷队列（调用方须持有锁）。相邻同类型帧合并成一条。
func (s *sseFlusher) flushLocked() error {
	if len(s.pending) == 0 {
		return nil
	}
	frames := s.pending
	s.pending = nil
	merged := make([]sseFrame, 0, len(frames))
	for _, fr := range frames {
		if n := len(merged); n > 0 && merged[n-1].event == fr.event {
			merged[n-1].text += fr.text
			continue
		}
		merged = append(merged, fr)
	}
	for _, fr := range merged {
		b, err := json.Marshal(map[string]any{"text": fr.text, "convId": s.convID})
		if err != nil {
			s.err = err
			return err
		}
		if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", fr.event, b); err != nil {
			s.err = err
			return err
		}
		s.f.Flush()
	}
	return nil
}

// loop 后台批量写出协程。
func (s *sseFlusher) loop() {
	defer close(s.done)
	ticker := time.NewTicker(sseBatchWindow)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			s.mu.Lock()
			_ = s.flushLocked()
			s.mu.Unlock()
			return
		case <-ticker.C:
			s.mu.Lock()
			_ = s.flushLocked()
			s.mu.Unlock()
		}
	}
}

// writeSSE 以 SSE 格式写出一条事件并立即 Flush。返回 error 说明客户端已断开。
func (s *aiStreamServer) writeSSE(w http.ResponseWriter, f http.Flusher, event string, data map[string]any) error {
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

// notifyAIDone 在「主窗口没盯着」时把回答完成写进通知中心。
//
// 判定条件用「主窗口不可见 或 没有焦点」：AI 回答往往要等十几秒到几分钟，
// 用户切去别的窗口干活是常态。反过来，人就在 AI 页面上看着，还弹一条完成提醒，
// 是纯粹的打扰 —— 通知的价值在于「替你盯着」，不是复述你已经看到的东西。
func (s *aiStreamServer) notifyAIDone(convID, question, answer string) {
	if s.svc == nil || s.svc.App == nil || s.svc.App.DB == nil {
		return
	}
	if s.svc.App.MainWindowActive() {
		return
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return
	}
	title := "AI 回答已完成"
	if q := strings.TrimSpace(question); q != "" {
		title = "AI 回答已完成：" + logger.Trunc(strings.ReplaceAll(q, "\n", " "), 24)
	}
	s.svc.App.PushNotification("ai", "success", title, logger.Trunc(answer, 160), "ai", convID)
}
