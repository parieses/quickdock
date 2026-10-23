package aiutil

import (
	"strings"
	"unicode/utf8"
)

// 这个文件解决一个反复出现的问题：**给 AI 拼 prompt 时，被裁掉的往往是用户的问题**。
//
// 背景：services/ai.AIAsk / AIAskStream 超预算时**从头截断**（见 ai.AskMaxUserRune）。
// 也就是说调用方把什么排在最末尾，什么就最先被砍掉。之前 SSH 面板把「本次问题」
// 放最后（为了注意力），结果屏显 + 历史一满，用户的问题整段消失 → 多轮追问答非所问，
// 且极难复现。
//
// 所以规则是：**任何拼 prompt 的地方都必须自己按优先级做预算**，不能依赖 AIAsk 的截断。
// 这段逻辑此前在 services/sshx 与 services/clipboard 各写了一份，第三、第四个域
// 再各写一份，优先级口径必然漂移（一个把历史算进正文预算、另一个不算，
// 表现就是"同一个模型，这个面板答得比那个面板蠢"）。
//
// 现在收敛到这里，各域只声明"有哪些段、谁更重要"。

// Section 提示词中的一段上下文。
//
// 数组顺序即渲染顺序（模型读到的顺序），**不代表优先级**：
// 优先级由 Pinned / MaxShare / 数组位置（越靠后越重要）共同决定。
type Section struct {
	// Title 小节标题（渲染为 "## 标题"）。空串表示不加标题，只放正文。
	Title string
	// Body 段内容。为空时整段跳过。
	Body string
	// KeepTail 超预算时保留**尾部**。
	// 日志、终端屏显这类内容是「越新越相关」，砍掉开头比砍掉结尾损失小。
	KeepTail bool
	// Pinned 不参与裁剪：整段保留。
	// 用于模型据此决定"该有多保守"的前提信息（连接信息、环境约束），
	// 它被裁掉不是少一点细节，而是整段回答的口径都变了。
	Pinned bool
	// MaxShare 本段最多占用总预算的比例（0 = 不限）。
	// 典型用法：终端屏显限 0.5，把另一半留给历史与多轮连贯性。
	MaxShare float64
	// DropWhenTight 放不下时**整段丢弃**，而不是截成半句残话。
	// 半段截断的内容对模型是噪音（"日志的前 30 个字"既不能说清楚问题，
	// 又占掉了预算），不如干脆不给。
	DropWhenTight bool
}

// Prompt 一次问答的提示词装配参数。
type Prompt struct {
	// Max 总预算（rune）。调用方传 ai.AskMaxUserRune；<=0 表示不限制。
	//
	// 为什么不让本包直接引用 ai.AskMaxUserRune：internal 不能反向依赖 services，
	// 且不同调用方未来可能走不同通道（例如插件侧的更长上限）。
	Max int
	// Sections 上下文段，按「越靠后越重要」排列。
	Sections []Section
	// History 多轮历史（从旧到新）。装配时从**新往旧**塞，塞不下就丢更旧的。
	History []string
	// HistoryTitle 历史小节标题，空则用「之前的对话」。
	HistoryTitle string
	// HistoryItemMax 单条历史的 rune 上限（0 = 不限）。超出部分截断并加省略号。
	HistoryItemMax int
	// HistoryMaxTurns 最多带回几轮（0 = 不限）。
	HistoryMaxTurns int
	// Task 本次要求/问题。
	//
	// 它**永远最后渲染、永远不裁剪**——这是整个装配器存在的理由。
	Task string
	// TaskTitle 本次要求的小节标题，空则用「本次要求」。
	TaskTitle string
}

const (
	defaultHistoryTitle = "之前的对话"
	defaultTaskTitle    = "本次要求"
	// 每个 block 之间会插入 "\n\n"，预扣 2 个 rune，避免最后算出总量仍超预算。
	blockSeparatorCost = 2
)

