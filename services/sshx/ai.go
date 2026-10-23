package sshx

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/aiutil"
	"quickdock/internal/db"
	sshcore "quickdock/internal/sshx"
	"quickdock/services"
	"quickdock/services/ai"
)

// SSH 场景的 AI 问答：把「当前会话 + 终端屏显」交给模型，拿回可执行命令。
//
// 设计取舍（三条，都是踩过坑才定下来的）：
//
//  1. **只给命令，不自动执行。** 用户复制粘贴运行，模型的输出到卡片为止。
//     这既是产品形态要求，也是安全底线——自动执行一旦出错无法挽回。
//
//  2. **上下文由前端传屏显文本，不由后端取 Replay。** Replay 是原始字节，
//     里面全是 ANSI 转义序列和颜色码，直接喂模型只会得到一堆乱码；
//     而 xterm buffer 里的纯文本前端本来就有，不用多跑一趟。
//
//  3. **多轮对话不落库。** 走 AIService.AIAsk（一次性、无会话），
//     不污染用户的 AI 聊天历史。前端自己维护本次面板内的上下文即可。
//
// 围栏解析与风险判定已抽到 internal/aiutil（公共层），本文件只保留
// **SSH 特有**的部分：系统提示词、环境约束、风险规则表。判定逻辑本身
// 在公共层只有一份实现，新增场景域直接复用，不要在这里另写一套。
const (
	// aiScreenMaxRune 终端屏显送入模型的长度上限。
	//
	// 取 3000 而非更多：屏显是滚动窗口的尾巴，再长通常也是无关历史。
	// 同时它在成本上是第一道闸门——屏显每多 1000 rune 就多一份 token 费用，
	// 而超过 3000 rune 后模型对"刚才那段报错"的注意力反而被稀释。
	aiScreenMaxRune = 3000

	// aiSelectionMaxRune 用户选中输出的送入上限。
	//
	// 远小于屏显上限：既然是"特意圈出来的几行"，超过这个长度基本是误操作全选，
	// 喂进来的大量无关行反而会淹没真正被关注的报错。
	aiSelectionMaxRune = 2000

	// aiQuestionMaxRune 用户单次提问长度上限。
	aiQuestionMaxRune = 500

	// aiTimeoutSec 一次问答的超时。运维问题不需要长思考，30s 足够。
	aiTimeoutSec = 30

	// aiHistoryMaxTurns 前端多轮时最多带回的历史轮次（兜底裁剪用）。
	aiHistoryMaxTurns = 6
)

// sshAiSystemPrompt 是 SSH 场景的系统提示词。
//
// 写法上的两个要点：
//   - **要求模型自己声明前提**，而不是假装确定。用户是在生产机上敲命令，
//     一个基于错误假设的"确定"答案比明说"我需要你确认版本"的答案危险得多。
//   - **命令放代码块围栏里**，因为前端要靠围栏解析出命令卡片做复制，
//     解析不出来就退化成纯文本展示（见 aiutil.ExtractSegments）。
//
// 围栏用 mdFence 拼接而非直接写在原始字符串里：反引号无法在 Go 的原始字符串
// （反引号界定）中转义，直接写会提前闭合字符串并让后续中文变成语法错误。
const mdFence = "```"

// sshAiSystemPrompt 用 +拼接而非单一原始字符串，原因见 mdFence。
var sshAiSystemPrompt = strings.Join([]string{
	"你是一名资深 Linux/Unix 运维工程师助手，服务于一个 SSH 终端工具。",
	"",
	"你的输出会被解析成命令卡片供用户复制，因此必须遵守以下格式：",
	"",
	"1. 所有可执行命令放进 " + mdFence + "bash 代码块，一条命令一个块。",
	"2. 代码块之外用中文简短说明：这条命令做什么、有什么风险、需要什么前提。",
	"3. 不要在代码块里写注释占位符（如 <你的路径>），不确定的地方用真实可运行的探测命令代替。",
	"4. 禁止臆造：若缺少信息（发行版、路径、进程名、服务名），",
	"   先给一条只读的探测命令（如 uname -a、cat /etc/os-release、ps aux | grep xxx），",
	"   并说明拿到结果后再来问。",
	"5. 涉及删除、覆盖磁盘、改防火墙、改系统服务、重启的命令，必须显式标注风险等级与不可逆后果。",
	"6. 不要输出交互式全屏程序（vim、top、less），用户无法在此确认。",
	"",
	"终端屏显中若包含报错，请先解释原因再给修复命令。",
}, "\n")

