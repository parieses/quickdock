package ai

// 审批规则沉淀（WP6）。
//
// 原则：**沉淀改变的是「要不要问」，不改变「允不允许」**。
// 族级放行只是省掉一次弹窗，执行前的高危判定一条都不能少——
// `run_command` 的那两张禁令表（破坏工作区 / 供应链）在工具层，与这里无关，
// 批准与否都要过。
//
// 与「本次会话都允许」的分工：
//   - 「本次会话」是内存态（approvalGate.granted），一条对话结束就没了；
//   - 沉淀是落库的、跨会话的，因此必须有 scope 与撤销入口，
//     否则用户点错一次就永久放行了一类操作。

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/services"
)

// aiApprovalGrantsKey 沉淀规则的落库键。
const aiApprovalGrantsKey = "ai_approval_grants"

// 匹配方式。
const (
	grantKindPrefix = "prefix" // run_command：命令首词（族级）
	grantKindPath   = "path"   // 文件类：目录前缀
	grantKindExact  = "exact"  // 精确的 grantKey
)

// 作用域。
const (
	grantScopeGlobal    = "global"
	grantScopeWorkspace = "workspace"
)

// aiApprovalGrant 一条沉淀下来的放行规则。
type aiApprovalGrant struct {
	Tool  string `json:"tool"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Scope string `json:"scope"`
	// Workspace scope=workspace 时生效的目录（绝对路径）。
	Workspace string `json:"workspace,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AIGrantView 回给设置页的一条（比存储结构多一个可读描述）。
type AIGrantView struct {
	Tool  string `json:"tool"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Scope string `json:"scope"`
	// Workspace 非空时只在那个目录下生效。
	Workspace string `json:"workspace"`
	CreatedAt string `json:"created_at"`
	// Note 一句话说明这条规则放行了什么。
	Note string `json:"note"`
}

// loadApprovalGrants 读沉淀规则。
//
// 解析失败按「没有任何规则」处理：一条写坏的 JSON 不该变成全放行，
// 那比多问几次危险得多（与 loadApprovalRules 同一取舍）。
func (a *AIService) loadApprovalGrants() []aiApprovalGrant {
	if a == nil || a.App == nil || a.App.DB == nil {
		return nil
	}
	raw, err := a.App.DB.GetSetting(aiApprovalGrantsKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	var list []aiApprovalGrant
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		logger.W("[ai-approval] 沉淀规则解析失败，按无规则处理: %v", err)
		return nil
	}
	out := make([]aiApprovalGrant, 0, len(list))
	for _, g := range list {
		if strings.TrimSpace(g.Tool) == "" || strings.TrimSpace(g.Value) == "" {
			continue
		}
		if g.Kind != grantKindPrefix && g.Kind != grantKindPath && g.Kind != grantKindExact {
			continue
		}
		if g.Scope != grantScopeGlobal && g.Scope != grantScopeWorkspace {
			continue
		}
		if g.Scope == grantScopeWorkspace && strings.TrimSpace(g.Workspace) == "" {
			// workspace 作用域却没有目录 = 无从判断在哪生效，直接丢掉。
			continue
		}
		out = append(out, g)
	}
	return out
}

func (a *AIService) saveApprovalGrants(list []aiApprovalGrant) error {
	if len(list) == 0 {
		return a.App.DB.SetSetting(aiApprovalGrantsKey, "")
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return a.App.DB.SetSetting(aiApprovalGrantsKey, string(b))
}

// matchApprovalGrant 有没有一条沉淀规则覆盖这次调用。
//
// cwd 是本次 run 的工作目录；workspace 作用域的规则只在该目录之内生效。
// 这个判断必须在「问」之前做，且不改变任何 deny/风险判定——它只回答
// 「这一族/这个目录，用户是不是已经明确说过可以」。
func matchApprovalGrant(grants []aiApprovalGrant, tool, cwd string, args map[string]any) bool {
	if len(grants) == 0 {
		return false
	}
	cwd = strings.TrimSpace(cwd)
	for _, g := range grants {
		if g.Tool != tool {
			continue
		}
		if g.Scope == grantScopeWorkspace {
			if cwd == "" || !pathWithin(cwd, g.Workspace) {
				continue
			}
		}
		switch g.Kind {
		case grantKindPrefix:
			// 只对 run_command 有意义：命令首词与规则值比较。
			cmd := strings.TrimSpace(argString(args, "command"))
			if cmd == "" {
				continue
			}
			if commandFamily(cmd) == strings.ToLower(strings.TrimSpace(g.Value)) {
				return true
			}
		case grantKindPath:
			p := strings.TrimSpace(argString(args, "path"))
			if p == "" {
				continue
			}
			abs := p
			// Windows 上 filepath.IsAbs("/proj/src/a.go") 是 false（缺盘符），
			// 但 POSIX 风格路径在别的平台上就是绝对路径。两边都认，
			// 否则同一条规则在不同平台上会落到不同的解析分支上。
			if !filepath.IsAbs(abs) && !strings.HasPrefix(abs, "/") && !strings.HasPrefix(abs, `\`) && cwd != "" {
				abs = filepath.Join(cwd, p)
			}
			if pathWithin(abs, g.Value) {
				return true
			}
		case grantKindExact:
			key, ok := approvalGrantKey(tool, args)
			if ok && key == g.Value {
				return true
			}
		}
	}
	return false
}

// pathWithin 判断 target 是否在 dir 之内（含 dir 本身）。
//
// 两边都先 Clean：`/a/b/../c` 与 `/a/c` 必须判定为同一处，
// 否则规则绕过只需要在路径里塞一个 `..`。
func pathWithin(target, dir string) bool {
	target = filepath.Clean(strings.TrimSpace(target))
	dir = filepath.Clean(strings.TrimSpace(dir))
	if target == "" || dir == "" || dir == "." {
		return false
	}
	if target == dir {
		return true
	}
	return strings.HasPrefix(target, dir+string(filepath.Separator))
}

// commandFamily 取命令首词（去掉常见的前缀包装与路径）。
func commandFamily(cmd string) string {
	fields := strings.Fields(strings.TrimSpace(cmd))
	for _, f := range fields {
		f = strings.ToLower(f)
		// 跳过 `sudo`/`env` 之类的前缀与 VAR=val 赋值。
		if f == "sudo" || f == "env" || strings.Contains(f, "=") {
			continue
		}
		// 取 basename：`/usr/bin/npm` 与 `npm` 是同一族。
		if i := strings.LastIndexAny(f, `/\`); i >= 0 {
			f = f[i+1:]
		}
		return f
	}
	return ""
}

func argString(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// grantAllowedTool 这个工具能不能被沉淀。
//
// 三条硬约束，缺一条都会变成安全洞：
//   - 只有 LevelWrite：LevelRisk（任意命令、杀进程、删数据）永不沉淀；
//   - 只读工具不需要（本来就不问）；
//   - 工具必须声明过 GrantKey（rememberable），否则「记住」的粒度无从定义。
func (a *AIService) grantAllowedTool(name string) bool {
	sf := a.aiSurface("chat", aiPermissionFull, "", nil)
	t, ok := sf.Tool(name)
	if !ok {
		return false
	}
	if t.Level != mcpsrv.LevelWrite {
		return false
	}
	// 只判「有没有声明 GrantKey」，不调用它：那个函数要读参数才能算出 key 与
	// 粒度（run_command 看命令首词），而这里手上没有参数。声明存在就说明
	// 这个工具设计上支持「记住」，具体粒度由它自己决定。
	return t.GrantKey != nil
}

// AIListApprovalGrants 列出全部沉淀规则（设置页用）。
func (a *AIService) AIListApprovalGrants() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	grants := a.loadApprovalGrants()
	views := make([]AIGrantView, 0, len(grants))
	for _, g := range grants {
		views = append(views, AIGrantView{
			Tool: g.Tool, Kind: g.Kind, Value: g.Value, Scope: g.Scope,
			Workspace: g.Workspace, CreatedAt: g.CreatedAt,
			Note: grantNote(g),
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Tool != views[j].Tool {
			return views[i].Tool < views[j].Tool
		}
		return views[i].Value < views[j].Value
	})
	return services.Ok(views)
}

func grantNote(g aiApprovalGrant) string {
	switch g.Kind {
	case grantKindPrefix:
		return "「" + g.Value + "」开头的命令不再逐条确认"
	case grantKindPath:
		return "「" + g.Value + "」目录下的文件改动不再逐条确认"
	default:
		return "这一具体调用不再确认"
	}
}

// AIAddApprovalGrant 新增一条沉淀规则。
//
// kind 的取值受限：run_command 只允许 prefix（一条命令的完整字符串没法回放，
// 而「首词」才是用户真正在批准的东西）；文件类只允许 path。
func (a *AIService) AIAddApprovalGrant(tool, kind, value, scope, workspace string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	tool = strings.TrimSpace(tool)
	kind = strings.TrimSpace(kind)
	value = strings.TrimSpace(value)
	scope = strings.TrimSpace(scope)
	workspace = strings.TrimSpace(workspace)

	if !a.grantAllowedTool(tool) {
		return services.FailMsg("这个工具不允许沉淀放行规则（只有低危写操作可以）")
	}
	if kind == "" {
		// 没指定就按工具推：run_command 用前缀，文件类用目录。
		if tool == "run_command" {
			kind = grantKindPrefix
		} else {
			kind = grantKindPath
		}
	}
	if tool == "run_command" && kind != grantKindPrefix {
		return services.FailMsg("命令类只能按命令前缀沉淀")
	}
	if tool != "run_command" && kind != grantKindPath {
		return services.FailMsg("文件类只能按目录沉淀")
	}
	if scope == "" {
		scope = grantScopeWorkspace
	}
	if scope != grantScopeGlobal && scope != grantScopeWorkspace {
		return services.FailMsg("作用域只能是 global 或 workspace")
	}
	if scope == grantScopeWorkspace && workspace == "" {
		return services.FailMsg("按目录生效时必须给出目录")
	}
	if kind == grantKindPrefix {
		value = commandFamily(value)
		if value == "" {
			return services.FailMsg("无法从命令里取到命令名")
		}
		// 命令首词只允许字母数字与常见符号：带 shell 元字符的「首词」
		// 说明这条命令本身就不是简单调用，不该沉淀成族级放行。
		if strings.ContainsAny(value, ";&|>$`()'\"") {
			return services.FailMsg("命令里含 shell 元字符，不能沉淀为族级放行")
		}
	} else if kind == grantKindPath {
		value = filepath.Clean(value)
	}

	grants := a.loadApprovalGrants()
	for _, g := range grants {
		if g.Tool == tool && g.Kind == kind && g.Value == value &&
			g.Scope == scope && g.Workspace == workspace {
			return services.Ok(nil) // 已经存在，幂等
		}
	}
	grants = append(grants, aiApprovalGrant{
		Tool: tool, Kind: kind, Value: value, Scope: scope,
		Workspace: workspace, CreatedAt: time.Now().Format(time.RFC3339),
	})
	if err := a.saveApprovalGrants(grants); err != nil {
		return services.Fail(err)
	}
	logger.I("[ai-approval] 已沉淀放行规则 tool=%s kind=%s value=%s scope=%s", tool, kind, value, scope)
	return services.Ok(nil)
}

// AIDeleteApprovalGrant 撤销一条沉淀规则。删除后立即生效（下一轮读库）。
func (a *AIService) AIDeleteApprovalGrant(tool, kind, value, scope, workspace string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	grants := a.loadApprovalGrants()
	kept := make([]aiApprovalGrant, 0, len(grants))
	removed := 0
	for _, g := range grants {
		if g.Tool == strings.TrimSpace(tool) && g.Kind == strings.TrimSpace(kind) &&
			g.Value == strings.TrimSpace(value) && g.Scope == strings.TrimSpace(scope) &&
			g.Workspace == strings.TrimSpace(workspace) {
			removed++
			continue
		}
		kept = append(kept, g)
	}
	if removed == 0 {
		return services.FailMsg("没有找到这条规则（可能已被撤销）")
	}
	if err := a.saveApprovalGrants(kept); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
