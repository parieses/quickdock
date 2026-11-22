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
	aitools "quickdock/services/ai/tools"
)

const spawnAgentToolName = "spawn_agent"

// agentSpawnTimeout 单个子 agent 的执行兜底超时。
//
// 与 aiToolTimeout（5 分钟）分开：子 agent 是一次完整的 agent 循环，
// 读十个文件、跑两遍测试很常见，5 分钟会把它误杀在半路——而「派了活却被
// 掐死」比「多等几分钟」糟得多，因为父 agent 拿到的是失败，会重新派一遍。
const agentSpawnTimeout = 20 * time.Minute

// agentWrapUpLead 硬超时前多久要求子 agent 收尾。
//
// 2 分钟够它把已经查到的东西写成结论（一轮模型往返十几秒）。早于这个值通知，
// 等于提前砍掉一段本来还能干活的窗口；晚于它，模型往往正在一轮长工具调用里，
// 根本来不及看这条提示。
const agentWrapUpLead = 2 * time.Minute

// aiToolsClosedMaxHints 收尾后还允许模型「再调一次工具」被拒绝几次。
//
// 工具在收尾轮仍然挂在工具面上（为了保住请求前缀缓存，见 runAgent 的 toolsClosed），
// 所以模型有可能坚持调用。每次拒绝只花一轮、且几乎全是缓存命中；给两次机会，
// 第三次还调就结束，避免病态模型把收尾拖成死循环。
const aiToolsClosedMaxHints = 2

// agentNodeTokenCap 单个子 agent 的 token 上限（软收尾）。
//
// 与窗口无关：1M 窗口算出的单轮预算是 19.3 万，子 agent 照着这个额度跑，
// 就等于允许它把整个仓库读一遍。20 万够读完几十个文件 + 几轮推理，
// 超出通常说明它在盲扫（见 subAgentHint 的三条约束）。
const agentNodeTokenCap = 200_000

// fmtRemain 剩余时间的口语写法（「1 分 20 秒」/「40 秒」）。
//
// 不直接用 Duration.String()：那是「1m20s」，会出现在给模型看的提示词里。
func fmtRemain(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
}

// agentRoleSpec 一个角色能用的工具面。
//
// 角色的意义不是「给模型换个身份提示词」，而是收窄工具面：只读的 explore
// 拿不到 file_write / run_command，即使它在任务里被要求「顺手改一下」也改不了。
// 无人值守的子 agent 必须靠能力边界而不是提示词来约束。
type agentRoleSpec struct {
	maxLevel   int
	categories []string
	// exclude 逐个撤下「等级是只读、实际有副作用」的工具（见 ToolFilter.Exclude）。
	exclude map[string]bool
	summary string
}

// readOnlyExcludes 只读角色要摘掉的「伪只读」工具。
//
// 两个都在 code 域、都是 LevelRead，靠等级挡不住：project_memory 的
// action=write 真的写 MEMORY.md（无人值守的子 agent 顺手改掉项目记忆是事故），
// plan_update 是整份覆盖编排共用的清单（子 agent 改它会把父级的清单冲掉）。
var readOnlyExcludes = map[string]bool{
	"project_memory": true,
	"plan_update":    true,
}

// noFileWriteExcludes 给「能跑命令、但不能用文件工具改代码」的角色（tester / ops）。
// run_command 本身是 LevelWrite，必须留着才能跑测试、查端口；shell 里理论上仍能
// 写盘，所以这里撤的是 file_edit / file_write 这两个显式写工具，不是假装命令无副作用。
var noFileWriteExcludes = map[string]bool{
	"file_write":     true,
	"file_edit":      true,
	"project_memory": true,
	"plan_update":    true,
}

// noCommandExcludes 给「能写文件、但不跑命令」的角色（writer）。
//
// agent 注册表里**没有 file 域**：file_edit / file_write 与 run_command 同在 code 域，
// 所以不能靠关掉 code 域来禁命令，只能按工具名逐个撤。也正因如此，writer 的写权限
// 覆盖工作区内任意文件——ToolFilter 没有路径白名单，别把它描述成「只能写文档目录」。
var noCommandExcludes = map[string]bool{
	"run_command":    true,
	"project_memory": true,
	"plan_update":    true,
}

