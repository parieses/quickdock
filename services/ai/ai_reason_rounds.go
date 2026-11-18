package ai

// 按轮拆开的思考（WP20-b）。
//
// 背景：一条助手回复可能跨十几轮（思考 → 调工具 → 再思考）。原来所有轮的
// 思考被拼进 `reasoning_content` 一个字段，界面只能在整条回复的顶部开一个抽屉，
// 用户看到的是「几千字的思考全堆在最上面」，而且没法知道哪段思考对应哪次调用。
//
// 这里把每轮的原文单独记下来（`reasoning_rounds`），界面就能把「第 N 轮在想什么」
// 摆在第 N 轮的工具卡片上方。`reasoning_content` 仍然保留拼接版：
// 导出、复制、以及「旧数据没有分轮信息」时的回落都靠它。

import (
	"encoding/json"
	"strings"
)

// aiReasoningRound 一轮的思考。
type aiReasoningRound struct {
	Round int    `json:"round"`
	Text  string `json:"text"`
}

// encodeReasoningRounds 把按轮的思考编码成 JSON；没有内容时返回空串。
//
// 空串而不是 "[]"：旧数据与「模型没给思考」都走空串，前端只有一个判据。
func encodeReasoningRounds(rounds []aiReasoningRound) string {
	clean := make([]aiReasoningRound, 0, len(rounds))
	for _, r := range rounds {
		t := strings.TrimSpace(r.Text)
		if t == "" {
			continue
		}
		clean = append(clean, aiReasoningRound{Round: r.Round, Text: t})
	}
	if len(clean) == 0 {
		return ""
	}
	b, err := json.Marshal(clean)
	if err != nil {
		// 结构固定，序列化不该失败；真失败了就退化成「没有分轮信息」，
		// 界面上还有整段 reasoning_content 可看。
		return ""
	}
	return string(b)
}
