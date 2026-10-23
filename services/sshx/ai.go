package sshx

import (
	"regexp"
	"strconv"
	"strings"
	"time"

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
//     解析不出来就退化成纯文本展示（见 extractCommands）。
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
//
// 规则表的**顺序不代表优先级**：判定取最高等级的那条（见 evalRisk），
// 因此新增规则可以直接追加，不必担心插错位置导致等级被降级。
//
// 表是**有限收录**而非穷举：它覆盖的是不可逆或影响整机/服务的常见写法，
// 拦不住所有危险命令。所以生产环境还有一层提示词约束（sshEnvGuidance），
// 以及「高风险不给一键执行」这条界面底线——三层各自独立，不互为替代。
var sshAiDangerRule = []struct {
	// Pattern 匹配命令文本。
	Pattern *regexp.Regexp
	// Level 风险等级：high 不可逆/影响整机，medium 影响服务或数据。
	Level string
	// Note 给用户看的风险说明。
	Note string
}{
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z]*\s+)*-[a-zA-Z]*[rR][a-zA-Z]*f|\brm\s+-[a-zA-Z]*f[a-zA-Z]*[rR]|\brm\s+-fr\b|\brm\s+-rf\b`), "high", "递归强制删除，不可恢复，执行前务必确认路径"},
	{regexp.MustCompile(`\bdd\s+if=|\bdd\s+of=/dev/`), "high", "直接写裸设备，数据会被覆盖且无法恢复"},
	{regexp.MustCompile(`\bmkfs\b|\bfdisk\b|\bparted\b`), "high", "会重写分区表或格式化文件系统，磁盘数据全部丢失"},
	{regexp.MustCompile(`>\s*/dev/(sd|nvme|hd|vd)`), "high", "重定向覆盖磁盘设备，破坏分区"},
	{regexp.MustCompile(`\bchmod\s+(-R\s+)?777\b|\bchmod\s+.*\s+a\+rwx`), "medium", "过度放开的权限，可能引入越权访问"},
	{regexp.MustCompile(`\bchown\s+-R\b`), "medium", "批量改属主，范围过大会影响整机文件权限"},
	{regexp.MustCompile(`\b(iptables|ip6tables|nft)\b.*\s(-F|--flush|-X|-P)\b`), "high", "清空或改动防火墙规则，可能让自己失联"},
	{regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff|init\s+0)\b`), "high", "会立刻中断系统，远端操作等于断线"},
	{regexp.MustCompile(`\bkill(all)?\s+-9\s+-1\b|\bpkill\s+-9\b`), "high", "强杀范围内所有进程，可能杀掉 SSH 服务本身导致断线"},
	{regexp.MustCompile(`\bcurl\b[^|]*\|\s*(ba)?sh\b|\bwget\b[^|]*\|\s*(ba)?sh\b`), "high", "下载并直接执行远端脚本，内容不可预知"},
	{regexp.MustCompile(`\b(rm|mv|truncate)\b[^|;]*\s+[^\s;]*/(etc|usr|bin|sbin|boot|lib)\b`), "high", "改动系统关键目录，可能让系统无法启动"},
	{regexp.MustCompile(`>\s*\/(etc|usr|bin|sbin)\/\S+`), "high", "覆盖系统文件，出错后无法通过命令自行修复"},
	{regexp.MustCompile(`\b(systemctl|service)\s+(stop|restart|disable)\b`), "medium", "会影响服务可用性，生产环境请确认维护窗口"},
	{regexp.MustCompile(`\biptables\b.*(-A|-I)\b.*\bDROP\b|\bufw\b\s+(disable|default\s+deny)\b`), "medium", "新增阻断规则或关闭防火墙，可能导致远程失联"},

	// ---- 以下为第二批：容器 / 编排 / 数据库 / 包管理 / 版本控制 / 原地改写 ----
	//
	// 第一批只覆盖了"裸机运维"的经典危险动作，而现在的现场命令大量落在
	// 容器与数据库上，一条 `kubectl delete` 或 `DROP TABLE` 的破坏力不亚于 rm -rf，
	// 此前却连一条风险说明都没有。这类命令正是"模型给了、用户顺手一点就跑"的重灾区。
	{regexp.MustCompile(`\bkubectl\b.*\b(delete|drain|scale\s+--replicas=0)\b`), "high", "删除或驱逐 Kubernetes 资源，对应服务会立即中断"},
	{regexp.MustCompile(`\bdocker\s+rm\b|\bdocker\s+rmi\b`), "high", "删除容器或镜像，容器内未持久化的数据会一并丢失"},
	{regexp.MustCompile(`\bdocker\s+(system|volume|image|container)\s+prune\b`), "high", "批量清理未使用的资源，清理范围往往超出预期"},
	{regexp.MustCompile(`\bshred\b`), "high", "安全擦除文件内容，擦除后无法恢复"},
	{regexp.MustCompile(`\bDROP\s+(DATABASE|TABLE)\b|\bTRUNCATE\s+(TABLE\s+)?\w|\bDELETE\s+FROM\s+\w`), "high", "数据库删除/清空，数据不可恢复，执行前先确认库名与条件"},
	{regexp.MustCompile(`\bgit\s+reset\s+--hard\b|\bgit\s+clean\s+-[a-zA-Z]*[fd]`), "high", "丢弃未提交的改动或未跟踪文件，无法找回"},
	{regexp.MustCompile(`\bgit\s+push\s+(-f|--force)\b`), "high", "强制推送会覆盖远端提交历史，他人基于旧历史的改动会丢"},
	{regexp.MustCompile(`^\s*(sudo\s+)?rm\b[^\n]*\*`), "medium", "通配符删除，匹配范围可能比你以为的更大"},
	{regexp.MustCompile(`\bsed\s+-i\b|\bperl\s+-[a-zA-Z]*i\b`), "medium", "原地改写文件内容，建议先备份或先去掉 -i 预览结果"},
	{regexp.MustCompile(`\b(apt|apt-get|yum|dnf|apk|zypper)\b[^\n]*\b(remove|purge|erase|autoremove)\b`), "medium", "卸载软件包，可能连带移除依赖并影响其它服务"},
	{regexp.MustCompile(`\bcrontab\s+-r\b`), "medium", "清空当前用户的全部定时任务，不只是删掉一条"},
	{regexp.MustCompile(`\buserdel\b|\bgroupdel\b`), "medium", "删除用户或用户组，其名下文件会变成无主状态"},
	{regexp.MustCompile(`\bchmod\s+-R\b`), "medium", "递归修改权限，范围过大会影响整机可访问性"},
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
}

