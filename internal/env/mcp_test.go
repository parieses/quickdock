package env

import (
	"testing"

	"quickdock/internal/mcp"
)

// TestMCPRuntimeSetMaxLevelPersists 锁定工具等级会写回 config.json。
//
// 只改内存是不够的：MCPRuntime.Start 每次都从配置里读回等级，
// 于是「面板改了 → 重启服务 → 改回默认值」，而界面上并没有任何提示说设置丢了。
// 这类静默回滚排查起来极贵，所以在这里钉死。
func TestMCPRuntimeSetMaxLevelPersists(t *testing.T) {
	prev := mcp.MaxLevel()
	defer mcp.SetMaxLevel(prev)

	m := NewMCPRuntime()
	m.baseDir = t.TempDir()

	if err := m.SetMaxLevel(mcp.LevelRead); err != nil {
		t.Fatalf("保存等级失败: %v", err)
	}
	if got := mcp.MaxLevel(); got != mcp.LevelRead {
		t.Fatalf("内存等级应立即生效，期望 %d，实际 %d", mcp.LevelRead, got)
	}
	if got := m.loadConfig().MaxLevel; got != mcp.LevelRead {
		t.Fatalf("配置里应写入 %d，实际 %d", mcp.LevelRead, got)
	}

	// 再次覆盖：确认文件被真正重写，而不是第一次写入后就没再动过
	if err := m.SetMaxLevel(mcp.LevelWrite); err != nil {
		t.Fatalf("覆盖等级失败: %v", err)
	}
	if got := m.loadConfig().MaxLevel; got != mcp.LevelWrite {
		t.Fatalf("配置应更新为 %d，实际 %d", mcp.LevelWrite, got)
	}
}

// TestMCPRuntimeSetMaxLevelRejectsOutOfRange 越界等级必须报错而不是静默截断——
// 静默截断会让「我明明设了高危，怎么没生效」变成无解问题。
func TestMCPRuntimeSetMaxLevelRejectsOutOfRange(t *testing.T) {
	prev := mcp.MaxLevel()
	defer mcp.SetMaxLevel(prev)

	m := NewMCPRuntime()
	m.baseDir = t.TempDir()

	for _, bad := range []int{-1, mcp.LevelRisk + 1, 99} {
		if err := m.SetMaxLevel(bad); err == nil {
			t.Fatalf("等级 %d 越界，应报错", bad)
		}
	}
	// 失败不应污染已保存的配置
	if err := m.SetMaxLevel(mcp.LevelWrite); err != nil {
		t.Fatalf("保存等级失败: %v", err)
	}
	if err := m.SetMaxLevel(99); err == nil {
		t.Fatal("越界等级应报错")
	}
	if got := m.loadConfig().MaxLevel; got != mcp.LevelWrite {
		t.Fatalf("写失败不应覆盖已有配置，期望 %d，实际 %d", mcp.LevelWrite, got)
	}
}

// TestMCPRuntimeSetToolEnabledPersists 锁定单工具开关同样落盘、且不重复堆积。
//
// 与等级一样，Start 每次从配置读回名单：面板关掉的工具不能因为重启服务又冒出来。
// 反复点同一条若写成重复项，配置会越来越长且难以人工核对。
func TestMCPRuntimeSetToolEnabledPersists(t *testing.T) {
	prev := mcp.Disabled()
	defer mcp.SetDisabled(prev)

	m := NewMCPRuntime()
	m.baseDir = t.TempDir()

	if err := m.SetToolEnabled("note_create", false); err != nil {
		t.Fatalf("关闭工具失败: %v", err)
	}
	if got := mcp.Disabled(); len(got) != 1 || got[0] != "note_create" {
		t.Fatalf("内存应立即生效，实际 %v", got)
	}
	if got := m.loadConfig().DisabledTools; len(got) != 1 || got[0] != "note_create" {
		t.Fatalf("配置应写入 close 名单，实际 %v", got)
	}

	// 重复关闭同一条：不产生重复项
	if err := m.SetToolEnabled("note_create", false); err != nil {
		t.Fatalf("重复关闭应成功: %v", err)
	}
	if got := m.loadConfig().DisabledTools; len(got) != 1 {
		t.Fatalf("重复关闭不应堆积重复项，实际 %v", got)
	}

	// 重新开启：名单里不再有它
	if err := m.SetToolEnabled("note_create", true); err != nil {
		t.Fatalf("开启工具失败: %v", err)
	}
	if got := m.loadConfig().DisabledTools; len(got) != 0 {
		t.Fatalf("开启后配置应为空名单，实际 %v", got)
	}
	if got := mcp.Disabled(); len(got) != 0 {
		t.Fatalf("开启后内存名单应为空，实际 %v", got)
	}
}
