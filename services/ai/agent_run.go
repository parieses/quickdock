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
//   - 本文件不写 HTTP 状态码。runAgent 被调用时响应头早已以 200 发出去了，
//     之后所有失败都只能是 event: error，再想改状态码也改不了。
//   - 本文件不发桌面通知。notifyAIDone 判断的是窗口焦点与当前页面，
//     那是 UI 关注点，留给调用方。
//   - 本文件不决定 sink 的生命周期（谁创建谁关闭）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// 会话 id 在 runAgent 内部才确定（新建会话时由 DB 生成），而 SSE 的每个文本帧都要
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
	// Action 一次性动作（chat/explain/translate/summarize/review）。
	//
	// 动作只影响**这一条消息**：拼一段约束到 user 消息尾部，不改会话属性、
	// 不改工具面。空 = 普通对话（等价 chat）。
	Action string
	// ActionParams 动作参数（翻译目标语言、总结范围等）。
	ActionParams map[string]string
	Cwd          string
	Images       []AIChatImage
	// Skills 这条对话选中的技能名。nil = 本次请求没表态（沿用会话上的选择，
	// 建会话时按「没选过」落空串 → 走全局默认）；空切片 = 明确「本次不用技能」。
	// 两者必须分开：否则用户在输入框里删掉 /技能名 之后，下一条消息又把全局默认挂回来。
	Skills []string
	// Permission 这条对话的执行权限，三档："" = 默认（敏感操作逐个确认）、
	// "readonly" = 只读（不挂写类工具）、"full" = 完全访问（不再逐条确认）。
	//
	// 跟请求走而不是每轮从库里读：权限是「这条消息按什么口径执行」，
	// 用户在点发送前刚切过，读库会拿到上一轮的值——而这两者的差别正是
	// 「要不要停下来问」。空串且请求也没带时回落到会话上存的那档。
	Permission string
	// ProfileID 这一轮用哪个模型档案（ai_profiles.id）。前端每次发消息都带输入框里
	// 选中的那个；空 = 没表态，后端按「会话上存过的 → 全局激活项」回落。
	//
	// 跟请求走而不是每轮从库里读：用户可能刚在输入框里切完模型就按发送，
	// 那时库里还是上一轮的值。库里那份只负责「切走再切回来时把选择显示回去」。
	ProfileID string
	// Mentions 本次提问里用 @ 显式引用的文件 / 目录 / 历史对话（见 ai_mentions.go）。
	// 只影响这一次请求的上下文，不落库：引用是「这次要看它」，不是长期状态。
	Mentions []AIMention
	// AttachedFiles 用户主动插入的本地文件（见 ai_attached_files.go）。
	// 与 Mentions 一样只影响本次上下文、不落库。
	AttachedFiles []AIAttachedFile
	// Worktree 本次是否要求「在隔离工作区里干活」（见 ai_worktree.go）。
	//
	// 只在**还没建**工作区时起作用：一旦会话上记了 worktree_path，后续轮次无论
	// 传什么都在那个工作区里执行——否则模型上一轮改的文件下一轮就看不见了。
	Worktree bool
	// WorktreeBranch 基于哪条分支建工作区（空 = 当前分支）。
	WorktreeBranch string

	// ── 编排维度（阶段 2）。顶层对话这四项全为零值，行为与阶段 1 完全一致 ──

	// RunID / NodeID 这次执行属于哪次编排的哪个节点。
	// 顶层为空 → 第一次派活时由 agentScope 懒创建（见 agent_scope.go）。
	RunID  string
	NodeID string
	// Role 子 agent 的角色（见 agentRoleSpecs：explore / review / auditor /
	// tester / ops / writer / code），只用于系统提示里的自我说明。
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
	// ScopeCwd 子 agent 的读取范围（= 父级的范围），只有 Fresh 时传。
	//
	// 不用 Cwd：Cwd 是子 agent 干活的位置，可以是项目里的某个子目录，而
	// 「这个对话属于哪个项目」不会因为子任务缩到 src/ 就变。拿 Cwd 当范围会让
	// 子 agent 连项目的 go.mod 都读不到。
	ScopeCwd string

	// ── 定时任务专用（见 ai_schedule.go）──
	//
	// 不复用 ToolFilter：那个非 nil 会让 agentScope 把自己当子 agent 处理，
	// 连带改掉上下文组装。定时任务只是想收窄工具面，不该捎带改变编排语义。
	// ScheduledNoTools 优先级高于 ScheduledReadOnly。
	ScheduledNoTools  bool
	ScheduledReadOnly bool

	// 一次运行的费用护栏（0 = 沿用默认预算，普通 SSE 会话不传）。
	//
	// 无人值守的定时任务没有人在旁边点「停止」：默认的 120 轮工具 +
	// 20 倍上下文 token 预算是为「有人盯着的长任务」留的余量，放到到点自动跑
	// 的任务上，一次打转就能烧掉整月额度。两者都是收尾而非报错——撞线时撤掉
	// 工具让模型把已有结果说完，与现有的轮次/token 预算走同一条路径。
	MaxToolRounds    int
	TurnBudgetFactor int
}

// AgentRunResult 一次运行的结果。
// finalRunStatus 把一次运行的收口状态翻译成 agent_runs 的终态。
//
// 抽成纯函数是为了能单测：终态必须由结果本身决定，而不是散在各条 return 分支里
// 各写一遍——那正是这次漏网的根因（三条收口只发事件、没人写库）。
// Canceled 优先于 Failed：用户主动停下来的普通错误（上游返回半截后 ctx 取消）
// 不该在回放里被标成「失败」。
func finalRunStatus(res AgentRunResult) (status, errMsg string) {
	if res.Stopped {
		return db.AgentStatusCanceled, ""
	}
	if res.Err != nil {
		return db.AgentStatusFailed, res.Err.Error()
	}
	return db.AgentStatusDone, ""
}

