package ai

// spawn_agent：Supervisor 临场派活（阶段 2 的唯一新工具）。
//
// 为什么是「一个工具 + 现有工具循环」而不是先做 DAG：派活的决策依赖上一步的
// 结果（读到什么、缺什么），只有模型临场判断得出来；DAG 是把已知流程固化，
// 适合「每天同步一次报表」这类重复任务，阶段 3 再做。
//
// 这个工具注册在 mcpsrv.Agent（只有内置 AI 能调），等级 LevelWrite：
// 派活会写文件、会花钱，按只读工具对待不合适。用户在 MCP 面板把它关掉，
// 编排能力就真的没了——与其它工具共用同一道闸，不另设开关。

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
)

const spawnAgentToolName = "spawn_agent"

// agentSpawnTimeout 单个子 agent 的执行兜底超时。
//
// 与 aiToolTimeout（5 分钟）分开：子 agent 是一次完整的 agent 循环，
// 读十个文件、跑两遍测试很常见，5 分钟会把它误杀在半路——而「派了活却被
// 掐死」比「多等几分钟」糟得多，因为父 agent 拿到的是失败，会重新派一遍。
const agentSpawnTimeout = 20 * time.Minute

// agentRoleSpec 一个角色能用的工具面。
//
// 角色的意义不是「给模型换个身份提示词」，而是收窄工具面：只读的 explore
// 拿不到 file_write / run_command，即使它在任务里被要求「顺手改一下」也改不了。
// 无人值守的子 agent 必须靠能力边界而不是提示词来约束。
type agentRoleSpec struct {
	maxLevel   int
	categories []string
	summary    string
}

var agentRoleSpecs = map[string]agentRoleSpec{
	"explore": {
		maxLevel:   mcpsrv.LevelRead,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb},
		summary:    "只读排查：读文件、搜代码、查网页，不改任何东西",
	},
	"code": {
		maxLevel:   mcpsrv.LevelWrite,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb, mcpsrv.CategoryCode},
		summary:    "读写代码：读文件、改文件、跑命令（高危命令仍被拒）",
	},
	"review": {
		maxLevel:   mcpsrv.LevelRead,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb},
		summary:    "只读审阅：读代码、看 diff、给结论，不改任何东西",
	},
}

// filter 转成工具面过滤器。
//
// NoSpawn 恒为 true：子 agent 不能再派活（深度上限见 agentMaxDepth）。
// 这里再写一遍不是冗余——深度检查在 handler 里（能给出人话错误），
// 这里是不把工具挂给模型（让它根本不知道有这个能力，少走一次弯路）。
func (s agentRoleSpec) filter() *ToolFilter {
	cats := make(map[string]bool, len(s.categories))
	for _, c := range s.categories {
		cats[c] = true
	}
	return &ToolFilter{MaxLevel: s.maxLevel, Categories: cats, NoSpawn: true}
}

// registerSpawnTool 注册 spawn_agent。由 NewAIService 调用。
func (a *AIService) registerSpawnTool() {
	mcpsrv.Agent.Add(mcpsrv.Tool{
		Name: spawnAgentToolName,
		Description: "派一个子 agent 独立完成一项子任务，并拿回它的结论。" +
			"子 agent **看不到当前对话**，task 必须自带全部背景（目标、范围、期望产出）。" +
			"适合可以拆开并行做的活：分头调研几个问题、分别排查几个目录、独立审阅几个文件。" +
			"不适合：需要你来判断的取舍、需要跟用户确认的事、一句话就能答完的问题。",
		InputSchema: mcpsrv.Schema(
			"派子 agent。role 决定它能用哪些工具：explore/review 只读，code 可改文件。",
			[]string{"role", "task"},
			map[string]any{
				"role": mcpsrv.Str("角色：explore（只读排查）/ code（读写代码）/ review（只读审阅）"),
				"task": mcpsrv.Str("交给子 agent 的完整任务：写清目标、要看的范围、期望产出。它看不到当前对话"),
				"cwd":  mcpsrv.Str("子 agent 的工作目录（必须在当前工作目录之内）。不传则继承当前目录"),
			},
		),
		Level:    mcpsrv.LevelWrite,
		Category: mcpsrv.CategoryCode,
		HandlerCtx: func(ctx context.Context, args map[string]any) (any, error) {
			return a.handleSpawnAgent(ctx, args)
		},
		// 无 ctx 的路径（外部 MCP 客户端、面板里手动执行）：没有编排上下文，
		// 也不可能承载子 agent 的取消信号，直接拒绝而不是悄悄跑一个孤儿 run。
		Handler: func(args map[string]any) (any, error) {
			return nil, errors.New("spawn_agent 只能在 AI 对话里由内置 agent 调用")
		},
	})
}

