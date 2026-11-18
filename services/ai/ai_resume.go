package ai

// 任务状态与续跑（WP17）。
//
// 修的是两个同源问题：清单只走 SSE（刷新/中断即丢），而「继续」只是一条普通
// user 消息（模型从零重新规划，清单被重建）。
//
// 做法刻意保持最小：不引入 goal 状态机、不加轮次调度，只用会话上的三个字段
// （goal / plan / plan_status）回答一个问题——「这条对话上次做到哪了」。
// QuickDock 的对话是人盯着、一问一答的形态，不是无人值守长跑，
// 一个目标 + 一份清单覆盖绝大多数场景。

import (
	"encoding/json"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	aitools "quickdock/services/ai/tools"
)

const (
	// 续跑提示里最多列几条未完成项、每条多长。
	// 上限存在的理由：清单可能有二十条，全塞进 system 会挤掉真正的工作内容。
	aiResumeMaxItems    = 8
	aiResumeItemRunes   = 60
	aiResumeGoalRunes   = 120
	aiResumeSegmentRune = 700
)

// aiGoalOf 把首条用户消息裁成一句「目标」。
//
// 换行折成空格而不是截断成首行：用户常常先写一句总述再换行列要求，
// 只取首行会把「帮我重构这几个文件」这类真正说明意图的部分丢掉。
func aiGoalOf(message string) string {
	s := strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n"))
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	return clipRunes(s, aiResumeGoalRunes)
}

// planStatusOf 由清单内容推出状态：还有未完成项就是 running。
//
// 不给模型「手动标完成」的入口：它只要把最后一条改成 done，状态自然变 done。
// 多一个参数就多一种「说完成但没完成」的骗法。
func planStatusOf(items []aitools.PlanItem) string {
	for _, it := range items {
		if strings.TrimSpace(it.Text) == "" {
			continue
		}
		if strings.TrimSpace(it.Status) != "done" {
			return "running"
		}
	}
	return "done"
}

// saveConversationPlan 把清单落到会话上（失败只记 W：清单是旁路，
// 写不进去不该让正在跑的任务中断）。
func (a *AIService) saveConversationPlan(convID string, u aitools.PlanUpdate) {
	if a == nil || a.App == nil || a.App.DB == nil || strings.TrimSpace(convID) == "" {
		return
	}
	raw, err := json.Marshal(u)
	if err != nil {
		logger.W("[ai] 清单序列化失败 conv=%s: %v", convID, err)
		return
	}
	if err := a.App.DB.SetAIConversationPlan(convID, string(raw), planStatusOf(u.Items)); err != nil {
		logger.W("[ai] 清单落库失败 conv=%s: %v", convID, err)
	}
}

// aiResumeMarkers 短指令前缀：只用来判断「这句是不是在说接着干」。
//
// 为什么要判：用户打「继续」两个字时，模型没有任何依据知道「继续什么」；
// 没有这段提示，它只会从零重新规划，于是上一份清单被顶掉（用户看到的就是
// 「任务列表被重建」）。
var aiResumeMarkers = []string{
	"继续", "接着", "往下", "接上", "continue", "go on", "keep going", "carry on", "resume",
}

// isResumeCommand 判断是否纯续跑指令（不是「继续改 auth 中间件」那种带新内容的话）。
//
// 长度上限是关键：带实质要求的句子必须按新诉求处理，否则「继续，但改用另一个方案」
// 会被当成「照旧做」，反而更糟。
func isResumeCommand(message string) bool {
	s := strings.ToLower(strings.TrimSpace(message))
	if s == "" {
		return false
	}
	s = strings.Trim(s, " \t\r\n。，,.!！?？~～…")
	// 6 个汉字 ≈ 一句话的「无内容」上限；英文按字节放宽到 16。
	if len([]rune(s)) > 16 {
		return false
	}
	for _, m := range aiResumeMarkers {
		if !strings.HasPrefix(s, m) {
			continue
		}
		// 前缀之后允许「吧/啊/做/干」这类语气与泛动词，不允许再挂具体名词短语。
		rest := strings.TrimSpace(strings.TrimPrefix(s, m))
		if rest == "" {
			return true
		}
		return len([]rune(rest)) <= 3
	}
	return false
}

// resumeHintFor 生成续跑/清单提示；没有可用清单时返回空串。
//
// 两种语气分开写，因为要模型做的事不同：
//   - 纯「继续」：别重新规划，先更新原清单；
//   - 有新内容但旧清单没做完：新旧可能无关，允许重建，但必须知道有旧清单在。
func resumeHintFor(conv *db.AIConversation, message string) string {
	if conv == nil || strings.TrimSpace(conv.Plan) == "" {
		return ""
	}
	if strings.TrimSpace(conv.PlanStatus) == "done" {
		// 上一份清单已经全打勾：这不是续跑，别拿它干扰新任务。
		return ""
	}
	var p aitools.PlanUpdate
	if err := json.Unmarshal([]byte(conv.Plan), &p); err != nil {
		// 坏数据（历史版本 / 手改过的库）：宁可不提示，也不要给模型一段乱码清单。
		return ""
	}

	var pending []string
	for _, it := range p.Items {
		text := strings.TrimSpace(it.Text)
		if text == "" || strings.TrimSpace(it.Status) == "done" {
			continue
		}
		pending = append(pending, clipRunes(text, aiResumeItemRunes))
		if len(pending) >= aiResumeMaxItems {
			break
		}
	}
	if len(pending) == 0 {
		return ""
	}

	var b strings.Builder
	if isResumeCommand(message) {
		b.WriteString("\n\n【续跑】用户这句话是在让你接着上一次做，不是新任务。\n")
	} else {
		b.WriteString("\n\n【已有清单】这条对话的清单还没做完（见下）。\n")
	}
	if goal := strings.TrimSpace(conv.Goal); goal != "" {
		b.WriteString("上次的目标：")
		b.WriteString(clipRunes(goal, aiResumeGoalRunes))
		b.WriteString("\n")
	}
	b.WriteString("未完成项：\n")
	for i, t := range pending {
		b.WriteString("  ")
		b.WriteString(itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	if note := strings.TrimSpace(p.Note); note != "" {
		b.WriteString("上次的进展备注：")
		b.WriteString(clipRunes(note, aiResumeItemRunes))
		b.WriteString("\n")
	}
	if isResumeCommand(message) {
		b.WriteString("请先调用 plan_update 更新这份清单（把已完成的标 done、下一条标 doing），" +
			"不要另起一份新清单；若用户其实想做别的事，再重建。")
	} else {
		b.WriteString("若用户这次仍是同一件事，请更新这份清单而不是重建；若明显是另一件事，可以重建。")
	}
	return clipRunes(b.String(), aiResumeSegmentRune)
}
