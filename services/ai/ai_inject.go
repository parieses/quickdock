package ai

import (
	"context"
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
	// handed 最近被接走（drain 出去）的 ID。
	//
	// 只为一件事：用户在横条上点「立即发送」时，前端用同一个 id 又请求一次。
	// 若那条已经在这一瞬间被接走（回执还在路上），这次请求只能当「催」看，
	// 绝不能再入队——否则循环会把它当新的一句话再接一次。
	handed []string

	// turnCancel 当前这一轮「正在跑的那次模型调用」的取消函数（nil = 不在模型调用里）。
	//
	// 只服务一个场景：用户点「立即发送」。默认接入点是「调模型之前」，
	// 模型正在往外吐一段长回答时，那句话得等整段吐完才被接走——用户看着
	// 「等待插入」不动，以为没发出去。打断当前这次生成，它就会立刻回到
	// 循环开头那个接入点。
	turnCancel context.CancelFunc
	// interrupted 记录「上一次取消是我们自己按立即发送造成的」。
	// 不区分的话，循环里会把这次 context.Canceled 当成用户点「停止」，
	// 于是整轮结束、刚插进来的那句话被退回输入框。
	interrupted bool
}

// injectHandedMax 记住多少个「已接走」的 ID。重复的窗口就在回执发出到前端
// 收到之间（本机几毫秒），留最近一小段足够，不必无限增长。
const injectHandedMax = 32

// push 入队，返回 false = 这一句已经在队里或已经接走过（重复提交，不再入队）。
//
// 「立即发送」不是再发一次：前端 rushInject 拿同一个 id 又请求了一次（带 immediate）。
// 无脑 append 的后果是队列里出现两条同 ID 的消息——循环把它们都接进上下文、
// 各回执一次，于是界面上、历史里都多出一条一模一样的用户消息。
// 同 ID 只更新文本（用户可能刚在输入框里改过字再点催）。
func (q *injectQueue) push(m InjectMessage) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		if q.items[i].ID == m.ID {
			q.items[i].Text = m.Text
			return false
		}
	}
	for _, id := range q.handed {
		if id == m.ID {
			return false
		}
	}
	q.items = append(q.items, m)
	return true
}

// attachTurn 登记当前这次模型调用的取消函数；返回的函数负责注销。
func (q *injectQueue) attachTurn(cancel context.CancelFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.turnCancel = cancel
}

// detachTurn 注销这次模型调用的取消函数。必须在调用返回后立刻执行，
// 否则下一轮 attachTurn 之前来一次「立即发送」会取消一个已经结束的 ctx。
//
// 不动 interrupted：标记要被调用方在错误分支里 consumeInterrupt 消费
// （见 agent_run.go）；这里清掉的话，打断就永远只能判成「用户点了停止」。
func (q *injectQueue) detachTurn() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.turnCancel = nil
}

// interrupt 打断正在跑的这次模型调用。返回 false = 当前不在模型调用里
// （比如工具正在执行），消息照样在队里，等这一步结束就会被接走。
func (q *injectQueue) interrupt() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.turnCancel == nil {
		return false
	}
	q.interrupted = true
	q.turnCancel()
	return true
}

// consumeInterrupt 取走并清掉「自己打断的」标记。
func (q *injectQueue) consumeInterrupt() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	was := q.interrupted
	q.interrupted = false
	return was
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
	// 记下接走过的 id（见 push 与 handed 的说明）：迟到的「立即发送」据此
	// 被认成催，而不是又插一句。空 id 不记——它匹配不上任何一次请求。
	for _, it := range out {
		if it.ID != "" {
			q.handed = append(q.handed, it.ID)
		}
	}
	if n := len(q.handed) - injectHandedMax; n > 0 {
		q.handed = append([]string(nil), q.handed[n:]...)
	}
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

// unregisterInjectQueue 注销时比对指针，理由同 unregisterGate：
// 旧 run 的 defer 迟到时不能把新 run 的队列删掉，否则新会话里插话一律 409。
func (a *AIService) unregisterInjectQueue(convID string, q *injectQueue) {
	convID = strings.TrimSpace(convID)
	a.injectsMu.Lock()
	defer a.injectsMu.Unlock()
	if cur, ok := a.injects[convID]; ok && (q == nil || cur == q) {
		delete(a.injects, convID)
	}
}

func (a *AIService) injectQueueFor(convID string) *injectQueue {
	a.injectsMu.Lock()
	defer a.injectsMu.Unlock()
	return a.injects[strings.TrimSpace(convID)]
}

// InjectAIMessage 把一句话塞进这条会话正在跑的那一轮。
// 第一个返回值 false = 没有在跑的轮次；第二个 = immediate 时是否真的打断了当前生成。
//
// 空文本直接拒：误触回车（输入框里只有一个空格或一个 `/`）不该在回答中间插一句空白。
//
// immediate 是用户在「等待插入」那条横条上点了「立即发送」：除了入队，还打断
// 正在跑的那次模型调用，让它立刻回到接入点把这句话接进上下文，而不是等模型
// 把手上这段长回答写完。打断失败（当前在跑工具，不在模型调用里）不算错误——
// 话已经在队里，那一步结束就会接走，只是前端得如实说一声。
func (a *AIService) InjectAIMessage(convID, id, text string, immediate bool) (bool, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, false
	}
	q := a.injectQueueFor(convID)
	if q == nil {
		return false, false
	}
	// 重复提交（同一 id 的「立即发送」）不会再入队，见 push。
	added := q.push(InjectMessage{ID: id, Text: text, At: time.Now()})
	cut := false
	if immediate {
		cut = q.interrupt()
	}
	logger.I("[ai] 运行中插入消息 conv=%s id=%s 字数=%d 立即=%v 新入队=%v 已打断=%v",
		convID, id, len([]rune(text)), immediate, added, cut)
	return true, cut
}
