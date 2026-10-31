package ai

// 执行事件的落点。
//
// 这里是全链路里唯一知道「事件最终长成 SSE 帧」的地方。执行核心
// （agent_run.go）只调 Push/Event/Failed 三个方法，不知道对面是 HTTP 连接、
// 是一张 agent_events 表，还是什么都不是——阶段 2 的编排器给一个写表的实现，
// runAgent 一行都不用改。

import (
	"context"
	"encoding/json"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// sseSink 把事件写回本地 SSE 流（AI 页的对话）。
type sseSink struct {
	fl     *sseFlusher
	cancel context.CancelFunc
}

// Push 合并写入文本碎片，顺带处理「下游已断」。
//
// 写失败就取消上游这条规则原来散在 onToken / reasonCb 里（各自写一遍
// `if fl.failed() { cancel() }`）。收进 sink 之后只有一处，而且不会漏——
// 漏掉的代价是前端已经断开、上游还在按 token 计费。
func (s *sseSink) Push(kind, text string) {
	s.fl.push(kind, text)
	if s.fl.failed() {
		s.cancel()
	}
}

// Event 同步写出结构事件，失败只吞掉：连接已断时没人接收错误，
// 而这里返回 error 会逼调用方给每个事件加一层无意义的判断。
func (s *sseSink) Event(kind string, data map[string]any) {
	_ = s.fl.writeEvent(kind, data)
}

// Failed 下游是否已断开。
func (s *sseSink) Failed() bool { return s.fl.failed() }

// SetConvID 新建会话后把 id 交回分帧层（见 convIDSetter 的说明）。
func (s *sseSink) SetConvID(id string) { s.fl.setConvID(id) }

// dbSink 把执行事件写进 agent_events 表（后台执行 / 子 agent 用）。
//
// 与 sseSink 的区别不只是落点，还有语义：SSE 那条路的读者是当场盯着屏幕的人，
// 断线即丢；这里的读者是「500ms 后轮询的前端」和「重启后再来看一眼的自己」，
// 所以必须落盘、必须有序（seq 由库分配）。
type dbSink struct {
	db     *db.Database
	runID  string
	nodeID string
}

// Push 不落表（token / reasoning 是流式碎片）。
//
// 一次回答能产生上千帧，落进去只会让事件表膨胀到几十万行，而内容本身
// 已经完整存在 ai_messages 里——要回放正文读消息表，不要读事件表。
func (s *dbSink) Push(kind, text string) {}

// Event 落一条结构化事件。
//
// 失败只记日志、不返回错误：事件是旁路，它写不进去不该让正在跑的 agent
// 跟着失败——用户要的是任务完成，不是完整的执行录像。
func (s *dbSink) Event(kind string, data map[string]any) {
	if s.db == nil || s.runID == "" {
		return
	}
	payload := ""
	if len(data) > 0 {
		b, err := json.Marshal(data)
		if err != nil {
			// 事件载荷来自 map[string]any，正常不会失败；失败说明里面有不可序列化的值
			// （比如 func/channel），留个能看懂的占位而不是丢掉整条事件。
			logger.W("[ai] 事件序列化失败 run=%s kind=%s: %v", s.runID, kind, err)
			payload = `{"marshal_error":true}`
		} else {
			payload = string(b)
		}
	}
	if _, err := s.db.AppendAgentEvent(s.runID, s.nodeID, kind, payload); err != nil {
		logger.W("[ai] 写执行事件失败 run=%s kind=%s: %v", s.runID, kind, err)
	}
}

// Failed 恒为 false：后台执行没有「下游断开」这回事，
// 是否该停由 ctx 决定（用户点停止 / run 超预算）。
func (s *dbSink) Failed() bool { return false }
