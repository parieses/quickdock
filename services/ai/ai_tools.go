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

	// aiChatTurnBudgetFactor 主循环的总 token 预算＝上下文预算 × 这个倍数。
	//
	// 轮次上限（aiToolMaxTotalRounds = 120）只数次数，不数钱：每轮都要把整份
	// 上下文重发一遍，一个「看着一直在动、其实在原地空转」的任务可以合法地烧掉
	// 上百万 token 而没有任何刹车。
	//
	// 取 20 倍（约 240 万）是留给正常大任务的余量：写几十节文档、跑十几条命令的
	// 长任务一般在 5~8 倍以内收工；而真正打转的循环会在几十轮内撞线。
	//
	// 为什么不直接用 agentScope 的 overBudget：那是 run 级 6 倍预算，专门给派活
	// 用的。主对话自己很容易烧掉 6 倍上下文，把主循环的用量累加进去之后，
	// spawn_agent 会在一进去就被拒——那等于把长任务里的编排能力废掉。
	aiChatTurnBudgetFactor = 20
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

// aiGateMode 需要「模式门」的那批工具的唯一放行模式（与 aiModePrompts 的 key 对应）。
//
// 需要注意门只对宿主工具（mcpsrv.Global 里的东西）成立：环境、笔记、待办、插件
// 这些直接读写用户数据的能力，在普通对话里被顺手指令去执行是设计错误——
// 用户问一句常识，AI 顺手停了他的服务。教程模式的定位本来就是「通过对话操作这个软件」，
// 工具在那里才是本职。
//
// agent 工具（services/ai/tools 里的抓网页、读目录、看 git、改文件、跑命令）不受这道门
// 约束：它们服务于「在某个目录里干活」，和「学这个软件怎么用」是两件事。它们自己的闸
// 在工具内部——写操作受 fsguard 的写白名单限制（没绑定目录直接拒绝）、高危命令在
// run_command 里被拒——不靠「只在教程模式开放」兜底。
const aiGateMode = "tutorial"

// aiGeneralCategories 宿主工具里所有模式都能用的分类。
//
// 目前只有 file：file_read / file_grep / file_search 是只读的，而「让 AI 看一眼这个文件」
// 在普通对话里同样成立；更关键的是 file_edit 的前置就是 file_read——读不到就改不了。
var aiGeneralCategories = map[string]bool{
	mcpsrv.CategoryFile: true,
}

// aiToolVisibleInMode 判断一个工具在该模式下要不要挂给模型。
func aiToolVisibleInMode(mode, category string, agentOnly bool) bool {
	if agentOnly || aiGeneralCategories[category] {
		return true
	}
	return mode == aiGateMode
}

// ToolFilter 在「按 mode 过滤」之上再收窄一次工具面（编排层用）。
//
// 为什么需要第二层：mode 过滤回答的是「这个任务类型该看到哪些工具」，
// 而 role 过滤回答的是「这个 agent 有多大权限」。子 agent 无人值守地跑，
// 它的边界必须由能力决定（拿不到 file_write），不能靠提示词说「别改」。
//
// MaxLevel 用 -1 表示不限：LevelRead 是 0，用 0 当「不限」会把 explore
// 的只读约束悄悄变成一个不生效的开关。
type ToolFilter struct {
	MaxLevel   int
	Categories map[string]bool
	// NoSpawn 撤下 spawn_agent（子 agent 不能再派活）。
	NoSpawn bool
}

// aiToolsPayload 把当前暴露、且该模式可见的工具转成 OpenAI tools[] 格式。
//
// 过滤分两层，顺序不能反：先由 ExposedFunctions 应用等级门与单工具开关
// （用户关掉的工具在这里已经没了），再按模式过滤分类。两层都不是权限——
// 它们决定的是「模型知道自己有什么」，真正的拦截在 mcpsrv.Call 里。
//
// 没有可用工具时返回 nil 而不是空切片：部分网关对 "tools": [] 直接报 400，
// 而空切片经过 json.Marshal 就是 []，不会变成 null。调用方据此决定要不要设字段。
func aiToolsPayload(mode string) []map[string]any {
	t, _ := aiToolsPayloadFor(mode, nil)
	return t
}

