package ai

// 工具执行的审批闸门（方案 A1）。
//
// 为什么必须有它：在它之前，模型调 file_edit / run_command 就会被立刻执行，
// 唯一的防线是 aiutil.LocalCmdDangerRules（挡 git reset --hard 之类）与 fsguard
// 写白名单。那两样挡得住「明显灾难」，挡不住「删错一个函数」。而「能改文件 +
// 能跑命令 ⨯ 执行前拦不住 ⨯ 改完看不见 diff ⨯ 改错撤不回」四项叠加，等于把一把
// 能删库的钥匙交给模型、再拆掉刹车。这一层专门补「拦」。
//
// 三个刻意选择：
//
//  1. 闸门随 ctx 走，不挂 AIService 字段。AIService 是单例、同时可能有三条对话在跑，
//     挂字段必然出现「A 对话的审批被 B 对话的点击回应」——agentScope 已经因为同一个
//     原因从服务字段改成 ctx 携带（见 agent_scope.go 开头）。
//  2. 只有顶层执行有闸门。子 agent 用 dbSink 跑在后台，没有「人」在等它：审批事件
//     发出去只会等到超时然后自动拒绝，等于把 code 角色的能力整个废掉。子 agent 的
//     约束是工具面（agentRoleSpec 的 maxLevel），而派活本身（spawn_agent，LevelWrite）
//     已经在父级过了这道闸。
//  3. 拒绝不是错误码，而是一句给模型看的话。它要能读出「换方案 / 请用户自己动手」；
//     收到一个 ERROR_403 它只会原地重试同一个调用。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"quickdock/internal/aiutil"
	"quickdock/internal/db"
	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
)

const (
	// aiApprovalTimeout 等用户点按钮的上限。
	//
	// 90 秒是折中：再短会让「去倒杯水回来点」变成自动拒绝，再长会让整条对话卡在
	// 一个用户已经走开的卡片上。超时按拒绝处理（而不是放行）——拿不准的时候不做，
	// 与 SSH 面板「拿不到标注按最保守处理」是同一条原则。
	aiApprovalTimeout = 90 * time.Second

	// aiApprovalPoll 等待期间检查「前端是否已断开」的间隔。
	//
	// 审批期间没有 token 流出，sseSink 那条「写失败 → cancel」的通路不会被触发，
	// 所以只能主动问 Failed()。500ms 与 run 树的轮询同频，对人不可感知。
	aiApprovalPoll = 500 * time.Millisecond
)

// approvalMode 三态处置策略。
type approvalMode string

const (
	approvalAuto approvalMode = "auto" // 直接执行
	approvalAsk  approvalMode = "ask"  // 停下来问
	approvalDeny approvalMode = "deny" // 一律拒绝
)

// aiPermissionFull 「完全访问」的落库取值。空串表示默认权限，不另设 'default'。
const aiPermissionFull = "full"

// aiNormalizePermission 归一请求里的权限取值：只认 "full"，其余一律默认权限。
//
// 拼错、传空、传 "default" 都落到默认这一侧——权限是安全开关，
// 不认识的取值必须往「更严」的方向倒，绝不能因为解析失败反而全放行。
func aiNormalizePermission(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), aiPermissionFull) {
		return aiPermissionFull
	}
	return ""
}

// approvalPolicy 一个工具本次的处置策略。
type approvalPolicy struct {
	mode approvalMode
	// rememberable 是否允许「本次会话都允许」。
	//
	// LevelRisk 不给：高危操作按单次授权，用户每点一次都要重新看一眼命令。
	// 提供这个按钮的结果是可预料的——用户为了少点几次而勾上它，然后一条 rm -rf
	// 在半小时后再也不问了。
	//
	// 注意这里只是「允许记住」，**能不能真记住由 approvalGrantKey 决定**：
	// run_command 还要看这条命令是否单一、有没有可归纳的命令族（见那个函数）。
	rememberable bool
}

