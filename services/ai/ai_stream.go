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
	"strconv"
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
	// 运行中插入消息（见 ai_inject.go）：前端在这一轮还没跑完时发的下一句。
	mux.HandleFunc("/ai/inject", s.handleInject)
	// 用户点停止（见 ai_stop.go）：与「页面断开」区分开，只为把话说对。
	mux.HandleFunc("/ai/stop", s.handleStop)
	// 面板类问答（SSH / 剪贴板 / 笔记，见 ai_ask_stream.go）：同一套 SSE，
	// 让它们不必再依赖 Wails 事件来逐字显示。
	mux.HandleFunc("/ai/ask-stream", s.handleAskStream)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	a.aiStream = s
	go func() {
		defer recoverPanic("ai stream server")
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.W("QuickDock: AI 流式服务异常退出: %v", err)
		}
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

// handle 只做 HTTP 外壳：握手、校验、解析、分帧；执行逻辑全在 runAgent。
//
// 这条边界是刻意划的。执行核心（agent_run.go）不碰 ResponseWriter，
// 才能单独验证（给一个测试用的 sink 就能跑完整轮循环）；而这里剩下的事
// 没有一件能脱离真实连接。
//
// 错误分两截，别搞混：
//   - 响应头写出去之前（本函数前半段）用 http.Error 回真实状态码；
//   - 写出去之后（runAgent 内部）只能发 event: error —— 已经回 200 了，改不了状态码。
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
		// SpaceID 只在新建任务时用：把这条对话直接放进某个空间。
		// 已有会话的归属不能被一次请求改掉——那会让「随便说句话就把任务搬了家」。
		SpaceID string        `json:"spaceId"`
		Message string        `json:"message"`
		Cwd     string        `json:"cwd"`
		Images  []AIChatImage `json:"images"`
		// Mentions 用 @ 显式引用的文件 / 目录 / 历史对话（见 ai_mentions.go）。
		// 引用内容由后端展开进上下文，前端只送引用目标，不送文件内容。
		Mentions []AIMention `json:"mentions"`
		// Skills 这条消息要用的技能名，来自输入框里打的 /名字。前端每次都带
		// （含空数组 =「这条不用技能」）；缺这个字段 = 前端没表态，后端沿用
		// 会话上存的选择（老会话因此不会因为升级就丢掉技能）。
		Skills []string `json:"skills"`
		// Permission 这条对话的执行权限："full" = 完全访问，其余/缺省 = 默认。
		// 缺这个字段的老前端一律按默认权限处理——安全开关断线时要倒向「问」。
		Permission string `json:"permission"`
		// Worktree 是否在隔离工作区里跑这条任务（见 ai_worktree.go）。
		// 老前端不带这个字段 → false，行为与升级前完全一致。
		Worktree bool `json:"worktree"`
		// WorktreeBranch 基于哪条分支建工作区（空 = 当前分支）。
		WorktreeBranch string `json:"worktreeBranch"`
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
	//
	// 用 WithCancelCause 而不是 WithCancel：取消的原因要能传下去。r.Context()
	// 被取消时原因只能是 context.Canceled（HTTP 层不区分），但用户点停止会先在
	// /ai/stop 里用 errStopByUser 掐一次，那个原因就留在了这条 ctx 上。
	ctx, cancelCause := context.WithCancelCause(r.Context())
	defer cancelCause(nil)

	// 停止入口：请求里的 convId 已知就现在挂，新建会话则等 id 出来再挂
	// （见 sseSink.onConvID）。注销放在 handler 退出时统一做，因为 id 可能是
	// 后来才有的。
	var stopConvID string
	var stopSlot *stopSlot
	registerStop := func(id string) {
		if id == "" || stopConvID != "" {
			return
		}
		stopConvID = id
		stopSlot = s.svc.registerStop(id, cancelCause)
	}
	registerStop(strings.TrimSpace(req.ConvID))
	defer func() {
		if stopConvID != "" {
			s.svc.unregisterStop(stopConvID, stopSlot)
		}
	}()

	// 从这里开始 fl 是响应体的唯一写者（后台协程会批量写 token）。
	//
	// 提前到执行之前创建（原来是等会话确定后才建）是为了让 sink 从头可用：
	// 否则「会话确定之前」那批事件只能直写 ResponseWriter，与后面的批量写
	// 变成两套写法、同一个格式。新建会话时 id 还不存在，由 sink.SetConvID 补上。
	fl := newSSEFlusher(w, flusher, strings.TrimSpace(req.ConvID))
	defer fl.close()

	res := s.svc.runAgent(ctx, AgentRunReq{
		ConvID:         req.ConvID,
		Mode:           req.Mode,
		SpaceID:        req.SpaceID,
		Message:        req.Message,
		Cwd:            req.Cwd,
		Images:         req.Images,
		Mentions:       req.Mentions,
		Skills:         req.Skills,
		Permission:     req.Permission,
		Worktree:       req.Worktree,
		WorktreeBranch: req.WorktreeBranch,
	}, &sseSink{fl: fl, cancel: cancelCause, onConvID: registerStop})

	// 每次回答结束都通知：用户常在别处干活，跑完/中断/失败都需要被叫回来，
	// 否则他不知道 AI 已经答完（尤其被切到后台、或去干别的了）。
	//
	// 但「中断」要分两种：用户按的停止配得上一条通知；页面自己断了（刷新、
	// 关窗、网络断）也弹「AI 回答已停止」就是在骗人——他根本没按过。
	switch {
	case res.Err == nil && !res.Stopped:
		s.notifyAIDone(res.ConvID, req.Message, res.Text)
	case res.Stopped && errors.Is(res.StopCause, errStopByUser):
		s.svc.App.PushNotification("ai", "info", "AI 回答已停止", logger.Trunc(strings.TrimSpace(req.Message), 24), "ai", res.ConvID)
	case res.Stopped:
		// 客户端断开：半截内容已经落库（saveSegment 里标了 stopped），这里只留痕。
		// 之所以还发一条 warn：如果是刷新页面，用户回来会看到半条回答，需要知道
		// 原因；如果是关掉应用，通知也跟着进程一起没了，不会造成噪音。
		logger.I("[ai] 客户端断开导致本轮结束 conv=%s cause=%v", res.ConvID, res.StopCause)
		s.svc.App.PushNotification("ai", "warn", "AI 回答被中断",
			"页面连接断开了，只保存了已生成的部分（"+logger.Trunc(strings.TrimSpace(req.Message), 24)+"）", "ai", res.ConvID)
	default:
		s.svc.App.PushNotification("ai", "error", "AI 回答失败", logger.Trunc(res.Err.Error(), 160), "ai", res.ConvID)
	}
}

