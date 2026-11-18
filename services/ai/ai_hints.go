package ai

// 两条行为约束提示（WP18 / WP19）。
//
// 为什么写在系统提示而不是只改工具描述：工具描述回答「这个工具能干什么」，
// 而「什么时候该用它」「文件该放哪」是行为约束。模型对工具描述的遵循度
// 明显低于系统提示（见 planHint 的同一段判断）。
//
// 两条都按需追加：没有写权限就别提 quickdock/ 目录，没挂 spawn_agent
// 就别讲派活——system 里每一句都在每轮付费，讲不存在的能力只会让模型
// 对着一个看不见的工具反复重试。

import "strings"

// workdirHint 「辅助文件放哪」的约定（WP18 / 实机反馈 U1）。
//
// 问题：AI 为跑任务写的一次性脚本（.js / .py / .ps1）直接落在项目根，
// 混进 git status，用户得自己分辨哪些是自己的、哪些是 AI 留下的。
//
// 判据刻意是「文件性质」而不是路径：有些任务确实要在 src/ 下新建组件，
// 硬拦成「只能写 quickdock/」会把正当需求挡掉。
const workdirHint = "\n\n[工作文件]\n" +
	"- 临时脚本、中间产物、调试输出一律写到 `.quickdock/tmp/` 子目录里（不存在就先建），" +
	"不要放在项目根或源码目录。\n" +
	"- 只有「项目本身要交付的东西」才用真实路径（改 src/ 下的代码、在指定位置新建文件等）。\n" +
	"- 判断依据是文件性质而不是路径：跑一次就丢的脚本属于 `.quickdock/tmp/`，会被提交的代码属于项目路径。\n" +
	"- 你留在 `.quickdock/tmp/` 里的文件要能被用户找到：收尾时说一句它是什么、能删还是留。\n"

// scaleHint 「任务规模」的判断（WP19 / 实机反馈 U2）。
//
// 问题：spawn_agent 早就挂上了，但模型几乎不用。原描述把它框成
// 「**并行**拆分」——于是「串行分治的大任务」被读成「不适合派」，
// 结果全在主循环里硬跑，轮次与上下文一起涨。
//
// 这里补的是「什么时候值得派」，并把「不要求能并行」写清楚。
const scaleHint = "\n\n[任务规模]\n" +
	"- 超过三步、或要分别看多个目录/多个问题的任务：先 plan_update 拆清单，再决定哪些条目派出去。\n" +
	"- 能独立完成、边界清楚的条目（分头调研、分别排查几个目录、独立审阅几个文件）交给 spawn_agent。" +
	"**串行分治也算**，不要求它们能同时跑。\n" +
	"- 条目之间彼此独立时，在同一轮里**一次性全派出去**（一轮多条 tool_calls），别一次只派一个、等它回来再派下一个——" +
	"「一个查后端、一个查前端」这样的活，应当同一轮派出两个 explore。\n" +
	"- 默认派只读角色（explore / review）。code 角色带写权限，只有用户明确要「另一个执行者动手改」时才用。\n" +
	"- 不派：需要你来判断的取舍、需要跟用户确认的事、一句话就能答完的问题。\n"

// writeToolNames 会写盘的 agent 工具名。用来判断「这条对话准不准动文件」。
var writeToolNames = []string{"file_write", "file_edit", "run_command"}

// hasAnyToolPayload 本次挂给模型的工具里有没有其中任意一个。
func hasAnyToolPayload(tools []map[string]any, names ...string) bool {
	for _, n := range names {
		if hasToolPayload(tools, n) {
			return true
		}
	}
	return false
}

// workdirHintFor 按「有没有工作目录 + 有没有写权限」决定要不要追加提示。
//
// 三个都不满足时返回空串：只读对话里说「把脚本写到 quickdock/」是纯噪音，
// 还可能诱导模型去尝试一个它根本调不到的工具。
func workdirHintFor(cwd string, tools []map[string]any) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	if !hasAnyToolPayload(tools, writeToolNames...) {
		return ""
	}
	return workdirHint
}

// scaleHintFor 只在 spawn_agent 真的挂给模型时才讲派活。
//
// 与 planHint 的同一类问题：讲一个不存在的工具名，模型会对着它反复重试。
func scaleHintFor(tools []map[string]any) string {
	if !hasToolPayload(tools, spawnAgentToolName) {
		return ""
	}
	return scaleHint
}
