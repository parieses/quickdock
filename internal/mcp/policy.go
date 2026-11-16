package mcp

import "sort"

// 暴露策略：谁能看到哪些工具。
//
// 这是本次拆分里最关键的一个概念。在此之前等级上限与单工具开关是**全局各一份**，
// 外部 MCP 客户端与内置 AI 共用，注释里把这当成优点（"权限只有一处真相"）。
// 但它成立的前提是两个入口是同一类消费者——实际上不是：
//
//   - 外部客户端是第三方 AI，用户对它的信任度、希望的开关位置都不同；
//   - 内置 AI 是软件自己的助手，用户希望它什么都能干、危险操作再问。
//
// 所以：**能力一份（Tool / Level / Domain），策略两份（MCP / AI）**。
// 本文件只管「一份策略长什么样、怎么判定」；两份策略各自存在自己的配置里
// （MCP 在 config.json，AI 在 settings 的 ai_tool_policy），互不读取。

// Policy 一份暴露策略：等级上限 + 被单独关掉的工具。
type Policy struct {
	MaxLevel int
	Disabled map[string]bool
}

// NewPolicy 构造策略，并把等级夹回合法区间。
//
// 越界的取值按「更严」的方向倒（负数→只读，超过高危→高危）：策略是安全开关，
// 解析失败或手改错时绝不能因为一个数字变成一个全放行的状态。
func NewPolicy(maxLevel int, disabled []string) Policy {
	if maxLevel < LevelRead {
		maxLevel = LevelRead
	}
	if maxLevel > LevelRisk {
		maxLevel = LevelRisk
	}
	p := Policy{MaxLevel: maxLevel, Disabled: make(map[string]bool, len(disabled))}
	for _, n := range disabled {
		if n != "" {
			p.Disabled[n] = true
		}
	}
	return p
}

// Allows 工具是否通过这份策略（等级 + 单工具开关）。
func (p Policy) Allows(t Tool) bool {
	return t.Level <= p.MaxLevel && !p.Disabled[t.Name]
}

// DisabledNames 被关掉的工具名（排序，便于落盘稳定）。
func (p Policy) DisabledNames() []string {
	out := make([]string, 0, len(p.Disabled))
	for name := range p.Disabled {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