// sshEnvLabel 主机环境取值到中文标签的映射。
//
// 用中文而不是把 prod/staging/test 原样拼进主机描述：一行中英混排的附带
// 信息模型几乎不会作出反应，而「生产环境」这四个字会实实在在改变它的保守程度。
var sshEnvLabel = map[string]string{
	db.SshEnvProd:    "生产",
	db.SshEnvStaging: "预发",
	db.SshEnvTest:    "测试",
}

// sshEnvGuidance 针对不同环境的额外约束，拼进 prompt 的连接信息小节。
//
// 为什么必须显式写进提示词而不是只靠风险规则表兜：规则表只能拦住**已被收录**
// 的写法，而真实的生产事故绝大多数来自表里没有的组合——sed -i 改错配置、
// docker rm 删错容器、一条没写完的 DELETE。所以要约束的是模型的**默认行为**：
// 先探查再变更、变更必须带回滚，而不是等命令生成后再去匹配词表。
func sshEnvGuidance(env string) string {
	switch env {
	case db.SshEnvProd:
		return "这是【生产环境】。请保守输出：默认先给只读探查命令确认现状；" +
			"任何写入、删除、重启、改配置的命令都要先写明影响范围、不可逆之处与回滚方式。"
	case db.SshEnvStaging:
		return "这是【预发环境】，可以给出变更命令，但仍需说明影响范围。"
	default:
		// 测试/未标注环境不加约束：这里本来就是用来试错的，额外的保守要求
		// 只会让模型给出一堆"请先确认"的废话。
		return ""
	}
}

