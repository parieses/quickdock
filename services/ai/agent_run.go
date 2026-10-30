package ai

// 一次 agent 运行的完整生命周期。
//
// 这段逻辑原来整个躺在 ai_stream.go 的 SSE handler 里，事件出口直接是
// http.ResponseWriter，于是「一条 HTTP 连接 = 一次执行、只能前台同步跑」。
// 多 agent 编排要的三件事——后台跑、并行跑、可中断可恢复——一条都要求
// 执行核心与 HTTP 解耦，所以把它抽成可重入函数：调用方给 ctx 和 sink，
// 引擎不碰 w / flusher，也就不可能往一个不存在的连接里写事件。
//
// 边界约定（改这里之前先看）：
//   - 本文件不写 HTTP 状态码。RunAgent 被调用时响应头早已以 200 发出去了，
//     之后所有失败都只能是 event: error，再想改状态码也改不了。
//   - 本文件不发桌面通知。notifyAIDone 判断的是窗口焦点与当前页面，
//     那是 UI 关注点，留给调用方。
//   - 本文件不决定 sink 的生命周期（谁创建谁关闭）。

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	aitools "quickdock/services/ai/tools"
)

// 执行期的前置校验错误。
//
// 单独定义而不是就地 errors.New：这些错误已经在事件里回过前端了，返回值只用来
// 让调用方判断「这次运行是否正常结束」（比如要不要发完成通知）——用哨兵值比
// 比较错误文本可靠。真正来自上游/DB 的错误仍原样返回，不做包装。
var (
	errNoAPIKey     = errors.New("未配置 API Key")
	errEmptyMessage = errors.New("消息为空")
	errDBNotReady   = errors.New("数据库尚未就绪")
	errConvNotFound = errors.New("会话不存在或已被删除")
)

// EventSink 执行期事件的唯一出口。
//
// 三个方法对应 sseFlusher 现成的 push / writeEvent / failed，所以 SSE 那条路的
// 适配成本为零；而编排器可以给一个把事件写进 agent_events 的实现，引擎一行都不用改。
type EventSink interface {
	// Push 高频文本碎片（token / reasoning）。允许丢（下游已断时）。
	Push(kind, text string)
	// Event 结构事件（conv / cwd / tool / tool_result / notice / error / done）。
	Event(kind string, data map[string]any)
	// Failed 下游是否已断开。引擎据此放弃后续轮次。
	Failed() bool
}

// convIDSetter 由能感知会话 id 的 sink 实现。
//
// 会话 id 在 RunAgent 内部才确定（新建会话时由 DB 生成），而 SSE 的每个文本帧都要
// 带上 convId。让引擎在拿到 id 的第一时间通知 sink，比让 sink 去猜、或者把
// newSSEFlusher 推迟到「会话已确定」之后再创建要干净——后者会把 HTTP 细节倒灌回引擎。
type convIDSetter interface {
	SetConvID(id string)
}

// AgentRunReq 一次运行的入参，与 SSE 请求体的字段同构。
type AgentRunReq struct {
	ConvID  string // 空 = 新建会话
	Mode    string // chat / plan / translate / summarize / explain / tutorial
	SpaceID string // 仅新建会话时生效
	Message string
	Cwd     string
	Images  []AIChatImage
	// Mentions 本次提问里用 @ 显式引用的文件 / 目录 / 历史对话（见 ai_mentions.go）。
	// 只影响这一次请求的上下文，不落库：引用是「这次要看它」，不是长期状态。
	Mentions []AIMention

	// ── 编排维度（阶段 2）。顶层对话这四项全为零值，行为与阶段 1 完全一致 ──

	// RunID / NodeID 这次执行属于哪次编排的哪个节点。
	// 顶层为空 → 第一次派活时由 agentScope 懒创建（见 agent_scope.go）。
	RunID  string
	NodeID string
	// Role 子 agent 的角色（explore / code / review），只用于系统提示里的自我说明。
	// 工具面由 ToolFilter 决定——提示词说「你是只读的」而工具其实可写，
	// 是比不说更糟的谎。
	Role string
	// Depth 当前 agent 在编排树里的深度（顶层 0），决定还能不能继续派活。
	Depth int
	// Fresh 子 agent 模式：不读父会话历史、不往 ai_messages 写任何东西。
	//
	// 为什么必须分开而不是「同一个会话继续聊」：子 agent 看到父对话会让上下文
	// 和花费随父对话线性增长，也更容易顺手做用户没让它做的事。它的全部输入
	// 就是那条自包含的 task。
	Fresh bool
	// ToolFilter 按 role 再收窄工具面；nil = 沿用现有按 mode 的规则。
	ToolFilter *ToolFilter
}

