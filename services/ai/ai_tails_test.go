package ai

// 两个尾巴的守卫：
//   - WP17：同内容用户消息的去重窗口（只有「刚刚」的才算重试）；
//   - WP2 ：压缩状态要能出现在上下文弹框的数据里。

import (
	"testing"
	"time"

	"quickdock/internal/db"
)

// 同内容重发：紧跟着的重试要吞掉，隔了一段时间的手动重发必须落库。
func TestSaveUserTurnRetryWindow(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	if err := svc.saveUserTurn(conv.ID, "继续", "", ""); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	// 同一次发送内部的重试：内容相同且刚刚写过 → 不重复。
	if err := svc.saveUserTurn(conv.ID, "继续", "", ""); err != nil {
		t.Fatalf("重试写入失败: %v", err)
	}
	msgs, _ := d.ListAIMessages(conv.ID)
	if len(msgs) != 1 {
		t.Fatalf("窗口内的重复应被吞掉，实际 %d 条", len(msgs))
	}

	// 把它回拨到窗口之外：这代表「上一轮失败、用户等了一会又发一次」。
	old := time.Now().Add(-2 * time.Minute).Format(time.RFC3339)
	if err := d.ExecuteParams(`UPDATE ai_messages SET created_at = ? WHERE conv_id = ?`,
		[]interface{}{old, conv.ID}); err != nil {
		t.Fatalf("回拨时间失败: %v", err)
	}
	if err := svc.saveUserTurn(conv.ID, "继续", "", ""); err != nil {
		t.Fatalf("第二次写入失败: %v", err)
	}
	msgs, _ = d.ListAIMessages(conv.ID)
	if len(msgs) != 2 {
		t.Fatalf("窗口外的同内容消息必须落库（否则用户会觉得「点了没反应」），实际 %d 条", len(msgs))
	}
}

// 时间解析不出来时按「不是重试」处理：宁可多一条，也不能吞用户的话。
func TestUserTurnRetryWindowParseFallback(t *testing.T) {
	if aiUserTurnWithinRetryWindow("") {
		t.Error("空时间应视为不是重试")
	}
	if aiUserTurnWithinRetryWindow("not-a-time") {
		t.Error("非法时间应视为不是重试")
	}
	if !aiUserTurnWithinRetryWindow(time.Now().Format(time.RFC3339)) {
		t.Error("刚刚应算重试窗口内")
	}
}

// 压缩状态：记下来之后，上下文快照要带出去。
func TestContextStatsCarriesLastCompact(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	// 没压过时字段为空 / 为 0，前端据此不渲染那一行。
	view, ok := svc.AIContextStats(conv.ID).Data.(AIContextStatsView)
	if !ok {
		t.Fatal("返回类型不对")
	}
	if view.LastCompactAt != "" || view.LastCompactBefore != 0 {
		t.Fatalf("未压缩时不该有压缩记录: %+v", view)
	}

	rememberCompaction(conv.ID, 90000, 40000, 12, 3, false)
	view, _ = svc.AIContextStats(conv.ID).Data.(AIContextStatsView)
	if view.LastCompactAt == "" {
		t.Error("压缩后应有时间戳")
	}
	if view.LastCompactBefore != 90000 || view.LastCompactAfter != 40000 {
		t.Errorf("压缩前后数字不对: %d → %d", view.LastCompactBefore, view.LastCompactAfter)
	}
	if view.LastCompactFolded != 12 || view.LastCompactSummarized != 3 {
		t.Errorf("折叠/摘要条数不对: %d / %d", view.LastCompactFolded, view.LastCompactSummarized)
	}
	if view.LastCompactByModel {
		t.Error("循环内折叠不该标成「模型摘要」")
	}

	// 摘要压缩（真调模型）要能区分开：前者贵、后者几毫秒。
	rememberCompaction(conv.ID, 120000, 30000, 0, 40, true)
	view, _ = svc.AIContextStats(conv.ID).Data.(AIContextStatsView)
	if !view.LastCompactByModel {
		t.Error("摘要压缩应标成 byModel")
	}
	if view.LastCompactSummarized != 40 {
		t.Errorf("摘要条数不对: %d", view.LastCompactSummarized)
	}
}