// toolArgErrorHint 上游因 tool_calls 参数非法报 400 时，补一句只有本地才知道的线索。
//
// 上游报文里的工具名/调用 id 常被网关打码成 ***，用户盯着 "arguments must be
// valid JSON" 根本不知道是哪一步写坏了。本轮 records 里恰好留着每个调用的
// 名字和参数错误（prepareToolCall 的原文回显），拼出来即可定位，不必翻日志。
func toolArgErrorHint(err error, records []aiToolRecord) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "must be valid json") && !strings.Contains(msg, "invalid json") {
		return ""
	}
	names := make([]string, 0, len(records))
	for _, r := range records {
		if strings.Contains(r.Error, "参数解析失败") {
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n本轮有 %d 个工具调用的参数不是合法 JSON（%s）。通常是模型输出被截断；已按空参数回传，模型会看到原始参数并重试。", len(names), strings.Join(names, "、"))
}

// aiWireMaxRune 单次提问「工具对话原文」落库的字符上限。
//
// 16 万字符 ≈ 10 万 token，已覆盖绝大多数任务的工具轮总量（看板典型提示词 30K）。
// 超了就整份不存：这份数据只服务于前缀缓存，存半截反而会让重建出的历史与
// 上一发不一致——既没命中，又白占库。
const aiWireMaxRune = 160000

// aiWireTotalMaxRune 单条会话保留的 wire 总量上限（字符）。
//
// 保真重放要留**每一**次提问的 wire（见 buildAIMessages），不裁的话库体积随
// 提问次数线性涨。1.2M 字 ≈ 60 万 token 的工具对话原文，已经覆盖任何窗口
// 装得下的历史；超出时从最老的开始丢（TrimAIConversationWires），丢掉的只是
// 很早那段的重放对齐，最近几轮照常命中。
const aiWireTotalMaxRune = 1_200_000

// composeBaseSystem 把一段「干净 system」（firstSystem 的结果，含模式提示/摘要/记忆/技能）
// 补上所有**稳定**的运行期行为约束，得到完整的 baseSystem。
//
// 初始装配与两处压缩后重建必须走同一处：重建出来的 messages 里 system 是干净的，
// 只贴 rules 会丢掉 planHint（怎么拆任务）、workdirHint（临时文件往哪写）、
// scaleHint（能不能派子 agent）、extToolsOverflowHint（外部工具被截）——
// 表现是长对话一压缩，AI 就突然不懂规矩了。
// 这些约束都按「这次到底挂了什么」决定说不说，所以参数是 tools/cwd/extTrimmed 本身。
//
// 续跑/清单提示（resumeHintFor）**不在这里拼**：清单随 plan_update 每轮都可能变，
// 放在 system 里等于每轮把整段前缀缓存作废一遍。system 必须是会话内一字不变的段；
// 会变的提示挪去贴在最后的 user 消息上（见 runAgent 里 applyMentionBlock 的用法）。
func composeBaseSystem(clean string, rules aiRules, tools []map[string]any, cwd string, extTrimmed bool) string {
	out := clean
	if hasToolPayload(tools, "plan_update") {
		out += planHint
	}
	out += workdirHintFor(cwd, tools)
	out += scaleHintFor(tools)
	out = withRules(out, rules)
	if extTrimmed {
		out += extToolsOverflowHint
	}
	return out
}

type AgentRunResult struct {
	ConvID  string
	Text    string
	Stopped bool // 没跑完（用户中断或连接断开）
	// StopCause 中断的具体原因：errStopByUser / errClientGone / 其它。
	//
	// Stopped 只回答「跑完了没有」，而通知文案要回答「谁停的」：用户按的停止
	// 值得告诉他一声，页面自己断了则不是他的动作（刷个页面就弹「AI 回答已停止」
	// 会让人以为是自己误触）。
	StopCause error
	// Usage 这次执行的累计用量。编排层用它做 run 级成本归集：
	// 每次都去 ai_usage 查一遍既慢又会把「编排外的调用」混进来。
	Usage     db.TokenUsage
	Rounds    int
	ToolCount int
	// RunID 本次执行对应的运行轨迹 id（未开启轨迹或未建记录时为空）。
	// 前端靠它在 done 事件之后打开轨迹抽屉。
	RunID string
	Err   error
}

// runAgent 跑完一次多轮 agent 执行：组装上下文 → 逐轮调模型 → 执行工具 → 落库。
//
// ctx 的取消代表用户中断（点停止、前端断开）。中断不是错误：已经收到的部分正文
// 会照常落库并标 stopped，这样刷新会话还能看到半截回答，而不是凭空消失。
//
// 刻意非导出：它是内部执行核心，入口只有 HTTP 流式外壳（ai_stream.go）和子 agent
// 派活（agent_spawn.go），都在本包内。导出版本会被 wails3 codegen 当 bound 方法
// 分析，EventSink 接口参数过不了 JSON 绑定（编译期那条 WARNING 就是它）。
// jobNoticeText 把「后台任务已结束」拼成一条通知。
//
// 只给事实（谁结束了、退出码、用时），不给输出：任务输出可能几十 MB，
// 而模型多数时候并不需要——需要时再调 job_output 读一段，比每轮都背着一份日志便宜。
// 这一点与同类工具的通知格式一致：通知里带 summary + 「要输出就去看」的指引。
func jobNoticeText(evs []aitools.JobDone) string {
	var b strings.Builder
	b.WriteString("<task-notification>\n")
	for _, ev := range evs {
		cmd := ev.Command
		if utf8.RuneCountInString(cmd) > 120 {
			cmd = string([]rune(cmd)[:120]) + "…"
		}
		b.WriteString("- 后台任务 " + ev.ID + " 已结束：")
		switch ev.Status {
		case db.AIJobDone:
			b.WriteString("已完成")
		case db.AIJobFailed:
			b.WriteString("失败")
		case db.AIJobKilled:
			b.WriteString("被终止")
		default:
			b.WriteString(ev.Status)
		}
		if ev.ExitCode != 0 {
			fmt.Fprintf(&b, "（exit=%d）", ev.ExitCode)
		}
		if ev.DurationMs > 0 {
			fmt.Fprintf(&b, " 用时 %s", (time.Duration(ev.DurationMs) * time.Millisecond).Round(100*time.Millisecond))
		}
		b.WriteString("\n  命令：" + cmd + "\n")
		if ev.ErrMsg != "" {
			b.WriteString("  原因：" + ev.ErrMsg + "\n")
		}
	}
	b.WriteString("</task-notification>\n")
	b.WriteString("以上是系统事件，不是用户发言：不要向用户复述这条通知，也不要因此重开话题。" +
		"需要具体输出时用 job_output 读（只读需要的那一段）；需要继续做的事接着做。" +
		"不要 sleep 或反复查 job_status 等任务——结束了自然会通知你。")
	return b.String()
}

// flushPendingInjects 收尾前把还没接进上下文的插话落成正式的用户消息。
//
// 用在「轮次或 token 预算已到顶、这一轮必须先收尾」的路径上。原来什么都不做：
// 用户等了十几秒打的那句话既没进上下文、也没回执、也没落库——前端只能把字退回
// 输入框，用户以为发丢了。落库之后它至少是一条真实存在的提问，下一条消息里
// 会随历史一起带上。
func (a *AIService) flushPendingInjects(q *injectQueue, convID string, sink EventSink) {
	if q == nil || sink == nil {
		return
	}
	items := q.drain()
	if len(items) == 0 {
		return
	}
	for _, it := range items {
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		if _, err := a.App.DB.AddAIMessageUser(convID, text, "", ""); err != nil {
			logger.W("[ai] 断点前保存插话失败 conv=%s: %v", convID, err)
		}
		// 回执照发：前端据此把「等待插入」换成正式气泡。
		sink.Event("inject_ack", map[string]any{"id": it.ID, "text": it.Text, "stored": true})
	}
}
func (a *AIService) runAgentBuiltin(ctx context.Context, req AgentRunReq, sink EventSink) AgentRunResult {
	res := AgentRunResult{ConvID: strings.TrimSpace(req.ConvID)}

	// 这一轮起的后台任务（run_command background=true）记在 ctx 上。
	// 用户点停止时 ctx 被取消，这里把它们一并收掉：命令类工具会随 ctx 死，
	// 后台任务却是独立进程，不收就会「对话停了、CPU 还在满」。
	// 正常跑完时 ctx 还没取消，defer 里什么都不做。
	jobs := &aitools.JobCollector{}
	ctx = aitools.WithJobCollector(ctx, jobs)
	defer func() {
		if ctx.Err() == nil {
			return
		}
		if n := jobs.KillAll(); n > 0 {
			logger.I("[ai] 已停止本轮启动的 %d 个后台任务 conv=%s", n, res.ConvID)
		}
	}()

	// 首字延迟的起点。必须在函数入口取：用户感知的等待是「按下回车到第一个字」，
	// 含上下文组装（查库 + 摘要压缩）的耗时。挪到调模型之前会让这个数字凭空变小。
	streamStart := time.Now()
	var firstTokenAt time.Duration

	mode := strings.TrimSpace(req.Mode)
	message := strings.TrimSpace(req.Message)
	// 权限口径在这里归一一次：后面建会话、回写、建闸门都用这一个值，
	// 免得「库里存 full、闸门却按默认问」这种两处各算一遍的老毛病。
	permission := aiNormalizePermission(req.Permission)

	// 模型跟着这条任务走：优先本次请求，其次会话上存过的，最后才是全局激活项。
	// 全局那份仍然有用——插件、SSH 屏显、摘要压缩这些没有模型选择器的入口靠它，
	// 且用户最后一次主动选的模型会写回它（见 AISetConversationProfile）。
	cfg, okp := a.aiProfileForRun(req.ProfileID, res.ConvID)
	if !okp || (cfg.APIKey == "" && profileNeedsKey(cfg)) {
		// 用户报「AI 不回话」时，最常见原因就是没配 Key；这条必须留痕。
		logger.W("[ai] 请求被拒：未配置 API Key（conv=%s mode=%s provider=%s base=%s）", res.ConvID, mode, cfg.Provider, cfg.BaseURL)
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
				// 技能随创建落库：在落地页勾好技能再发第一句时，这条路径是唯一记录点。
				Skills: encodeConvSkills(req.Skills),
				// 权限同理：落地页就能把权限切成完全访问再发第一句。
				Permission: permission,
				// 落地页输入框里选好的模型随建会话落库：少了这一步，第一句用的
				// 是请求带来的值、第二句却会回落到全局，出现「越聊越换了模型」。
				ProfileID: cfg.ID,
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
				// 新建会话时前端列表里那条是本地占位，带回去它才知道这条任务
				// 用的是哪个档案（切走再切回来才不会跳回全局默认）。
				"profileId": conv.ProfileID,
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
	// WP17：记下这条对话的目标（一句话）。
	//
	// 只在为空时写：目标回答的是「这条对话一开始要干什么」，每轮覆盖会把它改成
	// 最后一次追加的那句要求，于是「上次的目标」变得毫无意义。
	// 子 agent（Fresh）不写：它借的是父会话 id，写进去就把父会话的目标改掉了。
	if !req.Fresh && conv != nil && strings.TrimSpace(conv.Goal) == "" {
		if goal := aiGoalOf(message); goal != "" {
			if gerr := a.App.DB.SetAIConversationGoal(conv.ID, goal); gerr != nil {
				logger.W("[ai] 记录会话目标失败 conv=%s: %v", conv.ID, gerr)
			} else {
				conv.Goal = goal
			}
		}
	}
	// 后台任务的会话归属：到这里 res.ConvID 才最终确定（新会话是上面刚建的），
	// 在函数开头按 req.ConvID 挂会拿到空串，起的任务就永远查不到属于谁。
	// 子 agent（Fresh）借父会话 id，正好一起归到父会话面板里。
	ctx = aitools.WithConvID(ctx, res.ConvID)
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

	// 三档权限取一个生效值：请求里带了就以请求为准（前端每次发消息都带输入框里
	// 当前那一档），没带（插件、定时任务、老前端）才回落到会话上存的。
	// 工具面与审批闸门必须用同一个值，否则会出现「只读档却弹了写操作确认」
	// 或者反过来「完全访问却还在问」——两处各算一遍是这类不一致的老源头。
	effPermission := permission
	if strings.TrimSpace(req.Permission) == "" {
		effPermission = aiConversationPermission(conv)
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

	// 隔离工作区：勾了 Worktree 就把 cwd 换成这个任务专属的 git worktree，
	// 原工程目录一个字节都不动。已有工作区时这里只是「续用」。
	//
	// 放在 cwd 回写之后：回写已经把「用户选的目录」记成会话 cwd（那是工作区的来源），
	// 这里再用工作区路径覆盖一次。两次写的都是真实状态，只是先后有依赖。
	if !req.Fresh && strings.TrimSpace(cwd) != "" {
		if wt := a.applyWorktree(conv, cwd, req.Worktree, req.WorktreeBranch); wt != cwd {
			cwd = wt
			if err := a.App.DB.SetAIConversationCwd(res.ConvID, cwd); err != nil {
				logger.W("[ai] 保存工作区为会话目录失败 conv=%s: %v", res.ConvID, err)
			}
		}
	}

	// 技能选择的回写：请求里带了（前端每次发消息都带输入框里的 /技能名）就以请求为准，
	// 顺手存回会话——不然「这条对话用了哪些技能」只有前端内存记得，切走就没了。
	// 没带（nil）时不动库里的值：那表示「这次没表态」，不是「清空」。
	//
	// 必须同时更新内存里的 conv.Skills：后面的提示词装配读的是 conv.Skills，
	// 只写库不回填的话，**这一轮**用的还是上一轮的技能名单——用户新选的技能
	// 要等到第二句才生效，表现就是「我明明选了技能，它却没用」。
	if !req.Fresh && req.Skills != nil {
		if enc := encodeConvSkills(req.Skills); enc != conv.Skills {
			if err := a.App.DB.SetAIConversationSkills(res.ConvID, enc); err != nil {
				logger.W("[ai] 保存会话技能选择失败 conv=%s: %v", res.ConvID, err)
			}
			conv.Skills = enc
		}
	}

	// 权限的回写：前端每次发消息都带当前开关，与库里的值不同就存回去——
	// 「切走再切回来」看到的是上次真正执行用的权限，而不是一个可能已经过期的界面状态。
	//
	// 只在请求**明确**带了权限时写：定时任务、插件、老前端都不带这个字段，
	// 跟着写会把会话上的「完全访问」悄悄清成默认档。
	if !req.Fresh && strings.TrimSpace(req.Permission) != "" && permission != strings.TrimSpace(conv.Permission) {
		if err := a.App.DB.SetAIConversationPermission(res.ConvID, permission); err != nil {
			logger.W("[ai] 保存会话权限失败 conv=%s: %v", res.ConvID, err)
		}
	}

	// 模型的回写：用 cfg.ID 而不是 req.ProfileID——请求里那个 id 可能已被删掉，
	// aiProfileForRun 会回落到别的档案，把无效 id 存进库只会让下次选择显示空白。
	//
	// 只在值真的变了时写：这个 UPDATE 会改 updated_at，每轮都写会把这条任务
	// 一直顶在侧栏最前。也刻意不在这里动全局 ai_active_profile——切到一条老
	// 任务发消息不该把「用户最后选的模型」悄悄换成它当年用的那个。
	if !req.Fresh && strings.TrimSpace(req.ProfileID) != "" && cfg.ID != strings.TrimSpace(conv.ProfileID) {
		if err := a.App.DB.SetAIConversationProfile(res.ConvID, cfg.ID); err != nil {
			logger.W("[ai] 保存会话模型失败 conv=%s: %v", res.ConvID, err)
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
		sc = newChildAgentScope(a, res.ConvID, req.RunID, req.NodeID, cwd, req.ScopeCwd, req.Depth, cfg)
	} else {
		sc = newAgentScope(a, res.ConvID, message, cwd, cfg)
	}
	ctx = withAgentScope(ctx, sc)

	// 流式出口本身（还没包 dbSink）：进度帧只走这一条。
	//
	// 进度帧（child / spawn_progress）是纯瞬时状态，落进 agent_events 只是给
	// 回放添几百行没有意义的数据。所以挂到 ctx 上的是 SSE 那一个，不是 multiSink。
	liveSink := sink

	// 主对话也落 agent_events。
	//
	// 顶层此前只有 sseSink：事件发完就没了，run 树里只有子 agent 的步骤，主 agent
	// 「为什么派活、派之前试过什么、拿到结果后怎么收尾」全都无从回看。dbSink 就是
	// 为这件事准备的（见 agent_sink.go 开头），只是此前只给了子 agent。
	//
	// 只包顶层：子 agent 的 sink 已经是 dbSink（agent_spawn.go），再包一层会把
	// 同一条事件往同一张表写两遍。run 与根节点由 ensureRun 幂等创建——子 agent 那条
	// 路径带的是父级 RunID，会直接返回，不会重复建节点。
	//
	// 失败只记一行 W：运行轨迹是旁路，它建不起来不该让这轮对话发不出去。
	if !req.Fresh {
		if runID, nodeID, rerr := sc.ensureRun(); rerr != nil {
			logger.W("[ai] 建立运行记录失败，本次不落事件表 conv=%s: %v", res.ConvID, rerr)
		} else if runID != "" {
			sink = &multiSink{sinks: []EventSink{sink, &dbSink{db: a.App.DB, runID: runID, nodeID: nodeID}}}
		}
		// runID 在这里就定下来，**不能等到主循环结束后再赋值**：
		// 用户点停止、上游报错、预算收尾都会从循环里直接 return，
		// 那些路径的 done 事件同样要带 runId——否则「点停止后抽屉空着」。
		res.RunID = sc.RunID()
		// run 终态统一在这里收口：本函数后面每一条 return（落用户消息失败、
		// 组装上下文失败、取消、上游报错、正常结束）都会经过这个 defer。
		// 此前只在建根节点失败时写过一次状态，正常路径的 run 永远停在 running，
		// 于是轨迹抽屉按 status==='running' 无限轮询、回放里每条历史都显示运行中。
		// 轨迹关闭时 runID 为空，defer 直接跳过——那条路本就不落表。
		if runID := res.RunID; runID != "" {
			defer func() {
				status, errMsg := finalRunStatus(res)
				if uerr := a.App.DB.UpdateAgentRunStatus(runID, status, errMsg); uerr != nil {
					logger.W("[ai] 回填 run 终态失败 run=%s: %v", runID, uerr)
				}
				// run 终态不等于 lead 节点终态：子 agent 各有自己的写入点，
				// 顶层节点只由这里收口，否则「已完成」的组里 lead 一直转圈。
				if uerr := a.App.DB.FinishRootNode(runID, status); uerr != nil {
					logger.W("[ai] 回填 lead 节点终态失败 run=%s: %v", runID, uerr)
				}
			}()
		}
	}

	// 事件出口挂到 ctx：工具在执行期要能往这条流里说话（见 eventSinkFrom）。
	// 目前唯一的使用者是 spawn_agent——它把子 agent 的进度压成 child 帧推回来，
	// 否则主对话里那张「派活」卡片只能显示「调用中」，用户以为卡死。
	ctx = withEventSink(ctx, liveSink)

	// 审批闸门（见 ai_approval.go）。同样挂 ctx、同样只挂顶层执行：
	// 子 agent 用 dbSink 跑在后台，没有「人」在等它，审批事件发出去只会等到超时
	// 然后自动拒绝，把 code 角色整个废掉。子 agent 的约束是工具面
	// （agentRoleSpec 的 maxLevel），而派活本身（spawn_agent，LevelWrite）
	// 已经在父级过了这道闸——「批准派一个 code 角色」就是「批准它能改文件」。
	//
	// 运行中插入（见 ai_inject.go）挂在同一处：子 agent 背后没有「用户」在看着它，
	// 也就没人能往里插话。
	var injects *injectQueue
	if !req.Fresh {
		gate := newApprovalGate(sink, res.ConvID, a.App.DB, a.loadApprovalRules(), effPermission == aiPermissionFull, a.loadApprovalGrants(), cwd)
		a.registerGate(res.ConvID, gate)
		defer a.unregisterGate(res.ConvID, gate)
		ctx = withApprovalGate(ctx, gate)

		injects = a.registerInjectQueue(res.ConvID)
		defer a.unregisterInjectQueue(res.ConvID, injects)
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
		// 这一轮真正挂上的技能名，跟着用户消息落库，聊天里据此标出「用了哪个技能」。
		// 取的就是提示词那份 conv.Skills（上面刚回填过），两边不会对不上。
		if err := a.saveUserTurnAttached(res.ConvID, message, imagesJSON, convSkillNamesJSON(conv.Skills), attachedFilesHistoryJSON(req.AttachedFiles)); err != nil {
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
		messages = a.subAgentMessages(cfg, mode, message)
	} else {
		var berr error
		messages, berr = a.buildAIMessages(ctx, cfg, res.ConvID, mode, sink)
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
	}
	// 插入文件与 @ 引用都是「这次的材料」，拼成一块贴到最后一条 user 消息。
	if !req.Fresh && len(req.AttachedFiles) > 0 {
		mentionBlock += buildAttachedFilesBlock(req.AttachedFiles, sink)
	}
	// 续跑/清单提示也并进这一块，不进 system：
	// 清单随 plan_update 每轮都可能变，system 里放它等于每轮全量作废前缀缓存。
	// 位置在历史尾部，更新时只作废它自己这一小段。与 @ 引用同一张贴法。
	// 关键是压缩重建（请求前压缩 / 超窗重试）复用的就是 mentionBlock 这一块：
	// 若在这里单独 apply 一次，重建后会被漏掉——模型会突然看不到「还有清单没做完」。
	if rh := resumeHintFor(conv, message); rh != "" {
		mentionBlock += rh
	}
	if mentionBlock != "" {
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

	// 工具集：能力定义与外部 MCP 客户端同一份，但**暴露策略是两份**。
	// 这里用的是内置 AI 自己的开关（settings 里的 ai_tool_policy）——
	// 用户在 MCP 面板关掉某个工具，不会影响 AI；反过来也一样（见 ai_tool_scope.go）。
	//
	// 过滤链从上到下共四层，缺哪一层都会说出「你有这个能力」却不给：
	//   1. 策略（等级上限 + 单工具/域开关）；
	//   2. 场景（scene）：只读档只挂 LevelRead，写类工具连名字都不出现；
	//   3. 角色（agentRoleSpec）：子 agent 的工具面按角色收窄；
	//   4. 编排（spawnFilterFor）：不能再派活时把 spawn_agent 撤下——让它根本不知道有这个能力。
	filter := spawnFilterFor(req.ToolFilter, sc.CanSpawn())
	// 定时任务的只读档：MaxLevel=0 即只留 LevelRead 工具。改的是 spawnFilterFor
	// 返回的副本，不会污染编排传进来的 base。
	if req.ScheduledReadOnly {
		if filter == nil {
			filter = &ToolFilter{MaxLevel: 0, NoSpawn: true}
		} else {
			filter.MaxLevel = 0
			filter.NoSpawn = true
		}
	}
	// 第 5 层：技能声明（WP10）。只在这条对话**明确只选了一个技能**、
	// 且那个技能写了 allowed-tools 时生效（见 skillAllowNames 的取舍）。
	//
	// 子 agent（Fresh）压根不建也不读会话行，conv 必为 nil：它的工具面由角色
	// 限定，不吃父会话的技能声明。这里必须先判空——直接取 conv.Skills 会让
	// 每次 spawn_agent 在子 agent 启动到这里时空指针崩溃。
	//
	// 用 MaxLevel=-1（跳过等级判定）而不是 0：零值等于只读，会把技能声明的
	// 写类工具一起撤掉——那是这个字段最容易踩的坑（见 ToolFilter.MaxLevel）。
	if conv != nil {
		if allow, skillName := skillAllowNames(conv.Skills); allow != nil {
			if filter == nil {
				filter = &ToolFilter{MaxLevel: -1, AllowNames: allow}
			} else {
				filter.AllowNames = allow
			}
			// 明说一句：模型看不到的工具会表现为「它说它没有这个能力」，
			// 而用户刚才明明看见技能里写着能改文件——没有这条提示就无从解释。
			sink.Event("notice", map[string]any{
				"message": fmt.Sprintf("已按技能「%s」的 allowed-tools 收窄可用工具（%d 个）。", skillName, len(allow)),
			})
			logger.I("[ai] 技能 %s 收窄工具面: %d 个", skillName, len(allow))
		}
	}
	// 装配一次，后续执行复用同一个 Surface（见 withToolSurface）：
	// 模型看到的工具面与「能调什么」必须是同一份对象。
	sf := a.aiSurface(mode, effPermission, aiConversationLegacyScene(conv), filter)
	tools, extTrimmed := aiToolsPayload(sf)
	// 纯问答档：工具一个都不挂，模型看不到自然不会调，审批闸门也就无从触发。
	if req.ScheduledNoTools {
		tools = nil
	}
	// 提示词按「这次真的挂上了哪些域」生成，不再有三份手写常量跟着代码漂。
	toolHint := buildToolHint(sf)
	ctx = withToolSurface(ctx, sf)
	// 这个模式本来有没有工具可用。收尾轮靠它区分「本来就没有工具」和
	// 「有工具但轮次用完了」——后者必须给模型一句明确交代，否则它会把调用写成正文。
	modeHasTools := len(tools) > 0
	// system 的原始正文（还没贴任何提示段）。工具说明与目录说明改成每轮重建
	// 而不是一次追加：纯问答档（ScheduledNoTools）从头就没有工具，system 里
	// 那段「你可以调用 file_read」必须不存在，否则模型会把这次调用输出成
	// 纯文本标记（见 ai_dsml.go）。
	// 项目规则（AGENTS.md / .quickdock/rules*）：读一次，贯穿本次 run。
	// 压缩后重建复用的是同一份，不重新读盘——那次重建此刻正被超窗逼着做，
	// 多加一次 IO 没有收益，还可能读到用户正在编辑的半截文件。
	rules := a.loadAIRules(cwd, aiMentionPaths(req.Mentions))
	baseSystem := composeBaseSystem(firstSystem(messages), rules, tools, cwd, extTrimmed)
	// 子 agent 的角色提示拼在所有稳定段之后（见 subAgentMessages 的说明）：
	// 同一次编排里各角色共享 [档案+模式+workdir+scale+rules] 前缀，差异只在尾部。
	if req.Fresh && strings.TrimSpace(req.Role) != "" {
		baseSystem += subAgentHint(req.Role)
	}
	applySystemHints(messages, baseSystem, toolHint, cwd, modeHasTools, false)
	// 让用户看见「这次用上了哪些规则」：改了不生效又没有任何提示，
	// 是这类功能最常见的抱怨，而用户没法自己看出 AI 到底读没读到。
	if len(rules.Files) > 0 {
		sink.Event("rules", map[string]any{"files": rules.Files})
	}

	// ── 请求前实测：这一发到底有多大 ──
	//
	// buildAIMessages 估的是「库里历史有多长」，但真正发出去的还有三样它看不见：
	// 系统提示（记忆 + 技能 + 规则）、工具定义（几十个 MCP 工具的 JSON schema
	// 本身就有一两万 token）、以及 @ 引用展开的正文（最多几百行）。
	// 少算这三样，估算与上游实测能差出一倍——「估算说还有余量、请求却 400」
	// 基本都是这么来的。
	//
	// 所以这里在一切都挂好之后按**真实载荷**量一次：超了就在发出去之前压，
	// 而不是等 400 回来再重试（那时用户已经白等了一整个上游超时）。
	//
	// 子 agent 跳过：它的上下文只有角色提示 + 一条 task（见 subAgentMessages），
	// 没有历史可压，压了反而会去读父会话——正是 Fresh 要避免的事。
	if !req.Fresh {
		// 留 15% 余量（默认 85，可按档案覆盖，见 WP4c）：我们按「字数 ÷ 1.6」
		// 估 token，中文与 JSON 的真实分词普遍更贵，贴着天花板发等于赌分词器的心情。
		preflightMargin := preflightMarginOf(cfg)
		// WP4c 预警档：一次 run 只提示一次。
		usageWarned := false
		prevUsed := 0
		for pass := 0; pass < 2; pass++ {
			lim := contextLimitsOf(cfg)
			lim.Measured = lastRequestSize(res.ConvID)
			used := aiPayloadTokens(messages, tools)
			// 上一发的实测比这一发的估算还大时以实测算：估算偏低是常态，
			// 而 usage.prompt_tokens 是厂商自己数的（见 rememberRequestSize）。
			if lim.Measured > used {
				used = lim.Measured
			}
			ceiling := lim.Ceiling * preflightMargin / 100
			// 70% 预警：只提示、不压缩。
			//
			// 为什么要这条：用户第一次遇到「AI 忽然不记得前文」时，如果没有
			// 任何前兆，第一反应是「这软件有 bug」。而压缩本身是对的，
			// 缺的只是那句「马上要压了」。
			//
			// 阈值取 70% 而不是贴着 85%：留出十几个百分点的缓冲，
			// 用户看到提示时还有余量，不至于「提示即压缩」变成噪音。
			if !usageWarned && ceiling > 0 && used*100 >= ceiling*70 {
				usageWarned = true
				pct := used * 100 / ceiling
				if pct > 99 {
					pct = 99
				}
				sink.Event("notice", map[string]any{
					"level":   "warn",
					"message": compactWarningText(pct, preflightMargin),
				})
			}
			// 上一轮压完体积几乎没降：说明大头不在历史里（超大的工具定义、
			// 超长 @ 引用，或者窗口本身就小于固定开销）。再压一次只是白付
			// 一次摘要调用的钱和几秒等待，直接让它发出去。
			if pass > 0 && used >= prevUsed*95/100 {
				logger.I("[ai] 请求前压缩无明显效果，停止 conv=%s used=%d prev=%d", res.ConvID, used, prevUsed)
				break
			}
			// 已经压到下限就没得压了：再压等于把对话清空，而请求未必会失败
			// （估算本身有误差）。让它发出去，真超了还有下面 400 那条兜底。
			if used <= ceiling || used <= lim.Floor {
				break
			}
			prevUsed = used
			logger.I("[ai] 请求前实测超限 conv=%s used=%d ceiling=%d window=%d pass=%d",
				res.ConvID, used, ceiling, lim.Window, pass+1)
			// 目标是上限的一半而不是刚好压到线上：只压掉刚超出的那一点，
			// 下一轮工具结果一长就又超，等于每轮多付一次摘要的钱。
			newMsgs, berr := a.buildAIMessagesWithin(ctx, cfg, res.ConvID, mode, ceiling/2, true, sink)
			if berr != nil || len(newMsgs) == 0 {
				logger.W("[ai] 请求前压缩无果 conv=%s: %v", res.ConvID, berr)
				break
			}
			// system / 规则 / 行为约束 / @ 引用 / 续跑清单要重新贴回去：压缩换掉的是整个
			// 消息列表，这些都只存在于内存里（与下面超窗兜底走同一个 helper）。
			// 续跑提示已并进 mentionBlock，下面这次 applyMentionBlock 会一并贴回。
			baseSystem = composeBaseSystem(firstSystem(newMsgs), rules, tools, cwd, extTrimmed)
			applySystemHints(newMsgs, baseSystem, toolHint, cwd, len(tools) > 0, modeHasTools && len(tools) == 0)
			applyMentionBlock(newMsgs, mentionBlock)
			messages = newMsgs
			// 重建出来的是纯净消息（无轮次标记），必须按结构重新推导：
			// 否则下一轮打标会把 system + 全部历史 + 本轮用户消息一起打成当前轮，
			// 循环内折叠在本次运行剩余轮次里彻底失效（P0 修复）。
			retagMessagesByStructure(messages)
		}
	}

	// 流式调用模型，逐段写入 sink。
	// reasoningBuf / assistantBuf 跨轮累加：一条回复由多轮组成，但用户看到的是
	// 一整段话，分轮存反而要在前端再拼一次（而且拼错的顺序没人会发现）。
	var reasoningBuf strings.Builder
	var assistantBuf strings.Builder
	// WP20：推理分段统计。
	//
	// reasoningBuf 是跨轮累加的（一条回复由多轮组成），于是 N 轮的思考会被
	// 拼成一整段，用户看到的像「只思考了一次」。这两个计数加上下面的分隔符，
	// 把「确实只思考了一轮」与「被合并成一段」两种成因分开——
	// 前者不是 bug（很多模型只在首轮给 reasoning），后者是。
	reasonRounds := 0
	reasonChars := 0
	roundHasReasoning := false
	// 按轮拆开的思考：roundReason 是已经结束的轮，roundReasonBuf 是当前轮累积。
	// 两者合起来交给 SaveAssistantTurn 的 ReasoningRounds 字段。
	var roundReason []aiReasoningRound
	var roundReasonBuf strings.Builder
	// curTurn 当前轮次：saveSegment 是闭包，读不到循环变量 turnIdx。
	curTurn := 1
	// finalReasoningRounds 最后一次落库的分轮思考 JSON。done 事件带上它，
	// 前端才能把「刚跑完的这一条」画成和刷新后一样的按轮样子。
	finalReasoningRounds := ""
	var totalUsage db.TokenUsage
	var records []aiToolRecord
	// 本次提问内已成功执行的调用，跨轮复用做去重（见 ai_tools.go 的 toolCallKey）。
	// 放在循环外、一次提问一个：新提问要重新计数，否则用户说「再发一次」会被挡。
	seenCalls := map[string]aiToolRecord{}
	var lastErr error
	// noToolsRetry 本轮「没有工具却返回工具调用」已经补过一次纠正提示。
	// 只补一次：模型要是坚持调工具，再重试只会把同样的结果再买一遍。
	noToolsRetry := false
	// 空响应防御：部分思考模型 / 中转网关会把输出预算全花在 reasoning 上，
	// 正文一个字不吐就 [DONE]。这种轮次若被当成「正常最终回答」，会跳过落库
	// （saveSegment 见 content 和工具都空就 return），用户看到的就是「任务什么
	// 都没做就停了」。连续空回复最多容忍一次提示重试，再空就明确报错。
	emptyReplyStreak := 0
	const maxEmptyReplyStreak = 2

	// —— 分段落库 ——
	//
	// 一次提问通常只有一段回答。只有用户中途插话（见 ai_inject.go）时才会有第二段：
	// 那时模型看到的是新的一句用户消息，它接着说的话是对那句话的回答，和前面那段
	// 不是同一条回复。不切段的话，刷新后这条用户消息会排到整篇回答之前——
	// 「先问后答」变成「先答后问」，而用户明明看着它是插在回答中间的。
	//
	// segStart 是「这一段的工具记录从 records 的第几条开始」，segUsage 同理只算这一段。
	var segStart int
	var segUsage db.TokenUsage
	// lastSavedMsgID 最后一段落库的助手消息 id。
	//
	// 记忆整理的用量要并进「这条回复」：输入框下方的 token 用量是前端按消息行
	// 求和的，不落到某条消息上，那笔开销就永远不在用户看到的数字里。
	// 一次提问被插话切成多段时挂最后一段——记忆整理针对的是整次提问。
	var lastSavedMsgID string
	// fullText 是整次提问的完整正文（含被切开的那几段）：通知、记忆、res.Text 要全文，
	// 而 assistantBuf 每到一段结束就会被清空。
	var fullText strings.Builder

	// wireTail 本次提问里真正 append 到 messages 的工具对话
	// （assistant.tool_calls + 对应的 tool 结果），发出去的原样。
	//
	// 为什么单独留一份：下次提问时 buildAIMessages 是从库里重建历史的，而 DB 里的
	// assistant 只有「合并后的正文 + 界面用的工具摘要」。用那份重建，历史里的工具轮
	// 全部消失，请求前缀从第一个工具轮就开始与上一发不同 —— 上游的缓存全废。
	// 实测（看板 1198 请求）命中率 76%，而每次提问的首发几乎 0 命中。
	// 原样留一份就能在重建时把它接回去，前缀一路对齐到最后一个 tool 消息。
	//
	// **每一段提问都要留**（不再只留最后一次）：中间插话会把一次提问切成多段，
	// 每段各有自己的工具对话；只留最后一段，倒数第二次追问起前缀就分叉了。
	// segWireStart 是「当前段从 wireTail 的第几条开始」，saveSegment 落库时把
	// 属于这一段的 wire 原文挂到那一条回复上。
	var wireTail []map[string]interface{}
	segWireStart := 0

	// persistWireTo 把一段工具对话原文落到指定回复上（超上限整段跳过）。
	persistWireTo := func(messageID string, seg []map[string]interface{}) {
		if req.Fresh || messageID == "" || len(seg) == 0 {
			return
		}
		raw, err := json.Marshal(stripInternalMessageKeys(seg))
		if err != nil {
			logger.W("[ai] 工具对话原文序列化失败 conv=%s: %v", res.ConvID, err)
			return
		}
		if n := len([]rune(string(raw))); n > aiWireMaxRune {
			// 退化成旧行为：宁可丢这次命中率，也不让库被一次长任务撑爆。
			logger.I("[ai] 工具对话原文 %d 字超上限（%d），跳过落库 conv=%s", n, aiWireMaxRune, res.ConvID)
			return
		}
		if err := a.App.DB.SetAIMessageWire(messageID, string(raw)); err != nil {
			logger.W("[ai] 写入工具对话原文失败 conv=%s: %v", res.ConvID, err)
		}
	}

	saveSegment := func(content string, stopped bool) {
		if req.Fresh {
			return
		}
		content = strings.TrimSpace(content)
		recs := records[segStart:]
		toolJSON := encodeToolRecords(recs)
		// 没有正文但有工具记录时也要落库：用户看到的是「调了 3 个工具、没有回答」，
		// 这比一条彻底消失的回复更接近事实，也解释了钱花在哪。
		if content == "" && toolJSON == "" {
			logger.W("[ai] 模型未产出内容，跳过落库助手回复 conv=%s mode=%s stopped=%v", res.ConvID, mode, stopped)
			return
		}
		// 这一段结束：还没收口的当前轮思考先记下来。
		//
		// 轮次用 curTurn（绝对轮次）而不是 countRounds(recs)：后者是「本段内的工具轮数」，
		// 插话切段后会从 1 重新数，与轮次边界记下的绝对轮次撞号——前端按 round 关联时
		// 会把作答轮的思考挂到前面某个工具轮上，或干脆丢掉。
		if roundReasonBuf.Len() > 0 {
			roundReason = appendReasoningRound(roundReason, curTurn, roundReasonBuf.String())
			roundReasonBuf.Reset()
		}
		segReasoningRounds := encodeReasoningRounds(roundReason)
		finalReasoningRounds = segReasoningRounds
		if msg, e := a.App.DB.SaveAssistantTurn(res.ConvID, db.AssistantTurn{
			Content:          content,
			ReasoningContent: reasoningBuf.String(),
			ReasoningRounds:  segReasoningRounds,
			Action:           aiActionNameFor(req.Action, req.Mode),
			Model:            cfg.Model,
			Usage:            segUsage,
			Stopped:          stopped,
			ToolCalls:        toolJSON,
			ToolRounds:       countRounds(recs),
		}); e != nil {
			logger.W("[ai] 保存助手回复失败 conv=%s: %v", res.ConvID, e)
			sink.Event("error", map[string]any{"message": e.Error()})
		} else if msg != nil {
			lastSavedMsgID = msg.ID
			// 这一段的工具对话原文挂到这一段的回复上（见 wireTail 的说明）。
			// 插话切段后每段各有自己的 wire，重建时才能按真实顺序逐段接回。
			persistWireTo(msg.ID, wireTail[segWireStart:])
			segWireStart = len(wireTail)
		}
		segStart = len(records)
		segUsage = db.TokenUsage{}
		// 分轮思考跟着这一段一起交出去，下一段重新计。
		roundReason = roundReason[:0]
	}

	// persistWire 把收尾时还挂着的最后一段 wire 落库，并按总量预算裁剪全会的 wire。
	//
	// 三条收尾路径（正常结束 / 用户停止 / 上游报错）都要调：用户点停止之后接着
	// 再问一句是常态，那一发同样重建历史——不落就等于这次的工具轮白跑一遍。
	// 裁剪取代旧的「只留一条清其余」：保真重放要每一段都留，只控总量。
	persistWire := func() {
		if req.Fresh {
			return
		}
		persistWireTo(lastSavedMsgID, wireTail[segWireStart:])
		if err := a.App.DB.TrimAIConversationWires(res.ConvID, lastSavedMsgID, aiWireTotalMaxRune); err != nil {
			logger.W("[ai] 裁剪会话 wire 超量失败 conv=%s: %v", res.ConvID, err)
		}
	}

	var reasonCb func(text string)
	if cfg.ThinkingEnabled {
		reasonCb = func(text string) {
			reasoningBuf.WriteString(text)
			roundReasonBuf.WriteString(text)
			reasonChars += len([]rune(text))
			if !roundHasReasoning {
				roundHasReasoning = true
				reasonRounds++
			}
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

	// 轮次按「一批」算，不是整次提问的硬天花板（见下面 madeProgress 处）：
	// 一批走完，看这一批到底有没有往前推，有就再来一批。
	// 收尾那一轮不带 tools：轮次用尽还允许调工具的话，用户会看到卡片一路往外冒、
	// 就是等不到答案。
	loopBudget := aiLoopBudget(cfg)
	// WP5：轮次预算按入口分档（人工 12/120、定时 8/48、子 agent 8/48、只读 8/40）。
	// 只收不放：任何一档都不超过原来的 120 轮。
	rb := aiRoundBudgetFor(mode, req.ScheduledNoTools || req.ScheduledReadOnly, req.ScheduledReadOnly, sc.depth)
	// 工具总轮次预算：无人值守入口还可以再传更紧的值。
	// 只收窄不放大，脏参数不会把预算撑得比默认还高；limit 是「这一批」的上界，
	// 初值同样不能越过总预算，否则下面所有 limit > maxRounds 的收敛都白写。
	maxRounds := rb.Total
	if req.MaxToolRounds > 0 && req.MaxToolRounds < maxRounds {
		maxRounds = req.MaxToolRounds
	}
	// batchRounds 「一批」多少轮：下面所有「再给一批」的地方都用它。
	batchRounds := rb.Batch
	limit := batchRounds
	if limit > maxRounds {
		limit = maxRounds
	}
	// 本次提问的 token 预算（见 aiChatTurnBudget）。撞线不是报错，而是「收尾并说明」：
	// 走的是与轮次用尽完全相同的那条路（撤工具 + 一句 notice + 再跑一轮），
	// 不新增错误语义，用户说一句「接着做」就是新的一轮、预算重新计。
	chatBudget := aiChatTurnBudget(cfg)
	if req.TurnBudgetFactor > 0 {
		if b := aiContextBudget(cfg); b > 0 {
			chatBudget = b * req.TurnBudgetFactor
		}
	}
	// 子 agent 的单节点预算：不跟着上下文窗口放大。
	//
	// 1M 窗口算出来的单轮预算是 19.3 万，套到「只查三个文件改了什么」的子任务上，
	// 等于允许它把整个仓库读一遍（实测单人 122 万 prompt token）。这里给一个
	// 与窗口无关的上限，到线走收尾而不是硬断（见下面的预算分支）。
	if req.Fresh && (chatBudget == 0 || chatBudget > agentNodeTokenCap) {
		chatBudget = agentNodeTokenCap
	}
	// chatBudgetHard 自动续期的硬顶：3 倍初始预算。
	//
	// 为什么要续：预算本来是为了挡住「看着一直在动、其实在原地空转」，
	// 但它也在正常的大任务上误伤——写长文档、跑多轮测试，很快就撞线，
	// 表现就是「活儿干到一半自己断了」。判据换成「这一批有没有新动作」之后，
	// 空转照样会被挡住（madeProgress 会 false），有进展就给。
	chatBudgetHard := chatBudget * 3
	// turnBudgetStart 记下初始预算：续期的「半步」必须相对它算。
	//
	// 原来续期写死 `aiChatTurnBudget(cfg)/2`（= 10×history），而定时任务把初始
	// 预算收紧到 8×history——第一次续期就直接顶到 24×（硬顶），「半步续期」
	// 在收紧路径上完全失效，无人值守任务的花钱上限实际是配置值的 3 倍。
	turnBudgetStart := chatBudget
	budgetMark := 0
	budgetClosed := false

	// toolsClosed 这次 run 的工具「已经收回」，但请求里**仍然挂着**。
	//
	// 为什么不再把 tools 置 nil：撤下 tools 会让这一发请求的 tools 数组和 system
	// 文案都跟上一发不同，上游的前缀缓存整段作废——而收尾轮往往正是 prompt 最大
	// 的一发（前面十几轮攒下的缓存白省）。现在工具还在工具面上，只是**不执行**：
	// 模型若真发起调用，由循环里的收尾闸门回一条「本轮不执行」并让它改写作答，
	// 这样前缀始终一致，收尾那一发仍然几乎全是缓存命中。
	toolsClosed := false
	// toolsClosedHints 收尾提示已经给过几次。
	//
	// 给两次而不是一次：工具**还挂在工具面上**（这是保住缓存的前提），system 里
	// 也仍然写着「你可以调用这些工具」，模型偶尔会坚持再调一次。两次之内它基本
	// 都会改写作答；第三次还调就直接结束，避免无限循环。
	// 每次拒绝只多花一轮、且那一轮几乎全是缓存命中，代价很小。
	toolsClosedHints := 0
	// emptyNudgeOnClosed 收尾后「一句正文都没有」时的补救是否已经用过。
	//
	// 只补一次：模型连续几次都只发工具调用、正文全空，再逼也不会突然开口，
	// 而每多逼一轮都是一份全额输入的费用。
	emptyNudgeOnClosed := false

	// loopCompactShown 本次 run 是否已经出过「循环内折叠」的卡片（见循环里）。
	loopCompactShown := false
	// deadlineWarned 硬超时前的收尾提醒是否已发（只发一次，否则每轮都插一句）。
	deadlineWarned := false
	// 日志同样只记一次：折叠是长任务里每轮都会发生的常态，原实现每轮
	// 都打一条 I 日志，用户看到的就是「一直在提示超预算」，误以为是故障。
	loopCompactLogged := false

	// WP4a：进循环前先给已有消息（系统提示 + 历史 + 本轮用户消息）打上第 0 轮。
	// 之后每轮在 append 完 assistant + tool 批次时再打一次（见循环内）。
	// 一次性动作：把约束拼到**这一条 user 消息**的尾部。
	//
	// 兼容旧数据：老会话的 mode 本身就是 explain/translate/summarize，
	// 那些行没有 action 字段，按 mode 回填即可——不然升级后老会话会突然
	// 变成普通对话，用户会觉得「翻译功能没了」。
	action := aiActionByName(aiActionNameFor(req.Action, req.Mode))
	if instr := aiActionInstruction(action, req.ActionParams); instr != "" {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i]["role"] != "user" {
				continue
			}
			// 带图片的 user 消息 content 是块数组：那种情况不拼（拼错会把数组
			// 变成字符串，整条消息直接失效）。
			if s, ok := messages[i]["content"].(string); ok {
				messages[i]["content"] = s + instr
			}
			break
		}
	}

	tagMessagesRound(messages, 0)

	// 本次提问的网关会话标识：可读名（会话首问前几个字）+ 会话 id。
	//
	// 名字按**会话**取而不是按这次提问：看板上同一条会话的多次提问该显示成
	// 同一个名字，否则列表里像好几个人在问。整个 run 内值不变，网关才能把
	// 多轮工具调用的请求归成一条链（见 ai_session_tag.go）。
	ctx = withSessionInfo(ctx, sessionInfo{
		Name: a.sessionNameFor(res.ConvID, message),
		ID:   res.ConvID,
		Cwd:  cwd,
	})

	// WP15 尾巴：把「正在重试」送到界面上。
	//
	// 挂在 ctx 上而不是加参数：只有对话这一条路径有 sink，其余调用方
	// （问答、测试）不需要这条提示，也不该被迫多传一个 nil。
	ctx = withRetryNotice(ctx, func(attempt int, wait time.Duration, status int) {
		reason := "网络错误"
		if status > 0 {
			reason = fmt.Sprintf("HTTP %d", status)
		}
		sink.Event("notice", map[string]any{
			"level": "warn",
			"message": fmt.Sprintf("上游暂时不可用（%s），%.0f 秒后重试（第 %d/%d 次）。这是可恢复的，不用点停止。",
				reason, wait.Seconds(), attempt, aiRetryMax),
		})
	})

	for turnIdx := 1; turnIdx <= limit+1; turnIdx++ {
		curTurn = turnIdx
		// 预算兜底放在这一轮的最前面：撞线后要先把「闸门」关上（toolsClosed），
		// 否则这一轮模型真去调工具，钱就又花出去了。
		//
		// 与轮次预算同一条思路：**有进展就再给一段**，而不是到了就收尾。
		// 之前一撞预算就收尾，用户看到的就是「活儿干到一半自己断了」，
		// 然后被迫再打一句「继续」——那句话还会被当成新的用户诉求记进记忆。
		// 硬顶（3 倍）兜住病态空转：真在原地打转的话 madeProgress 很快就是 false。
		if !budgetClosed && chatBudget > 0 && (tokensOf(totalUsage) >= chatBudget || (req.Fresh && sc.overBudget())) {
			// 只有顶层会自动续期：主对话那头有用户，撞线了说一声「接着做」就是新的一轮。
			// 子 agent 背后没有人，续期等于把上限变成一句空话——这次实测 run 级预算
			// 72 万被冲到 123 万，中间没有任何一关卡住它。
			if !req.Fresh && madeProgress(records[budgetMark:]) && chatBudget < chatBudgetHard {
				// 半步 = 初始预算的一半（不是写死的 20×history，见 turnBudgetStart）。
				chatBudget += turnBudgetStart / 2
				if chatBudget > chatBudgetHard {
					chatBudget = chatBudgetHard
				}
				budgetMark = len(records)
				logger.I("[ai] 主循环超出 token 预算但有进展，自动续到 %d conv=%s 已用=%d",
					chatBudget, res.ConvID, tokensOf(totalUsage))
			} else {
				budgetClosed = true
				// limit 收到本轮：循环只再给一次「作答」机会；即便模型仍在调工具，
				// 也会被收尾闸门拦下，不会真花一次工具的钱。
				limit = turnIdx - 1
				// 本来就没工具（定时任务的问答档）：没有「收回」可言，也不该插
				// 一段「工具已收回」的提示——那只会让模型去找不存在的工具。
				hasTools := len(tools) > 0
				if hasTools {
					toolsClosed = true
				}
				msg := fmt.Sprintf("本次提问已消耗约 %d token，达到单次预算，先收尾；没做完说一声「接着做」就行", tokensOf(totalUsage))
				if req.Fresh {
					// 子 agent 的收尾话术不能提用户：它看不到对话，也没人能回它一句。
					msg = fmt.Sprintf("这个子任务已消耗约 %d token，达到上限：请立即用已经查到的信息给出结论，"+
						"不要再检索；没查完的部分在结论里写明「未覆盖」。", tokensOf(totalUsage))
					// 光撤 tools 还不够。实测（2026-11-19）四个 review 子任务各跑 8 轮、
					// 烧掉 27 万 prompt token，最后正文只剩一句「我先查看…再逐个审阅」——
					// 思考模型把分析全写在 reasoning 里，正文永远是过渡语，tools 一消失
					// 它就顺着过渡语收尾了。这里必须像硬超时那样明说「写结论」。
					if hasTools {
						messages = append(messages, map[string]interface{}{
							"role": "user",
							"content": "（系统提示）本子任务的额度已用完，工具已收回。现在请**在正文里**写出结论：" +
								"你查到了什么、结论是什么、哪些部分没有覆盖到。不要只在思考里分析，" +
								"发起方看不到你的思考过程。",
						})
					}
				} else if hasTools {
					// 主对话以前只弹通知、不告诉模型「工具没了」——因为 system 会被重建成
					// 「无工具」版。现在 system 保持原样（为了缓存），必须用尾部消息说明，
					// 否则模型会继续调工具、被闸门拦下，白多花一轮。
					messages = append(messages, map[string]interface{}{
						"role": "user",
						"content": "（系统提示）本次提问已达到 token 预算，工具已收回（再调用不会被执行）。" +
							"请直接用文字给出结论：做了什么、结论是什么、没做完的部分说明一声。",
					})
				}
				sink.Event("notice", map[string]any{"message": msg})
				logger.I("[ai] 主循环达到 token 预算 conv=%s 已用=%d 预算=%d 轮次=%d depth=%d",
					res.ConvID, tokensOf(totalUsage), chatBudget, turnIdx, sc.Depth())
			}
		}
		// 硬超时前的收尾提醒。
		//
		// spawn_agent 的 20 分钟是硬杀（context deadline）：到点取消，子 agent 连
		// 一句话都说不出来，父 agent 只能拿到「执行失败」再重派一遍。提前 2 分钟
		// 撤掉工具并要求它收尾，至少能留下一份带 partial 标记的结论。
		if !deadlineWarned && len(tools) > 0 {
			if dl, ok := ctx.Deadline(); ok {
				if remain := time.Until(dl); remain > 0 && remain <= agentWrapUpLead {
					deadlineWarned = true
					toolsClosed = true
					messages = append(messages, map[string]interface{}{
						"role": "user",
						"content": "（系统提示）本子任务剩余的运行时间不足 " + fmtRemain(remain) +
							"，工具已收回。请立即停止检索，用**已经查到的信息**输出结论，" +
							"并注明哪些部分没有覆盖到。",
					})
					sink.Event("notice", map[string]any{
						"message": fmt.Sprintf("子任务剩余时间不足 %s，已要求它收尾", fmtRemain(remain)),
					})
					logger.I("[ai] 子 agent 接近硬超时，提前收尾 conv=%s node=%s 剩余=%s",
						res.ConvID, sc.CurrentNodeID(), fmtRemain(remain))
				}
			}
		}
		// 每轮按「本轮到底有没有工具」重建 system（覆盖式，见 applySystemHints）。
		//
		// 收尾不再把 tools 撤下（见 toolsClosed 的说明）：这里传进去的 withTools
		// 在收尾轮仍是 true，system 与上一轮逐字节相同——正是为了让收尾那一发
		// 还能命中前缀缓存。模型被告知「工具已收回」靠的是尾部那条 user 消息。
		applySystemHints(messages, baseSystem, toolHint, cwd, len(tools) > 0, modeHasTools && len(tools) == 0)

		// WP20：新的一轮开始，把上一轮的思考归档。
		//
		// 分轮只靠 reasoning_round 事件（前端据此把已完成那轮单独收起），
		// 不再往 reasoning 正文里写「——— 第 N 轮 ———」：正文是给用户点开读的，
		// 里面夹轮次编号会把一段思考切得七零八落，用户明确要求去掉。
		if roundReasonBuf.Len() > 0 {
			// 上一轮的思考到这里结束，记进列表（轮次是 turnIdx-1）。
			finished := roundReasonBuf.String()
			roundReason = appendReasoningRound(roundReason, turnIdx-1, finished)
			roundReasonBuf.Reset()
			// 实时视图也按轮分段：前端收到它就把已完成那轮归档、清空当前缓冲。
			// 不发的话，界面上十几轮的思考会实时糊成一段——与落库后的分轮显示不一致。
			sink.Event("reasoning_round", map[string]any{"round": turnIdx - 1, "text": finished})
		}
		roundHasReasoning = false

		// 每轮开头压一次上下文：工具结果是在循环中间长出来的，
		// buildAIMessages 里那次摘要压缩管不到这里。
		//
		// 收尾轮（toolsClosed）不折：那一轮不会再长出新的工具结果，体积与上一发
		// 基本一样——上一发既然发得出去，这一发也不会超窗。而折叠会改写历史、
		// 把缓存整段作废，正是这次改造要避免的事。
		//
		// 预算随「上一发的实测用量」收紧：compactLoopMessages 的默认预算是按
		// 窗口的三分之一算的，那是个静态值——长会话里上一发可能已经占掉八成，
		// 再按三分之一往里塞工具结果，这一发就撞线了。上游报过 prompt_tokens
		// 之后，这一轮能给工具结果留的空间就是「上限 - 已用」，比固定值准。
		loopBudgetNow := aiLoopBudgetThisTurn(cfg, res.ConvID, loopBudget, estimateLoopTokens(messages))
		if !toolsClosed {
			if cr := compactLoopMessages(messages, loopBudgetNow); cr.Folded+cr.Summarized > 0 {
				// 每次折叠都要更新压缩状态（前端弹框读的是最新数字），但日志
				// 一次 run 只记一条就够了——数字每轮都在变，逐条刷只会制造噪音。
				if !loopCompactLogged {
					loopCompactLogged = true
					logger.I("[ai] 循环内上下文超预算：折叠 %d 条、摘要 %d 条（%d → %d tok）",
						cr.Folded, cr.Summarized, cr.Before, cr.After)
				}
				// 记一笔给弹框的「压缩状态」（WP2）：用户看不到任何痕迹时，
				// 「AI 忽然不记得前文」只会被当成 bug。
				rememberCompaction(res.ConvID, cr.Before, cr.After, cr.Folded, cr.Summarized, false)
				// 与历史摘要压缩共用 compact 事件，但多带 folded ids——
				// §8 的「压缩后重查率」就是用这些 id 去比对「之后有没有重查同一调用」。
				// start/done 成对发（虽然这里几乎是瞬时的）：前端按 phase 成对消费，
				// 只发一条会让那张卡片永远停在「正在压缩」。
				lim := contextLimitsOf(cfg)
				// 一次 run 只在第一次折叠时出一张卡。
				//
				// 折叠是每轮都可能发生的常态操作（几毫秒、不花钱），每轮都发一对
				// start/done 会让界面看起来「一直在压缩」，而用户根本无从判断
				// 哪一次是严重事件。后续折叠只记日志，数字仍在 context 统计里更新。
				if !loopCompactShown {
					loopCompactShown = true
					sink.Event("compact", loopCompactEvent("start", cr, lim, loopBudgetNow))
					sink.Event("compact", loopCompactEvent("done", cr, lim, loopBudgetNow))
				}
			}
		}

		// 后台任务结束的通知：在下一轮模型调用前递过去。
		//
		// 为什么必须注入而不是让模型自己查：任务结束的时刻与对话轮次无关，
		// 模型没有「被叫醒」的通道，只能反复 job_status 轮询——每查一次都是一次
		// 完整的模型往返，用户看到的就是一串「正在查询任务状态」。
		// 用 user 角色而不是 system：system 在对话中段插进去，各家的兼容性参差；
		// 正文里明确写「这是系统事件」，模型的反应与收到 system 提示一致。
		if evs := jobs.DrainDone(); len(evs) > 0 {
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": jobNoticeText(evs),
			})
		}

		// 用户在这一轮还没跑完时插进来的话（见 ai_inject.go）：在调模型之前接进来。
		//
		// 必须在这里接，而不是等这一轮结束再当新消息发：一轮可能包含十几次工具调用，
		// 用户等三十秒才发现「我那句提醒它根本还没看到」。也不能另起一轮——同一条会话
		// 两串历史同时往里写就是互相踩。
		if injects != nil {
			if items := injects.drain(); len(items) > 0 {
				// 插话是一个新诉求：它可能又要读文件、跑命令。收尾只是把闸门关上
				// （tools 一直挂在工具面上），这里把闸门重新打开即可——system 没变过，
				// 不需要重贴（重贴反而会改动前缀，白丢一次缓存）。
				if toolsClosed {
					toolsClosed = false
					toolsClosedHints = 0
					logger.I("[ai] 用户插话：重新开放工具 conv=%s", res.ConvID)
				}
				// 轮次预算同理补一批：上一问跑满的是上一问的事，不该卡住新提的这句。
				//
				// 但**token 预算已关闭时不能再扩**：否则「撞线收尾」被插话绕过，
				// 剩下几轮都在没有 token 护栏的情况下重发整份上下文。
				// 同时保证这一句至少能跑一轮工具：`limit == maxRounds` 时补不上，
				// 模型即便调工具也会被 `turnIdx > limit` 忽略，用户看到的是「它答应了但没动」。
				if !budgetClosed {
					if turnIdx > limit {
						limit = turnIdx + batchRounds
					}
					if turnIdx >= limit {
						limit = turnIdx + 1
					}
					if limit > maxRounds {
						limit = maxRounds
					}
				}

				// 先把已经生成的那半段收成一条独立回复（理由见 saveSegment 的说明）。
				// 判断条件要与 saveSegment 内部一致：正文、思考、工具三者有一个就算
				// 「这一段已经有东西了」，否则会白落一条空回复。
				if assistantBuf.Len() > 0 || reasoningBuf.Len() > 0 || len(records) > segStart {
					fullText.WriteString(assistantBuf.String())
					saveSegment(assistantBuf.String(), false)
					assistantBuf.Reset()
					reasoningBuf.Reset()
				}
				for _, it := range items {
					messages = append(messages, map[string]interface{}{
						"role":    "user",
						"content": it.Text,
					})
					// 直接追加，不走 saveUserTurn：那一条是给「重试同一轮」做幂等的，
					// 判据是「最后一条是不是同样的用户消息」。插话不是重试——连着插两
					// 句一模一样的话（「继续」「继续」）时它会把第二句当重复吞掉。
					if _, err := a.App.DB.AddAIMessage(res.ConvID, "user", it.Text); err != nil {
						logger.W("[ai] 保存插入消息失败 conv=%s: %v", res.ConvID, err)
					}
					// 回执：前端靠它把「等待插入」那行换成正式气泡，并在这里把
					// 已经吐出来的半段回答定格。带上原文，前端那一刻的内存即使
					// 已经切走了会话也照样能画出这句话。
					sink.Event("inject_ack", map[string]any{"id": it.ID, "text": it.Text})
				}
			}
		}

		// 超窗兜底：我们自己估的 token 与上游真实分词器之间总有偏差，
		// 历史里也可能躺着一条巨长的消息（整份文件、整段日志）。真撞了 400
		// 就按上游报的真实窗口强制压一次、重试一次，而不是把错误丢给用户
		// 让他自己想办法——「上下文超了」对普通用户是句废话。
		var turn chatTurn
		for attempt := 0; ; attempt++ {
			// 每次模型调用一条可取消的子 ctx：用户的「立即发送」要能在这一轮
			// 吐字的中途把它掐掉（见 ai_inject.go 的 interrupt）。
			// 它必须是 ctx 的子——前端断开、点停止、应用退出仍然要能取消它。
			turnCtx, turnCancel := context.WithCancel(ctx)
			if injects != nil {
				injects.attachTurn(turnCancel)
			}
			turn, err = a.streamAIChat(turnCtx, cfg, messages, res.ConvID, tools, onToken, reasonCb)
			if injects != nil {
				injects.detachTurn()
			}
			turnCancel()
			if err == nil {
				// 正常返回也要清一次：打断可能正好赶上上游发完 [DONE]，
				// 标记留下的话，下一次真正的失败会被误判成「立即发送打断」。
				if injects != nil {
					injects.consumeInterrupt()
				}
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
			newMsgs, berr := a.buildAIMessagesWithin(ctx, cfg, res.ConvID, mode, rebudget, true, sink)
			if berr != nil {
				break
			}
			// 新组装的上下文里 system 是干净的（没贴提示），base 要跟着换：
			// 它可能已经带上了这次新算出的摘要。规则、清单用法/工作目录/规模等
			// 行为约束由 composeBaseSystem + applySystemHints 重贴，和请求前压缩走同一处——
			// 漏贴的症状是「长对话压缩后 AI 突然不懂规矩」，极难复现。
			// 注意续跑/清单状态不在这里：它贴在末条 user 消息上（已并进 mentionBlock），
			// 由下面那次 applyMentionBlock 重贴——composeBaseSystem 不负责它。
			baseSystem = composeBaseSystem(firstSystem(newMsgs), rules, tools, cwd, extTrimmed)
			applySystemHints(newMsgs, baseSystem, toolHint, cwd, len(tools) > 0, modeHasTools && len(tools) == 0)
			// 引用材料、插入文件与续跑清单都要贴回去：重建出来的 messages 里那条
			// user 消息是库里的原文，@ 的内容与续跑提示只存在于内存。漏了这步，
			// 超窗重试之后 AI 会对着「照这个改」发愣、也不知道还有清单没做完。
			applyMentionBlock(newMsgs, mentionBlock)
			messages = newMsgs
			// 重建出来的是纯净消息（无轮次标记），必须按结构重新推导：
			// 否则下一轮打标会把 system + 全部历史 + 本轮用户消息一起打成当前轮，
			// 循环内折叠在本次运行剩余轮次里彻底失效（P0 修复）。
			retagMessagesByStructure(messages)
			sink.Event("notice", map[string]any{
				"message": "上下文超出模型窗口，已自动压缩历史并重试",
			})
		}
		totalUsage = addTokenUsage(totalUsage, turn.Usage)
		// 记下这一发的实测输入大小：下一轮判断要不要压时，它比我们的估算可信
		// （上游自己分的词）。多轮循环里上下文是逐轮长出来的，这个数字每轮刷新，
		// 于是「已经用了九成、再塞一轮工具结果就会超」这件事终于判得准了。
		rememberRequestSize(res.ConvID, turn.Usage.Prompt)
		// 段落用量单独累：落库是分段落的，每条回复只该记自己那几次调用的 token，
		// 否则插话后的第二段会把第一段的花费再算一遍。
		segUsage = addTokenUsage(segUsage, turn.Usage)
		// 明细按轮记、问答次数只第一轮 +1：一次提问触发五轮调用时，
		// 花费是真花了五份，但用户问的只有一句。
		//
		// run/node 在第一次派活后才存在：此前记空串（只归到会话），
		// 此后按 run 归集——「这次编排花了多少」因此只统计真正参与编排的那些调用。
		a.recordAIChatTurnScoped(res.ConvID, sc.RunID(), sc.CurrentNodeID(), cfg, turn.Usage, turnIdx == 1)

		if err != nil {
			// 用户在这段回答往外吐字的中途点了「立即发送」：这次取消是我们自己
			// 发起的，不是这一轮真的结束了，也不是用户点「停止」。已生成的那半段
			// 不丢——接着回到循环开头，那里的插入接入点看到 assistantBuf 有内容
			// 就会 saveSegment，再把排队的话接进上下文。
			//
			// 轮次预算也要补：这一轮可能是收尾轮，不补的话 continue 出去直接撞上
			// for 的上界，刚插进来的那句话就永远接不走了。
			if injects != nil && injects.consumeInterrupt() {
				if !budgetClosed && turnIdx >= limit {
					limit += batchRounds
					if limit > maxRounds {
						limit = maxRounds
					}
				}
				if turnIdx <= limit {
					logger.I("[ai] 立即发送：已打断当前生成 conv=%s 已完成=%d字 turn=%d limit=%d",
						res.ConvID, assistantBuf.Len(), turnIdx, limit)
					continue
				}
				// 轮次/预算都已到顶：不能再 continue（for 上界立刻到），也不能
				// 让排队的话消失——落成用户消息再收尾。
				logger.I("[ai] 立即发送已达轮次上限，插话改为落库 conv=%s turn=%d limit=%d",
					res.ConvID, turnIdx, limit)
				a.flushPendingInjects(injects, res.ConvID, sink)
				break
			}
			lastErr = err
			break
		}
		// 本轮有工具调用或正文 = 模型确实在推进任务，重置空响应计数，
		// 免得早先一次空响应残留的计数让后面某次偶发空响应直接判死。
		if len(turn.ToolCalls) > 0 || strings.TrimSpace(turn.Content) != "" {
			emptyReplyStreak = 0
		}

		if len(turn.ToolCalls) == 0 {
			// 空响应检测：没有工具调用，且正文是空的，但模型确实吐了东西
			// （思考或 usage）——这是「只思考不作答」，不是正常的最终回答。
			// 判定抽成纯函数便于单测（见 isEmptyModelReply）。
			if isEmptyModelReply(turn, reasoningBuf.Len()) {
				emptyReplyStreak++
				if emptyReplyStreak < maxEmptyReplyStreak && turnIdx <= limit && ctx.Err() == nil {
					// 输出被 length 截断时，光追加「请作答」没用：下一次仍会被
					// 同一个 max_tokens 掐在思考里。先把输出预算抬上去再重试。
					// 思考模型的 reasoning 也吃输出预算，8192 对这类任务太小。
					if turn.FinishReason == "length" {
						raised := raisedMaxTokensOnLength(cfg.MaxTokens, contextWindowOf(cfg))
						if raised > cfg.MaxTokens {
							logger.W("[ai] 输出被 length 截断，抬高 max_tokens 重试 conv=%s %d→%d 思考=%d字",
								res.ConvID, cfg.MaxTokens, raised, reasoningBuf.Len())
							cfg.MaxTokens = raised
						}
					}
					// 第一次：追加一条明确的 user 提示，逼模型吐出正文而不是继续空想。
					// 只重试一次（上限 2），避免网关持续异常时无限烧钱。
					logger.W("[ai] 模型只思考未作答，重试 conv=%s turn=%d 思考=%d字 finish=%s max_tokens=%d",
						res.ConvID, turnIdx, reasoningBuf.Len(), turn.FinishReason, cfg.MaxTokens)
					sink.Event("notice", map[string]any{
						"level":   "warn",
						"message": "模型刚才只思考、没有给出答复，正在重试一次。如果反复出现，建议关闭深度思考或换个模型。",
					})
					// 补一轮预算：否则 continue 出去可能直接撞上 for 上界。
					if turnIdx >= limit && !budgetClosed {
						limit++
						if limit > maxRounds {
							limit = maxRounds
						}
					}
					if turnIdx <= limit {
						messages = append(messages, map[string]interface{}{
							"role":    "user",
							"content": "你上一轮只做了思考，没有给出任何回复。请现在直接给出你的回答，或继续调用工具完成任务。",
						})
						continue
					}
				}
				// 重试仍空（或已到预算顶）：明确报错，绝不静默当成成功结束。
				// 只设 lastErr，让循环外统一的错误处理发一条 error 事件、把消息
				// 标记为 stopped；在这里也发会重复。用户因此能看到「本次没成功」，
				// 而不是一条凭空消失的空回复。
				logger.W("[ai] 模型连续 %d 轮只思考未作答，判定为空响应 conv=%s", emptyReplyStreak, res.ConvID)
				lastErr = fmt.Errorf("模型连续只思考、没有返回任何答复，本次未完成任务。请重试，或关闭深度思考 / 更换模型后再发")
				break
			}

			// 用户可能正好在这段回答往外吐字的时候插了话：那句话还排在队列里，
			// 没进上下文。直接 break 就等于把它丢在「这一轮已经结束」的缝里
			// （前端只能把字退回输入框），而它明明是一次新的诉求。
			//
			// 插话还可能赶上轮次预算刚好用完：预算这时补一批——上一问该用多少轮
			// 是上一问的事，不该卡住用户新提的这一句。
			if injects != nil && injects.pending() {
				if !budgetClosed && turnIdx >= limit {
					limit += batchRounds
					if limit > maxRounds {
						limit = maxRounds
					}
				}
				if turnIdx <= limit {
					// 这一段的最终回答要进 messages：插话之后模型看到的上下文里
					// 应该有「自己刚才说了什么」（否则下一轮的回答没有依据）；
					// 同时这也是前缀对齐的要求——下次提问重建历史时会从库里
					// 把这段回复接回去，本次 run 里不发它，重建出的前缀就从
					// 这里开始分叉（与 wireTail 的说明同理，方向反过来）。
					// 内容取 trim 后的：与 saveSegment 落库的正文逐字节一致，
					// 重建时才能对上。不进 wireTail——它本来就能从库里重建。
					if s := strings.TrimSpace(turn.Content); s != "" {
						messages = append(messages, map[string]interface{}{
							"role":    "assistant",
							"content": s,
						})
					}
					continue
				}
				// 补不上轮次了：别让这句话静默消失（见 flushPendingInjects）。
				a.flushPendingInjects(injects, res.ConvID, sink)
			}
			break // 没有工具调用 = 这一轮就是最终回答
		}
		if len(tools) == 0 {
			// 没有工具的一轮却仍返回工具调用（配额收尾轮 / 定时任务的问答档 /
			// 只读但工具全被过滤）。以前这里直接忽略并 break，代价是：模型把调用
			// 写成 DSML 文本 → 调用被丢掉 → 正文为空 → saveSegment 判定「未产出
			// 内容」跳过落库，用户看到「执行成功但没有任何输出」，库里只剩一条
			// user 消息。这里补一次明确的纠正，给它把话说圆的机会（只重试一次，
			// 避免网关异常时无限烧钱）。
			if !noToolsRetry {
				noToolsRetry = true
				logger.W("[ai] 无工具轮仍返回 %d 个工具调用，追加纠正提示后重试 conv=%s",
					len(turn.ToolCalls), res.ConvID)
				messages = append(messages, map[string]interface{}{
					"role": "user",
					"content": "你刚才尝试调用工具，但本次没有提供任何工具，调用不会被执行。" +
						"不要输出任何工具调用格式或标记，直接用文字回答上面的问题。",
				})
				// 补一轮预算：否则 continue 出去可能直接撞上 for 上界。
				if turnIdx >= limit && !budgetClosed {
					limit++
					if limit > maxRounds {
						limit = maxRounds
					}
				}
				if turnIdx <= limit {
					continue
				}
			}
			logger.W("[ai] 收尾轮仍返回 %d 个工具调用，已忽略 conv=%s", len(turn.ToolCalls), res.ConvID)
			break
		}

		// 收尾闸门：工具仍挂在工具面上（为了保住前缀缓存），但已经不允许执行。
		//
		// 模型没读懂尾部那句「工具已收回」时仍会发起调用。这里不执行、只补一条
		// 说明，并多给一轮让它把结论写出来；连续 aiToolsClosedMaxHints 次还在调，
		// 说明它读不懂，再给多少轮都是同一句「我先调用一下」。
		//
		// 前 aiToolsClosedMaxHints 次不能直接 break：工具调用不回填的话，这一轮
		// 的 assistant(tool_calls) 就凭空消失了，下次提问重建历史时与本次请求对不上。
		// 最后一次也不再让它空转——但这一轮说的话必须按协议补进 wireTail，正文本身
		// 早已通过 onToken 进了 assistantBuf，会随收尾一起落库。
		if toolsClosed {
			if toolsClosedHints >= aiToolsClosedMaxHints {
				wireTail = append(wireTail,
					stripInternalMessageKeys(closedToolMessages(turn.ToolCalls, turn.Content))...)
				// 整段对话一句正文都没有：用户会拿到一条空回复。再逼一次（只一次），
				// 否则「模型一直调工具」在界面上表现为「执行成功但什么都没有」。
				if strings.TrimSpace(assistantBuf.String()) == "" && !emptyNudgeOnClosed {
					emptyNudgeOnClosed = true
					logger.I("[ai] 收尾后仍无正文，追加一次「直接作答」提示 conv=%s", res.ConvID)
					messages = append(messages, map[string]interface{}{
						"role": "user",
						"content": "（系统提示）工具已收回，不要输出任何工具调用格式。请直接用文字说明这次任务的结论；" +
							"如果确实什么都没做，也请说一句，不要留空。",
					})
					if turnIdx >= limit {
						limit = turnIdx
						if limit > maxRounds+1 {
							limit = maxRounds + 1
						}
					}
					continue
				}
				logger.W("[ai] 收尾轮仍在调用工具，忽略并结束 conv=%s", res.ConvID)
				break
			}
			toolsClosedHints++
			logger.I("[ai] 收尾轮返回 %d 个工具调用，拒绝执行以保持请求前缀不变 conv=%s",
				len(turn.ToolCalls), res.ConvID)
			tailStart := len(messages)
			messages = append(messages, closedToolMessages(turn.ToolCalls, turn.Content)...)
			tagMessagesRound(messages[tailStart:], turnIdx)
			// 补一轮预算：continue 出去不能直接撞上 for 上界，否则这次收尾
			// 等于什么都没留下（与旧实现里「无工具轮仍调工具」的重试同一处理）。
			if turnIdx >= limit {
				limit = turnIdx
				if limit > maxRounds+1 {
					limit = maxRounds + 1
				}
			}
			continue
		}

		// 先告诉前端要调什么，再执行：一次工具可能跑几秒，
		// 这期间界面上得有「正在用 xx」，否则看起来就是卡死了。
		for _, c := range turn.ToolCalls {
			sink.Event("tool", map[string]any{
				"id": c.ID, "name": c.Name, "round": turnIdx,
				"args": logger.Trunc(c.Args, 200),
			})
		}
		// 命令执行期间的实时输出（pip install / npm install / go build 这类长命令）：
		// 卡片要能边跑边看到进度，而不是等跑完才一次性铺开。
		//
		// 回调挂在 ctx 上而不是给 runToolCalls 加参数：工具签名与注册表都不动，
		// 这一组里每个调用自己的 id 由 execOneToolCall 挂在同一个 ctx 上，
		// 所以并发跑两条命令也不会把输出写到同一张卡片上。
		toolCtx := aitools.WithCmdProgress(ctx, func(callID, add, tail string) {
			sink.Event("tool_output", map[string]any{
				"id": callID, "add": add, "tail": tail,
			})
		})
		// 任务清单（plan_update 工具）：清单更新不走 tool 卡片，单独一路事件。
		// 挂在 ctx 上而不是给 runToolCalls 加参数，理由与上面那条相同；
		// 前端因此能在步骤之间实时打勾，而不是等整轮结束看一段静态文字。
		toolCtx = aitools.WithPlanHook(toolCtx, func(u aitools.PlanUpdate) {
			sink.Event("plan", map[string]any{"items": u.Items, "note": u.Note})
			// WP17：同一份清单再落一次库。
			//
			// SSE 那条是给"正在看屏幕的人"的：刷新、切走、进程重启就没了。
			// 而清单要回答的是"中断后接着做"，所以必须持久化——
			// 双写而不是改成只落库：实时打勾的体验不能再等一次往返。
			a.saveConversationPlan(res.ConvID, u)
		})
		toolMsgs, recs := a.runToolCalls(toolCtx, turn.ToolCalls, turnIdx, turn.Content, seenCalls, cwd)
		records = append(records, recs...)
		tailStart := len(messages)
		messages = append(messages, toolMsgs...)
		// 发出去的原样留一份：重建历史时按这份接回去，请求前缀才与上一发一致
		// （理由见 wireTail 的说明）。strip 掉内部轮次标记——落的是要发的内容本身。
		wireTail = append(wireTail, stripInternalMessageKeys(toolMsgs)...)
		// WP4a：assistant(tool_calls) + 这批 tool 消息归入本轮。
		// 只标新追加的尾巴：整表打标会在「列表被重建过」时把历史一起改成本轮。
		// 标记只活在内存里，发请求前会被 stripInternalMessageKeys 剥掉。
		tagMessagesRound(messages[tailStart:], turnIdx)
		for _, r := range recs {
			sink.Event("tool_result", map[string]any{
				"id": r.ID, "name": r.Name, "ok": r.Error == "", "ms": r.Ms,
				"error": r.Error, "dedup": r.Dedup, "denied": r.Denied,
				"added": r.Added, "removed": r.Removed,
				// 行级 diff：卡片展开时的主体内容。只有 added/removed 时用户
				// 只知道「改了两行」而不知道改成了什么。
				"diff": r.Diff,
				// 产物路径（file_write / file_edit）：前端据此在本条回复下面
				// 实时挂出「产物」条，点击可预览。流式期就得有，等 done 再算
				// 会让「文件已经写完了」和「卡片上看得见」之间隔一整轮回答。
				"path": r.Path,
				// 编排类工具带回 run/node，前端可以立刻挂出 run 树，
				// 不必等整轮结束再回头查一次。
				"runId": r.RunID, "nodeId": r.NodeID,
				// 保留换行：JSON 结果压成一行就没法读了。
				//
				// 上限必须与落库一致（aiRecordResultMaxRune）。原先写死 400：
				// 一来几乎所有工具结果都超，每张卡片都挂一句「已截断：共 N 字」；
				// 二来刷新后走落库的 2000 字版本，同一张卡片内容会突然变长。
				"result": truncateKeepLines(r.Result, aiRecordResultMaxRune),
				// 人看的输出（命令的 stdout/stderr）：卡片上铺的是它，
				// 不是上面那段给模型的 JSON——那段只剩转义字符，
				// 截断后连 JSON 都不是。
				"output": truncateKeepLines(r.Output, aiRecordOutputMaxRune),
			})
		}

		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		// 一批轮次用尽：要么再开一批，要么撤掉工具让模型收尾。
		//
		// 判据只有一个——**这一批有没有真的往前推**（madeProgress：有真正执行过、
		// 不是被去重跳过、也不是被拒的调用）。有就无条件再给一批，没有就说明在打转。
		//
		// 这里刻意不再有「36 轮到了，让用户发一句继续」：轮次该用多少取决于活儿本身，
		// 拍一个数字当墙，表现就是模型干到一半自己收尾，用户被迫再打一句「继续」；
		// 而那句话会被当成新的用户诉求记进会话和记忆，下次它真会拿「继续」当任务。
		// maxRounds 是本次运行的硬兜底：默认 120 轮防病态空转，定时任务会收得更紧。
		if turnIdx >= limit {
			if !madeProgress(recs) {
				// 一整批都没有新动作：再给多少批也是同一个结果，早停省下来的
				// 是十几次全额计费。要说一声——不然界面上的答案是凭空结束的。
				sink.Event("notice", map[string]any{
					"message": fmt.Sprintf("连续 %d 轮工具调用没有新进展（在重复或未获授权），先收尾", batchRounds),
				})
				toolsClosed = true
				messages = append(messages, map[string]interface{}{
					"role": "user",
					"content": "（系统提示）工具已收回（再调用不会被执行）：不要继续调用工具，" +
						"请直接用文字给出结论，并说明为什么停在这里。",
				})
			} else if limit >= maxRounds {
				sink.Event("notice", map[string]any{
					"message": fmt.Sprintf("本次提问已连续执行 %d 轮工具，达到单次预算，先收尾；没做完的部分说一声「接着做」就行", turnIdx),
				})
				toolsClosed = true
				messages = append(messages, map[string]interface{}{
					"role": "user",
					"content": "（系统提示）本次提问已达到轮次上限，工具已收回（再调用不会被执行）。" +
						"不要再调用工具，请直接用文字给出结论；没做完的部分说明一声。",
				})
			} else {
				if !budgetClosed {
					limit += batchRounds
				}
				if limit > maxRounds {
					limit = maxRounds
				}
				// 静默续期：这件事不需要告诉用户。
				//
				// 原来这里会插一条「已连续执行 12 轮工具，自动继续（本次预算 24 轮）」，
				// 但用户读到的不是「它在努力」，而是「怎么又弹一句、还没做完」。
				// 真正需要被告知的只有两种：没进展了要收尾、或者到了硬顶要停。
				continue
			}
			// 收尾：不额外塞一条 user 提示——tool 结果后面跟 user 消息在部分网关
			// 会被判成非法序列，而 tools 消失本身就足以让模型收尾。
		}
	}

	assistant := assistantBuf.String()
	// 整次提问的正文：中途插过话时 assistant 只是最后一段，通知/记忆要的是全文。
	allText := fullText.String() + assistant
	rounds := countRounds(records)
	// 结果里带上用量与轮次：编排层要用它做 run 级成本归集与「这次派活值不值得」的判断。
	// 在这里一次性写完，后面几条 return 分支就都带上了。
	res.Usage = totalUsage
	res.Rounds = rounds
	res.ToolCount = len(records)
	// 顶层 run 的 used_tokens 在这里累加。此前只有子 agent（handleSpawnAgent）
	// 会写这一列，于是「整段会话」回放里 master run 恒显 0 tok——花掉的 token
	// 全在事件里、却不在 run 头上，用户看到的轨迹第一行就是假的 0。
	// 子 agent 自己也会走这个函数，用 req.Fresh 挡掉，避免和它的累加重复。
	if !req.Fresh && res.RunID != "" {
		if tk := totalUsage.Prompt + totalUsage.Completion; tk > 0 {
			if err := a.App.DB.AddAgentRunUsage(res.RunID, tk); err != nil {
				logger.W("[ai] 累加主 run 用量失败 run=%s: %v", res.RunID, err)
			}
		}
	}
	// WP1 修复：把 runID 带回结果层。
	//
	// 原来只有 spawn_agent 会在工具结果里写 run_id，于是「没派过活的对话」
	// 前端永远拿不到 runId，轨迹抽屉恒空——这个功能对最常见的用法等于不存在。

	if lastErr != nil {
		if ctx.Err() != nil {
			// 用户主动取消或页面断开。这里只记原因，怎么措辞交给上层
			// （见 ai_stream.go 的收尾通知）：执行核心不该关心通知长什么样。
			res.StopCause = context.Cause(ctx)
			// 保存已收到部分（用户消息已在调模型前落库）。
			// 工具调用不需要在这里额外打断：支持 ctx 的工具在取消那一刻就已经
			// 停下了（见 ai_tools.go 的 callToolWithTimeout）。
			saveSegment(assistant, true)
			persistWire()
			sink.Event("done", withReasoningRounds(withRunID(doneEvent(res.ConvID, firstTokenAt, totalUsage, cfg.Model, rounds, len(records), reasonRounds, reasonChars), res.RunID), finalReasoningRounds))
			res.Text = allText
			res.Stopped = true
			res.Err = lastErr
			return res
		}
		// 上游报错：用户消息已落库，历史不会空。若疑似因 base64 本地图片被拒，给明确提示。
		//
		// 已经流出去的那部分正文也要落库：否则界面上明明有半截回答，刷新一次就凭空消失，
		// 而错误提示只说「请求失败」。它同样算「被打断的回复」，用 stopped 标记。
		// 空正文但调过工具时 saveSegment 内部照样会落一条（见它的说明）。
		saveSegment(assistant, true)
		persistWire()
		msg := lastErr.Error()
		if hint := a.imageErrorHint(lastErr, req.Images); hint != "" {
			msg += hint
		}
		// 上游把工具名/id 打码成 *** 时，这条是唯一能告诉用户"坏在哪个工具"的线索。
		if hint := toolArgErrorHint(lastErr, records); hint != "" {
			msg += hint
		}
		sink.Event("error", map[string]any{"message": msg})
		res.Text = allText
		res.Err = lastErr
		return res
	}

	// 正常结束，落库助手回复（用户消息已在调模型前落库）
	saveSegment(assistant, false)
	persistWire()
	if !req.Fresh {
		// 只对顶层刷会话更新时间：子 agent 结束时改父会话的 updated_at 会让
		// 侧栏那条对话无缘无故跳到最前面，看起来像用户刚刚发过言。
		_ = a.App.DB.UpdateAIConversationMeta(res.ConvID, "", "")
	}
	logger.I("[ai] 流式结束 conv=%s 首字=%dms 轮次=%d 工具=%d in=%d out=%d cached=%d",
		res.ConvID, firstTokenAt.Milliseconds(), rounds, len(records), totalUsage.Prompt, totalUsage.Completion, totalUsage.Cached)
	sink.Event("done", withReasoningRounds(withRunID(doneEvent(res.ConvID, firstTokenAt, totalUsage, cfg.Model, rounds, len(records), reasonRounds, reasonChars), res.RunID), finalReasoningRounds))

	// 自动写项目每日记忆：确定性提取「用户要了什么、改了哪些文件、跑了几轮」，
	// 不调模型所以一定存在，让下一轮/下一会话不必重读整个项目。
	// 子 agent 跳过：它的 task 是生成的、不是用户诉求，落进每日记忆会污染口径。
	if !req.Fresh {
		dailyFiles := make([]string, 0, len(records))
		for _, rec := range records {
			if rec.Path != "" {
				dailyFiles = append(dailyFiles, rec.Path)
			}
		}
		// 重点块按任务标题归并：续跑时用户只打了「继续」，用它当条目名等于没记。
		// 首轮标题还没生成（conv.Title 为空），这时标题就是这句提问本身。
		dailyTitle := ""
		if conv != nil {
			dailyTitle = conv.Title
		}
		if dailyTitle == "" {
			dailyTitle = message
		}
		appendDailyMemory(res.ConvID, cwd, dailyTitle, req.Message, dailyFiles, rounds, len(records))
		// 任务收尾补一段「工程笔记」（需求/做了什么/结论/坑/门禁）与几条常青条目。
		// 后台跑一次调用——它跟「这次回答」无关，用户不该等它；失败也只是记忆少一段。
		// 计划模式不写：那是只读模式，连 AI 自己的记忆目录都不该动（见 ai_chat.go 同款判断）。
		if mode != "plan" {
			a.writeTaskMemoryAsync(cfg, taskMemoryInput{
				ConvID:     res.ConvID,
				MessageID:  lastSavedMsgID,
				SessionTag: sessionInfoOf(ctx),
				Cwd:        cwd,
				Title:      dailyTitle,
				User:       req.Message,
				Reply:      allText,
				Files:      dailyFiles,
				Rounds:     rounds,
				Tools:      len(records),
				ToolLines:  summarizeToolActivity(records, memoryNoteToolMaxLines),
			})
		}
	}

	res.Text = allText
	return res
}

// isEmptyModelReply 判定一轮模型输出是否为「只思考、没作答」的空响应。
//
// 某些思考模型 / 中转网关会把全部输出预算花在 reasoning 上，正文一个字不吐
// 就结束（实测 deepseek 系经第三方网关会出现：40 秒、2000+ 块全是思考、
// content/tool_calls 全空、usage 也为 0）。这种轮次如果不拦，外层会把
// 「没有工具调用」当成正常收尾，saveSegment 又因正文和工具都空而跳过落库——
// 用户看到的就是「任务停了、什么都没做」。
//
// 判据：没有工具调用 + 正文为空 + 这一轮确实吐过东西（思考内容或 token 用量）。
// reasoningChars 传的是 reasoningBuf 的跨轮累计长度；正文只看本轮 turn.Content，
// 避免把「插话切段后本轮正文恰好为空」误判成空响应。
func isEmptyModelReply(turn chatTurn, reasoningChars int) bool {
	if len(turn.ToolCalls) > 0 || strings.TrimSpace(turn.Content) != "" {
		return false
	}
	return reasoningChars > 0 || turn.Usage.Prompt > 0 || turn.Usage.Completion > 0
}

// raisedMaxTokensOnLength 返回 length 截断后重试该用的输出预算。
//
// thinking 模型的 reasoning 与正文共用 max_tokens：8192 这类默认值很容易
// 被思考吃光，正文一个字没吐就被上游 finish_reason=length 掐断。此时若只是
// 追加「请作答」重试，仍会撞同一堵墙——必须同时把预算抬上去。
// 下限 32K（reasoner 的常用起点）；上限取物理窗口 1/4，避免输入+预留输出
// 超过窗口反而 400。窗口未知时不做上限裁剪。
func raisedMaxTokensOnLength(current, window int) int {
	raised := current * 2
	if raised < 32768 {
		raised = 32768
	}
	if window > 0 {
		if ceiling := window / 4; ceiling >= 4096 && raised > ceiling {
			raised = ceiling
		}
	}
	if raised <= current {
		return current
	}
	return raised
}
