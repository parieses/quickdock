package ai

// WP4c：手动压缩入口 + 70% 预警档的守卫。
//
// 这里不验「摘要质量」（那要有模型与网络），验的是接线与口径：
// 目标预算与预判守卫一致、空会话不炸、缺少会话 id 给可读错误。

import (
	"strings"
	"testing"

	"quickdock/internal/db"
)

func TestCompactConversationBasic(t *testing.T) {
	svc, d := newTestAIService(t)

	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	// 空会话：没有历史可压，必须正常返回而不是报错。
	res := svc.AICompactConversation(conv.ID)
	if res.Code != 0 {
		t.Fatalf("空会话压缩失败: %+v", res)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("返回类型不对: %T", res.Data)
	}
	// 目标预算必须与预判守卫同一口径（上限的一半），否则「手动压完又被自动压」。
	cfg, _ := svc.aiProfileForRun("", conv.ID)
	lim := contextLimitsOf(cfg)
	want := lim.Ceiling / 2
	if want < lim.Floor {
		want = lim.Floor
	}
	if got, _ := data["target"].(int); got != want {
		t.Errorf("目标预算 = %v，期望 %d（ceiling/2，下限 floor）", data["target"], want)
	}
	if got, _ := data["ceiling"].(int); got != lim.Ceiling {
		t.Errorf("ceiling 回显不对: %v，期望 %d", data["ceiling"], lim.Ceiling)
	}
}

func TestCompactConversationErrors(t *testing.T) {
	svc, _ := newTestAIService(t)
	if res := svc.AICompactConversation(""); res.Code == 0 {
		t.Error("空 convID 应当失败")
	}
	if res := svc.AICompactConversation("no-such-conv"); res.Code == 0 {
		t.Error("不存在的会话应当失败")
	}
}

// 预警文案必须说清「多少会自动压缩」——只说「用了 75%」用户不知道下一步会怎样。
func TestCompactWarningText(t *testing.T) {
	msg := compactWarningText(75, 85)
	for _, want := range []string{"75%", "85%", "自动压缩"} {
		if !strings.Contains(msg, want) {
			t.Errorf("预警文案缺少 %q: %s", want, msg)
		}
	}
	// 越界的百分比要夹住，不能出现「用到 130%」这种话。
	if got := compactWarningText(130, 85); strings.Contains(got, "130") {
		t.Errorf("超过 99%% 应被夹住: %s", got)
	}
	if got := compactWarningText(-5, 85); strings.Contains(got, "-5") {
		t.Errorf("负数应被夹到 0: %s", got)
	}
}
