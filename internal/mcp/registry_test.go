package mcp

import (
	"strings"
	"testing"
)

// TestCallRespectsMaxLevel 锁定等级门在**执行路径**上也生效。
//
// 插件经 host.mcp.call 复用 Call，安全边界完全落在这里：tools/list 隐藏高危工具
// 只是「不告诉调用方」，而 Call 才是真正的拒绝点。只测 tools/list 会漏掉
// 「知道工具名就能直接调」这条路径。
func TestCallRespectsMaxLevel(t *testing.T) {
	prev := MaxLevel()
	defer SetMaxLevel(prev)

	Register(Tool{
		Name:    "test_safe_read",
		Level:   LevelRead,
		Handler: func(map[string]any) (any, error) { return "ok", nil },
	})

	highRiskCalled := false
	Register(Tool{
		Name:        "test_high_risk",
		Level:       LevelRisk,
		Handler:     func(map[string]any) (any, error) { highRiskCalled = true; return "boom", nil },
		Description: "单测用高危工具",
	})

	SetMaxLevel(LevelWrite) // 宿主默认最高等级

	if _, err := Call("test_safe_read", nil); err != nil {
		t.Fatalf("只读工具应可调用: %v", err)
	}

	got, err := Call("test_high_risk", nil)
	if err == nil {
		t.Fatalf("高危工具在 LevelWrite 下应被拒绝，实际返回: %v", got)
	}
	if highRiskCalled {
		t.Fatal("高危工具的 Handler 被执行了——等级门失效")
	}
	if !strings.Contains(err.Error(), "高危") {
		t.Errorf("拒绝原因应说明是等级限制，便于插件侧排查: %v", err)
	}

	if _, err := Call("test_not_registered", nil); err == nil {
		t.Fatal("未注册工具应报错而不是静默成功")
	}
}