// sshAiDangerRule 危险命令规则。
//
// 为什么不写一个通用的"危险词表"给全局用：SSH 场景的危害评估必须结合
// "这条命令在远程生产机上跑"这个前提，比如 `systemctl restart nginx`
// 在本地无害、在生产上要停服务。这类语义没法靠通用词表覆盖。
// **规则表因此留在 SSH 域内**，公共层只提供判定机制（aiutil.RiskRules.Eval）。
//
// 规则表的**顺序不代表优先级**：判定取最高等级的那条（见 aiutil.RiskRules.Eval），
// 因此新增规则可以直接追加，不必担心插错位置导致等级被降级。
//
// 表是**有限收录**而非穷举：它覆盖的是不可逆或影响整机/服务的常见写法，
// 拦不住所有危险命令。所以生产环境还有一层提示词约束（sshEnvGuidance），
// 以及「高风险不给一键执行」这条界面底线——三层各自独立，不互为替代。
var sshAiDangerRule = aiutil.RiskRules{
	{Pattern: regexp.MustCompile(`\brm\s+(-[a-zA-Z]*\s+)*-[a-zA-Z]*[rR][a-zA-Z]*f|\brm\s+-[a-zA-Z]*f[a-zA-Z]*[rR]|\brm\s+-fr\b|\brm\s+-rf\b`), Level: "high", Note: "递归强制删除，不可恢复，执行前务必确认路径"},
	{Pattern: regexp.MustCompile(`\bdd\s+if=|\bdd\s+of=/dev/`), Level: "high", Note: "直接写裸设备，数据会被覆盖且无法恢复"},
	{Pattern: regexp.MustCompile(`\bmkfs\b|\bfdisk\b|\bparted\b`), Level: "high", Note: "会重写分区表或格式化文件系统，磁盘数据全部丢失"},
	{Pattern: regexp.MustCompile(`>\s*/dev/(sd|nvme|hd|vd)`), Level: "high", Note: "重定向覆盖磁盘设备，破坏分区"},
	{Pattern: regexp.MustCompile(`\bchmod\s+(-R\s+)?777\b|\bchmod\s+.*\s+a\+rwx`), Level: "medium", Note: "过度放开的权限，可能引入越权访问"},
	{Pattern: regexp.MustCompile(`\bchown\s+-R\b`), Level: "medium", Note: "批量改属主，范围过大会影响整机文件权限"},
	{Pattern: regexp.MustCompile(`\b(iptables|ip6tables|nft)\b.*\s(-F|--flush|-X|-P)\b`), Level: "high", Note: "清空或改动防火墙规则，可能让自己失联"},
	{Pattern: regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff|init\s+0)\b`), Level: "high", Note: "会立刻中断系统，远端操作等于断线"},
	{Pattern: regexp.MustCompile(`\bkill(all)?\s+-9\s+-1\b|\bpkill\s+-9\b`), Level: "high", Note: "强杀范围内所有进程，可能杀掉 SSH 服务本身导致断线"},
	{Pattern: regexp.MustCompile(`\bcurl\b[^|]*\|\s*(ba)?sh\b|\bwget\b[^|]*\|\s*(ba)?sh\b`), Level: "high", Note: "下载并直接执行远端脚本，内容不可预知"},
	{Pattern: regexp.MustCompile(`\b(rm|mv|truncate)\b[^|;]*\s+[^\s;]*/(etc|usr|bin|sbin|boot|lib)\b`), Level: "high", Note: "改动系统关键目录，可能让系统无法启动"},
	{Pattern: regexp.MustCompile(`>\s*\/(etc|usr|bin|sbin)\/\S+`), Level: "high", Note: "覆盖系统文件，出错后无法通过命令自行修复"},
	{Pattern: regexp.MustCompile(`\b(systemctl|service)\s+(stop|restart|disable)\b`), Level: "medium", Note: "会影响服务可用性，生产环境请确认维护窗口"},
	{Pattern: regexp.MustCompile(`\biptables\b.*(-A|-I)\b.*\bDROP\b|\bufw\b\s+(disable|default\s+deny)\b`), Level: "medium", Note: "新增阻断规则或关闭防火墙，可能导致远程失联"},

	// ---- 以下为第二批：容器 / 编排 / 数据库 / 包管理 / 版本控制 / 原地改写 ----
	//
	// 第一批只覆盖了"裸机运维"的经典危险动作，而现在的现场命令大量落在
	// 容器与数据库上，一条 `kubectl delete` 或 `DROP TABLE` 的破坏力不亚于 rm -rf，
	// 此前却连一条风险说明都没有。这类命令正是"模型给了、用户顺手一点就跑"的重灾区。
	{Pattern: regexp.MustCompile(`\bkubectl\b.*\b(delete|drain|scale\s+--replicas=0)\b`), Level: "high", Note: "删除或驱逐 Kubernetes 资源，对应服务会立即中断"},
	{Pattern: regexp.MustCompile(`\bdocker\s+rm\b|\bdocker\s+rmi\b`), Level: "high", Note: "删除容器或镜像，容器内未持久化的数据会一并丢失"},
	{Pattern: regexp.MustCompile(`\bdocker\s+(system|volume|image|container)\s+prune\b`), Level: "high", Note: "批量清理未使用的资源，清理范围往往超出预期"},
	{Pattern: regexp.MustCompile(`\bshred\b`), Level: "high", Note: "安全擦除文件内容，擦除后无法恢复"},
	{Pattern: regexp.MustCompile(`\bDROP\s+(DATABASE|TABLE)\b|\bTRUNCATE\s+(TABLE\s+)?\w|\bDELETE\s+FROM\s+\w`), Level: "high", Note: "数据库删除/清空，数据不可恢复，执行前先确认库名与条件"},
	{Pattern: regexp.MustCompile(`\bgit\s+reset\s+--hard\b|\bgit\s+clean\s+-[a-zA-Z]*[fd]`), Level: "high", Note: "丢弃未提交的改动或未跟踪文件，无法找回"},
	{Pattern: regexp.MustCompile(`\bgit\s+push\s+(-f|--force)\b`), Level: "high", Note: "强制推送会覆盖远端提交历史，他人基于旧历史的改动会丢"},
	{Pattern: regexp.MustCompile(`^\s*(sudo\s+)?rm\b[^\n]*\*`), Level: "medium", Note: "通配符删除，匹配范围可能比你以为的更大"},
	{Pattern: regexp.MustCompile(`\bsed\s+-i\b|\bperl\s+-[a-zA-Z]*i\b`), Level: "medium", Note: "原地改写文件内容，建议先备份或先去掉 -i 预览结果"},
	{Pattern: regexp.MustCompile(`\b(apt|apt-get|yum|dnf|apk|zypper)\b[^\n]*\b(remove|purge|erase|autoremove)\b`), Level: "medium", Note: "卸载软件包，可能连带移除依赖并影响其它服务"},
	{Pattern: regexp.MustCompile(`\bcrontab\s+-r\b`), Level: "medium", Note: "清空当前用户的全部定时任务，不只是删掉一条"},
	{Pattern: regexp.MustCompile(`\buserdel\b|\bgroupdel\b`), Level: "medium", Note: "删除用户或用户组，其名下文件会变成无主状态"},
	{Pattern: regexp.MustCompile(`\bchmod\s+-R\b`), Level: "medium", Note: "递归修改权限，范围过大会影响整机可访问性"},
}

// SshAiAskRequest SSH 场景 AI 问答入参。
type SshAiAskRequest struct {
	// SessionID 目标会话。用于取主机信息（让模型知道自己在跟哪台机器说话）。
	SessionID string `json:"sessionId"`
	// Question 用户问题。
	Question string `json:"question"`
	// ScreenText 终端当前屏显（由前端从 xterm buffer 取出的纯文本）。
	ScreenText string `json:"screenText"`
	// Selection 用户在终端里选中的那一段输出（右键「问 AI」带来）。
	// 与 ScreenText 的区别：屏显是"整屏画面"，选区是用户**特意圈出来**的部分，
	// 它表达的是"我关心的就是这几行"，因此在 prompt 里优先于屏显、且不参与屏显的裁剪。
	Selection string `json:"selection"`
	// History 可选的历史问答，用于多轮追问。每项取 text 字段作为纯文本。
	History []string `json:"history"`
	// StreamID 流式标识。非空时后端会经 Wails 事件 ai:ask-delta 逐段推送增量，
	// 前端按此 ID 区分不同轮次；空则退化为一次性返回（插件等无前端通道的调用方）。
	StreamID string `json:"streamId"`
}

// SshAiAskResult SSH 场景 AI 问答返回。
type SshAiAskResult struct {
	// Answer 模型返回的完整正文（含 ```bash 围栏），前端据此渲染文字说明部分。
	Answer string `json:"answer"`
	// Commands 从正文里解析出的命令，供前端渲染成可复制的卡片。
	//
	// 保留这个扁平字段是为了兼容：分段渲染之外的调用方（如插件）仍按扁平列表取命令。
	Commands []aiutil.ScenarioCommand `json:"commands"`
	// Segments 按模型原始顺序排列的内容分段，前端据此交错渲染说明与命令。
	Segments []aiutil.ScenarioSegment `json:"segments"`
	// Model 实际使用的模型名。
	Model string `json:"model"`
	// HasRisk 是否存在高风险命令，前端据此在面板顶部显示警示条。
	HasRisk bool `json:"hasRisk"`
	// ElapsedMs 耗时毫秒。
	ElapsedMs int64 `json:"elapsedMs"`
}

