package ai

// 单个子任务的停止入口。
//
// 为什么需要它：一次编排常常同时派出去五六个子任务，而其中一两个会跑偏
// （扫错目录、绕进无关文件里）。此前用户只有两个选择——等它跑完（可能十几分钟、
// 几十万 token），或者把整条对话停掉（把已经跑了大半的其它子任务一起杀掉）。
//
// 取消函数由 handleSpawnAgent 注册（见 agent_spawn.go），这里只做「按 nodeID 取出来调」。
// 取消之后子 agent 的 runAgent 会走与「用户点停止」完全相同的那条路径：
// 半截正文标 canceled 回填，父 agent 拿到的是一份带 partial 标记的残本。

import (
	"context"
	"strings"

	"quickdock/services"
)

// AICancelAgentNode 停掉某个还在跑的子 agent（节点）。
//
// 返回 Code=1 而不是报错：节点可能已经自己结束了（用户点得慢半拍），
// 那不是错误，前端刷新一下时间线就是最新状态。
func (a *AIService) AICancelAgentNode(nodeID string) *services.ApiResult {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return services.FailMsg("缺少子任务 id")
	}
	v, ok := a.nodeCancels.Load(nodeID)
	if !ok {
		return services.FailMsg("这个子任务已经结束了")
	}
	// 注册时放的就是 context.CancelFunc（命名类型，不能断言成 func()）。
	if cancel, ok := v.(context.CancelFunc); ok {
		cancel()
		return services.Ok(nil)
	}
	// 走不到这里；万一将来换了类型，宁可不动作也不要 panic 掉整个服务。
	return services.FailMsg("这个子任务无法停止")
}
