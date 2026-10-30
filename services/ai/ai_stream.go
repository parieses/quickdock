package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

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

// aiStreamMaxBodyBytes 单次流式请求的请求体上限。
//
// 按已开放的图片能力推导：最多 10 张 × 单张 10 MiB（见 ai_image.go），
// base64 膨胀 4/3 后约 134 MB 字符，再给 JSON 转义与正文留一点余量。
// 这不是为了省内存，是为了让「有人（或模型）构造一个巨大的请求」有明确上界，
// 而不是把整个进程的内存交给一次请求决定。
const aiStreamMaxBodyBytes = 160 << 20

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

// handle 只做 HTTP 外壳：握手、校验、解析、分帧；执行逻辑全在 RunAgent。
//
// 这条边界是刻意划的。执行核心（agent_run.go）不碰 ResponseWriter，
// 才能单独验证（给一个测试用的 sink 就能跑完整轮循环）；而这里剩下的事
// 没有一件能脱离真实连接。
//
// 错误分两截，别搞混：
//   - 响应头写出去之前（本函数前半段）用 http.Error 回真实状态码；
//   - 写出去之后（RunAgent 内部）只能发 event: error —— 已经回 200 了，改不了状态码。
func (s *aiStreamServer) handle(w http.ResponseWriter, r *http.Request) {
	// 这条 handler 里任何 panic 都只该断掉这一条流：宿主进程必须活着，
	// 否则「问了一句引起异常的话」就等于把整个 QuickDock 搞崩。
	// net/http 自己会为连接 recover 一次，但那样日志里什么都不留、前端也收不到提示。
	defer recoverPanic("ai stream handler")

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
		ConvID string `json:"convId"`
		Mode   string `json:"mode"`
		// SpaceID 只在**新建**任务时用：把这条对话直接放进某个空间。
		// 已有会话的归属不能被一次请求改掉——那会让「随便说句话就把任务搬了家」。
		SpaceID string        `json:"spaceId"`
		Message string        `json:"message"`
		Cwd     string        `json:"cwd"`
		Images  []AIChatImage `json:"images"`
	}
	// 请求体必须设上限：一次可以带 10 张 10 MiB 的图（见 ai_image.go），
	// base64 之后是 ~134 MB 字符，无脑 ReadAll 等于把这么大一块直接读进内存。
	// 超限按 413 回，前端 finally 会把已生成的部分收尾，不会卡在转圈。
	raw, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, aiStreamMaxBodyBytes))
	if rerr != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(rerr, &tooLarge) {
			logger.W("[ai] 请求体超过 %d MB，已拒绝", aiStreamMaxBodyBytes>>20)
			http.Error(w, "请求体过大", http.StatusRequestEntityTooLarge)
			return
		}
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

	// 跟随请求上下文，前端 abort 时 r.Context() 取消，立即停止读取模型流。
	// defer 顺序要紧：cancel 先注册、fl.close 后注册，于是 close 先执行（LIFO），
	// 与改造前一致——先把残留帧冲刷干净，再撤掉上下文。
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 从这里开始 fl 是响应体的唯一写者（后台协程会批量写 token）。
	//
	// 提前到执行之前创建（原来是等会话确定后才建）是为了让 sink 从头可用：
	// 否则「会话确定之前」那批事件只能直写 ResponseWriter，与后面的批量写
	// 变成两套写法、同一个格式。新建会话时 id 还不存在，由 sink.SetConvID 补上。
	fl := newSSEFlusher(w, flusher, strings.TrimSpace(req.ConvID))
	defer fl.close()

	res := s.svc.RunAgent(ctx, AgentRunReq{
		ConvID:  req.ConvID,
		Mode:    req.Mode,
		SpaceID: req.SpaceID,
		Message: req.Message,
		Cwd:     req.Cwd,
		Images:  req.Images,
	}, &sseSink{fl: fl, cancel: cancel})

	// 用户已经切走了才提醒：盯着屏幕看完整段回答的人再收到一条「回答完成」纯属噪音，
	// 而趁回答生成的时间去干别的事、回来发现早就好了，才是最需要提醒的场景。
	// 中断与失败不提醒——那是「没答完」，不是「答完了」。
	if res.Err == nil && !res.Stopped {
		s.notifyAIDone(res.ConvID, req.Message, res.Text)
	}
}

// doneEvent 组装流式结束事件：附带首字延迟与本次用量，回传前端展示。
// 字段名与前端 AIPage 的读取保持一致（promptTokens/completionTokens）。
// 字段缺失（上游未回 usage）时为 0，前端按 0 隐藏即可。
func doneEvent(convID string, firstTokenAt time.Duration, usage db.TokenUsage, model string, rounds, tools int) map[string]any {
	out := map[string]any{
		"convId":           convID,
		"firstTokenMs":     firstTokenAt.Milliseconds(),
		"promptTokens":     usage.Prompt,
		"completionTokens": usage.Completion,
		"cachedTokens":     usage.Cached,
		// 这一条回复用了几轮工具、调了几个工具。token 里已经含了工具轮次的消耗，
		// 但「花了同样的钱，一个是模型自己想多了、一个是真的查了五轮」对用户
		// 是完全不同的两件事，所以要单独给出来。
		"toolRounds": rounds,
		"toolCalls":  tools,
		// 回传本次实际使用的模型：前端刚落地的那条回复要用它显示模型名，
		// 刷新后是 DB 里同一份值，两边口径一致（不取前端的档案名，那可能是旧值）。
		"model": strings.TrimSpace(model),
	}
	// 单条回复的费用估算。价目表只有一份（ai_pricing.go），前端不复制一份
	// —— 两处价格一旦漂移，气泡上写 ¥0.03、设置页账单写 ¥0.05，谁都不再可信。
	//
	// 未收录的模型**不填** cost：前端据此隐藏该项，与设置页「未知模型不计价」
	// 同一个口径。宁可少显示一个数字，也不要给一个看起来精确其实编的数字。
	if !usage.Empty() {
		if cost, ok := estimateCost(model, usage.Prompt, usage.Completion, usage.Cached); ok {
			out["cost"] = cost
		}
	}
	return out
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

// setConvID 补上会话 id。
//
// 新建会话时创建 fl 的那一刻这个 id 还不存在（它由 DB 生成），而每个文本帧都要
// 带上 convId。所以由执行核心在拿到 id 后立刻告知，而不是让这里去猜、或者把
// fl 的创建推迟到会话确定之后——后者会把 HTTP 的时序约束倒灌回执行核心。
func (s *sseFlusher) setConvID(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.convID = id
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

// notifyAIDone 在「主窗口没盯着」时把回答完成写进通知中心。
//
// 判定分两层：
//  1. 主窗口有焦点 —— 人就在窗口里，不打扰；
//  2. 窗口可见 **且** 停在前 AI 对话页 —— 窗口可能因为用户去点别的应用而失焦，
//     但他眼睛还在 AI 页上看着这段回答一个字一个字出来，此时再提醒一次是纯粹的复述。
//
// 只有「窗口隐藏/最小化」或「窗口停在别的页面」才值得写通知 —— 通知的价值在于
// 替用户盯着他看不到的地方。
func (s *aiStreamServer) notifyAIDone(convID, question, answer string) {
	if s.svc == nil || s.svc.App == nil || s.svc.App.DB == nil {
		return
	}
	if s.svc.App.MainWindowActive() {
		return
	}
	if s.svc.App.AIPageVisible() && s.svc.App.Flags != nil && s.svc.App.Flags.Main.Load() {
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
