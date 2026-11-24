package ai

// 内置 agent：让 AI 页的对话流自己调用 MCP 工具集。
//
// 与「外部 AI 通过 MCP 调宿主」是同一份工具、同一套闸门（internal/mcp 的等级门
// 与单工具开关），区别只在调用方：这里是宿主自己的 AI 页，那里是 dsh / 其他客户端。
// 不另设一套开关 —— 两套开关迟早会出现「面板里关了，内置 AI 还能调」，
// 而那种时候没人说得清应该信哪一个。
//
// 分层的职责：
//   - 本文件：工具规格转换、执行、结果整形、上下文压缩
//   - ai_chat.go：从 SSE 分片里把 tool_calls 拼出来（协议层）
//   - ai_stream.go：多轮循环、计费累加、事件下发、落库
//
// 这条链上唯一不可逆的语义在 ai_stream.go：工具调用轮次里的 assistant 文本
// 也会流给用户看。模型常常先说「我查一下」再调工具，那段话不该被吞掉。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/fsguard"
	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	aitools "quickdock/services/ai/tools"
)

const (
	// aiToolMaxRounds 一次提问里「一批」工具调用最多几轮。
	//
	// 8 轮够覆盖「查列表 → 读细节 → 交叉确认 → 回答」这类查询链路，但不够分段写长文档：
	// 实测让模型整理一份几十节的教程时，它每轮 file_edit 填一节，8 轮用完才写到第十八节，
	// 而收尾轮只能空手总结「还差什么」。12 轮覆盖了这类「边写边改」的活儿。
	// 再多就不该无脑继续——每多一轮都是全额计费（上下文要整份重发一遍）。
	//
	// 所以这是「一批」而不是硬上限：到顶时看这一批有没有真进展（见 agent_run.go
	// 的 madeProgress），有就再给一批，没有就收尾。
	aiToolMaxRounds = 12

	// aiToolMaxTotalRounds 单次提问的工具轮次预算（十批）＝病态兜底，不是进度墙。
	//
	// 曾经这里是 36（三批），到点就撤掉工具并提示用户「再发一句继续」。那个数字是
	// 拍的，害处却实在：一次真正的长排查（扫整个项目、边写边改一篇长文档）本来
	// 就说不准要多少轮，硬停只会让模型干到一半自己收尾，用户被迫再打一句「继续」
	// ——而那句话会被当成新的用户诉求记进会话与记忆。
	//
	// 现在只有「这一批有没有真实动作」决定要不要续（madeProgress）。这个预算防的
	// 是另一种病：每次换一个参数、看着一直在动，其实在原地空转——那种情况下只有
	// 账单会说话。正常任务碰不到：一次长排查通常 20~40 轮，十批给到 120 轮。
	aiToolMaxTotalRounds = aiToolMaxRounds * 10

	// aiToolResultMaxRune 单个工具结果送去模型的字符上限。
	// file_grep / log_read 这类结果可以是几十 KB，一条就能把上下文烧掉一半。
	// 超出部分不丢：完整结果写日志，只把头部递给模型，并在末尾说明被截断。
	aiToolResultMaxRune = 12000

	// aiToolFieldKeepRune 超限压缩时，单个字符串字段先保留多少字。
	// 2000 够放一段文件正文或一条 diff；仍超就把这个值逐级减半。
	aiToolFieldKeepRune = 2000

	// aiToolArrayKeep 超限压缩时，单个数组最多保留几项。
	// git_status 一次能返回 500 条变更，每条都短——按字段截救不了这种，
	// 只能截数组，并在末尾补一条 {"_dropped": n} 让模型知道自己少看了多少。
	aiToolArrayKeep = 200

	// aiToolFoldRune 上下文压缩时，被折叠的老工具结果保留多少字符。
	// 折叠而不是删除：模型仍需知道「我查过这个东西、结论大致是什么」，
	// 否则它会为了同一件事再查一遍，白花一轮。
	aiToolFoldRune = 600

	// aiRecordResultMaxRune 落库时的结果上限。
	//
	// 与 aiToolResultMaxRune 是两回事：那个是「喂给模型多少」（影响上下文和钱），
	// 这个是「留给人回看多少」（影响库体积）。完整结果从来没落过库——
	// 它只在当次请求的上下文里存在过。
	//
	// 流式下发（agent_run 的 tool_result 事件）也用这个值，别单独写小数字：
	// 两处不一致时，卡片一开始显示一截「已截断」，刷新后又突然变长。
	aiRecordResultMaxRune = 2000

	// aiRecordOutputMaxRune 落库时「人看的输出」（Output）上限。
	//
	// 比 Result 宽：Result 是给模型的结构化 JSON，一段命令输出在里面是
	// `"stdout": "line1\nline2..."` 这种转义形态，2000 字里没几行真内容；
	// Output 是纯文本，同样 8000 字能装下两百来行日志或一段文件正文，回看时才有用。
	// 流式下发用的也是这个值：两处不同的话，刷新一下卡片内容会突然变长/变短。
	aiRecordOutputMaxRune = 8000

	// aiLoopTokenBudget 已改为按模型窗口动态计算（见 ai_context.go 的 aiLoopBudget）。
	// 两者边界仍然不同、不能合并：
	//   - aiContextBudget 管「DB 里的历史有多长」，超了调模型做摘要（贵、慢、一次）；
	//   - aiLoopBudget 管「本轮循环里工具结果堆了多少」，超了就地折叠（便宜、不调模型）。
	// 只在 buildAIMessages 里压一次是压不住工具结果的：它们是在循环中间长出来的。

	// aiToolTimeout 单个工具调用的兜底超时。
	//
	// mcpsrv.CallAgent 是同步调用、也没有 ctx，工具自己不设防就会一直占着这条流：
	// 界面上卡片永远停在「执行中」，用户点停止也掐不掉（上游请求能取消，
	// 已经在跑的那次调用不能）。超时后按失败回给模型，对话还能继续往下走。
	//
	// 取 5 分钟而不是更短：run_command 自己允许最长 600 秒（用户会用来跑构建），
	// 卡在这条线上的次数很少，而「跑一半被告知超时」比「多等两分钟」更烦。
	// 这条线防的是挂死（网络黑洞、子进程不退出），不是限制正常调用。
	aiToolTimeout = 5 * time.Minute

	// aiChatTurnBudgetFactor 主循环的总预算＝上下文预算 × 这个倍数。
	//
	// 数的是**计费口径**（billableTokens：真正新增的输入 + 输出，缓存命中不算），
	// 不是每轮重发的整份 Prompt。原来按 Prompt+Completion 逐轮累加，1M 窗口
	// 97% 命中率下 60 轮就记 735 万、实际新增只有 25 万，长任务被提前收尾
	// （2026-11-24 实测）。口径修正后倍数必须同步收紧，否则预算形同虚设。
	//
	// 取 2 倍（默认软上限 12 万 → 24 万）是正常长任务的余量：实测 61 轮的大任务
	// 计费约 25 万，撞线后自动续半步（12 万）即可收工；真正打转的循环因为没有
	// 新增内容，计费几乎不涨，靠轮次上限（aiToolMaxTotalRounds = 120）兜住。
	// 硬顶仍是 3 倍初始预算（72 万），见 agent_run.go 的 chatBudgetHard。
	//
	// 为什么不直接用 agentScope 的 overBudget：那是 run 级预算，专门给派活用。
	// 主对话自己的用量累加进去之后，spawn_agent 会在一进去就被拒——那等于把
	// 长任务里的编排能力废掉。
	aiChatTurnBudgetFactor = 2
)

// aiChatTurnBudget 一次提问在主循环里的 token 上限（0 = 不限，配置不完整时）。
func aiChatTurnBudget(cfg AIProfile) int {
	b := aiContextBudget(cfg)
	if b <= 0 {
		return 0
	}
	return b * aiChatTurnBudgetFactor
}

