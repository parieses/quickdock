package ai

// 压缩后 system 提示重建（composeBaseSystem）的单测。
//
// 这段胶水一旦漏段，症状是「长对话压缩后 AI 突然不懂规矩」——它不知道
// 怎么拆任务、临时文件往哪写、能不能派子 agent，极难复现。
// 用纯函数直接断言每段都在，比跑带网络的 runAgent 全链路划算得多。
//
// resumeHint 已挪出 system（清单随 plan_update 每轮都变，进 system 会让
// 前缀缓存整段作废），这里顺带验证它不再出现在产物里。

import (
	"strings"
	"testing"

	"quickdock/internal/db"
)

func toolNamed(name string) map[string]any {
	return map[string]any{"function": map[string]any{"name": name}}
}

func TestComposeBaseSystemRestoresAllHints(t *testing.T) {
	tools := []map[string]any{
		toolNamed("plan_update"),
		toolNamed("file_write"),
		toolNamed("spawn_agent"),
	}
	rules := aiRules{Text: "[项目规则]\n必须跑测试"}

	got := composeBaseSystem("干净 system", rules, tools, "/proj", true)

	for _, want := range []string{
		"干净 system",
		planHint,
		workdirHint,
		scaleHint,
		rules.Text,
		extToolsOverflowHint,
	} {
		if want == "" {
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("重建后的 system 缺少片段 %q\n实际: %s", want, got)
		}
	}
}

func TestComposeBaseSystemSkipsHintsWithoutTools(t *testing.T) {
	// 没挂工具时不该讲「用 plan_update」「可以派 spawn_agent」——模型会对着
	// 看不见的工具名反复重试。这是重建与初始装配都必须守住的边界。
	got := composeBaseSystem("干净 system", aiRules{}, nil, "", false)
	for _, forbidden := range []string{planHint, scaleHint, workdirHint, extToolsOverflowHint} {
		if forbidden != "" && strings.Contains(got, forbidden) {
			t.Errorf("无工具时不应出现片段 %q\n实际: %s", forbidden, got)
		}
	}
}

// 清单提示已移出 system：system 是跨请求共享的前缀，每轮都变的清单放进去
// 会让前缀缓存整段作废（2026-11-20 缓存优化）。
func TestComposeBaseSystemExcludesResumeHint(t *testing.T) {
	// Plan 是 JSON（plan_update 落库的形态），markdown 勾选框解析不出来。
	conv := &db.AIConversation{
		Plan:       `{"items":[{"text":"一步","status":"done"},{"text":"两步","status":"todo"}]}`,
		PlanStatus: "running",
	}
	hint := resumeHintFor(conv, "继续")
	if hint == "" {
		t.Fatal("测试前提不成立：conv 应产出续跑提示")
	}
	got := composeBaseSystem("干净 system", aiRules{}, []map[string]any{toolNamed("plan_update")}, "", false)
	if strings.Contains(got, hint) {
		t.Errorf("system 不应再包含续跑提示（已挪到 user 消息）\n实际: %s", got)
	}
}
