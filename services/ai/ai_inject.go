package ai

import (
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
)

// 运行中插入（steering）：用户在一轮还没跑完时又发了一句话。
//
// 一条会话同一时刻只跑一轮——后端对一条会话只有一串历史，两轮同时往里写就是
// 互相踩（前端 send 里对「同一条会话」的拦截也是这个原因）。所以「跑着的时候
// 再发一句」只有两条路：等它跑完，或者把这句话塞进正在跑的这一轮。这里实现后者。
//
// 队列按会话 id 挂：会话 id 在 runAgent 建好会话之后才存在，注册与取用都在那里
// （与审批闸门同一处）；HTTP 层（ai_stream.go 的 /ai/inject）只负责把话塞进来。
// 没有注册 = 这条会话没有正在跑的轮次 = 塞进来也没人等，直接告回「没在跑」，
// 由前端把这句当新的一条发出去。
//
// 为什么不做成「先排队、等这一轮结束后自动发」：那要前端自己记住「这句话还没发」，
// 切走、刷新、关窗口任何一个动作都会丢掉它；而这句话是用户刚打的字，丢了就是丢了。
type InjectMessage struct {
	// ID 前端生成的幂等 id：插入被确认时原样回传，前端据此把「等待插入」那一行
	// 换成正式气泡。连着插两句时也只有它能把回执对上号。
	ID   string
	Text string
	At   time.Time
}

// injectQueue 一条会话的待插入消息。
//
// 用切片而不是 channel：插入方（HTTP handler）不该被消费方的节奏阻塞，
// 而「一次取走全部」正是循环里需要的语义。
type injectQueue struct {
	mu    sync.Mutex
	items []InjectMessage
}

func (q *injectQueue) push(m InjectMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, m)
}

// drain 取走当前所有待插入消息（取走即清空）。
func (q *injectQueue) drain() []InjectMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	out := q.items
	q.items = nil
	return out
}

// pending 只问「还有没有没接走的」，不动队列。
//
// 循环里用它决定「这一轮该不该继续」：模型刚给出最终回答、但用户的话还排在
// 队里，这时收尾就等于把那句话丢了。
func (q *injectQueue) pending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) > 0
}

// registerInjectQueue 在 runAgent 里注册，返回的队列供循环取用。
// 调用方必须配套 unregisterInjectQueue（defer），否则每问一次漏一个队列。
func (a *AIService) registerInjectQueue(convID string) *injectQueue {
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return nil
	}
	q := &injectQueue{}
	a.injectsMu.Lock()
	defer a.injectsMu.Unlock()
	if a.injects == nil {
		a.injects = make(map[string]*injectQueue)
	}
	a.injects[convID] = q
	return q
}

func (a *AIService) unregisterInjectQueue(convID string) {
	a.injectsMu.Lock()
	defer a.injectsMu.Unlock()
	delete(a.injects, strings.TrimSpace(convID))
}

func (a *AIService) injectQueueFor(convID string) *injectQueue {
	a.injectsMu.Lock()
	defer a.injectsMu.Unlock()
	return a.injects[strings.TrimSpace(convID)]
}

// InjectAIMessage 把一句话塞进这条会话正在跑的那一轮；返回 false = 没有在跑的轮次。
//
// 空文本直接拒：误触回车（输入框里只有一个空格或一个 `/`）不该在回答中间插一句空白。
func (a *AIService) InjectAIMessage(convID, id, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	q := a.injectQueueFor(convID)
	if q == nil {
		return false
	}
	q.push(InjectMessage{ID: id, Text: text, At: time.Now()})
	logger.I("[ai] 运行中插入消息 conv=%s id=%s 字数=%d", convID, id, len([]rune(text)))
	return true
}