// code 域里的**只读**工具（git_status / git_diff / dir_list / check_workdir /
// job_status / job_output / tool_result_read）。只读角色要能拿到它们：
//
// 原来 explore / review 只开 File + Web 两个域，而 git 工具在 code 域，于是
// 「看看未提交改动」这类任务里子 agent 拿不到 git_diff，只能全仓 file_grep 盲扫
// ——实测 6 个子 agent 各跑 40–79 次工具调用、单人烧掉 122 万 prompt token。
// 放开域而不放开等级是关键：file_write / file_edit / run_command 同在 code 域，
// 但它们是 LevelWrite，仍被 maxLevel 挡在外面。
var agentRoleSpecs = map[string]agentRoleSpec{
	"explore": {
		maxLevel:   mcpsrv.LevelRead,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb, mcpsrv.CategoryCode},
		exclude:    readOnlyExcludes,
		summary:    "只读排查：读文件、搜代码、看目录、看 git 改动、查网页，不改任何东西",
	},
	"review": {
		maxLevel:   mcpsrv.LevelRead,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb, mcpsrv.CategoryCode},
		exclude:    readOnlyExcludes,
		summary:    "只读审阅：读代码、看 diff、给结论，不改任何东西",
	},
	"auditor": {
		maxLevel:   mcpsrv.LevelRead,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb, mcpsrv.CategoryCode},
		exclude:    readOnlyExcludes,
		summary:    "只读安全审计：查依赖漏洞、危险配置与敏感信息，给风险清单，不改任何东西",
	},
	"tester": {
		maxLevel:   mcpsrv.LevelWrite,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb, mcpsrv.CategoryCode},
		exclude:    noFileWriteExcludes,
		summary:    "测试验证：可跑测试与只读命令、读代码，撤掉了显式文件写入工具（shell 里仍可能写盘）",
	},
	"ops": {
		maxLevel:   mcpsrv.LevelWrite,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb, mcpsrv.CategoryCode},
		exclude:    noFileWriteExcludes,
		summary:    "运维排查：可跑命令、看服务/端口/后台任务与日志，撤掉了显式文件写入工具",
	},
	"writer": {
		maxLevel:   mcpsrv.LevelWrite,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb, mcpsrv.CategoryCode},
		exclude:    noCommandExcludes,
		summary:    "文档写作：可读写文件、查网页，不跑命令、不改计划与项目记忆（写权限没有路径白名单）",
	},
	"code": {
		maxLevel:   mcpsrv.LevelWrite,
		categories: []string{mcpsrv.CategoryFile, mcpsrv.CategoryWeb, mcpsrv.CategoryCode},
		summary:    "读写代码：读文件、改文件、跑命令（高危命令仍被拒）",
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
	return &ToolFilter{MaxLevel: s.maxLevel, Categories: cats, NoSpawn: true, Exclude: s.exclude}
}

// agentRoleOrder 角色的固定展示顺序。
//
// map 遍历无序，而这段文案会进每轮请求的 system/工具描述：顺序随机会让上游的
// 前缀缓存每轮都失效，白付一遍钱。
var agentRoleOrder = []string{"explore", "review", "auditor", "tester", "ops", "writer", "code"}

// agentRoleNames 生成短名单（explore（只读排查）/ ...），工具 schema 与错误提示共用。
//
// 这两处曾经各写死一份角色说明，加角色时漏了一处——模型看到的是旧名单，
// 用户却已经能用新角色。从 specs 生成：summary 的中文冒号前就是短说明。
// 顺序取 agentRoleOrder，固定不变，避免每轮请求的前缀缓存因 map 遍历顺序失效。
func agentRoleNames() string {
	parts := make([]string, 0, len(agentRoleOrder))
	for _, name := range agentRoleOrder {
		spec, ok := agentRoleSpecs[name]
		if !ok {
			continue
		}
		brief := spec.summary
		if i := strings.Index(brief, "："); i > 0 {
			brief = brief[:i]
		}
		parts = append(parts, name+"（"+brief+"）")
	}
	return strings.Join(parts, " / ")
}

// agentRoleBrief 渲染「每个角色能干什么、不能干什么」。
//
// 从 agentRoleSpecs 生成而不是另写一份文案：这次踩的坑正是「task 里让 explore
// 用 git_diff，而它的工具面里根本没有」——角色能力与写给模型的说明必须同源。
func agentRoleBrief() string {
	var b strings.Builder
	for _, name := range agentRoleOrder {
		spec, ok := agentRoleSpecs[name]
		if !ok {
			continue
		}
		b.WriteString("\n- ")
		b.WriteString(name)
		b.WriteString("：")
		b.WriteString(spec.summary)
		if spec.maxLevel < mcpsrv.LevelWrite {
			b.WriteString("（**不能**写文件、跑命令、派活）")
		}
	}
	return b.String()
}

