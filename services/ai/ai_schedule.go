package ai

// 定时任务 → AI 会话的桥接层。
//
// 为什么单独一个文件：runAgent 是 SSE 与子 agent 共用的执行核心，刻意非导出
// （见 agent_run.go:154）。定时任务 runner 在 services 包，反向 import 本包会成环，
// 所以它不能直接调 runAgent；这里提供一个只收基本类型的导出包装，再由 main.go
// 以函数字段注入宿主（与 AskAIFn 同一套路）。
//
// 语义边界：
//   - 每个定时任务有一个专属空间（「任务名 · MM-DD HH:mm」，侧栏闹钟图标），
//     空间里只有一条会话；每次执行把这一轮问答追加进去，不再按次新建。
//     这样「一个任务 = 一个空间 = 一条对话」，多次运行按消息时间排在同一条线上，
//     上下文也能自然延续（超预算时由 buildAIMessagesWithin 压缩，不会无限膨胀）。
//     Scope="new" 保留为旧口径（每次新建一条），当前界面不暴露。
//   - 无人值守，因此 agent / readonly 两档一律用 full 权限：默认档遇到需确认的
//     工具会空等 90 秒再拒绝（ai_approval.go:44），一轮下来又慢又残缺。
//     full 只绕过「问」，fsguard 写白名单与高危命令硬拦截仍然生效。
//   - 不靠前端在场：推理、工具循环、落库全在后端；Wails 事件只用于刷新 UI，
//     事件没人监听就丢，拿它承载执行等于把「无人值守」变成「必须有人盯着」。

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
)

var scheduledSpaceMu sync.Mutex

// ScheduledPromptRequest 宿主传给 AI 门面的一次定时执行。
// 字段全是基本类型：跨包注入的函数签名不能带 services/ai 的类型，否则 services
// 包就得 import 本包，形成循环依赖。
type ScheduledPromptRequest struct {
	Prompt string
	Cwd    string
	Mode   string // chat | readonly | agent
	TaskID string
	// TaskName / TaskCreatedAt 只用于「空间被用户删掉后按原名重建」。
	// 创建时间是历史事实，重建时不能用当前时间，否则空间名会随时间漂移。
	TaskName      string
	TaskCreatedAt string
	TimeoutSec    int
	// OnProgress 执行期的步骤回调（工具名 + 参数摘要），可为 nil。
	// 定时执行没有 SSE 连接，不推这一条的话前端只能看到「正在执行…」四个字。
	OnProgress func(tool, args string)
}

// ScheduledPromptResult 用 ErrMsg 而不是 error：这个结构体可能被 wails codegen
// 扫到，error 接口过不了 JSON 绑定会产出一条假方法。
type ScheduledPromptResult struct {
	ConvID string
	Text   string
	ErrMsg string
}

// progressSink 把执行期的工具调用转成给宿主的进度回调。
//
// 不落库：进度回答的是「此刻在做什么」，事后回看没有意义（下一次执行会覆盖它）；
// 而每几百毫秒写一行库、只为让界面转两圈，代价与收益完全不成比例。
//
// 只转 tool 事件：token 碎片（Push）量大且对「跑到哪儿了」没有信息量，
// notice / error 由收尾路径统一反映在结果串里。
//
// 与原来那个空实现（nullSink）的区别仅在于多一条回调；没有回调时行为完全一致，
// 所以不需要保留两套实现。
type progressSink struct {
	onProgress func(tool, args string)
}

func (progressSink) Push(string, string) {}

func (s progressSink) Event(kind string, data map[string]any) {
	if s.onProgress == nil || kind != "tool" {
		return
	}
	tool, _ := data["name"].(string)
	args, _ := data["args"].(string)
	if strings.TrimSpace(tool) == "" {
		return
	}
	s.onProgress(tool, args)
}

func (progressSink) Failed() bool { return false }

// 无人值守执行的费用护栏。
//
// 普通会话默认允许 120 轮工具、20 倍上下文的 token 预算，那是留给「有人盯着、
// 随时能点停止」的长任务；定时任务没有人看，模型一旦在工具循环里打转，
// 到 600s 超时之前能把整月额度烧完。这两个值只收窄不放大：
//   - 24 轮工具足够「读几个文件 + 跑两三条命令 + 写一份摘要」这类正经日报；
//   - 1 倍上下文（12 万计费 token）对 24 轮问答绰绰有余，且始终紧于主对话的 2 倍。
//
// 原来这里是 8×：那是按「每轮重发整份 Prompt」的全量口径标定的。主循环改按
// billableTokens（缓存不算钱）计数后，8× 会变成主对话预算的 4 倍，与「只收窄」
// 的语义相反，故同步降到 1。
//
// 撞线不是失败：撤掉工具让模型把已有结果说完（见 agent_run.go 的预算收尾），
// 运行历史里仍是一次完整执行，用户想继续可以手动再跑一次。
const (
	scheduledMaxToolRounds    = 24
	scheduledTurnBudgetFactor = 1
)

