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

	"quickdock/internal/aiutil"
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

// approvalPolicy 一个工具本次的处置策略。
type approvalPolicy struct {
	mode approvalMode
	// rememberable 是否提供「本次会话都允许」。
	//
	// LevelRisk 不给：高危操作按单次授权，用户每点一次都要重新看一眼命令。
	// 提供这个按钮的结果是可预料的——用户为了少点几次而勾上它，然后一条 rm -rf
	// 在半小时后再也不问了。
	rememberable bool
}

// aiApprovalPolicyFor 由工具名与等级推导策略。
//
// 读类不拦是这条功能的生死线——如果读文件也要确认，三分钟内就会被关掉，
// 而关掉之后就再也不会打开。
func aiApprovalPolicyFor(name string, level int) approvalPolicy {
	switch level {
	case mcpsrv.LevelRead:
		return approvalPolicy{mode: approvalAuto}
	case mcpsrv.LevelWrite:
		// run_command 单独排除「本次会话都允许」：它放行的不是某个动作，而是
		// 「之后任意一条命令」。用户此刻批准的是这一条 npm install，按工具名记住
		// 一个「什么命令都行」的授权，等于用一次点击把整条防线关掉。
		// 其余写类工具（file_edit / file_write / spawn_agent）动作固定，记住有意义。
		if name == "run_command" {
			return approvalPolicy{mode: approvalAsk}
		}
		return approvalPolicy{mode: approvalAsk, rememberable: true}
	default:
		// LevelRisk 及以上：问，且只给单次。
		return approvalPolicy{mode: approvalAsk}
	}
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

func newApprovalGate(sink EventSink, convID string) *approvalGate {
	return &approvalGate{
		sink:    sink,
		convID:  convID,
		pending: make(map[string]chan approvalDecision),
		granted: make(map[string]bool),
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
	pol := aiApprovalPolicyFor(name, level)
	switch pol.mode {
	case approvalAuto:
		return true, ""
	case approvalDeny:
		return false, "该工具已被策略禁用"
	}

	g.askMu.Lock()
	defer g.askMu.Unlock()

	// 本会话已经放行过这个工具（用户点过「本次会话都允许」）。
	// 放在 askMu 之后再查：并发轮次里两个同样的调用会排在锁上，第一个点完之后
	// 第二个走到这里就能直接放行，不会把同一张卡再弹一次。
	g.pMu.Lock()
	allowed := g.granted[name]
	g.pMu.Unlock()
	if allowed {
		return true, ""
	}

	// 已经停止的提问不该再弹卡片：用户按了停止之后界面还在要确认，
	// 是最典型的「这软件失控了」的观感。
	if err := ctx.Err(); err != nil {
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
		"rememberable": pol.rememberable,
		"timeout":      int(aiApprovalTimeout.Seconds()),
	})

	timer := time.NewTimer(aiApprovalTimeout)
	defer timer.Stop()
	tick := time.NewTicker(aiApprovalPoll)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, "本次提问已停止"
		case <-timer.C:
			logger.W("[ai-approval] 等待确认超时 conv=%s tool=%s call=%s", g.convID, name, callID)
			return false, fmt.Sprintf("等待确认超时（%d 秒）", int(aiApprovalTimeout.Seconds()))
		case <-tick.C:
			if g.sink.Failed() {
				return false, "连接已断开"
			}
		case d := <-ch:
			if !d.Allow {
				return false, "用户拒绝了这次操作"
			}
			if d.Remember && pol.rememberable {
				g.pMu.Lock()
				g.granted[name] = true
				g.pMu.Unlock()
			}
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

// unregisterGate 必须在 RunAgent 退出时调用，否则每问一次漏一个 gate
// （连带它持住的 sink 与 map），长会话里是实打实的泄漏。
func (a *AIService) unregisterGate(convID string) {
	a.gatesMu.Lock()
	defer a.gatesMu.Unlock()
	delete(a.gates, convID)
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