// SshAiCommand 一条解析出的命令。
type SshAiCommand struct {
	// Text 命令原文（已去掉围栏标记）。
	Text string `json:"text"`
	// Risk 风险等级：high / medium / empty（未识别到风险）。
	Risk string `json:"risk"`
	// Note 风险说明，仅 Risk 非空时有值。
	Note string `json:"note"`
}

// SshAiSegment 回答中的一段内容。
//
// 为什么要分段而不是"一段说明 + 一堆命令"两个字段：
// 模型的输出是**交错**的——先解释、再给命令、再补充说明。
// 若把说明全堆在顶部、命令全放底部，用户会失去"哪条命令对应哪段解释"的对应关系，
// 也看不出模型原本的叙述顺序。分段后前端按顺序渲染，与模型输出一致。
type SshAiSegment struct {
	// Type 段落类型：text 说明文字 / cmd 命令。
	Type string `json:"type"`
	// Text 说明文字原文（Type=text 时有值，保留 markdown 语法交前端渲染）。
	Text string `json:"text"`
	// Commands 命令（Type=cmd 时有值）。
	Commands []SshAiCommand `json:"commands"`
}

// SshAiAskResult SSH 场景 AI 问答返回。
type SshAiAskResult struct {
	// Answer 模型返回的完整正文（含 ```bash 围栏），前端据此渲染文字说明部分。
	Answer string `json:"answer"`
	// Commands 从正文里解析出的命令，供前端渲染成可复制的卡片。
	//
	// 保留这个扁平字段是为了兼容：分段渲染之外的调用方（如插件）仍按扁平列表取命令。
	Commands []SshAiCommand `json:"commands"`
	// Segments 按模型原始顺序排列的内容分段，前端据此交错渲染说明与命令。
	Segments []SshAiSegment `json:"segments"`
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

	start := time.Now()
	text, model, err := s.App.AskAIFn(sshAiSystemPrompt, prompt, aiTimeoutSec)
	if err != nil {
		return services.Fail(err)
	}

	commands := extractCommands(text)
	segments := extractSegments(text)
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
	head.WriteString("## 连接信息\n")
	if hostLabel != "" {
		head.WriteString("当前连接的主机：" + hostLabel + "\n")
	} else {
		head.WriteString("当前连接的主机：未知（会话未建立）\n")
	}
	if g := sshEnvGuidance(hostEnv); g != "" {
		head.WriteString(g + "\n")
	}

	// 选区是用户主动圈出的重点，单独成节且不参与屏显裁剪。
	sel := strings.TrimSpace(selection)
	var selBlock string
	if sel != "" {
		// 上限比屏显严格得多：既然是"圈出来的几行"，超过这个长度基本是误操作全选，
		// 喂进来的大量无关行反而会淹没真正被关注的那段报错。
		if n := len([]rune(sel)); n > aiSelectionMaxRune {
			sel = string([]rune(sel)[:aiSelectionMaxRune]) + "\n…"
		}
		selBlock = "\n## 用户选中的输出（这是提问所指的部分）\n" + mdFence + "\n" + sel + "\n" + mdFence + "\n"
	}

	qBlock := "\n## 本次问题\n" + question
	headStr := head.String() + selBlock

	budget := ai.AskMaxUserRune - len([]rune(headStr)) - len([]rune(qBlock))
	if budget < 0 {
		// 极端：问题本身就超预算。此时只能保留问题的尾部（通常是疑问本体），
		// 连接信息丢弃——模型不知道主机名还能答，不知道问题则完全没法答。
		return qBlock
	}

	// 屏显：先看能给它留多少。
	screen := stripAnsi(screenText)
	var screenBlock string
	if n := len([]rune(screen)); n > 0 {
		if n > aiScreenMaxRune {
			screen = string([]rune(screen)[n-aiScreenMaxRune:])
		}
		// 屏显最多占预算的一半：留出另一半给历史与多轮连贯性。
		limit := budget / 2
		if r := len([]rune(screen)); r > limit {
			// 屏显保留**尾部**（最新画面），与整体裁剪口径一致。
			rs := []rune(screen)
			drop := r - limit
			// 只删整行，避免半行乱码让模型误读。
			tail := string(rs[drop:])
			if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
				tail = tail[i+1:]
			}
			screen = tail
		}
		screenBlock = "\n## 终端当前屏显（用户正在看的画面）\n" + mdFence + "\n" + screen + "\n" + mdFence + "\n"
	}

	// 历史：按剩余预算从**最新往旧**塞，塞不下就丢更旧的。
	rest := budget - len([]rune(screenBlock))
	var kept []string
	if rest > 0 && len(history) > 0 {
		if len(history) > aiHistoryMaxTurns {
			history = history[len(history)-aiHistoryMaxTurns:]
		}
		for i := len(history) - 1; i >= 0; i-- {
			h := strings.TrimSpace(history[i])
			if h == "" {
				continue
			}
			if n := len([]rune(h)); n > 800 {
				h = string([]rune(h)[:800]) + " …"
			}
			line := "- " + strings.ReplaceAll(h, "\n", " ") + "\n"
			cost := len([]rune(line))
			if cost > rest {
				break
			}
			rest -= cost
			kept = append(kept, line)
		}
	}

	var b strings.Builder
	b.WriteString(headStr)
	b.WriteString(screenBlock)
	if len(kept) > 0 {
		b.WriteString("\n## 之前的对话\n")
		for i := len(kept) - 1; i >= 0; i-- {
			b.WriteString(kept[i])
		}
	}
	b.WriteString(qBlock)
	return b.String()
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