// taskSpaceName 定时任务的专属空间名：任务名 + 创建时刻。
// 时间只到分钟、不带年份：侧栏约 200px，带年份必然截断；完整时间在 tooltip 里给。
func taskSpaceName(taskName, createdAt string) string {
	name := strings.TrimSpace(taskName)
	if name == "" {
		name = "定时任务"
	}
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60]) + "…"
	}
	return name + " · " + formatCreatedAt(createdAt)
}

// formatCreatedAt 兼容调度器与 AI 会话两套时间格式；解析不了就退回当前时刻，
// 总比显示一个空后缀强。只用于展示名，不参与任何调度计算。
func formatCreatedAt(s string) string {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("01-02 15:04")
		}
	}
	return time.Now().Format("01-02 15:04")
}

// autoSpaceSuffixRe 匹配 taskSpaceName 生成的「· MM-DD HH:mm」尾巴。
// 用来区分「这个名字是系统自动起的」和「用户自己改过」：只有前者可以被后台纠正。
var autoSpaceSuffixRe = regexp.MustCompile(` · \d{2}-\d{2} \d{2}:\d{2}$`)

// isAutoSpaceName 判断空间名是否由系统自动生成。
// 「定时任务」是早期所有 AI 任务共用空间的默认名——它同样是自动名，
// 认领之后应该跟着任务改叫「某任务 · 时间」，否则侧栏里那个老名字会一直挂着。
func isAutoSpaceName(name string) bool {
	n := strings.TrimSpace(name)
	if n == "" || n == "定时任务" {
		return true
	}
	return autoSpaceSuffixRe.MatchString(n)
}

// EnsureTaskSpace 给一个定时任务幂等地建好专属空间，返回空间 id。
//
// 两个调用时机：保存任务时（用户建完就能在侧栏看到闹钟空间），以及每次执行前
// （用户把空间删了的话按原创建时间重建，历史会话仍归得回去）。
// 按 task_id 查而不是按名字查：用户改过空间名也不该再建一个重复的分组。
//
//wails:ignore
func (a *AIService) EnsureTaskSpace(taskID, taskName, createdAt string) string {
	if a.App == nil || a.App.DB == nil || strings.TrimSpace(taskID) == "" {
		return ""
	}
	scheduledSpaceMu.Lock()
	defer scheduledSpaceMu.Unlock()
	sp, err := a.App.DB.EnsureScheduledSpace(taskID, taskSpaceName(taskName, createdAt))
	if err != nil {
		logger.W("[ai] 建立定时任务空间失败 task=%s: %v", taskID, err)
		return ""
	}
	if sp == nil {
		return ""
	}
	// 名字自愈：任务改过名却没走到 SyncTaskSpaceName（改名前程序退出、老版本残留），
	// 或库里还是个自动名（早期统一的「定时任务」），这里顺手纠正成标准名。
	// 只动自动名——用户手动改过的名字后台不该静默覆盖。
	if want := taskSpaceName(taskName, createdAt); sp.Name != want && isAutoSpaceName(sp.Name) {
		if err := a.App.DB.SyncScheduledSpaceName(taskID, sp.Name, want); err != nil {
			logger.W("[ai] 纠正定时任务空间名失败 task=%s: %v", taskID, err)
		}
	}
	return sp.ID
}

// SyncTaskSpaceName 任务改名后同步空间名前缀；只在空间名仍是旧自动名时生效，
// 用户手动改过的名字不会被覆盖（db 层用 name = 旧名做条件）。
//
//wails:ignore
func (a *AIService) SyncTaskSpaceName(taskID, oldTaskName, newTaskName, createdAt string) {
	if a.App == nil || a.App.DB == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	if err := a.App.DB.SyncScheduledSpaceName(
		taskID,
		taskSpaceName(oldTaskName, createdAt),
		taskSpaceName(newTaskName, createdAt),
	); err != nil {
		logger.W("[ai] 同步定时任务空间名失败 task=%s: %v", taskID, err)
	}
}

// scheduleRunPolicy 把界面上的三档翻译成执行参数。
//
// chat：不挂任何工具，纯问答，天然不会触发审批；
// readonly：只挂 LevelRead 工具（查文件/搜网页/读状态），模型看不到写工具；
// agent：完整工具面。
//
// 中间档不是「挂了写工具但拦下来」——模型看得到却调不动时会换个参数反复重试，
// 一路把上下文烧光（ai_tools.go:279 的注释就是这么处理 plan 模式的）。
func scheduleRunPolicy(mode string) (noTools, readOnly bool, permission string) {
	switch strings.TrimSpace(mode) {
	case "agent":
		return false, false, aiPermissionFull
	case "readonly":
		return false, true, aiPermissionFull
	default: // chat，也兜住空串/脏数据
		return true, false, ""
	}
}

