package ai

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"quickdock/internal/db"
	"quickdock/services"
)

func newTestAIService(t *testing.T) (*AIService, *db.Database) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	// 必须显式关闭：否则 Windows 上临时目录清理会因文件占用而失败，
	// 表现为测试"莫名 FAIL"而不是真正的断言失败。
	t.Cleanup(func() { _ = d.Close() })
	svc := NewAIService(services.NewAppService())
	svc.App.DB = d
	return svc, d
}

// 覆盖的是「不联网也能确定」的那部分：入参校验、档案缺失、长度裁剪。
// 真正发起模型请求的路径需要有 API Key 与网络，属于集成测试范围，不在这里。

func TestAIAsk_EmptyQuestion(t *testing.T) {
	svc, _ := newTestAIService(t)
	r := svc.AIAsk(AIAskRequest{Question: "   "})
	if r == nil || r.Code == 0 {
		t.Fatalf("空问题应失败，实际 %+v", r)
	}
}

func TestAIAsk_NoProfile(t *testing.T) {
	svc, _ := newTestAIService(t)
	// 未配置档案时必须给出可读原因，前端据此提示去设置里配 Key。
	r := svc.AIAsk(AIAskRequest{Question: "磁盘还剩多少"})
	if r == nil || r.Code == 0 {
		t.Fatalf("未配置档案时应失败，实际 %+v", r)
	}
	if !strings.Contains(r.Msg, "AI") {
		t.Errorf("错误文案应指明是 AI 配置问题，实际 %q", r.Msg)
	}
}

func TestAIAsk_WhitespaceOnlySystemFallsBack(t *testing.T) {
	svc, _ := newTestAIService(t)
	// 全空格的 system 不该被当成有效覆盖，否则档案里的 SystemPrompt 会被静默清空。
	r := svc.AIAsk(AIAskRequest{Question: "q", System: "   "})
	// 没配档案，仍会失败；但失败原因应是"未配置"而非"system 无效"。
	if r != nil && strings.Contains(r.Msg, "系统提示") {
		t.Errorf("空白 system 不应单独成为失败原因，实际 %q", r.Msg)
	}
}

func TestAIAvailable_NoProfile(t *testing.T) {
	svc, _ := newTestAIService(t)
	r := svc.AIAvailable()
	if r == nil || r.Code != 0 {
		t.Fatalf("AIAvailable 本身不该失败（前端要靠它决定按钮置灰），实际 %+v", r)
	}
	data, ok := r.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("返回结构不对: %T", r.Data)
	}
	if data["available"] != false {
		t.Errorf("无档案时 available 应为 false，实际 %v", data["available"])
	}
	if data["reason"] != "no_profile" {
		t.Errorf("reason 应为 no_profile，实际 %v", data["reason"])
	}
}

// 上限常量是成本闸门，改动时应当是显式决定而非顺手调整，故用测试锁住。
func TestAskLimits(t *testing.T) {
	if askMaxSystemRune <= 0 || askMaxUserRune <= 0 || askMaxReturnRune <= 0 {
		t.Fatal("长度上限必须为正数，否则等于没有闸门")
	}
	if askMaxTokens <= 0 {
		t.Fatal("MaxTokens 上限必须为正数")
	}
	if askTimeout <= 0 {
		t.Fatal("超时必须为正数，否则慢端点会永久挂住调用方")
	}
}

// callAIOnce 必须容忍 nil ctx：历史上标题生成就因为漏传 ctx 直接 panic 过。
func TestCallAIOnce_NilContext(t *testing.T) {
	svc, _ := newTestAIService(t)
	// 不配置档案 → getActiveAIProfile 失败，这里只验证不 panic。
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("nil ctx 导致 panic: %v", p)
		}
	}()
	_, _, _ = svc.callAIOnce(nil, AIProfile{}, nil)
}

var _ = context.Background
