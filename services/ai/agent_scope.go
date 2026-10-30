package ai

// 编排作用域：一次顶层执行的「谁能被派活、还能不能派、花了多少」。
//
// 它随 ctx 走而不是挂在 AIService 上：AIService 是单例，同时可能有三条对话在跑，
// 把 run 状态挂在服务上就会互相覆盖（出现「A 对话的子 agent 记到 B 的 run 里」）。
// ctx 是天然的请求边界——这正是 Go 里放请求级状态的地方。
//
// 懒创建 run 是有意的：普通聊天（绝大多数）根本不会派活，若每条消息都建一条 run，
// 表里会立刻堆满只有根节点的空树，而前端的 run 树面板会对着每条回复都显示一棵
// 「什么也没发生」的树。第一次真正派活时才建 run + 根节点。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"quickdock/internal/db"
)

const (
	// agentMaxDepth 编排树的最大深度。2 表示「主 agent 派子 agent」，
	// 子 agent 不能再往下派（派活深度 = depth+1 必须 < 2）。
	//
	// 为什么先卡死在两层：深度每加一层，一次提问的 token 花费按子节点数指数增长，
	// 而「子 agent 再派孙 agent」在实测里几乎总是在绕圈——真正需要三层的活儿，
	// 用 DAG（阶段 3）显式编排比让模型临场递归可控得多。
	agentMaxDepth = 2

	// agentMaxNodes 单次 run 的节点上限（含根节点）。
	// 超限后拒绝派活并给出明确文本：静默失败会让模型反复重试，把预算烧在重试上。
	agentMaxNodes = 8

	// agentMaxParallel 同一轮里最多几个子 agent 并发。
	// 3 是个折中：再多会同时压住上游的并发配额（多数网关按账号限流），
	// 而少一个又白等了。
	agentMaxParallel = 3

	// agentTaskMaxRune 单个任务描述的字数上限。
	// 子 agent 看不到父对话，任务描述就是它的全部输入；太短做不对，太长则
	// 复述一遍上下文、把父对话的 token 又付一次。4000 字足够写清范围与期望产出。
	agentTaskMaxRune = 4000
)

// agentScope 一次顶层执行的编排状态。
//
// 零值不可用，用 newAgentScope 构造。并发安全：同一轮里多个 spawn_agent
// 会同时进入 reserveNode / ensureRun。
type agentScope struct {
	svc    *AIService
	convID string
	goal   string
	cwd    string
	depth  int // 当前 agent 在树里的深度，顶层为 0
	budget int // run 级 token 上限，0 = 不限

	mu    sync.Mutex
	runID string
	// nodeID 当前这个 agent 在 run 树里对应的节点。
	// 顶层（lead）在第一次派活时随 run 一起建出来，此前为空；
	// 子 agent 在构造时就带上了自己的节点 id。
	nodeID string
	nodes  int
	used   int
	gates  map[string]chan struct{}
}

func newAgentScope(svc *AIService, convID, goal, cwd string, cfg AIProfile) *agentScope {
	return &agentScope{
		svc:    svc,
		convID: convID,
		goal:   clipRunes(strings.TrimSpace(goal), 300),
		cwd:    strings.TrimSpace(cwd),
		budget: agentRunTokenBudget(cfg),
	}
}

// newChildAgentScope 子 agent 的作用域：run 与节点都已存在，由父级传进来。
//
// 子 agent 的预算沿用同一个 run 的（sc.budget 只是本地镜像，
// 真正的超限判断看 overBudget —— 它按已用量比，不按剩余量分配）。
func newChildAgentScope(svc *AIService, convID, runID, nodeID, cwd string, depth int, cfg AIProfile) *agentScope {
	sc := newAgentScope(svc, convID, "", cwd, cfg)
	sc.runID = runID
	sc.nodeID = nodeID
	sc.depth = depth
	return sc
}

// agentRunTokenBudget 一次 run 的 token 上限（按模型上下文窗口推算）。
//
// 为什么要乘 6：单轮上下文预算是「一次调用能塞多少历史」，而一次编排会调用
// 模型很多次（主 agent 每轮 + 每个子 agent 各自多轮）。用单轮预算当 run 预算
// 会让第三个子 agent 就被判超限，白派了前两个。
func agentRunTokenBudget(cfg AIProfile) int {
	b := aiContextBudget(cfg)
	if b <= 0 {
		return 0
	}
	return b * 6
}

type agentScopeKey struct{}

func withAgentScope(ctx context.Context, sc *agentScope) context.Context {
	return context.WithValue(ctx, agentScopeKey{}, sc)
}

// agentScopeFrom 从 ctx 取编排作用域；不在编排上下文里返回 nil。
func agentScopeFrom(ctx context.Context) *agentScope {
	if ctx == nil {
		return nil
	}
	sc, _ := ctx.Value(agentScopeKey{}).(*agentScope)
	return sc
}