// aiToolsPayloadFor 同 aiToolsPayload，额外按 role 收窄（filter 为 nil 表示不收窄）。
//
// 第二个返回值表示外部 MCP 工具因名额被截：调用方（agent_run）据此在 system
// 里补一句说明——不说明的话，模型会拿着一轮里根本不存在的工具名去调，
// 然后对着「未知工具」反复重试。
func aiToolsPayloadFor(mode string, filter *ToolFilter) ([]map[string]any, bool) {
	fns := mcpsrv.ExposedFunctions()
	out := make([]map[string]any, 0, len(fns))
	// 外部工具按 server 分组收集：名额与体积都按**整组**判（见下面的说明）。
	var extGroups []*extToolGroup
	extByServer := map[string]*extToolGroup{}
	for _, f := range fns {
		if !aiToolVisibleInMode(mode, f.Category, f.AgentOnly) {
			continue
		}
		// plan 模式只挂只读工具。
		//
		// 写类**不挂**而不是「挂了但不让调」：模型看不到 file_write 时只会说
		// 「确认后我来改」；而看得到、调一次被拒，它会换个参数再试，
		// 一路把上下文和用户耐心都烧掉。
		if mode == aiPlanMode && f.Level > mcpsrv.LevelRead {
			continue
		}
		if filter != nil {
			// spawn_agent 的撤下要单独判：它按分类属于 code，靠 Categories
			// 拦不住（code 角色本来就该有 code 分类的工具）。
			if filter.NoSpawn && f.Name == spawnAgentToolName {
				continue
			}
			if filter.MaxLevel >= 0 && f.Level > filter.MaxLevel {
				continue
			}
			if filter.Categories != nil && !filter.Categories[f.Category] {
				continue
			}
		}
		desc := f.Description
		if f.Category == mcpsrv.CategoryExternal {
			// 外部工具的描述截到 extToolDescMaxRune（理由见那个常量）。
			desc = clipRunes(desc, extToolDescMaxRune)
		}
		entry := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        f.Name,
				"description": desc,
				"parameters":  f.Parameters,
			},
		}
		if f.Category != mcpsrv.CategoryExternal {
			out = append(out, entry)
			continue
		}
		// 体积按**实际要发出去的那份**算：截过的描述 + 序列化后的 schema。
		// schema 序列化失败（理论上不会）就按 0 计——宁可多挂一个，也不要
		// 因为一次编码错误把整组工具藏掉。
		size := len(desc)
		if raw, err := json.Marshal(f.Parameters); err == nil {
			size += len(raw)
		}
		key := extServerOf(f.Description)
		g, ok := extByServer[key]
		if !ok {
			g = &extToolGroup{server: key}
			extByServer[key] = g
			extGroups = append(extGroups, g)
		}
		g.items = append(g.items, entry)
		g.sizes = append(g.sizes, size)
		g.bytes += size
	}
	// 外部工具名额（阶段 D）：tools[] 总数一大，一部分上游直接 400，
	// token 账单也难看。内置工具优先，外部工具填剩余名额，超出不挂——
	// 名额只算过了可见性过滤的，被模式/角色裁掉的外部工具不占位置。
	// 截断后 agent_run 会在 system 里补说明，模型至少知道「有工具没挂上」。
	//
	// 这里按**整组**取舍：把某个 server 挂一半比整套不挂更糟——模型手里有
	// 「这个 server 的一部分工具」，会认为缺的那些能力根本不存在，于是换个
	// 说法反复试，而它其实只是没挂上。
	extTrimmed := false
	kept, bytes := 0, 0
	for _, g := range extGroups {
		if kept+len(g.items) <= extToolsMax && bytes+g.bytes <= extToolsMaxBytes {
			out = append(out, g.items...)
			kept += len(g.items)
			bytes += g.bytes
			continue
		}
		// 一个 server 自己就超了上限（只接了一两个 MCP 时很常见）：整套跳过等于
		// 用户刚接的 server 一个工具都调不到，比挂一半更坏。这时按顺序装到装不下
		// 为止，至少让模型看得见这个 server 的主要能力。
		if kept == 0 {
			for i, it := range g.items {
				if kept+1 > extToolsMax || bytes+g.sizes[i] > extToolsMaxBytes {
					break
				}
				out = append(out, it)
				kept++
				bytes += g.sizes[i]
			}
		}
		extTrimmed = true
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, extTrimmed
}

