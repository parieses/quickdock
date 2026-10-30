package ai

// 执行事件的落点。
//
// 这里是全链路里**唯一**知道「事件最终长成 SSE 帧」的地方。执行核心
// （agent_run.go）只调 Push/Event/Failed 三个方法，不知道对面是 HTTP 连接
// 还是别的什么——换一个实现（测试桩、日志）既能验证执行流程，又不用起 HTTP。

import "context"

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