// handleSpawnAgent 执行一次派活：建节点 → 跑子 agent → 回填结果。
//
// 返回 map 而不是纯文本：父 agent 需要知道「这次派活花了多少 token、成功没有」，
// 它在汇总时才能判断要不要再派。纯文本只能让模型从措辞里猜。
func (a *AIService) handleSpawnAgent(ctx context.Context, args map[string]any) (any, error) {
	sc := agentScopeFrom(ctx)
	if sc == nil || sc.svc != a {
		return nil, errors.New("没有编排上下文：spawn_agent 只能在 AI 对话里使用")
	}

	role := strings.ToLower(strings.TrimSpace(mcpsrv.Arg(args, "role")))
	spec, ok := agentRoleSpecs[role]
	if !ok {
		return nil, fmt.Errorf("未知角色 %q。可用：explore（只读排查）/ code（读写代码）/ review（只读审阅）", role)
	}
	task := strings.TrimSpace(mcpsrv.Arg(args, "task"))
	if task == "" {
		return nil, errors.New("task 不能为空：子 agent 看不到当前对话，任务必须自带全部背景")
	}
	if n := utf8.RuneCountInString(task); n > agentTaskMaxRune {
		return nil, fmt.Errorf("任务描述 %d 字，超过上限 %d：请只保留目标、范围与期望产出，不要复述上下文", n, agentTaskMaxRune)
	}
	// 深度与预算的拒绝都给出「接下来该怎么办」，否则模型会换个说法重试，
	// 把一个注定失败的调用重复到轮次用尽。
	if !sc.CanSpawn() {
		return nil, fmt.Errorf("当前已是第 %d 层 agent，不能再派活（最多 %d 层）：请直接用已有信息完成并给出结论",
			sc.depth, agentMaxDepth-1)
	}
	if sc.overBudget() {
		return nil, errors.New("本次编排的 token 预算已用完，不能再派子 agent：请用现有结果直接回答")
	}

	cwd, err := resolveSubAgentCwd(sc.cwd, mcpsrv.Arg(args, "cwd"))
	if err != nil {
		return nil, err
	}

	cfg, okp := a.getActiveAIProfile()
	model := ""
	if okp {
		model = cfg.Model
	}

	node, err := sc.reserveNode(role, task, cwd, model)
	if err != nil {
		if errors.Is(err, errAgentNodeLimit) {
			return nil, fmt.Errorf("本次编排已派 %d 个子任务，达到上限：请直接汇总现有结果回答，不要再派活",
				agentMaxNodes-1)
		}
		logger.W("[ai] 建编排节点失败 role=%s: %v", role, err)
		return nil, fmt.Errorf("无法记录这次派活：%v", err)
	}

	// 同目录写操作串行：两个 code 子 agent 同时改一棵树，必然互相踩
	// （一个把另一个刚写的文件覆盖回去，且两边都以为成功）。
	// 只读 role 不受限——它们并发读同一个目录是安全的，也是并行的价值所在。
	if spec.maxLevel >= mcpsrv.LevelWrite {
		gate := sc.cwdGate(cwd)
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		case <-ctx.Done():
			_ = a.App.DB.UpdateAgentNodeResult(node.ID, db.AgentStatusCanceled, "", db.TokenUsage{})
			return nil, errors.New("用户已停止，子 agent 未执行")
		}
	}

	logger.I("[ai] 派子 agent run=%s node=%s role=%s cwd=%s", node.RunID, node.ID, role, cwd)

	sink := &dbSink{db: a.App.DB, runID: node.RunID, nodeID: node.ID}
	res := a.runAgent(ctx, AgentRunReq{
		// 子 agent 挂在父会话上（成本归集与事件归属都靠它），但 Fresh：
		// 不读父对话历史、不往 ai_messages 里写任何东西。
		ConvID:     sc.convID,
		Mode:       "chat",
		Message:    task,
		Cwd:        cwd,
		Fresh:      true,
		Role:       role,
		RunID:      node.RunID,
		NodeID:     node.ID,
		Depth:      sc.depth + 1,
		ToolFilter: spec.filter(),
	}, sink)

	status := db.AgentStatusDone
	if res.Stopped {
		status = db.AgentStatusCanceled
	} else if res.Err != nil {
		status = db.AgentStatusFailed
	}
	if err := a.App.DB.UpdateAgentNodeResult(node.ID, status, res.Text, res.Usage); err != nil {
		logger.W("[ai] 回填子 agent 结果失败 node=%s: %v", node.ID, err)
	}
	tokens := res.Usage.Prompt + res.Usage.Completion
	if tokens > 0 {
		if err := a.App.DB.AddAgentRunUsage(node.RunID, tokens); err != nil {
			logger.W("[ai] 累加 run 用量失败 run=%s: %v", node.RunID, err)
		}
	}
	sc.addUsage(res.Usage)

	if res.Stopped {
		return nil, errors.New("子 agent 已被用户停止")
	}
	if res.Err != nil {
		return nil, fmt.Errorf("子 agent（%s）执行失败：%v", role, res.Err)
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		return nil, fmt.Errorf("子 agent（%s）没有产出任何内容（可能它判断任务无法完成），请换一种说法或自己做这一步", role)
	}
	// 用结构体而不是 map 返回：JSON 的键序由字段顺序决定，run_id / node_id 必须排在
	// 长文本之前——整条结果会被截到 2000 字再落库，放在 result 后面的字段会被切掉，
	// 而前端正是靠它们去认「这条消息对应哪棵 run 树」。
	return spawnAgentResult{
		RunID:  node.RunID,
		NodeID: node.ID,
		Role:   role,
		Tokens: tokens,
		Result: text,
	}, nil
}

