package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"quickdock/internal/logger"
)

// 面板类「问 AI」的本地流式通道（SSH / 剪贴板 / 笔记）。
//
// 为什么不继续用 Wails 事件（这三处原来都是 Event.Emit("ai:ask-delta")）：
// 与 AI 对话页同一个原因——事件在绑定方法执行期间会被攒住，用户看到的是
// 「一直转圈，然后整段蹦出来」。这条端点让它们改走 127.0.0.1 上的 SSE，
// 与 /ai/stream 同一套分帧，逐字可见。
//
// 业务不放这个包：prompt 组装、命令解析都在各域（services/sshx 等）。
// 这里只做「按 kind 找注册进来的执行函数，把它的增量写成帧」——与
// App.AskAIFn 的注入同一种避免循环依赖的办法（main.go 在装配阶段注册）。

// AskStreamEmit 业务侧推增量用的回调。event 取 "delta"（正文）或 "reason"（思考）。
// 客户端断开后写会失败，但不必处理：sseFlusher 自己会丢弃。
type AskStreamEmit func(event string, data map[string]any)

// AskStreamFn 一个面板场景的执行函数：解析 body、跑完、返回结构化结果。
//
// 结果会原样塞进 done 事件的 result 字段，前端拿到的东西与它原来
// 从绑定方法返回值里拿到的完全一致——这样面板的业务代码不必跟着改。
type AskStreamFn func(ctx context.Context, body []byte, emit AskStreamEmit) (any, error)

// RegisterAskStream 注册一个 kind 的执行函数。main.go 装配阶段调用。
//
//wails:ignore
func (a *AIService) RegisterAskStream(kind string, fn AskStreamFn) {
	if kind == "" || fn == nil {
		return
	}
	a.askStreamMu.Lock()
	defer a.askStreamMu.Unlock()
	if a.askStreams == nil {
		a.askStreams = make(map[string]AskStreamFn)
	}
	a.askStreams[kind] = fn
}

func (a *AIService) askStreamFn(kind string) AskStreamFn {
	a.askStreamMu.RLock()
	defer a.askStreamMu.RUnlock()
	return a.askStreams[kind]
}

// handleAskStream 把一次面板问答写成 SSE：
//
//	event: delta   {"text": "..."}     正文增量
//	event: reason  {"text": "..."}     思考增量（模型开了 thinking 才有）
//	event: done    {"result": {...}}   结束，结果与原绑定方法返回值同构
//	event: error   {"message": "..."}  失败
//
// 没有 delta 只有 done 也是合法的（模型一次吐完），前端不要把它当异常。
func (s *aiStreamServer) handleAskStream(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic("ai ask stream handler")

	if !s.prepareStream(w, r) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	var req struct {
		Kind string          `json:"kind"`
		Body json.RawMessage `json:"body"`
	}
	// 面板问答的入参比对话流小得多（屏显最多几千字），1 MiB 足够。
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "请求解析失败", http.StatusBadRequest)
		return
	}
	fn := s.svc.askStreamFn(req.Kind)
	if fn == nil {
		http.Error(w, "unknown kind", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	fl := newSSEFlusher(w, flusher, "")
	defer fl.close()

	emit := func(event string, data map[string]any) {
		_ = fl.writeEvent(event, data)
	}
	result, err := fn(r.Context(), req.Body, emit)
	if err != nil {
		logger.W("[ai] 面板问答失败 kind=%s: %v", req.Kind, err)
		_ = fl.writeEvent("error", map[string]any{"message": err.Error()})
		return
	}
	if emptySSE(result) {
		result = map[string]any{}
	}
	_ = fl.writeEvent("done", map[string]any{"result": result})
}

// emptySSE 判断结果是否为空：encoding/json 对 nil 只会写 null，
// 前端写 res.result.xxx 就会炸，所以统一换成 {}。
func emptySSE(v any) bool {
	if v == nil {
		return true
	}
	if m, ok := v.(map[string]any); ok && m == nil {
		return true
	}
	return false
}