// extToolGroup 一个外部 server 的工具集合，用于按整组判断名额与体积。
type extToolGroup struct {
	server string
	items  []map[string]any
	// sizes 与 items 一一对应：单条的实际字节数，整组超限时按它逐条装。
	sizes []int
	bytes int
}

// toolHint 交给模型的工具使用说明，只在本轮确实带了工具时追加。
//
// 为什么不用 system prompt 里的统一说明：那段提示在没工具时会让模型
// 尝试调用一个不存在的函数，然后反复道歉。这里跟着「有没有工具」走。
//
// 按模式分成两份而不是一份写全：普通对话只有 web_fetch，这时候跟它讲
// 「环境状态、定时任务、插件」等于告诉它「你有一堆其实没有的工具」，
// 它会照着去调，然后收到一串 tool not found。
const (
	// toolHintSystem 系统工具全套可用（只在「使用教程」模式）。
	//
	// 后两条是踩出来的，不是预防性条款：
	//   - 重复调用：Agnes 2.5 Flash 会在同一轮里给出两条一模一样的 robot_notify，
	//     两条都执行 → 用户手机收到两遍同样的消息。宿主只能拦「参数完全相同」的，
	//     换个参数重发同样的问题还得靠模型自己收敛。
	//   - 能力介绍：几十个工具的 schema 就在上下文里，问「你会什么」时模型会直接
	//     照抄工具名（env_list、robot_notify…），用户看不懂也不关心。
	toolHintSystem = "\n\n[可用工具]\n" +
		"你可以调用宿主提供的工具，直接查询甚至修改 QuickDock 里的东西——不必只讲操作步骤，" +
		"用户说「帮我看一下 / 帮我改一下 / 为什么不行」时，先调工具拿真实状态再回答。" +
		"工具返回错误时读错误信息自行调整参数，不要重复同一个失败调用；" +
		"某个能力不可用时直接说明，不要假装已经完成。\n" +
		"[工具使用纪律]\n" +
		"1. 同一个工具 + 完全相同的参数，一次提问里只调一次。结果会立刻回给你，重调拿不到新信息；" +
		"发通知、写文件、启停服务这类操作重放一遍就是第二次真实副作用。宿主会拦下重复调用并直接返回上次结果。\n" +
		"2. 需要多个信息时并行调用**不同**工具，不要反复调同一个。已经调过并拿到结果的，直接用那个结果。\n" +
		"3. 工具是你干活的手段，不是你的能力清单。被问到「你会什么 / 能做什么」时用 QuickDock 的功能说话" +
		"（工作空间、剪贴板、笔记、待办、定时任务、站点监控、环境管理、插件等），不要罗列工具名和接口。\n"

	// toolHintGeneral 网页抓取 + 代码操作（普通对话）。
	//
	// 讲法要跟「怎么用」和「做不了时怎么说」绑在一起，别只列工具名——
	// 模型最容易犯的两个错是把「没抓到」说成「这个网页不存在」，
	// 以及在没有工作目录的情况下反复换路径试写操作。
	//
	// 另外单独给了「执行纪律」一节：实测 agent 循环里最掉体验的不是答案错，
	// 而是每一步前后都夹一句旁白（「我先看一下 xxx」→ 调工具 →「好的，看完了，
	// 这个文件是…」）。一轮五次工具就是十句废话，正文被冲得七零八落。
	toolHintGeneral = "\n\n[可用工具]\n" +
		"**网页**：web_fetch 打开网址读正文，web_links 取页面里的链接，web_head 只看状态码与响应头。" +
		"查文档、看报错页、确认链接里写了什么时用它们，不要凭印象编造网页内容。打不开（403 / 超时 / 需要登录 / 只返回脚本）时如实说明，" +
		"把链接给用户让他自己看，不要假装读到了，也不要反复重试同一个地址。\n" +
		"**代码**：dir_list 看目录结构，file_read 读文件（行号在 content 里），file_grep 按内容搜，" +
		"git_status / git_diff 看改动与差异，file_write 新建文件、file_edit 精确替换、run_command 跑命令，" +
		"check_workdir 一次问清「这是什么项目、该怎么跑」。\n" +
		"[执行纪律]\n" +
		"- 要调工具就直接调：不写「我先看一下」「让我查查」这类预告，也不要每步都汇报一次进度。" +
		"工具返回后不复述结果——用户已经能在工具行里看到，你只给结论或做下一步。\n" +
		"- 要产出文件（写文档、建配置、生成脚本）就用 file_write，**不要**在 run_command 里 echo / Set-Content / " +
		"node -e 造文件——引号、换行、编码在那条路上都容易出错，而且用户看不出你写了什么。\n" +
		"- 改已有文件的流程是：check_workdir 或 dir_list 先看 → file_read 看清原文 → file_edit 精确替换 → git_diff 确认。" +
		"不要整文件重写，也不要在没读过的文件上猜着改；file_edit 报「没找到要替换的内容」时先 file_read 核对原文再改。\n" +
		"- **没有可用工作目录时写操作会被拒绝**：这时不要换个路径重试（工具的白名单不会因为" +
		"换个写法就放开），而是让用户把这个任务建在一个有效的空间里。\n" +
		"- run_command 只用来做必须走命令行的事（装依赖、跑测试、构建）。高危命令（删根目录、git reset --hard、强推、发版）会被拒绝，" +
		"这时把命令写给用户，让他在终端里自己执行。\n" +
		"- 记忆只写在**当前工作区的记忆目录**里（用 project_memory 工具读写）：项目任务在 <cwd>/.quickDock/MEMORY.md，" +
		"没有绑定项目目录的任务在数据目录下跨任务共享的助手记忆；项目任务首次对话时系统会自动生成基线画像。" +
		"绝不要在项目根目录创建或修改 AGENTS.md——那是用户维护的「项目规则文件」，每轮自动注入，不是你的记忆，去改它会污染用户的规则。" +
		"用户要求「扫描项目 / 建立记忆」时，先用 dir_list / check_workdir 探索，再把值得长期记的事实用 project_memory 写进 MEMORY.md。" +
		"用户说「记住」或在纠正你的做法时，也顺手把结论写进去——不然下次同样的错还要再犯一遍。\n" +
		"- **任务确实要分多步才能做完时**（改多个文件、装一套环境、排查一串原因），先用 GitHub 风格任务清单列出步骤：" +
		"`- [ ] 步骤一`、`- [x] 已完成`，每做完一步就把对应的 `[ ]` 改成 `[x]`，让用户实时看到进度；清单放在回答开头。" +
		"但只问一句话、只改一处、只查一个东西时**不要**列清单——那种清单只是给一句回答套了个壳，" +
		"用户扫两行就到底了，反而要多读一层。\n" +
		"- 查环境状态、定时任务、监控、待办笔记这些系统能力在「使用教程」模式里才有；用户在这边问起时，告诉他切到那个模式。\n"

	// toolHintPlan 计划模式：只有只读工具。
	//
	// 必须单独一份而不是复用 toolHintGeneral：那一段里讲 file_write 怎么用、
	// file_edit 怎么精确替换，而这个模式根本没挂它们——照着讲等于教模型去调
	// 一个不存在的函数，它会在正文里伪造调用（见 ai_dsml.go）。
	toolHintPlan = "\n\n[可用工具·只读]\n" +
		"本模式只挂了**只读**工具：dir_list、file_read、file_grep、git_status、git_diff、check_workdir、web_fetch。" +
		"file_write、file_edit、run_command 这些写类工具本次**没有挂载**，你调不到，也不要把调用写成正文。\n" +
		"用这些工具把要改的地方读清楚，然后给出一份任务清单让用户确认；真正动手要等用户点「开始执行」。\n" +
		"- 没有可用工作目录时读不到项目文件：不要猜路径，直接告诉用户这个任务需要绑定一个目录。\n" +
		"- 需要多个信息时并行调用**不同**工具，不要反复调同一个。\n"

	// toolHintExhausted 工具轮次用尽后的 system 说明。
	//
	// 这段存在的唯一原因是一个实测出来的坏结果：轮次用尽时工具被撤下（请求里不再有
	// tools），但 system 里那段「可用工具」如果还留着，模型会认为工具仍然可用，
	// 于是把调用写成正文——DeepSeek 会吐出一串 `<｜DSML｜invoke>` 标记
	// （见 ai_dsml.go）。用户在气泡里看到一段原始标记，然后就没了下文。
	// 所以收尾轮不仅要撤掉工具说明，还要明确告诉它「现在是纯文字收尾」。
	toolHintExhausted = "\n\n[工具轮次已用尽]\n" +
		"本轮**没有任何可用工具**（工具已经全部撤下，再输出工具调用不会被执行）。" +
		"请直接根据上面已经拿到的结果，用文字把最终答案写完并收尾；" +
		"不要输出任何工具调用格式或标记（诸如 invoke / parameter 这类 DSML 标记），" +
		"用户看不到标记本身，只会看到一段突然中断的回答。" +
		"确实还没做完的，用一段话讲清楚「已经完成什么、还差什么、下一步该怎么做」，不要留悬空的半句话。\n"
)