// AgentRunResult 一次运行的结果。
type AgentRunResult struct {
	ConvID  string
	Text    string
	Stopped bool // 用户中断
	// Usage 这次执行的累计用量。编排层用它做 run 级成本归集：
	// 每次都去 ai_usage 查一遍既慢又会把「编排外的调用」混进来。
	Usage     db.TokenUsage
	Rounds    int
	ToolCount int
	Err       error
}

// RunAgent 跑完一次多轮 agent 执行：组装上下文 → 逐轮调模型 → 执行工具 → 落库。
//
// ctx 的取消代表用户中断（点停止、前端断开）。中断不是错误：已经收到的部分正文
// 会照常落库并标 stopped，这样刷新会话还能看到半截回答，而不是凭空消失。
func (a *AIService) RunAgent(ctx context.Context, req AgentRunReq, sink EventSink) AgentRunResult {
	res := AgentRunResult{ConvID: strings.TrimSpace(req.ConvID)}

	// 首字延迟的起点。必须在函数入口取：用户感知的等待是「按下回车到第一个字」，
	// 含上下文组装（查库 + 摘要压缩）的耗时。挪到调模型之前会让这个数字凭空变小。
	streamStart := time.Now()
	var firstTokenAt time.Duration

	mode := strings.TrimSpace(req.Mode)
	message := strings.TrimSpace(req.Message)

	cfg, okp := a.getActiveAIProfile()
	if !okp || cfg.APIKey == "" {
		// 用户报「AI 不回话」时，最常见原因就是没配 Key；这条必须留痕。
		logger.W("[ai] 请求被拒：未配置 API Key（conv=%s mode=%s）", res.ConvID, mode)
		sink.Event("error", map[string]any{"message": "请在设置中配置 API Key"})
		res.Err = errNoAPIKey
		return res
	}
	// 纯图片提问（“这张图里有什么”）是合法场景：文字为空但有图片时放行。
	if message == "" && len(req.Images) == 0 {
		sink.Event("error", map[string]any{"message": "消息为空"})
		res.Err = errEmptyMessage
		return res
	}
	if mode != "" && aiModePrompts[mode] == "" {
		// 传了不认识的模式：回落 chat 而不是拒绝——模式只是提示词分支，
		// 为它中断一次对话不值得。
		mode = "chat"
	}

	// 图片闸门 + 落盘：按档案声明的 imageInput 能力校验（见 ai_image.go），
	// 本地图写盘后只把路径引用带去上下文。放在建会话之前——图片不合法时
	// 不该先留下一个空会话。
	imagesJSON, err := a.prepareChatImages(cfg, req.Images)
	if err != nil {
		logger.W("[ai] 图片校验失败 conv=%s: %v", res.ConvID, err)
		sink.Event("error", map[string]any{"message": err.Error()})
		res.Err = err
		return res
	}

	var conv *db.AIConversation
	if a.App.DB == nil {
		sink.Event("error", map[string]any{"message": "数据库尚未就绪，请稍后重试"})
		res.Err = errDBNotReady
		return res
	}
	// 子 agent（Fresh）不建也不读会话行：它借父会话的 id 做成本归集，
	// 但没有自己的消息流，也就不该在会话表里留下任何痕迹。
	if !req.Fresh {
		if res.ConvID == "" {
			created, cerr := a.App.DB.CreateAIConversation(db.NewAIConversation{
				Mode:    mode,
				SpaceID: strings.TrimSpace(req.SpaceID),
			})
			if cerr != nil {
				sink.Event("error", map[string]any{"message": cerr.Error()})
				res.Err = cerr
				return res
			}
			conv = created
			res.ConvID = conv.ID
			if cs, ok := sink.(convIDSetter); ok {
				cs.SetConvID(res.ConvID)
			}
			sink.Event("conv", map[string]any{
				"id": conv.ID, "title": conv.Title, "spaceId": conv.SpaceID, "mode": conv.Mode,
			})
		} else {
			conv, _ = a.App.DB.GetAIConversation(res.ConvID)
			// 请求没带模式时以任务自己的为准：模式是这个任务创建时选定的属性，
			// 「前端这次没传」不等于「改回对话」。
			if conv != nil && mode == "" {
				mode = strings.TrimSpace(conv.Mode)
			}
		}
	}
	if aiModePrompts[mode] == "" {
		mode = "chat"
	}
	// 会话可能被用户在流式进行中删除：此时继续写入会产生孤儿消息，直接终止并提示。
	if !req.Fresh && conv == nil {
		logger.E("[ai-stream] 会话不存在或已被删除，终止流式 convID=%s", res.ConvID)
		sink.Event("error", map[string]any{"message": "会话不存在或已被删除"})
		res.Err = errConvNotFound
		return res
	}

	// 工作目录：请求里带了就以它为准（用户刚在界面上切的），并写回会话；
	// 没带就沿用这个会话上次绑定的。绑在会话上而不是全局，是因为同时开两个
	// 项目对话是常态，而「在哪个目录里干活」必须跟着对话走。
	//
	// 目录不存在（用户删了/盘拔了）时不报错中断，而是当作未绑定：
	// 拿一个不存在的目录去调工具，会得到一堆「找不到文件」，用户根本看不出根因在这儿。
	cwd := strings.TrimSpace(req.Cwd)
	if req.Fresh {
		// 子 agent：目录由父级解析并校验过（见 resolveSubAgentCwd），这里只做存在性兜底。
		// 不回退到会话/空间：那会让它跑到父级没授权的目录里。目录不可用就按未绑定处理，
		// 写操作会在工具层被拒绝——比悄悄换个目录去写安全。
		if cwd != "" {
			if st, serr := os.Stat(cwd); serr != nil || !st.IsDir() {
				logger.W("[ai] 子 agent 的工作目录不可用 conv=%s cwd=%s: %v", res.ConvID, cwd, serr)
				cwd = ""
			}
		}
	} else if cwd != "" {
		if st, serr := os.Stat(cwd); serr != nil || !st.IsDir() {
			logger.W("[ai] 请求里的工作目录不可用，忽略 conv=%s cwd=%s: %v", res.ConvID, cwd, serr)
			cwd = ""
		}
	}
	// 绑定回写与空间回退都只对顶层执行：子 agent 的目录已经定了，改会话上的 cwd
	// 会让「用户下次打开这条对话」莫名其妙换到子 agent 的目录去。
	if !req.Fresh && cwd != "" && conv.Cwd != cwd {
		if err := a.App.DB.SetAIConversationCwd(res.ConvID, cwd); err != nil {
			logger.W("[ai] 保存会话工作目录失败 conv=%s: %v", res.ConvID, err)
		}
	} else if !req.Fresh && cwd == "" {
		// 任务自己没绑目录时回退到空间的目录。换项目改空间一处即可，
		// 不必逐条任务改；这也是空间存在的意义。
		fallback := strings.TrimSpace(conv.Cwd)
		if fallback == "" && strings.TrimSpace(conv.SpaceID) != "" {
			if sp, serr := a.App.DB.GetAISpace(conv.SpaceID); serr == nil && sp != nil {
				fallback = strings.TrimSpace(sp.Cwd)
			}
		}
		// 连空间都没有：给这个任务分配一个专属目录，AI 生成的文件有地方放。
		// 没有目录 = 写操作全被白名单拒绝，模型只能干瞪眼。
		if fallback == "" {
			fallback = a.ensureTaskDir(res.ConvID)
		}
		cwd = fallback
		if cwd != "" {
			if st, serr := os.Stat(cwd); serr != nil || !st.IsDir() {
				logger.W("[ai] 会话/空间绑定的工作目录已不可用，本次按未绑定处理 conv=%s cwd=%s", res.ConvID, cwd)
				cwd = ""
			}
		}
	}

	// 目录是本轮才定下来的（新分配的任务目录，或请求里刚带过来的）：回传前端，
	// 否则工具栏上那条目录指示要等「切走再切回」才对得上——而那正是用户最需要
	// 知道「AI 会把文件写到哪」的时刻。
	if !req.Fresh && cwd != "" && strings.TrimSpace(conv.Cwd) != cwd {
		sink.Event("cwd", map[string]any{"cwd": cwd})
	}

	// 编排作用域随 ctx 走（不是服务字段）：顶层在这里新建，子 agent 用父级传进来的
	// RunID/NodeID/Depth 构造。两条路径共用一个类型，深度与预算的判断因此只有一处实现。
	var sc *agentScope
	if req.Fresh {
		sc = newChildAgentScope(a, res.ConvID, req.RunID, req.NodeID, cwd, req.Depth, cfg)
	} else {
		sc = newAgentScope(a, res.ConvID, message, cwd, cfg)
	}
	ctx = withAgentScope(ctx, sc)

	// 审批闸门（见 ai_approval.go）。同样挂 ctx、同样只挂顶层执行：
	// 子 agent 用 dbSink 跑在后台，没有「人」在等它，审批事件发出去只会等到超时
	// 然后自动拒绝，把 code 角色整个废掉。子 agent 的约束是工具面
	// （agentRoleSpec 的 maxLevel），而派活本身（spawn_agent，LevelWrite）
	// 已经在父级过了这道闸——「批准派一个 code 角色」就是「批准它能改文件」。
	if !req.Fresh {
		gate := newApprovalGate(sink, res.ConvID, a.App.DB, a.loadApprovalRules())
		a.registerGate(res.ConvID, gate)
		defer a.unregisterGate(res.ConvID)
		ctx = withApprovalGate(ctx, gate)
	}

	// 编辑快照钩子（见 ai_checkpoint.go）。这一条子 agent **也要挂**，
	// 与审批相反：子 agent 改的文件同样得能回滚，而它借的就是父会话的 convID，
	// 快照因此自然归到同一条对话的回滚点列表里——用户在界面上看到的是一整串
	// 改动，不必去猜哪一步是子 agent 干的。
	if hook := newCheckpointStore(a.App.DB, res.ConvID); hook != nil {
		ctx = aitools.WithCheckpointHook(ctx, hook)
	}

	// 调模型前先落库用户消息：无论后续成功 / 失败 / 被打断，历史都不会出现空会话，
	// 用户刷新或重进会话都能看到自己问了什么，而不是点进来一片空白。
	//
	// 子 agent 跳过：它的 task 不是「用户说的话」，落进 ai_messages 会让侧栏那条
	// 对话里凭空冒出几条不是用户写的提问。
	if !req.Fresh {
		if err := a.saveUserTurn(res.ConvID, message, imagesJSON); err != nil {
			logger.W("[ai] 保存用户消息失败 conv=%s: %v", res.ConvID, err)
			sink.Event("error", map[string]any{"message": err.Error()})
			res.Err = err
			return res
		}
	}

	// 组装上下文（含摘要压缩）
	// 注意：用户消息尚未存 DB，buildAIMessages 会从 DB 加载历史 + 追加 currentUser 作为最后一条。
	// 模型回复后统一落库两条消息，避免重复。
	var messages []map[string]interface{}
	if req.Fresh {
		// 子 agent 的上下文只有「角色提示 + 那条自包含的 task」：
		// 不读父对话历史（那会让上下文与花费随父对话线性增长）。
		messages = a.subAgentMessages(cfg, mode, req.Role, message)
	} else {
		var berr error
		messages, berr = a.buildAIMessages(ctx, cfg, res.ConvID, mode)
		if berr != nil {
			logger.W("[ai] 组装上下文失败 conv=%s mode=%s: %v", res.ConvID, mode, berr)
			sink.Event("error", map[string]any{"message": berr.Error()})
			res.Err = berr
			return res
		}
	}

	// @ 引用（见 ai_mentions.go）：展开成材料贴到最后一条用户消息上。
	//
	// 只影响这一次请求、不落库：用户气泡里显示的是自己打的字，历史里也只存那些字。
	// 子 agent 跳过——它的上下文只有角色提示 + 那条自包含的 task，没有「用户消息」可贴；
	// 派活时父级已经把该给的材料写进 task 了。
	//
	// 不新加事件类型：展开失败已经在 buildMentionBlock 里走 notice 报给用户，
	// 而「被截断了多少行」写在上下文里给模型看——它需要知道自己拿到的是残本，
	// 用户那边看到的引用 chip 本来就不含内容。
	var mentionBlock string
	if !req.Fresh && len(req.Mentions) > 0 {
		mentionBlock = a.buildMentionBlock(cwd, req.Mentions, sink)
		applyMentionBlock(messages, mentionBlock)
	}

	// 首次消息自动生成标题（子 agent 不参与：它没有会话行）
	if !req.Fresh && conv != nil && conv.Title == "" {
		title := message
		if title == "" {
			// 纯图片提问：用固定词兜底，否则标题为空、列表里显示“新对话”
			title = "图片问答"
		}
		if utf8.RuneCountInString(title) > 30 {
			title = string([]rune(title)[:30]) + "…"
		}
		_ = a.App.DB.UpdateAIConversationMeta(res.ConvID, title, "")
		sink.Event("conv", map[string]any{"id": res.ConvID, "title": title})
	}

	// 工具集：与外部 MCP 客户端同一份（等级门与单工具开关都已应用过）。
	// 用户在内置 AI 页关掉某个工具的方式就是去 MCP 面板关它——不另设一套开关。
	//
	// 但「同一份」不等于「每个模式都给」：系统工具只在教程模式挂（见 ai_tools.go
	// 的 aiGeneralCategories），普通对话只拿得到 web_fetch。
	//
	// 第三层过滤是 role（阶段 2）：子 agent 的工具面由 agentRoleSpec 收窄，
	// 并且一旦不能再派活就把 spawn_agent 撤下——让它根本不知道有这个能力。
	filter := spawnFilterFor(req.ToolFilter, sc.CanSpawn())
	tools := aiToolsPayloadFor(mode, filter)
	// 这个模式本来有没有工具可用。收尾轮靠它区分「本来就没有工具」和
	// 「有工具但轮次用完了」——后者必须给模型一句明确交代，否则它会把调用写成正文。
	modeHasTools := len(tools) > 0
	// system 的原始正文（还没贴任何提示段）。工具说明与目录说明改成每轮重建
	// 而不是一次追加：收尾轮撤下 tools 之后，system 里那段「你可以调用 file_read」
	// 也必须一起撤掉，否则模型会把这次调用输出成纯文本标记（见 ai_dsml.go）。
	baseSystem := firstSystem(messages)
	// 项目规则（AGENTS.md / .quickdock/rules*）：读一次，贯穿本次 run。
	// 压缩后重建复用的是同一份，不重新读盘——那次重建此刻正被超窗逼着做，
	// 多加一次 IO 没有收益，还可能读到用户正在编辑的半截文件。
	rules := a.loadAIRules(cwd, aiMentionPaths(req.Mentions))
	baseSystem = withRules(baseSystem, rules)
	applySystemHints(messages, baseSystem, mode, cwd, modeHasTools, false)
	// 让用户看见「这次用上了哪些规则」：改了不生效又没有任何提示，
	// 是这类功能最常见的抱怨，而用户没法自己看出 AI 到底读没读到。
	if len(rules.Files) > 0 {
		sink.Event("rules", map[string]any{"files": rules.Files})
	}

	// 流式调用模型，逐段写入 sink。
	// reasoningBuf / assistantBuf 跨轮累加：一条回复由多轮组成，但用户看到的是
	// 一整段话，分轮存反而要在前端再拼一次（而且拼错的顺序没人会发现）。
	var reasoningBuf strings.Builder
	var assistantBuf strings.Builder
	var totalUsage db.TokenUsage
	var records []aiToolRecord
	// 本次提问内已成功执行的调用，跨轮复用做去重（见 ai_tools.go 的 toolCallKey）。
	// 放在循环外、一次提问一个：新提问要重新计数，否则用户说「再发一次」会被挡。
	seenCalls := map[string]aiToolRecord{}
	var lastErr error

	var reasonCb func(text string)
	if cfg.ThinkingEnabled {
		reasonCb = func(text string) {
			reasoningBuf.WriteString(text)
			// 下游断开由 sink 自己触发 ctx 取消，这里不必再判 failed()。
			sink.Push("reasoning", text)
		}
	}
	onToken := func(text string) {
		// 首字延迟：从收到请求到第一个内容 token（含上下文组装耗时），只记第一次。
		// 多轮时后面的轮次不再覆盖——用户感知的等待是「按下回车到第一个字」。
		if firstTokenAt == 0 {
			firstTokenAt = time.Since(streamStart)
		}
		assistantBuf.WriteString(text)
		sink.Push("token", text)
	}

	// 最多 aiToolMaxRounds 轮工具调用 + 1 轮收尾。
	// 收尾那一轮不带 tools：轮次用尽还允许调工具的话，用户会看到卡片一路往外冒、
	// 就是等不到答案。
	loopBudget := aiLoopBudget(cfg)

	for turnIdx := 1; turnIdx <= aiToolMaxRounds+1; turnIdx++ {
		// 每轮按「本轮到底有没有工具」重建 system（覆盖式，见 applySystemHints）。
		// 这一句和上面撤 tools 是一对：只撤 tools 不撤提示，模型会把调用写成正文。
		applySystemHints(messages, baseSystem, mode, cwd, len(tools) > 0, modeHasTools && len(tools) == 0)

		// 每轮开头压一次上下文：工具结果是在循环中间长出来的，
		// buildAIMessages 里那次摘要压缩管不到这里。
		compactLoopMessages(messages, loopBudget)

		// 超窗兜底：我们自己估的 token 与上游真实分词器之间总有偏差，
		// 历史里也可能躺着一条巨长的消息（整份文件、整段日志）。真撞了 400
		// 就按上游报的真实窗口强制压一次、重试一次，而不是把错误丢给用户
		// 让他自己想办法——「上下文超了」对普通用户是句废话。
		var turn chatTurn
		for attempt := 0; ; attempt++ {
			turn, err = a.streamAIChat(ctx, cfg, messages, res.ConvID, tools, onToken, reasonCb)
			if err == nil {
				break
			}
			// 三个前置条件缺一不可：
			//   - 整条回复还没开始吐字：已经输出半截再重试，用户会看到同一段话两遍；
			//   - 还没重试过：压完仍超就别继续撞，避免拖长等待；
			//   - 用户没点停止。
			if attempt >= 1 || assistantBuf.Len() > 0 || reasoningBuf.Len() > 0 || ctx.Err() != nil {
				break
			}
			window, ok := contextOverflow(err, cfg.Model)
			if !ok {
				break
			}
			// 顺带压输出预留：max_tokens 是预扣而非上限，上游把
			// 「输入 + 预留输出」一起跟窗口比。只压历史不动它，仍然可能超。
			if window <= 0 {
				window = contextWindowOf(cfg)
			}
			if out := window / 8; out >= 512 && (cfg.MaxTokens == 0 || cfg.MaxTokens > out) {
				cfg.MaxTokens = out
			}
			rebudget := aiContextBudget(cfg) * 2 / 5
			logger.W("[ai] 上游报上下文超窗 conv=%s model=%s window=%d 重试预算=%d",
				res.ConvID, cfg.Model, window, rebudget)
			// 子 agent 没有历史可压（上下文就是 system + 一条 task）：重压只会重读
			// 父会话，把它变成「看得见父对话」——那正是 Fresh 要避免的。直接放弃这次重试。
			if req.Fresh {
				break
			}
			newMsgs, berr := a.buildAIMessagesWithin(ctx, cfg, res.ConvID, mode, rebudget, true)
			if berr != nil {
				break
			}
			// 新组装的上下文里 system 是干净的（没贴提示），base 要跟着换：
			// 它可能已经带上了这次新算出的摘要。
			baseSystem = firstSystem(newMsgs)
			// 规则跟着一起贴回去：buildAIMessagesWithin 组出来的 system 是干净的。
			// 漏了这一句的症状是「长对话聊到一半突然不认项目约定」，极难复现。
			baseSystem = withRules(baseSystem, rules)
			applySystemHints(newMsgs, baseSystem, mode, cwd, len(tools) > 0, modeHasTools && len(tools) == 0)
			// 引用材料同样要贴回去：重建出来的 messages 里那条 user 消息是库里的原文，
			// @ 的内容只存在于内存。漏了这步，超窗重试之后 AI 会对着「照这个改」发愣。
			applyMentionBlock(newMsgs, mentionBlock)
			messages = newMsgs
			sink.Event("notice", map[string]any{
				"message": "上下文超出模型窗口，已自动压缩历史并重试",
			})
		}
		totalUsage = addTokenUsage(totalUsage, turn.Usage)
		// 明细按轮记、问答次数只第一轮 +1：一次提问触发五轮调用时，
		// 花费是真花了五份，但用户问的只有一句。
		//
		// run/node 在第一次派活后才存在：此前记空串（只归到会话），
		// 此后按 run 归集——「这次编排花了多少」因此只统计真正参与编排的那些调用。
		a.recordAIChatTurnScoped(res.ConvID, sc.RunID(), sc.CurrentNodeID(), cfg, turn.Usage, turnIdx == 1)

		if err != nil {
			lastErr = err
			break
		}
		if len(turn.ToolCalls) == 0 {
			break // 没有工具调用 = 这一轮就是最终回答
		}
		if turnIdx > aiToolMaxRounds {
			// 兜底：收尾轮已经不带 tools，正常模型不会再返回 tool_calls。
			// 真返回了也没有下一轮可以呈现结果，直接忽略并留痕。
			logger.W("[ai] 收尾轮仍返回 %d 个工具调用，已忽略 conv=%s", len(turn.ToolCalls), res.ConvID)
			break
		}

		// 先告诉前端要调什么，再执行：一次工具可能跑几秒，
		// 这期间界面上得有「正在用 xx」，否则看起来就是卡死了。
		for _, c := range turn.ToolCalls {
			sink.Event("tool", map[string]any{
				"id": c.ID, "name": c.Name, "round": turnIdx,
				"args": logger.Trunc(c.Args, 200),
			})
		}
		toolMsgs, recs := a.runToolCalls(ctx, turn.ToolCalls, turnIdx, turn.Content, seenCalls, cwd)
		records = append(records, recs...)
		messages = append(messages, toolMsgs...)
		for _, r := range recs {
			sink.Event("tool_result", map[string]any{
				"id": r.ID, "name": r.Name, "ok": r.Error == "", "ms": r.Ms,
				"error": r.Error, "dedup": r.Dedup, "denied": r.Denied,
				"added": r.Added, "removed": r.Removed,
				// 行级 diff：卡片展开时的主体内容。只有 added/removed 时用户
				// 只知道「改了两行」而不知道改成了什么。
				"diff": r.Diff,
				// 编排类工具带回 run/node，前端可以立刻挂出 run 树，
				// 不必等整轮结束再回头查一次。
				"runId": r.RunID, "nodeId": r.NodeID,
				// 保留换行：JSON 结果压成一行就没法读了。
				"result": truncateKeepLines(r.Result, 400),
			})
		}

		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		// 轮次用尽：下一轮不带工具。不额外塞一条 user 提示——
		// tool 结果后面跟 user 消息在部分网关会被判成非法序列，
		// 而 tools 消失本身就足以让模型收尾。
		if turnIdx >= aiToolMaxRounds {
			tools = nil
		}
	}

	assistant := assistantBuf.String()
	toolCallsJSON := encodeToolRecords(records)
	rounds := countRounds(records)
	// 结果里带上用量与轮次：编排层要用它做 run 级成本归集与「这次派活值不值得」的判断。
	// 在这里一次性写完，后面几条 return 分支就都带上了。
	res.Usage = totalUsage
	res.Rounds = rounds
	res.ToolCount = len(records)

	// 仅落库助手回复：用户消息已在调模型前由 saveUserTurn 落库（幂等，重试不重复）。
	// stopped 标记随行落库：用户点停止后刷新会话，仍能看出这条是半截的。
	saveAssistant := func(asst string, stopped bool) {
		if req.Fresh {
			// 子 agent 不落库：它的输入输出由 agent_nodes.result 与 run 树承载。
			// 写进 ai_messages 会让侧栏那条对话里冒出几条「用户没问过的提问」，
			// 而且下次打开对话时它们会被当成历史喂给主 agent（上下文污染）。
			return
		}
		asst = strings.TrimSpace(asst)
		// 没有正文但有工具记录时也要落库：用户看到的是「调了 3 个工具、没有回答」，
		// 这比一条彻底消失的回复更接近事实，也解释了钱花在哪。
		if asst == "" && toolCallsJSON == "" {
			logger.W("[ai] 模型未产出内容，跳过落库助手回复 conv=%s mode=%s stopped=%v", res.ConvID, mode, stopped)
			return
		}
		if _, e := a.App.DB.SaveAssistantTurn(res.ConvID, db.AssistantTurn{
			Content:          asst,
			ReasoningContent: reasoningBuf.String(),
			Model:            cfg.Model,
			Usage:            totalUsage,
			Stopped:          stopped,
			ToolCalls:        toolCallsJSON,
			ToolRounds:       rounds,
		}); e != nil {
			logger.W("[ai] 保存助手回复失败 conv=%s: %v", res.ConvID, e)
			sink.Event("error", map[string]any{"message": e.Error()})
		}
	}

	if lastErr != nil {
		if ctx.Err() != nil {
			// 用户主动取消，保存已收到部分（用户消息已在调模型前落库）。
			// 正在跑的工具不会被这里打断——Tool.Handler 没有 ctx，
			// 只能等它自己返回（见 ai_tools.go 的说明）。
			saveAssistant(assistant, true)
			sink.Event("done", doneEvent(res.ConvID, firstTokenAt, totalUsage, cfg.Model, rounds, len(records)))
			res.Text = assistant
			res.Stopped = true
			res.Err = lastErr
			return res
		}
		// 上游报错：用户消息已落库，历史不会空。若疑似因 base64 本地图片被拒，给明确提示。
		//
		// 已经流出去的那部分正文也要落库：否则界面上明明有半截回答，刷新一次就凭空消失，
		// 而错误提示只说「请求失败」。它同样算「被打断的回复」，用 stopped 标记。
		if assistant != "" || toolCallsJSON != "" {
			saveAssistant(assistant, true)
		}
		msg := lastErr.Error()
		if hint := a.imageErrorHint(lastErr, req.Images); hint != "" {
			msg += hint
		}
		sink.Event("error", map[string]any{"message": msg})
		res.Text = assistant
		res.Err = lastErr
		return res
	}

	// 正常结束，落库助手回复（用户消息已在调模型前落库）
	saveAssistant(assistant, false)
	if !req.Fresh {
		// 只对顶层刷会话更新时间：子 agent 结束时改父会话的 updated_at 会让
		// 侧栏那条对话无缘无故跳到最前面，看起来像用户刚刚发过言。
		_ = a.App.DB.UpdateAIConversationMeta(res.ConvID, "", "")
	}
	logger.I("[ai] 流式结束 conv=%s 首字=%dms 轮次=%d 工具=%d in=%d out=%d cached=%d",
		res.ConvID, firstTokenAt.Milliseconds(), rounds, len(records), totalUsage.Prompt, totalUsage.Completion, totalUsage.Cached)
	sink.Event("done", doneEvent(res.ConvID, firstTokenAt, totalUsage, cfg.Model, rounds, len(records)))

	res.Text = assistant
	return res
}