// fenceOpenerRe 识别围栏代码块的**开标记**行。
//
// 为什么不用「一个正则匹配整块」：模型输出的围栏有三种不稳定形态——
//   - 未闭合（被 max_tokens 截断，或模型自己忘了写收尾 ```)
//   - 用 ~~~ 而不是 ```
//   - 语言标注五花八门（bash/sh/shell/zsh/console/shell-session/无标注）
//
// 配对正则 ```([\s\S]*?)``` 对前两种直接失效：未闭合时一条命令都解析不出来，
// 用户看到的就是「模型明明给了命令，界面上却空空如也」。
// 因此改成**逐行扫描 + 状态机**：只要看到开标记就进入围栏态，
// 遇到匹配的收尾则闭合，遇到不匹配的开标记则继续吞入（兼容嵌套围栏内容），
// 直到文本结束仍未闭合也照样把已收集内容当成一个代码块——
// 命令宁可多显示一条，也不能整条消失。
var fenceOpenerRe = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})\\s*([A-Za-z0-9_+-]*)")

// extractCommands 从模型输出里抽出命令并打风险标签。
//
// 不解析、不执行、只做文本提取——这是本文件最重要的约束。
//
// 复用 extractSegments 的分段结果，保证「扁平命令列表」与「有序分段」
// 对同一段文字的判定完全一致：否则前端按分段渲染、后端按扁平列表算风险时，
// 会出现同一条命令在两处的存在性判断打架。
func extractCommands(text string) []SshAiCommand {
	out := make([]SshAiCommand, 0, 4)
	for _, seg := range extractSegments(text) {
		if seg.Type == "cmd" {
			out = append(out, seg.Commands...)
		}
	}
	return out
}