// aiAgentFileWriteAuto 这些工具即便挂在写等级上，内置 AI 执行时也自动放行：
// file_write / file_edit 只作用于用户已授权的项目目录（fsguard 写白名单不含家目录），
// 且每次写盘前都有快照可回滚（ai_tools.go 的 snapshotBeforeWrite）。
//
// 在目录里干活时它们是每轮必调的高频动作——改一行弹一次确认，等于把「让 AI 写代码」
// 这件事的摩擦拉到不可用。读类（LevelRead）天然不拦，这里补「写文件」这一档的自动放行。
//
// 这里的豁免刻意只覆盖「改授权目录里的文件」：run_command 仍是任意命令执行，与「改一个
// 已登记目录里的文件」危险度不在一个量级，所以保留审批与「本次会话都允许」按钮。
// git 相关命令（commit / push / log）走 run_command，自然落在「允许 + 记住」那一档。
//
// 用户若在设置页显式把 file_write / file_edit 设成 ask / deny，下面的 rules 分支优先，
// 这条豁免不生效——它只决定「没有覆盖时的默认行为」。
var aiAgentFileWriteAuto = map[string]bool{
	"file_write": true,
	"file_edit":  true,
}

// aiApprovalPolicyFor 由用户覆盖、工具名与等级推导策略。
//
// 读类不拦是这条功能的生死线——如果读文件也要确认，三分钟内就会被关掉，
// 而关掉之后就再也不会打开。
//
// rules 优先：用户显式设过的策略就是他的决定，哪怕它比默认值更宽松。
// 唯一的例外是「设成 ask」时要按等级决定给不给「本次会话都允许」——
// 高危工具的那个按钮一旦给出去，等于把用户刚做的收紧又打开了。
func aiApprovalPolicyFor(name string, level int, rules map[string]approvalMode) approvalPolicy {
	if m, ok := rules[name]; ok {
		switch m {
		case approvalAuto:
			return approvalPolicy{mode: approvalAuto}
		case approvalDeny:
			return approvalPolicy{mode: approvalDeny}
		default:
			return approvalPolicy{mode: approvalAsk, rememberable: level < mcpsrv.LevelRisk}
		}
	}
	// 写文件类对内置 AI 自动放行（只限授权目录 + 写前快照回滚）：见 aiAgentFileWriteAuto。
	// 放在等级分支之前——它是对「写等级但免确认」这一特例的显式声明，高于等级的默认值。
	if aiAgentFileWriteAuto[name] {
		return approvalPolicy{mode: approvalAuto}
	}
	switch level {
	case mcpsrv.LevelRead:
		return approvalPolicy{mode: approvalAuto}
	case mcpsrv.LevelWrite:
		// 写类一律允许记住，具体粒度交给 approvalGrantKey：
		// run_command 记的是命令族（npm 记 npm，rm 仍要问）。
		return approvalPolicy{mode: approvalAsk, rememberable: true}
	default:
		// LevelRisk 及以上：问，且只给单次。
		return approvalPolicy{mode: approvalAsk}
	}
}

// approvalShellWords 「执行壳」：它们本身不是干活的那个程序，跳过再取首词。
//
// 必须跳过 sudo：否则 `sudo rm -rf /` 会被记成「sudo 一族」，之后任意一条
// sudo 命令都放行——那比不记更糟。
var approvalShellWords = map[string]bool{
	"sudo": true, "doas": true, "command": true, "exec": true,
	"time": true, "nohup": true, "env": true, "nice": true, "start": true,
}

// approvalInterpreterWords 能执行任意命令的解释器。
//
// 与 approvalShellWords 的区别：那批是「一层壳」，跳过它取下一个词还是同一个程序
// （`sudo rm` 记成 rm 是对的）；而这批的下一词是它自己的开关（`cmd /c …`、`bash -lc …`），
// 取到的不是被执行的程序，按它记族毫无意义。
//
// 也不能像 sudo 那样跳过：跳过 `cmd` 会取到 `/c`，跳过 `bash` 会取到 `-lc`。
// 唯一安全的做法是命中就不给「记住」——批准一次 `cmd` 等于放行之后任意
// `cmd /c del /s /q …`，那比每次多点一次确认糟得多。
var approvalInterpreterWords = map[string]bool{
	// shell
	"cmd": true, "powershell": true, "pwsh": true, "wsl": true,
	"bash": true, "sh": true, "zsh": true, "fish": true,
	"ksh": true, "csh": true, "tcsh": true, "dash": true,
	// 脚本宿主：参数就是代码
	"python": true, "python3": true, "py": true, "node": true, "deno": true,
	"bun": true, "perl": true, "ruby": true, "php": true, "lua": true,
	"osascript": true, "expect": true,
	// 任意 jar / 类
	"java": true, "javaw": true,
}

