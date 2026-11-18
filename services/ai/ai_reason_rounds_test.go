package ai

// 分轮思考的编码与回落（WP20-b）。
//
// 用户的原话：「我这就一个统一的思考在最上面，这个不太合适吧」。
// 成因是一条回复只存一条 assistant 消息，所有轮的思考被拼进一个字段。
// 这里验的是「拆开之后仍然不丢、不串、旧数据还能看」。

import (
	"encoding/json"
	"testing"

	"quickdock/internal/db"
)

func TestEncodeReasoningRounds(t *testing.T) {
	// 空内容（模型某些轮没给思考）不该产出空条目。
	got := encodeReasoningRounds([]aiReasoningRound{
		{Round: 1, Text: "先看目录结构"},
		{Round: 2, Text: "   "},
		{Round: 3, Text: "再跑测试"},
	})
	if got == "" {
		t.Fatal("有内容时不该是空串")
	}
	var back []aiReasoningRound
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if len(back) != 2 {
		t.Fatalf("空思考应被丢掉，实际 %d 条: %s", len(back), got)
	}
	if back[0].Round != 1 || back[1].Round != 3 {
		t.Errorf("轮次丢失: %v", back)
	}
	if back[1].Text != "再跑测试" {
		t.Errorf("文本不对: %q", back[1].Text)
	}
}

// 全空 → 空串（前端只有一个判据：有没有分轮信息）。
func TestEncodeReasoningRoundsEmpty(t *testing.T) {
	if got := encodeReasoningRounds(nil); got != "" {
		t.Errorf("nil 应编码成空串，实际 %q", got)
	}
	if got := encodeReasoningRounds([]aiReasoningRound{{Round: 1, Text: "  "}}); got != "" {
		t.Errorf("全空白应编码成空串，实际 %q", got)
	}
}

// 落库往返：SaveAssistantTurn 写入后能读回来，且不影响 reasoning_content。
func TestReasoningRoundsRoundTrip(t *testing.T) {
	_, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	rounds := encodeReasoningRounds([]aiReasoningRound{
		{Round: 1, Text: "读一下文件"},
		{Round: 2, Text: "改这两行"},
	})
	msg, err := d.SaveAssistantTurn(conv.ID, db.AssistantTurn{
		Content:          "正文",
		ReasoningContent: "第一轮：读一下文件\n\n第二轮：改这两行",
		ReasoningRounds:  rounds,
	})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if msg.ReasoningRounds != rounds {
		t.Errorf("分轮思考没落库: %q", msg.ReasoningRounds)
	}

	msgs, err := d.ListAIMessages(conv.ID)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("读回失败: %v %d", err, len(msgs))
	}
	if msgs[0].ReasoningRounds == "" {
		t.Error("读回时分轮思考丢了（检查 validColumns 与列迁移）")
	}
	// 拼接版仍然保留：导出、复制、旧前端都靠它。
	if msgs[0].ReasoningContent == "" {
		t.Error("reasoning_content 不该为空")
	}
}
