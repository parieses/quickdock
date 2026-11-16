package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"quickdock/internal/lock"
)

// Caller 谁在调工具。两条路径的差异从「两份代码」收敛成「一个入参」。
type Caller int

const (
	// CallerMCP 外部 MCP 客户端（dsh / WorkBuddy / Claude Desktop…）。
	CallerMCP Caller = iota
	// CallerAI 内置 AI 页与它派出的子 agent。
	CallerAI
)

// ToolScope 一次装配的输入：调用方、它自己那份策略、场景与角色收窄。
//
// 不叫 Scope 是因为 registry.go 已有同名函数（取宿主注入的读取范围），
// 两者语义完全不同，撞名会让调用处读起来像在传一个目录。
type ToolScope struct {
	Caller Caller
	// Policy 调用方自己的暴露策略。**不从全局读**——两份策略互不影响正是本次改造的目的。
	Policy Policy
	// Scene 能力档（assistant / readonly）。仅内置 AI 使用；MCP 路径忽略。
	Scene string
	// Keep 额外收窄（子 agent 的角色边界）。nil 表示不收窄。
	Keep func(Tool) bool
}

// Dropped 一个没挂上的工具及原因。面板与自检要能回答「为什么它调不到」。
type Dropped struct {
	Name   string
	Reason string
}

// Surface 一次装配的结果：模型能看到什么、能调什么，都以它为准。
//
// 关键点：**可见性与调用权限用的是同一份对象**。以前 tools[] 按 mode+role 过滤，
// 真正执行时又在 callTool 里按另一套条件判一遍，于是出现「面板显示开启、普通对话里
// 却调不到」。Invoke 只认 Surface 成员，这个不一致在结构上就不成立了。
type Surface struct {
	Caller Caller
	// Scene 本次装配用的能力档（仅内置 AI 有值）。提示词据此说明「只读档没有写工具」。
	Scene string
	Tools []Tool
	// Domains 可见域，按面板顺序排列。提示词按它拼段落。
	Domains []Domain
	Dropped []Dropped

	byName map[string]Tool
}

// Has 本次装配里有没有这个工具。
func (s Surface) Has(name string) bool {
	_, ok := s.byName[name]
	return ok
}

// Tool 取工具定义。
func (s Surface) Tool(name string) (Tool, bool) {
	t, ok := s.byName[name]
	return t, ok
}

// Names 全部工具名（有序）。
func (s Surface) Names() []string {
	out := make([]string, 0, len(s.Tools))
	for _, t := range s.Tools {
		out = append(out, t.Name)
	}
	return out
}

// Resolve 装配一个工具面。这是唯一的装配入口。
//
// 顺序固定：策略（等级/单工具开关）→ 域归属（对外）或场景 → 角色收窄。
// 三层都是「模型知不知道有这个能力」，真正的拦截在 Invoke，两者共用同一个 Surface。
func Resolve(sc ToolScope) Surface {
	regMu.RLock()
	defer regMu.RUnlock()

	sets := []struct {
		m     map[string]Tool
		agent bool
	}{{tools, false}}
	if sc.Caller == CallerAI {
		// agent 工具（抓网页、改文件、跑命令）只给内置 AI：外部客户端自带这些。
		sets = append(sets, struct {
			m     map[string]Tool
			agent bool
		}{agents, true})
	}

	sf := Surface{Caller: sc.Caller, Scene: sc.Scene, byName: map[string]Tool{}}
	visibleDomains := map[string]bool{}
	for _, set := range sets {
		for name, t := range set.m {
			switch {
			case !sc.Policy.Allows(t):
				sf.Dropped = append(sf.Dropped, Dropped{Name: name, Reason: dropReason(sc.Policy, t)})
				continue
			}
			if sc.Caller == CallerMCP {
				if !DomainExposed(t.Domain) {
					sf.Dropped = append(sf.Dropped, Dropped{Name: name, Reason: "不对外部客户端开放"})
					continue
				}
			} else {
				if !DomainAllows(t.Domain, sc.Scene, t.Level) {
					sf.Dropped = append(sf.Dropped, Dropped{Name: name, Reason: "当前能力档未开放"})
					continue
				}
				if sc.Keep != nil && !sc.Keep(t) {
					sf.Dropped = append(sf.Dropped, Dropped{Name: name, Reason: "角色边界"})
					continue
				}
			}
			sf.Tools = append(sf.Tools, t)
			sf.byName[name] = t
			visibleDomains[t.Domain] = true
		}
	}
	// 排序：tools[] 每轮都作为请求前缀，顺序稳定才能吃到上游的前缀缓存。
	sort.Slice(sf.Tools, func(i, j int) bool { return sf.Tools[i].Name < sf.Tools[j].Name })
	sort.Slice(sf.Dropped, func(i, j int) bool { return sf.Dropped[i].Name < sf.Dropped[j].Name })

	for _, d := range Domains() {
		if visibleDomains[d.ID] {
			sf.Domains = append(sf.Domains, d)
		}
	}
	return sf
}

func dropReason(p Policy, t Tool) string {
	if p.Disabled[t.Name] {
		return "已单独关闭"
	}
	if t.Level > p.MaxLevel {
		return "超出等级上限"
	}
	return "不在本次装配范围"
}