// aiToolCall 一次工具调用。
//
// Args 保留模型给出的 JSON 字符串原文：解析失败时要原样回给模型看
// （它会自己改），而不是丢一个「参数非法」让它无从下手。
type aiToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// aiToolRecord 一条落库/展示用的调用记录。
// 与 aiToolCall 分开：Args 在记录里是格式化后的样子，Result 是给用户看的摘要，
// 两者混用会让「回传给模型的内容」和「界面上展示的内容」互相污染。
type aiToolRecord struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
	Ms     int64  `json:"ms"`
	Round  int    `json:"round"`
	// Dedup 这次调用与之前某次完全相同，被跳过没有真执行。
	// 单独标记而不是只写在 Result 里：界面上「0ms 成功」和「压根没跑」是两件事，
	// 用户看到两张一模一样的卡片会以为工具跑了两遍。
	Dedup bool `json:"dedup,omitempty"`
	// Denied 这次调用被用户拒绝（审批闸门），没有执行。
	//
	// 与 Dedup 分开而不是合并成一个「没跑」标志：两者在界面上要显示不同的话
	// ——「同样的事刚做过，已跳过」和「你拒绝了这次操作」。合成一个，用户会以为
	// 工具执行失败了。
	Denied bool `json:"denied,omitempty"`
	// Added / Removed 这次调用改动的行数（file_edit 有值，其它工具为 0）。
	//
	// 单独成字段而不是让前端去结果文本里抠：结果是一段给模型看的 JSON/文本，
	// 格式会随工具演进；而「改了几行」是卡片上最该一眼看到的信息，
	// 放在摘要行右侧比埋在展开内容里有用得多。
	Added   int `json:"added,omitempty"`
	Removed int `json:"removed,omitempty"`
	// Diff 这次改动的行级 diff（只有 file_edit / file_write 有）。
	//
	// 与 Added/Removed 并存而不是替代：数字用来在摘要行上一眼看到「改了多少」，
	// diff 用来展开看「改成了什么」。只有数字时用户只知道「动了两行」，
	// 那正是「不敢让它改代码」的原因。
	Diff string `json:"diff,omitempty"`
	// Path 这次调用触及的文件（file_write / file_edit 有值）。
	//
	// 单独成字段而不是让前端从结果 JSON 里抠：结果可能被截断，截在半截的 JSON
	// 前端只能整块放弃；而「这条回复产出了哪些文件、点开预览」靠它聚合。
	// 落库随 tool_calls 一起走，刷新后产物条还在。
	Path string `json:"path,omitempty"`
	// RunID / NodeID 这次调用所属的编排 run 与节点（只有 spawn_agent 这类
	// 会派活的工具才有）。落库而不是让前端解析结果文本：结果可能被截断，
	// 一旦截在中间就不是合法 JSON，前端只能整块放弃。
	RunID  string `json:"run_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	// Output 这次调用「人看的」输出（目前只有 run_command 有）。
	//
	// 与 Result 并存而不是替代：Result 是回给模型的 JSON（要结构化、
	// 要被工具自己解析），Output 是终端里的那几行文字。卡片上直接铺 Result
	// 就是一团转义字符——换行成了 \n、引号成了 \"，一截断更只剩半截 JSON，
	// 用户既看不到命令输出，也解析不了那段文本。
	Output string `json:"output,omitempty"`
}

// agentLinked 由「会建立编排节点」的工具结果实现，告诉调度处这次调用属于哪个 run/node。
//
// 用接口而不是在 ai_tools.go 里认具体类型：认类型意味着每加一个编排类工具就要回来
// 改一次调度代码，而调度代码是整条链上最不该频繁动的地方。
type agentLinked interface {
	AgentLink() (runID, nodeID string)
}

// ToolFilter 编排层的角色收窄（子 agent 用）。
//
// 场景（Scene）回答「这条对话准用多大能力」，角色回答「这个无人值守的执行者
// 准用多大能力」。子 agent 的边界必须由能力决定（拿不到 file_write），
// 不能靠提示词说「别改」。
//
// MaxLevel 用 -1 表示不限：LevelRead 是 0，用 0 当「不限」会把 explore
// 的只读约束悄悄变成一个不生效的开关。
type ToolFilter struct {
	// MaxLevel 本次允许的最高危险等级。**零值不是「不限」，而是 LevelRead（0）**：
	// 只留只读工具。想要「不收窄」就传 nil 的 *ToolFilter（keep 里 f == nil 直接放行）。
	//
	// 为什么要写这一条：`&ToolFilter{}` 看起来很自然是「空过滤器 = 不过滤」，
	// 实际会把 file_write / run_command / spawn_agent 全撤掉——症状是
	// 「模型说它没有写权限」，而装配代码一行都没错。
	// 负数表示跳过等级判定。
	MaxLevel   int
	Categories map[string]bool
	// NoSpawn 撤下 spawn_agent（子 agent 不能再派活）。
	NoSpawn bool
	// AllowNames 只留这些工具（WP10：技能的 allowed-tools 声明）。
	//
	// nil = 不按名字收窄（默认）。非 nil 时**只收不放**：它只能在上面的等级/
	// 域过滤之后再筛掉，永远加不回被策略撤掉的工具——否则一个技能文件就能
	// 把用户关掉的危险工具重新打开。
	AllowNames map[string]bool
	// Exclude 按名字逐个撤下（先于 AllowNames 生效）。
	//
	// 与 MaxLevel 分工：等级挡的是「写」，挡不住那些**长得像只读、实际会写**的
	// 工具（project_memory 的 action=write、plan_update 的整份覆盖）。角色收窄
	// 只放宽域、不放宽等级之后，靠这一条把这些漏网的逐个摘掉。
	Exclude map[string]bool
}

// keep 把角色约束翻译成 mcpsrv.Resolve 的 Keep 回调。nil 表示不收窄。
func (f *ToolFilter) keep(t mcpsrv.Tool) bool {
	if f == nil {
		return true
	}
	// spawn_agent 的撤下要单独判：它按域属于 code，靠 Categories 拦不住
	// （code 角色本来就该有 code 域的工具）。
	if f.NoSpawn && t.Name == spawnAgentToolName {
		return false
	}
	if f.MaxLevel >= 0 && t.Level > f.MaxLevel {
		return false
	}
	if f.Exclude != nil && f.Exclude[t.Name] {
		return false
	}
	if f.Categories != nil && !f.Categories[t.Domain] {
		return false
	}
	// 技能声明（WP10）放在最后：先让策略与场景把不该有的撤掉，再看技能
	// 想留下的那批里还剩什么。顺序反过来会出现「技能声明了 run_command，
	// 于是它绕过了只读档」。
	if f.AllowNames != nil && !f.AllowNames[t.Name] {
		return false
	}
	return true
}

// ---- 工具面随 ctx 走 ----
//
// 装配一次、执行时复用同一个 Surface：模型看到的工具面与「能调什么」必须是
// 同一份对象，否则又会出现「面板说开着、调用说没资格」。挂在 ctx 上而不是
// 塞进 AIService 字段：服务是单例，同时可能有三条对话在跑。

type toolSurfaceCtxKey struct{}

func withToolSurface(ctx context.Context, sf mcpsrv.Surface) context.Context {
	return context.WithValue(ctx, toolSurfaceCtxKey{}, sf)
}

func toolSurfaceFrom(ctx context.Context) (mcpsrv.Surface, bool) {
	sf, ok := ctx.Value(toolSurfaceCtxKey{}).(mcpsrv.Surface)
	return sf, ok
}

// extToolGroup 一个外部 server 的工具集合，用于按整组判断名额与体积。
type extToolGroup struct {
	server string
	items  []map[string]any
	// sizes 与 items 一一对应：单条的实际字节数，整组超限时按它逐条装。
	sizes []int
	bytes int
}

// firstSystem 取出 system 正文（不含任何后贴的提示段）。
func firstSystem(messages []map[string]interface{}) string {
	if len(messages) == 0 || messages[0]["role"] != "system" {
		return ""
	}
	s, _ := messages[0]["content"].(string)
	return s
}

// applySystemHints 覆盖式重建 system：原文 + 工具说明 + 工作目录说明。
//
// 为什么是覆盖而不是像以前那样往后追加：轮次用尽后调用方会撤下 tools，
// 而 system 里那段「你可以调用 file_read」如果不能一起撤掉，模型就会把这次的
// 调用输出成正文（DeepSeek 的 DSML 标记），用户看到的是半句话 + 一堆原始标记。
// 只有每轮按「当前到底有没有工具」重建，才能把已经说过的话收回来。
//
// base 必须是没贴过任何提示的原文，由调用方在首次组装后取一次并存着
// （见 ai_stream.go）。重复传已贴过的内容会导致提示段层层叠加。
func applySystemHints(messages []map[string]interface{}, base, hint, cwd string, withTools, exhausted bool) {
	if len(messages) == 0 || messages[0]["role"] != "system" {
		return
	}
	s := base
	switch {
	case withTools:
		s += hint
	case exhausted:
		// 本来有工具、这一轮却被撤下了：只能是没有轮次了（见 ai_stream.go 的收尾轮）。
		s += toolHintExhausted
	default:
		// 本次从始至终没有工具（定时任务的问答档 / 权限过滤后一个都不剩）。
		// 不说明的话模型会照旧发起调用，而调用会被静默忽略，整轮变成空回复。
		s += toolHintNoTools
	}
	s += cwdHint(cwd)
	messages[0]["content"] = s
}

// planHint 任务清单工具的用法提示（见 aitools 的 plan.go）。
//
// 写在提示词里而不是只靠工具描述：工具描述是「这个工具能干什么」，
// 而「什么时候必须用它」是行为约束，模型对工具描述的遵循度明显低于系统提示。
// 只在 plan_update 真的挂给模型时才追加（见 agent_run.go）。
const planHint = "\n\n[任务清单]\n" +
	"- 三步以上才能做完的任务，动手前先用 plan_update 列一份步骤清单（全部 todo，当前那条 doing），每完成一步立刻更新并把那条标成 done。\n" +
	"- 清单会实时显示在用户界面上，必须与实际进度一致：没做的事不要标 done，也不要用正文里的 `- [ ]` 代替它。\n" +
	"- **收尾必须再更一次**：最后一步做完、开始写最终回答**之前**，再调一次 plan_update 把做完的标 done；" +
	"确实没做/跳过/失败的条目留在 todo，并在正文里说一句为什么。清单会一直挂在答复下面，" +
	"停在 doing 会让人以为任务还在跑（实测就这么停在 1/3 过）。\n" +
	"- 更新清单后不要在正文里复述整份清单；哪些条目自己推进、哪些派出去，按下面的 [任务规模] 判断。" +
	"两步以内的事不用清单。\n"

// hasToolPayload 判断本次挂给模型的工具里有没有某个名字。
//
// 只看名字：调用方用它决定「要不要在 system 里提这个工具」，与参数无关。
func hasToolPayload(tools []map[string]any, name string) bool {
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			continue
		}
		if s, _ := fn["name"].(string); s == name {
			return true
		}
	}
	return false
}

// cwdHint 告诉模型本次对话绑定了哪个工作目录。
//
// 不说清楚会有三种坏结果：拿相对路径去猜、写文件时把绝对路径猜错、
// 以及写操作被拒绝后不知道为什么被拒而反复重试同一个调用。
// 没绑定时也要说一句——「写不了」是有原因的，模型该知道原因是「用户还没选目录」，
// 这样它会去请求用户选，而不是换个路径再试一次。
//
// 顺带写清 run_command 的 shell：不写的话模型会先花好几轮跑 `where powershell`、
// `$PSVersionTable`、`echo x > probe.txt` 去「探测环境」，而它其实只要用 file_write
// 就能把文件写出来。实测这种探测能吃掉一半的轮次预算。
func cwdHint(cwd string) string {
	var hint string
	if strings.TrimSpace(cwd) == "" {
		// 走到这里说明目录既没绑上也没建出来（任务目录创建失败，或空间目录已被删掉）。
		// 正常情况每个任务都自带目录，所以不要让用户去界面上找「选目录」——那个入口已经没有了。
		hint = "\n\n[工作目录]\n本次对话**没有可用的工作目录**：读写文件必须用绝对路径，" +
			"写操作（file_write / file_edit / run_command）会被直接拒绝。请告诉用户把任务建在有效的空间里" +
			"（或重开一个任务），不要自己猜路径反复试。\n"
	} else {
		hint = "\n\n[工作目录]\n本次对话绑定的工作目录是：" + cwd + "\n" +
			"相对路径都按它解析；dir_list / file_read / file_write / file_edit / run_command 不传路径时默认就是这里。" +
			"要新建文件时默认建在这个目录里（不要散落到别处），所有落盘改动都必须落在这个目录内，其它位置会被拒绝。\n"
		// 默认边界必须提前说清：否则模型看到报错里的外部路径会直接去读，
		// 撞一次确认卡（或一次拒绝）才回头，白烧一轮。
		hint += "**默认只在这个目录内操作**：读目录之外的路径会先请用户点确认（同意只对那一次生效），" +
			"写操作直接拒绝。如果用户发来的报错或线索指向另一个项目，不要自己改路径反复试——" +
			"要么说明「需要你同意读取 <路径>」，要么让用户把那个目录设为工作目录。\n"
	}
	if runtime.GOOS == "windows" {
		hint += "run_command 由 **cmd.exe** 执行（不是 PowerShell）：命令按 cmd 语法写，" +
			"不要为了确认 shell 或找 node / powershell 先跑探测命令——写文件用 file_write 就够了。\n"
	}
	return hint
}

// pathReadTools 带 path 参数、且只读的那批工具。
//
// 越界读取的确认只对这批成立：写类本来就走审批闸门（并且写白名单另有约束），
// 在这里再判一次等于同一个动作问两遍；不带 path 的工具默认落在工作目录，
// 没有越界的可能。
var pathReadTools = map[string]bool{
	"file_read":     true,
	"file_grep":     true,
	"file_find":     true,
	"dir_list":      true,
	"git_status":    true,
	"git_diff":      true,
	"check_workdir": true,
}

// skillReadPath 判断本次读工具的目标是否落在 skills 根目录下，是则返回清理后的绝对路径。
//
// 技能由宿主安装到数据目录的 skills/ 下，提示词明确要求模型「先用 file_read 读
// <路径> 再照做」（见 skills.go 的 renderSkillsPrompt）。它因此天然不在会话工作目录
// ai_tasks/<id> 内，但属于宿主自己安装的可信内容：既不该弹「越界读取」确认卡，
// 也不该被 scoped 读取拒绝——技能本来就是让 AI 读的。
//
// 判定复用 fsguard.Outside，与执行层 ResolveReadScopedExtra 是同一套 clean/within，
// 保证「跳过弹卡」和「执行层放行」口径一致；否则会出现不弹卡却静默读取失败的错配。
// 只认绝对路径：相对路径在工具执行时按 scope 解析，拿 skills 根当基准会误判。
func skillReadPath(args map[string]any) string {
	p := mcpsrv.Arg(args, "path")
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	root := skillsRoot()
	if root == "" {
		return ""
	}
	if _, outside := fsguard.Outside(p, root); outside {
		return ""
	}
	return filepath.Clean(p)
}

// parallelSafeTools 允许在同一轮里并行执行的工具白名单。
//
// 用白名单常量而不是给 Tool 加一个「我可以并发」的字段：后者会让每个工具
// 各自声明能力，而「能不能并发」取决于这一轮里还跑着什么——run_command
// 读文件时是安全的，和 file_edit 同轮就不安全。这种判断只有调度处（这里）
// 知道，让工具自己声明迟早会出现「两个都标了可并发、结果互相踩」。
//
// 每加一个名字前必须回答两个问题，答不上来就别加：
//  1. 它有没有**写盘 / 写库 / 改共享状态**？LevelRead 不代表只读（见 parallelUnsafeReadTools）。
//  2. 它和同轮其它调用之间有没有真实依赖？有依赖的并发就是随机结果。
var parallelSafeTools = map[string]bool{
	// 派活天然各干各的：子 agent 之间不共享上下文，同目录写操作由
	// agent_spawn.go 的目录闸串行。
	spawnAgentToolName: true,

	// —— 只读检索：只打开文件/索引读，不写盘、不改库、无包级缓存 ——
	"file_read":     true, // 单文件读
	"file_grep":     true, // 目录内正则搜索（有命中数/时间预算）
	"file_find":     true, // 目录内按文件名搜（有深度/条目数/时间预算）
	"file_search":   true, // Everything 按文件名检索，只读索引
	"dir_list":      true, // 目录列举
	"git_status":    true, // git status --porcelain，只读
	"git_diff":      true, // git diff，只读（无临时文件）
	"check_workdir": true, // stat 一下目录，纯探测

	// —— 网络只读：只发请求，不落盘、不共享缓存 ——
	"web_fetch": true,
	"web_head":  true,
	"web_links": true,

	// —— 后台任务查询：打开日志文件读头/尾，不消费、不推进任何游标 ——
	"job_status": true,
	"job_output": true,
}

// parallelUnsafeReadTools 名字/等级像只读、**实际不能并发**的工具，以及原因。
//
// 这张表的存在就是为了下面这句话：**LevelRead 不等于无副作用**。
// 没有它，下一个人按「LevelRead 就能加进白名单」办事，会静默踩到这些：
//   - project_memory 的 action=write 真的 MkdirAll + WriteFile 写 MEMORY.md，
//     两个并发写同一份文件就是后写者覆盖前写者，而它长得完全像个读工具；
//   - plan_update 是「整份覆盖」语义（见 tools/plan.go），并发时后写者赢，
//     用户看到的清单会随机少几步。
//
// 这张表不参与调度，它只被测试用来自证「排除是刻意的，不是漏了」。
var parallelUnsafeReadTools = map[string]string{
	"project_memory": "action=write 会 MkdirAll + 写 MEMORY.md，并发写互相覆盖",
	"plan_update":    "整份覆盖会话清单，并发时后写者赢，清单会错乱",
}

// canRunParallel 判断这一轮要不要并发执行。
//
// 条件是「每一个调用都可并发」而不是「有可并发的就并发」：一轮里
// [file_edit, spawn, file_edit] 混着时，spawn 与 file_edit 之间存在真实
// 依赖（子 agent 可能在读父 agent 正要改的文件），并发就是制造随机结果。
// 混轮一律退回串行——顺序确定，代价只是慢一点。
//
// 另外要求「参数两两不同」：去重（seen）是在 execOneToolCall 里做的，
// 并发时两个一模一样的 spawn 会同时通过检查、双双真的跑起来——
// 那正是去重要防的事（同一次派活付两遍钱）。带重样的轮次退回串行，
// 第二个就会命中 seen 直接返回上次结果。
func canRunParallel(calls []aiToolCall) bool {
	if len(calls) < 2 {
		return false
	}
	seenKeys := make(map[string]bool, len(calls))
	for _, c := range calls {
		if !parallelSafeTools[c.Name] {
			return false
		}
		// 排除表在调度处也生效，不只是文档：白名单是人工审计的，会漂；
		// 哪天有人把 project_memory 加进白名单（它确实是 LevelRead，很容易被
		// 当成只读），这里立刻把整轮退回串行，而不是让两份 MEMORY.md 互相覆盖。
		if why, unsafe := parallelUnsafeReadTools[c.Name]; unsafe {
			_ = why
			return false
		}
		// 解析后再比：toolCallKey 在 args 为空时会统一归一成 "{}"，
		// 直接传空 map 会让所有同名调用都被判成重复。
		args, err := parseToolArgs(c.Args)
		if err != nil {
			// 参数写坏了：这条终究会在执行时报错，不差这一次串行。
			return false
		}
		k := toolCallKey(c.Name, args, c.Args)
		if seenKeys[k] {
			return false
		}
		seenKeys[k] = true
	}
	return true
}

// parallelGroups 把一轮工具切成「连续可并发段」，返回每段在 calls 里的下标。
//
// 为什么不是 canRunParallel 那种「整轮全安全才并发」：一轮里 [spawn, spawn,
// spawn, run_command, run_command] 是最常见的形态——模型派完活顺手跑几条校验。
// 只要混进一条 run_command，整轮就退回串行，三个子 agent 一个接一个跑，
// 用户看到的就是「派了三个子任务，比一个主任务还慢」（实机 2026-11-18）。
//
// 切段后段内并发、段间严格保序：spawn 之间仍可同时跑，命令仍在它们之后执行；
// 而 [file_edit, spawn] 这类有依赖的混轮依然是两段串行，不会制造随机结果。
// 同段内重复调用（判重键相同）也单独成段，让 execOneToolCall 的 seen 去重照常生效。
func parallelGroups(calls []aiToolCall) [][]int {
	groups := make([][]int, 0, len(calls))
	var cur []int
	flush := func() {
		if len(cur) > 0 {
			groups = append(groups, cur)
			cur = nil
		}
	}
	seen := make(map[string]bool, len(calls))
	for i, c := range calls {
		key := ""
		ok := parallelSafeTools[c.Name]
		if ok {
			_, unsafe := parallelUnsafeReadTools[c.Name]
			ok = !unsafe
		}
		if ok {
			var args map[string]any
			args, err := parseToolArgs(c.Args)
			if err != nil {
				ok = false
			} else {
				key = toolCallKey(c.Name, args, c.Args)
				if seen[key] {
					ok = false
				}
			}
		}
		if !ok {
			flush()
			// 段被打断后判重集合也重置：下一段从空状态重新判断重复。
			seen = make(map[string]bool, len(calls))
		}
		if ok {
			seen[key] = true
			cur = append(cur, i)
		} else {
			groups = append(groups, []int{i})
		}
	}
	flush()
	return groups
}

// wireToolArguments 把模型给出的参数串整理成上游一定接受的合法 JSON。
//
// 流式输出常在 tool_calls.arguments 上被截断（半截 JSON），模型也偶尔会吐一段
// 解释文字当参数。本地执行层本来就容忍这种坏参数：prepareToolCall 把解析错误
// 当成工具结果回给模型，让它自己改。但回传上游时 assistant.tool_calls[].function.
// arguments 必须是合法 JSON，原样回传半截串会让整轮请求直接 400
// "Assistant tool call xxx.arguments must be valid JSON."——本地明明处理好了，
// 却死在下一发请求上；而报错里的工具名还常被网关打码成 ***，无从定位。
//
// 归一成 `{}`（"这次没拿到可用参数"）：模型会从紧随其后的 tool 消息里读到
// 真实原因（原始串已被回显）并重试，不损失自我纠正的机会。
func wireToolArguments(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(s), &args); err == nil && args != nil {
		return raw // 合法就原样回传，含前后空白也不影响上游校验
	}
	return "{}"
}

// runToolCalls 执行一轮里的全部工具调用，返回要追加到 messages 的消息与记录。
//
// assistantText 是这一轮模型在调工具之前说的话（「我先查一下环境」）。
// 不能丢：它属于那条 assistant 消息的 content，缺了会让后续轮次的上下文里
// 出现「悬空」的工具调用（模型看到自己发了 tool_calls 却没说要做什么）。
//
// 顺序约定（OpenAI 协议硬要求）：先一条带 tool_calls 的 assistant 消息，
// 紧接着每个调用一条 role=tool 的结果消息，顺序与 tool_calls 一致。
// 少一条或顺序错了，上游会直接 400，而且报错信息通常只说
// 「messages with role 'tool' must be a response to a preceding message with 'tool_calls'」。
//
// 单个工具失败不中断整轮：把错误文本当结果回给模型，它通常会换参数重试
// 或改用别的工具。只有 ctx 被取消（用户点了停止）才提前退出。
//
// seen 是「本次提问内已经成功执行过的调用」（key 见 toolCallKey），由调用方在
// 一次提问开始时建好、跨轮复用：只在一次提问内有效，新提问重新建空的——
// 用户说「再发一次」时那是新的意图，不该被上一次的记录挡掉。
//
// 并发只改「怎么跑」，不改「怎么拼」：结果按 calls 的下标回填，拼消息时
// 一律按 calls 顺序取——按完成先后拼会让 tool 消息顺序随机，上游直接 400。
func (a *AIService) runToolCalls(ctx context.Context, calls []aiToolCall, round int, assistantText string, seen map[string]aiToolRecord, cwd string) ([]map[string]interface{}, []aiToolRecord) {
	// assistant 侧：把模型给出的 tool_calls 原样回传。
	// content 用空串而不是 nil：个别网关会把 null 当成「这不是一条合法消息」。
	wireCalls := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		// 模型写坏的 arguments 不能原样回传：上游会校验 JSON 合法性，
		// 半截串会让整轮请求 400（见 wireToolArguments）。本地仍按原始串
		// 执行、把错误回给模型；这里只收拾"回传上游那一刻"的表示。
		wireArgs := wireToolArguments(c.Args)
		if wireArgs != c.Args {
			logger.W("[ai] 工具参数不是合法 JSON，回传前已置空 id=%s name=%s raw=%s",
				c.ID, c.Name, logger.Trunc(c.Args, 160))
		}
		wireCalls = append(wireCalls, map[string]any{
			"id":   c.ID,
			"type": "function",
			"function": map[string]any{
				"name":      c.Name,
				"arguments": wireArgs,
			},
		})
	}
	out := []map[string]interface{}{{
		"role":       "assistant",
		"content":    assistantText,
		"tool_calls": wireCalls,
	}}

	records := make([]aiToolRecord, len(calls))
	contents := make([]string, len(calls))

	// seen 会被同一轮里的并发调用同时读写：加锁而不是给每个 goroutine 一份副本，
	// 因为去重的语义是「全轮共享」——两次同样的派活，第二次就该被跳过。
	var seenMu sync.Mutex
	exec := func(i int) {
		records[i], contents[i] = a.execOneToolCall(ctx, calls[i], round, seen, &seenMu, cwd)
	}

	// 段内并发、段间保序（见 parallelGroups）：一条 run_command 不该把
	// 同一轮里三条派活全拖成串行；但写工具后面的派活仍要等它做完。
	for _, group := range parallelGroups(calls) {
		if len(group) < 2 {
			// 单个调用也要起心跳：只派一个子 agent 是最常见的用法，而它可能要跑
			// 十几分钟——那段时间界面上同样只有「执行中」三个字。
			beat := startSpawnHeartbeat(ctx, calls, group)
			exec(group[0])
			beat.stop()
			continue
		}
		sem := make(chan struct{}, agentMaxParallel)
		var wg sync.WaitGroup
		// 派活组的心跳：wg.Wait() 期间主 agent 一个字都不出（它在等结论），
		// 而这一等可能是几分钟。前端只能显示「调用中」，用户看到的是「卡住了」。
		// 这里每 8 秒推一条「已完成 2/5 · 已等 1 分 20 秒」，让等待有进度。
		beat := startSpawnHeartbeat(ctx, calls, group)
		for _, idx := range group {
			i := idx
			wg.Add(1)
			go func() {
				defer wg.Done()
				// 并发路径里 panic 必须自己兜：主 goroutine 的 recover 管不到它，
				// 一旦漏出去整个进程就没了。
				defer recoverPanic("ai tool " + calls[i].Name)
				sem <- struct{}{}
				defer func() { <-sem }()
				exec(i)
				beat.doneOne(calls[i].Name)
			}()
		}
		wg.Wait()
		beat.stop()
	}

	for i := range calls {
		out = append(out, toolMessage(calls[i].ID, contents[i]))
	}
	return out, records
}

// closedToolMessages 收尾轮「工具已收回、调用不执行」的回执。
//
// 为什么不能像以前那样把 tools 撤掉：撤掉之后这一发请求的 tools 数组与 system
// 都和上一发不同，上游的前缀缓存整段作废——而收尾轮往往正是 prompt 最大的一发
// （全量重算一次 = 前面十几轮攒下的缓存全部白省）。现在工具仍然挂在工具面上，
// 只是不执行。
//
// 协议上必须把 assistant.tool_calls 与对应的 tool 消息补齐：只给 assistant 不带
// tool 结果，部分网关会直接 400（tool_calls must be followed by tool messages）。
func closedToolMessages(calls []aiToolCall, assistantText string) []map[string]interface{} {
	wireCalls := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		wireCalls = append(wireCalls, map[string]any{
			"id":   c.ID,
			"type": "function",
			"function": map[string]any{
				"name":      c.Name,
				"arguments": wireToolArguments(c.Args),
			},
		})
	}
	out := make([]map[string]interface{}, 0, len(calls)+1)
	out = append(out, map[string]interface{}{
		"role":       "assistant",
		"content":    assistantText,
		"tool_calls": wireCalls,
	})
	for _, c := range calls {
		out = append(out, toolMessage(c.ID, "（系统提示）本次提问已进入收尾：工具调用不会被执行。"+
			"不要继续调用工具，请直接用文字给出结论；没做完的部分说明一声。"))
	}
	return out
}

// spawnHeartbeatInterval 派活组心跳间隔。
//
// 8 秒：子 agent 一轮通常十几秒，更密没有新信息；更疏则「刚派出去」那半分钟
// 看起来仍然像卡死。只在该组含 spawn_agent 时才起，其它并发组不受影响。
const spawnHeartbeatInterval = 8 * time.Second

// spawnHeartbeat 派活等待期的进度心跳。
//
// 解决的问题：runToolCalls 在这一组上是 wg.Wait()，主 agent 既不出字也不推任何
// 事件——用户看到的只有「正在调用 spawn_agent」。真实情况可能是「6 个里完成 2 个」，
// 但这个事实此前完全没有出口（子 agent 的事件写进的是 agent_events 表）。
type spawnHeartbeat struct {
	sink   EventSink
	total  int
	done   atomic.Int32
	start  time.Time
	stopCh chan struct{}
	once   sync.Once
}

// startSpawnHeartbeat 起心跳；该组不含派活（或不在流式执行里）时返回空对象，
// 下面三个方法都做了 nil 安全，调用处不必判空。
func startSpawnHeartbeat(ctx context.Context, calls []aiToolCall, group []int) *spawnHeartbeat {
	h := &spawnHeartbeat{}
	sink := eventSinkFrom(ctx)
	if sink == nil {
		return h
	}
	for _, i := range group {
		if calls[i].Name == spawnAgentToolName {
			h.total++
		}
	}
	if h.total == 0 {
		return h
	}
	h.sink = sink
	h.start = time.Now()
	h.stopCh = make(chan struct{})
	go func() {
		t := time.NewTicker(spawnHeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-h.stopCh:
				return
			case <-t.C:
				h.emit()
			}
		}
	}()
	return h
}

// emit 推一条汇总帧。并发安全：sseFlusher.writeEvent 自带锁。
func (h *spawnHeartbeat) emit() {
	if h == nil || h.sink == nil {
		return
	}
	h.sink.Event("spawn_progress", map[string]any{
		"done":      int(h.done.Load()),
		"total":     h.total,
		"elapsedMs": time.Since(h.start).Milliseconds(),
	})
}

// doneOne 某个调用跑完（成功失败都算，用户关心的是「还剩几个」）。
func (h *spawnHeartbeat) doneOne(name string) {
	if h == nil || h.sink == nil || name != spawnAgentToolName {
		return
	}
	h.done.Add(1)
	h.emit()
}

// stop 收尾：停 ticker。用 sync.Once 是因为 defer 与显式调用可能各来一次。
func (h *spawnHeartbeat) stop() {
	if h == nil || h.stopCh == nil {
		return
	}
	h.once.Do(func() { close(h.stopCh) })
}

// toolCallPrep 一次调用在执行前的准备结果（WP13：把准备阶段从主流程里拆出来）。
//
// 拆出来的理由不是为了整洁：这一段里有四条「必须先做」的事（解析参数、
// 清掉模型伪造的宿主参数、注入真实 cwd/scope、给技能目录开读白名单），
// 顺序错了就是安全洞（模型编一个 _cwd 就能多出一个写根）。放成独立函数后，
// 它有了自己的名字与测试入口，而不必读完整条 200 行的主流程才能确认顺序。
type toolCallPrep struct {
	// Args 注入宿主参数之后的参数表（工具真正拿到的就是它）。
	Args map[string]any
	// ArgsText 卡片上显示的参数摘要。
	ArgsText string
	// SkillRead 这次读的是技能目录：不需要越界确认（内容由提示词点名要求）。
	SkillRead bool
	// Err 参数解析失败；此时 Args 不可用。
	Err error
	// Raw 模型给的原始参数串（解析失败时回显给模型）。
	Raw string
}

// prepareToolCall 解析参数并注入宿主侧参数。
func prepareToolCall(ctx context.Context, c aiToolCall, cwd string) toolCallPrep {
	prep := toolCallPrep{Raw: c.Args}
	args, aerr := parseToolArgs(c.Args)
	prep.Args = args
	prep.ArgsText = formatToolArgs(args, c.Args)
	if aerr != nil {
		prep.Err = aerr
		return prep
	}

	// 工作目录与读取范围都由宿主注入，不信任模型传的值：这些键不在任何工具的
	// schema 里，模型既看不见也无从伪造。这里无条件先删再按实际情况注入——
	// 旧写法只在 cwd 非空时覆盖，cwd 为空时模型自己编一个 _cwd 就能让写白名单
	// 凭空多出一个根目录。
	//
	// _scope 默认就是 cwd：**默认只能操作当前目录**，包括系统给「没选目录」的对话
	// 分配的 ai_tasks/<id>（那也确实是它当前唯一的目录）。想读别处只有两条路——
	// 用户把它设为工作目录，或在下面对这次越界读取当场点允许。
	delete(args, "_cwd")
	delete(args, "_scope")
	delete(args, "_allow")
	if cwd != "" {
		args["_cwd"] = cwd
	}
	// 范围跟编排的根走（见 agentScope.scope）：子 agent 被派到子目录时，
	// 「这次对话属于哪个项目」没有变，它仍然要能读项目根下的文件。
	scope := cwd
	if sc := agentScopeFrom(ctx); sc != nil && sc.scope != "" {
		scope = sc.scope
	}
	if scope != "" {
		args["_scope"] = scope
	}

	// 读取 skill 目录不需要询问：技能是宿主安装的可信内容，提示词要求模型先
	// file_read 它的 SKILL.md，而它位于数据目录、几乎总在会话工作目录之外。
	// 把 skills 根注入 _allow，让执行层的 scoped 读取放行（与下面跳过越界确认卡
	// 成对，缺一就会变成「不弹卡但静默读取失败」）。只认绝对路径，且只对读类
	// 工具生效；模型伪造的 _allow 已在上面 delete，这里重新按真实目标决定。
	prep.SkillRead = pathReadTools[c.Name] && skillReadPath(args) != ""
	if prep.SkillRead {
		args["_allow"] = []string{skillsRoot()}
	}
	return prep
}

// execOneToolCall 执行单个工具调用，返回记录与要回给模型的文本。
//
// 抽出来是为了让「串行一轮」与「并发一轮」走完全相同的逻辑：两份实现迟早
// 会在去重、超时、错误整形上分叉，而分叉的那一半是跑得少的那一半。
//
// WP13：这条主流程现在是**有名字的六个阶段**，顺序不可调换：
//  1. 停止判定（ctx 已取消 → 不执行，但仍要回一条 tool 消息）；
//  2. 参数准备（prepareToolCall：解析 + 宿主参数注入）；
//  3. 去重（seen，命中就把上次结果递回）；
//  4. 越界判定（只读工具读目录外时要求点名同意）；
//  5. 审批 + 执行（toolApprover + callToolWithTimeout）；
//  6. 结果整形（shapeToolResult：diff 摘除、截断、产物路径、编排链接）。
func (a *AIService) execOneToolCall(ctx context.Context, c aiToolCall, round int, seen map[string]aiToolRecord, seenMu *sync.Mutex, cwd string) (aiToolRecord, string) {
	rec := aiToolRecord{ID: c.ID, Name: c.Name, Round: round}

	// 阶段 1：用户点了停止。
	//
	// 把剩余调用标记为未执行照常回给模型（协议要求每条 tool_call 都有对应的
	// tool 消息），但不真的去跑。
	if err := ctx.Err(); err != nil {
		rec.Error = "用户已停止，未执行"
		return rec, rec.Error
	}

	// 本次调用的 id 挂到 ctx：写盘前的快照要能与「哪一步改的」对上。
	// 在参数解析之前就设置——参数写坏也要能对应到卡片上的那条记录。
	ctx = aitools.WithToolCallID(ctx, c.ID)

	// 阶段 2：参数准备。
	prep := prepareToolCall(ctx, c, cwd)
	rec.Args = prep.ArgsText
	if prep.Err != nil {
		// 参数不是合法 JSON：原样回给模型，它看得懂自己写坏了什么。
		rec.Error = prep.Err.Error()
		return rec, "参数解析失败：" + rec.Error + "\n原始参数：" + prep.Raw
	}
	args := prep.Args
	skillRead := prep.SkillRead

	// 同一次提问内，同一个工具 + 完全相同的参数只真执行一次。
	//
	// 依据是实测：模型会在同一轮里给出两条一模一样的 robot_notify，
	// 也会在下一轮「再确认一次」。这类重复几乎从不是用户意图，而副作用类
	// 工具（发通知、写文件、停服务）重放一遍就是实打实的第二次副作用——
	// 提示词能减少它，兜底必须在这里，因为模型不遵守规则时提示词什么也拦不住。
	//
	// 命中时不报错（那是「执行失败」的语义，卡片会变红），而是把上次结果原样递回：
	// 模型要的信息它手上已经有了，用户看到的是一行「已跳过」。
	// 失败过的调用不缓存：模型拿同样参数重试一次是合理的（网络抖动、临时占用）。
	key := toolCallKey(c.Name, args, c.Args)
	if seen != nil {
		seenMu.Lock()
		prev, hit := seen[key]
		seenMu.Unlock()
		if hit {
			if prev.Denied {
				// 被拒绝的调用也算「这次提问里已经问过了」：模型原样重试只会
				// 再弹一张一模一样的卡，用户还得再点一次拒绝。拒绝要是一道墙，
				// 不是一扇会自动重开的门。
				rec.Denied = true
				rec.Error = prev.Error
				rec.Result = fmt.Sprintf(
					"这个调用在第 %d 轮已被用户拒绝，本次提问内不要重试。改用别的办法，或先向用户说明为什么非它不可。",
					prev.Round)
				return rec, rec.Result
			}
			rec.Dedup = true
			rec.Result = fmt.Sprintf(
				"与第 %d 轮执行过的 %s 参数完全相同，已跳过、没有重复执行。上次的结果如下（无需再调）：\n%s",
				prev.Round, prev.Name, prev.Result)
			return rec, rec.Result
		}
	}

	// 越界读取：只读工具平时不弹卡（读个文件都要确认，三分钟内就会被关掉），
	// 但目标在当前对话目录之外时必须由用户点名同意——这就是「默认只能操作当前目录」
	// 的落地点。判定只对带 path 的读类工具做：没有 path 时默认用工作目录，不可能越界。
	// skill 目录是唯一例外：技能内容由提示词点名要求读取，是宿主自己的可信文件，
	// 不该为「读技能说明」弹确认卡；放行已在上面注入 _allow 完成。
	outsideAbs := ""
	if scope := mcpsrv.Scope(args); scope != "" && pathReadTools[c.Name] && !skillRead {
		if p := mcpsrv.Arg(args, "path"); p != "" {
			if abs, out := fsguard.Outside(p, scope); out {
				outsideAbs = abs
			}
		}
	}
	outsideNote := ""
	if outsideAbs != "" {
		outsideNote = "这次读取不在当前对话的目录内：" + outsideAbs +
			"；当前目录是 " + mcpsrv.Scope(args) +
			"。同意只对这一次生效——要长期读它，请把该目录设为工作目录"
	}

	// 审批：作为 Approver 注入统一的 Invoke 闸门链（锁 → 成员 → 审批 → Handler）。
	//
	// 位置刻意在去重与参数校验之后：
	//   - 去重命中的是「同一轮里第二次一模一样的调用」，那时工具根本没跑，
	//     问用户等于同一件事问两遍；
	//   - 参数坏了的调用执行必然报错，问也没有意义。
	//
	// 被拒绝的调用也写进 seen（见上面命中分支）：拒绝是本次提问内的一道墙。
	// seen 只活在这一次提问里，重新发问仍然会重新征求同意——所以「用户可能改主意」
	// 的机会并没有丢，丢掉的只是「同一句话问两遍」。
	// 阶段 5：审批 + 执行。
	//
	// 审批作为 Approver 注入统一的 Invoke 闸门链（锁 → 成员 → 审批 → Handler）。
	//
	// 位置刻意在去重与参数校验之后：
	//   - 去重命中的是「同一轮里第二次一模一样的调用」，那时工具根本没跑，
	//     问用户等于同一件事问两遍；
	//   - 参数坏了的调用执行必然报错，问也没有意义。
	//
	// 被拒绝的调用也写进 seen（见上面命中分支）：拒绝是本次提问内的一道墙。
	// seen 只活在这一次提问里，重新发问仍然会重新征求同意——所以「用户可能改主意」
	// 的机会并没有丢，丢掉的只是「同一句话问两遍」。
	ap := a.toolApprover(ctx, c, args, outsideAbs, outsideNote)

	// 工具面从 ctx 取：与模型看到的那份 tools[] 是同一个 Surface。
	// 取不到时用一个空面，Invoke 会回「本次未开放」而不是静默执行。
	sf, ok := toolSurfaceFrom(ctx)
	if !ok {
		sf = mcpsrv.Resolve(mcpsrv.ToolScope{Caller: mcpsrv.CallerAI, Policy: a.aiToolPolicy(), Scene: mcpsrv.SceneAssistant})
	}
	started := time.Now()
	val, cerr := callToolWithTimeout(ctx, sf, c.Name, args, ap, toolTimeoutFor(c.Name))
	rec.Ms = time.Since(started).Milliseconds()

	// 审批拒绝不是「执行失败」：卡片要变红、模型要收到「别重试同一句」的说明。
	if den, denied := mcpsrv.IsDenied(cerr); denied {
		rec.Denied = true
		rec.Error = "未获批准，未执行"
		rec.Result = aiApprovalRejectText(c.Name, den.Note)
		if seen != nil {
			seenMu.Lock()
			seen[key] = rec
			seenMu.Unlock()
		}
		return rec, rec.Result
	}

	// 阶段 6：结果整形。
	text := shapeToolResult(ctx, &rec, val, cerr)
	if seen != nil && cerr == nil {
		seenMu.Lock()
		seen[key] = rec
		// 写类成功之后，读类的缓存全部作废（见 dropStaleReads）。
		// 写类自己的条目要留着：同一句写重放一遍是第二次副作用，必须继续挡。
		if mcpsrv.ToolLevel(c.Name) > mcpsrv.LevelRead {
			dropStaleReads(seen)
		}
		seenMu.Unlock()
	}
	return rec, text
}

// toolApprover 把审批闸门包成 Approver（WP13 抽出）。
//
// 两条「无人值守」的语义必须留在这里，而不是散在调用点：
//   - 子 agent 没有交互通道（gate == nil）：写类边界的收窄靠角色过滤器，
//     所以直接放行；但**越界读取**例外——没有闸门可问时宁可拒绝，
//     也不让子 agent 自己决定跨目录读。
//   - 用户同意越界读取后，_allow 只跟着这一次调用的 args 走，执行完就没了。
func (a *AIService) toolApprover(ctx context.Context, c aiToolCall, args map[string]any, outsideAbs, outsideNote string) mcpsrv.Approver {
	gate := approvalGateFrom(ctx)
	return approverFunc(func(actx context.Context, req mcpsrv.ApprovalRequest) (bool, string) {
		// 越界读取没有闸门可问（子 agent 无人值守）：直接拒绝，不让循环自己决定跨目录。
		if outsideAbs != "" && gate == nil {
			return false, outsideAbs + " 不在当前目录内，这个执行上下文无法向用户确认权限。" +
				"请只用当前目录里的内容，或让用户把该目录设为工作目录。"
		}
		if gate == nil {
			// 子 agent 没有交互通道，写类边界的收窄靠角色（ToolFilter），不靠这里。
			return true, ""
		}
		allow, note := gate.await(actx, c.ID, c.Name, mcpsrv.ToolLevel(c.Name), args, outsideAbs != "", outsideNote)
		if allow && outsideAbs != "" {
			// 只放行这一次调用：_allow 跟着 args 走，工具执行完就没了。
			args["_allow"] = []string{filepath.Dir(outsideAbs)}
		}
		return allow, note
	})
}

// shapeToolResult 把工具返回值整理成记录 + 回给模型的文本（WP13 抽出）。
//
// 这里有个容易被当成 bug 的取舍：**diff 不进模型上下文**。模型刚写过那段内容，
// 把 diff 再塞回去纯属浪费 token（一次 file_edit 的 diff 可能几百行，
// 对它没有任何新信息），所以先摘到 rec.Diff 给卡片用，再序列化剩下的部分。
func shapeToolResult(ctx context.Context, rec *aiToolRecord, val any, cerr error) string {
	// diff 先摘出来，再序列化结果给模型。
	rec.Diff = takeToolDiff(val)

	text := mcpsrv.JSON(val)
	if cerr != nil {
		// 错误用文本回给模型而不是中断：模型据此可以换参数或换工具，
		// 这比让它把整轮回答作废有用得多。
		rec.Error = cerr.Error()
		text = "工具执行失败：" + rec.Error
		// 失败时没有结构化结果可压，交给纯文本兜底（见 truncateToolResult）。
		val = nil
	}
	rec.Result = truncateToolResult(ctx, val, text, rec.Name, rec.ID)
	rec.Output = toolOutputText(rec.Name, val)
	rec.Added, rec.Removed = toolDiffLines(val)
	// 产物路径（file_write / file_edit）：回答产物条与文件预览的依据。
	rec.Path = toolResultPath(rec.Name, val)
	// 编排类工具（spawn_agent）额外带回它建的 run/node：前端据此在这条回复下面
	// 挂出 run 树。其余工具走不到这里，字段留空、落库时被 omitempty 省掉。
	if al, ok := val.(agentLinked); ok {
		rec.RunID, rec.NodeID = al.AgentLink()
	}
	return rec.Result
}

// toolTimeoutFor 单个工具的超时上限。
//
// spawn_agent 单独放宽：它内部是一整轮 agent 循环（读文件、跑测试），
// 用 5 分钟会把它误杀在半路，而父 agent 拿到「失败」只会重新派一次。
func toolTimeoutFor(name string) time.Duration {
	if name == spawnAgentToolName {
		return agentSpawnTimeout
	}
	return aiToolTimeout
}

// callToolWithTimeout 执行一个工具，并给它套上超时。
//
// 超时用 context.WithTimeout 而不是 time.Timer：前者会把取消信号真的送到
// 实现了 HandlerCtx 的工具里——run_command 会连带子进程一起 kill、spawn_agent
// 会停掉自己的循环。后者只是「不等了」，那条 `go vet ./...` 或全盘检索还在后台
// 跑着占 CPU，用户看到的是「对话停了但电脑一直响」。
//
// 仍有一批工具只有 Handler、没有 ctx（见 mcp.Tool 的两个字段）：那类工具在超时
// 后确实会跑完，只是结果被丢弃。这是工具层的现状，不是这里能解决的——所以
// 文案里不再笼统地写「可能还在后台跑」，只在真正超时的时候这么说。
func callToolWithTimeout(ctx context.Context, sf mcpsrv.Surface, name string, args map[string]any, ap mcpsrv.Approver, timeout time.Duration) (any, error) {
	type outcome struct {
		val any
		err error
	}
	// 从父 ctx 派生而不是另起一个：父 ctx 取消（用户停止）照样能打断工具，
	// 而这一次超时只影响这一个工具，不会波及整轮。
	tctx, tcancel := context.WithTimeout(ctx, timeout)
	defer tcancel()

	ch := make(chan outcome, 1) // 缓冲 1：超时后 goroutine 仍能退出，不泄漏
	go func() {
		defer recoverPanic("ai tool " + name)
		v, e := mcpsrv.Invoke(tctx, sf, name, args, ap)
		ch <- outcome{v, e}
	}()

	select {
	case r := <-ch:
		// 工具可能先收到了取消信号、再把它包成自己的错误返回（run_command 就是
		// "signal: killed"）。那种错误直接透给模型毫无意义，按原因翻译一次。
		if r.err != nil && tctx.Err() != nil {
			if ctx.Err() != nil {
				return nil, errors.New("已停止：用户中止了这一轮任务，工具调用被中断")
			}
			return nil, fmt.Errorf("工具 %s 执行超过 %v，已取消", name, timeout)
		}
		return r.val, r.err
	case <-tctx.Done():
		// 父 ctx 取消 = 用户停止；否则是这一条工具自己的超时。两者对用户的意义
		// 完全不同，卡片文案必须分开——否则「我按了停止」会被写成「工具超时」。
		if ctx.Err() != nil {
			return nil, errors.New("已停止：用户中止了这一轮任务，工具调用被中断")
		}
		logger.W("[ai-tool] %s 执行超过 %v 未返回，已取消（支持 ctx 的工具会真的停下）", name, timeout)
		return nil, fmt.Errorf("工具 %s 执行超过 %v，已取消", name, timeout)
	}
}

// aiToolDiffMaxBytes diff 字段的上限。
//
// 20KB 大致是 300 行 diff。超过就不给逐行内容：一条 SSE 帧塞进几 MB 会让前端
// 每次渲染都卡一下，而那个体量的 diff 没有人会读——真要看整体改动，git diff 更合适。
const aiToolDiffMaxBytes = 20 << 10

// takeToolDiff 从工具结果里取出 unified diff，并把它从结果里删掉。
//
// 删除这一步是有意的：diff 只给前端看，不该跟着结果回给模型。模型刚写过这段
// 内容，把 diff 再塞回上下文纯属浪费 token，而且会把工具结果的体积放大几倍
// ——一次 file_edit 的 diff 几百行，而模型的上下文预算要留给代码本身。
func takeToolDiff(val any) string {
	m, ok := val.(map[string]any)
	if !ok {
		return ""
	}
	raw, ok := m["diff"]
	if !ok {
		return ""
	}
	delete(m, "diff")
	s, _ := raw.(string)
	if s == "" {
		return ""
	}
	if len(s) > aiToolDiffMaxBytes {
		return "改动过大，逐行 diff 已省略（上方数字是增删行数）；要看整体改动用 git_diff"
	}
	return s
}

// toolResultPath 从工具结果里取「这次触及的文件路径」。
//
// 只有 file_write / file_edit 算「产物」：dir_list / check_workdir 的结果里
// 同样有 path 键，但那只是「看了哪里」，列进产物条是误导。其余工具一律空串——
// 与 toolDiffLines 同一原则：宁可少给，不从任意 JSON 里猜。
func toolResultPath(tool string, val any) string {
	if tool != "file_write" && tool != "file_edit" {
		return ""
	}
	m, ok := val.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m["path"].(string)
	return s
}

// toolDiffLines 从工具结果里取「改动行数」（added, removed）。
//
// 只认 addedLines / removedLines 两个键，且只在会改文件的工具上生效
// （file_edit 与 file_write；后者只带 addedLines）。别的工具没有就返回 0，
// 卡片上不显示 —— 宁可少显示，也不要从任意 JSON 里猜一个数字出来当成「改了几行」。
func toolDiffLines(val any) (int, int) {
	m, ok := val.(map[string]any)
	if !ok {
		return 0, 0
	}
	num := func(k string) int {
		switch v := m[k].(type) {
		case int:
			return v
		case float64:
			return int(v)
		case json.Number:
			n, err := v.Int64()
			if err != nil {
				return 0
			}
			return int(n)
		}
		return 0
	}
	a, r := num("addedLines"), num("removedLines")
	if a == 0 && r == 0 {
		return 0, 0
	}
	return a, r
}

// parseToolArgs 解析模型给的参数。
// 空串按「无参数」处理：无参工具（app_info / port_list）模型经常给 "" 而不是 {}。
func parseToolArgs(raw string) (map[string]any, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(s), &args); err != nil {
		return map[string]any{}, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// toolCallKey 生成「同名 + 同参数」的判重键。
//
// 用解析后重新序列化的参数而不是原始字符串：模型两次给出的 JSON 可能
// 只差空格或键的顺序（`{"a":1,"b":2}` vs `{"b":2, "a":1}`），按原文比较会漏判。
// Go 的 json.Marshal 对 map 的键排序是固定的，所以等价参数必然得到同一个串。
// 解析失败（调用方已单独处理）时退回原文，宁可漏判也不要误判成同一次调用。
func toolCallKey(name string, args map[string]any, raw string) string {
	canon := strings.TrimSpace(raw)
	if len(args) == 0 {
		// ""/null/{} 都是「无参数」，归一化之后再比较。
		canon = "{}"
	} else if b, err := json.Marshal(args); err == nil {
		canon = string(b)
	}
	return name + "\x00" + canon
}

// toolCallName 从去重键里取回工具名（键是 name + \x00 + 规范化参数）。
func toolCallName(key string) string {
	if i := strings.IndexByte(key, 0); i >= 0 {
		return key[:i]
	}
	return ""
}

// dropStaleReads 作废本次提问里所有「读类工具」的缓存。
//
// 为什么必须做：去重键只由 name+args 组成，文件内容变了而参数没变时，第二次读
// 命中的是**改之前**的正文。典型序列 file_read A → file_edit A → file_read A，
// 模型拿着旧内容去改第二处，file_edit 报「没找到要替换的内容」，然后重读、再改
// ——白烧两三轮回合，用户看到的是「AI 在同一处反复改错」。
//
// 只删读类：写 / 命令 / 通知必须继续按「同一句只做一次」去重（重放一遍就是
// 第二次副作用），而且它们的参数里带着要写的内容，不存在「参数相同内容却变了」。
// 调用方持锁。
func dropStaleReads(seen map[string]aiToolRecord) {
	for k := range seen {
		if name := toolCallName(k); name != "" && mcpsrv.ToolLevel(name) == mcpsrv.LevelRead {
			delete(seen, k)
		}
	}
}

// formatToolArgs 生成给人看的参数文本（卡片上那一行）。
// 解析失败时退回原文——此时「用户看到模型写了什么」比「格式整齐」重要。
func formatToolArgs(args map[string]any, raw string) string {
	if len(args) == 0 {
		if s := strings.TrimSpace(raw); s != "" && s != "null" && s != "{}" {
			return logger.Trunc(s, 200)
		}
		return ""
	}
	// 下划线开头的是宿主注入的内部参数（_cwd 之类），模型没传过、用户看了也不懂，
	// 不该出现在工具卡片上。
	visible := make(map[string]any, len(args))
	for k, v := range args {
		if strings.HasPrefix(k, "_") {
			continue
		}
		visible[k] = v
	}
	if len(visible) == 0 {
		return ""
	}
	b, err := json.Marshal(visible)
	if err != nil {
		return logger.Trunc(raw, 200)
	}
	return logger.Trunc(string(b), 200)
}

// toolMessage 一条 role=tool 的结果消息。
func toolMessage(callID, content string) map[string]interface{} {
	return map[string]interface{}{
		"role":         "tool",
		"tool_call_id": callID,
		"content":      content,
	}
}

// truncateToolResult 把单个工具结果压到模型可接受的体量。
//
// 完整结果不落库（会撑爆 ai_messages），但写一行日志保留「完整版在多长、
// 前半截是什么」，需要复盘时能看出模型当时到底看到了多少。
// truncateToolResult 把过长的工具结果压到 aiToolResultMaxRune 以内。
//
// 不能简单砍头：结果几乎总是 JSON，砍在中间既不可解析，又会把排在后面的关键
// 字段（count / truncated / nextOffset）一起砍掉——模型看到半个数组，却不知道
// 自己在看半个数组，于是照样按「这就是全部」下结论。
// 所以超限时先按结构压：长字符串缩短、长数组保留前 N 项并补一条 _dropped，
// 短字段（计数、标记、路径）原样留着；结构压不下去才退回头部截断。
func truncateToolResult(ctx context.Context, val any, text, tool, callID string) string {
	n := len([]rune(text))
	if n <= aiToolResultMaxRune {
		return text
	}
	// WP3：先把原文落盘，再截断。
	//
	// 顺序不能反：截断之后原文就没了，模型要细节只能重调一次工具（又付一遍钱）。
	// 落盘失败不算错误——外置是省钱的优化，退回纯截断仍然能跑。
	refID := ""
	if convID := aitools.ConvIDFrom(ctx); convID != "" && strings.TrimSpace(callID) != "" {
		if id, serr := aitools.SaveToolResult(convID, callID, tool, text); serr != nil {
			logger.W("[ai-tool] %s 结果外置失败，退回纯截断: %v", tool, serr)
		} else {
			refID = id
		}
	}

	// 纯文本结果（工具直接返回字符串）不走结构压缩：那一趟会把它 marshal 成
	// 带引号的 JSON 字符串，模型看到的是多了一层转义的正文。
	_, plainText := val.(string)
	if val != nil && !plainText {
		if shrunk, ok := shrinkToolJSON(val, aiToolResultMaxRune); ok {
			logger.W("[ai-tool] %s 结果过长（%d 字），已按字段压缩到 %d 字",
				tool, n, len([]rune(shrunk)))
			if refID != "" {
				// 压缩后的版本已经够用，但原文仍然留着：模型想核对细节时不必重调工具。
				shrunk += fmt.Sprintf("\n\n[完整原文已存为 result #%s（约 %d 字），需要未被压缩的字段时用 tool_result_read(id=%q) 读取。]",
					refID, n, refID)
			}
			return shrunk
		}
	}
	logger.W("[ai-tool] %s 结果过长（%d 字），已截断喂给模型；头部：%s", tool, n, logger.Trunc(text, 300))
	head := string([]rune(text)[:aiToolResultMaxRune])
	if refID != "" {
		// offset 用**字节**：读取工具按字节切片，而这里的 head 是按字符切的。
		// 直接把字符数当 offset 传过去，遇到中文就会从半个字中间开始。
		return head + fmt.Sprintf(
			"\n\n[结果过长：原文 %d 字，这里只给前 %d 字。\n"+
				" 完整内容已存为 result #%s，可用 tool_result_read(id=%q, offset=%d) 接着读后半段（也可以从 0 开始分段读）。]",
			n, aiToolResultMaxRune, refID, refID, len(head))
	}
	return head + fmt.Sprintf("\n\n[结果过长，已截断：原文 %d 字，这里只给出前 %d 字。需要更多请缩小范围（比如加 glob 过滤、减少 limit）。]", n, aiToolResultMaxRune)
}

// shrinkToolJSON 按结构把工具结果压到预算内；压不下去返回 false。
//
// 逐级收紧「单个字段保留多少字」。大多数超限结果在第一档就够（一段文件正文、
// 一条 diff），数组过长的靠 aiToolArrayKeep 兜住。
func shrinkToolJSON(val any, max int) (string, bool) {
	raw, err := json.Marshal(val)
	if err != nil {
		return "", false
	}
	// UseNumber：压完再序列化时不要把整数变成 1.234e+06，
	// 模型看到科学计数法的行号/字节数会读错。
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return "", false
	}
	for _, lv := range []struct{ perField, arrayKeep int }{
		{aiToolFieldKeepRune, aiToolArrayKeep},
		{aiToolFieldKeepRune / 2, aiToolArrayKeep / 2},
		{aiToolFieldKeepRune / 4, aiToolArrayKeep / 4},
		{aiToolFieldKeepRune / 8, aiToolArrayKeep / 8},
	} {
		out, err := json.Marshal(shrinkJSONValue(tree, lv.perField, lv.arrayKeep))
		if err != nil {
			return "", false
		}
		if len([]rune(string(out))) <= max {
			return string(out), true
		}
	}
	return "", false
}

// shrinkJSONValue 递归重建 JSON 值：长字符串截短，长数组只留前 arrayKeep 项。
// 返回新树，原树不动（同一份结果后面还要给前端用）。
func shrinkJSONValue(v any, perField, arrayKeep int) any {
	switch t := v.(type) {
	case string:
		r := []rune(t)
		if len(r) <= perField {
			return t
		}
		return string(r[:perField]) + fmt.Sprintf("\n…[此处省略 %d 字]", len(r)-perField)
	case []any:
		limit := len(t)
		if limit > arrayKeep {
			limit = arrayKeep
		}
		out := make([]any, 0, limit+1)
		for i := 0; i < limit; i++ {
			out = append(out, shrinkJSONValue(t[i], perField, arrayKeep))
		}
		if dropped := len(t) - limit; dropped > 0 {
			out = append(out, map[string]any{"_dropped": dropped})
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = shrinkJSONValue(val, perField, arrayKeep)
		}
		return out
	}
	return v
}

// estimateLoopTokens 估算一组消息的 token 数。
//
// 只做「字符数 × 0.625」的粗估（中文按 1 字 ≈ 1 token 偏保守），目的是判断
// 「要不要折叠」，不是给用户看账单——精确值只能等上游返回 usage，而那时
// 上下文已经发出去、钱已经花了。
func estimateLoopTokens(messages []map[string]interface{}) int {
	n := 0
	for _, m := range messages {
		switch c := m["content"].(type) {
		case string:
			n += len([]rune(c)) * 10 / 16
		case []map[string]interface{}:
			// 带图消息的 content 是块数组（见 ai_image.go 的 messageContent）。
			// 不能整块序列化后按字符估：一张图在这里是几百万个 base64 字符，
			// 而模型侧一张图只按尺寸计约 1k token —— 按字符算会高估三个数量级，
			// 结果是每轮都判定「超预算」，把本该留下的结果全折叠掉。
			// 图片按张数计（与 buildAIMessages 的估法一致），文本块才按字符。
			for _, blk := range c {
				if blk["type"] == "image_url" {
					n += aiImageTokenEstimate
					continue
				}
				if s, ok := blk["text"].(string); ok {
					n += len([]rune(s)) * 10 / 16
				}
			}
		}
		// tool_calls 的参数也占 token，虽然通常不大，但别漏——漏了会让
		// 「估算说没超、实际超了」变成系统性偏差。
		if tc, ok := m["tool_calls"]; ok {
			if b, err := json.Marshal(tc); err == nil {
				n += len([]rune(string(b))) * 10 / 16
			}
		}
		n += 4 // 每条消息的固定开销（role、分隔符）
	}
	return n
}

// compactLoopMessages 压住循环内的上下文增长，返回折叠了几条。
//
// 策略是「折叠最早的工具结果」而不是「让模型重新摘要」：
//   - 摘要要再调一次模型，在已经连调好几轮的循环里是雪上加霜（钱和时间都翻倍）；
//   - 工具结果是最占体积、又最不需要逐字保留的部分——「查过 port_list，结论是
//     9230 在监听」比完整 JSON 有用得多；
//   - 最近一轮的结果一定不折叠：模型正要用它，折了等于让它重查。
//
// budget 由 aiLoopBudget 按模型窗口算出来传进来：固定 24000 对 1M 窗口的模型
// 是白白折叠（本可以塞进更多文件原文），对 8K 模型则根本压不住。
//
// 就地修改 messages 里的 map（同一个 map 会被后续轮次复用）。

// 两种 compact 事件（前端按 kind 分开渲染）。
const (
	compactKindHistory = "history"
	compactKindLoop    = "loop"
)

// loopCompactEvent 循环内压缩的事件载荷。
//
// 复用 compactEvent 的字段（前端那张卡已经在读 before/after/window…），
// 额外带三个：
//   - folded / summarized：这一轮压了几条、各自什么手段；
//   - folded_ids：被处理的 tool_call id，用于「压缩后重查率」这个指标。
func loopCompactEvent(phase string, cr compactResult, lim aiContextLimits, budget int) map[string]any {
	after := cr.After
	if phase == "start" {
		// start 只表达「开始了」：此时还没压完，after 给 0 会让卡片显示
		// 「90k → 0」，看起来像把上下文清空了。
		after = 0
	}
	// kind=loop + attempted=false：这不是摘要，也不会失败。
	// 前端据此显示「已折叠 N 条较早结果」，而不是「摘要失败，已直接截断」。
	ev := compactEvent(compactKindLoop, phase, cr.Before, after, cr.Folded+cr.Summarized, compactKeepRecentRounds, lim, budget, false, false)
	ev["folded"] = cr.Folded
	ev["summarized"] = cr.Summarized
	ev["folded_ids"] = cr.FoldedIDs
	return ev
}

// compactResult 一次循环内压缩的结果。
//
// 为什么要回结构而不是一个 int：`compact` 事件要带 `folded_ids`（§8 的
// 「压缩后重查率」全靠它算），还要区分「折叠」与「摘要」两种动作——
// 只回一个数字的话，这些信息在函数里就丢了，事后只能靠时间窗口猜。
type compactResult struct {
	// Before / After 压缩前后的估算 token（给事件卡显示 90k → 40k）。
	Before int
	After  int
	// Folded 折叠到 aiToolFoldRune 的条数。
	Folded int
	// Summarized 只留一行的条数（更早的轮次）。
	Summarized int
	// FoldedIDs 被处理过的工具调用 id（含折叠与摘要）。
	FoldedIDs []string
}

// compactTier 压缩分层（WP4b）。
//
// 三层的边界用「离最后一轮多远」表示，而不是绝对轮次：
// 任务可能跑 3 轮也可能跑 60 轮，只有「最近的还热、早的先冷」这件事是恒定的。
const (
	// compactKeepRecentRounds 最近几轮原样保留（含最后一轮）。
	compactKeepRecentRounds = 2
	// compactSummarizeAfter 超过这么多轮之前的一律只留一行。
	compactSummarizeAfter = 8
	// compactSummaryRune 摘要层每条保留多少字（够认出「做过什么」）。
	compactSummaryRune = 120
	// compactFoldTargetPercent 触发折叠后要压到预算的百分之多少。
	//
	// 为什么不是「压到刚好不超」：折叠会就地改写历史里的工具结果，而改写点
	// 之后的上游前缀缓存全部作废。压到刚好不超的话，下一轮新增一条工具结果
	// 就又超了，于是**每轮都折一次、每轮都丢缓存**——实测 202 轮发生过折叠，
	// 那些轮次平均命中率 66.7%，没折叠的是 80.9%。
	// 一次压到 60%，可以撑好几轮才再次越线，把「省上下文」的成本从每轮摊成每几轮。
	compactFoldTargetPercent = 60
)

// compactLoopMessages 压住循环内的上下文增长，返回压缩明细。
//
// 三层策略（全部就地处理，**不调摘要模型**，所以成本不变）：
//
//	最近 2 轮   不动：模型正要用它们，折了等于让它重查一次（多花一轮的钱）；
//	3–8 轮      折叠到 compactFoldRune：留工具名 + 结论首段；
//	更早        只留一行：正文全部走 WP3 的 result id（或重新调用）。
//
// 触发条件是「本轮估算超过 budget」，触发后**一次压到 budget 的 60%**
// （见 compactFoldTargetPercent）：压到刚好不超会让下一轮又超、又折一次，
// 而每折一次都会改写历史、丢掉上游的前缀缓存。
//
// budget 由 aiLoopBudget 按模型窗口算出来传进来：固定 24000 对 1M 窗口的模型
// 是白白折叠（本可以塞进更多文件原文），对 8K 模型则根本压不住。
//
// 就地修改 messages 里的 map（同一个 map 会被后续轮次复用）。
func compactLoopMessages(messages []map[string]interface{}, budget int) compactResult {
	res := compactResult{Before: estimateLoopTokens(messages), After: estimateLoopTokens(messages)}
	if budget <= 0 {
		budget = 24000
	}
	if res.Before <= budget {
		return res
	}
	// 一次折到位：目标不是 budget，而是更低的 60%。budget 很小时（单测会传 1）
	// 目标会算成 0，等于「能折多少折多少」——与旧行为一致，不会死循环。
	target := budget * compactFoldTargetPercent / 100

	// WP4a：用显式轮次索引代替「反向扫数组」。
	rounds := resolveMessageRounds(messages)
	lastRound := lastRoundOf(rounds)
	// 从最老的轮次开始处理：先压最冷的，够用就停。
	for i := 0; i < len(messages); i++ {
		if rounds[i] > lastRound-compactKeepRecentRounds {
			// 最近两轮（含最后一轮）原样保留。
			continue
		}
		m := messages[i]
		if m == nil || m["role"] != "tool" {
			continue
		}
		s, ok := m["content"].(string)
		if !ok {
			continue
		}
		n := len([]rune(s))
		if n <= compactSummaryRune {
			// 已经够短了，再折没有收益。
			continue
		}
		name := toolNameOfCall(messages, argString(m, "tool_call_id"))
		// compactSummarizeAfter 之前的轮次只留一行；其后的折叠到 fold 上限。
		older := rounds[i] <= lastRound-compactSummarizeAfter
		if older {
			m["content"] = fmt.Sprintf("[更早的工具结果已摘要：%s 原文 %d 字。要细节请重新调用该工具（缩小范围），或从上下文里的 result id 读原文。]", name, n)
			res.Summarized++
		} else if n > aiToolFoldRune {
			m["content"] = string([]rune(s)[:aiToolFoldRune]) +
				fmt.Sprintf("\n\n[较早的工具结果已折叠：%s 原文 %d 字，只保留前 %d 字。若确实需要完整内容，请重新调用该工具并缩小范围。]", name, n, aiToolFoldRune)
			res.Folded++
		} else {
			// 在摘要层与折叠层之间但已经不长：不动，也不算进统计。
			continue
		}
		if id := argString(m, "tool_call_id"); id != "" {
			res.FoldedIDs = append(res.FoldedIDs, id)
		}
		if estimateLoopTokens(messages) <= target {
			break
		}
	}
	res.After = estimateLoopTokens(messages)
	// 日志交给调用点按 run 去重：这里每轮都会被调用，逐条记 I 日志会让
	// 长任务的日志全是同一句「超预算」，反而看不出真正的问题。
	return res
}

// toolNameOfCall 从 assistant 的 tool_calls 里反查某个 callID 的工具名。
//
// tool 消息自己不带工具名（协议里只有 tool_call_id），而折叠/摘要文案里
// 「file_read 原文 40KB」比「某条工具结果」有用得多——用户与模型都要靠它
// 判断「这条要不要重读」。找不到时回落成 "工具"。
func toolNameOfCall(messages []map[string]interface{}, callID string) string {
	if callID == "" {
		return "工具"
	}
	for _, m := range messages {
		if m == nil || m["role"] != "assistant" {
			continue
		}
		calls, ok := m["tool_calls"].([]map[string]any)
		if !ok {
			continue
		}
		for _, c := range calls {
			if argString(c, "id") != callID {
				continue
			}
			if fn, ok := c["function"].(map[string]any); ok {
				if name := argString(fn, "name"); name != "" {
					return name
				}
			}
		}
	}
	return "工具"
}

// toolOutputText 把工具结果渲染成「人看的」文本（只给卡片，不喂模型）。
//
// 为什么需要：结果本身是 JSON——喂给模型要的是结构化字段，可卡片上直接铺开
// 这段 JSON 就是一团转义字符：换行成了 \n、引号成了 \"，一旦截断更只剩半截
// JSON。用户想看的「命令到底输出了什么」一个字节都没有，看到的是一串
// "\ufffd\ufffd\ufffd" 和一句「已截断」。
//
// 目前只有 run_command 需要：它的值本来就是给人读的终端输出。其它工具的
// 结果（文件内容 / diff / 文件列表）都有自己的展示分支。
func toolOutputText(name string, val any) string {
	// file_read：卡片上该显示文件正文，而不是那坨 {"path":...,"content":"..."}。
	// 之前只有 run_command 走这条分支，读文件的结果原样铺出来，行号外面裹着 JSON
	// 转义，再被 400 字的上限一截——用户看到的就是「只读了十几行」。
	if name == "file_read" {
		m, ok := val.(map[string]any)
		if !ok {
			return ""
		}
		content := mcpsrv.Arg(m, "content")
		if content == "" {
			// 空文件 / offset 超出末尾：真正的解释在结果自己的 truncatedNote 里，
			// 这里不另编一句话。
			return ""
		}
		from := mcpsrv.ArgInt(m, "from", 0)
		to := mcpsrv.ArgInt(m, "to", 0)
		if from <= 0 || to < from {
			// 没有行号区间（老数据 / 非标准调用）：正文照样给，别硬编一个「第 0–0 行」。
			return content
		}
		head := fmt.Sprintf("第 %d–%d 行 · 共 %d 行", from, to, mcpsrv.ArgInt(m, "lines", to-from+1))
		if mcpsrv.ArgBool(m, "truncated") {
			head += " · 文件还有更多内容（需要时用 offset 继续读）"
		} else {
			head += " · 已到文件末尾"
		}
		return head + "\n" + content
	}
	if name != "run_command" {
		return ""
	}
	m, ok := val.(map[string]any)
	if !ok {
		return ""
	}
	// 没有 stdout 键说明这不是同步执行的命令结果（后台任务只回 jobId/logPath），
	// 卡片上照旧展示原始结果，别在这里编一块空输出。
	if _, has := m["stdout"]; !has {
		return ""
	}
	stdout := mcpsrv.Arg(m, "stdout")
	stderr := mcpsrv.Arg(m, "stderr")

	var b strings.Builder
	var flags []string
	if code := mcpsrv.ArgInt(m, "exitCode", 0); code != 0 {
		flags = append(flags, fmt.Sprintf("exit %d", code))
	}
	if mcpsrv.ArgBool(m, "backgrounded") {
		// 不能沿用下面那句「超时，已终止进程」：进程还在跑，写着「已终止」
		// 用户会以为它死了，转头再去启动一次，于是端口撞车。
		flags = append(flags, fmt.Sprintf("超出前台等待时间，已转入后台继续运行（job %s）", mcpsrv.Arg(m, "jobId")))
	} else if mcpsrv.ArgBool(m, "timeout") {
		flags = append(flags, "超时，已终止进程")
	}
	if mcpsrv.ArgBool(m, "canceled") {
		// 「停了」和「失败了」在用户眼里是两件事：前者是他按的停止，后者要他去查问题。
		flags = append(flags, "已停止（进程已终止）")
	}
	if mcpsrv.ArgBool(m, "outputTruncated") {
		flags = append(flags, "输出超过上限，只保留了前段")
	}
	if len(flags) > 0 {
		b.WriteString(strings.Join(flags, " · "))
		b.WriteString("\n")
	}
	switch {
	case stdout != "" && stderr != "":
		// 两路都有内容时才给 separators：否则标签比内容还长。
		b.WriteString(strings.TrimRight(stdout, "\n"))
		b.WriteString("\n--- stderr ---\n")
		b.WriteString(strings.TrimRight(stderr, "\n"))
	case stdout != "":
		b.WriteString(strings.TrimRight(stdout, "\n"))
	default:
		b.WriteString(strings.TrimRight(stderr, "\n"))
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		// 成功且没有任何输出：留一句人话，比一张空白卡片清楚。
		return "（无输出）"
	}
	return out
}

// truncateKeepLines 按字符截断，但保留换行。
//
// 不用 logger.Trunc：它把换行压成空格（日志要一行一条），而工具结果多半是
// 缩进 JSON 或带行号的匹配结果，压成一行就完全没法看了。
func truncateKeepLines(s string, maxRunes int) string {
	r := []rune(s)
	if maxRunes <= 0 || len(r) <= maxRunes {
		return s
	}
	// 带上原始长度：只说「已截断」等于让人猜「少看了多少」——一行 30 万字和
	// 一行 3000 字看起来一样，而前者才该去文件里看完整版。
	return string(r[:maxRunes]) + fmt.Sprintf("…（已截断：共 %d 字，只显示前 %d 字）", len(r), maxRunes)
}

// encodeToolRecords 把记录序列化成落库的 JSON；空记录返回空串。
//
// 落库前再截一次结果：喂给模型的上限是 12000 字，五行这样的记录就是 60KB，
// 而 ai_messages 每条回复都要读一遍。完整内容本来就没进过库（只有模型看到过），
// 这里留 2000 字足够让用户回忆起「它当时查到了什么」。
//
// 空串而不是 "[]"：绝大多数回复没有工具调用，没必要为此在每行多存两个字节，
// 前端判空逻辑也更短（`if (!m.tool_calls) return`）。
func encodeToolRecords(records []aiToolRecord) string {
	if len(records) == 0 {
		return ""
	}
	brief := make([]aiToolRecord, len(records))
	copy(brief, records)
	for i := range brief {
		brief[i].Result = truncateKeepLines(brief[i].Result, aiRecordResultMaxRune)
		brief[i].Output = truncateKeepLines(brief[i].Output, aiRecordOutputMaxRune)
		brief[i].Args = truncateKeepLines(brief[i].Args, 300)
	}
	b, err := json.Marshal(brief)
	if err != nil {
		logger.W("[ai] 工具调用记录序列化失败: %v", err)
		return ""
	}
	return string(b)
}

// countRounds 统计实际发生的轮数（按记录里的 Round 去重）。
// 用记录算而不是用循环变量：轮次可能在工具执行阶段被取消打断，
// 变量会数到「打算跑的第 N 轮」，而记录只包含真正发生过的。
func countRounds(records []aiToolRecord) int {
	seen := map[int]bool{}
	for _, r := range records {
		if r.Round > 0 {
			seen[r.Round] = true
		}
	}
	return len(seen)
}

// madeProgress 判断这一批工具调用里有没有「真的往前走」的动作。
//
// 只看两件事：有没有真执行过（Dedup = 与之前某次完全相同，被跳过）、
// 有没有被用户拒绝（Denied = 用户说不行，再续一批只会重问一遍）。
// 执行失败也算进展：模型会换参数再试，那正是它在想办法。
//
// 用它决定「轮次到顶后要不要再开一批」：全是去重/拒绝说明模型在原地打转，
// 再给 12 轮就是把同一段上下文重发 12 次。
func madeProgress(records []aiToolRecord) bool {
	for _, r := range records {
		if r.Dedup || r.Denied {
			continue
		}
		return true
	}
	return false
}