// Ask 就当前 SSH 会话向 AI 提问。
//
// 只做「组装上下文 + 调用 + 解析命令」，不碰 SSH 连接本身：
// 不执行任何命令、不写任何数据，只读会话元信息。
func (s *SshService) Ask(req SshAiAskRequest) *services.ApiResult {
	if s.App.AskAIFn == nil {
		return failCode(CodeInternal, "AI 能力不可用")
	}

	question := strings.TrimSpace(req.Question)
	if question == "" {
		return failSsh(errInvalidInput)
	}
	if n := len([]rune(question)); n > aiQuestionMaxRune {
		question = string([]rune(question)[:aiQuestionMaxRune])
	}

	// 会话必须真实存在，否则说明前端传了个过期 sessionId。
	// 这里只校验不建立连接：AI 问答是纯辅助能力，不该在会话已断开时偷偷重连。
	hostLabel := ""
	hostEnv := ""
	if req.SessionID != "" {
		if _, ok := s.manager.Get(req.SessionID); !ok {
			return failSsh(sshcore.ErrSessionNotFound)
		}
		if hostID := s.ownerOf(req.SessionID); hostID != "" {
			if h, err := s.App.DB.GetSshHost(hostID); err == nil && h != nil {
				hostLabel = describeHost(h)
				// 环境单独取一份：主机描述里的"（生产环境）"只是给模型看的背景，
				// 而 hostEnv 要驱动一条**行为约束**（见 sshEnvGuidance），两者用途不同。
				hostEnv = h.Env
			}
		}
	}

	prompt := buildSshAiPrompt(hostLabel, hostEnv, req.ScreenText, question, req.History, req.Selection)

	// 流式：把增量经 Wails 事件推给前端。批量按 ~40 rune 做一次 IPC，
	// 避免逐 token 高频调用把窄带 Wails 事件总线打满（SSH 答案通常不长，但翻译/摘要会）。
	// 缓冲区在 AskAIFnStream 返回后再 flush 一次，确保结尾片段不丢。
	var deltaBuf strings.Builder
	emitDelta := func() {
		if deltaBuf.Len() == 0 {
			return
		}
		if s.App.App() != nil {
			s.App.App().Event.Emit("ai:ask-delta", map[string]interface{}{"id": req.StreamID, "delta": deltaBuf.String()})
		}
		deltaBuf.Reset()
	}
	var onToken func(string)
	if req.StreamID != "" {
		onToken = func(delta string) {
			deltaBuf.WriteString(delta)
			if deltaBuf.Len() >= 40 {
				emitDelta()
			}
		}
	}

	start := time.Now()
	text, model, err := s.App.AskAIFnStream(sshAiSystemPrompt, prompt, aiTimeoutSec, onToken)
	if onToken != nil {
		emitDelta()
	}
	if err != nil {
		return services.Fail(err)
	}

	commands := aiutil.ExtractCommands(text, sshAiDangerRule)
	segments := aiutil.ExtractSegments(text, sshAiDangerRule)
	hasRisk := false
	for _, c := range commands {
		if c.Risk == "high" {
			hasRisk = true
			break
		}
	}

	return services.Ok(SshAiAskResult{
		Answer:    text,
		Commands:  commands,
		Segments:  segments,
		Model:     model,
		HasRisk:   hasRisk,
		ElapsedMs: time.Since(start).Milliseconds(),
	})
}