// prepareStream 三条流式端点共用的握手：CORS + POST 校验 + 随机令牌。
// 返回 false 表示已经回过错误（或这是 OPTIONS 预检），调用方直接 return。
//
// 抽出来不是为了少写几行：三处各写一遍时，改其中一处的校验（比如令牌头名）
// 必然漏掉另外两处，而漏掉的表现是「某个面板突然 403」，很难联想到是这里。
func (s *aiStreamServer) prepareStream(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "http://127.0.0.1"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Stream-Token")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if r.Header.Get("X-Stream-Token") != s.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// handleInject 接收「运行中插入的消息」（见 ai_inject.go）。
//
// 为什么是独立的短请求而不是复用 /ai/stream：插入必须**立刻有回音**——这一轮还在
// 不在跑、话有没有接住，前端要据此决定「等它接走」还是「把这句当新的一条发」。
// 走流式的话，回音要等这一轮结束才可能出来，那就等于没有。
//
// 没在跑时回 409 而不是 500：这不是失败，是「这一轮刚好结束了」。前端据此改走
// 正常的发送路径，用户看到的是一条新消息，而不是一句「插入失败」。
func (s *aiStreamServer) handleInject(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic("ai inject handler")

	if !s.prepareStream(w, r) {
		return
	}

	var req struct {
		ConvID  string `json:"convId"`
		ID      string `json:"id"`
		Message string `json:"message"`
		// Immediate 用户点了「立即发送」：打断正在生成的回答，立刻接走这句话。
		// 老前端不带这个字段 → 行为与升级前完全一致（只入队，等当前这次调用结束）。
		Immediate bool `json:"immediate"`
	}
	// 插入只带文本，1 MiB 绰绰有余；仍然设上限，理由与 /ai/stream 相同。
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "请求解析失败", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	ok, cut := s.svc.InjectAIMessage(req.ConvID, req.ID, req.Message, req.Immediate)
	if !ok {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"ok":false,"reason":"no-run"}`)
		return
	}
	// interrupted 只对 immediate 有意义：false 表示此刻在跑工具、不在模型调用里，
	// 这句话还在队里，那一步结束就会接走——前端据此把按钮退回去并说明原因，
	// 而不是一直转着「正在打断…」。
	_, _ = io.WriteString(w, `{"ok":true,"interrupted":`+strconv.FormatBool(cut)+`}`)
}

// handleStop 接收「用户点了停止」。
//
// 为什么需要它：点停止最直接的做法是 abort 掉 fetch，而 abort 与刷新页面、
// 关闭窗口、网线被拔在 HTTP 层完全一样——都是 r.Context() 取消。后端没有
// 别的通道能知道「这一次断开是用户主动的」，于是只能一律记成用户中断，
// 症状是刷一下页面就收到一条「AI 回答已停止」。
//
// 只入队一个取消原因，不等待执行收尾：请求立刻返回，前端拿到 ok 就 abort，
// 停止按钮的手感不变。
func (s *aiStreamServer) handleStop(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic("ai stop handler")

	if !s.prepareStream(w, r) {
		return
	}

	var req struct {
		ConvID string `json:"convId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "请求解析失败", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// ok=false 不是错误：这一轮可能刚好自己结束了。前端照常 abort，
	// 只是后端不会把它记成「用户停止」（那时它本来也已经正常收尾了）。
	_, _ = io.WriteString(w, `{"ok":`+strconv.FormatBool(s.svc.requestStop(req.ConvID))+`}`)
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
	// 未收录的模型不填 cost：前端据此隐藏该项，与设置页「未知模型不计价」
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
// 上游每吐一个 token 就 Write+Flush 一次，一次长回复能产生上千次
// 系统调用 + 上千个 SSE 帧，浏览器侧也要解析上千次。前端已做 16ms 合帧，
// 服务端这层再合并一次，能进一步减少网络包与事件数。
//
// 并发约束：http.ResponseWriter 不是并发安全的，且 SSE 要求帧序严格。
// 因此本类型是响应体的唯一写者：token 先进队列，由后台协程按 20ms 窗口
// 批量写出；控制类事件（error/done）走 writeEvent，会先在锁内把队列里的
// token 冲刷干净再写自己，保证「先 token 后 done」的顺序不变。
type sseFlusher struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	f      http.Flusher
	rc     *http.ResponseController
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

// sseWriteTimeout 单次写响应的上限。
//
// 为什么必须设：对端如果变成半开连接（笔记本合盖、Wi-Fi 掉线、代理静默丢包），
// Write 会一直阻塞在 TCP 发送缓冲里——不报错、不返回。那时后台 flush 协程卡死，
// close() 里的 <-done 也跟着卡死，整个 handler 永远不退出，连带那条 agent 协程
// 与它持有的 ctx。一两条这样的连接看起来只是「回答卡住」，积几条就是进程内
// 一批永远不会结束的请求。
const sseWriteTimeout = 15 * time.Second

// sseCloseWait close() 等待后台协程退出的上限。
//
// 写超时已经能让阻塞的写自己失败，这里是第二道闸：万一某次写卡在
// ResponseController 不支持的老 writer 上，handler 也不该被它拖住。
//
// 必须比 sseWriteTimeout 大：否则一次正在进行的写还没到截止时间，handler 就
// 先返回了，而后台协程会往一个已经交还给 net/http 的连接上继续写——那种写入
// 是未定义行为，可能污染同一条复用连接上的下一个响应。
const sseCloseWait = sseWriteTimeout + 5*time.Second

func newSSEFlusher(w http.ResponseWriter, f http.Flusher, convID string) *sseFlusher {
	s := &sseFlusher{
		w:      w,
		f:      f,
		rc:     http.NewResponseController(w),
		convID: convID,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go s.loop()
	return s
}

// arm 给下一次写设一个截止时间。
//
// 用 http.ResponseController 而不是自己拿 net.Conn：经过中间件包装的
// ResponseWriter 只有它能穿透到真正的连接。SetWriteDeadline 返回
// ErrNotSupported 时静默放过——那是测试里的假 writer，不该因此报错。
func (s *sseFlusher) arm() {
	if s.rc != nil {
		_ = s.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	}
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
	s.arm()
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
//
// 有界等待：半开连接上的一次写可能卡到写截止时间才失败，这里不能无脑
// `<-s.done`——那会把 handler 连同它持的 ctx 一起吊死（见 sseCloseWait）。
func (s *sseFlusher) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	select {
	case <-s.done:
	case <-time.After(sseCloseWait):
		logger.W("[ai] SSE 后台写出协程未在 %v 内退出（连接多半已半开），放弃等待", sseCloseWait)
	}
	// 撤掉写截止时间：截止时间是设在 TCP 连接上的，而这条连接随后可能被
	// net/http 复用给下一个请求——留着一个已经过去的截止时间，下一个请求
	// 一写就失败，症状是「偶尔某个请求立刻 500」，且与 AI 毫无关系。
	if s.rc != nil {
		_ = s.rc.SetWriteDeadline(time.Time{})
	}
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
		s.arm()
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

// notifyAIDone 在回答正常完成时把结果写进通知中心。
//
// 这里不再判断「窗口有没有盯着」——用户明确反馈过：盯着屏幕时也常常注意不到
// AI 已经答完（尤其回答较长、他中途切去干别的），所以一律通知。
// 中断与失败由调用方用不同的标题单独通知。
func (s *aiStreamServer) notifyAIDone(convID, question, answer string) {
	if s.svc == nil || s.svc.App == nil || s.svc.App.DB == nil {
		return
	}
	answer = strings.TrimSpace(answer)
	title := "AI 回答已完成"
	if q := strings.TrimSpace(question); q != "" {
		title = "AI 回答已完成：" + logger.Trunc(strings.ReplaceAll(q, "\n", " "), 24)
	}
	body := answer
	if body == "" {
		body = "（无文本输出）"
	}
	s.svc.App.PushNotification("ai", "success", title, logger.Trunc(body, 160), "ai", convID)
}
