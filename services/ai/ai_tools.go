package ai

// 内置 agent：让 AI 页的对话流自己调用 MCP 工具集。
//
// 与「外部 AI 通过 MCP 调宿主」是**同一份工具、同一套闸门**（internal/mcp 的等级门
// 与单工具开关），区别只在调用方：这里是宿主自己的 AI 页，那里是 dsh / 其他客户端。
// 不另设一套开关 —— 两套开关迟早会出现「面板里关了，内置 AI 还能调」，
// 而那种时候没人说得清应该信哪一个。
//
// 分层的职责：
//   - 本文件：工具规格转换、执行、结果整形、上下文压缩
//   - ai_chat.go：从 SSE 分片里把 tool_calls 拼出来（协议层）
//   - ai_stream.go：多轮循环、计费累加、事件下发、落库
//
// 这条链上唯一不可逆的语义在 ai_stream.go：**工具调用轮次里的 assistant 文本
// 也会流给用户看**。模型常常先说「我查一下」再调工具，那段话不该被吞掉。

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	aitools "quickdock/services/ai/tools"
)

const (
	// aiToolMaxRounds 一次提问里最多允许几轮工具调用。
	//
	// 8 轮够覆盖「查列表 → 读细节 → 交叉确认 → 回答」这类查询链路，但**不够分段写长文档**：
	// 实测让模型整理一份几十节的教程时，它每轮 file_edit 填一节，8 轮用完才写到第十八节，
	// 而收尾轮只能空手总结「还差什么」。12 轮覆盖了这类「边写边改」的活儿。
	// 再多就基本是模型在原地打转，而每多一轮都是全额计费（上下文要整份重发一遍）。
	// 到顶后不是硬截断：见 ai_stream.go 里「轮次用尽」的处理——会再要一次
	// 不带工具的收尾回答，否则用户看到的是半句话加一张卡片。
	aiToolMaxRounds = 12

	// aiToolResultMaxRune 单个工具结果送去模型的字符上限。
	// file_grep / log_read 这类结果可以是几十 KB，一条就能把上下文烧掉一半。
	// 超出部分不丢：完整结果写日志，只把头部递给模型，并在末尾说明被截断。
	aiToolResultMaxRune = 12000

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
)