// BuildPrompt 按优先级把各段装配成一段提示词，保证结果不超预算。
//
// 优先级（高 → 低）：
//  1. Task —— 永不裁剪
//  2. Pinned 段 —— 永不裁剪
//  3. 非固定段 —— 从后往前分配，均分剩余预算（后面的段更重要）
//  4. History —— 用前面吃剩的预算，从新往旧塞
//
// 最后还有一道兜底：万一某处算漏（标题长度差异等），按「保留尾部」裁一次，
// 仍然保证问题活着，而不是交给 AIAsk 从头砍。
func BuildPrompt(p Prompt) string {
	blocks := make([]string, len(p.Sections))
	built := make([]bool, len(p.Sections))

	fixedCost := 0

	// 1) 固定段：整段保留，先占住成本。
	for i, s := range p.Sections {
		if !s.Pinned {
			continue
		}
		body := strings.TrimSpace(s.Body)
		if body == "" && s.Title == "" {
			continue
		}
		blocks[i] = renderBlock(s.Title, body)
		built[i] = true
		fixedCost += runeCount(blocks[i]) + blockSeparatorCost
	}

	taskTitle := p.TaskTitle
	if taskTitle == "" {
		taskTitle = defaultTaskTitle
	}
	taskBlock := ""
	if body := strings.TrimSpace(p.Task); body != "" {
		taskBlock = renderBlock(taskTitle, body)
		fixedCost += runeCount(taskBlock) + blockSeparatorCost
	}

	// 2) 其余预算给非固定段。
	remaining := -1 // <0 表示不限
	if p.Max > 0 {
		remaining = p.Max - fixedCost
		if remaining < 0 {
			remaining = 0
		}
	}

	pending := 0
	for i, s := range p.Sections {
		if built[i] || strings.TrimSpace(s.Body) == "" {
			continue
		}
		pending++
	}

	for i := len(p.Sections) - 1; i >= 0 && pending > 0; i-- {
		s := p.Sections[i]
		if built[i] {
			continue
		}
		body := strings.TrimSpace(s.Body)
		if body == "" {
			continue
		}

		// 均分剩余预算：前面的段没吃完的余量会自然留给更靠前的段。
		share := -1
		if remaining >= 0 {
			share = remaining / pending
			if s.MaxShare > 0 {
				if limit := int(float64(p.Max) * s.MaxShare); limit < share {
					share = limit
				}
			}
		}

		// 标题与块间距是**固定开销**，正文只能用 share 剩下的部分。
		// 早期版本直接拿 share 当正文预算，结果块实际长度总是超出 share 一个标题，
		// 末尾那道兜底裁剪就会回头把这一段整体砍掉——表现为"给了预算的段反而消失"。
		overhead := runeCount(renderBlock(s.Title, "")) + blockSeparatorCost
		bodyBudget := -1
		if share >= 0 {
			bodyBudget = share - overhead
			if bodyBudget < 0 {
				bodyBudget = 0
			}
		}

		// DropWhenTight：容不下整段就整段消失，不留残句。
		if bodyBudget == 0 && s.DropWhenTight {
			pending--
			continue
		}
		if bodyBudget >= 0 && runeCount(body) > bodyBudget {
			if s.DropWhenTight {
				pending--
				continue
			}
			body = TrimRunes(body, bodyBudget, s.KeepTail)
		}

		blocks[i] = renderBlock(s.Title, body)
		built[i] = true
		pending--
		if remaining >= 0 {
			remaining -= runeCount(blocks[i]) + blockSeparatorCost
			if remaining < 0 {
				remaining = 0
			}
		}
	}

	// 3) 历史用剩下的预算。
	historyBlock := ""
	if b := packHistory(p, remaining); b != "" {
		historyBlock = b
		if remaining >= 0 {
			remaining -= runeCount(b) + blockSeparatorCost
		}
	}

	// 4) 按原始顺序拼装：上下文 → 历史 → 本次要求。
	out := make([]string, 0, len(p.Sections)+2)
	for i := range p.Sections {
		if built[i] {
			out = append(out, blocks[i])
		}
	}
	if historyBlock != "" {
		out = append(out, historyBlock)
	}
	if taskBlock != "" {
		out = append(out, taskBlock)
	}
	res := strings.Join(out, "\n\n")

	// 兜底：保留尾部（问题在最后），别让 AIAsk 从头砍。
	if p.Max > 0 && runeCount(res) > p.Max {
		res = TrimRunes(res, p.Max, true)
	}
	return res
}

// packHistory 把历史从新往旧塞进剩余预算。
//
// 从新往旧而不是从旧往新：多轮追问里最近一轮才是上下文，
// 更早的丢掉只是少一点连贯性，丢掉最近一轮则直接断片。
func packHistory(p Prompt, remaining int) string {
	if len(p.History) == 0 {
		return ""
	}
	hs := p.History
	if p.HistoryMaxTurns > 0 && len(hs) > p.HistoryMaxTurns {
		hs = hs[len(hs)-p.HistoryMaxTurns:]
	}

	kept := make([]string, 0, len(hs))
	for i := len(hs) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hs[i])
		if h == "" {
			continue
		}
		if p.HistoryItemMax > 0 && runeCount(h) > p.HistoryItemMax {
			// 保留头部：一轮回答的结论通常在最前面。
			h = TrimRunes(h, p.HistoryItemMax, false) + " …"
		}
		// 历史压成单行：多行会把小节撑得很长，且换行对模型没有额外语义。
		line := "- " + strings.ReplaceAll(h, "\n", " ") + "\n"
		cost := runeCount(line)
		if remaining >= 0 && cost > remaining {
			break
		}
		if remaining >= 0 {
			remaining -= cost
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return ""
	}

	title := p.HistoryTitle
	if title == "" {
		title = defaultHistoryTitle
	}
	var b strings.Builder
	b.WriteString("## " + title + "\n")
	// kept 是从新到旧收集的，回填时反一下变成正常的阅读顺序。
	for i := len(kept) - 1; i >= 0; i-- {
		b.WriteString(kept[i])
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderBlock 渲染一个小节。没有标题时只输出正文。
func renderBlock(title, body string) string {
	body = strings.TrimSpace(body)
	if title == "" {
		return body
	}
	if body == "" {
		return "## " + title
	}
	return "## " + title + "\n" + body
}

func runeCount(s string) int {
	return utf8.RuneCountInString(s)
}