// Depth 当前 agent 的深度（顶层 0）。
func (sc *agentScope) Depth() int {
	if sc == nil {
		return 0
	}
	return sc.depth
}

// CanSpawn 当前 agent 还能不能派子 agent。
func (sc *agentScope) CanSpawn() bool {
	if sc == nil {
		return false
	}
	return sc.depth+1 < agentMaxDepth
}

// RunID 已创建的 run id（未派过活时为空）。
func (sc *agentScope) RunID() string {
	if sc == nil {
		return ""
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.runID
}

// CurrentNodeID 当前 agent 对应的节点 id（顶层尚未建 run 时为空）。
func (sc *agentScope) CurrentNodeID() string {
	if sc == nil {
		return ""
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.nodeID
}

// ensureRun 拿到本次执行的 run 与根节点，必要时创建（幂等）。
//
// 根节点不是可有可无的装饰：run 树要能渲染成「一个目标下面挂着几个子任务」，
// 没有根就只能在顶层平铺，用户看不出「这些子任务是为哪个问题服务的」。
func (sc *agentScope) ensureRun() (runID, nodeID string, err error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.runID != "" {
		return sc.runID, sc.nodeID, nil
	}
	if sc.svc == nil || sc.svc.App == nil || sc.svc.App.DB == nil {
		return "", "", errors.New("数据库尚未就绪，无法记录编排运行")
	}
	run, err := sc.svc.App.DB.CreateAgentRun(db.NewAgentRun{
		ConvID:       sc.convID,
		Goal:         sc.goal,
		Cwd:          sc.cwd,
		Trigger:      "chat",
		BudgetTokens: sc.budget,
	})
	if err != nil {
		return "", "", err
	}
	node, err := sc.svc.App.DB.CreateAgentNode(db.NewAgentNode{
		RunID: run.ID,
		Role:  "lead",
		Task:  sc.goal,
		Cwd:   sc.cwd,
	})
	if err != nil {
		// 建根节点失败的 run 没有渲染价值（前端拿不到树），标成失败而不是
		// 留一条永远 running 的记录——后者会让「有没有卡住的 run」这个问题永远查不清。
		_ = sc.svc.App.DB.UpdateAgentRunStatus(run.ID, db.AgentStatusFailed, err.Error())
		return "", "", err
	}
	sc.runID = run.ID
	sc.nodeID = node.ID
	sc.nodes = 1
	return sc.runID, sc.nodeID, nil
}

// reserveNode 在 run 树里登记一个子节点，并把上限检查一起做掉。
//
// 检查与写入必须在同一把锁里：两个子 agent 同时派活时，先查后写会让第 8、9 个
// 都拿到「还没满」的答案，于是上限变成 9（或更多，取决于运气）。
func (sc *agentScope) reserveNode(role, task, cwd, model string) (*db.AgentNode, error) {
	runID, parentID, err := sc.ensureRun()
	if err != nil {
		return nil, err
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.nodes >= agentMaxNodes {
		return nil, errAgentNodeLimit
	}
	node, err := sc.svc.App.DB.CreateAgentNode(db.NewAgentNode{
		RunID:    runID,
		ParentID: parentID,
		Role:     role,
		Task:     task,
		Cwd:      cwd,
		Model:    model,
	})
	if err != nil {
		return nil, err
	}
	sc.nodes++
	return node, nil
}

// addUsage 累加本次 run 已经烧掉的 token（子 agent 的用量由这里汇总）。
func (sc *agentScope) addUsage(u db.TokenUsage) {
	if sc == nil {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.used += u.Prompt + u.Completion
}

// overBudget 是否已超出 run 级预算。
func (sc *agentScope) overBudget() bool {
	if sc == nil {
		return false
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.budget > 0 && sc.used >= sc.budget
}

// cwdGate 取某个目录的串行闸。
//
// 「同 cwd 强制串行」只对会写盘的 role 生效（见 agent_spawn.go）：两个 explore
// 在同一目录里只读并发是安全的，而两个 code 一起改同一棵树必然互相踩。
// 用 channel 而不是 sync.Mutex：排队等闸的子 agent 需要能被用户点停止打断，
// Mutex 没有可取消的等待。
func (sc *agentScope) cwdGate(cwd string) chan struct{} {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.gates == nil {
		sc.gates = map[string]chan struct{}{}
	}
	g, ok := sc.gates[cwd]
	if !ok {
		g = make(chan struct{}, 1)
		sc.gates[cwd] = g
	}
	return g
}

// errAgentNodeLimit 节点数超限。单独定义是为了让 spawn 侧能给出一句
// 「该怎么继续」的话，而不是把 err.Error() 直接丢给模型。
var errAgentNodeLimit = errors.New("本次编排的子任务数量已达上限")

func clipRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}