// aiToolCall 一次工具调用。
//
// Args 保留模型给出的 **JSON 字符串原文**：解析失败时要原样回给模型看
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
	// RunID / NodeID 这次调用所属的编排 run 与节点（只有 spawn_agent 这类
	// 会派活的工具才有）。落库而不是让前端解析结果文本：结果可能被截断，
	// 一旦截在中间就不是合法 JSON，前端只能整块放弃。
	RunID  string `json:"run_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
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
// 需要注意门只对**宿主工具**（mcpsrv.Global 里的东西）成立：环境、笔记、待办、插件
// 这些直接读写用户数据的能力，在普通对话里被顺手指令去执行是设计错误——
// 用户问一句常识，AI 顺手停了他的服务。教程模式的定位本来就是「通过对话操作这个软件」，
// 工具在那里才是本职。
//
// agent 工具（services/ai/tools 里的抓网页、读目录、看 git、改文件、跑命令）不受这道门
// 约束：它们服务于「在某个目录里干活」，和「学这个软件怎么用」是两件事。它们自己的闸
// 在工具内部——写操作受 fsguard 的写白名单限制（没绑定目录直接拒绝）、高危命令在
// run_command 里被拒——不靠「只在教程模式开放」兜底。
const aiGateMode = "tutorial"

// aiGeneralCategories 宿主工具里**所有模式**都能用的分类。
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
	return aiToolsPayloadFor(mode, nil)
}

// aiToolsPayloadFor 同 aiToolsPayload，额外按 role 收窄（filter 为 nil 表示不收窄）。
func aiToolsPayloadFor(mode string, filter *ToolFilter) []map[string]any {
	fns := mcpsrv.ExposedFunctions()
	out := make([]map[string]any, 0, len(fns))
	for _, f := range fns {
		if !aiToolVisibleInMode(mode, f.Category, f.AgentOnly) {
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
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        f.Name,
				"description": f.Description,
				"parameters":  f.Parameters,
			},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	toolHintGeneral = "\n\n[可用工具]\n" +
		"**网页**：web_fetch 打开网址读正文，web_links 取页面里的链接，web_head 只看状态码与响应头。" +
		"查文档、看报错页、确认链接里写了什么时用它们，不要凭印象编造网页内容。打不开（403 / 超时 / 需要登录 / 只返回脚本）时如实说明，" +
		"把链接给用户让他自己看，不要假装读到了，也不要反复重试同一个地址。\n" +
		"**代码**：dir_list 看目录结构，file_read 读文件（行号在 content 里），file_grep 按内容搜，" +
		"git_status / git_diff 看改动与差异，file_write 新建文件、file_edit 精确替换、run_command 跑命令，" +
		"check_workdir 一次问清「这是什么项目、该怎么跑」。\n" +
		"- 要产出文件（写文档、建配置、生成脚本）就用 file_write，**不要**在 run_command 里 echo / Set-Content / " +
		"node -e 造文件——引号、换行、编码在那条路上都容易出错，而且用户看不出你写了什么。\n" +
		"- 改已有文件的流程是：check_workdir 或 dir_list 先看 → file_read 看清原文 → file_edit 精确替换 → git_diff 确认。" +
		"不要整文件重写，也不要在没读过的文件上猜着改；file_edit 报「没找到要替换的内容」时先 file_read 核对原文再改。\n" +
		"- **没有可用工作目录时写操作会被拒绝**：这时不要换个路径重试（工具的白名单不会因为" +
		"换个写法就放开），而是让用户把这个任务建在一个有效的空间里。\n" +
		"- run_command 只用来做必须走命令行的事（装依赖、跑测试、构建）。高危命令（删根目录、git reset --hard、强推、发版）会被拒绝，" +
		"这时把命令写给用户，让他在终端里自己执行。\n" +
		"- 查环境状态、定时任务、监控、待办笔记这些系统能力在「使用教程」模式里才有；用户在这边问起时，告诉他切到那个模式。\n"

	// toolHintExhausted 工具轮次用尽后的 system 说明。
	//
	// 这段存在的唯一原因是一个实测出来的坏结果：轮次用尽时工具被撤下（请求里不再有
	// tools），但 system 里那段「可用工具」如果还留着，模型会认为工具仍然可用，
	// 于是**把调用写成正文**——DeepSeek 会吐出一串 `<｜DSML｜invoke>` 标记
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
	if mode == aiGateMode {
		return toolHintSystem
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
// base 必须是**没贴过任何提示**的原文，由调用方在首次组装后取一次并存着
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
	}
	if runtime.GOOS == "windows" {
		hint += "run_command 由 **cmd.exe** 执行（不是 PowerShell）：命令按 cmd 语法写，" +
			"不要为了确认 shell 或找 node / powershell 先跑探测命令——写文件用 file_write 就够了。\n"
	}
	return hint
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
// 单个工具失败**不中断整轮**：把错误文本当结果回给模型，它通常会换参数重试
// 或改用别的工具。只有 ctx 被取消（用户点了停止）才提前退出。
//
// seen 是「本次提问内已经成功执行过的调用」（key 见 toolCallKey），由调用方在
// 一次提问开始时建好、跨轮复用：**只在一次提问内有效**，新提问重新建空的——
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

	// 工作目录由宿主注入，不信任模型传的值：这个键不在任何工具的 schema 里，
	// 模型既看不见也无从伪造；即使它自己编一个 _cwd，也会被这里覆盖。
	// 没绑定目录时不注入，工具退回「绝对路径 + 读白名单」那套逻辑。
	if cwd != "" {
		args["_cwd"] = cwd
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
			rec.Dedup = true
			rec.Result = fmt.Sprintf(
				"与第 %d 轮执行过的 %s 参数完全相同，已跳过、没有重复执行。上次的结果如下（无需再调）：\n%s",
				prev.Round, prev.Name, prev.Result)
			return rec, rec.Result
		}
	}

	// 审批闸门（见 ai_approval.go）：写类与高危工具在这里停下来等用户点确认。
	//
	// 位置刻意在去重与参数校验之后：
	//   - 去重命中的是「同一轮里第二次一模一样的调用」，那时工具根本没跑，
	//     问用户等于同一件事问两遍；
	//   - 参数坏了的调用执行必然报错，问也没有意义。
	//
	// 被拒绝的调用不写进 seen：拒绝的是这一次，用户下次再看到可能就同意了。
	// 这也意味着模型若原样重试，会再弹一次卡片——那正是「拒绝」应有的效果。
	if gate := approvalGateFrom(ctx); gate != nil {
		if allow, note := gate.await(ctx, c.ID, c.Name, mcpsrv.ToolLevel(c.Name), args); !allow {
			rec.Denied = true
			rec.Error = "未获批准，未执行"
			rec.Result = aiApprovalRejectText(c.Name, note)
			return rec, rec.Result
		}
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
		rec.Error = cerr.Error()
		// 错误用文本回给模型而不是中断：模型据此可以换参数或换工具，
		// 这比让它把整轮回答作废有用得多。
		text = "工具执行失败：" + rec.Error
	}
	rec.Result = truncateToolResult(text, c.Name)
	rec.Added, rec.Removed = toolDiffLines(val)
	// 编排类工具（spawn_agent）额外带回它建的 run/node：前端据此在这条回复下面
	// 挂出 run 树。其余工具走不到这里，字段留空、落库时被 omitempty 省掉。
	if al, ok := val.(agentLinked); ok {
		rec.RunID, rec.NodeID = al.AgentLink()
	}
	if seen != nil && cerr == nil {
		seenMu.Lock()
		seen[key] = rec
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

// callToolWithTimeout 执行一个工具，并给它套上超时外壳。
//
// 为什么只能套外壳而不是真取消：多数工具的 Handler 没有 ctx 参数，工具内部拿不到
// 取消信号（命令类工具会自己结束，读文件类则本就很快）。所以超时返回的那一刻，
// 那次调用仍在后台跑完——它不会再写这条流（结果被丢弃），代价是一次空跑。
// 这点代价换来的是「AI 页永远不会因为一个工具挂住而停在转圈」。
//
// 实现了 HandlerCtx 的工具（spawn_agent）不同：它的取消是真的，内部会随 ctx 停下，
// 不会白烧 token。
//
// 同理，ctx 被取消（用户点停止）时也不等它：直接按「已停止」返回，
// 让本轮剩下的调用快速收尾，而不是让停止按钮看起来没反应。
func callToolWithTimeout(ctx context.Context, name string, args map[string]any, timeout time.Duration) (any, error) {
	type outcome struct {
		val any
		err error
	}
	ch := make(chan outcome, 1) // 缓冲 1：超时后 goroutine 仍能退出，不泄漏
	go func() {
		defer recoverPanic("ai tool " + name)
		v, e := mcpsrv.CallAgentContext(ctx, name, args)
		ch <- outcome{v, e}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.val, r.err
	case <-timer.C:
		logger.W("[ai-tool] %s 执行超过 %v 未返回，已按超时处理", name, timeout)
		return nil, fmt.Errorf("工具 %s 执行超过 %v 仍未返回，已放弃等待（它可能还在后台跑，结果不会再用）", name, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
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
// 用**解析后重新序列化**的参数而不是原始字符串：模型两次给出的 JSON 可能
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
func truncateToolResult(text, tool string) string {
	n := len([]rune(text))
	if n <= aiToolResultMaxRune {
		return text
	}
	logger.W("[ai-tool] %s 结果过长（%d 字），已截断喂给模型；头部：%s", tool, n, logger.Trunc(text, 300))
	head := string([]rune(text)[:aiToolResultMaxRune])
	return head + fmt.Sprintf("\n\n[结果过长，已截断：原文 %d 字，这里只给出前 %d 字。需要更多请缩小范围（比如加 glob 过滤、减少 limit）。]", n, aiToolResultMaxRune)
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
//   - 最近一轮的结果**一定不折叠**：模型正要用它，折了等于让它重查。
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

// truncateKeepLines 按字符截断，但**保留换行**。
//
// 不用 logger.Trunc：它把换行压成空格（日志要一行一条），而工具结果多半是
// 缩进 JSON 或带行号的匹配结果，压成一行就完全没法看了。
func truncateKeepLines(s string, maxRunes int) string {
	r := []rune(s)
	if maxRunes <= 0 || len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…（已截断）"
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
