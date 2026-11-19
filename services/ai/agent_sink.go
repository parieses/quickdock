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

	"quickdock/internal/aiutil"
	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// sseSink 把事件写回本地 SSE 流（AI 页的对话）。
type sseSink struct {
	fl *sseFlusher
	// cancel 用 CancelCauseFunc 而不是 CancelFunc：取消的原因要能带到执行核心
	// 和通知逻辑里去（用户点停止 / 客户端断开 / 上游超时），否则只能一律
	// 当成「用户中断」——刷个页面就收到一条「AI 回答已停止」。
	cancel context.CancelCauseFunc
	// onConvID 新会话 id 确定时的回调。
	//
	// 新建会话时请求里没有 convId（由后端生成），所以停止入口没法在 handler
	// 开头注册——那条「用户点了停止」的信号会找不到落点。会话 id 一出来就补挂。
	onConvID func(string)
}

// Push 合并写入文本碎片，顺带处理「下游已断」。
//
// 写失败就取消上游这条规则原来散在 onToken / reasonCb 里（各自写一遍
// `if fl.failed() { cancel() }`）。收进 sink 之后只有一处，而且不会漏——
// 漏掉的代价是前端已经断开、上游还在按 token 计费。
//
// 这里取消的原因固定是「客户端断开」：写失败的含义就是连接没了，
// 与用户按停止无关（那个走 /ai/stop）。
func (s *sseSink) Push(kind, text string) {
	s.fl.push(kind, text)
	if s.fl.failed() {
		s.cancel(errClientGone)
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
func (s *sseSink) SetConvID(id string) {
	s.fl.setConvID(id)
	if s.onConvID != nil && id != "" {
		s.onConvID(id)
	}
}

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
	// tool_output 是命令执行期间的实时滚动输出（run_command 专用）。
	// 它的读者只有「此刻盯着屏幕的人」，内容与随后那条 tool_result 大量重复，
	// 而频率是每 120ms 一条——落表只会把事件表撑大，回放时没人会去看过程录像。
	if kind == "tool_output" {
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
			// WP8：落库前脱敏。
			//
			// 事件表是 WP1 新开的「持久化面」：工具参数里可能带 `--token xxx`、
			// 命令输出里可能有 PEM 私钥、URL 里可能内嵌凭据。这些内容进库之后
			// 会被轨迹抽屉原样显示，也会随备份出到别处——而它本来只该在这一次
			// 对话里存在。
			//
			// 复用 aiutil 那套规则而不是另写一份：SSH 屏显那边已经用它挡住了
			// 同类内容，两份规则一旦漂移，会出现「屏显里遮住了、轨迹里漏出来」。
			payload = aiutil.RedactSecrets(string(b))
		}
	}
	if _, err := s.db.AppendAgentEvent(s.runID, s.nodeID, kind, payload); err != nil {
		logger.W("[ai] 写执行事件失败 run=%s kind=%s: %v", s.runID, kind, err)
	}
}

// Failed 恒为 false：后台执行没有「下游断开」这回事，
// 是否该停由 ctx 决定（用户点停止 / run 超预算）。
func (s *dbSink) Failed() bool { return false }

// multiSink 把同一批事件依次转发给多个下游。
//
// 为什么需要它：顶层对话此前只有 sseSink——事件发完就没了，于是 run 树里只有
// 子 agent 的步骤，主 agent「为什么派活、派之前试过什么」无从回看。dbSink 本来
// 就是为这件事准备的（见本文件开头），只是此前只给了子 agent。
//
// 顺序固定为传入顺序（SSE 在前、落库在后）：用户看到的那一刻，库里已经有一份；
// 断线也不会丢掉已经发生的事。
type multiSink struct {
	sinks []EventSink
}

// Push 原样转发。文本碎片对 dbSink 是空实现，转发没有代价。
func (m *multiSink) Push(kind, text string) {
	for _, s := range m.sinks {
		s.Push(kind, text)
	}
}

// Event 逐个转发。单个下游写失败由它自己消化（dbSink 只记 W），不中断其它下游。
func (m *multiSink) Event(kind string, data map[string]any) {
	for _, s := range m.sinks {
		s.Event(kind, data)
	}
}

