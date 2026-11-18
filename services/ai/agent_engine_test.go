package ai

// WP14：引擎接口缝。
//
// 这里只验「缝是真的」：注册表能取到内置引擎、未知名字回落而不是崩、
// 伪造的引擎能真的被调用（证明调用点走的是接口而不是硬编码）。

import (
	"context"
	"testing"

	"quickdock/internal/db"
)

// fakeEngine 记录自己被调用了几次，用来证明 runAgent 走的是接口。
type fakeEngine struct {
	name  string
	calls int
}

func (f *fakeEngine) Name() string { return f.name }

func (f *fakeEngine) Run(ctx context.Context, req AgentRunReq, sink EventSink) AgentRunResult {
	f.calls++
	return AgentRunResult{ConvID: req.ConvID, Text: "from-fake"}
}

func TestAgentEngineRegistryDefaultIsBuiltin(t *testing.T) {
	svc, _ := newTestAIService(t)
	e := svc.aiAgentEngine()
	if e.Name() != aiAgentEngineBuiltin {
		t.Errorf("默认引擎应为内置，实际 %q", e.Name())
	}
}

// 未知引擎名必须回落内置，而不是 panic / 空指针。
func TestAgentEngineUnknownFallsBack(t *testing.T) {
	svc, d := newTestAIService(t)
	if err := d.SetSetting(aiAgentEngineSettingKey, "dsn-not-installed"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	e := svc.aiAgentEngine()
	if e.Name() != aiAgentEngineBuiltin {
		t.Errorf("未知引擎应回落内置，实际 %q", e.Name())
	}
}

// 注册一个新引擎后，runAgent 必须真的调到它。
func TestRunAgentUsesRegisteredEngine(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	fake := &fakeEngine{name: "fake"}
	aiAgentEngineFactories["fake"] = func(*AIService) AIAgentEngine { return fake }
	t.Cleanup(func() { delete(aiAgentEngineFactories, "fake") })

	if err := d.SetSetting(aiAgentEngineSettingKey, "fake"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}

	res := svc.runAgent(context.Background(), AgentRunReq{ConvID: conv.ID, Message: "hi"}, progressSink{})
	if fake.calls != 1 {
		t.Fatalf("注册的引擎没被调用: %d", fake.calls)
	}
	if res.Text != "from-fake" {
		t.Errorf("结果应来自假引擎，实际 %q", res.Text)
	}
}