// extractSegments 按模型输出的原始顺序切分出「说明文字」与「命令」两类分段。
//
// 与 extractCommands 的区别：后者是扁平列表（只要命令，不管位置），
// 前者保留位置信息，让前端能把说明和命令交错渲染 ——
// 模型输出本来就是交错的（先解释 → 给命令 → 再补充），
// 拆成"说明全在上、命令全在下"会丢掉"哪条命令对应哪段解释"的对应关系。
//
// 围栏识别是本文件里判定"什么算命令"的**唯一**实现：
// extractCommands 直接复用本函数的结果，前端 utils/sshAiSegments.ts 也按
// 同一套规则实现。三处判定不一致会导致同一条命令在正文和卡片里各出现一次。
func extractSegments(text string) []SshAiSegment {
	lines := strings.Split(text, "\n")
	var segments []SshAiSegment
	var buf []string
	var fenceLines []string
	var inFence bool
	var fenceChar byte
	var fenceLen int
	seen := map[string]bool{}

	flushText := func() {
		body := strings.TrimSpace(strings.Join(buf, "\n"))
		buf = nil
		if body == "" {
			return
		}
		segments = append(segments, SshAiSegment{Type: "text", Text: body})
	}

	flushCmd := func() {
		body := strings.TrimSpace(strings.Join(fenceLines, "\n"))
		fenceLines = nil
		if body == "" {
			return
		}
		cmds := parseFenceCommands(body, seen)
		if len(cmds) == 0 {
			return
		}
		segments = append(segments, SshAiSegment{Type: "cmd", Commands: cmds})
	}

	for _, line := range lines {
		m := fenceOpenerRe.FindStringSubmatch(line)
		if m == nil {
			if inFence {
				fenceLines = append(fenceLines, line)
			} else {
				buf = append(buf, line)
			}
			continue
		}
		mark := m[1]
		if !inFence {
			flushText()
			inFence = true
			fenceChar = mark[0]
			fenceLen = len(mark)
			continue
		}
		if mark[0] == fenceChar && len(mark) >= fenceLen {
			flushCmd()
			inFence = false
			continue
		}
		fenceLines = append(fenceLines, line)
	}
	// 收尾：未闭合围栏也要把命令救回来。
	if inFence {
		flushCmd()
	}
	flushText()
	return segments
}

// shellPromptRe 匹配行首的 shell 提示符（`$` 普通用户 / `#` root）后跟空白。
//
// 模型偶尔按"贴终端实录"的写法给命令，这两种前缀都不是命令的一部分：
//   - 带 `$ ` 发出去，远端会把 `$` 当成命令名报 `command not found`，
//     用户看到的就是「AI 给的命令是坏的」；
//   - `#` 开头的行若一律当注释跳过，模型整段用 root 提示符写法时
//     命令会**全部消失**——正是这个功能最不该出现的结果。
//
// 要求前缀后必须有空白：`$HOME/bin/x`、`#!/bin/bash` 这类不该被当成提示符。
var shellPromptRe = regexp.MustCompile(`^[$#][ \t]+(.*)$`)