// ApprovalRequest 一次审批请求。
type ApprovalRequest struct {
	CallID string
	Name   string
	Level  int
	Args   map[string]any
	// Note 覆盖卡片上的风险说明（越界读取等场景由调用方给具体原因）。
	Note string
	// Force 等级上是只读、但这次目标越界，必须升到「问」。
	Force bool
}

// Approver 审批执行者。
//
// 内置 AI 传入交互式实现（弹卡等用户点）；外部 MCP 客户端传入策略型实现
// （读放行、写按设置 + 落审计）。返回 false 时 note 必须写清「因为什么没执行」——
// 模型据此换方案，一句空的拒绝会让它原地重试。
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (allow bool, note string)
}

// DeniedError 审批拒绝。用类型而不是字符串约定，调用方才能稳定地把它
// 转成「被拒绝」的展示（工具卡变红），而不是一次普通执行失败。
type DeniedError struct {
	Name string
	Note string
}

func (e *DeniedError) Error() string {
	if e.Note == "" {
		return "未执行 " + e.Name + "：未获批准"
	}
	return "未执行 " + e.Name + "：" + e.Note
}

// Invoke 执行一个工具。唯一闸门链：锁 → 成员 → 审批 → Handler。
//
// 与旧 callTool 的差别不只是改名：
//   - 成员判定改用 Surface，和模型看到的那份工具面是同一个对象；
//   - 审批变成可注入的 Approver，外部路径终于也走这道闸（以前只有内置 AI 走）。
func Invoke(ctx context.Context, sf Surface, name string, args map[string]any, ap Approver) (any, error) {
	// 启动锁生效时对外部 MCP 客户端一律拒绝，只读也不例外：
	// note_list / file_read 返回的就是用户数据本身，「只读放行」等于把整库交给
	// 本机任意进程。内置 AI 不拦：锁的语义是「不让人看界面」，不是「掐掉后台的活」。
	if sf.Caller == CallerMCP && lock.IsLocked() {
		return nil, fmt.Errorf("QuickDock 已锁定：请先在主窗口解锁后再调用工具 %s", name)
	}

	t, ok := sf.Tool(name)
	if !ok {
		// 名字存在但没进本次装配时给出具体原因：外部客户端猜 file_edit 很容易，
		// 回「未知工具」会让人以为是版本不对。
		if reason, known := droppedReason(sf, name); known {
			return nil, fmt.Errorf("工具 %s 本次未开放：%s", name, reason)
		}
		if inAgentRegistry(name) {
			return nil, fmt.Errorf("工具 %s 仅内置 AI 可用，不对外部 MCP 客户端开放", name)
		}
		return nil, fmt.Errorf("未知工具: %s", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	if ap != nil {
		allow, note := ap.Approve(ctx, ApprovalRequest{
			Name:  name,
			Level: t.Level,
			Args:  args,
			Note:  riskNoteOf(t, args),
		})
		if !allow {
			return nil, &DeniedError{Name: name, Note: note}
		}
	}
	if t.HandlerCtx != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		return t.HandlerCtx(ctx, args)
	}
	if t.Handler == nil {
		return nil, fmt.Errorf("工具 %s 没有实现", name)
	}
	return t.Handler(args)
}

// droppedReason 查一个工具没进本次装配的原因。
func droppedReason(sf Surface, name string) (string, bool) {
	for _, d := range sf.Dropped {
		if d.Name == name {
			return d.Reason, true
		}
	}
	return "", false
}

func inAgentRegistry(name string) bool {
	regMu.RLock()
	defer regMu.RUnlock()
	_, ok := agents[name]
	return ok
}

// riskNoteOf 默认风险说明（工具自己声明了 RiskNote 时以它为准）。
func riskNoteOf(t Tool, args map[string]any) string {
	if t.RiskNote != nil {
		if note := strings.TrimSpace(t.RiskNote(args)); note != "" {
			return note
		}
	}
	switch t.Level {
	case LevelWrite:
		return "会修改磁盘上的数据"
	default:
		return "高危操作，请确认目标与影响"
	}
}

// ArgBriefOf 审批卡片上的参数摘要。
func ArgBriefOf(t Tool, args map[string]any) string {
	if t.ArgBrief != nil {
		return t.ArgBrief(args)
	}
	if p := Arg(args, "path"); p != "" {
		return p
	}
	for _, k := range []string{"task", "query", "url", "pattern", "note", "title"} {
		if v := Arg(args, k); v != "" {
			return v
		}
	}
	return ""
}

// GrantKeyOf 「本次会话都允许」记在哪个 key 上。
//
// 第二个返回值为 false 表示这次不给记住按钮。默认按工具名记忆；工具可以声明
// 更细的粒度（run_command 按命令族、schedule_create 按任务名），这样「记住」
// 不会悄悄把整个工具变成静默放行。
func GrantKeyOf(t Tool, args map[string]any) (string, bool) {
	if t.GrantKey != nil {
		return t.GrantKey(args)
	}
	return t.Name, true
}

// IsDenied 判断错误是不是审批拒绝。
func IsDenied(err error) (*DeniedError, bool) {
	var d *DeniedError
	if errors.As(err, &d) {
		return d, true
	}
	return nil, false
}