// spawnAgentResult 一次派活返回给父 agent 的结果。
type spawnAgentResult struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id"`
	Role   string `json:"role"`
	Tokens int    `json:"tokens"`
	Result string `json:"result"`
}

// AgentLink 实现 agentLinked：让调用方把 run/node 写进工具调用记录，
// 前端据此在同一条回复下面挂出 run 树，不必去解析（可能被截断的）结果文本。
func (r spawnAgentResult) AgentLink() (string, string) { return r.RunID, r.NodeID }

// resolveSubAgentCwd 解析子 agent 的工作目录，并强制它落在父目录之内。
//
// 越界一律拒绝而不是「自动纠正成父目录」：子 agent 拿到一个它没被授权写的目录，
// 会在那里一路报「写操作被拒绝」，而真正的原因（越界了）永远不会出现在它的上下文里。
func resolveSubAgentCwd(parent, want string) (string, error) {
	parent = strings.TrimSpace(parent)
	want = strings.TrimSpace(want)
	if want == "" {
		return parent, nil
	}
	if parent == "" {
		return "", errors.New("当前对话没有工作目录，子 agent 无法确定在哪里干活：请先让用户把任务建在有效的空间里")
	}
	abs := want
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(parent, abs)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(filepath.Clean(parent), abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("子 agent 的工作目录 %q 不在当前工作目录之内，已拒绝", want)
	}
	return abs, nil
}

// subAgentHint 子 agent 的系统提示补充。
//
// 三条都是「没有人在旁边」带来的约束，不是风格要求：
//   - 看不到父对话：任务自包含，不要反问「你指的是哪个文件」——问不到人；
//   - 要产出结论：父 agent 只能拿到这段文字，中间过程对它不可见；
//   - 不做越权决定：需求有歧义就按最保守的解读做，并在结论里说明假设。
func subAgentHint(role string) string {
	spec, ok := agentRoleSpecs[role]
	summary := ""
	if ok {
		summary = spec.summary
	}
	return "\n\n[子 agent 上下文]\n" +
		"你是一次编排里被派活的子 agent（角色：" + role + "，" + summary + "）。" +
		"**你看不到发起方的对话**，上面这条任务就是你掌握的全部信息；也**没有用户在场**，反问不会有人回答。\n" +
		"请自己把能查的查清楚（工具都在），然后输出**结论**：做了什么、发现了什么、结论是什么、有什么前提假设。" +
		"不要输出过程叙述（「我先看看…」），父 agent 只需要你的结论。\n" +
		"任务里有歧义时按最保守的解读执行，并在结论里明确写出你的假设；做不到的部分直接说明做不到及其原因，不要假装完成。"
}

// subAgentMessages 子 agent 的初始上下文：系统提示 + 那条自包含任务。
//
// 与 buildAIMessages 的区别只有两处（都是刻意的）：
//   - 不读历史：子 agent 的任务是自包含的，带上父对话只会让它偏离任务；
//   - 不做摘要压缩：没有历史可压，压了反而多一次模型调用（要花钱）。
func (a *AIService) subAgentMessages(cfg AIProfile, mode, role, task string) []map[string]interface{} {
	system := aiModePrompts[mode]
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt + "\n\n" + system
	}
	system += subAgentHint(role)
	return []map[string]interface{}{
		{"role": "system", "content": system},
		{"role": "user", "content": task},
	}
}

// spawnFilterFor 合并「role 的工具面」与「还能不能派活」。
//
// base 为 nil 且可以派活时返回 nil（完全不收窄）——这是顶层普通对话的路径，
// 行为必须与阶段 1 逐字一致，否则每加一层过滤都是给所有对话加一次回归风险。
func spawnFilterFor(base *ToolFilter, canSpawn bool) *ToolFilter {
	if base == nil && canSpawn {
		return nil
	}
	f := ToolFilter{MaxLevel: -1}
	if base != nil {
		f = *base
	}
	if !canSpawn {
		f.NoSpawn = true
	}
	return &f
}