// describeHost 生成主机描述，标注环境以提醒模型（也提醒用户）。
//
// 环境用中文标签（见 sshEnvLabel）而不是数据库里的英文原值：
// 一行「root@10.0.0.1（prod环境）」对模型来说只是一段背景噪声，
// 而「（生产环境）」才是它据此收紧输出的信号。
func describeHost(h *db.SshHost) string {
	if h == nil {
		return ""
	}
	label := h.User + "@" + h.Host
	if h.Port > 0 && h.Port != 22 {
		label += ":" + strconv.Itoa(h.Port)
	}
	if h.Env != "" {
		if l, ok := sshEnvLabel[h.Env]; ok {
			label += "（" + l + "环境）"
		} else {
			// 未收录的取值（老数据或将来新增）原样保留，
			// 好过静默丢掉环境信息让模型以为这是台无关紧要的机器。
			label += "（" + h.Env + "环境）"
		}
	}
	return label
}

// buildSshAiPrompt 组装用户提示词。
//
// 结构固定为「环境 → 屏显 → 历史 → 本次问题」。
// 顺序有两层讲究：
//   - 注意力：屏显贴近问题、问题放最末尾，让模型优先看当前实际状态，
//     而不是被前面的历史对话带偏。
//   - 但 AIAsk 超预算时是**从头截断**，末尾的"本次问题"最容易被砍掉。
//     所以顺序的好處与超预算的风险是冲突的，必须由预算机制兜住（见下）。
//
// 预算：调用 services/ai.AskMaxUserRune 而不是自己定数字。
// 优先级从高到低：**本次问题 > 屏显 > 历史**。
// 原因很直接——问题丢了 AI 答非所问，屏显丢了 AI 看不见现场，
// 而历史少了最多让多轮追问没那么连贯。历史超预算时按"丢最旧的"逐条剔除。
//
// hostEnv 与 hostLabel 分开传：后者是展示用的主机串，前者驱动行为约束。
// 合成一个参数的话，将来想在描述里加别的信息就会把环境判定也一起改坏。
func buildSshAiPrompt(hostLabel, hostEnv, screenText, question string, history []string, selection string) string {
	// 连接信息 + 环境约束，永远保留：这是模型判断"该有多保守"的唯一依据，
	// 屏显和历史再长也不能把它挤掉（否则生产机上的输出会变得和测试机一样随意）。
	var head strings.Builder
	if hostLabel != "" {
		head.WriteString("当前连接的主机：" + hostLabel + "\n")
	} else {
		head.WriteString("当前连接的主机：未知（会话未建立）\n")
	}
	if g := sshEnvGuidance(hostEnv); g != "" {
		head.WriteString(g + "\n")
	}

	sections := []aiutil.Section{
		// Pinned：不是"细节"，而是回答口径的前提。
		{Title: "连接信息", Body: head.String(), Pinned: true},
	}

	// 选区是用户主动圈出的重点，单独成节且不参与屏显裁剪。
	if sel := strings.TrimSpace(selection); sel != "" {
		// 上限比屏显严格得多：既然是"圈出来的几行"，超过这个长度基本是误操作全选，
		// 喂进来的大量无关行反而会淹没真正被关注的那段报错。
		sel = aiutil.TrimRunes(sel, aiSelectionMaxRune, false) + "\n…"
		sections = append(sections, aiutil.Section{
			Title:  "用户选中的输出（这是提问所指的部分）",
			Body:   mdFence + "\n" + sel + "\n" + mdFence,
			Pinned: true,
		})
	}

	if screen := stripAnsi(screenText); strings.TrimSpace(screen) != "" {
		screen = aiutil.TrimRunes(screen, aiScreenMaxRune, true)
		sections = append(sections, aiutil.Section{
			Title: "终端当前屏显（用户正在看的画面）",
			Body:  mdFence + "\n" + screen + "\n" + mdFence,
			// 屏显保留**尾部**（最新画面），最多占一半预算：
			// 另一半留给历史与多轮连贯性，屏显吃满会让追问彻底断片。
			KeepTail: true,
			MaxShare: 0.5,
		})
	}

	// 预算分配、历史打包、问题保底都在 aiutil.BuildPrompt 里（各域共用一份实现）。
	return aiutil.BuildPrompt(aiutil.Prompt{
		Max:             ai.AskMaxUserRune,
		Sections:        sections,
		History:         history,
		HistoryTitle:    "之前的对话",
		HistoryItemMax:  800,
		HistoryMaxTurns: aiHistoryMaxTurns,
		Task:            question,
		TaskTitle:       "本次问题",
	})
}

// stripAnsi 去掉 ANSI 转义序列。
//
// 前端理论上已经给出纯文本，这里仍做一遍清洗：xterm buffer 取出的内容在某些
// 终端下仍会带颜色标记（SGR），而 ESC 字符进到 JSON 里会让模型读到噪声，
// 也可能撑大请求体积。
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*(\x07|\x1b\\)|\x1b[()][A-B0-9]`)

func stripAnsi(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	s = ansiRe.ReplaceAllString(s, "")
	// 去掉退格与回车覆盖的残留：终端重绘留下的重复行会让模型误读结构。
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