// approvalCommandHead 取一条命令的「首词」，用作命令族的标识。
//
// 返回空字符串表示「这条不该给记住按钮」——多段命令（含 && || ; | &）一律如此。
// 理由：`cd repo && rm -rf build` 若按首词记成 cd，下次 `cd x && rm -rf y`
// 就再也不问了，而用户当初批准的是「切个目录」。
func approvalCommandHead(cmd string) string {
	s := strings.TrimSpace(cmd)
	if s == "" {
		return ""
	}
	// 一次只批准一条命令：连接符说明这里其实有多条。
	if strings.ContainsAny(s, "&|;\n") {
		return ""
	}
	fields := strings.Fields(s)
	for len(fields) > 0 {
		w := fields[0]
		// 跳过前置环境变量赋值 FOO=bar（只认「前面不像路径」的形态，
		// 避免把 C:\a=b\npm 这种路径里的等号当成赋值）。
		if i := strings.IndexByte(w, '='); i > 0 && !strings.ContainsAny(w[:i], `/\`) {
			fields = fields[1:]
			continue
		}
		if approvalShellWords[strings.ToLower(w)] {
			fields = fields[1:]
			continue
		}
		break
	}
	if len(fields) == 0 {
		return ""
	}
	// 去掉路径与扩展名：C:\Program Files\nodejs\npm.cmd → npm
	base := fields[0]
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1", ".com"} {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "" {
		return ""
	}
	if approvalInterpreterWords[base] {
		return ""
	}
	return base
}

// approvalGrantKey 「本次会话都允许」记在哪个 key 上。
//
// 第二个返回值为 false 表示这次不给记住按钮。除了高危等级，还有一类情况：
// run_command 拿不到可归纳的命令族（空命令、多段命令）。此时只让用户点单次——
// 记不住比记错安全。
func approvalGrantKey(name string, args map[string]any) (string, bool) {
	if name != "run_command" {
		return name, true
	}
	head := approvalCommandHead(mcpsrv.Arg(args, "command"))
	if head == "" {
		return "", false
	}
	return "run_command:" + head, true
}

// aiApprovalGrantScope 描述「这次记住的是什么范围」，给前端写在按钮旁边。
//
// 按命令族记住必须让用户看见范围：他点的是「本次会话都允许」，如果他以为是
// 「这一条」，后来发现 npm 之外也被静默放行（或反过来），这个按钮就再没人信了。
func aiApprovalGrantScope(grantKey string) string {
	if head, ok := strings.CutPrefix(grantKey, "run_command:"); ok && head != "" {
		return head
	}
	return ""
}

// approvalDecision 一次点击的结果。
type approvalDecision struct {
	Allow    bool
	Remember bool
}

// approvalGate 一次顶层执行的审批通道。
type approvalGate struct {
	sink   EventSink
	convID string
	// database 只用于落审批明细（ai_approvals），不参与拦截判定。
	database *db.Database
	// rules 用户为具体工具设定的策略覆盖（见 ai_approval_rules.go）。
	//
	// 一次 run 读一次、全程复用：一轮对话里改设置不该让同一轮的两个调用
	// 按两套策略走。改了设置下一轮对话生效。
	rules map[string]approvalMode

	// fullAccess 这条对话是否处于「完全访问」。
	//
	// 一次 run 一个值：用户在这轮进行中把权限切回来，也不影响正在跑的这条——
	// 半路改口径会让同一轮里前后的调用按两套规则走，事后没人说得清
	// 「那次 rm 到底是批准过还是没批准过」。
	fullAccess bool

	// askMu 串行化整个「问 → 等」过程。
	//
	// 并发轮次里三个工具同时要审批时，不加这个锁就会同时发三条 approval 事件，
	// 前端一次弹三张卡；用户点第一张的时候，另外两张的上下文已经变了。
	// 串行的代价只是「多问几秒」，换来的是「一次只看一张卡」。
	askMu sync.Mutex

	// pMu 保护下面两个 map。必须与 askMu 分开：decide 发生在另一条 RPC goroutine
	// 里，如果它也去抢 askMu，就会和正在等待的 await 互相等死。
	pMu     sync.Mutex
	pending map[string]chan approvalDecision
	granted map[string]bool
}

func newApprovalGate(sink EventSink, convID string, database *db.Database, rules map[string]approvalMode, fullAccess bool) *approvalGate {
	return &approvalGate{
		sink:       sink,
		convID:     convID,
		database:   database,
		rules:      rules,
		fullAccess: fullAccess,
		pending:    make(map[string]chan approvalDecision),
		granted:    make(map[string]bool),
	}
}

// record 落一条审批明细。
//
// 写失败只记日志、不影响审批结果：统计是治理用的，不能因为它把这半秒的
// 用户等待变成一次失败。db 为空（单测、无库）时直接跳过。
func (g *approvalGate) record(callID, name, decision string, remember bool) {
	if g == nil || g.database == nil {
		return
	}
	err := g.database.RecordAIApproval(db.AIApproval{
		ID:       uuid.New().String(),
		ConvID:   g.convID,
		CallID:   callID,
		Tool:     name,
		Decision: decision,
		Remember: remember,
	})
	if err != nil {
		logger.W("[ai-approval] 记录审批结果失败: %v", err)
	}
}

// approvalCtxKey 私有类型：避免与别的包往 ctx 里塞的键撞名。
type approvalCtxKey struct{}

func withApprovalGate(ctx context.Context, g *approvalGate) context.Context {
	return context.WithValue(ctx, approvalCtxKey{}, g)
}

// approvalGateFrom 取本次执行的闸门。没有（子 agent / 非 AI 调用路径）时返回 nil，
// 调用方据此跳过审批——「没有闸门」不是错误，是设计。
func approvalGateFrom(ctx context.Context) *approvalGate {
	g, _ := ctx.Value(approvalCtxKey{}).(*approvalGate)
	return g
}

// await 若该调用需要人工确认，就在这里停下来等。
//
// 返回 (是否允许, 给模型看的说明)。allow=false 时说明里必须写清「因为什么没执行」：
// 模型据此换方案，而一句空的 failed 会让它原地重试同一个调用。
func (g *approvalGate) await(ctx context.Context, callID, name string, level int, args map[string]any) (bool, string) {
	pol := aiApprovalPolicyFor(name, level, g.rules)
	switch pol.mode {
	case approvalAuto:
		return true, ""
	case approvalDeny:
		return false, "该工具已被策略禁用"
	}

	// 完全访问：用户在这个对话里显式按过「允许完全访问」，并勾了风险确认。
	//
	// 只绕过「问」这一档，不改 auto / deny —— deny 是用户在设置页逐个关掉的工具，
	// 属于比「完全访问」更明确的意图，不该被一个更宽的开关重新打开。
	//
	// 仍然挡得住的东西（刻意保留）：
	//   - run_command 的硬拦截：LocalCmdDangerRules 判 high 的命令（rm -rf /、
	//     格式化磁盘之类）由工具层直接拒绝执行，不经过这道闸门；
	//   - fsguard 写白名单：写操作只能在授权目录里，完全访问不是「整个磁盘都授权」。
	// 这两层是能力边界，不是确认步骤，放开确认步骤不该连带放开它们。
	if g.fullAccess {
		return true, ""
	}

	g.askMu.Lock()
	defer g.askMu.Unlock()

	// 「本次会话都允许」记在 grantKey 上：普通写工具就是工具名，
	// run_command 是「run_command:命令首词」——批准 npm install 之后 npm 一族
	// 不再问，而 rm / curl / git push 换了首词，照问。
	grantKey, canRemember := approvalGrantKey(name, args)
	rememberable := pol.rememberable && canRemember

	// 本会话已经放行过这一族调用（用户点过「本次会话都允许」）。
	// 放在 askMu 之后再查：并发轮次里两个同样的调用会排在锁上，第一个点完之后
	// 第二个走到这里就能直接放行，不会把同一张卡再弹一次。
	if canRemember {
		g.pMu.Lock()
		allowed := g.granted[grantKey]
		g.pMu.Unlock()
		if allowed {
			return true, ""
		}
	}

	// 已经停止的提问不该再弹卡片：用户按了停止之后界面还在要确认，
	// 是最典型的「这软件失控了」的观感。
	if err := ctx.Err(); err != nil {
		g.record(callID, name, db.AIDecisionStopped, false)
		return false, "本次提问已停止"
	}

	// 带缓冲 1：decide 在另一条 goroutine 里，无缓冲会把它卡住并牵连那条 RPC。
	ch := make(chan approvalDecision, 1)
	g.pMu.Lock()
	g.pending[callID] = ch
	g.pMu.Unlock()
	defer func() {
		g.pMu.Lock()
		delete(g.pending, callID)
		g.pMu.Unlock()
	}()

	g.sink.Event("approval", map[string]any{
		"callId":       callID,
		"convId":       g.convID,
		"name":         name,
		"level":        level,
		"levelName":    aiApprovalLevelName(level),
		"args":         aiApprovalArgsBrief(name, args),
		"riskNote":     aiApprovalRiskNote(name, level, args),
		"rememberable": rememberable,
		"grantScope":   aiApprovalGrantScope(grantKey),
		"timeout":      int(aiApprovalTimeout.Seconds()),
	})

	timer := time.NewTimer(aiApprovalTimeout)
	defer timer.Stop()
	tick := time.NewTicker(aiApprovalPoll)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			g.record(callID, name, db.AIDecisionStopped, false)
			return false, "本次提问已停止"
		case <-timer.C:
			logger.W("[ai-approval] 等待确认超时 conv=%s tool=%s call=%s", g.convID, name, callID)
			g.record(callID, name, db.AIDecisionTimeout, false)
			return false, fmt.Sprintf("等待确认超时（%d 秒）", int(aiApprovalTimeout.Seconds()))
		case <-tick.C:
			if g.sink.Failed() {
				g.record(callID, name, db.AIDecisionDisconnected, false)
				return false, "连接已断开"
			}
		case d := <-ch:
			if !d.Allow {
				g.record(callID, name, db.AIDecisionDeny, false)
				return false, "用户拒绝了这次操作"
			}
			remember := d.Remember && rememberable
			if remember {
				g.pMu.Lock()
				g.granted[grantKey] = true
				g.pMu.Unlock()
			}
			// 记的是「实际生效的记住」而不是「用户点没点」：高危工具与多段命令
			// 点不了这个按钮，若按点击算，统计会显示一堆其实没生效的记住。
			g.record(callID, name, db.AIDecisionAllow, remember)
			return true, ""
		}
	}
}

// decide 把一次点击送到正等着它的那条调用上。
//
// 返回「这个 callId 是否真的有人在等」：前端卡片可能比 gate 晚一步消失
// （会话已结束、用户已点停止），此时静默丢弃比报错好——报错只会让前端弹一个
// 用户无从解释的红框。
func (g *approvalGate) decide(callID string, allow, remember bool) bool {
	g.pMu.Lock()
	ch, ok := g.pending[callID]
	g.pMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- approvalDecision{Allow: allow, Remember: remember}:
		return true
	default:
		// 缓冲已满 = 已经有人点过（连点两下），后一次丢掉。
		return false
	}
}

// registerGate / unregisterGate 维护「会话 → 审批通道」的索引。
//
// 为什么要 AIService 上的一张表：bound 方法 AIApproveToolCall 只拿得到 convID
// （前端只能知道自己当时在哪个会话里），要找到那个 goroutine 正在等的 channel，
// 只能查表。表按 convID 索引而不是单个字段，所以并发会话互不影响。
func (a *AIService) registerGate(convID string, g *approvalGate) {
	if convID == "" || g == nil {
		return
	}
	a.gatesMu.Lock()
	defer a.gatesMu.Unlock()
	if a.gates == nil {
		a.gates = make(map[string]*approvalGate)
	}
	a.gates[convID] = g
}

// unregisterGate 必须在 runAgent 退出时调用，否则每问一次漏一个 gate
// （连带它持住的 sink 与 map），长会话里是实打实的泄漏。
//
// 传 g 而不是只传 convID，是为了在锁内比对指针再删：停止后几百毫秒内用户
// 又发一句时，旧 run 的 defer 可能晚于新 run 的 register 执行——只按 convID
// 删就会把**新的**那个 gate 删掉，症状是新卡片点了没反应（前端以为在问，
// 后端查表查不到），而日志里一切正常。
func (a *AIService) unregisterGate(convID string, g *approvalGate) {
	a.gatesMu.Lock()
	defer a.gatesMu.Unlock()
	if cur, ok := a.gates[convID]; ok && (g == nil || cur == g) {
		delete(a.gates, convID)
	}
}

func (a *AIService) gateFor(convID string) *approvalGate {
	a.gatesMu.Lock()
	defer a.gatesMu.Unlock()
	return a.gates[convID]
}

// AIApproveToolCall 前端对一条待确认的工具调用作出决定（bound 方法）。
//
// decision 只认 "allow" / "deny"，不把前端字符串直接转 bool：拼错一个字母会被
// 当成 false（拒绝），而那种 bug 表现为「点了允许但什么也没发生」，极难查。
func (a *AIService) AIApproveToolCall(convID, callID, decision string, remember bool) error {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return fmt.Errorf("缺少 callId")
	}
	var allow bool
	switch strings.TrimSpace(decision) {
	case "allow":
		allow = true
	case "deny":
		allow = false
	default:
		return fmt.Errorf("未知决定 %q（只接受 allow / deny）", decision)
	}

	g := a.gateFor(strings.TrimSpace(convID))
	if g == nil {
		// 这个会话没有在等审批：已结束、已停止，或卡片残留。返回 nil 而不是错误
		// ——前端点一张已经失效的卡片时，弹一个红框只会让人以为软件坏了。
		logger.W("[ai-approval] 会话没有待审批的调用 conv=%s call=%s", convID, callID)
		return nil
	}
	if !g.decide(callID, allow, remember) {
		logger.W("[ai-approval] 没有在等这个调用 conv=%s call=%s（可能已超时或已停止）", convID, callID)
	}
	return nil
}

// aiApprovalLevelName 等级的英文标识（前端据此选配色，不用数字）。
func aiApprovalLevelName(level int) string {
	switch level {
	case mcpsrv.LevelRead:
		return "read"
	case mcpsrv.LevelWrite:
		return "write"
	default:
		return "risk"
	}
}

// aiApprovalArgsBrief 卡片上展示的参数摘要。
//
// 只挑用户判断所需的键，不做全量展示：run_command 的整条命令必须给人看（那是他
// 判断的唯一依据），而 file_edit 的 old_string / new_string 可能很长——给路径就够
// 知道改的是哪个文件。全量塞进事件会让一条 SSE 帧大到几 MB。
func aiApprovalArgsBrief(name string, args map[string]any) string {
	if name == "run_command" {
		return mcpsrv.Arg(args, "command")
	}
	if p := mcpsrv.Arg(args, "path"); p != "" {
		return p
	}
	// 其余工具尽量给一句能看懂的。
	for _, k := range []string{"task", "query", "url", "pattern", "note"} {
		if v := mcpsrv.Arg(args, k); v != "" {
			return v
		}
	}
	return ""
}

// aiApprovalRiskNote 风险说明。
//
// run_command 复用 aiutil.LocalCmdDangerRules：那张表的判定前提是「在本机跑」，
// 与本机执行一致；SSH 那张表的前提是远程生产机，混用会给出不准确的警告
// （`systemctl restart` 在那边是线上中断，在这边可能只是重启自己的服务）。
// 其余工具按等级给一句通用说明——编造更具体的风险描述反而会让人不再读它。
func aiApprovalRiskNote(name string, level int, args map[string]any) string {
	if name == "run_command" {
		cmd := mcpsrv.Arg(args, "command")
		if lv, note := aiutil.LocalCmdDangerRules.Eval(cmd); note != "" {
			if lv == "high" {
				// 说明必须自洽：run_command 对 high 风险是一律不执行的（见
				// tools/code.go）。不写这半句，用户点完「允许」却收到一个失败，
				// 会觉得审批是个假按钮。
				return "高危：" + note + "。这类命令宿主一律不执行，允许了也不会跑"
			}
			return note
		}
		return "在本机执行一条命令，输出会回给模型"
	}
	if name == spawnAgentToolName {
		// 这张卡要挡住的是「派活」这个动作，但用户很容易把它理解成「改几个文件」。
		// 差别在于点完之后：子 agent 在自己的循环里跑，非高危命令不再逐次弹确认，
		// 用户要到结果交回主对话才看得到发生了什么。不写这半句，
		// 用户是在完全不知情的情况下批准了一个无人值守的执行者。
		role := strings.ToLower(strings.TrimSpace(mcpsrv.Arg(args, "role")))
		if spec, ok := agentRoleSpecs[role]; ok {
			return "派一个子 agent 独立执行：" + spec.summary +
				"。批准后它在自己的循环里连续动手，中途不会再逐条向你确认（高危命令仍被拒），结果交回本对话"
		}
		return "派一个子 agent 独立执行，中途不会再逐条向你确认（高危命令仍被拒）"
	}
	switch level {
	case mcpsrv.LevelWrite:
		return "会修改磁盘上的文件"
	default:
		return "高危操作，请确认目标与影响"
	}
}

// aiApprovalRejectText 未获批准时回给模型的文本。
//
// 三件事都要写到：没执行、为什么、接下来该怎么办。缺最后一条时，模型最常见的
// 反应是「换个说法再调一次同一个工具」，而用户看到的是同一个弹窗又来一遍。
func aiApprovalRejectText(name, note string) string {
	return fmt.Sprintf("未执行 %s：%s。不要重复调用同一工具——请改用不需要该权限的做法，或直接告诉用户需要他手动完成这一步。", name, note)
}
