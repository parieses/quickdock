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

// TestCallRespectsMaxLevel 锁定等级门在执行路径上也生效。
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

// TestDisabledToolHiddenButReopenable 锁定单工具开关的三处一致性。
//
// 关闭必须同时作用于 List（客户端看不到）、Catalog（面板标出「关了」而不是
// 「没资格」）与 Call（知道工具名也调不动）。关闭不等于从注册表删除——
// 面板要能把它列出来再打开。
func TestDisabledToolHiddenButReopenable(t *testing.T) {
	prevLvl, prevOff := MaxLevel(), Disabled()
	defer func() {
		SetMaxLevel(prevLvl)
		SetDisabled(prevOff)
	}()
	SetMaxLevel(LevelWrite)

	called := false
	Register(Tool{
		Name:        "test_toggle",
		Description: "单测用可开关工具",
		Level:       LevelRead,
		Handler: func(map[string]any) (any, error) {
			called = true
			return "ok", nil
		},
	})

	SetDisabled([]string{"test_toggle"})
	for _, d := range List() {
		if d.Name == "test_toggle" {
			t.Fatal("被关闭的工具不应出现在 tools/list")
		}
	}

	var info ToolInfo
	found := false
	for _, c := range Catalog() {
		if c.Name == "test_toggle" {
			info, found = c, true
		}
	}
	if !found {
		t.Fatal("Catalog 应保留被关闭的工具，否则面板无法重新开启")
	}
	if !info.Disabled || info.Exposed {
		t.Fatalf("Catalog 应标记已关闭且未开放，实际 %+v", info)
	}

	if _, err := Call("test_toggle", nil); err == nil {
		t.Fatal("被关闭的工具应拒绝调用")
	}
	if called {
		t.Fatal("被关闭工具的 Handler 被执行了——单工具开关失效")
	}

	SetDisabled(nil)
	if _, err := Call("test_toggle", nil); err != nil {
		t.Fatalf("重新开启后应可调用: %v", err)
	}
}

// TestAnnotationsDerivedFromLevel 锁定 annotations 由危险等级推导。
//
// 客户端靠 readOnlyHint 决定要不要弹确认框：写操作被标成只读，模型就能
// 跳过确认直接改数据，等于绕过等级门。
func TestAnnotationsDerivedFromLevel(t *testing.T) {
	prev := MaxLevel()
	defer SetMaxLevel(prev)
	SetMaxLevel(LevelRisk)

	Register(Tool{
		Name:    "test_anno_read",
		Level:   LevelRead,
		Handler: func(map[string]any) (any, error) { return nil, nil },
	})
	Register(Tool{
		Name:    "test_anno_write",
		Level:   LevelWrite,
		Handler: func(map[string]any) (any, error) { return nil, nil },
	})
	Register(Tool{
		Name:    "test_anno_risk",
		Level:   LevelRisk,
		Handler: func(map[string]any) (any, error) { return nil, nil },
	})

	defs := make(map[string]map[string]any, len(List()))
	for _, d := range List() {
		defs[d.Name] = d.Annotations
	}

	if ro, _ := defs["test_anno_read"]["readOnlyHint"].(bool); !ro {
		t.Error("LevelRead 应标 readOnlyHint=true")
	}
	if ro, _ := defs["test_anno_write"]["readOnlyHint"].(bool); ro {
		t.Error("LevelWrite 不应标 readOnlyHint=true")
	}
	if de, _ := defs["test_anno_risk"]["destructiveHint"].(bool); !de {
		t.Error("LevelRisk 应标 destructiveHint=true")
	}
	if de, _ := defs["test_anno_write"]["destructiveHint"].(bool); de {
		t.Error("LevelWrite 不应标 destructiveHint=true")
	}
}

// TestAgentRegistrySeparatedFromMCP 锁定两个注册表的分工。
//
// 这条分界是产品决定：MCP 只对外提供「QuickDock 自身的能力」，抓网页/改文件/跑命令
// 这类属于 AI 助手自带的手艺，外部客户端本就有。写错的表现是客户端 tools/list 里
// 冒出一个能改磁盘的 file_edit——用户不会认为那是自己的工具。
func TestAgentRegistrySeparatedFromMCP(t *testing.T) {
	prev := MaxLevel()
	defer SetMaxLevel(prev)
	SetMaxLevel(LevelWrite)

	Agent.Add(Tool{
		Name:        "agent_probe",
		Description: "单测用 agent 工具",
		Level:       LevelRead,
		Category:    CategoryCode,
		Handler:     func(map[string]any) (any, error) { return "agent-ok", nil },
	})

	for _, d := range List() {
		if d.Name == "agent_probe" {
			t.Fatal("agent 工具不该出现在 MCP 的 tools/list 里")
		}
	}

	var inAI bool
	for _, f := range ExposedFunctions() {
		if f.Name == "agent_probe" {
			inAI = true
			if !f.AgentOnly {
				t.Error("ExposedFunctions 要标出 AgentOnly，否则模式门会把它当宿主工具")
			}
		}
	}
	if !inAI {
		t.Fatal("内置 AI 应该看得到 agent 工具")
	}

	var info *ToolInfo
	cat := Catalog()
	for i := range cat {
		if cat[i].Name == "agent_probe" {
			info = &cat[i]
		}
	}
	if info == nil {
		t.Fatal("面板要能看到 agent 工具，否则无法单独关闭")
	}
	if !info.AgentOnly || info.Exposed {
		t.Fatalf("agent 工具应标记 AgentOnly 且 Exposed=false，实际 %+v", *info)
	}

	// 执行路径同样要分开：名字是可猜的，只靠 list 隐藏等于没拦。
	if _, err := Call("agent_probe", nil); err == nil {
		t.Fatal("MCP 侧 Call 应拒绝 agent 工具")
	} else if !strings.Contains(err.Error(), "仅内置 AI") {
		t.Fatalf("错误信息应说明原因，实际: %v", err)
	}
	if v, err := CallAgent("agent_probe", nil); err != nil || v != "agent-ok" {
		t.Fatalf("CallAgent 应能执行 agent 工具，实际 v=%v err=%v", v, err)
	}
}

// TestAgentToolHonorsPerToolSwitch 单工具开关对两套注册表一样生效。
//
// 用户在内置 AI 的定位上比对外暴露更在意「别让它碰我的文件」，
// 所以面板里关掉 file_edit 必须真的挡住 AI，而不是只挡住 MCP 客户端。
func TestAgentToolHonorsPerToolSwitch(t *testing.T) {
	prevMax, prevOff := MaxLevel(), Disabled()
	defer func() { SetMaxLevel(prevMax); SetDisabled(prevOff) }()
	SetMaxLevel(LevelWrite)

	Agent.Add(Tool{
		Name:    "agent_switch_probe",
		Level:   LevelRead,
		Handler: func(map[string]any) (any, error) { return "ok", nil },
	})
	SetDisabled([]string{"agent_switch_probe"})

	if _, err := CallAgent("agent_switch_probe", nil); err == nil {
		t.Fatal("关掉的工具内置 AI 也不该能调")
	}
	for _, f := range ExposedFunctions() {
		if f.Name == "agent_switch_probe" {
			t.Fatal("关掉的工具不该出现在 tools[] 里——模型看到就会去调，然后收到一串拒绝")
		}
	}
}