// Failed 任一下游断开即视为断开——与 sseSink 的语义一致：
// SSE 断了就没必要继续跑，而 dbSink 恒为 false，所以组合体的行为等同 SSE。
func (m *multiSink) Failed() bool {
	for _, s := range m.sinks {
		if s.Failed() {
			return true
		}
	}
	return false
}

// SetConvID 透传给实现了 convIDSetter 的下游。
//
// 现在 runAgent 是在会话 id 确定之后才包 multiSink（见 agent_run.go），所以这条
// 透传暂时用不上；但它必须存在——否则将来把包裹点前移，SSE 帧会静默丢掉 convId，
// 前端失去会话归属，而症状是「消息跑到别的会话里」这种极难定位的怪事。
func (m *multiSink) SetConvID(id string) {
	for _, s := range m.sinks {
		if c, ok := s.(convIDSetter); ok {
			c.SetConvID(id)
		}
	}
}

// ---- 事件出口随 ctx 走 ----
//
// 需要它的只有一件事：**工具在执行期要往父级的流里说话**。spawn_agent 跑在
// runToolCalls 的 goroutine 里，手上只有 ctx；而它最该报告的事（子 agent 跑到
// 第几轮、现在在调什么）只有它自己知道。把 sink 传进去签一层层改签名会波及
// 整条工具调用链，挂 ctx 与 withAgentScope / withToolSurface 是同一个理由。

type eventSinkCtxKey struct{}

func withEventSink(ctx context.Context, s EventSink) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, eventSinkCtxKey{}, s)
}

// eventSinkFrom 从 ctx 取父级事件出口；不在流式执行里（单测、问答）返回 nil。
func eventSinkFrom(ctx context.Context) EventSink {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(eventSinkCtxKey{}).(EventSink)
	return s
}

// childRelaySink 把子 agent 的关键事件压成一条 child 帧，转发给父级 sink。
//
// 为什么不直接把父 sink 塞进 multiSink：子 agent 的 token / reasoning / tool 帧
// 会原样流进主对话——前端会把子 agent 的工具调用画成主 agent 的卡片，正文还会
// 和主 agent 的回答混在一起。所以这里**按白名单转发**，且一律压成一条精简帧：
//
//	{"phase":"tool|tool_done|round|done","callId":"<父级 spawn 调用的 id>", ...}
//
// callId 是关键：前端凭它把进度贴到「派活」那张卡片上，不必维护 node→call 的映射。
type childRelaySink struct {
	parent EventSink
	callID string
	nodeID string
	role   string
}

// Push 丢弃：子 agent 的正文与思考不进主对话（结论会随工具结果回来）。
func (s *childRelaySink) Push(kind, text string) {}

// Event 按白名单压缩转发。
func (s *childRelaySink) Event(kind string, data map[string]any) {
	if s == nil || s.parent == nil {
		return
	}
	switch kind {
	case "tool":
		// 子 agent 开始调一个工具：卡片上显示「第 N 轮 · file_grep」。
		s.emit("tool", map[string]any{
			"round": data["round"], "tool": data["name"], "toolCallId": data["id"],
		})
	case "tool_result":
		// 工具跑完了：带上耗时，让「它到底在干什么」有节奏感。
		s.emit("tool_done", map[string]any{
			"toolCallId": data["id"], "ok": data["ok"], "ms": data["ms"],
		})
	case "reasoning_round":
		// 一轮结束：即使没有工具调用，也说明它在推进，别让界面看起来静止。
		s.emit("round", map[string]any{"round": data["round"]})
	case "done":
		s.emit("done", nil)
	}
}

func (s *childRelaySink) emit(phase string, extra map[string]any) {
	ev := map[string]any{
		"phase": phase, "callId": s.callID, "nodeId": s.nodeID, "role": s.role,
	}
	for k, v := range extra {
		ev[k] = v
	}
	s.parent.Event("child", ev)
}

// Failed 恒为 false：子 agent 的生死由 ctx 决定（父级取消会传染下来），
// 不因为「父级 SSE 断了」提前收——那会让已经在跑的活白干。
func (s *childRelaySink) Failed() bool { return false }
