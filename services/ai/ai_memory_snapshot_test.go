package ai

// 会话内记忆快照：锁住「跨轮前缀缓存」这件容易被无声破坏的事。
//
// 为什么值得单独测：记忆块贴在 system 最前面，而它每轮都会变。只要它跟着变，
// 上游的前缀缓存就整段作废——用户看到的是「命中率怎么比别的工具低一截」，
// 而代码里没有任何地方会报错。实测（2026-11-20，本机 ai_usage）：同一会话跨 run
// 的第一次请求命中率 24.6%，纯聊天会话整段 15-22%。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quickdock/internal/db"
)

// 纯聊天会话跨轮：整段 system 必须逐字节一致。
//
// 这是「聊天命中率 15–22%」的直接回归位：聊天一轮只有一个请求，system 一变，
// 上游前缀缓存整段作废。记忆（每日日志/MEMORY.md）是唯一每轮都会变的部分，
// 快照之后这里必须全等。
func TestChatSystemPromptStableAcrossTurns(t *testing.T) {
	svc, d := newTestAIService(t)
	cwd := t.TempDir()
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat", Title: "聊天", Cwd: cwd})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	cfg := AIProfile{Model: "test-model", MaxTokens: 4096, ContextWindow: 128000}

	msgs1, err := svc.buildAIMessages(context.Background(), cfg, conv.ID, "chat", nil)
	if err != nil || len(msgs1) == 0 {
		t.Fatalf("组装上下文失败: %v", err)
	}
	sys1, _ := msgs1[0]["content"].(string)
	if sys1 == "" {
		t.Fatal("system 为空")
	}

	// 这一轮结束了：真实场景里每日日志会多一行、笔记会多一段、MEMORY.md 会多几条。
	appendDailyMemory(conv.ID, cwd, "聊天", "第一句", nil, 0, 0)
	appendDailyNote(conv.ID, cwd, "聊天", "第一句", time.Now(), []string{"需求：随便聊聊"})
	if err := updateOverallMemory(resolveMemoryScope(cwd).root, []memoryEntry{
		{Category: "事实", Text: "这条是第二轮才写进去的记忆，不该改变本会话的 system"},
	}); err != nil {
		t.Fatalf("写记忆失败: %v", err)
	}

	msgs2, err := svc.buildAIMessages(context.Background(), cfg, conv.ID, "chat", nil)
	if err != nil || len(msgs2) == 0 {
		t.Fatalf("第二次组装失败: %v", err)
	}
	sys2, _ := msgs2[0]["content"].(string)
	if sys1 != sys2 {
		t.Errorf("同一会话跨轮 system 变了——上游前缀缓存会整段作废\n第一轮:\n%s\n\n第二轮:\n%s", sys1, sys2)
	}

	// 另一条会话（或快照过期后）要能读到新记忆。
	other, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat", Title: "另一条", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	msgs3, err := svc.buildAIMessages(context.Background(), cfg, other.ID, "chat", nil)
	if err != nil || len(msgs3) == 0 {
		t.Fatal(err)
	}
	sys3, _ := msgs3[0]["content"].(string)
	if !strings.Contains(sys3, "第二轮才写进去的记忆") {
		t.Errorf("新会话应读到最新记忆:\n%s", sys3)
	}
}

// 同一会话内：记忆文件变了，注入块也不能变。
func TestMemoryBlockSnapshotStableWithinConversation(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-snap", cwd, "任务", "第一句诉求", nil, 1, 1)

	first := memoryBlockForConversation("conv-snap", cwd)
	if first == "" {
		t.Fatal("应注入一段记忆")
	}
	if !strings.Contains(first, "第一句诉求") {
		t.Fatalf("快照内容不对:\n%s", first)
	}

	// 又跑了一轮：每日记忆多了一行（真实场景里每轮都会这样）。
	appendDailyMemory("conv-snap", cwd, "任务", "第二句诉求", nil, 2, 2)

	same := memoryBlockForConversation("conv-snap", cwd)
	if same != first {
		t.Errorf("同一会话内记忆块必须保持不变，否则前缀缓存整段作废:\n旧=%q\n新=%q", first, same)
	}

	// 别的会话不受影响，读到的是最新内容。
	other := memoryBlockForConversation("conv-other", cwd)
	if !strings.Contains(other, "第二句诉求") {
		t.Errorf("另一条会话应读到最新记忆:\n%s", other)
	}
}