// scheduledConvTitle 定时任务那条会话的标题。
//
// 一条会话对应一个任务，所以标题就是任务名（空间名里已经有创建时间，
// 会话再用时间当标题反而看不出这是哪个任务）。空名兜底成「定时任务」。
func scheduledConvTitle(taskName string) string {
	name := strings.TrimSpace(taskName)
	if name == "" {
		name = "定时任务"
	}
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60]) + "…"
	}
	return name
}

// RunScheduledPrompt 跑完一轮 AI 会话并落库，供定时任务 runner 注入调用。
//
// 刻意不导出到前端：这是 Go 侧的无头入口，前端要聊天走 SSE。
//
//wails:ignore
func (a *AIService) RunScheduledPrompt(in ScheduledPromptRequest) ScheduledPromptResult {
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return ScheduledPromptResult{ErrMsg: "提示词为空"}
	}
	if a.App == nil || a.App.DB == nil {
		return ScheduledPromptResult{ErrMsg: "数据库尚未就绪"}
	}

	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	// 必须有超时：模型流挂住时不带 deadline 会永久占着这个 goroutine，
	// 而 schedInflight 又按任务 id 持有，下一次调度点会被一直跳过。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 复用还是新建：一个任务一条会话，能查到就续聊，查不到（首次运行）才新建。
	// 查库失败不阻断执行：宁可多一条会话，也不该因为读不到旧会话就不跑任务。
	convID := ""
	if c, cerr := a.App.DB.FindScheduledConversation(in.TaskID); cerr != nil {
		logger.W("[ai] 读取定时任务会话失败 task=%s: %v", in.TaskID, cerr)
	} else if c != nil {
		convID = c.ID
	}

	noTools, readOnly, permission := scheduleRunPolicy(in.Mode)
	// 空间先落地：除了给新建会话用，还要拿它把「续聊的那条旧会话」挂回来。
	spaceID := a.EnsureTaskSpace(in.TaskID, in.TaskName, in.TaskCreatedAt)
	// 用户删过空间的话，上面会按原创建时间重建一个新的——旧会话的 space_id 还指着
	// 已删的那个，侧栏按空间归组时就再也列不出来（既不在任何空间里，也不在「任务」
	// 组，那组只收 space_id 为空的）。续聊前顺手归位，一条 UPDATE 的事。
	if convID != "" && spaceID != "" {
		if err := a.App.DB.SetAIConversationSpace(convID, spaceID); err != nil {
			logger.W("[ai] 归位定时会话到空间失败 conv=%s: %v", convID, err)
		}
	}
	// Fresh=false（默认）：只有它才会建会话、写用户消息、存助手回复。
	// 子 agent 的 Fresh=true 不落 ai_messages，定时任务的产出就看不见了。
	res := a.runAgent(ctx, AgentRunReq{
		ConvID:            convID,
		Message:           prompt,
		Mode:              "chat",
		Cwd:               strings.TrimSpace(in.Cwd),
		SpaceID:           spaceID,
		Permission:        permission,
		ScheduledNoTools:  noTools,
		ScheduledReadOnly: readOnly,
		MaxToolRounds:     scheduledMaxToolRounds,
		TurnBudgetFactor:  scheduledTurnBudgetFactor,
	}, progressSink{onProgress: in.OnProgress})

	if res.ConvID != "" {
		// 只有「这一轮新建了会话」才写标题：续聊时改名会把用户看到的任务名顶掉，
		// 而标题在这个口径下代表的是任务本身，不是某一次运行。
		if convID == "" {
			if err := a.App.DB.UpdateAIConversationMeta(res.ConvID, scheduledConvTitle(in.TaskName), ""); err != nil {
				logger.W("[ai] 设置定时会话标题失败 conv=%s: %v", res.ConvID, err)
			}
		}
		if err := a.App.DB.MarkAIScheduledConversation(res.ConvID, in.TaskID); err != nil {
			// 未读标记是体验增强，写失败不该把一次成功执行判成失败。
			logger.W("[ai] 标记定时会话未读失败 conv=%s: %v", res.ConvID, err)
		}
	}
	if res.Err != nil {
		return ScheduledPromptResult{ConvID: res.ConvID, Text: res.Text, ErrMsg: res.Err.Error()}
	}
	if res.ConvID == "" {
		return ScheduledPromptResult{ErrMsg: "AI 未返回会话 id"}
	}
	if res.Stopped {
		return ScheduledPromptResult{
			ConvID: res.ConvID,
			Text:   res.Text,
			ErrMsg: fmt.Sprintf("本轮提前结束（%v）", res.StopCause),
		}
	}
	return ScheduledPromptResult{ConvID: res.ConvID, Text: res.Text}
}
