package ai

// 会话回放（WP12）。
//
// WP1 一直在往 `agent_runs` / `agent_nodes` / `agent_events` 里写，但只有
// 「当前这一次运行」有入口（轨迹抽屉按 runID 拉）。于是那些数据只能被用来
// 排障，不能被用来回答「这次任务我到底让它做了什么」——而后者才是用户
// 隔天回来最想知道的事。
//
// 这里只做**读**：把一段会话的所有 run 连同事件按时间顺序拼出来。

import (
	"strings"

	"quickdock/internal/db"
	"quickdock/services"
)

// aiSessionRunLimit 一次回放最多带多少条 run。
//
// 20 条足够覆盖「一天里在这个会话里干的活」；再多的话时间轴本身也没法看，
// 而事件量会按 run 数线性放大。
const aiSessionRunLimit = 20

// aiSessionEventLimit 每条 run 最多带多少事件。
//
// 与轨迹抽屉同一个量级（200）：够看清一次任务的全过程，
// 又不至于把大任务的上万条 `tool_output` 拖进前端。
const aiSessionEventLimit = 200

// AISessionRun 回放里的一条 run（run 本体 + 它的节点与事件）。
type AISessionRun struct {
	Run    *db.AgentRun    `json:"run"`
	Nodes  []db.AgentNode  `json:"nodes"`
	Events []db.AgentEvent `json:"events"`
	// Children 这条 run 派出去的子 agent 数（父子关系靠 agent_nodes 表达，
	// 这里只给个数：回放里要能一眼看出「这次任务是不是拆过」）。
	Children int `json:"children"`
}

// AISessionTimeline 会话的完整时间轴。
//
// 按 `created_at` 升序（老 → 新）：回放是「从头看起」，倒序会让用户
// 每次都得先滚到底。
func (a *AIService) AISessionTimeline(convID string, limit int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return services.FailMsg("缺少会话 id")
	}
	if limit <= 0 || limit > aiSessionRunLimit {
		limit = aiSessionRunLimit
	}

	runs, err := a.App.DB.ListAgentRunsByConv(convID, limit)
	if err != nil {
		return services.Fail(err)
	}
	out := make([]AISessionRun, 0, len(runs))
	for i := range runs {
		run := runs[i]
		nodes, nerr := a.App.DB.ListAgentNodes(run.ID)
		if nerr != nil {
			// 单条 run 的节点读失败不该让整段回放失败：时间轴少一层细节，
			// 但用户还能看到这次任务发生过。
			nodes = nil
		}
		events, eerr := a.App.DB.ListAgentEvents(run.ID, 0, aiSessionEventLimit)
		if eerr != nil {
			events = nil
		}
		children := 0
		for _, n := range nodes {
			if strings.TrimSpace(n.ParentID) != "" {
				children++
			}
		}
		out = append(out, AISessionRun{
			Run:      &runs[i],
			Nodes:    append([]db.AgentNode{}, nodes...),
			Events:   append([]db.AgentEvent{}, events...),
			Children: children,
		})
	}
	// 升序：ListAgentRunsByConv 是倒序（最近的先），回放要反过来。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return services.Ok(map[string]any{
		"convId": convID,
		"runs":   out,
	})
}
