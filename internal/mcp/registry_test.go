package mcp

import (
	"strings"
	"testing"
)

// TestCatalogIncludesBlockedTools 锁定 Catalog 与 List 的分工。
//
// List 只给「当前等级下能调的」，Catalog 给全量并标出谁被挡住。
// 两者混用会让管理界面把「被等级拦住」显示成「没有这个工具」——
// 用户和模型都会据此得出错误结论。
func TestCatalogIncludesBlockedTools(t *testing.T) {
	prev := MaxLevel()
	defer SetMaxLevel(prev)
	SetMaxLevel(LevelWrite)

	Register(Tool{
		Name:        "catalog_read",
		Description: "单测用只读工具",
		Level:       LevelRead,
		Handler:     func(map[string]any) (any, error) { return "ok", nil },
	})
	Register(Tool{
		Name:        "catalog_risk",
		Description: "单测用高危工具",
		Level:       LevelRisk,
		Handler:     func(map[string]any) (any, error) { return "boom", nil },
	})

	cat := Catalog()
	byName := make(map[string]ToolInfo, len(cat))
	for _, c := range cat {
		byName[c.Name] = c
	}
	read, ok := byName["catalog_read"]
	if !ok {
		t.Fatal("Catalog 应包含只读工具")
	}
	if !read.Exposed {
		t.Error("LevelRead 工具在 LevelWrite 下应标记为开放")
	}
	risk, ok := byName["catalog_risk"]
	if !ok {
		t.Fatal("Catalog 应包含高危工具（List 里看不到，Catalog 必须看得到）")
	}
	if risk.Exposed {
		t.Error("LevelRisk 工具在 LevelWrite 下应标记为未开放")
	}
	if risk.Level != LevelRisk {
		t.Errorf("Catalog 应带回等级，期望 %d，实际 %d", LevelRisk, risk.Level)
	}
	for i := 1; i < len(cat); i++ {
		if cat[i-1].Level > cat[i].Level {
			t.Fatalf("Catalog 应按等级升序，%s(%d) 排在 %s(%d) 之后",
				cat[i-1].Name, cat[i-1].Level, cat[i].Name, cat[i].Level)
		}
	}
}

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