// toolHint 返回该模式下的工具说明正文。
func toolHint(mode string) string {
	switch mode {
	case aiGateMode:
		return toolHintSystem
	case aiPlanMode:
		return toolHintPlan
	}
	return toolHintGeneral
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
func applySystemHints(messages []map[string]interface{}, base, mode, cwd string, withTools, exhausted bool) {
	if len(messages) == 0 || messages[0]["role"] != "system" {
		return
	}
	s := base
	switch {
	case withTools:
		s += toolHint(mode)
	case exhausted:
		// 本来有工具、这一轮却被撤下了：只能是没有轮次了（见 ai_stream.go 的收尾轮）。
		s += toolHintExhausted
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
	"- 更新清单后直接继续做下一步，不要在正文里复述整份清单。两步以内的事不用清单。\n"

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
	"dir_list":      true,
	"git_status":    true,
	"git_diff":      true,
	"check_workdir": true,
}

// parallelSafeTools 允许在同一轮里并行执行的工具白名单。
//
// 用白名单常量而不是给 Tool 加一个「我可以并发」的字段：后者会让每个工具
// 各自声明能力，而「能不能并发」取决于这一轮里还跑着什么——run_command
// 读文件时是安全的，和 file_edit 同轮就不安全。这种判断只有调度处（这里）
// 知道，让工具自己声明迟早会出现「两个都标了可并发、结果互相踩」。
//
// 目前只有 spawn_agent：它天然是「派出去各干各的」，且子 agent 之间不共享
// 上下文；同目录写操作由 agent_spawn.go 的目录闸串行。
var parallelSafeTools = map[string]bool{
	spawnAgentToolName: true,
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
		wireCalls = append(wireCalls, map[string]any{
			"id":   c.ID,
			"type": "function",
			"function": map[string]any{
				"name":      c.Name,
				"arguments": c.Args,
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

	if canRunParallel(calls) {
		sem := make(chan struct{}, agentMaxParallel)
		var wg sync.WaitGroup
		for i := range calls {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				// 并发路径里 panic 必须自己兜：主 goroutine 的 recover 管不到它，
				// 一旦漏出去整个进程就没了。
				defer recoverPanic("ai tool " + calls[i].Name)
				sem <- struct{}{}
				defer func() { <-sem }()
				exec(i)
			}()
		}
		wg.Wait()
	} else {
		for i := range calls {
			exec(i)
		}
	}

	for i := range calls {
		out = append(out, toolMessage(calls[i].ID, contents[i]))
	}
	return out, records
}

// execOneToolCall 执行单个工具调用，返回记录与要回给模型的文本。
//
// 抽出来是为了让「串行一轮」与「并发一轮」走完全相同的逻辑：两份实现迟早
// 会在去重、超时、错误整形上分叉，而分叉的那一半是跑得少的那一半。
func (a *AIService) execOneToolCall(ctx context.Context, c aiToolCall, round int, seen map[string]aiToolRecord, seenMu *sync.Mutex, cwd string) (aiToolRecord, string) {
	rec := aiToolRecord{ID: c.ID, Name: c.Name, Round: round}

	// 用户点了停止：把剩余调用标记为未执行照常回给模型（协议要求每条
	// tool_call 都有对应的 tool 消息），但不真的去跑。
	if err := ctx.Err(); err != nil {
		rec.Error = "用户已停止，未执行"
		return rec, rec.Error
	}

	// 本次调用的 id 挂到 ctx：写盘前的快照要能与「哪一步改的」对上。
	// 在参数解析之前就设置——参数写坏也要能对应到卡片上的那条记录。
	ctx = aitools.WithToolCallID(ctx, c.ID)

	args, aerr := parseToolArgs(c.Args)
	rec.Args = formatToolArgs(args, c.Args)
	if aerr != nil {
		// 参数不是合法 JSON：原样回给模型，它看得懂自己写坏了什么。
		rec.Error = aerr.Error()
		return rec, "参数解析失败：" + rec.Error + "\n原始参数：" + c.Args
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
	outsideAbs := ""
	if scope := mcpsrv.Scope(args); scope != "" && pathReadTools[c.Name] {
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

	// 审批闸门（见 ai_approval.go）：写类与高危工具在这里停下来等用户点确认。
	//
	// 位置刻意在去重与参数校验之后：
	//   - 去重命中的是「同一轮里第二次一模一样的调用」，那时工具根本没跑，
	//     问用户等于同一件事问两遍；
	//   - 参数坏了的调用执行必然报错，问也没有意义。
	//
	// 被拒绝的调用也写进 seen（见上面命中分支）：拒绝是本次提问内的一道墙。
	// seen 只活在这一次提问里，重新发问仍然会重新征求同意——所以「用户可能改主意」
	// 的机会并没有丢，丢掉的只是「同一句话问两遍」。
	gate := approvalGateFrom(ctx)
	if gate != nil {
		if allow, note := gate.await(ctx, c.ID, c.Name, mcpsrv.ToolLevel(c.Name), args, outsideAbs != "", outsideNote); !allow {
			rec.Denied = true
			rec.Error = "未获批准，未执行"
			rec.Result = aiApprovalRejectText(c.Name, note)
			if seen != nil {
				seenMu.Lock()
				seen[key] = rec
				seenMu.Unlock()
			}
			return rec, rec.Result
		}
	}

	// 越界的读取必须有一次明确的「允许」。没有闸门的执行者（子 agent）走不到确认这一步，
	// 所以直接拒绝：无人值守的循环不该自己决定跨目录。
	if outsideAbs != "" {
		if gate == nil {
			rec.Denied = true
			rec.Error = "未获批准，未执行"
			rec.Result = "未执行 " + c.Name + "：" + outsideAbs + " 不在当前目录内，" +
				"这个执行上下文无法向用户确认权限。请只用当前目录里的内容，或让用户把该目录设为工作目录。"
			if seen != nil {
				seenMu.Lock()
				seen[key] = rec
				seenMu.Unlock()
			}
			return rec, rec.Result
		}
		// 只放行这一次调用：_allow 跟着 args 走，工具执行完就没了。
		args["_allow"] = []string{filepath.Dir(outsideAbs)}
	}

	started := time.Now()
	// CallAgentContext 而不是 Call：内置 AI 要能用 agent 专用工具（改文件、跑命令、抓网页），
	// 而 spawn_agent 这类会跑长活儿的工具还需要 ctx 才能被用户点停止打断。
	// 等级门与单工具开关在 callTool 里照样生效，所以「面板关掉 = AI 也用不了」不变。
	val, cerr := callToolWithTimeout(ctx, c.Name, args, toolTimeoutFor(c.Name))
	rec.Ms = time.Since(started).Milliseconds()

	// diff 先摘出来，再序列化结果给模型：
	// 模型刚写过这段内容，把 diff 再塞回它的上下文纯属浪费 token
	// （一次 file_edit 的 diff 可能几百行，而对它没有任何新信息）。
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
	rec.Result = truncateToolResult(val, text, c.Name)
	rec.Output = toolOutputText(c.Name, val)
	rec.Added, rec.Removed = toolDiffLines(val)
	// 产物路径（file_write / file_edit）：回答产物条与文件预览的依据。
	rec.Path = toolResultPath(c.Name, val)
	// 编排类工具（spawn_agent）额外带回它建的 run/node：前端据此在这条回复下面
	// 挂出 run 树。其余工具走不到这里，字段留空、落库时被 omitempty 省掉。
	if al, ok := val.(agentLinked); ok {
		rec.RunID, rec.NodeID = al.AgentLink()
	}
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
	return rec, rec.Result
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
func callToolWithTimeout(ctx context.Context, name string, args map[string]any, timeout time.Duration) (any, error) {
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
		v, e := mcpsrv.CallAgentContext(tctx, name, args)
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
func truncateToolResult(val any, text, tool string) string {
	n := len([]rune(text))
	if n <= aiToolResultMaxRune {
		return text
	}
	// 纯文本结果（工具直接返回字符串）不走结构压缩：那一趟会把它 marshal 成
	// 带引号的 JSON 字符串，模型看到的是多了一层转义的正文。
	_, plainText := val.(string)
	if val != nil && !plainText {
		if shrunk, ok := shrinkToolJSON(val, aiToolResultMaxRune); ok {
			logger.W("[ai-tool] %s 结果过长（%d 字），已按字段压缩到 %d 字",
				tool, n, len([]rune(shrunk)))
			return shrunk
		}
	}
	logger.W("[ai-tool] %s 结果过长（%d 字），已截断喂给模型；头部：%s", tool, n, logger.Trunc(text, 300))
	head := string([]rune(text)[:aiToolResultMaxRune])
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
func compactLoopMessages(messages []map[string]interface{}, budget int) int {
	if budget <= 0 {
		budget = 24000
	}
	if estimateLoopTokens(messages) <= budget {
		return 0
	}
	// 先算出最后一轮 tool 消息的起点：它和它之后的一律不动。
	lastRoundStart := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "tool" {
			continue
		}
		// 走到一条带 tool_calls 的 assistant，说明它之后那批 tool 就是最后一轮。
		if _, ok := messages[i]["tool_calls"]; ok {
			lastRoundStart = i + 1
			break
		}
	}

	folded := 0
	for i := 0; i < lastRoundStart && i < len(messages); i++ {
		m := messages[i]
		if m["role"] != "tool" {
			continue
		}
		s, ok := m["content"].(string)
		if !ok || len([]rune(s)) <= aiToolFoldRune {
			continue
		}
		m["content"] = string([]rune(s)[:aiToolFoldRune]) +
			fmt.Sprintf("\n\n[较早的工具结果已折叠：原文 %d 字，只保留前 %d 字。若确实需要完整内容，请重新调用该工具并缩小范围。]", len([]rune(s)), aiToolFoldRune)
		folded++
		if estimateLoopTokens(messages) <= budget {
			break
		}
	}
	if folded > 0 {
		logger.I("[ai] 循环内上下文超预算，已折叠 %d 条较早的工具结果", folded)
	}
	return folded
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
