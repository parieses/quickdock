package ai

// 编排运行树的只读查询（前端 500ms 轮询用）。
//
// 为什么是轮询而不是第二条 SSE：对话流那条 SSE 是「一条连接 = 一次执行」的形态，
// 多个 run 并行时无法复用；而 run 树的数据量很小（几十个节点、几百条事件），
// 500ms 拉一次增量对本地 SQLite 的压力可以忽略。
// 真要更实时，阶段 3 再改成 Wails 事件推送——那时改的是这里和前端，不动执行核心。

import (
	"quickdock/internal/db"
	"quickdock/services"
)

// AgentRunTree run 树的一次性快照。
//
// run / nodes / events 一起返回而不是让前端分别调三次：轮询时几次调用之间可能
// 正好跨过「节点建了、事件也写了」的瞬间，分别拉会出现「树里还没这个节点、
// 事件却已经指向它」的错位，前端画树时就会挂个孤儿节点。
//
// 直接返回 db 的结构体而不是转成 map：字段契约由 db 层一处定义，
// 前端绑定的 TS 类型也跟着它生成，不会出现「map 里少个 key 静默变 undefined」。
type AgentRunTree struct {
	Run    *db.AgentRun    `json:"run"`
	Nodes  []db.AgentNode  `json:"nodes"`
	Events []db.AgentEvent `json:"events"`
	// LastSeq 本批返回的最大事件 seq；前端下次轮询原样带回来做增量。
	LastSeq int `json:"last_seq"`
}

// AIAgentRunTree 拉一棵 run 树（run + 全部节点 + seq > afterSeq 的事件）。
//
// afterSeq = 0 表示从头拉（首次进入、刷新后重看）。返回体里带 run 状态，
// 前端据此决定还要不要继续轮询（终态即停），不必自己推断。
func (a *AIService) AIAgentRunTree(runID string, afterSeq int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if runID == "" {
		return services.FailMsg("缺少 run id")
	}

	run, err := a.App.DB.GetAgentRun(runID)
	if err != nil {
		return services.Fail(err)
	}
	if run == nil {
		// run 不存在（例如会话已被删）：返回空树而不是报错。
		// 任务没了是合法状态，前端应当停止轮询，不需要弹一个「加载失败」。
		return services.Ok(&AgentRunTree{Nodes: []db.AgentNode{}, Events: []db.AgentEvent{}})
	}

	nodes, err := a.App.DB.ListAgentNodes(runID)
	if err != nil {
		return services.Fail(err)
	}
	events, err := a.App.DB.ListAgentEvents(runID, afterSeq, 200)
	if err != nil {
		return services.Fail(err)
	}

	tree := &AgentRunTree{
		Run:    run,
		Nodes:  append([]db.AgentNode{}, nodes...),
		Events: append([]db.AgentEvent{}, events...),
		// 空切片而不是 nil：Wails 序列化 nil 切片会变成 null，
		// 前端 forEach 直接抛错，而「没有事件」是最常见的第一帧。
		LastSeq: afterSeq,
	}
	for _, e := range events {
		if e.Seq > tree.LastSeq {
			tree.LastSeq = e.Seq
		}
	}
	return services.Ok(tree)
}
