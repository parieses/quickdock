package sshx

import (
	"strings"
	"testing"
)

// SSH 问答应进「当前会话对应的一条 AI 任务」——用户的核心诉求是
// 「这次连接里的所有问答都在同一个任务里」，所以重点测三条：
// 同一会话复用任务、不同会话各自一条、关闭后重连换代。

func TestEnsureAIConvReusesPerSession(t *testing.T) {
	svc := newTestService(t)

	a1 := svc.ensureAIConv("sess-a", "root@10.0.0.1")
	a2 := svc.ensureAIConv("sess-a", "root@10.0.0.1")
	if a1 == "" || a1 != a2 {
		t.Fatalf("同一会话应复用同一个任务: %q / %q", a1, a2)
	}

	b := svc.ensureAIConv("sess-b", "root@10.0.0.2")
	if b == "" || b == a1 {
		t.Fatalf("不同会话应各自一个任务: a=%q b=%q", a1, b)
	}

	// 空 sessionID 不建任务（插件等无会话调用方走这条路）。
	if got := svc.ensureAIConv("", "root@x"); got != "" {
		t.Fatalf("空 sessionId 不该建任务，得到 %q", got)
	}
}

func TestRecordAiTurnWritesPair(t *testing.T) {
	svc := newTestService(t)
	convID := svc.ensureAIConv("sess-a", "root@10.0.0.1")

	svc.recordAiTurn(convID, "磁盘还有多少", "看这条：\n```bash\ndf -h\n```", "用的是 df")
	svc.recordAiTurn(convID, "那 inode 呢", "```bash\ndf -i\n```", "")

	msgs, err := svc.App.DB.ListAIMessages(convID)
	if err != nil {
		t.Fatalf("读取消息失败: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("两轮问答应有 4 条消息，得到 %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "磁盘还有多少" {
		t.Fatalf("第一条应是用户提问，得到 %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || !strings.Contains(msgs[1].Content, "df -h") {
		t.Fatalf("第二条应是助手回答，得到 %+v", msgs[1])
	}
	if msgs[1].ReasoningContent != "用的是 df" {
		t.Fatalf("思考内容应一并落库，得到 %q", msgs[1].ReasoningContent)
	}

	// 时间戳要跟着推：侧栏按 updated_at 排序，不推的话刚问完的任务会沉底。
	conv, err := svc.App.DB.GetAIConversation(convID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if conv.UpdatedAt == "" {
		t.Fatal("任务缺少 updated_at")
	}
}

func TestDropAIConvStartsNewTask(t *testing.T) {
	svc := newTestService(t)
	old := svc.ensureAIConv("sess-a", "root@10.0.0.1")
	svc.dropAIConv("sess-a")
	fresh := svc.ensureAIConv("sess-a", "root@10.0.0.1")
	if fresh == "" || fresh == old {
		t.Fatalf("关闭后重连应创建新任务: old=%q fresh=%q", old, fresh)
	}
	// 旧任务保留（用户仍可回看），只是不再被这条会话引用。
	if _, err := svc.App.DB.GetAIConversation(old); err != nil {
		t.Fatalf("旧任务不该被删除: %v", err)
	}
}

func TestConvTitleFor(t *testing.T) {
	if got := convTitleFor(""); !strings.Contains(got, "SSH") {
		t.Fatalf("主机未知时也该有 SSH 前缀，得到 %q", got)
	}
	got := convTitleFor("root@10.0.0.1（生产环境）")
	if !strings.HasPrefix(got, "SSH · ") || !strings.Contains(got, "生产环境") {
		t.Fatalf("标题应带主机与环境，得到 %q", got)
	}
	long := convTitleFor(strings.Repeat("机", 200))
	if n := len([]rune(long)); n > aiConvTitleMaxRune {
		t.Fatalf("标题应裁到 %d 字，得到 %d", aiConvTitleMaxRune, n)
	}
}

// SSH 任务必须统一挂在「SSH 助手」空间下：用户要的是「每次连接都放进这个空间」，
// 而不是让连接史混进自己的对话列表。顺带验证空间是幂等的 ——
// 每轮提问都会走一遍 EnsureSSHSpace，多建几个空空间就等于侧栏里长出好几组同名分组。
func TestEnsureAIConvUsesSSHSpace(t *testing.T) {
	svc := newTestService(t)

	a := svc.ensureAIConv("sess-a", "root@10.0.0.1")
	b := svc.ensureAIConv("sess-b", "root@10.0.0.2")
	if a == "" || b == "" {
		t.Fatalf("任务应创建成功: a=%q b=%q", a, b)
	}

	ca, err := svc.App.DB.GetAIConversation(a)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if ca.SpaceID == "" {
		t.Fatal("SSH 任务应归入 SSH 空间，实际 space_id 为空")
	}
	sp, err := svc.App.DB.GetAISpace(ca.SpaceID)
	if err != nil {
		t.Fatalf("读取空间失败: %v", err)
	}
	if sp.Kind != "ssh" {
		t.Fatalf("空间 kind 应为 ssh，得到 %q", sp.Kind)
	}

	cb, err := svc.App.DB.GetAIConversation(b)
	if err != nil {
		t.Fatalf("读取第二个任务失败: %v", err)
	}
	if cb.SpaceID != ca.SpaceID {
		t.Fatalf("不同会话的任务应落在同一个空间: %q / %q", ca.SpaceID, cb.SpaceID)
	}

	// 幂等：再取一次不该新建空间。
	again, err := svc.App.DB.EnsureSSHSpace()
	if err != nil {
		t.Fatalf("再次获取空间失败: %v", err)
	}
	if again.ID != sp.ID {
		t.Fatalf("EnsureSSHSpace 应复用已有空间: 原 %q 新 %q", sp.ID, again.ID)
	}
	all, err := svc.App.DB.ListAISpaces()
	if err != nil {
		t.Fatalf("列出空间失败: %v", err)
	}
	n := 0
	for _, s := range all {
		if s.Kind == "ssh" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("SSH 空间应只有一个，得到 %d 个", n)
	}
}
