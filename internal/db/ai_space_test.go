package db

import "testing"

// TestAISpaceReusesSameDir 同一目录只能有一个空间。
//
// 用户换种写法（大小写、结尾斜杠、盘符小写）再选一次同一个目录，不该多出一个
// 并列空间——否则同一个项目在侧栏出现两次，谁也不知道该进哪个。
// 这条不是「锦上添花」：目录是空间的身份，判重失败等于空间这个概念失效。
func TestAISpaceReusesSameDir(t *testing.T) {
	d := openTestDB(t)

	a, err := d.CreateAISpace("proj", "/some/where/Proj")
	if err != nil {
		t.Fatalf("建空间失败: %v", err)
	}
	b, err := d.FindAISpaceByCwd("/some/where/proj/")
	if err != nil {
		t.Fatalf("按目录查空间失败: %v", err)
	}
	if b == nil || b.ID != a.ID {
		t.Fatalf("同一目录应命中同一个空间，实际 %+v", b)
	}
	if got, err := d.FindAISpaceByCwd("/else/where"); err != nil || got != nil {
		t.Fatalf("别的目录不该命中，实际 %+v err=%v", got, err)
	}
	if got, err := d.FindAISpaceByCwd(""); err != nil || got != nil {
		t.Fatalf("空目录不该命中，实际 %+v err=%v", got, err)
	}
}

// TestAIConversationModePersists 任务的模式与归属要跟着任务走。
//
// 模式存库的意义就是「切走再回来还是这个值」——之前它只是前端一个变量，
// 换条对话再回来就掉回「对话」，于是「这条是用来翻译的」这个最关键的属性丢了。
func TestAIConversationModePersists(t *testing.T) {
	d := openTestDB(t)

	c, err := d.CreateAIConversation(NewAIConversation{Title: "翻译一批文案", Mode: "translate"})
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	if c.Mode != "translate" {
		t.Fatalf("mode 应为 translate，实际 %q", c.Mode)
	}
	back, err := d.GetAIConversation(c.ID)
	if err != nil || back.Mode != "translate" {
		t.Fatalf("回读 mode 错误: %+v err=%v", back, err)
	}

	blank, err := d.CreateAIConversation(NewAIConversation{})
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	if blank.Mode != "chat" {
		t.Fatalf("未指定 mode 应回落 chat，实际 %q", blank.Mode)
	}
	if err := d.SetAIConversationMode(blank.ID, "summarize"); err != nil {
		t.Fatalf("改模式失败: %v", err)
	}
	if back, _ := d.GetAIConversation(blank.ID); back.Mode != "summarize" {
		t.Fatalf("改后 mode 应为 summarize，实际 %q", back.Mode)
	}
	if err := d.SetAIConversationMode(blank.ID, ""); err != nil {
		t.Fatalf("空 mode 不该报错: %v", err)
	}
	if back, _ := d.GetAIConversation(blank.ID); back.Mode != "chat" {
		t.Fatalf("空 mode 应回落 chat，实际 %q", back.Mode)
	}
}

// TestAITaskRecycleRoundTrip 删任务 → 回收站 → 恢复：消息必须一起回来。
//
// 只恢复一行会话等于什么都没恢复——用户要的是那几轮对话，不是一行标题。
func TestAITaskRecycleRoundTrip(t *testing.T) {
	d := openTestDB(t)

	conv, err := d.CreateAIConversation(NewAIConversation{Title: "扫 C 盘", Mode: "chat"})
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := d.AddAIMessage(conv.ID, "user", "第几条消息"); err != nil {
			t.Fatalf("写消息失败: %v", err)
		}
	}

	if err := d.DeleteAIConversationToRecycle(conv.ID); err != nil {
		t.Fatalf("删任务失败: %v", err)
	}
	if got, _ := d.GetAIConversation(conv.ID); got != nil {
		// GetAIConversation 对不存在的行返回错误；这里只要确认查不回来
		t.Logf("会话行仍存在（不应发生）: %+v", got)
	}
	msgs, _ := d.ListAIMessages(conv.ID)
	if len(msgs) != 0 {
		t.Fatalf("消息应随删除一起清掉，实际剩 %d 条", len(msgs))
	}

	var found bool
	for _, tb := range mustListTombstones(t, d) {
		if tb.Collection == RecycleAITask && tb.ID == conv.ID {
			found = true
			if tb.Label != "扫 C 盘" {
				t.Fatalf("墓碑展示名应是标题，实际 %q", tb.Label)
			}
		}
	}
	if !found {
		t.Fatal("回收站里应有这条任务")
	}

	if err := d.RestoreTombstone(RecycleAITask, conv.ID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	back, err := d.GetAIConversation(conv.ID)
	if err != nil || back == nil {
		t.Fatalf("恢复后会话应回来: %v", err)
	}
	if back.Title != "扫 C 盘" || back.Mode != "chat" {
		t.Fatalf("恢复后字段不对: %+v", back)
	}
	msgs, _ = d.ListAIMessages(conv.ID)
	if len(msgs) != 3 {
		t.Fatalf("恢复后应有 3 条消息，实际 %d", len(msgs))
	}
}