// commandTokenRe 判断一行"像不像一条命令"：首个 token 得是可执行文件名的常见形状。
//
// 只在兜底回扫里用（见 parseFenceCommands）：那时 `# ` 既可能是 root 提示符
// 也可能真是注释，靠它把「# 这是一段说明」这类中文注释挡掉，
// 避免把注释当成命令发给远端。正常扫描不加这道过滤——
// 命令的写法太杂（`./configure`、`(cd /tmp && ls)`），一过滤就会误杀真命令。
var commandTokenRe = regexp.MustCompile(`^[A-Za-z0-9_./-]+`)

// parseFenceCommands 把一个围栏块的正文解析成命令列表。
//
// 两遍扫描，第二遍只在必要时发生：
//  1. 正常遍：剥掉 `$ ` 提示符，`#` 开头按代码块的常规语义当作注释跳过。
//  2. 兜底遍：**仅当**第一遍一条命令都没得到、且块内**每一行**都以 `# ` 开头时触发。
//     那种形态说明模型整段用了 root 提示符写法，一律当注释会让命令全部消失；
//     而只要块里还有一行不带前缀，就说明它是常规代码块，`#` 就是注释——
//     所以这个条件必须同时满足，缺一个都会把注释变成命令。
func parseFenceCommands(body string, seen map[string]bool) []SshAiCommand {
	lines := strings.Split(body, "\n")
	cmds := collectFenceCommands(lines, false, seen)
	if len(cmds) == 0 && allPromptLines(lines, "#") {
		cmds = collectFenceCommands(lines, true, seen)
	}
	return cmds
}

// collectFenceCommands 扫一遍行，产出命令。
//
// rescueHash=true 时把 `# ` 当作 root 提示符剥离（并额外用 commandTokenRe 过滤）；
// 否则 `# ` 一律视为注释。
func collectFenceCommands(lines []string, rescueHash bool, seen map[string]bool) []SshAiCommand {
	var cmds []SshAiCommand
	for _, line := range lines {
		cmd := strings.TrimSpace(line)
		if cmd == "" {
			continue
		}
		if m := shellPromptRe.FindStringSubmatch(cmd); m != nil {
			// 只有 rescueHash 时才把 `# ` 当提示符；`$ ` 在任何一遍都剥。
			if cmd[0] == '#' && !rescueHash {
				continue
			}
			cmd = strings.TrimSpace(m[1])
		}
		// 未跟空白的 `#`（如 #!/bin/bash、#注释）在任何一遍都是注释。
		if cmd == "" || strings.HasPrefix(cmd, "#") {
			continue
		}
		if rescueHash && !commandTokenRe.MatchString(cmd) {
			continue
		}
		if seen[cmd] {
			continue
		}
		seen[cmd] = true
		level, note := evalRisk(cmd)
		cmds = append(cmds, SshAiCommand{
			Text: cmd,
			Risk: level,
			Note: note,
		})
	}
	return cmds
}

// allPromptLines 判断块内**每个**非空行是否都以指定提示符 + 空白开头。
func allPromptLines(lines []string, marker string) bool {
	n := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		n++
		if !strings.HasPrefix(l, marker+" ") && !strings.HasPrefix(l, marker+"\t") {
			return false
		}
	}
	return n > 0
}

// evalRisk 判定单条命令的风险等级与说明。
//
// 等级和说明**在同一个函数里产出**，不拆成 classifyRisk / riskNote 两个：
// 拆开时两边的优先级判定一旦写得不一样，界面就会出现「红框高风险卡片却配了
// 一条中等风险的说明」这种自相矛盾的展示——而这两处是分开被调用的，
// 编译期发现不了。合起来后，说明必定来自被选中等级的那条规则。
//
// 取**最高等级**而不是第一条命中的规则：规则表是分批追加的，
// 书写顺序不构成优先级（一条命令可能同时命中多条规则）。
// `git push -f` 同时命中"版本控制"与别的模式时，等级必须是 high。
//
// 只给一条说明而不是全部：命令卡片只有一行位置，
// 列三条风险说明反而让人看不出哪条最要紧。
func evalRisk(cmd string) (level string, note string) {
	var medium, mediumNote string
	for _, r := range sshAiDangerRule {
		if !r.Pattern.MatchString(cmd) {
			continue
		}
		if r.Level == "high" {
			// 最高级，没有更高的了，直接返回。
			return r.Level, r.Note
		}
		if medium == "" {
			medium, mediumNote = r.Level, r.Note
		}
	}
	return medium, mediumNote
}
