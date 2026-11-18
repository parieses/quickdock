package ai

// WP12：会话回放。
//
// 这里验的是「顺序与形状」，不是界面：时间轴看反了（新→旧）会让用户
// 以为最后发生的事在最前面，而这是纯逻辑，能在后端测。

import (
	"testing"

	"quickdock/internal/db"
)

func TestSessionTimelineOrderAndShape(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	// 同一秒内建两条 run：`created_at` 精度到秒，顺序只能靠 id。
	for i := 0; i < 2; i++ {
		run, err := d.CreateAgentRun(db.NewAgentRun{ConvID: conv.ID, Goal: "任务", Trigger: "chat"})
		if err != nil {
			t.Fatalf("建 run 失败: %v", err)
		}
		if _, err := d.AppendAgentEvent(run.ID, "", "tool", `{"name":"file_read"}`); err != nil {
			t.Fatalf("写事件失败: %v", err)
		}
	}

	res := svc.AISessionTimeline(conv.ID, 0)
	if res.Code != 0 {
		t.Fatalf("回放失败: %+v", res)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("返回类型不对: %T", res.Data)
	}
	runs, ok := data["runs"].([]AISessionRun)
	if !ok || len(runs) != 2 {
		t.Fatalf("run 条数不对: %T %d", data["runs"], len(runs))
	}

	// 必须是升序：与 ListAgentRunsByConv（倒序）相反。
	desc, err := d.ListAgentRunsByConv(conv.ID, 20)
	if err != nil || len(desc) != 2 {
		t.Fatalf("读 run 列表失败: %v %d", err, len(desc))
	}
	if runs[0].Run.ID != desc[1].ID || runs[1].Run.ID != desc[0].ID {
		t.Errorf("时间轴顺序应为升序，实际 %s, %s", runs[0].Run.ID, runs[1].Run.ID)
	}

	// 每条 run 都要带上它的事件（回放的意义就在这里）。
	for i, r := range runs {
		if len(r.Events) != 1 {
			t.Errorf("第 %d 条 run 的事件数不对: %d", i, len(r.Events))
		}
		if r.Run == nil {
			t.Errorf("第 %d 条 run 为空指针", i)
		}
		// 没有子 agent 时 children 必须是 0（而不是未初始化导致的随机数）。
		if r.Children != 0 {
			t.Errorf("第 %d 条 run 不该有子任务: %d", i, r.Children)
		}
	}
}

func TestSessionTimelineEmpty(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	res := svc.AISessionTimeline(conv.ID, 0)
	if res.Code != 0 {
		t.Fatalf("空会话应正常返回: %+v", res)
	}
	data, _ := res.Data.(map[string]any)
	runs, ok := data["runs"].([]AISessionRun)
	if !ok {
		t.Fatalf("runs 应当是切片而不是 nil: %T", data["runs"])
	}
	if len(runs) != 0 {
		t.Errorf("空会话不该有 run: %d", len(runs))
	}

	// 缺会话 id 要报错，而不是回一段空时间轴让前端以为「就是没记录」。
	if res := svc.AISessionTimeline("  ", 0); res.Code == 0 {
		t.Error("空 convID 应当失败")
	}
}