// 快照过期后必须重读（否则会话里永远看不到新记忆）。
func TestMemoryBlockSnapshotExpires(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-ttl", cwd, "任务", "旧内容", nil, 1, 1)
	first := memoryBlockForConversation("conv-ttl", cwd)
	if first == "" {
		t.Fatal("应注入一段记忆")
	}

	appendDailyMemory("conv-ttl", cwd, "任务", "新内容", nil, 2, 2)

	old := aiMemorySnapshotTTL
	aiMemorySnapshotTTL = 0
	defer func() { aiMemorySnapshotTTL = old }()

	if got := memoryBlockForConversation("conv-ttl", cwd); !strings.Contains(got, "新内容") {
		t.Errorf("TTL 过期后应重读记忆:\n%s", got)
	}
}

// 没有会话 id 的一次性调用不进快照表（它们没有「下一轮」可言）。
func TestMemoryBlockNoConvIDBypassesSnapshot(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-none", cwd, "任务", "甲", nil, 1, 1)
	_ = memoryBlockForConversation("", cwd)
	appendDailyMemory("conv-none", cwd, "任务", "乙", nil, 2, 2)
	got := memoryBlockForConversation("", cwd)
	if !strings.Contains(got, "乙") {
		t.Errorf("空会话 id 应直接现读:\n%s", got)
	}
	aiMemorySnapMu.Lock()
	_, exists := aiMemorySnaps[""]
	aiMemorySnapMu.Unlock()
	if exists {
		t.Error("空会话 id 不该写进快照表")
	}
}

// 快照表有上限：超过 aiMemorySnapMax 时丢最旧的，不能无界增长。
func TestMemorySnapshotsEvicted(t *testing.T) {
	aiMemorySnapMu.Lock()
	aiMemorySnaps = map[string]aiMemorySnapshot{}
	for i := 0; i < aiMemorySnapMax; i++ {
		aiMemorySnaps["c"+itoa(i)] = aiMemorySnapshot{
			root: "r", block: "x", at: time.Unix(int64(i), 0),
		}
	}
	evictOldestMemorySnapshotLocked()
	aiMemorySnapMu.Unlock()

	if len(aiMemorySnaps) != aiMemorySnapMax-1 {
		t.Fatalf("应丢 1 个，剩 %d", len(aiMemorySnaps))
	}
	if _, ok := aiMemorySnaps["c0"]; ok {
		t.Error("应丢最旧的那个（c0）")
	}
}

// 换工作目录（换项目）时必须重建，不能沿用别的项目的记忆。
func TestMemoryBlockSnapshotRebuildsOnRootChange(t *testing.T) {
	cwdA := t.TempDir()
	cwdB := t.TempDir()
	appendDailyMemory("conv-move", cwdA, "任务", "项目A的事", nil, 1, 1)
	appendDailyMemory("conv-move", cwdB, "任务", "项目B的事", nil, 1, 1)

	a := memoryBlockForConversation("conv-move", cwdA)
	if !strings.Contains(a, "项目A的事") {
		t.Fatalf("A 内容不对:\n%s", a)
	}
	b := memoryBlockForConversation("conv-move", cwdB)
	if !strings.Contains(b, "项目B的事") || strings.Contains(b, "项目A的事") {
		t.Errorf("换 cwd 后应重新读那个项目的记忆:\n%s", b)
	}
	// 用不到，但保留目录存在性检查，避免 tempdir 被清理导致的伪失败。
	if _, err := os.Stat(filepath.Join(cwdA, memoryDirName)); err != nil {
		t.Fatal(err)
	}
}
