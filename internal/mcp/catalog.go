package mcp

import (
	"sort"
	"strings"
	"sync"
)

// Access 一个域在某个场景里的开放程度。
//
// 三档而不是布尔：只读档（readonly）需要「能看不能改」，而这个区别不能靠
// 单个工具的 Level 在装配时临时判断——那等于把「域策略」又拆回 81 个工具
// 各自的属性，加一个工具就要想一次。域级三档把这件事收敛到 17 条声明里。
type Access int

const (
	AccessNone Access = iota // 不挂
	AccessRead               // 只挂 LevelRead
	AccessFull               // 全挂（仍受 Policy.MaxLevel 裁）
)

// 能力档（场景）。只有内置 AI 用；外部 MCP 客户端没有场景概念。
//
// 两档而不是更多：用户真正能说清的需求只有「什么都能干（写操作问我）」和
// 「只准看」。再分「教学 / 代码 / 审阅」只会让设置页多出几个没人调的开关，
// 而子 agent 的收窄已经由角色（Scope.Keep）负责。
const (
	SceneAssistant = "assistant"
	SceneReadOnly  = "readonly"
)

// Domain 一个能力域：面板分组、提示词段落、场景策略的共同载体。
//
// 为什么要有它：在此之前 category 一个字段同时背了「UI 分组」和「可见性开关」，
// 于是每加一个域都要在五个地方各改一次（常量、i18n、面板顺序、提示词、白名单）。
// 现在这五件事都从这里派生，注册处只需要说「这条工具属于哪个域」。
type Domain struct {
	// ID 域标识，同时是工具名前缀的语义来源（env_list 属于 env）。
	ID string
	// LabelKey 面板标题的 i18n key。
	LabelKey string
	// Order 面板分组顺序，小的在前。
	Order int
	// Summary 一句话说明，面板与自检输出用。
	Summary string
	// Hint 注入 system 的说明段落（只读部分）：这里只能写必然挂载的工具名，
	// 写类工具放到 WriteHint 里，否则只读档的提示词会教模型去调一个没挂的函数。
	Hint string
	// WriteHint 该域有写工具挂载时追加的段落（怎么改、改之前看什么）。
	WriteHint string
	// External 是否暴露给外部 MCP 客户端。
	//
	// web / code 这类「在目录里干活」的域为 false：外部客户端自带抓网页与终端，
	// 再暴露一遍只会让模型在两套同名能力之间挑。这条边界以前只靠「两个注册表」
	// 隐式表达，现在写成字段，面板上也能解释清楚。
	External bool
	// Scenes 场景 → 开放程度。未声明的场景按 AccessFull 处理：
	// 新场景忘了声明时宁可多挂（还能靠 Policy 与审批兜底），也不要静默把能力藏空。
	Scenes map[string]Access
}

var (
	domainMu sync.RWMutex
	domains  = map[string]Domain{}
)

// DefineDomain 注册一个域。应在工具注册之前调用（Resolve 要用它判场景）。
//
// 重复注册按后者覆盖并留一条日志：热重载外部 server 时可能走到这里，
// panic 会让整个进程起不来，而「最后一个声明生效」是可预期的行为。
func DefineDomain(d Domain) {
	d.ID = strings.TrimSpace(d.ID)
	if d.ID == "" {
		return
	}
	domainMu.Lock()
	defer domainMu.Unlock()
	domains[d.ID] = d
}

// Domains 返回全部已定义域，按 Order、再按 ID 排序（面板顺序的唯一来源）。
func Domains() []Domain {
	domainMu.RLock()
	defer domainMu.RUnlock()
	out := make([]Domain, 0, len(domains))
	for _, d := range domains {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// DomainByID 取一个域。
func DomainByID(id string) (Domain, bool) {
	domainMu.RLock()
	defer domainMu.RUnlock()
	d, ok := domains[id]
	return d, ok
}

// DomainAllows 判断某域在某场景下是否挂载某个等级的工具。
//
// 未登记的域一律放行：注册处漏写 Domain 时，功能应该「能用但没分组」，
// 而不是整组消失——后者在界面上完全看不出来，排查成本高得多。
func DomainAllows(domainID, scene string, level int) bool {
	if scene == "" {
		return true
	}
	d, ok := DomainByID(domainID)
	if !ok {
		return true
	}
	access, declared := d.Scenes[scene]
	if !declared {
		return true
	}
	switch access {
	case AccessNone:
		return false
	case AccessRead:
		return level == LevelRead
	default:
		return true
	}
}

// DomainExposed 判断某域是否对外部 MCP 客户端开放。
func DomainExposed(domainID string) bool {
	d, ok := DomainByID(domainID)
	if !ok {
		return true
	}
	return d.External
}