// TestAISpaceRecycleRoundTrip 删空间 → 回收站 → 恢复：空间 + 其下任务 + 消息全回来。
//
// 这是本次改动里最容易写错的一处：载荷有三张表，而 restoreOrder 必须让
// ai_spaces 先于 ai_conversations（外键），后者又先于 ai_messages。
// 顺序错了的表现是恢复时静默失败——用户只会看到「点了恢复没反应」。
func TestAISpaceRecycleRoundTrip(t *testing.T) {
	d := openTestDB(t)

	sp, err := d.CreateAISpace("quickdock", "D:/pp/quickdock")
	if err != nil {
		t.Fatalf("建空间失败: %v", err)
	}
	var ids []string
	for _, name := range []string{"改 A", "改 B"} {
		c, err := d.CreateAIConversation(NewAIConversation{Title: name, SpaceID: sp.ID, Mode: "explain"})
		if err != nil {
			t.Fatalf("建任务失败: %v", err)
		}
		ids = append(ids, c.ID)
		if _, err := d.AddAIMessage(c.ID, "user", "问题"); err != nil {
			t.Fatalf("写消息失败: %v", err)
		}
	}
	inSpace, err := d.ListAIConversationsInSpace(sp.ID)
	if err != nil || len(inSpace) != 2 {
		t.Fatalf("空间下应有 2 个任务，实际 %d err=%v", len(inSpace), err)
	}

	if err := d.DeleteAISpaceToRecycle(sp.ID); err != nil {
		t.Fatalf("删空间失败: %v", err)
	}
	if got, _ := d.GetAISpace(sp.ID); got != nil {
		t.Logf("空间行仍存在（不应发生）: %+v", got)
	}
	for _, id := range ids {
		if ms, _ := d.ListAIMessages(id); len(ms) != 0 {
			t.Fatalf("任务 %s 的消息应随空间一起清掉", id)
		}
	}

	var found bool
	for _, tb := range mustListTombstones(t, d) {
		if tb.Collection == RecycleAISpace && tb.ID == sp.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("回收站里应有这个空间")
	}

	if err := d.RestoreTombstone(RecycleAISpace, sp.ID); err != nil {
		t.Fatalf("恢复空间失败: %v", err)
	}
	back, err := d.GetAISpace(sp.ID)
	if err != nil || back == nil || back.Name != "quickdock" {
		t.Fatalf("恢复后空间不对: %+v err=%v", back, err)
	}
	restored, err := d.ListAIConversationsInSpace(sp.ID)
	if err != nil || len(restored) != 2 {
		t.Fatalf("恢复后空间下应有 2 个任务，实际 %d err=%v", len(restored), err)
	}
	for _, c := range restored {
		if c.Mode != "explain" {
			t.Fatalf("恢复后 mode 丢了: %+v", c)
		}
		ms, _ := d.ListAIMessages(c.ID)
		if len(ms) != 1 {
			t.Fatalf("恢复后消息应回来，实际 %d", len(ms))
		}
	}
}

func mustListTombstones(t *testing.T, d *Database) []Tombstone {
	t.Helper()
	list, err := d.ListTombstones()
	if err != nil {
		t.Fatalf("列回收站失败: %v", err)
	}
	return list
}