// registerSpawnTool 注册 spawn_agent。由 NewAIService 调用。
func (a *AIService) registerSpawnTool() {
	mcpsrv.Agent.Add(mcpsrv.Tool{
		Name: spawnAgentToolName,
		Description: fmt.Sprintf("派一个子 agent 独立完成一项子任务，并拿回它的结论。"+
			"多个独立子任务可以在同一轮里同时派出（**一轮最多 %d 个并发，本次编排合计最多 %d 个子任务**）；"+
			"拆成几块就派几个，别只派两三个把剩下的活留给自己。", agentMaxParallel, agentMaxNodes-1) +
			"子 agent **看不到当前对话**，task 必须自带全部背景（目标、范围、期望产出）；" +
			"要按「改了哪些文件」分工时，先自己跑一次 git_status + git_diff（stat 即可），" +
			"把文件名与增删行数直接写进 task——别指望子 agent 自己去猜。" +
			agentRoleBrief() + "\n" +
			// 「什么时候值得派」放在「不适合」前面，而且不提「并行」：
			// 原来的措辞是「适合可以拆开并行做的活」，读起来像「不能同时跑就别派」，
			// 于是串行分治的大任务全留在主循环里硬跑（实机反馈 U2）。
			"什么时候值得派：任务超过三步，或者要分别去看多个目录/多个问题/多个文件——" +
			"**默认把能独立完成的条目派出去**，主对话只留需要串起来判断的部分。串行分治也算，不要求能同时跑。" +
			"按条目性质选角色（" + agentRoleNames() + "）：排查/审阅/安全审计用只读角色，" +
			"跑测试、查环境用 tester / ops，写文档用 writer，动手改代码的独立条目用 code；" +
			"别因为怕子 agent 改文件就把活全留给自己。" +
			"不适合：需要你来判断的取舍、需要跟用户确认的事、一句话就能答完的问题。",
		InputSchema: mcpsrv.Schema(
			"派子 agent。role 决定它能用哪些工具："+agentRoleNames()+"。",
			[]string{"role", "task"},
			map[string]any{
				"role": mcpsrv.Str("角色：" + agentRoleNames()),
				"task": mcpsrv.Str("交给子 agent 的完整任务：写清目标、要看的范围、期望产出。它看不到当前对话"),
				"cwd":  mcpsrv.Str("子 agent 的工作目录（必须在当前工作目录之内）。不传则继承当前目录"),
			},
		),
		Level:    mcpsrv.LevelWrite,
		Category: mcpsrv.CategoryCode,
		// 风险说明跟着工具走：这张卡要挡住的是「派活」这个动作，但用户很容易
		// 把它理解成「改几个文件」。差别在点完之后——子 agent 在自己的循环里跑，
		// 非高危命令不再逐次弹确认，用户要到结果交回主对话才看得到发生了什么。
		// 不写这半句，用户就是在完全不知情的情况下批准了一个无人值守的执行者。
		RiskNote: func(args map[string]any) string {
			role := strings.ToLower(strings.TrimSpace(mcpsrv.Arg(args, "role")))
			if spec, ok := agentRoleSpecs[role]; ok {
				return "派一个子 agent 独立执行：" + spec.summary +
					"。批准后它在自己的循环里连续动手，中途不会再逐条向你确认（高危命令仍被拒），结果交回本对话"
			}
			return "派一个子 agent 独立执行，中途不会再逐条向你确认（高危命令仍被拒）"
		},
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
		return nil, fmt.Errorf("未知角色 %q。可用：%s", role, agentRoleNames())
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
		used, budget := sc.budgetUsage()
		return nil, fmt.Errorf("本次编排已用约 %d token，达到总额度 %d：不能再派子 agent，"+
			"请用现有结果直接回答（剩下的活自己做，别再拆）", used, budget)
	}

	cwd, err := resolveSubAgentCwd(sc.cwd, mcpsrv.Arg(args, "cwd"))
	if err != nil {
		return nil, err
	}

	cfg, okp := a.aiProfileForRun(sc.profileID, sc.convID)
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

	// 单个子任务的取消入口（见 AICancelAgentNode）。
	//
	// 与「父级 ctx」是父子关系：用户点停止会连带取消它，而取消它不影响父级——
	// 这正是「砍掉跑偏的那一个，其余继续跑」需要的语义。
	//
	// 注册在等目录闸**之前**：code 角色可能正排在同目录的队列里（另一个 code 在跑），
	// 那段时间它还没有任何产出，正是最该能一键砍掉的时刻。
	childCtx, cancelChild := context.WithCancel(ctx)
	defer cancelChild() // 包里顺手收掉，避免 goroutine 泄漏
	a.nodeCancels.Store(node.ID, cancelChild)
	defer a.nodeCancels.Delete(node.ID)

	// 同目录写操作串行：两个 code 子 agent 同时改一棵树，必然互相踩
	// （一个把另一个刚写的文件覆盖回去，且两边都以为成功）。
	// 只读 role 不受限——它们并发读同一个目录是安全的，也是并行的价值所在。
	if spec.maxLevel >= mcpsrv.LevelWrite {
		gate := sc.cwdGate(cwd)
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		case <-childCtx.Done():
			// 轨迹关闭时 node 是内存态（ID 为空），没有记录可回填。
			if node.ID != "" {
				_ = a.App.DB.UpdateAgentNodeResult(node.ID, db.AgentStatusCanceled, "", db.TokenUsage{})
			}
			return nil, errors.New("用户已停止，子 agent 未执行")
		}
	}

	logger.I("[ai] 派子 agent run=%s node=%s role=%s cwd=%s", node.RunID, node.ID, role, cwd)

	sink := EventSink(&dbSink{db: a.App.DB, runID: node.RunID, nodeID: node.ID})
	parent := eventSinkFrom(ctx)
	if parent != nil {
		// 子 agent 的进度只挑关键帧转给父级（见 childRelaySink）：主对话里那张
		// 「派活」卡片因此能显示「第 N 轮 · 在调什么」，不用让用户去开子任务抽屉。
		callID := aitools.ToolCallIDFrom(ctx)
		sink = &multiSink{sinks: []EventSink{
			sink,
			&childRelaySink{parent: parent, callID: callID, nodeID: node.ID, role: role},
		}}
		// 起始帧：模型可能几十秒之后才调第一个工具，这段空白里卡片上得先有字。
		parent.Event("child", map[string]any{
			"phase": "start", "callId": callID, "nodeId": node.ID, "role": role,
		})
	}

	res := a.runAgent(childCtx, AgentRunReq{
		// 子 agent 挂在父会话上（成本归集与事件归属都靠它），但 Fresh：
		// 不读父对话历史、不往 ai_messages 里写任何东西。
		ConvID:  sc.convID,
		Mode:    "chat",
		Message: task,
		Cwd:     cwd,
		// 范围跟着编排的根走，不跟子目录走（理由见 AgentRunReq.ScopeCwd）。
		ScopeCwd: sc.scope,
		// 子 agent 跟父级用同一个档案：Fresh 的 runAgent 不读父会话的
		// profile_id，不显式带就会回落到全局激活项。
		ProfileID:  sc.profileID,
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
	tokens := res.Usage.Prompt + res.Usage.Completion
	// 轨迹关闭时 node 是内存态（ID 为空）：没有节点/事件表记录要回填，
	// 但用量与预算统计照常——关掉轨迹只是不落录像，不是不干活。
	if node.ID != "" {
		if err := a.App.DB.UpdateAgentNodeResult(node.ID, status, res.Text, res.Usage); err != nil {
			logger.W("[ai] 回填子 agent 结果失败 node=%s: %v", node.ID, err)
		}
		if tokens > 0 {
			if err := a.App.DB.AddAgentRunUsage(node.RunID, tokens); err != nil {
				logger.W("[ai] 累加 run 用量失败 run=%s: %v", node.RunID, err)
			}
		}
	}
	sc.addUsage(res.Usage)

	text := strings.TrimSpace(res.Text)
	// 半成品也要交回去。
	//
	// 原来 res.Err != nil 一律丢成「执行失败」：子 agent 查了十几分钟、写下的
	// 结论全被扔掉，父 agent 只能重派一遍（同一次活付两遍钱）。而 runAgent 在
	// 取消 / 报错时**已经**把半截正文放进了 res.Text——只是这里没接。
	// 代价是父 agent 可能把残本当完整结论用，所以必须带 partial 与 note 说清楚。
	partial := func(note string) any {
		return spawnAgentResult{
			RunID: node.RunID, NodeID: node.ID, Role: role, Tokens: tokens,
			Partial: true, Note: clipRunes(note, 200), Result: text,
		}
	}
	if res.Stopped {
		if text != "" {
			return partial("子任务被中止，下面是中止前已产出的部分结论"), nil
		}
		return nil, errors.New("子 agent 已被用户停止")
	}
	if res.Err != nil {
		if text != "" {
			return partial("子任务未跑完：" + res.Err.Error()), nil
		}
		return nil, fmt.Errorf("子 agent（%s）执行失败：%v", role, res.Err)
	}
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
//
// 字段顺序不是随意的：整条结果会被截到 2000 字再进上下文，所以短字段
// （run_id / node_id / partial / note）必须排在 result 之前，否则长结论一铺，
// 前端与父 agent 都看不到「这是不是一个残本」。
type spawnAgentResult struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id"`
	Role   string `json:"role"`
	Tokens int    `json:"tokens"`
	// Partial 子任务没跑完（超时 / 被停 / 报错）但已产出部分结论。
	// 父 agent 必须据此判断「这能不能当结论用」，否则会把残本当完整答案。
	Partial bool   `json:"partial,omitempty"`
	Note    string `json:"note,omitempty"`
	Result  string `json:"result"`
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
// 前半三条是「没有人在旁边」带来的约束，不是风格要求：
//   - 看不到父对话：任务自包含，不要反问「你指的是哪个文件」——问不到人；
//   - 要产出结论：父 agent 只能拿到这段文字，中间过程对它不可见；
//   - 不做越权决定：需求有歧义就按最保守的解读做，并在结论里说明假设。
//
// 后半三条来自一次实测事故（2026-11-19）：6 个子 agent 各跑 40–79 次工具调用、
// 单人烧掉 122 万 prompt token，原因全是「不知道哪些文件改了」而全仓盲扫、
// 每次 file_read 都吃满 400 行默认值、结论写了两千多字回灌父上下文。
// 这三条针对的正是这三个具体的浪费。
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
		// 实测（2026-11-19）：四个 review 各跑 8 轮、每次 27 万 prompt token，
		// 最后正文只剩一句「我先查看…再逐个审阅」——思考模型把分析全写在
		// reasoning 里，发起方一个字都拿不到。这条必须单独说清楚。
		"**结论必须写在正文里**：发起方只能看到你的正文，看不到你的思考过程；" +
		"把分析全写在思考里、正文只留一句过渡语，等于什么都没交付。\n" +
		"任务里有歧义时按最保守的解读执行，并在结论里明确写出你的假设；做不到的部分直接说明做不到及其原因，不要假装完成。\n" +
		// —— 下面三条是「怎么省时间省钱」，不是礼貌要求 ——
		"**范围已经给定就直接读**：任务里点名了目录/文件/模块，就逐个 dir_list + file_read 过去；" +
		"**不要全仓 file_grep 去猜范围**（那是把几十万 token 花在无关文件上）。\n" +
		"**读文件要精确**：先 file_grep 拿到行号，再用 file_read 的 offset / limit 只读那一段；" +
		"不传 offset/limit 时默认读 400 行——那是默认值，不是规定值。同一个文件不要反复全文重读。\n" +
		"**结论要短**：控制在 800 字以内，只写父 agent 做决策需要的事实与判断（改了什么、在哪、风险是什么）。" +
		"结论会被贴回父对话，写长了会挤掉父 agent 自己的思考空间。"
}

// subAgentMessages 子 agent 的初始上下文：系统提示 + 那条自包含任务。
//
// 与 buildAIMessages 的区别只有两处（都是刻意的）：
//   - 不读历史：子 agent 的任务是自包含的，带上父对话只会让它偏离任务；
//   - 不做摘要压缩：没有历史可压，压了反而多一次模型调用（要花钱）。
//
// **角色提示不在这里拼**：它按角色不同而不同，嵌在 system 中段会把同一次
// 编排里 N 个子 agent 的共享前缀截断在那里。挪到 runAgent 里拼在所有稳定段
// （workdir/scale/rules）之后，子 agent 之间、子 agent 与父级之间就能共享
// 「档案提示+模式+稳定段」这一大段前缀，各自冷启动的部分只剩角色差异。
func (a *AIService) subAgentMessages(cfg AIProfile, mode, task string) []map[string]interface{} {
	system := aiModePrompts[mode]
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt + "\n\n" + system
	}
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
